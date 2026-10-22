package opspush

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
