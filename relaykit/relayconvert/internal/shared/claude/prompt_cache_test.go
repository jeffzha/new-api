package claude

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyPromptCachePolicy(t *testing.T) {
	tests := []struct {
		name       string
		request    *dto.ClaudeRequest
		policy     convmeta.ClaudePromptCachePolicy
		want       string
		wantAbsent bool
	}{
		{name: "disabled", request: &dto.ClaudeRequest{}, policy: convmeta.ClaudePromptCachePolicy{}, wantAbsent: true},
		{name: "default five minutes", request: &dto.ClaudeRequest{}, policy: convmeta.ClaudePromptCachePolicy{Enabled: true, TTL: "5m"}, want: `{"type":"ephemeral"}`},
		{name: "one hour", request: &dto.ClaudeRequest{}, policy: convmeta.ClaudePromptCachePolicy{Enabled: true, TTL: "1h"}, want: `{"type":"ephemeral","ttl":"1h"}`},
		{name: "preserves explicit top level", request: &dto.ClaudeRequest{CacheControl: json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`)}, policy: convmeta.ClaudePromptCachePolicy{Enabled: true, TTL: "5m"}, want: `{"type":"ephemeral","ttl":"1h"}`},
		{name: "mixed ttl stays explicit only", request: requestWithExplicitControls(`{"type":"ephemeral","ttl":"1h"}`), policy: convmeta.ClaudePromptCachePolicy{Enabled: true, TTL: "5m"}, wantAbsent: true},
		{name: "four explicit breakpoints skip auto", request: requestWithFourControls(), policy: convmeta.ClaudePromptCachePolicy{Enabled: true, TTL: "5m"}, wantAbsent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, ApplyPromptCachePolicy(test.request, test.policy))
			if test.wantAbsent {
				assert.Empty(t, test.request.CacheControl)
				return
			}
			assert.JSONEq(t, test.want, string(test.request.CacheControl))
		})
	}
}

func requestWithExplicitControls(control string) *dto.ClaudeRequest {
	text := "system"
	return &dto.ClaudeRequest{System: []dto.ClaudeMediaMessage{{Type: "text", Text: &text, CacheControl: json.RawMessage(control)}}}
}

func requestWithFourControls() *dto.ClaudeRequest {
	control := json.RawMessage(`{"type":"ephemeral"}`)
	text := "cached"
	return &dto.ClaudeRequest{
		System: []dto.ClaudeMediaMessage{{Type: "text", Text: &text, CacheControl: control}},
		Tools: []any{
			&dto.Tool{Name: "one", CacheControl: control},
			&dto.Tool{Name: "two", CacheControl: control},
			&dto.Tool{Name: "three", CacheControl: control},
		},
	}
}
