package agencycontract

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func componentContractFixture(parts ...BillingComponent) BillingEvent {
	if len(parts) == 0 {
		parts = []BillingComponent{{ComponentID: "model", StandardQuota: 1000, ChargedTotalQuota: 900,
			CommissionableQuota: 900, SettlementCostQuota: 750, TheoreticalCommissionQuota: 150,
			PaidAllocatedQuota: 600, NonpaidAllocatedQuota: 200, DebtAllocatedQuota: 100,
			CommissionQuota: 100, CommissionAmountMicros: 100, CommissionEligible: true}}
	}
	event := BillingEvent{SchemaVersion: ComponentSchemaVersion, EventType: "agency.billing_finalized",
		EventID: "component-contract-event", OperationID: "component-contract-operation", FinancialChargeID: "component-contract-charge",
		UserID: 1, MoneySeq: 1, JournalRevision: 1, EventCount: 1, BusinessStatus: "success", CurrencyCode: "TOKENS", Components: parts}
	for _, part := range parts {
		event.StandardQuota += part.StandardQuota
		event.ChargedTotalQuota += part.ChargedTotalQuota
		event.CommissionableQuota += part.CommissionableQuota
		event.NoncommissionableQuota += part.NoncommissionableQuota
		event.SettlementCostQuota += part.SettlementCostQuota
		event.TheoreticalCommissionQuota += part.TheoreticalCommissionQuota
		event.PaidAllocatedQuota += part.PaidAllocatedQuota
		event.CommissionQuota += part.CommissionQuota
		event.CommissionAmountMicros += part.CommissionAmountMicros
		event.ReversedCommissionAmountMicros += part.ReversedCommissionAmountMicros
		event.CommissionEligible = event.CommissionEligible || part.CommissionEligible
	}
	return event
}

func TestBillingComponentsVersionBoundaryPreservesLegacyAndRejectsSmuggling(t *testing.T) {
	legacy := BillingEvent{SchemaVersion: SchemaVersion, EventType: "agency.billing_reserved", StandardQuota: 123, PaidAllocatedQuota: 45}
	require.NoError(t, ValidateBillingComponents(legacy), "v1 is still validated by its existing consumer contract")
	for _, test := range []struct {
		name  string
		event BillingEvent
	}{
		{"v1 smuggled components", BillingEvent{SchemaVersion: SchemaVersion, Components: componentContractFixture().Components}},
		{"unknown legacy-like schema", BillingEvent{SchemaVersion: "agency-billing-v3"}},
		{"unknown component schema", BillingEvent{SchemaVersion: "agency-billing-v3", Components: componentContractFixture().Components}},
		{"v2 missing components", BillingEvent{SchemaVersion: ComponentSchemaVersion}},
		{"empty schema", BillingEvent{}},
	} {
		t.Run(test.name, func(t *testing.T) { require.Error(t, ValidateBillingComponents(test.event)) })
	}
}

func TestBillingComponentsFinalizedGoldenAndExactCurrencyConversion(t *testing.T) {
	event := componentContractFixture()
	require.NoError(t, ValidateBillingComponents(event))
	event.CurrencyCode, event.QuotaPerUnit, event.ExchangeRate = "CNY", "100000", "1.005"
	event.Components[0].CommissionAmountMicros = 1005
	event.CommissionAmountMicros = 1005
	require.NoError(t, ValidateBillingComponents(event))
	// Below one quota, currency conversion can still round to one micro.
	event = componentContractFixture(BillingComponent{ComponentID: "half", ChargedTotalQuota: 2, CommissionableQuota: 2,
		SettlementCostQuota: 1, TheoreticalCommissionQuota: 1, PaidAllocatedQuota: 1, DebtAllocatedQuota: 1,
		CommissionQuota: 1, CommissionAmountMicros: 1, CommissionEligible: true})
	event.CurrencyCode, event.QuotaPerUnit, event.ExchangeRate = "CNY", "2000000", "1"
	require.NoError(t, ValidateBillingComponents(event))
}

func TestBillingComponentsRejectSelfConsistentForgedFinalizedAmounts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*BillingComponent)
	}{
		{"negative standard", func(p *BillingComponent) { p.StandardQuota = -1 }},
		{"negative charged", func(p *BillingComponent) { p.ChargedTotalQuota = -1 }},
		{"negative paid", func(p *BillingComponent) { p.PaidAllocatedQuota = -1 }},
		{"negative nonpaid", func(p *BillingComponent) { p.NonpaidAllocatedQuota = -1 }},
		{"negative debt", func(p *BillingComponent) { p.DebtAllocatedQuota = -1 }},
		{"source under-allocation", func(p *BillingComponent) { p.DebtAllocatedQuota-- }},
		{"source over-allocation", func(p *BillingComponent) { p.NonpaidAllocatedQuota++ }},
		{"source sum overflow", func(p *BillingComponent) {
			p.PaidAllocatedQuota = math.MaxInt64
			p.NonpaidAllocatedQuota = math.MaxInt64
		}},
		{"charge split mismatch", func(p *BillingComponent) { p.NoncommissionableQuota = 1 }},
		{"mixed eligibility", func(p *BillingComponent) { p.CommissionableQuota--; p.NoncommissionableQuota++ }},
		{"settlement larger than charge", func(p *BillingComponent) { p.SettlementCostQuota = 901 }},
		{"forged theoretical basis", func(p *BillingComponent) { p.TheoreticalCommissionQuota++ }},
		{"forged paid commission", func(p *BillingComponent) { p.CommissionQuota++; p.CommissionAmountMicros++ }},
		{"forged currency amount", func(p *BillingComponent) { p.CommissionAmountMicros++ }},
		{"ineligible component earns", func(p *BillingComponent) { p.CommissionEligible = false }},
		{"finalize carries reversal", func(p *BillingComponent) { p.ReversedCommissionAmountMicros = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			part := componentContractFixture().Components[0]
			test.change(&part)
			// Keep the envelope equal to the forged component: arithmetic
			// integrity must not depend only on aggregate equality.
			require.Error(t, ValidateBillingComponents(componentContractFixture(part)))
		})
	}
	for _, status := range []string{"failed", "cancelled", "pending", ""} {
		event := componentContractFixture()
		event.BusinessStatus = status
		require.Error(t, ValidateBillingComponents(event), "a non-successful result cannot claim eligibility")
	}
}

func TestBillingComponentsRejectInvalidIdentityAndEnvelope(t *testing.T) {
	for _, id := range []string{"", " ", " padded", "padded ", strings.Repeat("a", 129), "a\x00b", string([]byte{0xff})} {
		event := componentContractFixture()
		event.Components[0].ComponentID = id
		require.Error(t, ValidateBillingComponents(event))
	}
	event := componentContractFixture()
	event.Components = append(event.Components, event.Components[0])
	require.Error(t, ValidateBillingComponents(event))
	event = componentContractFixture()
	event.ChargedTotalQuota++
	require.Error(t, ValidateBillingComponents(event))
	event = componentContractFixture()
	event.CommissionEligible = false
	require.Error(t, ValidateBillingComponents(event))
	event = componentContractFixture()
	event.EventType = "agency.topup_completed"
	require.Error(t, ValidateBillingComponents(event))
	oversized := componentContractFixture()
	oversized.Components = make([]BillingComponent, 129)
	require.Error(t, ValidateBillingComponents(oversized))
	// IDs intentionally retain exact case. Database keys must hash them
	// instead of relying on a case-insensitive text collation.
	event = componentContractFixture(BillingComponent{ComponentID: "a"}, BillingComponent{ComponentID: "A"}, BillingComponent{ComponentID: "模型"})
	require.NoError(t, ValidateBillingComponents(event))
}

func TestBillingComponentsRejectOverflowBeforeAggregateComparison(t *testing.T) {
	parts := []BillingComponent{
		{ComponentID: "a", ChargedTotalQuota: math.MaxInt64, NoncommissionableQuota: math.MaxInt64, PaidAllocatedQuota: math.MaxInt64},
		{ComponentID: "b", ChargedTotalQuota: 1, NoncommissionableQuota: 1, PaidAllocatedQuota: 1},
	}
	event := BillingEvent{SchemaVersion: ComponentSchemaVersion, EventType: "agency.billing_finalized", Components: parts}
	require.Error(t, ValidateBillingComponents(event))
	event = componentContractFixture()
	event.CurrencyCode, event.QuotaPerUnit, event.ExchangeRate = "CNY", "0.000000001", "1000000000000"
	require.Error(t, ValidateBillingComponents(event))
	for _, snapshot := range [][2]string{{"0", "1"}, {"-1", "1"}, {"1", "0"}, {"1", "-1"}, {"NaN", "1"}, {"1", "+Inf"}} {
		event = componentContractFixture()
		event.CurrencyCode, event.QuotaPerUnit, event.ExchangeRate = "CNY", snapshot[0], snapshot[1]
		require.Error(t, ValidateBillingComponents(event))
	}
}

func TestBillingComponentsRefundDeltasUseOriginalCumulativeRounding(t *testing.T) {
	// Original B=3, T=1, G=2, P=2, K=1. After cumulative refunds 1->2,
	// the refund delta is B=1/P=0/G=0/K=1. Recomputing commission from
	// this delta would incorrectly reject a valid original-basis reversal.
	part := BillingComponent{ComponentID: "model", ChargedTotalQuota: 1, CommissionableQuota: 1,
		NonpaidAllocatedQuota: 1, CommissionQuota: 1, ReversedCommissionAmountMicros: 1, CommissionEligible: true}
	event := componentContractFixture(part)
	event.EventType, event.OriginalEventID, event.BusinessStatus = "agency.billing_reversed", "original-finalize", "refunded"
	require.NoError(t, ValidateBillingComponents(event))
	// Micros can reverse even when the independently rounded quota delta
	// is zero. Original currency conversion is never recomputed here.
	event.Components[0].CommissionQuota, event.CommissionQuota = 0, 0
	require.NoError(t, ValidateBillingComponents(event))
	for _, original := range []string{"", " ", event.EventID} {
		invalid := event
		invalid.OriginalEventID = original
		require.Error(t, ValidateBillingComponents(invalid))
	}
	event.Components[0].CommissionAmountMicros, event.CommissionAmountMicros = 1, 1
	require.Error(t, ValidateBillingComponents(event), "refunds must not also produce new earnings")
}

func TestComponentEventProjectsAmountsWithoutLosingFinancialIdentity(t *testing.T) {
	event := componentContractFixture()
	event.OriginalEventID = "original"
	tokenID, agencyID, bindingID := int64(17), int64(18), int64(19)
	event.TokenID, event.AgencyID, event.BindingID = &tokenID, &agencyID, &bindingID
	part := BillingComponent{ComponentID: "fee", StandardQuota: 2, ChargedTotalQuota: 3, NoncommissionableQuota: 3,
		PaidAllocatedQuota: 1, NonpaidAllocatedQuota: 1, DebtAllocatedQuota: 1, CommissionSkipReason: "fee"}
	projected := ComponentEvent(event, part)
	assert.Nil(t, projected.Components)
	assert.Equal(t, event.EventID, projected.EventID)
	assert.Equal(t, event.OriginalEventID, projected.OriginalEventID)
	assert.Equal(t, event.FinancialChargeID, projected.FinancialChargeID)
	assert.Equal(t, event.OperationID, projected.OperationID)
	assert.Equal(t, event.TokenID, projected.TokenID)
	assert.Equal(t, event.AgencyID, projected.AgencyID)
	assert.Equal(t, event.BindingID, projected.BindingID)
	assert.Equal(t, int64(2), projected.StandardQuota)
	assert.Equal(t, int64(3), projected.ChargedTotalQuota)
	assert.Equal(t, int64(3), projected.NoncommissionableQuota)
	assert.Equal(t, int64(1), projected.PaidAllocatedQuota)
	assert.False(t, projected.CommissionEligible)
	assert.Zero(t, projected.CommissionQuota)
	assert.Equal(t, "fee", projected.CommissionSkipReason)
	require.Len(t, event.Components, 1, "projection must not mutate the source envelope")
}
