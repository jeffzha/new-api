package mobilecloudseedance

import (
	"net/http/httptest"
	"testing"

	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateTaskBillingUsesExistingCNYPriceMatrix(t *testing.T) {
	oldQuotaPerUnit := appcommon.QuotaPerUnit
	oldExchangeRate := operation_setting.USDExchangeRate
	appcommon.QuotaPerUnit = 500_000
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() {
		appcommon.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.USDExchangeRate = oldExchangeRate
	})

	tests := []struct {
		name       string
		resolution string
		hasVideo   bool
		wantPrice  string
		wantTokens int64
	}{
		{name: "480p reserves 720p tier", resolution: "480p", wantPrice: "46", wantTokens: 108_000},
		{name: "720p with video", resolution: "720p", hasVideo: true, wantPrice: "28", wantTokens: 432_000},
		{name: "1080p", resolution: "1080p", wantPrice: "51", wantTokens: 243_000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			payload := &requestPayload{
				Model:      ModelName,
				Resolution: test.resolution,
				Content:    []contentItem{{Type: "text", Text: "test"}},
			}
			duration := intValue(5)
			payload.Duration = &duration
			if test.hasVideo {
				payload.Content = append(payload.Content, contentItem{
					Type:     "video_url",
					VideoURL: &mediaURL{URL: "https://example.com/reference.mp4"},
				})
			}
			context.Set(requestContextKey, payload)
			info := &relaycommon.RelayInfo{
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeMobileCloudSeedance},
				TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
				OriginModelName: ModelName,
				PriceData: types.PriceData{
					GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
				},
			}

			estimate, taskErr := (&TaskAdaptor{}).EstimateTaskBilling(context, info)
			require.Nil(t, taskErr)
			require.NotNil(t, estimate)
			assert.Equal(t, billingProvider, estimate.Snapshot.Provider)
			assert.Equal(t, test.wantPrice, estimate.Snapshot.UnitPricePerMillionTokens)
			assert.Equal(t, test.wantTokens, estimate.Snapshot.EstimatedTokens)
			expected := decimal.NewFromInt(test.wantTokens).
				Div(decimal.NewFromInt(1_000_000)).
				Mul(decimal.RequireFromString(test.wantPrice)).
				Div(decimal.NewFromFloat(7.3)).
				Mul(decimal.NewFromInt(500_000))
			assert.Equal(t, appcommon.QuotaFromDecimal(expected), estimate.PriceData.Quota)
		})
	}
}

func TestAdjustBillingOnCompleteUsesFrozenSnapshot(t *testing.T) {
	oldQuotaPerUnit := appcommon.QuotaPerUnit
	appcommon.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		appcommon.QuotaPerUnit = oldQuotaPerUnit
	})

	task := &model.Task{
		PrivateData: model.TaskPrivateData{
			BillingContext: &model.TaskBillingContext{
				ProviderBilling: &model.TaskProviderBillingSnapshot{
					Provider:                  billingProvider,
					Currency:                  "CNY",
					UnitPricePerMillionTokens: "46",
					CNYPerUSD:                 "7.3",
					GroupRatio:                1.2,
				},
			},
		},
	}
	result := &relaycommon.TaskInfo{TotalTokens: 250_000}

	quota, clamp, handled, err := (&TaskAdaptor{}).AdjustBillingOnCompleteChecked(task, result)
	require.NoError(t, err)
	assert.Nil(t, clamp)
	assert.True(t, handled)
	expected := decimal.NewFromInt(250_000).
		Div(decimal.NewFromInt(1_000_000)).
		Mul(decimal.NewFromInt(46)).
		Div(decimal.NewFromFloat(7.3)).
		Mul(decimal.NewFromInt(500_000)).
		Mul(decimal.NewFromFloat(1.2))
	assert.Equal(t, appcommon.QuotaFromDecimal(expected), quota)

	_, _, handled, err = (&TaskAdaptor{}).AdjustBillingOnCompleteChecked(
		task,
		&relaycommon.TaskInfo{TotalTokens: 0},
	)
	assert.True(t, handled)
	require.EqualError(t, err, "Mobile Cloud Seedance succeeded without usage.total_tokens")
}

func intValue(value int) dto.IntValue {
	return dto.IntValue(value)
}
