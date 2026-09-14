package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPaymentSnapshotDB(t *testing.T, billingMode string) *User {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}, &Log{}))
	require.NoError(t, MigrateAgency(db))
	previousDB, previousLogDB, previousQuota := DB, LOG_DB, common.QuotaPerUnit
	previousRedis, previousBatch := common.RedisEnabled, common.BatchUpdateEnabled
	DB, LOG_DB, common.QuotaPerUnit = db, db, 1000
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	t.Cleanup(func() {
		DB, LOG_DB, common.QuotaPerUnit = previousDB, previousLogDB, previousQuota
		common.RedisEnabled, common.BatchUpdateEnabled = previousRedis, previousBatch
		_ = sqlDB.Close()
	})
	user := &User{Username: "snapshot-user", Status: common.UserStatusEnabled, BillingMode: billingMode, FundingVersion: 1}
	require.NoError(t, db.Create(user).Error)
	return user
}

func completePaymentSnapshotOrder(provider, trade string, payment *TopupPaymentSnapshot) error {
	switch provider {
	case PaymentProviderEpay:
		_, err := RechargeEpay(trade, "alipay", "127.0.0.1", payment)
		return err
	case PaymentProviderStripe:
		return Recharge(trade, "customer-test", "127.0.0.1", payment)
	case PaymentProviderCreem:
		return RechargeCreem(trade, "", "", "127.0.0.1", payment)
	case PaymentProviderWaffo:
		return RechargeWaffo(trade, "127.0.0.1", payment)
	case PaymentProviderWaffoPancake:
		return RechargeWaffoPancake(trade, payment)
	default:
		return fmt.Errorf("unexpected test provider %s", provider)
	}
}

func TestProviderTopupSnapshotsPreserveActualPaymentAndDistinctQuotaUnits(t *testing.T) {
	for _, billingMode := range []string{"", AgencyDurableBillingMode} {
		for _, provider := range []struct {
			name                                  string
			quota                                 int
			basis, value, calculation, multiplier string
		}{
			{PaymentProviderEpay, 2000, "topup.amount", "2", "multiply_quota_per_unit", "1000"},
			{PaymentProviderStripe, 3000, "topup.money", "3", "multiply_quota_per_unit", "1000"},
			{PaymentProviderCreem, 2, "topup.amount", "2", "identity", ""},
			{PaymentProviderWaffo, 2000, "topup.amount", "2", "multiply_quota_per_unit", "1000"},
			{PaymentProviderWaffoPancake, 2000, "topup.amount", "2", "multiply_quota_per_unit", "1000"},
		} {
			t.Run(billingMode+"/"+provider.name, func(t *testing.T) {
				user := setupPaymentSnapshotDB(t, billingMode)
				order := TopUp{UserId: user.Id, Amount: 2, Money: 3, TradeNo: "snapshot-order", PaymentProvider: provider.name, Status: common.TopUpStatusPending}
				require.NoError(t, order.Insert())
				payment := &TopupPaymentSnapshot{ActualMoney: "12.3400", CurrencyCode: " usd ", PaymentReference: "provider-payment-123"}
				require.NoError(t, completePaymentSnapshotOrder(provider.name, order.TradeNo, payment))
				require.NoError(t, DB.First(user, user.Id).Error)
				assert.Equal(t, provider.quota, user.Quota, "actual payment amount must never replace the provider's credited-quota calculation")
				completed := GetTopUpByTradeNo(order.TradeNo)
				require.NotNil(t, completed)
				var storedPayment TopupPaymentSnapshot
				require.NoError(t, common.UnmarshalJsonStr(completed.PaymentSnapshot, &storedPayment))
				assert.Equal(t, TopupPaymentSnapshot{ActualMoney: "12.34", CurrencyCode: "USD", PaymentReference: "provider-payment-123"}, storedPayment)
				var conversion TopupQuotaConversion
				require.NoError(t, common.UnmarshalJsonStr(completed.QuotaConversionSnapshot, &conversion))
				assert.Equal(t, TopupQuotaConversion{SchemaVersion: 1, BasisField: provider.basis, BasisValue: provider.value, Calculation: provider.calculation, QuotaPerUnit: provider.multiplier, CreditedQuota: fmt.Sprint(provider.quota)}, conversion)
				common.QuotaPerUnit = 2000
				require.NoError(t, completePaymentSnapshotOrder(provider.name, order.TradeNo, payment))
				changed := *payment
				changed.ActualMoney = "99"
				require.Error(t, completePaymentSnapshotOrder(provider.name, order.TradeNo, &changed))
				changed = *payment
				changed.CurrencyCode = "EUR"
				require.Error(t, completePaymentSnapshotOrder(provider.name, order.TradeNo, &changed))
				changed = *payment
				changed.PaymentReference = "other-payment"
				require.Error(t, completePaymentSnapshotOrder(provider.name, order.TradeNo, &changed))
				require.NoError(t, DB.First(user, user.Id).Error)
				assert.Equal(t, provider.quota, user.Quota)
				assert.Equal(t, completed.QuotaConversionSnapshot, GetTopUpByTradeNo(order.TradeNo).QuotaConversionSnapshot)
				var facts []AgencyTopupFact
				require.NoError(t, DB.Find(&facts).Error)
				if billingMode == "" {
					assert.Empty(t, facts)
					return
				}
				require.Len(t, facts, 1)
				assert.Equal(t, int64(provider.quota), facts[0].CreditedQuota)
				assert.Equal(t, int64(provider.quota), facts[0].PaidQuota)
				assert.Equal(t, "payment_self", facts[0].FundingSource)
				assert.Equal(t, "12.34", facts[0].ActualMoney)
				assert.Equal(t, "USD", facts[0].CurrencyCode)
				assert.Equal(t, "provider-payment-123", facts[0].PaymentReference)
				assert.Equal(t, completed.QuotaConversionSnapshot, facts[0].QuotaConversionSnapshot)
				var balance AgencyFundingAccount
				require.NoError(t, DB.Where("user_id = ?", user.Id).First(&balance).Error)
				assert.Equal(t, int64(provider.quota), balance.PaidAvailable, "the opening balance must not include this top-up a second time")
				var ledger []AgencyFundingLedger
				require.NoError(t, DB.Where("operation_id = ?", order.TradeNo).Find(&ledger).Error)
				require.Len(t, ledger, 1)
				assert.Equal(t, "USD", ledger[0].CurrencyCode)
				var deliveries int64
				require.NoError(t, DB.Model(&AgencyEventDelivery{}).Count(&deliveries).Error)
				assert.Equal(t, int64(1), deliveries)
			})
		}
	}
}

func TestAssistedTopupUsesImmutableQuotaOverrideAndActor(t *testing.T) {
	user := setupPaymentSnapshotDB(t, AgencyDurableBillingMode)
	order := TopUp{UserId: user.Id, InitiatedByUserId: 99, FundingSource: "payment_assisted", CreditedQuota: 1234, Amount: 2, Money: 1, MoneyDecimal: "1.00", TradeNo: "assisted-override", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending}
	require.NoError(t, order.Insert())
	_, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "1.00", CurrencyCode: "CNY", PaymentReference: "assisted-receipt"})
	require.NoError(t, err)
	require.NoError(t, DB.First(user, user.Id).Error)
	assert.Equal(t, 1234, user.Quota)
	var fact AgencyTopupFact
	require.NoError(t, DB.First(&fact).Error)
	assert.Equal(t, "payment_assisted", fact.FundingSource)
	assert.Equal(t, int64(99), fact.InitiatedByUserID)
	assert.Equal(t, int64(1234), fact.CreditedQuota)
	var conversion TopupQuotaConversion
	require.NoError(t, common.UnmarshalJsonStr(fact.QuotaConversionSnapshot, &conversion))
	assert.Equal(t, "assisted_exact_quote", conversion.Calculation)
}

func TestAssistedTopupRejectsCallbackAmountMismatch(t *testing.T) {
	user := setupPaymentSnapshotDB(t, AgencyDurableBillingMode)
	order := TopUp{UserId: user.Id, InitiatedByUserId: 99, FundingSource: "payment_assisted", CreditedQuota: 1234, Amount: 2, Money: 1, MoneyDecimal: "1.00", TradeNo: "assisted-mismatch", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending}
	require.NoError(t, order.Insert())
	_, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "0.99", CurrencyCode: "CNY", PaymentReference: "bad-assist-receipt"})
	require.ErrorIs(t, err, ErrTopUpPaymentAmountMismatch)
	assert.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(order.TradeNo).Status)
}

func TestTopupWithoutPaymentEvidenceKeepsUnknownMoneyAndManualCompletionNonpaid(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint("manual=", manual), func(t *testing.T) {
			user := setupPaymentSnapshotDB(t, AgencyDurableBillingMode)
			order := TopUp{UserId: user.Id, Amount: 2, Money: 3, TradeNo: "no-payment-evidence", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending}
			require.NoError(t, order.Insert())
			if manual {
				require.NoError(t, ManualCompleteTopUp(order.TradeNo, "127.0.0.1"))
			} else {
				_, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1")
				require.NoError(t, err)
			}
			var fact AgencyTopupFact
			require.NoError(t, DB.First(&fact).Error)
			assert.Empty(t, fact.ActualMoney)
			assert.Empty(t, fact.CurrencyCode)
			assert.Empty(t, fact.PaymentReference)
			if manual {
				assert.Zero(t, fact.PaidQuota)
				assert.Equal(t, int64(2000), fact.BonusQuota)
				assert.Equal(t, "admin_adjustment", fact.CompletionSource)
			}
			payment := &TopupPaymentSnapshot{ActualMoney: "12.34", CurrencyCode: "CNY", PaymentReference: "unverified-late-evidence"}
			alreadyDone, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", payment)
			require.NoError(t, err)
			assert.True(t, alreadyDone)
			assert.Empty(t, GetTopUpByTradeNo(order.TradeNo).PaymentSnapshot)
			require.NoError(t, DB.First(&fact, fact.ID).Error)
			assert.Empty(t, fact.ActualMoney)
		})
	}
}

func TestInvalidTopupPaymentSnapshotRollsBackOrderAndWallet(t *testing.T) {
	for _, payment := range []TopupPaymentSnapshot{{ActualMoney: "1e999999", CurrencyCode: "USD", PaymentReference: "ref"}, {ActualMoney: "-1", CurrencyCode: "USD", PaymentReference: "ref"}, {ActualMoney: "1", CurrencyCode: "", PaymentReference: "ref"}, {ActualMoney: "1", CurrencyCode: "USD", PaymentReference: ""}} {
		t.Run(payment.ActualMoney+"/"+payment.CurrencyCode+"/"+payment.PaymentReference, func(t *testing.T) {
			user := setupPaymentSnapshotDB(t, AgencyDurableBillingMode)
			order := TopUp{UserId: user.Id, Amount: 2, Money: 3, TradeNo: "invalid-snapshot", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending}
			require.NoError(t, order.Insert())
			_, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", &payment)
			require.ErrorIs(t, err, ErrTopupPaymentSnapshot)
			assert.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(order.TradeNo).Status)
			require.NoError(t, DB.First(user, user.Id).Error)
			assert.Zero(t, user.Quota)
			var count int64
			require.NoError(t, DB.Model(&AgencyTopupFact{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestVerifiedFreeCheckoutCreatesOnlyBonusFunding(t *testing.T) {
	user := setupPaymentSnapshotDB(t, AgencyDurableBillingMode)
	order := TopUp{UserId: user.Id, Amount: 2000, Money: 3, TradeNo: "free-checkout", PaymentProvider: PaymentProviderCreem, Status: common.TopUpStatusPending}
	require.NoError(t, order.Insert())
	require.NoError(t, RechargeCreem(order.TradeNo, "", "", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "0", CurrencyCode: "USD", PaymentReference: "free-order"}))
	var fact AgencyTopupFact
	require.NoError(t, DB.First(&fact).Error)
	assert.Equal(t, "0", fact.ActualMoney)
	assert.Zero(t, fact.PaidQuota)
	assert.Equal(t, int64(2000), fact.BonusQuota)
	var balance AgencyFundingAccount
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&balance).Error)
	assert.Zero(t, balance.PaidAvailable)
	assert.Equal(t, int64(2000), balance.NonpaidAvailable)
	require.NoError(t, DB.First(user, user.Id).Error)
	assert.Equal(t, 2000, user.Quota)
}

func TestProviderReferenceCanBeRecordedWithoutInventingActualPaidMoney(t *testing.T) {
	for _, provider := range []string{PaymentProviderWaffo, PaymentProviderWaffoPancake} {
		t.Run(provider, func(t *testing.T) {
			user := setupPaymentSnapshotDB(t, AgencyDurableBillingMode)
			order := TopUp{UserId: user.Id, Amount: 2, Money: 3, TradeNo: "reference-only", PaymentProvider: provider, Status: common.TopUpStatusPending}
			require.NoError(t, order.Insert())
			payment := &TopupPaymentSnapshot{PaymentReference: "provider-known-reference"}
			require.NoError(t, completePaymentSnapshotOrder(provider, order.TradeNo, payment))
			require.NoError(t, completePaymentSnapshotOrder(provider, order.TradeNo, payment))
			var fact AgencyTopupFact
			require.NoError(t, DB.First(&fact).Error)
			assert.Equal(t, "provider-known-reference", fact.PaymentReference)
			assert.Empty(t, fact.ActualMoney)
			assert.Empty(t, fact.CurrencyCode)
			assert.NotEmpty(t, fact.QuotaConversionSnapshot)
			assert.Equal(t, int64(2000), fact.CreditedQuota)
		})
	}
}

func TestFundingReceiptReplayComparesPaymentAndConversionWithoutRepeatingDebtRepayment(t *testing.T) {
	user := setupPaymentSnapshotDB(t, AgencyDurableBillingMode)
	require.NoError(t, DB.Create(&AgencyFundingAccount{UserID: int64(user.Id), DebtQuota: 100, Version: 1}).Error)
	require.NoError(t, DB.Create(&AgencyFundingDebt{UserID: int64(user.Id), OriginOperationID: "earlier-debt", DebtKind: "payment_chargeback", OriginalQuota: 100, OutstandingQuota: 100}).Error)
	snapshot := &TopupFundingSnapshot{Payment: &TopupPaymentSnapshot{ActualMoney: "1.00", CurrencyCode: "USD", PaymentReference: "receipt-1"}, Conversion: &TopupQuotaConversion{SchemaVersion: 1, BasisField: "topup.amount", BasisValue: "200", Calculation: "identity", CreditedQuota: "200"}}
	for i := 0; i < 2; i++ {
		require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
			return RecordAgencyTopup(tx, int64(user.Id), PaymentProviderCreem, "repay-snapshot", "payment_callback", 200, 0, snapshot)
		}))
	}
	snapshot.Payment.PaymentReference = "another-receipt"
	err := DB.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), PaymentProviderCreem, "repay-snapshot", "payment_callback", 200, 0, snapshot)
	})
	require.ErrorIs(t, err, ErrAgencyTopupConflict)
	snapshot.Payment.PaymentReference = "receipt-1"
	snapshot.Conversion.BasisField = "topup.money"
	err = DB.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), PaymentProviderCreem, "repay-snapshot", "payment_callback", 200, 0, snapshot)
	})
	require.ErrorIs(t, err, ErrAgencyTopupConflict)
	var balance AgencyFundingAccount
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&balance).Error)
	assert.Zero(t, balance.DebtQuota)
	assert.Equal(t, int64(100), balance.PaidAvailable)
	var repayments []AgencyDebtRepayment
	require.NoError(t, DB.Find(&repayments).Error)
	require.Len(t, repayments, 1)
	assert.Equal(t, int64(100), repayments[0].Quota)
}
