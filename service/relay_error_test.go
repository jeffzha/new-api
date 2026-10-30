package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestShouldRetryRelayErrorHonorsChannelPinOnChannelError(t *testing.T) {
	err := types.NewError(errors.New("channel failed"), types.ErrorCodeChannelNoAvailableKey)
	for _, test := range []struct {
		name      string
		pin       *dto.ChannelPin
		wantRetry bool
	}{
		{name: "unrestricted channel error", wantRetry: true},
		{
			name: "single attempt pin suppresses channel error retry",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt,
			},
			wantRetry: false,
		},
		{
			name: "origin task pin permits retry on the same channel",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel,
			},
			wantRetry: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.pin != nil {
				GetChannelConstraints(c).AddPin(*test.pin)
			}
			assert.Equal(t, test.wantRetry, ShouldRetryRelayError(c, err, 1))
		})
	}
}

func TestProcessChannelErrorMasksDisableReasonAndNotification(t *testing.T) {
	previousDB, previousType := model.DB, common.MainDatabaseType()
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousAutoDisable, previousErrorLog := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	previousNotifyLimit := constant.NotifyLimitCount
	previousClient, previousWorker := httpClient, system_setting.WorkerUrl
	fetch := system_setting.GetFetchSetting()
	previousFetch := *fetch
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = previousAutoDisable, previousErrorLog
		constant.NotifyLimitCount = previousNotifyLimit
		httpClient, system_setting.WorkerUrl = previousClient, previousWorker
		*fetch = previousFetch
	})
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = true, false
	constant.NotifyLimitCount = 10
	channel := &model.Channel{Name: "relay-review", Key: "fixture-key", Type: 1, Status: common.ChannelStatusEnabled, Group: "default", Models: "test-model"}
	require.NoError(t, channel.Insert())
	notifications := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		notifications <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	httpClient, system_setting.WorkerUrl = server.Client(), ""
	fetch.EnableSSRFProtection = false
	settings, err := common.Marshal(kitdto.UserSetting{NotifyType: kitdto.NotifyTypeWebhook, WebhookUrl: server.URL})
	require.NoError(t, err)
	root := &model.User{Username: "notification-test-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Setting: string(settings)}
	require.NoError(t, database.Create(root).Error)
	notifyKey := fmt.Sprintf("%d:%s:%s", root.Id, formatNotifyType(channel.Id, common.ChannelStatusAutoDisabled), time.Now().Format("2006010215"))
	notifyLimitStore.Delete(notifyKey)
	t.Cleanup(func() { notifyLimitStore.Delete(notifyKey) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream https://private.example.com/path?token=review-token api_key:review-secret"), types.ErrorCodeChannelNoAvailableKey, http.StatusUnauthorized)
	ProcessChannelError(c, types.ChannelError{ChannelId: channel.Id, ChannelName: channel.Name, AutoBan: true}, apiErr, nil)
	var notification WebhookPayload
	select {
	case payload := <-notifications:
		require.NoError(t, common.Unmarshal(payload, &notification))
	case <-time.After(5 * time.Second):
		t.Fatal("automatic channel-disable notification was not delivered")
	}
	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.Status)
	wantReason := "status_code=401, upstream https://***.com/***?token=*** api_key:***"
	assert.Equal(t, wantReason, loaded.GetOtherInfo()["status_reason"])
	assert.Contains(t, notification.Content, wantReason)
	assert.NotContains(t, notification.Content, "review-token")
	assert.NotContains(t, notification.Content, "review-secret")
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

func TestVideoEndpointAlertExplainsClientAction(t *testing.T) {
	text := formatRelayAlert("req-video", "/v1/chat/completions", 208, 0,
		"doubao-seedance-2-0-fast-260128", "openai", http.StatusBadRequest, "invalid_request", "new_api_error",
		"model doubao-seedance-2-0-fast-260128 is a video generation model; use POST /v1/videos instead of /v1/chat/completions", nil, "video endpoint required")
	assert.Contains(t, text, "责任归属: 客户端调用方式")
	assert.Contains(t, text, "计费和请求上游前拒绝")
	assert.Contains(t, text, "POST /v1/videos")
	assert.NotContains(t, text, "待核实")
}

func TestAudioTranscriptionEndpointAlertExplainsClientAction(t *testing.T) {
	text := formatRelayAlert("req-asr", "/v1/chat/completions", 15, 0,
		"fun-asr-flash-2026-06-15", "openai", http.StatusBadRequest, "invalid_request", "new_api_error",
		"model fun-asr-flash-2026-06-15 is an audio transcription model; use POST /v1/audio/transcriptions with multipart/form-data (model and file) instead of /v1/chat/completions", nil, "audio endpoint required")
	assert.Contains(t, text, "责任归属: 客户端调用方式")
	assert.Contains(t, text, "POST /v1/audio/transcriptions")
	assert.Contains(t, text, "model 和 file")
	assert.NotContains(t, text, "配置模型价格")
	assert.NotContains(t, text, "视频模型使用")
}

func TestFeishuRelayAlertSignsMasksAndDeduplicates(t *testing.T) {
	assert.Equal(t, "wSds2BzzFIIGf/WrhUO+NI1q/9j+FRJd3JNHKAq0NZY=", signFeishuAlert(1599360473, "test-secret"))

	previousClient := feishuAlertHTTPClient
	feishuAlertState.Lock()
	previousSeen := feishuAlertState.seen
	feishuAlertState.seen = make(map[string]time.Time)
	feishuAlertState.Unlock()
	t.Cleanup(func() {
		feishuAlertHTTPClient = previousClient
		feishuAlertState.Lock()
		feishuAlertState.seen = previousSeen
		feishuAlertState.Unlock()
	})

	type capturedRequest struct {
		query   string
		payload feishuAlertPayload
	}
	captured := make(chan capturedRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		var payload feishuAlertPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		captured <- capturedRequest{query: r.URL.RawQuery, payload: payload}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	t.Cleanup(server.Close)
	feishuAlertHTTPClient = server.Client()
	t.Setenv("FEISHU_ALERT_WEBHOOK_URL", server.URL+"/hook?existing=1")
	t.Setenv("FEISHU_ALERT_SECRET", "test-secret")

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions?api_key=should-not-leak", nil)
	c.Set(common.RequestIdKey, "req-feishu-1")
	apiErr := types.NewErrorWithStatusCode(
		errors.New("upstream https://private.example.com/path?token=secret-token api_key:secret-key"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	NotifyFeishuRelayError(c, nil, types.RelayFormatOpenAI, apiErr)

	var request capturedRequest
	select {
	case request = <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("Feishu alert was not delivered")
	}
	assert.Equal(t, "existing=1", request.query)
	assert.Equal(t, "text", request.payload.MsgType)
	assert.NotEmpty(t, request.payload.Timestamp)
	assert.NotEmpty(t, request.payload.Sign)
	assert.Contains(t, request.payload.Content.Text, "req-feishu-1")
	assert.Contains(t, request.payload.Content.Text, "/v1/chat/completions")
	assert.NotContains(t, request.payload.Content.Text, "should-not-leak")
	assert.NotContains(t, request.payload.Content.Text, "secret-token")
	assert.NotContains(t, request.payload.Content.Text, "secret-key")

	NotifyFeishuRelayError(c, nil, types.RelayFormatOpenAI, apiErr)
	select {
	case <-captured:
		t.Fatal("duplicate Feishu alert was delivered")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestFeishuChannelProbeReportsFailuresAndRecoveryWithoutHealthyNoise(t *testing.T) {
	previousClient := feishuAlertHTTPClient
	t.Cleanup(func() { feishuAlertHTTPClient = previousClient })
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload feishuAlertPayload
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		messages = append(messages, payload.Content.Text)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(server.Close)
	feishuAlertHTTPClient = server.Client()
	t.Setenv("FEISHU_ALERT_WEBHOOK_URL", server.URL)
	t.Setenv("FEISHU_ALERT_SECRET", "")
	channel := &model.Channel{Id: 98701, Name: "probe-provider", Status: common.ChannelStatusEnabled}
	apiErr := types.NewErrorWithStatusCode(errors.New("api_key:must-not-leak upstream unavailable"), types.ErrorCodeBadResponseStatusCode, 503)

	NotifyFeishuChannelProbe(channel, "test-model", 123, nil, nil)
	require.Empty(t, messages, "healthy checks must not generate notifications")
	NotifyFeishuChannelProbe(channel, "test-model", 123, nil, apiErr)
	NotifyFeishuChannelProbe(channel, "test-model", 124, nil, apiErr)
	require.Len(t, messages, 1, "repeat failures are suppressed for ten minutes")
	assert.Contains(t, messages[0], "渠道巡检失败")
	assert.Contains(t, messages[0], "probe-provider（#98701）")
	assert.Contains(t, messages[0], "test-model")
	assert.Contains(t, messages[0], "503")
	assert.NotContains(t, messages[0], "已自动停用")
	assert.NotContains(t, messages[0], "must-not-leak")
	NotifyFeishuChannelProbe(channel, "test-model", 40, nil, nil)
	NotifyFeishuChannelProbe(channel, "test-model", 40, nil, nil)
	require.Len(t, messages, 2, "recovery is sent once")
	assert.Contains(t, messages[1], "渠道巡检恢复")
	NotifyFeishuChannelProbe(channel, "test-model", 124, nil, apiErr)
	require.Len(t, messages, 3, "a new outage after recovery must alert immediately")
	NotifyFeishuChannelProbe(channel, "test-model", 40, nil, nil)
}

func TestFeishuChannelProbeDistinguishesLocalErrorsFromUpstreamFailures(t *testing.T) {
	previousClient := feishuAlertHTTPClient
	t.Cleanup(func() { feishuAlertHTTPClient = previousClient })
	var message string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload feishuAlertPayload
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		message = payload.Content.Text
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(server.Close)
	feishuAlertHTTPClient = server.Client()
	t.Setenv("FEISHU_ALERT_WEBHOOK_URL", server.URL)
	channel := &model.Channel{Id: 98702, Name: "unsupported-probe", Status: common.ChannelStatusAutoDisabled}
	NotifyFeishuChannelProbe(channel, "video-model", 1, errors.New("channel test is not supported"), nil)
	assert.Contains(t, message, "平台巡检未完成")
	assert.Contains(t, message, "不能据此判定上游故障")
	assert.NotContains(t, message, "上游状态码: 200")
	NotifyFeishuChannelProbe(channel, "video-model", 40, nil, nil)
	assert.Contains(t, message, "渠道巡检恢复")
	assert.Contains(t, message, "本次检测成功不等于渠道已重新启用")
}

func TestFeishuChannelStatusReportsPersistedChannelAndMultiKeyState(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Channel{}))
	connection, err := database.DB()
	require.NoError(t, err)
	previousDB, previousClient := model.DB, feishuAlertHTTPClient
	model.DB = database
	t.Cleanup(func() {
		model.DB, feishuAlertHTTPClient = previousDB, previousClient
		require.NoError(t, connection.Close())
	})
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload feishuAlertPayload
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		messages = append(messages, payload.Content.Text)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(server.Close)
	feishuAlertHTTPClient = server.Client()
	t.Setenv("FEISHU_ALERT_WEBHOOK_URL", server.URL)
	t.Setenv("FEISHU_ALERT_SECRET", "")
	channel := &model.Channel{Name: "status-provider", Status: common.ChannelStatusAutoDisabled}
	require.NoError(t, database.Create(channel).Error)
	notifyFeishuChannelStatus(channel.Id, channel.Name, "api_key:must-not-leak upstream failed", false)
	require.Len(t, messages, 1)
	assert.Contains(t, messages[0], "渠道自动停用已生效")
	assert.Contains(t, messages[0], "渠道已退出正常请求路由")
	assert.NotContains(t, messages[0], "must-not-leak")
	notifyFeishuChannelStatus(channel.Id, channel.Name, "stale enable event", true)
	require.Len(t, messages, 1, "an enable notification must not contradict the persisted status")
	require.NoError(t, database.Model(channel).Update("status", common.ChannelStatusEnabled).Error)
	notifyFeishuChannelStatus(channel.Id, channel.Name, "probe succeeded", true)
	require.Len(t, messages, 2)
	assert.Contains(t, messages[1], "渠道已重新参与正常请求路由")
	channel.ChannelInfo.IsMultiKey = true
	require.NoError(t, database.Model(channel).Update("channel_info", channel.ChannelInfo).Error)
	notifyFeishuChannelStatus(channel.Id, channel.Name, "one key failed", false)
	require.Len(t, messages, 3)
	assert.Contains(t, messages[2], "渠道密钥自动停用已生效")
	assert.Contains(t, messages[2], "其他可用密钥仍可承接请求")
	require.NoError(t, database.Model(channel).Update("status", common.ChannelStatusManuallyDisabled).Error)
	notifyFeishuChannelStatus(channel.Id, channel.Name, "stale disable event", false)
	require.Len(t, messages, 3, "manual disables must not be reported as automatic disables")
}
