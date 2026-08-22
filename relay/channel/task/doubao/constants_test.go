package doubao

import (
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetVideoInputRatioSeedanceAliases(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		resolution string
		hasVideo   bool
		want       float64
	}{
		{
			name:       "seedance 2.0 480p 720p base",
			model:      "doubao-seedance-2-0-filter-off",
			resolution: "720p",
			want:       1,
		},
		{
			name:       "seedance 2.0 720p with video input",
			model:      "doubao-seedance-2-0-filter-off",
			resolution: "720p",
			hasVideo:   true,
			want:       4.3 / 7.0,
		},
		{
			name:       "seedance 2.0 1080p",
			model:      "doubao-seedance-2-0-filter-off",
			resolution: "1080p",
			want:       7.7 / 7.0,
		},
		{
			name:       "seedance 2.0 1080p with video input",
			model:      "doubao-seedance-2-0-filter-off",
			resolution: "1080p",
			hasVideo:   true,
			want:       4.7 / 7.0,
		},
		{
			name:       "seedance 2.0 4k",
			model:      "doubao-seedance-2-0-filter-off",
			resolution: "4K",
			want:       4.0 / 7.0,
		},
		{
			name:       "seedance 2.0 4k with video input",
			model:      "doubao-seedance-2-0-filter-off",
			resolution: "4K",
			hasVideo:   true,
			want:       2.4 / 7.0,
		},
		{
			name:       "seedance 2.0 fast base",
			model:      "doubao-seedance-2-0-fast-filter-off",
			resolution: "720p",
			want:       1,
		},
		{
			name:       "seedance 2.0 fast with video input",
			model:      "doubao-seedance-2-0-fast-filter-off",
			resolution: "720p",
			hasVideo:   true,
			want:       3.3 / 5.6,
		},
		{
			name:       "seedance 2.0 mini base",
			model:      "dreamina-seedance-2-0-mini-filter-off",
			resolution: "720p",
			want:       1,
		},
		{
			name:       "seedance 2.0 mini with video input",
			model:      "dreamina-seedance-2-0-mini-filter-off",
			resolution: "720p",
			hasVideo:   true,
			want:       2.1 / 3.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetVideoInputRatio(tt.model, tt.resolution, tt.hasVideo)
			require.True(t, ok)
			assert.InDelta(t, tt.want, got, 0.000001)
		})
	}
}

func TestSeedance25PricingUsesPublishedRates(t *testing.T) {
	tests := []struct {
		name       string
		resolution string
		hasVideo   bool
		wantPrice  float64
	}{
		{name: "720p without video", resolution: "720p", wantPrice: 10.70},
		{name: "720p with video", resolution: "720p", hasVideo: true, wantPrice: 6.40},
		{name: "1080p without video", resolution: "1080p", wantPrice: 11.70},
		{name: "1080p with video", resolution: "1080p", hasVideo: true, wantPrice: 7.00},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ratio, ok := GetVideoInputRatio(seedance25Model, tt.resolution, tt.hasVideo)
			require.True(t, ok)
			basePrice := videoPriceTable[seedance25Model][videoPriceKey{}]
			assert.InDelta(t, tt.wantPrice, basePrice*ratio, 0.000001)
		})
	}

	assert.Contains(t, ModelList, seedance25Model)
	assert.Contains(t, ModelList, "doubao-seedance-2-0-260128")
	assert.Contains(t, ModelList, "doubao-seedance-2-0-filter-off")
}

func TestGetVideoInputRatioOfficialSeedanceModelsUnchanged(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		resolution string
		hasVideo   bool
		want       float64
	}{
		{
			name:       "official seedance 2.0 base",
			model:      "doubao-seedance-2-0-260128",
			resolution: "720p",
			want:       1,
		},
		{
			name:       "official seedance 2.0 base with video input",
			model:      "doubao-seedance-2-0-260128",
			resolution: "720p",
			hasVideo:   true,
			want:       28.0 / 46.0,
		},
		{
			name:       "official seedance 2.0 1080p",
			model:      "doubao-seedance-2-0-260128",
			resolution: "1080p",
			want:       51.0 / 46.0,
		},
		{
			name:       "official seedance 2.0 1080p with video input",
			model:      "doubao-seedance-2-0-260128",
			resolution: "1080p",
			hasVideo:   true,
			want:       31.0 / 46.0,
		},
		{
			name:       "official seedance 2.0 4k",
			model:      "doubao-seedance-2-0-260128",
			resolution: "4K",
			want:       26.0 / 46.0,
		},
		{
			name:       "official seedance 2.0 4k with video input",
			model:      "doubao-seedance-2-0-260128",
			resolution: "4K",
			hasVideo:   true,
			want:       16.0 / 46.0,
		},
		{
			name:       "official seedance 2.0 fast base",
			model:      "doubao-seedance-2-0-fast-260128",
			resolution: "720p",
			want:       1,
		},
		{
			name:       "official seedance 2.0 fast with video input",
			model:      "doubao-seedance-2-0-fast-260128",
			resolution: "720p",
			hasVideo:   true,
			want:       22.0 / 37.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetVideoInputRatio(tt.model, tt.resolution, tt.hasVideo)
			require.True(t, ok)
			assert.InDelta(t, tt.want, got, 0.000001)
		})
	}
}

func TestSeedanceResolutionPrefersTopLevelField(t *testing.T) {
	req := common.TaskSubmitReq{
		Size:       "720p",
		Resolution: "1080p",
		Metadata: map[string]interface{}{
			"resolution": "4K",
		},
	}

	assert.Equal(t, "1080p", seedanceResolution(req))
}

func TestGetVideoCompletionUSDPerMTokens(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		otherRatios map[string]float64
		want        float64
		wantOK      bool
	}{
		{
			name:   "base price",
			model:  "doubao-seedance-2-0-filter-off",
			want:   7.0,
			wantOK: true,
		},
		{
			name:        "video input ratio",
			model:       "doubao-seedance-2-0-filter-off",
			otherRatios: map[string]float64{videoInputRatioKey: 4.3 / 7.0},
			want:        4.3,
			wantOK:      true,
		},
		{
			name:        "unrelated ratios are ignored",
			model:       "doubao-seedance-2-0-filter-off",
			otherRatios: map[string]float64{"seconds": 15},
			want:        7.0,
			wantOK:      true,
		},
		{
			name:        "legacy model keeps generic completion billing",
			model:       "doubao-seedance-2-0-260128",
			otherRatios: map[string]float64{videoInputRatioKey: 28.0 / 46.0},
			wantOK:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := getVideoCompletionUSDPerMTokens(tt.model, tt.otherRatios)
			assert.Equal(t, tt.wantOK, ok)
			assert.InDelta(t, tt.want, got, 0.000001)
		})
	}
}
