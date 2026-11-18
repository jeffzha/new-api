// Package modelmarketplace contains the public, display-only model catalog contract.
package modelmarketplace

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service/businessapi"
)

type Query struct {
	Page      int
	PageSize  int
	Search    string
	ModelType string
	Vendor    string
}

func ParseQuery(values url.Values, detail bool) (Query, error) {
	q := Query{Page: 1, PageSize: 20}
	for key, items := range values {
		if len(items) != 1 || strings.TrimSpace(items[0]) == "" {
			return q, fmt.Errorf("%s must have one non-empty value", key)
		}
		value := strings.TrimSpace(items[0])
		if detail {
			if key != "model" || len(value) > 256 {
				return q, fmt.Errorf("detail requires only model")
			}
			continue
		}
		switch key {
		case "page", "page_size":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || (key == "page" && n > 1000000) || (key == "page_size" && n > 100) {
				return q, fmt.Errorf("invalid %s", key)
			}
			if key == "page" {
				q.Page = n
			} else {
				q.PageSize = n
			}
		case "q":
			if len(value) > 200 {
				return q, fmt.Errorf("q is too long")
			}
			q.Search = strings.ToLower(value)
		case "model_type":
			q.ModelType = strings.ToLower(value)
			if !businessapi.ValidModelType(q.ModelType) {
				return q, fmt.Errorf("invalid model_type")
			}
		case "vendor":
			if len(value) > 200 {
				return q, fmt.Errorf("vendor is too long")
			}
			q.Vendor = strings.ToLower(value)
		default:
			return q, fmt.Errorf("unknown parameter: %s", key)
		}
	}
	if detail && values.Get("model") == "" {
		return q, fmt.Errorf("model is required")
	}
	return q, nil
}

type Endpoint struct {
	Type constant.EndpointType `json:"type"`
	common.EndpointInfo
}

// Price is the public square's base-price snapshot, never a customer discount.
// Config retains complex tiers and usage schemas without flattening them to a misleading headline price.
type Price struct {
	Scope              string      `json:"scope"`
	Source             string      `json:"source"`
	Status             string      `json:"status"`
	Currency           string      `json:"currency"`
	USDExchangeRate    float64     `json:"usd_exchange_rate"`
	RatioUSDPerMillion float64     `json:"ratio_usd_per_million"`
	Version            string      `json:"version,omitempty"`
	Stale              bool        `json:"stale"`
	Config             *PriceRules `json:"config,omitempty"`
	DisplayRows        any         `json:"display_rows,omitempty"`
}

// PriceRules deliberately lists display fields. Adding a private field to the
// internal model.Pricing can never silently expose it through this API.
type PriceRules struct {
	QuotaType              int                                  `json:"quota_type"`
	ModelRatio             float64                              `json:"model_ratio"`
	ModelPrice             float64                              `json:"model_price"`
	CompletionRatio        float64                              `json:"completion_ratio"`
	CacheRatio             *float64                             `json:"cache_ratio,omitempty"`
	CreateCacheRatio       *float64                             `json:"create_cache_ratio,omitempty"`
	ImageRatio             *float64                             `json:"image_ratio,omitempty"`
	AudioRatio             *float64                             `json:"audio_ratio,omitempty"`
	AudioCompletionRatio   *float64                             `json:"audio_completion_ratio,omitempty"`
	BillingMode            string                               `json:"billing_mode,omitempty"`
	BillingExpr            string                               `json:"billing_expr,omitempty"`
	BillingUsageSchema     map[string]jsplugin.UsageFieldSchema `json:"billing_usage_schema,omitempty"`
	BillingUsageExamples   []jsplugin.UsageExample              `json:"billing_usage_examples,omitempty"`
	BillingMaterialPricing *jsplugin.MaterialPricingCatalog     `json:"billing_material_pricing,omitempty"`
	BillingPluginVariants  []PriceVariant                       `json:"billing_plugin_variants,omitempty"`
	ProviderPricing        *model.ProviderPricing               `json:"provider_pricing,omitempty"`
}

type PriceVariant struct {
	Name  string      `json:"name"`
	Rules *PriceRules `json:"rules"`
}

// PriceProjection is installed once by an optional integration, and reads memory only.
// Product deployments use local pricing; project deployments can project the existing OPS snapshot.
var PriceProjection func([]model.Pricing, bool) map[string]Price

type Item struct {
	ID          string              `json:"id"`
	Object      string              `json:"object"`
	DisplayName string              `json:"display_name"`
	ModelType   string              `json:"model_type"`
	Description string              `json:"description"`
	Icon        string              `json:"icon"`
	Tags        []string            `json:"tags"`
	Vendor      model.PricingVendor `json:"vendor"`
	Endpoints   []Endpoint          `json:"endpoints"`
	Pricing     Price               `json:"pricing"`
}

type Page struct {
	Success  bool   `json:"success"`
	Object   string `json:"object"`
	Data     []Item `json:"data"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int    `json:"total"`
	HasMore  bool   `json:"has_more"`
	Facets   Facets `json:"facets"`
}

type Facets struct {
	ModelTypes map[string]int `json:"model_types"`
	Vendors    map[string]int `json:"vendors"`
}

func Paginate(items []Item, q Query) Page {
	filtered := make([]Item, 0, len(items))
	facets := Facets{ModelTypes: map[string]int{}, Vendors: map[string]int{}}
	for _, item := range items {
		facets.ModelTypes[item.ModelType]++
		facets.Vendors[item.Vendor.Name]++
		if q.ModelType != "" && item.ModelType != q.ModelType {
			continue
		}
		if q.Vendor != "" && strings.ToLower(item.Vendor.Name) != q.Vendor {
			continue
		}
		haystack := strings.ToLower(item.ID + " " + item.Description + " " + strings.Join(item.Tags, " "))
		if q.Search != "" && !strings.Contains(haystack, q.Search) {
			continue
		}
		filtered = append(filtered, item)
	}
	slices.SortFunc(filtered, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
	total := len(filtered)
	start := min((q.Page-1)*q.PageSize, total)
	end := min(start+q.PageSize, total)
	return Page{Success: true, Object: "list", Data: filtered[start:end], Page: q.Page, PageSize: q.PageSize, Total: total, HasMore: end < total, Facets: facets}
}

func Tags(raw string) []string {
	tags := []string{}
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		if common.Unmarshal([]byte(raw), &tags) == nil {
			if tags == nil {
				return []string{}
			}
			return tags
		}
	}
	for tag := range strings.SplitSeq(raw, ",") {
		if tag = strings.TrimSpace(tag); tag != "" && !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

func PublicPrice(p model.Pricing, detail bool) *PriceRules {
	rules := &PriceRules{QuotaType: p.QuotaType, ModelRatio: p.ModelRatio, ModelPrice: p.ModelPrice, CompletionRatio: p.CompletionRatio, CacheRatio: p.CacheRatio, CreateCacheRatio: p.CreateCacheRatio, ImageRatio: p.ImageRatio, AudioRatio: p.AudioRatio, AudioCompletionRatio: p.AudioCompletionRatio, BillingMode: p.BillingMode, BillingExpr: p.BillingExpr, BillingUsageSchema: p.BillingUsageSchema, ProviderPricing: p.ProviderPricing}
	if detail {
		rules.BillingUsageExamples = p.BillingUsageExamples
		rules.BillingMaterialPricing = p.BillingMaterialPricing
		for _, variant := range p.BillingPluginVariants {
			rules.BillingPluginVariants = append(rules.BillingPluginVariants, PriceVariant{Name: variant.PluginName, Rules: &PriceRules{BillingMode: variant.BillingMode, BillingExpr: variant.BillingExpr, BillingUsageSchema: variant.BillingUsageSchema, BillingUsageExamples: variant.BillingUsageExamples, BillingMaterialPricing: variant.BillingMaterialPricing}})
		}
	}
	return rules
}
