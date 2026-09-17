package happyhorse

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = strings.TrimSpace(info.ApiKey)
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
	if a.baseURL == "" {
		a.baseURL = "https://dashscope.aliyuncs.com"
	}
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if err := validate(req, info.OriginModelName); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	relaycommon.StoreTaskRequest(c, info, constant.TaskActionGenerate, req)
	return nil
}

type requestMetadata struct {
	Resolution      string   `json:"resolution,omitempty"`
	Ratio           string   `json:"ratio,omitempty"`
	Duration        *int     `json:"duration,omitempty"`
	Watermark       *bool    `json:"watermark,omitempty"`
	Seed            *int     `json:"seed,omitempty"`
	FirstFrame      string   `json:"first_frame,omitempty"`
	ReferenceImages []string `json:"reference_image,omitempty"`
	Video           string   `json:"video,omitempty"`
	AudioSetting    string   `json:"audio_setting,omitempty"`
}

func validate(req relaycommon.TaskSubmitReq, modelName string) error {
	modelName = resolveModel(strings.TrimSpace(modelName))
	if modelName == "" {
		modelName = resolveModel(strings.TrimSpace(req.Model))
	}
	if !contains(ModelList, modelName) {
		return fmt.Errorf("unsupported HappyHorse model %q", modelName)
	}
	if strings.TrimSpace(req.Prompt) == "" && modelName != modelI2V {
		return fmt.Errorf("prompt is required")
	}
	meta, err := metadataForRequest(req)
	if err != nil {
		return err
	}
	meta.Resolution = strings.ToUpper(strings.TrimSpace(meta.Resolution))
	if meta.Resolution == "" {
		meta.Resolution = "720P"
	}
	if !resolutions[meta.Resolution] {
		return fmt.Errorf("resolution must be 480P, 720P, or 1080P")
	}
	if meta.Duration != nil && (*meta.Duration < 3 || *meta.Duration > 15) {
		return fmt.Errorf("duration must be between 3 and 15 seconds")
	}
	if meta.Seed != nil && (*meta.Seed < 0 || *meta.Seed > 2147483647) {
		return fmt.Errorf("seed must be between 0 and 2147483647")
	}
	switch modelName {
	case modelEdit, modelEdit11:
		if meta.Resolution != "720P" && meta.Resolution != "1080P" {
			return fmt.Errorf("video edit supports only 720P or 1080P")
		}
		if meta.Duration != nil || meta.Ratio != "" {
			return fmt.Errorf("video edit does not support duration or ratio")
		}
		if strings.TrimSpace(meta.Video) == "" {
			return fmt.Errorf("video is required")
		}
		if len(meta.ReferenceImages) > 5 {
			return fmt.Errorf("video edit supports at most 5 reference images")
		}
		if meta.AudioSetting != "" && meta.AudioSetting != "auto" && meta.AudioSetting != "origin" {
			return fmt.Errorf("audio_setting must be auto or origin")
		}
	case modelI2V:
		if strings.TrimSpace(meta.FirstFrame) == "" {
			return fmt.Errorf("first_frame is required")
		}
		if len(meta.ReferenceImages) > 1 {
			return fmt.Errorf("image-to-video requires exactly one first_frame")
		}
		if !validImageReference(meta.FirstFrame) {
			return fmt.Errorf("first_frame must be an HTTP(S) URL or image data URI")
		}
		if meta.Ratio != "" {
			return fmt.Errorf("image-to-video does not support ratio")
		}
	case modelR2V:
		if len(meta.ReferenceImages) < 1 || len(meta.ReferenceImages) > 9 {
			return fmt.Errorf("reference_image must contain 1 to 9 images")
		}
		if !strings.Contains(req.Prompt, "[Image ") {
			return fmt.Errorf("prompt must reference images with [Image N]")
		}
		for _, image := range meta.ReferenceImages {
			if !validImageReference(image) {
				return fmt.Errorf("reference_image must contain HTTP(S) URLs or image data URIs")
			}
		}
	default:
		if meta.Ratio != "" && !ratios[meta.Ratio] {
			return fmt.Errorf("unsupported ratio %q", meta.Ratio)
		}
	}
	if isEditModel(modelName) && !validVideoReference(meta.Video) {
		return fmt.Errorf("video must be an HTTP(S) URL")
	}
	if isEditModel(modelName) {
		for _, image := range meta.ReferenceImages {
			if !validImageReference(image) {
				return fmt.Errorf("reference_image must contain HTTP(S) URLs or image data URIs")
			}
		}
	}
	return nil
}

func metadataForRequest(req relaycommon.TaskSubmitReq) (requestMetadata, error) {
	meta := requestMetadata{}
	if err := req.UnmarshalMetadata(&meta); err != nil {
		return meta, err
	}
	if meta.Resolution == "" {
		meta.Resolution = req.Resolution
	}
	if meta.Ratio == "" {
		meta.Ratio = req.Ratio
	}
	if meta.Duration == nil {
		if req.Dur != nil {
			meta.Duration = req.Dur
		} else if req.Duration != 0 {
			value := req.Duration
			meta.Duration = &value
		}
	}
	if meta.FirstFrame == "" {
		meta.FirstFrame = strings.TrimSpace(req.Image)
		if meta.FirstFrame == "" && len(req.Images) > 0 {
			meta.FirstFrame = strings.TrimSpace(req.Images[0])
		}
	}
	if len(meta.ReferenceImages) == 0 {
		meta.ReferenceImages = append([]string(nil), req.Images...)
	}
	if meta.Video == "" {
		meta.Video = strings.TrimSpace(req.InputReference)
	}
	return meta, nil
}

func validHTTPImageReference(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "data:image/") {
		return true
	}
	if strings.HasPrefix(value, "data:image/") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(value), "data:image/") {
		return true
	}
	u, err := url.Parse(value)
	return strings.HasPrefix(strings.ToLower(value), "data:image/") || (err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "")
}

func validImageReference(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(strings.ToLower(value), "data:") || validHTTPImageReference(value)
}

func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}
	meta, err := metadataForRequest(req)
	if err != nil {
		return nil
	}
	duration := 5
	if meta.Duration != nil {
		duration = *meta.Duration
	}
	modelName := resolveModel(req.Model)
	if info != nil && strings.TrimSpace(info.OriginModelName) != "" {
		modelName = resolveModel(info.OriginModelName)
	}
	if isEditModel(modelName) {
		// The provider charges input + output duration. Without media metadata,
		// reserve the bounded maximum (15s processed input + 15s output) and
		// reconcile to usage.duration once the task completes.
		duration = 30
	}
	resolution := strings.ToUpper(meta.Resolution)
	if resolution == "" {
		resolution = "720P"
	}
	ratio := resolutionRatio(modelT2V, resolution)
	if isEditModel(resolveModel(req.Model)) {
		ratio = resolutionRatio(modelEdit, resolution)
	}
	return map[string]float64{"seconds": float64(duration), "resolution-" + resolution: ratio}
}

// AdjustBillingOnComplete reconciles the pre-charge with the provider's
// measured duration. HappyHorse returns usage.duration as a floating-point
// number; video-edit uses the input+output duration represented by that field.
func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, result *relaycommon.TaskInfo) int {
	quota, _, handled, _ := a.AdjustBillingOnCompleteChecked(task, result)
	if !handled {
		return 0
	}
	return quota
}

func (a *TaskAdaptor) AdjustBillingOnCompleteChecked(task *model.Task, result *relaycommon.TaskInfo) (int, *common.QuotaClamp, bool, error) {
	if task == nil || result == nil || result.Status != model.TaskStatusSuccess || len(result.UsageFacts) == 0 {
		return 0, nil, false, nil
	}
	billing := task.PrivateData.BillingContext
	if billing == nil || len(billing.OtherRatios) == 0 {
		return 0, nil, false, nil
	}
	duration, ok := usageFloat(result.UsageFacts["duration"])
	if !ok || duration <= 0 || duration > 15*6 {
		return 0, nil, false, nil
	}
	ratio := make(map[string]float64, len(billing.OtherRatios))
	for key, value := range billing.OtherRatios {
		ratio[key] = value
	}
	ratio["seconds"] = duration
	base := float64(task.Quota)
	for _, value := range billing.OtherRatios {
		if value > 0 {
			base /= value
		}
	}
	for _, value := range ratio {
		base *= value
	}
	quota, clamp := common.QuotaFromFloatChecked(base)
	return quota, clamp, true, nil
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return joinURL(a.baseURL, submitPath)
}
func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-DashScope-Async", "enable")
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}
	modelName := resolveModel(info.UpstreamModelName)
	if modelName == "" {
		modelName = resolveModel(info.OriginModelName)
	}
	if modelName == "" {
		modelName = resolveModel(req.Model)
	}
	meta, err := metadataForRequest(req)
	if err != nil {
		return nil, err
	}
	meta.Resolution = strings.ToUpper(meta.Resolution)
	if meta.Resolution == "" {
		meta.Resolution = "720P"
	}
	p := &parameters{Resolution: meta.Resolution, Ratio: meta.Ratio, Duration: meta.Duration, Watermark: meta.Watermark, Seed: meta.Seed, AudioSetting: meta.AudioSetting}
	if isEditModel(modelName) {
		p.Duration = nil
		p.Ratio = ""
	}
	body := request{Model: modelName, Input: input{Prompt: req.Prompt, FirstFrame: meta.FirstFrame, ReferenceImages: meta.ReferenceImages, Video: meta.Video}, Parameters: p}
	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, body)
}
func (a *TaskAdaptor) ParseResponse(_ *gin.Context, resp *http.Response, _ *relaycommon.RelayInfo) (*channel.TaskSubmitResponse, *taskdto.TaskError) {
	if resp == nil || resp.Body == nil {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("response body is nil"), "upstream_error", http.StatusBadGateway)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusBadGateway)
	}
	_ = resp.Body.Close()
	result, err := decodeResponse(body)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "invalid_response", http.StatusBadGateway)
	}
	if result.Code != "" {
		return nil, service.TaskErrorWrapper(fmt.Errorf("%s: %s", result.Code, result.Message), "upstream_error", resp.StatusCode)
	}
	if strings.TrimSpace(result.Output.TaskID) == "" {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("task_id is empty"), "invalid_response", http.StatusBadGateway)
	}
	return &channel.TaskSubmitResponse{UpstreamTaskID: result.Output.TaskID, TaskData: body}, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, task *model.Task, proxy string) (*http.Response, error) {
	if task == nil || strings.TrimSpace(task.GetUpstreamTaskID()) == "" {
		return nil, fmt.Errorf("invalid task")
	}
	uri, err := joinURL(baseURL, "/api/v1/tasks/"+url.PathEscape(task.GetUpstreamTaskID()))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}
func (a *TaskAdaptor) ParseTaskResult(_ *model.Task, _ *http.Response, body []byte) (*relaycommon.TaskInfo, error) {
	result, err := decodeResponse(body)
	if err != nil {
		return nil, err
	}
	info := &relaycommon.TaskInfo{}
	switch strings.ToUpper(result.Output.TaskStatus) {
	case "PENDING":
		info.Status = model.TaskStatusQueued
		info.Progress = taskcommon.ProgressQueued
	case "RUNNING":
		info.Status = model.TaskStatusInProgress
		info.Progress = taskcommon.ProgressInProgress
	case "SUCCEEDED":
		info.Status = model.TaskStatusSuccess
		info.Progress = taskcommon.ProgressComplete
		info.Url = result.Output.VideoURL
	case "FAILED", "CANCELED", "UNKNOWN":
		info.Status = model.TaskStatusFailure
		info.Progress = taskcommon.ProgressComplete
		info.Reason = firstNonEmpty(result.Message, result.Output.Message, "HappyHorse task failed")
	default:
		return nil, fmt.Errorf("unknown task status %q", result.Output.TaskStatus)
	}
	if result.Usage != nil {
		info.UsageFacts = map[string]any{"duration": result.Usage.Duration, "input_video_duration": result.Usage.InputVideoDuration, "output_video_duration": result.Usage.OutputVideoDuration}
	}
	return info, nil
}
func (a *TaskAdaptor) GetModelList() []string { return ModelList }
func (a *TaskAdaptor) GetChannelName() string { return ChannelName }

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	video := task.ToOpenAIVideo()
	video.TaskID = task.TaskID
	if task.Status == model.TaskStatusSuccess && task.GetResultURL() != "" {
		if video.Metadata == nil {
			video.Metadata = make(map[string]any)
		}
		video.Metadata["url"] = task.GetResultURL()
	}
	if task.Status == model.TaskStatusFailure {
		video.Error = &relaydto.OpenAIVideoError{Code: "generation_failed", Message: task.FailReason}
	}
	return common.Marshal(video)
}

func resolveModel(name string) string {
	if name == modelAlias {
		return modelT2V
	}
	return name
}

func isEditModel(name string) bool { return name == modelEdit || name == modelEdit11 }
func (a *TaskAdaptor) TaskEndpointSnapshot() *model.TaskEndpointSnapshot {
	return &model.TaskEndpointSnapshot{BaseURL: a.baseURL, FetchPath: "/api/v1/tasks/{task_id}"}
}

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid upstream base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func validVideoReference(value string) bool {
	u, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

var _ channel.TaskAdaptor = (*TaskAdaptor)(nil)
