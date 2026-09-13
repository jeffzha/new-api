package agencyhub

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type componentReconciliationFixture struct {
	app       *App
	component model.AgencyChargeComponent
	funding   model.AgencyComponentFunding
}

// Financial history comes from actual gateway transactions. Each corruption
// below must become a visible, non-repairable finding without changing money.
func newComponentReconciliationFixture(t *testing.T) componentReconciliationFixture {
	t.Helper()
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Token{}))
	priorDB, priorRedis := model.DB, common.RedisEnabled
	model.DB, common.RedisEnabled = app.db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = priorDB, priorRedis })
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "true")
	user := model.User{Username: "matrix-review", AffCode: "matrix-review", Status: common.UserStatusEnabled, BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
	require.NoError(t, app.db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "matrix-review-token", RemainQuota: 100, Status: common.TokenStatusEnabled}
	require.NoError(t, app.db.Create(&token).Error)
	require.NoError(t, app.db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, model.RecordAgencyTopup(app.db, int64(user.Id), "payment", "matrix-topup", "payment_callback", 60, 40))
	snapshot := &agencycontract.PricingSnapshot{AgencyID: 1, BindingID: 1, CommissionEligible: true, OriginModelName: "matrix-model",
		SettlementBPS: 6000, SalesBPS: 9000, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	_, _, err := model.TryReserveAgencyWalletAndTokenWithSequence(user.Id, token.Id, 100, token.Key, "matrix-charge", 100, false, snapshot)
	require.NoError(t, err)
	_, err = model.AgencyCommitWalletCharge(agencycontract.BillingEvent{FinancialChargeID: "matrix-charge", UserID: int64(user.Id),
		BusinessStatus: "success", ChargedTotalQuota: 100, CommissionableQuota: 90, NoncommissionableQuota: 10, SettlementCostQuota: 60, TheoreticalCommissionQuota: 30,
		Components: []agencycontract.BillingComponent{
			{ComponentID: "model", ChargedTotalQuota: 90, CommissionableQuota: 90, SettlementCostQuota: 60, TheoreticalCommissionQuota: 30, CommissionEligible: true},
			{ComponentID: "fee", ChargedTotalQuota: 10, NoncommissionableQuota: 10},
		}}, token.Key)
	require.NoError(t, err)
	_, err = model.AgencyRefundWalletCharge(model.AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: "matrix-charge", ComponentID: "model", CumulativeQuota: 30, RefundID: "matrix-refund", Reason: "model_after_sale"}, token.Key)
	require.NoError(t, err)
	verifyComponentFundingEvidence(t, app, "matrix-charge")
	f := componentReconciliationFixture{app: app}
	require.NoError(t, app.db.Where("charge_id = ? AND component_key = ?", "matrix-charge", model.AgencyComponentKey("model")).First(&f.component).Error)
	require.NoError(t, app.db.Where("charge_component_id = ?", f.component.ID).First(&f.funding).Error)
	return f
}

func TestReconciliationFindsComponentSourceCorruption(t *testing.T) {
	for _, tc := range []struct {
		name, check string
		corrupt     func(*testing.T, componentReconciliationFixture)
	}{
		{"missing_matrix", "component_paid_matrix", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Delete(&f.funding).Error)
		}},
		{"same_total_wrong_source", "component_paid_matrix", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Model(&f.funding).Updates(map[string]any{"paid_quota": f.funding.PaidQuota - 1, "nonpaid_quota": f.funding.NonpaidQuota + 1}).Error)
		}},
		{"other_customer_allocation", "allocation_user", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Model(&model.AgencyFundingAllocation{}).Where("id = ?", f.funding.AllocationID).Update("user_id", f.component.UserID+1).Error)
		}},
		{"other_charge_allocation", "allocation_charge", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Model(&model.AgencyFundingAllocation{}).Where("id = ?", f.funding.AllocationID).Update("charge_id", "another-charge").Error)
		}},
		{"wrong_lot", "allocation_lot", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Model(&f.funding).Update("lot_id", f.funding.LotID+1).Error)
		}},
		{"missing_allocation", "allocation_exists", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Delete(&model.AgencyFundingAllocation{}, f.funding.AllocationID).Error)
		}},
		{"same_total_allocation_source_swapped", "paid_remaining_matrix", func(t *testing.T, f componentReconciliationFixture) {
			var allocation model.AgencyFundingAllocation
			require.NoError(t, f.app.db.First(&allocation, f.funding.AllocationID).Error)
			require.NoError(t, f.app.db.Model(&allocation).Updates(map[string]any{"consumed": allocation.Consumed - 1, "nonpaid_consumed": allocation.NonpaidConsumed + 1}).Error)
		}},
		{"refund_wrong_source", "component_refund_paid_target", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Model(&f.component).Updates(map[string]any{"restored_paid_quota": f.component.RestoredPaidQuota - 1, "restored_nonpaid_quota": f.component.RestoredNonpaidQuota + 1}).Error)
		}},
		{"refund_wrong_matrix", "paid_refund_fifo", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Model(&f.funding).Update("restored_paid_quota", f.funding.RestoredPaidQuota+1).Error)
		}},
		{"commission_below_cap_but_wrong", "component_refund_micros", func(t *testing.T, f componentReconciliationFixture) {
			require.NoError(t, f.app.db.Model(&f.component).Update("reversed_commission_micros", f.component.ReversedCommissionMicros-1).Error)
		}},
		{"original_result_changed", "component_original_matches_committed", func(t *testing.T, f componentReconciliationFixture) {
			var original agencycontract.BillingComponent
			require.NoError(t, common.UnmarshalJsonStr(f.component.OriginalResult, &original))
			original.SettlementCostQuota++
			encoded, err := common.Marshal(original)
			require.NoError(t, err)
			require.NoError(t, f.app.db.Model(&f.component).Update("original_result", string(encoded)).Error)
		}},
		{"positive_overflow_cannot_match", "component_paid_matrix", func(t *testing.T, f componentReconciliationFixture) {
			// MaxInt64 + MaxInt64 + 56 wraps to the original paid54 in int64.
			require.NoError(t, f.app.db.Model(&f.funding).Update("paid_quota", int64(math.MaxInt64)).Error)
			for i, amount := range []int64{math.MaxInt64, 56} {
				require.NoError(t, f.app.db.Create(&model.AgencyComponentFunding{ChargeComponentID: f.component.ID, AllocationID: int64(900 + i), LotID: f.funding.LotID, PaidQuota: amount, Version: 1}).Error)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newComponentReconciliationFixture(t)
			tc.corrupt(t, f)
			summary, err := f.app.Reconcile(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 2, summary.CheckedChargeComponents)
			var issue model.AgencyReconciliationIssue
			require.NoError(t, f.app.db.Where("object_type = ? AND object_id = ? AND status = ?", "charge_component", stringID(f.component.ID), "open").First(&issue).Error)
			assert.Contains(t, issue.Difference, tc.check)
			result, err := reconciliationEvidence(f.app.db, issue, false)
			require.NoError(t, err)
			assert.Equal(t, "inconsistent", result.State)
			assert.Empty(t, result.AllowedActions, "corrupt finances must not offer verify-resolved or blind delivery repair")
			failedExpectedCheck := false
			for _, check := range result.Checks {
				if strings.Contains(check.Name, tc.check) && !check.Matched {
					failedExpectedCheck = true
				}
			}
			assert.True(t, failedExpectedCheck, "%s must report a mismatch", tc.check)
			var account model.AgencyFundingAccount
			require.NoError(t, f.app.db.Where("user_id = ?", f.component.UserID).First(&account).Error)
			assert.EqualValues(t, 18, account.PaidAvailable)
			assert.EqualValues(t, 12, account.NonpaidAvailable)
		})
	}
}

func TestReconciliationFindsMissingComponentsAndJournalRefundDrift(t *testing.T) {
	for _, missingComponents := range []bool{false, true} {
		t.Run(map[bool]string{false: "journal_drift", true: "all_components_deleted"}[missingComponents], func(t *testing.T) {
			f := newComponentReconciliationFixture(t)
			var operation model.AgencyBillingOperation
			require.NoError(t, f.app.db.Where("charge_id = ? AND operation = ?", f.component.ChargeID, "finalize").First(&operation).Error)
			if missingComponents {
				require.NoError(t, f.app.db.Where("charge_id = ?", f.component.ChargeID).Delete(&model.AgencyChargeComponent{}).Error)
			} else {
				require.NoError(t, f.app.db.Model(&model.AgencyBillingJournal{}).Where("charge_id = ?", f.component.ChargeID).Update("reversed_quota", 29).Error)
			}
			_, err := f.app.Reconcile(context.Background())
			require.NoError(t, err)
			var issue model.AgencyReconciliationIssue
			require.NoError(t, f.app.db.Where("object_type = ? AND object_id = ? AND status = ?", "billing_operation", operation.OperationID, "open").First(&issue).Error)
			if missingComponents {
				assert.Contains(t, issue.Difference, "journal_component_count")
			} else {
				assert.Contains(t, issue.Difference, "journal_refunded_quota")
			}
			result, err := reconciliationEvidence(f.app.db, issue, false)
			require.NoError(t, err)
			assert.Empty(t, result.AllowedActions)
		})
	}
}
