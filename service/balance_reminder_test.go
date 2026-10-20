package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestBalanceReminderTiersCollapseAndRearm(t *testing.T) {
	thresholds := []int{0, 100, 200, 500}
	sent, tier, notify := balanceReminderTransition(80, thresholds, nil)
	require.True(t, notify)
	assert.Equal(t, 100, tier)
	assert.Len(t, sent, 3)
	_, _, notify = balanceReminderTransition(50, thresholds, sent)
	assert.False(t, notify)
	sent, _, notify = balanceReminderTransition(300, thresholds, sent)
	assert.False(t, notify)
	assert.Equal(t, map[string]bool{"500": true}, sent)
	sent, tier, notify = balanceReminderTransition(90, thresholds, sent)
	assert.True(t, notify)
	assert.Equal(t, 100, tier)
	sent, tier, notify = balanceReminderTransition(0, thresholds, sent)
	assert.True(t, notify)
	assert.Zero(t, tier)
	_, _, notify = balanceReminderTransition(-10, thresholds, sent)
	assert.False(t, notify)
}

func TestBalanceReminderThresholdsAreCNYNotRawQuota(t *testing.T) {
	beforeRate, beforeUnit := operation_setting.USDExchangeRate, common.QuotaPerUnit
	operation_setting.USDExchangeRate, common.QuotaPerUnit = 7.3, 500000
	t.Cleanup(func() { operation_setting.USDExchangeRate, common.QuotaPerUnit = beforeRate, beforeUnit })
	assert.Equal(t, []int{0, 68493, 136986, 342466}, balanceReminderThresholds(dto.UserSetting{QuotaWarningThreshold: 1000}))
	assert.Equal(t, []int{0, 68493, 136986, 342466, 500000}, balanceReminderThresholds(dto.UserSetting{QuotaWarningThreshold: 500000}))
}

func TestBalanceReminderDeliveryRetriesAndDeduplicatesAcrossWorkers(t *testing.T) {
	truncate(t)
	seedUser(t, 71, 1)
	user := model.User{Id: 71, Email: "notify@example.com"}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, client
	t.Cleanup(func() { common.RedisEnabled, common.RDB = previousEnabled, previousClient; _ = client.Close() })
	ctx := context.Background()
	// A failed provider acceptance does not mark the tier as delivered.
	require.Error(t, checkBalanceReminder(ctx, user, func(int, string, dto.UserSetting, dto.Notify) error {
		return errors.New("mail temporarily unavailable")
	}))
	assert.False(t, server.Exists("balance_reminder:v1:71"))
	deliveries := 0
	send := func(id int, email string, settings dto.UserSetting, notification dto.Notify) error {
		deliveries++
		assert.Equal(t, 71, id)
		assert.Equal(t, "notify@example.com", email)
		assert.Contains(t, notification.Content, "当前钱包余额")
		return nil
	}
	require.NoError(t, checkBalanceReminder(ctx, user, send))
	require.NoError(t, checkBalanceReminder(ctx, user, send))
	assert.Equal(t, 1, deliveries)
	// A concurrent instance owning the lease must not send.
	server.Set("balance_reminder:v1:71:lock", "other-worker")
	require.NoError(t, checkBalanceReminder(ctx, user, send))
	assert.Equal(t, 1, deliveries)
	server.Del("balance_reminder:v1:71:lock")
	// An account with no destination must not consume its reminder state.
	require.NoError(t, checkBalanceReminder(ctx, model.User{Id: 72}, send))
	assert.False(t, server.Exists("balance_reminder:v1:72"))
}

func TestBalanceReminderDatabaseMatrix(t *testing.T) {
	for _, tc := range []struct{ name, env string }{
		{"sqlite", ""}, {"mysql", "TEST_REMINDER_MYSQL_DSN"}, {"postgres", "TEST_REMINDER_POSTGRES_DSN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := model.DB
			if tc.env != "" {
				dsn := os.Getenv(tc.env)
				if dsn == "" {
					t.Skip(tc.env + " not configured")
				}
				var driver gorm.Dialector = mysql.Open(dsn)
				if tc.name == "postgres" {
					driver = postgres.Open(dsn)
				}
				var err error
				db, err = gorm.Open(driver, &gorm.Config{})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { _ = sqlDB.Close() })
			}
			previousDB, previousRedis := model.DB, common.RedisEnabled
			model.DB, common.RedisEnabled = db, false
			t.Cleanup(func() { model.DB, common.RedisEnabled = previousDB, previousRedis })
			require.NoError(t, db.AutoMigrate(&model.User{}))
			require.NoError(t, db.AutoMigrate(&model.User{}))
			require.NoError(t, db.Unscoped().Where("id IN ?", []int{8071, 8072, 8073, 8074}).Delete(&model.User{}).Error)
			t.Cleanup(func() { db.Unscoped().Where("id IN ?", []int{8071, 8072, 8073, 8074}).Delete(&model.User{}) })
			for _, user := range []model.User{
				{Id: 8071, Username: "reminder-active", AffCode: "reminder-active", Status: common.UserStatusEnabled, UsedQuota: 1, Email: "active@example.com", Quota: 1000000},
				{Id: 8072, Username: "reminder-idle", AffCode: "reminder-idle", Status: common.UserStatusEnabled, Email: "idle@example.com"},
				{Id: 8073, Username: "reminder-disabled", AffCode: "reminder-disabled", Status: common.UserStatusDisabled, UsedQuota: 1, Email: "disabled@example.com"},
				{Id: 8074, Username: "reminder-deleted", AffCode: "reminder-deleted", Status: common.UserStatusEnabled, RequestCount: 1, Email: "deleted@example.com"},
			} {
				require.NoError(t, db.Create(&user).Error)
			}
			require.NoError(t, db.Delete(&model.User{}, 8074).Error)
			balanceReminderMemory.Lock()
			previousStates := balanceReminderMemory.states
			balanceReminderMemory.states = make(map[int]map[string]bool)
			balanceReminderMemory.Unlock()
			t.Cleanup(func() {
				balanceReminderMemory.Lock()
				balanceReminderMemory.states = previousStates
				balanceReminderMemory.Unlock()
			})
			require.NoError(t, runBalanceReminderPass(context.Background()))
			balanceReminderMemory.Lock()
			assert.Contains(t, balanceReminderMemory.states, 8071)
			assert.NotContains(t, balanceReminderMemory.states, 8072)
			assert.NotContains(t, balanceReminderMemory.states, 8073)
			assert.NotContains(t, balanceReminderMemory.states, 8074)
			balanceReminderMemory.Unlock()
			var user model.User
			require.NoError(t, db.First(&user, 8071).Error)
			assert.Equal(t, 1000000, user.Quota, "reminder scan must never alter accounting")
			var version string
			query := "select version()"
			if tc.name == "sqlite" {
				query = "select sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
		})
	}
}
