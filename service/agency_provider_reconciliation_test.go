package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgencyProviderReconciliationRefundKeepsUsageProjectionAndFinancialReceipt(t *testing.T) {
	db, task, user, token, original := agencyAsyncRefundFixture(t)
	require.NoError(t, db.AutoMigrate(&model.TaskBillingReconciliation{}))
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", task.ChannelId).Update("type", constant.ChannelTypeSeedanceDomestic).Error)
	require.NoError(t, model.EnqueueTaskBillingReconciliation(task, model.TaskBillingProviderSeedanceDomestic))
	adaptor := &taskBillingReconciliationAdaptor{resolution: &TaskBillingResolution{
		ActualQuota: 60, TotalTokens: 1000, SupplierPrice: "2.5", SupplierDiscount: "1", SupplierAmountPaid: "0.0025",
	}}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })
	summary := RunTaskBillingReconciliationOnce(context.Background(), 10)
	assert.Equal(t, 1, summary.Settled)
	assert.Zero(t, summary.Retried)
	var wallet model.User
	var persistedToken model.Token
	var channel model.Channel
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.First(&persistedToken, token.Id).Error)
	require.NoError(t, db.First(&channel, task.ChannelId).Error)
	assert.Equal(t, 40, wallet.Quota)
	assert.Equal(t, 60, wallet.UsedQuota, "usage projection still records the committed adjustment")
	assert.Equal(t, 1, wallet.RequestCount)
	assert.Equal(t, int64(60), channel.UsedQuota)
	assert.Equal(t, 60, persistedToken.UsedQuota)
	assert.Equal(t, 40, persistedToken.RemainQuota)
	var record model.TaskBillingReconciliation
	require.NoError(t, db.Where("task_id = ?", task.ID).First(&record).Error)
	assert.Equal(t, 100, record.PreConsumedQuota)
	assert.Equal(t, -40, record.QuotaDelta)
	// Another worker can recheck the same provider bill without changing
	// balances, duplicating the commission event, or subtracting usage again.
	require.NoError(t, model.UpdateTaskBillingReconciliation(record.ID, map[string]any{"status": model.TaskBillingReconciliationPending, "next_retry_at": 0}))
	summary = RunTaskBillingReconciliationOnce(context.Background(), 10)
	assert.Equal(t, 1, summary.Settled)
	require.NoError(t, db.First(&wallet, user.Id).Error)
	assert.Equal(t, 60, wallet.UsedQuota)
	assert.Equal(t, 40, wallet.Quota)
	var reversals, logs int64
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "reverse").Count(&reversals).Error)
	require.NoError(t, db.Model(&model.Log{}).Where("user_id = ? AND type = ?", user.Id, model.LogTypeRefund).Count(&logs).Error)
	assert.Equal(t, int64(1), reversals)
	assert.Equal(t, int64(1), logs)
}
