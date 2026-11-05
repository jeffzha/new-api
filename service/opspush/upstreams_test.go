package opspush

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/upstreammap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestUpstreamEnrichmentPreservesV11BillingFactsWhenDiagnosticsAreLarge(t *testing.T) {
	p, _ := pusherFixture(t, &opsStub{})
	p.Config.Spec = SpecV11
	p.Config.URL = "https://ops.example/test"
	t.Setenv("OPS_UPSTREAM_SYNC_ENABLED", "true")
	t.Setenv("OPS_UPSTREAM_URL", p.Config.URL)
	require.NoError(t, upstreammap.Migrate(p.DB))
	row, err := upstreammap.Ensure(p.DB, "https://upstream.example/")
	require.NoError(t, err)
	target := upstreammap.Target(p.Config.URL, p.Config.KeyID)
	require.NoError(t, upstreammap.EnsureBinding(p.DB, row.ID, target))
	require.NoError(t, p.DB.Model(&upstreammap.Binding{}).Where("mapping_id = ? AND target = ?", row.ID, target).Update("ops_upstream_id", 37).Error)
	other := model.NewLogOther()
	other.SetAdmin("upstream", map[string]any{"base_url": "https://upstream.example"})
	log := &model.Log{UserId: 1, ChannelId: 1, Type: model.LogTypeConsume, ModelName: "image", RequestId: "upstream-large", Other: other.JSONString()}
	event := EventV11(log, "", nil, Conversion{QuotaPerUnit: 500000, USDExchangeRate: 7.3, TopupPrice: 7.3})
	extra := event["extra"].(map[string]any)
	extra["diagnostics"] = strings.Repeat("x", 4000)
	extra["usage_facts"] = map[string]any{"input_images": 3}
	extra["image_count"] = 2
	require.NoError(t, p.attachUpstream(log, event))
	raw, err := common.Marshal(event["extra"])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(raw), maxExtra)
	result := object(raw)
	assert.Equal(t, "37", string(result["ops_upstream_id"]))
	assert.Equal(t, "2", string(result["image_count"]))
	assert.Equal(t, "500000", string(result["quotaPerUnit"]))
	assert.Equal(t, "7.3", string(result["usdExchangeRate"]))
	assert.Equal(t, "3", string(object(result["usage_facts"])["input_images"]))
	assert.NotContains(t, result, "diagnostics")
}
