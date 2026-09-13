package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

// settleAgencyComponentsTx freezes the gateway's existing price-engine results
// against the exact sources already removed from the wallet. Eligibility never
// influences the proportional allocation of paid/nonpaid funds.
func settleAgencyComponentsTx(tx *gorm.DB, journal *AgencyBillingJournal, event *agencycontract.BillingEvent, snapshot agencycontract.PricingSnapshot) error {
	if event.StandardQuota < 0 || event.CommissionableQuota < 0 || event.CommissionableQuota > event.ChargedTotalQuota ||
		event.NoncommissionableQuota != event.ChargedTotalQuota-event.CommissionableQuota ||
		event.SettlementCostQuota < 0 || event.TheoreticalCommissionQuota < 0 {
		return errors.New("invalid agency charge components")
	}
	parts := append([]agencycontract.BillingComponent(nil), event.Components...)
	if len(parts) == 0 {
		// Preserve the existing model engine's rounding and cost basis. Only
		// split fees whose noncommissionable amount was explicitly identified.
		modelCharge := event.CommissionableQuota
		if modelCharge == 0 {
			modelCharge = event.ChargedTotalQuota
		}
		parts = []agencycontract.BillingComponent{{ComponentID: "default", StandardQuota: event.StandardQuota,
			ChargedTotalQuota: modelCharge, CommissionableQuota: event.CommissionableQuota,
			NoncommissionableQuota: modelCharge - event.CommissionableQuota, SettlementCostQuota: event.SettlementCostQuota,
			TheoreticalCommissionQuota: event.TheoreticalCommissionQuota,
			CommissionEligible:         event.CommissionableQuota > 0 || event.ChargedTotalQuota == 0,
			CommissionSkipReason:       event.CommissionSkipReason}}
		if modelCharge < event.ChargedTotalQuota {
			parts = append(parts, agencycontract.BillingComponent{ComponentID: "noncommissionable",
				ChargedTotalQuota: event.NoncommissionableQuota, NoncommissionableQuota: event.NoncommissionableQuota,
				CommissionSkipReason: "noncommissionable_fee"})
		}
	}
	if len(parts) > 128 {
		return errors.New("too many agency charge components")
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].ComponentID < parts[j].ComponentID })
	charges := make([]agencycontract.ChargeComponent, len(parts))
	var basis [6]int64
	for i, part := range parts {
		charges[i] = agencycontract.ChargeComponent{ComponentID: part.ComponentID, ChargedQuota: part.ChargedTotalQuota}
		values := [6]int64{part.StandardQuota, part.ChargedTotalQuota, part.CommissionableQuota, part.NoncommissionableQuota, part.SettlementCostQuota, part.TheoreticalCommissionQuota}
		for j, value := range values {
			if value < 0 || basis[j] > math.MaxInt64-value {
				return errors.New("agency component price overflow")
			}
			basis[j] += value
		}
		if part.CommissionableQuota > part.ChargedTotalQuota || part.NoncommissionableQuota != part.ChargedTotalQuota-part.CommissionableQuota {
			return errors.New("agency component charge basis mismatch")
		}
		if part.CommissionEligible {
			if part.NoncommissionableQuota != 0 || part.SettlementCostQuota > part.CommissionableQuota || part.TheoreticalCommissionQuota != part.CommissionableQuota-part.SettlementCostQuota {
				return errors.New("agency component commission basis mismatch")
			}
		} else if part.CommissionableQuota != 0 || part.TheoreticalCommissionQuota != 0 {
			return errors.New("ineligible agency component has commission basis")
		}
	}
	if basis != [6]int64{event.StandardQuota, event.ChargedTotalQuota, event.CommissionableQuota, event.NoncommissionableQuota, event.SettlementCostQuota, event.TheoreticalCommissionQuota} {
		return errors.New("agency component price aggregate mismatch")
	}
	var allocations []AgencyFundingAllocation
	if err := tx.Where("user_id = ? AND charge_id = ?", journal.UserID, journal.ChargeID).Find(&allocations).Error; err != nil {
		return err
	}
	var lots []AgencyFundingLot
	lotIDs := make([]int64, 0, len(allocations))
	for _, allocation := range allocations {
		if allocation.LotID > 0 {
			lotIDs = append(lotIDs, allocation.LotID)
		}
	}
	if len(lotIDs) > 0 {
		if err := tx.Where("id IN ?", lotIDs).Find(&lots).Error; err != nil {
			return err
		}
	}
	lotByID := make(map[int64]AgencyFundingLot, len(lots))
	for _, lot := range lots {
		lotByID[lot.ID] = lot
	}
	sort.Slice(allocations, func(i, j int) bool {
		a, b := allocations[i], allocations[j]
		if lotByID[a.LotID].MoneySeq != lotByID[b.LotID].MoneySeq {
			return lotByID[a.LotID].MoneySeq < lotByID[b.LotID].MoneySeq
		}
		if a.LotID != b.LotID {
			return a.LotID < b.LotID
		}
		return a.ID < b.ID
	})
	remaining := make([][3]int64, len(allocations))
	var totals [3]int64
	for i, allocation := range allocations {
		if allocation.LotID > 0 && lotByID[allocation.LotID].UserID != journal.UserID {
			return errors.New("agency component source lot is missing or belongs to another user")
		}
		paid, nonpaid, debt, err := agencyFundingAllocationActiveParts(allocation)
		if err != nil {
			return err
		}
		remaining[i] = [3]int64{paid, nonpaid, debt}
		for j, value := range remaining[i] {
			if totals[j] > int64(common.MaxQuota)-value {
				return errors.New("agency component source overflow")
			}
			totals[j] += value
		}
	}
	if totals[0]+totals[1]+totals[2] != event.ChargedTotalQuota {
		return errors.New("agency component source total does not match charged wallet")
	}
	funding, err := agencycontract.AllocateComponentFunding(charges, totals[0], totals[1])
	if err != nil {
		return err
	}
	event.CommissionableQuota, event.NoncommissionableQuota = 0, 0
	event.TheoreticalCommissionQuota, event.PaidAllocatedQuota = 0, totals[0]
	event.CommissionQuota, event.CommissionAmountMicros, event.ReversedCommissionAmountMicros = 0, 0, 0
	event.CommissionEligible = false
	for i := range parts {
		part := &parts[i]
		part.PaidAllocatedQuota, part.NonpaidAllocatedQuota, part.DebtAllocatedQuota = funding[i].PaidQuota, funding[i].NonpaidQuota, funding[i].DebtQuota
		part.CommissionEligible = part.CommissionEligible && snapshot.CommissionEligible && event.BusinessStatus == "success"
		part.CommissionQuota, part.CommissionAmountMicros, part.ReversedCommissionAmountMicros = 0, 0, 0
		if !part.CommissionEligible {
			part.CommissionableQuota, part.TheoreticalCommissionQuota = 0, 0
			part.NoncommissionableQuota = part.ChargedTotalQuota
			if part.CommissionSkipReason == "" {
				part.CommissionSkipReason = "commission_ineligible"
			}
		} else {
			part.CommissionQuota, err = agencycontract.CommissionForPaid(part.TheoreticalCommissionQuota, part.PaidAllocatedQuota, part.CommissionableQuota, true)
			if err != nil {
				return err
			}
			part.CommissionAmountMicros, err = agencyFrozenCommissionMicros(part.CommissionQuota, snapshot)
			if err != nil {
				return err
			}
		}
		event.CommissionableQuota += part.CommissionableQuota
		event.NoncommissionableQuota += part.NoncommissionableQuota
		event.TheoreticalCommissionQuota += part.TheoreticalCommissionQuota
		event.CommissionQuota += part.CommissionQuota
		if event.CommissionAmountMicros > math.MaxInt64-part.CommissionAmountMicros {
			return errors.New("agency component commission aggregate overflow")
		}
		event.CommissionAmountMicros += part.CommissionAmountMicros
		event.CommissionEligible = event.CommissionEligible || part.CommissionEligible
		encoded, err := common.Marshal(part)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(part.ComponentID))
		component := AgencyChargeComponent{ChargeID: journal.ChargeID, SegmentNo: journal.SegmentNo,
			ComponentKey: hex.EncodeToString(digest[:]), ComponentID: part.ComponentID, UserID: journal.UserID,
			OriginalResult: string(encoded), Version: 1}
		if err := tx.Create(&component).Error; err != nil {
			return err
		}
		needed := [3]int64{part.PaidAllocatedQuota, part.NonpaidAllocatedQuota, part.DebtAllocatedQuota}
		for j, allocation := range allocations {
			var take [3]int64
			for source := range needed {
				take[source] = min(needed[source], remaining[j][source])
				needed[source] -= take[source]
				remaining[j][source] -= take[source]
			}
			if take == [3]int64{} {
				continue
			}
			row := AgencyComponentFunding{ChargeComponentID: component.ID, AllocationID: allocation.ID, LotID: allocation.LotID,
				PaidQuota: take[0], NonpaidQuota: take[1], DebtQuota: take[2], Version: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		if needed != [3]int64{} {
			return errors.New("agency component source matrix is incomplete")
		}
	}
	event.Components = parts
	event.SchemaVersion = agencycontract.ComponentSchemaVersion
	return agencycontract.ValidateBillingComponents(*event)
}
