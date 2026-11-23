package opspush

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/pkg/opsstorage"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
	var driver gorm.Dialector = sqlite.Open("file:opspush-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	engine := os.Getenv("OPS_NATIVE_TEST_ENGINE")
	if engine == "mysql" {
		driver = mysql.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	}
	if engine == "postgres" {
		driver = postgres.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	}
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	if engine != "" {
		common.SetDatabaseTypes(common.DatabaseType(engine), common.DatabaseType(engine))
	}
	t.Cleanup(func() { common.SetDatabaseTypes(previousMain, previousLog) })
	db, err := gorm.Open(driver, &gorm.Config{})
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

func TestPendingLargeBatchSurvivesSchemaUpgrade(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`)}}
	pusher, db := pusherFixture(t, stub)
	// Reproduce the original TEXT schema before upgrading. This row is still
	// empty, so restoring the old schema cannot truncate any test evidence.
	legacy := &struct {
		BatchBody string `gorm:"type:text"`
	}{}
	require.NoError(t, db.Table("ops_push_states").Migrator().AlterColumn(legacy, "BatchBody"))
	require.NoError(t, pusher.Migrate())
	body, err := common.Marshal(batch{SchemaVersion: "1", BatchId: "gw-upgrade-pending", Events: []map[string]any{{"requestId": "r1", "evidence": strings.Repeat("x", 80*1024)}}})
	require.NoError(t, err)
	require.NoError(t, db.Model(&State{}).Where("id = 1").Updates(map[string]any{"batch_id": "gw-upgrade-pending", "batch_last": 4, "batch_body": string(body), "attempts": 2}).Error)
	var before State
	require.NoError(t, db.First(&before, 1).Error)
	for range 2 {
		require.NoError(t, pusher.Migrate(), "a restart must not shrink a pending batch column")
		var after State
		require.NoError(t, db.First(&after, 1).Error)
		assert.Equal(t, before, after, "pending bytes, identity and delivery progress survive migration")
	}
	assert.Zero(t, pusher.Step())
	require.Len(t, stub.bodies, 1)
	assert.Equal(t, string(body), stub.bodies[0], "the upgraded process sends the original bytes")
	var delivered State
	require.NoError(t, db.First(&delivered, 1).Error)
	assert.Equal(t, int64(4), delivered.Cursor)
	assert.Empty(t, delivered.BatchBody)
}

func TestCustodyStreamKeepsIndependentCursorAndNeverReplaysProductMoney(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){
		reply(503, `{"error":{"code":"storage_unavailable"}}`),
		reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`),
	}}
	p, db := pusherFixture(t, stub)
	p.Config.Scope, p.Config.StateID = "overseas-usd", 2
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-usd")
	p.Config.Spec, p.Config.Currency, p.Config.UsageOnly, p.Config.Since = SpecV11, "USD", true, 1
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskBillingReconciliation{}, &model.User{}, &model.Token{}, &model.OPSWalletAdmission{}, &model.OPSWalletHold{}, &model.OPSWalletUsage{}))
	relayUser := model.User{Username: "relay-delivery-test", AffCode: "relay-only", Quota: 12345}
	require.NoError(t, db.Create(&relayUser).Error)
	relayToken := model.Token{UserId: relayUser.Id, Key: "test-only-relay-delivery", RemainQuota: 1000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&relayToken).Error)
	_, err := model.ReserveOPSWallet(db, model.OPSWalletHoldIdentity{RequestID: "r1", UserID: relayUser.Id, TokenID: relayToken.Id, Model: "m"}, 15, model.OPSWalletPosition{UserID: relayUser.Id, EntryID: 1, Quota: 100})
	require.NoError(t, err)
	relayEvent, err := common.Marshal(map[string]any{"requestId": "r1", "userId": fmt.Sprint(relayUser.Id), "model": "m", "status": "success", "currency": "USD", "amount": "0.00002", "extra": map[string]any{"billing_scope": "overseas-usd", "project_relay_terminal": true, "recordKind": "usage", "direction": "debit"}})
	require.NoError(t, err)
	require.NoError(t, model.FinalizeOPSRelay(db, "r1", relayUser.Id, 10, relayEvent))
	require.NoError(t, p.Config.Validate())
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", "r1").Update("other", `{"billing_scope":"overseas-usd"}`).Error)
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", "r2").Update("other", `{"billing_scope":"another-station"}`).Error)
	for _, log := range []model.Log{
		{Type: model.LogTypeConsume, UserId: 1, ModelName: "m", RequestId: "task-submit", CreatedAt: 300, Other: `{"billing_scope":"overseas-usd","task_id":"task-1"}`},
		{Type: model.LogTypeTopup, UserId: 1, RequestId: "stripe-ref", Quota: 500000, CreatedAt: 300, Other: `{"billing_scope":"overseas-usd"}`},
		{Type: model.LogTypeConsume, UserId: 1, ModelName: "m", RequestId: "malformed-scope", CreatedAt: 300, Other: `{"billing_scope":123}`},
	} {
		require.NoError(t, db.Create(&log).Error)
	}
	for range 2 {
		require.NoError(t, p.Migrate())
	}
	assert.Greater(t, p.Step(), time.Duration(0))
	assert.False(t, p.wakeAllowed, "new calls cannot bypass failed financial delivery backoff")
	var pendingRelay State
	require.NoError(t, db.First(&pendingRelay, 2).Error)
	for _, key := range []string{"amount", "currency", "model", "status", "extra"} {
		var changed batch
		require.NoError(t, common.UnmarshalJsonStr(string(pendingRelay.BatchBody), &changed))
		changed.Events[0][key] = "foreign"
		raw, err := common.Marshal(changed)
		require.NoError(t, err)
		candidate := pendingRelay
		candidate.BatchBody = opsstorage.LargeText(raw)
		require.Error(t, p.acknowledgeRelayBatch(&candidate), "changed %s must not acknowledge a debit", key)
	}
	assert.Zero(t, p.Step())
	require.Len(t, stub.bodies, 2)
	assert.Equal(t, stub.bodies[0], stub.bodies[1])
	var sent batch
	require.NoError(t, common.UnmarshalJsonStr(stub.bodies[0], &sent))
	require.Len(t, sent.Events, 1)
	assert.Equal(t, "r1", sent.Events[0]["requestId"])
	assert.Equal(t, "USD", sent.Events[0]["currency"])
	assert.Equal(t, "overseas-usd", sent.Events[0]["extra"].(map[string]any)["billing_scope"])
	assert.Equal(t, idleWait, p.Step(), "the duplicate consume log is not another debit")
	assert.True(t, p.wakeAllowed, "an idle outbox may wake immediately after a new commit")
	var relayDebit model.OPSWalletUsage
	require.NoError(t, db.First(&relayDebit).Error)
	assert.Positive(t, relayDebit.DeliveredAt)
	var legacy, custody State
	require.NoError(t, db.First(&legacy, 1).Error)
	require.NoError(t, db.First(&custody, 2).Error)
	assert.Equal(t, int64(1), legacy.Cursor)
	assert.Zero(t, legacy.Delivered)
	assert.Equal(t, int64(7), custody.Cursor)
	assert.Equal(t, int64(1), custody.Delivered)
	assert.False(t, p.ownsLog(&model.Log{Other: `{"billing_scope":123}`}))
	assert.False(t, (&Pusher{}).ownsLog(&model.Log{Other: `{"billing_scope":"overseas-usd"}`}))

	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.OPSWalletAdmission{}, &model.OPSWalletHold{}))
	user := model.User{Username: "task-delivery-test", Quota: 12345}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "test-only-terminal-delivery", RemainQuota: 1000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&token).Error)
	request := opsauthority.TaskRequestID("task-original")
	_, err = model.ReserveOPSWallet(db, model.OPSWalletHoldIdentity{RequestID: "task-original", ChargeRequestID: request, UserID: user.Id, TokenID: token.Id, Model: "m"}, 75, model.OPSWalletPosition{UserID: user.Id, EntryID: 8, Quota: 100})
	require.NoError(t, err)
	require.NoError(t, model.BeginOPSTaskDispatch(db, "task-original", user.Id, "public-task", fmt.Sprintf("%064x", 1), 8, "video"))
	require.NoError(t, model.ResolveOPSTaskDispatch(db, "task-original", user.Id, "upstream-task", false))
	for i := range 21 {
		old := model.Task{TaskID: fmt.Sprintf("old-bridge-%d", i), Status: model.TaskStatusSuccess, OpsUsagePending: true}
		require.NoError(t, db.Create(&old).Error)
	}
	frozen, err := common.Marshal(map[string]any{"requestId": request, "userId": fmt.Sprint(user.Id), "model": "m", "status": "success", "currency": "USD", "amount": "0.00012", "extra": map[string]any{"billing_scope": "overseas-usd", "project_task_terminal": true, "project_original_request": "task-original", "recordKind": "usage", "direction": "debit"}})
	require.NoError(t, err)
	task := model.Task{TaskID: "public-task", ChannelId: 8, Platform: "video", UserId: user.Id, Quota: 75, Status: model.TaskStatusSuccess, OpsUsagePending: true, PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-task", BillingSource: "ops", BillingScope: opsauthority.Scope(), TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: "task-original"}}}
	require.NoError(t, db.Create(&task).Error)
	assert.Equal(t, idleWait, p.Step(), "a terminal row cannot send before its reservation is finalized")
	assert.Equal(t, idleWait, p.Step())
	task.PrivateData.OPSTerminal = &model.OPSTaskTerminal{Quota: 60, Event: frozen}
	require.NoError(t, db.Model(&task).Update("private_data", task.PrivateData).Error)
	stub.responses = []func(http.ResponseWriter){reply(503, `{"error":{"code":"storage_unavailable"}}`), reply(200, `{"data":{"accepted":0,"duplicates":1,"rejected":[]}}`)}
	for range 2 {
		assert.Equal(t, idleWait, p.Step(), "a bounded scan must pass old bridge rows without starving custody tasks")
	}
	assert.Greater(t, p.Step(), time.Duration(0))
	require.Len(t, stub.bodies, 3)
	var pendingTask State
	require.NoError(t, db.First(&pendingTask, 2).Error)
	for _, key := range []string{"amount", "currency", "model", "status"} {
		var changed batch
		require.NoError(t, common.UnmarshalJsonStr(string(pendingTask.BatchBody), &changed))
		changed.Events[0][key] = "foreign"
		raw, err := common.Marshal(changed)
		require.NoError(t, err)
		candidate := pendingTask
		candidate.BatchBody = opsstorage.LargeText(raw)
		require.Error(t, p.acknowledgeTaskBatch(&candidate), "changed %s must not acknowledge a task", key)
	}
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.True(t, task.OpsUsagePending)
	assert.Equal(t, 60, task.Quota, "the pusher recovers committed terminal intent without provider polling")
	assert.JSONEq(t, string(frozen), string(task.PrivateData.OpsUsage), "native JSON columns may normalize key order and whitespace")
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 940, token.RemainQuota)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 12345, user.Quota, "financial recovery never changes the local wallet")
	// A replacement process resumes identical pending bytes after lease expiry.
	replacement := New(db, db, p.Config)
	replacement.Now = func() time.Time { return p.Now().Add(2 * time.Minute) }
	assert.Zero(t, replacement.Step())
	require.Len(t, stub.bodies, 4)
	assert.Equal(t, stub.bodies[2], stub.bodies[3])
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.False(t, task.OpsUsagePending)
	require.NoError(t, db.First(&custody, 2).Error)
	assert.Equal(t, int64(7), custody.Cursor, "terminal delivery does not skip ordinary usage")
	assert.Equal(t, idleWait, replacement.Step())
	assert.Len(t, stub.bodies, 4, "acknowledged terminal usage is not sent again")
}

func TestCustodyBatchesRetainUnsentEvents(t *testing.T) {
	for _, kind := range []string{"relay", "task"} {
		t.Run(kind, func(t *testing.T) {
			p, db := pusherFixture(t, &opsStub{})
			p.Config.Scope = "overseas-usd"
			t.Setenv("OPS_WALLET_AUTHORITY", "ops")
			t.Setenv("OPS_PROJECT_SCOPE", p.Config.Scope)
			require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskBillingReconciliation{}, &model.OPSWalletHold{}, &model.OPSWalletUsage{}))
			require.NoError(t, db.Model(&State{}).Where("id = 1").Update("owner", p.Owner).Error)
			var taskIDs []int64
			for i := range 2 {
				original := fmt.Sprintf("bounded-%s-%d", kind, i)
				request := original
				extra := map[string]any{"billing_scope": p.Config.Scope, "project_relay_terminal": true}
				if kind == "task" {
					request = opsauthority.TaskRequestID(original)
					extra = map[string]any{"billing_scope": p.Config.Scope, "project_task_terminal": true, "project_original_request": original}
				}
				// Relay events fit their 48 KB limit; task finalization also
				// permits events larger than half a delivery batch.
				size := 40 * 1024
				if kind == "task" {
					size = 600 * 1024
				}
				raw, err := common.Marshal(map[string]any{"requestId": request, "userId": "1", "model": "m", "currency": "USD", "extra": extra, "evidence": strings.Repeat("x", size)})
				require.NoError(t, err)
				hold := model.OPSWalletHold{ID: fmt.Sprintf("%x", sha256.Sum256([]byte(p.Config.Scope+"\x00"+original))), Scope: p.Config.Scope, RequestID: original, ChargeRequestID: request, UserID: 1, Model: "m", State: "settled", ActualQuota: 10}
				if kind == "task" {
					task := model.Task{TaskID: original, UserId: 1, Quota: 10, Status: model.TaskStatusSuccess, OpsUsagePending: true, PrivateData: model.TaskPrivateData{BillingSource: "ops", BillingScope: p.Config.Scope, OpsUsage: raw}}
					require.NoError(t, db.Create(&task).Error)
					hold.TaskID = task.ID
					taskIDs = append(taskIDs, task.ID)
				} else {
					require.NoError(t, db.Create(&model.OPSWalletUsage{ID: hold.ID, Scope: p.Config.Scope, RequestID: original, UserID: 1, Quota: 10, Payload: string(raw)}).Error)
				}
				require.NoError(t, db.Create(&hold).Error)
			}
			state := State{Id: 1, Cursor: 4}
			seen := map[string]bool{}
			rounds, perBatch := 1, 2
			if kind == "task" {
				rounds, perBatch = 2, 1
			}
			for round := range rounds {
				var built bool
				var err error
				if kind == "task" {
					built, err = p.buildTaskBatch(&state)
				} else {
					built, err = p.buildRelayBatch(&state)
				}
				require.NoError(t, err)
				require.True(t, built)
				assert.LessOrEqual(t, len(state.BatchBody), maxBody)
				assert.Equal(t, int64(4), state.BatchLast, "delivery cannot advance ordinary log search")
				var before State
				require.NoError(t, db.First(&before, 1).Error)
				for range 2 {
					require.NoError(t, p.Migrate())
					var after State
					require.NoError(t, db.First(&after, 1).Error)
					assert.Equal(t, before, after, "restart retains the complete pending batch")
				}
				var sent batch
				require.NoError(t, common.UnmarshalJsonStr(string(state.BatchBody), &sent))
				require.Len(t, sent.Events, perBatch)
				for _, event := range sent.Events {
					request := event["requestId"].(string)
					assert.False(t, seen[request], "the next batch must contain the retained event")
					seen[request] = true
				}
				if kind == "task" {
					if round == 0 {
						assert.Less(t, p.taskAfter, taskIDs[1], "the unsent task must remain in the next scan")
					}
					require.NoError(t, p.acknowledgeTaskBatch(&state))
				} else {
					require.NoError(t, p.acknowledgeRelayBatch(&state))
				}
				var remaining int64
				if kind == "task" {
					require.NoError(t, db.Model(&model.Task{}).Where("ops_usage_pending = ?", true).Count(&remaining).Error)
				} else {
					require.NoError(t, db.Model(&model.OPSWalletUsage{}).Where("delivered_at = ?", 0).Count(&remaining).Error)
				}
				assert.Equal(t, int64(2-perBatch*(round+1)), remaining)
			}
		})
	}
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

func TestUsageOnlyPushExcludesWalletRecordsAndKeepsUSDUsage(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":3,"duplicates":0,"rejected":[]}}`)}}
	pusher, db := pusherFixture(t, stub)
	pusher.Config.Spec, pusher.Config.Currency, pusher.Config.UsageOnly = SpecV11, "USD", true
	for _, kind := range []int{model.LogTypeTopup, model.LogTypeManage, model.LogTypeSystem, model.LogTypeRefund} {
		require.NoError(t, db.Create(&model.Log{Type: kind, UserId: 1, Quota: 500000, CreatedAt: 300, RequestId: "wallet"}).Error)
	}
	pusher.Step()
	require.Len(t, stub.bodies, 1)
	var received batch
	require.NoError(t, common.UnmarshalJsonStr(stub.bodies[0], &received))
	require.Len(t, received.Events, 3)
	for _, event := range received.Events {
		assert.Equal(t, "USD", event["currency"])
		assert.Equal(t, "usage", event["extra"].(map[string]any)["recordKind"])
		assert.NotContains(t, event, "balanceAfter")
	}
}

func TestUsageOnlyPushDoesNotSendOrRewritePendingWalletBatch(t *testing.T) {
	stub := &opsStub{}
	pusher, db := pusherFixture(t, stub)
	pusher.Config.UsageOnly = true
	body := `{"schemaVersion":"1.1","batchId":"pending-wallet","events":[{"extra":{"recordKind":"topup"}}]}`
	require.NoError(t, db.Model(&State{}).Where("id = 1").Updates(map[string]any{"batch_id": "pending-wallet", "batch_body": body, "batch_last": 4}).Error)
	assert.Equal(t, stalledWait, pusher.Step())
	assert.Empty(t, stub.bodies)
	var state State
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, body, string(state.BatchBody))
	assert.Equal(t, int64(1), state.Cursor)
}

func TestUSDUsageBackfillPreservesLiveCursorAndExcludesWallets(t *testing.T) {
	p, db := pusherFixture(t, &opsStub{})
	p.Config.Spec, p.Config.Currency, p.Config.UsageOnly = SpecV11, "USD", true
	for _, kind := range []int{model.LogTypeTopup, model.LogTypeManage, model.LogTypeSystem, model.LogTypeRefund} {
		require.NoError(t, db.Create(&model.Log{Type: kind, UserId: 1, Quota: 500000, CreatedAt: 300, RequestId: "wallet"}).Error)
	}
	require.NoError(t, db.Create(&model.Log{Type: model.LogTypeError, UserId: 1, ModelName: "m", CreatedAt: 300, RequestId: "historical-error"}).Error)
	const pending = `{"schemaVersion":"1.1","batchId":"live-pending","events":[]}`
	require.NoError(t, db.Model(&State{}).Where("id = 1").Updates(map[string]any{"batch_id": "live-pending", "batch_body": pending}).Error)
	var before State
	require.NoError(t, db.First(&before, 1).Error)
	page, err := p.BackfillPage(context.Background(), 0, 9, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), page.AfterID)
	assert.Equal(t, 2, page.Scanned)
	retry, err := p.BackfillPage(context.Background(), 0, 9, 2)
	require.NoError(t, err)
	assert.Equal(t, page.Body, retry.Body)
	page, err = p.BackfillPage(context.Background(), page.AfterID, 9, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(9), page.AfterID)
	var received batch
	require.NoError(t, common.UnmarshalJsonStr(page.Body, &received))
	require.Len(t, received.Events, 3)
	assert.Equal(t, "gw-history-usd-3-9", received.BatchId)
	for _, event := range received.Events {
		assert.Equal(t, "USD", event["currency"])
		assert.Contains(t, []any{"usage", "error"}, event["extra"].(map[string]any)["recordKind"])
		assert.NotContains(t, event, "balanceAfter")
	}
	var after State
	require.NoError(t, db.First(&after, 1).Error)
	assert.Equal(t, before, after, "historical preparation never takes the live lease or changes its batch")
	for _, limit := range []int{0, 101} {
		_, err := p.BackfillPage(context.Background(), 0, 9, limit)
		require.Error(t, err)
	}
	require.NoError(t, db.Create(&model.Log{Type: model.LogTypeConsume, UserId: 1, ModelName: "m", CreatedAt: p.Now().Unix(), RequestId: "unsettled"}).Error)
	_, err = p.BackfillPage(context.Background(), 0, 10, 2)
	require.ErrorContains(t, err, "unsettled")
	require.NoError(t, db.Model(&model.Log{}).Where("id = ?", 2).Update("quota", -1).Error)
	_, err = p.BackfillPage(context.Background(), 0, 9, 100)
	require.ErrorContains(t, err, "historical log 2")
}

var testConversion = Conversion{QuotaPerUnit: 500000, USDExchangeRate: 7.3, TopupPrice: 7.3}

func TestUSDWalletBackfillPreservesRecordedAmountsAndLiveState(t *testing.T) {
	p, db := pusherFixture(t, &opsStub{})
	p.Config.Spec, p.Config.Currency, p.Config.UsageOnly = SpecV11, "USD", true
	logs := []model.Log{
		{Type: model.LogTypeTopup, UserId: 1, Quota: 5_000_000, CreatedAt: 300, Other: `{"quota_source":"online_topup","quota_direction":"credit","trade_no":"usd-history"}`},
		{Type: model.LogTypeManage, UserId: 1, Quota: 250000, CreatedAt: 300, Other: `{"quota_source":"admin_adjustment","quota_direction":"debit"}`},
		{Type: model.LogTypeSystem, UserId: 1, Quota: 125000, CreatedAt: 300, Other: `{"quota_source":"redemption","quota_direction":"credit"}`},
		{Type: model.LogTypeRefund, UserId: 1, ModelName: "video", Quota: 50000, CreatedAt: 300, RequestId: "refunded-request"},
	}
	require.NoError(t, db.Create(&logs).Error)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}))
	require.NoError(t, db.Create(&model.TopUp{UserId: 1, TradeNo: "usd-history", PaymentMethod: "stripe", PaymentSnapshot: `{"actual_money":"10","currency_code":"USD"}`}).Error)
	var before State
	require.NoError(t, db.First(&before, 1).Error)
	page, err := p.WalletBackfillPage(context.Background(), 0, int64(logs[3].Id), 100)
	require.NoError(t, err)
	assert.Equal(t, 4, page.Scanned)
	replay, err := p.WalletBackfillPage(context.Background(), 0, int64(logs[3].Id), 100)
	require.NoError(t, err)
	assert.Equal(t, page, replay)
	var received batch
	require.NoError(t, common.UnmarshalJsonStr(page.Body, &received))
	require.Len(t, received.Events, 4)
	assert.Contains(t, received.BatchId, "gw-wallet-history-usd-")
	for i, event := range received.Events {
		assert.Equal(t, "USD", event["currency"])
		assert.Equal(t, []string{"10", "0.5", "0.25", "0.1"}[i], event["amount"])
		assert.Equal(t, []string{"topup", "adjustment", "gift", "refund"}[i], event["extra"].(map[string]any)["recordKind"])
		assert.NotContains(t, event, "balanceAfter")
		assert.NotContains(t, event, "gatewayQuota")
	}
	assert.Equal(t, "debit", received.Events[1]["extra"].(map[string]any)["direction"])
	facts := received.Events[0]["extra"].(map[string]any)["topup"].(map[string]any)
	assert.Equal(t, "10", facts["paidAmount"])
	assert.Equal(t, "USD", facts["paidCurrency"])
	assert.Equal(t, "refunded-request", received.Events[3]["extra"].(map[string]any)["request_id"])
	var after State
	require.NoError(t, db.First(&after, 1).Error)
	assert.Equal(t, before, after)
	require.NoError(t, db.Model(&model.Log{}).Where("id = ?", logs[1].Id).Update("other", `{}`).Error)
	_, err = p.WalletBackfillPage(context.Background(), 0, int64(logs[3].Id), 100)
	require.ErrorContains(t, err, "amount is not recorded")
}

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
}

func TestEventV11RefundRequestIds(t *testing.T) {
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

func TestUSDEventsKeepNativeAmountsAndDomesticFormat(t *testing.T) {
	for _, tc := range []struct {
		kind          int
		other, amount string
	}{
		{model.LogTypeConsume, "{}", "0.0055"},
		{model.LogTypeConsume, `{"billing_source":"subscription","wallet_quota_deducted":0}`, "0"},
		{model.LogTypeConsume, `{"billing_source":"subscription"}`, "0"},
		{model.LogTypeRefund, `{"billing_source":"subscription"}`, "0"},
		{model.LogTypeRefund, "{}", "0.0055"},
		{model.LogTypeManage, `{"quota_source":"admin_adjustment","quota_direction":"debit"}`, "0.0055"},
		{model.LogTypeSystem, `{"quota_source":"checkin_gift"}`, "0.0055"},
		{model.LogTypeTopup, `{"quota_source":"online_topup"}`, "0.0055"},
		{model.LogTypeError, "{}", ""},
	} {
		log := &model.Log{Id: 91, Type: tc.kind, UserId: 17, ModelName: "gpt-test", RequestId: "usd-91", Quota: 2750, Other: tc.other}
		topup := &model.TopUp{TradeNo: "usd-trade", PaymentMethod: "stripe", CompleteTime: 1792540800,
			PaymentSnapshot: `{"actual_money":"20.00","currency_code":"EUR"}`}
		event, err := EventUSD(log, "usd-channel", topup, testConversion)
		require.NoError(t, err)
		assert.Equal(t, "USD", event["currency"])
		assert.NotContains(t, event, "gatewayQuota")
		if tc.amount == "" {
			assert.NotContains(t, event, "amount")
		} else {
			assert.Equal(t, tc.amount, event["amount"])
		}
		extra := extraOfEvent(t, event)
		assert.NotContains(t, extra, "usdExchangeRate")
		assert.NotContains(t, extra, "topupPrice")
		if tc.kind == model.LogTypeTopup {
			facts := extra["topup"].(map[string]any)
			assert.Equal(t, "20.00", facts["paidAmount"])
			assert.Equal(t, "EUR", facts["paidCurrency"])
			assert.NotContains(t, facts, "moneyCny")
		}
		if tc.kind == model.LogTypeRefund {
			assert.Equal(t, "usd-91", extra["request_id"])
		}
		domestic := EventV11(log, "usd-channel", topup, testConversion)
		assert.Equal(t, 2750, domestic["gatewayQuota"])
		assert.NotContains(t, domestic, "currency")
		assert.NotContains(t, domestic, "amount")
	}
	for _, log := range []*model.Log{
		{Id: 92, UserId: 17, Type: model.LogTypeSystem, Quota: 0},
		{Id: 93, UserId: 17, Type: model.LogTypeConsume, ModelName: "m", Quota: -1},
		{Id: 94, UserId: 17, Type: model.LogTypeConsume, ModelName: "m", Quota: 1, Other: `{"wallet_quota_deducted":-1}`},
		{Id: 95, UserId: 17, Type: model.LogTypeConsume, ModelName: "m", Quota: 1, Other: `{"wallet_quota_deducted":2}`},
	} {
		_, err := EventUSD(log, "", nil, testConversion)
		require.Error(t, err, "unrecorded or invalid money must stop the outbox")
	}
}

func TestUSDAmountIsExactAndBounded(t *testing.T) {
	for _, tc := range []struct {
		quota int64
		unit  float64
		want  string
	}{
		{1, 500000, "0.000002"}, {-1, 500000, "-0.000002"}, {0, 500000, "0"}, {50_000_000, 500000, "100"},
		{1, 3, ""}, {1, 0, ""}, {1, math.NaN(), ""}, {math.MaxInt64, 500000, ""},
	} {
		value, err := USDAmount(tc.quota, tc.unit)
		if tc.want == "" {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			assert.Equal(t, tc.want, value)
		}
	}
	cfg := Config{URL: "https://ops.example.com", KeyID: "usd-test", Secret: "test-only-secret-value", Currency: "USD"}
	require.Error(t, cfg.Validate())
	cfg.Spec = SpecV11
	require.NoError(t, cfg.Validate())
}

func TestUSDOutboxRetriesExactBodyAndRetainsRejectedMoney(t *testing.T) {
	stub := &opsStub{responses: []func(http.ResponseWriter){
		reply(503, `{"error":{"code":"storage_unavailable"}}`),
		reply(400, `{"error":{"code":"invalid_events","details":{"rejected":[{"index":0,"requestId":"r1","reason":"invalid_amount"}]}}}`),
		reply(200, `{"data":{"accepted":3,"duplicates":0,"rejected":[]}}`),
	}}
	p, db := pusherFixture(t, stub)
	p.Config.Spec, p.Config.Currency = SpecV11, "USD"
	require.NoError(t, db.AutoMigrate(&model.TopUp{}))
	p.Step()
	p.Step()
	var state State
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, int64(1), state.Cursor)
	assert.NotEmpty(t, state.BatchBody)
	assert.Zero(t, state.Rejected)
	p.Step()
	require.Len(t, stub.bodies, 3)
	assert.Equal(t, stub.bodies[0], stub.bodies[1])
	assert.Equal(t, stub.bodies[0], stub.bodies[2])
	assert.Contains(t, stub.bodies[0], `"currency":"USD"`)
	assert.NotContains(t, stub.bodies[0], "gatewayQuota")
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
	require.NoError(t, common.UnmarshalJsonStr(string(state.BatchBody), &v1))
	assert.Equal(t, "1", v1.SchemaVersion)
	require.Len(t, v1.Events, 3, "v1 sends calls and failures only")
	assert.NotContains(t, state.BatchBody, "recordKind")

	pusher.Config.Spec = SpecV11
	state.BatchId = ""
	_, err = pusher.build(state)
	require.NoError(t, err)
	var v11 batch
	require.NoError(t, common.UnmarshalJsonStr(string(state.BatchBody), &v11))
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
