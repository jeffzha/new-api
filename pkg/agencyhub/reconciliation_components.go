package agencyhub

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

// Component evidence is checked inside the caller's consistent read snapshot.
// It never repairs financial rows or exposes their full billing basis. Original
// results must agree with the immutable gateway operation before they can be
// used to verify cumulative model refunds and their original source matrix.
func reconciliationComponentEvidence(tx *gorm.DB, objectID string, v *reconciliationVerification) error {
	id, err := strconv.ParseInt(objectID, 10, 64)
	if err != nil || id <= 0 {
		return gorm.ErrRecordNotFound
	}
	var component model.AgencyChargeComponent
	if err := tx.First(&component, id).Error; err != nil {
		return err
	}
	var journal model.AgencyBillingJournal
	if err := tx.Where("charge_id = ? AND segment_no = ?", component.ChargeID, component.SegmentNo).First(&journal).Error; err != nil {
		return err
	}
	v.check("component_journal_user", stringID(journal.UserID), stringID(component.UserID))
	v.check("component_journal_finalized", "true", strconv.FormatBool(journal.Status == "finalized" || journal.Status == "partially_reversed" || journal.Status == "reversed"))
	var operations []model.AgencyBillingOperation
	if err := tx.Where("charge_id = ? AND segment_no = ? AND operation = ?", component.ChargeID, component.SegmentNo, "finalize").Limit(2).Find(&operations).Error; err != nil {
		return err
	}
	v.check("component_has_one_finalization", "1", strconv.Itoa(len(operations)))
	if len(operations) != 1 {
		return nil
	}
	op := operations[0]
	var event agencycontract.BillingEvent
	validEvent := common.UnmarshalJsonStr(op.CommittedResult, &event) == nil &&
		event.SchemaVersion == agencycontract.ComponentSchemaVersion && event.EventType == "agency.billing_finalized" &&
		event.EventID != "" && event.OperationID == op.OperationID && event.FinancialChargeID == component.ChargeID &&
		event.SegmentNo == component.SegmentNo && event.UserID == component.UserID &&
		event.JournalRevision == op.Revision && event.MoneySeq == op.MoneySeq &&
		event.EventCount == 1 && op.EventCount == 1 && event.EventIndex == 0 && agencycontract.ValidateBillingComponents(event) == nil
	v.check("component_finalization_valid", "true", strconv.FormatBool(validEvent))
	if !validEvent {
		return nil
	}
	var original agencycontract.BillingComponent
	validOriginal := common.UnmarshalJsonStr(component.OriginalResult, &original) == nil
	v.check("component_original_result_decodes", "true", strconv.FormatBool(validOriginal))
	if !validOriginal {
		return nil
	}
	matched := false
	for _, part := range event.Components {
		if part.ComponentID == component.ComponentID {
			matched = part == original
			break
		}
	}
	v.check("component_original_matches_committed", "true", strconv.FormatBool(matched))
	v.check("component_identity", original.ComponentID, component.ComponentID)
	v.check("component_key", model.AgencyComponentKey(original.ComponentID), component.ComponentKey)
	if !matched {
		return nil
	}
	var count int64
	if err := tx.Model(&model.AgencyChargeComponent{}).Where("charge_id = ? AND segment_no = ?", component.ChargeID, component.SegmentNo).Count(&count).Error; err != nil {
		return err
	}
	v.check("component_count_matches_committed", strconv.Itoa(len(event.Components)), stringID(count))
	target, refundErr := agencycontract.CumulativeComponentRefund(agencycontract.ComponentFunding{
		ComponentID: original.ComponentID, ChargedQuota: original.ChargedTotalQuota,
		PaidQuota: original.PaidAllocatedQuota, NonpaidQuota: original.NonpaidAllocatedQuota, DebtQuota: original.DebtAllocatedQuota,
	}, component.RefundedQuota)
	v.check("component_refund_bound", "true", strconv.FormatBool(refundErr == nil))
	if refundErr == nil {
		v.check("component_refund_paid_target", stringID(target.RestoredPaidQuota), stringID(component.RestoredPaidQuota))
		v.check("component_refund_nonpaid_target", stringID(target.RestoredNonpaidQuota), stringID(component.RestoredNonpaidQuota))
		v.check("component_refund_debt_target", stringID(target.RestoredDebtQuota), stringID(component.RestoredDebtQuota))
		for _, amount := range []struct {
			name               string
			original, reversed int64
		}{
			{"component_refund_commission", original.CommissionQuota, component.ReversedCommissionQuota},
			{"component_refund_micros", original.CommissionAmountMicros, component.ReversedCommissionMicros},
		} {
			// Integer arithmetic also handles corrupt, oversized counters without
			// allowing overflow to turn a discrepancy into an apparent match.
			expected := new(big.Int)
			if component.RefundedQuota > 0 {
				numerator := new(big.Int).Mul(big.NewInt(amount.original), big.NewInt(component.RefundedQuota))
				remainder, denominator := new(big.Int), big.NewInt(original.ChargedTotalQuota)
				expected.QuoRem(numerator, denominator, remainder)
				if remainder.Lsh(remainder, 1).Cmp(denominator) >= 0 {
					expected.Add(expected, big.NewInt(1))
				}
			}
			v.check(amount.name, expected.String(), stringID(amount.reversed))
		}
	}

	var matrix []model.AgencyComponentFunding
	if err := tx.Where("charge_component_id = ?", component.ID).Order("id ASC").Find(&matrix).Error; err != nil {
		return err
	}
	var originalTotals, restoredTotals [3]big.Int
	remaining := [3]int64{component.RestoredPaidQuota, component.RestoredNonpaidQuota, component.RestoredDebtQuota}
	for _, row := range matrix {
		prefix := fmt.Sprintf("matrix_%d_", row.ID)
		amounts := [3]int64{row.PaidQuota, row.NonpaidQuota, row.DebtQuota}
		restored := [3]int64{row.RestoredPaidQuota, row.RestoredNonpaidQuota, row.RestoredDebtQuota}
		for i, source := range []string{"paid", "nonpaid", "debt"} {
			v.nonnegative(prefix+source, amounts[i])
			v.nonnegative(prefix+source+"_restored", restored[i])
			v.check(prefix+source+"_refund_bound", "true", strconv.FormatBool(restored[i] <= amounts[i]))
			originalTotals[i].Add(&originalTotals[i], big.NewInt(amounts[i]))
			restoredTotals[i].Add(&restoredTotals[i], big.NewInt(restored[i]))
			if remaining[i] >= 0 && amounts[i] >= 0 {
				expected := remaining[i]
				if amounts[i] < expected {
					expected = amounts[i]
				}
				v.check(prefix+source+"_refund_fifo", stringID(expected), stringID(restored[i]))
				remaining[i] -= expected
			}
		}
		var allocation model.AgencyFundingAllocation
		err := gorm.ErrRecordNotFound
		if row.AllocationID > 0 {
			err = tx.First(&allocation, row.AllocationID).Error
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		v.check(prefix+"allocation_exists", "true", strconv.FormatBool(err == nil))
		if err == nil {
			v.check(prefix+"allocation_user", stringID(component.UserID), stringID(allocation.UserID))
			v.check(prefix+"allocation_charge", component.ChargeID, allocation.ChargeID)
			v.check(prefix+"allocation_lot", stringID(allocation.LotID), stringID(row.LotID))
			if err := reconciliationComponentAllocationEvidence(tx, allocation, v); err != nil {
				return err
			}
		}
		if row.LotID > 0 {
			var lot model.AgencyFundingLot
			err := tx.Select("id, user_id").First(&lot, row.LotID).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			v.check(prefix+"lot_exists", "true", strconv.FormatBool(err == nil))
			if err == nil {
				v.check(prefix+"lot_user", stringID(component.UserID), stringID(lot.UserID))
			}
		} else {
			v.check(prefix+"no_lot_paid", "0", stringID(row.PaidQuota))
			v.check(prefix+"lot_identity_valid", "0", stringID(row.LotID))
		}
		stamp := fmt.Sprintf("%d:%d:%d:%d", row.ID, row.AllocationID, row.LotID, row.Version)
		v.check("component_matrix_snapshot", stamp, stamp)
	}
	originalAmounts := [3]int64{original.PaidAllocatedQuota, original.NonpaidAllocatedQuota, original.DebtAllocatedQuota}
	restoredAmounts := [3]int64{component.RestoredPaidQuota, component.RestoredNonpaidQuota, component.RestoredDebtQuota}
	for i, source := range []string{"paid", "nonpaid", "debt"} {
		v.check("component_"+source+"_matrix", stringID(originalAmounts[i]), originalTotals[i].String())
		v.check("component_"+source+"_restored_matrix", stringID(restoredAmounts[i]), restoredTotals[i].String())
	}
	total := new(big.Int).Add(&originalTotals[0], &originalTotals[1])
	total.Add(total, &originalTotals[2])
	v.check("component_charge_conservation", stringID(original.ChargedTotalQuota), total.String())
	v.check("component_version", stringID(component.Version), stringID(component.Version))
	return nil
}

// Several components may share an allocation. Compare all their remaining
// source amounts together so one row cannot spend another component's funds.
func reconciliationComponentAllocationEvidence(tx *gorm.DB, allocation model.AgencyFundingAllocation, v *reconciliationVerification) error {
	var matrix []model.AgencyComponentFunding
	if err := tx.Where("allocation_id = ?", allocation.ID).Order("id ASC").Find(&matrix).Error; err != nil {
		return err
	}
	var totals [3]big.Int
	for _, row := range matrix {
		original := [3]int64{row.PaidQuota, row.NonpaidQuota, row.DebtQuota}
		restored := [3]int64{row.RestoredPaidQuota, row.RestoredNonpaidQuota, row.RestoredDebtQuota}
		for i := range original {
			totals[i].Add(&totals[i], big.NewInt(original[i]))
			totals[i].Sub(&totals[i], big.NewInt(restored[i]))
		}
	}
	prefix := fmt.Sprintf("allocation_%d_", allocation.ID)
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"reserved", allocation.Reserved}, {"consumed", allocation.Consumed}, {"nonpaid_consumed", allocation.NonpaidConsumed},
		{"debt_consumed", allocation.DebtConsumed}, {"reversed", allocation.Reversed},
		{"revoked_reserved_debt", allocation.RevokedReservedDebt}, {"revoked_nonpaid", allocation.RevokedNonpaid},
	} {
		v.nonnegative(prefix+field.name, field.value)
	}
	paidBase := allocation.Consumed
	if allocation.Reserved > paidBase {
		paidBase = allocation.Reserved
	}
	paid := new(big.Int).Sub(big.NewInt(paidBase), big.NewInt(allocation.Reversed))
	paid.Sub(paid, big.NewInt(allocation.RevokedReservedDebt))
	nonpaid := new(big.Int).Sub(big.NewInt(allocation.NonpaidConsumed), big.NewInt(allocation.RevokedNonpaid))
	v.check(prefix+"active_paid_nonnegative", "true", strconv.FormatBool(paid.Sign() >= 0))
	v.check(prefix+"active_nonpaid_nonnegative", "true", strconv.FormatBool(nonpaid.Sign() >= 0))
	v.check(prefix+"paid_remaining_matrix", paid.String(), totals[0].String())
	v.check(prefix+"nonpaid_remaining_matrix", nonpaid.String(), totals[1].String())
	v.check(prefix+"debt_remaining_matrix", stringID(allocation.DebtConsumed), totals[2].String())
	v.check(prefix+"version", stringID(allocation.Version), stringID(allocation.Version))
	return nil
}

// Finalization is scanned even when every mutable component row is missing.
// This also binds aggregate refund watermarks to their authoritative journal;
// the historical journal column ReversedCommissionQuota stores currency micros.
func reconciliationComponentJournalEvidence(tx *gorm.DB, event agencycontract.BillingEvent, v *reconciliationVerification) error {
	var journal model.AgencyBillingJournal
	err := tx.Where("charge_id = ? AND segment_no = ?", event.FinancialChargeID, event.SegmentNo).First(&journal).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	v.check("component_journal_exists", "true", strconv.FormatBool(err == nil))
	if err != nil {
		return nil
	}
	v.check("journal_component_user", stringID(event.UserID), stringID(journal.UserID))
	v.check("journal_component_currency", event.CurrencyCode, journal.CurrencyCode)
	v.check("journal_original_charge", stringID(event.ChargedTotalQuota), stringID(journal.ChargedTotalQuota))
	v.check("journal_original_commission", stringID(event.CommissionAmountMicros), stringID(journal.CommissionAmountMicros))
	var components []model.AgencyChargeComponent
	if err := tx.Where("charge_id = ? AND segment_no = ?", event.FinancialChargeID, event.SegmentNo).Order("id ASC").Limit(129).Find(&components).Error; err != nil {
		return err
	}
	v.check("journal_component_count", strconv.Itoa(len(event.Components)), strconv.Itoa(len(components)))
	refunded, reversed := new(big.Int), new(big.Int)
	for _, row := range components {
		refunded.Add(refunded, big.NewInt(row.RefundedQuota))
		reversed.Add(reversed, big.NewInt(row.ReversedCommissionMicros))
		v.nonnegative("journal_component_refund", row.RefundedQuota)
		v.nonnegative("journal_component_reversal", row.ReversedCommissionMicros)
		stamp := fmt.Sprintf("%d:%s:%d:%d:%d", row.ID, row.ComponentKey, row.RefundedQuota, row.ReversedCommissionMicros, row.Version)
		v.check("journal_component_snapshot", stamp, stamp)
	}
	v.check("journal_refunded_quota", refunded.String(), stringID(journal.ReversedQuota))
	v.check("journal_reversed_micros", reversed.String(), stringID(journal.ReversedCommissionQuota))
	return nil
}
