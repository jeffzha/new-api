package modelhealth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func healthDB(t *testing.T) *gorm.DB {
	t.Helper()
	var dialect gorm.Dialector = sqlite.Open(filepath.ToSlash(filepath.Join(t.TempDir(), "health.db")) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if dsn := os.Getenv("TEST_MODEL_HEALTH_POSTGRES_DSN"); dsn != "" {
		dialect = postgres.Open(dsn)
	}
	if dsn := os.Getenv("TEST_MODEL_HEALTH_MYSQL_DSN"); dsn != "" {
		dialect = mysql.Open(dsn)
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	old := model.DB
	model.DB = db
	models := []any{&model.ModelHealthDelivery{}, &model.ModelHealthRun{}, &model.ModelHealthConfig{}, &model.OpsRequestEvent{}, &model.OpsUpstreamAttempt{}, &model.Task{}, &model.Channel{}, &model.Option{}}
	for _, m := range models {
		if db.Migrator().HasTable(m) {
			t.Fatal("model health integration tests require an empty, dedicated database")
		}
	}
	t.Cleanup(func() {
		model.DB = old
		for _, m := range models {
			if err := db.Migrator().DropTable(m); err != nil {
				t.Error(err)
			}
		}
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	if err := db.AutoMigrate(models[3:]...); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedHealth(t *testing.T, db *gorm.DB, kind string) (Selection, Plan) {
	t.Helper()
	const origin = "https://provider.example"
	channel := model.Channel{Name: "Local fixture", Type: constant.ChannelTypeOpenAI, Key: "unused-customer-key", Models: "example-" + kind, BaseURL: common.GetPointer(origin)}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE", "test-monitoring-key")
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE_ORIGIN", origin)
	selection := Selection{Channel: channel.Id, Model: channel.Models, Endpoint: "openai"}
	p := Template{Selection: selection, Path: "/v1/chat/completions", Credential: "MODEL_HEALTH_KEY_FIXTURE", Auth: "bearer", Body: map[string]any{"max_tokens": float64(4)}, MaxTokens: 20, MaxMicro: 100, TimeoutSeconds: 60, MinimumConfirmed: true}
	if kind == "video" {
		selection.Endpoint = "openai-video"
		p.Selection = selection
		p.Path = "/v1/videos"
		p.Body = map[string]any{"seconds": float64(4)}
		p.PollPath = "/v1/videos/{task_id}"
		p.TaskField = "id"
		p.StatusField = "status"
		p.ResultField = "metadata.url"
		p.SuccessValues = []string{"done"}
		p.PendingValues = []string{"queued"}
		p.FailureValues = []string{"failed"}
	}
	if err := SaveTemplate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := SaveBudget(ctx, Budget{Run: 100, Day: 200, Month: 1000, Concurrent: 2, ChannelRPM: 2, ChannelTPM: 200}); err != nil {
		t.Fatal(err)
	}
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	owned, err := acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	require.True(t, owned)
	plan, err := GetPlan(ctx, selection)
	if err != nil || !plan.Ready {
		t.Fatalf("expected ready plan: %v", err)
	}
	return selection, plan
}

func migrateHealth(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.ModelHealthConfig{}, &model.ModelHealthRun{}, &model.ModelHealthDelivery{}); err != nil {
		t.Fatal(err)
	}
}

type mockProvider func(*http.Request) (*http.Response, error)

func (f mockProvider) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestMigrationAdmissionAndAmbiguousSubmission(t *testing.T) {
	db := healthDB(t)
	if err := db.Create(&model.OpsRequestEvent{RequestID: "old-request"}).Error; err != nil {
		t.Fatal(err)
	}
	migrateHealth(t, db)
	sel, p := seedHealth(t, db, "video")
	migrateHealth(t, db)
	var old int64
	if err := db.Model(&model.OpsRequestEvent{}).Where("request_id = ?", "old-request").Count(&old).Error; err != nil || old != 1 {
		t.Fatalf("existing telemetry lost: %d, %v", old, err)
	}
	ctx := context.Background()
	if _, err := Enqueue(ctx, sel, p.Revision, 99); !IsValidation(err) {
		t.Fatalf("stale cost confirmation: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := Enqueue(ctx, sel, p.Revision, 100); results <- err })
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !IsValidation(err) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("admissions=%d, want 1", won)
	}
	posts := 0
	client := &http.Client{Transport: mockProvider(func(r *http.Request) (*http.Response, error) {
		posts++
		data, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.String() != "https://provider.example/v1/videos" || r.Header.Get("Authorization") != "Bearer test-monitoring-key" || !strings.Contains(string(data), `"model":"example-video"`) {
			t.Errorf("probe did not use the bound endpoint, model and isolated credential")
		}
		return reply(500, `{"code":"do_request_failed","message":"private material https://cdn.example/path?token=secret"}`), nil
	})}
	for range 2 {
		if err := work(ctx, client, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	var run model.ModelHealthRun
	if err := db.First(&run).Error; err != nil {
		t.Fatal(err)
	}
	if run.State != "uncertain" || run.Stage != "submission" || run.ReservationMicro != 100 || run.ActualMicro != nil || posts != 1 {
		t.Fatalf("ambiguous submission was retried or treated as free: %+v, posts=%d", run, posts)
	}
	if _, err := Enqueue(ctx, sel, p.Revision, 100); !IsValidation(err) {
		t.Fatalf("ambiguous result no longer blocks duplicate attempt: %v", err)
	}
	text, err := Evidence(ctx, run.ID)
	if err != nil || strings.Contains(text, "private material") || strings.Contains(text, "test-monitoring-key") || strings.Contains(text, "cdn.example") || !strings.Contains(text, "do_request_failed") && !strings.Contains(text, `"code"`) {
		t.Fatalf("unexpected evidence: %q / %v", text, err)
	}
	if err := Reconcile(ctx, run.ID, 12); err != nil {
		t.Fatal(err)
	}
	if n, err := spend(db, dayStart(time.Now().Unix())); err != nil || n != 12 {
		t.Fatalf("reconciled spend: %d / %v", n, err)
	}
}

func TestMediaSchedulePreflightAndNotificationPages(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	sel, _ := seedHealth(t, db, "video")
	global := defaultRule
	global.Enabled = true
	require.NoError(t, SaveRule(ctx, "global", Selection{}, global))
	pl, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	require.Equal(t, 7*86400, pl.Rule.IntervalSeconds)
	require.False(t, pl.Rule.Enabled)
	img, err := GetRule(ctx, "image")
	require.NoError(t, err)
	require.Equal(t, 5*86400, img.IntervalSeconds)
	require.Equal(t, 1, img.PerCycle)
	img.Enabled = true
	require.NoError(t, SaveRule(ctx, "video", Selection{}, img))
	pl, err = GetPlan(ctx, sel)
	require.NoError(t, err)
	require.Equal(t, img, pl.Rule)
	_, err = Enqueue(ctx, sel, pl.Revision, pl.Template.MaxMicro)
	require.NoError(t, err)
	require.NoError(t, releaseWorker(ctx, "fixture"))
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE", "")
	pl, err = GetPlan(ctx, sel)
	require.NoError(t, err)
	require.False(t, pl.Ready)
	require.Contains(t, pl.Blocked, "Monitoring worker is disabled")
	require.NotContains(t, pl.Blocked, "Dedicated probe credential missing or not bound to this upstream")
	require.Contains(t, pl.Blocked, "A detection is already running or requires reconciliation")
	for i := range 21 {
		id := fmt.Sprintf("target-%02d", i)
		require.NoError(t, model.SaveModelHealthConfig(db, "notify-"+id, "notification", Destination{ID: id}))
		require.NoError(t, db.Create(&model.ModelHealthDelivery{ID: id, CreatedAt: 123}).Error)
	}
	a, err := ListDestinations(ctx, 1)
	require.NoError(t, err)
	require.Len(t, a.Items, 20)
	require.True(t, a.HasMore)
	b, err := ListDestinations(ctx, 2)
	require.NoError(t, err)
	require.False(t, b.HasMore)
	require.Equal(t, "target-20", b.Items[0].ID)
	c, err := ListDeliveries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, c.Items, 20)
	require.True(t, c.HasMore)
	d, err := ListDeliveries(ctx, 2)
	require.NoError(t, err)
	require.False(t, d.HasMore)
	require.Equal(t, "target-00", d.Items[0].ID)
}

func TestDetectionRuleCadence(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	video, _ := seedHealth(t, db, "video")
	text, _ := seedHealth(t, db, "text")
	target, err := findTarget(db, video.Channel, video.Model, video.Endpoint)
	require.NoError(t, err)
	old := defaultRule
	old.Enabled, old.IntervalSeconds, old.PerCycle, old.PerDay = true, 1800, 2, 12
	old.FailAfter = 3
	require.NoError(t, SaveRule(ctx, "global", Selection{}, old))
	require.NoError(t, model.SaveModelHealthConfig(db, "type-video", "rule", old))
	require.NoError(t, model.SaveModelHealthConfig(db, "r-"+hash(target.ID, video.Endpoint)[:62], "rule", old))
	typed, err := GetRule(ctx, "video")
	require.NoError(t, err)
	assert.Equal(t, 7*86400, typed.IntervalSeconds)
	assert.Equal(t, 1, typed.PerCycle)
	assert.Equal(t, 1, typed.PerDay)
	assert.True(t, typed.Enabled)
	assert.Equal(t, 3, typed.FailAfter)
	plan, err := GetPlan(ctx, video)
	require.NoError(t, err)
	assert.Equal(t, typed, plan.Rule)
	textPlan, err := GetPlan(ctx, text)
	require.NoError(t, err)
	assert.Equal(t, 86400, textPlan.Rule.IntervalSeconds)
	assert.Equal(t, 1, textPlan.Rule.PerCycle)
	assert.Equal(t, 1, textPlan.Rule.PerDay)
	assert.ErrorContains(t, SaveRule(ctx, "video", Selection{}, old), "Paid media")
	assert.ErrorContains(t, SaveRule(ctx, "model", video, old), "Paid media")
	typed.IntervalSeconds = 4 * 86400
	require.NoError(t, SaveRule(ctx, "model", video, typed))
	plan, err = GetPlan(ctx, video)
	require.NoError(t, err)
	assert.Equal(t, typed, plan.Rule)
	assert.NoError(t, SaveRule(ctx, "llm", Selection{}, old))
	textPlan, err = GetPlan(ctx, text)
	require.NoError(t, err)
	assert.Equal(t, old, textPlan.Rule)
}

func TestMediaScheduleWaitsFullIntervalAfterLastProbe(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	t.Setenv("CHANNEL_TEST_ENABLED", "false")
	sel, _ := seedHealth(t, db, "video")
	rule := recommendedRule("video")
	rule.Enabled = true
	require.NoError(t, SaveRule(ctx, "video", Selection{}, rule))
	target, err := findTarget(db, sel.Channel, sel.Model, sel.Endpoint)
	require.NoError(t, err)
	now := time.Now().Unix()
	start := now - int64(rule.IntervalSeconds) + 120
	require.NoError(t, db.Create(&model.ModelHealthRun{
		Target: target.ID, ChannelID: sel.Channel, ModelName: sel.Model, Endpoint: sel.Endpoint,
		Source: "manual", State: "succeeded", CreatedAt: start - 180, StartedAt: start,
		ActualMicro: common.GetPointer(int64(1)),
	}).Error)
	schedule(ctx, now)
	var queued int64
	query := func() *gorm.DB {
		return db.Model(&model.ModelHealthRun{}).Where("target = ? AND source = ? AND state = ?", target.ID, "scheduled", "queued")
	}
	require.NoError(t, query().Count(&queued).Error)
	assert.Zero(t, queued)
	require.NoError(t, db.Model(&model.ModelHealthRun{}).Where("target = ?", target.ID).Update("started_at", start-240).Error)
	t.Setenv("CHANNEL_TEST_ENABLED", "")
	t.Setenv("CHANNEL_TEST_FREQUENCY", "")
	require.NoError(t, db.Create(&model.Option{Key: "monitor_setting.auto_test_channel_enabled", Value: "true"}).Error)
	schedule(ctx, now)
	require.NoError(t, query().Count(&queued).Error)
	assert.Zero(t, queued, "the legacy all-channel scheduler covers this target")
	require.NoError(t, db.Create(&model.Option{Key: "monitor_setting.channel_test_mode", Value: "auto_ban_only"}).Error)
	schedule(ctx, now)
	schedule(ctx, now)
	require.NoError(t, query().Count(&queued).Error)
	assert.EqualValues(t, 1, queued)
}

func TestStandaloneWorkerAdmissionAndHandoff(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	sel, plan := seedHealth(t, db, "text")
	d := Destination{ID: "ops", Name: "Ops", Kind: "feishu", URLRef: "MODEL_HEALTH_HOOK_FIXTURE", Interval: 60, MaxPerIncident: 1, MaxPerDay: 2, EndHour: 24}
	t.Setenv(d.URLRef, "https://open.feishu.cn/open-apis/bot/v2/hook/private-fixture")
	require.NoError(t, SaveDestination(ctx, d))
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	owned, err := acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	require.True(t, owned)

	// Act as an HTTP slot without either of the executor's secrets.
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE", "")
	t.Setenv(d.URLRef, "")
	require.True(t, WorkerAvailable(ctx))
	current, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	require.True(t, current.Ready)
	var row model.ModelHealthConfig
	require.NoError(t, db.Take(&row, "id = ?", workerStateID).Error)
	assert.NotContains(t, row.Value, "test-monitoring-key")
	assert.NotContains(t, row.Value, "private-fixture")
	assert.NotContains(t, row.Value, "provider.example")
	require.NoError(t, TestDestination(ctx, d.ID))
	run, err := Enqueue(ctx, sel, plan.Revision, plan.Template.MaxMicro)
	require.NoError(t, err)
	assert.Equal(t, "queued", run.State)

	owned, err = acquireWorker(ctx, "standby", workerState{})
	require.NoError(t, err)
	assert.False(t, owned)
	require.NoError(t, releaseWorker(ctx, "standby"))
	assert.True(t, WorkerAvailable(ctx))

	// An expired executor is offline even if the queue serializer is still used.
	require.NoError(t, model.SaveModelHealthConfig(db, workerStateID, "internal", workerState{Owner: "fixture", ExpiresAt: time.Now().Unix() - 1}))
	require.NoError(t, model.ModelHealthTransaction(ctx, func(*gorm.DB) error { return nil }))
	assert.False(t, WorkerAvailable(ctx))
	_, err = Enqueue(ctx, sel, plan.Revision, plan.Template.MaxMicro)
	assert.EqualError(t, err, "Monitoring worker is disabled")
	assert.EqualError(t, TestDestination(ctx, d.ID), "Monitoring worker is disabled")
	owned, err = acquireWorker(ctx, "standby", workerState{})
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, releaseWorker(ctx, "fixture"))
	assert.True(t, WorkerAvailable(ctx))
	current, err = GetPlan(ctx, sel)
	require.NoError(t, err)
	assert.Contains(t, current.Blocked, "Dedicated probe credential missing or not bound to this upstream")
	assert.EqualError(t, TestDestination(ctx, d.ID), "Notification destination is not configured on the monitoring worker")
	require.NoError(t, releaseWorker(ctx, "standby"))
	assert.False(t, WorkerAvailable(ctx))
}

func TestStandaloneWorkerDrainsClaimedSubmission(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	sel, plan := seedHealth(t, db, "video")
	run, err := Enqueue(ctx, sel, plan.Revision, plan.Template.MaxMicro)
	require.NoError(t, err)
	require.NoError(t, releaseWorker(ctx, "fixture"))

	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	allowResponse := sync.OnceFunc(func() { close(finish) })
	var methods []string
	client := &http.Client{Transport: mockProvider(func(r *http.Request) (*http.Response, error) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			assert.Equal(t, "/v1/videos/task-fixture", r.URL.Path)
			return reply(200, `{"status":"queued"}`), nil
		}
		close(started)
		select {
		case <-finish:
			return reply(202, `{"id":"task-fixture"}`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}
	go func() {
		defer close(done)
		runWorker(ctx, client, "active")
	}()
	t.Cleanup(func() {
		cancel()
		allowResponse()
		<-done
	})
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not reach the queued submission")
	}
	cancel()
	allowResponse()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not drain and release its lease")
	}
	require.NoError(t, db.First(&run, run.ID).Error)
	require.Equal(t, "polling", run.State)
	require.Equal(t, "task-fixture", run.UpstreamTaskID)
	assert.False(t, WorkerAvailable(context.Background()))
	require.NoError(t, work(context.Background(), client, run.NextPoll))
	assert.Equal(t, []string{http.MethodPost, http.MethodGet}, methods)
}

func TestOpsCostsUseOnlyConfirmedMatchingReceipts(t *testing.T) {
	now := time.Date(2026, 11, 22, 16, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, opsCostsPath, r.URL.Path)
		require.NotEmpty(t, r.Header.Get("X-Signature"))
		refs := r.URL.Query()["probe"]
		require.Len(t, refs, 2)
		require.NotContains(t, r.URL.RawQuery, "task-fixture")
		week := time.Date(2026, 11, 23, 0, 0, 0, 0, cst).Unix()
		require.Equal(t, fmt.Sprint(week), r.URL.Query().Get("week_start"))
		fmt.Fprintf(w, `{"data":{"source":"model_health_receipts","currency":"CNY","week_start":%d,"week_micro":0,"total_micro":10,"amounts":{%q:0,%q:null,"unrelated":10}}}`, week, refs[0], refs[1])
	}))
	defer server.Close()
	local := int64(888)
	result := readOpsCosts(context.Background(), opsclient.New(server.URL, "test", "fixture-secret-only", time.Second), []model.ModelHealthRun{
		{ID: 1, ChannelID: 3, UpstreamTaskID: "task-fixture", ActualMicro: &local},
		{ID: 2, ChannelID: 3, RequestID: "fixture-request", ActualMicro: &local},
		{ID: 3, ChannelID: 3, ActualMicro: &local},
	}, now)
	require.Equal(t, "confirmed", result.State)
	require.Equal(t, map[int64]int64{1: 0}, result.Amounts)
	require.EqualValues(t, 0, *result.WeekMicro)
	require.EqualValues(t, 10, *result.TotalMicro)
}

func TestUserEvidenceShowsOnlyMatchingChannelAndTaskMetadata(t *testing.T) {
	db := healthDB(t)
	ch := model.Channel{Name: "Local evidence provider", Key: "private-channel-key"}
	require.NoError(t, db.Create(&ch).Error)
	event := model.OpsRequestEvent{RequestID: "local-call", ChannelID: ch.Id, UserID: 3, ModelName: "platform-video", TaskID: "local-task"}
	require.NoError(t, db.Create(&event).Error)
	for i, channel := range []int{ch.Id, ch.Id + 1} {
		require.NoError(t, db.Create(&model.OpsUpstreamAttempt{RequestID: event.RequestID, ChannelID: channel, AttemptIndex: i, StatusCode: 502 + i}).Error)
	}
	task := model.Task{UserId: event.UserID, ChannelId: ch.Id, TaskID: event.TaskID, Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "provider-task", Key: "private-task-key"}}
	require.NoError(t, db.Create(&task).Error)
	text, err := UserEvidence(context.Background(), event.ID)
	require.NoError(t, err)
	for _, value := range []string{"Local evidence provider", "provider-task", "HTTP 502", "SUCCESS", "未采集"} {
		require.Contains(t, text, value)
	}
	for _, value := range []string{"HTTP 503", "private-channel-key", "private-task-key"} {
		require.NotContains(t, text, value)
	}
}

func TestTaskSubmissionDoesNotMeanArtifactSuccess(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	sel, p := seedHealth(t, db, "video")
	ctx := context.Background()
	r, err := Enqueue(ctx, sel, p.Revision, 100)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: mockProvider(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/videos":
			return reply(202, `{"id":"task-123"}`), nil
		case "/v1/videos/task-123":
			return reply(200, `{"status":"done","metadata":{"url":"https://cdn.example/video.mp4?sig=private-signature"},"private_key_name":"private content"}`), nil
		case "/video.mp4":
			if req.Header.Get("Authorization") != "" {
				t.Error("download leaked provider authorization")
			}
			res := reply(206, "\x00\x00\x00\x10ftypisom\x00\x00\x00\x00")
			res.Header.Set("Content-Type", "text/plain")
			res.Header.Set("Content-Range", "bytes 0-15/42")
			res.Header.Set("Set-Cookie", "session=private-cookie")
			return res, nil
		default:
			t.Fatalf("wrong probe endpoint: %v", req.URL.Path)
			return nil, nil
		}
	})}
	now := time.Now().Unix()
	if err := work(ctx, client, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&r, r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if r.State != "polling" || r.UpstreamTaskID != "task-123" {
		t.Fatalf("submission not kept pending: %+v", r)
	}
	if err := work(ctx, client, r.NextPoll); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&r, r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if r.State != "failed" || r.Stage != "validation" {
		t.Fatalf("broken artifact incorrectly healthy: %+v", r)
	}
	require.Equal(t, "video_download_content_type_mismatch", r.Reason)
	md, err := Evidence(ctx, r.ID)
	require.NoError(t, err)
	for _, observed := range []string{"task-123", "请求头", "请求体", "等待响应头", "响应头", "响应体", "text/plain", "Content-Range", "isom", "206"} {
		require.Contains(t, md, observed)
	}
	for _, secret := range []string{"private-signature", "private-cookie", "private_key_name", "test-monitoring-key", "cdn.example/video.mp4"} {
		require.NotContains(t, md, secret)
	}
}

func TestNotificationDedupAndRecovery(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	now := time.Now().Unix()
	d := Destination{ID: "team", MaxPerDay: 20, MaxPerIncident: 2, Interval: 60, Recovery: true}
	target := Target{ID: "target", ChannelID: 1, Model: "test", Upstream: "test-v2"}
	rule := Rule{FailAfter: 2, RecoverAfter: 2}
	failed := []Sample{{State: "failed", Source: "user", RequestID: "r2", At: now - 2}, {State: "failed", Source: "user", RequestID: "r1", At: now - 10}}
	for range 2 {
		notifyTarget(context.Background(), db, target, "openai", rule, d, failed, now)
	}
	var deliveries []model.ModelHealthDelivery
	if err := db.Find(&deliveries).Error; err != nil || len(deliveries) != 1 {
		t.Fatalf("dedup deliveries: %d %v", len(deliveries), err)
	}
	if err := db.Model(&deliveries[0]).Update("state", "sent").Error; err != nil {
		t.Fatal(err)
	}
	recovered := append([]Sample{{State: "succeeded", At: now}, {State: "succeeded", At: now - 1}}, failed...)
	for range 2 {
		notifyTarget(context.Background(), db, target, "openai", rule, d, recovered, now)
	}
	deliveries = nil
	if err := db.Where("event = ?", "recovery").Find(&deliveries).Error; err != nil || len(deliveries) != 1 {
		t.Fatalf("recovery deliveries: %d %v", len(deliveries), err)
	}
}

func TestChannelAndEndpointEvidenceStaySeparate(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	now := time.Now().Unix()
	rows := []model.OpsRequestEvent{
		{ChannelID: 4, ModelName: "example", UpstreamModelName: "mapped", Method: "POST", Path: "/v1/messages", OccurredAtMs: now * 1000, CompletedAtMs: now * 1000, StatusCode: 500, UpstreamStatusCode: 500},
		{ChannelID: 4, ModelName: "example", UpstreamModelName: "mapped", Method: "POST", Path: "/v1/chat/completions", OccurredAtMs: now * 1000, CompletedAtMs: now * 1000, Success: true},
		{ChannelID: 5, ModelName: "example", UpstreamModelName: "mapped", Method: "POST", Path: "/v1/messages", OccurredAtMs: now * 1000, CompletedAtMs: now * 1000, Success: true},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	target := Target{ChannelID: 4, Model: "example", Upstream: "mapped"}
	for endpoint, want := range map[string]string{"anthropic": "abnormal", "openai": "healthy"} {
		obs, err := samples(db, target, endpoint, now-1, now+1)
		if err != nil {
			t.Fatal(err)
		}
		if status := summarize(endpoint, obs, defaultRule, now); status.State != want || status.UserCount != 1 || status.ProbeCount != 0 {
			t.Fatalf("%s: %+v", endpoint, status)
		}
	}
}

func TestOverviewIsReadOnlyAndThirtyDayRangeIsAccepted(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	channel := model.Channel{Name: "Needs review", Type: constant.ChannelTypeAdvancedCustom, Models: "example-chat", Status: common.ChannelStatusEnabled, OtherSettings: "invalid json"}
	require.NoError(t, db.Create(&channel).Error)
	now := time.Now().Unix()
	filter := Filter{From: now - 30*86400, To: now, Limit: 20}
	result, err := List(context.Background(), filter)
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	assert.True(t, result.Rows[0].Paused)
	var current model.Channel
	require.NoError(t, db.First(&current, channel.Id).Error)
	assert.Equal(t, channel.OtherSettings, current.OtherSettings)
	filter.From--
	_, err = List(context.Background(), filter)
	assert.True(t, IsValidation(err))
}

func TestUnverifiedPreviousMonthChargeStillReservesBudget(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.ModelHealthRun{State: "succeeded", CreatedAt: monthStart(now) - 1, ReservationMicro: 100}).Error)
	used, err := spend(db, monthStart(now))
	require.NoError(t, err)
	assert.EqualValues(t, 100, used)
}

func TestDownloadedMP4RequiresACompleteContainer(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "public", "images", "home", "underwater.mp4"))
	require.NoError(t, err)
	assert.True(t, validProbeMP4(data))
	assert.False(t, validProbeMP4(data[:len(data)/2]), "an interrupted download must not be marked healthy")
}

func TestMonitoringEgressDeniesPrivateAndTranslatedAddresses(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "100.100.100.200", "::1", "::ffff:127.0.0.1", "fc00::1", "64:ff9b::7f00:1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("blocked address accepted: %s", ip)
		}
	}
	for _, address := range []string{"http://provider.example", "https://user@provider.example", "https://provider.example:8080", "https://127.0.0.1/"} {
		if _, err := checkedURL(address); err == nil {
			t.Errorf("invalid address accepted: %s", address)
		}
	}
}
