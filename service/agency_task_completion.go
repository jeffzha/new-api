package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

func completeAgencyTaskBilling(ctx context.Context, task *model.Task, expectedStatus model.TaskStatus, totalTokens int64) error {
	result, err := model.CompleteAgencyTask(task, expectedStatus, totalTokens)
	if err != nil {
		reason := "terminal_settlement_failed"
		if errors.Is(err, model.ErrAgencyTaskUsagePending) {
			reason = "final_usage_pending"
		} else if errors.Is(err, model.ErrAgencyChargeConflict) {
			reason = "terminal_conflict"
		}
		if markErr := model.MarkAgencyTaskReconcileRequired(task, reason); markErr != nil {
			logger.LogError(ctx, fmt.Sprintf("agency task %s reconciliation marker failed: %v", task.TaskID, markErr))
		}
		return err
	}
	if result.Changed && result.QuotaDelta != 0 {
		recordTaskQuotaAdjustment(ctx, task, result.PreConsumedQuota, task.Quota, "agency task terminal settlement")
	}
	return nil
}
