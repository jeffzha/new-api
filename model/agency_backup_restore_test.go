package model

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The external-database runner invokes seed, performs a real pg_dump/pg_restore
// into a different empty database, then invokes verify in another process.
// This is intentionally not an in-memory copy or a transaction rollback test.
func TestAgencyPostgresBackupRestorePaymentReceipt(t *testing.T) {
	phase := os.Getenv("AGENCY_HUB_BACKUP_PHASE")
	if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || (phase != "seed" && phase != "verify") {
		t.Skip("run with the isolated PostgreSQL backup/restore runner")
	}
	db := agencyDialectDB(t, "postgres")
	require.NotNil(t, db)
	pool, err := db.DB()
	require.NoError(t, err)
	previousDB, previousLogDB, previousKey := DB, LOG_DB, commonKeyCol
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousQuota, previousRedis := common.QuotaPerUnit, common.RedisEnabled
	DB, LOG_DB, commonKeyCol = db, db, `"key"`
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	common.QuotaPerUnit, common.RedisEnabled = 1000, false
	t.Cleanup(func() {
		DB, LOG_DB, commonKeyCol = previousDB, previousLogDB, previousKey
		common.SetDatabaseTypes(previousMain, previousLog)
		common.QuotaPerUnit, common.RedisEnabled = previousQuota, previousRedis
		require.NoError(t, pool.Close())
	})
	const userID = 997001
	const trade = "backup-verified-topup"
	payment := &TopupPaymentSnapshot{ActualMoney: "12.34", CurrencyCode: "CNY", PaymentReference: "backup-provider-receipt"}
	if phase == "seed" {
		require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &TopUp{}, &Task{}, &Log{}))
		require.NoError(t, MigrateAgency(db))
		user := User{Id: userID, Username: "backup-durable-customer", AffCode: "backup-durable", BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Status: common.UserStatusEnabled}
		require.NoError(t, db.Create(&user).Error)
		require.NoError(t, db.Create(&AgencyFundingAccount{UserID: userID, Version: 1}).Error)
		require.NoError(t, db.Create(&Token{UserId: userID, Key: "synthetic-backup-token", Status: common.TokenStatusEnabled, RemainQuota: 500}).Error)
		order := TopUp{UserId: userID, Amount: 2, Money: 99, TradeNo: trade, PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending}
		require.NoError(t, order.Insert())
		_, err = RechargeEpay(trade, "alipay", "127.0.0.1", payment)
		require.NoError(t, err)
		require.NoError(t, db.Create(&AgencyWithdrawal{RequestNo: "backup-paid-withdrawal", AgencyID: 997001, CurrencyCode: "CNY", AmountMicros: 12340000, Status: "paid", Version: 5, PaymentChannel: "synthetic-bank", PaymentReference: "backup-bank-confirmation", AccountSnapshot: "synthetic-encrypted-account", AccountSnapshotHash: "synthetic-account-hash", AccountSnapshotKeyID: "fixture-key-version", CreatedAtMS: 1, UpdatedAtMS: 5}).Error)
		require.NoError(t, db.Create(&AgencyCommissionBalance{AgencyID: 997001, CurrencyCode: "CNY", EarnedMicros: 12340000, PaidMicros: 12340000, Version: 5}).Error)
		return
	}

	// No migration or seeding is allowed in verification: every asserted row
	// must come from the restored archive, including immutable payment evidence.
	var user User
	require.NoError(t, db.First(&user, userID).Error)
	assert.Equal(t, AgencyDurableBillingMode, user.BillingMode)
	assert.Equal(t, 2000, user.Quota)
	var token Token
	require.NoError(t, db.Where("user_id = ?", userID).First(&token).Error)
	assert.Equal(t, "synthetic-backup-token", token.Key)
	assert.Equal(t, 500, token.RemainQuota)
	var before AgencyFundingAccount
	require.NoError(t, db.First(&before, userID).Error)
	var fact AgencyTopupFact
	require.NoError(t, db.Where("source_operation_id = ?", trade).First(&fact).Error)
	assert.Equal(t, "12.34", fact.ActualMoney)
	assert.Equal(t, "CNY", fact.CurrencyCode)
	assert.Equal(t, payment.PaymentReference, fact.PaymentReference)
	assert.Equal(t, int64(2000), fact.CreditedQuota)
	var conversion TopupQuotaConversion
	require.NoError(t, common.UnmarshalJsonStr(fact.QuotaConversionSnapshot, &conversion))
	assert.Equal(t, "2000", conversion.CreditedQuota)
	var outbox AgencyBillingOutbox
	require.NoError(t, db.Where("operation_id = ?", trade).First(&outbox).Error)
	var delivery AgencyEventDelivery
	require.NoError(t, db.Where("event_id = ?", outbox.EventID).First(&delivery).Error)
	assert.Equal(t, "pending", delivery.Status)

	// A callback redelivered after restoration must consume the old receipt,
	// even if today's quota conversion configuration differs from checkout.
	common.QuotaPerUnit = 2000
	_, err = RechargeEpay(trade, "alipay", "127.0.0.1", payment)
	require.NoError(t, err)
	changed := *payment
	changed.ActualMoney = "99"
	_, err = RechargeEpay(trade, "alipay", "127.0.0.1", &changed)
	require.ErrorIs(t, err, ErrAgencyTopupConflict)
	require.NoError(t, db.First(&user, userID).Error)
	assert.Equal(t, 2000, user.Quota)
	var after AgencyFundingAccount
	require.NoError(t, db.First(&after, userID).Error)
	assert.Equal(t, before, after)
	var count int64
	require.NoError(t, db.Model(&AgencyFundingLedger{}).Where("operation_id = ?", trade).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("operation_id = ?", trade).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var withdrawal AgencyWithdrawal
	require.NoError(t, db.Where("request_no = ?", "backup-paid-withdrawal").First(&withdrawal).Error)
	assert.Equal(t, "paid", withdrawal.Status)
	assert.Equal(t, "backup-bank-confirmation", withdrawal.PaymentReference)
	assert.Equal(t, "fixture-key-version", withdrawal.AccountSnapshotKeyID)
	var commission AgencyCommissionBalance
	require.NoError(t, db.Where("agency_id = ?", 997001).First(&commission).Error)
	assert.Equal(t, int64(12340000), commission.PaidMicros)
	assert.Zero(t, commission.LockedMicros)
}
