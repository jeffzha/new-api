package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOPSAuthorityUsagePreservesAnthropicCacheAndContext(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	info := &relaycommon.RelayInfo{UserId: 14, RequestId: "authority-cache-ttl", OriginModelName: "claude-opus-5-gf", StartTime: time.Now()}
	u := &dto.Usage{PromptTokens: 100, CompletionTokens: 20, UsageSemantic: "anthropic", ClaudeCacheCreation5mTokens: 200, ClaudeCacheCreation1hTokens: 300}
	u.PromptTokensDetails.CachedTokens = 400
	raw, err := opsAuthorityRelayEvent(c, info, 100, u)
	require.NoError(t, err)
	var event map[string]any
	require.NoError(t, common.Unmarshal(raw, &event))
	extra := event["extra"].(map[string]any)
	require.Equal(t, float64(500), event["cacheWriteTokens"])
	require.Equal(t, float64(500), extra["cache_creation_tokens"])
	require.Equal(t, float64(1000), extra["input_tokens_total"])
	tokens := extra["billing_tokens"].(map[string]any)
	require.Equal(t, float64(1000), tokens["len"])
	require.Equal(t, float64(200), tokens["cc"])
	require.Equal(t, float64(300), tokens["cc1h"])
	// A large unrelated log detail must not strip the facts from the batch path.
	extra["unrelated"] = strings.Repeat("x", 6000)
	other, err := common.Marshal(extra)
	require.NoError(t, err)
	batched, err := opspush.EventUSD(&model.Log{Id: 1, UserId: 14, RequestId: "batch-cache-ttl", Type: model.LogTypeConsume, ModelName: "claude-opus-5-gf", CreatedAt: time.Now().Unix(), Other: string(other)}, "", nil, opspush.Conversion{QuotaPerUnit: 500000, USDExchangeRate: 1})
	require.NoError(t, err)
	encoded, err := common.Marshal(batched)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "billing_tokens")
	require.Contains(t, string(encoded), "input_tokens_total")
	require.Contains(t, string(encoded), "cache_creation_tokens_1h")
}

func TestOPSAuthorityUsageRejectsInvalidCacheCounts(t *testing.T) {
	info := &relaycommon.RelayInfo{UserId: 14, RequestId: "invalid-cache", OriginModelName: "claude", StartTime: time.Now()}
	for _, count := range []int{-1, 1_000_000_000_001} {
		u := &dto.Usage{UsageSemantic: "anthropic", ClaudeCacheCreation1hTokens: count}
		_, err := opsAuthorityRelayEvent(nil, info, 0, u)
		require.Error(t, err)
	}
}

func TestOPSAuthorityTerminalRetainsVideoPricingDimensions(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-dimensions")
	for _, reference := range []bool{false, true} {
		task := &model.Task{UserId: 14, Status: model.TaskStatusSuccess, SubmitTime: time.Now().Unix(),
			Properties: model.Properties{OriginModelName: "doubao-seedance-2-0-filteroff"},
			PrivateData: model.TaskPrivateData{BillingSource: "ops", BillingScope: opsauthority.Scope(),
				Execution:      &model.TaskExecutionSnapshot{RequestID: "video-dimensions"},
				BillingContext: &model.TaskBillingContext{PerCallBilling: true, UsageDimensions: &model.TaskUsageDimensions{Resolution: "720p", ReferenceVideo: &reference}}}}
		// The private JSON must survive restart before the provider completes.
		raw, err := task.PrivateData.Value()
		require.NoError(t, err)
		restored := *task
		require.NoError(t, restored.PrivateData.Scan(raw))
		PrepareOPSAuthorityTask(nil, &restored, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 10000})
		require.NotNil(t, restored.PrivateData.OPSTerminal)
		var event struct {
			Extra struct {
				Facts map[string]any `json:"usage_facts"`
			} `json:"extra"`
		}
		require.NoError(t, common.Unmarshal(restored.PrivateData.OPSTerminal.Event, &event))
		require.Equal(t, "720p", event.Extra.Facts["resolution"])
		require.Equal(t, reference, event.Extra.Facts["reference_video"])
	}
}
