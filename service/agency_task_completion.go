package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

func completeAgencyTaskBilling(ctx context.Context, task *model.Task, expectedStatus model.TaskStatus, totalTokens int64) error {
	result, err := model.CompleteAgencyTask(task, expectedStatus, totalTokens)
	if err != nil {
		reason := "terminal_settlement_failed"
		if errors.Is(err, model.ErrAgencyTaskUsagePending) {
			reason = "final_usage_pending"
		} else if errors.Is(err, model.ErrAgencyChargeConflict) {
			reason = "terminal_conflict"
		}
		if markErr := model.MarkAgencyTaskReconcileRequired(task, reason); markErr != nil {
			logger.LogError(ctx, fmt.Sprintf("agency task %s reconciliation marker failed: %v", task.TaskID, markErr))
		}
		return err
	}
	if result.Changed && result.QuotaDelta != 0 {
		recordTaskQuotaAdjustment(ctx, task, result.PreConsumedQuota, task.Quota, "agency task terminal settlement")
	}
	// Async video/provider-billing tasks (e.g. seedance) may never produce a
	// final provider bill our resolver can reconcile (openai_seedance upstreams
	// often omit token usage), so the successful charge would otherwise never
	// surface as an agency usage fact. Emit the forward usage event here using
	// the charged (pre-consumed) quota. It is a no-op when a terminal event
	// already exists for the charge, so the billing-reconciliation path (e.g.
	// seedance_domestic) remains authoritative and no double-finalize happens.
	if expectedStatus == model.TaskStatusSuccess {
		// Provider-billing tasks whose resolver cannot fetch a final provider
		// bill (openai_seedance upstreams commonly omit token usage) would
		// otherwise never surface a successful charge as an agency usage fact.
		// Emit the forward event from the charged quota, unless the provider
		// has its own authoritative billing-reconciliation finalizer (e.g.
		// seedance_domestic), which should stay authoritative.
		provider := ""
		if bc := task.PrivateData.BillingContext; bc != nil && bc.ProviderBilling != nil {
			provider = bc.ProviderBilling.Provider
		}
		if provider != model.TaskBillingProviderSeedanceDomestic {
			if finErr := ensureAgencyTaskFinalUsage(task, int64(task.Quota)); finErr != nil {
				logger.LogError(ctx, fmt.Sprintf("record agency task final usage %s failed: %v", task.TaskID, finErr))
			}
		}
	}
	return nil
}

// ensureAgencyTaskFinalUsage persists the forward agency.billing_finalized
// success event and usage fact for an async video task that settled at the
// charged quota. It deliberately does not touch wallet/token funding (already
// committed by the pre-consumed charge) and is idempotent: it only finalizes a
// journal that is not already terminal-success, and is a no-op otherwise so it
// cannot conflict with the billing-reconciliation finalizer.
func ensureAgencyTaskFinalUsage(task *model.Task, finalQuota int64) error {
	if task == nil || task.PrivateData.BillingContext == nil || task.PrivateData.BillingContext.AgencyPricing == nil {
		return nil
	}
	if finalQuota < 0 {
		return errors.New("negative agency task charge")
	}
	bc := task.PrivateData.BillingContext
	snapshot := *bc.AgencyPricing
	if strings.TrimSpace(snapshot.OriginModelName) == "" {
		return errors.New("agency task pricing missing origin model")
	}
	chargeID := strings.TrimSpace(bc.AgencyChargeID)
	if chargeID == "" {
		chargeID = task.TaskID
	}
	if fin := strings.TrimSpace(snapshot.FinancialChargeID); fin != "" {
		chargeID = fin
	}
	standard := bc.AgencyStandardQuota
	if standard == 0 {
		standard = finalQuota
	}
	charged := finalQuota
	policy := agencycontract.ResolvedPolicy{SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS, ModelKey: snapshot.ModelKey, OriginModelName: snapshot.OriginModelName}
	basis, err := agencycontract.Calculate(standard, policy, 0, true)
	if err != nil {
		return err
	}
	settlement := basis.SettlementCostQuota
	theoretical := charged - settlement
	if theoretical < 0 {
		return errors.New("negative agency commission")
	}
	commissionQuota, err := agencycontract.CommissionForPaid(theoretical, 0, charged, true)
	if err != nil {
		return err
	}
	commissionEligible := snapshot.CommissionEligible
	commissionableQuota := charged
	noncommissionableQuota := int64(0)
	if !commissionEligible {
		theoretical = 0
		commissionQuota = 0
		settlement = 0
		commissionableQuota = 0
		noncommissionableQuota = charged
	}
	commission, err := agencyCommissionMicros(commissionQuota, snapshot)
	if err != nil {
		return err
	}
	eventID := strings.TrimSpace(bc.AgencyBillingEventID)
	if eventID == "" {
		digest := sha256.Sum256([]byte("agency-task-final:" + chargeID))
		eventID = "agency-task-final-" + hex.EncodeToString(digest[:])
	}
	operationID := "agency-task-finalize-" + eventID
	occurred := time.Now().UnixMilli()
	event := agencycontract.BillingEvent{
		SchemaVersion:          agencycontract.SchemaVersion,
		EventID:                eventID,
		EventType:              "agency.billing_finalized",
		FinancialChargeID:      chargeID,
		OperationID:            operationID,
		SegmentNo:              0,
		JournalRevision:        1,
		EventIndex:             0,
		EventCount:             1,
		OccurredAtMS:           occurred,
		UserID:                 snapshot.UserID,
		TokenID:                int64Ptr(snapshot.TokenID),
		AgencyID:               &snapshot.AgencyID,
		BindingID:              &snapshot.BindingID,
		OriginModelName:        snapshot.OriginModelName,
		BusinessStatus:         "success",
		BillingStatus:          "finalized",
		CurrencyCode:           snapshot.CurrencyCode,
		QuotaPerUnit:           snapshot.QuotaPerUnit,
		ExchangeRate:           snapshot.ExchangeRate,
		SettlementBPS:          snapshot.SettlementBPS,
		SalesBPS:               snapshot.SalesBPS,
		CommissionEligible:     commissionEligible,
		StandardQuota:          standard,
		ChargedTotalQuota:      charged,
		CommissionableQuota:    commissionableQuota,
		NoncommissionableQuota: noncommissionableQuota,
		SettlementCostQuota:    settlement,
		TheoreticalCommissionQuota: theoretical,
		PaidAllocatedQuota:     0,
		NonpaidAllocatedQuota:  0,
		DebtAllocatedQuota:     0,
		CommissionQuota:        commissionQuota,
		CommissionAmountMicros: commission,
		FinancialFinal:         true,
	}
	if commissionEligible && len(snapshot.Hierarchy) > 1 {
		splits, splitErr := agencyTierCommissionSplits(snapshot, standard, charged, 0)
		if splitErr != nil {
			return splitErr
		}
		event.CommissionSplits = splits
		var tierTheory, tierCommission, tierMicros int64
		for _, split := range splits {
			tierTheory += split.TheoreticalQuota
			tierCommission += split.CommissionQuota
			tierMicros += split.CommissionAmountMicros
		}
		event.SettlementCostQuota = charged - tierTheory
		event.TheoreticalCommissionQuota = tierTheory
		event.CommissionQuota = tierCommission
		event.CommissionAmountMicros = tierMicros
	}
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	billingBasis := mustMarshal(map[string]any{
		"version":        "agency-task-basis-v1",
		"mode":           "cny_tokens",
		"task_id":        task.TaskID,
		"charge_id":      chargeID,
		"quota_per_unit": snapshot.QuotaPerUnit,
		"standard_quota": standard,
		"charged_quota":  charged,
	})
	modelKey, keyErr := agencycontract.ModelKey(snapshot.OriginModelName)
	if keyErr != nil {
		modelKey = "unknown"
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var journal model.AgencyBillingJournal
		if err := tx.Where("charge_id = ? AND segment_no = ?", chargeID, 0).First(&journal).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if journal.Status == "finalized" && journal.BusinessStatus == "success" {
			return nil
		}
		if journal.Status == "cancelled" {
			return nil
		}
		if err := tx.Model(&model.AgencyBillingJournal{}).Where("id = ?", journal.ID).Updates(map[string]any{
			"status": "finalized", "business_status": "success", "delivery_status": "pending",
			"billing_basis": billingBasis,
			"reserve_quota": charged, "charged_total_quota": charged, "commissionable_quota": commissionableQuota,
			"settlement_cost_quota": settlement, "theoretical_commission_quota": theoretical,
			"commission_quota": commissionQuota, "commission_amount_micros": commission,
			"last_error": "", "updated_at_ms": occurred,
		}).Error; err != nil {
			return err
		}
		var existingOp model.AgencyBillingOperation
		if err := tx.Where("charge_id = ? AND segment_no = ? AND revision = ? AND operation = ?", chargeID, 0, int64(1), "finalize").First(&existingOp).Error; err == nil {
			// A finalize operation already exists (e.g. failure finalize with a
			// different hash). Do not overwrite an authoritative operation.
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&model.AgencyBillingOperation{ChargeID: chargeID, SegmentNo: 0, Revision: 1, Operation: "finalize", InputHash: payloadHash, CommittedResult: string(payload), EventCount: 1, CreatedAtMS: occurred}).Error; err != nil {
			if !errors.Is(err, gorm.ErrDuplicatedKey) {
				return err
			}
		}
		if err := tx.Create(&model.AgencyBillingOutbox{EventID: eventID, OperationID: operationID, EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: event.UserID, MoneySeq: event.MoneySeq, Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion, CreatedAtMS: occurred}).Error; err != nil {
			if !errors.Is(err, gorm.ErrDuplicatedKey) {
				return err
			}
		}
		if err := tx.Create(&model.AgencyEventDelivery{EventID: eventID, Status: "pending", NextRetryAt: time.Now().Unix(), CreatedAt: time.Now().Unix()}).Error; err != nil {
			if !errors.Is(err, gorm.ErrDuplicatedKey) {
				return err
			}
		}
		usageFact := &model.AgencyUsageFact{
			EventID: eventID, ComponentID: "task", FinancialChargeID: chargeID,
			UserID: event.UserID, AgencyID: &snapshot.AgencyID, BindingID: &snapshot.BindingID,
			OriginModelName: snapshot.OriginModelName, ModelKey: modelKey,
			BusinessStatus: "success", StandardQuota: standard, SalesBPS: snapshot.SalesBPS,
			ChargedQuota: charged, CurrencyCode: snapshot.CurrencyCode, OccurredAtMS: occurred,
		}
		if err := tx.Create(usageFact).Error; err != nil {
			if !errors.Is(err, gorm.ErrDuplicatedKey) {
				return err
			}
		}
		return nil
	})
}
