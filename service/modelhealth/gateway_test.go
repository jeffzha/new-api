package modelhealth

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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
