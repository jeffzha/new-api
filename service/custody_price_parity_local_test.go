package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOverseasCustodyPriceUsageEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/overseas-custody-pricing.json")
	require.NoError(t, err)
	var fixture struct {
		Source []struct {
			Model     string
			Effective map[string]any
		}
	}
	require.NoError(t, common.Unmarshal(raw, &fixture))
	previous := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	defer func() { common.QuotaPerUnit = previous }()
	receipts := []map[string]any{}
	for _, source := range fixture.Source {
		if strings.Contains(source.Model, "seedance") {
			continue
		}
		expr, _ := source.Effective["billing_setting.billing_expr"].(string)
		for _, length := range []int{1000, 272000, 272001, 300000} {
			for _, cache := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
				u := &dto.Usage{PromptTokens: length, CompletionTokens: 100, TotalTokens: length + 100}
				anthropic := strings.HasPrefix(source.Model, "claude") && source.Model != "claude-3-5-sonnet-20241022"
				if anthropic {
					u.UsageSemantic = "anthropic"
					c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
				}
				if cache {
					u.PromptTokensDetails.CachedTokens = 100
					if anthropic {
						u.ClaudeCacheCreation5mTokens = 20
						u.ClaudeCacheCreation1hTokens = 30
					}
				}
				if source.Model == "gemini-2.0-flash" {
					u.PromptTokensDetails.AudioTokens = 50
				}
				info := &relaycommon.RelayInfo{UserId: 14, RequestId: fmt.Sprintf("parity-%s-%d-%v", source.Model, length, cache), OriginModelName: source.Model, StartTime: time.Unix(1795170000, 0), PriceData: types.PriceData{GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}, ImageRatio: 1}}
				f := func(k string, d float64) float64 {
					if v, ok := source.Effective[k].(float64); ok {
						return v
					}
					return d
				}
				info.PriceData.ModelRatio = f("ModelRatio", 1)
				info.PriceData.CompletionRatio = f("CompletionRatio", 1)
				info.PriceData.CacheRatio = f("CacheRatio", 1)
				info.PriceData.CacheCreationRatio = f("CreateCacheRatio", 1.25)
				info.PriceData.CacheCreation5mRatio = info.PriceData.CacheCreationRatio
				info.PriceData.CacheCreation1hRatio = info.PriceData.CacheCreationRatio * 1.6
				quota := calculateTextQuotaSummary(c, info, u).Quota
				var amount float64
				if expr != "" {
					params := BuildTieredTokenParams(u, anthropic, billingexpr.UsedVars(expr))
					value, _, err := billingexpr.RunExpr(expr, params)
					require.NoError(t, err)
					amount = value / 1000000
					quota = common.QuotaRound(amount * 500000)
				} else {
					amount = float64(quota) / 500000
				}
				eventRaw, err := opsAuthorityRelayEvent(c, info, quota, u)
				require.NoError(t, err)
				var event map[string]any
				require.NoError(t, common.Unmarshal(eventRaw, &event))
				receipts = append(receipts, map[string]any{"model": source.Model, "event": event, "expectedUsd": amount, "expectedQuota": quota, "expression": expr != ""})
			}
		}
	}
	require.Equal(t, 144, len(receipts))
	ref := false
	original := model.TaskPrivateData{BillingContext: &model.TaskBillingContext{UsageDimensions: &model.TaskUsageDimensions{Resolution: "720p", ReferenceVideo: &ref}}}
	encoded, err := common.Marshal(original)
	require.NoError(t, err)
	var restored model.TaskPrivateData
	require.NoError(t, common.Unmarshal(encoded, &restored))
	require.NotNil(t, restored.BillingContext.UsageDimensions.ReferenceVideo)
	require.False(t, *restored.BillingContext.UsageDimensions.ReferenceVideo)
	out, err := common.Marshal(receipts)
	require.NoError(t, err)
	if path := os.Getenv("OPS_PRICE_PARITY_RECEIPTS"); path != "" {
		require.NoError(t, os.WriteFile(path, out, 0600))
	}
}
