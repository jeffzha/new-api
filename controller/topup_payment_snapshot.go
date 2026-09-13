package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
	"golang.org/x/text/currency"
)

// paymentSnapshotFromMinorUnits converts signed-provider minor-unit amounts
// without floating point. Missing/unknown currency metadata stays unknown;
// checkout prices and the live payment configuration are never substitutes.
func paymentSnapshotFromMinorUnits(amount, currencyCode, reference, provider string) *model.TopupPaymentSnapshot {
	if strings.TrimSpace(reference) == "" {
		return nil
	}
	snapshot := &model.TopupPaymentSnapshot{PaymentReference: reference}
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	unit, err := currency.ParseISO(currencyCode)
	if err != nil || currencyCode == "XXX" {
		return snapshot
	}
	snapshot.CurrencyCode = currencyCode
	amount = strings.TrimSpace(amount)
	if amount == "" || len(amount) > 32 {
		return snapshot
	}
	for _, char := range amount {
		if char < '0' || char > '9' {
			return snapshot
		}
	}
	minor, err := decimal.NewFromString(amount)
	if err != nil {
		return snapshot
	}
	digits, _ := currency.Standard.Rounding(unit)
	// Stripe charges ISK/UGX using two-decimal API amounts even though the
	// ISO currency definitions have zero fractional digits.
	if provider == model.PaymentProviderStripe && (currencyCode == "ISK" || currencyCode == "UGX") {
		digits = 2
	}
	snapshot.ActualMoney = minor.Shift(-int32(digits)).String()
	return snapshot
}
