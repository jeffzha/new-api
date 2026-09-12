package agencyhub

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/require"
)

// TestWithdrawalReversalFreezesOutstandingRequests locks in the §22.3
// invariant that a commission reversal freezes every outstanding withdrawal
// (submitted/reviewing/approved) for the affected agency+currency while paid
// withdrawals remain immutable.
func TestWithdrawalReversalFreezesOutstandingRequests(t *testing.T) {
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "wd-reversal-freeze", DisplayName: "Reversal Freeze", Status: AgencyStatusActive, InviteCode: "WDFREEZE", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{
		AgencyID: agency.ID, CurrencyCode: "CNY", AvailableMicros: 0, LockedMicros: 0, Version: 1,
	}).Error)

	withdrawals := []model.AgencyWithdrawal{
		{RequestNo: "wd-freeze-1", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 50, Status: "submitted", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1},
		{RequestNo: "wd-freeze-2", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 50, Status: "reviewing", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1},
		{RequestNo: "wd-freeze-3", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 50, Status: "approved", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1},
		{RequestNo: "wd-freeze-paid", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 50, Status: "paid", Version: 1, PaymentReference: "paid-before-reversal", CreatedAtMS: 1, UpdatedAtMS: 1},
	}
	require.NoError(t, app.db.Create(&withdrawals).Error)

	original := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-withdrawal-freeze-original",
		EventType: "agency.billing_finalized", OperationID: "op-withdrawal-freeze-original",
		UserID: 1, AgencyID: &agency.ID, OriginModelName: "model", CurrencyCode: "CNY",
		CommissionEligible: true, CommissionAmountMicros: 100, OccurredAtMS: time.Now().UnixMilli(),
	}
	reversal := original
	reversal.EventID = "evt-withdrawal-freeze-reversal"
	reversal.EventType = "agency.billing_reversed"
	reversal.OriginalEventID = original.EventID
	reversal.OperationID = "op-withdrawal-freeze-reversal"
	reversal.CommissionAmountMicros = 0
	reversal.ReversedCommissionAmountMicros = 40
	for _, event := range []agencycontract.BillingEvent{original, reversal} {
		payload, err := common.Marshal(event)
		require.NoError(t, err)
		payloadHash, err := agencycontract.CanonicalHash(event)
		require.NoError(t, err)
		require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{
			EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType,
			UserID: event.UserID, Payload: string(payload), PayloadHash: payloadHash,
			SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS,
		}).Error)
		require.NoError(t, app.db.Create(&model.AgencyEventDelivery{
			EventID: event.EventID, Status: "pending", NextRetryAt: 1, CreatedAt: 1,
		}).Error)
	}

	require.NoError(t, app.RunConsumerOnce(t.Context(), 10))
	require.NoError(t, app.RunConsumerOnce(t.Context(), 10))

	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", agency.ID, "CNY").First(&balance).Error)
	require.Equal(t, int64(60), balance.AvailableMicros)
	require.Equal(t, int64(100), balance.EarnedMicros)
	require.Equal(t, int64(40), balance.ReversedMicros)

	var frozen []model.AgencyWithdrawal
	require.NoError(t, app.db.Where("agency_id = ? AND status = ? AND id IN ?", agency.ID, "on_hold", []int64{withdrawals[0].ID, withdrawals[1].ID, withdrawals[2].ID}).Find(&frozen).Error)
	require.Len(t, frozen, 3)
	expectedPrevious := map[int64]string{withdrawals[0].ID: "submitted", withdrawals[1].ID: "reviewing", withdrawals[2].ID: "approved"}
	for _, row := range frozen {
		require.Equal(t, expectedPrevious[row.ID], row.PreviousStatus)
		require.Contains(t, row.OnHoldReason, reversal.EventID)
	}

	var paid model.AgencyWithdrawal
	require.NoError(t, app.db.First(&paid, withdrawals[3].ID).Error)
	require.Equal(t, "paid", paid.Status)
	require.Equal(t, "paid-before-reversal", paid.PaymentReference)

	var transitionCount int64
	require.NoError(t, app.db.Model(&model.AgencyWithdrawalTransition{}).
		Where("withdrawal_id IN ? AND after_status = ?", []int64{withdrawals[0].ID, withdrawals[1].ID, withdrawals[2].ID}, "on_hold").
		Count(&transitionCount).Error)
	require.Equal(t, int64(3), transitionCount)
}
