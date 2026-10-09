package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

var ErrTaskBillingRecordNotReady = errors.New("task billing record is not ready")

// TaskBillingReconciliationDefaultBatch bounds how many provider-billing task
// reconciliations run per scheduled pass (every task-poll tick). Each entry
// performs at least one upstream bill fetch, so keep it modest to avoid
// hammering providers while still draining the backlog.
const TaskBillingReconciliationDefaultBatch = 20

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
	// Re-settle task finalizations written by the pre-fix backfill, which the
	// agency hub rejects for missing operation identity and which projected
	// usage facts at the repair time instead of the charge time.
	if repaired := repairNonCanonicalAgencyTaskSettlements(ctx, limit); repaired > 0 {
		logger.LogInfo(ctx, fmt.Sprintf("agency task settlement repair finalized %d legacy charge(s)", repaired))
	}
	// Charges that could never be priced from token usage (tiered and plugin
	// video models) stay in reconcile_required until they are settled at the
	// quota the wallet already paid.
	if repaired := repairPendingFinalUsageTaskSettlements(ctx, limit); repaired > 0 {
		logger.LogInfo(ctx, fmt.Sprintf("agency task final usage repair finalized %d charge(s)", repaired))
	}
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
		if resolveErr != nil || resolution == nil || resolution.TotalTokens <= 0 || resolution.ActualQuota < 0 ||
			(resolution.ActualQuota == 0 && !model.IsAgencyDurableUser(task.UserId)) {
			// Some async video upstreams (e.g. openai_seedance via laomandi)
			// never report a billable token usage, so this reconciliation can
			// never settle. For agency-durable users, finalize the successful
			// charge at the quota that was actually charged instead of retrying
			// forever with no provider bill, so the usage fact still appears.
			if model.IsAgencyDurableUser(task.UserId) &&
				task.PrivateData.BillingContext != nil &&
				task.PrivateData.BillingContext.AgencyPricing != nil {
				if finErr := ensureAgencyTaskFinalUsage(&task, int64(task.Quota)); finErr != nil {
					logger.LogError(ctx, fmt.Sprintf("agency task %s provider-bill fallback finalize failed: %s", task.TaskID, finErr.Error()))
				} else {
					_ = finishTaskBillingReconciliation(record.ID, model.TaskBillingReconciliationNotNeeded, "provider bill unavailable; finalized at charged quota")
					summary.Settled++
					continue
				}
			}
			if resolveErr != nil {
				retryTaskBillingReconciliation(record, resolveErr)
			} else {
				retryTaskBillingReconciliation(record, errors.New("provider returned invalid billing usage"))
			}
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
			settlement.Task.Quota = settlement.PreConsumedQuota + settlement.QuotaDelta
			recordTaskQuotaAdjustment(
				ctx,
				settlement.Task,
				settlement.PreConsumedQuota,
				settlement.Task.Quota,
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

// repairedTaskSettlements bounds the legacy repair to one attempt per charge
// per process. A charge that cannot be re-settled (for example because its
// wallet allocation no longer matches the frozen task quota) stays visible for
// operator reconciliation instead of being retried on every poll tick.
var repairedTaskSettlements sync.Map

// repairNonCanonicalAgencyTaskSettlements re-finalizes task charges whose
// finalize receipt was written without the operation identity and money
// sequence the agency hub verifies. Those receipts keep their delivery in a
// permanent retry loop, and the usage facts written beside them carry the
// repair time, which is what made agent-center video calls look like they
// never reached the platform consumption log. Re-settling through the
// canonical writer fixes both: the hub accepts the event and projects the
// usage fact at the charge time.
func repairNonCanonicalAgencyTaskSettlements(ctx context.Context, limit int) int {
	charges, err := model.NonCanonicalTaskFinalizeCharges(limit)
	if err != nil {
		logger.LogError(ctx, "load non-canonical agency task finalizations failed: "+err.Error())
		return 0
	}
	repaired := 0
	for _, chargeID := range charges {
		if _, attempted := repairedTaskSettlements.LoadOrStore(chargeID, struct{}{}); attempted {
			continue
		}
		task, findErr := model.AgencyTaskForCharge(chargeID)
		if findErr != nil {
			logger.LogWarn(ctx, fmt.Sprintf("agency task charge %s repair skipped: %s", chargeID, findErr.Error()))
			continue
		}
		if settleErr := model.SettleAgencyTaskAtChargedQuota(task, int64(task.Quota)); settleErr != nil {
			logger.LogWarn(ctx, fmt.Sprintf("agency task charge %s repair failed: %s", chargeID, settleErr.Error()))
			continue
		}
		repaired++
	}
	return repaired
}

// repairedFinalUsageAttempts bounds the final-usage repair to one attempt per
// charge per process. A charge that still cannot settle (for example because
// the wallet allocation no longer matches the frozen task quota) stays visible
// for operator reconciliation instead of being retried on every poll tick.
var repairedFinalUsageAttempts sync.Map

// repairPendingFinalUsageTaskSettlements finalizes successful async task charges
// whose frozen token basis could never price a final usage. Tiered and plugin
// video models report seconds and resolution instead of tokens, so the token
// basis returns ErrAgencyTaskUsagePending and the charge used to sit in
// reconcile_required forever: the customer's wallet was charged and the platform
// consumption log recorded the call, while agency usage and commission showed
// nothing. Settling at the charged quota makes both sides agree without moving
// any money the wallet has not already paid.
func repairPendingFinalUsageTaskSettlements(ctx context.Context, limit int) int {
	charges, err := model.PendingFinalUsageTaskCharges(limit)
	if err != nil {
		logger.LogError(ctx, "load agency task charges pending final usage failed: "+err.Error())
		return 0
	}
	repaired := 0
	for _, chargeID := range charges {
		if _, attempted := repairedFinalUsageAttempts.LoadOrStore(chargeID, struct{}{}); attempted {
			continue
		}
		task, findErr := model.AgencyTaskForCharge(chargeID)
		if findErr != nil {
			logger.LogWarn(ctx, fmt.Sprintf("agency final usage charge %s repair skipped: %s", chargeID, findErr.Error()))
			continue
		}
		if task.Status != model.TaskStatusSuccess {
			continue
		}
		if settleErr := model.SettleAgencyTaskAtChargedQuota(task, int64(task.Quota)); settleErr != nil {
			logger.LogWarn(ctx, fmt.Sprintf("agency final usage charge %s repair failed: %s", chargeID, settleErr.Error()))
			continue
		}
		repaired++
	}
	return repaired
}
