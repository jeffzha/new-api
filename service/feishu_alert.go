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
		common.SysError("failed to create Feishu alert request: " + err.Error())
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := feishuAlertHTTPClient.Do(request)
	if err != nil {
		common.SysError("failed to send Feishu alert: " + err.Error())
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
