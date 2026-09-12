package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecordAgencyRealtimeSegmentIsHashIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-realtime-journal-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, model.MigrateAgency(db))

	agencyID, bindingID := int64(7), int64(11)
	info := &relaycommon.RelayInfo{
		UserId: 100, TokenId: 200, RequestId: "realtime-charge-1",
		RequestURLPath: "/v1/realtime", AgencyPricing: &agencycontract.PricingSnapshot{
			AgencyID: agencyID, BindingID: bindingID, OriginModelName: "gpt-realtime",
			ModelKey: "gpt-realtime", SettlementBPS: 7500, SalesBPS: 9000,
			CommissionEligible: true, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1",
		},
	}
	first := &dto.RealtimeUsage{TotalTokens: 10, InputTokens: 7, OutputTokens: 3}
	cumulative := &dto.RealtimeUsage{TotalTokens: 10, InputTokens: 7, OutputTokens: 3}
	require.NoError(t, RecordAgencyRealtimeSegment(info, 0, first, cumulative, 100, 100, "success"))
	// Replaying the exact frame is a no-op and must not create another journal,
	// operation, outbox event, or delivery row.
	require.NoError(t, RecordAgencyRealtimeSegment(info, 0, first, cumulative, 100, 100, "success"))
	var journalCount, operationCount, outboxCount, deliveryCount int64
	require.NoError(t, db.Model(&model.AgencyBillingJournal{}).Where("charge_id = ?", info.RequestId).Count(&journalCount).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ?", info.RequestId).Count(&operationCount).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Count(&outboxCount).Error)
	require.NoError(t, db.Model(&model.AgencyEventDelivery{}).Count(&deliveryCount).Error)
	require.Equal(t, int64(1), journalCount)
	require.Equal(t, int64(1), operationCount)
	require.Equal(t, int64(1), outboxCount)
	require.Equal(t, int64(1), deliveryCount)
	recorded, err := AgencyRealtimeSegmentRecorded(info, first, cumulative)
	require.NoError(t, err)
	require.True(t, recorded)
	changedCumulative := &dto.RealtimeUsage{TotalTokens: 11, InputTokens: 8, OutputTokens: 3}
	recorded, err = AgencyRealtimeSegmentRecorded(info, first, changedCumulative)
	require.NoError(t, err)
	require.False(t, recorded)

	// A same-number segment with different cumulative usage is not a harmless
	// retry: reject it instead of silently replacing the financial fact.
	conflict := &dto.RealtimeUsage{TotalTokens: 11, InputTokens: 8, OutputTokens: 3}
	require.Error(t, RecordAgencyRealtimeSegment(info, 0, conflict, conflict, 110, 110, "success"))

	second := &dto.RealtimeUsage{TotalTokens: 5, InputTokens: 3, OutputTokens: 2}
	nextCumulative := &dto.RealtimeUsage{TotalTokens: 15, InputTokens: 10, OutputTokens: 5}
	require.NoError(t, RecordAgencyRealtimeSegment(info, 1, second, nextCumulative, 50, 50, "success"))
	require.NoError(t, db.Model(&model.AgencyBillingJournal{}).Where("charge_id = ?", info.RequestId).Count(&journalCount).Error)
	require.Equal(t, int64(2), journalCount)
	var stored model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ? AND segment_no = ?", info.RequestId, 1).First(&stored).Error)
	require.NotEmpty(t, stored.UsageHash)
	require.JSONEq(t, `{"total_tokens":15,"input_tokens":10,"output_tokens":5,"input_token_details":{"text_tokens":0,"audio_tokens":0,"cached_tokens":0,"image_tokens":0},"output_token_details":{"text_tokens":0,"audio_tokens":0,"image_tokens":0,"reasoning_tokens":0}}`, stored.LastCumulativeUsage)
}

func TestRecordAgencyBillingEventRejectsConflictingFinalize(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-finalize-conflict-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, model.MigrateAgency(db))

	agencyID, bindingID := int64(7), int64(11)
	info := &relaycommon.RelayInfo{
		UserId: 100, TokenId: 200, RequestId: "finalize-charge-1",
		FinalPreConsumedQuota: 100, AgencyPaidAllocatedQuota: 100,
		RequestURLPath: "/v1/chat/completions",
		AgencyPricing: &agencycontract.PricingSnapshot{
			AgencyID: agencyID, BindingID: bindingID, OriginModelName: "model",
			ModelKey: "model", SettlementBPS: 7500, SalesBPS: 9000,
			CommissionEligible: true, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1",
		},
	}
	require.NoError(t, RecordAgencyBillingEvent(info, 100, "success"))
	require.ErrorContains(t, RecordAgencyBillingEvent(info, 101, "success"), "payload hash conflict")

	var journalCount, operationCount int64
	require.NoError(t, db.Model(&model.AgencyBillingJournal{}).Where("charge_id = ?", info.RequestId).Count(&journalCount).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ?", info.RequestId).Count(&operationCount).Error)
	require.Equal(t, int64(1), journalCount)
	require.Equal(t, int64(1), operationCount)
}

func TestRecordAgencyBillingEventUsesActualStandardQuota(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-finalize-actual-standard-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, model.MigrateAgency(db))

	info := &relaycommon.RelayInfo{
		UserId: 149, TokenId: 45, RequestId: "finalize-actual-standard-1",
		AgencyStandardQuota:      2,
		AgencyPaidAllocatedQuota: 0,
		AgencyPricing: &agencycontract.PricingSnapshot{
			AgencyID: 1, BindingID: 1, OriginModelName: "Hunyuan/hy3",
			ModelKey: "hunyuan-hy3", SettlementBPS: 5500, SalesBPS: 9000,
			CommissionEligible: true, CurrencyCode: "TOKENS", QuotaPerUnit: "500000", ExchangeRate: "1",
		},
	}

	require.NoError(t, RecordAgencyBillingEvent(info, 2, "success"))

	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", info.RequestId).First(&journal).Error)
	require.Equal(t, int64(2), journal.ChargedTotalQuota)
	require.Equal(t, int64(1), journal.SettlementCostQuota)
	require.Equal(t, int64(1), journal.TheoreticalCommissionQuota)
}

func TestRecordAgencyBillingEventRollsBackJournalWhenOutboxConflicts(t *testing.T) {
	// Crash injection between the journal/operation writes and the outbox/delivery
	// writes: a duplicate outbox event_id forces the insert to fail mid-transaction
	// (acceptance §22.3「journal 与 outbox/delivery 之间（同事务应整体回滚）」). No
	// half-transaction may be visible afterwards: the journal and operation rows for
	// the new charge must be rolled back together with the delivery.
	db, err := gorm.Open(sqlite.Open("file:agency-finalize-outbox-conflict?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, model.MigrateAgency(db))

	const blockedEvent = "blocked-outbox-event"
	require.NoError(t, db.Create(&model.AgencyBillingOutbox{
		EventID: blockedEvent, OperationID: "blocked-op", EventIndex: 0, EventCount: 1,
		EventKind: "agency.billing_finalized", UserID: 100, MoneySeq: 1,
		Payload: "{}", PayloadHash: "blocked-hash", SchemaVersion: agencycontract.SchemaVersion, CreatedAtMS: time.Now().UnixMilli(),
	}).Error)

	info := &relaycommon.RelayInfo{
		UserId: 100, TokenId: 200, RequestId: "outbox-crash-charge-1",
		AgencyBillingEventID:     blockedEvent,
		FinalPreConsumedQuota:    100,
		AgencyPaidAllocatedQuota: 100,
		RequestURLPath:           "/v1/chat/completions",
		AgencyPricing: &agencycontract.PricingSnapshot{
			AgencyID: 7, BindingID: 11, OriginModelName: "model", ModelKey: "model",
			SettlementBPS: 7500, SalesBPS: 9000, CommissionEligible: true,
			CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1",
		},
	}
	require.Error(t, RecordAgencyBillingEvent(info, 100, "success"))

	var journalCount, operationCount, deliveryCount, outboxCount int64
	require.NoError(t, db.Model(&model.AgencyBillingJournal{}).Where("charge_id = ?", info.RequestId).Count(&journalCount).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ?", info.RequestId).Count(&operationCount).Error)
	require.NoError(t, db.Model(&model.AgencyEventDelivery{}).Where("event_id = ?", blockedEvent).Count(&deliveryCount).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("event_id = ?", blockedEvent).Count(&outboxCount).Error)
	require.Zero(t, journalCount, "journal must roll back with the failed outbox insert")
	require.Zero(t, operationCount, "operation must roll back with the failed outbox insert")
	require.Zero(t, deliveryCount, "delivery must never be written")
	require.Equal(t, int64(1), outboxCount, "only the pre-existing blocked outbox remains")
}
