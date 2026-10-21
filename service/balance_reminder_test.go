package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
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
	assert.Equal(t, []int{0, 68493, 136986, 342466}, balanceReminderThresholds(dto.UserSetting{}))
	assert.Equal(t, []int{0, 68493, 136986, 342466, 500000}, balanceReminderThresholds(dto.UserSetting{QuotaWarningThreshold: 500000}))
}

func TestBalanceReminderEmailDestinationAndMissingEmail(t *testing.T) {
	truncate(t)
	beforeServer, beforePort, beforeFrom := common.SMTPServer, common.SMTPPort, common.SMTPFrom
	beforeAccount, beforeToken := common.SMTPAccount, common.SMTPToken
	beforeSSL, beforeTLS, beforeRedis := common.SMTPSSLEnabled, common.SMTPStartTLSEnabled, common.RedisEnabled
	beforeNotifyLimit := constant.NotifyLimitCount
	t.Cleanup(func() {
		common.SMTPServer, common.SMTPPort, common.SMTPFrom = beforeServer, beforePort, beforeFrom
		common.SMTPAccount, common.SMTPToken = beforeAccount, beforeToken
		common.SMTPSSLEnabled, common.SMTPStartTLSEnabled, common.RedisEnabled = beforeSSL, beforeTLS, beforeRedis
		constant.NotifyLimitCount = beforeNotifyLimit
	})
	common.SMTPAccount, common.SMTPToken = "", ""
	common.SMTPSSLEnabled, common.SMTPStartTLSEnabled, common.RedisEnabled = false, false, false
	constant.NotifyLimitCount = 100
	common.SMTPFrom = "service@example.com"
	for i, tc := range []struct {
		name, accountEmail, notificationEmail, recipient string
	}{
		{"fallback to bound account email", "account@example.com", "", "account@example.com"},
		{"notification email takes precedence", "account@example.com", "notify@example.com", "notify@example.com"},
		{"notification email without account binding", "", "notify@example.com", "notify@example.com"},
		{"neither address means no delivery", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := 8270 + i
			user := model.User{Id: id, Username: fmt.Sprintf("reminder-email-%d", id), AffCode: fmt.Sprintf("reminder-email-%d", id), Email: tc.accountEmail, Quota: 1, Status: common.UserStatusEnabled}
			require.NoError(t, model.DB.Create(&user).Error)
			settings, err := common.Marshal(dto.UserSetting{NotificationEmail: tc.notificationEmail})
			require.NoError(t, err)
			user.Setting = string(settings)
			t.Cleanup(func() { delete(balanceReminderMemory.states, id) })
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			host, port, err := net.SplitHostPort(listener.Addr().String())
			require.NoError(t, err)
			common.SMTPServer = host
			common.SMTPPort, err = strconv.Atoi(port)
			require.NoError(t, err)
			messages := make(chan string, 1)
			if tc.recipient != "" {
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					wire := textproto.NewConn(conn)
					_ = wire.PrintfLine("220 localhost SMTP test")
					for {
						line, err := wire.ReadLine()
						if err != nil {
							return
						}
						switch {
						case line == "DATA":
							_ = wire.PrintfLine("354 send message")
							body, err := wire.ReadDotBytes()
							if err != nil {
								return
							}
							messages <- string(body)
							_ = wire.PrintfLine("250 accepted")
						case line == "QUIT":
							_ = wire.PrintfLine("221 goodbye")
							return
						default:
							_ = wire.PrintfLine("250 OK")
						}
					}
				}()
			}
			require.NoError(t, checkBalanceReminder(context.Background(), user, NotifyUser))
			if tc.recipient == "" {
				assert.NotContains(t, balanceReminderMemory.states, id, "missing address must not consume reminder state")
				return
			}
			select {
			case message := <-messages:
				assert.Contains(t, message, "To: "+tc.recipient)
				assert.Contains(t, message, "当前钱包余额")
				assert.NotContains(t, message, "{{value}}")
			case <-time.After(5 * time.Second):
				t.Fatal("SMTP server did not receive the low-balance email")
			}
			require.NoError(t, checkBalanceReminder(context.Background(), user, NotifyUser))
			assert.Empty(t, messages, "same tier must not deliver twice")
		})
	}
}

// Opt-in smoke test: production SMTP settings arrive through stdin, never files
// or command arguments. All balances and reminder states remain in test memory.
func TestBalanceReminderSMTPDeliverySmoke(t *testing.T) {
	if os.Getenv("REMINDER_SMTP_SMOKE") != "1" {
		t.Skip("explicit real-email smoke test not enabled")
	}
	recipient := os.Getenv("REMINDER_SMTP_RECIPIENT")
	require.NotEmpty(t, recipient)
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
	require.NoError(t, err)
	var options map[string]string
	require.NoError(t, common.Unmarshal(input, &options))
	common.SMTPServer, common.SMTPFrom = options["SMTPServer"], options["SMTPFrom"]
	common.SMTPAccount, common.SMTPToken = options["SMTPAccount"], options["SMTPToken"]
	common.SMTPPort, err = strconv.Atoi(options["SMTPPort"])
	require.NoError(t, err)
	common.SMTPSSLEnabled = options["SMTPSSLEnabled"] == "true"
	common.SMTPStartTLSEnabled = options["SMTPStartTLSEnabled"] == "true"
	common.SMTPForceAuthLogin = options["SMTPForceAuthLogin"] == "true"
	system_setting.ServerAddress = options["ServerAddress"]
	require.NotEmpty(t, system_setting.ServerAddress)
	common.RedisEnabled = false
	constant.NotifyLimitCount = 2
	constant.NotificationLimitDurationMinute = 10
	operation_setting.USDExchangeRate, common.QuotaPerUnit = 7.3, 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeCNY
	truncate(t)
	const id = 8290
	quota := common.QuotaRound(4.0 / 7.3 * common.QuotaPerUnit)
	seedUser(t, id, quota)
	t.Cleanup(func() { delete(balanceReminderMemory.states, id) })
	user := model.User{Id: id, Email: recipient}
	deliveries := 0
	send := func(id int, email string, settings dto.UserSetting, data dto.Notify) error {
		deliveries++
		data.Title = "【测试邮件，无真实扣款】低余额提醒（模拟余额4元）"
		data.Content = "本邮件由管理员授权测试触发。模拟余额低于5元，未修改线上账户余额、未调用模型、未产生费用。<br/>" + data.Content
		return NotifyUser(id, email, settings, data)
	}
	require.NoError(t, checkBalanceReminder(context.Background(), user, send))
	require.NoError(t, checkBalanceReminder(context.Background(), user, send))
	assert.Equal(t, 1, deliveries)
	var after model.User
	require.NoError(t, model.DB.First(&after, id).Error)
	assert.Equal(t, quota, after.Quota)
	t.Log("SMTP accepted one simulated low-balance email; duplicate suppressed; test balance unchanged")
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
