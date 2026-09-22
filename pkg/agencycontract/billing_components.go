package agencycontract

import (
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// BillingComponent freezes one independently refundable part of a charge.
// Source allocation and commission are gateway results, never client prices.
type BillingComponent struct {
	ComponentID                    string `json:"component_id"`
	StandardQuota                  int64  `json:"standard_quota"`
	ChargedTotalQuota              int64  `json:"charged_total_quota"`
	CommissionableQuota            int64  `json:"commissionable_charged_quota"`
	NoncommissionableQuota         int64  `json:"noncommissionable_quota"`
	SettlementCostQuota            int64  `json:"settlement_cost_quota"`
	TheoreticalCommissionQuota     int64  `json:"theoretical_commission_quota"`
	PaidAllocatedQuota             int64  `json:"paid_allocated_quota"`
	NonpaidAllocatedQuota          int64  `json:"nonpaid_allocated_quota"`
	DebtAllocatedQuota             int64  `json:"debt_allocated_quota"`
	CommissionQuota                int64  `json:"commission_quota"`
	CommissionAmountMicros         int64  `json:"commission_amount_micros"`
	ReversedCommissionAmountMicros int64  `json:"reversed_commission_amount_micros"`
	CommissionEligible             bool   `json:"commission_eligible"`
	CommissionSkipReason           string `json:"commission_skip_reason,omitempty"`
}

// ComponentEvent projects a component without changing its source identity.
// Consumers keep a single receipt for the envelope and key financial rows by
// the original event ID plus ComponentID.
func ComponentEvent(event BillingEvent, component BillingComponent) BillingEvent {
	event.Components = nil
	event.StandardQuota = component.StandardQuota
	event.ChargedTotalQuota = component.ChargedTotalQuota
	event.CommissionableQuota = component.CommissionableQuota
	event.NoncommissionableQuota = component.NoncommissionableQuota
	event.SettlementCostQuota = component.SettlementCostQuota
	event.TheoreticalCommissionQuota = component.TheoreticalCommissionQuota
	event.PaidAllocatedQuota = component.PaidAllocatedQuota
	event.NonpaidAllocatedQuota = component.NonpaidAllocatedQuota
	event.DebtAllocatedQuota = component.DebtAllocatedQuota
	event.CommissionQuota = component.CommissionQuota
	event.CommissionAmountMicros = component.CommissionAmountMicros
	event.ReversedCommissionAmountMicros = component.ReversedCommissionAmountMicros
	event.CommissionEligible = component.CommissionEligible
	event.CommissionSkipReason = component.CommissionSkipReason
	return event
}

// ValidateBillingComponents binds every component to the envelope totals.
// v1 payloads cannot carry components: an old consumer would ignore them.
func ValidateBillingComponents(event BillingEvent) error {
	if err := ValidateCommissionSplits(event); err != nil {
		return err
	}
	if event.SchemaVersion == SchemaVersion && len(event.Components) == 0 {
		return nil
	}
	if event.SchemaVersion != ComponentSchemaVersion || len(event.Components) == 0 || len(event.Components) > 128 {
		return errors.New("invalid billing component schema")
	}
	isReversal := event.EventType == "agency.billing_reversed"
	if event.EventType != "" && event.EventType != "agency.billing_finalized" && !isReversal {
		return errors.New("invalid billing component event type")
	}
	if isReversal && (strings.TrimSpace(event.OriginalEventID) == "" || event.OriginalEventID == event.EventID) {
		return errors.New("billing component reversal requires original event")
	}
	seen := make(map[string]bool, len(event.Components))
	totals := make([]int64, 12)
	eligible := false
	for _, part := range event.Components {
		if part.ComponentID == "" || strings.TrimSpace(part.ComponentID) != part.ComponentID || len(part.ComponentID) > 128 || !utf8.ValidString(part.ComponentID) || strings.ContainsRune(part.ComponentID, '\x00') || seen[part.ComponentID] {
			return errors.New("invalid billing component identity")
		}
		seen[part.ComponentID] = true
		values := []int64{part.StandardQuota, part.ChargedTotalQuota, part.CommissionableQuota, part.NoncommissionableQuota, part.SettlementCostQuota, part.TheoreticalCommissionQuota, part.PaidAllocatedQuota, part.NonpaidAllocatedQuota, part.DebtAllocatedQuota, part.CommissionQuota, part.CommissionAmountMicros, part.ReversedCommissionAmountMicros}
		for i, value := range values {
			if value < 0 || totals[i] > math.MaxInt64-value {
				return errors.New("billing component amount overflow")
			}
			totals[i] += value
		}
		if part.CommissionableQuota > part.ChargedTotalQuota || part.NoncommissionableQuota != part.ChargedTotalQuota-part.CommissionableQuota ||
			part.PaidAllocatedQuota > part.ChargedTotalQuota || part.NonpaidAllocatedQuota < 0 || part.DebtAllocatedQuota < 0 ||
			part.NonpaidAllocatedQuota > part.ChargedTotalQuota-part.PaidAllocatedQuota || part.DebtAllocatedQuota != part.ChargedTotalQuota-part.PaidAllocatedQuota-part.NonpaidAllocatedQuota {
			return errors.New("billing component funding does not conserve charge")
		}
		if !part.CommissionEligible && (part.CommissionableQuota != 0 || part.TheoreticalCommissionQuota != 0 || part.CommissionQuota != 0 || part.CommissionAmountMicros != 0 || part.ReversedCommissionAmountMicros != 0) {
			return errors.New("ineligible billing component has commission")
		}
		if part.CommissionEligible && part.NoncommissionableQuota != 0 {
			return errors.New("mixed commission eligibility within one component")
		}
		if isReversal {
			// Refund quantities are differences of independently rounded
			// cumulative values against the original component. Recomputing
			// G=B-T, K=G*P/B or micros from these deltas would reject valid
			// refunds and can over-reverse prior commission.
			if part.CommissionAmountMicros != 0 {
				return errors.New("billing component reversal cannot earn commission")
			}
		} else {
			if part.ReversedCommissionAmountMicros != 0 {
				return errors.New("finalized billing component cannot reverse commission")
			}
			if part.CommissionEligible {
				if event.BusinessStatus != "success" || part.SettlementCostQuota > part.CommissionableQuota || part.TheoreticalCommissionQuota != part.CommissionableQuota-part.SettlementCostQuota {
					return errors.New("invalid billing component commission basis")
				}
				var expectedCommission int64
				if part.CommissionableQuota > 0 {
					expectedCommission = conditionalRefundShare(part.TheoreticalCommissionQuota, part.PaidAllocatedQuota, part.CommissionableQuota)
				}
				if part.CommissionQuota != expectedCommission {
					return errors.New("billing component paid commission mismatch")
				}
				expectedMicros := part.CommissionQuota
				if event.CurrencyCode != "TOKENS" {
					unit, unitErr := decimal.NewFromString(event.QuotaPerUnit)
					rate, rateErr := decimal.NewFromString(event.ExchangeRate)
					if strings.TrimSpace(event.CurrencyCode) == "" || unitErr != nil || rateErr != nil || !unit.IsPositive() || !rate.IsPositive() {
						return errors.New("invalid billing component currency snapshot")
					}
					// Match the frozen gateway currency conversion. The quota
					// and amount are validated independently, since their
					// cumulative refund deltas may round at different steps.
					amount := decimal.NewFromInt(part.CommissionQuota).Div(unit).Mul(rate).Mul(decimal.NewFromInt(1000000)).Round(0)
					if amount.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
						return errors.New("billing component commission micros overflow")
					}
					expectedMicros = amount.IntPart()
				}
				if part.CommissionAmountMicros != expectedMicros {
					return errors.New("billing component currency amount mismatch")
				}
			}
		}
		eligible = eligible || part.CommissionEligible
	}
	wanted := []int64{event.StandardQuota, event.ChargedTotalQuota, event.CommissionableQuota, event.NoncommissionableQuota, event.SettlementCostQuota, event.TheoreticalCommissionQuota, event.PaidAllocatedQuota, event.NonpaidAllocatedQuota, event.DebtAllocatedQuota, event.CommissionQuota, event.CommissionAmountMicros, event.ReversedCommissionAmountMicros}
	for i := range totals {
		// The first component-schema rollout did not include aggregate
		// nonpaid/debt counters on the envelope. Per-component conservation is
		// still enforced above; accept that legacy envelope shape while
		// enforcing the fields whenever either side carries a value.
		if (i == 7 || i == 8) && totals[i] != 0 && wanted[i] == 0 {
			continue
		}
		if totals[i] != wanted[i] {
			return errors.New("billing component aggregate mismatch")
		}
	}
	if eligible != event.CommissionEligible {
		return errors.New("billing component eligibility mismatch")
	}
	return nil
}

// ValidateCommissionSplits binds tiered commission rows to the envelope
// totals. Empty splits are the legacy single-agency representation.
func ValidateCommissionSplits(event BillingEvent) error {
	if len(event.CommissionSplits) == 0 {
		return nil
	}
	if len(event.CommissionSplits) > 128 {
		return errors.New("too many commission splits")
	}
	seen := make(map[int64]struct{}, len(event.CommissionSplits))
	var theoretical, commission, micros, reversedMicros int64
	for index, split := range event.CommissionSplits {
		if split.AgencyID <= 0 || split.CostBPS < 0 || split.CostBPS > MaxCoefficientBPS || split.Depth <= 0 || split.TheoreticalQuota < 0 || split.PaidAllocatedQuota < 0 || split.CommissionQuota < 0 || split.CommissionAmountMicros < 0 || split.ReversedCommissionAmountMicros < 0 {
			return errors.New("invalid commission split")
		}
		if _, exists := seen[split.AgencyID]; exists {
			return errors.New("duplicate commission split agency")
		}
		seen[split.AgencyID] = struct{}{}
		if index > 0 && split.Depth <= event.CommissionSplits[index-1].Depth {
			return errors.New("commission split depths must increase")
		}
		var err error
		theoretical, err = addChecked(theoretical, split.TheoreticalQuota)
		if err != nil {
			return err
		}
		commission, err = addChecked(commission, split.CommissionQuota)
		if err != nil {
			return err
		}
		micros, err = addChecked(micros, split.CommissionAmountMicros)
		if err != nil {
			return err
		}
		reversedMicros, err = addChecked(reversedMicros, split.ReversedCommissionAmountMicros)
		if err != nil {
			return err
		}
	}
	if event.EventType == "agency.billing_reversed" {
		if micros != 0 || reversedMicros != event.ReversedCommissionAmountMicros {
			return errors.New("commission split reversal totals mismatch")
		}
		return nil
	}
	if theoretical != event.TheoreticalCommissionQuota || commission != event.CommissionQuota || micros != event.CommissionAmountMicros || reversedMicros != 0 {
		return errors.New("commission split totals mismatch")
	}
	return nil
}

func addChecked(current, value int64) (int64, error) {
	if value < 0 || current > math.MaxInt64-value {
		return 0, errors.New("commission split amount overflow")
	}
	return current + value, nil
}
