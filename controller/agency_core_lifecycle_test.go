package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This follows the real registration controller, payment completion, pricing
// engine, billing factory, actual-usage settlement, outbox consumer and authenticated
// reporting API. No financial operation, funding allocation or commission is
// manufactured by the fixture. The upstream usage is deterministic, not a paid call.
func TestAgencyCoreLifecycleFromInvitationToAuthenticatedCommissionReport(t *testing.T) {
	for _, components := range []bool{false, true} {
		t.Run("component_billing_"+strconv.FormatBool(components), func(t *testing.T) {
			db, app := setupAgencyInviteControllerTest(t)
			t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", strconv.FormatBool(components))
			require.NoError(t, db.AutoMigrate(&model.TopUp{}, &model.Log{}, &model.Channel{}))
			priorQuotaPerUnit, priorPreConsumed := common.QuotaPerUnit, common.PreConsumedQuota
			priorBatch, priorLogEnabled := common.BatchUpdateEnabled, common.LogConsumeEnabled
			priorDisplay := operation_setting.GetGeneralSetting().QuotaDisplayType
			priorModelRatios, priorCompletionRatios := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()
			priorGroupRatios := ratio_setting.GroupRatio2JSONString()
			common.QuotaPerUnit, common.PreConsumedQuota = 100, 100
			common.BatchUpdateEnabled, common.LogConsumeEnabled = false, true
			operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"agency-public-model":1}`))
			require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"agency-public-model":2}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":0.25,"different-route":0.1}`))
			t.Cleanup(func() {
				common.QuotaPerUnit, common.PreConsumedQuota = priorQuotaPerUnit, priorPreConsumed
				common.BatchUpdateEnabled, common.LogConsumeEnabled = priorBatch, priorLogEnabled
				operation_setting.GetGeneralSetting().QuotaDisplayType = priorDisplay
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(priorModelRatios))
				require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(priorCompletionRatios))
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(priorGroupRatios))
			})

			agency, temporaryPassword, err := app.CreateAgency(1, "Core Lifecycle Agency", "core_operator", agencyInviteTestPolicy())
			require.NoError(t, err)
			registered := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"core_customer","password":"password123","invite":%q}`, agency.InviteCode))
			require.Contains(t, registered.Body.String(), `"success":true`)
			var customer model.User
			require.NoError(t, db.Where("username = ?", "core_customer").First(&customer).Error)
			require.Equal(t, model.AgencyDurableBillingMode, customer.BillingMode)
			require.Zero(t, customer.Quota)
			var token model.Token
			require.NoError(t, db.Where("user_id = ?", customer.Id).First(&token).Error)
			initialTokenQuota := token.RemainQuota

			order := model.TopUp{UserId: customer.Id, Amount: 1, Money: 1, TradeNo: "core-order",
				PaymentProvider: model.PaymentProviderEpay, PaymentMethod: "alipay", Status: common.TopUpStatusPending}
			require.NoError(t, order.Insert())
			payment := &model.TopupPaymentSnapshot{ActualMoney: "1.00", CurrencyCode: "USD", PaymentReference: "verified-provider-order"}
			alreadyDone, err := model.RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", payment)
			require.NoError(t, err)
			assert.False(t, alreadyDone)
			alreadyDone, err = model.RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", payment)
			require.NoError(t, err)
			assert.True(t, alreadyDone, "replayed provider callback cannot double credit the customer")
			require.NoError(t, model.ApplyAgencyQuotaDelta(int64(customer.Id), 100, "admin_grant"))
			require.NoError(t, db.First(&customer, customer.Id).Error)
			require.Equal(t, 200, customer.Quota)

			channel := model.Channel{Name: "local-usage-fixture", Type: 1, Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(&channel).Error)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{UserId: customer.Id, TokenId: token.Id, TokenKey: token.Key,
				TokenUnlimited: token.UnlimitedQuota, UserGroup: "default", UsingGroup: "different-route",
				UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
				OriginModelName: "agency-public-model", RequestId: "core-financial-charge", RequestURLPath: "/v1/chat/completions",
				ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: 1}, StartTime: time.Now(), FirstResponseTime: time.Now()}
			price, err := resolveAgencyPrice(ctx, info, func() (hosttypes.PriceData, error) {
				return helper.ModelPriceHelper(ctx, info, 100, &relaytypes.TokenCountMeta{})
			})
			require.NoError(t, err)
			assert.Equal(t, 90, price.QuotaToPreConsume)
			assert.Equal(t, 0.9, price.GroupRatioInfo.GroupRatio, "routing group discount must be replaced, not multiplied")
			assert.Equal(t, int64(100), info.AgencyStandardQuota)
			require.Nil(t, service.PreConsumeBilling(ctx, price.QuotaToPreConsume, info))
			require.IsType(t, &service.AgencyBillingSession{}, info.Billing)
			var journal model.AgencyBillingJournal
			require.NoError(t, db.Where("charge_id = ?", info.RequestId).First(&journal).Error)
			assert.Equal(t, "reserved", journal.Status)
			var earnedBeforeSettlement int64
			require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&earnedBeforeSettlement).Error)
			assert.Zero(t, earnedBeforeSettlement)

			// Actual Q=100+20*2=140, B=126, T=105 and G=21. Only paid
			// P=100 counts, so K=round(21*100/126)=17 and M=$0.17.
			service.PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, nil)
			require.NoError(t, db.First(&journal, journal.ID).Error)
			require.Equal(t, "finalized", journal.Status)
			assert.Equal(t, int64(126), journal.ChargedTotalQuota)
			assert.Equal(t, int64(105), journal.SettlementCostQuota)
			assert.Equal(t, int64(100), journal.PaidAllocatedQuota)
			assert.Equal(t, int64(17), journal.CommissionQuota)
			assert.Equal(t, int64(170000), journal.CommissionAmountMicros)
			require.NoError(t, service.SettleBilling(ctx, info, 126), "finalization replay must return the same receipt")
			assert.ErrorIs(t, service.SettleBilling(ctx, info, 127), model.ErrAgencyChargeConflict, "a replay with changed final usage must not silently succeed")
			require.NoError(t, db.First(&customer, customer.Id).Error)
			require.NoError(t, db.First(&token, token.Id).Error)
			assert.Equal(t, 74, customer.Quota)
			assert.Equal(t, initialTokenQuota-126, token.RemainQuota)
			assert.Equal(t, 126, token.UsedQuota)
			var account model.AgencyFundingAccount
			require.NoError(t, db.First(&account, customer.Id).Error)
			assert.Zero(t, account.PaidAvailable)
			assert.Equal(t, int64(74), account.NonpaidAvailable)
			assert.Zero(t, account.DebtQuota)
			var envelopes []model.AgencyBillingOutbox
			require.NoError(t, db.Where("user_id = ?", customer.Id).Order("money_seq").Find(&envelopes).Error)
			require.Len(t, envelopes, 4, "topup, grant, reservation and one finalization")
			for _, envelope := range envelopes {
				var event agencycontract.BillingEvent
				require.NoError(t, common.UnmarshalJsonStr(envelope.Payload, &event))
				assert.Equal(t, envelope.MoneySeq, event.MoneySeq, "every real money operation must occupy the same sequence in its immutable payload")
				assert.Equal(t, envelope.EventID, event.EventID)
				assert.Equal(t, envelope.OperationID, event.OperationID)
			}

			require.NoError(t, app.RunConsumerOnce(context.Background(), 20))
			require.NoError(t, app.RunConsumerOnce(context.Background(), 20), "consumer replay cannot duplicate earnings")
			var balance model.AgencyCommissionBalance
			require.NoError(t, db.Where("agency_id = ? AND currency_code = ?", agency.ID, "USD").First(&balance).Error)
			assert.Equal(t, int64(170000), balance.EarnedMicros)
			assert.Equal(t, balance.EarnedMicros, balance.AvailableMicros)
			var entries []model.AgencyCommissionLedger
			require.NoError(t, db.Where("agency_id = ?", agency.ID).Find(&entries).Error)
			require.Len(t, entries, 1)
			assert.Equal(t, int64(17), entries[0].CommissionQuota)
			assert.Equal(t, "agency-public-model", entries[0].OriginModelName)
			var usageFacts []model.AgencyUsageFact
			require.NoError(t, db.Where("user_id = ?", customer.Id).Find(&usageFacts).Error)
			require.Len(t, usageFacts, 1, "topups, grants and reservations are not model calls")
			assert.Equal(t, int64(126), usageFacts[0].ChargedQuota)
			for _, envelope := range envelopes {
				var receipt model.AgencySourceEvent
				require.NoError(t, db.Where("event_id = ?", envelope.EventID).First(&receipt).Error)
				assert.Equal(t, envelope.MoneySeq, receipt.MoneySeq)
				assert.Contains(t, []string{"done", "skipped"}, receipt.ProcessingStatus)
			}

			// Reporting must work using the issued operator password/session, not
			// by injecting an identity into the Gin context or inserting a session.
			hubRouter := app.Router()
			nonceResponse := httptest.NewRecorder()
			hubRouter.ServeHTTP(nonceResponse, httptest.NewRequest(http.MethodGet, "/agency/api/v1/auth/nonce", nil))
			require.Equal(t, http.StatusOK, nonceResponse.Code)
			var nonceEnvelope struct{ Data struct{ Nonce string } }
			require.NoError(t, common.Unmarshal(nonceResponse.Body.Bytes(), &nonceEnvelope))
			loginBody, err := common.Marshal(map[string]string{"username": "core_operator", "password": temporaryPassword, "nonce": nonceEnvelope.Data.Nonce})
			require.NoError(t, err)
			loginRequest := httptest.NewRequest(http.MethodPost, "/agency/api/v1/auth/login", bytes.NewReader(loginBody))
			loginRequest.Header.Set("Content-Type", "application/json")
			for _, cookie := range nonceResponse.Result().Cookies() {
				loginRequest.AddCookie(cookie)
			}
			loginResponse := httptest.NewRecorder()
			hubRouter.ServeHTTP(loginResponse, loginRequest)
			require.Equal(t, http.StatusOK, loginResponse.Code, loginResponse.Body.String())
			cookies := loginResponse.Result().Cookies()
			passwordBody, err := common.Marshal(map[string]string{"current_password": temporaryPassword, "new_password": "Core-Password-2026!"})
			require.NoError(t, err)
			passwordRequest := httptest.NewRequest(http.MethodPost, "/agency/api/v1/auth/change-password", bytes.NewReader(passwordBody))
			passwordRequest.Header.Set("Content-Type", "application/json")
			for _, cookie := range cookies {
				passwordRequest.AddCookie(cookie)
				if cookie.Name == "agency_csrf" {
					passwordRequest.Header.Set("X-CSRF-Token", cookie.Value)
				}
			}
			passwordResponse := httptest.NewRecorder()
			hubRouter.ServeHTTP(passwordResponse, passwordRequest)
			require.Equal(t, http.StatusOK, passwordResponse.Code, passwordResponse.Body.String())
			today := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
			reportRequest := httptest.NewRequest(http.MethodGet, "/agency/api/v1/reports/summary?start_date="+today+"&end_date="+today, nil)
			for _, cookie := range cookies {
				reportRequest.AddCookie(cookie)
			}
			reportResponse := httptest.NewRecorder()
			hubRouter.ServeHTTP(reportResponse, reportRequest)
			require.Equal(t, http.StatusOK, reportResponse.Code, reportResponse.Body.String())
			var report struct {
				Data struct {
					Calls            string
					ChargedQuota     string `json:"charged_quota"`
					CommissionMicros string `json:"commission_micros"`
				}
			}
			require.NoError(t, common.Unmarshal(reportResponse.Body.Bytes(), &report))
			assert.Equal(t, "1", report.Data.Calls)
			assert.Equal(t, "126", report.Data.ChargedQuota)
			assert.Equal(t, "170000", report.Data.CommissionMicros)
			if components {
				input := model.AgencyComponentRefundInput{UserID: int64(customer.Id), ChargeID: info.RequestId,
					RefundID: "core-half-refund", CumulativeQuota: 63, Reason: "model_after_sale"}
				refund, err := service.RefundAgencyModelCharge(input, token.Key)
				require.NoError(t, err)
				assert.Equal(t, agencycontract.ComponentSchemaVersion, refund.SchemaVersion)
				assert.Equal(t, int64(85000), refund.ReversedCommissionAmountMicros)
				replayed, err := service.RefundAgencyModelCharge(input, token.Key)
				require.NoError(t, err)
				assert.Equal(t, refund.EventID, replayed.EventID)
				require.NoError(t, app.RunConsumerOnce(context.Background(), 20))
				require.NoError(t, db.First(&balance, balance.ID).Error)
				assert.Equal(t, int64(85000), balance.AvailableMicros)
				assert.Equal(t, int64(85000), balance.ReversedMicros)
				require.NoError(t, db.First(&customer, customer.Id).Error)
				require.NoError(t, db.First(&token, token.Id).Error)
				assert.Equal(t, 137, customer.Quota)
				assert.Equal(t, initialTokenQuota-63, token.RemainQuota)
				assert.Equal(t, 63, token.UsedQuota)
			}
		})
	}
}

func TestAgencyCoreCancelledReservationCompletesConsumerWithoutCommission(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "true")
	agency, _, err := app.CreateAgency(1, "Cancellation Agency", "cancel_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	registered := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"cancel_customer","password":"password123","invite":%q}`, agency.InviteCode))
	require.Contains(t, registered.Body.String(), `"success":true`)
	var customer model.User
	require.NoError(t, db.Where("username = ?", "cancel_customer").First(&customer).Error)
	var token model.Token
	require.NoError(t, db.Where("user_id = ?", customer.Id).First(&token).Error)
	initialTokenQuota := token.RemainQuota
	require.NoError(t, model.ApplyAgencyQuotaDelta(int64(customer.Id), 100, "admin_grant"))
	snapshot, err := service.AgencyQuoteForUser(customer.Id, token.Id, "cancel-model", 0)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{UserId: customer.Id, TokenId: token.Id, TokenKey: token.Key,
		TokenUnlimited: token.UnlimitedQuota, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
		RequestId: "cancel-charge", OriginModelName: "cancel-model", RequestURLPath: "/v1/chat/completions"}
	require.NoError(t, service.AttachAgencyQuote(info, snapshot, 100))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.Nil(t, service.PreConsumeBilling(ctx, 90, info))
	info.Billing.Refund(ctx)
	info.Billing.Refund(ctx)
	assert.False(t, info.Billing.NeedsRefund())
	assert.ErrorIs(t, info.Billing.Settle(90), model.ErrAgencyChargeConflict)
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", info.RequestId).First(&journal).Error)
	assert.Equal(t, "cancelled", journal.Status)
	assert.Zero(t, journal.ChargedTotalQuota)
	assert.Zero(t, journal.CommissionQuota)
	require.NoError(t, db.First(&customer, customer.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, customer.Quota)
	assert.Equal(t, initialTokenQuota, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	require.NoError(t, app.RunConsumerOnce(context.Background(), 2))
	var cancelledOutbox model.AgencyBillingOutbox
	require.NoError(t, db.Where("event_id = ?", info.AgencyBillingEventID).First(&cancelledOutbox).Error)
	var forgedCancellation agencycontract.BillingEvent
	require.NoError(t, common.UnmarshalJsonStr(cancelledOutbox.Payload, &forgedCancellation))
	forgedCancellation.ChargedTotalQuota = 1
	forgedCancellation.NoncommissionableQuota = 1
	assert.ErrorContains(t, app.ProcessBillingEvent(forgedCancellation), "not finalized", "charged usage cannot masquerade as a cancelled reservation")
	require.NoError(t, app.RunConsumerOnce(context.Background(), 20))
	var finalDelivery model.AgencyEventDelivery
	require.NoError(t, db.Where("event_id = ?", info.AgencyBillingEventID).First(&finalDelivery).Error)
	assert.Contains(t, []string{"done", "skipped"}, finalDelivery.Status, "a cancelled reservation cannot block all subsequent money sequences")
	var commissionCount int64
	require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&commissionCount).Error)
	assert.Zero(t, commissionCount)
	var usageCount int64
	require.NoError(t, db.Model(&model.AgencyUsageFact{}).Count(&usageCount).Error)
	assert.Zero(t, usageCount, "cancelling a reservation does not create a billed model call")
}

func TestAgencyCoreTextSettlementUsesUnroundedModelBasis(t *testing.T) {
	for _, testCase := range []struct {
		name                        string
		prompt, charged, settlement int
	}{
		{name: "one_token_must_not_become_negative_commission", prompt: 1, charged: 1, settlement: 1},
		{name: "three_tokens_do_not_round_standard_quota_first", prompt: 3, charged: 4, settlement: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, app := setupAgencyInviteControllerTest(t)
			t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "false")
			require.NoError(t, db.AutoMigrate(&model.Log{}, &model.Channel{}))
			previousModelRatios := ratio_setting.ModelRatio2JSONString()
			previousBatch, previousPreConsumed := common.BatchUpdateEnabled, common.PreConsumedQuota
			common.BatchUpdateEnabled = false
			common.PreConsumedQuota = 1
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"agency-small-model":1.5}`))
			t.Cleanup(func() {
				common.BatchUpdateEnabled, common.PreConsumedQuota = previousBatch, previousPreConsumed
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModelRatios))
			})
			agency, _, err := app.CreateAgency(1, "Small Usage Agency", "small_operator", agencyInviteTestPolicy())
			require.NoError(t, err)
			registered := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"small_customer","password":"password123","invite":%q}`, agency.InviteCode))
			require.Contains(t, registered.Body.String(), `"success":true`)
			var customer model.User
			require.NoError(t, db.Where("username = ?", "small_customer").First(&customer).Error)
			var token model.Token
			require.NoError(t, db.Where("user_id = ?", customer.Id).First(&token).Error)
			require.NoError(t, model.ApplyAgencyQuotaDelta(int64(customer.Id), 100, "admin_grant"))
			channel := model.Channel{Name: "small-usage-fixture", Type: 1, Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(&channel).Error)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{UserId: customer.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: token.UnlimitedQuota,
				OriginModelName: "agency-small-model", RequestId: "small-charge", RequestURLPath: "/v1/chat/completions",
				UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, UserGroup: "default", UsingGroup: "default",
				ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: 1}, StartTime: time.Now(), FirstResponseTime: time.Now()}
			price, err := resolveAgencyPrice(ctx, info, func() (hosttypes.PriceData, error) {
				return helper.ModelPriceHelper(ctx, info, testCase.prompt, &relaytypes.TokenCountMeta{})
			})
			require.NoError(t, err)
			require.Nil(t, service.PreConsumeBilling(ctx, price.QuotaToPreConsume, info))
			service.PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: testCase.prompt, TotalTokens: testCase.prompt}, nil)
			var journal model.AgencyBillingJournal
			require.NoError(t, db.Where("charge_id = ?", info.RequestId).First(&journal).Error)
			assert.Equal(t, "finalized", journal.Status)
			assert.Equal(t, int64(testCase.charged), journal.ChargedTotalQuota)
			assert.Equal(t, int64(testCase.settlement), journal.SettlementCostQuota)
			require.NoError(t, db.First(&customer, customer.Id).Error)
			assert.Equal(t, 100-testCase.charged, customer.Quota)
		})
	}
}
