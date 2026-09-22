package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var ErrAgencyChargeConflict = errors.New("agency charge operation conflicts with committed state")

// acceptAgencyJournalTx freezes the quote exactly once. Follow-up reservations
// use that accepted quote even after the agency is disabled, repriced or moved.
// The caller holds the user's financial lock for the entire operation.
func acceptAgencyJournalTx(tx *gorm.DB, userID, tokenID int, chargeID string, snapshot *agencycontract.PricingSnapshot) (*AgencyBillingJournal, error) {
	var journal AgencyBillingJournal
	err := tx.Where("charge_id = ? AND segment_no = 0", chargeID).First(&journal).Error
	if err == nil {
		if journal.UserID != int64(userID) || (journal.TokenID != nil && *journal.TokenID != int64(tokenID)) {
			return nil, ErrAgencyChargeConflict
		}
		if journal.Status != "reserved" && journal.Status != "submitted" {
			return nil, ErrAgencyChargeConflict
		}
		if err := common.UnmarshalJsonStr(journal.PricingSnapshot, snapshot); err != nil {
			return nil, err
		}
		return &journal, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if snapshot.BindingRevision > 0 && snapshot.AgencyStateRevision > 0 && snapshot.PolicyVersionID > 0 {
		if err := ValidateAgencyPricingSnapshotTx(tx, int64(userID), snapshot); err != nil {
			return nil, err
		}
	}
	now := time.Now().UnixMilli()
	snapshot.FinancialChargeID = chargeID
	snapshot.UserID = int64(userID)
	snapshot.TokenID = int64(tokenID)
	snapshot.AcceptedAtMS = now
	encoded, err := common.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	token := int64(tokenID)
	journal = AgencyBillingJournal{ChargeID: chargeID, UserID: int64(userID), TokenID: &token,
		Status: "reserved", BusinessStatus: "pending", DeliveryStatus: "pending", PricingSnapshot: string(encoded),
		BillingBasis: "{}", CurrencyCode: snapshot.CurrencyCode, Version: 1, CreatedAtMS: now, UpdatedAtMS: now}
	if err := tx.Create(&journal).Error; err != nil {
		return nil, err
	}
	return &journal, nil
}

// agencyOperationInputHash excludes generated envelope fields and funded
// results. Replays compare the caller's immutable business input, not wall time.
func agencyOperationInputHash(event agencycontract.BillingEvent) (string, error) {
	event.EventID, event.OperationID = "", ""
	event.OccurredAtMS, event.MoneySeq, event.JournalRevision = 0, 0, 0
	event.EventIndex, event.EventCount = 0, 0
	event.PaidAllocatedQuota, event.NonpaidAllocatedQuota, event.DebtAllocatedQuota = 0, 0, 0
	event.CommissionQuota, event.CommissionAmountMicros = 0, 0
	event.Components = append([]agencycontract.BillingComponent(nil), event.Components...)
	for i := range event.Components {
		part := &event.Components[i]
		part.PaidAllocatedQuota, part.NonpaidAllocatedQuota, part.DebtAllocatedQuota = 0, 0, 0
		part.CommissionQuota, part.CommissionAmountMicros, part.ReversedCommissionAmountMicros = 0, 0, 0
	}
	sort.Slice(event.Components, func(i, j int) bool { return event.Components[i].ComponentID < event.Components[j].ComponentID })
	return agencycontract.CanonicalHash(event)
}

// writeAgencyJournalEventTx commits the journal revision, immutable operation
// receipt, funding ledger and outbox/delivery in the caller's money transaction.
func writeAgencyJournalEventTx(tx *gorm.DB, journal *AgencyBillingJournal, event *agencycontract.BillingEvent, operation, inputHash string, before AgencyFundingAccount) error {
	var after AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", journal.UserID).First(&after).Error; err != nil {
		return err
	}
	if after.MoneySeq <= before.MoneySeq {
		if after.MoneySeq == math.MaxInt64 {
			return errors.New("agency money sequence overflow")
		}
		after.MoneySeq++
		result := tx.Model(&AgencyFundingAccount{}).Where("user_id = ? AND version = ?", after.UserID, after.Version).
			Updates(map[string]any{"money_seq": after.MoneySeq, "version": after.Version + 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAgencyChargeConflict
		}
	}
	event.SchemaVersion = agencycontract.SchemaVersion
	if len(event.Components) > 0 {
		event.SchemaVersion = agencycontract.ComponentSchemaVersion
	}
	if event.EventType == "" {
		event.EventType = "agency.billing_finalized"
	}
	event.FinancialChargeID = journal.ChargeID
	event.SegmentNo = journal.SegmentNo
	event.JournalRevision = journal.Revision + 1
	event.MoneySeq = after.MoneySeq
	event.EventIndex, event.EventCount = 0, 1
	event.OccurredAtMS = time.Now().UnixMilli()
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s", journal.ChargeID, journal.SegmentNo, event.JournalRevision, operation)))
	event.OperationID = "agency-op-" + hex.EncodeToString(digest[:])
	if event.EventID == "" {
		event.EventID = "agency-event-" + hex.EncodeToString(digest[:])
	}
	if err := agencycontract.ValidateBillingComponents(*event); err != nil {
		return err
	}
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	hash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	result := tx.Model(&AgencyBillingJournal{}).Where("id = ? AND version = ?", journal.ID, journal.Version).
		Updates(map[string]any{
			"status": journal.Status, "business_status": event.BusinessStatus, "billing_basis": journal.BillingBasis,
			"reserve_quota": journal.ReserveQuota, "charged_total_quota": journal.ChargedTotalQuota,
			"commissionable_quota": journal.CommissionableQuota, "settlement_cost_quota": journal.SettlementCostQuota,
			"theoretical_commission_quota": journal.TheoreticalCommissionQuota, "paid_allocated_quota": journal.PaidAllocatedQuota,
			"commission_quota": journal.CommissionQuota, "commission_amount_micros": journal.CommissionAmountMicros,
			"reversed_quota": journal.ReversedQuota, "reversed_commission_quota": journal.ReversedCommissionQuota,
			"revision": event.JournalRevision, "version": journal.Version + 1, "updated_at_ms": event.OccurredAtMS,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAgencyChargeConflict
	}
	if err := tx.Create(&AgencyBillingOperation{ChargeID: journal.ChargeID, SegmentNo: journal.SegmentNo,
		Revision: event.JournalRevision, Operation: operation, InputHash: inputHash, CommittedResult: string(payload),
		OperationID: event.OperationID, MoneySeq: event.MoneySeq, EventCount: 1, CreatedAtMS: event.OccurredAtMS}).Error; err != nil {
		return err
	}
	if err := tx.Create(&AgencyFundingLedger{OperationID: event.OperationID, UserID: journal.UserID, MoneySeq: event.MoneySeq,
		SourceKind: "billing_" + operation, PaidDelta: after.PaidAvailable - before.PaidAvailable,
		NonpaidDelta: after.NonpaidAvailable - before.NonpaidAvailable, DebtDelta: after.DebtQuota - before.DebtQuota,
		PaidAfter: after.PaidAvailable, NonpaidAfter: after.NonpaidAvailable, DebtAfter: after.DebtQuota,
		AgencyID: event.AgencyID, BindingID: event.BindingID, CurrencyCode: event.CurrencyCode, CreatedAtMS: event.OccurredAtMS}).Error; err != nil {
		return err
	}
	if err := tx.Create(&AgencyBillingOutbox{EventID: event.EventID, OperationID: event.OperationID,
		EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: journal.UserID, MoneySeq: event.MoneySeq,
		Payload: string(payload), PayloadHash: hash, SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS}).Error; err != nil {
		return err
	}
	return tx.Create(&AgencyEventDelivery{EventID: event.EventID, Status: "pending", NextRetryAt: event.OccurredAtMS / 1000, CreatedAt: event.OccurredAtMS / 1000}).Error
}

func recordAgencyReservationTx(tx *gorm.DB, journal *AgencyBillingJournal, snapshot *agencycontract.PricingSnapshot, target int64, before AgencyFundingAccount) error {
	if journal.Revision > 0 && journal.ReserveQuota == target {
		return nil
	}
	journal.ReserveQuota = target
	event := agencycontract.BillingEvent{EventType: "agency.billing_reserved", UserID: journal.UserID,
		TokenID: journal.TokenID, AgencyID: &snapshot.AgencyID, BindingID: &snapshot.BindingID,
		OriginModelName: snapshot.OriginModelName, BusinessStatus: "pending", BillingStatus: journal.Status,
		CurrencyCode: snapshot.CurrencyCode, QuotaPerUnit: snapshot.QuotaPerUnit, ExchangeRate: snapshot.ExchangeRate,
		SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS, CommissionSkipReason: "reservation_pending"}
	hash, err := agencycontract.CanonicalHash(struct {
		ChargeID string `json:"charge_id"`
		Target   int64  `json:"target"`
	}{journal.ChargeID, target})
	if err != nil {
		return err
	}
	return writeAgencyJournalEventTx(tx, journal, &event, "reserve", hash, before)
}

// AgencyCommitWalletCharge settles all wallet, token, source allocation and
// financial facts atomically. A successful HTTP delivery is intentionally not
// inferred here: callers provide the actual business outcome in event.
func AgencyCommitWalletCharge(input agencycontract.BillingEvent, tokenKey string) (agencycontract.BillingEvent, error) {
	var result agencycontract.BillingEvent
	err := runAgencyFundingTransaction(func(tx *gorm.DB) error {
		var err error
		result, err = AgencyCommitWalletChargeTx(tx, input, tokenKey)
		return err
	})
	if err == nil && common.RedisEnabled {
		if cacheErr := InvalidateUserCache(int(input.UserID)); cacheErr != nil {
			common.SysError("agency settled wallet cache invalidation: " + cacheErr.Error())
		}
		if input.TokenID != nil && *input.TokenID > 0 && tokenKey != "" {
			if cacheErr := InvalidateTokenCache(tokenKey); cacheErr != nil {
				common.SysError("agency settled token cache invalidation: " + cacheErr.Error())
			}
		}
	}
	return result, err
}

// AgencyCommitWalletChargeTx uses a savepoint in the caller's transaction so
// task state and reconciliation receipts can commit with the financial facts.
// The caller owns the outer transaction and cache invalidation after commit.
func AgencyCommitWalletChargeTx(tx *gorm.DB, input agencycontract.BillingEvent, tokenKey string) (agencycontract.BillingEvent, error) {
	if tx == nil {
		return agencycontract.BillingEvent{}, ErrAgencyFundingUnavailable
	}
	if input.UserID <= 0 || strings.TrimSpace(input.FinancialChargeID) == "" || len(input.FinancialChargeID) > 128 || input.ChargedTotalQuota < 0 || input.ChargedTotalQuota > int64(common.MaxQuota) ||
		input.InputTokens < 0 || input.OutputTokens < 0 || input.CacheReadTokens < 0 || input.CacheWriteTokens < 0 {
		return agencycontract.BillingEvent{}, ErrAgencyFundingUnavailable
	}
	// Realtime uses its separate segment-aware transaction. This wallet API
	// owns the whole charge reservation and cannot safely share its allocations.
	if input.SegmentNo != 0 {
		return agencycontract.BillingEvent{}, ErrAgencyChargeConflict
	}
	inputHash, err := agencyOperationInputHash(input)
	if err != nil {
		return agencycontract.BillingEvent{}, err
	}
	result := input
	err = tx.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, input.UserID).Error; err != nil {
			return err
		}
		if user.BillingMode != AgencyDurableBillingMode {
			return ErrAgencyFundingUnavailable
		}
		var before AgencyFundingAccount
		if err := AgencyLockForUpdate(tx).Where("user_id = ?", user.Id).First(&before).Error; err != nil {
			return err
		}
		var journal AgencyBillingJournal
		if err := AgencyLockForUpdate(tx).Where("charge_id = ? AND segment_no = ?", input.FinancialChargeID, input.SegmentNo).First(&journal).Error; err != nil {
			return err
		}
		if journal.UserID != input.UserID {
			return ErrAgencyChargeConflict
		}
		if journal.Status == "finalized" || journal.Status == "cancelled" || journal.Status == "partially_reversed" || journal.Status == "reversed" {
			var committed AgencyBillingOperation
			if err := tx.Where("charge_id = ? AND segment_no = ? AND operation = ?", journal.ChargeID, journal.SegmentNo, "finalize").First(&committed).Error; err != nil {
				return err
			}
			if committed.InputHash != inputHash {
				return ErrAgencyChargeConflict
			}
			return common.UnmarshalJsonStr(committed.CommittedResult, &result)
		}
		if journal.Status != "reserved" && journal.Status != "submitted" && journal.Status != "reconcile_required" {
			return ErrAgencyChargeConflict
		}
		var snapshot agencycontract.PricingSnapshot
		if err := common.UnmarshalJsonStr(journal.PricingSnapshot, &snapshot); err != nil {
			return err
		}
		allocated, err := agencyFundingAllocatedTx(tx, int64(user.Id), journal.ChargeID)
		if err != nil {
			return err
		}
		delta := input.ChargedTotalQuota - allocated
		if delta != 0 {
			if err := adjustAgencyChargeTxWithRule(tx, user.Id, int(delta), journal.ChargeID, input.ChargedTotalQuota, agencyFundingRuleVersion(&snapshot)); err != nil {
				return err
			}
			if journal.TokenID != nil && *journal.TokenID > 0 {
				if err := agencyAdjustAcceptedTokenTx(tx, *journal.TokenID, tokenKey, delta); err != nil {
					return err
				}
			}
		}
		result = input
		result.AgencyID, result.BindingID = &snapshot.AgencyID, &snapshot.BindingID
		result.TokenID = journal.TokenID
		result.OriginModelName = snapshot.OriginModelName
		result.SettlementBPS, result.SalesBPS = snapshot.SettlementBPS, snapshot.SalesBPS
		result.CurrencyCode, result.QuotaPerUnit, result.ExchangeRate = snapshot.CurrencyCode, snapshot.QuotaPerUnit, snapshot.ExchangeRate
		result.CommissionEligible = snapshot.CommissionEligible && input.BusinessStatus == "success"
		result.PaidAllocatedQuota, result.NonpaidAllocatedQuota, result.DebtAllocatedQuota = 0, 0, 0
		result.CommissionQuota = 0
		result.CommissionAmountMicros = 0
		componentBilling := len(input.Components) > 0 || common.GetEnvOrDefaultBool("AGENCY_COMPONENT_BILLING_ENABLED", false)
		var taskBasis struct {
			Basis AgencyTaskChargeBasis `json:"frozen_task_basis"`
		}
		if input.BillingBasis != "" {
			if err := common.UnmarshalJsonStr(input.BillingBasis, &taskBasis); err == nil && taskBasis.Basis.Version == AgencyTaskChargeBasisVersion {
				componentBilling = taskBasis.Basis.ComponentBilling
				if len(input.Components) > 0 && !componentBilling {
					return ErrAgencyChargeConflict
				}
			}
		}
		if input.BusinessStatus != "cancelled" && componentBilling {
			if err := settleAgencyComponentsTx(tx, &journal, &result, snapshot); err != nil {
				return err
			}
		} else {
			result.Components = nil
			// Retain the v1 producer during the staged component-schema rollout.
			// Explicit component requests always use the v2 transaction above.
			var allocations []AgencyFundingAllocation
			if err := tx.Where("user_id = ? AND charge_id = ?", user.Id, journal.ChargeID).Find(&allocations).Error; err != nil {
				return err
			}
			for _, allocation := range allocations {
				paid, nonpaid, debt, err := agencyFundingAllocationActiveParts(allocation)
				if err != nil {
					return err
				}
				if paid > int64(common.MaxQuota)-result.PaidAllocatedQuota {
					return errors.New("agency paid allocation overflow")
				}
				result.PaidAllocatedQuota += paid
				if nonpaid > int64(common.MaxQuota)-result.NonpaidAllocatedQuota {
					return errors.New("agency nonpaid allocation overflow")
				}
				if debt > int64(common.MaxQuota)-result.DebtAllocatedQuota {
					return errors.New("agency debt allocation overflow")
				}
				result.NonpaidAllocatedQuota += nonpaid
				result.DebtAllocatedQuota += debt
			}
			if result.CommissionEligible {
				if len(snapshot.Hierarchy) > 1 {
					splits, totalTheoretical, totalCommission, totalMicros, splitErr := agencyTieredCommissionSplits(result, snapshot)
					if splitErr != nil {
						return splitErr
					}
					result.CommissionSplits = splits
					result.SettlementCostQuota = result.ChargedTotalQuota - totalTheoretical
					result.TheoreticalCommissionQuota = totalTheoretical
					result.CommissionQuota = totalCommission
					result.CommissionAmountMicros = totalMicros
				} else {
					result.CommissionQuota, err = agencycontract.CommissionForPaid(result.TheoreticalCommissionQuota, result.PaidAllocatedQuota, result.CommissionableQuota, true)
					if err != nil {
						return err
					}
					result.CommissionAmountMicros, err = agencyFrozenCommissionMicros(result.CommissionQuota, snapshot)
					if err != nil {
						return err
					}
				}
			}
		}
		journal.Status = "finalized"
		if input.BusinessStatus == "cancelled" {
			journal.Status = "cancelled"
		}
		result.BillingStatus, result.FinancialFinal = journal.Status, true
		journal.ChargedTotalQuota, journal.CommissionableQuota = result.ChargedTotalQuota, result.CommissionableQuota
		journal.SettlementCostQuota, journal.TheoreticalCommissionQuota = result.SettlementCostQuota, result.TheoreticalCommissionQuota
		journal.PaidAllocatedQuota, journal.CommissionQuota = result.PaidAllocatedQuota, result.CommissionQuota
		journal.CommissionAmountMicros, journal.BillingBasis = result.CommissionAmountMicros, result.BillingBasis
		return writeAgencyJournalEventTx(tx, &journal, &result, "finalize", inputHash, before)
	})
	return result, err
}

func agencyTieredCommissionSplits(event agencycontract.BillingEvent, snapshot agencycontract.PricingSnapshot) ([]agencycontract.CommissionSplit, int64, int64, int64, error) {
	if len(snapshot.Hierarchy) <= 1 || event.StandardQuota < 0 || event.ChargedTotalQuota < 0 || event.PaidAllocatedQuota < 0 || event.PaidAllocatedQuota > event.CommissionableQuota {
		return nil, 0, 0, 0, errors.New("invalid tiered commission settlement input")
	}
	nodes := make([]agencycontract.TierNode, len(snapshot.Hierarchy))
	for i, node := range snapshot.Hierarchy {
		nodes[i] = agencycontract.TierNode{AgencyID: node.AgencyID, CostBPS: node.CostBPS, MinSpreadBPS: node.MinSpreadBPS, SalesBPS: node.SalesBPS}
	}
	result, err := agencycontract.CalculateTieredCommission(event.StandardQuota, nodes, snapshot.PlatformCostBPS, snapshot.MinSpreadBPS, event.PaidAllocatedQuota, true)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if result.CustomerChargedQuota != event.ChargedTotalQuota {
		return nil, 0, 0, 0, errors.New("tiered commission charge mismatch")
	}
	splits := make([]agencycontract.CommissionSplit, 0, len(result.Segments))
	var theoretical, commission, micros int64
	for i, segment := range result.Segments {
		amount, amountErr := agencyFrozenCommissionMicros(segment.CommissionQuota, snapshot)
		if amountErr != nil {
			return nil, 0, 0, 0, amountErr
		}
		node := snapshot.Hierarchy[i]
		splits = append(splits, agencycontract.CommissionSplit{AgencyID: segment.AgencyID, ParentAgencyID: node.ParentAgencyID, Depth: node.Depth, CostBPS: node.CostBPS, SalesBPS: node.SalesBPS, TheoreticalQuota: segment.TheoreticalQuota, PaidAllocatedQuota: segment.PaidAllocatedQuota, CommissionQuota: segment.CommissionQuota, CommissionAmountMicros: amount})
		if theoretical > math.MaxInt64-segment.TheoreticalQuota || commission > math.MaxInt64-segment.CommissionQuota || micros > math.MaxInt64-amount {
			return nil, 0, 0, 0, errors.New("tiered commission overflow")
		}
		theoretical += segment.TheoreticalQuota
		commission += segment.CommissionQuota
		micros += amount
	}
	return splits, theoretical, commission, micros, nil
}

func agencyAdjustAcceptedTokenTx(tx *gorm.DB, tokenID int64, tokenKey string, delta int64) error {
	var token Token
	err := AgencyLockForUpdate(tx).Where("id = ?", tokenID).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Deleting a token must not cancel already accepted wallet usage or
		// recreate the deleted token. The journal retains its immutable token ID.
		return nil
	}
	if err != nil {
		return err
	}
	if tokenKey != "" && token.Key != tokenKey {
		return ErrAgencyChargeConflict
	}
	remain, used := int64(token.RemainQuota)-delta, int64(token.UsedQuota)+delta
	if remain < -int64(common.MaxQuota)-1 || remain > int64(common.MaxQuota) || used < 0 || used > int64(common.MaxQuota) {
		return errors.New("agency token quota arithmetic overflow")
	}
	result := tx.Model(&Token{}).Where("id = ?", tokenID).
		Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": common.GetTimestamp()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAgencyChargeConflict
	}
	return nil
}

func agencyFrozenCommissionMicros(quota int64, snapshot agencycontract.PricingSnapshot) (int64, error) {
	if snapshot.CurrencyCode == "TOKENS" {
		return quota, nil
	}
	unit, err := decimal.NewFromString(snapshot.QuotaPerUnit)
	if err != nil || !unit.IsPositive() {
		return 0, errors.New("invalid frozen quota unit")
	}
	rate, err := decimal.NewFromString(snapshot.ExchangeRate)
	if err != nil || !rate.IsPositive() {
		return 0, errors.New("invalid frozen exchange rate")
	}
	amount := decimal.NewFromInt(quota).Div(unit).Mul(rate).Mul(decimal.NewFromInt(1000000)).Round(0)
	if amount.IsNegative() || amount.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, errors.New("agency commission amount overflow")
	}
	return amount.IntPart(), nil
}
