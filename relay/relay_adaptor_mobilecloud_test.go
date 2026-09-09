package relay

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMobileCloudSeedanceChannelRegistration(t *testing.T) {
	channelType := constant.ChannelTypeMobileCloudSeedance
	require.Greater(t, len(constant.ChannelBaseURLs), channelType)
	assert.Equal(t, "MobileCloudSeedance", constant.GetChannelTypeName(channelType))

	adaptor := GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channelType)))
	require.NotNil(t, adaptor)
	assert.Equal(t, "Mobile Cloud Seedance", adaptor.GetChannelName())
	assert.Equal(t, []string{"doubao-seedance-2.0"}, adaptor.GetModelList())
}
