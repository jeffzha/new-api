package opswallet

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/require"
)

func TestOPSCommissionNonpaidPriceDifferenceRecordsDebitWithoutInventingCommission(t *testing.T) {
	db := commissionTestDB(t)
	applyCommission(t, db, commissionEntry(1, "credit", "2", "2"))
	snapshot := &agencycontract.PricingSnapshot{UserID: 1, AgencyID: 7, BindingID: 8, OriginModelName: "local-model", CurrencyCode: "USD", ExchangeRate: "1", CommissionEligible: true, SalesBPS: 5500, SettlementBPS: 4000}
	seedCommissionHold(t, db, "nonpaid-price-difference", 1, snapshot)
	standard := "1"
	charge := commissionEntry(2, "charge", "-0.6", "1.4")
	charge.RequestID, charge.Model, charge.StandardAmount = "nonpaid-price-difference", "local-model", &standard
	applyCommission(t, db, charge)
	applyCommission(t, db, charge)
	var recorded model.OPSAgencyCharge
	require.NoError(t, db.First(&recorded).Error)
	require.Equal(t, int64(600000), recorded.AmountMicros)
	var event agencycontract.BillingEvent
	require.NoError(t, common.Unmarshal([]byte(recorded.EventJSON), &event))
	require.False(t, event.CommissionEligible)
	require.Equal(t, int64(600000), event.NonpaidAllocatedQuota)
	require.Equal(t, int64(600000), event.NoncommissionableQuota)
	require.Zero(t, event.CommissionAmountMicros)
	require.Empty(t, event.CommissionSplits)
	require.Equal(t, "nonpaid_price_difference_review", event.CommissionSkipReason)
	require.Contains(t, event.BillingBasis, "550000")
	require.Contains(t, event.BillingBasis, "600000")
	var hold model.OPSWalletHold
	require.NoError(t, db.First(&hold).Error)
	require.Contains(t, hold.AgencyPricing, "5500", "the frozen quote is never rewritten")
	refund := commissionEntry(3, "refund", "0.3", "1.7")
	refund.RequestID, refund.RefundID = charge.RequestID, "partial-nonpaid-refund"
	applyCommission(t, db, refund)
	applyCommission(t, db, refund)
	var account model.OPSAgencyAccount
	require.NoError(t, db.First(&account).Error)
	require.Equal(t, int64(1700000), account.BalanceMicros)
	var count int64
	require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&count).Error)
	require.Zero(t, count)

	// Real payment or debt still requires a separate reviewed correction.
	for _, lot := range []model.OPSAgencyLot{{Paid: true, Amount: 600000}, {Source: "debt", Amount: 600000}} {
		_, err := commissionCharge(charge, snapshot, []model.OPSAgencyLot{lot}, time.Now().UnixMilli())
		require.Error(t, err)
	}
}
