package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

// ErrAgencyChargeCorrectionUnsupported marks a charge that cannot be corrected
// by the audited correction path. A correction only rewrites the agency
// projection of an already finalized charge to the funding the customer wallet
// actually paid, so it never moves money and can never invent an amount.
var ErrAgencyChargeCorrectionUnsupported = errors.New("agency charge is not eligible for an audited correction")

// AgencyBillingCorrection is the immutable operator audit record of one
// historical charge correction. The unique key makes a repeated correction of
// the same charge to the same total an inert replay instead of a second event.
type AgencyBillingCorrection struct {
	ID                              int64  `gorm:"primaryKey"`
	ChargeID                        string `gorm:"size:128;not null;uniqueIndex:uidx_agency_charge_correction,priority:1"`
	CorrectedChargedTotalQuota      int64  `gorm:"not null;uniqueIndex:uidx_agency_charge_correction,priority:2"`
	UserID                          int64  `gorm:"not null;index:idx_agency_charge_correction_user"`
	TaskID                          *int64
	OriginalEventID                 string `gorm:"size:128"`
	PreviousChargedTotalQuota       int64  `gorm:"not null"`
	PreviousCommissionAmountMicros  int64
	CorrectedCommissionAmountMicros int64
	JournalRevision                 int64  `gorm:"not null"`
	EventID                         string `gorm:"size:128"`
	OperationID                     string `gorm:"size:128"`
	Reason                          string `gorm:"type:text"`
	OperatorID                      int64
	CreatedAtMS                     int64 `gorm:"not null"`
}

func (AgencyBillingCorrection) TableName() string { return AgencyTablePrefix + "billing_corrections" }

// AgencyChargeCorrectionTarget is the read-only preview row of the backlog
// scan. Eligible targets are the charges whose agency usage still differs from
// the wallet; every other row keeps its reason so an operator can see what was
// deliberately left alone.
type AgencyChargeCorrectionTarget struct {
	ChargeID                  string `json:"charge_id"`
	UserID                    int64  `json:"user_id"`
	TaskID                    *int64 `json:"task_id,omitempty"`
	OriginModelName           string `json:"origin_model_name"`
	OccurredAtMS              int64  `json:"occurred_at_ms"`
	PreviousChargedTotalQuota int64  `json:"previous_charged_total_quota"`
	AllocatedQuota            int64  `json:"allocated_quota"`
	Eligible                  bool   `json:"eligible"`
	Detail                    string `json:"detail"`
}

// AgencyChargeCorrectionResult reports one applied (or already applied)
// correction, including the before and after amounts of the audit record.
type AgencyChargeCorrectionResult struct {
	ChargeID                        string `json:"charge_id"`
	UserID                          int64  `json:"user_id"`
	TaskID                          *int64 `json:"task_id,omitempty"`
	AllocatedQuota                  int64  `json:"allocated_quota"`
	PreviousChargedTotalQuota       int64  `json:"previous_charged_total_quota"`
	CorrectedChargedTotalQuota      int64  `json:"corrected_charged_total_quota"`
	PreviousCommissionAmountMicros  int64  `json:"previous_commission_amount_micros"`
	CorrectedCommissionAmountMicros int64  `json:"corrected_commission_amount_micros"`
	JournalRevision                 int64  `json:"journal_revision"`
	EventID                         string `json:"event_id"`
	OperationID                     string `json:"operation_id"`
	AlreadyApplied                  bool   `json:"already_applied"`
}

type agencyChargeCorrectionPlan struct {
	Journal  AgencyBillingJournal
	Previous agencycontract.BillingEvent
	Event    agencycontract.BillingEvent
}

// agencyChargeCorrectionPlanTx verifies every precondition of a correction
// without writing anything. A correction is only allowed when the charge holds
// a canonical finalize receipt, keeps a single segment, uses no component
// billing, and the wallet allocation is the authoritative total.
func agencyChargeCorrectionPlanTx(tx *gorm.DB, journal *AgencyBillingJournal, allocated int64) (agencyChargeCorrectionPlan, error) {
	plan := agencyChargeCorrectionPlan{}
	if journal == nil {
		return plan, ErrAgencyChargeCorrectionUnsupported
	}
	if allocated <= 0 {
		return plan, fmt.Errorf("%w: the wallet holds no funding for this charge", ErrAgencyChargeCorrectionUnsupported)
	}
	if journal.Status != "finalized" {
		return plan, fmt.Errorf("%w: charge status is %s", ErrAgencyChargeCorrectionUnsupported, journal.Status)
	}
	var segmented int64
	if err := tx.Model(&AgencyBillingJournal{}).
		Where("charge_id = ? AND segment_no > 0", journal.ChargeID).Count(&segmented).Error; err != nil {
		return plan, err
	}
	if segmented > 0 {
		return plan, fmt.Errorf("%w: segmented realtime charges keep per-segment evidence", ErrAgencyChargeCorrectionUnsupported)
	}
	var components int64
	if err := tx.Model(&AgencyChargeComponent{}).
		Where("charge_id = ? AND segment_no = ?", journal.ChargeID, journal.SegmentNo).Count(&components).Error; err != nil {
		return plan, err
	}
	if components > 0 {
		return plan, fmt.Errorf("%w: component billing keeps its own reconciliation watermarks", ErrAgencyChargeCorrectionUnsupported)
	}
	var receipt AgencyBillingOperation
	if err := tx.Where("charge_id = ? AND segment_no = ? AND operation = ?", journal.ChargeID, journal.SegmentNo, "finalize").
		Order("revision DESC").First(&receipt).Error; err != nil {
		return plan, fmt.Errorf("%w: no canonical finalize receipt", ErrAgencyChargeCorrectionUnsupported)
	}
	if receipt.OperationID == "" || receipt.MoneySeq <= 0 {
		return plan, fmt.Errorf("%w: the finalize receipt predates operation identity", ErrAgencyChargeCorrectionUnsupported)
	}
	if err := common.UnmarshalJsonStr(receipt.CommittedResult, &plan.Previous); err != nil {
		return plan, err
	}
	previous := plan.Previous
	if previous.EventID == "" || previous.FinancialChargeID != journal.ChargeID || previous.SegmentNo != journal.SegmentNo ||
		previous.UserID != journal.UserID {
		return plan, fmt.Errorf("%w: finalize receipt identity mismatch", ErrAgencyChargeCorrectionUnsupported)
	}
	if previous.SchemaVersion == agencycontract.ComponentSchemaVersion {
		return plan, fmt.Errorf("%w: component billing events are corrected through their component receipts", ErrAgencyChargeCorrectionUnsupported)
	}
	var snapshot agencycontract.PricingSnapshot
	if err := common.UnmarshalJsonStr(journal.PricingSnapshot, &snapshot); err != nil {
		return plan, err
	}
	if snapshot.FinancialChargeID != journal.ChargeID || snapshot.UserID != journal.UserID {
		return plan, fmt.Errorf("%w: frozen pricing snapshot identity mismatch", ErrAgencyChargeCorrectionUnsupported)
	}
	if len(snapshot.Hierarchy) > 1 {
		return plan, fmt.Errorf("%w: multi level commission hierarchies need a tiered recomputation", ErrAgencyChargeCorrectionUnsupported)
	}
	standard, settlement, err := agencyChargeCorrectionQuotasTx(tx, journal, snapshot, previous, allocated)
	if err != nil {
		return plan, err
	}
	eligible := snapshot.CommissionEligible && previous.CommissionEligible
	event := previous
	event.SchemaVersion = agencycontract.SchemaVersion
	event.EventType = agencycontract.BillingEventCorrected
	event.OriginalEventID = previous.EventID
	event.Components = nil
	event.CommissionSplits = nil
	event.EventID, event.OperationID = "", ""
	event.FinancialChargeID, event.SegmentNo = journal.ChargeID, journal.SegmentNo
	event.UserID = journal.UserID
	event.TokenID = journal.TokenID
	event.AgencyID, event.BindingID = &snapshot.AgencyID, &snapshot.BindingID
	event.OriginModelName = snapshot.OriginModelName
	event.BusinessStatus, event.BillingStatus, event.FinancialFinal = "success", "finalized", true
	event.CurrencyCode, event.QuotaPerUnit, event.ExchangeRate = snapshot.CurrencyCode, snapshot.QuotaPerUnit, snapshot.ExchangeRate
	event.SettlementBPS, event.SalesBPS = snapshot.SettlementBPS, snapshot.SalesBPS
	event.CommissionEligible, event.CommissionSkipReason = eligible, snapshot.EligibilityReason
	event.StandardQuota, event.ChargedTotalQuota = standard, allocated
	event.PaidAllocatedQuota, event.NonpaidAllocatedQuota, event.DebtAllocatedQuota = 0, 0, 0
	event.CommissionQuota, event.CommissionAmountMicros, event.ReversedCommissionAmountMicros = 0, 0, 0
	// Keep the occurrence of the original call so the agency usage row stays
	// aligned with the platform consumption log.
	event.OccurredAtMS = journal.CreatedAtMS
	if event.OccurredAtMS <= 0 {
		event.OccurredAtMS = previous.OccurredAtMS
	}
	if eligible {
		if settlement > allocated {
			settlement = allocated
		}
		event.CommissionableQuota, event.NoncommissionableQuota = allocated, 0
		event.SettlementCostQuota = settlement
		event.TheoreticalCommissionQuota = allocated - settlement
	} else {
		event.CommissionableQuota, event.NoncommissionableQuota = 0, allocated
		event.SettlementCostQuota, event.TheoreticalCommissionQuota = 0, 0
	}
	totals, err := agencyChargeAllocationTotalsTx(tx, journal.UserID, journal.ChargeID)
	if err != nil {
		return plan, err
	}
	if totals[0]+totals[1]+totals[2] != allocated {
		return plan, fmt.Errorf("%w: funding allocation is inconsistent with the charge", ErrAgencyChargeCorrectionUnsupported)
	}
	event.PaidAllocatedQuota, event.NonpaidAllocatedQuota, event.DebtAllocatedQuota = totals[0], totals[1], totals[2]
	if eligible {
		event.CommissionQuota, err = agencycontract.CommissionForPaid(event.TheoreticalCommissionQuota, event.PaidAllocatedQuota, event.CommissionableQuota, true)
		if err != nil {
			return plan, err
		}
		event.CommissionAmountMicros, err = agencyFrozenCommissionMicros(event.CommissionQuota, snapshot)
		if err != nil {
			return plan, err
		}
	}
	plan.Journal, plan.Event = *journal, event
	return plan, nil
}

// agencyChargeCorrectionQuotasTx recovers the standard and settlement cost of
// the corrected charge. A task with a settled provider bill is recomputed at
// the frozen coefficients; every other charge keeps the proportions of its
// finalize receipt so no cost is invented.
func agencyChargeCorrectionQuotasTx(tx *gorm.DB, journal *AgencyBillingJournal, snapshot agencycontract.PricingSnapshot,
	previous agencycontract.BillingEvent, allocated int64) (int64, int64, error) {
	standard := agencyScaleCorrectionQuota(previous.StandardQuota, previous.ChargedTotalQuota, allocated)
	settlement := agencyScaleCorrectionQuota(previous.SettlementCostQuota, previous.ChargedTotalQuota, allocated)
	if rebuilt, ok := agencyChargeCorrectionRecomputeTx(tx, journal, snapshot, allocated); ok {
		standard, settlement = rebuilt.StandardQuota, rebuilt.SettlementCostQuota
	}
	if standard < allocated {
		standard = allocated
	}
	if settlement < 0 {
		settlement = 0
	}
	return standard, settlement, nil
}

func agencyChargeCorrectionRecomputeTx(tx *gorm.DB, journal *AgencyBillingJournal, snapshot agencycontract.PricingSnapshot,
	allocated int64) (agencycontract.BillingEvent, bool) {
	if journal.TaskID == nil || *journal.TaskID <= 0 {
		return agencycontract.BillingEvent{}, false
	}
	basis, err := agencyChargeFrozenTaskBasis(journal.BillingBasis)
	if err != nil || basis.Version != AgencyTaskChargeBasisVersion {
		return agencycontract.BillingEvent{}, false
	}
	var record TaskBillingReconciliation
	if err := tx.Where("task_id = ?", *journal.TaskID).First(&record).Error; err != nil {
		return agencycontract.BillingEvent{}, false
	}
	if record.Status != TaskBillingReconciliationSettled || record.TotalTokens <= 0 {
		return agencycontract.BillingEvent{}, false
	}
	rebuilt, err := AgencyTaskFinalCharge(basis, snapshot, record.TotalTokens, false)
	if err != nil || rebuilt.ChargedTotalQuota != allocated {
		return agencycontract.BillingEvent{}, false
	}
	return rebuilt, true
}

// agencyChargeFrozenTaskBasis accepts both frozen shapes: the reservation
// writes the basis directly, while a finalized charge stores it inside the
// billing snapshot envelope.
func agencyChargeFrozenTaskBasis(encoded string) (AgencyTaskChargeBasis, error) {
	var basis AgencyTaskChargeBasis
	if err := common.UnmarshalJsonStr(encoded, &basis); err != nil {
		return basis, err
	}
	if basis.Version != "" {
		return basis, nil
	}
	var completed struct {
		Basis AgencyTaskChargeBasis `json:"frozen_task_basis"`
	}
	if err := common.UnmarshalJsonStr(encoded, &completed); err != nil {
		return basis, err
	}
	return completed.Basis, nil
}

// agencyScaleCorrectionQuota keeps the frozen charge proportions when a
// charge is realigned to what the wallet paid. Both operands stay below the
// quota ceiling, so the product cannot overflow.
func agencyScaleCorrectionQuota(value, previousTotal, correctedTotal int64) int64 {
	if value <= 0 || previousTotal <= 0 || correctedTotal <= 0 {
		return 0
	}
	return value * correctedTotal / previousTotal
}

func agencyChargeAllocationTotalsTx(tx *gorm.DB, userID int64, chargeID string) ([3]int64, error) {
	var allocations []AgencyFundingAllocation
	if err := tx.Where("user_id = ? AND charge_id = ?", userID, chargeID).Find(&allocations).Error; err != nil {
		return [3]int64{}, err
	}
	var totals [3]int64
	for _, allocation := range allocations {
		paid, nonpaid, debt, err := agencyFundingAllocationActiveParts(allocation)
		if err != nil {
			return [3]int64{}, err
		}
		if totals[0] > int64(common.MaxQuota)-paid || totals[1] > int64(common.MaxQuota)-nonpaid ||
			totals[2] > int64(common.MaxQuota)-debt {
			return [3]int64{}, errors.New("agency correction allocation overflow")
		}
		totals[0] += paid
		totals[1] += nonpaid
		totals[2] += debt
	}
	return totals, nil
}

// CorrectAgencyTaskCharge rewrites one historical charge to the total the
// customer wallet already paid and emits an audited correction event for the
// agency projection. It is intentionally narrow:
//
//   - the corrected total is the active funding allocation, so the correction
//     can never move money;
//   - the charge must hold a canonical finalize receipt, which the correction
//     supersedes without deleting or rewriting any earlier evidence;
//   - the frozen basis and the settled provider bill keep usage, cost and
//     commission under the agreed policy;
//   - every run appends a journal revision, an immutable billing operation, an
//     outbox event and an audit row. A replay of the same target is inert.
func CorrectAgencyTaskCharge(chargeID string, operatorID int64, reason string) (AgencyChargeCorrectionResult, error) {
	chargeID = strings.TrimSpace(chargeID)
	result := AgencyChargeCorrectionResult{}
	if chargeID == "" {
		return result, ErrAgencyChargeCorrectionUnsupported
	}
	if utf8.RuneCountInString(reason) > 500 {
		reason = string([]rune(reason)[:500])
	}
	reason = strings.TrimSpace(reason)
	err := DB.Transaction(func(tx *gorm.DB) error {
		var journal AgencyBillingJournal
		if err := AgencyLockForUpdate(tx).Where("charge_id = ? AND segment_no = 0", chargeID).First(&journal).Error; err != nil {
			return err
		}
		allocated, err := agencyFundingAllocatedTx(tx, journal.UserID, chargeID)
		if err != nil {
			return err
		}
		result = AgencyChargeCorrectionResult{ChargeID: chargeID, UserID: journal.UserID, TaskID: journal.TaskID,
			AllocatedQuota: allocated, PreviousChargedTotalQuota: journal.ChargedTotalQuota,
			PreviousCommissionAmountMicros:  journal.CommissionAmountMicros,
			CorrectedChargedTotalQuota:      journal.ChargedTotalQuota,
			CorrectedCommissionAmountMicros: journal.CommissionAmountMicros,
			JournalRevision:                 journal.Revision}
		if allocated == journal.ChargedTotalQuota {
			result.AlreadyApplied = true
			return nil
		}
		plan, err := agencyChargeCorrectionPlanTx(tx, &journal, allocated)
		if err != nil {
			return err
		}
		var before AgencyFundingAccount
		if err := AgencyLockForUpdate(tx).Where("user_id = ?", journal.UserID).First(&before).Error; err != nil {
			return err
		}
		event := plan.Event
		inputHash, err := agencyOperationInputHash(event)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		// The journal's own projection must follow the corrected event, exactly
		// as the canonical wallet charge writer does, so later readers see the
		// amount the wallet paid instead of the superseded total.
		journal.Status, journal.BusinessStatus = "finalized", event.BusinessStatus
		journal.ChargedTotalQuota, journal.CommissionableQuota = event.ChargedTotalQuota, event.CommissionableQuota
		journal.SettlementCostQuota, journal.TheoreticalCommissionQuota = event.SettlementCostQuota, event.TheoreticalCommissionQuota
		journal.PaidAllocatedQuota, journal.CommissionQuota = event.PaidAllocatedQuota, event.CommissionQuota
		journal.CommissionAmountMicros, journal.BillingBasis = event.CommissionAmountMicros, event.BillingBasis
		journal.UpdatedAtMS = now
		if err := writeAgencyJournalEventTx(tx, &journal, &event, "correct", inputHash, before); err != nil {
			return err
		}
		correction := AgencyBillingCorrection{ChargeID: chargeID, CorrectedChargedTotalQuota: event.ChargedTotalQuota,
			UserID: journal.UserID, TaskID: journal.TaskID, OriginalEventID: plan.Previous.EventID,
			PreviousChargedTotalQuota:       result.PreviousChargedTotalQuota,
			PreviousCommissionAmountMicros:  result.PreviousCommissionAmountMicros,
			CorrectedCommissionAmountMicros: event.CommissionAmountMicros,
			JournalRevision:                 event.JournalRevision, EventID: event.EventID, OperationID: event.OperationID,
			Reason: reason, OperatorID: operatorID, CreatedAtMS: now}
		if err := tx.Create(&correction).Error; err != nil {
			return err
		}
		result.CorrectedChargedTotalQuota = event.ChargedTotalQuota
		result.CorrectedCommissionAmountMicros = event.CommissionAmountMicros
		result.JournalRevision = event.JournalRevision
		result.EventID, result.OperationID = event.EventID, event.OperationID
		return nil
	})
	if err != nil {
		return AgencyChargeCorrectionResult{}, err
	}
	return result, nil
}

// AgencyChargeCorrectionTargets scans finalized charges whose recorded total
// no longer matches the funding the wallet already paid. The cursor keeps a
// full backlog review restartable instead of truncating it to one page.
func AgencyChargeCorrectionTargets(cursor int64, limit int) ([]AgencyChargeCorrectionTarget, int64, bool, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	var journals []AgencyBillingJournal
	if err := DB.Where("id > ? AND segment_no = 0 AND status = ?", cursor, "finalized").
		Order("id").Limit(limit).Find(&journals).Error; err != nil {
		return nil, cursor, false, err
	}
	next := cursor
	targets := make([]AgencyChargeCorrectionTarget, 0)
	for i := range journals {
		journal := journals[i]
		next = journal.ID
		allocated, err := agencyFundingAllocatedTx(DB, journal.UserID, journal.ChargeID)
		if err != nil {
			return nil, next, false, err
		}
		if allocated == journal.ChargedTotalQuota {
			continue
		}
		target := AgencyChargeCorrectionTarget{ChargeID: journal.ChargeID, UserID: journal.UserID, TaskID: journal.TaskID,
			OccurredAtMS: journal.CreatedAtMS, PreviousChargedTotalQuota: journal.ChargedTotalQuota, AllocatedQuota: allocated}
		plan, err := agencyChargeCorrectionPlanTx(DB, &journal, allocated)
		if err != nil {
			target.Detail = err.Error()
		} else {
			target.Eligible, target.Detail = true, "ready"
			target.OriginModelName = plan.Event.OriginModelName
		}
		targets = append(targets, target)
	}
	return targets, next, len(journals) < limit, nil
}

// CorrectAgencyChargeBacklog applies the audited correction to every eligible
// charge found by the scan until the limit is reached or the backlog ends.
// Targets that cannot be corrected are reported instead of silently skipped,
// so an incomplete alignment stays visible to the operator.
func CorrectAgencyChargeBacklog(operatorID int64, reason string, limit int) ([]AgencyChargeCorrectionResult, []AgencyChargeCorrectionTarget, error) {
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	applied := make([]AgencyChargeCorrectionResult, 0, limit)
	blocked := make([]AgencyChargeCorrectionTarget, 0)
	cursor := int64(0)
	for len(applied) < limit {
		targets, next, done, err := AgencyChargeCorrectionTargets(cursor, 500)
		if err != nil {
			return applied, blocked, err
		}
		for _, target := range targets {
			if !target.Eligible {
				blocked = append(blocked, target)
				continue
			}
			result, err := CorrectAgencyTaskCharge(target.ChargeID, operatorID, reason)
			if err != nil {
				return applied, blocked, fmt.Errorf("charge %s: %w", target.ChargeID, err)
			}
			if !result.AlreadyApplied {
				applied = append(applied, result)
			}
			if len(applied) >= limit {
				break
			}
		}
		cursor = next
		if done || len(applied) >= limit {
			break
		}
	}
	return applied, blocked, nil
}

// StartAgencyChargeCorrectionBackfill realigns the historical agency records
// that still disagree with the amount the customer wallet actually paid. It is
// opt-in because it rewrites financial projections: a corrected charge keeps
// every earlier receipt and gains an immutable correction revision with an
// audit row, and the wallet itself is never touched. A later boot therefore
// finds nothing left to do.
func StartAgencyChargeCorrectionBackfill() {
	if !common.GetEnvOrDefaultBool("AGENCY_CHARGE_CORRECTION_BACKFILL_ENABLED", false) {
		return
	}
	go func() {
		applied, blocked, err := CorrectAgencyChargeBacklog(0,
			"release backfill: align the agency usage record with the wallet allocation", 5000)
		if err != nil {
			common.SysError("agency charge correction backfill failed: " + err.Error())
			return
		}
		for _, target := range blocked {
			common.SysError(fmt.Sprintf("agency charge correction blocked: charge=%s user=%d recorded=%d wallet=%d detail=%s",
				target.ChargeID, target.UserID, target.PreviousChargedTotalQuota, target.AllocatedQuota, target.Detail))
		}
		if len(applied) == 0 {
			common.SysLog(fmt.Sprintf("agency charge correction backfill: nothing to correct (%d blocked)", len(blocked)))
			return
		}
		var delta int64
		for _, item := range applied {
			delta += item.PreviousChargedTotalQuota - item.CorrectedChargedTotalQuota
		}
		common.SysLog(fmt.Sprintf("agency charge correction backfill: corrected %d charges, quota delta %d (%d blocked)",
			len(applied), delta, len(blocked)))
	}()
}
