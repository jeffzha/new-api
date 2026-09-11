package agencyhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var errBalanceOverflow = errors.New("agency financial balance overflow")

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, errBalanceOverflow
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, errBalanceOverflow
	}
	return left + right, nil
}

// RecordPaidFunding records a verified payment source and its exact credited
// quota. It is intended to be called from the gateway's payment transaction;
// it never infers paid quota from a provider's display amount.
func (a *App) RecordPaidFunding(tx *gorm.DB, userID int64, sourceKind, sourceID, completionSource string, paidQuota, bonusQuota int64, agencyID, bindingID *int64) error {
	if tx == nil || userID <= 0 || paidQuota < 0 || bonusQuota < 0 || strings.TrimSpace(sourceID) == "" {
		return errors.New("invalid funding source")
	}
	now := time.Now().UnixMilli()
	db := tx
	var account model.AgencyFundingAccount
	if err := model.AgencyLockForUpdate(db).Where("user_id = ?", userID).First(&account).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		account = model.AgencyFundingAccount{UserID: userID, Version: 1}
		if err := db.Create(&account).Error; err != nil {
			return err
		}
	}
	paid, err := checkedAdd(account.PaidAvailable, paidQuota)
	if err != nil {
		return err
	}
	nonpaid, err := checkedAdd(account.NonpaidAvailable, bonusQuota)
	if err != nil {
		return err
	}
	seq := account.MoneySeq + 1
	if seq <= account.MoneySeq {
		return errBalanceOverflow
	}
	lot := &model.AgencyFundingLot{UserID: userID, SourceKind: sourceKind, SourceID: sourceID, CompletionSource: completionSource, PaidInitial: paidQuota, BonusInitial: bonusQuota, PaidAvailable: paidQuota, BonusAvailable: bonusQuota, MoneySeq: seq, Version: 1, CreatedAt: now / 1000}
	if err := db.Create(lot).Error; err != nil {
		return err
	}
	if err := db.Model(&account).Updates(map[string]any{"paid_available": paid, "nonpaid_available": nonpaid, "money_seq": seq, "version": account.Version + 1, "updated_at": now / 1000}).Error; err != nil {
		return err
	}
	ledger := &model.AgencyFundingLedger{OperationID: sourceID, EntryNo: 0, UserID: userID, MoneySeq: seq, SourceKind: sourceKind, LotID: &lot.ID, PaidDelta: paidQuota, NonpaidDelta: bonusQuota, PaidAfter: paid, NonpaidAfter: nonpaid, DebtAfter: account.DebtQuota, AgencyID: agencyID, BindingID: bindingID, CreatedAtMS: now}
	return db.Create(ledger).Error
}

// ReservePaid moves paid quota from available to a durable charge allocation.
// The gateway can later consume/release this allocation during finalization.
func (a *App) ReservePaid(tx *gorm.DB, userID int64, chargeID string, amount int64) ([]model.AgencyFundingAllocation, error) {
	if tx == nil || userID <= 0 || strings.TrimSpace(chargeID) == "" || amount < 0 {
		return nil, errors.New("invalid funding reservation")
	}
	var account model.AgencyFundingAccount
	if err := model.AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
		return nil, err
	}
	if account.ReconcileBlocked {
		return nil, errors.New("funding account is blocked for reconciliation")
	}
	if account.PaidAvailable < amount {
		return nil, errors.New("insufficient paid quota")
	}
	remaining := amount
	var lots []model.AgencyFundingLot
	if err := model.AgencyLockForUpdate(tx).Where("user_id = ? AND paid_available > 0", userID).Order("money_seq ASC, id ASC").Find(&lots).Error; err != nil {
		return nil, err
	}
	allocations := make([]model.AgencyFundingAllocation, 0)
	for _, lot := range lots {
		if remaining == 0 {
			break
		}
		take := lot.PaidAvailable
		if take > remaining {
			take = remaining
		}
		lot.PaidAvailable -= take
		lot.PaidReserved += take
		lot.Version++
		if err := tx.Model(&lot).Updates(map[string]any{"paid_available": lot.PaidAvailable, "paid_reserved": lot.PaidReserved, "version": lot.Version}).Error; err != nil {
			return nil, err
		}
		allocation := model.AgencyFundingAllocation{ChargeID: chargeID, SegmentNo: 0, ComponentID: "default", UserID: userID, LotID: lot.ID, Reserved: take, Version: 1}
		if err := tx.Create(&allocation).Error; err != nil {
			return nil, err
		}
		allocations = append(allocations, allocation)
		remaining -= take
	}
	if remaining != 0 {
		return nil, errors.New("funding lots are inconsistent")
	}
	seq := account.MoneySeq + 1
	if seq <= account.MoneySeq {
		return nil, errBalanceOverflow
	}
	if err := tx.Model(&account).Updates(map[string]any{"paid_available": account.PaidAvailable - amount, "money_seq": seq, "version": account.Version + 1, "updated_at": time.Now().Unix()}).Error; err != nil {
		return nil, err
	}
	return allocations, nil
}

// ProcessBillingEvent is the sidecar consumer entry point. The gateway event
// contains final B/T/P/K/M snapshots, so this method never recomputes model
// prices and remains safe when the gateway is upgraded independently.
func (a *App) ProcessBillingEvent(event agencycontract.BillingEvent) error {
	if event.EventID == "" || event.UserID <= 0 || event.SchemaVersion != agencycontract.SchemaVersion {
		return errors.New("invalid billing event")
	}
	if event.MoneySeq < 0 || event.StandardQuota < 0 || event.ChargedTotalQuota < 0 ||
		event.CommissionableQuota < 0 || event.NoncommissionableQuota < 0 ||
		event.CommissionableQuota > event.ChargedTotalQuota ||
		event.NoncommissionableQuota > event.ChargedTotalQuota {
		return errors.New("invalid billing event amounts")
	}
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	now := event.OccurredAtMS
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return a.db.Transaction(func(tx *gorm.DB) error {
		return a.processBillingEventTx(tx, event, string(payload), payloadHash, now)
	})
}

func (a *App) processBillingEventWithLease(ctx context.Context, event agencycontract.BillingEvent, delivery model.AgencyEventDelivery) error {
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	now := event.OccurredAtMS
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.AgencyEventDelivery
		if err := model.AgencyLockForUpdate(tx).Where(
			"id = ? AND status = ? AND lease_owner = ? AND lease_token = ? AND lease_until >= ?",
			delivery.ID, "claimed", delivery.LeaseOwner, delivery.LeaseToken, time.Now().Unix(),
		).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("agency event delivery lease lost")
			}
			return err
		}
		if err := a.processBillingEventTx(tx, event, string(payload), payloadHash, now); err != nil {
			return err
		}
		return a.markDeliveryTx(tx, current, "done", nil)
	})
}

func (a *App) processBillingEventTx(tx *gorm.DB, event agencycontract.BillingEvent, payload string, payloadHash string, now int64) error {
	if event.OccurredAtMS == 0 {
		event.OccurredAtMS = now
	}
	var exists model.AgencySourceEvent
	var source *model.AgencySourceEvent
	if err := tx.Where("event_id = ?", event.EventID).First(&exists).Error; err == nil {
		if exists.PayloadHash != payloadHash {
			return errors.New("billing event payload hash conflict")
		}
		if exists.ProcessingStatus == "done" || exists.ProcessingStatus == "skipped" {
			return nil
		}
		source = &exists
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	} else {
		// Reversal events intentionally point back to the original
		// operation and may carry the same journal revision. They are
		// separate immutable source events keyed by event_id, so they
		// must not be collapsed into the original operation event.
		if event.EventType != "agency.billing_reversed" && strings.TrimSpace(event.OperationID) != "" {
			var duplicateSource model.AgencySourceEvent
			duplicateErr := tx.Where("source_operation_id = ? AND journal_revision = ? AND event_id <> ?", event.OperationID, event.JournalRevision, event.EventID).First(&duplicateSource).Error
			if duplicateErr == nil {
				if duplicateSource.PayloadHash != payloadHash {
					return errors.New("billing event payload hash conflict")
				}
				if duplicateSource.ProcessingStatus == "done" || duplicateSource.ProcessingStatus == "skipped" {
					return nil
				}
				source = &duplicateSource
			} else if !errors.Is(duplicateErr, gorm.ErrRecordNotFound) {
				return duplicateErr
			}
		}
		if source == nil {
			source = &model.AgencySourceEvent{EventID: event.EventID, SourceOperationID: event.OperationID, JournalRevision: event.JournalRevision, SchemaVersion: event.SchemaVersion, PayloadHash: payloadHash, UserID: event.UserID, MoneySeq: event.MoneySeq, ProcessingStatus: "processing", Payload: payload, CreatedAtMS: now}
			if err := tx.Create(source).Error; err != nil {
				return err
			}
		}
	}
	// Funding events for one user form a strict money_seq chain. A late
	// delivery must wait for every earlier outbox event for that user, while
	// events for other users continue independently. If an earlier event is
	// already poison, the dependency remains visible as an open reconciliation
	// issue and is deliberately retried only after root resolves it.
	if event.MoneySeq > 1 {
		var priorOutboxCount int64
		if err := tx.Model(&model.AgencyBillingOutbox{}).
			Where("user_id = ? AND money_seq > 0 AND money_seq < ?", event.UserID, event.MoneySeq).
			Count(&priorOutboxCount).Error; err != nil {
			return err
		}
		if priorOutboxCount > 0 {
			var priorCompletedCount int64
			if err := tx.Model(&model.AgencySourceEvent{}).
				Where("user_id = ? AND money_seq > 0 AND money_seq < ? AND processing_status IN ?", event.UserID, event.MoneySeq, []string{"done", "skipped"}).
				Count(&priorCompletedCount).Error; err != nil {
				return err
			}
			if priorCompletedCount < priorOutboxCount {
				return errors.New("agency event waiting for prior user money sequence")
			}
		}
	}
	isReversal := event.EventType == "agency.billing_reversed"
	isFundingReversal := event.EventType == "agency.funding_reversed"
	if !isReversal && !isFundingReversal {
		if err := a.recordUsageFact(tx, event); err != nil {
			return err
		}
	}
	if event.AgencyID == nil || isFundingReversal || !event.CommissionEligible || (!isReversal && event.CommissionAmountMicros <= 0) || (isReversal && event.ReversedCommissionAmountMicros <= 0) {
		if event.AgencyID != nil && !isReversal && !isFundingReversal {
			if err := a.recordDailyStat(tx, event, 0, false); err != nil {
				return err
			}
		}
		source.ProcessingStatus = "skipped"
		source.SkipReason = event.CommissionSkipReason
		if source.SkipReason == "" {
			switch {
			case event.AgencyID == nil:
				source.SkipReason = "no_agency_binding"
			case isFundingReversal:
				source.SkipReason = "funding_reversal_noncommissionable"
			case !event.CommissionEligible:
				source.SkipReason = "commission_ineligible"
			default:
				source.SkipReason = "zero_commission"
			}
		}
		return tx.Model(source).Updates(map[string]any{"processing_status": source.ProcessingStatus, "skip_reason": source.SkipReason}).Error
	}
	amount := event.CommissionAmountMicros
	entryType := "earned"
	originalID := (*int64)(nil)
	if isReversal {
		entryType = "reversal"
		amount = -event.ReversedCommissionAmountMicros
		var original model.AgencyCommissionLedger
		if event.OriginalEventID == "" {
			return errors.New("reversal event is missing original_event_id")
		}
		if err := tx.Where("event_id = ? AND entry_type = ?", event.OriginalEventID, "earned").First(&original).Error; err != nil {
			return err
		}
		originalID = &original.ID
		var reversed int64
		if err := tx.Model(&model.AgencyCommissionLedger{}).Where("original_entry_id = ? AND entry_type = ?", original.ID, "reversal").Select("COALESCE(SUM(-amount_micros),0)").Scan(&reversed).Error; err != nil {
			return err
		}
		if event.ReversedCommissionAmountMicros < 0 || reversed > original.AmountMicros-event.ReversedCommissionAmountMicros {
			return errors.New("commission reversal exceeds original entry")
		}
	}
	if amount == 0 {
		source.ProcessingStatus = "skipped"
		return tx.Model(source).Update("processing_status", "skipped").Error
	}
	entry := &model.AgencyCommissionLedger{EventID: event.EventID, ComponentID: "default", EntryType: entryType, OriginalEntryID: originalID, AgencyID: *event.AgencyID, BindingID: valueOrZero(event.BindingID), UserID: event.UserID, OriginModelName: event.OriginModelName, StandardQuota: event.StandardQuota, SettlementCostQuota: event.SettlementCostQuota, TheoreticalCommissionQuota: event.TheoreticalCommissionQuota, PaidAllocatedQuota: event.PaidAllocatedQuota, CommissionQuota: event.CommissionQuota, AmountMicros: amount, CurrencyCode: event.CurrencyCode, QuotaPerUnit: event.QuotaPerUnit, ExchangeRate: event.ExchangeRate, OccurredAtMS: now}
	if err := tx.Create(entry).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil
		}
		return err
	}
	var balance model.AgencyCommissionBalance
	if err := model.AgencyLockForUpdate(tx).Where("agency_id = ? AND currency_code = ?", *event.AgencyID, event.CurrencyCode).First(&balance).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		balance = model.AgencyCommissionBalance{AgencyID: *event.AgencyID, CurrencyCode: event.CurrencyCode, Version: 1}
		if err := tx.Create(&balance).Error; err != nil {
			return err
		}
	}
	available, err := checkedAdd(balance.AvailableMicros, amount)
	// A reversal may legitimately drive available below zero when the
	// original commission has already been withdrawn. Positive entries may
	// never create a negative balance.
	if err != nil || (available < 0 && amount > 0) {
		if err == nil {
			err = errors.New("commission reversal exceeds balance")
		}
		return err
	}
	earned := balance.EarnedMicros
	reversed := balance.ReversedMicros
	if amount > 0 {
		earned, err = checkedAdd(earned, amount)
	} else {
		reversed, err = checkedAdd(reversed, -amount)
	}
	if err != nil {
		return err
	}
	if err = tx.Model(&balance).Updates(map[string]any{"available_micros": available, "earned_micros": earned, "reversed_micros": reversed, "version": balance.Version + 1, "updated_at_ms": now}).Error; err != nil {
		return err
	}
	if isReversal {
		if err := holdUnpaidWithdrawals(tx, *event.AgencyID, event.CurrencyCode, event.EventID, now); err != nil {
			return err
		}
	}
	if err := a.recordDailyStat(tx, event, amount, isReversal); err != nil {
		return err
	}
	source.ProcessingStatus = "done"
	return tx.Model(source).Update("processing_status", "done").Error
}

func (a *App) recordUsageFact(tx *gorm.DB, event agencycontract.BillingEvent) error {
	if event.AgencyID == nil {
		return nil
	}
	modelKey, err := agencycontract.ModelKey(event.OriginModelName)
	if err != nil {
		modelKey = "unknown"
	}
	skipReason := event.CommissionSkipReason
	if skipReason == "" && event.NoncommissionableQuota > 0 {
		skipReason = "noncommissionable_charge"
	}
	if event.CommissionEligible && event.CommissionAmountMicros <= 0 {
		skipReason = "zero_commission"
	}
	fact := model.AgencyUsageFact{
		EventID:         event.EventID,
		ComponentID:     "default",
		UsageHash:       event.UsageHash,
		CumulativeUsage: event.CumulativeUsage,
		UserID:          event.UserID,
		AgencyID:        event.AgencyID,
		BindingID:       event.BindingID,
		OriginModelName: event.OriginModelName,
		ModelKey:        modelKey,
		Endpoint:        event.Endpoint,
		BusinessStatus:  event.BusinessStatus,
		StandardQuota:   event.StandardQuota,
		SalesBPS:        event.SalesBPS,
		ChargedQuota:    event.ChargedTotalQuota,
		CurrencyCode:    event.CurrencyCode,
		OccurredAtMS:    event.OccurredAtMS,
		SkipReason:      skipReason,
	}
	if fact.BusinessStatus == "" {
		fact.BusinessStatus = event.BillingStatus
	}
	if err := tx.Create(&fact).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil
		}
		return err
	}
	return nil
}

// holdUnpaidWithdrawals freezes payout attempts after a commission reversal or
// agency disable changes the payability evidence. Paid/rejected/cancelled
// withdrawals are immutable; all other outstanding requests require manual
// review before they can move again.
func holdUnpaidWithdrawals(tx *gorm.DB, agencyID int64, currencyCode, eventID string, now int64) error {
	var rows []model.AgencyWithdrawal
	if err := model.AgencyLockForUpdate(tx).
		Where("agency_id = ? AND currency_code = ? AND status IN ?", agencyID, currencyCode, []string{"submitted", "reviewing", "approved"}).
		Find(&rows).Error; err != nil {
		return err
	}
	for _, withdrawal := range rows {
		if err := tx.Model(&withdrawal).Updates(map[string]any{"status": "on_hold", "version": withdrawal.Version + 1, "previous_status": withdrawal.Status, "on_hold_reason": "commission reversal " + eventID, "updated_at_ms": now}).Error; err != nil {
			return err
		}
		transitionID := fmt.Sprintf("%s-withdrawal-%d", eventID, withdrawal.ID)
		if err := tx.Create(&model.AgencyWithdrawalTransition{WithdrawalID: withdrawal.ID, OperationID: transitionID, BeforeStatus: withdrawal.Status, AfterStatus: "on_hold", ActorType: ActorTypeSystem, ActorID: 0, Evidence: eventID, CreatedAtMS: now}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (a *App) recordDailyStat(tx *gorm.DB, event agencycontract.BillingEvent, amount int64, reversal bool) error {
	if event.AgencyID == nil {
		return nil
	}
	modelKey, err := agencycontract.ModelKey(event.OriginModelName)
	if err != nil {
		modelKey = "unknown"
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	statDate := time.UnixMilli(event.OccurredAtMS).In(location).Format("2006-01-02")
	source := "nonpaid"
	if event.PaidAllocatedQuota > 0 {
		source = "wallet"
	}
	query := model.AgencyLockForUpdate(tx).Where("agency_id = ? AND stat_date = ? AND binding_id IS NULL AND user_id IS NULL AND model_key = ? AND currency_code = ? AND billing_source = ?", *event.AgencyID, statDate, modelKey, event.CurrencyCode, source)
	var stat model.AgencyDailyStat
	if err := query.First(&stat).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		stat = model.AgencyDailyStat{StatDate: statDate, AgencyID: *event.AgencyID, ModelKey: modelKey, CurrencyCode: event.CurrencyCode, BillingSource: source, Revision: 1}
	}
	if reversal {
		stat.ReversalMicros += -amount
	} else {
		stat.Calls++
		stat.UsageQuota += event.ChargedTotalQuota
		stat.ChargedQuota += event.ChargedTotalQuota
		stat.CommissionMicros += amount
	}
	stat.Revision++
	if stat.ID == 0 {
		return tx.Create(&stat).Error
	}
	return tx.Model(&stat).Updates(map[string]any{"calls": stat.Calls, "usage_quota": stat.UsageQuota, "charged_quota": stat.ChargedQuota, "commission_micros": stat.CommissionMicros, "reversal_micros": stat.ReversalMicros, "revision": stat.Revision}).Error
}
func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (a *App) commissionSummary(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	var balances []model.AgencyCommissionBalance
	if err := a.db.Where("agency_id = ?", agency.ID).Order("currency_code").Find(&balances).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	respondOK(c, gin.H{"items": balances})
}
func (a *App) commissionLedger(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page或limit同时使用", nil)
		return
	}
	rawCursor := strings.TrimSpace(c.Query("cursor"))
	if rawCursor != "" || strings.TrimSpace(c.Query("page")) == "" {
		var cursor agencyCursor
		var err error
		if rawCursor != "" {
			cursor, err = a.decodeCursor(rawCursor, c, "commission_ledger", identity)
			if err != nil {
				respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
				return
			}
		}
		size, err := cursorPageSize(c)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
			return
		}
		query := a.db.Where("agency_id = ?", agency.ID)
		var total int64
		if err := query.Model(&model.AgencyCommissionLedger{}).Count(&total).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
			return
		}
		if rawCursor != "" {
			query = query.Where("(occurred_at_ms < ? OR (occurred_at_ms = ? AND id < ?))", cursor.PositionMS, cursor.PositionMS, cursor.PositionID)
		}
		var rows []model.AgencyCommissionLedger
		if err := query.Order("occurred_at_ms DESC, id DESC").Limit(size + 1).Find(&rows).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
			return
		}
		hasMore := len(rows) > size
		if hasMore {
			rows = rows[:size]
		}
		nextCursor := ""
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			nextCursor, err = a.encodeCursor(agencyCursor{
				Kind:       "commission_ledger",
				Scope:      cursorScope(c, "commission_ledger", identity),
				ActorType:  identity.ActorType,
				ActorID:    identity.ActorID,
				AgencyID:   agency.ID,
				PositionMS: last.OccurredAtMS,
				PositionID: last.ID,
			})
			if err != nil {
				respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
				return
			}
		}
		respondOK(c, gin.H{"items": rows, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
		return
	}
	page := 1
	if parsed, err := strconv.Atoi(c.Query("page")); err == nil && parsed > 0 {
		page = parsed
	}
	size := 50
	if parsed, err := strconv.Atoi(c.Query("page_size")); err == nil && parsed > 0 && parsed <= 200 {
		size = parsed
	}
	var rows []model.AgencyCommissionLedger
	query := a.db.Where("agency_id = ?", agency.ID)
	var total int64
	if err := query.Model(&model.AgencyCommissionLedger{}).Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	if err := query.Order("occurred_at_ms DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}

func (a *App) createWithdrawal(c *gin.Context) {
	if !a.config.WithdrawalsEnabled {
		respondError(c, http.StatusServiceUnavailable, "withdrawals_disabled", "提现功能暂未开放", nil)
		return
	}
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	if agency.Status != AgencyStatusActive {
		respondError(c, http.StatusForbidden, "agency_disabled", "代理商已停用，不能发起提现", nil)
		return
	}
	var request struct {
		CurrencyCode string       `json:"currency_code"`
		AmountMicros decimalInt64 `json:"amount_micros"`
		AccountID    decimalInt64 `json:"account_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.AmountMicros <= 0 || request.AccountID <= 0 || strings.TrimSpace(request.CurrencyCode) == "" {
		respondError(c, http.StatusUnprocessableEntity, "invalid_withdrawal", "提现金额或币种无效", nil)
		return
	}
	amountMicros := request.AmountMicros.Int64()
	accountID := request.AccountID.Int64()
	identity := currentIdentity(c)
	now := time.Now().UnixMilli()
	var withdrawal model.AgencyWithdrawal
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var account model.AgencyWithdrawalAccount
		if model.AgencyLockForUpdate(tx).Where("id = ? AND agency_id = ? AND status = ?", accountID, agency.ID, "active").First(&account).Error != nil {
			return errors.New("withdrawal account is invalid")
		}
		var balance model.AgencyCommissionBalance
		if err := model.AgencyLockForUpdate(tx).Where("agency_id = ? AND currency_code = ?", agency.ID, request.CurrencyCode).First(&balance).Error; err != nil {
			return err
		}
		if balance.AvailableMicros < amountMicros {
			return errors.New("insufficient commission balance")
		}
		locked, err := checkedAdd(balance.LockedMicros, amountMicros)
		if err != nil {
			return err
		}
		if err := tx.Model(&balance).Updates(map[string]any{"available_micros": balance.AvailableMicros - amountMicros, "locked_micros": locked, "version": balance.Version + 1, "updated_at_ms": now}).Error; err != nil {
			return err
		}
		no, err := randomToken(16)
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(account.Ciphertext))
		withdrawal = model.AgencyWithdrawal{RequestNo: no, AgencyID: agency.ID, CurrencyCode: request.CurrencyCode, AmountMicros: amountMicros, Status: "submitted", Version: 1, AccountID: account.ID, AccountVersion: account.Version, AccountSnapshotHash: hex.EncodeToString(hash[:]), AccountSnapshot: account.Ciphertext, AccountSnapshotKeyID: account.KeyID, CreatedAtMS: now, UpdatedAtMS: now}
		if err = tx.Create(&withdrawal).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyWithdrawalTransition{WithdrawalID: withdrawal.ID, OperationID: requestID(c), BeforeStatus: "", AfterStatus: "submitted", AmountDeltaMicros: -amountMicros, ActorType: identity.ActorType, ActorID: identity.ActorID, CreatedAtMS: now}).Error
	})
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "withdrawal_failed", err.Error(), nil)
		return
	}
	respondCreated(c, withdrawalView(withdrawal))
}

func withdrawalView(row model.AgencyWithdrawal) gin.H {
	return gin.H{
		"id":                row.ID,
		"request_no":        row.RequestNo,
		"currency_code":     row.CurrencyCode,
		"amount_micros":     strconv.FormatInt(row.AmountMicros, 10),
		"status":            row.Status,
		"version":           row.Version,
		"account_id":        row.AccountID,
		"account_version":   row.AccountVersion,
		"payment_channel":   row.PaymentChannel,
		"payment_reference": row.PaymentReference,
		"on_hold_reason":    row.OnHoldReason,
		"created_at_ms":     row.CreatedAtMS,
		"updated_at_ms":     row.UpdatedAtMS,
	}
}

func (a *App) listWithdrawals(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page或limit同时使用", nil)
		return
	}
	rawCursor := strings.TrimSpace(c.Query("cursor"))
	if rawCursor != "" || strings.TrimSpace(c.Query("page")) == "" {
		var cursor agencyCursor
		var err error
		if rawCursor != "" {
			cursor, err = a.decodeCursor(rawCursor, c, "withdrawals", identity)
			if err != nil {
				respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
				return
			}
		}
		size, err := cursorPageSize(c)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
			return
		}
		query := a.db.Where("agency_id = ?", agency.ID)
		var total int64
		if err := query.Model(&model.AgencyWithdrawal{}).Count(&total).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
			return
		}
		if rawCursor != "" {
			query = query.Where("id < ?", cursor.PositionID)
		}
		var rows []model.AgencyWithdrawal
		if err := query.Order("id DESC").Limit(size + 1).Find(&rows).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
			return
		}
		hasMore := len(rows) > size
		if hasMore {
			rows = rows[:size]
		}
		items := make([]gin.H, 0, len(rows))
		for _, row := range rows {
			items = append(items, withdrawalView(row))
		}
		nextCursor := ""
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			nextCursor, err = a.encodeCursor(agencyCursor{
				Kind:       "withdrawals",
				Scope:      cursorScope(c, "withdrawals", identity),
				ActorType:  identity.ActorType,
				ActorID:    identity.ActorID,
				AgencyID:   agency.ID,
				PositionID: last.ID,
			})
			if err != nil {
				respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
				return
			}
		}
		respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
		return
	}
	var rows []model.AgencyWithdrawal
	if err := a.db.Where("agency_id = ?", agency.ID).Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	// Never serialize the encrypted payout account snapshot or key identifier
	// to an agency operator. The snapshot is retained only so Root can prove
	// which account version was paid; it is not part of the operator-facing
	// history contract.
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, withdrawalView(row))
	}
	respondOK(c, gin.H{"items": items})
}

func (a *App) listRootWithdrawals(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page同时使用", nil)
		return
	}
	pageSize, err := cursorPageSize(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
		return
	}
	var cursor agencyCursor
	rawCursor := strings.TrimSpace(c.Query("cursor"))
	if rawCursor != "" {
		cursor, err = a.decodeCursor(rawCursor, c, "root_withdrawals", identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	query := a.db.Model(&model.AgencyWithdrawal{})
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		switch status {
		case "submitted", "reviewing", "approved", "paying", "payment_unknown", "on_hold", "paid", "rejected", "cancelled":
			query = query.Where("status = ?", status)
		default:
			respondError(c, http.StatusBadRequest, "invalid_status", "无效的提现状态", nil)
			return
		}
	}
	if currency := strings.TrimSpace(c.Query("currency_code")); currency != "" {
		if len(currency) > 16 {
			respondError(c, http.StatusBadRequest, "invalid_currency", "币种无效", nil)
			return
		}
		query = query.Where("currency_code = ?", currency)
	}
	if rawAgency := strings.TrimSpace(c.Query("agency_id")); rawAgency != "" {
		agencyID, parseErr := strconv.ParseInt(rawAgency, 10, 64)
		if parseErr != nil || agencyID <= 0 {
			respondError(c, http.StatusBadRequest, "invalid_agency_id", "代理商ID无效", nil)
			return
		}
		query = query.Where("agency_id = ?", agencyID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取提现记录失败", nil)
		return
	}
	if rawCursor != "" {
		query = query.Where("id < ?", cursor.PositionID)
	}
	var rows []model.AgencyWithdrawal
	if err := query.Order("id DESC").Limit(pageSize + 1).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取提现记录失败", nil)
		return
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := withdrawalView(row)
		item["agency_id"] = row.AgencyID
		item["reviewer_id"] = row.ReviewerID
		item["previous_status"] = row.PreviousStatus
		item["payment_lease_until"] = row.PaymentLeaseUntil
		items = append(items, item)
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: "root_withdrawals", Scope: cursorScope(c, "root_withdrawals", identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID,
			PositionID: rows[len(rows)-1].ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}

func (a *App) cancelOwnWithdrawal(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的提现ID", nil)
		return
	}
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	var request struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if request.ExpectedVersion <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前提现版本", nil)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if len(request.Reason) > 1000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reason", "撤回原因过长", nil)
		return
	}
	now := time.Now().UnixMilli()
	var withdrawal model.AgencyWithdrawal
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).
			Where("id = ? AND agency_id = ?", id, agency.ID).
			First(&withdrawal).Error; err != nil {
			return gorm.ErrRecordNotFound
		}
		if request.ExpectedVersion != withdrawal.Version {
			return errors.New("withdrawal version conflict")
		}
		if withdrawal.Status != "submitted" && withdrawal.Status != "reviewing" {
			return errors.New("withdrawal can no longer be cancelled")
		}
		var balance model.AgencyCommissionBalance
		if err := model.AgencyLockForUpdate(tx).
			Where("agency_id = ? AND currency_code = ?", agency.ID, withdrawal.CurrencyCode).
			First(&balance).Error; err != nil {
			return err
		}
		if balance.LockedMicros < withdrawal.AmountMicros {
			return errors.New("locked balance is inconsistent")
		}
		before := withdrawal.Status
		available, err := checkedAdd(balance.AvailableMicros, withdrawal.AmountMicros)
		if err != nil {
			return err
		}
		if err := tx.Model(&balance).Updates(map[string]any{
			"available_micros": available,
			"locked_micros":    balance.LockedMicros - withdrawal.AmountMicros,
			"version":          balance.Version + 1,
			"updated_at_ms":    now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&withdrawal).Updates(map[string]any{
			"status":         "cancelled",
			"version":        withdrawal.Version + 1,
			"updated_at_ms":  now,
			"on_hold_reason": request.Reason,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyWithdrawalTransition{
			WithdrawalID:      withdrawal.ID,
			OperationID:       requestID(c),
			BeforeStatus:      before,
			AfterStatus:       "cancelled",
			AmountDeltaMicros: withdrawal.AmountMicros,
			ActorType:         identity.ActorType,
			ActorID:           identity.ActorID,
			Evidence:          request.Reason,
			CreatedAtMS:       now,
		}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "not_found", "提现记录不存在", nil)
			return
		}
		respondError(c, http.StatusConflict, "withdrawal_cancel_failed", err.Error(), nil)
		return
	}
	respondOK(c, gin.H{"status": "cancelled", "version": withdrawal.Version + 1})
}

func (a *App) validateWithdrawalPayingGate(tx *gorm.DB, withdrawal model.AgencyWithdrawal) error {
	var agency model.Agency
	if err := model.AgencyLockForUpdate(tx).Select("id, status").First(&agency, withdrawal.AgencyID).Error; err != nil {
		return err
	}
	if agency.Status != AgencyStatusActive {
		return errors.New("agency is disabled")
	}
	var account model.AgencyWithdrawalAccount
	if err := model.AgencyLockForUpdate(tx).
		Where("id = ? AND agency_id = ?", withdrawal.AccountID, withdrawal.AgencyID).
		First(&account).Error; err != nil {
		return errors.New("withdrawal account snapshot is unavailable")
	}
	if withdrawal.AccountID <= 0 ||
		account.Version != withdrawal.AccountVersion ||
		account.KeyID != withdrawal.AccountSnapshotKeyID ||
		account.Ciphertext != withdrawal.AccountSnapshot {
		return errors.New("withdrawal account snapshot changed")
	}
	hash := sha256.Sum256([]byte(withdrawal.AccountSnapshot))
	if hex.EncodeToString(hash[:]) != withdrawal.AccountSnapshotHash {
		return errors.New("withdrawal account snapshot hash mismatch")
	}
	var balance model.AgencyCommissionBalance
	if err := model.AgencyLockForUpdate(tx).
		Where("agency_id = ? AND currency_code = ?", withdrawal.AgencyID, withdrawal.CurrencyCode).
		First(&balance).Error; err != nil {
		return err
	}
	if balance.LockedMicros < withdrawal.AmountMicros || balance.AvailableMicros < 0 {
		return errors.New("withdrawal balance is not payable")
	}
	var issueCount int64
	if err := tx.Model(&model.AgencyReconciliationIssue{}).
		Where("status = ? AND object_type IN ? AND (object_id = ? OR object_id = ?)", "open", []string{"agency", "commission_balance"}, strconv.FormatInt(withdrawal.AgencyID, 10), withdrawal.CurrencyCode).
		Count(&issueCount).Error; err != nil {
		return err
	}
	if issueCount > 0 {
		return errors.New("agency has open reconciliation issues")
	}
	return nil
}

func (a *App) reviewWithdrawal(c *gin.Context) { a.transitionWithdrawal(c, "approved") }
func (a *App) rejectWithdrawal(c *gin.Context) { a.transitionWithdrawal(c, "rejected") }
func (a *App) transitionWithdrawalCommand(c *gin.Context) {
	var request struct {
		TargetStatus    string `json:"target_status"`
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.TargetStatus) == "" {
		respondError(c, http.StatusBadRequest, "invalid_request", "必须提供目标状态", nil)
		return
	}
	switch request.TargetStatus {
	case "reviewing", "approved", "paying", "payment_unknown", "on_hold", "rejected", "cancelled":
		c.Set("agency_transition_expected_version", request.ExpectedVersion)
		c.Set("agency_transition_reason", request.Reason)
		a.transitionWithdrawal(c, request.TargetStatus)
	default:
		respondError(c, http.StatusUnprocessableEntity, "invalid_status", "不支持的提现状态", nil)
	}
}
func (a *App) transitionWithdrawal(c *gin.Context, target string) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的提现ID", nil)
		return
	}
	identity := currentIdentity(c)
	var request struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&request)
	if value, exists := c.Get("agency_transition_expected_version"); exists {
		if expected, ok := value.(int64); ok {
			request.ExpectedVersion = expected
		}
	}
	if value, exists := c.Get("agency_transition_reason"); exists {
		if reason, ok := value.(string); ok {
			request.Reason = reason
		}
	}
	if request.ExpectedVersion <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前提现版本", nil)
		return
	}
	now := time.Now().UnixMilli()
	var withdrawal model.AgencyWithdrawal
	var paymentLeaseToken int64
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).First(&withdrawal, id).Error; err != nil {
			return err
		}
		if request.ExpectedVersion != withdrawal.Version {
			return errors.New("withdrawal version conflict")
		}
		allowed := map[string]map[string]bool{
			"submitted":       {"reviewing": true, "approved": true, "rejected": true, "on_hold": true, "cancelled": true},
			"reviewing":       {"approved": true, "rejected": true, "on_hold": true, "cancelled": true},
			"approved":        {"paying": true, "on_hold": true, "cancelled": true},
			"paying":          {"payment_unknown": true, "paid": true, "on_hold": true},
			"payment_unknown": {"paid": true, "on_hold": true},
			"on_hold":         {"reviewing": true, "cancelled": true},
		}
		if !allowed[withdrawal.Status][target] {
			return errors.New("withdrawal status transition is not allowed")
		}
		before := withdrawal.Status
		if before == "on_hold" && (withdrawal.PreviousStatus == "paying" || withdrawal.PreviousStatus == "payment_unknown") && (target == "reviewing" || target == "cancelled") {
			return errors.New("payment status must be verified before moving this withdrawal")
		}
		if target == "paying" {
			if err := a.validateWithdrawalPayingGate(tx, withdrawal); err != nil {
				return err
			}
			paymentLeaseToken = time.Now().UnixNano()
			if paymentLeaseToken <= 0 {
				return errors.New("payment lease token generation failed")
			}
		}
		if target == "rejected" || target == "cancelled" {
			if withdrawal.Status == "paying" || withdrawal.Status == "payment_unknown" || withdrawal.Status == "paid" {
				return errors.New("paid withdrawal cannot be cancelled")
			}
			var balance model.AgencyCommissionBalance
			if err := model.AgencyLockForUpdate(tx).Where("agency_id = ? AND currency_code = ?", withdrawal.AgencyID, withdrawal.CurrencyCode).First(&balance).Error; err != nil {
				return err
			}
			if balance.LockedMicros < withdrawal.AmountMicros {
				return errors.New("locked balance is inconsistent")
			}
			available, err := checkedAdd(balance.AvailableMicros, withdrawal.AmountMicros)
			if err != nil {
				return err
			}
			if err := tx.Model(&balance).Updates(map[string]any{"available_micros": available, "locked_micros": balance.LockedMicros - withdrawal.AmountMicros, "version": balance.Version + 1, "updated_at_ms": now}).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{"status": target, "version": withdrawal.Version + 1, "reviewer_id": identity.ActorID, "updated_at_ms": now, "on_hold_reason": request.Reason}
		if target == "on_hold" {
			updates["previous_status"] = before
		}
		if target == "paying" {
			updates["payment_lease_owner"] = fmt.Sprintf("%s:%d", identity.ActorType, identity.ActorID)
			updates["payment_lease_token"] = paymentLeaseToken
			updates["payment_lease_until"] = time.Now().Add(10 * time.Minute).Unix()
		}
		// A payment_unknown/on_hold transition ends the local payment lease.
		// Marking an already-attempted external payment as paid is a fact
		// recording operation and must remain possible after the old lease
		// expires; it must never create a second payment attempt.
		if target == "payment_unknown" || target == "on_hold" {
			updates["payment_lease_owner"] = ""
			updates["payment_lease_token"] = 0
			updates["payment_lease_until"] = 0
		}
		if target == "paid" {
			updates["payment_lease_owner"] = ""
			updates["payment_lease_token"] = 0
			updates["payment_lease_until"] = 0
		}
		if err := tx.Model(&withdrawal).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyWithdrawalTransition{WithdrawalID: withdrawal.ID, OperationID: requestID(c), BeforeStatus: before, AfterStatus: target, ActorType: identity.ActorType, ActorID: identity.ActorID, Evidence: request.Reason, CreatedAtMS: now}).Error
	})
	if err != nil {
		respondError(c, http.StatusConflict, "withdrawal_transition_failed", err.Error(), nil)
		return
	}
	response := gin.H{"status": target}
	if target == "paying" {
		response["payment_lease_token"] = paymentLeaseToken
		response["payment_lease_until"] = time.Now().Add(10 * time.Minute).Unix()
	}
	respondOK(c, response)
}
func (a *App) markWithdrawalPaid(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的提现ID", nil)
		return
	}
	identity := currentIdentity(c)
	var request struct {
		ExpectedVersion   int64  `json:"expected_version"`
		PaymentChannel    string `json:"payment_channel"`
		PaymentLeaseToken int64  `json:"payment_lease_token"`
		PaymentReference  string `json:"payment_reference"`
	}
	if err = c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.PaymentReference) == "" {
		respondError(c, http.StatusBadRequest, "invalid_request", "必须提供付款凭证", nil)
		return
	}
	if request.ExpectedVersion <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前提现版本", nil)
		return
	}
	request.PaymentChannel = strings.TrimSpace(request.PaymentChannel)
	if request.PaymentChannel == "" {
		request.PaymentChannel = "bank"
	}
	if len(request.PaymentChannel) > 64 || len(strings.TrimSpace(request.PaymentReference)) > 191 {
		respondError(c, http.StatusBadRequest, "invalid_request", "付款渠道或凭证过长", nil)
		return
	}
	var withdrawal model.AgencyWithdrawal
	now := time.Now().UnixMilli()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).First(&withdrawal, id).Error; err != nil {
			return err
		}
		before := withdrawal.Status
		if withdrawal.Status == "paid" && withdrawal.PaymentReference == request.PaymentReference && (withdrawal.PaymentChannel == "" || withdrawal.PaymentChannel == request.PaymentChannel) {
			return nil
		}
		if request.ExpectedVersion != withdrawal.Version {
			return errors.New("withdrawal version conflict")
		}
		if withdrawal.Status != "paying" && withdrawal.Status != "payment_unknown" &&
			!(withdrawal.Status == "on_hold" && (withdrawal.PreviousStatus == "paying" || withdrawal.PreviousStatus == "payment_unknown")) {
			return errors.New("withdrawal is not payable")
		}
		if withdrawal.Status == "paying" && withdrawal.PaymentLeaseToken != 0 {
			owner := fmt.Sprintf("%s:%d", identity.ActorType, identity.ActorID)
			if withdrawal.PaymentLeaseOwner != owner || request.PaymentLeaseToken != withdrawal.PaymentLeaseToken || withdrawal.PaymentLeaseUntil < time.Now().Unix() {
				return errors.New("payment lease is invalid or expired")
			}
		}
		var duplicate int64
		if err := tx.Model(&model.AgencyWithdrawal{}).
			Where("payment_reference = ? AND id <> ? AND (payment_channel = ? OR payment_channel = ?)", request.PaymentReference, withdrawal.ID, request.PaymentChannel, "").
			Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errors.New("payment reference already used")
		}
		if err := tx.Model(&model.AgencyWithdrawalPaymentReference{}).
			Where("payment_channel = ? AND payment_reference = ? AND withdrawal_id <> ?", request.PaymentChannel, request.PaymentReference, withdrawal.ID).
			Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errors.New("payment reference already used")
		}
		if err := tx.Model(&withdrawal).Updates(map[string]any{"status": "paid", "version": withdrawal.Version + 1, "payment_channel": request.PaymentChannel, "payment_reference": request.PaymentReference, "payment_lease_owner": "", "payment_lease_token": 0, "payment_lease_until": 0, "reviewer_id": identity.ActorID, "updated_at_ms": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.AgencyWithdrawalPaymentReference{
			PaymentChannel: request.PaymentChannel, PaymentReference: request.PaymentReference,
			WithdrawalID: withdrawal.ID, CreatedAtMS: now,
		}).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return errors.New("payment reference already used")
			}
			return err
		}
		var balance model.AgencyCommissionBalance
		if err := model.AgencyLockForUpdate(tx).Where("agency_id = ? AND currency_code = ?", withdrawal.AgencyID, withdrawal.CurrencyCode).First(&balance).Error; err != nil {
			return err
		}
		if balance.LockedMicros < withdrawal.AmountMicros {
			return errors.New("locked balance is inconsistent")
		}
		paidMicros, err := checkedAdd(balance.PaidMicros, withdrawal.AmountMicros)
		if err != nil {
			return err
		}
		if err := tx.Model(&balance).Updates(map[string]any{"locked_micros": balance.LockedMicros - withdrawal.AmountMicros, "paid_micros": paidMicros, "version": balance.Version + 1, "updated_at_ms": now}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyWithdrawalTransition{WithdrawalID: withdrawal.ID, OperationID: requestID(c), BeforeStatus: before, AfterStatus: "paid", ActorType: identity.ActorType, ActorID: identity.ActorID, Evidence: request.PaymentChannel + ":" + request.PaymentReference, CreatedAtMS: now}).Error
	})
	if err != nil {
		respondError(c, http.StatusConflict, "withdrawal_transition_failed", err.Error(), nil)
		return
	}
	respondOK(c, gin.H{"status": "paid"})
}
