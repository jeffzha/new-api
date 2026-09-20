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
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
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
}

func TestPlatformPricingQueryRejectsUnknownArgumentsAndDoesNotTreatModelVersionsAsChannels(t *testing.T) {
	_, err := decodePlatformPricingQuery(common.RawMessage(`{"model_name":"x","unexpected":true}`))
	require.Error(t, err)
	assert.Equal(t, 0, channelIDFromMessage("deepseek-v4-flash pricing"))
	assert.Equal(t, 12, channelIDFromMessage("show channel ID 12 pricing"))
	assert.Equal(t, 12, channelIDFromMessage("查询渠道 ID 12 的价格"))
}
