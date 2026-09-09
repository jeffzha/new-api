package claude

import (
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

const maxClaudePromptCacheBreakpoints = 4

func ApplyPromptCachePolicy(request *dto.ClaudeRequest, policy convmeta.ClaudePromptCachePolicy) error {
	if request == nil || !policy.Enabled || len(request.CacheControl) > 0 {
		return nil
	}

	ttl := strings.TrimSpace(policy.TTL)
	if ttl == "" {
		ttl = "5m"
	}
	if ttl != "5m" && ttl != "1h" {
		return nil
	}

	count, explicitTTLs := explicitPromptCacheBreakpoints(request)
	if count >= maxClaudePromptCacheBreakpoints {
		return nil
	}
	for _, explicitTTL := range explicitTTLs {
		if explicitTTL != ttl {
			return nil
		}
	}

	cacheControl := map[string]string{"type": "ephemeral"}
	if ttl == "1h" {
		cacheControl["ttl"] = ttl
	}
	encoded, err := kitutil.Marshal(cacheControl)
	if err != nil {
		return err
	}
	request.CacheControl = encoded
	return nil
}

func explicitPromptCacheBreakpoints(request *dto.ClaudeRequest) (int, []string) {
	controls := make([]json.RawMessage, 0)
	appendControl := func(control json.RawMessage) {
		if len(control) > 0 {
			controls = append(controls, control)
		}
	}

	switch tools := request.Tools.(type) {
	case []any:
		for _, item := range tools {
			switch tool := item.(type) {
			case *dto.Tool:
				if tool != nil {
					appendControl(tool.CacheControl)
				}
			case dto.Tool:
				appendControl(tool.CacheControl)
			}
		}
	case []*dto.Tool:
		for _, tool := range tools {
			if tool != nil {
				appendControl(tool.CacheControl)
			}
		}
	}

	if system, ok := request.System.([]dto.ClaudeMediaMessage); ok {
		for _, block := range system {
			appendControl(block.CacheControl)
		}
	}
	for _, message := range request.Messages {
		if blocks, err := message.ParseContent(); err == nil {
			for _, block := range blocks {
				appendControl(block.CacheControl)
			}
		}
	}

	ttls := make([]string, 0, len(controls))
	for _, control := range controls {
		var value struct {
			TTL string `json:"ttl"`
		}
		if err := kitutil.Unmarshal(control, &value); err != nil {
			continue
		}
		ttl := strings.TrimSpace(value.TTL)
		if ttl == "" {
			ttl = "5m"
		}
		ttls = append(ttls, ttl)
	}
	return len(controls), ttls
}
