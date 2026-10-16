package model

import (
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Receipt time belongs to the CN stream; legacy event time retains its usage
// semantics. The receipt commits and rolls back with the financial mutation.
type CNFinanceReceipt struct {
	EventID    string `gorm:"size:191;primaryKey"`
	OccurredAt string `gorm:"size:40;not null"`
}

type CNFundingValueLot struct {
	LotID          string `gorm:"size:64;primaryKey"`
	InitialQuota   int64  `gorm:"not null"`
	RemainingQuota int64  `gorm:"not null"`
	RemainingValue string `gorm:"size:128;not null"`
	PaidValue      string `gorm:"size:128;not null"`
	HeldQuota      int64  `gorm:"not null"`
	HeldValue      string `gorm:"size:128;not null"`
	RetiredQuota   int64  `gorm:"not null"`
	RetiredValue   string `gorm:"size:128;not null"`
}

type CNFundingValueAllocation struct {
	AllocationID string `gorm:"size:64;primaryKey"`
	LotID        string `gorm:"size:64;not null;index"`
	Quota        int64  `gorm:"not null"`
	Value        string `gorm:"size:128;not null"`
}

// Seed from immutable heads when upgrading an existing stream. Published
// allocations keep their exact value and never receive a new residual share.
func cnFundingValueLotTx(tx *gorm.DB, lotID string, initial int64, amount decimal.Decimal) (*CNFundingValueLot, error) {
	if initial <= 0 || amount.IsNegative() {
		return nil, ErrCNConflict
	}
	var balance CNFundingValueLot
	err := lockForUpdate(tx).Where("lot_id = ?", lotID).First(&balance).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		balance = CNFundingValueLot{LotID: lotID, InitialQuota: initial, RemainingQuota: initial, RemainingValue: amount.String(), PaidValue: amount.String(), HeldValue: "0", RetiredValue: "0"}
		var history []CNFinanceOutbox
		if err = tx.Order("revision DESC").Find(&history).Error; err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		remaining := amount
		for _, row := range history {
			var fact CNFact
			if err = common.UnmarshalJsonStr(row.Payload, &fact); err != nil {
				return nil, err
			}
			if fact.Kind != "funding_allocation" {
				continue
			}
			id, _ := fact.Funding["allocationId"].(string)
			if fact.Funding["lotId"] != lotID || seen[id] {
				continue
			}
			seen[id] = true
			if fact.Cumulative.Quota == nil || fact.Cumulative.Money == nil {
				return nil, ErrCNConflict
			}
			q, e := strconv.ParseInt(*fact.Cumulative.Quota, 10, 64)
			if e != nil {
				return nil, e
			}
			value, e := cnDecimal(fact.Cumulative.Money.Gross)
			if e != nil {
				return nil, e
			}
			balance.RemainingQuota -= q
			remaining = remaining.Sub(value)
			if err = tx.Create(&CNFundingValueAllocation{AllocationID: id, LotID: lotID, Quota: q, Value: value.String()}).Error; err != nil {
				return nil, err
			}
		}
		if balance.RemainingQuota < 0 || remaining.IsNegative() {
			return nil, ErrCNConflict
		}
		balance.RemainingValue = remaining.String()
		if err = tx.Create(&balance).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if balance.PaidValue != amount.String() || balance.InitialQuota != initial {
		return nil, ErrCNConflict
	}
	return &balance, nil
}

func cnFundingValueTx(tx *gorm.DB, lotID, allocationID string, initial, target int64, amount decimal.Decimal) (decimal.Decimal, error) {
	if target < 0 {
		return decimal.Zero, ErrCNConflict
	}
	balance, err := cnFundingValueLotTx(tx, lotID, initial, amount)
	if err != nil {
		return decimal.Zero, err
	}
	remaining, err := cnDecimal(balance.RemainingValue)
	if err != nil {
		return decimal.Zero, err
	}
	var allocation CNFundingValueAllocation
	err = lockForUpdate(tx).Where("allocation_id = ?", allocationID).First(&allocation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		allocation = CNFundingValueAllocation{AllocationID: allocationID, LotID: lotID, Value: "0"}
		if err = tx.Create(&allocation).Error; err != nil {
			return decimal.Zero, err
		}
	} else if err != nil {
		return decimal.Zero, err
	}
	prior, err := cnDecimal(allocation.Value)
	if err != nil {
		return decimal.Zero, err
	}
	delta := target - allocation.Quota
	value := prior
	if delta > 0 {
		if delta > balance.RemainingQuota {
			return decimal.Zero, ErrCNConflict
		}
		share := cnDivideHalfEven(amount.Mul(decimal.NewFromInt(delta)), decimal.NewFromInt(initial))
		if delta == balance.RemainingQuota || share.GreaterThan(remaining) {
			share = remaining
		}
		value = prior.Add(share)
	} else if delta < 0 {
		// Refund the frozen allocation value, including its residual, exactly.
		if target < 0 || allocation.Quota <= 0 {
			return decimal.Zero, ErrCNConflict
		}
		value = cnDivideHalfEven(prior.Mul(decimal.NewFromInt(target)), decimal.NewFromInt(allocation.Quota))
	}
	remaining = remaining.Sub(value.Sub(prior))
	if remaining.IsNegative() || remaining.GreaterThan(amount) {
		return decimal.Zero, ErrCNConflict
	}
	if err = tx.Model(&CNFundingValueLot{}).Where("lot_id = ?", lotID).Updates(map[string]any{"remaining_quota": balance.RemainingQuota - delta, "remaining_value": remaining.String()}).Error; err != nil {
		return decimal.Zero, err
	}
	err = tx.Model(&CNFundingValueAllocation{}).Where("allocation_id = ?", allocationID).Updates(map[string]any{"quota": target, "value": value.String()}).Error
	if err != nil {
		return decimal.Zero, err
	}
	balance.RemainingQuota -= delta
	balance.RemainingValue = remaining.String()
	return value, cnValidateFundingValueTx(tx, balance)
}

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
	receipt := CNFinanceReceipt{EventID: event.EventID, OccurredAt: time.Now().UTC().Format(CNTimeLayout)}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
		return err
	}
	if err := tx.Where("event_id = ?", event.EventID).First(&receipt).Error; err != nil {
		return err
	}
	occurred, err := time.Parse(CNTimeLayout, receipt.OccurredAt)
	if err != nil {
		return err
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
		if err := lockForUpdate(tx).First(&lot, allocation.LotID).Error; err != nil {
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
		if purchase.PaymentStatus != "success" {
			continue
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
		f := cnBaseFact("funding_allocation", allocationID, "0", event.OperationID, "agency_wallet", lot.UserID, occurred, evidence)
		f.EffectiveAt = time.UnixMilli(event.OccurredAtMS).UTC().Format(CNTimeLayout)
		f.SourcePeriod = f.EffectiveAt[:7]
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
			if p.Revision > f.Revision {
				return ErrCNConflict
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
			value, err := cnFundingValueTx(tx, lotID, allocationID, lot.PaidInitial, paid, amount)
			if err != nil {
				return err
			}
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
		f.Funding = map[string]any{"allocationId": allocationID, "lotId": lotID, "chargeEconomicId": event.FinancialChargeID, "chargeSegmentId": strconv.Itoa(event.SegmentNo), "allocatedQuota": quota, "recognizedValue": f.Cumulative.Money, "policyVersion": "agency-native-purchase-residual-v2", "authority": "native", "previousAllocationFactId": f.PreviousFactID, "restoresAllocationFactId": restores, "evidence": f.SourceEvidence}
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
