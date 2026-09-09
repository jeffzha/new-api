package relayconvert

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatToClaudePreservesCacheControls(t *testing.T) {
	topLevel := json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`)
	block := json.RawMessage(`{"type":"ephemeral"}`)
	var input dto.GeneralOpenAIRequest
	require.NoError(t, kitutil.Unmarshal([]byte(`{
		"model":"claude-test","max_tokens":128,
		"cache_control":{"type":"ephemeral","ttl":"1h"},
		"messages":[
			{"role":"system","content":[{"type":"text","text":"stable system","cache_control":{"type":"ephemeral"}}]},
			{"role":"developer","content":[{"type":"text","text":"developer rules","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}
		],
		"tools":[{"type":"function","cache_control":{"type":"ephemeral"},"function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`), &input))
	request, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, input)
	require.NoError(t, err)
	assert.JSONEq(t, string(topLevel), string(request.CacheControl))

	system, ok := request.System.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, system, 2)
	assert.JSONEq(t, string(block), string(system[0].CacheControl))
	assert.JSONEq(t, string(block), string(system[1].CacheControl))

	content, err := request.Messages[0].ParseContent()
	require.NoError(t, err)
	require.Len(t, content, 1)
	assert.JSONEq(t, string(block), string(content[0].CacheControl))

	tools, ok := request.Tools.([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(*dto.Tool)
	require.True(t, ok)
	assert.JSONEq(t, string(block), string(tool.CacheControl))
}

func TestOpenAIResponsesToClaudeInjectsAutoCacheAndPreservesBlocks(t *testing.T) {
	maxTokens := uint(128)
	block := `{"type":"ephemeral"}`
	request, err := OpenAIResponsesRequestToClaudeMessages(context.Background(), &convmeta.Values{Options: &convmeta.Options{
		Claude: convmeta.ClaudeOptions{PromptCache: convmeta.ClaudePromptCachePolicy{Enabled: true, TTL: "5m"}},
	}}, &dto.OpenAIResponsesRequest{
		Model:           "claude-test",
		MaxOutputTokens: &maxTokens,
		Input:           json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"hello","cache_control":` + block + `}]}]`),
		Tools:           json.RawMessage(`[{"type":"function","name":"lookup","description":"Lookup","parameters":{"type":"object"},"cache_control":` + block + `}]`),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"ephemeral"}`, string(request.CacheControl))

	content, err := request.Messages[0].ParseContent()
	require.NoError(t, err)
	require.Len(t, content, 1)
	assert.JSONEq(t, block, string(content[0].CacheControl))

	tools, ok := request.Tools.([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(*dto.Tool)
	require.True(t, ok)
	assert.JSONEq(t, block, string(tool.CacheControl))
}
