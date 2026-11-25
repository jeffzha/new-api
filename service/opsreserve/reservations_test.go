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
			require.NoError(t, db.Migrator().DropTable(&Intent{}, &model.Token{}, &model.Task{}, &model.Midjourney{}))
			require.NoError(t, db.AutoMigrate(&model.Token{}))
			key := model.Token{UserId: 1, Key: "isolated-" + kind, Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1}
			require.NoError(t, db.Create(&key).Error)
			// Rehearse a drained polling-owner handoff using the existing lock
			// table. Old binaries must remain fenced between successful passes.
			require.NoError(t, db.Migrator().DropTable(&model.SystemTask{}, &model.SystemTaskLock{}))
			require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
			previousRunnerDB := model.DB
			model.DB = db
			func() {
				defer func() { model.DB = previousRunnerDB }()
				t.Setenv("SYSTEM_TASK_RELEASE_OWNER", "prepared-successor")
				for _, taskType := range []string{model.SystemTaskTypeAsyncTaskPoll, model.SystemTaskTypeMidjourneyPoll} {
					require.NoError(t, db.Create(&model.SystemTaskLock{Type: taskType, LockedBy: "prepared-successor", LockedUntil: 253402300799}).Error)
					for range 2 {
						task, err := model.CreateSystemTask(taskType, nil, nil)
						require.NoError(t, err)
						_, claimed, err := model.ClaimSystemTask(task.ID, taskType, "old-runner", common.GetTimestamp()+60)
						require.NoError(t, err)
						require.False(t, claimed)
						_, claimed, err = model.ClaimSystemTask(task.ID, taskType, "prepared-successor", common.GetTimestamp()+60)
						require.NoError(t, err)
						require.True(t, claimed)
						require.NoError(t, model.RenewSystemTaskLock(task.TaskID, "prepared-successor", common.GetTimestamp()+60))
						require.NoError(t, model.FinishSystemTask(task.TaskID, "prepared-successor", model.SystemTaskStatusSucceeded, nil, ""))
					}
				}
			}()
			// Rehearse an upgrade from the released task schema and the first hold
			// schema, including rows written before nullable outbox fields existed.
			require.NoError(t, db.AutoMigrate(&model.Midjourney{}, &Intent{}))
			for _, field := range []string{"OPSReservationGateway", "OPSReservationRequest", "OPSReservationBaseURL"} {
				require.NoError(t, db.Migrator().DropColumn(&model.Midjourney{}, field))
			}
			for _, field := range []string{"MidjourneyID", "UsagePayload", "UsageLog", "RefundPayload", "UsageDelivered", "RefundDelivered", "TokenRefunded"} {
				require.NoError(t, db.Migrator().DropColumn(&Intent{}, field))
			}
			require.NoError(t, db.Table("midjourneys").Create(map[string]any{"user_id": 9, "mj_id": "existing-history", "quota": 17}).Error)
			// Upgrade a released token schema, then repeat preparation for idempotency.
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.Token{}, &Intent{}, &model.Midjourney{}))
				require.NoError(t, model.PrepareOPSHoldSchema(db))
			}
			assert.True(t, db.Migrator().HasIndex(&Intent{}, "idx_ops_hold_release"))
			require.NoError(t, db.Migrator().DropIndex(&Intent{}, "idx_ops_hold_release"))
			require.NoError(t, db.Exec("CREATE INDEX idx_ops_hold_release ON ops_wallet_hold_intents (gateway, updated)").Error)
			require.ErrorContains(t, model.PrepareOPSHoldSchema(db), "definition conflict")
			require.NoError(t, db.Migrator().DropIndex(&Intent{}, "idx_ops_hold_release"))
			require.NoError(t, model.PrepareOPSHoldSchema(db))
			var history model.Midjourney
			require.NoError(t, db.Where("mj_id = ?", "existing-history").First(&history).Error)
			assert.Equal(t, 17, history.Quota)
			assert.Empty(t, history.OPSReservationGateway)
			var version string
			query := "SELECT version()"
			if kind == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("%s", version)
			var mu sync.Mutex
			states := map[string]string{}
			usageBodies := []string{}
			usageSeen := map[string]bool{}
			fault := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/ingest/v1/usage-events" {
					var body struct {
						Batch  string
						Events []map[string]any
					}
					var raw map[string]any
					if common.DecodeJson(r.Body, &raw) != nil {
						w.WriteHeader(400)
						return
					}
					body.Batch, _ = raw["batchId"].(string)
					encoded, _ := common.Marshal(raw)
					mu.Lock()
					defer mu.Unlock()
					usageBodies = append(usageBodies, string(encoded))
					accepted, duplicates := 1, 0
					if usageSeen[body.Batch] {
						accepted, duplicates = 0, 1
					}
					usageSeen[body.Batch] = true
					if fault {
						w.WriteHeader(502)
						return
					}
					receipt, _ := common.Marshal(map[string]any{"data": map[string]any{"accepted": accepted, "duplicates": duplicates, "rejected": []any{}}})
					_, _ = w.Write(receipt)
					return
				}
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
			// Legacy Midjourney settlement/refund is atomic with its durable
			// usage outbox, even when OPS commits but its acknowledgment is lost.
			require.NoError(t, c.Reserve(ctx, "mj-hold", 1, "mj_imagine", 50))
			require.NoError(t, c.TokenBudget(ctx, "mj-hold", 1, key.Id, 50, false))
			require.NoError(t, c.MarkDispatch(ctx, "mj-hold"))
			mj := model.Midjourney{UserId: 1, MjId: "mj-upstream", Quota: 40, TokenId: key.Id, OPSReservationGateway: "gateway", OPSReservationRequest: "mj-hold"}
			previousDB := model.DB
			model.DB = db
			t.Cleanup(func() { model.DB = previousDB })
			require.NoError(t, mj.Insert())
			var linked Intent
			require.NoError(t, db.Where("request = ?", "mj-hold").First(&linked).Error)
			assert.Equal(t, mj.Id, linked.MidjourneyID)
			usage := `{"batchId":"mj-hold-usage","events":[{"requestId":"mj-hold"}]}`
			for range 2 {
				require.NoError(t, model.FinalizeOPSHoldMidjourney(ctx, db, &mj, usage, false))
			}
			check(20, 80)
			mu.Lock()
			fault = true
			mu.Unlock()
			require.Error(t, c.DeliverUsage(ctx, "mj-hold"))
			mu.Lock()
			fault = false
			mu.Unlock()
			require.NoError(t, c.DeliverUsage(ctx, "mj-hold"))
			require.NoError(t, c.DeliverUsage(ctx, "mj-hold"))
			mu.Lock()
			assert.Len(t, usageBodies, 2)
			assert.Equal(t, usageBodies[0], usageBodies[1])
			mu.Unlock()
			refund := `{"batchId":"mj-hold-refund","events":[{"requestId":"mj-hold-r1"}]}`
			for range 2 {
				require.NoError(t, model.FinalizeOPSHoldMidjourney(ctx, db, &mj, refund, true))
			}
			check(60, 40)
			require.NoError(t, c.DeliverUsage(ctx, "mj-hold"))
			// A stale poll result must never overwrite a completed key refund.
			mj.Status, mj.Progress = "FAILURE", "100%"
			require.NoError(t, mj.Update())
			var afterRefund model.Midjourney
			require.NoError(t, db.First(&afterRefund, mj.Id).Error)
			assert.Zero(t, afterRefund.Quota)
			wrong := mj
			wrong.OPSReservationGateway = "other"
			require.Error(t, model.FinalizeOPSHoldMidjourney(ctx, db, &wrong, refund, true))
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
			// Final key accounting and recoverable pricing facts commit together,
			// without depending on the separately configured display-log database.
			require.NoError(t, c.Reserve(ctx, "journal", 1, "model", 30))
			require.NoError(t, c.TokenBudget(ctx, "journal", 1, key.Id, 30, false))
			require.NoError(t, c.MarkDispatch(ctx, "journal"))
			journalLog := &model.Log{UserId: 1, TokenId: key.Id, ModelName: "model", RequestId: "journal", Quota: 20}
			for range 2 {
				require.NoError(t, model.FinalizeOPSHoldUsage(ctx, db, "gateway", journalLog, "immutable-pricing-facts"))
			}
			check(40, 60)
			require.Error(t, model.FinalizeOPSHoldUsage(ctx, db, "gateway", journalLog, "changed-facts"))
			journalLog.TokenId++
			require.Error(t, model.FinalizeOPSHoldUsage(ctx, db, "gateway", journalLog, "immutable-pricing-facts"))
			journalLog.TokenId--
			journalLog.UserId = 2
			require.Error(t, model.FinalizeOPSHoldUsage(ctx, db, "gateway", journalLog, "immutable-pricing-facts"))
			check(40, 60)
		})
	}
}
