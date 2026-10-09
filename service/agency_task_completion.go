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
	// Async video/provider-billing tasks (e.g. seedance) may never produce a
	// final provider bill our resolver can reconcile (openai_seedance upstreams
	// commonly omit token usage), so the successful charge would otherwise never
	// surface as an agency usage fact. Finalize it at the charged quota through
	// the canonical settlement path instead. The call is a no-op when a
	// canonical terminal event already exists, so the billing-reconciliation
	// path (e.g. seedance_domestic) stays authoritative and nothing is
	// finalized twice.
	if expectedStatus == model.TaskStatusSuccess {
		// The provider-bill reconciler for the domestic Seedance channel stays
		// authoritative: it replaces the pre-charged quota with the exact
		// provider bill, which may be higher than the estimate. Every other
		// async provider is finalized here when it never reports usage.
		provider := ""
		if bc := task.PrivateData.BillingContext; bc != nil && bc.ProviderBilling != nil {
			provider = bc.ProviderBilling.Provider
		}
		if provider != model.TaskBillingProviderSeedanceDomestic {
			if finErr := ensureAgencyTaskFinalUsage(task, int64(task.Quota)); finErr != nil {
				logger.LogError(ctx, fmt.Sprintf("record agency task final usage %s failed: %v", task.TaskID, finErr))
			}
		}
	}
	return nil
}

// ensureAgencyTaskFinalUsage finalizes an asynchronous task whose provider bill
// cannot be resolved at the quota already removed from the wallet. It delegates
// to the canonical model settlement so the emitted agency.billing_finalized
// event carries the operation identity, money sequence and charge-time
// occurrence the agency hub verifies before projecting usage and commission
// facts. It moves no money: a charge whose wallet allocation does not match the
// requested quota is refused and left for reconciliation.
func ensureAgencyTaskFinalUsage(task *model.Task, finalQuota int64) error {
	if task == nil || task.PrivateData.BillingContext == nil || task.PrivateData.BillingContext.AgencyPricing == nil {
		return nil
	}
	return model.SettleAgencyTaskAtChargedQuota(task, finalQuota)
}
