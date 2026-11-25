package opsreserve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDurableReservationRecoveryAcrossDatabases(t *testing.T) {
	oldType, oldRedis, oldBatch := common.MainDatabaseType(), common.RedisEnabled, common.BatchUpdateEnabled
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	t.Cleanup(func() {
		common.SetMainDatabaseType(oldType)
		common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch
	})
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var driver gorm.Dialector
			switch kind {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "hold.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_txlock=immediate")
			case "mysql":
				dsn := os.Getenv("OPS_HOLD_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("isolated MySQL DSN required")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("OPS_HOLD_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("isolated PostgreSQL DSN required")
				}
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			common.SetMainDatabaseType(common.DatabaseType(kind))
			// External DSNs must refer to this task's disposable database.
			require.NoError(t, db.Migrator().DropTable(&Intent{}, &model.Token{}, &model.Task{}))
			require.NoError(t, db.AutoMigrate(&model.Token{}))
			key := model.Token{UserId: 1, Key: "isolated-" + kind, Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1}
			require.NoError(t, db.Create(&key).Error)
			// Upgrade a released token schema, then repeat preparation for idempotency.
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.Token{}, &Intent{}))
			}
			assert.True(t, db.Migrator().HasIndex(&Intent{}, "idx_ops_hold_release"))
			var version string
			query := "SELECT version()"
			if kind == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("%s", version)
			var mu sync.Mutex
			states := map[string]string{}
			fault := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Request   string `json:"requestId"`
					Operation string `json:"operation"`
				}
				if common.DecodeJson(r.Body, &in) != nil {
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				switch in.Operation {
				case "reserve":
					if states[in.Request] == "released" {
						w.WriteHeader(409)
						return
					}
					states[in.Request] = "reserved"
				case "release":
					if fault {
						w.WriteHeader(502)
						return
					}
					states[in.Request] = "released"
				case "status":
				default:
					w.WriteHeader(400)
					return
				}
				raw, _ := common.Marshal(map[string]any{"data": map[string]any{"requestId": in.Request, "state": states[in.Request]}})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			c := &Client{DB: db, Transport: opsclient.New(server.URL, "gateway", strings.Repeat("s", 32), time.Second)}
			ctx := context.Background()
			check := func(remain, used int) {
				t.Helper()
				var token model.Token
				require.NoError(t, db.First(&token, key.Id).Error)
				assert.Equal(t, remain, token.RemainQuota)
				assert.Equal(t, used, token.UsedQuota)
			}
			require.NoError(t, c.Reserve(ctx, "success", 1, "model", 100))
			require.NoError(t, c.TokenBudget(ctx, "success", 1, key.Id, 100, false))
			require.NoError(t, c.TokenBudget(ctx, "success", 1, key.Id, 100, false))
			check(900, 100)
			require.Error(t, c.TokenBudget(ctx, "success", 2, key.Id, 100, false))
			require.NoError(t, c.MarkDispatch(ctx, "success"))
			require.NoError(t, c.TokenBudget(ctx, "success", 1, key.Id, 60, true))
			require.NoError(t, c.TokenBudget(ctx, "success", 1, key.Id, 60, true))
			check(940, 60)
			require.Error(t, c.Release(ctx, "success"))
			require.Error(t, c.TokenBudget(ctx, "success", 1, key.Id, 61, true))
			check(940, 60)
			// A failed remote release leaves a durable marker. Replaying after a
			// simulated restart does not refund the independent key a second time.
			require.NoError(t, c.Reserve(ctx, "failed", 1, "model", 100))
			require.NoError(t, c.TokenBudget(ctx, "failed", 1, key.Id, 100, false))
			mu.Lock()
			fault = true
			mu.Unlock()
			require.Error(t, c.Release(ctx, "failed"))
			check(840, 160)
			mu.Lock()
			fault = false
			mu.Unlock()
			restarted := &Client{DB: db, Transport: c.Transport}
			require.NoError(t, db.Model(&Intent{}).Where("request = ?", "failed").Update("updated", int64(1)).Error)
			require.NoError(t, restarted.Recover(ctx))
			check(940, 60)
			var count int64
			require.NoError(t, db.Model(&Intent{}).Where("request = ?", "failed").Count(&count).Error)
			assert.Zero(t, count)
			// A pre-dispatch abandoned lease can be canceled; a dispatched request
			// of the same age remains reserved, including across a process restart.
			for _, id := range []string{"abandoned", "inflight"} {
				require.NoError(t, c.Reserve(ctx, id, 1, "model", 100))
				require.NoError(t, c.TokenBudget(ctx, id, 1, key.Id, 100, false))
			}
			require.NoError(t, c.MarkDispatch(ctx, "inflight"))
			require.NoError(t, db.Model(&Intent{}).Where("request IN ?", []string{"abandoned", "inflight"}).Updates(map[string]any{"lease_until": int64(1), "updated": int64(1)}).Error)
			require.NoError(t, restarted.Recover(ctx))
			check(840, 160)
			mu.Lock()
			assert.Equal(t, "released", states["abandoned"])
			assert.Equal(t, "reserved", states["inflight"])
			mu.Unlock()
			require.Error(t, c.MarkDispatch(ctx, "abandoned"))
			// Settle and posted receipt reconciliation remove only finalized intents.
			require.NoError(t, c.TokenBudget(ctx, "inflight", 1, key.Id, 80, true))
			mu.Lock()
			states["inflight"] = "settled"
			states["success"] = "settled"
			mu.Unlock()
			require.NoError(t, db.Model(&Intent{}).Where("gateway = ?", "gateway").Update("updated", int64(1)).Error)
			require.NoError(t, restarted.Recover(ctx))
			check(860, 140)
			require.NoError(t, db.Model(&Intent{}).Count(&count).Error)
			assert.Zero(t, count)
			// Distinct request IDs must still contend on the same API-key row.
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", key.Id).Updates(map[string]any{"remain_quota": 100, "used_quota": 0}).Error)
			for _, id := range []string{"race-a", "race-b"} {
				require.NoError(t, c.Reserve(ctx, id, 1, "model", 60))
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, id := range []string{"race-a", "race-b"} {
				wg.Go(func() { <-start; results <- c.TokenBudget(ctx, id, 1, key.Id, 60, false) })
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else {
					assert.ErrorIs(t, err, model.ErrInsufficientAgencyTokenQuota)
				}
			}
			assert.Equal(t, 1, successes)
			check(40, 60)
			for _, id := range []string{"race-a", "race-b"} {
				require.NoError(t, c.Release(ctx, id))
			}
			check(100, 0)
			// Async submission retains the key hold. The terminal task transaction
			// changes its budget exactly once and recovery removes only a posted hold.
			require.NoError(t, db.AutoMigrate(&model.Task{}))
			require.NoError(t, c.Reserve(ctx, "async", 1, "model", 60))
			require.NoError(t, c.TokenBudget(ctx, "async", 1, key.Id, 60, false))
			require.NoError(t, c.AttachTask(ctx, "async", "task-async"))
			require.NoError(t, c.MarkDispatch(ctx, "async"))
			task := model.Task{TaskID: "task-async", UserId: 1, Quota: 60, Status: model.TaskStatusSuccess, PrivateData: model.TaskPrivateData{BillingSource: "ops", OPSReservationGateway: "gateway", TokenId: key.Id, Execution: &model.TaskExecutionSnapshot{RequestID: "async"}}}
			require.NoError(t, db.Create(&task).Error)
			for range 2 {
				_, err := model.FinalizeOPSHoldTask(ctx, db, &task, 40)
				require.NoError(t, err)
			}
			check(60, 40)
			_, err = model.FinalizeOPSHoldTask(ctx, db, &task, 41)
			require.Error(t, err)
			otherTask := task
			otherTask.PrivateData.OPSReservationGateway = "another-gateway"
			_, err = model.FinalizeOPSHoldTask(ctx, db, &otherTask, 40)
			require.Error(t, err)
			// Failure is terminal zero, even if the provider left an estimate.
			// Unknown work requires an operator's provider evidence before release.
			require.NoError(t, c.Reserve(ctx, "review", 1, "model", 50))
			require.NoError(t, c.TokenBudget(ctx, "review", 1, key.Id, 50, false))
			require.NoError(t, c.MarkDispatch(ctx, "review"))
			require.Error(t, c.ResolveReviewed(ctx, "review", "confirmed_no_charge", "", "operator", 0))
			for range 2 {
				require.NoError(t, c.ResolveReviewed(ctx, "review", "confirmed_no_charge", "provider-terminal-receipt", "operator", 0))
			}
			check(60, 40)
			var reviewed Intent
			require.NoError(t, db.Where("request = ?", "review").First(&reviewed).Error)
			assert.Equal(t, "provider-terminal-receipt", reviewed.ReviewEvidence)
			assert.True(t, reviewed.Reconciled)
			require.Error(t, c.Reserve(ctx, "review", 1, "model", 50))
			// A posted remote charge may be reconciled only with explicit evidence.
			require.NoError(t, c.Reserve(ctx, "posted-review", 1, "model", 50))
			require.NoError(t, c.TokenBudget(ctx, "posted-review", 1, key.Id, 50, false))
			require.Error(t, c.ResolveReviewed(ctx, "posted-review", "posted_charge", "ledger-receipt", "operator", 30))
			mu.Lock()
			states["posted-review"] = "settled"
			mu.Unlock()
			for range 2 {
				require.NoError(t, c.ResolveReviewed(ctx, "posted-review", "posted_charge", "ledger-receipt", "operator", 30))
			}
			require.Error(t, c.ResolveReviewed(ctx, "posted-review", "posted_charge", "different-evidence", "operator", 30))
			check(30, 70)
			// Restore the fixture budget without changing already reconciled records.
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", key.Id).Updates(map[string]any{"remain_quota": 60, "used_quota": 40}).Error)
			require.NoError(t, c.Reserve(ctx, "async-fail", 1, "model", 60))
			require.NoError(t, c.TokenBudget(ctx, "async-fail", 1, key.Id, 60, false))
			require.NoError(t, c.AttachTask(ctx, "async-fail", "task-fail"))
			failedTask := model.Task{TaskID: "task-fail", UserId: 1, Quota: 60, Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingSource: "ops", OPSReservationGateway: "gateway", TokenId: key.Id, Execution: &model.TaskExecutionSnapshot{RequestID: "async-fail"}}}
			require.NoError(t, db.Create(&failedTask).Error)
			for range 2 {
				_, err := model.FinalizeOPSHoldTask(ctx, db, &failedTask, 60)
				require.NoError(t, err)
			}
			check(60, 40)
		})
	}
}
