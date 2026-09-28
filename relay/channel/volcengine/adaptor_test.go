package volcengine

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeedreamArkOpenAIUsesOpenAIImagePath(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSeedreamArkOpenAI,
			ChannelBaseUrl: "https://api.uzoomtech.com",
		},
		RelayFormat: types.RelayFormatOpenAIImage,
		RelayMode:   relayconstant.RelayModeImagesGenerations,
	}
	info.ChannelMeta.UpstreamModelName = "doubao-seedream-5-0-pro-260628"

	url, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://api.uzoomtech.com/v1/images/generations", url)
}

func TestStandardVolcengineKeepsArkV3ImagePath(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeVolcEngine,
			ChannelBaseUrl: "https://ark.cn-beijing.volces.com",
		},
		RelayFormat: types.RelayFormatOpenAIImage,
		RelayMode:   relayconstant.RelayModeImagesGenerations,
	}
	info.ChannelMeta.UpstreamModelName = "doubao-seedream-5-0-pro-260628"

	url, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://ark.cn-beijing.volces.com/api/v3/images/generations", url)
}

func TestSeedream50ProValidatesOptionASize(t *testing.T) {
	tests := []struct {
		name     string
		size     string
		wantSize string
		wantErr  string
	}{
		{name: "default", wantSize: "1024x1024"},
		{name: "minimum pixels", size: "1280x720", wantSize: "1280x720"},
		{name: "square 2k", size: "2048x2048", wantSize: "2048x2048"},
		{name: "below minimum pixels", size: "1024x896", wantErr: "between 921600 and 4620000 pixels"},
		{name: "above maximum pixels", size: "2304x2048", wantErr: "between 921600 and 4620000 pixels"},
		{name: "dimension granularity", size: "1025x1024", wantErr: "multiples of 16"},
		{name: "aspect ratio", size: "4096x256", wantSize: "4096x256"},
		{name: "aspect ratio too wide", size: "4352x256", wantErr: "between 1:16 and 16:1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				OriginModelName: "public-seedream-pro",
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "doubao-seedream-5-0-pro-260628",
				},
				RelayMode: relayconstant.RelayModeImagesGenerations,
			}
			converted, err := (&Adaptor{}).ConvertImageRequest(
				gin.CreateTestContextOnly(httptest.NewRecorder(), gin.New()),
				info,
				dto.ImageRequest{Model: "public-seedream-pro", Prompt: "test", Size: test.size},
			)
			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)
				return
			}
			require.NoError(t, err)
			request, ok := converted.(dto.ImageRequest)
			require.True(t, ok)
			assert.Equal(t, test.wantSize, request.Size)
		})
	}
}

func TestSeedream50FlashDoesNotInheritUnpublishedProSizeLimits(t *testing.T) {
	assert.Contains(t, ModelList, "doubao-seedream-5-0-flash-260915")
	info := &relaycommon.RelayInfo{
		OriginModelName: "doubao-seedream-5-0-flash-260915",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}
	converted, err := (&Adaptor{}).ConvertImageRequest(
		gin.CreateTestContextOnly(httptest.NewRecorder(), gin.New()),
		info,
		dto.ImageRequest{Model: info.OriginModelName, Prompt: "test", Size: "512x512"},
	)
	require.NoError(t, err)
	request, ok := converted.(dto.ImageRequest)
	require.True(t, ok)
	assert.Equal(t, "512x512", request.Size)
}
