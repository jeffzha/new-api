package model

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgencyTaskCompletionResult struct {
	Task             Task
	Changed          bool
	FinancialFinal   bool
	BillingPending   bool
	PreConsumedQuota int
	QuotaDelta       int
	TokenKey         string
}

func agencyTaskFrozenBasis(tx *gorm.DB, task *Task) (AgencyBillingJournal, AgencyTaskChargeBasis, agencycontract.PricingSnapshot, error) {
	var journal AgencyBillingJournal
	var basis AgencyTaskChargeBasis
	var pricing agencycontract.PricingSnapshot
	chargeID := task.TaskID
	if bc := task.PrivateData.BillingContext; bc != nil && strings.TrimSpace(bc.AgencyChargeID) != "" {
		chargeID = strings.TrimSpace(bc.AgencyChargeID)
	}
	err := tx.Where("charge_id = ? AND segment_no = 0", chargeID).First(&journal).Error
	if err != nil {
		return journal, basis, pricing, err
	}
	if journal.UserID != int64(task.UserId) || journal.TokenID == nil || *journal.TokenID != int64(task.PrivateData.TokenId) {
		return journal, basis, pricing, ErrAgencyChargeConflict
	}
	if err := common.UnmarshalJsonStr(journal.PricingSnapshot, &pricing); err != nil {
		return journal, basis, pricing, err
	}
	if err := common.UnmarshalJsonStr(journal.BillingBasis, &basis); err != nil {
		return journal, basis, pricing, err
	}
	if basis.Version == "" {
		var completed struct {
			Basis AgencyTaskChargeBasis `json:"frozen_task_basis"`
		}
		if err := common.UnmarshalJsonStr(journal.BillingBasis, &completed); err != nil {
			return journal, basis, pricing, err
		}
		basis = completed.Basis
	}
	if pricing.FinancialChargeID != journal.ChargeID || pricing.UserID != journal.UserID || pricing.TokenID != int64(task.PrivateData.TokenId) {
		return journal, basis, pricing, ErrAgencyChargeConflict
	}
	return journal, basis, pricing, nil
}

// CompleteAgencyTask is the common terminal boundary for polling/callbacks.
// The provider call has already finished; no network operation runs under the
// task/financial transaction. Failure leaves the prior task status pollable.
func CompleteAgencyTask(observed *Task, expectedStatus TaskStatus, totalTokens int64) (*AgencyTaskCompletionResult, error) {
	if observed == nil || observed.ID <= 0 || (observed.Status != TaskStatusSuccess && observed.Status != TaskStatusFailure) {
		return nil, ErrAgencyChargeConflict
	}
	// This hint selects the receipt lock before opening the transaction. A
	// consistent read inside a MySQL REPEATABLE READ transaction would freeze
	// an old journal snapshot before waiting for the current task row lock.
	var hint Task
	if err := DB.First(&hint, observed.ID).Error; err != nil {
		return nil, err
	}
	result := &AgencyTaskCompletionResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var task Task
		var pendingReceipt *TaskBillingReconciliation
		if bc := hint.PrivateData.BillingContext; observed.Status == TaskStatusSuccess && bc != nil && bc.ProviderBilling != nil && bc.ProviderBilling.AsyncReconciliationRequired {
			// Reconciliation locks its receipt before the task. Acquire that
			// same order here so a polling success cannot deadlock a bill worker.
			now := time.Now().Unix()
			pendingReceipt = &TaskBillingReconciliation{TaskID: hint.ID, Provider: bc.ProviderBilling.Provider,
				ChannelID: hint.ChannelId, UpstreamTaskID: hint.GetUpstreamTaskID(), Status: TaskBillingReconciliationPending,
				NextRetryAt: now, CreatedAt: now, UpdatedAt: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(pendingReceipt).Error; err != nil {
				return err
			}
			if err := lockForUpdate(tx).Where("task_id = ?", hint.ID).First(pendingReceipt).Error; err != nil {
				return err
			}
		}
		if err := lockForUpdate(tx).First(&task, observed.ID).Error; err != nil {
			return err
		}
		if task.UserId != observed.UserId || task.TaskID != observed.TaskID || task.GetUpstreamTaskID() != observed.GetUpstreamTaskID() ||
			(task.PrivateData.BillingSource == TaskBillingSourceSubscription && task.PrivateData.SubscriptionId > 0) {
			return ErrAgencyChargeConflict
		}
		if task.Status != expectedStatus && task.Status != observed.Status {
			return ErrAgencyChargeConflict
		}
		if (task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure) && task.Status != observed.Status {
			return ErrAgencyChargeConflict
		}
		journal, basis, pricing, err := agencyTaskFrozenBasis(tx, &task)
		if err != nil {
			return err
		}
		result.PreConsumedQuota = task.Quota
		task.Status, task.Progress = observed.Status, observed.Progress
		task.StartTime, task.FinishTime = observed.StartTime, observed.FinishTime
		task.FailReason, task.Data = observed.FailReason, observed.Data
		task.PrivateData.ResultURL = observed.PrivateData.ResultURL
		if task.Status == TaskStatusSuccess && basis.ProviderBilling != nil && basis.ProviderBilling.AsyncReconciliationRequired &&
			(journal.Status == "submitted" || journal.Status == "reserved" || journal.Status == "reconcile_required") {
			// Business success is known, but the provider bill remains pending.
			// The unique receipt is committed alongside SUCCESS, so a restart
			// cannot strand a successful task outside the reconciliation queue.
			if pendingReceipt == nil || pendingReceipt.Provider != basis.ProviderBilling.Provider {
				return ErrAgencyChargeConflict
			}
			task.BillingReconciliationPending = false
			result.BillingPending = true
		} else {
			if basis.Version != AgencyTaskChargeBasisVersion && task.Status != TaskStatusFailure {
				return errors.New("agency task frozen pricing basis is missing")
			}
			if pendingReceipt != nil && journal.Status == "finalized" {
				if basis.ProviderBilling == nil || pendingReceipt.Status != TaskBillingReconciliationSettled || pendingReceipt.Provider != basis.ProviderBilling.Provider ||
					pendingReceipt.TotalTokens <= 0 || (totalTokens != 0 && totalTokens != pendingReceipt.TotalTokens) {
					return ErrAgencyChargeConflict
				}
				// A repeated business-status callback does not carry a new bill.
				// Validate against the authoritative receipt that finalized it.
				totalTokens = pendingReceipt.TotalTokens
			}
			event, err := AgencyTaskFinalCharge(basis, pricing, totalTokens, task.Status == TaskStatusFailure)
			if err != nil {
				return err
			}
			committed, changed, tokenKey, err := finalizeAgencyTaskChargeTx(tx, &task, journal, event)
			if err != nil {
				return err
			}
			result.Changed, result.TokenKey = changed, tokenKey
			result.FinancialFinal = committed.FinancialFinal
			result.QuotaDelta = task.Quota - result.PreConsumedQuota
		}
		updated := tx.Model(&Task{}).Where("id = ? AND status = ?", task.ID, expectedStatus).
			Select("status", "progress", "start_time", "finish_time", "fail_reason", "data", "private_data", "quota", "billing_reconciliation_pending").Updates(&task)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 && expectedStatus != observed.Status {
			// A validated duplicate terminal receipt is inert; it must not
			// overwrite metadata or a different amount from the winning call.
			var persisted Task
			if err := tx.First(&persisted, task.ID).Error; err != nil {
				return err
			}
			if persisted.Status != task.Status || persisted.Quota != task.Quota {
				return ErrAgencyChargeConflict
			}
			task = persisted
		}
		result.Task = task
		return nil
	})
	if err != nil {
		return nil, err
	}
	*observed = result.Task
	if result.Changed && common.RedisEnabled {
		if err := InvalidateUserCache(observed.UserId); err != nil {
			common.SysError("agency task wallet cache invalidation: " + err.Error())
		}
		if result.TokenKey != "" {
			if err := InvalidateTokenCache(result.TokenKey); err != nil {
				common.SysError("agency task token cache invalidation: " + err.Error())
			}
		}
	}
	return result, nil
}

func finalizeAgencyTaskChargeTx(tx *gorm.DB, task *Task, journal AgencyBillingJournal, input agencycontract.BillingEvent) (agencycontract.BillingEvent, bool, string, error) {
	var token Token
	if task.PrivateData.TokenId > 0 {
		err := tx.Where("id = ? AND user_id = ?", task.PrivateData.TokenId, task.UserId).First(&token).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return input, false, "", err
		}
	}
	committed, err := AgencyCommitWalletChargeTx(tx, input, token.Key)
	if err != nil {
		return input, false, token.Key, err
	}
	if err := tx.Model(&AgencyBillingJournal{}).Where("id = ? AND user_id = ?", journal.ID, task.UserId).
		Updates(map[string]any{"task_id": task.ID, "last_error": ""}).Error; err != nil {
		return input, false, token.Key, err
	}
	changed := journal.Status == "reserved" || journal.Status == "submitted" || journal.Status == "reconcile_required"
	if task.PrivateData.BillingContext == nil {
		task.PrivateData.BillingContext = &TaskBillingContext{}
	}
	task.PrivateData.BillingContext.AgencyBillingEventID = committed.EventID
	task.PrivateData.BillingContext.AgencyPaidAllocatedQuota = committed.PaidAllocatedQuota
	task.Quota = int(committed.ChargedTotalQuota)
	return committed, changed, token.Key, nil
}

// MarkAgencyTaskReconcileRequired retains unknown outcomes without inventing a
// terminal failure. Already finalized charges retain their terminal financial
// status while recording the conflicting callback for operator reconciliation.
func MarkAgencyTaskReconcileRequired(task *Task, reason string) error {
	if task == nil || task.PrivateData.BillingContext == nil || task.PrivateData.BillingContext.AgencyChargeID == "" {
		return ErrAgencyChargeConflict
	}
	switch reason {
	case "task_timeout", "channel_unavailable", "provider_response_unknown", "final_usage_pending", "terminal_settlement_failed", "terminal_conflict":
	default:
		reason = "terminal_settlement_failed"
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var journal AgencyBillingJournal
		if err := lockForUpdate(tx).Where("charge_id = ? AND user_id = ? AND segment_no = 0", task.PrivateData.BillingContext.AgencyChargeID, task.UserId).First(&journal).Error; err != nil {
			return err
		}
		updates := map[string]any{"last_error": reason, "updated_at_ms": time.Now().UnixMilli(), "version": journal.Version + 1}
		if journal.Status == "reserved" || journal.Status == "submitted" || journal.Status == "reconcile_required" {
			updates["status"] = "reconcile_required"
		}
		return tx.Model(&AgencyBillingJournal{}).Where("id = ? AND version = ?", journal.ID, journal.Version).Updates(updates).Error
	})
}
