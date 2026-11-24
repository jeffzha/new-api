package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestProjectWalletAuthorityAcrossDialects(t *testing.T) {
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	t.Cleanup(func() { common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch })
	oldReader := opsbilling.Default
	t.Cleanup(func() { opsbilling.Default = oldReader })
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			mainType := common.DatabaseType(dialect)
			common.SetDatabaseTypes(mainType, common.DatabaseTypeSQLite)
			t.Cleanup(func() { common.SetDatabaseTypes(oldMain, oldLogType) })
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "authority.db"))
			case "mysql", "postgres":
				dsn := os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("isolated external database not configured")
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
			// Representative released schema and a product task exist before enabling.
			t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.Midjourney{}, &model.TaskBillingReconciliation{}, &model.SystemTask{}, &model.SystemTaskLock{}))
			id := "pw" + common.GetUUID()[:14]
			user := model.User{Username: id, AffCode: id, Status: common.UserStatusEnabled, Quota: 3000}
			require.NoError(t, db.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: id, Status: common.TokenStatusEnabled, RemainQuota: 10000}
			require.NoError(t, db.Create(&token).Error)
			product := model.Task{TaskID: id + "product", UserId: user.Id, Quota: 12, Status: model.TaskStatusInProgress}
			require.NoError(t, db.Create(&product).Error)
			// Fresh process/cache: TableName is fixed once a service starts.
			t.Setenv("OPS_WALLET_AUTHORITY", "ops")
			t.Setenv("OPS_PROJECT_SCOPE", "project-uac")
			projectDB, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			projectPool, err := projectDB.DB()
			require.NoError(t, err)
			projectPool.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = projectPool.Close() })
			// Project tables are additive and can also start on an empty database.
			for range 2 {
				require.NoError(t, projectDB.AutoMigrate(&model.Task{}, &model.Midjourney{}, &model.TaskBillingReconciliation{}, &model.SystemTask{}, &model.SystemTaskLock{}))
			}
			oldDB, oldLog := model.DB, model.LOG_DB
			model.DB = projectDB
			t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLog })
			logDB, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "logs.db")), &gorm.Config{})
			require.NoError(t, e)
			logPool, e := logDB.DB()
			require.NoError(t, e)
			t.Cleanup(func() { _ = logPool.Close() })
			model.LOG_DB = logDB
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			var walletQuota atomic.Int64
			walletQuota.Store(1000)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, marshalErr := common.Marshal(map[string]any{"data": map[string]any{"userId": fmt.Sprint(user.Id), "balance": fmt.Sprint(walletQuota.Load()), "balanceQuota": walletQuota.Load(), "negative": false, "lastEntryId": 1, "updatedAt": "2026-11-04T01:00:00Z"}})
				require.NoError(t, marshalErr)
				_, _ = w.Write(raw)
			}))
			t.Cleanup(server.Close)
			opsbilling.Default = opsbilling.New(opsbilling.Config{BaseURL: server.URL, KeyID: "test", Secret: "test-secret-authority-only", Timeout: time.Second})
			info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: id, RequestId: id, ChannelMeta: &relaycommon.ChannelMeta{}}
			denied := *info
			apiErr := PreConsumeBilling(ctx, 1001, &denied)
			require.NotNil(t, apiErr)
			require.Equal(t, http.StatusForbidden, apiErr.StatusCode)
			var untouched model.Token
			require.NoError(t, projectDB.First(&untouched, token.Id).Error)
			require.Equal(t, 10000, untouched.RemainQuota)
			require.Nil(t, PreConsumeBilling(ctx, 200, info))
			acceptedBinding := *info.OPSWalletBinding
			metadata := model.NewLogOther()
			appendBillingInfo(info, metadata)
			assert.Equal(t, "ops", metadata.Snapshot()["billing_source"])
			assert.Equal(t, "project-uac", metadata.Snapshot()["billing_scope"])
			assert.Equal(t, acceptedBinding, metadata.Snapshot()["root_info"].(map[string]any)["ops_wallet_binding"])
			require.ErrorIs(t, info.Billing.Reserve(1001), opsbilling.ErrInsufficientBalance)
			require.NoError(t, info.Billing.Reserve(250))
			walletQuota.Store(0)
			opsbilling.Default.InvalidateCall(user.Id)
			// Usage already accepted must settle/refund even if money runs out.
			require.NoError(t, SettleBilling(ctx, info, 120))
			require.NoError(t, SettleBilling(ctx, info, 120))
			require.Error(t, SettleBilling(ctx, info, 121))
			info.Billing.Refund(ctx)
			var money model.User
			var budget model.Token
			require.NoError(t, projectDB.First(&money, user.Id).Error)
			require.NoError(t, projectDB.First(&budget, token.Id).Error)
			require.Equal(t, 3000, money.Quota)
			require.Equal(t, 9880, budget.RemainQuota)
			require.NoError(t, PostConsumeQuota(info, 10, 0, false))
			require.NoError(t, PostConsumeQuota(info, -10, 10, false))
			require.Error(t, PostConsumeQuota(info, -11, 10, false))
			info.TokenId = token.Id
			failed := *info
			failed.Billing = nil
			walletQuota.Store(1000)
			opsbilling.Default = opsbilling.New(opsbilling.Config{BaseURL: server.URL, KeyID: "test", Secret: "test-secret-authority-only", Timeout: time.Second})
			require.Nil(t, PreConsumeBilling(ctx, 100, &failed))
			failed.Billing.Refund(ctx)
			failed.Billing.Refund(ctx)
			require.NoError(t, projectDB.First(&budget, token.Id).Error)
			require.Equal(t, 9880, budget.RemainQuota)
			require.Error(t, SettleBilling(ctx, &failed, -1))
			require.Error(t, SettleBilling(ctx, &failed, common.MaxQuota+1))
			project := model.Task{TaskID: id + "project", UserId: user.Id, Status: model.TaskStatusSuccess, SubmitTime: time.Now().Unix(), Quota: 120, Properties: model.Properties{OriginModelName: "model-a"}, PrivateData: model.TaskPrivateData{BillingSource: "ops", TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: id}}}
			project.PrivateData.OPSWalletBinding = info.OPSWalletBinding
			prepareProjectTaskUsage(&project, []byte(`{"usage":{"total_tokens":1234},"duration":5,"resolution":"720p"}`))
			require.True(t, project.OpsUsagePending)
			require.NoError(t, projectDB.Create(&project).Error)
			frozen := string(project.PrivateData.OpsUsage)
			prepareProjectTaskUsage(&project, []byte(`{"usage":{"total_tokens":9999}}`))
			require.Equal(t, frozen, string(project.PrivateData.OpsUsage))
			var actual map[string]any
			require.NoError(t, common.Unmarshal(project.PrivateData.OpsUsage, &actual))
			require.NotEqual(t, id, actual["requestId"])
			require.NoError(t, taskAdjustFunding(&project, 20))
			adjusted, err := model.ApplyTaskQuotaAdjustment(&project, 60)
			require.NoError(t, err)
			require.False(t, adjusted.WalletAdjusted)
			require.NoError(t, projectDB.First(&money, user.Id).Error)
			require.Equal(t, 3000, money.Quota)
			var count int64
			require.NoError(t, projectDB.Model(&model.Task{}).Where("task_id = ?", product.TaskID).Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, db.Model(&model.Task{}).Where("id = ?", product.ID).Count(&count).Error)
			require.EqualValues(t, 1, count)
			// A process-specific push cursor never acquires the product's row.
			p := opspush.New(projectDB, logDB, opspush.Config{StateID: 2, Scope: opsauthority.Scope()})
			require.NoError(t, p.Migrate())
			require.NoError(t, p.Migrate())
			require.NoError(t, projectDB.Create(&opspush.State{Id: 1, Cursor: 999}).Error)
			model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{UserId: user.Id, LogType: model.LogTypeRefund, ModelName: "model-a", Quota: 1, Other: model.NewLogOther()})
			var log model.Log
			require.NoError(t, logDB.First(&log).Error)
			require.Contains(t, log.Other, `"billing_scope":"project-uac"`)
		})
	}
}

func TestProjectWalletAuthorityConfigRejectsMixedWriters(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	require.Error(t, opsauthority.Validate())
	t.Setenv("OPS_PROJECT_SCOPE", "project-uac")
	t.Setenv("OPS_PUSH_STATE_ID", "2")
	t.Setenv("OPS_BILLING_ENABLED", "true")
	t.Setenv("OPS_BILLING_RECEIPTS", "charges")
	for _, key := range []string{"OPS_PUSH_URL", "OPS_BILLING_URL"} {
		t.Setenv(key, "https://ops.example.test/test")
	}
	for _, key := range []string{"OPS_PUSH_KEY_ID", "OPS_BILLING_KEY_ID"} {
		t.Setenv(key, "project-only")
	}
	for _, key := range []string{"OPS_PUSH_SECRET", "OPS_BILLING_SECRET"} {
		t.Setenv(key, "fixture-project-secret-123")
	}
	for _, key := range []string{"OPS_BRIDGE_ENABLED", "OPS_ENFORCE_CALLS", "OPS_CN_DURABLE_TASK_ENABLED", "OPS_CN_DURABLE_MIDJOURNEY_ENABLED"} {
		t.Setenv(key, "false")
	}
	require.NoError(t, opsauthority.Validate())
	t.Setenv("OPS_BRIDGE_ENABLED", "true")
	require.Error(t, opsauthority.Validate())
	t.Setenv("OPS_BRIDGE_ENABLED", "false")
	t.Setenv("OPS_WALLET_PROFILE", "product")
	require.Error(t, opsauthority.Validate(), "a product cannot inherit project cursors and scope")
	t.Setenv("OPS_PROJECT_SCOPE", "")
	t.Setenv("OPS_PUSH_STATE_ID", "1")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_PUSH_SPEC", "v1.1")
	t.Setenv("OPS_USER_SYNC_BALANCES_ENABLED", "false")
	t.Setenv("OPS_INGEST_BUDGET_ENABLED", "true")
	require.ErrorContains(t, opsauthority.Validate(), "online custody handoff", "flags cannot make an unimplemented live-money handoff ready")
	require.Equal(t, "tasks", opsauthority.TaskTable("tasks"))
	require.Empty(t, opsauthority.Scope())
	t.Setenv("OPS_USER_SYNC_BALANCES_ENABLED", "true")
	require.Error(t, opsauthority.Validate(), "source balance snapshots must not overwrite OPS money")
	t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
	t.Setenv("OPS_FINANCIAL_OUTBOX_ENABLED", "true")
	t.Setenv("OPS_FINANCIAL_OUTBOX_SCOPE", "accepted-project")
	require.NoError(t, opsauthority.Validate(), "rollback can drain already committed financial events")
	assert.Equal(t, "accepted-project", opsauthority.FinancialOutboxScope())
	t.Setenv("OPS_FINANCIAL_OUTBOX_SCOPE", "invalid scope")
	require.Error(t, opsauthority.Validate())
	t.Setenv("OPS_FINANCIAL_OUTBOX_SCOPE", "accepted-project")
	t.Setenv("OPS_PUSH_SPEC", "v1")
	require.Error(t, opsauthority.Validate(), "a rollback must not change the financial event protocol")
	t.Setenv("OPS_PUSH_SPEC", "v1.1")
	t.Setenv("OPS_TASK_OUTBOX_ENABLED", "true")
	t.Setenv("OPS_PROJECT_SCOPE", "accepted-project")
	require.ErrorContains(t, opsauthority.Validate(), "scoped worker", "project rollback must not silently switch its task tables and lease to the product")
	t.Setenv("OPS_TASK_OUTBOX_ENABLED", "false")
	require.ErrorContains(t, opsauthority.Validate(), "scoped worker", "the ordinary scanner must not send unscoped product logs with retained project credentials")
	t.Setenv("OPS_PROJECT_SCOPE", "")
	require.NoError(t, opsauthority.Validate(), "native product tasks retain their existing table during rollback")
}

func TestTaskUsageKeepsAcceptedWalletOwnerAcrossProcessConfiguration(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
	binding, err := opsauthority.NewBinding("https://ops.example.invalid/test", "accepted-ledger", "accepted-project")
	require.NoError(t, err)
	task := model.Task{
		TaskID: "immutable-owner-task", UserId: 15, Quota: 120,
		Status: model.TaskStatusSuccess, SubmitTime: 1793491200,
		Properties: model.Properties{OriginModelName: "model-a"},
		PrivateData: model.TaskPrivateData{BillingSource: "ops",
			OPSWalletBinding: binding,
			Execution:        &model.TaskExecutionSnapshot{RequestID: "immutable-owner-request"}},
	}
	// A configuration rollback cannot discard usage owed to the accepted owner.
	prepareOpsTaskUsage(&task, 0, []byte(`{"usage":{"total_tokens":123}}`))
	require.True(t, task.OpsUsagePending)
	var event map[string]any
	require.NoError(t, common.Unmarshal(task.PrivateData.OpsUsage, &event))
	assert.Equal(t, "15", event["userId"])
	extra, ok := event["extra"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, extra["project_task_terminal"])
	assert.Equal(t, "immutable-owner-request", extra["project_original_request"])
	assert.Equal(t, "accepted-project", extra["billing_scope"])
	frozen := string(task.PrivateData.OpsUsage)
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_PROFILE", "product")
	t.Setenv("OPS_PROJECT_SCOPE", "different-project")
	prepareOpsTaskUsage(&task, 0, []byte(`{"usage":{"total_tokens":999}}`))
	assert.Equal(t, frozen, string(task.PrivateData.OpsUsage))
	unbound := task
	unbound.PrivateData.OPSWalletBinding = nil
	unbound.PrivateData.OpsUsage = nil
	unbound.OpsUsagePending = false
	prepareOpsTaskUsage(&unbound, 0, []byte(`{"usage":{"total_tokens":123}}`))
	assert.False(t, unbound.OpsUsagePending, "missing destination cannot be reconstructed from current configuration")
	require.NotPanics(t, func() { finalizeOpsTaskReservation(nil) })
}

func TestAcceptedWalletOwnerSurvivesConfigurationChange(t *testing.T) {
	for _, tc := range []struct {
		name, process, source string
		wantUser, wantToken   int
	}{
		{"legacy debit after enabling OPS", "ops", BillingSourceWallet, 990, 990},
		{"OPS debit after configuration rollback", "gateway", "ops", 1000, 990},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			t.Setenv("OPS_WALLET_AUTHORITY", tc.process)
			t.Setenv("OPS_WALLET_PROFILE", "product")
			seedUser(t, 71, 1000)
			seedToken(t, 71, 71, "immutable-wallet-owner", 1000)
			info := &relaycommon.RelayInfo{UserId: 71, TokenId: 71, TokenKey: "immutable-wallet-owner", BillingSource: tc.source}
			require.NoError(t, PostConsumeQuota(info, 10, 0, false))
			assert.Equal(t, tc.wantUser, getUserQuota(t, 71))
			assert.Equal(t, tc.wantToken, getTokenRemainQuota(t, 71))
			require.NoError(t, PostConsumeQuota(info, -10, 10, false))
			assert.Equal(t, 1000, getUserQuota(t, 71))
			assert.Equal(t, 1000, getTokenRemainQuota(t, 71))
		})
	}
	require.Error(t, PostConsumeQuota(nil, 10, 0, false))
}

func TestMidjourneyWalletOwnerSurvivesConfigurationChange(t *testing.T) {
	for _, tc := range []struct {
		name, accepted, completing string
		wantUser                   int
	}{
		{"legacy task after enabling OPS", "gateway", "ops", 990},
		{"OPS task after configuration rollback", "ops", "gateway", 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			t.Setenv("OPS_WALLET_PROFILE", "product")
			t.Setenv("OPS_WALLET_AUTHORITY", tc.accepted)
			oldReader := opsbilling.Default
			t.Cleanup(func() { opsbilling.Default = oldReader })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"data":{"userId":"72","balance":"10","balanceQuota":1000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-25T01:00:00Z"}}`))
			}))
			t.Cleanup(server.Close)
			opsbilling.Default = opsbilling.New(opsbilling.Config{BaseURL: server.URL, KeyID: "fixture-mj-ledger", Secret: "fixture-mj-secret", Timeout: time.Second})
			require.NoError(t, model.PrepareOPSWalletFundingSchema(t.Context(), model.DB))
			t.Cleanup(func() { require.NoError(t, model.DB.Exec("DELETE FROM ops_wallet_events").Error) })
			seedUser(t, 72, 1000)
			seedToken(t, 72, 72, "midjourney-wallet-owner", 1000)
			seedChannel(t, 72)
			info := &relaycommon.RelayInfo{UserId: 72, TokenId: 72, TokenKey: "midjourney-wallet-owner", RequestId: "midjourney-accepted-request", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 72}}
			require.NoError(t, CheckMidjourneyWallet(nil, info, 10))
			task := &model.Midjourney{UserId: 72, ChannelId: 72, MjId: "accepted-wallet-owner", Action: "IMAGINE"}
			prepared, err := PrepareMidjourneyTaskBilling(info, task, 10, true)
			require.NoError(t, err)
			require.True(t, prepared)
			require.NoError(t, task.Insert())
			// A different process can finish this accepted task. Its active flag
			// must not select another wallet for settlement or refund.
			t.Setenv("OPS_WALLET_AUTHORITY", tc.completing)
			billed, err := SettleMidjourneyTaskBilling(info, task, prepared)
			require.NoError(t, err)
			require.True(t, billed)
			assert.Equal(t, tc.wantUser, getUserQuota(t, 72))
			assert.Equal(t, 990, getTokenRemainQuota(t, 72))
			persisted := getMidjourneyTask(t, task.Id)
			assert.Equal(t, info.BillingSource, persisted.BillingSource)
			if tc.accepted == "ops" {
				assert.NotEmpty(t, persisted.OPSWalletBinding)
				assert.Equal(t, info.RequestId, persisted.BillingRequestID)
			}
			stale := persisted
			seedChargedAccounting(t, 72, 72, 72, 10, 1)
			require.True(t, RefundMidjourneyQuota(t.Context(), &persisted, "isolated owner regression"))
			assert.Equal(t, 1000, getUserQuota(t, 72))
			assert.Equal(t, 1000, getTokenRemainQuota(t, 72))
			require.True(t, RefundMidjourneyQuota(t.Context(), &persisted, "repeat observation"))
			if tc.accepted == "ops" {
				require.True(t, RefundMidjourneyQuota(t.Context(), &stale, "stale poller replay"))
				t.Setenv("OPS_FINANCIAL_OUTBOX_ENABLED", "true")
				rows, err := model.ReadOPSWalletFunding(model.DB, 100)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				require.NoError(t, model.ValidateOPSWalletFunding(rows[0]))
			}
			assert.Equal(t, 1000, getUserQuota(t, 72))
			assert.Equal(t, 1000, getTokenRemainQuota(t, 72))
		})
	}
}
