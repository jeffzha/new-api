package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service/modelmarketplace"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelMarketplaceCatalogContract(t *testing.T) {
	initModelListColumnNames(t)
	db := businessDatabaseFixture(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}))
	oldMemory := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = oldMemory })
	require.NoError(t, db.Create(&model.Vendor{Id: 7, Name: "Example"}).Error)
	require.NoError(t, db.Create(&model.Model{ModelName: "hy3", Description: "Example chat model", Tags: `["chat","tools"]`, VendorID: 7, Status: 1}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "marketplacetest", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 2, UserId: 1, Key: "marketplacerestricted", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, Group: "default", ModelLimitsEnabled: true, ModelLimits: "hy3"}).Error)
	model.InvalidatePricingCache()
	engine := gin.New()
	auth := engine.Group("/v1", middleware.TokenAuth())
	auth.GET("/models", func(c *gin.Context) { ListModels(c, constant.ChannelTypeOpenAI) })
	auth.GET("/model-marketplace", ModelMarketplace)
	auth.GET("/model-marketplace/detail", ModelMarketplace)
	request := func(path, key, etag string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer sk-"+key)
		}
		r.Header.Set("If-None-Match", etag)
		engine.ServeHTTP(w, r)
		return w
	}
	const path = "/v1/model-marketplace"
	w := request(path+"?page_size=100", "marketplacetest", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var page modelmarketplace.Page
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &page))
	models := request("/v1/models", "marketplacetest", "")
	standard := decodeListModelsPayload(t, models)
	want := make([]string, 0, len(standard.Data))
	got := make([]string, 0, len(page.Data))
	for _, item := range standard.Data {
		want = append(want, item.Id)
	}
	for _, item := range page.Data {
		got = append(got, item.ID)
	}
	assert.ElementsMatch(t, want, got)
	assert.Equal(t, len(want), page.Total)
	assert.Equal(t, "private, no-cache", w.Header().Get("Cache-Control"))
	for _, forbidden := range []string{"settlement_bps", "sales_bps", "channel_id", "base_url", "policy_json", "marketplacetest"} {
		assert.NotContains(t, w.Body.String(), forbidden)
	}
	cached := request(path+"?page_size=100", "marketplacetest", w.Header().Get("ETag"))
	assert.Equal(t, 304, cached.Code)
	assert.Empty(t, cached.Body.String())
	assert.Equal(t, 401, request(path, "", w.Header().Get("ETag")).Code, "304 must not bypass auth")
	restricted := request(path, "marketplacerestricted", w.Header().Get("ETag"))
	require.Equal(t, 200, restricted.Code, restricted.Body.String())
	require.NoError(t, common.Unmarshal(restricted.Body.Bytes(), &page))
	require.Len(t, page.Data, 1)
	assert.Equal(t, "hy3", page.Data[0].ID)
	assert.Equal(t, 404, request(path+"/detail?model=per-call", "marketplacerestricted", "").Code)
	detail := request(path+"/detail?model=hy3", "marketplacetest", "")
	require.Equal(t, 200, detail.Code, detail.Body.String())
	var single struct {
		Data modelmarketplace.Item `json:"data"`
	}
	require.NoError(t, common.Unmarshal(detail.Body.Bytes(), &single))
	assert.Equal(t, "Example", single.Data.Vendor.Name)
	assert.Equal(t, []string{"chat", "tools"}, single.Data.Tags)
	assert.Equal(t, "standard", single.Data.Pricing.Scope)
	require.NotNil(t, single.Data.Pricing.Config)
	assert.NotContains(t, detail.Body.String(), "enable_groups")
	assert.NotContains(t, detail.Body.String(), "plugin_key")
	assert.Equal(t, "Example chat model", single.Data.Description)
	first := request(path+"?page_size=1", "marketplacetest", "")
	require.NoError(t, common.Unmarshal(first.Body.Bytes(), &page))
	require.Len(t, page.Data, 1)
	assert.True(t, page.HasMore)
	firstID := page.Data[0].ID
	second := request(path+"?page_size=1&page=2", "marketplacetest", "")
	require.NoError(t, common.Unmarshal(second.Body.Bytes(), &page))
	require.Len(t, page.Data, 1)
	assert.NotEqual(t, firstID, page.Data[0].ID)
	filtered := request(path+"?q=chat&vendor=example&model_type=LLM", "marketplacetest", "")
	require.NoError(t, common.Unmarshal(filtered.Body.Bytes(), &page))
	require.Len(t, page.Data, 1)
	assert.Equal(t, "hy3", page.Data[0].ID)
	empty := request(path+"?q=absent", "marketplacetest", "")
	assert.Contains(t, empty.Body.String(), `"data":[]`)
	for _, suffix := range []string{"?page=0", "?page_size=101", "?page=1&page=2", "?model_type=bad", "?q=", "?include=secret", "/detail", "/detail?model=hy3&model=Hy3", "/detail?model=hy3&page=1"} {
		assert.Equal(t, 400, request(path+suffix, "marketplacetest", "").Code, suffix)
	}
	// A configured channel change is visible on the next query, with no external registry write.
	oldPrice := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"new-embedding":0.01}`))
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "new-embedding", ChannelId: 1, Enabled: true}).Error)
	model.InitChannelCache()
	newest := request(path+"?model_type=embedding", "marketplacetest", "")
	assert.Contains(t, newest.Body.String(), `"id":"new-embedding"`)
	// Metadata and price cache invalidation must change the validator.
	require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "hy3").Update("description", "Updated").Error)
	model.InvalidatePricingCache()
	updated := request(path+"/detail?model=hy3", "marketplacetest", detail.Header().Get("ETag"))
	assert.Equal(t, 200, updated.Code)
	assert.Contains(t, updated.Body.String(), "Updated")
	// A square-hidden model cannot reappear through the external marketplace.
	require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "hy3").Update("status", 0).Error)
	model.InvalidatePricingCache()
	for _, target := range []string{path, "/v1/models"} {
		hidden := request(target, "marketplacetest", "")
		assert.NotContains(t, hidden.Body.String(), `"id":"hy3"`)
	}
	assert.Equal(t, 404, request(path+"/detail?model=hy3", "marketplacetest", "").Code)
}

func TestModelMarketplacePriceRulesPreserveComplexPricing(t *testing.T) {
	zero := 0.0
	schema := map[string]jsplugin.UsageFieldSchema{"input_images": {Type: "number", Unit: "image"}}
	expression := `tier("short",len <= 32000 ? p * 2 + ai * 9 : p * 3) + u("input_images") * 0.02`
	p := model.Pricing{ModelRatio: 2, CompletionRatio: 3, AudioRatio: &zero, BillingMode: "tiered_expr", BillingExpr: expression, BillingUsageSchema: schema, EnableGroup: []string{"private-routing"}, OwnerBy: "internal", BillingPluginVariants: []model.PricingPluginVariant{{PluginKey: "private-plugin", PluginName: "Video", BillingMode: "tiered_expr", BillingExpr: `u("seconds") * 0.1`, BillingUsageSchema: schema}}, ProviderPricing: &model.ProviderPricing{Kind: "video_token_matrix", Currency: "CNY", Unit: "1M_video_tokens", Tiers: []model.VideoTokenPricingTier{{Resolution: "1080p", WithoutVideo: 2, WithVideo: 3}}}}
	full := modelmarketplace.PublicPrice(p, true)
	assert.Equal(t, expression, full.BillingExpr)
	assert.Equal(t, schema, full.BillingUsageSchema)
	require.NotNil(t, full.AudioRatio)
	assert.Zero(t, *full.AudioRatio)
	require.Len(t, full.BillingPluginVariants, 1)
	assert.Equal(t, p.ProviderPricing, full.ProviderPricing)
	summary := modelmarketplace.PublicPrice(p, false)
	assert.Nil(t, summary.BillingPluginVariants)
	raw, err := common.Marshal(full)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-plugin")
	assert.NotContains(t, string(raw), "private-routing")
	assert.Equal(t, "private-plugin", p.BillingPluginVariants[0].PluginKey, "cached input must remain unchanged")
}

func TestModelMarketplaceDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("local engine not configured")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"matrix-embedding":1}`))
			require.NoError(t, db.Create(&model.User{Id: 700, Username: "catalogmatrix", AffCode: "catalogmatrix", Status: common.UserStatusEnabled, Group: "default"}).Error)
			channel := model.Channel{Id: 700, Name: "catalogmatrix", Key: "unused", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(&channel).Error)
			require.NoError(t, db.Create(&model.Ability{ChannelId: 700, Group: "default", Model: "matrix-embedding", Enabled: true}).Error)
			model.InvalidatePricingCache()
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Set("id", 700)
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			})
			engine.GET("/v1/model-marketplace", ModelMarketplace)
			engine.GET("/v1/models", func(c *gin.Context) { ListModels(c, constant.ChannelTypeOpenAI) })
			for _, path := range []string{"/v1/model-marketplace?model_type=embedding", "/v1/models?model_type=embedding"} {
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				require.Equal(t, 200, w.Code, w.Body.String())
				assert.Contains(t, w.Body.String(), `"id":"matrix-embedding"`)
			}
		})
	}
}
