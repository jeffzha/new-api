package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

// ErrAgencyComponentRefundProvenance blocks a refund whose current source
// history cannot prove the exact lot or debt repayment to restore. Returning
// this error rolls back wallet, token, commission and all cumulative markers.
var ErrAgencyComponentRefundProvenance = errors.New("agency component refund requires intact funding provenance")

// AgencyComponentRefundInput is an internal gateway financial command, not a
// public customer-controlled price DTO. The caller authorizes the refund.
// CumulativeQuota targets the named original component, or the whole charge
// when ComponentID is empty. RefundID identifies this exact command permanently.
type AgencyComponentRefundInput struct {
	UserID          int64  `json:"user_id"`
	ChargeID        string `json:"charge_id"`
	SegmentNo       int    `json:"segment_no"`
	ComponentID     string `json:"component_id,omitempty"`
	CumulativeQuota int64  `json:"cumulative_quota"`
	RefundID        string `json:"refund_id"`
	Reason          string `json:"reason"`
}

// AgencyRefundWalletCharge commits the complete source-aware model refund.
// Payment chargebacks must use their separate funding-reversal transaction.
func AgencyRefundWalletCharge(input AgencyComponentRefundInput, tokenKey string) (agencycontract.BillingEvent, error) {
	var event agencycontract.BillingEvent
	err := runAgencyFundingTransaction(func(tx *gorm.DB) error {
		var err error
		event, err = AgencyRefundWalletChargeTx(tx, input, tokenKey)
		return err
	})
	if err == nil && common.RedisEnabled {
		if cacheErr := InvalidateUserCache(int(input.UserID)); cacheErr != nil {
			common.SysError("agency refunded wallet cache invalidation: " + cacheErr.Error())
		}
		if tokenKey != "" {
			if cacheErr := InvalidateTokenCache(tokenKey); cacheErr != nil {
				common.SysError("agency refunded token cache invalidation: " + cacheErr.Error())
			}
		}
	}
	return event, err
}

// AgencyRefundWalletChargeTx requires the caller's transaction. Locks follow
// settlement order: user, funding account, journal, component/source rows.
func AgencyRefundWalletChargeTx(tx *gorm.DB, input AgencyComponentRefundInput, tokenKey string) (agencycontract.BillingEvent, error) {
	if tx == nil || input.UserID <= 0 || input.SegmentNo < 0 || input.CumulativeQuota < 0 || input.CumulativeQuota > int64(common.MaxQuota) ||
		input.ChargeID == "" || strings.TrimSpace(input.ChargeID) != input.ChargeID || len(input.ChargeID) > 128 ||
		input.RefundID == "" || strings.TrimSpace(input.RefundID) != input.RefundID || len(input.RefundID) > 128 ||
		strings.TrimSpace(input.ComponentID) != input.ComponentID || len(input.ComponentID) > 128 || input.Reason == "payment_chargeback" {
		return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
	}
	inputHash, err := agencycontract.CanonicalHash(input)
	if err != nil {
		return agencycontract.BillingEvent{}, err
	}
	digest := sha256.Sum256([]byte(input.RefundID))
	refundKey := "refund:" + hex.EncodeToString(digest[:])
	var user User
	if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, input.UserID).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if user.BillingMode != AgencyDurableBillingMode {
		return agencycontract.BillingEvent{}, ErrAgencyFundingUnavailable
	}
	if _, err := expireAgencyRedemptionLotsTx(tx, input.UserID, common.GetTimestamp()); err != nil {
		return agencycontract.BillingEvent{}, err
	}
	var before AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", input.UserID).First(&before).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	var journal AgencyBillingJournal
	if err := AgencyLockForUpdate(tx).Where("charge_id = ? AND segment_no = ?", input.ChargeID, input.SegmentNo).First(&journal).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if journal.UserID != input.UserID {
		return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
	}
	var prior AgencyBillingOperation
	err = tx.Where("charge_id = ? AND segment_no = ? AND operation = ? AND usage_hash = ?", input.ChargeID, input.SegmentNo, "reverse", refundKey).First(&prior).Error
	if err == nil {
		if prior.InputHash != inputHash {
			return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
		}
		var replay agencycontract.BillingEvent
		if err := common.UnmarshalJsonStr(prior.CommittedResult, &replay); err != nil {
			return replay, err
		}
		return replay, agencycontract.ValidateBillingComponents(replay)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return agencycontract.BillingEvent{}, err
	}
	if journal.Status != "finalized" && journal.Status != "partially_reversed" && journal.Status != "reversed" {
		return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
	}
	var finalized AgencyBillingOperation
	if err := tx.Where("charge_id = ? AND segment_no = ? AND operation = ?", input.ChargeID, input.SegmentNo, "finalize").Order("revision DESC").First(&finalized).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	var original agencycontract.BillingEvent
	if err := common.UnmarshalJsonStr(finalized.CommittedResult, &original); err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if original.SchemaVersion != agencycontract.ComponentSchemaVersion || original.EventID == "" || original.UserID != input.UserID ||
		original.FinancialChargeID != input.ChargeID || original.SegmentNo != input.SegmentNo {
		return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
	}
	if err := agencycontract.ValidateBillingComponents(original); err != nil {
		return agencycontract.BillingEvent{}, err
	}
	var components []AgencyChargeComponent
	if err := AgencyLockForUpdate(tx).Where("charge_id = ? AND segment_no = ?", input.ChargeID, input.SegmentNo).Find(&components).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if len(components) != len(original.Components) {
		return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
	}
	sort.Slice(components, func(i, j int) bool { return components[i].ComponentID < components[j].ComponentID })
	originals := make(map[string]agencycontract.BillingComponent, len(original.Components))
	for _, component := range original.Components {
		originals[component.ComponentID] = component
	}
	funding := make([]agencycontract.ComponentFunding, len(components))
	var previousTotal, previousMicros int64
	for i, component := range components {
		var stored agencycontract.BillingComponent
		if err := common.UnmarshalJsonStr(component.OriginalResult, &stored); err != nil {
			return agencycontract.BillingEvent{}, err
		}
		if component.UserID != input.UserID || stored != originals[component.ComponentID] || stored.ComponentID != component.ComponentID ||
			component.RefundedQuota < 0 || component.RefundedQuota > stored.ChargedTotalQuota || component.ReversedCommissionMicros < 0 || component.ReversedCommissionMicros > stored.CommissionAmountMicros {
			return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
		}
		funding[i] = agencycontract.ComponentFunding{ComponentID: component.ComponentID, ChargedQuota: stored.ChargedTotalQuota,
			PaidQuota: stored.PaidAllocatedQuota, NonpaidQuota: stored.NonpaidAllocatedQuota, DebtQuota: stored.DebtAllocatedQuota}
		priorRefund, err := agencycontract.CumulativeComponentRefund(funding[i], component.RefundedQuota)
		if err != nil {
			return agencycontract.BillingEvent{}, err
		}
		if priorRefund.RestoredPaidQuota != component.RestoredPaidQuota || priorRefund.RestoredNonpaidQuota != component.RestoredNonpaidQuota ||
			priorRefund.RestoredDebtQuota != component.RestoredDebtQuota ||
			component.ReversedCommissionQuota != agencyRefundProportion(stored.CommissionQuota, component.RefundedQuota, stored.ChargedTotalQuota) ||
			component.ReversedCommissionMicros != agencyRefundProportion(stored.CommissionAmountMicros, component.RefundedQuota, stored.ChargedTotalQuota) {
			return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
		}
		previousTotal += component.RefundedQuota
		previousMicros += component.ReversedCommissionMicros
	}
	if previousTotal != journal.ReversedQuota || previousMicros != journal.ReversedCommissionQuota {
		return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
	}
	var targets []agencycontract.ComponentRefund
	if input.ComponentID == "" {
		targets, err = agencycontract.CumulativeComponentRefunds(funding, input.CumulativeQuota)
		if err != nil {
			return agencycontract.BillingEvent{}, err
		}
	} else {
		targets = make([]agencycontract.ComponentRefund, len(components))
		found := false
		for i, component := range components {
			amount := component.RefundedQuota
			if component.ComponentID == input.ComponentID {
				amount, found = input.CumulativeQuota, true
			}
			targets[i], err = agencycontract.CumulativeComponentRefund(funding[i], amount)
			if err != nil {
				return agencycontract.BillingEvent{}, err
			}
		}
		if !found {
			return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
		}
	}
	var paidDelta, nonpaidDelta, debtDelta, refundDelta int64
	for i, target := range targets {
		component := components[i]
		if target.RefundedQuota < component.RefundedQuota || target.RestoredPaidQuota < component.RestoredPaidQuota ||
			target.RestoredNonpaidQuota < component.RestoredNonpaidQuota || target.RestoredDebtQuota < component.RestoredDebtQuota {
			return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
		}
		paidDelta += target.RestoredPaidQuota - component.RestoredPaidQuota
		nonpaidDelta += target.RestoredNonpaidQuota - component.RestoredNonpaidQuota
		debtDelta += target.RestoredDebtQuota - component.RestoredDebtQuota
		refundDelta += target.RefundedQuota - component.RefundedQuota
	}
	if paidDelta+nonpaidDelta+debtDelta != refundDelta {
		return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
	}
	var debtRestoration agencyComponentDebtRestoration
	event := original
	event.EventID, event.OperationID = "", ""
	event.EventType, event.OriginalEventID = "agency.billing_reversed", original.EventID
	event.BusinessStatus = input.Reason
	event.StandardQuota, event.ChargedTotalQuota, event.CommissionableQuota, event.NoncommissionableQuota = 0, 0, 0, 0
	event.SettlementCostQuota, event.TheoreticalCommissionQuota, event.PaidAllocatedQuota = 0, 0, 0
	event.CommissionQuota, event.CommissionAmountMicros, event.ReversedCommissionAmountMicros = 0, 0, 0
	event.CommissionEligible, event.CommissionSkipReason = false, ""
	event.Components = make([]agencycontract.BillingComponent, 0, len(components))
	for i := range components {
		component, target := &components[i], targets[i]
		originalPart := originals[component.ComponentID]
		if err := restoreAgencyComponentSourcesTx(tx, journal, *component, target, &debtRestoration); err != nil {
			return agencycontract.BillingEvent{}, err
		}
		commission := agencyRefundProportion(originalPart.CommissionQuota, target.RefundedQuota, originalPart.ChargedTotalQuota)
		micros := agencyRefundProportion(originalPart.CommissionAmountMicros, target.RefundedQuota, originalPart.ChargedTotalQuota)
		if commission < component.ReversedCommissionQuota || micros < component.ReversedCommissionMicros {
			return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
		}
		part := agencycontract.BillingComponent{ComponentID: component.ComponentID, ChargedTotalQuota: target.RefundedQuota - component.RefundedQuota,
			PaidAllocatedQuota: target.RestoredPaidQuota - component.RestoredPaidQuota, NonpaidAllocatedQuota: target.RestoredNonpaidQuota - component.RestoredNonpaidQuota,
			DebtAllocatedQuota: target.RestoredDebtQuota - component.RestoredDebtQuota, CommissionQuota: commission - component.ReversedCommissionQuota,
			ReversedCommissionAmountMicros: micros - component.ReversedCommissionMicros, CommissionEligible: originalPart.CommissionEligible,
			CommissionSkipReason: originalPart.CommissionSkipReason}
		if part.CommissionEligible {
			part.CommissionableQuota = part.ChargedTotalQuota
		} else {
			part.NoncommissionableQuota = part.ChargedTotalQuota
		}
		event.Components = append(event.Components, part)
		event.ChargedTotalQuota += part.ChargedTotalQuota
		event.CommissionableQuota += part.CommissionableQuota
		event.NoncommissionableQuota += part.NoncommissionableQuota
		event.PaidAllocatedQuota += part.PaidAllocatedQuota
		event.CommissionQuota += part.CommissionQuota
		event.ReversedCommissionAmountMicros += part.ReversedCommissionAmountMicros
		event.CommissionEligible = event.CommissionEligible || part.CommissionEligible
		if err := tx.Model(component).Updates(map[string]any{"refunded_quota": target.RefundedQuota,
			"restored_paid_quota": target.RestoredPaidQuota, "restored_nonpaid_quota": target.RestoredNonpaidQuota,
			"restored_debt_quota": target.RestoredDebtQuota, "reversed_commission_quota": commission,
			"reversed_commission_micros": micros, "version": component.Version + 1}).Error; err != nil {
			return agencycontract.BillingEvent{}, err
		}
	}
	if debtRestoration.PaidRestored+debtRestoration.NonpaidRestored+debtRestoration.ExpiredDebtNonpaid+debtRestoration.DebtReduced != debtDelta || before.DebtQuota < debtRestoration.DebtReduced {
		return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
	}
	paidDelta += debtRestoration.PaidRestored
	nonpaidDelta += debtRestoration.NonpaidRestored - debtRestoration.ExpiredDirectNonpaid
	debtDelta = debtRestoration.DebtReduced
	repaymentOperation := fmt.Sprintf("%s:%d:%s", journal.ChargeID, journal.SegmentNo, refundKey)
	paidRepaid, nonpaidRepaid, err := repayAgencyFundingSourcesTx(tx, input.UserID, repaymentOperation, before.MoneySeq+1, before.DebtQuota-debtDelta, debtRestoration.Sources)
	if err != nil {
		return agencycontract.BillingEvent{}, err
	}
	paidDelta -= paidRepaid
	nonpaidDelta -= nonpaidRepaid
	debtDelta += paidRepaid + nonpaidRepaid
	expiredNonpaid := debtRestoration.ExpiredDebtNonpaid + debtRestoration.ExpiredDirectNonpaid
	walletRefund := refundDelta - expiredNonpaid
	if walletRefund < 0 || int64(user.Quota) > int64(common.MaxQuota)-walletRefund || before.PaidAvailable > int64(common.MaxQuota)-paidDelta || before.NonpaidAvailable > int64(common.MaxQuota)-nonpaidDelta {
		return agencycontract.BillingEvent{}, ErrAgencyFundingUnavailable
	}
	if int64(user.Quota) != before.PaidAvailable+before.NonpaidAvailable-before.DebtQuota {
		return agencycontract.BillingEvent{}, ErrAgencyComponentRefundProvenance
	}
	if err := tx.Model(&user).Update("quota", int64(user.Quota)+walletRefund).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if journal.TokenID != nil && *journal.TokenID > 0 {
		if err := agencyAdjustAcceptedTokenTx(tx, *journal.TokenID, tokenKey, -refundDelta); err != nil {
			return agencycontract.BillingEvent{}, err
		}
	}
	if err := tx.Model(&AgencyFundingAccount{}).Where("user_id = ?", before.UserID).Updates(map[string]any{"paid_available": before.PaidAvailable + paidDelta,
		"nonpaid_available": before.NonpaidAvailable + nonpaidDelta, "debt_quota": before.DebtQuota - debtDelta}).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	journal.ReversedQuota += refundDelta
	journal.ReversedCommissionQuota += event.ReversedCommissionAmountMicros // historical column stores micros
	if journal.ReversedQuota > 0 {
		journal.Status = "partially_reversed"
	}
	if journal.ReversedQuota > 0 && journal.ReversedQuota == journal.ChargedTotalQuota {
		journal.Status = "reversed"
	}
	event.BillingStatus = journal.Status
	if err := agencycontract.ValidateBillingComponents(event); err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if err := writeAgencyJournalEventTx(tx, &journal, &event, "reverse", inputHash, before); err != nil {
		return agencycontract.BillingEvent{}, err
	}
	if err := tx.Model(&AgencyBillingOperation{}).Where("operation_id = ?", event.OperationID).Update("usage_hash", refundKey).Error; err != nil {
		return agencycontract.BillingEvent{}, err
	}
	return event, nil
}

// agencyRefundProportion computes an exact cumulative commission watermark.
// Financial inputs were validated against the immutable finalized event.
func agencyRefundProportion(original, refund, charge int64) int64 {
	if original == 0 || refund == 0 {
		return 0
	}
	product := new(big.Int).Mul(big.NewInt(original), big.NewInt(refund))
	quotient, remainder := new(big.Int), new(big.Int)
	denominator := big.NewInt(charge)
	quotient.QuoRem(product, denominator, remainder)
	if remainder.Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient.Int64()
}

// restoreAgencyComponentSourcesTx returns cumulative source targets in the
// original per-source FIFO matrix order. Allocation IDs are never replaced.
type agencyComponentDebtRestoration struct {
	PaidRestored         int64
	NonpaidRestored      int64
	ExpiredDebtNonpaid   int64
	ExpiredDirectNonpaid int64
	DebtReduced          int64
	Sources              []agencyFundingSource
}

func restoreAgencyComponentSourcesTx(tx *gorm.DB, journal AgencyBillingJournal, component AgencyChargeComponent, target agencycontract.ComponentRefund, debtRestoration *agencyComponentDebtRestoration) error {
	var matrix []AgencyComponentFunding
	if err := AgencyLockForUpdate(tx).Where("charge_component_id = ?", component.ID).Order("id ASC").Find(&matrix).Error; err != nil {
		return err
	}
	var original agencycontract.BillingComponent
	if err := common.UnmarshalJsonStr(component.OriginalResult, &original); err != nil {
		return err
	}
	var originalTotals, restoredTotals [3]int64
	for _, row := range matrix {
		values, restored := [3]int64{row.PaidQuota, row.NonpaidQuota, row.DebtQuota}, [3]int64{row.RestoredPaidQuota, row.RestoredNonpaidQuota, row.RestoredDebtQuota}
		for source := range values {
			if values[source] < 0 || restored[source] < 0 || restored[source] > values[source] || values[source] > int64(common.MaxQuota)-originalTotals[source] {
				return ErrAgencyComponentRefundProvenance
			}
			originalTotals[source] += values[source]
			restoredTotals[source] += restored[source]
		}
	}
	if originalTotals != [3]int64{original.PaidAllocatedQuota, original.NonpaidAllocatedQuota, original.DebtAllocatedQuota} ||
		restoredTotals != [3]int64{component.RestoredPaidQuota, component.RestoredNonpaidQuota, component.RestoredDebtQuota} {
		return ErrAgencyComponentRefundProvenance
	}
	remaining := [3]int64{target.RestoredPaidQuota, target.RestoredNonpaidQuota, target.RestoredDebtQuota}
	for _, row := range matrix {
		values, restored := [3]int64{row.PaidQuota, row.NonpaidQuota, row.DebtQuota}, [3]int64{row.RestoredPaidQuota, row.RestoredNonpaidQuota, row.RestoredDebtQuota}
		var delta, next [3]int64
		for source := range values {
			next[source] = min(remaining[source], values[source])
			remaining[source] -= next[source]
			delta[source] = next[source] - restored[source]
			if delta[source] < 0 {
				return ErrAgencyComponentRefundProvenance
			}
		}
		if delta == [3]int64{} {
			continue
		}
		var allocation AgencyFundingAllocation
		if err := AgencyLockForUpdate(tx).First(&allocation, row.AllocationID).Error; err != nil {
			return err
		}
		if allocation.UserID != journal.UserID || allocation.ChargeID != journal.ChargeID || allocation.LotID != row.LotID ||
			allocation.Reversed != 0 || allocation.RevokedReservedDebt != 0 || allocation.RevokedNonpaid != 0 {
			return ErrAgencyComponentRefundProvenance
		}
		if allocation.Consumed < delta[0] || allocation.NonpaidConsumed < delta[1] || allocation.DebtConsumed < delta[2] ||
			(delta[0] > 0 && row.LotID <= 0) || (delta[2] > 0 && row.LotID != 0) {
			return ErrAgencyComponentRefundProvenance
		}
		if delta[2] > 0 {
			sources, debt, expired, err := restoreAgencyAllocationDebtTx(tx, allocation, delta[2], false)
			if err != nil {
				return err
			}
			for _, source := range sources {
				debtRestoration.PaidRestored += source.Paid
				debtRestoration.NonpaidRestored += source.Nonpaid
			}
			debtRestoration.Sources = append(debtRestoration.Sources, sources...)
			debtRestoration.DebtReduced += debt
			debtRestoration.ExpiredDebtNonpaid += expired
		}
		if row.LotID > 0 {
			var lot AgencyFundingLot
			if err := AgencyLockForUpdate(tx).First(&lot, row.LotID).Error; err != nil {
				return err
			}
			if lot.UserID != journal.UserID || lot.PaidConsumed < delta[0] || lot.BonusConsumed < delta[1] ||
				lot.PaidAvailable > int64(common.MaxQuota)-delta[0] {
				return ErrAgencyComponentRefundProvenance
			}
			if delta[0] > 0 {
				if err := tx.Model(&lot).Updates(map[string]any{"paid_consumed": lot.PaidConsumed - delta[0],
					"paid_available": lot.PaidAvailable + delta[0], "version": lot.Version + 1}).Error; err != nil {
					return err
				}
				lot.PaidConsumed -= delta[0]
				lot.PaidAvailable += delta[0]
				lot.Version++
			}
			restoredNonpaid := delta[1]
			if delta[1] > 0 {
				expired, err := restoreAgencyBonusTx(tx, &lot, delta[1], false, common.GetTimestamp())
				if err != nil {
					return err
				}
				if expired {
					debtRestoration.ExpiredDirectNonpaid += delta[1]
					restoredNonpaid = 0
				}
			}
			if delta[0]+restoredNonpaid > 0 {
				debtRestoration.Sources = append(debtRestoration.Sources, agencyFundingSource{LotID: row.LotID, Paid: delta[0], Nonpaid: restoredNonpaid})
			}
		}
		if err := tx.Model(&allocation).Updates(map[string]any{"consumed": allocation.Consumed - delta[0], "nonpaid_consumed": allocation.NonpaidConsumed - delta[1],
			"debt_consumed": allocation.DebtConsumed - delta[2], "released": allocation.Released + delta[0] + delta[1] + delta[2], "version": allocation.Version + 1}).Error; err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]any{"restored_paid_quota": next[0], "restored_nonpaid_quota": next[1],
			"restored_debt_quota": next[2], "version": row.Version + 1}).Error; err != nil {
			return err
		}
	}
	if remaining != [3]int64{} {
		return ErrAgencyComponentRefundProvenance
	}
	return nil
}
