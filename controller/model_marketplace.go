package controller

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/businessapi"
	"github.com/QuantumNous/new-api/service/modelmarketplace"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func ModelMarketplace(c *gin.Context) {
	detail := strings.HasSuffix(c.FullPath(), "/detail")
	query, err := modelmarketplace.ParseQuery(c.Request.URL.Query(), detail)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": "invalid_catalog_query", "message": err.Error()}})
		return
	}
	visible, err := visibleOpenAIModels(c, true)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "catalog_unavailable", "message": "Model catalog is temporarily unavailable"}})
		return
	}
	prices := model.GetPricing()
	byName := make(map[string]model.Pricing, len(prices))
	for _, price := range prices {
		byName[price.ModelName] = price
	}
	vendors := make(map[int]model.PricingVendor)
	for _, vendor := range model.GetVendors() {
		vendors[vendor.ID] = vendor
	}
	endpoints := model.GetSupportedEndpointMap()
	items := make([]modelmarketplace.Item, 0, len(visible))
	for _, entry := range visible {
		if detail && entry.Id != c.Query("model") {
			continue
		}
		p, hasPrice := byName[entry.Id]
		vendor, ok := vendors[p.VendorID]
		if !ok {
			vendor.Name = entry.OwnedBy
		}
		item := modelmarketplace.Item{ID: entry.Id, Object: "model", DisplayName: entry.Id, ModelType: businessapi.ModelType(entry.Id, entry.SupportedEndpointTypes), Description: p.Description, Icon: p.Icon, Tags: modelmarketplace.Tags(p.Tags), Vendor: vendor, Endpoints: []modelmarketplace.Endpoint{}, Pricing: modelmarketplace.Price{Scope: "standard", Source: "gateway", Status: "unavailable", Currency: "USD"}}
		for _, et := range entry.SupportedEndpointTypes {
			// Only return Gateway-relative public paths. Custom upstream URLs are private.
			info, exists := endpoints[string(et)]
			if exists && strings.HasPrefix(info.Path, "/") && !strings.HasPrefix(info.Path, "//") {
				item.Endpoints = append(item.Endpoints, modelmarketplace.Endpoint{Type: et, EndpointInfo: info})
			}
		}
		slices.SortFunc(item.Endpoints, func(a, b modelmarketplace.Endpoint) int { return strings.Compare(string(a.Type), string(b.Type)) })
		if hasPrice && helper.HasModelBillingConfig(entry.Id) {
			item.Pricing = modelmarketplace.Price{Scope: "standard", Source: "gateway", Status: "available", Currency: "USD", Config: modelmarketplace.PublicPrice(p, detail)}
		}
		items = append(items, item)
	}
	page := modelmarketplace.Paginate(items, query)
	if detail {
		if len(items) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "model_not_found", "message": "Model is not available to this API key"}})
			return
		}
		page.Data = items
	}
	// Project only the selected page. Complex expressions and schemas are never
	// recompiled or fetched remotely, and unrelated models do not pay conversion cost.
	if modelmarketplace.PriceProjection != nil {
		selected := make([]model.Pricing, 0, len(page.Data))
		for _, item := range page.Data {
			if p, ok := byName[item.ID]; ok {
				selected = append(selected, p)
			}
		}
		projected := modelmarketplace.PriceProjection(selected, detail)
		for i := range page.Data {
			if price, ok := projected[page.Data[i].ID]; ok {
				page.Data[i].Pricing = price
			}
		}
	}
	for i := range page.Data {
		page.Data[i].Pricing.USDExchangeRate = operation_setting.USDExchangeRate
		page.Data[i].Pricing.RatioUSDPerMillion = 1000000 / common.QuotaPerUnit
	}
	var response any = page
	if detail {
		response = gin.H{"success": true, "object": "model", "data": page.Data[0]}
	}
	body, err := common.Marshal(response)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
	// Authentication and caller filtering have already run, including on 304.
	c.Header("Cache-Control", "private, no-cache")
	c.Header("Vary", "Authorization, x-api-key, x-goog-api-key")
	c.Header("ETag", tag)
	c.Header("Access-Control-Expose-Headers", common.RequestIdKey+", ETag")
	for match := range strings.SplitSeq(c.GetHeader("If-None-Match"), ",") {
		if match = strings.TrimSpace(match); match == tag || match == "W/"+tag || match == "*" {
			c.Status(http.StatusNotModified)
			return
		}
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}
