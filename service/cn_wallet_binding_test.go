package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCNBoundWalletBillingSessionAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
	oldRedis, oldBatch, oldUnit := common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit
	oldSettings := *operation_setting.GetGeneralSetting()
	common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = false, false, 100
	operation_setting.GetGeneralSetting().QuotaDisplayType = "USD"
	t.Cleanup(func() {
		common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = oldRedis, oldBatch, oldUnit
		*operation_setting.GetGeneralSetting() = oldSettings
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			if dialect == "sqlite" {
				driver = sqlite.Open(filepath.Join(t.TempDir(), "bound-service.db"))
			} else {
				dsn := os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("isolated external DB not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}))
			require.NoError(t, model.MigrateCNFinance(db, "test-cn"))
			oldDB, oldLog := model.DB, model.LOG_DB
			model.DB, model.LOG_DB = db, db
			t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLog })
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			fixture := func() (*relaycommon.RelayInfo, model.Token) {
				t.Helper()
				id := "w" + common.GetUUID()[:15]
				u := model.User{Username: id, AffCode: id, Status: common.UserStatusEnabled, Quota: 10000}
				require.NoError(t, db.Create(&u).Error)
				token := model.Token{UserId: u.Id, Key: id, Status: common.TokenStatusEnabled, RemainQuota: 10000}
				require.NoError(t, db.Create(&token).Error)
				info := &relaycommon.RelayInfo{RequestId: id, UserId: u.Id, TokenId: token.Id, TokenKey: id, ForcePreConsume: true}
				info.UserSetting.BillingPreference = "wallet_only"
				return info, token
			}
			check := func(info *relaycommon.RelayInfo, wallet, token int) {
				t.Helper()
				var u model.User
				var credential model.Token
				require.NoError(t, db.First(&u, info.UserId).Error)
				require.NoError(t, db.First(&credential, info.TokenId).Error)
				require.Equal(t, 10000-wallet, u.Quota)
				require.Equal(t, 10000-token, credential.RemainQuota)
				require.Equal(t, token, credential.UsedQuota)
			}
			info, token := fixture()
			require.Nil(t, PreConsumeBilling(ctx, 100, info))
			session := info.Billing.(*BillingSession)
			require.NoError(t, session.Reserve(150))
			require.NoError(t, session.Reserve(150))
			check(info, 150, 150)
			restored := *info
			restored.Billing = nil
			require.Nil(t, PreConsumeBilling(ctx, 100, &restored))
			require.Equal(t, 150, restored.Billing.GetPreConsumedQuota())
			// A failed token write cannot leave a wallet debit or final CN fact.
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("bound-service-token-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "tokens" {
					tx.AddError(errors.New("injected token delta failure"))
				}
			}))
			require.Error(t, restored.Billing.Settle(180))
			check(info, 150, 150)
			require.NoError(t, db.Callback().Update().Remove("bound-service-token-fail"))
			require.NoError(t, db.First(&token, token.Id).Error)
			require.NoError(t, db.Unscoped().Delete(&model.Token{}, token.Id).Error)
			require.Error(t, restored.Billing.Settle(180))
			var receipt model.CNWalletLifecycle
			require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&receipt).Error)
			require.Equal(t, "reserved", receipt.State)
			var facts int64
			require.NoError(t, db.Model(&model.CNFinanceOutbox{}).Where("chain_key = ?", model.CNIdentity("charge", "request:"+info.RequestId, "0")).Count(&facts).Error)
			require.Zero(t, facts)
			require.NoError(t, db.Create(&token).Error)
			check(info, 150, 150)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("bound-service-outbox-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected outbox failure"))
				}
			}))
			require.Error(t, restored.Billing.Settle(180))
			check(info, 150, 150)
			require.NoError(t, db.Callback().Create().Remove("bound-service-outbox-fail"))
			require.NoError(t, restored.Billing.Settle(180))
			require.NoError(t, session.Settle(180))
			require.ErrorIs(t, session.Settle(181), model.ErrCNConflict)
			session.Refund(ctx)
			check(info, 180, 180)

			trusted, _ := fixture()
			trusted.ForcePreConsume = false
			ctx.Set("token_quota", 10000)
			require.Nil(t, PreConsumeBilling(ctx, 100, trusted))
			require.Zero(t, trusted.Billing.GetPreConsumedQuota())
			var binding model.CNWalletBinding
			require.NoError(t, db.Where("request_id = ?", trusted.RequestId).First(&binding).Error)
			require.Zero(t, binding.InitialReserve)
			require.NoError(t, trusted.Billing.Settle(60))
			check(trusted, 60, 60)

			play, _ := fixture()
			play.IsPlayground, play.TokenId = true, 0
			require.Nil(t, PreConsumeBilling(ctx, 30, play))
			require.NoError(t, play.Billing.Reserve(50))
			require.NoError(t, play.Billing.Settle(20))
			require.NoError(t, db.Where("request_id = ?", play.RequestId).First(&model.CNWalletBinding{}).Error)
			unlimited, unlimitedToken := fixture()
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", unlimitedToken.Id).Updates(map[string]any{"unlimited_quota": true, "remain_quota": 0}).Error)
			unlimited.TokenUnlimited = true
			require.Nil(t, PreConsumeBilling(ctx, 10, unlimited))
			require.NoError(t, unlimited.Billing.Settle(20))
			require.NoError(t, db.First(&unlimitedToken, unlimitedToken.Id).Error)
			require.Equal(t, -20, unlimitedToken.RemainQuota)
			require.Equal(t, 20, unlimitedToken.UsedQuota)

			cancel, _ := fixture()
			require.Nil(t, PreConsumeBilling(ctx, 40, cancel))
			require.NoError(t, cancel.Billing.Reserve(80))
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("bound-service-cancel-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected cancellation failure"))
				}
			}))
			cancel.Billing.Refund(ctx)
			cancel.Billing.Refund(ctx)
			require.Eventually(t, func() bool {
				var intent model.CNWalletCancellation
				return db.Where("request_id = ?", cancel.RequestId).First(&intent).Error == nil && intent.Attempts > 0
			}, 5*time.Second, 10*time.Millisecond)
			check(cancel, 80, 80)
			require.NoError(t, db.Callback().Create().Remove("bound-service-cancel-fail"))
			require.NoError(t, model.CompleteCNWalletCancellation(context.Background(), db.Session(&gorm.Session{NewDB: true}), cancel.RequestId))
			require.NoError(t, model.CompleteCNWalletCancellation(context.Background(), db, cancel.RequestId))
			check(cancel, 0, 0)

			bad, _ := fixture()
			bad.TokenId = 0
			require.NotNil(t, PreConsumeBilling(ctx, 10, bad))
			missing, _ := fixture()
			missing.RequestId = ""
			require.NotNil(t, PreConsumeBilling(ctx, 10, missing))
			check(missing, 0, 0)
			for _, mode := range []string{"redis", "batch", "capture-off"} {
				guard, _ := fixture()
				common.RedisEnabled = mode == "redis"
				common.BatchUpdateEnabled = mode == "batch"
				if mode == "capture-off" {
					t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "false")
				}
				require.NotNil(t, PreConsumeBilling(ctx, 10, guard))
				check(guard, 0, 0)
				common.RedisEnabled, common.BatchUpdateEnabled = false, false
				t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
			}
			legacy, _ := fixture()
			legacy.RequestId = ""
			t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "false")
			require.Nil(t, PreConsumeBilling(ctx, 10, legacy))
			require.NoError(t, legacy.Billing.Settle(20))
			check(legacy, 20, 20)
			t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
		})
	}
}
