package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type taskBillingReconciliationAdaptor struct {
	resolution         *TaskBillingResolution
	initializedBaseURL string
}

func TestCNTaskReconciliationRecoveryFencesStaleWorkers(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_TASK_ENABLED", "true")
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if dialect != "sqlite" {
				dsn := os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("disposable external database not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			prefix := fmt.Sprintf("q58_%d_", time.Now().UnixNano())
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: prefix}})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			oldDB, oldLog, oldFactory := model.DB, model.LOG_DB, GetTaskAdaptorFunc
			oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			t.Cleanup(func() {
				model.DB, model.LOG_DB, GetTaskAdaptorFunc = oldDB, oldLog, oldFactory
				common.SetDatabaseTypes(oldMain, oldLogType)
				tables, err := db.Migrator().GetTables()
				require.NoError(t, err)
				for _, table := range tables {
					if strings.HasPrefix(table, prefix) {
						require.NoError(t, db.Migrator().DropTable(table))
					}
				}
				require.NoError(t, pool.Close())
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.TaskBillingReconciliation{}))
			require.NoError(t, model.MigrateCNFinance(db, "q58-test"))
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "q58", AffCode: "q58", Quota: 10000}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "q58-token", RemainQuota: 10000}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "q58", Type: constant.ChannelTypeSeedanceDomestic}).Error)
			task := &model.Task{TaskID: "q58-task", UserId: 1, ChannelId: 1, Platform: constant.TaskPlatform("seedance_domestic"), Status: model.TaskStatusSuccess,
				PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{ProviderBilling: &model.TaskProviderBillingSnapshot{Provider: model.TaskBillingProviderSeedanceDomestic, AsyncReconciliationRequired: true}}}}
			row, err := model.PrepareCNTask(task, "q58-request", model.TaskBillingSourceWallet, 100, 1, false, false)
			require.NoError(t, err)
			task.CNBillingKey, task.Quota = row.Key, 100
			task.PrivateData.TokenId, task.PrivateData.BillingSource = 1, model.TaskBillingSourceWallet
			task.PrivateData.Execution = &model.TaskExecutionSnapshot{RequestID: row.RequestID}
			task.PrivateData.UpstreamTaskID = "q58-upstream"
			require.NoError(t, model.BeginCNTaskDispatch(row.Key, strings.Repeat("a", 64), 1, string(task.Platform)))
			require.NoError(t, model.RecordCNTaskUpstream(row.Key, task.PrivateData.UpstreamTaskID))
			require.NoError(t, task.Insert())
			require.NoError(t, model.EnqueueTaskBillingReconciliation(task, model.TaskBillingProviderSeedanceDomestic))
			GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
				return &taskBillingReconciliationAdaptor{resolution: &TaskBillingResolution{ActualQuota: 50, TotalTokens: 1}}
			}
			failSettlement, recoveredBeforeRetry := true, false
			var recoveryErr error
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("q58-final-failure", func(tx *gorm.DB) {
				if failSettlement && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CNFinanceOutbox" {
					failSettlement = false
					tx.AddError(errors.New("q58 injected final fact failure"))
				}
			}))
			// Interleave recovery after the failed financial transaction returns,
			// before the original worker starts its retry update transaction.
			require.NoError(t, db.Callback().Update().Before("gorm:begin_transaction").Register("q58-recover-before-retry", func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]any)
				if !ok || recoveredBeforeRetry || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "TaskBillingReconciliation" || updates["status"] != model.TaskBillingReconciliationPending {
					return
				}
				recoveredBeforeRetry = true
				recoveryErr = db.Model(&model.CNTaskBilling{}).Where(map[string]any{"key": row.Key}).Update("next_attempt_at", 0).Error
				if recoveryErr == nil {
					recoveryErr = model.RecoverCNTaskBilling(context.Background(), db, 10)
				}
			}))
			summary := RunTaskBillingReconciliationOnce(context.Background(), 10)
			require.NoError(t, db.Callback().Create().Remove("q58-final-failure"))
			require.NoError(t, db.Callback().Update().Remove("q58-recover-before-retry"))
			require.True(t, recoveredBeforeRetry)
			require.NoError(t, recoveryErr)
			require.Equal(t, 1, summary.Retried)
			require.NoError(t, db.First(&row, map[string]any{"key": row.Key}).Error)
			require.Equal(t, "final", row.State)
			require.False(t, row.Pending)
			var record model.TaskBillingReconciliation
			require.NoError(t, db.Where("task_id = ?", task.ID).First(&record).Error)
			require.Equal(t, model.TaskBillingReconciliationSettled, record.Status)
			terminal := record
			retryTaskBillingReconciliation(&record, errors.New("late retry"))
			require.NoError(t, finishTaskBillingReconciliation(&record, model.TaskBillingReconciliationNotNeeded, "late finish"))
			require.NoError(t, model.UpdateTaskBillingReconciliation(record.ID, map[string]any{"status": model.TaskBillingReconciliationPending}))
			require.NoError(t, db.First(&record, record.ID).Error)
			require.Equal(t, terminal, record)
			require.Zero(t, RunTaskBillingReconciliationOnce(context.Background(), 10).Pending)
			var user model.User
			var token model.Token
			require.NoError(t, db.First(&user, 1).Error)
			require.NoError(t, db.First(&token, 1).Error)
			require.Equal(t, 9950, user.Quota)
			require.Equal(t, 50, user.UsedQuota)
			require.Equal(t, 9950, token.RemainQuota)
			var facts int64
			require.NoError(t, db.Model(&model.CNFinanceOutbox{}).Count(&facts).Error)
			require.EqualValues(t, 2, facts)

			legacy := model.TaskBillingReconciliation{TaskID: 9999, Status: model.TaskBillingReconciliationPending}
			require.NoError(t, db.Create(&legacy).Error)
			staleDue := legacy
			claimed, err := legacy.Claim()
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, db.Model(&legacy).Update("updated_at", time.Now().Unix()-301).Error)
			claimed, err = staleDue.Claim()
			require.NoError(t, err)
			require.False(t, claimed, "old due-list attempt cannot claim a newer lease")
			var successor model.TaskBillingReconciliation
			require.NoError(t, db.First(&successor, legacy.ID).Error)
			claimed, err = successor.Claim()
			require.NoError(t, err)
			require.True(t, claimed)
			retryTaskBillingReconciliation(&legacy, errors.New("superseded worker"))
			require.NoError(t, finishTaskBillingReconciliation(&legacy, model.TaskBillingReconciliationNotNeeded, "superseded worker"))
			var current model.TaskBillingReconciliation
			require.NoError(t, db.First(&current, legacy.ID).Error)
			require.Equal(t, successor, current)
			require.NoError(t, finishTaskBillingReconciliation(&successor, model.TaskBillingReconciliationNotNeeded, "owner finished"))
			retryTaskBillingReconciliation(&successor, errors.New("late owner retry"))
			require.NoError(t, db.First(&current, legacy.ID).Error)
			require.Equal(t, model.TaskBillingReconciliationNotNeeded, current.Status)
		})
	}
}

func (a *taskBillingReconciliationAdaptor) Init(info *relaycommon.RelayInfo) {
	a.initializedBaseURL = info.ChannelMeta.ChannelBaseUrl
}

func (a *taskBillingReconciliationAdaptor) FetchTask(_ string, _ string, _ *model.Task, _ string) (*http.Response, error) {
	return nil, nil
}

func (a *taskBillingReconciliationAdaptor) ParseTaskResult(_ *model.Task, _ *http.Response, _ []byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (a *taskBillingReconciliationAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskBillingReconciliationAdaptor) ResolveTaskBilling(_ context.Context, _ *model.Task) (*TaskBillingResolution, error) {
	return a.resolution, nil
}

func TestTaskBillingReconciliationSettlesProviderUsageIdempotently(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.TaskBillingReconciliation{}))
	t.Cleanup(func() { model.DB.Exec("DELETE FROM task_billing_reconciliations") })

	const userID, tokenID, channelID = 71, 72, 73
	seedUser(t, userID, 10_000)
	seedToken(t, tokenID, userID, "seedance-reconcile", 5_000)
	seedChannel(t, channelID)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("type", constant.ChannelTypeSeedanceDomestic).Error)

	task := makeTask(userID, channelID, 100, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatusSuccess
	task.SubmitTime = time.Now().Unix()
	task.PrivateData.UpstreamTaskID = "1200"
	task.PrivateData.Key = "supplier-secret"
	task.PrivateData.Endpoint = &model.TaskEndpointSnapshot{BaseURL: "https://frozen-seedance.example"}
	task.PrivateData.BillingContext.ProviderBilling = &model.TaskProviderBillingSnapshot{
		Provider:                    model.TaskBillingProviderSeedanceDomestic,
		Currency:                    "CNY",
		UnitPricePerMillionTokens:   "46",
		CNYPerUSD:                   "7.3",
		GroupRatio:                  1,
		AsyncReconciliationRequired: true,
	}
	require.NoError(t, model.DB.Create(task).Error)
	require.NoError(t, model.EnqueueTaskBillingReconciliation(task, model.TaskBillingProviderSeedanceDomestic))
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("type", constant.ChannelTypeOpenAI).Error)

	adaptor := &taskBillingReconciliationAdaptor{resolution: &TaskBillingResolution{
		ActualQuota:        50,
		TotalTokens:        15_870,
		SupplierPrice:      "46.000000",
		SupplierDiscount:   "1.00",
		SupplierAmountPaid: "0.73",
	}}
	previousFactory := GetTaskAdaptorFunc
	selectedPlatform := constant.TaskPlatform("")
	GetTaskAdaptorFunc = func(platform constant.TaskPlatform) TaskPollingAdaptor {
		selectedPlatform = platform
		return adaptor
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, model.DB.Exec(`
CREATE TRIGGER fail_task_billing_settlement
BEFORE UPDATE OF status ON task_billing_reconciliations
WHEN NEW.status = 'settled'
BEGIN
  SELECT RAISE(ABORT, 'forced settlement failure');
END`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_billing_settlement") })

	failed := RunTaskBillingReconciliationOnce(context.Background(), 10)
	assert.Equal(t, 1, failed.Retried)
	assert.Equal(t, 10_000, getUserQuota(t, userID))
	assert.Equal(t, 5_000, getTokenRemainQuota(t, tokenID))
	var rolledBackTask model.Task
	require.NoError(t, model.DB.First(&rolledBackTask, task.ID).Error)
	assert.Equal(t, 100, rolledBackTask.Quota)
	require.NoError(t, model.DB.Exec("DROP TRIGGER fail_task_billing_settlement").Error)
	var retryRecord model.TaskBillingReconciliation
	require.NoError(t, model.DB.Where("task_id = ?", task.ID).First(&retryRecord).Error)
	require.NoError(t, model.UpdateTaskBillingReconciliation(retryRecord.ID, map[string]any{"next_retry_at": 0}))

	summary := RunTaskBillingReconciliationOnce(context.Background(), 10)
	assert.Equal(t, 1, summary.Settled)
	assert.Equal(t, constant.TaskPlatform(fmt.Sprintf("%d", constant.ChannelTypeSeedanceDomestic)), selectedPlatform)
	assert.Equal(t, "https://frozen-seedance.example", adaptor.initializedBaseURL)
	assert.Equal(t, 10_050, getUserQuota(t, userID))
	assert.Equal(t, 5_050, getTokenRemainQuota(t, tokenID))

	var storedTask model.Task
	require.NoError(t, model.DB.First(&storedTask, task.ID).Error)
	assert.Equal(t, 50, storedTask.Quota)
	var record model.TaskBillingReconciliation
	require.NoError(t, model.DB.Where("task_id = ?", task.ID).First(&record).Error)
	assert.Equal(t, model.TaskBillingReconciliationSettled, record.Status)
	assert.Equal(t, int64(15_870), record.TotalTokens)
	assert.Equal(t, 100, record.PreConsumedQuota)
	assert.Equal(t, 50, record.ActualQuota)
	assert.Equal(t, -50, record.QuotaDelta)

	// Seed a deliberately reopened legacy record to verify settlement replay.
	require.NoError(t, model.DB.Model(&record).Updates(map[string]any{
		"status":        model.TaskBillingReconciliationPending,
		"next_retry_at": 0,
	}).Error)
	replay := RunTaskBillingReconciliationOnce(context.Background(), 10)
	assert.Equal(t, 1, replay.Settled)
	assert.Equal(t, 10_050, getUserQuota(t, userID))
	assert.Equal(t, 5_050, getTokenRemainQuota(t, tokenID))
}

func TestTaskBillingReconciliationRecoversFailedInitialEnqueue(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.TaskBillingReconciliation{}))
	t.Cleanup(func() { model.DB.Exec("DELETE FROM task_billing_reconciliations") })

	task := makeTask(81, 82, 100, 0, BillingSourceWallet, 0)
	task.Status = model.TaskStatusSubmitted
	task.PrivateData.UpstreamTaskID = "1300"
	task.PrivateData.BillingContext.ProviderBilling = &model.TaskProviderBillingSnapshot{
		Provider:                    model.TaskBillingProviderSeedanceDomestic,
		Currency:                    "CNY",
		UnitPricePerMillionTokens:   "46",
		CNYPerUSD:                   "7.3",
		GroupRatio:                  1,
		AsyncReconciliationRequired: true,
	}
	task.BillingReconciliationPending = true
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, model.DB.Exec(`
CREATE TRIGGER fail_task_billing_enqueue
BEFORE INSERT ON task_billing_reconciliations
BEGIN
  SELECT RAISE(ABORT, 'forced enqueue failure');
END`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_billing_enqueue") })

	require.Error(t, model.EnqueueTaskBillingReconciliation(task, model.TaskBillingProviderSeedanceDomestic))
	var storedTask model.Task
	require.NoError(t, model.DB.First(&storedTask, task.ID).Error)
	assert.True(t, storedTask.BillingReconciliationPending)
	var count int64
	require.NoError(t, model.DB.Model(&model.TaskBillingReconciliation{}).
		Where("task_id = ?", task.ID).Count(&count).Error)
	assert.Zero(t, count)
	assert.True(t, model.HasPendingTaskBillingReconciliations())

	require.NoError(t, model.DB.Exec("DROP TRIGGER fail_task_billing_enqueue").Error)
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = nil
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })
	RunTaskBillingReconciliationOnce(context.Background(), 10)

	require.NoError(t, model.DB.First(&storedTask, task.ID).Error)
	assert.False(t, storedTask.BillingReconciliationPending)
	var record model.TaskBillingReconciliation
	require.NoError(t, model.DB.Where("task_id = ?", task.ID).First(&record).Error)
	assert.Equal(t, model.TaskBillingReconciliationPending, record.Status)
	assert.Equal(t, model.TaskBillingProviderSeedanceDomestic, record.Provider)

	enqueued, err := model.EnqueuePendingTaskBillingReconciliations(10)
	require.NoError(t, err)
	assert.Zero(t, enqueued)
}

func TestTaskBillingReconciliationDeletedTokenRetainsReservation(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.TaskBillingReconciliation{}))
	t.Cleanup(func() { model.DB.Exec("DELETE FROM task_billing_reconciliations") })

	const userID, tokenID, channelID = 83, 84, 85
	seedUser(t, userID, 10_000)
	seedToken(t, tokenID, userID, "seedance-deleted-token", 5_000)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, 100, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatusSuccess
	require.NoError(t, model.DB.Create(task).Error)
	require.NoError(t, model.DB.Delete(&model.Token{}, tokenID).Error)

	record := &model.TaskBillingReconciliation{
		TaskID:      task.ID,
		Provider:    model.TaskBillingProviderSeedanceDomestic,
		ChannelID:   channelID,
		Status:      model.TaskBillingReconciliationProcessing,
		NextRetryAt: time.Now().Unix(),
		CreatedAt:   time.Now().Unix(),
		UpdatedAt:   time.Now().Unix(),
	}
	require.NoError(t, model.DB.Create(record).Error)

	result, err := model.SettleTaskBillingReconciliation(record.ID, model.TaskBillingReconciliationSettlement{
		ActualQuota: 50,
		TotalTokens: 1000,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.TokenUnavailable)
	assert.False(t, result.Applied)
	assert.Zero(t, result.QuotaDelta)
	assert.Equal(t, 10_000, getUserQuota(t, userID))

	var storedTask model.Task
	require.NoError(t, model.DB.First(&storedTask, task.ID).Error)
	assert.Equal(t, 100, storedTask.Quota)
	var storedRecord model.TaskBillingReconciliation
	require.NoError(t, model.DB.First(&storedRecord, record.ID).Error)
	assert.Equal(t, model.TaskBillingReconciliationSettled, storedRecord.Status)
	assert.Equal(t, 0, storedRecord.QuotaDelta)
	assert.Contains(t, storedRecord.LastError, "token unavailable")
}
