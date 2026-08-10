package providerverify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTencentVerifierUsesSignedOfficialContracts(t *testing.T) {
	actions := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("X-TC-Action")
		actions = append(actions, action)
		assert.Equal(t, "2026-05-20", r.Header.Get("X-TC-Version"))
		assert.Equal(t, "ap-guangzhou", r.Header.Get("X-TC-Region"))
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=secret-id/2026-08-09/adp/tc3_request"))
		w.Header().Set("Content-Type", "application/json")
		if action == "DescribeApp" {
			_, _ = w.Write([]byte(`{"Response":{"App":{"Metadata":{"AppId":"app-1","AppMode":4,"SpaceId":"space-1"},"SecretInfo":{"AppKey":"app-key"},"Status":{"Status":2}},"RequestId":"request-app"}}`))
			return
		}
		if action == "DescribeAgentDetail" {
			_, _ = w.Write([]byte(`{"Response":{"Agent":{"AgentId":"agent-1"},"RequestId":"request-agent"}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	verifier, err := newTencentVerifier(server.URL, time.Second, func() time.Time {
		return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	})
	require.NoError(t, err)

	result, err := verifier.Verify(context.Background(), validTarget())
	require.NoError(t, err)
	assert.Equal(t, "verified", result.Result)
	assert.Equal(t, []string{"request-app", "request-agent"}, result.ProviderRequestIDs)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, result.SanitizedResponseHash)
	assert.Equal(t, []string{"DescribeApp", "DescribeAgentDetail"}, actions)
}

func TestTencentVerifierFailsClosedOnResourceMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TC-Action") == "DescribeApp" {
			_, _ = w.Write([]byte(`{"Response":{"App":{"Metadata":{"AppId":"app-1","AppMode":1,"SpaceId":"other-space"},"SecretInfo":{"AppKey":"wrong"},"Status":{"Status":1}},"RequestId":"request-app"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"Response":{"Agent":{"AgentId":"other-agent"},"RequestId":"request-agent"}}`))
	}))
	defer server.Close()
	verifier, err := newTencentVerifier(server.URL, time.Second, time.Now)
	require.NoError(t, err)

	result, err := verifier.Verify(context.Background(), validTarget())
	require.NoError(t, err)
	assert.Equal(t, "invalid", result.Result)
	assert.Equal(t, "space_id_mismatch", result.ErrorCode)
	assert.NotContains(t, result.ErrorMessage, "wrong")
}

func TestTencentVerifierRejectsRedirectsAndUnsafeEndpoints(t *testing.T) {
	_, err := newTencentVerifier("https://evil.example", time.Second, time.Now)
	assert.Error(t, err)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Response":{"RequestId":"should-not-be-reached"}}`))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	verifier, err := newTencentVerifier(redirector.URL, time.Second, time.Now)
	require.NoError(t, err)
	_, err = verifier.Verify(context.Background(), validTarget())
	assert.Error(t, err)
}

func validTarget() Target {
	return Target{
		ProviderEnvironment: model.ProviderChinaTencentCloud,
		Region:              "ap-guangzhou", SpaceID: "space-1", AppID: "app-1", TemplateAgentID: "agent-1",
		AppKey: "app-key", SecretID: "secret-id", SecretKey: "secret-key",
	}
}
