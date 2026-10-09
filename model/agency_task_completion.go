package model

import (
	"errors"
	"strconv"
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

// SettleAgencyTaskAtChargedQuota finalizes a successful asynchronous task at
// the quota already removed from the wallet when the provider bill can never
// be resolved (openai_seedance style video upstreams report no billable
// usage). It commits through the canonical wallet charge transaction so the
// emitted agency.billing_finalized event carries the operation identity,
// money sequence and journal revision the agency hub verifies before it
// projects usage and commission facts.
//
// The settlement is idempotent. A charge that already holds a canonical
// finalize receipt is left untouched; a receipt written by the pre-fix
// backfill (no operation identity) is removed together with the outbox row,
// delivery row and projected usage facts it created, so the same charge can
// be settled again on the canonical path. The event occurrence is the charge
// time, which keeps agency usage aligned with the platform consumption log.
func SettleAgencyTaskAtChargedQuota(task *Task, finalQuota int64) error {
	if task == nil || task.ID <= 0 || task.Status != TaskStatusSuccess || task.PrivateData.BillingContext == nil {
		return nil
	}
	bc := task.PrivateData.BillingContext
	if bc.AgencyPricing == nil || strings.TrimSpace(bc.AgencyPricing.OriginModelName) == "" {
		return nil
	}
	chargeID := strings.TrimSpace(bc.AgencyChargeID)
	if chargeID == "" {
		chargeID = strings.TrimSpace(task.TaskID)
	}
	if chargeID == "" {
		return nil
	}
	if finalQuota < 0 {
		return errors.New("negative agency task charge")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var journal AgencyBillingJournal
		if err := lockForUpdate(tx).Where("charge_id = ? AND segment_no = ?", chargeID, 0).First(&journal).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if journal.UserID != int64(task.UserId) {
			return ErrAgencyChargeConflict
		}
		if journal.Status == "cancelled" || journal.Status == "reversed" || journal.Status == "partially_reversed" {
			return nil
		}
		canonicalReceipt, err := discardNonCanonicalTaskFinalizeTx(tx, &journal)
		if err != nil {
			return err
		}
		if canonicalReceipt {
			return nil
		}
		allocated, err := agencyFundingAllocatedTx(tx, journal.UserID, chargeID)
		if err != nil {
			return err
		}
		if allocated != finalQuota {
			// A backfill must never move money: only the quota the wallet
			// already holds can be finalized.
			return ErrAgencyChargeConflict
		}
		var snapshot agencycontract.PricingSnapshot
		if err := common.UnmarshalJsonStr(journal.PricingSnapshot, &snapshot); err != nil {
			return err
		}
		event, err := agencyTaskChargedQuotaEvent(bc, snapshot, finalQuota)
		if err != nil {
			return err
		}
		// Align the agency usage record with the platform consumption log,
		// which is written when the charge is accepted.
		event.OccurredAtMS = journal.CreatedAtMS
		if _, err := AgencyCommitWalletChargeTx(tx, event, ""); err != nil {
			return err
		}
		return tx.Model(&AgencyBillingJournal{}).Where("id = ? AND user_id = ?", journal.ID, journal.UserID).
			Updates(map[string]any{"task_id": task.ID, "last_error": "", "updated_at_ms": time.Now().UnixMilli()}).Error
	})
}

// discardNonCanonicalTaskFinalizeTx reports whether the charge already holds
// a canonical finalize receipt. A receipt written without the operation
// identity and money sequence is removed, together with the outbox row,
// delivery row and usage facts keyed by its event ID, and the journal returns
// to a settleable state so the canonical writer can finalize it again.
func discardNonCanonicalTaskFinalizeTx(tx *gorm.DB, journal *AgencyBillingJournal) (bool, error) {
	var receipt AgencyBillingOperation
	err := tx.Where("charge_id = ? AND segment_no = ? AND operation = ?", journal.ChargeID, journal.SegmentNo, "finalize").First(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if receipt.OperationID != "" && receipt.MoneySeq > 0 {
		return true, nil
	}
	var legacyReceipt agencycontract.BillingEvent
	if err := common.UnmarshalJsonStr(receipt.CommittedResult, &legacyReceipt); err != nil {
		return false, err
	}
	if legacyReceipt.EventID != "" {
		for _, table := range []any{&AgencyUsageFact{}, &AgencyEventDelivery{}, &AgencyBillingOutbox{}} {
			if err := tx.Where("event_id = ?", legacyReceipt.EventID).Delete(table).Error; err != nil {
				return false, err
			}
		}
	}
	if err := tx.Where("id = ?", receipt.ID).Delete(&AgencyBillingOperation{}).Error; err != nil {
		return false, err
	}
	if journal.Status != "finalized" && journal.Status != "settled" {
		return false, nil
	}
	return false, tx.Model(&AgencyBillingJournal{}).Where("id = ? AND version = ?", journal.ID, journal.Version).
		Updates(map[string]any{"status": "submitted", "business_status": "pending", "delivery_status": "pending",
			"last_error": "", "version": journal.Version + 1, "updated_at_ms": time.Now().UnixMilli()}).Error
}

// agencyTaskBackfillBasis rebuilds the frozen pricing basis recorded before
// the provider call so a task without a provider bill can still be settled at
// its agreed coefficients.
func agencyTaskBackfillBasis(bc *TaskBillingContext, snapshot agencycontract.PricingSnapshot) (AgencyTaskChargeBasis, bool) {
	quotaPerUnit, err := strconv.ParseFloat(strings.TrimSpace(snapshot.QuotaPerUnit), 64)
	if err != nil || quotaPerUnit <= 0 {
		return AgencyTaskChargeBasis{}, false
	}
	basis := AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion, QuotaPerUnit: quotaPerUnit,
		ComponentBilling: common.GetEnvOrDefaultBool("AGENCY_COMPONENT_BILLING_ENABLED", false),
		ModelPrice:       bc.ModelPrice, ModelRatio: bc.ModelRatio, OtherMultiplier: 1}
	switch {
	case bc.ProviderBilling != nil && strings.EqualFold(strings.TrimSpace(bc.ProviderBilling.Currency), "CNY"):
		basis.Mode, basis.ProviderBilling = "cny_tokens", bc.ProviderBilling
	case bc.ModelPrice > 0:
		basis.Mode = "fixed"
	default:
		return AgencyTaskChargeBasis{}, false
	}
	return basis, true
}

// agencyTaskChargedQuotaEvent builds the finalize event for a successful
// asynchronous task whose provider bill is unavailable. The quota already
// removed from the wallet stays authoritative; the standard quota is recovered
// from the frozen provider pricing so cost and commission follow the agreed
// coefficients instead of treating the discounted customer price as cost.
func agencyTaskChargedQuotaEvent(bc *TaskBillingContext, snapshot agencycontract.PricingSnapshot, charged int64) (agencycontract.BillingEvent, error) {
	basis, priced := agencyTaskBackfillBasis(bc, snapshot)
	standard := charged
	tokens := int64(0)
	if priced {
		if basis.Mode == "cny_tokens" && basis.ProviderBilling != nil {
			tokens = basis.ProviderBilling.EstimatedTokens
		}
		rebuilt, err := AgencyTaskFinalCharge(basis, snapshot, tokens, false)
		if err == nil && rebuilt.StandardQuota > 0 {
			standard = rebuilt.StandardQuota
		} else if len(snapshot.Hierarchy) > 1 {
			// A tiered policy recomputes the customer charge from the standard
			// quota, so an unrecoverable basis cannot be settled without
			// inventing money.
			return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
		}
	} else if len(snapshot.Hierarchy) > 1 {
		return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
	}
	policy := agencycontract.ResolvedPolicy{SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS,
		ModelKey: snapshot.ModelKey, OriginModelName: snapshot.OriginModelName}
	resolved, err := agencycontract.Calculate(standard, policy, 0, true)
	if err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if resolved.SettlementCostQuota > charged {
		return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
	}
	commissionable, noncommissionable := charged, int64(0)
	settlement, theoretical := resolved.SettlementCostQuota, charged-resolved.SettlementCostQuota
	if !snapshot.CommissionEligible {
		commissionable, noncommissionable = 0, charged
		settlement, theoretical = 0, 0
	}
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventType: "agency.billing_finalized",
		FinancialChargeID: snapshot.FinancialChargeID, SegmentNo: 0,
		UserID: snapshot.UserID, TokenID: agencyTaskTokenPointer(snapshot.TokenID),
		AgencyID: &snapshot.AgencyID, BindingID: &snapshot.BindingID,
		OriginModelName: snapshot.OriginModelName,
		BusinessStatus:  "success", BillingStatus: "finalized",
		CurrencyCode: snapshot.CurrencyCode, QuotaPerUnit: snapshot.QuotaPerUnit, ExchangeRate: snapshot.ExchangeRate,
		SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS,
		CommissionEligible: snapshot.CommissionEligible, CommissionSkipReason: snapshot.EligibilityReason,
		StandardQuota: standard, ChargedTotalQuota: charged,
		CommissionableQuota: commissionable, NoncommissionableQuota: noncommissionable,
		SettlementCostQuota: settlement, TheoreticalCommissionQuota: theoretical,
		FinancialFinal: true,
	}
	if basis.ComponentBilling {
		event.SchemaVersion = agencycontract.ComponentSchemaVersion
		event.Components = []agencycontract.BillingComponent{{ComponentID: "task", StandardQuota: standard,
			ChargedTotalQuota: charged, CommissionableQuota: commissionable, NoncommissionableQuota: noncommissionable,
			SettlementCostQuota: settlement, TheoreticalCommissionQuota: theoretical,
			CommissionEligible: snapshot.CommissionEligible, CommissionSkipReason: snapshot.EligibilityReason}}
	}
	encoded, err := common.Marshal(struct {
		Basis       AgencyTaskChargeBasis `json:"frozen_task_basis"`
		TotalTokens int64                 `json:"total_tokens"`
		Outcome     string                `json:"outcome"`
	}{basis, tokens, event.BusinessStatus})
	if err != nil {
		return agencycontract.BillingEvent{}, err
	}
	event.BillingBasis = string(encoded)
	return event, nil
}

func agencyTaskTokenPointer(value int64) *int64 { return &value }
