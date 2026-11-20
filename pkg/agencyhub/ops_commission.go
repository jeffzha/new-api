package agencyhub

import (
	"errors"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"gorm.io/gorm"
)

// ProjectOPSCommissionTx reuses the existing source commission ledger. Its
// caller must atomically persist a verified OPS receipt and replay barrier.
// It never writes a customer wallet or sends a payment.
func ProjectOPSCommissionTx(tx *gorm.DB, event agencycontract.BillingEvent) error {
	if !opsauthority.Enabled() || event.CurrencyCode != "USD" || event.QuotaPerUnit != "1000000" || event.ExchangeRate != "1" || event.EventID == "" || event.UserID <= 0 {
		return errors.New("invalid OPS source commission event")
	}
	if event.EventType == "agency.billing_reversed" {
		// Zero-earning tiers have no earned ledger row to reverse.
		splits := make([]agencycontract.CommissionSplit, 0, len(event.CommissionSplits))
		for _, split := range event.CommissionSplits {
			if split.ReversedCommissionAmountMicros > 0 {
				splits = append(splits, split)
			}
		}
		event.CommissionSplits = splits
	}
	if err := agencycontract.ValidateCommissionSplits(event); err != nil {
		return err
	}
	app := &App{}
	if _, err := app.projectCommissionSplits(tx, event, "", event.OccurredAtMS); err != nil {
		return err
	}
	if event.EventType != "agency.billing_reversed" {
		amount := event.CommissionAmountMicros
		// Legacy usage/stat rows have no per-row quota unit. Keep their existing
		// 500000 units/USD contract; the ledger retains every USD micro.
		// Cumulative truncation preserves the funding partition.
		usage := event
		usage.StandardQuota /= 2
		usage.ChargedTotalQuota /= 2
		usage.PaidAllocatedQuota = event.PaidAllocatedQuota / 2
		usage.NonpaidAllocatedQuota = (event.PaidAllocatedQuota+event.NonpaidAllocatedQuota)/2 - usage.PaidAllocatedQuota
		usage.DebtAllocatedQuota = usage.ChargedTotalQuota - usage.PaidAllocatedQuota - usage.NonpaidAllocatedQuota
		if err := app.recordUsageFact(tx, usage, ""); err != nil {
			return err
		}
		if err := app.recordDailyStat(tx, usage, amount, false); err != nil {
			return err
		}
	} else {
		reversed := int64(0)
		for _, split := range event.CommissionSplits {
			reversed += split.ReversedCommissionAmountMicros
			if split.ReversedCommissionAmountMicros > 0 {
				if err := holdUnpaidWithdrawals(tx, split.AgencyID, event.CurrencyCode, event.EventID, event.OccurredAtMS); err != nil {
					return err
				}
			}
		}
		if err := app.recordDailyStat(tx, event, -reversed, true); err != nil {
			return err
		}
	}
	return nil
}
