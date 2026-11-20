package doubao

import (
	"fmt"
	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestOverseasCustodyVideoPriceEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/overseas-custody-pricing.json")
	require.NoError(t, err)
	var fixture struct {
		Source []struct {
			Model     string
			Effective map[string]any
		}
	}
	require.NoError(t, appcommon.Unmarshal(raw, &fixture))
	previous := appcommon.QuotaPerUnit
	appcommon.QuotaPerUnit = 500000
	defer func() { appcommon.QuotaPerUnit = previous }()
	receipts := []map[string]any{}
	for _, source := range fixture.Source {
		if !strings.Contains(source.Model, "seedance") {
			continue
		}
		for _, resolution := range []string{"480p", "720p", "1080p", "4k"} {
			for _, reference := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				request := relaycommon.TaskSubmitReq{Prompt: "test", Model: source.Model, Resolution: resolution, Duration: 5}
				if reference {
					request.Content = []map[string]any{{"type": "video_url", "video_url": map[string]any{"url": "https://example.invalid/test.mp4"}}}
				}
				info := &relaycommon.RelayInfo{OriginModelName: source.Model, ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDoubaoVideo}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}, PriceData: types.PriceData{ModelRatio: source.Effective["ModelRatio"].(float64), GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}}
				relaycommon.StoreTaskRequest(c, info, "generate", request)
				a := &TaskAdaptor{}
				bc := &model.TaskBillingContext{OriginModelName: source.Model, GroupRatio: 1, ModelRatio: info.PriceData.ModelRatio}
				if a.SupportsTaskBilling(constant.ChannelTypeDoubaoVideo, source.Model) {
					estimate, taskErr := a.EstimateTaskBilling(c, info)
					require.Nil(t, taskErr)
					bc.ProviderBilling = estimate.Snapshot
				} else {
					bc.OtherRatios = a.EstimateBilling(c, info)
				}
				task := &model.Task{PrivateData: model.TaskPrivateData{BillingContext: bc}}
				quota, clamp, handled, err := a.AdjustBillingOnCompleteChecked(task, &relaycommon.TaskInfo{TotalTokens: 1000000})
				require.NoError(t, err)
				require.Nil(t, clamp)
				if !handled {
					multiplier := 1.0
					for _, r := range bc.OtherRatios {
						multiplier *= r
					}
					quota = appcommon.QuotaFromFloat(1000000 * bc.ModelRatio * multiplier)
				}
				receipts = append(receipts, map[string]any{"model": source.Model, "event": map[string]any{"requestId": fmt.Sprintf("video-%s-%s-%v", source.Model, resolution, reference), "promptTokens": 0, "completionTokens": 0, "cacheReadTokens": 0, "cacheWriteTokens": 0, "attributes": map[string]any{"resolution": resolution, "referenceVideo": reference}, "billedTokens": 1000000, "extra": map[string]any{}}, "expectedUsd": float64(quota) / 500000, "expectedQuota": quota, "expression": false})
			}
		}
	}
	require.Equal(t, 64, len(receipts))
	out, err := appcommon.Marshal(receipts)
	require.NoError(t, err)
	if path := os.Getenv("OPS_PRICE_PARITY_RECEIPTS"); path != "" {
		require.NoError(t, os.WriteFile(path, out, 0600))
	}
}
