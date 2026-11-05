package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/upstreammap"
	"github.com/gin-gonic/gin"
)

// Coalesce bursts of channel edits. Database discovery runs in the worker.
var UpstreamMappingChanges = make(chan struct{}, 1)

func NotifyUpstreamMappingChange() {
	select {
	case UpstreamMappingChanges <- struct{}{}:
	default:
	}
}

func CaptureUpstreamLogReference(other *LogOther, raw string) {
	if other == nil {
		return
	}
	base, err := upstreammap.Normalize(raw)
	if err != nil {
		return
	}
	other.SetAdmin("upstream", map[string]any{"base_url": base})
	if row, ok := upstreammap.Cached(base); ok {
		other.SetAdmin("upstream", map[string]any{"base_url": base, "gateway_upstream_id": row.MappingID, "ops_upstream_id": row.OpsID})
	}
}
func captureRequestUpstream(c *gin.Context, other *LogOther) *LogOther {
	if other == nil {
		other = NewLogOther()
	}
	if admin, ok := other.Snapshot()["admin_info"].(map[string]any); ok {
		if upstream, ok := admin["upstream"].(map[string]any); ok && upstream["base_url"] != nil {
			return other
		}
	}
	raw := common.GetContextKeyString(c, constant.ContextKeyChannelBaseUrl)
	if raw == "" {
		raw = constant.GetChannelBaseURL(common.GetContextKeyInt(c, constant.ContextKeyChannelType))
	}
	CaptureUpstreamLogReference(other, raw)
	return other
}
