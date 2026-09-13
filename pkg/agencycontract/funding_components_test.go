package agencycontract

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllocateComponentFundingUsesRemainingChargeForNonpaid(t *testing.T) {
	input := []ChargeComponent{{"c", 3}, {"a", 7}, {"free", 0}, {"b", 5}}
	result, err := AllocateComponentFunding(input, 7, 5)
	require.NoError(t, err)
	assert.Equal(t, []ComponentFunding{
		{"a", 7, 3, 2, 2}, {"b", 5, 2, 2, 1}, {"c", 3, 2, 1, 0}, {"free", 0, 0, 0, 0},
	}, result)
	assert.Equal(t, []ChargeComponent{{"c", 3}, {"a", 7}, {"free", 0}, {"b", 5}}, input, "caller order must remain unchanged")
}

func TestAllocateComponentFundingTiesUseStableIDsAndIgnoreInputOrder(t *testing.T) {
	orders := [][]ChargeComponent{
		{{"a", 1}, {"b", 1}, {"c", 1}},
		{{"c", 1}, {"a", 1}, {"b", 1}},
		{{"b", 1}, {"c", 1}, {"a", 1}},
	}
	for _, input := range orders {
		result, err := AllocateComponentFunding(input, 1, 1)
		require.NoError(t, err)
		assert.Equal(t, []ComponentFunding{{"a", 1, 1, 0, 0}, {"b", 1, 0, 1, 0}, {"c", 1, 0, 0, 1}}, result)
	}
}

func TestAllocateComponentFundingAllowsZeroAndSingleSources(t *testing.T) {
	for _, test := range []struct {
		name    string
		charge  int64
		paid    int64
		nonpaid int64
		want    ComponentFunding
	}{
		{"free", 0, 0, 0, ComponentFunding{"a", 0, 0, 0, 0}},
		{"all paid", 9, 9, 0, ComponentFunding{"a", 9, 9, 0, 0}},
		{"all nonpaid", 9, 0, 9, ComponentFunding{"a", 9, 0, 9, 0}},
		{"all debt", 9, 0, 0, ComponentFunding{"a", 9, 0, 0, 9}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := AllocateComponentFunding([]ChargeComponent{{"a", test.charge}}, test.paid, test.nonpaid)
			require.NoError(t, err)
			assert.Equal(t, []ComponentFunding{test.want}, result)
		})
	}
}

func TestAllocateComponentFundingRejectsInvalidMoneyAndIdentity(t *testing.T) {
	for _, test := range []struct {
		name       string
		components []ChargeComponent
		paid       int64
		nonpaid    int64
	}{
		{"no components", nil, 0, 0},
		{"empty ID", []ChargeComponent{{"", 1}}, 0, 0},
		{"blank ID", []ChargeComponent{{"\t ", 1}}, 0, 0},
		{"padded ID", []ChargeComponent{{" a", 1}}, 0, 0},
		{"duplicate ID", []ChargeComponent{{"a", 1}, {"a", 2}}, 0, 0},
		{"negative charge", []ChargeComponent{{"a", -1}}, 0, 0},
		{"negative paid", []ChargeComponent{{"a", 1}}, -1, 0},
		{"negative nonpaid", []ChargeComponent{{"a", 1}}, 0, -1},
		{"paid over charge", []ChargeComponent{{"a", 1}}, 2, 0},
		{"nonpaid over remainder", []ChargeComponent{{"a", 2}}, 1, 2},
		{"funding a free charge", []ChargeComponent{{"a", 0}}, 0, 1},
		{"total charge overflow", []ChargeComponent{{"a", math.MaxInt64}, {"b", 1}}, 0, 0},
		{"source sum overflow", []ChargeComponent{{"a", math.MaxInt64}}, math.MaxInt64, math.MaxInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := AllocateComponentFunding(test.components, test.paid, test.nonpaid)
			require.Error(t, err)
			assert.Nil(t, result)
		})
	}
}

func TestCumulativeComponentRefundsGoldenSources(t *testing.T) {
	funding := []ComponentFunding{{"c", 3, 2, 1, 0}, {"free", 0, 0, 0, 0}, {"a", 7, 3, 2, 2}, {"b", 5, 2, 2, 1}}
	result, err := CumulativeComponentRefunds(funding, 6)
	require.NoError(t, err)
	assert.Equal(t, []ComponentRefund{
		{"a", 3, 1, 1, 1}, {"b", 2, 1, 1, 0}, {"c", 1, 1, 0, 0}, {"free", 0, 0, 0, 0},
	}, result)
	assert.Equal(t, "c", funding[0].ComponentID)
	full, err := CumulativeComponentRefunds(funding, 15)
	require.NoError(t, err)
	assert.Equal(t, []ComponentRefund{
		{"a", 7, 3, 2, 2}, {"b", 5, 2, 2, 1}, {"c", 3, 2, 1, 0}, {"free", 0, 0, 0, 0},
	}, full)
}

func TestCumulativeComponentRefundsAvoidLargestRemainderRegression(t *testing.T) {
	// With charges 5,3,1, independent largest remainders refund c=1 at total
	// 4, then c=0 at total 5. Stable conditional proportions must never ask
	// the customer to return a previously restored component or source.
	funding := []ComponentFunding{{"c", 1, 1, 0, 0}, {"a", 5, 2, 2, 1}, {"b", 3, 1, 1, 1}}
	wantCharges := [][3]int64{
		{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {2, 1, 0}, {2, 2, 0},
		{3, 2, 0}, {3, 2, 1}, {4, 2, 1}, {4, 3, 1}, {5, 3, 1},
	}
	previous := make([]ComponentRefund, 3)
	var restoredByIncrements [3]ComponentRefund
	for cumulative, want := range wantCharges {
		result, err := CumulativeComponentRefunds(funding, int64(cumulative))
		require.NoError(t, err)
		require.Len(t, result, 3)
		assert.Equal(t, want, [3]int64{result[0].RefundedQuota, result[1].RefundedQuota, result[2].RefundedQuota})
		var total int64
		for i, refund := range result {
			assert.GreaterOrEqual(t, refund.RefundedQuota, previous[i].RefundedQuota)
			assert.GreaterOrEqual(t, refund.RestoredPaidQuota, previous[i].RestoredPaidQuota)
			assert.GreaterOrEqual(t, refund.RestoredNonpaidQuota, previous[i].RestoredNonpaidQuota)
			assert.GreaterOrEqual(t, refund.RestoredDebtQuota, previous[i].RestoredDebtQuota)
			assert.Equal(t, refund.RefundedQuota, refund.RestoredPaidQuota+refund.RestoredNonpaidQuota+refund.RestoredDebtQuota)
			total += refund.RefundedQuota
			restoredByIncrements[i].ComponentID = refund.ComponentID
			restoredByIncrements[i].RefundedQuota += refund.RefundedQuota - previous[i].RefundedQuota
			restoredByIncrements[i].RestoredPaidQuota += refund.RestoredPaidQuota - previous[i].RestoredPaidQuota
			restoredByIncrements[i].RestoredNonpaidQuota += refund.RestoredNonpaidQuota - previous[i].RestoredNonpaidQuota
			restoredByIncrements[i].RestoredDebtQuota += refund.RestoredDebtQuota - previous[i].RestoredDebtQuota
		}
		assert.Equal(t, int64(cumulative), total)
		previous = result
	}
	assert.Equal(t, [3]ComponentRefund{{"a", 5, 2, 2, 1}, {"b", 3, 1, 1, 1}, {"c", 1, 1, 0, 0}}, restoredByIncrements)
	reordered := []ComponentFunding{funding[1], funding[2], funding[0]}
	result, err := CumulativeComponentRefunds(reordered, 4)
	require.NoError(t, err)
	assert.Equal(t, []ComponentRefund{{"a", 2, 1, 1, 0}, {"b", 2, 1, 1, 0}, {"c", 0, 0, 0, 0}}, result)
}

func TestCumulativeComponentRefundRoundsHalfAwayWithinOriginalSources(t *testing.T) {
	for _, test := range []struct {
		name    string
		funding ComponentFunding
		want    ComponentRefund
	}{
		{"paid before debt", ComponentFunding{"a", 2, 1, 0, 1}, ComponentRefund{"a", 1, 1, 0, 0}},
		{"nonpaid before debt", ComponentFunding{"a", 2, 0, 1, 1}, ComponentRefund{"a", 1, 0, 1, 0}},
		{"paid before nonpaid", ComponentFunding{"a", 2, 1, 1, 0}, ComponentRefund{"a", 1, 1, 0, 0}},
		{"pure debt", ComponentFunding{"a", 2, 0, 0, 2}, ComponentRefund{"a", 1, 0, 0, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := CumulativeComponentRefund(test.funding, 1)
			require.NoError(t, err)
			assert.Equal(t, test.want, result)
		})
	}
}

func TestComponentFundingAndRefundPreserveInt64Precision(t *testing.T) {
	const maximum = int64(math.MaxInt64)
	funding, err := AllocateComponentFunding([]ChargeComponent{{"b", 1}, {"a", maximum - 1}}, maximum-2, 1)
	require.NoError(t, err)
	assert.Equal(t, []ComponentFunding{{"a", maximum - 1, maximum - 3, 1, 1}, {"b", 1, 1, 0, 0}}, funding)
	partial, err := CumulativeComponentRefunds(funding, maximum-1)
	require.NoError(t, err)
	assert.Equal(t, []ComponentRefund{{"a", maximum - 2, maximum - 4, 1, 1}, {"b", 1, 1, 0, 0}}, partial)
	full, err := CumulativeComponentRefunds(funding, maximum)
	require.NoError(t, err)
	assert.Equal(t, []ComponentRefund{{"a", maximum - 1, maximum - 3, 1, 1}, {"b", 1, 1, 0, 0}}, full)
}

func TestCumulativeComponentRefundRejectsInvalidSnapshotsAndRefunds(t *testing.T) {
	for _, test := range []struct {
		name    string
		funding ComponentFunding
		refund  int64
	}{
		{"empty ID", ComponentFunding{"", 1, 1, 0, 0}, 0},
		{"blank ID", ComponentFunding{" ", 1, 1, 0, 0}, 0},
		{"negative charge", ComponentFunding{"a", -1, 0, 0, 0}, 0},
		{"negative paid", ComponentFunding{"a", 1, -1, 1, 1}, 0},
		{"negative nonpaid", ComponentFunding{"a", 1, 1, -1, 1}, 0},
		{"negative debt", ComponentFunding{"a", 1, 1, 1, -1}, 0},
		{"sources smaller than charge", ComponentFunding{"a", 2, 1, 0, 0}, 0},
		{"paid exceeds charge", ComponentFunding{"a", 1, 2, 0, 0}, 0},
		{"nonpaid exceeds remainder", ComponentFunding{"a", 1, 1, 1, 0}, 0},
		{"overflowing sources", ComponentFunding{"a", math.MaxInt64, math.MaxInt64, math.MaxInt64, 0}, 0},
		{"negative refund", ComponentFunding{"a", 1, 1, 0, 0}, -1},
		{"refund exceeds charge", ComponentFunding{"a", 1, 1, 0, 0}, 2},
		{"positive refund of free component", ComponentFunding{"a", 0, 0, 0, 0}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := CumulativeComponentRefund(test.funding, test.refund)
			require.Error(t, err)
			_, err = CumulativeComponentRefunds([]ComponentFunding{test.funding}, test.refund)
			require.Error(t, err)
		})
	}
	for _, funding := range [][]ComponentFunding{
		nil,
		{{"a", 1, 1, 0, 0}, {"a", 1, 1, 0, 0}},
		{{"a", math.MaxInt64, math.MaxInt64, 0, 0}, {"b", 1, 1, 0, 0}},
	} {
		_, err := CumulativeComponentRefunds(funding, 0)
		require.Error(t, err)
	}
	zero, err := CumulativeComponentRefunds([]ComponentFunding{{"free", 0, 0, 0, 0}}, 0)
	require.NoError(t, err)
	assert.Equal(t, []ComponentRefund{{"free", 0, 0, 0, 0}}, zero)
}
