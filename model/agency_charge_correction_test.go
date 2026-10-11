package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgencyChargeCorrectionRealignsTheRecordedTotalWithoutMovingMoney(t *testing.T) {
	// The historical mismatch this corrects: an upstream adjustment reached the
	// wallet while the journal kept the pre-adjustment total, so the agency usage
	// record no longer matched the platform consumption log.
	db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		Mode: "tokens", QuotaPerUnit: 10, ModelRatio: 1.25, OtherMultiplier: 2}, 100)
	chargeID := task.PrivateData.BillingContext.AgencyChargeID
	task.Status = TaskStatusSuccess
	task.PrivateData.BillingContext.AgencyPricing.OriginModelName = "doubao-seedance-2-0-260128"
	_, err := CompleteAgencyTask(&task, TaskStatusInProgress, 40)
	require.NoError(t, err)

	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", chargeID).First(&journal).Error)
	require.Equal(t, "finalized", journal.Status)
	require.Equal(t, int64(80), journal.ChargedTotalQuota)
	finalizeRevision := journal.Revision
	occurredAt := journal.CreatedAtMS

	// Historical upstream bills were refunded straight into the wallet, so the
	// funding allocation is the authoritative amount while the journal keeps
	// the pre-refund total.
	require.NoError(t, AdjustAgencyCharge(user.Id, -30, chargeID, 0))
	require.NoError(t, db.Where("charge_id = ?", chargeID).First(&journal).Error)
	assert.Equal(t, int64(80), journal.ChargedTotalQuota, "the legacy adjustment never touched the journal")

	targets, _, _, err := AgencyChargeCorrectionTargets(0, 50)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, chargeID, targets[0].ChargeID)
	assert.True(t, targets[0].Eligible, targets[0].Detail)
	assert.Equal(t, int64(80), targets[0].PreviousChargedTotalQuota)
	assert.Equal(t, int64(50), targets[0].AllocatedQuota)

	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	quotaBefore, remainBefore, usedBefore := user.Quota, token.RemainQuota, token.UsedQuota

	result, err := CorrectAgencyTaskCharge(chargeID, 42, "align with the wallet allocation")
	require.NoError(t, err)
	assert.False(t, result.AlreadyApplied)
	assert.Equal(t, int64(80), result.PreviousChargedTotalQuota)
	assert.Equal(t, int64(50), result.CorrectedChargedTotalQuota)
	assert.NotEmpty(t, result.EventID)
	assert.NotEmpty(t, result.OperationID)

	require.NoError(t, db.Where("charge_id = ?", chargeID).First(&journal).Error)
	assert.Equal(t, "finalized", journal.Status)
	assert.Equal(t, int64(50), journal.ChargedTotalQuota)
	assert.Greater(t, journal.Revision, finalizeRevision)
	assert.Equal(t, int64(50), journal.ChargedTotalQuota-journal.ReversedQuota)

	var operation AgencyBillingOperation
	require.NoError(t, db.Where("charge_id = ? AND segment_no = 0 AND revision = ? AND operation = ?",
		chargeID, journal.Revision, "correct").First(&operation).Error)
	assert.NotEmpty(t, operation.OperationID)
	assert.Greater(t, operation.MoneySeq, int64(0))

	var outbox AgencyBillingOutbox
	require.NoError(t, db.Where("event_id = ?", result.EventID).First(&outbox).Error)
	assert.Equal(t, "agency.billing_corrected", outbox.EventKind)
	assert.Equal(t, occurredAt, outbox.CreatedAtMS, "the correction keeps the occurrence of the original call")
	var event agencycontract.BillingEvent
	require.NoError(t, common.UnmarshalJsonStr(outbox.Payload, &event))
	assert.Equal(t, agencycontract.BillingEventCorrected, event.EventType)
	assert.NotEmpty(t, event.OriginalEventID)
	assert.Equal(t, int64(50), event.ChargedTotalQuota)
	assert.Equal(t, occurredAt, event.OccurredAtMS)
	assert.Equal(t, event.ChargedTotalQuota, event.PaidAllocatedQuota+event.NonpaidAllocatedQuota+event.DebtAllocatedQuota)
	assert.Equal(t, event.CommissionableQuota, event.SettlementCostQuota+event.TheoreticalCommissionQuota)

	var correction AgencyBillingCorrection
	require.NoError(t, db.Where("charge_id = ?", chargeID).First(&correction).Error)
	assert.Equal(t, int64(42), correction.OperatorID)
	assert.Equal(t, int64(80), correction.PreviousChargedTotalQuota)
	assert.Equal(t, int64(50), correction.CorrectedChargedTotalQuota)
	assert.Equal(t, result.EventID, correction.EventID)
	assert.Equal(t, "align with the wallet allocation", correction.Reason)
	assert.Equal(t, event.OriginalEventID, correction.OriginalEventID)

	// A correction never moves money.
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, quotaBefore, user.Quota)
	assert.Equal(t, remainBefore, token.RemainQuota)
	assert.Equal(t, usedBefore, token.UsedQuota)

	// Replaying the same target is inert and keeps a single audit trail.
	replay, err := CorrectAgencyTaskCharge(chargeID, 42, "align with the wallet allocation")
	require.NoError(t, err)
	assert.True(t, replay.AlreadyApplied)
	var corrections, corrections2, outboxCount int64
	require.NoError(t, db.Model(&AgencyBillingCorrection{}).Where("charge_id = ?", chargeID).Count(&corrections).Error)
	require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", chargeID, "correct").Count(&corrections2).Error)
	require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("event_kind = ?", "agency.billing_corrected").Count(&outboxCount).Error)
	assert.Equal(t, int64(1), corrections)
	assert.Equal(t, int64(1), corrections2)
	assert.Equal(t, int64(1), outboxCount)
}

func TestAgencyChargeCorrectionBacklogAppliesEveryEligibleCharge(t *testing.T) {
	_, task, user, _ := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		Mode: "tokens", QuotaPerUnit: 10, ModelRatio: 1.25, OtherMultiplier: 2}, 100)
	chargeID := task.PrivateData.BillingContext.AgencyChargeID
	task.Status = TaskStatusSuccess
	_, err := CompleteAgencyTask(&task, TaskStatusInProgress, 40)
	require.NoError(t, err)
	require.NoError(t, AdjustAgencyCharge(user.Id, -30, chargeID, 0))

	applied, blocked, err := CorrectAgencyChargeBacklog(7, "backlog alignment", 10)
	require.NoError(t, err)
	require.Empty(t, blocked)
	require.Len(t, applied, 1)
	assert.Equal(t, chargeID, applied[0].ChargeID)
	assert.Equal(t, int64(50), applied[0].CorrectedChargedTotalQuota)

	// A second pass finds nothing left to correct.
	applied, blocked, err = CorrectAgencyChargeBacklog(7, "backlog alignment", 10)
	require.NoError(t, err)
	assert.Empty(t, applied)
	assert.Empty(t, blocked)
}
