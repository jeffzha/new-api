package model

import (
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// AgencyTaskChargeBasis stores only the immutable pricing inputs needed after
// provider completion. It deliberately excludes credentials, raw task bodies,
// response URLs and mutable gateway pricing configuration.
type AgencyTaskChargeBasis struct {
	Version           string                       `json:"version"`
	ComponentBilling  bool                         `json:"component_billing"`
	Mode              string                       `json:"mode"`
	QuotaPerUnit      float64                      `json:"quota_per_unit"`
	ModelPrice        float64                      `json:"model_price"`
	ModelRatio        float64                      `json:"model_ratio"`
	OtherMultiplier   float64                      `json:"other_multiplier"`
	IgnoreOtherRatios bool                         `json:"ignore_other_ratios"`
	ProviderBilling   *TaskProviderBillingSnapshot `json:"provider_billing,omitempty"`
}

const AgencyTaskChargeBasisVersion = "agency-task-basis-v1"

var ErrAgencyTaskUsagePending = errors.New("agency task final usage is not available")

// FreezeAgencyTaskChargeBasis must finish before sending the provider request.
// Repeated preparation verifies equality instead of replacing accepted prices.
func FreezeAgencyTaskChargeBasis(userID int, chargeID string, basis AgencyTaskChargeBasis) error {
	if _, err := basis.quotaAtRatio(1, 1); err != nil {
		return err
	}
	encoded, err := common.Marshal(basis)
	if err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var journal AgencyBillingJournal
		if err := lockForUpdate(tx).Where("charge_id = ? AND segment_no = 0 AND user_id = ?", chargeID, userID).First(&journal).Error; err != nil {
			return err
		}
		if journal.Status != "reserved" {
			return ErrAgencyChargeConflict
		}
		if journal.BillingBasis != "" && journal.BillingBasis != "{}" {
			if journal.BillingBasis == string(encoded) {
				return nil
			}
			return ErrAgencyChargeConflict
		}
		updated := tx.Model(&AgencyBillingJournal{}).Where("id = ? AND version = ?", journal.ID, journal.Version).
			Updates(map[string]any{"billing_basis": string(encoded), "version": journal.Version + 1})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAgencyChargeConflict
		}
		return nil
	})
}

// quotaAtRatio preserves the task engine's original rounding order. Fixed
// prices truncate the price/group product before applying task multipliers;
// token prices truncate the final product; CNY token prices use decimal math.
func (basis AgencyTaskChargeBasis) quotaAtRatio(totalTokens int64, ratio float64) (int, error) {
	if basis.Version != AgencyTaskChargeBasisVersion || basis.QuotaPerUnit <= 0 || math.IsNaN(basis.QuotaPerUnit) || math.IsInf(basis.QuotaPerUnit, 0) ||
		ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) || basis.OtherMultiplier <= 0 || math.IsNaN(basis.OtherMultiplier) || math.IsInf(basis.OtherMultiplier, 0) {
		return 0, errors.New("invalid frozen agency task pricing basis")
	}
	switch basis.Mode {
	case "fixed":
		if basis.ModelPrice < 0 || math.IsNaN(basis.ModelPrice) || math.IsInf(basis.ModelPrice, 0) {
			return 0, errors.New("invalid frozen task model price")
		}
		base, err := common.QuotaFromFloatStrict(basis.ModelPrice * basis.QuotaPerUnit * ratio)
		if err != nil || basis.IgnoreOtherRatios {
			return base, err
		}
		return common.QuotaFromFloatStrict(float64(base) * basis.OtherMultiplier)
	case "tokens":
		if totalTokens <= 0 {
			return 0, ErrAgencyTaskUsagePending
		}
		if basis.ModelRatio < 0 || math.IsNaN(basis.ModelRatio) || math.IsInf(basis.ModelRatio, 0) {
			return 0, errors.New("invalid frozen task model ratio")
		}
		return common.QuotaFromFloatStrict(float64(totalTokens) * basis.ModelRatio * ratio * basis.OtherMultiplier)
	case "cny_tokens":
		if totalTokens <= 0 {
			return 0, ErrAgencyTaskUsagePending
		}
		provider := basis.ProviderBilling
		if provider == nil || provider.Provider == "" || provider.Currency != "CNY" {
			return 0, errors.New("invalid frozen task provider billing snapshot")
		}
		unitPrice, err := decimal.NewFromString(provider.UnitPricePerMillionTokens)
		if err != nil || !unitPrice.IsPositive() {
			return 0, errors.New("invalid frozen provider unit price")
		}
		exchangeRate, err := decimal.NewFromString(provider.CNYPerUSD)
		if err != nil || !exchangeRate.IsPositive() {
			return 0, errors.New("invalid frozen provider exchange rate")
		}
		quota := decimal.NewFromInt(totalTokens).Div(decimal.NewFromInt(1_000_000)).Mul(unitPrice).
			Div(exchangeRate).Mul(decimal.NewFromFloat(basis.QuotaPerUnit)).Mul(decimal.NewFromFloat(ratio))
		return common.QuotaFromDecimalStrict(quota)
	default:
		return 0, errors.New("unsupported frozen task pricing mode")
	}
}

// AgencyTaskFinalCharge evaluates the same frozen engine separately at Q=1,
// S and C. A cost is never inferred by dividing an already rounded charge.
func AgencyTaskFinalCharge(basis AgencyTaskChargeBasis, snapshot agencycontract.PricingSnapshot, totalTokens int64, cancelled bool) (agencycontract.BillingEvent, error) {
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, UserID: snapshot.UserID,
		FinancialChargeID: snapshot.FinancialChargeID, BusinessStatus: "success", BillingStatus: "finalized", FinancialFinal: true,
		CommissionEligible: snapshot.CommissionEligible, CommissionSkipReason: snapshot.EligibilityReason}
	if cancelled {
		event.BusinessStatus, event.BillingStatus, event.CommissionEligible = "cancelled", "cancelled", false
		event.CommissionSkipReason = "task_confirmed_failure"
	} else {
		if snapshot.SettlementBPS < 0 || snapshot.SalesBPS < snapshot.SettlementBPS || snapshot.SalesBPS > agencycontract.MaxCoefficientBPS {
			return event, errors.New("invalid frozen task policy coefficients")
		}
		standard, err := basis.quotaAtRatio(totalTokens, 1)
		if err != nil {
			return event, err
		}
		charged, err := basis.quotaAtRatio(totalTokens, float64(snapshot.SalesBPS)/10000)
		if err != nil {
			return event, err
		}
		cost, err := basis.quotaAtRatio(totalTokens, float64(snapshot.SettlementBPS)/10000)
		if err != nil {
			return event, err
		}
		event.StandardQuota, event.ChargedTotalQuota = int64(standard), int64(charged)
		if event.CommissionEligible {
			event.CommissionableQuota, event.SettlementCostQuota = int64(charged), int64(cost)
			event.TheoreticalCommissionQuota = int64(charged - cost)
		} else {
			event.NoncommissionableQuota = int64(charged)
		}
		if basis.ComponentBilling {
			event.SchemaVersion = agencycontract.ComponentSchemaVersion
			event.Components = []agencycontract.BillingComponent{{ComponentID: "task", StandardQuota: event.StandardQuota,
				ChargedTotalQuota: event.ChargedTotalQuota, CommissionableQuota: event.CommissionableQuota,
				NoncommissionableQuota: event.NoncommissionableQuota, SettlementCostQuota: event.SettlementCostQuota,
				TheoreticalCommissionQuota: event.TheoreticalCommissionQuota, CommissionEligible: event.CommissionEligible,
				CommissionSkipReason: event.CommissionSkipReason}}
		}
	}
	encoded, err := common.Marshal(struct {
		Basis       AgencyTaskChargeBasis `json:"frozen_task_basis"`
		TotalTokens int64                 `json:"total_tokens"`
		Outcome     string                `json:"outcome"`
	}{basis, totalTokens, event.BusinessStatus})
	if err != nil {
		return event, err
	}
	event.BillingBasis = string(encoded)
	return event, nil
}
