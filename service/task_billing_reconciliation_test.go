package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taskBillingReconciliationAdaptor struct {
	resolution         *TaskBillingResolution
	initializedBaseURL string
}

func (a *taskBillingReconciliationAdaptor) Init(info *relaycommon.RelayInfo) {
	a.initializedBaseURL = info.ChannelMeta.ChannelBaseUrl
}

func (a *taskBillingReconciliationAdaptor) FetchTask(_ string, _ string, _ map[string]any, _ string) (*http.Response, error) {
	return nil, nil
}

func (a *taskBillingReconciliationAdaptor) ParseTaskResult(_ []byte) (*relaycommon.TaskInfo, error) {
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
	assert.Equal(t, constant.TaskPlatform("59"), selectedPlatform)
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

	require.NoError(t, model.UpdateTaskBillingReconciliation(record.ID, map[string]any{
		"status":        model.TaskBillingReconciliationPending,
		"next_retry_at": 0,
	}))
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
