package opspricing

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/shopspring/decimal"
)

type DisplayPrice struct {
	model.Pricing
	ListPrice *ListPrice `json:"list_price"`
}

type ListPrice struct {
	Rows       []DisplayRow   `json:"display_rows,omitempty"`
	Source     string         `json:"source"`
	Comparison string         `json:"comparison"`
	Official   map[string]any `json:"official_cny,omitempty"`
	Local      model.Pricing  `json:"local"`
}

// Display copies catalog entries. No model cache or pricing setting is mutated.
// Only officialCny is used; sale prices and customer coefficients are not decoded.
func (r *Reader) Display(local []model.Pricing, rate float64, quotaPerUnit float64) []DisplayPrice {
	current := r.current.Load()
	result := make([]DisplayPrice, 0, len(local))
	for _, item := range local {
		row := DisplayPrice{Pricing: item, ListPrice: &ListPrice{Source: "local", Comparison: "unavailable", Local: item}}
		if current == nil {
			result = append(result, row)
			continue
		}
		entries := current.byModel[item.ModelName]
		if len(entries) != 1 {
			if len(entries) > 1 {
				row.ListPrice.Comparison = "review"
			}
			result = append(result, row)
			continue
		}
		components := entries[0].officialAt(time.Now())
		if len(components) == 0 {
			result = append(result, row)
			continue
		}
		row.ListPrice.Official = components
		// Fail back to the existing display if the two instances use different
		// quota/currency conversion. Never compensate by changing global settings.
		if !samePrice(current.list.Station.USDExchangeRate, rate) || float64(current.list.Station.QuotaPerUnit) != quotaPerUnit {
			row.ListPrice.Comparison = "conversion_mismatch"
			result = append(result, row)
			continue
		}
		rows, err := displayRows(components)
		if err != nil {
			row.ListPrice.Comparison = "unsupported"
			result = append(result, row)
			continue
		}
		row.ListPrice.Rows = rows
		display, projectionErr := projectPrice(item, components, decimal.NewFromFloat(rate))
		if projectionErr != nil {
			// Rich OPS rules are rendered directly from validated rows. Do not
			// fabricate billing expressions for time periods or task conditions.
			row.ListPrice.Source = "ops"
			row.ListPrice.Comparison = "review"
			result = append(result, row)
			continue
		}
		row.Pricing = display
		row.ListPrice.Source = "ops"
		row.ListPrice.Comparison = comparePrice(item, display, components, rate, quotaPerUnit)
		result = append(result, row)
	}
	return result
}

func positivePrice(value any) (decimal.Decimal, error) {
	d, err := boundedPrice(value)
	if err != nil || !d.IsPositive() {
		return decimal.Zero, errors.New("invalid positive price")
	}
	return d, nil
}

// Limit decimal precision before arithmetic so an invalid exponent cannot
// allocate unbounded memory while comparing or formatting an external price.
func boundedPrice(value any) (decimal.Decimal, error) {
	text := fmt.Sprint(value)
	if len(text) > 64 {
		return decimal.Zero, errors.New("invalid list price")
	}
	d, err := decimal.NewFromString(text)
	if err != nil || d.Exponent() < -24 || d.Exponent() > 7 || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(10000000)) {
		return decimal.Zero, errors.New("invalid list price")
	}
	return d, nil
}

func samePrice(value any, number float64) bool {
	d, err := boundedPrice(value)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return false
	}
	return d.Sub(decimal.NewFromFloat(number)).Abs().LessThanOrEqual(decimal.NewFromFloat(0.000001))
}

var componentVariables = map[string]string{
	"input_per_million": "p", "output_per_million": "c", "cache_read_per_million": "cr", "cache_write_per_million": "cc",
	"per_image": `u("images")`, "per_second": `u("seconds")`, "per_reference_image": `u("input_images")`,
	"per_reference_video_second": `u("input_video_seconds")`, "per_million_tokens": `u("billed_tokens")`,
}

func priceTerms(components map[string]any, rate decimal.Decimal) (string, error) {
	keys := make([]string, 0, len(components))
	for key := range components {
		if key != "matrix" && key != "when" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	terms := make([]string, 0, len(keys))
	for _, key := range keys {
		price, err := boundedPrice(components[key])
		if err != nil {
			return "", errors.New("invalid list price")
		}
		usd := price.DivRound(rate, 24)
		if key == "per_request" {
			if len(keys) != 1 {
				return "", errors.New("mixed request price")
			}
			return "fixed(" + usd.String() + ")", nil
		}
		variable, ok := componentVariables[key]
		if !ok {
			return "", errors.New("unsupported list price component")
		}
		if key == "per_million_tokens" {
			terms = append(terms, variable+" * "+usd.String()+" / 1000000")
			continue
		}
		terms = append(terms, variable+" * "+usd.String())
	}
	if len(terms) == 0 {
		return "", errors.New("empty list price")
	}
	return strings.Join(terms, " + "), nil
}

func projectPrice(local model.Pricing, components map[string]any, rate decimal.Decimal) (model.Pricing, error) {
	defaults := make(map[string]any)
	for k, v := range components {
		if k != "matrix" {
			defaults[k] = v
		}
	}
	body, err := priceTerms(defaults, rate)
	if err != nil {
		return local, err
	}
	expression := `tier("default", ` + body + `)`
	conditionSchema := make(map[string]jsplugin.UsageFieldSchema)
	if raw, exists := components["matrix"]; exists {
		rows, ok := raw.([]any)
		if !ok || len(rows) > 200 {
			return local, errors.New("invalid list price dimensions")
		}
		type dimension struct {
			condition   string
			terms       string
			specificity int
		}
		dimensions := make([]dimension, 0, len(rows))
		for _, rawRow := range rows {
			row, ok := rawRow.(map[string]any)
			if !ok {
				return local, errors.New("invalid list price dimension")
			}
			when, ok := row["when"].(map[string]any)
			if !ok || len(when) == 0 {
				return local, errors.New("invalid list price condition")
			}
			keys := make([]string, 0, len(when))
			for k := range when {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			conditions := make([]string, 0, len(keys))
			for _, key := range keys {
				switch key {
				case "size", "quality", "resolution":
					value, ok := when[key].(string)
					if !ok || len(value) > 80 {
						return local, errors.New("invalid list price condition")
					}
					conditions = append(conditions, `u(`+strconv.Quote(key)+`) == `+strconv.Quote(value))
					field := conditionSchema[key]
					if !slices.Contains(field.Enum, value) {
						field.Enum = append(field.Enum, value)
					}
					conditionSchema[key] = field
				case "referenceVideo":
					return local, errors.New("reference video display requires an explicit usage schema")
				default:
					return local, errors.New("unsupported list price condition")
				}
			}
			merged := make(map[string]any, len(defaults)+len(row))
			for k, v := range defaults {
				merged[k] = v
			}
			for k, v := range row {
				if k != "when" {
					merged[k] = v
				}
			}
			terms, err := priceTerms(merged, rate)
			if err != nil {
				return local, err
			}
			dimensions = append(dimensions, dimension{strings.Join(conditions, " && "), terms, len(when)})
		}
		slices.SortStableFunc(dimensions, func(a, b dimension) int { return b.specificity - a.specificity })
		for i := len(dimensions) - 1; i >= 0; i-- {
			expression = dimensions[i].condition + ` ? tier("ops-` + strconv.Itoa(i) + `", ` + dimensions[i].terms + `) : ` + expression
		}
	}
	if _, err := billingexpr.CompileFromCache(expression); err != nil {
		return local, errors.New("invalid list price expression")
	}
	result := local
	result.BillingMode = "tiered_expr"
	result.BillingExpr = expression
	result.BillingPluginVariants = nil
	result.BillingMaterialPricing = nil
	result.ProviderPricing = nil
	result.ModelPrice = 0
	result.ModelRatio = 0
	result.CompletionRatio = 0
	result.CacheRatio = nil
	result.CreateCacheRatio = nil
	result.ImageRatio = nil
	result.AudioRatio = nil
	result.AudioCompletionRatio = nil
	result.BillingUsageSchema = map[string]jsplugin.UsageFieldSchema{
		"seconds":             {Type: "number", Unit: "second", Description: jsplugin.LocalizedText{"en": "Duration unit price", "zh": "时长单价"}},
		"images":              {Type: "number", Unit: "count", Description: jsplugin.LocalizedText{"en": "Image generation unit price", "zh": "图片生成单价"}},
		"input_images":        {Type: "number", Unit: "count", Description: jsplugin.LocalizedText{"en": "Reference image unit price", "zh": "参考图片单价"}},
		"input_video_seconds": {Type: "number", Unit: "second", Description: jsplugin.LocalizedText{"en": "Reference video unit price", "zh": "参考视频单价"}},
		"billed_tokens":       {Type: "number", Unit: "token", Description: jsplugin.LocalizedText{"en": "Video token unit price", "zh": "视频 Token 单价"}},
	}
	result.BillingUsageExamples = nil
	for field := range result.BillingUsageSchema {
		if !strings.Contains(expression, `u(`+strconv.Quote(field)+`)`) {
			delete(result.BillingUsageSchema, field)
		}
	}
	for field, schema := range conditionSchema {
		result.BillingUsageSchema[field] = schema
	}
	if len(defaults) == 1 && defaults["per_request"] != nil && components["matrix"] == nil {
		result.QuotaType = 1
	} else {
		result.QuotaType = 0
	}
	return result, nil
}

func comparePrice(local, display model.Pricing, official map[string]any, rate, quotaPerUnit float64) string {
	if len(local.BillingPluginVariants) > 0 || local.ProviderPricing != nil || local.BillingMaterialPricing != nil {
		return "review"
	}
	if local.BillingMode == "tiered_expr" {
		if strings.Join(strings.Fields(local.BillingExpr), "") == strings.Join(strings.Fields(display.BillingExpr), "") {
			return "match"
		}
		return "review"
	}
	// Decimal prices are compared without applying group or customer discounts.
	if local.AudioRatio != nil || local.AudioCompletionRatio != nil || local.ImageRatio != nil {
		return "review"
	}
	if official["matrix"] != nil {
		return "review"
	}
	localPrices := map[string]float64{}
	if local.QuotaType == 1 {
		localPrices["per_request"] = local.ModelPrice * rate
	} else {
		base := local.ModelRatio * 1000000 / quotaPerUnit * rate
		localPrices["input_per_million"] = base
		localPrices["output_per_million"] = base * local.CompletionRatio
		if local.CacheRatio != nil {
			localPrices["cache_read_per_million"] = base * (*local.CacheRatio)
		}
		if local.CreateCacheRatio != nil {
			localPrices["cache_write_per_million"] = base * (*local.CreateCacheRatio)
		}
	}
	if len(localPrices) != len(official) {
		return "differ"
	}
	for key, price := range localPrices {
		if !samePrice(official[key], price) {
			return "differ"
		}
	}
	return "match"
}
