package opsbridge_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpsOutboxRestartAndConflict(t *testing.T) {
	status := 503
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		assert.Equal(t, "fixture-key", r.Header.Get("X-Gateway-Key"))
		assert.Equal(t, "event-1", r.Header.Get("Idempotency-Key"))
		w.WriteHeader(status)
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	cfg := opsbridge.Config{URL: server.URL, GatewayID: "gateway-1", Key: "fixture-key", StateDir: t.TempDir()}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	_, err = opsbridge.Open(cfg)
	require.Error(t, err, "a second writer must be rejected")
	require.NoError(t, b.Enqueue("native-usage/bulk", "event-1", map[string]any{"amount": 7}))
	require.Error(t, b.Enqueue("native-usage/bulk", "event-1", map[string]any{"amount": 8}))
	now := time.Now()
	worked, err := b.ProcessNext(context.Background(), now)
	require.True(t, worked)
	require.Error(t, err)
	worked, err = b.ProcessNext(context.Background(), now)
	require.False(t, worked)
	require.NoError(t, err)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	defer b.Close()
	status = 200
	worked, err = b.ProcessNext(context.Background(), now.Add(time.Hour))
	require.True(t, worked)
	require.NoError(t, err)
	worked, err = b.ProcessNext(context.Background(), now.Add(2*time.Hour))
	require.False(t, worked)
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	assert.NotContains(t, fmt.Sprint(b.Status()), "fixture-key")
	other := cfg
	other.GatewayID = "gateway-2"
	other.StateDir = filepath.Join(t.TempDir(), "other")
	b2, err := opsbridge.Open(other)
	require.NoError(t, err)
	require.NoError(t, b2.Close())
}

func TestOpsPermanentFailureRequiresRepair(t *testing.T) {
	status := 409
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"secret":"must-not-leak"}`)
	}))
	defer server.Close()
	b, err := opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	require.NoError(t, b.Enqueue("objects/sync", "sync-1", map[string]any{}))
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "must-not-leak")
	worked, err := b.ProcessNext(context.Background(), time.Now().Add(time.Hour))
	require.False(t, worked)
	require.NoError(t, err)
	jobs := b.Status()["deliveries"].([]map[string]any)
	require.Len(t, jobs, 1)
	assert.Equal(t, "needs_repair", jobs[0]["status"])
	require.NoError(t, b.Retry(jobs[0]["id"].(string)))
	status = 200
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.NoError(t, err)
}

type pricingStore struct {
	entries map[string]opsbridge.LocalPrice
	writes  int
}

func (s *pricingStore) Read(names []string) (map[string]opsbridge.LocalPrice, error) {
	result := map[string]opsbridge.LocalPrice{}
	for _, name := range names {
		if item, ok := s.entries[name]; ok {
			result[name] = item
		} else {
			result[name] = opsbridge.LocalPrice{Version: "empty", Configured: map[string]any{}}
		}
	}
	return result, nil
}
func (s *pricingStore) Apply(before map[string]opsbridge.LocalPrice, after map[string]map[string]any) error {
	for name := range after {
		current, err := s.Read([]string{name})
		if err != nil {
			return err
		}
		if current[name].Version != before[name].Version {
			return fmt.Errorf("conflict")
		}
	}
	for name, values := range after {
		encoded, _ := common.Marshal(values)
		s.entries[name] = opsbridge.LocalPrice{Version: string(encoded), Configured: values}
	}
	s.writes++
	return nil
}

func catalogServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "official-catalog"):
			fmt.Fprint(w, `{"data":{"schemaVersion":"1","currency":"USD","catalogVersion":"v1","components":[{"priceKey":"p","vendorName":"OpenAI","modelName":"test-model","callType":"text_generation","metricCode":"input_token","pricePerUnit":"0.0000025"},{"priceKey":"c","vendorName":"OpenAI","modelName":"test-model","callType":"text_generation","metricCode":"output_token","pricePerUnit":"0.000015"}]}}`)
		case strings.HasSuffix(r.URL.Path, "preview"):
			fmt.Fprint(w, `{"data":{"previewHash":"preview-v1","catalogVersion":"v1"}}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
}

func TestOpsImportProtectsLocalPriceAndRecovers(t *testing.T) {
	server := catalogServer(t)
	defer server.Close()
	cfg := opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir()}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	store := &pricingStore{entries: map[string]opsbridge.LocalPrice{"test-model": {Version: "local-1", Configured: map[string]any{"ModelRatio": 0.4}}}}
	plan, err := b.PreviewImport(context.Background(), store)
	require.NoError(t, err)
	assert.Equal(t, []string{"test-model"}, plan.Protected)
	assert.Equal(t, `tier("ops", c * 15 + p * 2.5)`, plan.After["test-model"]["billing_setting.billing_expr"])
	require.Error(t, b.ApplyImport(context.Background(), store, plan.ID, []string{"test-model"}, nil))
	require.NoError(t, b.ApplyImport(context.Background(), store, plan.ID, []string{"test-model"}, []string{"test-model"}))
	assert.Equal(t, 1, store.writes)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	defer b.Close()
	require.NoError(t, b.ApplyImport(context.Background(), store, plan.ID, []string{"test-model"}, []string{"test-model"}))
	assert.Equal(t, 1, store.writes, "replay must not overwrite subsequent administrator changes")
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.NoError(t, err)
	plan, err = b.PreviewImport(context.Background(), store)
	require.NoError(t, err)
	assert.Empty(t, plan.Protected)
	store.entries["test-model"] = opsbridge.LocalPrice{Version: "local-2", Configured: map[string]any{"ModelRatio": 0.6}}
	require.ErrorContains(t, b.ApplyImport(context.Background(), store, plan.ID, []string{"test-model"}, nil), "conflict")
	assert.Equal(t, 1, store.writes)
}

func TestOpsGateBlocksBeforeUpstream(t *testing.T) {
	for _, tc := range []struct {
		path, decision string
		want           int
	}{{"/v1/chat/completions", "blocked", 429}, {"/v1/images/generations", "blocked", 429}, {"/v1/audio/transcriptions", "blocked", 429}, {"/v1/rerank", "blocked", 429}, {"/v1/video/tasks", "allowed", 409}} {
		t.Run(tc.path, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"data":{"decision":%q}}`, tc.decision) }))
			defer server.Close()
			b, err := opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir(), EnforceCalls: true})
			require.NoError(t, err)
			defer b.Close()
			opsbridge.Default = b
			defer func() { opsbridge.Default = nil }()
			gin.SetMode(gin.TestMode)
			router := gin.New()
			called := false
			router.Use(func(c *gin.Context) {
				c.Set("id", 1)
				c.Set("original_model", "test-model")
				c.Set(common.RequestIdKey, "request-1")
			})
			router.POST(tc.path, opsbridge.CallGate(), func(c *gin.Context) { called = true; c.Status(200) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("POST", tc.path, nil))
			assert.Equal(t, tc.want, response.Code)
			assert.False(t, called)
		})
	}
}

func TestOpsTaskReservationSurvivesRestartAndConsumesOriginalRequest(t *testing.T) {
	paths := []string{}
	bodies := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if strings.HasSuffix(r.URL.Path, "/call-reservations") {
			fmt.Fprint(w, `{"data":{"decision":"allowed"}}`)
			return
		}
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer server.Close()
	cfg := opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir(), EnforceCalls: true}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	opsbridge.Default = b
	defer func() { opsbridge.Default = nil }()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("id", 7)
	c.Set("token_id", 9)
	c.Set(common.RequestIdKey, "request-video-1")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDoubaoVideo)
	require.NoError(t, opsbridge.ReserveTask(c, "task-video-1", "seedance", "video_generation"))
	require.NoError(t, opsbridge.BeforeTaskDispatch(c))
	opsbridge.FinishTaskSubmission(c, true)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	opsbridge.Default = b
	require.Len(t, b.PendingTasks(), 1)
	require.NoError(t, b.RenewTasks(context.Background()))
	record := map[string]any{"event_id": "ops-task-task-video-1", "request_id": "wrong",
		"occurred_at": time.Now().UTC().Format(time.RFC3339), "provider": "seedance_video", "terminal": true}
	require.NoError(t, b.TaskSucceeded("task-video-1", record))
	worked, err := b.ProcessNext(context.Background(), time.Now().Add(time.Minute))
	require.True(t, worked)
	require.NoError(t, err)
	require.Contains(t, paths, "/api/v1/gateway/call-reservations/renew")
	require.Contains(t, paths, "/api/v1/gateway/native-usage/bulk")
	assert.Contains(t, strings.Join(bodies, "\n"), `"request_id":"request-video-1"`)
	require.NoError(t, b.Close())
}

func TestOpsTaskRecoveryCancelsOnlyBeforeProviderDispatch(t *testing.T) {
	cancels := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/call-reservations/cancel") {
			cancels++
		}
		if strings.HasSuffix(r.URL.Path, "/call-reservations") {
			fmt.Fprint(w, `{"data":{"decision":"allowed"}}`)
			return
		}
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer server.Close()
	reserve := func(stateDir, requestID, taskID string, dispatched bool) *opsbridge.Bridge {
		b, err := opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: stateDir, EnforceCalls: true})
		require.NoError(t, err)
		opsbridge.Default = b
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
		c.Set("id", 7)
		c.Set(common.RequestIdKey, requestID)
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDoubaoVideo)
		require.NoError(t, opsbridge.ReserveTask(c, taskID, "seedance", "video_generation"))
		if dispatched {
			require.NoError(t, opsbridge.BeforeTaskDispatch(c))
		}
		return b
	}
	for _, tc := range []struct {
		name       string
		dispatched bool
		wantCancel int
	}{{"not-dispatched", false, 1}, {"dispatched", true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			cancels = 0
			dir := t.TempDir()
			b := reserve(dir, "request-"+tc.name, "task-"+tc.name, tc.dispatched)
			require.NoError(t, b.Close())
			var err error
			b, err = opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: dir, EnforceCalls: true})
			require.NoError(t, err)
			require.NoError(t, b.RecoverInterruptedTasks())
			for range 3 {
				worked, processErr := b.ProcessNext(context.Background(), time.Now().Add(time.Minute))
				require.NoError(t, processErr)
				if !worked {
					break
				}
			}
			assert.Equal(t, tc.wantCancel, cancels)
			require.NoError(t, b.Close())
		})
	}
	opsbridge.Default = nil
}

func TestOpsTaskTerminalOutcomesAreDurable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		finalize   func(*opsbridge.Bridge, string) error
		wantPath   string
		wantStatus string
	}{
		{"failure releases reservation", func(b *opsbridge.Bridge, taskID string) error { return b.TaskFailed(taskID) }, "/api/v1/gateway/call-reservations/cancel", "cancel_queued"},
		{"missing usage needs reconciliation", func(b *opsbridge.Bridge, taskID string) error { return b.TaskUsageGap(taskID) }, "", "task_reconciliation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if strings.HasSuffix(r.URL.Path, "/call-reservations") {
					fmt.Fprint(w, `{"data":{"decision":"allowed"}}`)
					return
				}
				fmt.Fprint(w, `{"data":{}}`)
			}))
			defer server.Close()
			b, err := opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir(), EnforceCalls: true})
			require.NoError(t, err)
			opsbridge.Default = b
			t.Cleanup(func() { opsbridge.Default = nil; _ = b.Close() })
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			c.Set("id", 7)
			c.Set(common.RequestIdKey, "request-"+tc.name)
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDoubaoVideo)
			require.NoError(t, opsbridge.ReserveTask(c, "task-"+tc.name, "seedance", "video_generation"))
			require.NoError(t, opsbridge.BeforeTaskDispatch(c))
			require.NoError(t, tc.finalize(b, "task-"+tc.name))
			opsbridge.FinishTaskSubmission(c, true)
			assert.Equal(t, 1, b.Status()["request_states"].(map[string]int)[tc.wantStatus])
			worked, processErr := b.ProcessNext(context.Background(), time.Now())
			if tc.wantPath == "" {
				assert.False(t, worked)
				require.NoError(t, processErr)
				return
			}
			require.True(t, worked)
			require.NoError(t, processErr)
			assert.Contains(t, paths, tc.wantPath)
		})
	}
}

func TestOpsTaskUsageStillExportsWithoutCallEnforcement(t *testing.T) {
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer server.Close()
	b, err := opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	require.NoError(t, b.TaskSucceeded("task-without-gate", map[string]any{"event_id": "ops-task-task-without-gate"}))
	worked, err := b.ProcessNext(context.Background(), time.Now())
	require.True(t, worked)
	require.NoError(t, err)
	assert.Contains(t, paths, "/api/v1/gateway/native-usage/bulk")
}

func TestOpsNotificationCacheSurvivesAckFailure(t *testing.T) {
	reads := 0
	ackStatus := 503
	acks := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/notifications"):
			fmt.Fprint(w, `{"data":{"items":[{"id":7,"type":"entitlement_changed"}]}}`)
		case strings.HasSuffix(r.URL.Path, "/quota-snapshots"):
			reads++
			fmt.Fprint(w, `{"data":{"items":[]}}`)
		case strings.HasSuffix(r.URL.Path, "/ack"):
			var body map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &body))
			encoded, _ := common.Marshal(body)
			acks = append(acks, string(encoded))
			w.WriteHeader(ackStatus)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	cfg := opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir()}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	require.NoError(t, b.RefreshNotifications(context.Background()))
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.Error(t, err)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	defer b.Close()
	require.NoError(t, b.RefreshNotifications(context.Background()))
	ackStatus = 200
	_, err = b.ProcessNext(context.Background(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, reads)
	require.Len(t, acks, 2)
	assert.Equal(t, acks[0], acks[1])
	_, err = b.ArchiveDelivered()
	require.NoError(t, err)
	require.NoError(t, b.RefreshNotifications(context.Background()))
	assert.Equal(t, 1, reads)
	assert.Empty(t, b.Status()["deliveries"])
}

func TestOpsGateKeepsQueuedUsageWhenHandlerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":{"decision":"allowed"}}`) }))
	defer server.Close()
	cfg := opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir(), EnforceCalls: true}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	opsbridge.Default = b
	defer func() { opsbridge.Default = nil }()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 1)
		c.Set("original_model", "model")
		c.Set(common.RequestIdKey, "request-with-usage")
	})
	router.POST("/v1/chat/completions", opsbridge.CallGate(), func(c *gin.Context) {
		require.NoError(t, b.RecordUsage("request-with-usage", map[string]any{"event_id": "request-with-usage"}))
		c.Status(500)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	defer b.Close()
	assert.Equal(t, 1, b.Status()["request_states"].(map[string]int)["usage_queued"])
	jobs := b.Status()["deliveries"].([]map[string]any)
	require.Len(t, jobs, 1)
	assert.Equal(t, "native-usage/bulk", jobs[0]["path"])
}

func TestOpsGatePanicRetainsMissingUsageEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":{"decision":"allowed"}}`) }))
	defer server.Close()
	b, err := opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir(), EnforceCalls: true})
	require.NoError(t, err)
	defer b.Close()
	opsbridge.Default = b
	defer func() { opsbridge.Default = nil }()
	router := gin.New()
	router.Use(gin.Recovery(), func(c *gin.Context) {
		c.Set("id", 1)
		c.Set("original_model", "model")
		c.Set(common.RequestIdKey, "panic-request")
	})
	router.POST("/v1/chat/completions", opsbridge.CallGate(), func(c *gin.Context) { panic("fixture panic") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	assert.Equal(t, 500, response.Code)
	assert.Equal(t, 1, b.Status()["request_states"].(map[string]int)["needs_usage_evidence"])
	assert.Empty(t, b.Status()["deliveries"])
}

func TestOpsArchivePreservesReplayProtectionAndUnresolvedEvidence(t *testing.T) {
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, `{}`) }))
	defer server.Close()
	cfg := opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir()}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	record := map[string]any{"event_id": "completed"}
	require.NoError(t, b.RecordUsage("completed", record))
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.NoError(t, err)
	require.NoError(t, b.MarkUsageGap("missing-usage"))
	require.NoError(t, b.Enqueue("objects/sync", "broken", map[string]any{}))
	status = 409
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.Error(t, err)
	count, err := b.ArchiveDelivered()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	defer b.Close()
	require.NoError(t, b.RecordUsage("completed", record))
	require.Error(t, b.RecordUsage("completed", map[string]any{"event_id": "different"}))
	assert.Equal(t, 1, b.Status()["request_states"].(map[string]int)["needs_usage_evidence"])
	jobs := b.Status()["deliveries"].([]map[string]any)
	require.Len(t, jobs, 1)
	assert.Equal(t, "needs_repair", jobs[0]["status"])
}

func TestOpsInventoryBlocksReorderingAndUsesScopedSnapshot(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, common.DecodeJson(r.Body, &body))
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	b, err := opsbridge.Open(opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	objects := []map[string]any{{"object_type": "user", "external_id": "1", "gateway_user_id": "1"}}
	require.NoError(t, b.QueueInventory("snapshot-1", objects))
	require.Error(t, b.QueueInventory("snapshot-2", objects))
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, []any{"user", "api_key", "channel"}, body["snapshot_types"])
	require.NoError(t, b.QueueInventory("snapshot-2", objects))
}

func TestOpsPricingSnapshotOrderingAndArchiveStorageFailure(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/gateway/pricing-snapshots", r.URL.Path)
		require.NoError(t, common.DecodeJson(r.Body, &received))
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	cfg := opsbridge.Config{URL: server.URL, GatewayID: "g", Key: "k", StateDir: t.TempDir()}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	payload := map[string]any{"entries": []any{map[string]any{"model_name": "local", "configured": map[string]any{"ModelRatio": 2}}}, "complete": true}
	require.NoError(t, b.QueuePricingSnapshot("prices-1", payload))
	require.Error(t, b.QueuePricingSnapshot("prices-2", payload))
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	defer b.Close()
	_, err = b.ProcessNext(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, true, received["complete"])
	assert.NotContains(t, received, "official_prices")
	// Storage failure retains delivered evidence and blocks further mutations.
	require.NoError(t, os.WriteFile(filepath.Join(cfg.StateDir, "archive"), []byte("blocked"), 0600))
	_, err = b.ArchiveDelivered()
	require.Error(t, err)
	assert.NotEmpty(t, b.Status()["persistence_error"])
	require.Error(t, b.QueuePricingSnapshot("prices-2", payload))
	assert.Len(t, b.Status()["deliveries"], 1)
}

// Run through scripts/b_http_acceptance.py in the Ops checkout. This opt-in
// case talks to the real Ops HTTP handler and never contacts a model provider.
func TestOpsRealHTTPAcceptance(t *testing.T) {
	base := os.Getenv("OPS_HTTP_ACCEPTANCE_URL")
	if base == "" {
		t.Skip("isolated Ops HTTP fixture not configured")
	}
	ctx := context.Background()
	cfg := opsbridge.Config{URL: base, GatewayID: "g1", Key: "key-a", StateDir: t.TempDir(), EnforceCalls: true}
	b, err := opsbridge.Open(cfg)
	require.NoError(t, err)
	defer func() { _ = b.Close() }()
	opsbridge.Default = b
	defer func() { opsbridge.Default = nil }()
	store := &pricingStore{entries: map[string]opsbridge.LocalPrice{}}
	plan, err := b.PreviewImport(ctx, store)
	require.NoError(t, err)
	require.Contains(t, plan.After, "claude-test")
	require.NoError(t, b.ApplyImport(ctx, store, plan.ID, []string{"claude-test"}, nil))
	flush := func() {
		for range 30 {
			worked, err := b.ProcessNext(ctx, time.Now().Add(time.Hour))
			require.NoError(t, err)
			if !worked {
				return
			}
		}
		t.Fatal("delivery did not drain")
	}
	flush()
	objects := []map[string]any{{"object_type": "user", "external_id": "1", "gateway_user_id": "1", "display_name": "HTTP fixture"},
		{"object_type": "api_key", "external_id": "2", "gateway_user_id": "1"},
		{"object_type": "channel", "external_id": "ch", "channel_id": "ch"}}
	require.NoError(t, b.QueueInventory("http-objects", objects))
	require.NoError(t, b.QueuePricingSnapshot("http-pricing", map[string]any{
		"entries":  []any{map[string]any{"model_name": "claude-test", "version": "local-http-v1", "configured": store.entries["claude-test"].Configured}},
		"complete": true, "quota_per_unit": 500000, "group_ratio": map[string]any{"default": 1},
		"group_group_ratio": map[string]any{}, "usd_exchange_rate": 7}))
	flush()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 1)
		c.Set("token_id", 2)
		c.Set("original_model", "claude-test")
		c.Set(common.RequestIdKey, c.GetHeader("X-Fixture-Request"))
	})
	called := 0
	router.POST("/v1/chat/completions", opsbridge.CallGate(), func(c *gin.Context) {
		called++
		id := c.GetString(common.RequestIdKey)
		require.NoError(t, b.RecordUsage(id, map[string]any{"event_id": id, "request_id": id,
			"occurred_at": time.Now().UTC().Format(time.RFC3339), "provider": "anthropic", "terminal": true,
			"customer_context": map[string]any{"gateway_user_id": "1", "api_key_id": "2"},
			"routing_context":  map[string]any{"channel_id": "ch", "model_name": "claude-test", "vendor_name": "anthropic", "call_type": "text_generation"},
			"usage":            map[string]any{"input_tokens": 100, "output_tokens": 2}}))
		c.Status(200)
	})
	for i, expected := range []int{200, 429} {
		request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		request.Header.Set("X-Fixture-Request", fmt.Sprintf("http-request-%d", i))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assert.Equal(t, expected, response.Code, response.Body.String())
	}
	assert.Equal(t, 1, called, "exhaustion must block before invoking the upstream handler")
	flush()
	// Drain through the existing authenticated operator route.
	body := strings.NewReader(`{"limit":10}`)
	request, err := http.NewRequestWithContext(ctx, "POST", base+"/api/v1/tenant/a/usage-jobs/drain", body)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+os.Getenv("OPS_HTTP_TENANT_TOKEN"))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.DecodeJson(response.Body, &result))
	require.NoError(t, response.Body.Close())
	require.Equal(t, 200, response.StatusCode, result)
	encoded, err := common.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "succeeded")
	require.NoError(t, b.RefreshNotifications(ctx))
	flush()
	_, err = b.ArchiveDelivered()
	require.NoError(t, err)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(cfg)
	require.NoError(t, err)
	opsbridge.Default = b
	require.NoError(t, b.RefreshNotifications(ctx))
	flush()
	assert.Empty(t, b.Status()["deliveries"])
}
