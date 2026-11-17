package businessapi

import (
	"strconv"

	"github.com/QuantumNous/new-api/model"
)

type Pricing struct {
	SalesMultiplier *float64 `json:"sales_multiplier"`
	Status          string   `json:"status"`
	Version         *string  `json:"version"`
}

// PlatformPricing deliberately ignores account, group and distributor overrides.
// One committed local snapshot is loaded per query; no remote OPS call is made.
func PlatformPricing(names []string) (map[string]Pricing, error) {
	policy, err := model.LoadAgencyPlatformPolicy(model.DB)
	if err != nil {
		return nil, err
	}
	prices := make(map[string]Pricing, len(names))
	for _, name := range names {
		prices[name] = Pricing{Status: "unavailable"}
	}
	if policy.Revision <= 0 {
		return prices, nil
	}
	version := strconv.FormatInt(policy.Revision, 10)
	for _, price := range policy.ModelPrices {
		if _, visible := prices[price.OriginModelName]; !visible || !price.IsPricingConfirmed() {
			continue
		}
		multiplier := float64(price.DefaultSalesBPS) / 10000
		prices[price.OriginModelName] = Pricing{SalesMultiplier: &multiplier, Status: "active", Version: &version}
	}
	return prices, nil
}
