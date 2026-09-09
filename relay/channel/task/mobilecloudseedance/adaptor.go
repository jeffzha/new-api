package mobilecloudseedance

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
	proxy   string
	clients sdkClientProvider
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = strings.TrimSpace(info.ApiKey)
	a.baseURL = strings.TrimRight(strings.TrimSpace(info.ChannelBaseUrl), "/")
	a.proxy = strings.TrimSpace(info.ChannelSetting.Proxy)
	if a.clients == nil {
		a.clients = defaultSDKClients
	}
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	if c.Request == nil || c.Request.Method != http.MethodPost || c.Request.URL.Path != "/v1/video/generations" {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("Mobile Cloud Seedance only supports POST /v1/video/generations"),
			"unsupported_endpoint",
			http.StatusNotFound,
		)
	}
	if err := validateBaseURL(a.baseURL); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_channel_config", http.StatusInternalServerError)
	}
	if a.apiKey == "" {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("Mobile Cloud API key is empty"),
			"invalid_channel_config",
			http.StatusInternalServerError,
		)
	}
	if a.proxy != "" {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("Mobile Cloud Seedance SDK does not support channel proxy configuration"),
			"invalid_channel_config",
			http.StatusInternalServerError,
		)
	}
	if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); taskErr != nil {
		return taskErr
	}
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	payload, err := convertRequest(&req)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	c.Set(requestContextKey, payload)
	return nil
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	if err := validateBaseURL(a.baseURL); err != nil {
		return "", err
	}
	return a.baseURL + createTaskPath, nil
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	if info.UpstreamModelName != "" && info.UpstreamModelName != ModelName {
		return nil, fmt.Errorf("Mobile Cloud Seedance only supports upstream model %q", ModelName)
	}
	payload, err := requestFromContext(c)
	if err != nil {
		return nil, err
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, _ *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	var payload map[string]interface{}
	if err := common.DecodeJson(requestBody, &payload); err != nil {
		return nil, fmt.Errorf("decode Mobile Cloud Seedance request: %w", err)
	}
	client, err := a.clientProvider().Get(a.baseURL, a.apiKey, ModelName)
	if err != nil {
		return nil, err
	}
	if err := c.Request.Context().Err(); err != nil {
		return nil, err
	}
	taskID, err := client.CreateVideoGenerationTask(payload)
	if err != nil {
		return nil, fmt.Errorf("create Mobile Cloud Seedance task: %w", err)
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("Mobile Cloud Seedance returned an empty task ID")
	}
	return jsonResponse(http.StatusOK, createTaskResponse{ID: taskID})
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *taskdto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var result createTaskResponse
	if err := common.Unmarshal(responseBody, &result); err != nil {
		return "", nil, service.TaskErrorWrapper(
			errors.Wrapf(err, "body: %s", responseBody),
			"unmarshal_response_body_failed",
			http.StatusInternalServerError,
		)
	}
	if strings.TrimSpace(result.ID) == "" {
		return "", nil, service.TaskErrorWrapper(
			fmt.Errorf("task_id is empty"),
			"invalid_response",
			http.StatusInternalServerError,
		)
	}

	video := relaydto.NewOpenAIVideo()
	video.ID = info.PublicTaskID
	video.TaskID = info.PublicTaskID
	video.CreatedAt = time.Now().Unix()
	video.Model = info.OriginModelName
	c.JSON(http.StatusOK, video)
	return result.ID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	if strings.TrimSpace(proxy) != "" {
		return nil, fmt.Errorf("Mobile Cloud Seedance SDK does not support channel proxy configuration")
	}
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	client, err := a.clientProvider().Get(baseURL, strings.TrimSpace(key), ModelName)
	if err != nil {
		return nil, err
	}
	result, err := client.QueryVideoGenerationTask(taskID)
	if err != nil {
		return nil, fmt.Errorf("query Mobile Cloud Seedance task: %w", err)
	}
	return jsonResponse(http.StatusOK, result)
}

func (a *TaskAdaptor) FetchTaskAt(baseURL, key, fetchPath string, body map[string]any, proxy string) (*http.Response, error) {
	if fetchPath != fetchTaskPath {
		return nil, fmt.Errorf("unsupported Mobile Cloud Seedance fetch path %q", fetchPath)
	}
	return a.FetchTask(baseURL, key, body, proxy)
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var result taskResponse
	if err := common.Unmarshal(respBody, &result); err != nil {
		return nil, errors.Wrap(err, "unmarshal Mobile Cloud Seedance task result")
	}

	taskInfo := &relaycommon.TaskInfo{}
	switch result.Status {
	case "pending", "queued":
		taskInfo.Status = model.TaskStatusQueued
		taskInfo.Progress = "10%"
	case "processing", "running":
		taskInfo.Status = model.TaskStatusInProgress
		taskInfo.Progress = "50%"
	case "succeeded":
		if strings.TrimSpace(result.Content.VideoURL) == "" {
			return nil, fmt.Errorf("Mobile Cloud Seedance succeeded without content.video_url")
		}
		taskInfo.Status = model.TaskStatusSuccess
		taskInfo.Progress = "100%"
		taskInfo.Url = result.Content.VideoURL
		taskInfo.CompletionTokens = result.Usage.CompletionTokens
		taskInfo.TotalTokens = result.Usage.TotalTokens
	case "failed", "cancelled", "expired":
		taskInfo.Status = model.TaskStatusFailure
		taskInfo.Progress = "100%"
		taskInfo.Reason = result.Error.Message
	default:
		return nil, fmt.Errorf("Mobile Cloud Seedance returned unknown task status %q", result.Status)
	}
	return taskInfo, nil
}

func (a *TaskAdaptor) TaskEndpointSnapshot() *model.TaskEndpointSnapshot {
	return &model.TaskEndpointSnapshot{
		BaseURL:   a.baseURL,
		FetchPath: fetchTaskPath,
	}
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

func (a *TaskAdaptor) clientProvider() sdkClientProvider {
	if a.clients != nil {
		return a.clients
	}
	return defaultSDKClients
}

func requestFromContext(c *gin.Context) (*requestPayload, error) {
	value, ok := c.Get(requestContextKey)
	if !ok {
		return nil, fmt.Errorf("Mobile Cloud Seedance request not found in context")
	}
	payload, ok := value.(*requestPayload)
	if !ok || payload == nil {
		return nil, fmt.Errorf("invalid Mobile Cloud Seedance request in context")
	}
	return payload, nil
}

func validateBaseURL(baseURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid Mobile Cloud gateway base URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported Mobile Cloud gateway URL scheme %q", parsed.Scheme)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("Mobile Cloud gateway base URL must not contain query or fragment")
	}
	return nil
}

func jsonResponse(status int, value any) (*http.Response, error) {
	data, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(data)),
	}, nil
}
