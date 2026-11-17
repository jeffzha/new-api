package opsbridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type commandTestExecutor struct {
	calls int
	fail  bool
}

func (e *commandTestExecutor) ExecuteOpsAgencyCommand(context.Context, OpsAgencyCommand) (map[string]any, error) {
	e.calls++
	if e.fail {
		return nil, fmt.Errorf("private database credential must not escape")
	}
	return map[string]any{"applied": true}, nil
}

func TestAgencyCommandSignedContractAndDurableReplay(t *testing.T) {
	now := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	key := "isolated-command-test-key-32-characters"
	// Python's ensure_ascii=False, sort_keys=True, compact JSON contract.
	payload := `{"agencyId":"ops-agency","kind":"suspend_customer","payload":{"externalAgencyId":"7","externalUserId":"42","note":"中文<&>"}}`
	envelope := fmt.Sprintf(`{"expiresAt":"2026-10-15T03:05:00Z","gatewayId":"gateway-1","id":"cmd-1","leaseToken":"lease-1","payload":%s,"payloadHash":"%x","tenantId":"tenant-1"}`, payload, sha256.Sum256([]byte(payload)))
	var parsed commandEnvelope
	require.NoError(t, common.Unmarshal([]byte(envelope), &parsed))
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(envelope))
	claim := commandClaimResponse{Command: &parsed, Algorithm: "hmac-sha256", SignedPayload: envelope, Signature: fmt.Sprintf("%x", mac.Sum(nil))}
	ackStatus := 503
	claims, acks := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "gateway-1", r.Header.Get("X-Gateway-Id"))
		switch r.URL.Path {
		case "/api/v1/gateway/agency-center/commands/claim":
			claims++
			body, err := common.Marshal(map[string]any{"data": claim})
			require.NoError(t, err)
			_, _ = w.Write(body)
		case "/api/v1/gateway/agency-center/commands/ack":
			acks++
			w.WriteHeader(ackStatus)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := Config{URL: server.URL, GatewayID: "gateway-1", Key: "test-key", StateDir: t.TempDir(), CommandSigningKey: key}
	b, err := Open(cfg)
	require.NoError(t, err)
	executor := &commandTestExecutor{}
	b.commandExecutor = executor
	_, _, err = b.verifyAgencyCommand(claim, now)
	require.NoError(t, err)
	for _, mutate := range []func(*commandClaimResponse){
		func(c *commandClaimResponse) { c.Signature = "bad" },
		func(c *commandClaimResponse) { c.Algorithm = "none" },
		func(c *commandClaimResponse) { cp := *c.Command; cp.GatewayID = "other"; c.Command = &cp },
	} {
		invalid := claim
		mutate(&invalid)
		_, _, err = b.verifyAgencyCommand(invalid, now)
		require.Error(t, err)
	}
	_, _, err = b.verifyAgencyCommand(claim, now.Add(6*time.Minute))
	require.Error(t, err)
	worked, err := b.ProcessAgencyCommand(t.Context(), now)
	require.True(t, worked)
	require.Error(t, err)
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, 1, claims)
	require.NoError(t, b.Close())
	b, err = Open(cfg)
	require.NoError(t, err)
	b.commandExecutor = executor
	defer func() { require.NoError(t, b.Close()) }()
	ackStatus = 200
	_, err = b.ProcessAgencyCommand(t.Context(), now)
	require.NoError(t, err)
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, 1, claims, "retry ACK before claiming more work")
	assert.Equal(t, "acknowledged", b.state.CommandReceipts["cmd-1"].Status)
	assert.Empty(t, b.state.CommandReceipts["cmd-1"].LeaseToken)
	_, err = b.ProcessAgencyCommand(t.Context(), now)
	require.NoError(t, err)
	assert.Equal(t, 1, executor.calls, "completed command replay must not repeat side effects")
	assert.Equal(t, 3, acks)
	b.state.CommandReceipts["cmd-1"].Status = "executing"
	b.state.CommandReceipts["cmd-1"].Outcome = ""
	require.NoError(t, b.saveLocked())
	_, err = b.ProcessAgencyCommand(t.Context(), now)
	require.ErrorContains(t, err, "reconciliation")
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, "awaiting_reconcile", b.state.CommandReceipts["cmd-1"].Status)
	for attempt := 1; attempt <= 2; attempt++ {
		envelope = fmt.Sprintf(`{"attempt":%d,"expiresAt":"2026-10-15T03:05:00Z","gatewayId":"gateway-1","id":"cmd-retry","leaseToken":"lease-%d","payload":%s,"payloadHash":"%x","tenantId":"tenant-1"}`, attempt, attempt, payload, sha256.Sum256([]byte(payload)))
		require.NoError(t, common.Unmarshal([]byte(envelope), &parsed))
		mac = hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(envelope))
		claim = commandClaimResponse{Command: &parsed, Algorithm: "hmac-sha256", SignedPayload: envelope, Signature: fmt.Sprintf("%x", mac.Sum(nil))}
		executor.fail = attempt == 1
		_, err = b.ProcessAgencyCommand(t.Context(), now)
		require.NoError(t, err)
		assert.Equal(t, attempt+1, executor.calls)
		assert.Equal(t, attempt, b.state.CommandReceipts["cmd-retry"].ExecutionAttempt)
		if attempt == 1 {
			assert.Equal(t, "failed", b.state.CommandReceipts["cmd-retry"].Outcome)
			assert.Equal(t, map[string]any{"error": "gateway_command_failed"}, b.state.CommandReceipts["cmd-retry"].Result)
			_, err = b.ProcessAgencyCommand(t.Context(), now)
			require.NoError(t, err)
			assert.Equal(t, 2, executor.calls, "a failed receipt needs a newer signed attempt before execution")
		} else {
			assert.Equal(t, "succeeded", b.state.CommandReceipts["cmd-retry"].Outcome)
		}
	}
	_, err = b.ProcessAgencyCommand(t.Context(), now)
	require.NoError(t, err)
	assert.Equal(t, 3, executor.calls, "successful retry remains exactly-once on replay")
	ackStatus = http.StatusConflict
	_, err = b.ProcessAgencyCommand(t.Context(), now)
	require.Error(t, err)
	assert.Equal(t, "awaiting_reconcile", b.state.CommandReceipts["cmd-retry"].Status)
	assert.Equal(t, 3, executor.calls, "a rejected ACK must not execute the business operation again")
	require.NoError(t, b.Close())
	b, err = Open(cfg)
	require.NoError(t, err)
	b.commandExecutor = executor
	assert.Equal(t, "awaiting_reconcile", b.state.CommandReceipts["cmd-retry"].Status)
	ackStatus = http.StatusOK
	_, err = b.ProcessAgencyCommand(t.Context(), now)
	require.NoError(t, err)
	assert.Equal(t, "acknowledged", b.state.CommandReceipts["cmd-retry"].Status)
	assert.Equal(t, 3, executor.calls, "a known result is re-acknowledged after restart without executing")
	envelope = fmt.Sprintf(`{"attempt":1,"expiresAt":"2026-10-15T03:05:00Z","gatewayId":"gateway-1","id":"cmd-retry","leaseToken":"stale-lease","payload":%s,"payloadHash":"%x","tenantId":"tenant-1"}`, payload, sha256.Sum256([]byte(payload)))
	require.NoError(t, common.Unmarshal([]byte(envelope), &parsed))
	mac = hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(envelope))
	claim = commandClaimResponse{Command: &parsed, Algorithm: "hmac-sha256", SignedPayload: envelope, Signature: fmt.Sprintf("%x", mac.Sum(nil))}
	_, err = b.ProcessAgencyCommand(t.Context(), now)
	require.ErrorContains(t, err, "stale")
	assert.Equal(t, 3, executor.calls)
}

func TestAgencyCommandOptInDoesNotBreakUsageOnlyBridge(t *testing.T) {
	t.Setenv("OPS_BRIDGE_ENABLED", "true")
	t.Setenv("OPS_AGENCY_COMMANDS_ENABLED", "false")
	t.Setenv("OPS_BRIDGE_URL", "http://127.0.0.1:1")
	t.Setenv("OPS_GATEWAY_ID", "usage-only")
	t.Setenv("OPS_GATEWAY_KEY", "fixture")
	t.Setenv("OPS_BRIDGE_STATE_DIR", t.TempDir())
	t.Setenv("OPS_AGENCY_COMMAND_SIGNING_KEY", "")
	require.NoError(t, StartConfigured(t.Context(), &commandTestExecutor{}, nil))
	require.NotNil(t, Default)
	assert.Nil(t, Default.commandExecutor)
	require.NoError(t, Default.Close())
	Default = nil
	t.Setenv("OPS_AGENCY_COMMANDS_ENABLED", "true")
	require.Error(t, StartConfigured(t.Context(), &commandTestExecutor{}, nil))
	assert.Nil(t, Default)
}
