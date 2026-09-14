package model

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// TopupPaymentSnapshot contains only facts from a verified successful provider
// response. ActualMoney is a decimal amount in major currency units, not quota
// or an estimated checkout price. Missing historical evidence stays unknown.
type TopupPaymentSnapshot struct {
	ActualMoney      string `json:"actual_money"`
	CurrencyCode     string `json:"currency_code"`
	PaymentReference string `json:"payment_reference"`
}

// TopupQuotaConversion records the calculation actually used by this provider.
// It intentionally contains no inferred exchange rate or paid-money ratio.
type TopupQuotaConversion struct {
	SchemaVersion int    `json:"schema_version"`
	BasisField    string `json:"basis_field"`
	BasisValue    string `json:"basis_value"`
	Calculation   string `json:"calculation"`
	QuotaPerUnit  string `json:"quota_per_unit,omitempty"`
	CreditedQuota string `json:"credited_quota"`
}

type TopupFundingSnapshot struct {
	Payment    *TopupPaymentSnapshot `json:"payment,omitempty"`
	Conversion *TopupQuotaConversion `json:"conversion,omitempty"`
	// InitiatedByUserID is internal provenance for administrator-assisted
	// payments. It is never copied into provider evidence or exposed to the
	// payment gateway.
	InitiatedByUserID int64 `json:"-"`
}

var ErrTopupPaymentSnapshot = errors.New("invalid top-up payment snapshot")

func (snapshot TopupPaymentSnapshot) normalized() (TopupPaymentSnapshot, error) {
	snapshot.ActualMoney = strings.TrimSpace(snapshot.ActualMoney)
	snapshot.CurrencyCode = strings.ToUpper(strings.TrimSpace(snapshot.CurrencyCode))
	snapshot.PaymentReference = strings.TrimSpace(snapshot.PaymentReference)
	if len(snapshot.ActualMoney) > 64 || (snapshot.CurrencyCode != "" && len(snapshot.CurrencyCode) != 3) || len(snapshot.PaymentReference) == 0 || len(snapshot.PaymentReference) > 191 {
		return TopupPaymentSnapshot{}, ErrTopupPaymentSnapshot
	}
	for _, char := range snapshot.CurrencyCode {
		if char < 'A' || char > 'Z' {
			return TopupPaymentSnapshot{}, ErrTopupPaymentSnapshot
		}
	}
	if snapshot.ActualMoney == "" {
		// A provider reference can be authoritative even when its callback
		// does not establish the payer's amount or currency semantics.
		return snapshot, nil
	}
	if snapshot.CurrencyCode == "" {
		return TopupPaymentSnapshot{}, ErrTopupPaymentSnapshot
	}
	// Reject exponent notation before decimal parsing so untrusted exponents
	// cannot allocate arbitrary memory during String conversion.
	for _, char := range snapshot.ActualMoney {
		if (char < '0' || char > '9') && char != '.' {
			return TopupPaymentSnapshot{}, ErrTopupPaymentSnapshot
		}
	}
	amount, err := decimal.NewFromString(snapshot.ActualMoney)
	if err != nil || amount.IsNegative() {
		return TopupPaymentSnapshot{}, ErrTopupPaymentSnapshot
	}
	snapshot.ActualMoney = amount.String()
	return snapshot, nil
}

// applyPaymentSnapshot compares callback evidence before a completed order can
// take its idempotent return. It cannot enrich or overwrite historical receipts.
func (topUp *TopUp) applyPaymentSnapshot(snapshots []*TopupPaymentSnapshot) error {
	if len(snapshots) > 1 {
		return ErrTopupPaymentSnapshot
	}
	if len(snapshots) == 0 || snapshots[0] == nil {
		return nil
	}
	if topUp.Status == common.TopUpStatusSuccess && topUp.PaymentSnapshot == "" {
		// Pre-upgrade/manual completed orders have no payment receipt to
		// compare. Acknowledge their existing result without inventing history
		// or turning a manual completion into verified paid funding.
		return nil
	}
	normalized, err := snapshots[0].normalized()
	if err != nil {
		return err
	}
	encoded, err := common.Marshal(normalized)
	if err != nil {
		return err
	}
	if topUp.PaymentSnapshot != "" {
		if topUp.PaymentSnapshot != string(encoded) {
			return ErrAgencyTopupConflict
		}
		return nil
	}
	topUp.PaymentSnapshot = string(encoded)
	return nil
}

func (topUp *TopUp) fundingSnapshot(creditedQuota int) (*TopupFundingSnapshot, error) {
	snapshot := &TopupFundingSnapshot{}
	if topUp.PaymentSnapshot != "" {
		snapshot.Payment = &TopupPaymentSnapshot{}
		if err := common.UnmarshalJsonStr(topUp.PaymentSnapshot, snapshot.Payment); err != nil {
			return nil, err
		}
	}
	conversion := TopupQuotaConversion{SchemaVersion: 1, BasisField: "topup.amount", BasisValue: strconv.FormatInt(topUp.Amount, 10), Calculation: "multiply_quota_per_unit", QuotaPerUnit: decimal.NewFromFloat(common.QuotaPerUnit).String(), CreditedQuota: strconv.Itoa(creditedQuota)}
	if topUp.CreditedQuota > 0 && strings.TrimSpace(topUp.MoneyDecimal) != "" {
		conversion.BasisField = "topup.money_decimal"
		conversion.BasisValue = topUp.MoneyDecimal
		conversion.Calculation = "assisted_exact_quote"
		conversion.QuotaPerUnit = ""
	}
	switch topUp.PaymentProvider {
	case PaymentProviderStripe:
		conversion.BasisField = "topup.money"
		conversion.BasisValue = decimal.NewFromFloat(topUp.Money).String()
	case PaymentProviderCreem:
		conversion.Calculation = "identity"
		conversion.QuotaPerUnit = ""
	}
	snapshot.Conversion = &conversion
	encoded, err := common.Marshal(conversion)
	if err != nil {
		return nil, err
	}
	topUp.QuotaConversionSnapshot = string(encoded)
	return snapshot, nil
}
