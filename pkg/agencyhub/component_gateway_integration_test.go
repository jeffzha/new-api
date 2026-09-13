package agencyhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This crosses the real wallet transaction, immutable outbox, worker, reports
// and refund transaction. It does not manufacture committed financial events.
func TestComponentGatewayRefundAndProjectionAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := reconciliationConcurrentDB(t, dialect)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
			tx := db.Begin()
			require.NoError(t, tx.Error)
			priorDB, priorRedis := model.DB, common.RedisEnabled
			priorMain, priorLog := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, common.RedisEnabled = tx, false
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			t.Cleanup(func() {
				model.DB, common.RedisEnabled = priorDB, priorRedis
				common.SetDatabaseTypes(priorMain, priorLog)
				require.NoError(t, tx.Rollback().Error)
			})
			t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "true")
			app := New(tx, tx, Config{BasePath: "/agency", InstanceID: "component-integration"})
			prefix := "integrated-" + common.GetUUID()[:10]
			agency := model.Agency{Code: prefix, DisplayName: "Component integration", InviteCode: common.GetUUID()[:10], Status: "active", Version: 1}
			require.NoError(t, tx.Create(&agency).Error)
			policyJSON, err := common.Marshal(agencycontract.Policy{DefaultSettlementBPS: 6000, DefaultSalesBPS: 9000, SalesCapBPS: 30000})
			require.NoError(t, err)
			policy := model.AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: 1, PolicyJSON: string(policyJSON), PolicyHash: "fixture-policy"}
			require.NoError(t, tx.Create(&policy).Error)
			require.NoError(t, tx.Model(&agency).Update("current_policy_version_id", policy.ID).Error)
			user := model.User{Username: prefix, AffCode: prefix, Status: common.UserStatusEnabled,
				BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
			require.NoError(t, tx.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: prefix, RemainQuota: 100, Status: common.TokenStatusEnabled}
			require.NoError(t, tx.Create(&token).Error)
			require.NoError(t, tx.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
			require.NoError(t, model.RecordAgencyTopup(tx, int64(user.Id), "payment", prefix+"-topup", "payment_callback", 60, 40))
			snapshot := &agencycontract.PricingSnapshot{AgencyID: agency.ID, BindingID: 1, CommissionEligible: true,
				OriginModelName: "frozen-model", SettlementBPS: 6000, SalesBPS: 9000, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
			_, _, err = model.TryReserveAgencyWalletAndTokenWithSequence(user.Id, token.Id, 100, token.Key, prefix, 100, false, snapshot)
			require.NoError(t, err)
			original, err := model.AgencyCommitWalletCharge(agencycontract.BillingEvent{FinancialChargeID: prefix, UserID: int64(user.Id),
				OriginModelName: "untrusted-recovery-metadata", BusinessStatus: "success", ChargedTotalQuota: 100,
				CommissionableQuota: 90, NoncommissionableQuota: 10, SettlementCostQuota: 60, TheoreticalCommissionQuota: 30,
				Components: []agencycontract.BillingComponent{
					{ComponentID: "Model", ChargedTotalQuota: 45, CommissionableQuota: 45, SettlementCostQuota: 30, TheoreticalCommissionQuota: 15, CommissionEligible: true},
					{ComponentID: "model", ChargedTotalQuota: 45, CommissionableQuota: 45, SettlementCostQuota: 30, TheoreticalCommissionQuota: 15, CommissionEligible: true},
					{ComponentID: "fee", ChargedTotalQuota: 10, NoncommissionableQuota: 10},
				}}, token.Key)
			require.NoError(t, err)
			assert.Equal(t, "frozen-model", original.OriginModelName)
			assert.Equal(t, 6000, original.SettlementBPS)
			assert.Equal(t, int64(18), original.CommissionAmountMicros)
			verifyComponentFundingEvidence(t, app, prefix)
			for _, refund := range []struct {
				component, id        string
				cumulative, reversed int64
			}{
				{"fee", "fee-full", 10, 0}, {"model", "lower-partial", 15, 3},
				{"model", "lower-full", 45, 6}, {"Model", "upper-full", 45, 9},
			} {
				input := model.AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: prefix, ComponentID: refund.component,
					RefundID: prefix + refund.id, CumulativeQuota: refund.cumulative, Reason: "model_after_sale"}
				event, err := model.AgencyRefundWalletCharge(input, token.Key)
				require.NoError(t, err)
				assert.Equal(t, refund.reversed, event.ReversedCommissionAmountMicros)
				replay, err := model.AgencyRefundWalletCharge(input, token.Key)
				require.NoError(t, err)
				assert.Equal(t, event.EventID, replay.EventID)
				verifyComponentFundingEvidence(t, app, prefix)
			}
			// The original is deliberately delivered after all refunds committed.
			require.NoError(t, app.RunConsumerOnce(context.Background(), 20))
			var deliveries []model.AgencyEventDelivery
			require.NoError(t, tx.Where("event_id IN (?)", tx.Model(&model.AgencyBillingOutbox{}).Select("event_id").Where("user_id = ?", user.Id)).Find(&deliveries).Error)
			require.Len(t, deliveries, 7, "topup, reservation, finalization and four refund operations")
			for _, delivery := range deliveries {
				assert.Equal(t, "done", delivery.Status, delivery.EventID)
			}
			var balances []model.AgencyCommissionBalance
			require.NoError(t, tx.Where("agency_id = ?", agency.ID).Find(&balances).Error)
			require.Len(t, balances, 1)
			assert.Equal(t, int64(18), balances[0].EarnedMicros)
			assert.Equal(t, int64(18), balances[0].ReversedMicros)
			assert.Zero(t, balances[0].AvailableMicros)
			var lower, upper model.AgencyCommissionLedger
			require.NoError(t, tx.Where("event_id = ? AND component_key = ?", original.EventID, model.AgencyComponentKey("model")).First(&lower).Error)
			require.NoError(t, tx.Where("event_id = ? AND component_key = ?", original.EventID, model.AgencyComponentKey("Model")).First(&upper).Error)
			assert.NotEqual(t, upper.ID, lower.ID)
			var lowerReversed int64
			require.NoError(t, tx.Model(&model.AgencyCommissionLedger{}).Where("original_entry_id = ?", lower.ID).Select("SUM(amount_micros)").Scan(&lowerReversed).Error)
			assert.Equal(t, int64(-9), lowerReversed)
			require.NoError(t, tx.First(&user, user.Id).Error)
			require.NoError(t, tx.First(&token, token.Id).Error)
			assert.Equal(t, 100, user.Quota)
			assert.Equal(t, 100, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			var source model.AgencyFundingLot
			require.NoError(t, tx.Where("source_id = ?", prefix+"-topup").First(&source).Error)
			assert.Equal(t, int64(60), source.PaidAvailable)
			assert.Equal(t, int64(40), source.BonusAvailable)
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
			ctx.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/reports/summary?start_date="+now+"&end_date="+now, nil)
			ctx.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
			app.reportSummary(ctx)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var report struct {
				Data struct {
					Calls            string
					ChargedQuota     string `json:"charged_quota"`
					CommissionMicros string `json:"commission_micros"`
					ReversalMicros   string `json:"reversal_micros"`
				}
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &report))
			assert.Equal(t, "1", report.Data.Calls, "three billed components represent one request")
			assert.Equal(t, "100", report.Data.ChargedQuota)
			assert.Equal(t, "18", report.Data.CommissionMicros)
			assert.Equal(t, "18", report.Data.ReversalMicros)
		})
	}
}

// Reuse the real gateway lifecycle to verify evidence across all supported
// dialects before and after each cumulative refund, including full fee refunds.
func verifyComponentFundingEvidence(t *testing.T, app *App, chargeID string) {
	t.Helper()
	var operations []model.AgencyBillingOperation
	require.NoError(t, app.db.Where("charge_id = ? AND operation IN ?", chargeID, []string{"finalize", "reverse"}).Find(&operations).Error)
	require.NotEmpty(t, operations)
	for _, operation := range operations {
		result, err := reconciliationEvidence(app.db, model.AgencyReconciliationIssue{ObjectType: "billing_operation", ObjectID: operation.OperationID, Status: "open"}, false)
		require.NoError(t, err)
		assert.Equal(t, "consistent", result.State, "%s: %+v", operation.OperationID, result.Checks)
	}
	var components []model.AgencyChargeComponent
	require.NoError(t, app.db.Where("charge_id = ?", chargeID).Find(&components).Error)
	require.NotEmpty(t, components)
	for _, component := range components {
		result, err := reconciliationEvidence(app.db, model.AgencyReconciliationIssue{ObjectType: "charge_component", ObjectID: stringID(component.ID), Status: "open"}, false)
		require.NoError(t, err)
		assert.Equal(t, "consistent", result.State, "%s: %+v", component.ComponentID, result.Checks)
		assert.Equal(t, []string{"verify_resolved"}, result.AllowedActions)
	}
}

func TestComponentReadinessRejectsMissingFinancialUniqueness(t *testing.T) {
	app := newAgencyTestApp(t)
	app.SetReady(true)
	require.NoError(t, app.db.Migrator().DropIndex(&model.AgencyCommissionLedger{}, "uidx_agency_commission_component_key"))
	response := httptest.NewRecorder()
	app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.Contains(t, response.Body.String(), "uidx_agency_commission_component_key")
	require.NoError(t, model.MigrateAgency(app.db))
	response = httptest.NewRecorder()
	app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "agency-billing-v2")
}
