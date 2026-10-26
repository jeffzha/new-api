package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

type feishuAlertPayload struct {
	MsgType string `json:"msg_type"`
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
	Sign      string `json:"sign,omitempty"`
}

type feishuAlertResponse struct {
	Code          int    `json:"code"`
	Message       string `json:"msg"`
	StatusCode    int    `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

var feishuAlertState = struct {
	sync.Mutex
	seen map[string]time.Time
}{seen: make(map[string]time.Time)}

var feishuAlertHTTPClient = &http.Client{Timeout: 5 * time.Second}

var feishuChannelProbeState = struct {
	sync.Mutex
	failed map[int]bool
}{failed: make(map[int]bool)}

// NotifyFeishuChannelProbe is called outside the probe worker so webhook delays
// cannot hold up channel tests or recovery.
func NotifyFeishuChannelProbe(channel *model.Channel, modelName string, milliseconds int64, localErr error, apiErr *types.NewAPIError) {
	if channel == nil || strings.TrimSpace(os.Getenv("FEISHU_ALERT_WEBHOOK_URL")) == "" {
		return
	}
	failed := localErr != nil || apiErr != nil
	feishuChannelProbeState.Lock()
	wasFailed := feishuChannelProbeState.failed[channel.Id]
	if failed {
		feishuChannelProbeState.failed[channel.Id] = true
	} else {
		delete(feishuChannelProbeState.failed, channel.Id)
	}
	feishuChannelProbeState.Unlock()
	if !failed && !wasFailed {
		return
	}
	failureKey := fmt.Sprintf("channel-probe-failure:%d", channel.Id)
	recoveryKey := fmt.Sprintf("channel-probe-recovery:%d", channel.Id)
	title, detail, action, key := "渠道巡检恢复", "本次上游检测成功", "本次检测成功不等于渠道已重新启用；渠道启停以生效通知和后台状态为准", recoveryKey
	if failed {
		title, key = "渠道巡检失败", failureKey
		action = "请在渠道管理查看测试结果及上游服务；是否停用以自动禁用配置和实际状态为准"
		if apiErr != nil {
			detail = fmt.Sprintf("上游状态码: %d\n错误码: %s\n摘要: %s", apiErr.StatusCode, apiErr.GetErrorCode(), common.LocalLogPreview(apiErr.MaskSensitiveError()))
		} else {
			detail = "平台巡检未完成，不能据此判定上游故障\n摘要: " + common.LocalLogPreview(common.MaskSensitiveInfo(localErr.Error()))
			action = "请核对平台巡检支持范围、测试模型和配置；本地检测错误不触发自动恢复"
		}
	}
	if failed != wasFailed {
		feishuAlertState.Lock()
		delete(feishuAlertState.seen, failureKey)
		delete(feishuAlertState.seen, recoveryKey)
		feishuAlertState.Unlock()
	}
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600))
	text := fmt.Sprintf("[new-api] %s\n渠道: %s（#%d）\n测试模型: %s\n耗时: %.3f 秒\n%s\n处理建议: %s\n时间: %s（北京时间）", title, common.LocalLogPreview(channel.Name), channel.Id, modelName, float64(milliseconds)/1000, detail, action, now.Format("2006-01-02 15:04:05"))
	sendFeishuAlert(key, text)
}

func notifyFeishuChannelStatus(channelID int, channelName, reason string, enabling bool) {
	if strings.TrimSpace(os.Getenv("FEISHU_ALERT_WEBHOOK_URL")) == "" {
		return
	}
	channel, err := model.GetChannelById(channelID, false)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to verify channel status for Feishu notification: channel_id=%d", channelID))
		return
	}
	title := "渠道自动停用已生效"
	detail := "渠道已退出正常请求路由"
	if enabling {
		if channel.Status != common.ChannelStatusEnabled {
			return
		}
		title, detail = "渠道自动恢复已生效", "渠道已重新参与正常请求路由"
	} else if channel.Status == common.ChannelStatusEnabled {
		if !channel.ChannelInfo.IsMultiKey {
			return
		}
		title, detail = "渠道密钥自动停用已生效", "多密钥渠道中的异常密钥已停用，其他可用密钥仍可承接请求"
	} else if channel.Status != common.ChannelStatusAutoDisabled {
		return
	}
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600))
	text := fmt.Sprintf("[new-api] %s\n渠道: %s（#%d）\n状态说明: %s\n原因: %s\n时间: %s（北京时间）", title, common.LocalLogPreview(channelName), channelID, detail, common.LocalLogPreview(common.MaskSensitiveInfo(reason)), now.Format("2006-01-02 15:04:05"))
	sendFeishuAlert(fmt.Sprintf("channel-status:%d:%t:%d", channelID, enabling, now.UnixNano()), text)
}

func NotifyFeishuRelayError(c *gin.Context, info *relaycommon.RelayInfo, relayFormat types.RelayFormat, err *types.NewAPIError) {
	if err == nil {
		return
	}
	modelName := ""
	userID, channelID := 0, 0
	if info != nil {
		modelName = info.OriginModelName
		userID = info.UserId
		channelID = info.GetChannelID()
	}
	requestID, path := requestIDForAlert(c), requestPathForAlert(c)
	text := formatRelayAlert(requestID, path, userID, channelID, modelName, string(relayFormat), err.StatusCode, string(err.GetErrorCode()), string(err.GetErrorType()), err.Error(), info, common.LocalLogPreview(err.MaskSensitiveError()))
	go sendFeishuAlert(alertKey(requestID, path, string(err.GetErrorCode()), modelName), text)
}

func NotifyFeishuTaskError(c *gin.Context, info *relaycommon.RelayInfo, err *taskdto.TaskError) {
	if err == nil {
		return
	}
	modelName := ""
	userID, channelID := 0, 0
	if info != nil {
		modelName = info.OriginModelName
		userID = info.UserId
		channelID = info.GetChannelID()
	}
	requestID, path := requestIDForAlert(c), requestPathForAlert(c)
	summary := common.LocalLogPreview(common.MaskSensitiveInfo(err.Message))
	text := formatRelayAlert(requestID, path, userID, channelID, modelName, "task", err.StatusCode, err.Code, "task_error", err.Message, info, summary)
	go sendFeishuAlert(alertKey(requestID, path, err.Code, modelName), text)
}

func formatRelayAlert(requestID, path string, userID, channelID int, modelName, protocol string, statusCode int, code, errorType, message string, info *relaycommon.RelayInfo, summary string) string {
	owner, evidence, action, upstreamModel, endpoint := classifyAlert(code, message, info)
	return fmt.Sprintf("[new-api] 用户调用失败\n请求: %s\n接口: %s\n用户 ID: %d\n渠道 ID: %d\n模型: %s\n协议: %s\n状态码: %d\n错误码: %s\n错误类型: %s\n责任归属: %s\n判断依据: %s\n处理建议: %s\n上游模型: %s\n上游地址: %s\n摘要: %s", requestID, path, userID, channelID, modelName, protocol, statusCode, code, errorType, owner, evidence, action, upstreamModel, endpoint, summary)
}

func classifyAlert(code, message string, info *relaycommon.RelayInfo) (owner, evidence, action, upstreamModel, endpoint string) {
	owner, evidence, action = "待核实", "现有错误信息不足以区分平台与上游", "根据请求 ID 查询渠道侧原始请求和响应"
	if info != nil && info.ChannelMeta != nil {
		upstreamModel = info.ChannelMeta.UpstreamModelName
		endpoint = relaycommon.SanitizeURLForLog(info.ChannelMeta.ChannelBaseUrl)
	}
	lower := strings.ToLower(message)
	switch {
	case code == "invalid_request" && strings.Contains(lower, "audio transcription model; use post /v1/audio/transcriptions"):
		owner, evidence, action = "客户端调用方式", "语音识别模型误用了文本接口，平台已在计费和请求上游前拒绝", "请使用 POST /v1/audio/transcriptions，提交 multipart/form-data 的 model 和 file 字段"
	case code == "invalid_request" && strings.Contains(lower, "video generation model; use post /v1/videos"):
		owner, evidence, action = "客户端调用方式", "视频模型误用了文本接口，平台已在计费和请求上游前拒绝", "请将视频生成请求改为 POST /v1/videos，并使用视频接口参数"
	case code == "model_price_error":
		owner, evidence, action = "平台配置", "平台价格校验未通过，未发送上游请求", "配置模型价格；视频模型使用 /v1/videos"
	case code == "invalid_request" && (strings.Contains(lower, "seconds") || strings.Contains(lower, "prompt is required") || strings.Contains(lower, "cannot unmarshal")):
		owner, evidence, action = "平台兼容/请求转换", "平台参数解析或转换阶段失败，未形成有效上游请求", "核对客户端字段类型和视频请求格式"
	case strings.Contains(lower, "model_not_found") || strings.Contains(lower, "no available channel"):
		owner, evidence, action = "上游渠道配置", "上游明确返回模型不存在或分组无可用渠道", "联系对应渠道开通模型或修正上游模型映射"
	case strings.Contains(lower, "quota insufficient") || strings.Contains(lower, "insufficient_user_quota"):
		owner, evidence, action = "代理余额", "代理钱包额度校验未通过", "补充代理钱包余额并核对预扣账本"
	case strings.Contains(lower, "task_id is empty") || strings.Contains(lower, "upstream returned no task id"):
		owner, evidence, action = "上游响应", "平台收到上游响应但未找到任务 ID", "将请求 ID、渠道 ID 和上游原始响应交给渠道排查"
	case code == "do_request_failed" || strings.Contains(lower, "upstream"):
		owner, evidence, action = "上游连接/服务", "平台已尝试访问上游，但连接或服务返回异常", "按上游地址、请求 ID 和状态码联系渠道"
	}
	return
}

func requestIDForAlert(c *gin.Context) string {
	if c == nil {
		return "unknown"
	}
	if value := strings.TrimSpace(c.GetString(common.RequestIdKey)); value != "" {
		return value
	}
	return "unknown"
}

func requestPathForAlert(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "unknown"
	}
	if path := strings.TrimSpace(c.Request.URL.Path); path != "" {
		return path
	}
	return "unknown"
}

func alertKey(requestID, path, code, modelName string) string {
	return requestID + "|" + path + "|" + code + "|" + modelName
}

func sendFeishuAlert(key, text string) {
	webhook := strings.TrimSpace(os.Getenv("FEISHU_ALERT_WEBHOOK_URL"))
	if webhook == "" || text == "" || !claimFeishuAlert(key) {
		return
	}
	parsed, err := url.Parse(webhook)
	if err != nil || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		common.SysError("invalid FEISHU_ALERT_WEBHOOK_URL")
		return
	}
	secret := strings.TrimSpace(os.Getenv("FEISHU_ALERT_SECRET"))
	payload := feishuAlertPayload{MsgType: "text"}
	payload.Content.Text = text
	if secret != "" {
		timestamp := time.Now().Unix()
		payload.Timestamp = fmt.Sprintf("%d", timestamp)
		payload.Sign = signFeishuAlert(timestamp, secret)
	}
	body, err := common.Marshal(payload)
	if err != nil {
		common.SysError("failed to encode Feishu alert: " + err.Error())
		return
	}
	request, err := http.NewRequest(http.MethodPost, parsed.String(), strings.NewReader(string(body)))
	if err != nil {
		common.SysError("failed to create Feishu alert request")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := feishuAlertHTTPClient.Do(request)
	if err != nil {
		// HTTP client errors can contain the complete secret webhook URL.
		common.SysError("failed to send Feishu alert: transport error")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		common.SysError(fmt.Sprintf("Feishu alert returned HTTP %d", response.StatusCode))
		return
	}
	var result feishuAlertResponse
	if err := common.DecodeJson(response.Body, &result); err != nil {
		if !errors.Is(err, io.EOF) {
			common.SysError("failed to decode Feishu alert response: " + err.Error())
		}
		return
	}
	if result.Code != 0 || result.StatusCode != 0 {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.StatusMessage)
		}
		common.SysError(fmt.Sprintf("Feishu alert rejected: code=%d status_code=%d message=%s", result.Code, result.StatusCode, common.LocalLogPreview(message)))
	}
}

func signFeishuAlert(timestamp int64, secret string) string {
	key := fmt.Sprintf("%d\n%s", timestamp, secret)
	mac := hmac.New(sha256.New, []byte(key))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func claimFeishuAlert(key string) bool {
	now := time.Now()
	feishuAlertState.Lock()
	defer feishuAlertState.Unlock()
	for existing, claimedAt := range feishuAlertState.seen {
		if now.Sub(claimedAt) >= 10*time.Minute {
			delete(feishuAlertState.seen, existing)
		}
	}
	if claimedAt, ok := feishuAlertState.seen[key]; ok && now.Sub(claimedAt) < 10*time.Minute {
		return false
	}
	feishuAlertState.seen[key] = now
	return true
}
