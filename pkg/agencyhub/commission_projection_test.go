package agencyhub

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/require"
)

func TestFactsProjectionContinuesWhenCommissionProcessingIsPaused(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.FactProjectionEnabled = true
	app.config.CommissionEnabled = false
	agencyID := int64(901)
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "facts-while-commission-paused", EventType: "agency.billing_finalized",
		OperationID: "facts-while-commission-paused-op", UserID: 90101, AgencyID: &agencyID,
		OriginModelName: "hunyuan/hy3", Endpoint: "/v1/chat/completions", BusinessStatus: "success", BillingStatus: "finalized",
		CurrencyCode: "CNY", CommissionEligible: true, CommissionAmountMicros: 37, ChargedTotalQuota: 100,
		InputTokens: 81, OutputTokens: 19, CacheReadTokens: 7, CacheWriteTokens: 3,
		PaidAllocatedQuota: 100, OccurredAtMS: time.Now().UnixMilli(),
	}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	hash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType, UserID: event.UserID, Payload: string(payload), PayloadHash: hash, SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS}).Error)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: event.EventID, Status: "pending", NextRetryAt: 1, CreatedAt: 1}).Error)

	require.NoError(t, app.RunConsumerOnce(t.Context(), 10))
	var usageCount int64
	require.NoError(t, app.db.Model(&model.AgencyUsageFact{}).Where("event_id = ?", event.EventID).Count(&usageCount).Error)
	require.Equal(t, int64(1), usageCount)
	var usage model.AgencyUsageFact
	require.NoError(t, app.db.Where("event_id = ?", event.EventID).First(&usage).Error)
	require.Equal(t, int64(81), usage.InputTokens)
	require.Equal(t, int64(19), usage.OutputTokens)
	require.Equal(t, int64(7), usage.CacheReadTokens)
	require.Equal(t, int64(3), usage.CacheWriteTokens)
	var jobs []model.AgencyCommissionJob
	require.NoError(t, app.db.Where("event_id = ?", event.EventID).Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.Equal(t, "deferred", jobs[0].Status)
	var ledgerCount int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", event.EventID).Count(&ledgerCount).Error)
	require.Zero(t, ledgerCount)

	app.config.CommissionEnabled = true
	require.NoError(t, app.drainCommissionJobs(t.Context()))
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", event.EventID).Count(&ledgerCount).Error)
	require.Equal(t, int64(1), ledgerCount)
	require.NoError(t, app.db.First(&jobs[0], jobs[0].ID).Error)
	require.Equal(t, "done", jobs[0].Status)
}

func TestDirectBillingRecoveryHonorsCommissionPause(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.FactProjectionEnabled = true
	app.config.CommissionEnabled = false
	agencyID := int64(902)
	event := agencycontract.BillingEvent{
		SchemaVersion:          agencycontract.SchemaVersion,
		EventID:                "direct-recovery-while-commission-paused",
		EventType:              "agency.billing_finalized",
		OperationID:            "direct-recovery-while-commission-paused-op",
		UserID:                 90201,
		AgencyID:               &agencyID,
		OriginModelName:        "hunyuan/hy3",
		Endpoint:               "/v1/chat/completions",
		BusinessStatus:         "success",
		BillingStatus:          "finalized",
		CurrencyCode:           "CNY",
		CommissionEligible:     true,
		CommissionAmountMicros: 41,
		ChargedTotalQuota:      75,
		PaidAllocatedQuota:     75,
		OccurredAtMS:           time.Now().UnixMilli(),
	}

	require.NoError(t, app.ProcessBillingEvent(event))
	var usageCount int64
	require.NoError(t, app.db.Model(&model.AgencyUsageFact{}).Where("event_id = ?", event.EventID).Count(&usageCount).Error)
	require.Equal(t, int64(1), usageCount)
	var ledgerCount int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", event.EventID).Count(&ledgerCount).Error)
	require.Zero(t, ledgerCount)
	var job model.AgencyCommissionJob
	require.NoError(t, app.db.Where("event_id = ?", event.EventID).First(&job).Error)
	require.Equal(t, "deferred", job.Status)
}

func TestCustomerProjectionMetaSeparatesFactLagFromCommissionPause(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.FactProjectionEnabled = true
	app.config.CommissionEnabled = false
	now := time.Now().UnixMilli()
	require.NoError(t, app.db.Create(&model.AgencySourceEvent{
		EventID: "meta-complete", UserID: 501, MoneySeq: 7, ProcessingStatus: "facts_done", CreatedAtMS: now - 1000,
		Payload: "{}", PayloadHash: "meta-hash", SchemaVersion: agencycontract.SchemaVersion,
	}).Error)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{
		EventID: "meta-pending", OperationID: "meta-pending-op", UserID: 501, MoneySeq: 8,
		EventKind: "agency.billing_finalized", Payload: "{}", PayloadHash: "meta-pending-hash", SchemaVersion: agencycontract.SchemaVersion,
		CreatedAtMS: now,
	}).Error)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: "meta-pending", Status: "pending", NextRetryAt: now / 1000, CreatedAt: now / 1000}).Error)

	meta, err := app.customerProjectionMeta(t.Context(), 501)
	require.NoError(t, err)
	require.Equal(t, "lagging", meta["projection_status"])
	require.Equal(t, "paused", meta["commission_status"])
	require.Equal(t, "7", meta["as_of_money_seq"])
	require.Equal(t, "1", meta["pending_delivery_count"])
}
