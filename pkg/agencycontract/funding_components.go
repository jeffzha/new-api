package agencycontract

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
)

// ChargeComponent identifies one immutable charge within a billing operation.
// Its ID determines rounding ties and must remain unchanged during refunds.
type ChargeComponent struct {
	ComponentID  string `json:"component_id"`
	ChargedQuota int64  `json:"charged_quota"`
}

// ComponentFunding records the original sources of one charged component.
// These are consumed amounts, not current available account balances.
type ComponentFunding struct {
	ComponentID  string `json:"component_id"`
	ChargedQuota int64  `json:"charged_quota"`
	PaidQuota    int64  `json:"paid_quota"`
	NonpaidQuota int64  `json:"nonpaid_quota"`
	DebtQuota    int64  `json:"debt_quota"`
}

// ComponentRefund contains cumulative restored amounts. A database transaction
// applies only the difference from its persisted prior cumulative amounts.
type ComponentRefund struct {
	ComponentID          string `json:"component_id"`
	RefundedQuota        int64  `json:"refunded_quota"`
	RestoredPaidQuota    int64  `json:"restored_paid_quota"`
	RestoredNonpaidQuota int64  `json:"restored_nonpaid_quota"`
	RestoredDebtQuota    int64  `json:"restored_debt_quota"`
}

// AllocateComponentFunding applies paid_first_v1 to all charge components at
// once. Paid uses largest remainders against original charges; nonpaid uses
// largest remainders against the remaining charges; the remainder is debt.
// Results are ordered by ID without changing the caller's slice. The caller
// fills each source quota from its original FIFO lots in this same ID order.
func AllocateComponentFunding(components []ChargeComponent, paidQuota, nonpaidQuota int64) ([]ComponentFunding, error) {
	if len(components) == 0 {
		return nil, errors.New("charge components are required")
	}
	if paidQuota < 0 || nonpaidQuota < 0 {
		return nil, errors.New("component funding must not be negative")
	}
	ordered := append([]ChargeComponent(nil), components...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ComponentID < ordered[j].ComponentID })
	weights := make([]int64, len(ordered))
	var total int64
	for i, component := range ordered {
		if component.ComponentID == "" || strings.TrimSpace(component.ComponentID) != component.ComponentID {
			return nil, errors.New("invalid charge component ID")
		}
		if i > 0 && component.ComponentID == ordered[i-1].ComponentID {
			return nil, fmt.Errorf("duplicate charge component: %s", component.ComponentID)
		}
		if component.ChargedQuota < 0 || component.ChargedQuota > math.MaxInt64-total {
			return nil, errors.New("invalid or overflowing component charges")
		}
		total += component.ChargedQuota
		weights[i] = component.ChargedQuota
	}
	if paidQuota > total || nonpaidQuota > total-paidQuota {
		return nil, errors.New("component funding exceeds total charges")
	}
	paid := largestRemainderFunding(weights, total, paidQuota)
	for i := range weights {
		weights[i] -= paid[i]
	}
	nonpaid := largestRemainderFunding(weights, total-paidQuota, nonpaidQuota)
	result := make([]ComponentFunding, len(ordered))
	for i, component := range ordered {
		result[i] = ComponentFunding{
			ComponentID: component.ComponentID, ChargedQuota: component.ChargedQuota,
			PaidQuota: paid[i], NonpaidQuota: nonpaid[i], DebtQuota: weights[i] - nonpaid[i],
		}
	}
	return result, nil
}

// largestRemainderFunding requires nonnegative weights summing to total and
// 0 <= amount <= total. Weights already follow component ID order. Products
// use big integers: aggregate funding is int64 accounting, not a single
// gateway request quota that may be saturated to the int32 billing limit.
func largestRemainderFunding(weights []int64, total, amount int64) []int64 {
	result := make([]int64, len(weights))
	if amount == 0 {
		return result
	}
	indices := make([]int, len(weights))
	remainders := make([]big.Int, len(weights))
	denominator := big.NewInt(total)
	var allocated int64
	for i, weight := range weights {
		product := new(big.Int).Mul(big.NewInt(amount), big.NewInt(weight))
		quotient := new(big.Int)
		quotient.QuoRem(product, denominator, &remainders[i])
		result[i] = quotient.Int64()
		allocated += result[i]
		indices[i] = i
	}
	sort.Slice(indices, func(i, j int) bool {
		comparison := remainders[indices[i]].Cmp(&remainders[indices[j]])
		if comparison != 0 {
			return comparison > 0
		}
		return indices[i] < indices[j]
	})
	for _, index := range indices {
		if allocated == amount {
			break
		}
		result[index]++
		allocated++
	}
	return result
}

// CumulativeComponentRefunds distributes an unspecified cumulative refund
// using stable conditional proportions. Unlike independent largest-remainder
// allocations for each refund, increasing the cumulative refund cannot reduce
// any component's previously restored amount. The full refund exactly restores
// every original source. Explicit component refunds use CumulativeComponentRefund.
func CumulativeComponentRefunds(funding []ComponentFunding, cumulativeRefund int64) ([]ComponentRefund, error) {
	if len(funding) == 0 {
		return nil, errors.New("funded components are required")
	}
	ordered := append([]ComponentFunding(nil), funding...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ComponentID < ordered[j].ComponentID })
	var remainingCharge int64
	for i, component := range ordered {
		if err := validateComponentFunding(component); err != nil {
			return nil, err
		}
		if i > 0 && component.ComponentID == ordered[i-1].ComponentID {
			return nil, fmt.Errorf("duplicate charge component: %s", component.ComponentID)
		}
		if component.ChargedQuota > math.MaxInt64-remainingCharge {
			return nil, errors.New("component charges overflow")
		}
		remainingCharge += component.ChargedQuota
	}
	if cumulativeRefund < 0 || cumulativeRefund > remainingCharge {
		return nil, errors.New("cumulative refund is outside original charges")
	}
	result := make([]ComponentRefund, len(ordered))
	remainingRefund := cumulativeRefund
	for i, component := range ordered {
		var componentRefund int64
		if remainingCharge > 0 {
			componentRefund = conditionalRefundShare(remainingRefund, component.ChargedQuota, remainingCharge)
		}
		refund, err := CumulativeComponentRefund(component, componentRefund)
		if err != nil {
			return nil, err
		}
		result[i] = refund
		remainingRefund -= componentRefund
		remainingCharge -= component.ChargedQuota
	}
	return result, nil
}

// CumulativeComponentRefund restores one original component's sources using
// exact half-away rounding, paid first and then nonpaid/debt conditionally.
// It does not infer whether a debt lot was subsequently repaid: the caller
// resolves the returned original debt quota using persisted repayment records.
func CumulativeComponentRefund(funding ComponentFunding, cumulativeRefund int64) (ComponentRefund, error) {
	if err := validateComponentFunding(funding); err != nil {
		return ComponentRefund{}, err
	}
	if cumulativeRefund < 0 || cumulativeRefund > funding.ChargedQuota {
		return ComponentRefund{}, errors.New("cumulative refund is outside original component charge")
	}
	result := ComponentRefund{ComponentID: funding.ComponentID, RefundedQuota: cumulativeRefund}
	if cumulativeRefund == 0 {
		return result, nil
	}
	result.RestoredPaidQuota = conditionalRefundShare(cumulativeRefund, funding.PaidQuota, funding.ChargedQuota)
	remaining := cumulativeRefund - result.RestoredPaidQuota
	if remaining > 0 {
		result.RestoredNonpaidQuota = conditionalRefundShare(remaining, funding.NonpaidQuota, funding.ChargedQuota-funding.PaidQuota)
	}
	result.RestoredDebtQuota = remaining - result.RestoredNonpaidQuota
	return result, nil
}

func validateComponentFunding(component ComponentFunding) error {
	if component.ComponentID == "" || strings.TrimSpace(component.ComponentID) != component.ComponentID {
		return errors.New("invalid funded component ID")
	}
	if component.ChargedQuota < 0 || component.PaidQuota < 0 || component.NonpaidQuota < 0 || component.DebtQuota < 0 {
		return errors.New("component funding must not be negative")
	}
	if component.PaidQuota > component.ChargedQuota || component.NonpaidQuota > component.ChargedQuota-component.PaidQuota || component.DebtQuota != component.ChargedQuota-component.PaidQuota-component.NonpaidQuota {
		return errors.New("component funding does not equal original charge")
	}
	return nil
}

// conditionalRefundShare is exact RoundHalfAwayFromZero(amount*weight/total)
// for nonnegative validated values bounded by total. Quotients never exceed
// amount or weight, even when the intermediate product exceeds int64.
func conditionalRefundShare(amount, weight, total int64) int64 {
	product := new(big.Int).Mul(big.NewInt(amount), big.NewInt(weight))
	remainder := new(big.Int)
	denominator := big.NewInt(total)
	quotient := new(big.Int)
	quotient.QuoRem(product, denominator, remainder)
	if remainder.Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient.Int64()
}
