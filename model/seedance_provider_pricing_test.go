package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeedanceProviderPricingMatrixByPublicModel(t *testing.T) {
	resetPricingEndpointTestTables(t)
	insertPricingEndpointChannel(t, 601, constant.ChannelTypeDoubaoVideo, dto.ChannelOtherSettings{})
	for _, modelName := range []string{
		seedancepricing.StandardSeedanceModel,
		mobileCloudSeedanceModel,
		seedancepricing.FastSeedanceModel,
		"gpt-4o",
	} {
		insertPricingEndpointAbility(t, 601, modelName)
	}

	pricingByModel := make(map[string]Pricing)
	for _, pricing := range GetPricing() {
		pricingByModel[pricing.ModelName] = pricing
	}

	tests := []struct {
		name            string
		model           string
		wantResolutions []string
	}{
		{
			name:            "domestic Seedance exposes every configured resolution",
			model:           seedancepricing.StandardSeedanceModel,
			wantResolutions: []string{"720p", "1080p", "4K"},
		},
		{
			name:            "mobile cloud exposes its supported resolutions",
			model:           mobileCloudSeedanceModel,
			wantResolutions: []string{"480p", "720p", "1080p"},
		},
		{
			name:            "fast Seedance exposes its configured default tier",
			model:           seedancepricing.FastSeedanceModel,
			wantResolutions: []string{"default"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing, ok := pricingByModel[tt.model]

			require.True(t, ok)
			require.NotNil(t, pricing.ProviderPricing)
			providerPricing := pricing.ProviderPricing
			assert.Equal(t, providerPricingKindVideoTokenMatrix, providerPricing.Kind)
			assert.Equal(t, "CNY", providerPricing.Currency)
			assert.Equal(t, "1M_video_tokens", providerPricing.Unit)
			resolutions := make([]string, 0, len(providerPricing.Tiers))
			for _, tier := range providerPricing.Tiers {
				resolutions = append(resolutions, tier.Resolution)
			}
			assert.Equal(t, tt.wantResolutions, resolutions)
			if tt.model == seedancepricing.FastSeedanceModel {
				assert.Equal(t, VideoTokenPricingTier{
					Resolution:   "default",
					WithoutVideo: 37,
					WithVideo:    22,
				}, providerPricing.Tiers[0])
			} else {
				assert.Equal(t, VideoTokenPricingTier{
					Resolution:   tt.wantResolutions[0],
					WithoutVideo: 46,
					WithVideo:    28,
				}, providerPricing.Tiers[0])
			}

			modelPrice, hasModelPrice := ratio_setting.GetModelPrice(tt.model, false)
			if hasModelPrice {
				assert.Equal(t, 1, pricing.QuotaType)
				assert.Equal(t, modelPrice, pricing.ModelPrice)
			} else {
				modelRatio, _, _ := ratio_setting.GetModelRatio(tt.model)
				assert.Zero(t, pricing.QuotaType)
				assert.Equal(t, modelRatio, pricing.ModelRatio)
				assert.Equal(t, ratio_setting.GetCompletionRatio(tt.model), pricing.CompletionRatio)
			}
			if billing_setting.GetBillingMode(tt.model) != billing_setting.BillingModeTieredExpr {
				assert.Empty(t, pricing.BillingMode)
				assert.Empty(t, pricing.BillingExpr)
			}
		})
	}

	ordinary, ok := pricingByModel["gpt-4o"]
	require.True(t, ok)
	assert.Nil(t, ordinary.ProviderPricing)
}

func TestProviderPricingIsAbsentForOrdinaryModel(t *testing.T) {
	pricing := Pricing{ModelName: "gpt-4o", ProviderPricing: getProviderPricing("gpt-4o")}

	encoded, err := common.Marshal(pricing)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "provider_pricing")
}

func TestProviderPricingReflectsConfiguredSeedancePrices(t *testing.T) {
	resetPricingEndpointTestTables(t)
	insertPricingEndpointChannel(t, 602, constant.ChannelTypeDoubaoVideo, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 602, seedancepricing.StandardSeedanceModel)
	insertPricingEndpointAbility(t, 602, mobileCloudSeedanceModel)

	// Prime the API pricing cache so this test also protects immediate cache
	// invalidation after administrators change the Seedance matrix.
	require.NotEmpty(t, GetPricing())

	defaults := seedancepricing.DefaultPricesCNY()
	defaultJSON, err := common.Marshal(defaults)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.True(t, handleConfigUpdate(seedancepricing.OptionKey, string(defaultJSON)))
	})

	updated := seedancepricing.DefaultPricesCNY()
	updated[seedancepricing.StandardSeedanceModel]["720p"][seedancepricing.WithoutVideoKey] = 47.5
	updatedJSON, err := common.Marshal(updated)
	require.NoError(t, err)
	require.True(t, handleConfigUpdate(seedancepricing.OptionKey, string(updatedJSON)))

	pricingByModel := make(map[string]Pricing)
	for _, pricing := range GetPricing() {
		pricingByModel[pricing.ModelName] = pricing
	}
	domestic := pricingByModel[seedancepricing.StandardSeedanceModel].ProviderPricing
	mobileCloud := pricingByModel[mobileCloudSeedanceModel].ProviderPricing
	require.NotNil(t, domestic)
	require.NotNil(t, mobileCloud)
	assert.Equal(t, 47.5, domestic.Tiers[0].WithoutVideo)
	assert.Equal(t, 47.5, mobileCloud.Tiers[0].WithoutVideo)
	assert.Equal(t, 47.5, mobileCloud.Tiers[1].WithoutVideo)
}
