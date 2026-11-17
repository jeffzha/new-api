package opspush

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/upstreammap"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/upstreamalerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamEnrichmentPreservesV11BillingFactsWhenDiagnosticsAreLarge(t *testing.T) {
	p, _ := pusherFixture(t, &opsStub{})
	p.Config.Spec = SpecV11
	p.Config.URL = "https://ops.example/test"
	t.Setenv("OPS_UPSTREAM_SYNC_ENABLED", "true")
	t.Setenv("OPS_UPSTREAM_URL", p.Config.URL)
	require.NoError(t, upstreammap.Migrate(p.DB))
	row, err := upstreammap.Ensure(p.DB, "https://upstream.example/")
	require.NoError(t, err)
	target := upstreammap.Target(p.Config.URL, p.Config.KeyID)
	require.NoError(t, upstreammap.EnsureBinding(p.DB, row.ID, target))
	require.NoError(t, p.DB.Model(&upstreammap.Binding{}).Where("mapping_id = ? AND target = ?", row.ID, target).Update("ops_upstream_id", 37).Error)
	other := model.NewLogOther()
	other.SetAdmin("upstream", map[string]any{"base_url": "https://upstream.example"})
	log := &model.Log{UserId: 1, ChannelId: 1, Type: model.LogTypeConsume, ModelName: "image", RequestId: "upstream-large", Other: other.JSONString()}
	event := EventV11(log, "", nil, Conversion{QuotaPerUnit: 500000, USDExchangeRate: 7.3, TopupPrice: 7.3})
	extra := event["extra"].(map[string]any)
	extra["diagnostics"] = strings.Repeat("x", 4000)
	extra["usage_facts"] = map[string]any{"input_images": 3}
	extra["image_count"] = 2
	require.NoError(t, p.attachUpstream(log, event))
	raw, err := common.Marshal(event["extra"])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(raw), maxExtra)
	result := object(raw)
	assert.Equal(t, "37", string(result["ops_upstream_id"]))
	assert.Equal(t, "2", string(result["image_count"]))
	assert.Equal(t, "500000", string(result["quotaPerUnit"]))
	assert.Equal(t, "7.3", string(result["usdExchangeRate"]))
	assert.Equal(t, "3", string(object(result["usage_facts"])["input_images"]))
	assert.NotContains(t, result, "diagnostics")
	log.Type, log.RequestId = model.LogTypeError, "upstream-balance"
	require.True(t, upstreamalerts.AppendRelayError(other,
		types.WithOpenAIError(types.OpenAIError{Code: "insufficient_balance"}, 403), log.RequestId, log.ChannelId))
	log.Other = other.JSONString()
	event = EventV11(log, "", nil, Conversion{QuotaPerUnit: 500000, USDExchangeRate: 7.3})
	require.NoError(t, p.attachUpstream(log, event))
	raw, err = common.Marshal(event["extra"])
	require.NoError(t, err)
	result = object(raw)
	assert.Equal(t, "37", string(result["ops_upstream_id"]))
	assert.Equal(t, `"upstream_balance_insufficient"`, string(object(result["upstream_alert"])["type"]))
}

func TestUpstreamBalanceFailureRetriesWithoutDuplicatingCharge(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){
		reply(503, `{"error":{"code":"storage_unavailable"}}`),
		reply(200, `{"data":{"accepted":4,"duplicates":0,"rejected":[]}}`),
	}}
	p, db := pusherFixture(t, stub)
	p.Config.Spec = SpecV11
	other := model.NewLogOther()
	err := types.WithOpenAIError(types.OpenAIError{Code: "insufficient_balance", Message: "余额不足"}, 403)
	require.True(t, upstreamalerts.AppendRelayError(other, err, "original-request", 32))
	failure := &model.Log{UserId: 1, ChannelId: 32, Type: model.LogTypeError, RequestId: "original-request",
		CreatedAt: 300, ModelName: "example-model", Other: other.JSONString()}
	require.NoError(t, db.Create(failure).Error)
	assert.Greater(t, p.Step(), time.Duration(0))
	assert.Equal(t, time.Duration(0), p.Step())
	require.Len(t, stub.bodies, 2)
	assert.Equal(t, stub.bodies[0], stub.bodies[1])
	assert.Contains(t, stub.bodies[0], `"type":"upstream_balance_insufficient"`)
	assert.Contains(t, stub.bodies[0], `"requestId":"original-request-e`)
	assert.Equal(t, idleWait, p.Step())
}

func TestUpstreamBalanceFailureContract(t *testing.T) {
	cases := []struct {
		name string
		err  *types.NewAPIError
		want bool
	}{
		{"explicit provider code", types.WithOpenAIError(types.OpenAIError{Code: "InsufficientBalance", Message: "account rejected"}, 403), true},
		{"financial quota", types.WithOpenAIError(types.OpenAIError{Code: "insufficient_quota", Message: "You exceeded your current quota, please check your plan and billing details"}, 429), true},
		{"Claude credit", types.WithClaudeError(types.ClaudeError{Type: "invalid_request_error", Message: "Your credit balance is too low"}, 400), true},
		{"Chinese upstream", types.NewOpenAIError(errors.New("账户余额不足，请充值"), types.ErrorCodeBadResponseStatusCode, 402), true},
		{"generic quota", types.WithOpenAIError(types.OpenAIError{Code: "insufficient_quota", Message: "quota exceeded"}, 429), false},
		{"request rate", types.WithOpenAIError(types.OpenAIError{Code: "rate_limit_exceeded", Message: "requests per minute exceeded"}, 429), false},
		{"payment status alone", types.WithOpenAIError(types.OpenAIError{Message: "payment required"}, 402), false},
		{"local wallet", types.NewErrorWithStatusCode(errors.New("余额不足"), types.ErrorCodeInsufficientUserQuota, 403), false},
		{"upstream gateway wallet", types.WithOpenAIError(types.OpenAIError{Code: "insufficient_user_quota", Message: "余额不足"}, 403), true},
		{"transport message", types.NewErrorWithStatusCode(errors.New("余额不足"), types.ErrorCodeDoRequestFailed, 500), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alert := upstreamalerts.FromRelayError(tc.err)
			assert.Equal(t, tc.want, alert != nil)
		})
	}
	other := model.NewLogOther()
	err := types.WithOpenAIError(types.OpenAIError{Code: "insufficient_balance",
		Message: "余额不足 api_key=secret-fixture https://private.example?token=private-fixture"}, 403)
	require.True(t, upstreamalerts.AppendRelayError(other, err, "request-failover", 32))
	other.SetAdmin("diagnostics", strings.Repeat("large", 1500))
	failure := &model.Log{Id: 17, UserId: 1, ChannelId: 32, Type: model.LogTypeError,
		RequestId: "request-failover", ModelName: "example-model", Other: other.JSONString()}
	for _, event := range []map[string]any{Event(failure, "channel"), EventV11(failure, "channel", nil, Conversion{QuotaPerUnit: 500000, USDExchangeRate: 7.3})} {
		require.NotNil(t, event)
		assert.Equal(t, "request-failover-e17", event["requestId"])
		assert.Equal(t, "error", event["status"])
		raw, marshalErr := common.Marshal(event["extra"])
		require.NoError(t, marshalErr)
		assert.LessOrEqual(t, len(raw), maxExtra)
		var extra map[string]any
		require.NoError(t, common.Unmarshal(raw, &extra))
		alert, ok := extra["upstream_alert"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, upstreamalerts.BalanceInsufficient, alert["type"])
		assert.Equal(t, "request-failover", alert["request_id"])
		assert.Nil(t, alert["balance"])
		assert.Nil(t, alert["currency"])
		assert.NotContains(t, string(raw), "secret-fixture")
		assert.NotContains(t, string(raw), "private-fixture")
	}
	failure.Type, failure.Id = model.LogTypeConsume, 18
	success := Event(failure, "fallback channel")
	assert.Equal(t, "request-failover", success["requestId"])
	raw, marshalErr := common.Marshal(success["extra"])
	require.NoError(t, marshalErr)
	assert.NotContains(t, object(raw), "upstream_alert")
	assert.Equal(t, http.StatusForbidden, err.StatusCode)
}
