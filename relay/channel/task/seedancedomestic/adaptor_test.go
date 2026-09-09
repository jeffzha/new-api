package seedancedomestic

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRequestPreservesSpecialContentAndDefaults(t *testing.T) {
	c, info := seedanceTestContext(t, `{
  "model":"doubao-seedance-2-0-260128",
  "content":[
    {"type":"image_url","image_url":{"url":"asset://asset-1"},"role":"reference_image","vendor_field":"keep-me"},
    {"type":"text","text":"move naturally"}
  ]
}`)
	adaptor := &TaskAdaptor{}

	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	request, err := getNormalizedRequest(c)

	require.NoError(t, err)
	assert.Equal(t, "720p", request.Resolution)
	assert.Equal(t, "adaptive", request.Ratio)
	assert.Equal(t, 5, request.Dur)
	assert.Equal(t, 1, request.AudioStatus)
	require.Len(t, request.Content, 2)
	assert.Equal(t, "reference_image", request.Content[0]["role"])
	assert.Equal(t, "keep-me", request.Content[0]["vendor_field"])
}

func TestValidateRequestRejectsUnsafeBillingInputs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		message string
	}{
		{name: "duration too large", body: `{"model":"m","prompt":"x","dur":16}`, message: "dur must be"},
		{name: "duration zero", body: `{"model":"m","prompt":"x","dur":0}`, message: "dur must be"},
		{name: "standard duration zero", body: `{"model":"m","prompt":"x","duration":0}`, message: "dur must be"},
		{name: "bad resolution", body: `{"model":"m","prompt":"x","resolution":"2k"}`, message: "resolution"},
		{name: "bad ratio", body: `{"model":"m","prompt":"x","ratio":"2:1"}`, message: "ratio"},
		{name: "bad audio status", body: `{"model":"m","prompt":"x","audio_status":2}`, message: "audio_status"},
		{name: "audio alone", body: `{"model":"m","content":[{"type":"audio_url","audio_url":{"url":"https://example.com/a.mp3"}}]}`, message: "audio input cannot"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, info := seedanceTestContext(t, test.body)
			taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info)
			require.NotNil(t, taskErr)
			assert.Contains(t, taskErr.Message, test.message)
			assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
		})
	}
}

func TestValidateRequestRejects4KWhenDisabled(t *testing.T) {
	oldEnabled := seedanceDomestic4KEnabled
	seedanceDomestic4KEnabled = false
	t.Cleanup(func() { seedanceDomestic4KEnabled = oldEnabled })
	c, info := seedanceTestContext(t, `{
  "model":"doubao-seedance-2-0-260128",
  "prompt":"a cinematic shot",
  "resolution":"4K",
  "ratio":"21:9",
  "dur":4
}`)

	taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
	assert.Contains(t, taskErr.Message, "4k is not enabled")
}

func TestValidateRequestAcceptsAndNormalizes4KWhenEnabled(t *testing.T) {
	oldEnabled := seedanceDomestic4KEnabled
	seedanceDomestic4KEnabled = true
	t.Cleanup(func() { seedanceDomestic4KEnabled = oldEnabled })
	c, info := seedanceTestContext(t, `{
  "model":"doubao-seedance-2-0-260128",
  "prompt":"a cinematic shot",
  "resolution":"4K",
  "ratio":"21:9",
  "dur":4
}`)
	adaptor := &TaskAdaptor{}

	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	request, err := getNormalizedRequest(c)

	require.NoError(t, err)
	assert.Equal(t, "4k", request.Resolution)
	assert.Equal(t, "21:9", request.Ratio)
	requestBody, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	var upstreamRequest generateRequest
	require.NoError(t, common.DecodeJson(requestBody, &upstreamRequest))
	assert.Equal(t, "4k", upstreamRequest.Resolution)
	assert.Equal(t, "21:9", upstreamRequest.Ratio)
}

func TestOfficial4KPixelDimensions(t *testing.T) {
	expected := map[string]int64{
		"16:9": 3840 * 2160,
		"4:3":  3326 * 2494,
		"1:1":  2880 * 2880,
		"3:4":  2494 * 3326,
		"9:16": 2160 * 3840,
		"21:9": 4398 * 1886,
	}

	assert.Equal(t, expected, resolutionPixels["4k"])
}

func TestEstimateTaskBillingMatchesOfficial4KTiers(t *testing.T) {
	oldEnabled := seedanceDomestic4KEnabled
	seedanceDomestic4KEnabled = true
	t.Cleanup(func() { seedanceDomestic4KEnabled = oldEnabled })
	oldRate := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() { operation_setting.USDExchangeRate = oldRate })
	tests := []struct {
		name      string
		content   string
		tokens    int64
		unitPrice string
		quota     int
		hasVideo  bool
	}{
		{
			name:      "without video input",
			content:   "",
			tokens:    777_600,
			unitPrice: "26",
			quota:     1_384_767,
		},
		{
			name:      "with video input",
			content:   `,"content":[{"type":"video_url","video_url":{"url":"https://example.com/input.mp4"}}]`,
			tokens:    3_693_600,
			unitPrice: "16",
			quota:     4_047_781,
			hasVideo:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := `{"model":"doubao-seedance-2-0-260128","prompt":"a cinematic shot","resolution":"4k","ratio":"16:9","dur":4` + test.content + `}`
			c, info := seedanceTestContext(t, body)
			info.PriceData.GroupRatioInfo = types.GroupRatioInfo{GroupRatio: 1}
			adaptor := &TaskAdaptor{}
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

			estimate, taskErr := adaptor.EstimateTaskBilling(c, info)

			require.Nil(t, taskErr)
			require.NotNil(t, estimate)
			require.NotNil(t, estimate.Snapshot)
			assert.Equal(t, test.tokens, estimate.Snapshot.EstimatedTokens)
			assert.Equal(t, test.unitPrice, estimate.Snapshot.UnitPricePerMillionTokens)
			assert.Equal(t, test.hasVideo, estimate.Snapshot.HasVideoInput)
			assert.Equal(t, test.quota, estimate.PriceData.Quota)
		})
	}
}

func TestEstimateTaskBillingMatchesOfficial720pExample(t *testing.T) {
	oldRate := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() { operation_setting.USDExchangeRate = oldRate })
	c, info := seedanceTestContext(t, `{
  "model":"doubao-seedance-2-0-260128",
  "prompt":"a cinematic shot",
  "resolution":"720p",
  "ratio":"16:9",
  "dur":5
}`)
	info.PriceData.GroupRatioInfo = types.GroupRatioInfo{GroupRatio: 1}
	adaptor := &TaskAdaptor{}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

	estimate, taskErr := adaptor.EstimateTaskBilling(c, info)

	require.Nil(t, taskErr)
	require.NotNil(t, estimate)
	require.NotNil(t, estimate.Snapshot)
	assert.Equal(t, int64(108000), estimate.Snapshot.EstimatedTokens)
	assert.Equal(t, "46", estimate.Snapshot.UnitPricePerMillionTokens)
	assert.False(t, estimate.Snapshot.HasVideoInput)
	assert.Equal(t, 340274, estimate.PriceData.Quota)
}

func TestEstimateTaskBillingMatchesOfficial1080pTiers(t *testing.T) {
	oldRate := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() { operation_setting.USDExchangeRate = oldRate })
	tests := []struct {
		name      string
		content   string
		tokens    int64
		unitPrice string
		quota     int
		hasVideo  bool
	}{
		{
			name:      "without video input",
			content:   "",
			tokens:    194_400,
			unitPrice: "51",
			quota:     679_068,
		},
		{
			name:      "with video input",
			content:   `,"content":[{"type":"video_url","video_url":{"url":"https://example.com/input.mp4"}}]`,
			tokens:    923_400,
			unitPrice: "31",
			quota:     1_960_644,
			hasVideo:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := `{"model":"doubao-seedance-2-0-260128","prompt":"a cinematic shot","resolution":"1080p","ratio":"16:9","dur":4` + test.content + `}`
			c, info := seedanceTestContext(t, body)
			info.PriceData.GroupRatioInfo = types.GroupRatioInfo{GroupRatio: 1}
			adaptor := &TaskAdaptor{}
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

			estimate, taskErr := adaptor.EstimateTaskBilling(c, info)

			require.Nil(t, taskErr)
			require.NotNil(t, estimate)
			require.NotNil(t, estimate.Snapshot)
			assert.Equal(t, test.tokens, estimate.Snapshot.EstimatedTokens)
			assert.Equal(t, test.unitPrice, estimate.Snapshot.UnitPricePerMillionTokens)
			assert.Equal(t, test.hasVideo, estimate.Snapshot.HasVideoInput)
			assert.Equal(t, test.quota, estimate.PriceData.Quota)
		})
	}
}

func TestEstimateTaskBillingUsesVideoInputPriceAndConservativeDuration(t *testing.T) {
	oldRate := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() { operation_setting.USDExchangeRate = oldRate })
	c, info := seedanceTestContext(t, `{
  "model":"doubao-seedance-2-0-260128",
  "prompt":"continue the motion",
  "content":[{"type":"video_url","video_url":{"url":"https://example.com/input.mp4"}}],
  "resolution":"720p",
  "ratio":"16:9",
  "dur":4
}`)
	info.PriceData.GroupRatioInfo = types.GroupRatioInfo{GroupRatio: 1}
	adaptor := &TaskAdaptor{}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

	estimate, taskErr := adaptor.EstimateTaskBilling(c, info)

	require.Nil(t, taskErr)
	assert.Equal(t, int64(410_400), estimate.Snapshot.EstimatedTokens)
	assert.Equal(t, "28", estimate.Snapshot.UnitPricePerMillionTokens)
	assert.True(t, estimate.Snapshot.HasVideoInput)
	assert.Equal(t, 787_068, estimate.PriceData.Quota)
}

func TestEstimateTaskBillingBoundsEveryInputVideoDuration(t *testing.T) {
	oldRate := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() { operation_setting.USDExchangeRate = oldRate })
	c, info := seedanceTestContext(t, `{
  "model":"doubao-seedance-2-0-260128",
  "prompt":"combine the references",
  "content":[
    {"type":"video_url","video_url":{"url":"https://example.com/one.mp4"}},
    {"type":"video_url","video_url":{"url":"https://example.com/two.mp4"}},
    {"type":"video_url","video_url":{"url":"https://example.com/three.mp4"}}
  ],
  "resolution":"720p",
  "ratio":"16:9",
  "dur":5
}`)
	info.PriceData.GroupRatioInfo = types.GroupRatioInfo{GroupRatio: 1}
	adaptor := &TaskAdaptor{}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

	estimate, taskErr := adaptor.EstimateTaskBilling(c, info)

	require.Nil(t, taskErr)
	assert.Equal(t, int64(1_080_000), estimate.Snapshot.EstimatedTokens)
	assert.Equal(t, 2_071_233, estimate.PriceData.Quota)
}

func TestDomesticAdaptorUsesLMDKeyAndHidesUpstreamTaskID(t *testing.T) {
	adaptor := &TaskAdaptor{apiKey: " \tsupplier-secret\r\n "}
	request := httptest.NewRequest(http.MethodPost, "https://example.com", nil)
	request.Header.Set("Authorization", "Bearer client-secret")
	require.NoError(t, adaptor.BuildRequestHeader(nil, request, nil))
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.Equal(t, "supplier-secret", request.Header.Get("lmd-key"))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		OriginModelName: "doubao-seedance-2-0-260128",
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
	response := &http.Response{Body: io.NopCloser(strings.NewReader(`{"state":1,"data":{"id":1200},"error":null}`))}

	upstreamID, taskData, taskErr := adaptor.DoResponse(c, response, info)

	require.Nil(t, taskErr)
	assert.Equal(t, "1200", upstreamID)
	assert.NotContains(t, recorder.Body.String(), "1200")
	assert.Contains(t, recorder.Body.String(), "task_public")
	assert.NotContains(t, string(taskData), "1200")
	assert.Contains(t, string(taskData), "task_public")
}

func TestDomesticAdaptorSanitizesPolledProviderTaskID(t *testing.T) {
	adaptor := &TaskAdaptor{}

	data, err := adaptor.SanitizeTaskData(&model.Task{TaskID: "task_public"}, []byte(`{
  "state":1,
  "data":{"id":1200,"status":2,"video_url":"https://example.com/video.mp4"},
  "error":null
}`))

	require.NoError(t, err)
	assert.NotContains(t, string(data), "1200")
	assert.Contains(t, string(data), "task_public")
	assert.Contains(t, string(data), "https://example.com/video.mp4")
}

func TestDomesticAdaptorConvertsStoredTaskToOpenAIVideo(t *testing.T) {
	task := &model.Task{
		TaskID:     "task_public",
		Status:     model.TaskStatusSuccess,
		Progress:   "100%",
		CreatedAt:  100,
		UpdatedAt:  200,
		Properties: model.Properties{OriginModelName: "doubao-seedance-2-0-260128"},
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "1200",
			ResultURL:      "https://example.com/result.mp4",
		},
	}

	data, err := (&TaskAdaptor{}).ConvertToOpenAIVideo(task)

	require.NoError(t, err)
	var video dto.OpenAIVideo
	require.NoError(t, common.Unmarshal(data, &video))
	assert.Equal(t, "task_public", video.ID)
	assert.Equal(t, "task_public", video.TaskID)
	assert.Equal(t, dto.VideoStatusCompleted, video.Status)
	assert.Equal(t, 100, video.Progress)
	assert.Equal(t, "doubao-seedance-2-0-260128", video.Model)
	assert.Equal(t, "https://example.com/result.mp4", video.Metadata["url"])
	assert.NotContains(t, string(data), "1200")
}

func TestDomesticAdaptorSnapshotsPollingEndpoint(t *testing.T) {
	adaptor := &TaskAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelBaseUrl: "https://supplier.example.com",
	}})

	snapshot := adaptor.TaskEndpointSnapshot()

	assert.Equal(t, "https://supplier.example.com", snapshot.BaseURL)
	assert.Equal(t, generateInfoPath, snapshot.FetchPath)
}

func TestResolveTaskBillingUsesPrivateBillAPIAndFrozenPrice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, billListPath, request.URL.Path)
		assert.Equal(t, "supplier-secret", request.Header.Get("lmd-key"))
		assert.Empty(t, request.Header.Get("Authorization"))
		var payload billListRequest
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		assert.Equal(t, 0, payload.Page)
		assert.Equal(t, billPageSize, payload.Size)
		_, _ = w.Write([]byte(`{
  "state":1,
  "data":{"total":1,"page":0,"size":"1000","list":[{
    "id":1200,"resolution":"720p","ratio":"16:9","dur":5,
    "expense_time":"2026-07-16 12:00:00","total_tokens":108871,
	    "price":"999.000000","original_price":"5.0","discount":"1.00","amount_paid":"5.0"
  }]},
  "error":null
}`))
	}))
	defer server.Close()
	adaptor := &TaskAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelBaseUrl: server.URL,
		ApiKey:         " \tsupplier-secret\r\n ",
	}})
	task := &model.Task{
		SubmitTime: timeNowUnix(),
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "1200",
			BillingContext: &model.TaskBillingContext{ProviderBilling: &model.TaskProviderBillingSnapshot{
				Provider:                  providerName,
				Currency:                  "CNY",
				UnitPricePerMillionTokens: "46",
				CNYPerUSD:                 "7.3",
				GroupRatio:                1,
			}},
		},
	}

	resolution, err := adaptor.ResolveTaskBilling(t.Context(), task)

	require.NoError(t, err)
	assert.Equal(t, int64(108871), resolution.TotalTokens)
	assert.Equal(t, 343018, resolution.ActualQuota)
	assert.Equal(t, "999.000000", resolution.SupplierPrice)
	assert.Equal(t, "5.0", resolution.SupplierAmountPaid)
}

func TestQuotaFromDomesticUsageSaturatesOversizedProviderTokens(t *testing.T) {
	quota, clamp, err := quotaFromDomesticUsage(int64(^uint64(0)>>1), &model.TaskProviderBillingSnapshot{
		Provider:                  providerName,
		Currency:                  "CNY",
		UnitPricePerMillionTokens: "51",
		CNYPerUSD:                 "7.3",
		GroupRatio:                1,
	})

	require.NoError(t, err)
	assert.Equal(t, common.MaxQuota, quota)
	require.NotNil(t, clamp)
	assert.Equal(t, "QuotaFromDecimal", clamp.Op)
}

func seedanceTestContext(t *testing.T, body string) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
}

func timeNowUnix() int64 {
	return 1_784_169_600
}

var _ channel.TaskBillingEstimator = (*TaskAdaptor)(nil)
