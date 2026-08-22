package seedance_video_pricing

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPricesCNYAreComplete(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		resolution string
		hasVideo   bool
		want       float64
	}{
		{name: "standard 720p", model: StandardSeedanceModel, resolution: "720p", want: 46},
		{name: "standard 480p falls back to 720p", model: StandardSeedanceModel, resolution: "480p", want: 46},
		{name: "standard empty falls back to 720p", model: StandardSeedanceModel, want: 46},
		{name: "standard unknown falls back to 720p", model: StandardSeedanceModel, resolution: "future-resolution", want: 46},
		{name: "standard 720p video", model: StandardSeedanceModel, resolution: "720p", hasVideo: true, want: 28},
		{name: "standard 1080p", model: StandardSeedanceModel, resolution: "1080p", want: 51},
		{name: "standard 1080p video", model: StandardSeedanceModel, resolution: "1080p", hasVideo: true, want: 31},
		{name: "standard 4k", model: StandardSeedanceModel, resolution: "4K", want: 26},
		{name: "standard 4k video", model: StandardSeedanceModel, resolution: "4k", hasVideo: true, want: 16},
		{name: "fast default", model: FastSeedanceModel, resolution: "720p", want: 37},
		{name: "fast default video", model: FastSeedanceModel, resolution: "1080p", hasVideo: true, want: 22},
		{name: "2.5 480p uses 720p tier", model: Seedance25Model, resolution: "480p", want: 70},
		{name: "2.5 720p video", model: Seedance25Model, resolution: "720p", hasVideo: true, want: 42},
		{name: "2.5 1080p list price", model: Seedance25Model, resolution: "1080p", want: 77},
		{name: "2.5 1080p video list price", model: Seedance25Model, resolution: "1080p", hasVideo: true, want: 46},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, ok := GetUnitPriceCNY(tt.model, tt.resolution, tt.hasVideo)
			require.True(t, ok)
			assert.Equal(t, tt.want, price.InexactFloat64())
		})
	}
}

func TestSeedance25RejectsUnsupportedResolutionWithoutChangingSeedance20Fallback(t *testing.T) {
	_, ok := GetUnitPriceCNY(Seedance25Model, "4k", false)
	assert.False(t, ok)
	_, ok = GetUnitPriceCNY(Seedance25Model, "future-resolution", false)
	assert.False(t, ok)

	price, ok := GetUnitPriceCNY(StandardSeedanceModel, "future-resolution", false)
	require.True(t, ok)
	assert.Equal(t, 46.0, price.InexactFloat64())
}

func TestRebuildPriceIndexMigratesLegacySeedance20Matrix(t *testing.T) {
	original := clonePrices(seedanceVideoPricing.PricesCNY)
	t.Cleanup(func() {
		seedanceVideoPricing.PricesCNY = original
		require.NoError(t, RebuildPriceIndex())
	})

	legacy := DefaultPricesCNY()
	delete(legacy, Seedance25Model)
	legacy[StandardSeedanceModel]["720p"][WithoutVideoKey] = 49.5
	seedanceVideoPricing.PricesCNY = legacy

	require.NoError(t, RebuildPriceIndex())
	standardPrice, ok := GetUnitPriceCNY(StandardSeedanceModel, "720p", false)
	require.True(t, ok)
	assert.Equal(t, 49.5, standardPrice.InexactFloat64())
	seedance25Price, ok := GetUnitPriceCNY(Seedance25Model, "1080p", false)
	require.True(t, ok)
	assert.Equal(t, 77.0, seedance25Price.InexactFloat64())
}

func TestValidatePricesCNYRejectsIncompleteOrUnsafeMatrices(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(PricesCNY)
	}{
		{
			name: "missing model",
			mutate: func(prices PricesCNY) {
				delete(prices, FastSeedanceModel)
			},
		},
		{
			name: "unknown model",
			mutate: func(prices PricesCNY) {
				delete(prices, FastSeedanceModel)
				prices["invented-mini-model"] = map[string]map[string]float64{
					"default": {WithoutVideoKey: 1, WithVideoKey: 1},
				}
			},
		},
		{
			name: "missing resolution",
			mutate: func(prices PricesCNY) {
				delete(prices[StandardSeedanceModel], "4k")
			},
		},
		{
			name: "extra resolution",
			mutate: func(prices PricesCNY) {
				prices[FastSeedanceModel]["720p"] = map[string]float64{WithoutVideoKey: 37, WithVideoKey: 22}
			},
		},
		{
			name: "missing input variant",
			mutate: func(prices PricesCNY) {
				delete(prices[StandardSeedanceModel]["720p"], WithVideoKey)
			},
		},
		{
			name: "extra input variant",
			mutate: func(prices PricesCNY) {
				prices[StandardSeedanceModel]["720p"]["audio_only"] = 1
			},
		},
		{
			name: "zero price",
			mutate: func(prices PricesCNY) {
				prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = 0
			},
		},
		{
			name: "negative price",
			mutate: func(prices PricesCNY) {
				prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = -1
			},
		},
		{
			name: "nan price",
			mutate: func(prices PricesCNY) {
				prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = math.NaN()
			},
		},
		{
			name: "infinite price",
			mutate: func(prices PricesCNY) {
				prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = math.Inf(1)
			},
		},
		{
			name: "price above maximum",
			mutate: func(prices PricesCNY) {
				prices[StandardSeedanceModel]["720p"][WithoutVideoKey] = MaxPriceCNYPerMillion + 1
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prices := DefaultPricesCNY()
			tt.mutate(prices)
			assert.Error(t, ValidatePricesCNY(prices))
		})
	}
}

func TestValidatePricesCNYJSONUsesStableContract(t *testing.T) {
	encoded, err := common.Marshal(DefaultPricesCNY())
	require.NoError(t, err)
	require.NoError(t, ValidatePricesCNYJSON(string(encoded)))
	assert.Error(t, ValidatePricesCNYJSON(`{"doubao-seedance-2-0-260128":{"720p":{"without_video":"46","with_video":28}}}`))
}
