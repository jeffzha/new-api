package model

import (
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// CaptureCNAgencyFundingTx freezes forward native allocations at the financial
// journal commit. Mutable current allocations are never used to fabricate past
// revisions. Lot purchase money comes from AgencyTopupFact, not snapshot JSON.
func CaptureCNAgencyFundingTx(tx *gorm.DB, event agencycontract.BillingEvent) error {
	if !CNFinanceCaptureEnabled() {
		return nil
	}
	if event.EventType != "agency.billing_finalized" && event.EventType != "agency.billing_corrected" && event.EventType != "agency.billing_reversed" {
		return nil
	}
	var allocations []AgencyFundingAllocation
	if err := tx.Where("charge_id = ? AND user_id = ?", event.FinancialChargeID, event.UserID).Order("id").Find(&allocations).Error; err != nil {
		return err
	}
	for _, allocation := range allocations {
		if allocation.LotID <= 0 {
			continue
		} // Opening/debt without a purchase remains unknown.
		var lot AgencyFundingLot
		if err := tx.First(&lot, allocation.LotID).Error; err != nil {
			return err
		}
		var purchase AgencyTopupFact
		err := tx.Where("source_operation_id = ? AND user_id = ?", lot.SourceID, lot.UserID).First(&purchase).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err = cnCaptureAgencyPurchase(tx, lot, purchase); err != nil {
			return err
		}
		paid, _, _, err := agencyFundingAllocationActiveParts(allocation)
		if err != nil {
			return err
		}
		if lot.PaidInitial <= 0 {
			continue
		}
		lotID := CNIdentity("agency-lot", lot.SourceKind, lot.SourceID, "paid")
		allocationID := CNIdentity("agency-allocation", allocation.ChargeID, strconv.Itoa(allocation.SegmentNo), allocation.ComponentID, lotID)
		evidence := CNEvidence{System: "gateway-cn", RecordType: "agency_native_allocation_at_journal", RecordID: allocationID, EvidenceID: event.EventID}
		f := cnBaseFact("funding_allocation", allocationID, "0", event.OperationID, "agency_wallet", lot.UserID, time.Now().UTC(), evidence)
		f.FactID = CNIdentity(allocationID, event.EventID)
		f.Revision = event.JournalRevision
		quota := strconv.FormatInt(paid, 10)
		previousQuota := int64(0)
		var previous CNFinanceOutbox
		chain := CNIdentity(f.Kind, f.EconomicID, f.SegmentID)
		err = tx.Where("chain_key = ?", chain).Order("revision DESC").First(&previous).Error
		var previousMoney *CNMoney
		if err == nil {
			var p CNFact
			if err = common.UnmarshalJsonStr(previous.Payload, &p); err != nil {
				return err
			}
			if p.Revision == f.Revision {
				continue
			}
			f.PreviousFactID = &previous.FactID
			f.EffectiveAt = p.EffectiveAt
			f.SourcePeriod = p.SourcePeriod
			previousQuota, err = strconv.ParseInt(*p.Cumulative.Quota, 10, 64)
			if err != nil {
				return err
			}
			previousMoney = p.Cumulative.Money
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		delta := strconv.FormatInt(paid-previousQuota, 10)
		f.Cumulative = CNMetric{Quota: &quota}
		f.Delta = CNMetric{Quota: &delta}
		paymentID := CNIdentity("agency-payment", purchase.SourceOperationID)
		if purchase.ActualMoney != "" && purchase.PaymentReference != "" && opsMoneyCurrency.MatchString(purchase.CurrencyCode) {
			amount, err := cnDecimal(purchase.ActualMoney)
			if err != nil {
				return err
			}
			value := cnDivideHalfEven(amount.Mul(decimal.NewFromInt(paid)), decimal.NewFromInt(lot.PaidInitial))
			prior := decimal.Zero
			if previousMoney != nil {
				prior, err = cnDecimal(previousMoney.Gross)
				if err != nil {
					return err
				}
			}
			f.Cumulative.Money = cnUnknownTaxMoney(value.String(), purchase.CurrencyCode)
			f.Delta.Money = cnUnknownTaxMoney(value.Sub(prior).String(), purchase.CurrencyCode)
			f.Quality = "verified_payment"
			f.Conversion = map[string]any{"sourceUnit": "currency", "quotaPerUnit": nil, "originalCurrency": purchase.CurrencyCode, "settlementCurrency": purchase.CurrencyCode, "exchangeRate": "1", "fxVersion": "identity-1", "rounding": "HALF_EVEN_18"}
		}
		var restores any
		if paid < previousQuota {
			restores = f.PreviousFactID
		}
		f.Funding = map[string]any{"allocationId": allocationID, "lotId": lotID, "chargeEconomicId": event.FinancialChargeID, "chargeSegmentId": strconv.Itoa(event.SegmentNo), "allocatedQuota": quota, "recognizedValue": f.Cumulative.Money, "policyVersion": "agency-native-purchase-proportion-v1", "authority": "native", "previousAllocationFactId": f.PreviousFactID, "restoresAllocationFactId": restores, "evidence": f.SourceEvidence}
		f.Links["paymentIds"] = []string{paymentID}
		f.Links["fundingLotIds"] = []string{lotID}
		f.Links["allocationIds"] = []string{allocationID}
		f.Links["chargeFactIds"] = []string{CNIdentity("charge", "agency_billing_outbox", event.EventID)}
		if err = StageCNFactTx(tx, f); err != nil {
			return err
		}
	}
	return nil
}

func cnCaptureAgencyPurchase(tx *gorm.DB, lot AgencyFundingLot, purchase AgencyTopupFact) error {
	paymentID := CNIdentity("agency-payment", purchase.SourceOperationID)
	evidence := CNEvidence{System: "gateway-cn", RecordType: "agency_topup_fact", RecordID: purchase.SourceOperationID, EvidenceID: CNIdentity(purchase.SourceOperationID, purchase.PaymentReference, purchase.ActualMoney, purchase.CurrencyCode, purchase.QuotaConversionSnapshot)}
	f := cnBaseFact("payment", paymentID, "0", purchase.SourceOperationID, "agency_wallet", purchase.UserID, time.UnixMilli(purchase.OccurredAtMS), evidence)
	if purchase.PaymentStatus != "success" || purchase.ActualMoney == "" || purchase.PaymentReference == "" || !opsMoneyCurrency.MatchString(purchase.CurrencyCode) {
		return nil
	}
	if _, err := cnDecimal(purchase.ActualMoney); err != nil {
		return err
	}
	f.Cumulative.Money = cnUnknownTaxMoney(purchase.ActualMoney, purchase.CurrencyCode)
	f.Delta = f.Cumulative
	f.Quality = "verified_payment"
	f.Conversion = map[string]any{"sourceUnit": "currency", "quotaPerUnit": nil, "originalCurrency": purchase.CurrencyCode, "settlementCurrency": purchase.CurrencyCode, "exchangeRate": "1", "fxVersion": "identity-1", "rounding": "HALF_EVEN_18"}
	f.Links["paymentIds"] = []string{paymentID}
	if err := StageCNFactTx(tx, f); err != nil {
		return err
	}
	for _, part := range []struct {
		kind  string
		quota int64
		money *CNMoney
	}{{"paid", lot.PaidInitial, f.Cumulative.Money}, {"bonus", lot.BonusInitial, cnUnknownTaxMoney("0", purchase.CurrencyCode)}} {
		if part.quota <= 0 {
			continue
		}
		lotID := CNIdentity("agency-lot", lot.SourceKind, lot.SourceID, part.kind)
		l := cnBaseFact("funding_lot", lotID, "0", purchase.SourceOperationID, "agency_wallet", lot.UserID, time.Unix(lot.CreatedAt, 0), evidence)
		l.FactID = CNIdentity("funding-lot", lotID)
		l.Quality = "verified_payment"
		l.Conversion = f.Conversion
		quota := strconv.FormatInt(part.quota, 10)
		l.Cumulative = CNMetric{Quota: &quota, Money: part.money}
		l.Delta = l.Cumulative
		l.Funding = map[string]any{"lotId": lotID, "paymentId": paymentID, "lotKind": part.kind, "purchasedQuota": quota, "paidValue": part.money, "purchasedAt": l.EffectiveAt, "policyVersion": "agency-native-purchase-proportion-v1", "evidence": l.SourceEvidence}
		l.Links["paymentIds"] = []string{paymentID}
		l.Links["fundingLotIds"] = []string{lotID}
		if err := StageCNFactTx(tx, l); err != nil {
			return err
		}
	}
	return nil
}

func cnCaptureNativeSources(tx *gorm.DB) error {
	if tx.Migrator().HasTable(&AgencyTopupFact{}) && tx.Migrator().HasTable(&AgencyFundingLot{}) {
		var lots []AgencyFundingLot
		if err := tx.Where("paid_initial > 0 OR bonus_initial > 0").Order("id").Find(&lots).Error; err != nil {
			return err
		}
		for _, lot := range lots {
			var purchase AgencyTopupFact
			err := tx.Where("source_operation_id = ? AND user_id = ?", lot.SourceID, lot.UserID).First(&purchase).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if err = cnCaptureSource(tx, "payments_topups", purchase.SourceOperationID, func(child *gorm.DB) error { return cnCaptureAgencyPurchase(child, lot, purchase) }); err != nil {
				return err
			}
		}
	}
	if tx.Migrator().HasTable(&AgencyCommissionLedger{}) {
		var rows []AgencyCommissionLedger
		if err := tx.Order("id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			identity := CNIdentity("agency-commission", row.EventID, row.ComponentID, strconv.FormatInt(row.AgencyID, 10), row.EntryType, strconv.Itoa(row.HierarchyDepth))
			var count int64
			if err := tx.Model(&CNFinanceOutbox{}).Where("fact_id = ?", identity).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			if !opsMoneyCurrency.MatchString(row.CurrencyCode) {
				continue
			}
			evidence := CNEvidence{System: "gateway-cn", RecordType: "agency_commission_ledger", RecordID: identity, EvidenceID: row.EventID}
			f := cnBaseFact("agency_commission", identity, "0", row.EventID, "not_applicable", row.UserID, time.UnixMilli(row.OccurredAtMS), evidence)
			f.FactID = identity
			f.Quality = "direct"
			amount := decimal.NewFromInt(row.AmountMicros).Div(decimal.NewFromInt(1000000)).String()
			f.Cumulative.Money = cnUnknownTaxMoney(amount, row.CurrencyCode)
			f.Delta = f.Cumulative
			f.Conversion = map[string]any{"sourceUnit": "currency", "quotaPerUnit": nil, "originalCurrency": row.CurrencyCode, "settlementCurrency": row.CurrencyCode, "exchangeRate": "1", "fxVersion": "identity-1", "rounding": "HALF_EVEN_18"}
			if err := cnCaptureSource(tx, "agency_commissions", identity, func(child *gorm.DB) error { return StageCNFactTx(child, f) }); err != nil {
				return err
			}
		}
	}
	return nil
}
