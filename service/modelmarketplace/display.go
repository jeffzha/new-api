package modelmarketplace

import (
	"math"

	"github.com/QuantumNous/new-api/model"
)

// DisplayOptions describes transformations of public standard prices only.
// Raw rules retain their original currency, tiers and quantity units.
type DisplayOptions struct {
	DefaultPriceMode  string         `json:"default_price_mode"`
	PriceModes        []string       `json:"price_modes"`
	DefaultTokenUnit  string         `json:"default_token_unit"`
	TokenUnits        []string       `json:"token_units"`
	TokenUnitDivisors map[string]int `json:"token_unit_divisors"`
	RechargeAvailable bool           `json:"recharge_available"`
	RechargeFactor    *float64       `json:"recharge_factor"`
	SortOptions       []string       `json:"sort_options"`
}

func NewDisplayOptions(rechargeRate, exchangeRate float64) DisplayOptions {
	options := DisplayOptions{DefaultPriceMode: "standard", PriceModes: []string{"standard"}, DefaultTokenUnit: "1M", TokenUnits: []string{"1M", "1K"}, TokenUnitDivisors: map[string]int{"1M": 1, "1K": 1000}, SortOptions: []string{"name", "name_desc"}}
	factor := rechargeRate / exchangeRate
	if rechargeRate > 0 && exchangeRate > 0 && !math.IsNaN(factor) && !math.IsInf(factor, 0) && factor > 0 {
		options.RechargeAvailable = true
		options.RechargeFactor = &factor
		options.PriceModes = append(options.PriceModes, "recharge")
	}
	return options
}

// PricingType mirrors the model square's disjoint billing categories.
func PricingType(p model.Pricing) string {
	if len(p.BillingUsageSchema) > 0 {
		return "task"
	}
	if p.QuotaType == 1 {
		return "request"
	}
	return "token"
}
