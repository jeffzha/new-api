package opspricing

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/modelmarketplace"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func init() {
	// Register a display adapter independently from routing, billing and wallets.
	modelmarketplace.PriceProjection = marketplacePrices
}

func marketplacePrices(local []model.Pricing, detail bool) map[string]modelmarketplace.Price {
	result := make(map[string]modelmarketplace.Price, len(local))
	reader := Default
	if reader == nil {
		return result
	}
	status := reader.Status()
	for _, row := range reader.Display(local, operation_setting.USDExchangeRate, common.QuotaPerUnit) {
		price := modelmarketplace.Price{Scope: "standard", Source: "gateway", Status: "unavailable", Currency: "USD", Version: status.Version, Stale: status.Stale}
		if helper.HasModelBillingConfig(row.ModelName) {
			price.Status = "available"
			price.Config = modelmarketplace.PublicPrice(row.Pricing, detail)
		}
		if row.ListPrice != nil && row.ListPrice.Source == "ops" {
			// CNY rows are authoritative for display, including rules that cannot be
			// losslessly projected into the local expression format. Never mix them
			// with a local fallback price under the same currency.
			price.Source, price.Currency, price.Status = "ops", "CNY", "available"
			price.Config = nil
			price.DisplayRows = row.ListPrice.Rows
		}
		result[row.ModelName] = price
	}
	return result
}
