package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	businessservice "github.com/QuantumNous/new-api/service/businessapi"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func businessDatabaseFixture(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db := agencyPricingCatalogFixture(t)
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })
	return db
}

func businessSourcesFixture(t *testing.T) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	previous, existed := common.OptionMap[businessapi.SourcesOption]
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	common.OptionMap[businessapi.SourcesOption] = `{"123":"agent_dev"}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if existed {
			common.OptionMap[businessapi.SourcesOption] = previous
		} else {
			delete(common.OptionMap, businessapi.SourcesOption)
		}
	})
}

func publishBusinessPricing(t *testing.T, db *gorm.DB, revision int64) {
	t.Helper()
	unconfirmed := false
	data, err := common.Marshal(agencycontract.PlatformPolicy{Revision: revision, ModelPrices: []agencycontract.PlatformModelPrice{
		{OriginModelName: "hy3", DefaultSalesBPS: 8000},
		{OriginModelName: "free-model", DefaultSalesBPS: 0},
		{OriginModelName: "per-call", DefaultSalesBPS: 10000, PricingConfirmed: &unconfirmed},
	}})
	require.NoError(t, err)
	version := model.AgencyPlatformPriceVersion{Revision: revision, PolicyJSON: string(data), PolicyHash: "test", CreatedByID: 1}
	require.NoError(t, db.Create(&version).Error)
	require.NoError(t, db.Save(&model.AgencyPlatformPriceState{ID: 1, Revision: revision, CurrentVersionID: version.ID}).Error)
}

func TestBusinessModelPricingIsOptInAndUsesPlatformDefaults(t *testing.T) {
	initModelListColumnNames(t)
	db := businessDatabaseFixture(t)
	bindAgencyPricingCustomer(t, db)
	publishBusinessPricing(t, db, 42)
	for _, query := range []string{"", "?include=pricing"} {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/models"+query, nil)
			c.Set("id", 1)
			ListModels(c, constant.ChannelTypeOpenAI)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var response struct {
				Data []map[string]any `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Len(t, response.Data, 4)
			for _, row := range response.Data {
				if query == "" {
					assert.NotContains(t, row, "pricing")
					assert.NotContains(t, row, "display_name")
					continue
				}
				assert.Equal(t, row["id"], row["display_name"])
				price := row["pricing"].(map[string]any)
				switch row["id"] {
				case "hy3":
					assert.Equal(t, 0.8, price["sales_multiplier"], "customer price 1.25 must not replace the platform default")
					assert.Equal(t, "42", price["version"])
					assert.Equal(t, "active", price["status"])
				case "free-model":
					assert.Equal(t, float64(0), price["sales_multiplier"])
					assert.Equal(t, "active", price["status"])
				default:
					assert.Nil(t, price["sales_multiplier"])
					assert.Nil(t, price["version"])
					assert.Equal(t, "unavailable", price["status"])
				}
			}
		})
	}
	// The enriched response must use precisely the same key-limited catalog.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?include=pricing", nil)
	c.Set("id", 1)
	common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"hy3": true})
	ListModels(c, constant.ChannelTypeOpenAI)
	var limited struct {
		Data []businessModel `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &limited))
	require.Len(t, limited.Data, 1)
	assert.Equal(t, "hy3", limited.Data[0].Id)
	publishBusinessPricing(t, db, 43)
	prices, err := businessservice.PlatformPricing([]string{"hy3"})
	require.NoError(t, err)
	assert.Equal(t, "43", *prices["hy3"].Version)
}

func TestBusinessCallerValidationAndReplay(t *testing.T) {
	businessSourcesFixture(t)
	for _, tc := range []struct {
		name, metadata string
		status         int
	}{
		{"legacy", "", 200},
		{"null", `,"caller_context":null`, 200},
		{"complete", `,"caller_context":{"source_system":"agent_dev","tenant_id":" 001 ","tenant_name":"单位","caller_id":"0","caller_name":"张三"}`, 200},
		{"partial", `,"caller_context":{"tenant_id":"001","caller_id":null}`, 200},
		{"wrong-source", `,"caller_context":{"source_system":"agent_ops"}`, 400},
		{"wrong-type", `,"caller_context":{"tenant_id":1}`, 400},
		{"control", `,"caller_context":{"caller_name":"line\nbreak"}`, 400},
		{"long", `,"caller_context":{"tenant_id":"` + strings.Repeat("界", 129) + `"}`, 400},
		{"array", `,"caller_context":[]`, 400},
		{"unknown", `,"caller_context":{"unknown":"value"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(middleware.BodyStorageCleanup(), func(c *gin.Context) { c.Set("token_id", 123) }, middleware.BusinessCaller())
			router.POST("/v1/chat/completions", func(c *gin.Context) {
				caller, exists := businessapi.FromContext(c)
				require.True(t, exists)
				assert.Equal(t, "agent_dev", caller.SourceSystem)
				if tc.name == "complete" {
					assert.Equal(t, "001", caller.TenantID)
					assert.Equal(t, "0", caller.CallerID)
					assert.Equal(t, "张三", caller.CallerName)
				}
				// Distribution, raw forwarding and retries all consume this body cache.
				for range 3 {
					storage, err := common.GetBodyStorage(c)
					require.NoError(t, err)
					reader, err := storage.NewReader()
					require.NoError(t, err)
					body, err := io.ReadAll(reader)
					require.NoError(t, err)
					require.NoError(t, reader.Close())
					var forwarded map[string]any
					require.NoError(t, common.Unmarshal(body, &forwarded))
					assert.NotContains(t, forwarded, "caller_context")
					assert.Equal(t, float64(0), forwarded["temperature"])
					assert.Equal(t, true, forwarded["stream"])
					assert.Equal(t, float64(9007199254740992), forwarded["sentinel"])
					assert.Contains(t, string(body), "9007199254740993", "unrelated JSON numbers must retain precision")
				}
				c.Status(200)
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"hy3","temperature":0,"stream":true,"sentinel":9007199254740993`+tc.metadata+`}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
		})
	}
}

func TestBusinessCallerSourceRegistrationRejectsInvalidConfiguration(t *testing.T) {
	businessSourcesFixture(t)
	db := businessDatabaseFixture(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	for _, invalid := range []string{`[]`, `{"0":"agent_dev"}`, `{"123":""}`, `{"123":"agent\ndev"}`, `{"123":1}`} {
		assert.Error(t, model.UpdateOption(businessapi.SourcesOption, invalid))
	}
	source, err := businessapi.SourceForToken(123)
	require.NoError(t, err)
	assert.Equal(t, "agent_dev", source)
	assert.NoError(t, model.UpdateOption(businessapi.SourcesOption, `{"123":"agent_ops"}`))
	source, err = businessapi.SourceForToken(123)
	require.NoError(t, err)
	assert.Equal(t, "agent_ops", source)
}

func TestBusinessCallerUnregisteredKeysKeepLegacyCallsAndRejectClaimedSource(t *testing.T) {
	businessSourcesFixture(t)
	for _, tc := range []struct {
		metadata string
		status   int
	}{
		{"", http.StatusOK},
		{`,"caller_context":{"caller_id":"0086"}`, http.StatusOK},
		{`,"caller_context":{"source_system":"agent_dev"}`, http.StatusBadRequest},
	} {
		router := gin.New()
		router.Use(middleware.BodyStorageCleanup(), func(c *gin.Context) { c.Set("token_id", 999) }, middleware.BusinessCaller())
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			caller, _ := businessapi.FromContext(c)
			assert.Empty(t, caller.SourceSystem)
			c.Status(http.StatusOK)
		})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"hy3"`+tc.metadata+`}`))
		router.ServeHTTP(recorder, request)
		assert.Equal(t, tc.status, recorder.Code)
	}
}

func TestBusinessCallerPersistenceAcrossDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			previousRedis := common.RedisEnabled
			common.RedisEnabled = false
			t.Cleanup(func() { common.RedisEnabled = previousRedis })
			var db *gorm.DB
			var err error
			if dialect == "sqlite" {
				db = businessDatabaseFixture(t)
			} else {
				dsn := os.Getenv("BUSINESS_API_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("disposable external database not configured")
				}
				if dialect == "mysql" {
					db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
				} else {
					db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
				}
				require.NoError(t, err)
				previous := model.DB
				model.DB = db
				t.Cleanup(func() {
					model.DB = previous
					sqlDB, err := db.DB()
					require.NoError(t, err)
					require.NoError(t, sqlDB.Close())
				})
			}
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}, &model.AgencyPlatformPriceState{}, &model.AgencyPlatformPriceVersion{}))
			if dialect != "sqlite" {
				require.NoError(t, db.Create(&model.User{Id: 1, Username: "business-test", Password: "unused", AffCode: "business-test", Status: common.UserStatusEnabled}).Error)
			}
			publishBusinessPricing(t, db, 100)
			prices, err := businessservice.PlatformPricing([]string{"hy3", "Hy3", "free-model"})
			require.NoError(t, err)
			assert.Equal(t, 0.8, *prices["hy3"].SalesMultiplier)
			assert.Nil(t, prices["Hy3"].SalesMultiplier)
			assert.Equal(t, float64(0), *prices["free-model"].SalesMultiplier)
			oldLogDB, oldEnabled, oldExport := model.LOG_DB, common.LogConsumeEnabled, common.DataExportEnabled
			model.LOG_DB, common.LogConsumeEnabled, common.DataExportEnabled = db, true, false
			t.Cleanup(func() {
				model.LOG_DB, common.LogConsumeEnabled, common.DataExportEnabled = oldLogDB, oldEnabled, oldExport
			})
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Set("username", "business-test")
			c.Set(businessapi.CallerKey, businessapi.Caller{SourceSystem: "agent_dev", TenantID: "001", CallerID: "0086", CallerName: "张三"})
			for _, stream := range []bool{false, true} {
				other := model.NewLogOther()
				other.SetPublic("group_ratio", 1)
				other.SetRoot("retained_diagnostic", true)
				model.RecordConsumeLog(c, 1, model.RecordConsumeLogParams{ModelName: "hy3", TokenId: 123, IsStream: stream, Other: other})
				model.RecordErrorLog(c, 1, 1, "hy3", "business", "upstream error", 123, 1, stream, "default", other)
			}
			var logs []*model.Log
			require.NoError(t, db.Where("token_id = ?", 123).Find(&logs).Error)
			require.Len(t, logs, 4)
			for _, log := range logs {
				assert.Contains(t, log.Other, `"caller_context"`)
				assert.Contains(t, log.Other, `"caller_id":"0086"`)
				for _, project := range []func([]*model.Log){model.FormatAdminLogs, model.FormatRootLogs} {
					copyLog := *log
					project([]*model.Log{&copyLog})
					assert.NotContains(t, copyLog.Other, "caller_context")
					assert.NotContains(t, copyLog.Other, "0086")
					assert.Contains(t, copyLog.Other, "group_ratio")
				}
			}
			visible, err := model.GetLogByTokenId(123)
			require.NoError(t, err)
			for _, log := range visible {
				assert.NotContains(t, fmt.Sprint(log.Other), "caller_context")
			}
		})
	}
}

func TestBusinessChatRouteStripsCallerBeforeUpstreamAndPersistsAttribution(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	initModelListColumnNames(t)
	db := businessDatabaseFixture(t)
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Token{}))
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.OpsConcurrencyLimit{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, model.MigrateAgency(db))
	oldPrice, oldGroups := ratio_setting.ModelPrice2JSONString(), ratio_setting.GroupRatio2JSONString()
	oldBatch, oldMemory, oldConsume := common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.LogConsumeEnabled
	oldRetry, oldDisable := common.RetryTimes, common.AutomaticDisableChannelEnabled
	common.RetryTimes, common.AutomaticDisableChannelEnabled = 1, false
	oldPass := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.LogConsumeEnabled = false, false, true
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"business-chat":0.01}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	common.OptionMapRWMutex.Lock()
	oldSources := common.OptionMap[businessapi.SourcesOption]
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	common.OptionMap[businessapi.SourcesOption] = `{"123":"agent_dev"}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.LogConsumeEnabled = oldBatch, oldMemory, oldConsume
		common.RetryTimes, common.AutomaticDisableChannelEnabled = oldRetry, oldDisable
		model_setting.GetGlobalSettings().PassThroughRequestEnabled = oldPass
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroups))
		common.OptionMapRWMutex.Lock()
		common.OptionMap[businessapi.SourcesOption] = oldSources
		common.OptionMapRWMutex.Unlock()
	})
	user := model.User{Id: 123, Username: "business-route", AffCode: "business-route", Status: common.UserStatusEnabled, Group: "default", Quota: 1000000, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.Token{Id: 123, UserId: 123, Key: "businessroutekey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.NotContains(t, string(body), "caller_context")
		assert.NotContains(t, string(body), "private-caller")
		if upstreamCalls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"temporary test failure","type":"server_error"}}`)
			return
		}
		var payload struct {
			Stream bool `json:"stream"`
		}
		assert.NoError(t, common.Unmarshal(body, &payload))
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"business-test\",\"object\":\"chat.completion.chunk\",\"model\":\"business-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"business-test\",\"object\":\"chat.completion.chunk\",\"model\":\"business-chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"total_tokens\":11}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"business-test","object":"chat.completion","model":"business-chat","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`)
	}))
	t.Cleanup(upstream.Close)
	channel := model.Channel{Name: "business-route", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Key: "unused", BaseURL: &upstream.URL, Models: "business-chat", Group: "default"}
	require.NoError(t, channel.Insert())
	service.InitHttpClient()
	engine := gin.New()
	engine.Use(middleware.BodyStorageCleanup())
	engine.POST("/v1/chat/completions", middleware.TokenAuth(), middleware.BusinessCaller(), middleware.Distribute(), opsbridge.CallGate(), middleware.RelayUserConcurrency(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAI) })
	for _, pass := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			model_setting.GetGlobalSettings().PassThroughRequestEnabled = pass
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"business-chat","messages":[{"role":"user","content":"OK"}],"stream":%t,"caller_context":{"tenant_id":"tenant_1","caller_id":"private-caller"}}`, stream)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer businessroutekey")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.NotContains(t, recorder.Body.String(), "caller_context")
			if stream {
				assert.Contains(t, recorder.Body.String(), "[DONE]")
			} else {
				assert.Contains(t, recorder.Body.String(), `"chat.completion"`)
			}
		}
	}
	var logs []*model.Log
	require.NoError(t, db.Where("token_id = ? AND type = ?", 123, model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 4)
	for _, log := range logs {
		assert.Contains(t, log.Other, `"source_system":"agent_dev"`)
		assert.Contains(t, log.Other, `"caller_id":"private-caller"`)
	}
	assert.EqualValues(t, 5, upstreamCalls.Load(), "one retry must use the same sanitized request")
	// This must fail at ingress, even when no usable model/channel exists.
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing","caller_context":{"source_system":"agent_ops"}}`))
	request.Header.Set("Authorization", "Bearer businessroutekey")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "does not match")
}
