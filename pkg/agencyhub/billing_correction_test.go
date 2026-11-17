package agencyhub

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func agencyV1BillingFixture() agencycontract.BillingEvent {
	agencyID, bindingID := int64(7810), int64(12)
	return agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: "v1-billing-original",
		OperationID: "v1-billing-finalize-operation", EventType: "agency.billing_finalized",
		FinancialChargeID: "v1-billing-charge", JournalRevision: 1, MoneySeq: 1, EventCount: 1,
		UserID: 781001, AgencyID: &agencyID, BindingID: &bindingID, OriginModelName: "doubao-seedance-2-0-260128",
		Endpoint: "/v1/videos", BusinessStatus: "success", BillingStatus: "finalized", FinancialFinal: true,
		OccurredAtMS: 1789272000000, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1",
		CommissionEligible: true, StandardQuota: 125, ChargedTotalQuota: 100, CommissionableQuota: 100,
		SettlementCostQuota: 50, TheoreticalCommissionQuota: 50, PaidAllocatedQuota: 100,
		CommissionQuota: 50, CommissionAmountMicros: 50}
}

// agencyV1CorrectionFixture derives the authoritative correction event the
// gateway writes once the wallet has already paid the corrected total.
func agencyV1CorrectionFixture(original agencycontract.BillingEvent, charged, commission int64) agencycontract.BillingEvent {
	event := original
	event.EventID = fmt.Sprintf("v1-billing-corrected-%d", charged)
	event.OperationID = fmt.Sprintf("v1-billing-corrected-operation-%d", charged)
	event.EventType, event.OriginalEventID = agencycontract.BillingEventCorrected, original.EventID
	event.JournalRevision, event.MoneySeq = original.JournalRevision+1, original.MoneySeq+1
	event.ChargedTotalQuota, event.CommissionableQuota = charged, charged
	event.NoncommissionableQuota = 0
	event.SettlementCostQuota = charged / 2
	event.TheoreticalCommissionQuota = charged - event.SettlementCostQuota
	event.PaidAllocatedQuota, event.CommissionQuota = charged, commission
	event.CommissionAmountMicros = commission
	return event
}

func TestBillingCorrectionRewritesTheProjectedUsageAndCommission(t *testing.T) {
	app := newAgencyTestApp(t)
	original := agencyV1BillingFixture()
	commitComponentBillingFixture(t, app.db, original)
	require.NoError(t, app.ProcessBillingEvent(original))

	correction := agencyV1CorrectionFixture(original, 60, 30)
	correction.OccurredAtMS = original.OccurredAtMS + 3_600_000
	commitComponentBillingFixture(t, app.db, correction)
	require.NoError(t, app.ProcessBillingEvent(correction))

	// One corrected call, not a duplicate row.
	var facts []model.AgencyUsageFact
	require.NoError(t, app.db.Where("component_key = ?", model.AgencyComponentKey("default")).Find(&facts).Error)
	require.Len(t, facts, 1)
	assert.Equal(t, original.EventID, facts[0].EventID, "the corrected row keeps the identity of the original call")
	assert.Equal(t, int64(60), facts[0].ChargedQuota)
	assert.Equal(t, int64(60), facts[0].PaidQuota)
	assert.Equal(t, correction.OccurredAtMS, facts[0].OccurredAtMS)
	assert.Equal(t, original.FinancialChargeID, facts[0].FinancialChargeID)

	// Commission moves by the difference only, and the smaller amount is
	// recorded as a reversal so every report keeps subtracting it.
	var entries []model.AgencyCommissionLedger
	require.NoError(t, app.db.Where("agency_id = ?", *original.AgencyID).Order("id").Find(&entries).Error)
	require.Len(t, entries, 2)
	assert.Equal(t, "earned", entries[0].EntryType)
	assert.Equal(t, int64(50), entries[0].AmountMicros)
	assert.Equal(t, "reversal", entries[1].EntryType)
	assert.Equal(t, int64(-20), entries[1].AmountMicros)
	require.NotNil(t, entries[1].OriginalEntryID)
	assert.Equal(t, entries[0].ID, *entries[1].OriginalEntryID)
	assert.Equal(t, correction.EventID, entries[1].EventID)

	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", *original.AgencyID, original.CurrencyCode).First(&balance).Error)
	assert.Equal(t, int64(30), balance.AvailableMicros)
	assert.Equal(t, int64(50), balance.EarnedMicros)
	assert.Equal(t, int64(20), balance.ReversedMicros)

	// The daily aggregate follows the corrected call instead of counting a
	// second request.
	var stat model.AgencyDailyStat
	require.NoError(t, app.db.Where("agency_id = ? AND stat_date = ? AND billing_source = ?",
		*original.AgencyID, agencyStatDate(correction.OccurredAtMS), "wallet").First(&stat).Error)
	assert.Equal(t, int64(1), stat.Calls)
	assert.Equal(t, int64(60), stat.UsageQuota)
	assert.Equal(t, int64(60), stat.ChargedQuota)
	assert.Equal(t, int64(30), stat.CommissionMicros)

	// A redelivered correction is inert.
	require.NoError(t, app.ProcessBillingEvent(correction))
	var factCount, entryCount int64
	require.NoError(t, app.db.Model(&model.AgencyUsageFact{}).Where("component_key = ?", model.AgencyComponentKey("default")).Count(&factCount).Error)
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("agency_id = ?", *original.AgencyID).Count(&entryCount).Error)
	assert.Equal(t, int64(1), factCount)
	assert.Equal(t, int64(2), entryCount)
}

func TestBillingCorrectionRaisesTheCommissionWithOneEarningRow(t *testing.T) {
	app := newAgencyTestApp(t)
	original := agencyV1BillingFixture()
	commitComponentBillingFixture(t, app.db, original)
	require.NoError(t, app.ProcessBillingEvent(original))

	correction := agencyV1CorrectionFixture(original, 120, 60)
	commitComponentBillingFixture(t, app.db, correction)
	require.NoError(t, app.ProcessBillingEvent(correction))

	var facts []model.AgencyUsageFact
	require.NoError(t, app.db.Where("component_key = ?", model.AgencyComponentKey("default")).Find(&facts).Error)
	require.Len(t, facts, 1)
	assert.Equal(t, int64(120), facts[0].ChargedQuota)

	var entries []model.AgencyCommissionLedger
	require.NoError(t, app.db.Where("agency_id = ?", *original.AgencyID).Order("id").Find(&entries).Error)
	require.Len(t, entries, 2)
	assert.Equal(t, "earned", entries[1].EntryType)
	assert.Equal(t, int64(10), entries[1].AmountMicros)
	assert.Nil(t, entries[1].OriginalEntryID)

	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", *original.AgencyID, original.CurrencyCode).First(&balance).Error)
	assert.Equal(t, int64(60), balance.AvailableMicros)
	assert.Equal(t, int64(60), balance.EarnedMicros)
	assert.Zero(t, balance.ReversedMicros)
}

func TestBillingCorrectionMovesTheCallBetweenDailyBuckets(t *testing.T) {
	app := newAgencyTestApp(t)
	original := agencyV1BillingFixture()
	commitComponentBillingFixture(t, app.db, original)
	require.NoError(t, app.ProcessBillingEvent(original))

	correction := agencyV1CorrectionFixture(original, 60, 30)
	correction.OccurredAtMS = original.OccurredAtMS + 36*3_600_000
	commitComponentBillingFixture(t, app.db, correction)
	require.NoError(t, app.ProcessBillingEvent(correction))

	var corrected model.AgencyDailyStat
	require.NoError(t, app.db.Where("agency_id = ? AND stat_date = ?", *original.AgencyID,
		agencyStatDate(correction.OccurredAtMS)).First(&corrected).Error)
	assert.Equal(t, int64(1), corrected.Calls)
	assert.Equal(t, int64(60), corrected.UsageQuota)
	assert.Equal(t, int64(30), corrected.CommissionMicros)

	var previous model.AgencyDailyStat
	require.NoError(t, app.db.Where("agency_id = ? AND stat_date = ?", *original.AgencyID,
		agencyStatDate(original.OccurredAtMS)).First(&previous).Error)
	assert.Zero(t, previous.Calls)
	assert.Zero(t, previous.UsageQuota)
	assert.Zero(t, previous.CommissionMicros)
}

func TestBillingCorrectionRejectsMultiComponentEnvelopes(t *testing.T) {
	app := newAgencyTestApp(t)
	original := componentBillingFixture()
	commitComponentBillingFixture(t, app.db, original)
	require.NoError(t, app.ProcessBillingEvent(original))

	correction := original
	correction.EventID, correction.OperationID = "component-correction", "component-correction-operation"
	correction.EventType, correction.OriginalEventID = agencycontract.BillingEventCorrected, original.EventID
	correction.JournalRevision, correction.MoneySeq = 2, 2
	commitComponentBillingFixture(t, app.db, correction)
	err := app.ProcessBillingEvent(correction)
	require.ErrorContains(t, err, "multi component billing corrections are not supported")
}
