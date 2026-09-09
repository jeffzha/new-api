package model_compatibility

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAppliesActiveProfileWithinExactScope(t *testing.T) {
	previous := setRegistryForTest(t, `{
  "schema_version": 1,
  "enabled": true,
  "active_version": "v2",
  "versions": [
    {"id":"v1","profiles":[{"id":"inactive","channel_types":[43],"model_regex":"^deepseek-v4-pro$","relay_formats":["openai"],"rules":[{"path":"reasoning_effort","action":"map","values":{"low":"max"}}]}]},
    {"id":"v2","profiles":[{"id":"canary","channel_types":[43],"channel_ids":[7],"groups":["canary"],"model_regex":"^deepseek-v4-pro$","relay_formats":["openai"],"rules":[{"path":"reasoning_effort","action":"map","values":{"low":"high"}}]}]}
  ]
}`)
	defer previous()

	normalized, version, changes, err := Normalize([]byte(`{"reasoning_effort":"low","temperature":0}`), RequestContext{
		ChannelType: 43,
		ChannelID:   7,
		Group:       "canary",
		Model:       "deepseek-v4-pro",
		RelayFormat: "openai",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"reasoning_effort":"high","temperature":0}`, string(normalized))
	assert.Equal(t, "v2", version)
	require.Len(t, changes, 1)
	assert.Equal(t, "canary", changes[0].ProfileID)

	for _, ctx := range []RequestContext{
		{ChannelType: 43, ChannelID: 8, Group: "canary", Model: "deepseek-v4-pro", RelayFormat: "openai"},
		{ChannelType: 43, ChannelID: 7, Group: "default", Model: "deepseek-v4-pro", RelayFormat: "openai"},
		{ChannelType: 43, ChannelID: 7, Group: "canary", Model: "deepseek-v4-pro", RelayFormat: "claude"},
	} {
		untouched, _, scopedChanges, scopedErr := Normalize([]byte(`{"reasoning_effort":"low"}`), ctx)
		require.NoError(t, scopedErr)
		assert.JSONEq(t, `{"reasoning_effort":"low"}`, string(untouched))
		assert.Empty(t, scopedChanges)
	}
}

func TestNormalizePreservesDestinationAndExplicitZero(t *testing.T) {
	previous := setRegistryForTest(t, `{
  "schema_version":1,"enabled":true,"active_version":"v1","versions":[{"id":"v1","profiles":[{
    "id":"safe","channel_types":[43],"model_regex":"^model$","relay_formats":["openai"],"rules":[
      {"path":"old_effort","action":"rename","to":"reasoning_effort"},
      {"path":"max_tokens","action":"inject_if_absent","value":2048}
    ]
  }]}]}`)
	defer previous()

	normalized, _, changes, err := Normalize([]byte(`{"old_effort":"low","reasoning_effort":"high","max_tokens":0}`), RequestContext{
		ChannelType: 43, Model: "model", RelayFormat: "openai",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"old_effort":"low","reasoning_effort":"high","max_tokens":0}`, string(normalized))
	assert.Empty(t, changes)
}

func TestValidateRegistryRejectsUnsafeOrInvalidContracts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing active", raw: `{"schema_version":1,"enabled":true,"active_version":"missing","versions":[{"id":"v1","profiles":[]}]}`, want: "does not exist"},
		{name: "invalid regex", raw: `{"schema_version":1,"enabled":true,"active_version":"v1","versions":[{"id":"v1","profiles":[{"id":"p","channel_types":[1],"model_regex":"[","relay_formats":["openai"],"rules":[]}]}]}`, want: "invalid model_regex"},
		{name: "protected request content", raw: `{"schema_version":1,"enabled":true,"active_version":"v1","versions":[{"id":"v1","profiles":[{"id":"p","channel_types":[1],"model_regex":".*","relay_formats":["openai"],"rules":[{"path":"messages.0.content","action":"drop"}]}]}]}`, want: "protected"},
		{name: "invalid channel scope", raw: `{"schema_version":1,"enabled":true,"active_version":"v1","versions":[{"id":"v1","profiles":[{"id":"p","channel_types":[1],"channel_ids":[0],"model_regex":".*","relay_formats":["openai"],"rules":[]}]}]}`, want: "channel_ids"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRegistryJSON(test.raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestNormalizeRejectRuleReturnsRequestError(t *testing.T) {
	previous := setRegistryForTest(t, `{"schema_version":1,"enabled":true,"active_version":"v1","versions":[{"id":"v1","profiles":[{"id":"p","channel_types":[1],"model_regex":"^m$","relay_formats":["openai"],"rules":[{"path":"unsupported","action":"reject","message":"unsupported is unavailable"}]}]}]}`)
	defer previous()

	_, version, _, err := Normalize([]byte(`{"unsupported":true}`), RequestContext{ChannelType: 1, Model: "m", RelayFormat: "openai"})
	assert.Equal(t, "v1", version)
	var requestErr *RequestError
	require.ErrorAs(t, err, &requestErr)
	assert.Equal(t, "unsupported", requestErr.Path)
}

func setRegistryForTest(t *testing.T, raw string) func() {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	wasNil := common.OptionMap == nil
	if wasNil {
		common.OptionMap = make(map[string]string)
	}
	old, existed := common.OptionMap[OptionKey]
	common.OptionMap[OptionKey] = raw
	common.OptionMapRWMutex.Unlock()
	return func() {
		common.OptionMapRWMutex.Lock()
		if existed {
			common.OptionMap[OptionKey] = old
		} else if wasNil {
			common.OptionMap = nil
		} else {
			delete(common.OptionMap, OptionKey)
		}
		common.OptionMapRWMutex.Unlock()
	}
}
