package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

var ErrTaskBillingRecordNotReady = errors.New("task billing record is not ready")

type TaskBillingResolution struct {
	ActualQuota        int
	TotalTokens        int64
	QuotaClamp         *common.QuotaClamp
	SupplierPrice      string
	SupplierDiscount   string
	SupplierAmountPaid string
	ExpenseTime        string
}

type TaskBillingReconciler interface {
	ResolveTaskBilling(ctx context.Context, task *model.Task) (*TaskBillingResolution, error)
}

type TaskBillingReconciliationSummary struct {
	Pending int `json:"pending"`
	Settled int `json:"settled"`
	Retried int `json:"retried"`
	Skipped int `json:"skipped"`
}

func RunTaskBillingReconciliationOnce(ctx context.Context, limit int) TaskBillingReconciliationSummary {
	summary := TaskBillingReconciliationSummary{}
	if _, err := model.EnqueuePendingTaskBillingReconciliations(limit); err != nil {
		logger.LogError(ctx, "recover task billing reconciliation enqueue failed: "+err.Error())
	}
	if GetTaskAdaptorFunc == nil {
		return summary
	}
	records, err := model.GetDueTaskBillingReconciliations(limit)
	if err != nil {
		logger.LogError(ctx, "load task billing reconciliations failed: "+err.Error())
		return summary
	}
	summary.Pending = len(records)
	for _, record := range records {
		if ctx.Err() != nil {
			break
		}
		claimed, claimErr := model.ClaimTaskBillingReconciliation(record.ID)
		if claimErr != nil {
			logger.LogError(ctx, fmt.Sprintf("claim task billing reconciliation %d failed: %s", record.ID, claimErr.Error()))
			continue
		}
		if !claimed {
			continue
		}

		var task model.Task
		if findErr := model.DB.First(&task, record.TaskID).Error; findErr != nil {
			_ = finishTaskBillingReconciliation(record.ID, model.TaskBillingReconciliationNotNeeded, "task no longer exists")
			summary.Skipped++
			continue
		}
		if task.Status == model.TaskStatusFailure {
			_ = finishTaskBillingReconciliation(record.ID, model.TaskBillingReconciliationNotNeeded, "task failed and was refunded")
			summary.Skipped++
			continue
		}
		if task.Status != model.TaskStatusSuccess {
			retryTaskBillingReconciliation(record, ErrTaskBillingRecordNotReady)
			summary.Retried++
			continue
		}

		channel, channelErr := model.CacheGetChannel(task.ChannelId)
		if channelErr != nil {
			retryTaskBillingReconciliation(record, channelErr)
			summary.Retried++
			continue
		}
		channelType := channel.Type
		if record.Provider == model.TaskBillingProviderSeedanceDomestic {
			channelType = constant.ChannelTypeSeedanceDomestic
		}
		adaptor := GetTaskAdaptorFunc(constant.TaskPlatform(fmt.Sprintf("%d", channelType)))
		if adaptor == nil {
			retryTaskBillingReconciliation(record, errors.New("task adaptor not found"))
			summary.Retried++
			continue
		}
		key := channel.Key
		if task.PrivateData.Key != "" {
			key = task.PrivateData.Key
		}
		baseURL := channel.GetBaseURL()
		if endpoint := task.PrivateData.Endpoint; endpoint != nil && endpoint.BaseURL != "" {
			baseURL = endpoint.BaseURL
		}
		adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          channelType,
			ChannelId:            channel.Id,
			ChannelBaseUrl:       baseURL,
			ApiKey:               key,
			ChannelSetting:       channel.GetSetting(),
			ChannelOtherSettings: channel.GetOtherSettings(),
		}})
		reconciler, ok := adaptor.(TaskBillingReconciler)
		if !ok {
			_ = finishTaskBillingReconciliation(record.ID, model.TaskBillingReconciliationNotNeeded, "task adaptor does not support billing reconciliation")
			summary.Skipped++
			continue
		}

		resolution, resolveErr := reconciler.ResolveTaskBilling(ctx, &task)
		if resolveErr != nil {
			retryTaskBillingReconciliation(record, resolveErr)
			summary.Retried++
			continue
		}
		if resolution == nil || resolution.TotalTokens <= 0 || resolution.ActualQuota <= 0 {
			retryTaskBillingReconciliation(record, errors.New("provider returned invalid billing usage"))
			summary.Retried++
			continue
		}

		tokenKey := ""
		if task.PrivateData.TokenId > 0 {
			tokenKey = resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
		}
		settlement, settleErr := model.SettleTaskBillingReconciliation(record.ID, model.TaskBillingReconciliationSettlement{
			ActualQuota:        resolution.ActualQuota,
			TotalTokens:        resolution.TotalTokens,
			SupplierPrice:      resolution.SupplierPrice,
			SupplierDiscount:   resolution.SupplierDiscount,
			SupplierAmountPaid: resolution.SupplierAmountPaid,
			ExpenseTime:        resolution.ExpenseTime,
		})
		if settleErr != nil {
			retryTaskBillingReconciliation(record, settleErr)
			summary.Retried++
			continue
		}
		if settlement.Applied {
			if cacheErr := model.SyncTaskBillingReconciliationCaches(
				settlement.Task.UserId,
				settlement.Task.PrivateData.TokenId,
				tokenKey,
				settlement.QuotaDelta,
				settlement.WalletAdjusted,
			); cacheErr != nil {
				logger.LogWarn(ctx, fmt.Sprintf("sync task billing reconciliation %d caches failed: %s", record.ID, cacheErr.Error()))
				if invalidateErr := model.InvalidateUserCache(settlement.Task.UserId); invalidateErr != nil {
					logger.LogWarn(ctx, fmt.Sprintf("invalidate user cache after reconciliation %d failed: %s", record.ID, invalidateErr.Error()))
				}
				if invalidateErr := model.InvalidateTokenCache(tokenKey); invalidateErr != nil {
					logger.LogWarn(ctx, fmt.Sprintf("invalidate token cache after reconciliation %d failed: %s", record.ID, invalidateErr.Error()))
				}
			}
			settlement.Task.Quota = resolution.ActualQuota
			recordTaskQuotaAdjustment(
				ctx,
				settlement.Task,
				settlement.PreConsumedQuota,
				resolution.ActualQuota,
				"Seedance domestic provider bill reconciliation",
				resolution.QuotaClamp,
			)
		} else if settlement.TokenUnavailable {
			logger.LogWarn(ctx, fmt.Sprintf(
				"task billing reconciliation %d settled with deleted token; retained pre-consumed quota",
				record.ID,
			))
		}
		summary.Settled++
	}
	return summary
}

func retryTaskBillingReconciliation(record *model.TaskBillingReconciliation, reconcileErr error) {
	attempts := record.Attempts + 1
	delay := 15 * time.Second
	for i := 1; i < attempts && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	message := ""
	if reconcileErr != nil {
		message = reconcileErr.Error()
	}
	_ = model.UpdateTaskBillingReconciliation(record.ID, map[string]any{
		"status":        model.TaskBillingReconciliationPending,
		"next_retry_at": time.Now().Add(delay).Unix(),
		"last_error":    message,
	})
}

func finishTaskBillingReconciliation(id int64, status string, message string) error {
	updates := map[string]any{
		"status":        status,
		"next_retry_at": 0,
		"last_error":    message,
	}
	return model.UpdateTaskBillingReconciliation(id, updates)
}
