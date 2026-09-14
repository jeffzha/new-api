package agencyhub

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func componentBillingFixture() agencycontract.BillingEvent {
	agencyID, bindingID := int64(718392), int64(18)
	return agencycontract.BillingEvent{
		SchemaVersion: agencycontract.ComponentSchemaVersion, EventID: "component-original", OperationID: "component-finalize",
		EventType: "agency.billing_finalized", FinancialChargeID: "component-charge", JournalRevision: 1, MoneySeq: 1, EventCount: 1,
		UserID: 718393, AgencyID: &agencyID, BindingID: &bindingID, OriginModelName: "Hunyuan/hy3", Endpoint: "/v1/chat/completions",
		BusinessStatus: "success", BillingStatus: "finalized", FinancialFinal: true, OccurredAtMS: 1789272000000,
		CurrencyCode: "CNY", QuotaPerUnit: "100000", ExchangeRate: "1", CommissionEligible: true,
		InputTokens: 120, OutputTokens: 30, CacheReadTokens: 20, CacheWriteTokens: 10,
		StandardQuota: 210, ChargedTotalQuota: 180, CommissionableQuota: 150, NoncommissionableQuota: 30,
		SettlementCostQuota: 120, TheoreticalCommissionQuota: 30, PaidAllocatedQuota: 120, CommissionQuota: 20, CommissionAmountMicros: 200,
		Components: []agencycontract.BillingComponent{
			{ComponentID: "model-a", StandardQuota: 100, ChargedTotalQuota: 90, CommissionableQuota: 90, SettlementCostQuota: 75,
				TheoreticalCommissionQuota: 15, PaidAllocatedQuota: 60, NonpaidAllocatedQuota: 20, DebtAllocatedQuota: 10, CommissionQuota: 10, CommissionAmountMicros: 100, CommissionEligible: true},
			{ComponentID: "model-b", StandardQuota: 80, ChargedTotalQuota: 60, CommissionableQuota: 60, SettlementCostQuota: 45,
				TheoreticalCommissionQuota: 15, PaidAllocatedQuota: 40, NonpaidAllocatedQuota: 20, CommissionQuota: 10, CommissionAmountMicros: 100, CommissionEligible: true},
			{ComponentID: "fee", StandardQuota: 30, ChargedTotalQuota: 30, NoncommissionableQuota: 30, PaidAllocatedQuota: 20, NonpaidAllocatedQuota: 10, CommissionSkipReason: "noncommissionable_charge"},
			{ComponentID: "free", CommissionSkipReason: "free_model"},
		},
	}
}

// Each event has real immutable operation/outbox evidence. The caller may
// advance the journal before delivering its original event to cover backlog.
func commitComponentBillingFixture(t *testing.T, db *gorm.DB, event agencycontract.BillingEvent) model.AgencyEventDelivery {
	t.Helper()
	require.NoError(t, validateBillingEvent(event))
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	hash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	var journal model.AgencyBillingJournal
	err = db.Where("charge_id = ? AND segment_no = ?", event.FinancialChargeID, event.SegmentNo).First(&journal).Error
	if err == gorm.ErrRecordNotFound {
		journal = model.AgencyBillingJournal{ChargeID: event.FinancialChargeID, SegmentNo: event.SegmentNo, UserID: event.UserID,
			Revision: event.JournalRevision, Version: 1, Status: "finalized", PricingSnapshot: "{}"}
		require.NoError(t, db.Create(&journal).Error)
	} else {
		require.NoError(t, err)
		require.NoError(t, db.Model(&journal).Updates(map[string]any{"revision": event.JournalRevision, "status": "partially_reversed"}).Error)
	}
	operation := "finalize"
	if event.EventType == "agency.billing_reversed" {
		operation = "refund"
	}
	require.NoError(t, db.Create(&model.AgencyBillingOperation{ChargeID: event.FinancialChargeID, SegmentNo: event.SegmentNo,
		OperationID: event.OperationID, Revision: event.JournalRevision, Operation: operation, MoneySeq: event.MoneySeq,
		EventCount: event.EventCount, InputHash: hash, CommittedResult: string(payload)}).Error)
	require.NoError(t, db.Create(&model.AgencyBillingOutbox{EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType,
		SchemaVersion: event.SchemaVersion, UserID: event.UserID, MoneySeq: event.MoneySeq, EventCount: 1, Payload: string(payload), PayloadHash: hash}).Error)
	delivery := model.AgencyEventDelivery{EventID: event.EventID, Status: "pending", CreatedAt: 1}
	require.NoError(t, db.Create(&delivery).Error)
	return delivery
}

func componentRefundFixture(original agencycontract.BillingEvent) agencycontract.BillingEvent {
	event := original
	event.EventID, event.OperationID = "component-refund-1", "component-refund-operation-1"
	event.EventType, event.OriginalEventID = "agency.billing_reversed", original.EventID
	event.JournalRevision, event.MoneySeq = 2, 2
	event.StandardQuota, event.ChargedTotalQuota, event.CommissionableQuota = 0, 30, 30
	event.NoncommissionableQuota, event.SettlementCostQuota, event.TheoreticalCommissionQuota = 0, 0, 0
	event.PaidAllocatedQuota, event.CommissionQuota, event.CommissionAmountMicros = 20, 5, 0
	event.ReversedCommissionAmountMicros = 50
	event.Components = []agencycontract.BillingComponent{{ComponentID: "model-b", ChargedTotalQuota: 30, CommissionableQuota: 30,
		PaidAllocatedQuota: 20, NonpaidAllocatedQuota: 10, CommissionQuota: 5, ReversedCommissionAmountMicros: 50, CommissionEligible: true}}
	return event
}

func TestComponentRefundRetainsQuotaWhenCurrencyDeltaRoundsToZero(t *testing.T) {
	app := newAgencyTestApp(t)
	original := componentBillingFixture()
	original.StandardQuota, original.ChargedTotalQuota, original.CommissionableQuota = 100, 100, 100
	original.NoncommissionableQuota, original.SettlementCostQuota = 0, 0
	original.TheoreticalCommissionQuota, original.PaidAllocatedQuota, original.CommissionQuota = 100, 100, 100
	original.QuotaPerUnit, original.CommissionAmountMicros = "100000000", 1
	original.Components = []agencycontract.BillingComponent{{ComponentID: "model", StandardQuota: 100, ChargedTotalQuota: 100,
		CommissionableQuota: 100, TheoreticalCommissionQuota: 100, PaidAllocatedQuota: 100, CommissionQuota: 100, CommissionAmountMicros: 1, CommissionEligible: true}}
	commitComponentBillingFixture(t, app.db, original)
	require.NoError(t, app.ProcessBillingEvent(original))
	first := componentRefundFixture(original)
	first.ChargedTotalQuota, first.CommissionableQuota, first.PaidAllocatedQuota, first.CommissionQuota = 10, 10, 10, 10
	first.ReversedCommissionAmountMicros = 0
	first.Components = []agencycontract.BillingComponent{{ComponentID: "model", ChargedTotalQuota: 10, CommissionableQuota: 10,
		PaidAllocatedQuota: 10, CommissionQuota: 10, CommissionEligible: true}}
	commitComponentBillingFixture(t, app.db, first)
	require.NoError(t, app.ProcessBillingEvent(first))
	last := first
	last.EventID, last.OperationID = "component-rounding-final", "component-rounding-final-operation"
	last.JournalRevision, last.MoneySeq = 3, 3
	last.ChargedTotalQuota, last.CommissionableQuota, last.PaidAllocatedQuota, last.CommissionQuota = 90, 90, 90, 90
	last.ReversedCommissionAmountMicros = 1
	last.Components = []agencycontract.BillingComponent{{ComponentID: "model", ChargedTotalQuota: 90, CommissionableQuota: 90,
		PaidAllocatedQuota: 90, CommissionQuota: 90, ReversedCommissionAmountMicros: 1, CommissionEligible: true}}
	commitComponentBillingFixture(t, app.db, last)
	require.NoError(t, app.ProcessBillingEvent(last))
	var entries []model.AgencyCommissionLedger
	require.NoError(t, app.db.Where("entry_type = ?", "reversal").Order("id").Find(&entries).Error)
	require.Len(t, entries, 2)
	assert.Zero(t, entries[0].AmountMicros)
	assert.Equal(t, int64(10), entries[0].CommissionQuota)
	assert.Equal(t, int64(100), entries[0].CommissionQuota+entries[1].CommissionQuota)
	assert.Equal(t, int64(-1), entries[0].AmountMicros+entries[1].AmountMicros)
}

func TestComponentBillingProjectsOneReceiptAndRequestAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := reconciliationConcurrentDB(t, dialect)
			// A surrounding rollback keeps the isolated external harness usable
			// for other tests. ProcessBillingEvent uses real nested transactions.
			tx := db.Begin()
			require.NoError(t, tx.Error)
			t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
			app := New(tx, tx, Config{InstanceID: "component-worker"})
			event := componentBillingFixture()
			delivery := commitComponentBillingFixture(t, tx, event)
			require.NoError(t, tx.Model(&delivery).Updates(map[string]any{"status": "claimed", "lease_owner": "component-worker", "lease_token": 42, "lease_until": time.Now().Unix() + 60}).Error)
			delivery.Status, delivery.LeaseOwner, delivery.LeaseToken = "claimed", "component-worker", 42
			require.NoError(t, app.processDelivery(context.Background(), delivery))
			require.NoError(t, app.ProcessBillingEvent(event))
			var receiptCount int64
			require.NoError(t, tx.Model(&model.AgencySourceEvent{}).Where("event_id = ?", event.EventID).Count(&receiptCount).Error)
			assert.Equal(t, int64(1), receiptCount)
			require.NoError(t, tx.First(&delivery, delivery.ID).Error)
			assert.Equal(t, "done", delivery.Status)
			var usage []model.AgencyUsageFact
			require.NoError(t, tx.Where("event_id = ?", event.EventID).Order("component_id").Find(&usage).Error)
			require.Len(t, usage, 4)
			assert.Equal(t, []string{"fee", "free", "model-a", "model-b"}, []string{usage[0].ComponentID, usage[1].ComponentID, usage[2].ComponentID, usage[3].ComponentID})
			assert.Equal(t, int64(180), usage[0].ChargedQuota+usage[1].ChargedQuota+usage[2].ChargedQuota+usage[3].ChargedQuota)
			var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64
			for _, row := range usage {
				inputTokens += row.InputTokens
				outputTokens += row.OutputTokens
				cacheReadTokens += row.CacheReadTokens
				cacheWriteTokens += row.CacheWriteTokens
			}
			assert.Equal(t, event.InputTokens, inputTokens)
			assert.Equal(t, event.OutputTokens, outputTokens)
			assert.Equal(t, event.CacheReadTokens, cacheReadTokens)
			assert.Equal(t, event.CacheWriteTokens, cacheWriteTokens)
			var ledger []model.AgencyCommissionLedger
			require.NoError(t, tx.Where("event_id = ?", event.EventID).Order("component_id").Find(&ledger).Error)
			require.Len(t, ledger, 2)
			assert.Equal(t, "model-a", ledger[0].ComponentID)
			assert.Equal(t, "model-b", ledger[1].ComponentID)
			assert.Equal(t, int64(200), ledger[0].AmountMicros+ledger[1].AmountMicros)
			var balance model.AgencyCommissionBalance
			require.NoError(t, tx.Where("agency_id = ?", *event.AgencyID).First(&balance).Error)
			assert.Equal(t, int64(200), balance.AvailableMicros)
			var stat model.AgencyDailyStat
			require.NoError(t, tx.Where("agency_id = ?", *event.AgencyID).First(&stat).Error)
			assert.Equal(t, int64(1), stat.Calls)
			assert.Equal(t, int64(180), stat.ChargedQuota)
			assert.Equal(t, int64(200), stat.CommissionMicros)
			// This consumer fixture supplies immutable events, not wallet
			// allocations. Full operation/journal reconciliation is asserted
			// by the real gateway transaction test below in the integration suite.
			evidence, err := reconciliationEvidence(tx, model.AgencyReconciliationIssue{ObjectType: "billing_outbox", ObjectID: event.EventID}, false)
			require.NoError(t, err)
			assert.Equal(t, "consistent", evidence.State, "%+v", evidence)
		})
	}
}

func TestComponentRefundsTargetOriginalComponentAndPreserveDelayedFinalize(t *testing.T) {
	app := newAgencyTestApp(t)
	original := componentBillingFixture()
	commitComponentBillingFixture(t, app.db, original)
	refund := componentRefundFixture(original)
	commitComponentBillingFixture(t, app.db, refund)
	// The journal now reflects the refund, but the original immutable finalize
	// must still become visible first before the negative component projection.
	require.NoError(t, app.ProcessBillingEvent(original))
	require.NoError(t, app.ProcessBillingEvent(refund))
	require.NoError(t, app.ProcessBillingEvent(refund))
	var originalB, reversal model.AgencyCommissionLedger
	require.NoError(t, app.db.Where("event_id = ? AND component_id = ?", original.EventID, "model-b").First(&originalB).Error)
	require.NoError(t, app.db.Where("event_id = ?", refund.EventID).First(&reversal).Error)
	assert.Equal(t, &originalB.ID, reversal.OriginalEntryID)
	assert.Equal(t, int64(-50), reversal.AmountMicros)
	remaining := refund
	remaining.EventID, remaining.OperationID = "component-refund-2", "component-refund-operation-2"
	remaining.JournalRevision, remaining.MoneySeq = 3, 3
	remaining.ChargedTotalQuota, remaining.CommissionableQuota, remaining.PaidAllocatedQuota = 120, 120, 80
	remaining.CommissionQuota, remaining.ReversedCommissionAmountMicros = 15, 150
	remaining.Components = []agencycontract.BillingComponent{
		{ComponentID: "model-a", ChargedTotalQuota: 90, CommissionableQuota: 90, PaidAllocatedQuota: 60, NonpaidAllocatedQuota: 20, DebtAllocatedQuota: 10, CommissionQuota: 10, ReversedCommissionAmountMicros: 100, CommissionEligible: true},
		refund.Components[0],
	}
	commitComponentBillingFixture(t, app.db, remaining)
	require.NoError(t, app.ProcessBillingEvent(remaining))
	fee := refund
	fee.EventID, fee.OperationID, fee.JournalRevision, fee.MoneySeq = "fee-refund", "fee-refund-operation", 4, 4
	fee.CommissionableQuota, fee.NoncommissionableQuota, fee.CommissionQuota, fee.ReversedCommissionAmountMicros = 0, 30, 0, 0
	fee.CommissionEligible = false
	fee.Components = []agencycontract.BillingComponent{{ComponentID: "fee", ChargedTotalQuota: 30, NoncommissionableQuota: 30, PaidAllocatedQuota: 20, NonpaidAllocatedQuota: 10}}
	commitComponentBillingFixture(t, app.db, fee)
	require.NoError(t, app.ProcessBillingEvent(fee))
	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ?", *original.AgencyID).First(&balance).Error)
	assert.Zero(t, balance.AvailableMicros)
	assert.Equal(t, int64(200), balance.ReversedMicros)
	var stat model.AgencyDailyStat
	require.NoError(t, app.db.Where("agency_id = ?", *original.AgencyID).First(&stat).Error)
	assert.Equal(t, int64(1), stat.Calls)
	assert.Equal(t, int64(180), stat.ChargedQuota)
	assert.Equal(t, int64(200), stat.ReversalMicros)
	var feeEntries int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", fee.EventID).Count(&feeEntries).Error)
	assert.Zero(t, feeEntries, "refunding an excluded component cannot reverse model commission")
}

func TestComponentBillingRejectsInvalidEnvelopesBeforeProjection(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*agencycontract.BillingEvent)
	}{
		{"aggregate", func(e *agencycontract.BillingEvent) { e.CommissionAmountMicros++ }},
		{"duplicate", func(e *agencycontract.BillingEvent) { e.Components[1].ComponentID = e.Components[0].ComponentID }},
		{"padded_id", func(e *agencycontract.BillingEvent) { e.Components[0].ComponentID = " model-a" }},
		{"v1_with_components", func(e *agencycontract.BillingEvent) { e.SchemaVersion = agencycontract.SchemaVersion }},
		{"v2_without_components", func(e *agencycontract.BillingEvent) { e.Components = nil }},
		{"eligibility", func(e *agencycontract.BillingEvent) { e.Components[0].CommissionEligible = false }},
		{"missing_operation", func(e *agencycontract.BillingEvent) { e.OperationID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newAgencyTestApp(t)
			event := componentBillingFixture()
			test.change(&event)
			require.Error(t, app.ProcessBillingEvent(event))
			require.Error(t, app.processBillingEventWithLease(context.Background(), event, model.AgencyEventDelivery{}))
			var receipts int64
			require.NoError(t, app.db.Model(&model.AgencySourceEvent{}).Count(&receipts).Error)
			assert.Zero(t, receipts)
		})
	}
}

func TestComponentBillingVerifiesWholeCommittedOperation(t *testing.T) {
	app := newAgencyTestApp(t)
	event := componentBillingFixture()
	commitComponentBillingFixture(t, app.db, event)
	forged := event
	forged.Endpoint = "/v1/responses"
	require.NoError(t, validateBillingEvent(forged), "an internally consistent envelope is not authoritative evidence")
	require.ErrorContains(t, app.ProcessBillingEvent(forged), "payload hash conflict")
	var receipts, ledger int64
	require.NoError(t, app.db.Model(&model.AgencySourceEvent{}).Count(&receipts).Error)
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Count(&ledger).Error)
	assert.Zero(t, receipts)
	assert.Zero(t, ledger)
}

func TestComponentRefundRejectsWrongHistoryAndCumulativeOverdraw(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*agencycontract.BillingEvent)
		errorText string
	}{
		{"agency", func(e *agencycontract.BillingEvent) { other := int64(99); e.AgencyID = &other }, "original owner"},
		{"currency", func(e *agencycontract.BillingEvent) { e.CurrencyCode = "USD" }, "original owner"},
		{"rate", func(e *agencycontract.BillingEvent) { e.ExchangeRate = "9" }, "original owner"},
		{"component", func(e *agencycontract.BillingEvent) { e.Components[0].ComponentID = "fee" }, "record not found"},
		{"cumulative", func(e *agencycontract.BillingEvent) {
			e.ReversedCommissionAmountMicros = 60
			e.Components[0].ReversedCommissionAmountMicros = 60
		}, "exceeds original"},
		{"cumulative_quota", func(e *agencycontract.BillingEvent) {
			e.CommissionQuota = 6
			e.Components[0].CommissionQuota = 6
		}, "quota reversal exceeds original"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newAgencyTestApp(t)
			original := componentBillingFixture()
			commitComponentBillingFixture(t, app.db, original)
			require.NoError(t, app.ProcessBillingEvent(original))
			refund := componentRefundFixture(original)
			commitComponentBillingFixture(t, app.db, refund)
			require.NoError(t, app.ProcessBillingEvent(refund))
			refund.EventID, refund.OperationID, refund.MoneySeq, refund.JournalRevision = "bad-refund", "bad-refund-operation", 3, 3
			test.change(&refund)
			commitComponentBillingFixture(t, app.db, refund)
			require.ErrorContains(t, app.ProcessBillingEvent(refund), test.errorText)
			var balance model.AgencyCommissionBalance
			require.NoError(t, app.db.Where("agency_id = ?", *original.AgencyID).First(&balance).Error)
			assert.Equal(t, int64(150), balance.AvailableMicros)
			assert.Equal(t, int64(50), balance.ReversedMicros)
			var rejectedReceipts int64
			require.NoError(t, app.db.Model(&model.AgencySourceEvent{}).Where("event_id = ?", refund.EventID).Count(&rejectedReceipts).Error)
			assert.Zero(t, rejectedReceipts)
		})
	}
}

func TestComponentBillingRollsBackEveryProjectionOnDailyOverflow(t *testing.T) {
	app := newAgencyTestApp(t)
	event := componentBillingFixture()
	commitComponentBillingFixture(t, app.db, event)
	key, err := agencycontract.ModelKey(event.OriginModelName)
	require.NoError(t, err)
	date := time.UnixMilli(event.OccurredAtMS).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
	require.NoError(t, app.db.Create(&model.AgencyDailyStat{StatDate: date, AgencyID: *event.AgencyID, ModelKey: key, CurrencyCode: "CNY", BillingSource: "wallet", Calls: math.MaxInt64, Revision: 1}).Error)
	require.ErrorIs(t, app.ProcessBillingEvent(event), errBalanceOverflow)
	for _, table := range []any{&model.AgencySourceEvent{}, &model.AgencyUsageFact{}, &model.AgencyCommissionLedger{}, &model.AgencyCommissionBalance{}} {
		var rows int64
		require.NoError(t, app.db.Model(table).Count(&rows).Error)
		assert.Zero(t, rows, "%T must roll back with the envelope", table)
	}
}

func TestComponentRefundRollsBackEarlierComponentsWhenLaterCapFails(t *testing.T) {
	app := newAgencyTestApp(t)
	original := componentBillingFixture()
	commitComponentBillingFixture(t, app.db, original)
	require.NoError(t, app.ProcessBillingEvent(original))
	refund := componentRefundFixture(original)
	commitComponentBillingFixture(t, app.db, refund)
	require.NoError(t, app.ProcessBillingEvent(refund))
	withdrawal := model.AgencyWithdrawal{RequestNo: "component-payout", AgencyID: *original.AgencyID, CurrencyCode: "CNY", AmountMicros: 20, Status: "approved", Version: 1}
	require.NoError(t, app.db.Create(&withdrawal).Error)
	refund.EventID, refund.OperationID, refund.MoneySeq, refund.JournalRevision = "multi-refund-bad", "multi-refund-bad-op", 3, 3
	refund.ChargedTotalQuota, refund.CommissionableQuota, refund.PaidAllocatedQuota = 120, 120, 80
	refund.CommissionQuota, refund.ReversedCommissionAmountMicros = 16, 160
	refund.Components = []agencycontract.BillingComponent{
		{ComponentID: "model-a", ChargedTotalQuota: 90, CommissionableQuota: 90, PaidAllocatedQuota: 60, NonpaidAllocatedQuota: 20, DebtAllocatedQuota: 10, CommissionQuota: 10, ReversedCommissionAmountMicros: 100, CommissionEligible: true},
		{ComponentID: "model-b", ChargedTotalQuota: 30, CommissionableQuota: 30, PaidAllocatedQuota: 20, NonpaidAllocatedQuota: 10, CommissionQuota: 6, ReversedCommissionAmountMicros: 60, CommissionEligible: true},
	}
	commitComponentBillingFixture(t, app.db, refund)
	require.ErrorContains(t, app.ProcessBillingEvent(refund), "exceeds original")
	var entries int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", refund.EventID).Count(&entries).Error)
	assert.Zero(t, entries, "model-a projection must roll back when model-b is invalid")
	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ?", *original.AgencyID).First(&balance).Error)
	assert.Equal(t, int64(150), balance.AvailableMicros)
	assert.Equal(t, int64(50), balance.ReversedMicros)
	require.NoError(t, app.db.First(&withdrawal, withdrawal.ID).Error)
	assert.Equal(t, "approved", withdrawal.Status, "a rolled-back reversal cannot leave a withdrawal on hold")
}

func TestComponentWorkerPoisonsMalformedContractAndMetadata(t *testing.T) {
	for _, kind := range []string{"components", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			app := newAgencyTestApp(t)
			event := componentBillingFixture()
			delivery := commitComponentBillingFixture(t, app.db, event)
			if kind == "components" {
				event.Components = nil
				payload, err := common.Marshal(event)
				require.NoError(t, err)
				hash, err := agencycontract.CanonicalHash(event)
				require.NoError(t, err)
				require.NoError(t, app.db.Model(&model.AgencyBillingOutbox{}).Where("event_id = ?", event.EventID).
					Updates(map[string]any{"payload": string(payload), "payload_hash": hash}).Error)
			} else {
				require.NoError(t, app.db.Model(&model.AgencyBillingOutbox{}).Where("event_id = ?", event.EventID).Update("money_seq", 99).Error)
			}
			require.NoError(t, app.RunConsumerOnce(context.Background(), 10))
			require.NoError(t, app.db.First(&delivery, delivery.ID).Error)
			assert.Equal(t, "poison", delivery.Status)
			var receipts, ledger, issues int64
			require.NoError(t, app.db.Model(&model.AgencySourceEvent{}).Count(&receipts).Error)
			require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Count(&ledger).Error)
			require.NoError(t, app.db.Model(&model.AgencyReconciliationIssue{}).Where("object_id = ? AND status = ?", event.EventID, "open").Count(&issues).Error)
			assert.Zero(t, receipts)
			assert.Zero(t, ledger)
			assert.Equal(t, int64(1), issues)
		})
	}
}
