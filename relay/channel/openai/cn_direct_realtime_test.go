package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kittypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type cnResponseFrame struct {
	id     string
	tokens int
}

func runCNResponseHandler(t *testing.T, info *relaycommon.RelayInfo, frames []cnResponseFrame, failure bool) *dto.RealtimeUsage {
	t.Helper()
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("GET", "/v1/realtime", nil)
	connections := make(chan *websocket.Conn, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			connections <- conn
		}
	}))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	defer client.Close()
	gatewayClient := <-connections
	defer gatewayClient.Close()
	gatewayTarget, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	defer gatewayTarget.Close()
	provider := <-connections
	defer provider.Close()
	info.ClientWs, info.TargetWs = gatewayClient, gatewayTarget
	type result struct {
		err   *kittypes.NewAPIError
		usage *dto.RealtimeUsage
	}
	done := make(chan result, 1)
	go func() { err, usage := OpenaiRealtimeHandler(ctx, info); done <- result{err, usage} }()
	for i, frame := range frames {
		// Standard OpenAI response.done carries per-response usage and identity.
		raw, err := common.Marshal(map[string]any{"event_id": fmt.Sprintf("event_%d", i), "type": "response.done", "response": map[string]any{"id": frame.id, "object": "realtime.response", "status": "completed", "usage": map[string]any{"total_tokens": frame.tokens, "input_tokens": frame.tokens, "output_tokens": 0, "input_token_details": map[string]any{"text_tokens": frame.tokens, "audio_tokens": 0, "cached_tokens": 0}, "output_token_details": map[string]any{"text_tokens": 0, "audio_tokens": 0}}}})
		require.NoError(t, err)
		require.NoError(t, provider.WriteMessage(websocket.TextMessage, raw))
		if failure && i == len(frames)-1 {
			break
		}
		require.NoError(t, client.SetReadDeadline(time.Now().Add(10*time.Second)))
		_, _, err = client.ReadMessage()
		require.NoError(t, err)
	}
	if !failure {
		require.NoError(t, provider.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
	}
	select {
	case outcome := <-done:
		if failure {
			require.NotNil(t, outcome.err)
		} else {
			require.Nil(t, outcome.err)
			require.NotNil(t, outcome.usage)
		}
		return outcome.usage
	case <-time.After(10 * time.Second):
		t.Fatal("actual realtime handler did not finish")
		return nil
	}
}

func TestCNDirectRealtimeActualHandlerAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_DIRECT_WALLET_ENABLED", "true")
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	oldRatio, oldGroup := ratio_setting.ModelRatio2JSONString(), ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-entry":1}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch
		_ = ratio_setting.UpdateModelRatioByJSONString(oldRatio)
		_ = ratio_setting.UpdateGroupRatioByJSONString(oldGroup)
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			if dialect == "sqlite" {
				driver = sqlite.Open(filepath.Join(t.TempDir(), "entry.db"))
			} else {
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
					t.Skip("isolated external DB not configured")
				}
				dsn := os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("isolated external DB not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}))
			require.NoError(t, model.MigrateCNFinance(db, "test-cn"))
			previous := model.DB
			model.DB = db
			t.Cleanup(func() { model.DB = previous })
			for _, second := range []int{20, 25} {
				id := "e" + common.GetUUID()[:15]
				user := model.User{Username: id, AffCode: id, Status: common.UserStatusEnabled, Quota: 1000}
				require.NoError(t, db.Create(&user).Error)
				token := model.Token{UserId: user.Id, Key: id, Status: common.TokenStatusEnabled, RemainQuota: 1000}
				require.NoError(t, db.Create(&token).Error)
				channel := model.Channel{Name: id}
				require.NoError(t, db.Create(&channel).Error)
				info := &relaycommon.RelayInfo{RequestId: id, UserId: user.Id, TokenId: token.Id, TokenKey: id, OriginModelName: "direct-entry", UsingGroup: "default", UserGroup: "default", RelayFormat: kittypes.RelayFormatOpenAIRealtime, StartTime: time.Now(), ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, UpstreamModelName: "direct-entry"}}
				info.UserSetting.BillingPreference = "wallet_only"
				info.PriceData.ModelRatio = 1
				info.PriceData.GroupRatioInfo.GroupRatio = 1
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest("GET", "/v1/realtime", nil)
				require.Nil(t, service.PreConsumeBilling(ctx, 10, info))
				first := runCNResponseHandler(t, info, []cnResponseFrame{{"resp_b", second}}, false)
				require.Equal(t, second, first.TotalTokens)
				// Reconstruct the session before finalization: resp_a arrives late,
				// resp_b is replayed, and equal usage under another ID is still charged.
				restored := *info
				restored.Billing, restored.RealtimePreConsumedQuota = nil, 0
				require.Nil(t, service.PreConsumeBilling(ctx, 10, &restored))
				outcome := runCNResponseHandler(t, &restored, []cnResponseFrame{{"resp_a", 20}, {"resp_a", 20}, {"resp_b", second}, {"resp_a", 20}}, false)
				expected := 20 + second
				require.Equal(t, expected, outcome.TotalTokens)
				service.PostWssConsumeQuota(ctx, &restored, "direct-entry", outcome, "")
				service.PostWssConsumeQuota(ctx, &restored, "direct-entry", outcome, "")
				var actual model.User
				var credential model.Token
				var source model.Channel
				require.NoError(t, db.First(&actual, user.Id).Error)
				require.NoError(t, db.First(&credential, token.Id).Error)
				require.NoError(t, db.First(&source, channel.Id).Error)
				require.Equal(t, 1000-expected, actual.Quota)
				require.Equal(t, expected, actual.UsedQuota)
				require.Equal(t, 1, actual.RequestCount)
				require.Equal(t, 1000-expected, credential.RemainQuota)
				require.Equal(t, expected, credential.UsedQuota)
				require.EqualValues(t, expected, source.UsedQuota)
				var receipts int64
				require.NoError(t, db.Model(&model.CNDirectWalletSegment{}).Where("request_id = ?", id).Count(&receipts).Error)
				require.EqualValues(t, 2, receipts)
				page, err := model.ReadCNFacts(context.Background(), db, "", 1000)
				require.NoError(t, err)
				var published model.CNFact
				for _, fact := range page["items"].([]model.CNFact) {
					if fact.RequestID != nil && *fact.RequestID == id {
						published = fact
					}
				}
				require.NotEmpty(t, published.FactID)
				require.Equal(t, fmt.Sprint(expected), *published.Cumulative.Quota)
				if dir := os.Getenv("S4R_TEST_ARTIFACT_DIR"); dir != "" {
					raw, err := common.Marshal(published)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("response-id-canonical-%s-%d.json", dialect, expected)), raw, 0600))
				}
				copyInfo := restored
				copyInfo.RealtimePreConsumedQuota = 0
				frame := &dto.RealtimeUsage{TotalTokens: second, InputTokens: second, InputTokenDetails: dto.InputTokenDetails{TextTokens: second}}
				total, err := service.PreConsumeCNRealtimeResponse(ctx, &copyInfo, "resp_b", frame)
				require.NoError(t, err)
				require.Equal(t, expected, total.TotalTokens)
				require.Error(t, preConsumeUsageWithCumulative(ctx, &copyInfo, frame, total, frame))
				require.NoError(t, db.First(&actual, user.Id).Error)
				require.Equal(t, 1000-expected, actual.Quota)
				conflict := restored
				conflict.RequestId, conflict.Billing = id+"conflict", nil
				require.Nil(t, service.PreConsumeBilling(ctx, 0, &conflict))
				runCNResponseHandler(t, &conflict, []cnResponseFrame{{"resp_x", 20}, {"resp_x", 25}}, true)
				row, err := model.ReadCNDirectWallet(conflict.RequestId, user.Id)
				require.NoError(t, err)
				require.EqualValues(t, 20, row.SegmentQuota)
				require.NotEmpty(t, row.CoverageGap)
				conflict.Billing.Refund(ctx)
				missing := restored
				missing.RequestId, missing.Billing = id+"missing", nil
				require.Nil(t, service.PreConsumeBilling(ctx, 0, &missing))
				runCNResponseHandler(t, &missing, []cnResponseFrame{{"", 20}}, true)
				row, err = model.ReadCNDirectWallet(missing.RequestId, user.Id)
				require.NoError(t, err)
				require.Zero(t, row.SegmentQuota)
				require.NotEmpty(t, row.CoverageGap)
				missing.Billing.Refund(ctx)
			}
		})
	}
}
