package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOPSAuthorityRejectsUnsupportedSubscriptionPurchasesBeforePayment(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_BILLING_CURRENCY", "USD")
	t.Setenv("OPS_PUSH_CURRENCY", "USD")
	user := setupSnapshotWebhookDB(t, model.PaymentProviderStripe)
	require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{}))
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"quota": 5_000_000, "billing_mode": ""}).Error)
	plan := model.SubscriptionPlan{Title: "local-only-subscription", Enabled: true, PriceAmount: 1, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 1000}
	require.NoError(t, model.DB.Create(&plan).Error)
	const unsupported = "subscriptions are unavailable while OPS owns the wallet"
	for _, handler := range []struct {
		name  string
		serve gin.HandlerFunc
	}{
		{"balance", SubscriptionRequestBalancePay},
		{"stripe", SubscriptionRequestStripePay},
		{"creem", SubscriptionRequestCreemPay},
		{"epay", SubscriptionRequestEpay},
		{"waffo", SubscriptionRequestWaffoPancakePay},
	} {
		t.Run(handler.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/purchase", func(c *gin.Context) { c.Set("id", user.Id) }, handler.serve)
			request := httptest.NewRequest(http.MethodPost, "/purchase", strings.NewReader(fmt.Sprintf(`{"plan_id":%d}`, plan.Id)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Contains(t, response.Body.String(), unsupported)
		})
	}
	require.ErrorContains(t, model.PurchaseSubscriptionWithBalance(user.Id, plan.Id), unsupported)
	_, err := model.AdminBindSubscription(user.Id, plan.Id, "test")
	require.ErrorContains(t, err, unsupported)
	order := model.SubscriptionOrder{UserId: user.Id, PlanId: plan.Id, TradeNo: "pending-subscription-at-cutover", Money: 1, PaymentProvider: model.PaymentProviderStripe, Status: common.TopUpStatusPending}
	require.NoError(t, model.DB.Create(&order).Error)
	require.ErrorContains(t, model.CompleteSubscriptionOrder(order.TradeNo, "", model.PaymentProviderStripe, ""), unsupported)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 5_000_000, user.Quota, "subscription paths cannot spend the historical source wallet")
	require.NoError(t, model.DB.First(&order, order.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	var subscriptions, orders int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Count(&orders).Error)
	assert.Zero(t, subscriptions)
	assert.Equal(t, int64(1), orders, "no new checkout order may be created under OPS custody")
}

func TestProviderMinorUnitPaymentSnapshotsPreserveCurrencyScale(t *testing.T) {
	for _, test := range []struct{ provider, amount, currency, expected string }{
		{model.PaymentProviderStripe, "1234", "usd", "12.34"},
		{model.PaymentProviderStripe, "1234", "JPY", "1234"},
		{model.PaymentProviderStripe, "1234", "KWD", "1.234"},
		{model.PaymentProviderStripe, "500", "ISK", "5"},
		{model.PaymentProviderStripe, "500", "UGX", "5"},
		{model.PaymentProviderCreem, "1234", "USD", "12.34"},
		{model.PaymentProviderCreem, "0", "USD", "0"},
		{model.PaymentProviderCreem, "", "USD", ""},
		{model.PaymentProviderStripe, "100", "NOTKNOWN", ""},
		{model.PaymentProviderStripe, "1e99999", "USD", ""},
	} {
		t.Run(test.provider+"/"+test.currency+"/"+test.amount, func(t *testing.T) {
			snapshot := paymentSnapshotFromMinorUnits(test.amount, test.currency, "provider-receipt", test.provider)
			require.NotNil(t, snapshot)
			assert.Equal(t, test.expected, snapshot.ActualMoney)
			assert.Equal(t, "provider-receipt", snapshot.PaymentReference)
		})
	}
	assert.Nil(t, paymentSnapshotFromMinorUnits("100", "USD", "", model.PaymentProviderStripe))
}

func setupSnapshotWebhookDB(t *testing.T, provider string) *model.User {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Log{}, &model.SubscriptionOrder{}))
	require.NoError(t, model.MigrateAgency(db))
	previousDB, previousLogs, previousQuota := model.DB, model.LOG_DB, common.QuotaPerUnit
	previousRedis, previousBatch := common.RedisEnabled, common.BatchUpdateEnabled
	previousSMTP := common.SMTPServer
	model.DB, model.LOG_DB, common.QuotaPerUnit = db, db, 1000
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	common.SMTPServer = ""
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.QuotaPerUnit = previousDB, previousLogs, previousQuota
		common.RedisEnabled, common.BatchUpdateEnabled = previousRedis, previousBatch
		common.SMTPServer = previousSMTP
		_ = sqlDB.Close()
	})
	user := &model.User{Username: "snapshot-webhook", Status: common.UserStatusEnabled, BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(&model.TopUp{UserId: user.Id, Amount: 200, Money: 3, TradeNo: "webhook-snapshot", PaymentProvider: provider, Status: common.TopUpStatusPending}).Error)
	return user
}

func TestVerifiedStripeWebhookPersistsProviderPaymentRatherThanQuotaBase(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	oldAPI, oldWebhook, oldPrice := setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId
	setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId = "sk_test_snapshot", "whsec_snapshot", "price_snapshot"
	t.Cleanup(func() {
		setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId = oldAPI, oldWebhook, oldPrice
	})
	user := setupSnapshotWebhookDB(t, model.PaymentProviderStripe)
	body := `{"id":"evt_snapshot","object":"event","type":"checkout.session.completed","data":{"object":{"id":"cs_snapshot","client_reference_id":"webhook-snapshot","customer":"cus_snapshot","status":"complete","payment_status":"paid","payment_intent":"pi_snapshot","amount_total":1234,"currency":"usd"}}}`
	router := gin.New()
	router.POST("/stripe", StripeWebhook)
	for _, valid := range []bool{false, true, true} {
		request := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(body))
		timestamp := fmt.Sprint(time.Now().Unix())
		signer := hmac.New(sha256.New, []byte(setting.StripeWebhookSecret))
		_, err := signer.Write([]byte(timestamp + "." + body))
		require.NoError(t, err)
		signature := hex.EncodeToString(signer.Sum(nil))
		if !valid {
			signature = strings.Repeat("0", len(signature))
		}
		request.Header.Set("Stripe-Signature", "t="+timestamp+",v1="+signature)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if !valid {
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo("webhook-snapshot").Status)
			continue
		}
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	var fact model.AgencyTopupFact
	require.NoError(t, model.DB.First(&fact).Error)
	assert.Equal(t, "12.34", fact.ActualMoney)
	assert.Equal(t, "USD", fact.CurrencyCode)
	assert.Equal(t, "pi_snapshot", fact.PaymentReference)
	assert.Equal(t, int64(3000), fact.CreditedQuota)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 3000, user.Quota)
}

func TestUSDStripeWebhookRetriesAtomicFunding(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	oldAPI, oldWebhook, oldPrice := setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId
	setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId = "sk_test_snapshot", "whsec_snapshot", "price_snapshot"
	t.Cleanup(func() {
		setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId = oldAPI, oldWebhook, oldPrice
	})
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "false")
	t.Setenv("OPS_BILLING_CURRENCY", "USD")
	t.Setenv("OPS_PUSH_CURRENCY", "USD")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-webhook-test")
	user := setupSnapshotWebhookDB(t, model.PaymentProviderStripe)
	common.QuotaPerUnit = 500_000
	quote, err := common.Marshal(model.StripeCheckoutQuote{Version: 1, SessionID: "cs_snapshot", Currency: "USD", AmountSubtotal: 1234})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", "webhook-snapshot").Update("payment_params", string(quote)).Error)
	router := gin.New()
	router.POST("/stripe", StripeWebhook)
	callback := func(eventType, status, paymentStatus, currency string, amount int, at time.Time, valid bool) int {
		body := fmt.Sprintf(`{"id":"evt_usd_test","object":"event","type":%q,"data":{"object":{"id":"cs_snapshot","client_reference_id":"webhook-snapshot","customer":"cus_snapshot","status":%q,"payment_status":%q,"payment_intent":"pi_snapshot","amount_subtotal":1234,"amount_total":%d,"currency":%q}}}`, eventType, status, paymentStatus, amount, currency)
		stamp := fmt.Sprint(at.Unix())
		signer := hmac.New(sha256.New, []byte(setting.StripeWebhookSecret))
		_, err := signer.Write([]byte(stamp + "." + body))
		require.NoError(t, err)
		signature := hex.EncodeToString(signer.Sum(nil))
		if !valid {
			signature = strings.Repeat("0", len(signature))
		}
		request := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(body))
		request.Header.Set("Stripe-Signature", "t="+stamp+",v1="+signature)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response.Code
	}
	const completed = "checkout.session.completed"
	const succeeded = "checkout.session.async_payment_succeeded"
	assert.Equal(t, http.StatusBadRequest, callback(completed, "complete", "paid", "usd", 1234, time.Now(), false))
	assert.Equal(t, http.StatusBadRequest, callback(completed, "complete", "paid", "usd", 1234, time.Now().Add(-10*time.Minute), true))
	assert.Equal(t, http.StatusOK, callback(completed, "complete", "unpaid", "usd", 1234, time.Now(), true))
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo("webhook-snapshot").Status)
	assert.Equal(t, http.StatusInternalServerError, callback(completed, "complete", "paid", "cny", 1234, time.Now(), true))
	assert.Equal(t, http.StatusInternalServerError, callback(completed, "complete", "paid", "usd", 1200, time.Now(), true))
	assert.Equal(t, http.StatusInternalServerError, callback(succeeded, "complete", "unpaid", "usd", 1234, time.Now(), true))
	assert.Equal(t, http.StatusInternalServerError, callback(succeeded, "complete", "paid", "usd", 1234, time.Now(), true))
	order := model.GetTopUpByTradeNo("webhook-snapshot")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status, "failed funding rolls back the order")
	assert.Empty(t, order.PaymentSnapshot, "failed funding rolls back payment evidence")
	require.NoError(t, model.DB.AutoMigrate(&model.OPSWalletEvent{}))
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	for range 2 {
		assert.Equal(t, http.StatusOK, callback(succeeded, "complete", "paid", "usd", 1234, time.Now(), true))
	}
	assert.Equal(t, http.StatusInternalServerError, callback(completed, "complete", "paid", "cny", 1234, time.Now(), true))
	assert.Equal(t, http.StatusInternalServerError, callback(completed, "complete", "paid", "usd", 9999, time.Now(), true))
	assert.Equal(t, http.StatusOK, callback("checkout.session.expired", "expired", "unpaid", "usd", 1234, time.Now(), true))
	assert.Equal(t, http.StatusOK, callback("checkout.session.async_payment_failed", "complete", "unpaid", "usd", 1234, time.Now(), true))
	order = model.GetTopUpByTradeNo("webhook-snapshot")
	assert.Equal(t, common.TopUpStatusSuccess, order.Status, "late failure/expiry cannot overwrite success")
	rows, err := model.ReadOPSUSDFunding(model.DB, 20)
	require.NoError(t, err)
	require.Len(t, rows, 1, "both paid event types share one persistent business identity")
	var envelope struct {
		Events []struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
			Extra    struct {
				Topup model.OPSUSDFundingFacts `json:"topup"`
			} `json:"extra"`
		} `json:"events"`
	}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Payload, &envelope))
	require.Len(t, envelope.Events, 1)
	assert.Equal(t, "USD", envelope.Events[0].Currency)
	assert.Equal(t, "3", envelope.Events[0].Amount, "credit follows the frozen wallet quote, not payment display units")
	assert.Equal(t, "12.34", envelope.Events[0].Extra.Topup.PaidAmount)
	assert.Equal(t, "USD", envelope.Events[0].Extra.Topup.PaidCurrency)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Zero(t, user.Quota, "funding must not credit both wallets")
}

func TestStripeCheckoutQuoteRequiresExactSessionCurrencyAndAmount(t *testing.T) {
	quote := model.StripeCheckoutQuote{Version: 1, SessionID: "cs_test", Currency: "USD", AmountSubtotal: 1000}
	for _, tc := range []struct {
		name, session, currency string
		subtotal, total         int64
		promotion, invalid      bool
	}{
		{"exact", "cs_test", "usd", 1000, 1000, false, false},
		{"different session", "cs_other", "usd", 1000, 1000, false, true},
		{"different currency", "cs_test", "cny", 1000, 1000, false, true},
		{"different quote", "cs_test", "usd", 900, 900, false, true},
		{"unexpected discount", "cs_test", "usd", 1000, 900, false, true},
		{"frozen promotion", "cs_test", "usd", 1000, 900, true, false},
		{"negative", "cs_test", "usd", 1000, -1, true, true},
		{"overcharge", "cs_test", "usd", 1000, 1001, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quote.AllowPromotionCodes = tc.promotion
			err := quote.ValidatePayment(tc.session, tc.currency, tc.subtotal, tc.total)
			if tc.invalid {
				require.ErrorIs(t, err, model.ErrTopUpPaymentAmountMismatch)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVerifiedCreemWebhookSnapshotsAmountPaidAndRejectsConflictingReplay(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	oldAPI, oldWebhook, oldProducts := setting.CreemApiKey, setting.CreemWebhookSecret, setting.CreemProducts
	setting.CreemApiKey, setting.CreemWebhookSecret, setting.CreemProducts = "creem_snapshot", "creem_webhook_snapshot", `[{"productId":"prod_snapshot"}]`
	t.Cleanup(func() {
		setting.CreemApiKey, setting.CreemWebhookSecret, setting.CreemProducts = oldAPI, oldWebhook, oldProducts
	})
	user := setupSnapshotWebhookDB(t, model.PaymentProviderCreem)
	router := gin.New()
	router.POST("/creem", CreemWebhook)
	for i, amount := range []int{1234, 1234, 9999} {
		body := fmt.Sprintf(`{"id":"evt_creem_snapshot","eventType":"checkout.completed","object":{"request_id":"webhook-snapshot","order":{"id":"ord_snapshot","transaction":"tx_snapshot","status":"paid","type":"onetime","amount_paid":%d,"currency":"USD"},"customer":{}}}`, amount)
		request := httptest.NewRequest(http.MethodPost, "/creem", strings.NewReader(body))
		request.Header.Set(CreemSignatureHeader, generateCreemSignature(body, setting.CreemWebhookSecret))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if i == 2 {
			assert.Equal(t, http.StatusInternalServerError, response.Code)
			continue
		}
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	var facts []model.AgencyTopupFact
	require.NoError(t, model.DB.Find(&facts).Error)
	require.Len(t, facts, 1)
	assert.Equal(t, "12.34", facts[0].ActualMoney)
	assert.Equal(t, "USD", facts[0].CurrencyCode)
	assert.Equal(t, "tx_snapshot", facts[0].PaymentReference)
	assert.Equal(t, int64(200), facts[0].CreditedQuota)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 200, user.Quota)
}

func TestVerifiedEpayWebhookStoresSignedCNYMoneyAndProviderTradeNumber(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	oldAddress, oldID, oldKey, oldMethods := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods
	operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = "https://pay.example.test", "123", "epay_snapshot_key"
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	t.Cleanup(func() {
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods = oldAddress, oldID, oldKey, oldMethods
	})
	user := setupSnapshotWebhookDB(t, model.PaymentProviderEpay)
	router := gin.New()
	router.POST("/epay", EpayNotify)
	for i, money := range []string{"12.34", "12.34", "99.00"} {
		params := epay.GenerateParams(map[string]string{"pid": "123", "out_trade_no": "webhook-snapshot", "trade_no": "epay_provider_trade", "type": "alipay", "money": money, "name": "wallet", "trade_status": "TRADE_SUCCESS"}, operation_setting.EpayKey)
		form := url.Values{}
		for key, value := range params {
			form.Set(key, value)
		}
		request := httptest.NewRequest(http.MethodPost, "/epay", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if i == 2 {
			assert.Equal(t, "fail", response.Body.String())
			continue
		}
		assert.Equal(t, "success", response.Body.String())
	}
	var facts []model.AgencyTopupFact
	require.NoError(t, model.DB.Find(&facts).Error)
	require.Len(t, facts, 1)
	assert.Equal(t, "12.34", facts[0].ActualMoney)
	assert.Equal(t, "CNY", facts[0].CurrencyCode)
	assert.Equal(t, "epay_provider_trade", facts[0].PaymentReference)
	assert.Equal(t, int64(200000), facts[0].CreditedQuota)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 200000, user.Quota)
}
