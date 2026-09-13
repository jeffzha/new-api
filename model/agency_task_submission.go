package model

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// BeginAgencyTaskSubmission is the durable boundary before sending a task to
// its provider. An ambiguous attempt is never automatically sent again: most
// video providers do not implement idempotent creation.
func BeginAgencyTaskSubmission(userID int, chargeID, publicID, requestHash, trace string) error {
	if userID <= 0 || strings.TrimSpace(chargeID) == "" || strings.TrimSpace(publicID) == "" || requestHash == "" {
		return ErrAgencyChargeConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var journal AgencyBillingJournal
		if err := lockForUpdate(tx).Where("charge_id = ? AND segment_no = 0 AND user_id = ?", chargeID, userID).First(&journal).Error; err != nil {
			return err
		}
		if journal.Status != "reserved" {
			return ErrAgencyChargeConflict
		}
		var prior AgencyTaskSubmissionAttempt
		err := tx.Where("charge_id = ? OR public_task_id = ?", chargeID, publicID).First(&prior).Error
		if err == nil {
			// A definitive provider rejection ends this request. The caller may
			// start a fresh request, but cannot reuse an accepted public ID.
			return ErrAgencyChargeConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&AgencyTaskSubmissionAttempt{ChargeID: chargeID, SubmitNo: 1,
			PublicTaskID: publicID, RequestHash: requestHash, Status: "submitting",
			EvidenceTrace: trace, CreatedAtMS: time.Now().UnixMilli()}).Error
	})
}

// RecordAgencyTaskSubmission records provider acceptance before any success
// response reaches the caller. The task row is persisted separately, but this
// receipt retains both IDs if a crash prevents that insertion.
func RecordAgencyTaskSubmission(chargeID, publicID, providerID string) error {
	if strings.TrimSpace(providerID) == "" {
		return ErrAgencyChargeConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var journal AgencyBillingJournal
		if err := lockForUpdate(tx).Where("charge_id = ? AND segment_no = 0", chargeID).First(&journal).Error; err != nil {
			return err
		}
		var attempt AgencyTaskSubmissionAttempt
		if err := lockForUpdate(tx).Where("charge_id = ? AND public_task_id = ?", chargeID, publicID).First(&attempt).Error; err != nil {
			return err
		}
		if attempt.Status == "submitted" && attempt.ProviderTaskID == providerID && journal.Status == "submitted" {
			return nil
		}
		if attempt.Status != "submitting" || journal.Status != "reserved" {
			return ErrAgencyChargeConflict
		}
		updated := tx.Model(&AgencyTaskSubmissionAttempt{}).Where("id = ? AND status = ?", attempt.ID, "submitting").
			Updates(map[string]any{"status": "submitted", "provider_task_id": providerID})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAgencyChargeConflict
		}
		updated = tx.Model(&AgencyBillingJournal{}).Where("id = ? AND version = ?", journal.ID, journal.Version).
			Updates(map[string]any{"status": "submitted", "version": journal.Version + 1, "updated_at_ms": time.Now().UnixMilli()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAgencyChargeConflict
		}
		return nil
	})
}

// ResolveAgencyTaskSubmissionFailure records only a stable, non-sensitive
// reason code. Unknown outcomes retain the reservation for reconciliation;
// they never imply a refund or permission to generate another video.
func ResolveAgencyTaskSubmissionFailure(chargeID string, definiteRejection bool, reason string) error {
	reason = agencyTaskSubmissionReason(reason)
	return DB.Transaction(func(tx *gorm.DB) error {
		var journal AgencyBillingJournal
		if err := lockForUpdate(tx).Where("charge_id = ? AND segment_no = 0", chargeID).First(&journal).Error; err != nil {
			return err
		}
		if journal.Status != "reserved" && journal.Status != "submitted" && journal.Status != "reconcile_required" {
			return ErrAgencyChargeConflict
		}
		status := "reconcile_required"
		if definiteRejection && journal.Status == "reserved" {
			status = "rejected"
		}
		updated := tx.Model(&AgencyTaskSubmissionAttempt{}).Where("charge_id = ? AND status IN ?", chargeID, []string{"submitting", "submitted", "reconcile_required"}).
			Updates(map[string]any{"status": status, "evidence_trace": reason})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAgencyChargeConflict
		}
		if status == "rejected" {
			return nil
		}
		updated = tx.Model(&AgencyBillingJournal{}).Where("id = ? AND version = ?", journal.ID, journal.Version).
			Updates(map[string]any{"status": "reconcile_required", "version": journal.Version + 1, "updated_at_ms": time.Now().UnixMilli()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAgencyChargeConflict
		}
		return nil
	})
}

func agencyTaskSubmissionReason(reason string) string {
	switch reason {
	case "transport_error", "upstream_rejected", "upstream_status_unknown",
		"response_parse_error", "receipt_persist_failed", "task_insert_failed":
		return reason
	default:
		return "unknown"
	}
}
