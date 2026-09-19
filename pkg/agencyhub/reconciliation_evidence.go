package agencyhub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type reconciliationCheck struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Matched  bool   `json:"matched"`
}
type reconciliationVerification struct {
	State          string                `json:"state"`
	EvidenceHash   string                `json:"evidence_hash"`
	Checks         []reconciliationCheck `json:"checks"`
	AllowedActions []string              `json:"allowed_actions"`
}

func (v *reconciliationVerification) check(name, expected, actual string) {
	v.Checks = append(v.Checks, reconciliationCheck{Name: name, Expected: expected, Actual: actual, Matched: expected == actual})
	if expected != actual && v.State != "unsupported" {
		v.State = "inconsistent"
	}
}

func (v *reconciliationVerification) nonnegative(name string, amount int64) {
	v.Checks = append(v.Checks, reconciliationCheck{Name: name, Expected: ">= 0", Actual: stringID(amount), Matched: amount >= 0})
	if amount < 0 && v.State != "unsupported" {
		v.State = "inconsistent"
	}
}

func (a *App) reconciliationIssueView(issue model.AgencyReconciliationIssue) gin.H {
	var actorID, resolvedAt any
	if issue.ActorID != nil {
		actorID = stringID(*issue.ActorID)
	}
	if issue.ResolvedAtMS != nil {
		resolvedAt = stringID(*issue.ResolvedAtMS)
	}
	objectName := a.reconciliationObjectName(issue)
	return gin.H{"id": stringID(issue.ID), "object_type": issue.ObjectType, "object_id": issue.ObjectID, "object_name": objectName, "difference": issue.Difference, "evidence_hash": issue.EvidenceHash, "status": issue.Status, "resolution": issue.Resolution, "actor_id": actorID, "created_at_ms": stringID(issue.CreatedAtMS), "resolved_at_ms": resolvedAt, "repair_event_id": issue.RepairEventID, "resolution_evidence": issue.ResolutionEvidence}
}

func (a *App) reconciliationObjectName(issue model.AgencyReconciliationIssue) string {
	objectName := issue.ObjectID
	prefix, rawID := issue.ObjectType, issue.ObjectID
	if issue.ObjectType == "commission_balance" || issue.ObjectType == "withdrawal_lock" {
		agencyID, currency, ok := strings.Cut(issue.ObjectID, ":")
		if ok {
			var agency model.Agency
			if a != nil && a.db != nil && a.db.Select("display_name").First(&agency, agencyID).Error == nil {
				return fmt.Sprintf("%s · %s", agency.DisplayName, currency)
			}
		}
	}
	if explicitType, explicitID, ok := strings.Cut(issue.ObjectID, ":"); ok {
		if _, err := strconv.ParseInt(explicitID, 10, 64); err == nil {
			prefix, rawID, objectName = explicitType, explicitID, explicitID
		}
	}
	if a == nil || a.db == nil {
		return objectName
	}
	var name string
	switch prefix {
	case "agency":
		var row model.Agency
		if err := a.db.Select("display_name").First(&row, rawID).Error; err == nil {
			name = row.DisplayName
		}
	case "withdrawal":
		var row model.AgencyWithdrawal
		if err := a.db.Select("request_no").First(&row, rawID).Error; err == nil {
			name = row.RequestNo
		}
	case "withdrawal_account":
		var row model.AgencyWithdrawalAccount
		if err := a.db.Select("last4").First(&row, rawID).Error; err == nil {
			name = fmt.Sprintf("收款账户 · 尾号 %s", row.Last4)
		}
	case "user", "customer", "funding_account", "active_binding":
		var row model.User
		if err := a.db.Select("username, display_name").First(&row, rawID).Error; err == nil {
			name = userAccountName(row)
		}
	case "funding_lot":
		var lot model.AgencyFundingLot
		if err := a.db.Select("user_id").First(&lot, rawID).Error; err == nil {
			var row model.User
			if err := a.db.Select("username, display_name").First(&row, lot.UserID).Error; err == nil {
				name = userAccountName(row)
			}
		}
	}
	if strings.TrimSpace(name) != "" {
		return name
	}
	return objectName
}

// tx must already be a consistent snapshot transaction. Resolution callers
// lock the issue/session and use serializable isolation before closing it.
// The hash contains only whitelisted evidence, never event payloads or secrets.
func reconciliationEvidence(tx *gorm.DB, issue model.AgencyReconciliationIssue, lock bool) (reconciliationVerification, error) {
	v := reconciliationVerification{State: "consistent", Checks: []reconciliationCheck{}, AllowedActions: []string{}}
	query := tx
	if lock && issue.ObjectType == "commission_balance" {
		// Reusing a GORM chain without a new session accumulates SELECT/WHERE
		// clauses across tables on dialects that emit FOR UPDATE.
		query = model.AgencyLockForUpdate(tx).Session(&gorm.Session{})
	}
	// Gateway-owned rows are read through the serializable snapshot: the
	// sidecar must not require UPDATE privileges on users or immutable outbox
	// just to verify them. This also avoids mixing MySQL snapshot and current
	// reads, and avoids exclusive locks inverting withdrawal/worker ordering.
	var sourceErr error
	canRestore := false
	switch issue.ObjectType {
	case "funding_account":
		id, err := strconv.ParseInt(issue.ObjectID, 10, 64)
		if err != nil || id <= 0 {
			sourceErr = gorm.ErrRecordNotFound
			break
		}
		var user model.User
		if sourceErr = query.Select("id, quota").First(&user, id).Error; sourceErr != nil {
			break
		}
		var account model.AgencyFundingAccount
		if sourceErr = query.Where("user_id = ?", id).First(&account).Error; sourceErr != nil {
			break
		}
		expected := new(big.Int).Add(big.NewInt(account.PaidAvailable), big.NewInt(account.NonpaidAvailable))
		expected.Sub(expected, big.NewInt(account.DebtQuota))
		v.check("wallet_equals_available_minus_debt", expected.String(), stringID(int64(user.Quota)))
		v.nonnegative("paid_available", account.PaidAvailable)
		v.nonnegative("nonpaid_available", account.NonpaidAvailable)
		v.nonnegative("debt_quota", account.DebtQuota)
		v.check("account_version", stringID(account.Version), stringID(account.Version))
		v.check("money_seq", stringID(account.MoneySeq), stringID(account.MoneySeq))
	case "commission_balance", "withdrawal_lock":
		parts := strings.SplitN(issue.ObjectID, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			sourceErr = gorm.ErrRecordNotFound
			break
		}
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id <= 0 {
			sourceErr = gorm.ErrRecordNotFound
			break
		}
		var balance model.AgencyCommissionBalance
		if sourceErr = query.Where("agency_id = ? AND currency_code = ?", id, parts[1]).First(&balance).Error; sourceErr != nil {
			break
		}
		if issue.ObjectType == "commission_balance" {
			left := new(big.Int).Sub(big.NewInt(balance.EarnedMicros), big.NewInt(balance.ReversedMicros))
			right := new(big.Int).Add(big.NewInt(balance.AvailableMicros), big.NewInt(balance.LockedMicros))
			right.Add(right, big.NewInt(balance.PaidMicros))
			v.check("commission_balance_equation", left.String(), right.String())
			v.nonnegative("earned_micros", balance.EarnedMicros)
			v.nonnegative("reversed_micros", balance.ReversedMicros)
			v.nonnegative("paid_micros", balance.PaidMicros)
			v.check("commission_reversal_within_earned", "true", strconv.FormatBool(balance.ReversedMicros <= balance.EarnedMicros))
			// Available may be negative after an already paid commission is
			// reversed. Preserve that debt; it must not be treated as corruption.
		} else {
			var rows []model.AgencyWithdrawal
			if sourceErr = query.Select("id, status, amount_micros, version").Where("agency_id = ? AND currency_code = ? AND status IN ?", id, parts[1], []string{"submitted", "reviewing", "approved", "on_hold", "paying", "payment_unknown"}).Order("id ASC").Find(&rows).Error; sourceErr != nil {
				break
			}
			total := new(big.Int)
			for _, row := range rows {
				total.Add(total, big.NewInt(row.AmountMicros))
				v.check("withdrawal_amount_positive", "true", strconv.FormatBool(row.AmountMicros > 0))
				stamp := fmt.Sprintf("%d:%s:%d:%d", row.ID, row.Status, row.AmountMicros, row.Version)
				v.check("withdrawal_snapshot", stamp, stamp)
			}
			v.check("locked_equals_outstanding_withdrawals", total.String(), stringID(balance.LockedMicros))
		}
		v.nonnegative("locked_micros", balance.LockedMicros)
		v.check("balance_version", stringID(balance.Version), stringID(balance.Version))
	case "funding_lot":
		idText := strings.TrimSuffix(issue.ObjectID, ":bonus")
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || id <= 0 {
			sourceErr = gorm.ErrRecordNotFound
			break
		}
		var lot model.AgencyFundingLot
		if sourceErr = query.First(&lot, id).Error; sourceErr != nil {
			break
		}
		paid, bonus := new(big.Int), new(big.Int)
		for _, amount := range []int64{lot.PaidAvailable, lot.PaidReserved, lot.PaidConsumed, lot.PaidRevoked, lot.PaidDebtRepaid} {
			paid.Add(paid, big.NewInt(amount))
		}
		for _, amount := range []int64{lot.BonusAvailable, lot.BonusReserved, lot.BonusConsumed, lot.BonusRevoked, lot.BonusDebtRepaid, lot.BonusExpired} {
			bonus.Add(bonus, big.NewInt(amount))
		}
		v.check("paid_lot_conservation", stringID(lot.PaidInitial), paid.String())
		v.check("bonus_lot_conservation", stringID(lot.BonusInitial), bonus.String())
		for _, bucket := range []struct {
			name   string
			amount int64
		}{
			{"paid_initial", lot.PaidInitial}, {"paid_available", lot.PaidAvailable}, {"paid_reserved", lot.PaidReserved},
			{"paid_consumed", lot.PaidConsumed}, {"paid_revoked", lot.PaidRevoked}, {"paid_debt_repaid", lot.PaidDebtRepaid},
			{"bonus_initial", lot.BonusInitial}, {"bonus_available", lot.BonusAvailable}, {"bonus_reserved", lot.BonusReserved},
			{"bonus_consumed", lot.BonusConsumed}, {"bonus_revoked", lot.BonusRevoked}, {"bonus_debt_repaid", lot.BonusDebtRepaid},
			{"bonus_expired", lot.BonusExpired},
		} {
			v.nonnegative(bucket.name, bucket.amount)
		}
		v.check("lot_version", stringID(lot.Version), stringID(lot.Version))
	case "charge_component":
		sourceErr = reconciliationComponentEvidence(query, issue.ObjectID, &v)
	case "active_binding":
		id, err := strconv.ParseInt(issue.ObjectID, 10, 64)
		if err != nil || id <= 0 {
			sourceErr = gorm.ErrRecordNotFound
			break
		}
		var current model.AgencyActiveUserBinding
		if sourceErr = query.Where("user_id = ?", id).First(&current).Error; sourceErr != nil {
			break
		}
		var history model.AgencyUserBinding
		if sourceErr = query.First(&history, current.BindingID).Error; sourceErr != nil {
			break
		}
		v.check("binding_user", stringID(current.UserID), stringID(history.UserID))
		v.check("binding_agency", stringID(current.AgencyID), stringID(history.AgencyID))
		v.check("binding_revision", stringID(current.Revision), stringID(history.Revision))
		v.check("binding_is_current", "true", strconv.FormatBool(history.EndedAtMS == nil))
		v.check("binding_id", stringID(current.BindingID), stringID(current.BindingID))
	case "billing_operation":
		var operations []model.AgencyBillingOperation
		if sourceErr = query.Where("operation_id = ? OR ((operation_id = ? OR operation_id IS NULL) AND charge_id = ?)", issue.ObjectID, "", issue.ObjectID).Order("id ASC").Limit(2).Find(&operations).Error; sourceErr != nil {
			break
		}
		if len(operations) != 1 {
			sourceErr = gorm.ErrRecordNotFound
			break
		}
		op := operations[0]
		legacyID := fmt.Sprintf("%s:%d:%d:%s", op.ChargeID, op.SegmentNo, op.Revision, op.Operation)
		var rows []model.AgencyBillingOutbox
		if sourceErr = query.Where("operation_id = ? OR operation_id = ?", issue.ObjectID, legacyID).Order("event_index ASC, id ASC").Find(&rows).Error; sourceErr != nil {
			break
		}
		v.check("operation_event_count", strconv.Itoa(op.EventCount), strconv.Itoa(len(rows)))
		v.check("operation_event_count_valid", "true", strconv.FormatBool(op.EventCount >= 0))
		// The current writer stores exactly one BillingEvent as its committed
		// result (writeAgencyJournalEventTx). There is no result-list contract to
		// prove a multi-event operation, so do not invent one during recovery.
		if op.EventCount != 1 {
			v.State = "unsupported"
		}
		v.check("operation_result_format_supported", "true", strconv.FormatBool(op.EventCount == 1))
		var committed agencycontract.BillingEvent
		validResult := common.Unmarshal([]byte(op.CommittedResult), &committed) == nil && committed.EventID != "" && agencycontract.ValidateBillingComponents(committed) == nil &&
			(committed.OperationID == issue.ObjectID || committed.OperationID == legacyID) && committed.FinancialChargeID == op.ChargeID && committed.SegmentNo == op.SegmentNo &&
			committed.MoneySeq == op.MoneySeq && committed.JournalRevision == op.Revision && committed.EventCount == op.EventCount && committed.EventIndex == 0
		v.check("operation_committed_result", "true", strconv.FormatBool(validResult))
		if validResult && committed.SchemaVersion == agencycontract.ComponentSchemaVersion && op.Operation == "finalize" {
			if err := reconciliationComponentJournalEvidence(query, committed, &v); err != nil {
				return v, err
			}
		}
		committedDigest := sha256.Sum256([]byte(op.CommittedResult))
		committedHash := hex.EncodeToString(committedDigest[:])
		v.check("operation_result_hash", committedHash, committedHash)
		v.check("operation_revision", stringID(op.Revision), stringID(op.Revision))
		v.check("operation_money_seq", stringID(op.MoneySeq), stringID(op.MoneySeq))
		for index, row := range rows {
			v.check("event_index", strconv.Itoa(index), strconv.Itoa(row.EventIndex))
			v.check("event_count", strconv.Itoa(op.EventCount), strconv.Itoa(row.EventCount))
			if !validResult || op.EventCount != 1 {
				continue
			}
			expectedHash := billingPayloadHash(op.CommittedResult)
			v.check("committed_event_id", committed.EventID, row.EventID)
			v.check("committed_outbox_hash", expectedHash, strings.ToLower(strings.TrimSpace(row.PayloadHash)))
			v.check("event_operation", committed.OperationID, row.OperationID)
			v.check("event_schema", committed.SchemaVersion, row.SchemaVersion)
			v.check("event_kind", committed.EventType, row.EventKind)
			v.check("event_user", stringID(committed.UserID), stringID(row.UserID))
			v.check("event_money_seq", stringID(committed.MoneySeq), stringID(row.MoneySeq))
			var emitted agencycontract.BillingEvent
			if common.Unmarshal([]byte(row.Payload), &emitted) != nil {
				v.check("outbox_payload_decodes", "true", "false")
				continue
			}
			actualHash := billingPayloadHash(row.Payload)
			v.check("committed_payload_hash", expectedHash, actualHash)
		}
	case "billing_outbox":
		var outbox model.AgencyBillingOutbox
		if sourceErr = query.Where("event_id = ?", issue.ObjectID).First(&outbox).Error; sourceErr != nil {
			break
		}
		var event agencycontract.BillingEvent
		if common.Unmarshal([]byte(outbox.Payload), &event) != nil {
			v.State = "unsupported"
			v.check("outbox_payload_decodes", "true", "false")
			break
		}
		hash := billingPayloadHash(outbox.Payload)
		v.check("outbox_payload_hash", strings.ToLower(strings.TrimSpace(outbox.PayloadHash)), hash)
		v.check("event_schema_supported", "true", strconv.FormatBool(event.SchemaVersion == agencycontract.SchemaVersion || event.SchemaVersion == agencycontract.ComponentSchemaVersion))
		v.check("event_components_valid", "true", strconv.FormatBool(agencycontract.ValidateBillingComponents(event) == nil))
		v.check("stored_schema", event.SchemaVersion, outbox.SchemaVersion)
		v.check("event_identity", outbox.EventID, event.EventID)
		v.check("source_event_identity", issue.ObjectID, outbox.EventID)
		v.check("event_operation", outbox.OperationID, event.OperationID)
		v.check("event_kind", outbox.EventKind, event.EventType)
		v.check("event_index", strconv.Itoa(outbox.EventIndex), strconv.Itoa(event.EventIndex))
		v.check("event_count", strconv.Itoa(outbox.EventCount), strconv.Itoa(event.EventCount))
		v.check("event_batch_valid", "true", strconv.FormatBool(event.EventCount > 0 && event.EventIndex >= 0 && event.EventIndex < event.EventCount))
		v.check("event_user", stringID(outbox.UserID), stringID(event.UserID))
		v.check("event_money_seq", stringID(outbox.MoneySeq), stringID(event.MoneySeq))
		validAmounts := event.UserID > 0 && event.MoneySeq >= 0 && event.StandardQuota >= 0 && event.ChargedTotalQuota >= 0 && event.CommissionableQuota >= 0 && event.NoncommissionableQuota >= 0 && event.CommissionableQuota <= event.ChargedTotalQuota && event.NoncommissionableQuota <= event.ChargedTotalQuota
		v.check("event_amounts_valid", "true", strconv.FormatBool(validAmounts))
		var receipt model.AgencySourceEvent
		receiptErr := query.Where("event_id = ?", outbox.EventID).First(&receipt).Error
		if receiptErr != nil && !errors.Is(receiptErr, gorm.ErrRecordNotFound) {
			return v, receiptErr
		}
		if receiptErr == nil {
			v.check("receipt_payload_hash", hash, receipt.PayloadHash)
			v.check("receipt_operation", event.OperationID, receipt.SourceOperationID)
			v.check("receipt_schema", event.SchemaVersion, receipt.SchemaVersion)
			v.check("receipt_user", stringID(event.UserID), stringID(receipt.UserID))
			v.check("receipt_money_seq", stringID(event.MoneySeq), stringID(receipt.MoneySeq))
			v.check("receipt_journal_revision", stringID(event.JournalRevision), stringID(receipt.JournalRevision))
			v.check("receipt_is_terminal", "true", strconv.FormatBool(receipt.ProcessingStatus == "done" || receipt.ProcessingStatus == "skipped"))
		}
		var delivery model.AgencyEventDelivery
		deliveryErr := query.Where("event_id = ?", outbox.EventID).First(&delivery).Error
		if deliveryErr != nil && !errors.Is(deliveryErr, gorm.ErrRecordNotFound) {
			return v, deliveryErr
		}
		canRestore = errors.Is(deliveryErr, gorm.ErrRecordNotFound) && v.State == "consistent"
		v.check("delivery_exists", "true", strconv.FormatBool(deliveryErr == nil))
		if deliveryErr == nil {
			v.check("delivery_id", stringID(delivery.ID), stringID(delivery.ID))
			v.check("delivery_state", delivery.Status, delivery.Status)
			validState := delivery.Status == "pending" || delivery.Status == "retry" || delivery.Status == "claimed" || delivery.Status == "done"
			v.check("delivery_state_valid", "true", strconv.FormatBool(validState))
			if delivery.Status == "done" {
				v.check("done_delivery_has_receipt", "true", strconv.FormatBool(receiptErr == nil))
			}
		}
	default:
		v.State = "unsupported"
		v.check("supported_invariant", "true", "false")
	}
	if sourceErr != nil {
		if !errors.Is(sourceErr, gorm.ErrRecordNotFound) {
			return v, sourceErr
		}
		v.State = "unsupported"
		v.check("authoritative_source_exists", "true", "false")
	}
	if issue.Status == "open" || ((issue.Status == "ignored" || issue.Status == "resolved") && strings.TrimSpace(issue.ResolutionEvidence) == "") {
		if v.State == "consistent" {
			v.AllowedActions = append(v.AllowedActions, "verify_resolved")
		}
		if canRestore {
			v.AllowedActions = append(v.AllowedActions, "restore_delivery")
		}
	}
	encoded, err := common.Marshal(gin.H{"issue_id": stringID(issue.ID), "object_type": issue.ObjectType, "object_id": issue.ObjectID, "status": issue.Status, "detected_evidence_hash": issue.EvidenceHash, "checks": v.Checks, "state": v.State})
	if err != nil {
		return v, err
	}
	digest := sha256.Sum256(encoded)
	v.EvidenceHash = hex.EncodeToString(digest[:])
	return v, nil
}
