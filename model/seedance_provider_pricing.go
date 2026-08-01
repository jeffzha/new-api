package model

import seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"

const (
	providerPricingKindVideoTokenMatrix = "video_token_matrix"
	mobileCloudSeedanceModel            = "doubao-seedance-2.0"
)

type ProviderPricing struct {
	Kind     string                  `json:"kind"`
	Currency string                  `json:"currency"`
	Unit     string                  `json:"unit"`
	Tiers    []VideoTokenPricingTier `json:"tiers"`
}

type VideoTokenPricingTier struct {
	Resolution   string  `json:"resolution"`
	WithoutVideo float64 `json:"without_video"`
	WithVideo    float64 `json:"with_video"`
}

func getProviderPricing(modelName string) *ProviderPricing {
	priceModel := ""
	resolutions := []string(nil)
	switch modelName {
	case seedancepricing.StandardSeedanceModel:
		priceModel = seedancepricing.StandardSeedanceModel
		resolutions = []string{"720p", "1080p", "4K"}
	case mobileCloudSeedanceModel:
		// The mobile-cloud API does not accept 4K. Its 480p requests use the
		// configured 720p rate, matching the task billing normalization.
		priceModel = seedancepricing.StandardSeedanceModel
		resolutions = []string{"480p", "720p", "1080p"}
	case seedancepricing.FastSeedanceModel:
		priceModel = seedancepricing.FastSeedanceModel
		resolutions = []string{"default"}
	default:
		return nil
	}

	tiers := make([]VideoTokenPricingTier, 0, len(resolutions))
	for _, resolution := range resolutions {
		withoutVideo, ok := seedancepricing.GetUnitPriceCNY(
			priceModel,
			resolution,
			false,
		)
		if !ok {
			return nil
		}
		withVideo, ok := seedancepricing.GetUnitPriceCNY(
			priceModel,
			resolution,
			true,
		)
		if !ok {
			return nil
		}
		tiers = append(tiers, VideoTokenPricingTier{
			Resolution:   resolution,
			WithoutVideo: withoutVideo.InexactFloat64(),
			WithVideo:    withVideo.InexactFloat64(),
		})
	}

	return &ProviderPricing{
		Kind:     providerPricingKindVideoTokenMatrix,
		Currency: "CNY",
		Unit:     "1M_video_tokens",
		Tiers:    tiers,
	}
}
