package volcengine

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
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
