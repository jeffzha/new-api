package seedance_video_pricing

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/shopspring/decimal"
)

const (
	OptionKey              = "seedance_video_pricing.prices_cny"
	RequestOptionKey       = "seedance_video_pricing.request_prices_cny"
	StandardSeedanceModel  = "doubao-seedance-2-0-260128"
	FastSeedanceModel      = "doubao-seedance-2-0-fast-260128"
	Seedance25Model        = "doubao-seedance-2-5-260628"
	AimodelSeedance20Model = "doubao-seedance-2.0"
	AimodelSeedance25Model = "doubao-seedance-2.5"
	MiniSeedanceModel      = "doubao-seedance-2.0-mini"
	MaxPriceCNYPerMillion  = 1_000_000.0

	WithoutVideoKey = "without_video"
	WithVideoKey    = "with_video"
)

type PricesCNY map[string]map[string]map[string]float64

type Setting struct {
	PricesCNY        PricesCNY                     `json:"prices_cny"`
	RequestPricesCNY map[string]map[string]float64 `json:"request_prices_cny"`
}

var requiredResolutions = map[string][]string{
	StandardSeedanceModel:  {"720p", "1080p", "4k"},
	FastSeedanceModel:      {"default"},
	Seedance25Model:        {"720p", "1080p"},
	AimodelSeedance20Model: {"720p", "1080p", "4k"},
	AimodelSeedance25Model: {"720p", "1080p"},
	MiniSeedanceModel:      {"720p"},
}

var defaultPricesCNY = func() PricesCNY {
	prices := PricesCNY{
		StandardSeedanceModel: {
			"720p": {
				WithoutVideoKey: 46,
				WithVideoKey:    28,
			},
			"1080p": {
				WithoutVideoKey: 51,
				WithVideoKey:    31,
			},
			"4k": {
				WithoutVideoKey: 26,
				WithVideoKey:    16,
			},
		},
		FastSeedanceModel: {
			"default": {
				WithoutVideoKey: 37,
				WithVideoKey:    22,
			},
		},
		Seedance25Model: {
			// The official 480P and 720P tiers have the same list price, so
			// 480P is normalized to this tier during billing.
			"720p": {
				WithoutVideoKey: 70,
				WithVideoKey:    42,
			},
			// The console's temporary 72%-discounted prices are 55.44/33.12.
			// Store the restored official list prices: 55.44/0.72=77 and
			// 33.12/0.72=46 CNY per million tokens.
			"1080p": {
				WithoutVideoKey: 77,
				WithVideoKey:    46,
			},
		},
	}
	// aimodel uses the vendor's dotted model IDs. Keep their public pricing
	// aligned with the equivalent dated Seedance models.
	prices[AimodelSeedance20Model] = cloneModelPrices(prices[StandardSeedanceModel])
	prices[AimodelSeedance25Model] = cloneModelPrices(prices[Seedance25Model])
	return prices
}()

var seedanceVideoPricing = Setting{
	PricesCNY:        clonePrices(defaultPricesCNY),
	RequestPricesCNY: DefaultRequestPricesCNY(),
}

// Request prices have a separate option so legacy token rates are never
// reinterpreted as per-task prices during an upgrade.
func DefaultRequestPricesCNY() map[string]map[string]float64 {
	return map[string]map[string]float64{
		MiniSeedanceModel: {WithoutVideoKey: 23, WithVideoKey: 14},
	}
}

func ValidateRequestPricesCNYJSON(raw string) error {
	var prices map[string]map[string]float64
	if err := common.UnmarshalJsonStr(raw, &prices); err != nil {
		return fmt.Errorf("parse Seedance per-request CNY prices: %w", err)
	}
	if len(prices) != 1 || len(prices[MiniSeedanceModel]) != 2 {
		return fmt.Errorf("per-request prices must configure only %s with without_video and with_video", MiniSeedanceModel)
	}
	for _, key := range []string{WithoutVideoKey, WithVideoKey} {
		price := prices[MiniSeedanceModel][key]
		if price <= 0 || price > MaxPriceCNYPerMillion || math.IsNaN(price) || math.IsInf(price, 0) {
			return fmt.Errorf("per-request price %s must be positive, finite, and no greater than %.0f", key, MaxPriceCNYPerMillion)
		}
	}
	return nil
}

type priceIndex struct {
	prices        map[string]decimal.Decimal
	requestPrices map[string]decimal.Decimal
}

var currentPriceIndex atomic.Pointer[priceIndex]

func init() {
	config.GlobalConfig.Register("seedance_video_pricing", &seedanceVideoPricing)
	if err := RebuildPriceIndex(); err != nil {
		panic(err)
	}
}

func ValidatePricesCNYJSON(raw string) error {
	var prices PricesCNY
	if err := common.Unmarshal([]byte(raw), &prices); err != nil {
		return fmt.Errorf("parse Seedance video CNY prices: %w", err)
	}
	return ValidatePricesCNY(prices)
}

func ValidatePricesCNY(prices PricesCNY) error {
	for modelName := range prices {
		if _, ok := requiredResolutions[modelName]; !ok {
			return fmt.Errorf("unsupported Seedance CNY-priced model %q", modelName)
		}
	}
	for modelName, resolutions := range requiredResolutions {
		modelPrices, ok := prices[modelName]
		// Accept an optional legacy Mini token matrix without using it for new tasks.
		if IsPerRequest(modelName) && !ok {
			continue
		}
		if !ok {
			return fmt.Errorf("Seedance video CNY prices are missing model %q", modelName)
		}
		if len(modelPrices) != len(resolutions) {
			return fmt.Errorf("model %q must configure exactly %d resolution tiers", modelName, len(resolutions))
		}
		for _, resolution := range resolutions {
			tier, ok := modelPrices[resolution]
			if !ok {
				return fmt.Errorf("model %q is missing resolution tier %q", modelName, resolution)
			}
			if len(tier) != 2 {
				return fmt.Errorf("model %q resolution %q must contain only %q and %q", modelName, resolution, WithoutVideoKey, WithVideoKey)
			}
			for _, inputType := range []string{WithoutVideoKey, WithVideoKey} {
				price, ok := tier[inputType]
				if !ok {
					return fmt.Errorf("model %q resolution %q is missing %q", modelName, resolution, inputType)
				}
				if price <= 0 || price > MaxPriceCNYPerMillion || math.IsNaN(price) || math.IsInf(price, 0) {
					return fmt.Errorf("model %q resolution %q price %q must be positive, finite, and no greater than %.0f", modelName, resolution, inputType, MaxPriceCNYPerMillion)
				}
			}
		}
	}
	return nil
}

func RebuildPriceIndex() error {
	// Preserve all existing custom prices during schema upgrades and add only
	// newly supported model matrices from defaults.
	prices := clonePrices(seedanceVideoPricing.PricesCNY)
	defaults := clonePrices(defaultPricesCNY)
	for modelName := range requiredResolutions {
		if IsPerRequest(modelName) {
			continue
		}
		if _, ok := prices[modelName]; !ok {
			prices[modelName] = defaults[modelName]
		}
	}
	seedanceVideoPricing.PricesCNY = prices
	if err := ValidatePricesCNY(seedanceVideoPricing.PricesCNY); err != nil {
		return err
	}
	next := &priceIndex{prices: make(map[string]decimal.Decimal, 8)}
	for modelName, resolutionPrices := range seedanceVideoPricing.PricesCNY {
		if IsPerRequest(modelName) {
			continue
		}
		for resolution, inputPrices := range resolutionPrices {
			for inputType, price := range inputPrices {
				next.prices[priceIndexKey(modelName, resolution, inputType)] = decimal.NewFromFloat(price)
			}
		}
	}
	requestJSON, err := common.Marshal(seedanceVideoPricing.RequestPricesCNY)
	if err != nil {
		return err
	}
	if err := ValidateRequestPricesCNYJSON(string(requestJSON)); err != nil {
		return err
	}
	next.requestPrices = make(map[string]decimal.Decimal, 2)
	for inputType, price := range seedanceVideoPricing.RequestPricesCNY[MiniSeedanceModel] {
		next.requestPrices[inputType] = decimal.NewFromFloat(price)
	}
	currentPriceIndex.Store(next)
	return nil
}

func IsPerRequest(modelName string) bool {
	return strings.TrimSpace(modelName) == MiniSeedanceModel
}

func GetRequestPriceCNY(modelName, resolution string, hasVideo bool) (decimal.Decimal, bool) {
	if !IsPerRequest(modelName) {
		return decimal.Zero, false
	}
	if _, ok := NormalizeResolution(modelName, resolution); !ok {
		return decimal.Zero, false
	}
	index := currentPriceIndex.Load()
	if index == nil {
		return decimal.Zero, false
	}
	key := WithoutVideoKey
	if hasVideo {
		key = WithVideoKey
	}
	price, ok := index.requestPrices[key]
	return price, ok
}

func SupportsModel(modelName string) bool {
	_, ok := requiredResolutions[strings.TrimSpace(modelName)]
	return ok
}

func GetUnitPriceCNY(modelName string, resolution string, hasVideo bool) (decimal.Decimal, bool) {
	if IsPerRequest(modelName) {
		return decimal.Zero, false
	}
	modelName = strings.TrimSpace(modelName)
	resolution, ok := NormalizeResolution(modelName, resolution)
	if !ok {
		return decimal.Zero, false
	}
	inputType := WithoutVideoKey
	if hasVideo {
		inputType = WithVideoKey
	}
	index := currentPriceIndex.Load()
	if index == nil {
		return decimal.Zero, false
	}
	price, ok := index.prices[priceIndexKey(modelName, resolution, inputType)]
	return price, ok
}

func NormalizeResolution(modelName string, resolution string) (string, bool) {
	modelName = strings.TrimSpace(modelName)
	if !SupportsModel(modelName) {
		return "", false
	}
	if modelName == FastSeedanceModel {
		return "default", true
	}
	if modelName == MiniSeedanceModel {
		switch strings.ToLower(strings.TrimSpace(resolution)) {
		case "", "480p", "720p":
			return "720p", true
		default:
			return "", false
		}
	}
	if modelName == Seedance25Model || modelName == AimodelSeedance25Model {
		switch strings.ToLower(strings.TrimSpace(resolution)) {
		case "", "480p", "720p":
			return "720p", true
		case "1080p":
			return "1080p", true
		default:
			return "", false
		}
	}
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "1080p":
		return "1080p", true
	case "4k":
		return "4k", true
	default:
		return "720p", true
	}
}

func DefaultPricesCNY() PricesCNY {
	return clonePrices(defaultPricesCNY)
}

func priceIndexKey(modelName string, resolution string, inputType string) string {
	return modelName + "\x00" + resolution + "\x00" + inputType
}

func clonePrices(source PricesCNY) PricesCNY {
	cloned := make(PricesCNY, len(source))
	for modelName, resolutions := range source {
		clonedResolutions := make(map[string]map[string]float64, len(resolutions))
		for resolution, inputPrices := range resolutions {
			clonedInputPrices := make(map[string]float64, len(inputPrices))
			for inputType, price := range inputPrices {
				clonedInputPrices[inputType] = price
			}
			clonedResolutions[resolution] = clonedInputPrices
		}
		cloned[modelName] = clonedResolutions
	}
	return cloned
}

func cloneModelPrices(source map[string]map[string]float64) map[string]map[string]float64 {
	return clonePrices(PricesCNY{"model": source})["model"]
}
