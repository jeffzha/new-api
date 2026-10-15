package openai

import (
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
			for _, count := range []int{20, 20, 25} {
				frame := dto.RealtimeEvent{Type: dto.RealtimeEventTypeResponseDone, Response: &dto.RealtimeResponse{Usage: &dto.RealtimeUsage{TotalTokens: count, InputTokens: count, InputTokenDetails: dto.InputTokenDetails{TextTokens: count}}}}
				raw, err := common.Marshal(frame)
				require.NoError(t, err)
				require.NoError(t, provider.WriteMessage(websocket.TextMessage, raw))
				require.NoError(t, client.SetReadDeadline(time.Now().Add(10*time.Second)))
				_, _, err = client.ReadMessage()
				require.NoError(t, err)
			}
			require.NoError(t, provider.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
			select {
			case outcome := <-done:
				require.Nil(t, outcome.err)
				require.NotNil(t, outcome.usage)
				require.Equal(t, 25, outcome.usage.TotalTokens)
				service.PostWssConsumeQuota(ctx, info, "direct-entry", outcome.usage, "")
				service.PostWssConsumeQuota(ctx, info, "direct-entry", outcome.usage, "")
			case <-time.After(10 * time.Second):
				t.Fatal("actual realtime handler did not finish")
			}
			var actual model.User
			var credential model.Token
			var source model.Channel
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.NoError(t, db.First(&credential, token.Id).Error)
			require.NoError(t, db.First(&source, channel.Id).Error)
			require.Equal(t, 975, actual.Quota)
			require.Equal(t, 25, actual.UsedQuota)
			require.Equal(t, 1, actual.RequestCount)
			require.Equal(t, 975, credential.RemainQuota)
			require.Equal(t, 25, credential.UsedQuota)
			require.EqualValues(t, 25, source.UsedQuota)
			var receipts int64
			require.NoError(t, db.Model(&model.CNDirectWalletSegment{}).Where("request_id = ?", id).Count(&receipts).Error)
			require.EqualValues(t, 2, receipts)
			// A process loses its counters: the real admission helper restores the
			// latest cumulative frame from permanent receipts without another debit.
			copyInfo := *info
			copyInfo.RealtimePreConsumedQuota = 0
			total := &dto.RealtimeUsage{}
			frame := &dto.RealtimeUsage{TotalTokens: 25, InputTokens: 25, InputTokenDetails: dto.InputTokenDetails{TextTokens: 25}}
			require.NoError(t, preConsumeUsageWithCumulative(ctx, &copyInfo, frame, total, frame))
			require.Equal(t, 25, total.TotalTokens)
			require.Error(t, preConsumeUsage(ctx, &copyInfo, frame, total))
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.Equal(t, 975, actual.Quota)
		})
	}
}
