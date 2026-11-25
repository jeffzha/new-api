package opspush

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fundingRoundTrip func(*http.Request) (*http.Response, error)

func (f fundingRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestFundingWakeRetainsRetryFloorAndImmutableEvent(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_PROFILE", "product")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_PUSH_URL", "https://ops.example.invalid")
	t.Setenv("OPS_PUSH_KEY_ID", "funding-wake")
	t.Setenv("OPS_INGEST_BUDGET_ENABLED", "false")
	synctest.Test(t, func(t *testing.T) {
		db, err := gorm.Open(sqlite.Open("file:funding-wake?mode=memory&cache=shared"), &gorm.Config{})
		require.NoError(t, err)
		pool, err := db.DB()
		require.NoError(t, err)
		defer pool.Close()
		pool.SetMaxOpenConns(1)
		require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
		require.NoError(t, model.PrepareOPSWalletFundingSchema(t.Context(), db))
		user := model.User{Username: "funding-wake", AffCode: "funding-wake"}
		require.NoError(t, db.Create(&user).Error)
		pusher := New(db, db, Config{URL: "https://ops.example.invalid", KeyID: "funding-wake", Secret: "synthetic-funding-secret"})
		require.NoError(t, pusher.Migrate())
		var bodies []string
		pusher.Client.Transport = fundingRoundTrip(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			bodies = append(bodies, string(body))
			status, response := http.StatusOK, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`
			header := make(http.Header)
			if len(bodies) == 1 {
				status, response = http.StatusTooManyRequests, `{}`
				header.Set("Retry-After", "10")
			}
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(response))}, nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		hints := make(chan struct{}, 1)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			pusher.runFunding(ctx, hints)
		}()
		synctest.Wait() // Empty startup scan is idle; no funding HTTP.
		require.Empty(t, bodies)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return model.QueueOPSWalletFundingTx(tx, int64(user.Id), 100, "gift", "checkin", "wake-first", nil)
		}))
		hints <- struct{}{}
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		require.Len(t, bodies, 1, "post-commit wake does not wait for the idle scan")
		hints <- struct{}{}
		synctest.Wait()
		time.Sleep(10*time.Second - time.Nanosecond)
		synctest.Wait()
		require.Len(t, bodies, 1, "a new hint cannot bypass Retry-After")
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Len(t, bodies, 2)
		assert.Equal(t, bodies[0], bodies[1], "retry carries the original event and batch identity")
		rows, err := model.ReadOPSWalletFunding(db, 50)
		require.NoError(t, err)
		assert.Empty(t, rows)
		// No hint: periodic scanning recovers a committed event after hint loss.
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return model.QueueOPSWalletFundingTx(tx, int64(user.Id), 100, "gift", "checkin", "wake-lost", nil)
		}))
		time.Sleep(idleWait)
		synctest.Wait()
		require.Len(t, bodies, 3)
		rows, err = model.ReadOPSWalletFunding(db, 50)
		require.NoError(t, err)
		assert.Empty(t, rows)
		// Another slot shares the state row: it must not send while the current
		// owner is live, and must recover the backlog after that lease expires.
		standby := New(db, db, pusher.Config)
		standby.Client = pusher.Client
		standbyCtx, stopStandby := context.WithCancel(t.Context())
		defer stopStandby()
		standbyDone := make(chan struct{})
		go func() {
			defer close(standbyDone)
			standby.runFunding(standbyCtx, nil)
		}()
		synctest.Wait()
		require.Len(t, bodies, 3, "standby cannot spend the active slot's send budget")
		cancel()
		synctest.Wait()
		<-stopped
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return model.QueueOPSWalletFundingTx(tx, int64(user.Id), 100, "gift", "checkin", "wake-takeover", nil)
		}))
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		require.Len(t, bodies, 4, "standby recovers pending funding after lease expiration")
		rows, err = model.ReadOPSWalletFunding(db, 50)
		require.NoError(t, err)
		assert.Empty(t, rows)
		stopStandby()
		synctest.Wait()
		<-standbyDone
	})
}

func TestNativeTaskOutboxRetainsAcceptedLedgerAfterRollback(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
	t.Setenv("OPS_WALLET_PROFILE", "product")
	stub := &opsStub{responses: []func(http.ResponseWriter){
		reply(200, `{"data":{"accepted":0,"duplicates":0,"rejected":[]}}`),
		reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`),
	}}
	pusher, db := pusherFixture(t, stub)
	pusher.Config.Spec, pusher.Config.TaskOutbox = SpecV11, true
	require.NoError(t, db.Model(&State{}).Where("id = ?", 1).Update("cursor", 4).Error)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskBillingReconciliation{}))
	binding, err := opsauthority.NewBinding(pusher.Config.URL, pusher.Config.KeyID, "")
	require.NoError(t, err)
	event, err := common.Marshal(map[string]any{"requestId": "accepted-terminal", "extra": map[string]any{"billing_scope": "", "project_task_terminal": true}})
	require.NoError(t, err)
	bridge := model.Task{TaskID: "older-bridge-task", Status: model.TaskStatusSuccess, OpsUsagePending: true,
		PrivateData: model.TaskPrivateData{OpsUsage: []byte(`{"bridge_scope":"legacy"}`)}}
	require.NoError(t, db.Create(&bridge).Error)
	// A full first page of bridge-owned records must not starve a native
	// terminal behind it. They stay pending for their own bridge drainer.
	for i := range 49 {
		older := model.Task{TaskID: fmt.Sprintf("older-bridge-%d", i), Status: model.TaskStatusSuccess, OpsUsagePending: true, PrivateData: bridge.PrivateData}
		require.NoError(t, db.Create(&older).Error)
	}
	task := model.Task{TaskID: "accepted-ops-task", Status: model.TaskStatusSuccess, OpsUsagePending: true,
		PrivateData: model.TaskPrivateData{BillingSource: "ops", OPSWalletBinding: binding, OpsUsage: event}}
	require.NoError(t, db.Create(&task).Error)
	// A changed key must not redirect an accepted charge to another tenant.
	pusher.Config.KeyID = "another-ledger"
	state, leased := pusher.lease()
	require.True(t, leased)
	built, err := pusher.build(state)
	require.NoError(t, err)
	assert.False(t, built)
	built, err = pusher.build(state)
	require.ErrorContains(t, err, "destination changed")
	assert.False(t, built)
	assert.Empty(t, state.BatchId)
	pusher.Config.KeyID = "gw-test"
	// Secret rotation is safe; the target ledger, event and scope are unchanged.
	pusher.Config.Secret = "rotated-fixture-secret-123"
	assert.Positive(t, pusher.Step(), "scan past the bridge-owned first page")
	assert.Positive(t, pusher.Step(), "HTTP 200 without complete acceptance is not delivery")
	var stored model.Task
	require.NoError(t, db.First(&stored, task.ID).Error)
	assert.True(t, stored.OpsUsagePending)
	pusher.Config.KeyID = "another-ledger"
	assert.Positive(t, pusher.Step(), "a persisted batch must revalidate its ledger on retry")
	require.Len(t, stub.bodies, 1, "a changed target receives no accepted task bytes")
	pusher.Config.KeyID = "gw-test"
	assert.Zero(t, pusher.Step())
	require.Len(t, stub.bodies, 2)
	assert.Equal(t, stub.bodies[0], stub.bodies[1], "retries retain the frozen accepted event")
	assert.NotContains(t, stub.bodies[0], "bridge_scope")
	require.NoError(t, db.First(&stored, task.ID).Error)
	assert.False(t, stored.OpsUsagePending)
	require.NoError(t, db.First(&bridge, bridge.ID).Error)
	assert.True(t, bridge.OpsUsagePending, "native delivery does not acknowledge bridge-owned evidence")
}

func TestAcceptedUsageRetainsLedgerAcrossDurableRetry(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
	stub := &opsStub{responses: []func(http.ResponseWriter){
		reply(500, `{}`),
		reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`),
	}}
	p, db := pusherFixture(t, stub)
	p.Config.Spec = SpecV11
	require.NoError(t, db.Model(&State{}).Where("id = ?", 1).Update("cursor", 4).Error)
	binding, err := opsauthority.NewBinding(p.Config.URL, p.Config.KeyID, "")
	require.NoError(t, err)
	other, err := common.Marshal(map[string]any{"billing_source": "ops", "billing_scope": "",
		"root_info": map[string]any{"ops_wallet_binding": binding, "diagnostics": strings.Repeat("x", 5000)}})
	require.NoError(t, err)
	log := model.Log{UserId: 1, Type: model.LogTypeConsume, ModelName: "m", RequestId: "accepted-wallet-request",
		CreatedAt: p.Now().Unix() - 20, Quota: 12, Other: string(other)}
	require.NoError(t, db.Create(&log).Error)
	state, ok := p.lease()
	require.True(t, ok)
	p.Config.KeyID = "another-ledger"
	_, err = p.build(state)
	require.ErrorContains(t, err, "destination changed")
	require.Empty(t, stub.bodies)
	p.Config.KeyID = "gw-test"
	assert.Positive(t, p.Step(), "a failed delivery retains the batch")
	require.Len(t, stub.bodies, 1)
	assert.Contains(t, stub.bodies[0], binding.Target, "long diagnostics must not remove the destination binding")
	assert.NotContains(t, stub.bodies[0], "diagnostics")
	p.Config.KeyID = "another-ledger"
	assert.Positive(t, p.Step())
	require.Len(t, stub.bodies, 1, "retries never redirect an accepted charge")
	p.Config.KeyID, p.Config.Secret = "gw-test", "rotated-fixture-secret-123"
	assert.Zero(t, p.Step())
	require.Len(t, stub.bodies, 2)
	assert.Equal(t, stub.bodies[0], stub.bodies[1])
	var final State
	require.NoError(t, db.First(&final, 1).Error)
	assert.Equal(t, int64(log.Id), final.Cursor)
	assert.Empty(t, final.BatchBody)
}

func TestSlowFundingDoesNotBlockUsageOrSpendItsRetryState(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "false")
	stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":3,"duplicates":0,"rejected":[]}}`)}}
	pusher, db := pusherFixture(t, stub)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, model.PrepareOPSWalletFundingSchema(t.Context(), db))
	user := model.User{Username: "funding-independent", AffCode: "funding-independent"}
	require.NoError(t, db.Create(&user).Error)
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_PROJECT_SCOPE", "funding-independent")
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(string(body), `"batchId":"wf-batch-`) {
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			stub.ServeHTTP(w, r)
			return
		}
		close(started)
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		close(release)
		cancel()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("funding worker did not stop")
		}
	})
	// Hold only the funding HTTP response; usage must still advance durably.
	pusher.Config.URL = server.URL
	t.Setenv("OPS_PUSH_URL", server.URL)
	t.Setenv("OPS_PUSH_KEY_ID", pusher.Config.KeyID)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.QueueOPSWalletFundingTx(tx, int64(user.Id), 100, "gift", "checkin", "independent", nil)
	}))
	go func() {
		defer close(stopped)
		pusher.runFunding(ctx, nil)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("funding request did not start")
	}
	finished := make(chan time.Duration, 1)
	go func() { finished <- pusher.Step() }()
	select {
	case wait := <-finished:
		require.Zero(t, wait)
	case <-time.After(3 * time.Second):
		t.Fatal("usage scanner blocked on an unrelated funding response")
	}
	var state State
	require.NoError(t, db.First(&state, 1).Error)
	assert.EqualValues(t, 4, state.Cursor)
	assert.Zero(t, state.Attempts)
	rows, err := model.ReadOPSWalletFunding(db, 100)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a delayed funding response is not an acknowledgement")
}

func TestFundingShutdownRetainsUnacknowledgedEvents(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "false")
	pusher, db := pusherFixture(t, &opsStub{})
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, model.PrepareOPSWalletFundingSchema(t.Context(), db))
	user := model.User{Username: "funding-shutdown", AffCode: "funding-shutdown"}
	require.NoError(t, db.Create(&user).Error)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_PROJECT_SCOPE", "funding-shutdown")
	t.Setenv("OPS_PUSH_URL", server.URL)
	t.Setenv("OPS_PUSH_KEY_ID", pusher.Config.KeyID)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.QueueOPSWalletFundingTx(tx, int64(user.Id), 100, "gift", "checkin", "shutdown", nil)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pusher.Config.URL = server.URL
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		pusher.runFunding(ctx, nil)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("funding request did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled funding worker did not stop")
	}
	rows, err := model.ReadOPSWalletFunding(db, 100)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "shutdown cannot acknowledge an unconfirmed credit")
	pusher.runFunding(ctx, nil) // A cancelled context cannot launch a replacement sender.
}

// Spec 3.6 test vectors.
func TestSignMatchesSpecVectors(t *testing.T) {
	secret := "demo-secret-for-signature-test-vector-only"
	body := []byte(`{"schemaVersion":"1","batchId":"b-20261021-000001","events":[{"requestId":"req-0001","occurredAt":"2026-10-20T23:59:30Z","userId":"1001","model":"gpt-4o","status":"success","promptTokens":1200,"completionTokens":350}]}`)
	cases := []struct{ method, target, body, signature string }{
		{"POST", "/api/v1/ingest/v1/usage-events", string(body), "1575bac03fa20552c2ae9b617f4e9024b7ad869939fbc21d359b24dc50d06f66"},
		{"GET", "/api/v1/ingest/v1/reconcile?date=2026-10-20", "", "3fdf3ea08f23ee117ffa64b43556e7635ed8aea04885bae4759f6fd08d253386"},
	}
	for _, c := range cases {
		header := signWith("gw", secret, c.method, c.target, []byte(c.body), "1792540800", "8f14e45fceea167a5a36dedd4bea2543")
		assert.Equal(t, c.signature, header.Get("X-Signature"), c.target)
	}
}

// The event must carry what the OPS pull collector derives from the same log, or the two copies conflict.
func TestEventMirrorsPullCollector(t *testing.T) {
	log := &model.Log{Id: 7, UserId: 12, CreatedAt: 1792540800, Type: model.LogTypeConsume, Username: "alice",
		TokenName: "prod", ModelName: "doubao-seedance-2.0", Quota: 2750, PromptTokens: 1200, CompletionTokens: 350,
		UseTime: 3, IsStream: true, ChannelId: 3, TokenId: 57, Group: "vip", RequestId: "20261020235930123456",
		Other: `{"cache_tokens":800,"cache_creation_tokens":12.0,"image_count":"2","usage_facts":{"seconds":5,"resolution":"1080p","generate_audio":true},"model_ratio":1.5}`}
	event := Event(log, "主渠道")
	require.NotNil(t, event)
	assert.Equal(t, "2026-10-21T00:00:00Z", event["occurredAt"])
	assert.Equal(t, "12", event["userId"])
	assert.Equal(t, int64(800), event["cacheReadTokens"])
	assert.Equal(t, int64(0), event["cacheWriteTokens"], "fractions read as 0, as in Python")
	assert.Equal(t, int64(0), event["imageCount"], "strings read as 0")
	assert.Equal(t, 3000, event["latencyMs"])
	assert.Equal(t, float64(5), event["durationSeconds"])
	assert.Equal(t, map[string]any{"resolution": "1080p", "withAudio": true}, event["attributes"])
	assert.Equal(t, "主渠道", event["channelName"])
	assert.Equal(t, "57", event["tokenId"])

	errorLog := *log
	errorLog.Type, errorLog.RequestId = model.LogTypeError, ""
	assert.Equal(t, "log-7", Event(&errorLog, "")["requestId"])
	assert.Equal(t, "error", Event(&errorLog, "")["status"])
	// A failed attempt of a retried request must not take the request id of its charge.
	errorLog.RequestId = log.RequestId
	assert.Equal(t, "20261020235930123456-e7", Event(&errorLog, "")["requestId"])
	assert.Equal(t, "20261020235930123456", event["requestId"])

	for _, broken := range []model.Log{{Id: 1, Type: model.LogTypeConsume, UserId: 1}, {Id: 2, Type: model.LogTypeConsume, ModelName: "m"},
		{Id: 3, Type: model.LogTypeTopup, UserId: 1, ModelName: "m"}, {Id: 4, Type: model.LogTypeConsume, UserId: 1, ModelName: "m", RequestId: "bad id"}} {
		assert.Nil(t, Event(&broken, ""), broken.Id)
	}
}

type opsStub struct {
	bodies    []string
	responses []func(http.ResponseWriter)
}

func (s *opsStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.bodies = append(s.bodies, string(raw))
	s.responses[0](w)
	if len(s.responses) > 1 {
		s.responses = s.responses[1:]
	}
}

func reply(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func pusherFixture(t *testing.T, stub *opsStub) (*Pusher, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:opspush-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.Channel{}))
	require.NoError(t, db.Create(&model.Log{Type: model.LogTypeConsume, UserId: 1, ModelName: "before", CreatedAt: 100, RequestId: "r0"}).Error)
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	pusher := New(db, db, Config{URL: server.URL, KeyID: "gw-test", Secret: "0123456789abcdef0123"})
	pusher.Now = func() time.Time { return time.Unix(10_000, 0) }
	require.NoError(t, pusher.Migrate())
	require.NoError(t, pusher.Migrate(), "a second start keeps the stored cursor")
	for i, id := range []string{"r1", "r2", "r3"} {
		require.NoError(t, db.Create(&model.Log{Type: model.LogTypeConsume, UserId: 1, ModelName: "m", CreatedAt: int64(200 + i), RequestId: id}).Error)
	}
	return pusher, db
}

func TestStepDeliversRetriesAndAdvances(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){
		reply(503, `{"error":{"code":"storage_unavailable"}}`),
		reply(200, `{"data":{"accepted":3,"duplicates":0,"rejected":[]}}`),
	}}
	pusher, db := pusherFixture(t, stub)

	assert.Greater(t, pusher.Step(), time.Duration(0), "5xx backs off")
	var state State
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, int64(1), state.Cursor, "starts after the logs present at first start")
	assert.Equal(t, "gw-2-4", state.BatchId)
	assert.Equal(t, 1, state.Attempts)

	assert.Equal(t, time.Duration(0), pusher.Step())
	require.Len(t, stub.bodies, 2)
	assert.Equal(t, stub.bodies[0], stub.bodies[1], "a retry resends the same bytes")
	assert.Contains(t, stub.bodies[0], `"batchId":"gw-2-4"`)
	assert.NotContains(t, stub.bodies[0], `"before"`)
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, int64(4), state.Cursor)
	assert.Empty(t, state.BatchId)
	assert.Equal(t, int64(3), state.Delivered)

	assert.Equal(t, idleWait, pusher.Step(), "nothing new")
	assert.Len(t, stub.bodies, 2)
}

func TestStepDropsRejectedEventsAndKeepsBatchId(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){
		reply(400, `{"error":{"code":"invalid_events","details":{"rejected":[{"index":1,"requestId":"r2","reason":"invalid_model"}],"rejectedCount":1}}}`),
		reply(200, `{"data":{"accepted":2,"duplicates":0,"rejected":[]}}`),
	}}
	pusher, db := pusherFixture(t, stub)
	pusher.Step()
	pusher.Step()
	require.Len(t, stub.bodies, 2)
	assert.Contains(t, stub.bodies[1], `"batchId":"gw-2-4"`)
	assert.NotContains(t, stub.bodies[1], `"r2"`)
	var state State
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, int64(4), state.Cursor)
	assert.Equal(t, int64(1), state.Rejected)
}

func TestLeaseKeepsOnePusher(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":3,"duplicates":0,"rejected":[]}}`)}}
	first, db := pusherFixture(t, stub)
	second := New(db, db, first.Config)
	second.Now = first.Now
	first.Step()
	assert.Equal(t, leaseSeconds*time.Second/2, second.Step(), "the other instance waits for the lease")
	assert.Len(t, stub.bodies, 1)
}

var testConversion = Conversion{QuotaPerUnit: 500000, USDExchangeRate: 7.3, TopupPrice: 7.3}

func extraOfEvent(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	require.NotNil(t, event)
	raw, err := common.Marshal(event["extra"])
	require.NoError(t, err)
	extra := map[string]any{}
	require.NoError(t, common.Unmarshal(raw, &extra))
	return extra
}

// v1.1 sends every wallet change with a non-negative gatewayQuota whose direction is in extra.
func TestEventV11Shapes(t *testing.T) {
	cases := []struct {
		log                        model.Log
		requestId, kind, direction string
		model                      bool
	}{
		{model.Log{Id: 11, Type: model.LogTypeTopup, Quota: 5_000_000, Content: "使用在线充值成功", Other: `{"quota_source":"online_topup","quota_direction":"credit","trade_no":"T1"}`}, "topup-11", "topup", "credit", false},
		{model.Log{Id: 12, Type: model.LogTypeConsume, ModelName: "gpt-4o", Quota: 2750, RequestId: "req-12"}, "req-12", "usage", "debit", true},
		{model.Log{Id: 13, Type: model.LogTypeManage, Quota: 100, Other: `{"quota_source":"admin_adjustment","quota_direction":"debit"}`}, "adjustment-13", "adjustment", "debit", false},
		{model.Log{Id: 14, Type: model.LogTypeSystem, Quota: 200, Other: `{"quota_source":"checkin_gift","quota_direction":"credit"}`}, "gift-14", "gift", "credit", false},
		{model.Log{Id: 15, Type: model.LogTypeError, ModelName: "gpt-4o", RequestId: "req-12"}, "req-12-e15", "error", "debit", true},
		{model.Log{Id: 16, Type: model.LogTypeRefund, ModelName: "seedance", Quota: 900, Other: `{"task_id":"task_abc","settlement_stage":"refund"}`}, "task_abc-r16", "refund", "credit", true},
		// Operator adjustments are logged as top-ups by the Gateway; the source tells them apart.
		{model.Log{Id: 17, Type: model.LogTypeTopup, Quota: 300, Other: `{"quota_source":"admin_adjustment","quota_direction":"debit"}`}, "adjustment-17", "adjustment", "debit", false},
	}
	wire := []string{"requestId", "upstreamRequestId", "occurredAt", "userId", "username", "tokenId", "tokenName", "group", "model",
		"channelId", "channelName", "promptTokens", "completionTokens", "cacheReadTokens", "cacheWriteTokens", "imageCount", "durationSeconds",
		"gatewayQuota", "status", "isStream", "latencyMs", "extra", "attributes"}
	for _, c := range cases {
		c.log.UserId, c.log.CreatedAt = 9, 1792540800
		event := EventV11(&c.log, "", nil, testConversion)
		extra := extraOfEvent(t, event)
		assert.Equal(t, c.requestId, event["requestId"], c.log.Id)
		assert.Equal(t, c.log.Quota, event["gatewayQuota"], c.log.Id)
		assert.Equal(t, map[bool]string{true: "error", false: "success"}[c.kind == "error"], event["status"], c.log.Id)
		_, hasModel := event["model"]
		assert.Equal(t, c.model, hasModel, c.log.Id)
		assert.Equal(t, c.kind, extra["recordKind"], c.log.Id)
		assert.Equal(t, c.direction, extra["direction"], c.log.Id)
		assert.Equal(t, float64(500000), extra["quotaPerUnit"], c.log.Id)
		assert.Equal(t, 7.3, extra["usdExchangeRate"], c.log.Id)
		assert.Equal(t, 7.3, extra["topupPrice"], c.log.Id)
		for name := range event {
			assert.Contains(t, wire, name, "only v1 wire fields at the top level")
		}
	}

	legacy := EventV11(&model.Log{Id: 20, UserId: 9, Type: model.LogTypeSystem, Content: "新用户注册赠送 1"}, "", nil, testConversion)
	extra := extraOfEvent(t, legacy)
	assert.Equal(t, false, extra["quotaRecorded"], "a log from before quotas were recorded says its 0 is unknown")
	assert.Equal(t, "新用户注册赠送 1", extra["content"])

	// The quota unit frozen in the log wins over the current setting.
	frozen := extraOfEvent(t, EventV11(&model.Log{Id: 21, UserId: 9, Type: model.LogTypeConsume, ModelName: "m", Other: `{"quota_per_unit":"400000"}`}, "", nil, testConversion))
	assert.Equal(t, float64(400000), frozen["quotaPerUnit"])

	// Persisted funding ownership survives rollback/restart with the switch
	// off. Audit rows must not become a second credit after outbox delivery.
	for _, source := range []string{"online_topup", "admin_topup", "redemption", "checkin_gift"} {
		log := &model.Log{Id: 22, UserId: 9, Type: model.LogTypeTopup, Quota: 500, Other: fmt.Sprintf(`{"quota_source":%q,"quota_direction":"credit"}`, source)}
		require.NotNil(t, EventV11(log, "", nil, testConversion), source)
		log.Other = fmt.Sprintf(`{"quota_source":%q,"quota_direction":"credit","wallet_event_owner":"ops_funding"}`, source)
		assert.Nil(t, EventV11(log, "", nil, testConversion), source)
	}

	// Replayed registration audits mirror one business gift, independent of
	// the new log row, runtime conversion or later account profile changes.
	for _, source := range []struct{ source, id string }{
		{model.LogQuotaSourceRegister, "reg-gift-9"},
		{model.LogQuotaSourceInvitee, "invite-gift-9"},
	} {
		log := model.Log{Id: 51, UserId: 9, Username: "accepted-name", CreatedAt: 1792540800, Type: model.LogTypeSystem, Quota: 100, Content: "accepted gift", Other: fmt.Sprintf(`{"quota_source":%q,"wallet_event_owner":"gateway","registration_event_id":%q,"quota_per_unit":400000,"usd_exchange_rate":7.1,"topup_price":7.2}`, source.source, source.id)}
		first := EventV11(&log, "", nil, testConversion)
		require.NotNil(t, first)
		assert.Equal(t, source.id, first["requestId"])
		extra := extraOfEvent(t, first)
		assert.Equal(t, 400000.0, extra["quotaPerUnit"])
		assert.Equal(t, 7.1, extra["usdExchangeRate"])
		assert.Equal(t, 7.2, extra["topupPrice"])
		log.Id = 99
		assert.Equal(t, first, EventV11(&log, "", nil, Conversion{QuotaPerUnit: 900000, USDExchangeRate: 8.1, TopupPrice: 8.2}))
	}
}

func TestEventV11RefundRequestIds(t *testing.T) {
	audit := &model.Log{Id: 29, UserId: 9, Type: model.LogTypeRefund, ModelName: "mj_imagine", RequestId: "accepted-request", Other: `{"task_id":"task-abc","wallet_event_owner":"ops_funding"}`}
	assert.Nil(t, Event(audit, ""), "an audit log cannot duplicate its durable refund event")
	assert.Nil(t, EventV11(audit, "", nil, testConversion))
	for _, c := range []struct {
		requestId, other, expected string
	}{
		{"req-7", `{"task_id":"task_abc"}`, "req-7-r30"},
		{"", `{"task_id":"task_abc"}`, "task_abc-r30"},
		{"", `{}`, "refund-30"},
		{"", `{"task_id":"bad id"}`, "refund-30"},
	} {
		log := &model.Log{Id: 30, UserId: 9, Type: model.LogTypeRefund, ModelName: "m", RequestId: c.requestId, Other: c.other}
		assert.Equal(t, c.expected, EventV11(log, "", nil, testConversion)["requestId"], c.other)
	}
}

func taskLog(t *testing.T, task *model.Task, added map[string]any) *model.Log {
	t.Helper()
	facts := task.BillingLogFacts()
	maps.Copy(facts, added)
	other, err := common.Marshal(facts)
	require.NoError(t, err)
	return &model.Log{Id: 40, UserId: 9, Type: model.LogTypeConsume, ModelName: "doubao-seedance-2.0", Quota: 28082, Other: string(other)}
}

// A settled Seedance task log carries the tokens the provider billed, the generated length, whether it was given a
// video, the upstream's own charge and the frozen CNY price; under v1 and v1.1 alike they survive the 4 KB cut.
func TestEventTaskFactsSurviveCut(t *testing.T) {
	task := &model.Task{Status: model.TaskStatusSuccess, Data: []byte(`{"duration":10,"resolution":"1080p","usage":{"total_tokens":217800,"amount":"3.20"}}`),
		PrivateData: model.TaskPrivateData{SettledTokens: 217800, BillingContext: &model.TaskBillingContext{
			OtherRatios: map[string]float64{"seconds": 5},
			ProviderBilling: &model.TaskProviderBillingSnapshot{Provider: "openai_seedance", Currency: "CNY", UnitPricePerMillionTokens: "31",
				CNYPerUSD: "7.3", Resolution: "1080p", HasVideoInput: true, EstimatedTokens: 100000},
		}}}
	log := taskLog(t, task, map[string]any{"task_id": "task_abc", "pre_consumed_quota": 315068, "actual_quota": 343150, "settlement_stage": "settle",
		"admin_info": map[string]any{"padding": strings.Repeat("x", 5000)}})

	for name, event := range map[string]map[string]any{"v1": Event(log, ""), "v1.1": EventV11(log, "", nil, testConversion)} {
		extra := extraOfEvent(t, event)
		assert.NotContains(t, extra, "admin_info", name)
		assert.Equal(t, float64(217800), extra["billed_tokens"], name)
		assert.NotContains(t, extra, "estimated_tokens", "a settled task reports what was billed, not the estimate")
		assert.Equal(t, "3.20", extra["upstream_amount"], name)
		assert.Equal(t, "settle", extra["settlement_stage"], name)
		assert.Equal(t, "task_abc", extra["task_id"], name)
		assert.Equal(t, float64(10), extra["seconds"], "the generated length replaces the requested one")
		assert.NotContains(t, extra, "usage_facts", "only an expression-priced task has usage facts")
		assert.Equal(t, float64(10), event["durationSeconds"], name)
		assert.Equal(t, map[string]any{"resolution": "1080p", "referenceVideo": true}, event["attributes"], name)
		raw, err := common.Marshal(event["extra"])
		require.NoError(t, err)
		assert.LessOrEqual(t, len(raw), maxExtra-256, name)
	}
	extra := extraOfEvent(t, EventV11(log, "", nil, testConversion))
	assert.Equal(t, "31", extra["unit_price_cny_per_million"])
	assert.Equal(t, "7.3", extra["cny_per_usd"])
	assert.Equal(t, "token", extra["billing_unit"])
	assert.Equal(t, "usage", extra["recordKind"])

	// At submission the task has only its estimate and the requested length.
	task.Status, task.PrivateData.SettledTokens = model.TaskStatusQueued, 0
	submitted := EventV11(taskLog(t, task, map[string]any{"task_id": "task_abc", "settlement_stage": "preconsume"}), "", nil, testConversion)
	extra = extraOfEvent(t, submitted)
	assert.Equal(t, float64(100000), extra["estimated_tokens"])
	assert.NotContains(t, extra, "billed_tokens", "an estimate is never reported as billed tokens")
	assert.NotContains(t, extra, "upstream_amount")
	assert.Equal(t, float64(5), submitted["durationSeconds"])

	// An expression-priced task gets the resolution it lacked filled into its usage facts.
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{UsageFacts: map[string]any{"seconds": 8}}
	assert.Equal(t, map[string]any{"seconds": 8, "resolution": "1080p"}, task.BillingLogFacts()["usage_facts"])
}

// The upstream result is read field by field; a value of another type is skipped, not fatal.
func TestTaskUpstreamFacts(t *testing.T) {
	for _, c := range []struct {
		data    string
		seconds any
		amount  any
	}{
		{`{"duration":"6","usage":{"amount":0.85}}`, float64(6), "0.85"},
		{`{"duration":-1,"duration_secs":7.5}`, 7.5, nil},
		{`{"duration":"auto","usage":"n/a"}`, nil, nil},
		{`{"duration":999999}`, nil, nil},
		{`[]`, nil, nil},
	} {
		task := &model.Task{Status: model.TaskStatusSuccess, Data: []byte(c.data), PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{}}}
		facts := task.BillingLogFacts()
		assert.Equal(t, c.seconds, facts["seconds"], c.data)
		assert.Equal(t, c.amount, facts["upstream_amount"], c.data)
	}
}

// Seedance plugins report the video input as usage_facts.video_input; MiniMax plugins report reference images and
// reference video seconds in usage_facts, which v1 keeps when it cuts the log.
func TestEventPluginVideoFacts(t *testing.T) {
	other := `{"task_id":"task_p","usage_facts":{"tokens":194400,"resolution":"720p","video_input":"video"},"expr_b64":"` + strings.Repeat("x", 5000) + `"}`
	event := Event(&model.Log{Id: 41, UserId: 9, Type: model.LogTypeConsume, ModelName: "doubao-seedance-2.0", Other: other}, "")
	assert.Equal(t, map[string]any{"resolution": "720p", "referenceVideo": true}, event["attributes"])

	other = `{"task_id":"task_m","usage_facts":{"seconds":6,"resolution":"768P","input_images":2,"input_video_seconds":3},"expr_b64":"` + strings.Repeat("x", 5000) + `"}`
	event = Event(&model.Log{Id: 42, UserId: 9, Type: model.LogTypeConsume, ModelName: "MiniMax-H3", Other: other}, "")
	facts := extraOfEvent(t, event)["usage_facts"].(map[string]any)
	assert.Equal(t, float64(2), facts["input_images"])
	assert.Equal(t, float64(3), facts["input_video_seconds"])
	assert.Equal(t, float64(6), event["durationSeconds"])
}

func TestBuildV11LinksTopupsAndKeepsV1Unchanged(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`)}}
	pusher, db := pusherFixture(t, stub)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}))
	require.NoError(t, db.Create(&model.TopUp{UserId: 1, TradeNo: "T-1", PaymentMethod: "alipay", Amount: 10, Money: 73, MoneyDecimal: "73.00",
		Status: common.TopUpStatusSuccess, CompleteTime: 300}).Error)
	require.NoError(t, db.Create(&model.TopUp{UserId: 2, TradeNo: "T-2", PaymentMethod: "wxpay", Amount: 1, Money: 7.3,
		Status: common.TopUpStatusSuccess, CompleteTime: 401}).Error)
	require.NoError(t, db.Create(&model.Log{Type: model.LogTypeTopup, UserId: 1, CreatedAt: 300, Quota: 5_000_000,
		Other: `{"quota_source":"online_topup","quota_direction":"credit","trade_no":"T-1"}`}).Error)
	// Written before top-up logs carried trade numbers: matched by user and completion time.
	require.NoError(t, db.Create(&model.Log{Type: model.LogTypeTopup, UserId: 2, CreatedAt: 400, Content: "使用在线充值成功",
		Other: `{"admin_info":{"payment_method":"wxpay"}}`}).Error)
	require.NoError(t, db.Create(&model.Log{Type: model.LogTypeRefund, UserId: 1, ModelName: "m", CreatedAt: 500, Quota: 10, Other: `{"task_id":"task_1"}`}).Error)

	state, leased := pusher.lease()
	require.True(t, leased)
	built, err := pusher.build(state)
	require.NoError(t, err)
	require.True(t, built)
	var v1 batch
	require.NoError(t, common.UnmarshalJsonStr(state.BatchBody, &v1))
	assert.Equal(t, "1", v1.SchemaVersion)
	require.Len(t, v1.Events, 3, "v1 sends calls and failures only")
	assert.NotContains(t, state.BatchBody, "recordKind")

	pusher.Config.Spec = SpecV11
	state.BatchId = ""
	_, err = pusher.build(state)
	require.NoError(t, err)
	var v11 batch
	require.NoError(t, common.UnmarshalJsonStr(state.BatchBody, &v11))
	assert.Equal(t, "1.1", v11.SchemaVersion)
	require.Len(t, v11.Events, 6)
	byId := map[string]map[string]any{}
	for _, event := range v11.Events {
		byId[event["requestId"].(string)] = event
	}
	require.Contains(t, byId, "topup-5")
	topup := byId["topup-5"]["extra"].(map[string]any)["topup"].(map[string]any)
	assert.Equal(t, "T-1", topup["tradeNo"])
	assert.Equal(t, "73.00", topup["moneyCny"], "money_decimal wins over money")
	assert.Equal(t, "alipay", topup["paymentMethod"])
	assert.Equal(t, float64(5_000_000), topup["creditedQuota"])
	assert.Equal(t, "1970-01-01T00:05:00Z", topup["completedAt"])
	require.Contains(t, byId, "topup-6")
	legacy := byId["topup-6"]["extra"].(map[string]any)
	assert.Equal(t, "T-2", legacy["topup"].(map[string]any)["tradeNo"])
	assert.Equal(t, "7.3", legacy["topup"].(map[string]any)["moneyCny"])
	assert.Equal(t, false, legacy["quotaRecorded"])
	assert.Contains(t, byId, "task_1-r7")

	// The same committed gift can produce two post-commit audit rows. A
	// single batch carries it once and still advances past both log IDs.
	for range 2 {
		require.NoError(t, db.Create(&model.Log{Type: model.LogTypeSystem, UserId: 1, CreatedAt: 600, Quota: 100,
			Other: `{"quota_source":"register_gift","wallet_event_owner":"gateway","registration_event_id":"reg-gift-1","quota_per_unit":500000,"usd_exchange_rate":7.3,"topup_price":7.3}`}).Error)
	}
	_, err = pusher.build(state)
	require.NoError(t, err)
	require.NoError(t, common.UnmarshalJsonStr(state.BatchBody, &v11))
	require.Len(t, v11.Events, 7)
	assert.Equal(t, "reg-gift-1", v11.Events[6]["requestId"])
	assert.EqualValues(t, 9, state.BatchLast)
	// A client model request with a similar prefix is not a registration
	// gift. Keep both usage records under the existing request contract.
	for i := range 2 {
		require.NoError(t, db.Create(&model.Log{Type: model.LogTypeConsume, UserId: 1, ModelName: "m", RequestId: "reg-gift-model-request", CreatedAt: int64(700 + i)}).Error)
	}
	_, err = pusher.build(state)
	require.NoError(t, err)
	require.NoError(t, common.UnmarshalJsonStr(state.BatchBody, &v11))
	require.Len(t, v11.Events, 9)
	assert.EqualValues(t, 11, state.BatchLast)
	// Conflicting facts for the same actual gift are held for investigation.
	require.NoError(t, db.Create(&model.Log{Type: model.LogTypeSystem, UserId: 1, CreatedAt: 600, Quota: 101,
		Other: `{"quota_source":"register_gift","wallet_event_owner":"gateway","registration_event_id":"reg-gift-1","quota_per_unit":500000,"usd_exchange_rate":7.3,"topup_price":7.3}`}).Error)
	_, err = pusher.build(state)
	require.ErrorContains(t, err, "registration gift event identity conflict")
}

func TestImageReferenceFactsSurvivePushAndLogBackfill(t *testing.T) {
	for _, references := range []int{0, 3} {
		for _, oversized := range []bool{false, true} {
			name := fmt.Sprintf("references_%d_oversized_%t", references, oversized)
			t.Run(name, func(t *testing.T) {
				stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":4,"duplicates":0,"rejected":[]}}`)}}
				pusher, db := pusherFixture(t, stub)
				other := map[string]any{"image_count": 2, "usage_facts": map[string]any{"input_images": references, "seconds": 6}}
				if oversized {
					other["expr_b64"] = strings.Repeat("x", 5000)
				}
				raw, err := common.Marshal(other)
				require.NoError(t, err)
				log := model.Log{Type: model.LogTypeConsume, UserId: 1, ModelName: "qwen-image-3.0", CreatedAt: 300, RequestId: "image-reference", Other: string(raw)}
				require.NoError(t, db.Create(&log).Error)
				pusher.Step()
				require.Len(t, stub.bodies, 1)
				var body struct {
					Events []map[string]any `json:"events"`
				}
				require.NoError(t, common.UnmarshalJsonStr(stub.bodies[0], &body))
				require.Len(t, body.Events, 4)
				extra := body.Events[3]["extra"].(map[string]any)
				assert.Equal(t, float64(2), extra["image_count"])
				assert.Equal(t, float64(references), extra["usage_facts"].(map[string]any)["input_images"])
				require.NoError(t, db.First(&log, log.Id).Error)
				var stored map[string]any
				require.NoError(t, common.UnmarshalJsonStr(log.Other, &stored))
				assert.Equal(t, stored["usage_facts"], extra["usage_facts"], "push and pull must carry the same billing facts")
				v11 := extraOfEvent(t, EventV11(&log, "", nil, testConversion))
				assert.Equal(t, stored["usage_facts"], v11["usage_facts"], "v1.1 must preserve reference counts even with a large expression")
			})
		}
	}
}
