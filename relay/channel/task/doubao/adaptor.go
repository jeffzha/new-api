package doubao

import (
	"bytes"
	"fmt"
	"io"
	"math"
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
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// ============================
// Request / Response structures
// ============================

type ContentItem struct {
	Type     string    `json:"type,omitempty"`
	Text     string    `json:"text,omitempty"`
	ImageURL *MediaURL `json:"image_url,omitempty"`
	VideoURL *MediaURL `json:"video_url,omitempty"`
	AudioURL *MediaURL `json:"audio_url,omitempty"`
	Role     string    `json:"role,omitempty"`
}

type MediaURL struct {
	URL string `json:"url,omitempty"`
}

type requestPayload struct {
	Model                 string         `json:"model"`
	Content               []ContentItem  `json:"content,omitempty"`
	CallbackURL           string         `json:"callback_url,omitempty"`
	ReturnLastFrame       *dto.BoolValue `json:"return_last_frame,omitempty"`
	ServiceTier           string         `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *dto.IntValue  `json:"execution_expires_after,omitempty"`
	GenerateAudio         *dto.BoolValue `json:"generate_audio,omitempty"`
	Draft                 *dto.BoolValue `json:"draft,omitempty"`
	Tools                 []struct {
		Type string `json:"type,omitempty"`
	} `json:"tools,omitempty"`
	SafetyIdentifier string         `json:"safety_identifier,omitempty"`
	Priority         *dto.IntValue  `json:"priority,omitempty"`
	Resolution       string         `json:"resolution,omitempty"`
	Ratio            string         `json:"ratio,omitempty"`
	Duration         *dto.IntValue  `json:"duration,omitempty"`
	Frames           *dto.IntValue  `json:"frames,omitempty"`
	Seed             *dto.IntValue  `json:"seed,omitempty"`
	CameraFixed      *dto.BoolValue `json:"camera_fixed,omitempty"`
	Watermark        *dto.BoolValue `json:"watermark,omitempty"`
}

type responsePayload struct {
	ID string `json:"id"` // task_id
}

type responseTask struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Seed            dto.IntValue `json:"seed"`
	Resolution      string       `json:"resolution"`
	Duration        int          `json:"duration"`
	Ratio           string       `json:"ratio"`
	FramesPerSecond int          `json:"framespersecond"`
	ServiceTier     string       `json:"service_tier"`
	Tools           []struct {
		Type string `json:"type"`
	} `json:"tools"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		ToolUsage        struct {
			WebSearch int `json:"web_search"`
		} `json:"tool_usage"`
	} `json:"usage"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// ============================
// Adaptor implementation
// ============================

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	apiKey      string
	baseURL     string
	submitPath  string
	fetchPath   string
}

func (a *TaskAdaptor) SupportsTaskBilling(channelType int, modelName string) bool {
	return channelType == constant.ChannelTypeDoubaoVideo && seedancepricing.SupportsModel(modelName)
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
	a.submitPath, a.fetchPath = resolveEndpointPaths(dto.ChannelOtherSettings{})
	if info.ChannelType == constant.ChannelTypeDoubaoVideo {
		a.submitPath, a.fetchPath = resolveEndpointPaths(info.ChannelOtherSettings)
	}
}

func (a *TaskAdaptor) EstimateTaskBilling(c *gin.Context, info *relaycommon.RelayInfo) (*channel.TaskBillingEstimate, *taskdto.TaskError) {
	request, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusBadRequest)
	}
	payload, err := a.convertToRequestPayload(&request)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusBadRequest)
	}
	videoCount := int64(0)
	for _, item := range payload.Content {
		if item.Type == "video_url" || item.VideoURL != nil {
			videoCount++
		}
	}
	hasVideo := videoCount > 0
	if videoCount > 3 {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("content supports at most 3 video inputs"),
			"model_price_error",
			http.StatusBadRequest,
		)
	}
	resolution := payload.Resolution
	_, ok := seedancepricing.NormalizeResolution(info.OriginModelName, resolution)
	if !ok {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("CNY pricing does not support model %s", info.OriginModelName),
			"model_price_error",
			http.StatusBadRequest,
		)
	}
	effectiveResolution := strings.ToLower(strings.TrimSpace(resolution))
	switch effectiveResolution {
	case "1080p", "4k":
	default:
		effectiveResolution = "720p"
	}
	unitPrice, ok := seedancepricing.GetUnitPriceCNY(info.OriginModelName, resolution, hasVideo)
	if !ok {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("CNY price is not configured for model %s at resolution %s", info.OriginModelName, resolution),
			"model_price_error",
			http.StatusBadRequest,
		)
	}
	exchangeRate := operation_setting.USDExchangeRate
	if exchangeRate <= 0 || math.IsNaN(exchangeRate) || math.IsInf(exchangeRate, 0) {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("USD exchange rate must be positive and finite"),
			"model_price_error",
			http.StatusInternalServerError,
		)
	}
	groupRatio := info.PriceData.GroupRatioInfo.GroupRatio
	if groupRatio < 0 || math.IsNaN(groupRatio) || math.IsInf(groupRatio, 0) {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("group ratio must be non-negative and finite"),
			"model_price_error",
			http.StatusInternalServerError,
		)
	}

	duration := int64(5)
	if payload.Duration != nil {
		duration = int64(*payload.Duration)
	}
	if duration <= 0 || duration > relaycommon.MaxTaskDurationSeconds {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("duration must be between 1 and %d seconds", relaycommon.MaxTaskDurationSeconds),
			"model_price_error",
			http.StatusBadRequest,
		)
	}
	if payload.Frames != nil {
		frames := int64(*payload.Frames)
		maxFrames := int64(relaycommon.MaxTaskDurationSeconds * 24)
		if frames <= 0 || frames > maxFrames {
			return nil, service.TaskErrorWrapperLocal(
				fmt.Errorf("frames must be between 1 and %d", maxFrames),
				"model_price_error",
				http.StatusBadRequest,
			)
		}
		frameDuration := (frames + 23) / 24
		if frameDuration > duration {
			duration = frameDuration
		}
	}
	if hasVideo {
		// Match the established Seedance domestic estimate without trusting
		// media metadata: reserve 15 seconds for each bounded video input.
		duration += videoCount * 15
	}
	pixels := int64(1280 * 720)
	switch effectiveResolution {
	case "1080p":
		pixels = 1920 * 1080
	case "4k":
		pixels = 3840 * 2160
	}
	estimatedTokens := decimal.NewFromInt(duration).
		Mul(decimal.NewFromInt(pixels)).
		Mul(decimal.NewFromInt(24)).
		Div(decimal.NewFromInt(1024)).
		Ceil().
		IntPart()
	snapshot := &model.TaskProviderBillingSnapshot{
		Provider:                    model.TaskBillingProviderDoubaoVideoCNY,
		Currency:                    "CNY",
		UnitPricePerMillionTokens:   unitPrice.String(),
		CNYPerUSD:                   strconv.FormatFloat(exchangeRate, 'f', -1, 64),
		GroupRatio:                  groupRatio,
		Resolution:                  effectiveResolution,
		HasVideoInput:               hasVideo,
		EstimatedTokens:             estimatedTokens,
		AsyncReconciliationRequired: false,
	}
	quota, clamp, err := taskcommon.QuotaFromCNYPerMillionTokens(estimatedTokens, snapshot)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusInternalServerError)
	}
	if clamp != nil {
		info.QuotaClamp = clamp
	}
	return &channel.TaskBillingEstimate{
		PriceData: types.PriceData{
			ModelPrice:     unitPrice.DivRound(decimal.NewFromFloat(exchangeRate), 12).InexactFloat64(),
			Quota:          quota,
			FreeModel:      groupRatio == 0,
			GroupRatioInfo: info.PriceData.GroupRatioInfo,
		},
		Snapshot: snapshot,
	}, nil
}

// ValidateRequestAndSetAction parses body, validates fields and sets default action.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) (taskErr *taskdto.TaskError) {
	// Accept only POST /v1/video/generations as "generate" action.
	return relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate)
}

// BuildRequestURL constructs the upstream URL.
func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return buildUpstreamURL(a.baseURL, a.submitPath)
}

// BuildRequestHeader sets required headers.
func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

// EstimateBilling 根据请求 metadata 中的输出分辨率与是否包含视频输入，返回相对基准价的计费 OtherRatio。
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}
	hasVideo := hasVideoInMetadata(req.Metadata)
	resolution := seedanceResolution(req)
	ratio, ok := GetVideoInputRatio(info.OriginModelName, resolution, hasVideo)
	if !ok || ratio == 1.0 {
		return nil
	}
	return map[string]float64{videoInputRatioKey: ratio}
}

func (a *TaskAdaptor) AdjustBillingOnCompleteChecked(task *model.Task, taskResult *relaycommon.TaskInfo) (int, *common.QuotaClamp, bool, error) {
	if task == nil || taskResult == nil {
		return 0, nil, false, nil
	}
	bc := task.PrivateData.BillingContext
	if bc == nil {
		return 0, nil, false, nil
	}
	if snapshot := bc.ProviderBilling; snapshot != nil && snapshot.Provider == model.TaskBillingProviderDoubaoVideoCNY {
		if taskResult.TotalTokens <= 0 {
			return 0, nil, true, fmt.Errorf("Doubao Video succeeded without usage.total_tokens")
		}
		quota, clamp, err := taskcommon.QuotaFromCNYPerMillionTokens(int64(taskResult.TotalTokens), snapshot)
		return quota, clamp, true, err
	}
	if taskResult.TotalTokens <= 0 {
		return 0, nil, false, nil
	}
	modelName := bc.OriginModelName
	if modelName == "" {
		modelName = task.Properties.OriginModelName
	}
	usdPerMTokens, ok := getVideoCompletionUSDPerMTokens(modelName, bc.OtherRatios)
	if !ok || usdPerMTokens <= 0 || bc.GroupRatio <= 0 {
		return 0, nil, false, nil
	}
	quota, clamp := common.QuotaRoundChecked(float64(taskResult.TotalTokens) / 1_000_000 * usdPerMTokens * common.QuotaPerUnit * bc.GroupRatio)
	return quota, clamp, true, nil
}

func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	quota, _, handled, err := a.AdjustBillingOnCompleteChecked(task, taskResult)
	if err != nil || !handled {
		return 0
	}
	return quota
}

func seedanceResolution(req relaycommon.TaskSubmitReq) string {
	if strings.TrimSpace(req.Resolution) != "" {
		return req.Resolution
	}
	if req.Metadata != nil {
		if resolution, _ := req.Metadata["resolution"].(string); strings.TrimSpace(resolution) != "" {
			return resolution
		}
	}
	return req.Size
}

// hasVideoInMetadata 直接检查 metadata 的 content 数组是否包含 video_url 条目，
// 避免构建完整的上游 requestPayload。
func hasVideoInMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	contentRaw, ok := metadata["content"]
	if !ok {
		return false
	}
	contentSlice, ok := contentRaw.([]interface{})
	if !ok {
		return false
	}
	for _, item := range contentSlice {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if itemMap["type"] == "video_url" {
			return true
		}
		if _, has := itemMap["video_url"]; has {
			return true
		}
	}
	return false
}

// BuildRequestBody converts request into Doubao specific format.
func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}

	body, err := a.convertToRequestPayload(&req)
	if err != nil {
		return nil, errors.Wrap(err, "convert request payload failed")
	}
	if info.IsModelMapped {
		body.Model = info.UpstreamModelName
	} else {
		info.UpstreamModelName = body.Model
	}
	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

// DoRequest delegates to common helper.
func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

// DoResponse handles upstream response, returns taskID etc.
func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *taskdto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	_ = resp.Body.Close()

	// Parse Doubao response
	var dResp responsePayload
	if err := common.Unmarshal(responseBody, &dResp); err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", responseBody), "unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}

	if dResp.ID == "" {
		taskErr = service.TaskErrorWrapper(fmt.Errorf("task_id is empty"), "invalid_response", http.StatusInternalServerError)
		return
	}

	ov := dto.NewOpenAIVideo()
	ov.ID = info.PublicTaskID
	ov.TaskID = info.PublicTaskID
	ov.CreatedAt = time.Now().Unix()
	ov.Model = info.OriginModelName

	c.JSON(http.StatusOK, ov)
	return dResp.ID, responseBody, nil
}

// FetchTask fetch task status
func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	return a.FetchTaskAt(baseUrl, key, a.fetchPath, body, proxy)
}

func (a *TaskAdaptor) FetchTaskAt(baseUrl, key, fetchPath string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	if strings.Count(fetchPath, "{task_id}") != 1 {
		return nil, fmt.Errorf("task fetch endpoint must contain exactly one {task_id} placeholder")
	}
	resolvedPath := strings.Replace(fetchPath, "{task_id}", url.PathEscape(taskID), 1)
	uri, err := buildUpstreamURL(baseUrl, resolvedPath)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) TaskEndpointSnapshot() *model.TaskEndpointSnapshot {
	return &model.TaskEndpointSnapshot{
		BaseURL:   a.baseURL,
		FetchPath: a.fetchPath,
	}
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

func (a *TaskAdaptor) convertToRequestPayload(req *relaycommon.TaskSubmitReq) (*requestPayload, error) {
	r := requestPayload{
		Model:   req.Model,
		Content: []ContentItem{},
	}

	// Add images if present
	if req.HasImage() {
		for _, imgURL := range req.Images {
			r.Content = append(r.Content, ContentItem{
				Type: "image_url",
				ImageURL: &MediaURL{
					URL: imgURL,
				},
			})
		}
	}

	metadata := req.Metadata
	if err := taskcommon.UnmarshalMetadata(metadata, &r); err != nil {
		return nil, errors.Wrap(err, "unmarshal metadata failed")
	}

	if r.Resolution == "" {
		r.Resolution = req.Resolution
	}
	if r.Duration == nil && req.Duration > 0 {
		r.Duration = lo.ToPtr(dto.IntValue(req.Duration))
	}
	if sec, _ := strconv.Atoi(req.Seconds); sec > 0 {
		r.Duration = lo.ToPtr(dto.IntValue(sec))
	}

	r.Content = lo.Reject(r.Content, func(c ContentItem, _ int) bool { return c.Type == "text" })
	r.Content = append(r.Content, ContentItem{
		Type: "text",
		Text: req.Prompt,
	})

	return &r, nil
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	resTask := responseTask{}
	if err := common.Unmarshal(respBody, &resTask); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	taskResult := relaycommon.TaskInfo{
		Code: 0,
	}

	// Map Doubao status to internal status
	switch resTask.Status {
	case "pending", "queued":
		taskResult.Status = model.TaskStatusQueued
		taskResult.Progress = "10%"
	case "processing", "running":
		taskResult.Status = model.TaskStatusInProgress
		taskResult.Progress = "50%"
	case "succeeded":
		taskResult.Status = model.TaskStatusSuccess
		taskResult.Progress = "100%"
		taskResult.Url = resTask.Content.VideoURL
		// 解析 usage 信息用于按倍率计费
		taskResult.CompletionTokens = resTask.Usage.CompletionTokens
		taskResult.TotalTokens = resTask.Usage.TotalTokens
	case "failed":
		taskResult.Status = model.TaskStatusFailure
		taskResult.Progress = "100%"
		taskResult.Reason = resTask.Error.Message
	default:
		// Unknown status, treat as processing
		taskResult.Status = model.TaskStatusInProgress
		taskResult.Progress = "30%"
	}

	return &taskResult, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(originTask *model.Task) ([]byte, error) {
	var dResp responseTask
	if err := common.Unmarshal(originTask.Data, &dResp); err != nil {
		return nil, errors.Wrap(err, "unmarshal doubao task data failed")
	}

	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = originTask.TaskID
	openAIVideo.TaskID = originTask.TaskID
	openAIVideo.Status = originTask.Status.ToVideoStatus()
	openAIVideo.SetProgressStr(originTask.Progress)
	openAIVideo.SetMetadata("url", dResp.Content.VideoURL)
	openAIVideo.CreatedAt = originTask.CreatedAt
	openAIVideo.CompletedAt = originTask.UpdatedAt
	openAIVideo.Model = originTask.Properties.OriginModelName

	if dResp.Status == "failed" {
		openAIVideo.Error = &dto.OpenAIVideoError{
			Message: dResp.Error.Message,
			Code:    dResp.Error.Code,
		}
	}

	return common.Marshal(openAIVideo)
}
