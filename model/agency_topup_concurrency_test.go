package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgencyTopupConcurrentCreditsAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			if dialect == "sqlite" {
				pool.SetMaxOpenConns(1)
			} else {
				pool.SetMaxOpenConns(4)
			}
			previousDB, previousLogDB := DB, LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			previousQuota, previousRedis := common.QuotaPerUnit, common.RedisEnabled
			DB, LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			common.QuotaPerUnit, common.RedisEnabled = 1000, false
			t.Cleanup(func() {
				DB, LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
				common.QuotaPerUnit, common.RedisEnabled = previousQuota, previousRedis
				require.NoError(t, pool.Close())
			})
			require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}, &Log{}))
			require.NoError(t, MigrateAgency(db))
			prefix := "credit-race-" + common.GetUUID()[:8]
			user := User{Username: prefix, AffCode: prefix, Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			orders := []TopUp{
				{UserId: user.Id, Amount: 1, Money: 1, TradeNo: prefix + "-1", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending},
				{UserId: user.Id, Amount: 1, Money: 1, TradeNo: prefix + "-2", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending},
			}
			require.NoError(t, db.Create(&orders).Error)
			start, results := make(chan struct{}), make(chan error, 2)
			for _, order := range orders {
				go func(order TopUp) {
					<-start
					_, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "CNY", PaymentReference: order.TradeNo + "-receipt"})
					results <- err
				}(order)
			}
			close(start)
			first, second := <-results, <-results
			require.NoError(t, first)
			require.NoError(t, second)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 2000, user.Quota)
			var account AgencyFundingAccount
			require.NoError(t, db.First(&account, user.Id).Error)
			assert.Equal(t, int64(2000), account.PaidAvailable)
			assert.Equal(t, int64(2), account.MoneySeq)
			var lots []AgencyFundingLot
			require.NoError(t, db.Where("user_id = ?", user.Id).Find(&lots).Error)
			require.Len(t, lots, 2, "each payment creates one lot; no duplicated opening lot")
			for _, lot := range lots {
				assert.Equal(t, int64(1000), lot.PaidInitial)
				assert.Equal(t, int64(1000), lot.PaidAvailable)
			}
			var count int64
			require.NoError(t, db.Model(&AgencyTopupFact{}).Where("user_id = ?", user.Id).Count(&count).Error)
			assert.Equal(t, int64(2), count)
		})
	}
}
