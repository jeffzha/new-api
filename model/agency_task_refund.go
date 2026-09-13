package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

// applyAgencyTaskRefundTx handles post-finalization component task refunds in
// the caller's existing task/accounting transaction. targetQuota is the task's
// remaining charge, not an incremental refund. A larger corrected final bill
// needs reconciliation with a new authoritative basis, never a legacy debit.
func applyAgencyTaskRefundTx(tx *gorm.DB, task *Task, targetQuota int, result *TaskQuotaAdjustmentResult) (bool, error) {
	if task.PrivateData.BillingSource == TaskBillingSourceSubscription && task.PrivateData.SubscriptionId > 0 {
		return false, nil
	}
	var user User
	if err := tx.Select("id, billing_mode").First(&user, task.UserId).Error; err != nil {
		return true, err
	}
	if user.BillingMode != AgencyDurableBillingMode {
		return false, nil
	}
	chargeID := task.TaskID
	knownEventID := ""
	var knownEvent agencycontract.BillingEvent
	if context := task.PrivateData.BillingContext; context != nil {
		if strings.TrimSpace(context.AgencyChargeID) != "" {
			chargeID = strings.TrimSpace(context.AgencyChargeID)
		}
		knownEventID = strings.TrimSpace(context.AgencyBillingEventID)
		if knownEventID != "" {
			var outbox AgencyBillingOutbox
			if err := tx.Where("event_id = ?", knownEventID).First(&outbox).Error; err != nil {
				return true, err
			}
			if err := common.UnmarshalJsonStr(outbox.Payload, &knownEvent); err != nil {
				return true, err
			}
			if knownEvent.SchemaVersion == agencycontract.ComponentSchemaVersion {
				if knownEvent.EventID != knownEventID || knownEvent.UserID != int64(task.UserId) || knownEvent.SegmentNo != 0 ||
					(strings.TrimSpace(context.AgencyChargeID) != "" && knownEvent.FinancialChargeID != chargeID) {
					return true, ErrAgencyChargeConflict
				}
				chargeID = knownEvent.FinancialChargeID
			}
		}
	}
	var componentCount int64
	if err := tx.Model(&AgencyChargeComponent{}).Where("charge_id = ? AND segment_no = ?", chargeID, 0).Count(&componentCount).Error; err != nil {
		return true, err
	}
	var operation AgencyBillingOperation
	err := tx.Where("charge_id = ? AND segment_no = ? AND operation = ?", chargeID, 0, "finalize").Order("revision DESC").First(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if knownEvent.SchemaVersion == agencycontract.ComponentSchemaVersion || componentCount > 0 {
			return true, ErrAgencyChargeConflict
		}
		var pending AgencyBillingJournal
		if err := tx.Where("charge_id = ? AND segment_no = 0", chargeID).First(&pending).Error; err == nil {
			var basis AgencyTaskChargeBasis
			if err := common.UnmarshalJsonStr(pending.BillingBasis, &basis); err != nil {
				return true, err
			}
			if basis.Version == AgencyTaskChargeBasisVersion {
				// New task reservations require a terminal fact and frozen usage
				// through CompleteAgencyTask, never an amount-only adjustment.
				return true, ErrAgencyChargeConflict
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return true, err
		}
		return false, nil // Reserved/submitted tasks are not model-sale refunds.
	}
	if err != nil {
		return true, err
	}
	var original agencycontract.BillingEvent
	if err := common.UnmarshalJsonStr(operation.CommittedResult, &original); err != nil {
		return true, err
	}
	if original.SchemaVersion != agencycontract.ComponentSchemaVersion {
		if knownEvent.SchemaVersion == agencycontract.ComponentSchemaVersion || componentCount > 0 {
			return true, ErrAgencyChargeConflict
		}
		return false, nil
	}
	if knownEventID != "" {
		originalHash, err := agencycontract.CanonicalHash(original)
		if err != nil {
			return true, err
		}
		knownHash, err := agencycontract.CanonicalHash(knownEvent)
		if err != nil {
			return true, err
		}
		if originalHash != knownHash {
			return true, ErrAgencyChargeConflict
		}
	}
	if task.ID <= 0 || original.UserID != int64(task.UserId) || original.FinancialChargeID != chargeID || original.SegmentNo != 0 ||
		original.ChargedTotalQuota < int64(targetQuota) || targetQuota > task.Quota {
		return true, ErrAgencyChargeConflict
	}
	if err := agencycontract.ValidateBillingComponents(original); err != nil {
		return true, err
	}
	// The existing task transaction already owns its task row. Acquire the
	// financial locks in the refund API's order before checking the watermark;
	// a simultaneous explicit model refund cannot turn this target into a
	// different delta after the check.
	if err := AgencyLockForUpdate(tx).Select("id, billing_mode").First(&user, task.UserId).Error; err != nil {
		return true, err
	}
	var account AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", task.UserId).First(&account).Error; err != nil {
		return true, err
	}
	var journal AgencyBillingJournal
	if err := AgencyLockForUpdate(tx).Where("charge_id = ? AND segment_no = ?", chargeID, 0).First(&journal).Error; err != nil {
		return true, err
	}
	if journal.UserID != int64(task.UserId) || journal.ChargedTotalQuota != original.ChargedTotalQuota ||
		(journal.Status != "finalized" && journal.Status != "partially_reversed" && journal.Status != "reversed") ||
		journal.ReversedQuota < 0 || journal.ChargedTotalQuota-journal.ReversedQuota != int64(task.Quota) ||
		(journal.TokenID == nil && task.PrivateData.TokenId > 0) ||
		(journal.TokenID != nil && *journal.TokenID != int64(task.PrivateData.TokenId)) {
		return true, ErrAgencyChargeConflict
	}
	if targetQuota == task.Quota {
		// Provider confirmation of an unchanged final charge completes its
		// reconciliation receipt without manufacturing a zero refund event.
		return true, nil
	}
	if task.PrivateData.TokenId > 0 {
		var token Token
		err := AgencyLockForUpdate(tx).Where("id = ? AND user_id = ?", task.PrivateData.TokenId, task.UserId).First(&token).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result.QuotaDelta = 0
			result.TokenUnavailable = true
			return true, nil
		}
		if err != nil {
			return true, err
		}
		result.TokenKey = token.Key
	}
	cumulative := original.ChargedTotalQuota - int64(targetQuota)
	digest := sha256.Sum256([]byte(fmt.Sprintf("task:%d:charge:%s:cumulative:%d", task.ID, chargeID, cumulative)))
	event, err := AgencyRefundWalletChargeTx(tx, AgencyComponentRefundInput{UserID: int64(task.UserId), ChargeID: chargeID,
		CumulativeQuota: cumulative, RefundID: "task-refund-" + hex.EncodeToString(digest[:]), Reason: "async_task_cumulative_refund"}, result.TokenKey)
	if err != nil {
		return true, err
	}
	if event.ChargedTotalQuota != int64(task.Quota-targetQuota) {
		return true, ErrAgencyChargeConflict
	}
	updated := tx.Model(&Task{}).Where("id = ? AND user_id = ? AND quota = ?", task.ID, task.UserId, task.Quota).Update("quota", targetQuota)
	if updated.Error != nil {
		return true, updated.Error
	}
	if updated.RowsAffected != 1 {
		return true, ErrAgencyChargeConflict
	}
	result.AppliedQuota = targetQuota
	result.QuotaDelta = targetQuota - task.Quota
	result.TokenAdjusted = task.PrivateData.TokenId > 0
	result.WalletAdjusted = true
	result.Changed = true
	result.AgencyRefundCommitted = true
	task.Quota = targetQuota
	return true, nil
}
