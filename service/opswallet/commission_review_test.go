package opswallet

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/require"
)

func TestOPSCommissionReviewedReplayRetainsSequenceAndRollback(t *testing.T) {
	db := commissionTestDB(t)
	applyCommission(t, db, commissionEntry(1, "credit", "2", "2"))
	require.NoError(t, db.Create(&model.OPSAgencyCursor{ID: model.OPSAgencyID("cursor", "commission"), EntryID: 3}).Error)
	snapshot := &agencycontract.PricingSnapshot{UserID: 1, AgencyID: 7, BindingID: 8, OriginModelName: "local-model", CurrencyCode: "USD", ExchangeRate: "1", CommissionEligible: true, SalesBPS: 5500, SettlementBPS: 4000}
	seedCommissionHold(t, db, "reviewed-original", 1, snapshot)
	standard := "1"
	charge := commissionEntry(2, "charge", "-0.6", "1.4")
	charge.RequestID, charge.Model, charge.StandardAmount = "reviewed-original", "local-model", &standard
	credit := commissionEntry(3, "credit", "1", "2.4")
	require.NoError(t, retainCommissionEntry(db, charge))
	require.NoError(t, retainCommissionEntry(db, credit))
	count, err := ReplayReviewedCommissionInbox(t.Context(), db, 1, 3, 3)
	require.Error(t, err)
	require.Zero(t, count)
	require.NoError(t, db.Model(&model.OPSAgencyPending{}).Where("entry_id=3").Update("payload_hash", "invalid").Error)
	count, err = ReplayReviewedCommissionInbox(t.Context(), db, 1, 2, 3)
	require.Error(t, err)
	require.Zero(t, count)
	var receipts int64
	require.NoError(t, db.Model(&model.OPSAgencyCharge{}).Count(&receipts).Error)
	require.Zero(t, receipts, "entire reviewed range rolls back")
	require.NoError(t, db.Where("entry_id=3").Delete(&model.OPSAgencyPending{}).Error)
	require.NoError(t, retainCommissionEntry(db, credit))
	count, err = ReplayReviewedCommissionInbox(t.Context(), db, 1, 2, 3)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	count, err = ReplayReviewedCommissionInbox(t.Context(), db, 1, 2, 3)
	require.NoError(t, err)
	require.Zero(t, count)
	var account model.OPSAgencyAccount
	require.NoError(t, db.First(&account).Error)
	require.Equal(t, int64(2400000), account.BalanceMicros)
	var row model.OPSAgencyCharge
	require.NoError(t, db.First(&row).Error)
	var event agencycontract.BillingEvent
	require.NoError(t, common.Unmarshal([]byte(row.EventJSON), &event))
	require.Zero(t, event.CommissionAmountMicros)
}
