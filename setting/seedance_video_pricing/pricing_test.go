package seedance_video_pricing

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPricesUSDAreComplete(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		resolution string
		hasVideo   bool
		want       float64
	}{
		{name: "standard 720p", model: StandardSeedanceModel, resolution: "720p", want: 7},
		{name: "standard 480p falls back to 720p", model: StandardSeedanceModel, resolution: "480p", want: 7},
		{name: "standard empty falls back to 720p", model: StandardSeedanceModel, want: 7},
		{name: "standard unknown falls back to 720p", model: StandardSeedanceModel, resolution: "future-resolution", want: 7},
		{name: "standard 720p video", model: StandardSeedanceModel, resolution: "720p", hasVideo: true, want: 4.3},
		{name: "standard 1080p", model: StandardSeedanceModel, resolution: "1080p", want: 7.7},
		{name: "standard 1080p video", model: StandardSeedanceModel, resolution: "1080p", hasVideo: true, want: 4.7},
		{name: "standard 4k", model: StandardSeedanceModel, resolution: "4K", want: 4},
		{name: "standard 4k video", model: StandardSeedanceModel, resolution: "4k", hasVideo: true, want: 2.4},
		{name: "fast default", model: FastSeedanceModel, resolution: "720p", want: 5.6},
		{name: "fast default video", model: FastSeedanceModel, resolution: "1080p", hasVideo: true, want: 3.3},
		{name: "2.5 480p uses 720p tier", model: Seedance25Model, resolution: "480p", want: 10.7},
		{name: "2.5 720p video", model: Seedance25Model, resolution: "720p", hasVideo: true, want: 6.4},
		{name: "2.5 1080p list price", model: Seedance25Model, resolution: "1080p", want: 11.7},
		{name: "2.5 1080p video list price", model: Seedance25Model, resolution: "1080p", hasVideo: true, want: 7},
		{name: "aimodel 2.0 pricing alias", model: AimodelSeedance20Model, resolution: "720p", want: 7},
		{name: "aimodel 2.5 pricing alias", model: AimodelSeedance25Model, resolution: "480p", want: 10.7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, ok := GetUnitPriceUSD(tt.model, tt.resolution, tt.hasVideo)
			require.True(t, ok)
			assert.Equal(t, tt.want, price.InexactFloat64())
		})
	}
}

func TestSeedance25RejectsUnsupportedResolutionWithoutChangingSeedance20Fallback(t *testing.T) {
	_, ok := GetUnitPriceUSD(Seedance25Model, "4k", false)
	assert.False(t, ok)
	_, ok = GetUnitPriceUSD(Seedance25Model, "future-resolution", false)
	assert.False(t, ok)
	price, ok := GetUnitPriceUSD(StandardSeedanceModel, "future-resolution", false)
	require.True(t, ok)
	assert.Equal(t, 7.0, price.InexactFloat64())
}

func TestRebuildPriceIndexPreservesCustomSeedanceMatrix(t *testing.T) {
	original := clonePrices(seedanceVideoPricing.PricesUSD)
	t.Cleanup(func() {
		seedanceVideoPricing.PricesUSD = original
		require.NoError(t, RebuildPriceIndex())
	})
	prices := DefaultPricesUSD()
	delete(prices, Seedance25Model)
	prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = 7.5
	seedanceVideoPricing.PricesUSD = prices
	require.NoError(t, RebuildPriceIndex())
	standardPrice, ok := GetUnitPriceUSD(StandardSeedanceModel, "720p", false)
	require.True(t, ok)
	assert.Equal(t, 7.5, standardPrice.InexactFloat64())
	seedance25Price, ok := GetUnitPriceUSD(Seedance25Model, "1080p", false)
	require.True(t, ok)
	assert.Equal(t, 11.7, seedance25Price.InexactFloat64())
}

func TestValidatePricesUSDRejectsIncompleteOrUnsafeMatrices(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(PricesUSD)
	}{
		{name: "missing model", mutate: func(prices PricesUSD) { delete(prices, FastSeedanceModel) }},
		{name: "unknown model", mutate: func(prices PricesUSD) {
			delete(prices, FastSeedanceModel)
			prices["invented-mini-model"] = map[string]map[string]float64{"default": {WithoutVideoKey: 1, WithVideoKey: 1}}
		}},
		{name: "missing resolution", mutate: func(prices PricesUSD) { delete(prices[StandardSeedanceModel], "4k") }},
		{name: "extra resolution", mutate: func(prices PricesUSD) {
			prices[FastSeedanceModel]["720p"] = map[string]float64{WithoutVideoKey: 5.6, WithVideoKey: 3.3}
		}},
		{name: "missing input variant", mutate: func(prices PricesUSD) { delete(prices[StandardSeedanceModel]["720p"], WithVideoKey) }},
		{name: "extra input variant", mutate: func(prices PricesUSD) { prices[StandardSeedanceModel]["720p"]["audio_only"] = 1 }},
		{name: "zero price", mutate: func(prices PricesUSD) { prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = 0 }},
		{name: "negative price", mutate: func(prices PricesUSD) { prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = -1 }},
		{name: "nan price", mutate: func(prices PricesUSD) { prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = math.NaN() }},
		{name: "infinite price", mutate: func(prices PricesUSD) { prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = math.Inf(1) }},
		{name: "price above maximum", mutate: func(prices PricesUSD) {
			prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = MaxPriceUSDPerMillion + 1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prices := DefaultPricesUSD()
			tt.mutate(prices)
			assert.Error(t, ValidatePricesUSD(prices))
		})
	}
}

func TestValidatePricesUSDJSONUsesStableContract(t *testing.T) {
	encoded, err := common.Marshal(DefaultPricesUSD())
	require.NoError(t, err)
	require.NoError(t, ValidatePricesUSDJSON(string(encoded)))
	assert.Error(t, ValidatePricesUSDJSON(`{"doubao-seedance-2-0-260128":{"720p":{"without_video":"7","with_video":4.3}}}`))
}
