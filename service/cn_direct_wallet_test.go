package service

import (
	"errors"
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
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCNDirectWalletServiceAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_DIRECT_WALLET_ENABLED", "true")
	oldRedis, oldBatch, oldUnit := common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit
	oldSettings := *operation_setting.GetGeneralSetting()
	oldRatios := ratio_setting.ModelRatio2JSONString()
	oldGroup := ratio_setting.GroupRatio2JSONString()
	common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = false, false, 100
	operation_setting.GetGeneralSetting().QuotaDisplayType = "USD"
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-test":1}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = oldRedis, oldBatch, oldUnit
		*operation_setting.GetGeneralSetting() = oldSettings
		_ = ratio_setting.UpdateModelRatioByJSONString(oldRatios)
		_ = ratio_setting.UpdateGroupRatioByJSONString(oldGroup)
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			if dialect == "sqlite" {
				driver = sqlite.Open(filepath.Join(t.TempDir(), "service.db"))
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
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}))
			require.NoError(t, model.MigrateCNFinance(db, "test-cn"))
			oldDB, oldLog := model.DB, model.LOG_DB
			model.DB, model.LOG_DB = db, db
			t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLog })
			id := "s" + common.GetUUID()[:15]
			user := model.User{Username: id, AffCode: id, Status: common.UserStatusEnabled, Quota: 10000}
			require.NoError(t, db.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: id, Status: common.TokenStatusEnabled, RemainQuota: 10000}
			require.NoError(t, db.Create(&token).Error)
			channel := model.Channel{Name: id}
			require.NoError(t, db.Create(&channel).Error)
			info := &relaycommon.RelayInfo{RequestId: id, UserId: user.Id, TokenId: token.Id, TokenKey: id, OriginModelName: "direct-test", UsingGroup: "default", UserGroup: "default", RelayFormat: kittypes.RelayFormatOpenAIRealtime, StartTime: time.Now()}
			info.ChannelMeta = &relaycommon.ChannelMeta{}
			info.ChannelId = channel.Id
			info.UpstreamModelName = "direct-test"
			info.UserSetting.BillingPreference = "wallet_only"
			info.PriceData.ModelRatio = 1
			info.PriceData.GroupRatioInfo.GroupRatio = 1
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/realtime", nil)
			require.Nil(t, PreConsumeBilling(ctx, 10, info))
			first := &dto.RealtimeUsage{TotalTokens: 20, InputTokens: 20, InputTokenDetails: dto.InputTokenDetails{TextTokens: 20}}
			_, _, err = PreWssConsumeQuotaWithResult(ctx, info, first)
			require.ErrorIs(t, err, model.ErrCNConflict)
			accepted, err := PreConsumeCNRealtimeResponse(ctx, info, "resp_a", first)
			require.NoError(t, err)
			require.Equal(t, first, accepted)
			// Reconstruct RelayInfo, preserving only trusted identity and accepted
			// request prices. The persistent response ID deduplicates the same frame.
			restored := *info
			restored.RealtimePreConsumedQuota = 0
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-test":9}`))
			_, err = PreConsumeCNRealtimeResponse(ctx, &restored, "resp_a", first)
			require.NoError(t, err)
			require.Equal(t, 20, restored.RealtimePreConsumedQuota)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-test":1}`))
			second := &dto.RealtimeUsage{TotalTokens: 25, InputTokens: 25, InputTokenDetails: dto.InputTokenDetails{TextTokens: 25}}
			total, err := PreConsumeCNRealtimeResponse(ctx, &restored, "resp_b", second)
			require.NoError(t, err)
			require.Equal(t, 45, total.TotalTokens)
			_, err = PreConsumeCNRealtimeResponse(ctx, &restored, "resp_b", &dto.RealtimeUsage{TotalTokens: 24})
			require.Error(t, err)
			_, err = PreConsumeCNRealtimeResponse(ctx, &restored, "", first)
			require.Error(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-test":9}`))
			restored.PriceData.ModelRatio = 9
			require.NoError(t, FinalizeCNDirectRealtime(&restored, total))
			require.NoError(t, FinalizeCNDirectRealtime(&restored, total))
			require.Error(t, FinalizeCNDirectRealtime(&restored, second))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-test":1}`))
			restored.PriceData.ModelRatio = 1
			var actual model.User
			var credential model.Token
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.NoError(t, db.First(&credential, token.Id).Error)
			require.Equal(t, 9955, actual.Quota)
			require.Equal(t, 45, actual.UsedQuota)
			require.Equal(t, 1, actual.RequestCount)
			require.Equal(t, 9955, credential.RemainQuota)
			require.Equal(t, 45, credential.UsedQuota)
			// No direct primitive can repurpose the realtime request as an extra fee.
			require.ErrorIs(t, PostConsumeQuota(&restored, 5, 0, false), model.ErrCNConflict)
			// The direct ordinary relay entry binds forward identity before I/O.
			relayInfo := *info
			relayInfo.Billing = nil
			relayInfo.RequestId = id + "relay"
			relayInfo.RelayFormat = kittypes.RelayFormatOpenAI
			relayInfo.FinalPreConsumedQuota = 0
			require.NoError(t, BeginCNDirectRelay(&relayInfo))
			require.Error(t, PostConsumeQuota(&relayInfo, 7, 0, false))
			usage := &dto.Usage{PromptTokens: 7, TotalTokens: 7}
			bound, err := BindCNDirectRelayUsage(&relayInfo, usage, true)
			require.NoError(t, err)
			require.True(t, bound)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("direct-service-final-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected final failure"))
				}
			}))
			require.Error(t, PostConsumeQuota(&relayInfo, 7, 0, false))
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.Equal(t, 9955, actual.Quota)
			require.Equal(t, 1, actual.RequestCount)
			require.NoError(t, db.Callback().Create().Remove("direct-service-final-fail"))
			require.NoError(t, PostConsumeQuota(&relayInfo, 7, 0, false))
			require.NoError(t, PostConsumeQuota(&relayInfo, 7, 0, false))
			require.ErrorIs(t, PostConsumeQuota(&relayInfo, 8, 0, false), model.ErrCNConflict)
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.Equal(t, 9948, actual.Quota)
			require.Equal(t, 52, actual.UsedQuota)
			require.Equal(t, 2, actual.RequestCount)
			// Actual free text entry publishes a zero-quota receipt and increments
			// request count once; repeated response handling cannot double the count.
			free := relayInfo
			free.Billing = nil
			free.RequestId = id + "free"
			free.PriceData.ModelRatio = 0
			free.PriceData.GroupRatioInfo.GroupRatio = 0
			require.NoError(t, BeginCNDirectRelay(&free))
			PostTextConsumeQuota(ctx, &free, &dto.Usage{PromptTokens: 2, TotalTokens: 2}, nil)
			PostTextConsumeQuota(ctx, &free, &dto.Usage{PromptTokens: 2, TotalTokens: 2}, nil)
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.Equal(t, 9948, actual.Quota)
			require.Equal(t, 3, actual.RequestCount)
			// Explicit opt-in rejects cache/batch and absent identity; disabled flag
			// retains the original legacy wallet/token delta behavior.
			missing := *info
			missing.Billing = nil
			missing.RequestId = ""
			require.Error(t, BeginCNDirectRealtime(&missing, 0))
			common.RedisEnabled = true
			require.Error(t, BeginCNDirectRealtime(&missing, 0))
			common.RedisEnabled = false
			common.BatchUpdateEnabled = true
			require.Error(t, BeginCNDirectRealtime(&missing, 0))
			common.BatchUpdateEnabled = false
			t.Setenv("OPS_CN_DURABLE_DIRECT_WALLET_ENABLED", "false")
			legacy := *info
			legacy.Billing = nil
			legacy.RequestId = ""
			require.NoError(t, PostConsumeQuota(&legacy, 3, 0, false))
			t.Setenv("OPS_CN_DURABLE_DIRECT_WALLET_ENABLED", "true")
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.Equal(t, 9945, actual.Quota)
			// Each response freezes the existing live calculator result. A final
			// receipt must sum those results across a price change, not reprice.
			mixed := *info
			mixed.RequestId, mixed.Billing = id+"mixed", nil
			require.NoError(t, BeginCNDirectRealtime(&mixed, 0))
			_, err = PreConsumeCNRealtimeResponse(ctx, &mixed, "resp_a", first)
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-test":2}`))
			mixedTotal, err := PreConsumeCNRealtimeResponse(ctx, &mixed, "resp_b", second)
			require.NoError(t, err)
			mixed.PriceData.ModelRatio = 9
			require.NoError(t, FinalizeCNDirectRealtime(&mixed, mixedTotal))
			mixedReceipt, err := model.ReadCNDirectWallet(mixed.RequestId, mixed.UserId)
			require.NoError(t, err)
			require.EqualValues(t, 70, mixedReceipt.Actual)
			require.NoError(t, db.First(&actual, user.Id).Error)
			require.Equal(t, 9875, actual.Quota)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"direct-test":1}`))
		})
	}
}
