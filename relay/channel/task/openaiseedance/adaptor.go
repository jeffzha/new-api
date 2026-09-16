package openaiseedance

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const (
	channelName       = "OpenAISeedance"
	submitPath        = "/v1/video/generations"
	fetchPath         = "/v1/video/generations/{task_id}"
	defaultModel      = seedancepricing.StandardSeedanceModel // doubao-seedance-2-0-260128
	requestContextKey = "openaiseedance_request"
)

var ModelList = []string{defaultModel}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
	proxy   string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = strings.TrimSpace(info.ApiKey)
	a.baseURL = strings.TrimSpace(info.ChannelBaseUrl)
	a.proxy = info.ChannelSetting.Proxy
}

func (a *TaskAdaptor) GetChannelName() string { return channelName }
func (a *TaskAdaptor) GetModelList() []string { return ModelList }

// SupportsTaskBilling reports whether this channel type provides its own price
// for the model using the shared seedance_video_pricing.prices_cny table —
// the same pricing source as "Seedance Domestic".
func (a *TaskAdaptor) SupportsTaskBilling(_ int, modelName string) bool {
	return seedancepricing.SupportsModel(modelName)
}

func storeTaskRequest(c *gin.Context, request *generateRequest) {
	c.Set(requestContextKey, request)
}

func getTaskRequest(c *gin.Context) (*generateRequest, error) {
	value, ok := c.Get(requestContextKey)
	if !ok {
		return nil, fmt.Errorf("normalized OpenAISeedance request is missing")
	}
	request, ok := value.(*generateRequest)
	if !ok || request == nil {
		return nil, fmt.Errorf("normalized OpenAISeedance request is invalid")
	}
	return request, nil
}

// ValidateRequestAndSetAction accepts an OpenAI-format /v1/video/generations
// body (model + prompt + duration) and normalizes it. Note: the model name is
// resolved after this method returns, so model/resolution validation happens in
// EstimateTaskBilling where info.OriginModelName is already set.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	var request relaycommon.TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" && len(request.Content) == 0 {
		return service.TaskErrorWrapperLocal(fmt.Errorf("prompt is required"), "invalid_request", http.StatusBadRequest)
	}
	normalized := &generateRequest{
		Prompt: prompt,
		Image:  firstReferenceImage(request),
	}
	var metadata requestMetadata
	if err := request.UnmarshalMetadata(&metadata); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	// resolution: metadata first, then top-level, then default 720p.
	resolution := strings.ToLower(strings.TrimSpace(metadata.Resolution))
	if resolution == "" {
		resolution = strings.ToLower(strings.TrimSpace(request.Resolution))
	}
	if resolution == "" {
		resolution = "720p"
	}
	// ratio: metadata first, then top-level, then default adaptive.
	ratio := strings.TrimSpace(metadata.Ratio)
	if ratio == "" {
		ratio = strings.TrimSpace(request.Ratio)
	}
	if ratio == "" {
		ratio = "adaptive"
	}
	// duration: metadata.duration takes priority over the top-level field.
	duration := 5
	if metadata.Duration != nil {
		duration = *metadata.Duration
	} else if metadata.Dur != nil {
		duration = *metadata.Dur
	} else if request.Duration != 0 {
		duration = request.Duration
	}
	if duration != -1 && (duration < 4 || duration > 15) {
		return service.TaskErrorWrapperLocal(fmt.Errorf("duration must be -1 or between 4 and 15"), "invalid_request", http.StatusBadRequest)
	}
	// audio: honor the generate_audio/audio_status params instead of hardcoding.
	audioStatus := 1 // OpenAI default is with-audio
	if metadata.GenerateAudio != nil {
		if *metadata.GenerateAudio {
			audioStatus = 1
		} else {
			audioStatus = 0
		}
	}
	if metadata.AudioStatus != nil {
		audioStatus = *metadata.AudioStatus
	}
	if request.GenerateAudio != nil {
		if *request.GenerateAudio {
			audioStatus = 1
		} else {
			audioStatus = 0
		}
	}
	if request.AudioStatus != nil {
		audioStatus = *request.AudioStatus
	}
	if audioStatus != 0 && audioStatus != 1 {
		return service.TaskErrorWrapperLocal(fmt.Errorf("audio_status must be 0 or 1"), "invalid_request", http.StatusBadRequest)
	}
	normalized.Resolution = resolution
	normalized.Ratio = ratio
	normalized.Duration = duration
	normalized.AudioStatus = audioStatus
	storeTaskRequest(c, normalized)
	return nil
}

// EstimateBilling is unused (provider-billing path via EstimateTaskBilling).
func (a *TaskAdaptor) EstimateBilling(_ *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	return nil
}

func (a *TaskAdaptor) EstimateTaskBilling(c *gin.Context, info *relaycommon.RelayInfo) (*channel.TaskBillingEstimate, *taskdto.TaskError) {
	request, err := getTaskRequest(c)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusBadRequest)
	}
	// info.OriginModelName is resolved by the time the billing estimator runs.
	if !seedancepricing.SupportsModel(info.OriginModelName) {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("model %q is not supported", info.OriginModelName), "model_price_error", http.StatusBadRequest)
	}
	if _, ok := seedancepricing.NormalizeResolution(info.OriginModelName, request.Resolution); !ok {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("unsupported resolution %q", request.Resolution), "model_price_error", http.StatusBadRequest)
	}
	hasVideo := hasVideoInput(c)
	exchangeRate := operation_setting.USDExchangeRate
	if exchangeRate <= 0 {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("USD exchange rate must be positive"), "model_price_error", http.StatusInternalServerError)
	}
	unitPrice, ok := seedancepricing.GetUnitPriceCNY(info.OriginModelName, request.Resolution, hasVideo)
	if !ok {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("CNY price is not configured for model %s at resolution %s", info.OriginModelName, request.Resolution),
			"model_price_error", http.StatusBadRequest)
	}
	tokens := estimateVideoTokens(request.Duration, request.Resolution, request.Ratio)
	snapshot := &model.TaskProviderBillingSnapshot{
		Provider:                    providerName,
		Currency:                    "CNY",
		UnitPricePerMillionTokens:   unitPrice.String(),
		CNYPerUSD:                   decimal.NewFromFloat(exchangeRate).String(),
		GroupRatio:                  info.PriceData.GroupRatioInfo.GroupRatio,
		Resolution:                  request.Resolution,
		HasVideoInput:               hasVideo,
		EstimatedTokens:             tokens,
		AsyncReconciliationRequired: info.PriceData.GroupRatioInfo.GroupRatio > 0,
	}
	quota, clamp, err := quotaFromUsage(tokens, snapshot)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusInternalServerError)
	}
	if clamp != nil {
		info.QuotaClamp = clamp
	}
	priceData := types.PriceData{
		ModelPrice:     unitPrice.Div(decimal.NewFromFloat(exchangeRate)).InexactFloat64(),
		Quota:          quota,
		FreeModel:      info.PriceData.GroupRatioInfo.GroupRatio == 0,
		GroupRatioInfo: info.PriceData.GroupRatioInfo,
	}
	return &channel.TaskBillingEstimate{PriceData: priceData, Snapshot: snapshot}, nil
}

func (a *TaskAdaptor) AdjustBillingOnSubmit(_ *relaycommon.RelayInfo, _ []byte) map[string]float64 {
	return nil
}
func (a *TaskAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int { return 0 }

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return buildUpstreamURL(a.baseURL, submitPath)
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, request *http.Request, _ *relaycommon.RelayInfo) error {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	request, err := getTaskRequest(c)
	if err != nil {
		return nil, err
	}
	// The laomandi/seedance gateway reads generation parameters (duration,
	// resolution, ratio, audio) from the metadata object — top-level values are
	// ignored and fall back to upstream defaults (5s/adaptive/with-audio).
	// Forward them inside metadata (matching the established convention) and keep
	// the prompt/model/image at the top level.
	metadata := map[string]any{
		"duration":       request.Duration,
		"resolution":     request.Resolution,
		"ratio":          request.Ratio,
		"generate_audio": request.AudioStatus == 1,
		"audio_status":   request.AudioStatus,
	}
	outbound := map[string]any{
		"prompt":   request.Prompt,
		"model":    info.UpstreamModelName,
		"metadata": metadata,
	}
	if isAimodelGateway(a.baseURL) {
		// aimodel requires the prompt in its content array as well.
		outbound["content"] = []map[string]string{{"type": "text", "text": request.Prompt}}
	}
	if request.Image != "" {
		outbound["image"] = request.Image
	}
	data, err := common.Marshal(outbound)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, response *http.Response, info *relaycommon.RelayInfo) (string, []byte, *taskdto.TaskError) {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = response.Body.Close()
	var dResp responseTask
	if err := common.Unmarshal(body, &dResp); err != nil {
		return "", body, service.TaskErrorWrapper(err, "unmarshal_response_body_failed", http.StatusBadGateway)
	}
	// The upstream may return the task id in either "id" or "task_id".
	upstreamID := strings.TrimSpace(dResp.ID)
	if upstreamID == "" {
		upstreamID = strings.TrimSpace(dResp.TaskID)
	}
	if upstreamID == "" {
		return "", body, service.TaskErrorWrapper(fmt.Errorf("upstream returned no task id"), "invalid_response", http.StatusBadGateway)
	}
	// Return the public task id to the client, keep the upstream id for polling.
	dResp.ID = info.PublicTaskID
	dResp.TaskID = info.PublicTaskID
	c.JSON(http.StatusOK, dResp)
	return upstreamID, body, nil
}

func (a *TaskAdaptor) ParseResponse(_ *gin.Context, response *http.Response, _ *relaycommon.RelayInfo) (*channel.TaskSubmitResponse, *taskdto.TaskError) {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	var result responseTask
	if err := common.Unmarshal(body, &result); err != nil {
		return nil, service.TaskErrorWrapper(err, "unmarshal_response_body_failed", http.StatusBadGateway)
	}
	upstreamID := strings.TrimSpace(result.ID)
	if upstreamID == "" {
		upstreamID = strings.TrimSpace(result.TaskID)
	}
	if upstreamID == "" {
		return nil, service.TaskErrorWrapper(fmt.Errorf("upstream returned no task id"), "invalid_response", http.StatusBadGateway)
	}
	return &channel.TaskSubmitResponse{UpstreamTaskID: upstreamID, TaskData: body}, nil
}

func (a *TaskAdaptor) FetchTask(baseURL string, key string, task *model.Task, _ string) (*http.Response, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	taskID := task.GetUpstreamTaskID()
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	uri := buildFetchURL(baseURL, taskID)
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client, err := service.GetHttpClientWithProxy(a.proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(_ *model.Task, _ *http.Response, respBody []byte) (*relaycommon.TaskInfo, error) {
	var resTask responseTask
	if err := common.Unmarshal(respBody, &resTask); err != nil {
		return nil, fmt.Errorf("unmarshal task result failed: %w", err)
	}
	result := &relaycommon.TaskInfo{Code: 0}
	switch resTask.Status {
	case "queued", "pending":
		result.Status = model.TaskStatusQueued
		result.Progress = taskcommon.ProgressQueued
	case "processing", "in_progress":
		result.Status = model.TaskStatusInProgress
		result.Progress = taskcommon.ProgressInProgress
	case "completed", "succeeded":
		result.Status = model.TaskStatusSuccess
		result.Url = resTask.Content.VideoURL
		if result.Url == "" {
			result.Url = resTask.ResultURL
		}
		result.Progress = taskcommon.ProgressComplete
	case "failed", "cancelled", "error":
		result.Status = model.TaskStatusFailure
		result.Progress = taskcommon.ProgressComplete
		if resTask.Error != nil {
			result.Reason = resTask.Error.Message
		} else {
			result.Reason = "task failed"
		}
	default:
		return nil, fmt.Errorf("unknown upstream task status %q", resTask.Status)
	}
	if resTask.Progress > 0 && resTask.Progress < 100 {
		result.Progress = fmt.Sprintf("%d%%", resTask.Progress)
	}
	return result, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	video := task.ToOpenAIVideo()
	video.TaskID = task.TaskID
	if task.Status == model.TaskStatusFailure {
		video.Error = &relaydto.OpenAIVideoError{
			Code:    "generation_failed",
			Message: task.FailReason,
		}
	}
	return common.Marshal(video)
}

// ResolveTaskBilling reconciles the final charge using the upstream status
// response's usage.total_tokens (OpenAI-compatible seedance upstreams expose it
// inline), unlike the bill-API approach used by Seedance Domestic.
func (a *TaskAdaptor) ResolveTaskBilling(ctx context.Context, task *model.Task) (*service.TaskBillingResolution, error) {
	if task == nil || task.PrivateData.BillingContext == nil {
		return nil, fmt.Errorf("task billing context is missing")
	}
	snapshot := task.PrivateData.BillingContext.ProviderBilling
	if snapshot == nil || snapshot.Provider != providerName {
		return nil, fmt.Errorf("openaiseedance billing snapshot is missing")
	}
	upstreamID := task.GetUpstreamTaskID()
	if upstreamID == "" {
		return nil, fmt.Errorf("upstream task id is empty")
	}
	total, err := a.queryTotalTokens(ctx, upstreamID)
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return nil, service.ErrTaskBillingRecordNotReady
	}
	quota, _, err := quotaFromUsage(total, snapshot)
	if err != nil {
		return nil, err
	}
	return &service.TaskBillingResolution{
		ActualQuota: quota,
		TotalTokens: total,
	}, nil
}

func (a *TaskAdaptor) queryTotalTokens(ctx context.Context, upstreamID string) (int64, error) {
	uri := buildFetchURL(a.baseURL, upstreamID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Accept", "application/json")
	client, err := service.GetHttpClientWithProxy(a.proxy)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("task status API returned HTTP %d", resp.StatusCode)
	}
	var resTask responseTask
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if err := common.Unmarshal(body, &resTask); err != nil {
		return 0, err
	}
	return int64(resTask.Usage.TotalTokens), nil
}

func hasVideoInput(c *gin.Context) bool {
	// OpenAI-format submissions are treated as text-only (without_video tier).
	return false
}

// firstReferenceImage extracts a single reference image URL from the OpenAI-format
// video request. It checks, in order: the top-level "image" field, the first entry
// of "images", then the first "content"[].image_url.url. An empty result means no
// reference image was supplied (pure text-to-video).
func firstReferenceImage(request relaycommon.TaskSubmitReq) string {
	if url := strings.TrimSpace(request.Image); url != "" {
		return url
	}
	for _, url := range request.Images {
		if url = strings.TrimSpace(url); url != "" {
			return url
		}
	}
	for _, item := range request.Content {
		switch v := item["image_url"].(type) {
		case string:
			if url := strings.TrimSpace(v); url != "" {
				return url
			}
		case map[string]any:
			if url, ok := v["url"].(string); ok && strings.TrimSpace(url) != "" {
				return strings.TrimSpace(url)
			}
		}
	}
	return ""
}

func quotaFromUsage(totalTokens int64, snapshot *model.TaskProviderBillingSnapshot) (int, *common.QuotaClamp, error) {
	if snapshot == nil || snapshot.Provider != providerName {
		return 0, nil, fmt.Errorf("missing OpenAISeedance billing snapshot")
	}
	return taskcommon.QuotaFromCNYPerMillionTokens(totalTokens, snapshot)
}

func buildUpstreamURL(baseURL string, path string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("upstream base URL is empty")
	}
	if !(strings.HasPrefix(baseURL, "http://") || strings.HasPrefix(baseURL, "https://")) {
		baseURL = "https://" + baseURL
	}
	if path == "" {
		path = submitPath
	}
	return baseURL + path, nil
}

func buildFetchURL(baseURL string, taskID string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !(strings.HasPrefix(baseURL, "http://") || strings.HasPrefix(baseURL, "https://")) {
		baseURL = "https://" + baseURL
	}
	return baseURL + strings.Replace(fetchPath, "{task_id}", taskID, 1)
}

func isAimodelGateway(baseURL string) bool {
	baseURL = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(baseURL), "https://"), "http://")
	return strings.EqualFold(strings.TrimRight(baseURL, "/"), "aimodel.szhtp.com")
}
