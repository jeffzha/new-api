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
	text := fmt.Sprintf("[new-api] 用户调用失败\n请求: %s\n接口: %s\n用户 ID: %d\n渠道 ID: %d\n模型: %s\n协议: %s\n状态码: %d\n错误码: %s\n错误类型: %s\n摘要: %s", requestID, path, userID, channelID, modelName, relayFormat, err.StatusCode, err.GetErrorCode(), err.GetErrorType(), common.LocalLogPreview(err.MaskSensitiveError()))
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
	text := fmt.Sprintf("[new-api] 异步任务调用失败\n请求: %s\n接口: %s\n用户 ID: %d\n渠道 ID: %d\n模型: %s\n状态码: %d\n错误码: %s\n摘要: %s", requestID, path, userID, channelID, modelName, err.StatusCode, err.Code, summary)
	go sendFeishuAlert(alertKey(requestID, path, err.Code, modelName), text)
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
