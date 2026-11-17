package opsfees

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/QuantumNous/new-api/service/businessbilling"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSharedFeeNoticeUsesBoundedBackgroundDelivery(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	oldClient, oldEnabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		client.Close()
		common.RDB, common.RedisEnabled = oldClient, oldEnabled
		model.ConfigureOpsFees(nil)
	})
	c := Config{Connection: "shared-test"}
	share := shareResults(ctx, c)
	require.NotNil(t, share)
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: c.Connection})
	log := &model.Log{RequestId: "shared", UserId: 15, ModelName: "m", Type: model.LogTypeConsume, Other: "{}"}
	model.RememberOpsFeePending(log)
	require.Eventually(t, func() bool {
		channels, _ := client.PubSubChannels(ctx, "ops-fee-results:shared-test").Result()
		return len(channels) == 1
	}, time.Second, time.Millisecond)
	amount := "0.0042"
	// Publishing simulates another blue/green process; the local request has
	// no callback or database path that can supply this fee.
	share([]model.OpsFee{{RequestID: "shared", UserID: "15", Model: "m", Amount: &amount, ChargedAt: "2026-11-17T01:00:00Z", Status: "available"}}, false)
	require.Eventually(t, func() bool { fee := model.WaitOpsFee(ctx, log, 0); return fee != nil && fee.Status == "available" }, time.Second, time.Millisecond)
	assert.Equal(t, "0.0042", *model.WaitOpsFee(ctx, log, 0).Amount)
}

func TestPushLogFeeMemoryAndCorrection(t *testing.T) {
	oldDB, oldLogs := model.DB, model.LOG_DB
	db := testDB(t, "sqlite")
	model.DB, model.LOG_DB = db, db
	require.NoError(t, model.MigrateOpsFees(db))
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "receipt-test"})
	c := Config{BaseURL: "https://ops.example", KeyID: "fees", Secret: "fixture-secret-for-receipts", Connection: "receipt-test", ReceiptKeyID: "push"}
	oldConfig := receiptConfig.Swap(&c)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogs
		model.ConfigureOpsFees(nil)
		receiptConfig.Store(oldConfig)
	})
	log := &model.Log{RequestId: "fast", UserId: 15, TokenId: 10, ModelName: "sample", Type: model.LogTypeConsume, Other: "{}", CreatedAt: time.Now().Unix()}
	model.RememberOpsFeePending(log)
	sent := []byte(`{"events":[{"requestId":"fast","userId":"15","model":"sample"}]}`)
	response := []byte(`{"data":{"charges":[{"requestId":"fast","userId":"15","status":"station_billed","logFee":{"requestId":"fast","userId":"15","model":"sample","opsAmount":"0.000006","refundAmount":"0","chargedAt":"2026-11-17T01:00:00Z"}}]}}`)
	AcceptPushReceipts(c.BaseURL, "unbound", c.Secret, sent, response, time.Now())
	assert.Equal(t, "waiting", model.WaitOpsFee(context.Background(), log, 0).Status)
	AcceptPushReceipts(c.BaseURL, "push", c.Secret, sent, response, time.Now())
	got := model.WaitOpsFee(context.Background(), log, 0)
	require.NotNil(t, got)
	require.NotNil(t, got.Amount)
	assert.Equal(t, "0.000006", *got.Amount)
	reads := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("count_fee_reads", func(*gorm.DB) { reads++ }))
	for range 3 {
		rows, err := model.LogFeeLogs(context.Background(), []string{"fast"}, 15)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.NoError(t, model.AttachOpsFees(context.Background(), rows))
		fee, _, reason, err := model.BusinessOpsFee(context.Background(), log)
		require.NoError(t, err)
		assert.Empty(t, reason)
		assert.Equal(t, "0.000006", *fee.Amount)
	}
	assert.Zero(t, reads, "hot fee and authorized log refresh must perform no SQL")
	rows, err := model.LogFeeLogs(context.Background(), []string{"fast"}, 16)
	require.NoError(t, err)
	assert.Empty(t, rows)
	wrong := *log
	wrong.ModelName = "another"
	assert.Nil(t, model.WaitOpsFee(context.Background(), &wrong, 0))
	zero := "0"
	got.Amount = &zero
	model.AcceptOpsFeeResults(c.Connection, []model.OpsFee{*got}, true)
	AcceptPushReceipts(c.BaseURL, "push", c.Secret, sent, response, time.Now())
	assert.Equal(t, "0", *model.WaitOpsFee(context.Background(), log, 0).Amount, "late push must not undo feed correction")
	billing := businessbilling.Receipt(context.Background(), "ops", businessapi.UnavailableBilling(), log)
	assert.Equal(t, "available", billing.Status)
	assert.Equal(t, "0", *billing.Amount)
	assert.Empty(t, billing.ChargeStatus)
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "other-tenant"})
	assert.Nil(t, model.WaitOpsFee(context.Background(), log, 0))
}

func TestMemoryFeeWaitWakesAndRespectsCancellation(t *testing.T) {
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "wait-test"})
	t.Cleanup(func() { model.ConfigureOpsFees(nil) })
	log := &model.Log{RequestId: "notify", UserId: 15, ModelName: "sample", Type: model.LogTypeConsume}
	model.RememberOpsFeePending(log)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, "waiting", model.WaitOpsFee(ctx, log, time.Second).Status)
	result := make(chan *model.OpsFee, 1)
	go func() { result <- model.WaitOpsFee(context.Background(), log, time.Second) }()
	amount := "0.000006"
	model.AcceptOpsFeeResults("wait-test", []model.OpsFee{{RequestID: log.RequestId, UserID: "15", Model: "sample", Amount: &amount, Currency: "CNY", Status: "available"}}, false)
	select {
	case fee := <-result:
		require.NotNil(t, fee)
		assert.Equal(t, "available", fee.Status)
		assert.Equal(t, amount, *fee.Amount)
	case <-time.After(2 * time.Second):
		t.Fatal("receipt notification did not release the waiting request")
	}
}

func BenchmarkHotOpsFeeMemory(b *testing.B) {
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "benchmark"})
	b.Cleanup(func() { model.ConfigureOpsFees(nil) })
	log := &model.Log{RequestId: "hot", UserId: 15, ModelName: "sample", Type: model.LogTypeConsume}
	model.RememberOpsFeePending(log)
	amount := "0.000006"
	model.AcceptOpsFeeResults("benchmark", []model.OpsFee{{RequestID: "hot", UserID: "15", Model: "sample", Amount: &amount, Status: "available"}}, false)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			fee := model.WaitOpsFee(context.Background(), log, 0)
			if fee == nil || fee.Amount == nil || *fee.Amount != amount {
				b.Error("hot receipt missing")
				return
			}
		}
	})
}

func testDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(filepath.Join(t.TempDir(), "fee.db"))
	case "mysql":
		dsn := os.Getenv("OPS_FEE_TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("set OPS_FEE_TEST_MYSQL_DSN for real MySQL verification")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("OPS_FEE_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("set OPS_FEE_TEST_POSTGRES_DSN for real PostgreSQL verification")
		}
		driver = postgres.Open(dsn)
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		require.FailNow(t, fmt.Sprint("test database unavailable"))
	}
	raw, _ := db.DB()
	t.Cleanup(func() { raw.Close() })
	common.SetMainDatabaseType(common.DatabaseType(dialect))
	if err = db.Migrator().DropTable(&model.OpsFee{}, &model.OpsFeeSync{}, &model.Log{}); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	return db
}

func feePage(t *testing.T, amount any, cursor string, more bool) []byte {
	t.Helper()
	raw, err := common.Marshal(map[string]any{"data": map[string]any{"items": []any{map[string]any{
		"requestId": "req-1", "userId": "15", "model": "sample", "opsAmount": amount, "stationAmount": "0.12", "refundAmount": "0", "difference": "0.02", "chargedAt": "2026-11-03T08:00:00Z",
	}}, "nextCursor": cursor, "hasMore": more}})
	if err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	return raw
}

func TestOpsFeeSignatureAndInstancePrefix(t *testing.T) {
	h := opsclient.SignWithNonce("gw", "demo-secret-for-signature-test-vector-only", "GET", "/api/v1/catalog/v1/fees", nil, "1792540800", "8f14e45fceea167a5a36dedd4bea2543")
	if got := h.Get("X-Signature"); got != "9c50d0d5aecffe326ba4ba430e9e3cff7a11a57e65adadc9c36dbc02c77ff7a6" {
		require.FailNow(t, fmt.Sprintf("signature differs: %s", got))
	}
	target := "/api/v1/catalog/v1/fees?since=2026-11-03T00%3A00%3A00Z~req"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/test"+target {
			assert.Fail(t, fmt.Sprintf("wrong instance target %s", r.URL.RequestURI()))
		}
		want := opsclient.SignWithNonce("gw", "test-secret-at-least-sixteen", r.Method, target, nil, r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"))
		if r.Header.Get("X-Signature") != want.Get("X-Signature") {
			assert.Fail(t, fmt.Sprint("instance prefix must be stripped only for signing"))
		}
		w.Write([]byte("{}"))
	}))
	defer server.Close()
	if _, err := opsclient.New(server.URL+"/test", "gw", "test-secret-at-least-sixteen", time.Second).Get(context.Background(), target, time.Now()); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	for _, base := range []string{"http://untrusted.example", "https://user:secret@example.com", "https://example.com/test?x=1", "https://example.com/../test"} {
		if opsclient.ValidateBase(base) == nil {
			assert.Fail(t, fmt.Sprintf("accepted invalid base %s", base))
		}
	}
	if err := opsclient.ValidateBase("https://ops.example.com/project"); err != nil {
		require.FailNow(t, fmt.Sprint("deployment prefix must be configurable"))
	}
}

func TestOpsFeeDatabaseMatrix(t *testing.T) {
	oldDB := model.DB
	oldType := common.MainDatabaseType()
	t.Cleanup(func() { model.DB = oldDB; common.SetMainDatabaseType(oldType); model.ConfigureOpsFees(nil) })
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := testDB(t, dialect)
			model.DB = db
			// Fresh migration, then repeat. Existing released logs remain byte-for-byte intact.
			if err := db.AutoMigrate(&model.Log{}); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			original := model.Log{UserId: 15, Type: model.LogTypeConsume, RequestId: "req-1", ModelName: "sample", CreatedAt: 100, Quota: 12345, Other: "existing-data"}
			if err := db.Create(&original).Error; err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			for range 2 {
				if err := model.MigrateOpsFees(db); err != nil {
					require.FailNow(t, fmt.Sprint(err))
				}
			}
			ctx := context.Background()
			state, err := model.ClaimOpsFeeSync(ctx, db, "connection", "owner-a", 200)
			if err != nil || state == nil {
				require.FailNow(t, fmt.Sprintf("claim %v", err))
			}
			second, err := model.ClaimOpsFeeSync(ctx, db, "connection", "owner-b", 201)
			if err != nil || second != nil {
				require.FailNow(t, fmt.Sprintf("duplicate worker %v", err))
			}
			reserved, err := model.ReserveOpsFeeRequest(ctx, db, "connection", "owner-a", 201000, 3000)
			require.NoError(t, err)
			require.True(t, reserved)
			reserved, err = model.ReserveOpsFeeRequest(ctx, db, "connection", "owner-a", 202000, 3000)
			require.NoError(t, err)
			require.False(t, reserved, "a new worker must obey the shared request gap")
			rows, cursor, _, err := decodePage(feePage(t, "0.10", "cursor-1", false), "connection", 201)
			if err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			if err = model.SaveOpsFeePage(ctx, db, "connection", "owner-a", cursor, rows, 201); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			// Same-page retry must work even on MySQL where unchanged UPDATE reports zero.
			if err = model.SaveOpsFeePage(ctx, db, "connection", "owner-a", cursor, rows, 201); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "connection"})
			logs := []*model.Log{&original, {UserId: 16, Type: 2, RequestId: "req-1", ModelName: "sample"}, {UserId: 15, Type: 2, RequestId: "req-1", ModelName: "another"}, {UserId: 15, Type: 5, RequestId: "req-1", ModelName: "sample"}}
			if err = model.AttachOpsFees(ctx, logs); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			if logs[0].OpsFee.Status != "available" || *logs[0].OpsFee.Amount != "0.10" || logs[1].OpsFee.Status != "waiting" || logs[2].OpsFee.Status != "waiting" || logs[3].OpsFee != nil {
				require.FailNow(t, fmt.Sprint("fee leaked across user/model/log kind"))
			}
			// Fee display is independent of who debited the wallet. Existing results
			// also remain visible before the configured initial polling watermark.
			model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "connection", Scope: "project-a", Since: 999})
			scoped := []*model.Log{
				{UserId: 15, Type: 2, RequestId: "req-1", ModelName: "sample", Other: `{"billing_scope":"project-a"}`},
				{UserId: 15, Type: 2, RequestId: "req-1", ModelName: "sample"},
				{UserId: 15, Type: 2, RequestId: "req-1", ModelName: "sample", Other: `{"billing_scope":"project-b"}`},
				{UserId: 15, Type: 2, RequestId: "new-project", ModelName: "sample", Other: `{"billing_scope":"project-a"}`},
			}
			require.NoError(t, model.AttachOpsFees(ctx, scoped))
			require.NotNil(t, scoped[0].OpsFee)
			assert.Equal(t, "available", scoped[0].OpsFee.Status)
			assert.Equal(t, "available", scoped[1].OpsFee.Status)
			assert.Equal(t, "available", scoped[2].OpsFee.Status)
			require.NotNil(t, scoped[3].OpsFee)
			assert.Equal(t, "waiting", scoped[3].OpsFee.Status)
			model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "connection"})
			// A correction overwrites one result, with no duplicate and no original-charge update.
			zero := "0"
			rows[0].Amount = &zero
			if err = model.SaveOpsFeePage(ctx, db, "connection", "owner-a", "cursor-2", rows, 202); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			var count int64
			db.Model(&model.OpsFee{}).Count(&count)
			if count != 1 {
				require.FailNow(t, fmt.Sprint("retry duplicated fee"))
			}
			var saved model.Log
			if err = db.First(&saved, original.Id).Error; err != nil || saved.Quota != 12345 || saved.Other != "existing-data" {
				require.FailNow(t, fmt.Sprint("original charge changed"))
			}
			// A failed insert rolls cursor back as part of the same transaction.
			if err = db.Migrator().DropTable(&model.OpsFee{}); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			if model.SaveOpsFeePage(ctx, db, "connection", "owner-a", "must-not-save", rows, 203) == nil {
				require.FailNow(t, fmt.Sprint("missing table should fail"))
			}
			db.First(&state, "connection = ?", "connection")
			if state.Cursor != "cursor-2" {
				require.FailNow(t, fmt.Sprint("failed page advanced cursor"))
			}
			if _, err = model.ClaimOpsFeeSync(ctx, db, "connection", "owner-b", 246); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			if model.SaveOpsFeePage(ctx, db, "connection", "owner-a", "stale", nil, 247) == nil {
				require.FailNow(t, fmt.Sprint("expired owner wrote cursor"))
			}
			for range 2 {
				if err = model.MigrateOpsFees(db); err != nil {
					require.FailNow(t, fmt.Sprint(err))
				}
			}
			// Upgrade preserves the existing log index and unique composite fee identity.
			if !db.Migrator().HasIndex(&model.Log{}, "idx_logs_request_id") {
				require.FailNow(t, fmt.Sprint("log request index lost"))
			}
			if err = model.SaveOpsFeePage(ctx, db, "connection", "owner-b", "recovered", rows, 247); err != nil {
				require.FailNow(t, fmt.Sprint(err))
			}
			if err = db.Create(&rows[0]).Error; err == nil {
				require.FailNow(t, fmt.Sprint("composite primary key not enforced"))
			}
		})
	}
}

func TestProductCallsKeepFeeReaderActive(t *testing.T) {
	oldType := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(oldType); model.ConfigureOpsFees(nil) })
	db := testDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	at := time.Now()
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Scope: "project-a"})
	require.NoError(t, db.Create(&model.Log{Type: 2, CreatedAt: at.Unix(), Other: "{}"}).Error)
	assert.True(t, model.OpsFeesRecentlyActive(context.Background(), db, at))
	assert.False(t, model.OpsFeesRecentlyActive(context.Background(), db, at.Add(11*time.Minute)))
}

func TestOpsFeeWorkerRetryAndCursor(t *testing.T) {
	old := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(old) })
	db := testDB(t, "sqlite")
	if err := db.AutoMigrate(&model.Log{}); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	if err := model.MigrateOpsFees(db); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	now := time.Unix(1793692800, 0)
	calls := 0
	mode := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if mode != 200 {
			w.Header().Set("Retry-After", "180")
			w.WriteHeader(mode)
			return
		}
		if calls == 1 {
			w.Write(feePage(t, "0", "cursor-1", false))
		} else {
			raw, _ := common.Marshal(map[string]any{"data": map[string]any{"items": []any{}, "nextCursor": nil, "hasMore": false}})
			w.Write(raw)
		}
	}))
	defer server.Close()
	c := Config{BaseURL: server.URL + "/test", KeyID: "gw", Secret: "test-secret-at-least-sixteen", Connection: "worker", ActiveInterval: 30 * time.Second, IdleInterval: 5 * time.Minute, Timeout: time.Second, RoundBudget: time.Second, MinRequestGap: 3 * time.Second, MaxPages: 3}
	worker := New(db, db, c)
	worker.Now = func() time.Time { return now }
	wait, err := worker.Step(context.Background())
	if err != nil || wait != 5*time.Minute || calls != 1 {
		require.FailNow(t, fmt.Sprintf("initial read %v %v %d", wait, err, calls))
	}
	if _, err = worker.Step(context.Background()); err != nil || calls != 1 {
		require.FailNow(t, fmt.Sprint("idle read repeated before schedule"))
	}
	now = now.Add(wait)
	if _, err = worker.Step(context.Background()); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	var state model.OpsFeeSync
	db.First(&state, "connection = ?", "worker")
	if state.Cursor != "cursor-1" {
		require.FailNow(t, fmt.Sprint("empty page erased cursor"))
	}
	now = now.Add(wait)
	mode = 429
	wait, err = worker.Step(context.Background())
	if err == nil || wait < 180*time.Second {
		require.FailNow(t, fmt.Sprint("Retry-After ignored"))
	}
	before := calls
	if _, _ = worker.Step(context.Background()); calls != before {
		require.FailNow(t, fmt.Sprint("error backoff bypassed"))
	}
	now = now.Add(wait)
	mode = 403
	wait, err = worker.Step(context.Background())
	if err == nil || wait < 5*time.Minute {
		require.FailNow(t, fmt.Sprint("permission failure retries too fast"))
	}
	db.First(&state, "connection = ?", "worker")
	if state.Cursor != "cursor-1" || state.Owner != "" {
		require.FailNow(t, fmt.Sprint("failed read changed progress or retained lease"))
	}
}

func TestOpsFeeFastPollingKeepsIdleAndBackoff(t *testing.T) {
	c := Config{BaseURL: "https://ops.example/test", KeyID: "fees", Secret: "same-tenant-test-secret"}
	require.True(t, c.AcceptsReceipts(opsclient.New(c.BaseURL, c.KeyID, c.Secret, time.Second)))
	require.False(t, c.AcceptsReceipts(opsclient.New(c.BaseURL, "project", "project-test-secret", time.Second)))
	c.ReceiptKeyID = "project"
	require.True(t, c.AcceptsReceipts(opsclient.New(c.BaseURL, "project", "project-test-secret", time.Second)))
	require.False(t, c.AcceptsReceipts(opsclient.New("https://other.example", "project", "project-test-secret", time.Second)))
	for key, value := range map[string]string{"OPS_FEES_URL": "https://ops.example/test", "OPS_FEES_KEY_ID": "gw", "OPS_FEES_SECRET": "test-secret-at-least-sixteen", "OPS_FEES_ACTIVE_SECONDS": "", "OPS_FEES_IDLE_SECONDS": "", "OPS_FEES_REQUEST_GAP_SECONDS": "", "OPS_FEES_TIMEOUT_SECONDS": "", "OPS_FEES_SINCE": ""} {
		t.Setenv(key, value)
	}
	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, cfg.ActiveInterval)
	t.Setenv("OPS_FEES_ACTIVE_SECONDS", "3")
	cfg, err = LoadConfig()
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, cfg.ActiveInterval)
	t.Setenv("OPS_FEES_ACTIVE_SECONDS", "2")
	_, err = LoadConfig()
	require.Error(t, err)

	old := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(old) })
	db := testDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	require.NoError(t, model.MigrateOpsFees(db))
	at := time.Unix(1793692800, 0)
	calls, status := 0, 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		raw, _ := common.Marshal(map[string]any{"data": map[string]any{"items": []any{}, "hasMore": false, "nextCursor": nil}})
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	cfg.BaseURL, cfg.Connection, cfg.ActiveInterval = server.URL, "fast-poll", 5*time.Second
	worker := New(db, db, cfg)
	worker.Now = func() time.Time { return at }
	wait, err := worker.Step(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, wait)
	require.Equal(t, 1, calls)
	// A product call arrives through a different process during idle mode.
	require.NoError(t, db.Create(&model.Log{Type: 2, CreatedAt: at.Unix()}).Error)
	at = at.Add(5 * time.Second)
	wait, err = worker.Step(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, wait)
	require.Equal(t, 2, calls)
	at = at.Add(time.Second)
	_, err = worker.Step(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, calls, "active polling must respect its schedule")
	at = at.Add(4 * time.Second)
	status = 429
	wait, err = worker.Step(context.Background())
	require.Error(t, err)
	require.GreaterOrEqual(t, wait, time.Minute)
	at = at.Add(5 * time.Second)
	_, _ = worker.Step(context.Background())
	require.Equal(t, 3, calls, "activity must never bypass an outage backoff")
}

func TestOpsFeePageValidationAndDisabledMode(t *testing.T) {
	for _, tc := range []struct {
		value  any
		status string
	}{{nil, "unconfirmed"}, {"0", "available"}, {"0.0012", "available"}} {
		rows, _, _, err := decodePage(feePage(t, tc.value, "c", false), "c", 1)
		if err != nil || rows[0].Status != tc.status {
			require.FailNow(t, fmt.Sprintf("null/zero semantics %v %v", tc, err))
		}
	}
	for _, value := range []string{"-0.1", "1e1000000000", strings.Repeat("9", 101), "bad"} {
		if _, _, _, err := decodePage(feePage(t, value, "c", false), "c", 1); err == nil {
			assert.Fail(t, fmt.Sprintf("accepted invalid amount %s", value))
		}
	}
	raw := feePage(t, "1", "c", false)
	var data map[string]any
	common.Unmarshal(raw, &data)
	page := data["data"].(map[string]any)
	page["items"] = append(page["items"].([]any), page["items"].([]any)...)
	raw, _ = common.Marshal(data)
	rows, _, _, err := decodePage(raw, "c", 1)
	if err != nil || len(rows) != 1 {
		require.FailNow(t, fmt.Sprint("duplicate correction not collapsed"))
	}
	for _, body := range []string{"{}", "null", fmt.Sprintf("%s", []byte("not-json"))} {
		if _, _, _, err = decodePage([]byte(body), "c", 1); err == nil {
			require.FailNow(t, fmt.Sprint("accepted malformed page"))
		}
	}
	t.Setenv("OPS_FEES_ENABLED", "")
	if err = StartConfigured(context.Background()); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
}

func TestOpsFeeStartupKeepsReceiptBindingOptional(t *testing.T) {
	oldDB, oldLogs, oldType := model.DB, model.LOG_DB, common.MainDatabaseType()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogs
		common.SetMainDatabaseType(oldType)
		model.ConfigureOpsFees(nil)
	})
	db := testDB(t, "sqlite")
	model.DB, model.LOG_DB = db, db
	for key, value := range map[string]string{
		"OPS_FEES_ENABLED": "true", "OPS_FEES_URL": "https://ops.example.com",
		"OPS_FEES_KEY_ID": "fees", "OPS_FEES_SECRET": "fee-reader-fixture-secret",
	} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Verify startup wiring without starting network work.
	require.NoError(t, StartConfigured(ctx))
	logs := []*model.Log{{UserId: 15, Type: 2, RequestId: "receipt", ModelName: "sample"}}
	require.NoError(t, model.AttachOpsFees(context.Background(), logs))
	assert.Equal(t, "waiting", logs[0].OpsFee.Status)
	bound := false
	require.NoError(t, StartConfigured(ctx, func(c Config, runtime *model.OpsFeeRuntime) {
		bound = true
		assert.Equal(t, c.Connection, runtime.Connection)
		runtime.CachedReceipt = func(id string, user int, name string, at time.Time) *model.OpsFee {
			amount := "0.000006"
			return &model.OpsFee{RequestID: id, UserID: "15", Model: name, Amount: &amount, Status: "available", Currency: "CNY"}
		}
	}))
	require.True(t, bound)
	require.NoError(t, model.AttachOpsFees(context.Background(), logs))
	assert.Equal(t, "0.000006", *logs[0].OpsFee.Amount)
}

func TestOpsFeeSeparateLogDatabase(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldType := common.MainDatabaseType()
	t.Cleanup(func() {
		model.DB = oldDB
		model.LOG_DB = oldLogDB
		common.SetMainDatabaseType(oldType)
		model.ConfigureOpsFees(nil)
	})
	primary := testDB(t, "sqlite")
	logs := testDB(t, "sqlite")
	model.DB = primary
	model.LOG_DB = logs
	require.NoError(t, model.MigrateOpsFees(primary))
	require.NoError(t, logs.AutoMigrate(&model.Log{}))
	entry := model.Log{UserId: 15, Type: 2, RequestId: "req-1", ModelName: "sample", CreatedAt: 100, Quota: 123}
	require.NoError(t, logs.Create(&entry).Error)
	rows, cursor, _, err := decodePage(feePage(t, "0.10", "cursor", false), "separate", 201)
	require.NoError(t, err)
	_, err = model.ClaimOpsFeeSync(context.Background(), primary, "separate", "owner", 200)
	require.NoError(t, err)
	require.NoError(t, model.SaveOpsFeePage(context.Background(), primary, "separate", "owner", cursor, rows, 201))
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "separate"})
	var page []*model.Log
	require.NoError(t, logs.Find(&page).Error)
	require.NoError(t, model.AttachOpsFees(context.Background(), page))
	require.Len(t, page, 1)
	assert.Equal(t, "0.10", *page[0].OpsFee.Amount)
	assert.Equal(t, 123, page[0].Quota)
	assert.False(t, primary.Migrator().HasTable(&model.Log{}))
	assert.False(t, logs.Migrator().HasTable(&model.OpsFee{}))
}
