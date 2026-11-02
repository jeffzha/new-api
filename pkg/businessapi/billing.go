package businessapi

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const BillingKey = "business_api_billing"
const ConversionKey = "business_api_conversion"

// Billing is the customer charge after settlement, never a catalog price or
// the distributor's cost. Missing amounts remain null, including on failure.
type Billing struct {
	Status   string  `json:"status"`
	Amount   *string `json:"amount"`
	Currency string  `json:"currency"`
	Quota    *int64  `json:"quota"`
	Source   string  `json:"source"`
}

type Conversion struct {
	Currency     string
	QuotaPerUnit string
	ExchangeRate string
}

func UnavailableBilling() Billing {
	return Billing{Status: "unavailable", Source: "gateway"}
}

func SettledBilling(quota int64, conversion Conversion) (Billing, error) {
	unit, e1 := decimal.NewFromString(conversion.QuotaPerUnit)
	rate, e2 := decimal.NewFromString(conversion.ExchangeRate)
	if quota < 0 || e1 != nil || e2 != nil || !unit.IsPositive() || !rate.IsPositive() {
		return UnavailableBilling(), errors.New("invalid billing conversion")
	}
	result := Billing{Status: "settled", Quota: &quota, Source: "gateway", Currency: conversion.Currency}
	if conversion.Currency == "TOKENS" {
		// Quota is a billing credit unit, not a currency amount.
		return result, nil
	}
	if conversion.Currency != "USD" && conversion.Currency != "CNY" {
		return UnavailableBilling(), errors.New("unsupported billing currency")
	}
	numerator := decimal.NewFromInt(quota).Mul(rate)
	amount, remainder := numerator.QuoRem(unit, 18)
	step := decimal.New(1, -18)
	comparison := remainder.Mul(decimal.NewFromInt(2)).Cmp(unit.Mul(step))
	if comparison > 0 || (comparison == 0 && amount.Shift(18).BigInt().Bit(0) == 1) {
		amount = amount.Add(step)
	}
	text := amount.String()
	result.Amount = &text
	return result, nil
}

func BillingFromContext(c *gin.Context) (Billing, bool) {
	value, exists := c.Get(BillingKey)
	billing, ok := value.(Billing)
	return billing, exists && ok
}
