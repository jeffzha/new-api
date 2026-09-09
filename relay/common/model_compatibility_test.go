package common

import (
	"testing"

	common2 "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	modelcompat "github.com/QuantumNous/new-api/setting/model_compatibility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyModelCompatibilityUsesFinalFormatAndSynchronizesAudit(t *testing.T) {
	common2.OptionMapRWMutex.Lock()
	wasNil := common2.OptionMap == nil
	if wasNil {
		common2.OptionMap = make(map[string]string)
	}
	previous, existed := common2.OptionMap[modelcompat.OptionKey]
	common2.OptionMap[modelcompat.OptionKey] = modelcompat.DefaultRegistryJSON()
	common2.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common2.OptionMapRWMutex.Lock()
		defer common2.OptionMapRWMutex.Unlock()
		if existed {
			common2.OptionMap[modelcompat.OptionKey] = previous
		} else if wasNil {
			common2.OptionMap = nil
		} else {
			delete(common2.OptionMap, modelcompat.OptionKey)
		}
	})

	info := &RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		ReasoningEffort:         "low",
		ChannelMeta: &ChannelMeta{
			ChannelType:       constant.ChannelTypeDeepSeek,
			UpstreamModelName: "deepseek-v4-pro",
		},
	}
	normalized, relayErr := ApplyModelCompatibility([]byte(`{"reasoning_effort":"low"}`), info)
	require.Nil(t, relayErr)
	assert.JSONEq(t, `{"reasoning_effort":"high"}`, string(normalized))
	assert.Equal(t, "high", info.ReasoningEffort)
	require.Len(t, info.ParamOverrideAudit, 1)
	assert.Contains(t, info.ParamOverrideAudit[0], "profile=deepseek-v4-openai")
}
