package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlatformPricingMCPReturnsLiveChannelCostRows(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:pricing-mcp-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	model.DB = db
	require.NoError(t, model.MigrateAgency(db))
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "root-mcp-reader", Role: common.RoleRootUser, Status: common.UserStatusEnabled}).Error)
	channel := model.Channel{Name: "MCP pricing channel", Type: 1, Key: "test-only", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "mcp-test-model", ChannelId: channel.Id, Enabled: true}).Error)
	policy := agencycontract.PlatformPolicy{Revision: 1, ModelPrices: []agencycontract.PlatformModelPrice{{
		OriginModelName: "mcp-test-model",
		AgencyCostBPS:   5500,
		DefaultSalesBPS: 6000,
		ChannelCosts:    []agencycontract.PlatformChannelCost{{ChannelID: channel.Id, PlatformCostBPS: 5000}},
	}}}
	encodedPolicy, err := common.Marshal(policy)
	require.NoError(t, err)
	version := model.AgencyPlatformPriceVersion{ID: 1, Revision: 1, PolicyJSON: string(encodedPolicy), PolicyHash: "test"}
	state := model.AgencyPlatformPriceState{ID: 1, Revision: 1, CurrentVersionID: 1}
	require.NoError(t, db.Create(&version).Error)
	require.NoError(t, db.Create(&state).Error)

	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_platform_model_pricing","arguments":{"model_name":"mcp-test-model"}}}`
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	PlatformPricingMCP(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Result struct {
			StructuredContent struct {
				Items []platformPricingMCPRow `json:"items"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Result.StructuredContent.Items, 1)
	row := response.Result.StructuredContent.Items[0]
	assert.Equal(t, channel.Id, row.ChannelID)
	require.NotNil(t, row.PlatformCostCoefficient)
	require.NotNil(t, row.AgencyCostCoefficient)
	require.NotNil(t, row.DefaultSalesCoefficient)
	assert.Equal(t, 0.5, *row.PlatformCostCoefficient)
	assert.Equal(t, 0.55, *row.AgencyCostCoefficient)
	assert.Equal(t, 0.6, *row.DefaultSalesCoefficient)

	// WorkBuddy namespaces discovered remote MCP tools before calling them.
	// The server must accept that transport-level name without accepting any
	// additional tool beyond the one it advertises.
	namespacedBody := `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"mcp__NEXIGHT pricing__list_platform_model_pricing","arguments":{"model_name":"mcp-test-model"}}}`
	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(namespacedBody))
	PlatformPricingMCP(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "mcp-test-model")
	assert.Equal(t, platformPricingMCPTool, normalizePlatformPricingMCPToolName("mcp__NEXIGHT pricing__list_platform_model_pricing"))
	assert.NotEqual(t, platformPricingMCPTool, normalizePlatformPricingMCPToolName("mcp____list_platform_model_pricing"))
	assert.NotEqual(t, platformPricingMCPTool, normalizePlatformPricingMCPToolName("mcp__NEXIGHT pricing__another_tool"))

	publicRows := publicMCPPricingRows([]platformPricingMCPRow{row})
	require.Len(t, publicRows, 1)
	assert.Zero(t, publicRows[0].ChannelID)
	assert.Empty(t, publicRows[0].ChannelName)
	assert.Nil(t, publicRows[0].PlatformCostCoefficient)
	assert.Nil(t, publicRows[0].AgencyCostCoefficient)
	require.NotNil(t, publicRows[0].DefaultSalesCoefficient)

	modelIntent, err := playgroundPricingIntentFromMessage("查询 mcp-test-model 的价格")
	require.NoError(t, err)
	assert.Equal(t, "mcp-test-model", modelIntent.Query.ModelName)
	modelRows, revision, _, err := queryPlatformPricing(modelIntent.Query)
	require.NoError(t, err)
	require.Len(t, modelRows, 1)
	assert.Contains(t, formatPricingAssistantReply(modelIntent, modelRows, revision), "mcp-test-model 的价格策略")
	languageModelContext := pricingAssistantLanguageModelContext("查询 mcp-test-model", modelIntent, modelRows, revision)
	assert.Contains(t, languageModelContext, "mcp-test-model")
	assert.Contains(t, languageModelContext, "default_sales_coefficient")
	assert.Contains(t, languageModelContext, "platform_cost_coefficient")
	assert.Contains(t, languageModelContext, "agency_cost_coefficient")
	assert.Contains(t, languageModelContext, channel.Name)
	assert.Contains(t, languageModelContext, "channel_id")

	channelIntent, err := playgroundPricingIntentFromMessage("查询 MCP pricing channel 的价格")
	require.NoError(t, err)
	require.NotNil(t, channelIntent.Query.ChannelID)
	assert.Equal(t, channel.Id, *channelIntent.Query.ChannelID)

	helpIntent, err := playgroundPricingIntentFromMessage("你是谁")
	require.NoError(t, err)
	helpReply := formatPricingAssistantReply(helpIntent, nil, 1)
	assert.Contains(t, helpReply, "我可以帮您查询具体模型或渠道")
	assert.NotContains(t, helpReply, "当前策略版本")

	generalPricingIntent, err := playgroundPricingIntentFromMessage("现在模型价格和渠道价格系数是怎么样的")
	require.NoError(t, err)
	assert.True(t, generalPricingIntent.ListRequested)
	generalRows, generalRevision, _, err := queryPlatformPricing(generalPricingIntent.Query)
	require.NoError(t, err)
	require.Len(t, generalRows, 1)
	assert.Contains(t, formatPricingAssistantReply(generalPricingIntent, generalRows, generalRevision), "mcp-test-model")
}

func TestPlatformPricingMCPExternalCredentialHidesChannelAndCostFields(t *testing.T) {
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })

	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:pricing-mcp-public-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	model.DB = db
	require.NoError(t, model.MigrateAgency(db))
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "root-public-mcp-reader", Role: common.RoleRootUser, Status: common.UserStatusEnabled}).Error)
	channel := model.Channel{Name: "Private supplier", Type: 1, Key: "test-only", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "public-test-model", ChannelId: channel.Id, Enabled: true}).Error)
	policyJSON, err := common.Marshal(agencycontract.PlatformPolicy{Revision: 1, ModelPrices: []agencycontract.PlatformModelPrice{{
		OriginModelName: "public-test-model",
		AgencyCostBPS:   5500,
		DefaultSalesBPS: 6000,
		ChannelCosts:    []agencycontract.PlatformChannelCost{{ChannelID: channel.Id, PlatformCostBPS: 5000}},
	}}})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyPlatformPriceVersion{ID: 1, Revision: 1, PolicyJSON: string(policyJSON), PolicyHash: "test"}).Error)
	require.NoError(t, db.Create(&model.AgencyPlatformPriceState{ID: 1, Revision: 1, CurrentVersionID: 1}).Error)

	body := "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":\"list_platform_model_pricing\",\"arguments\":{\"model_name\":\"public-test-model\"}}}"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	context.Set("mcp_access_credential", model.MCPAccessCredential{OwnerUserID: 1, TokenHash: "audit-only"})
	PlatformPricingMCP(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "Private supplier")
	assert.NotContains(t, recorder.Body.String(), "platform_cost_coefficient")
	assert.NotContains(t, recorder.Body.String(), "agency_cost_coefficient")
	assert.NotContains(t, recorder.Body.String(), "channel_id")
	assert.NotContains(t, recorder.Body.String(), "channel_name")
	assert.Contains(t, recorder.Body.String(), "default_sales_coefficient")

	listBody := "{\"jsonrpc\":\"2.0\",\"id\":8,\"method\":\"tools/list\"}"
	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listBody))
	context.Set("mcp_access_credential", model.MCPAccessCredential{OwnerUserID: 1})
	PlatformPricingMCP(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "channel_id")
	assert.NotContains(t, recorder.Body.String(), "platform cost")
}

func TestPlatformPricingQueryRejectsUnknownArgumentsAndDoesNotTreatModelVersionsAsChannels(t *testing.T) {
	_, err := decodePlatformPricingQuery(common.RawMessage(`{"model_name":"x","unexpected":true}`))
	require.Error(t, err)
	assert.Equal(t, 0, channelIDFromMessage("deepseek-v4-flash pricing"))
	assert.Equal(t, 12, channelIDFromMessage("show channel ID 12 pricing"))
	assert.Equal(t, 12, channelIDFromMessage("查询渠道 ID 12 的价格"))
}
