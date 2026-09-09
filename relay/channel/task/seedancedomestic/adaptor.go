package seedancedomestic

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const (
	generatePath      = "/asset/SdToolApi/generate"
	generateInfoPath  = "/asset/SdToolApi/generate-info"
	billListPath      = "/asset/SdToolApi/ListSplitBillDetail"
	requestContextKey = "seedance_domestic_request"
)

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
	proxy   string
}

func (a *TaskAdaptor) SupportsTaskBilling(channelType int, modelName string) bool {
	return channelType == constant.ChannelTypeSeedanceDomestic && modelName == defaultModel
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = strings.TrimSpace(info.ApiKey)
	a.baseURL = info.ChannelBaseUrl
	if a.baseURL == "" {
		a.baseURL = constant.ChannelBaseURLs[constant.ChannelTypeSeedanceDomestic]
	}
	a.proxy = info.ChannelSetting.Proxy
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	var request relaycommon.TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	var optionalFields struct {
		Duration *int `json:"duration"`
	}
	if err := common.UnmarshalBodyReusable(c, &optionalFields); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	normalized, err := normalizeGenerateRequest(&request, optionalFields.Duration)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	relaycommon.StoreTaskRequest(c, info, constant.TaskActionGenerate, request)
	c.Set(requestContextKey, normalized)
	return nil
}

func normalizeGenerateRequest(request *relaycommon.TaskSubmitReq, explicitDuration *int) (*generateRequest, error) {
	metadata := metadataRequest{}
	if err := request.UnmarshalMetadata(&metadata); err != nil {
		return nil, err
	}
	content := metadata.Content
	if len(request.Content) > 0 {
		content = append([]map[string]interface{}{}, request.Content...)
	}
	images := append([]string{}, request.Images...)
	if strings.TrimSpace(request.Image) != "" {
		images = append(images, request.Image)
	}
	for _, image := range images {
		content = append(content, map[string]interface{}{
			"type":      "image_url",
			"image_url": map[string]interface{}{"url": image},
		})
	}
	if prompt := strings.TrimSpace(request.Prompt); prompt != "" {
		filtered := make([]map[string]interface{}, 0, len(content)+1)
		for _, item := range content {
			if item["type"] != "text" {
				filtered = append(filtered, item)
			}
		}
		content = append(filtered, map[string]interface{}{"type": "text", "text": prompt})
	}

	resolution := strings.ToLower(strings.TrimSpace(metadata.Resolution))
	if strings.TrimSpace(request.Resolution) != "" {
		resolution = strings.ToLower(strings.TrimSpace(request.Resolution))
	}
	if resolution == "" {
		resolution = "720p"
	}
	if resolution == "4k" && !seedanceDomestic4KEnabled {
		return nil, fmt.Errorf("resolution 4k is not enabled for this deployment")
	}
	if _, ok := resolutionPixels[resolution]; !ok {
		if !seedanceDomestic4KEnabled {
			return nil, fmt.Errorf("resolution must be 720p or 1080p")
		}
		return nil, fmt.Errorf("resolution must be 720p, 1080p, or 4k")
	}
	ratio := strings.TrimSpace(metadata.Ratio)
	if strings.TrimSpace(request.Ratio) != "" {
		ratio = strings.TrimSpace(request.Ratio)
	}
	if ratio == "" {
		ratio = "adaptive"
	}
	if ratio != "adaptive" && resolutionPixels[resolution][ratio] == 0 {
		return nil, fmt.Errorf("unsupported ratio %q", ratio)
	}

	duration := 5
	if metadata.Dur != nil {
		duration = *metadata.Dur
	}
	if explicitDuration != nil {
		duration = *explicitDuration
	} else if request.Duration != 0 {
		duration = request.Duration
	}
	if strings.TrimSpace(request.Seconds) != "" {
		seconds, err := strconv.Atoi(request.Seconds)
		if err != nil {
			return nil, fmt.Errorf("seconds must be an integer")
		}
		duration = seconds
	}
	if request.Dur != nil {
		duration = *request.Dur
	}
	if duration != -1 && (duration < 4 || duration > 15) {
		return nil, fmt.Errorf("dur must be -1 or an integer between 4 and 15")
	}

	audioStatus := 1
	if metadata.GenerateAudio != nil && !*metadata.GenerateAudio {
		audioStatus = 0
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
		return nil, fmt.Errorf("audio_status must be 0 or 1")
	}

	if err := validateContent(content); err != nil {
		return nil, err
	}
	return &generateRequest{
		Content:     content,
		AudioStatus: audioStatus,
		Resolution:  resolution,
		Ratio:       ratio,
		Dur:         duration,
	}, nil
}

func validateContent(content []map[string]interface{}) error {
	if len(content) == 0 {
		return fmt.Errorf("content or prompt is required")
	}
	images, videos, audios, nonAudio := 0, 0, 0, 0
	for index, item := range content {
		typeName, _ := item["type"].(string)
		switch typeName {
		case "text":
			text, _ := item["text"].(string)
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("content[%d].text is required", index)
			}
			nonAudio++
		case "image_url":
			if contentMediaURL(item, "image_url") == "" {
				return fmt.Errorf("content[%d].image_url.url is required", index)
			}
			images++
			nonAudio++
		case "video_url":
			if contentMediaURL(item, "video_url") == "" {
				return fmt.Errorf("content[%d].video_url.url is required", index)
			}
			videos++
			nonAudio++
		case "audio_url":
			if contentMediaURL(item, "audio_url") == "" {
				return fmt.Errorf("content[%d].audio_url.url is required", index)
			}
			audios++
		default:
			return fmt.Errorf("content[%d].type is unsupported", index)
		}
	}
	if images > 9 || videos > 3 || audios > 3 {
		return fmt.Errorf("content supports at most 9 images, 3 videos, and 3 audio files")
	}
	if nonAudio == 0 {
		return fmt.Errorf("audio input cannot be used without text, image, or video input")
	}
	return nil
}

func contentMediaURL(item map[string]interface{}, field string) string {
	media, ok := item[field].(map[string]interface{})
	if !ok {
		return ""
	}
	value, _ := media["url"].(string)
	return strings.TrimSpace(value)
}

func (a *TaskAdaptor) EstimateTaskBilling(c *gin.Context, info *relaycommon.RelayInfo) (*channel.TaskBillingEstimate, *taskdto.TaskError) {
	request, err := getNormalizedRequest(c)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusBadRequest)
	}
	videoCount := 0
	for _, item := range request.Content {
		if item["type"] == "video_url" {
			videoCount++
		}
	}
	hasVideo := videoCount > 0
	exchangeRate := operation_setting.USDExchangeRate
	if exchangeRate <= 0 {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("USD exchange rate must be positive"), "model_price_error", http.StatusInternalServerError)
	}
	unitPrice := officialUnitPriceCNY(request.Resolution, hasVideo)
	tokens := estimateVideoTokens(request, videoCount)
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
	quota, clamp, err := quotaFromDomesticUsage(tokens, snapshot)
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

func getNormalizedRequest(c *gin.Context) (*generateRequest, error) {
	value, ok := c.Get(requestContextKey)
	if !ok {
		return nil, fmt.Errorf("normalized Seedance request is missing")
	}
	request, ok := value.(*generateRequest)
	if !ok || request == nil {
		return nil, fmt.Errorf("normalized Seedance request is invalid")
	}
	return request, nil
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return buildUpstreamURL(a.baseURL, generatePath)
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, request *http.Request, _ *relaycommon.RelayInfo) error {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Del("Authorization")
	request.Header.Set("lmd-key", strings.TrimSpace(a.apiKey))
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, _ *relaycommon.RelayInfo) (io.Reader, error) {
	request, err := getNormalizedRequest(c)
	if err != nil {
		return nil, err
	}
	data, err := common.Marshal(request)
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
	var envelope upstreamEnvelope[*generateResponse]
	if err := common.Unmarshal(body, &envelope); err != nil {
		return "", body, service.TaskErrorWrapper(err, "unmarshal_response_body_failed", http.StatusBadGateway)
	}
	if envelope.State != 1 || envelope.Data == nil {
		return "", body, service.TaskErrorWrapper(fmt.Errorf("upstream rejected request: %v", envelope.Error), "upstream_error", http.StatusBadRequest)
	}
	id, err := rawInt64(envelope.Data.ID)
	if err != nil || id <= 0 {
		return "", body, service.TaskErrorWrapper(fmt.Errorf("invalid upstream task id"), "invalid_response", http.StatusBadGateway)
	}
	publicTaskData, err := sanitizeProviderTaskID(body, info.PublicTaskID)
	if err != nil {
		return "", body, service.TaskErrorWrapper(err, "sanitize_response_failed", http.StatusBadGateway)
	}
	video := relaydto.NewOpenAIVideo()
	video.ID = info.PublicTaskID
	video.TaskID = info.PublicTaskID
	video.CreatedAt = time.Now().Unix()
	video.Model = info.OriginModelName
	c.JSON(http.StatusOK, video)
	return strconv.FormatInt(id, 10), publicTaskData, nil
}

func (a *TaskAdaptor) FetchTask(baseURL string, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid task_id")
	}
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid task_id")
	}
	return postJSON(baseURL, generateInfoPath, key, proxy, map[string]any{"id": id})
}

func (a *TaskAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	var envelope upstreamEnvelope[*generateInfoResponse]
	if err := common.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.State != 1 || envelope.Data == nil {
		return nil, fmt.Errorf("upstream task query failed: %v", envelope.Error)
	}
	result := &relaycommon.TaskInfo{Url: envelope.Data.VideoURL}
	switch envelope.Data.Status {
	case 0:
		result.Status = string(model.TaskStatusQueued)
		result.Progress = taskcommon.ProgressQueued
	case 1:
		result.Status = string(model.TaskStatusInProgress)
		result.Progress = taskcommon.ProgressInProgress
	case 2:
		result.Status = string(model.TaskStatusSuccess)
		result.Progress = taskcommon.ProgressComplete
	case 3:
		result.Status = string(model.TaskStatusFailure)
		result.Progress = taskcommon.ProgressComplete
		result.Reason = fmt.Sprint(envelope.Data.Message)
	default:
		return nil, fmt.Errorf("unknown upstream task status %d", envelope.Data.Status)
	}
	return result, nil
}

func (a *TaskAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
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

func (a *TaskAdaptor) SanitizeTaskData(task *model.Task, responseBody []byte) ([]byte, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required to sanitize provider data")
	}
	return sanitizeProviderTaskID(responseBody, task.TaskID)
}

func (a *TaskAdaptor) TaskEndpointSnapshot() *model.TaskEndpointSnapshot {
	return &model.TaskEndpointSnapshot{
		BaseURL:   a.baseURL,
		FetchPath: generateInfoPath,
	}
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return "seedance-domestic"
}

func buildUpstreamURL(baseURL string, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid upstream base URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String(), nil
}

func sanitizeProviderTaskID(body []byte, publicTaskID string) ([]byte, error) {
	if strings.TrimSpace(publicTaskID) == "" {
		return nil, fmt.Errorf("public task id is missing")
	}
	var payload map[string]interface{}
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	data, ok := payload["data"].(map[string]interface{})
	if ok {
		data["id"] = publicTaskID
	}
	return common.Marshal(payload)
}

func postJSON(baseURL string, path string, key string, proxy string, payload any) (*http.Response, error) {
	requestURL, err := buildUpstreamURL(baseURL, path)
	if err != nil {
		return nil, err
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("lmd-key", strings.TrimSpace(key))
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(request)
}
