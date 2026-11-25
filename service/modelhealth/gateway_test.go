package modelhealth

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayProbeUsesPlatformKeyPublicModelAndPinnedChannel(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	sel, original := seedHealth(t, db, "text")
	const origin = "https://gateway.example"
	const key = "fixtureplatformkey"
	t.Setenv("MODEL_HEALTH_GATEWAY_URL", origin)
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE", key)
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE_ORIGIN", origin)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	t.Cleanup(func() {
		require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.User{}))
	})
	user := model.User{Username: "probe-fixture", Password: "not-a-login-password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Quota: 10000}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: key, Name: "model-health/fixture", Status: common.TokenStatusEnabled,
		ExpiredTime: time.Now().Unix() + 3600, RemainQuota: 1000, ModelLimitsEnabled: true, ModelLimits: sel.Model, Group: "default"}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", sel.Channel).Update("model_mapping", "{\"example-text\":\"upstream-private-name\"}").Error)
	p := original.Template
	require.NoError(t, SaveTemplate(ctx, p))
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	owned, err := acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	require.True(t, owned)
	plan, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	require.True(t, plan.Ready, plan.Blocked)
	run, err := Enqueue(ctx, sel, plan.Revision, p.MaxMicro)
	require.NoError(t, err)
	posts := 0
	client := &http.Client{Transport: mockProvider(func(req *http.Request) (*http.Response, error) {
		posts++
		assert.Equal(t, origin+"/v1/chat/completions", req.URL.String())
		assert.Equal(t, "Bearer sk-"+key+"-"+strconv.Itoa(sel.Channel), req.Header.Get("Authorization"))
		assert.Nil(t, req.GetBody, "paid submission must not be replayable by the transport")
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "\"model\":\"example-text\"")
		assert.NotContains(t, string(body), "upstream-private-name")
		response := reply(200, "{\"choices\":[{\"message\":{\"content\":\"OK\"}}]}")
		response.Header.Set(common.RequestIdKey, "gateway-request-1")
		return response, nil
	})}
	require.NoError(t, work(ctx, client, time.Now().Unix()))
	require.NoError(t, work(ctx, client, time.Now().Unix()))
	require.NoError(t, db.First(&run, run.ID).Error)
	assert.Equal(t, "succeeded", run.State)
	assert.Equal(t, "gateway-request-1", run.RequestID)
	assert.Equal(t, 1, posts)
	assert.NotContains(t, run.Evidence, key)

	for _, change := range []map[string]any{
		{"unlimited_quota": true}, {"expired_time": time.Now().Unix() - 1},
		{"model_limits_enabled": false}, {"model_limits": "unrelated-model"},
		{"cross_group_retry": true}, {"group": "auto"}, {"remain_quota": 0},
		{"name": "customer-token"},
	} {
		require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(change).Error)
		assert.Empty(t, gatewayProbeKey(db, p, Target{ChannelID: sel.Channel, Model: sel.Model, base: "https://provider.example"}), change)
		require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{
			"unlimited_quota": false, "expired_time": token.ExpiredTime, "model_limits_enabled": true,
			"model_limits": sel.Model, "cross_group_retry": false, "group": "default", "remain_quota": 1000, "name": token.Name,
		}).Error)
	}

	// Revocation and a wrong destination must withdraw readiness, not fall back
	// to sending a platform key directly to the channel's upstream.
	require.NoError(t, db.Model(&token).Update("status", common.TokenStatusDisabled).Error)
	bindings, err = workerBindings(db)
	require.NoError(t, err)
	_, err = acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	plan, err = GetPlan(ctx, sel)
	require.NoError(t, err)
	assert.False(t, plan.Ready)
	assert.False(t, bindings.hasProbe(p, Target{base: "https://provider.example"}))
	t.Setenv("MODEL_HEALTH_GATEWAY_URL", "https://other.example")
	assert.Empty(t, probeCredential(p, Target{base: "https://provider.example"}))
}

func TestAutomaticTemplatesCoverExistingAndNewModelsWithoutEnablingSchedules(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.User{})) })
	t.Setenv("MODEL_HEALTH_GATEWAY_URL", "https://gateway.example")
	t.Setenv("MODEL_HEALTH_KEY_PLATFORM", "fixtureplatformkey")
	t.Setenv("MODEL_HEALTH_KEY_PLATFORM_ORIGIN", "https://gateway.example")
	user := model.User{Username: "auto-probe", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "fixtureplatformkey", Name: "model-health/automatic", Status: common.TokenStatusEnabled, ExpiredTime: time.Now().Unix() + 3600, RemainQuota: 1000, ModelLimitsEnabled: true, ModelLimits: "existing", Group: "default"}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "DeepSeek", Type: constant.ChannelTypeDeepSeek, Status: common.ChannelStatusEnabled, BaseURL: common.GetPointer("https://provider.example"), Models: "deepseek-v4-flash,deepseek-v4-pro,qwen3-rerank,qwen3.7-text-embedding-flash,unknown-embedding", Group: "default"}
	require.NoError(t, db.Create(&channel).Error)
	cache := miniredis.RunT(t)
	priorRedis, priorRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled, common.RDB = priorRedis, priorRDB
	})
	stale := token
	cacheKey := "token:" + common.GenerateHMAC(token.Key)
	require.NoError(t, common.RDB.HSet(ctx, cacheKey, "Id", token.Id, "Name", token.Name, "ModelLimits", token.ModelLimits, "RemainQuota", 870, "UsedQuota", 130).Err())
	policy := AutoTemplatePolicy{Enabled: true, Credential: "MODEL_HEALTH_KEY_PLATFORM", TokenID: token.Id, MaxMicro: 10000}
	require.NoError(t, SaveAutoTemplatePolicy(ctx, policy))
	reloaded, err := model.GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.NotContains(t, stale.ModelLimits, "deepseek-v4-flash")
	assert.Contains(t, reloaded.ModelLimits, "deepseek-v4-flash", "preheated token cache must not retain the old allowlist")
	assert.Equal(t, 870, reloaded.RemainQuota, "allowlist changes must preserve unflushed cached charges")
	assert.Equal(t, 130, reloaded.UsedQuota)
	// Simulate Redis being unavailable after the DB commit, then retry.
	require.NoError(t, common.RDB.HSet(ctx, cacheKey, "ModelLimits", stale.ModelLimits).Err())
	require.NoError(t, SyncAutoTemplates(ctx))
	reloaded, err = model.GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Contains(t, reloaded.ModelLimits, "deepseek-v4-flash")
	assert.Equal(t, 870, reloaded.RemainQuota)
	require.NoError(t, SyncAutoTemplates(ctx))
	var count int64
	require.NoError(t, db.Model(&model.ModelHealthConfig{}).Where("kind = ?", "template").Count(&count).Error)
	assert.EqualValues(t, 5, count)
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	_, err = acquireWorker(ctx, "automatic-fixture", bindings)
	require.NoError(t, err)
	require.NoError(t, SaveBudget(ctx, Budget{Run: 10000, Day: 100000, Month: 1000000, Concurrent: 1, ChannelRPM: 1, ChannelTPM: 256}))
	for _, name := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		plan, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: name, Endpoint: "openai"})
		require.NoError(t, err)
		assert.True(t, plan.Ready, plan.Blocked)
		assert.False(t, plan.Rule.Enabled)
		assert.Equal(t, "low", plan.Template.Body["reasoning_effort"])
	}
	unknown, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: "unknown-embedding", Endpoint: "embeddings"})
	require.NoError(t, err)
	assert.False(t, unknown.Ready)
	assert.Contains(t, unknown.Blocked, "Confirm the expected embedding dimension")
	assert.NotEmpty(t, unknown.Template.Body)
	// A manual override survives periodic provisioning.
	sel := Selection{Channel: channel.Id, Model: "deepseek-v4-flash", Endpoint: "openai"}
	plan, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	custom := plan.Template
	custom.Body["max_tokens"] = float64(64)
	require.NoError(t, SaveTemplate(ctx, custom))
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("models", channel.Models+",future-chat-model").Error)
	// Simulate a concurrent charge before the synchronization: quota is preserved.
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("remain_quota", 900).Error)
	require.NoError(t, SyncAutoTemplates(ctx))
	plan, err = GetPlan(ctx, sel)
	require.NoError(t, err)
	assert.Equal(t, float64(64), plan.Template.Body["max_tokens"])
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Contains(t, token.ModelLimits, "future-chat-model")
	newPlan, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: "future-chat-model", Endpoint: "openai"})
	require.NoError(t, err)
	assert.True(t, newPlan.Template.MinimumConfirmed)
	assert.False(t, newPlan.Ready, "executor must refresh the exact per-target binding")
	// Generated profiles follow channel changes; manual overrides stay manual.
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{"base_url": "https://new-provider.example", "model_mapping": `{"deepseek-v4-pro":"mapped-pro"}`}).Error)
	require.NoError(t, SyncAutoTemplates(ctx))
	generated, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: "deepseek-v4-pro", Endpoint: "openai"})
	require.NoError(t, err)
	assert.True(t, generated.Template.MinimumConfirmed)
	assert.Equal(t, defaultTemplateProfile, generated.Template.GeneratedProfile)
	manual, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	assert.Empty(t, manual.Template.GeneratedProfile)
	assert.False(t, manual.Ready, "a manual template must be reviewed after the upstream changes")
	// Revocation is never undone by automatic provisioning.
	require.NoError(t, db.Model(&token).Update("status", common.TokenStatusDisabled).Error)
	assert.Error(t, SyncAutoTemplates(ctx))
	policy.Enabled = false
	assert.NoError(t, SaveAutoTemplatePolicy(ctx, policy))
}

func TestBuiltinTemplatesUseBoundedProtocolRequests(t *testing.T) {
	for _, tc := range []struct {
		kind, endpoint, name, path string
		ready                      bool
	}{
		{"llm", "openai", "deepseek-v4-flash", "/v1/chat/completions", true},
		{"llm", "anthropic", "claude-sonnet", "/v1/messages", true},
		{"llm", "openai-response", "codex-model", "/v1/responses", true},
		{"llm", "gemini", "gemini-model", "/v1beta/models/gemini-model:generateContent", true},
		{"embedding", "embeddings", "qwen3.7-text-embedding-flash", "/v1/embeddings", true},
		{"rerank", "jina-rerank", "qwen3-rerank", "/v1/rerank", true},
		{"image", "image-generation", "unknown-image", "/v1/images/generations", false},
		{"video", "openai-video", "unknown-video", "/v1/videos", false},
		{"tts", "audio_speech", "unknown-voice", "/v1/audio/speech", false},
		{"speech2text", "audio_transcription", "fun-asr-flash", "/v1/audio/transcriptions", false},
		{"audio", "audio_generation", "unknown-audio", "/v1/chat/completions", false},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			target := Target{ID: "fixture", Model: tc.name, Upstream: tc.name, Type: tc.kind, base: "https://provider.example"}
			p := builtinTemplate(target, tc.endpoint, AutoTemplatePolicy{Credential: "MODEL_HEALTH_KEY_PLATFORM", MaxMicro: 10000})
			assert.Equal(t, tc.path, p.Path)
			assert.Equal(t, tc.ready, p.validFor(target, tc.endpoint))
			assert.True(t, safeTemplateValue(p.Body))
			assert.NotEmpty(t, p.Body)
			assert.Equal(t, 128, p.MaxTokens)
		})
	}
}
