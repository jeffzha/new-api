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
	OptionKey             = "seedance_video_pricing.prices_cny"
	StandardSeedanceModel = "doubao-seedance-2-0-260128"
	FastSeedanceModel     = "doubao-seedance-2-0-fast-260128"
	Seedance25Model       = "doubao-seedance-2-5-260628"
	MaxPriceCNYPerMillion = 1_000_000.0

	WithoutVideoKey = "without_video"
	WithVideoKey    = "with_video"
)

type PricesCNY map[string]map[string]map[string]float64

type Setting struct {
	PricesCNY PricesCNY `json:"prices_cny"`
}

var requiredResolutions = map[string][]string{
	StandardSeedanceModel: {"720p", "1080p", "4k"},
	FastSeedanceModel:     {"default"},
	Seedance25Model:       {"720p", "1080p"},
}

var defaultPricesCNY = PricesCNY{
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

var seedanceVideoPricing = Setting{
	PricesCNY: clonePrices(defaultPricesCNY),
}

type priceIndex struct {
	prices map[string]decimal.Decimal
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
	if len(prices) != len(requiredResolutions) {
		return fmt.Errorf("Seedance video CNY prices must configure exactly %d supported models", len(requiredResolutions))
	}
	for modelName := range prices {
		if _, ok := requiredResolutions[modelName]; !ok {
			return fmt.Errorf("unsupported Seedance CNY-priced model %q", modelName)
		}
	}
	for modelName, resolutions := range requiredResolutions {
		modelPrices, ok := prices[modelName]
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
	// Deployments upgraded from the Seedance 2.0-only schema must retain all
	// customized 2.0 prices. Add only the new 2.5 defaults in memory; future
	// saves from the settings page write the complete three-model matrix.
	if len(seedanceVideoPricing.PricesCNY) == 2 {
		_, hasStandard := seedanceVideoPricing.PricesCNY[StandardSeedanceModel]
		_, hasFast := seedanceVideoPricing.PricesCNY[FastSeedanceModel]
		_, hasSeedance25 := seedanceVideoPricing.PricesCNY[Seedance25Model]
		if hasStandard && hasFast && !hasSeedance25 {
			prices := clonePrices(seedanceVideoPricing.PricesCNY)
			prices[Seedance25Model] = clonePrices(defaultPricesCNY)[Seedance25Model]
			seedanceVideoPricing.PricesCNY = prices
		}
	}
	if err := ValidatePricesCNY(seedanceVideoPricing.PricesCNY); err != nil {
		return err
	}
	next := &priceIndex{prices: make(map[string]decimal.Decimal, 8)}
	for modelName, resolutionPrices := range seedanceVideoPricing.PricesCNY {
		for resolution, inputPrices := range resolutionPrices {
			for inputType, price := range inputPrices {
				next.prices[priceIndexKey(modelName, resolution, inputType)] = decimal.NewFromFloat(price)
			}
		}
	}
	currentPriceIndex.Store(next)
	return nil
}

func SupportsModel(modelName string) bool {
	_, ok := requiredResolutions[strings.TrimSpace(modelName)]
	return ok
}

func GetUnitPriceCNY(modelName string, resolution string, hasVideo bool) (decimal.Decimal, bool) {
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
	if modelName == Seedance25Model {
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
