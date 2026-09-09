package mobilecloudseedance

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSDKClient struct {
	createData   map[string]interface{}
	createTaskID string
	createErr    error
	queryTaskID  string
	queryResult  map[string]interface{}
	queryErr     error
}

func (c *fakeSDKClient) CreateVideoGenerationTask(data map[string]interface{}) (string, error) {
	c.createData = data
	return c.createTaskID, c.createErr
}

func (c *fakeSDKClient) QueryVideoGenerationTask(taskID string) (map[string]interface{}, error) {
	c.queryTaskID = taskID
	return c.queryResult, c.queryErr
}

type fakeSDKClientProvider struct {
	client  sdkClient
	baseURL string
	apiKey  string
	model   string
}

func (p *fakeSDKClientProvider) Get(baseURL, apiKey, modelName string) (sdkClient, error) {
	p.baseURL = baseURL
	p.apiKey = apiKey
	p.model = modelName
	return p.client, nil
}

func TestTaskAdaptorCreateAndQueryUseOfficialSDKClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sdk := &fakeSDKClient{
		createTaskID: "cgt-upstream",
		queryResult: map[string]interface{}{
			"id":     "cgt-upstream",
			"status": "succeeded",
			"content": map[string]interface{}{
				"video_url": "https://example.com/output.mp4",
			},
			"usage": map[string]interface{}{"total_tokens": 12345},
		},
	}
	provider := &fakeSDKClientProvider{client: sdk}
	adaptor := &TaskAdaptor{
		apiKey:  "supplier-key",
		baseURL: "https://zhenze-huhehaote.cmecloud.cn/api/v3",
		clients: provider,
	}

	requestBody := bytes.NewBufferString(`{"model":"doubao-seedance-2.0","content":[{"type":"text","text":"test"}]}`)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
	response, err := adaptor.DoRequest(context, &relaycommon.RelayInfo{}, requestBody)
	require.NoError(t, err)
	require.NotNil(t, response)
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"cgt-upstream"}`, string(responseBody))
	assert.Equal(t, ModelName, provider.model)
	assert.Equal(t, "supplier-key", provider.apiKey)
	assert.Equal(t, ModelName, sdk.createData["model"])

	queryResponse, err := adaptor.FetchTask(
		adaptor.baseURL,
		adaptor.apiKey,
		map[string]any{"task_id": "cgt-upstream"},
		"",
	)
	require.NoError(t, err)
	queryBody, err := io.ReadAll(queryResponse.Body)
	require.NoError(t, err)
	taskInfo, err := adaptor.ParseTaskResult(queryBody)
	require.NoError(t, err)
	assert.Equal(t, "cgt-upstream", sdk.queryTaskID)
	assert.Equal(t, model.TaskStatusSuccess, taskInfo.Status)
	assert.Equal(t, 12345, taskInfo.TotalTokens)
	assert.Equal(t, "https://example.com/output.mp4", taskInfo.Url)
}

func TestTaskAdaptorRejectsOpenAIVideosEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewBufferString(`{"model":"doubao-seedance-2.0","prompt":"test"}`))
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeMobileCloudSeedance},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}
	adaptor := &TaskAdaptor{
		apiKey:  "supplier-key",
		baseURL: "https://zhenze-huhehaote.cmecloud.cn/api/v3",
	}

	taskErr := adaptor.ValidateRequestAndSetAction(context, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, http.StatusNotFound, taskErr.StatusCode)
	assert.Equal(t, "unsupported_endpoint", taskErr.Code)
}

func TestParseTaskResultRejectsUnknownStatusAndMissingSuccessURL(t *testing.T) {
	adaptor := &TaskAdaptor{}

	unknown, err := common.Marshal(map[string]interface{}{"status": "mystery"})
	require.NoError(t, err)
	_, err = adaptor.ParseTaskResult(unknown)
	require.EqualError(t, err, `Mobile Cloud Seedance returned unknown task status "mystery"`)

	succeeded, err := common.Marshal(map[string]interface{}{
		"status": "succeeded",
		"usage":  map[string]interface{}{"total_tokens": 1},
	})
	require.NoError(t, err)
	_, err = adaptor.ParseTaskResult(succeeded)
	require.EqualError(t, err, "Mobile Cloud Seedance succeeded without content.video_url")
}

func TestTaskEndpointSnapshotFreezesMobileCloudGateway(t *testing.T) {
	adaptor := &TaskAdaptor{baseURL: "https://gateway.example.com/api/v3"}
	snapshot := adaptor.TaskEndpointSnapshot()
	assert.Equal(t, "https://gateway.example.com/api/v3", snapshot.BaseURL)
	assert.Equal(t, fetchTaskPath, snapshot.FetchPath)
}
