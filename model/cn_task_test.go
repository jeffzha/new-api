package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNTaskAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_TASK_ENABLED", "true")
	oldBatch, oldRedis, oldUnit := common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit
	oldRate, oldDisplay := operation_setting.USDExchangeRate, operation_setting.GetGeneralSetting().QuotaDisplayType
	common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = false, false, 10
	operation_setting.USDExchangeRate, operation_setting.GetGeneralSetting().QuotaDisplayType = 7, "CNY"
	t.Cleanup(func() {
		common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = oldBatch, oldRedis, oldUnit
		operation_setting.USDExchangeRate, operation_setting.GetGeneralSetting().QuotaDisplayType = oldRate, oldDisplay
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			if dialect == "sqlite" {
				pool, err := db.DB()
				require.NoError(t, err)
				require.NoError(t, pool.Close())
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "task.db")), &gorm.Config{})
				require.NoError(t, err)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			var databaseVersion string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&databaseVersion).Error)
			t.Logf("database version: %s", databaseVersion)
			if dialect == "sqlite" {
				pool.SetMaxOpenConns(1)
			}
			t.Cleanup(func() { _ = pool.Close() })
			previousDB, previousType := DB, common.MainDatabaseType()
			DB = db
			switch dialect {
			case "mysql":
				common.SetMainDatabaseType(common.DatabaseTypeMySQL)
			case "postgres":
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			default:
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			}
			t.Cleanup(func() { DB = previousDB; common.SetMainDatabaseType(previousType) })
			cnReset(t, db)
			require.NoError(t, db.Migrator().DropTable(&Log{}))
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &Task{}, &TaskBillingReconciliation{}, &SubscriptionPlan{}, &UserSubscription{}, &SubscriptionPreConsumeRecord{}, &Log{}))
			legacy := Task{TaskID: "pre-task-receipt", UserId: 999, Quota: 20, Status: TaskStatusSuccess}
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, db.Migrator().DropColumn(&Task{}, "CNBillingKey"))
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			var preserved Task
			require.NoError(t, db.First(&preserved, legacy.ID).Error)
			assert.Equal(t, legacy, preserved)
			var count int64
			require.NoError(t, db.Model(&CNTaskBilling{}).Count(&count).Error)
			assert.Zero(t, count)
			t.Run("upgrade_existing_receipts_exclude_other_lanes", func(t *testing.T) {
				for i, oldLane := range []string{"wallet", "subscription"} {
					t.Run(oldLane, func(t *testing.T) {
						userID := 2000 + i
						request := "upgrade-" + oldLane
						require.NoError(t, db.Create(&User{Id: userID, Username: request, AffCode: request, Quota: 10000}).Error)
						require.NoError(t, db.Create(&Token{Id: userID, UserId: userID, Key: request, RemainQuota: 10000}).Error)
						require.NoError(t, db.Create(&SubscriptionPlan{Id: userID, Title: request, TotalAmount: 10000, QuotaResetPeriod: SubscriptionResetNever}).Error)
						require.NoError(t, db.Create(&UserSubscription{Id: userID, UserId: userID, PlanId: userID, AmountTotal: 10000, Status: "active", StartTime: time.Now().Unix() - 1, EndTime: time.Now().Add(time.Hour).Unix()}).Error)
						require.NoError(t, db.Migrator().DropTable(&CNTaskRequestOwner{}))
						if oldLane == "wallet" {
							ok, err := ReserveCNWallet(request, userID, 100, true)
							require.NoError(t, err)
							require.True(t, ok)
						} else {
							_, err := PreConsumeCNSubscription(request, userID, 100, userID, false, false)
							require.NoError(t, err)
						}
						var historical any = &CNWalletLifecycle{}
						if oldLane == "subscription" {
							historical = &CNSubscriptionLifecycle{}
						}
						require.NoError(t, db.Where("request_id = ?", request).First(historical).Error)
						originalReceipt, err := common.Marshal(historical)
						require.NoError(t, err)
						for range 2 {
							require.NoError(t, MigrateCNFinance(db, "test-cn"))
						}
						if oldLane == "wallet" {
							_, err := PreConsumeCNSubscription(request, userID, 100, userID, false, false)
							require.ErrorIs(t, err, ErrCNConflict)
						} else {
							_, err := ReserveCNWallet(request, userID, 100, true)
							require.ErrorIs(t, err, ErrCNConflict)
						}
						_, err = PrepareCNTask(&Task{TaskID: request, UserId: userID, ChannelId: userID, Platform: constant.TaskPlatform("alibaba")}, request, TaskBillingSourceWallet, 100, userID, false, false)
						require.ErrorIs(t, err, ErrCNConflict)
						require.NoError(t, db.Where("request_id = ?", request).First(historical).Error)
						preservedReceipt, err := common.Marshal(historical)
						require.NoError(t, err)
						require.Equal(t, originalReceipt, preservedReceipt)
						var owners, receipts int64
						require.NoError(t, db.Model(&CNTaskRequestOwner{}).Where("request_id = ?", request).Count(&owners).Error)
						require.Zero(t, owners, "incompatible historical receipt must roll back the new claim")
						for _, receipt := range []any{&CNTaskBilling{}, &CNWalletLifecycle{}, &CNSubscriptionLifecycle{}} {
							var n int64
							require.NoError(t, db.Model(receipt).Where("request_id = ?", request).Count(&n).Error)
							receipts += n
						}
						require.EqualValues(t, 1, receipts)
						var user User
						var sub UserSubscription
						require.NoError(t, db.First(&user, userID).Error)
						require.NoError(t, db.First(&sub, userID).Error)
						require.EqualValues(t, 100, int64(10000-user.Quota)+sub.AmountUsed)
						if oldLane == "wallet" {
							_, err := ReserveCNWallet(request, userID, 100, true)
							require.NoError(t, err, "original lane remains resumable")
						} else {
							_, err := PreConsumeCNSubscription(request, userID, 100, userID, false, false)
							require.NoError(t, err, "original lane remains resumable")
						}
						var owner CNTaskRequestOwner
						require.NoError(t, db.Where("request_id = ?", request).First(&owner).Error)
						require.Equal(t, oldLane, owner.Lane)
						require.Equal(t, userID, owner.UserID)
					})
				}
			})
			t.Run("concurrent_first_prepare_cross_lane", func(t *testing.T) {
				for scenario := range 8 {
					request := fmt.Sprintf("race-request-%d", scenario)
					users := []int{800 + scenario*3, 801 + scenario*3, 802 + scenario*3}
					for _, user := range users {
						name := fmt.Sprintf("race-user-%d", user)
						require.NoError(t, db.Create(&User{Id: user, Username: name, AffCode: name, Quota: 1000}).Error)
						require.NoError(t, db.Create(&Token{Id: user, UserId: user, Key: name, RemainQuota: 1000}).Error)
						require.NoError(t, db.Create(&SubscriptionPlan{Id: user, Title: name, TotalAmount: 1000, QuotaResetPeriod: SubscriptionResetNever}).Error)
						require.NoError(t, db.Create(&UserSubscription{Id: user, UserId: user, PlanId: user, AmountTotal: 1000, Status: "active", StartTime: time.Now().Unix() - 1, EndTime: time.Now().Add(time.Hour).Unix()}).Error)
					}
					contenders := append([]int(nil), users...)
					if scenario%2 == 0 {
						contenders = []int{users[0], users[0], users[0]}
					}
					start := make(chan struct{})
					results := make(chan error, 3)
					for lane, user := range contenders {
						go func() {
							<-start
							var err error
							switch lane {
							case 0:
								seed := &Task{TaskID: request, UserId: user, ChannelId: user, Platform: constant.TaskPlatform("alibaba")}
								_, err = PrepareCNTask(seed, request, TaskBillingSourceWallet, 100, user, false, false)
							case 1:
								_, err = ReserveCNWallet(request, user, 100, true)
							case 2:
								_, err = PreConsumeCNSubscription(request, user, 100, user, false, false)
							}
							results <- err
						}()
					}
					close(start)
					winners := 0
					for range 3 {
						if err := <-results; err == nil {
							winners++
						} else {
							require.ErrorIs(t, err, ErrCNConflict)
						}
					}
					require.Equal(t, 1, winners)
					var receipts int64
					for _, receipt := range []any{&CNTaskBilling{}, &CNWalletLifecycle{}, &CNSubscriptionLifecycle{}} {
						var n int64
						require.NoError(t, db.Model(receipt).Where("request_id = ?", request).Count(&n).Error)
						receipts += n
					}
					require.EqualValues(t, 1, receipts)
					var walletBalance, subscriptionUsed int64
					require.NoError(t, db.Model(&User{}).Where("id IN ?", users).Select("sum(quota)").Scan(&walletBalance).Error)
					require.NoError(t, db.Model(&UserSubscription{}).Where("user_id IN ?", users).Select("sum(amount_used)").Scan(&subscriptionUsed).Error)
					require.EqualValues(t, 100, 3000-walletBalance+subscriptionUsed)
				}
			})
			t.Run("token_owner_mismatch_rolls_back_prepare", func(t *testing.T) {
				var before, after User
				require.NoError(t, db.First(&before, 800).Error)
				_, err := PrepareCNTask(&Task{TaskID: "token-owner-mismatch", UserId: 800, ChannelId: 800, Platform: constant.TaskPlatform("alibaba")}, "token-owner-mismatch", TaskBillingSourceWallet, 100, 801, false, false)
				require.ErrorIs(t, err, ErrCNConflict)
				require.NoError(t, db.First(&after, 800).Error)
				require.Equal(t, before.Quota, after.Quota)
				for _, receipt := range []any{&CNTaskBilling{}, &CNTaskRequestOwner{}} {
					var n int64
					require.NoError(t, db.Model(receipt).Where("request_id = ?", "token-owner-mismatch").Count(&n).Error)
					require.Zero(t, n)
				}
			})
			nextID := 100
			prepare := func(t *testing.T, name, source string, quota int) (*Task, *CNTaskBilling) {
				t.Helper()
				nextID++
				id := nextID
				balance := 10000
				if name == "fixed-0" {
					balance = 0
				}
				require.NoError(t, db.Create(&User{Id: id, Username: name, AffCode: name, Quota: balance}).Error)
				require.NoError(t, db.Create(&Token{Id: id, UserId: id, Key: "task-" + name, RemainQuota: 10000}).Error)
				require.NoError(t, db.Create(&Channel{Id: id, Name: name}).Error)
				if source == TaskBillingSourceSubscription {
					require.NoError(t, db.Create(&SubscriptionPlan{Id: id, Title: name, TotalAmount: 10000, QuotaResetPeriod: SubscriptionResetNever}).Error)
					require.NoError(t, db.Create(&UserSubscription{Id: id, UserId: id, PlanId: id, AmountTotal: 10000, Status: "active", StartTime: time.Now().Unix() - 1, EndTime: time.Now().Add(time.Hour).Unix()}).Error)
				}
				task := &Task{TaskID: "public-" + name, Platform: constant.TaskPlatform("alibaba"), UserId: id, ChannelId: id, Status: TaskStatusSubmitted, CreatedAt: time.Now().Unix(), PrivateData: TaskPrivateData{BillingContext: &TaskBillingContext{GroupRatio: 1, ModelRatio: 1, OriginModelName: "frozen-model"}}}
				row, err := PrepareCNTask(task, "request-"+name, source, quota, id, false, false)
				require.NoError(t, err)
				task.CNBillingKey, task.Quota = row.Key, quota
				task.PrivateData.TokenId, task.PrivateData.BillingSource, task.PrivateData.SubscriptionId = id, source, row.SubscriptionID
				task.PrivateData.Execution = &TaskExecutionSnapshot{RequestID: row.RequestID}
				return task, row
			}
			accept := func(t *testing.T, task *Task) {
				t.Helper()
				require.NoError(t, BeginCNTaskDispatch(task.CNBillingKey, strings.Repeat("a", 64), task.ChannelId, string(task.Platform)))
				task.PrivateData.UpstreamTaskID = "upstream-" + task.TaskID
				require.NoError(t, RecordCNTaskUpstream(task.CNBillingKey, task.PrivateData.UpstreamTaskID))
			}
			check := func(t *testing.T, task *Task, held, actual, requests int) {
				t.Helper()
				var user User
				var token Token
				var channel Channel
				require.NoError(t, db.First(&user, task.UserId).Error)
				require.NoError(t, db.Unscoped().First(&token, task.PrivateData.TokenId).Error)
				require.NoError(t, db.First(&channel, task.ChannelId).Error)
				if task.PrivateData.BillingSource == TaskBillingSourceWallet {
					balance := 10000
					if task.TaskID == "public-fixed-0" {
						balance = 0
					}
					assert.Equal(t, balance-held, user.Quota)
				} else {
					assert.Equal(t, 10000, user.Quota)
				}
				assert.Equal(t, actual, user.UsedQuota)
				assert.Equal(t, requests, user.RequestCount)
				assert.EqualValues(t, actual, channel.UsedQuota)
				assert.Equal(t, 10000-held, token.RemainQuota)
				assert.Equal(t, held, token.UsedQuota)
			}
			facts := func(t *testing.T, row *CNTaskBilling, quotas ...int) {
				t.Helper()
				var rows []CNFinanceOutbox
				require.NoError(t, db.Where("chain_key = ?", CNIdentity("charge", "request:"+row.RequestID, "0")).Order("revision").Find(&rows).Error)
				require.Len(t, rows, len(quotas))
				for i, quota := range quotas {
					var f CNFact
					require.NoError(t, common.UnmarshalJsonStr(rows[i].Payload, &f))
					assert.Equal(t, fmt.Sprint(quota), *f.Cumulative.Quota)
					assert.Equal(t, row.RequestID, *f.RequestID)
					assert.Equal(t, row.PublicTaskID, *f.TaskID)
					assert.Equal(t, row.QuotaPerUnit, f.Conversion["quotaPerUnit"])
					if i > 0 {
						assert.Equal(t, rows[i-1].FactID, *f.PreviousFactID)
					}
				}
			}
			t.Run("initial_correction_final_and_guards", func(t *testing.T) {
				task, row := prepare(t, "correct", TaskBillingSourceWallet, 100)
				_, err := ReserveCNWallet(row.RequestID, task.UserId, 100, true)
				require.ErrorIs(t, err, ErrCNConflict)
				_, err = PreConsumeCNSubscription(row.RequestID, task.UserId, 100, task.PrivateData.TokenId, false, false)
				require.ErrorIs(t, err, ErrCNConflict)
				check(t, task, 100, 0, 0)
				accept(t, task)
				require.ErrorIs(t, RecordCNTaskUpstream(row.Key, "other"), ErrCNConflict)
				require.NoError(t, task.Insert())
				require.NoError(t, task.Insert())
				check(t, task, 100, 100, 1)
				require.NoError(t, RequestCNTaskAdjustment(task, 140, false))
				require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
				require.NoError(t, db.First(task, task.ID).Error)
				task.Status = TaskStatusSuccess
				target := 60
				task.PrepareCNFinancialTerminal(&target, "")
				won, err := task.UpdateWithStatus(TaskStatusSubmitted)
				require.NoError(t, err)
				require.True(t, won)
				require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
				require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
				check(t, task, 60, 60, 1)
				facts(t, row, 100, 140, 60)
				require.NoError(t, db.First(task, task.ID).Error)
				stale := *task
				stale.CNBillingKey = ""
				require.ErrorIs(t, stale.Update(), ErrCNConflict)
				_, err = ApplyTaskQuotaAdjustment(&stale, 1)
				require.ErrorIs(t, err, ErrCNConflict)
				require.ErrorIs(t, CaptureCNTaskChargeTx(db, task, task.Quota), ErrCNConflict)
				require.ErrorIs(t, RequestCNTaskAdjustment(task, 61, true), ErrCNConflict)
			})
			t.Run("fixed_and_zero_terminal", func(t *testing.T) {
				for _, quota := range []int{0, 40} {
					task, row := prepare(t, fmt.Sprintf("fixed-%d", quota), TaskBillingSourceWallet, quota)
					accept(t, task)
					require.NoError(t, task.Insert())
					task.Status = TaskStatusSuccess
					task.PrepareCNFinancialTerminal(&quota, "")
					_, err := task.UpdateWithStatus(TaskStatusSubmitted)
					require.NoError(t, err)
					require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
					check(t, task, quota, quota, 1)
					facts(t, row, quota)
					var final CNTaskBilling
					require.NoError(t, db.First(&final, map[string]any{"key": row.Key}).Error)
					assert.Equal(t, "final", final.State)
					assert.False(t, final.Pending)
				}
			})
			t.Run("unknown_keeps_reserve_and_never_redispatches", func(t *testing.T) {
				task, row := prepare(t, "unknown", TaskBillingSourceWallet, 80)
				require.NoError(t, BeginCNTaskDispatch(row.Key, strings.Repeat("b", 64), task.ChannelId, string(task.Platform)))
				require.NoError(t, ResolveCNTaskSubmission(row.Key, false))
				require.ErrorIs(t, RequestCNTaskCancellation(row.Key), ErrCNConflict)
				require.ErrorIs(t, BeginCNTaskDispatch(row.Key, strings.Repeat("b", 64), task.ChannelId, string(task.Platform)), ErrCNConflict)
				require.NoError(t, RecoverCNTaskBilling(context.Background(), db, 100))
				check(t, task, 80, 0, 0)
				facts(t, row)
			})
			t.Run("acceptance_and_refund_atomic_recovery", func(t *testing.T) {
				task, row := prepare(t, "rollback", TaskBillingSourceWallet, 100)
				accept(t, task)
				for _, table := range []string{"tasks", "users", "channels", "cn_finance_outboxes", "cn_task_billings", "cn_task_effects"} {
					inject := func(tx *gorm.DB) {
						if tx.Statement.Table == table {
							if values, ok := tx.Statement.Dest.(map[string]any); ok && table == "cn_task_billings" && values["accepted_task"] != nil {
								return
							}
							if table == "users" {
								if values, ok := tx.Statement.Dest.(map[string]any); ok && values["used_quota"] == nil {
									return
								}
							}
							tx.AddError(errors.New("injected task financial failure"))
						}
					}
					require.NoError(t, db.Callback().Create().Before("gorm:create").Register("cn-task-fail", inject))
					require.NoError(t, db.Callback().Update().Before("gorm:update").Register("cn-task-fail", inject))
					require.Error(t, task.Insert())
					require.NoError(t, db.Callback().Create().Remove("cn-task-fail"))
					require.NoError(t, db.Callback().Update().Remove("cn-task-fail"))
					check(t, task, 100, 0, 0)
					assert.Zero(t, task.ID)
					facts(t, row)
				}
				t.Setenv("OPS_CN_DURABLE_TASK_ENABLED", "false")
				t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "false")
				require.NoError(t, pool.Close())
				db, err = gorm.Open(db.Dialector, &gorm.Config{Logger: db.Logger})
				require.NoError(t, err)
				pool, err = db.DB()
				require.NoError(t, err)
				if dialect == "sqlite" {
					pool.SetMaxOpenConns(1)
				}
				DB = db
				for range 2 {
					require.NoError(t, MigrateCNFinance(db, "test-cn"))
				}
				require.NoError(t, db.Model(&CNTaskBilling{}).Where(map[string]any{"key": row.Key}).Update("next_attempt_at", 0).Error)
				require.NoError(t, RecoverCNTaskBilling(context.Background(), db, 100))
				require.NoError(t, db.Where("cn_billing_key = ?", row.Key).First(task).Error)
				check(t, task, 100, 100, 1)
				task.Status = TaskStatusFailure
				_, err := task.UpdateWithStatus(TaskStatusSubmitted)
				require.NoError(t, err)
				for _, table := range []string{"tokens", "users", "channels", "tasks", "cn_finance_outboxes", "cn_task_billings", "cn_task_effects"} {
					inject := func(tx *gorm.DB) {
						if tx.Statement.Table == table {
							tx.AddError(errors.New("injected refund failure"))
						}
					}
					require.NoError(t, db.Callback().Create().Before("gorm:create").Register("cn-refund-fail", inject))
					require.NoError(t, db.Callback().Update().Before("gorm:update").Register("cn-refund-fail", inject))
					require.Error(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
					require.NoError(t, db.Callback().Create().Remove("cn-refund-fail"))
					require.NoError(t, db.Callback().Update().Remove("cn-refund-fail"))
					check(t, task, 100, 100, 1)
					facts(t, row, 100)
				}
				require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
				check(t, task, 0, 0, 1)
				facts(t, row, 100, 0)
			})
			t.Run("subscription_refund_uses_frozen_generation", func(t *testing.T) {
				task, row := prepare(t, "subscription", TaskBillingSourceSubscription, 100)
				accept(t, task)
				require.NoError(t, task.Insert())
				_, err := AdminResetUserSubscriptionsByPlan(task.UserId, task.PrivateData.SubscriptionId, false)
				require.NoError(t, err)
				task.Status = TaskStatusFailure
				_, err = task.UpdateWithStatus(TaskStatusSubmitted)
				require.NoError(t, err)
				require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
				var sub UserSubscription
				var period CNSubscriptionPeriod
				require.NoError(t, db.First(&sub, row.SubscriptionID).Error)
				require.NoError(t, db.Where("subscription_id = ? AND generation = ?", row.SubscriptionID, row.UsageGeneration).First(&period).Error)
				assert.Greater(t, sub.UsageGeneration, row.UsageGeneration)
				assert.Zero(t, sub.AmountUsed)
				assert.Zero(t, period.AmountUsed)
				check(t, task, 0, 0, 1)
			})
			t.Run("concurrent_terminal_is_once", func(t *testing.T) {
				task, row := prepare(t, "concurrent", TaskBillingSourceWallet, 70)
				accept(t, task)
				require.NoError(t, task.Insert())
				var wg sync.WaitGroup
				errs := make(chan error, 8)
				for range 8 {
					wg.Go(func() {
						copyTask := *task
						copyTask.Status = TaskStatusSuccess
						target := 50
						copyTask.PrepareCNFinancialTerminal(&target, "")
						_, err := copyTask.UpdateWithStatus(TaskStatusSubmitted)
						if err == nil {
							err = CompleteCNTaskIntent(context.Background(), db, row.Key)
						}
						errs <- err
					})
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				check(t, task, 50, 50, 1)
				facts(t, row, 70, 50)
			})
			t.Run("deleted_token_refund_records_exact_disposition", func(t *testing.T) {
				task, row := prepare(t, "deleted-token", TaskBillingSourceWallet, 100)
				accept(t, task)
				require.NoError(t, task.Insert())
				require.NoError(t, db.Delete(&Token{}, task.PrivateData.TokenId).Error)
				task.Status = TaskStatusFailure
				_, err := task.UpdateWithStatus(TaskStatusSubmitted)
				require.NoError(t, err)
				require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
				var user User
				var token Token
				require.NoError(t, db.First(&user, task.UserId).Error)
				require.NoError(t, db.Unscoped().First(&token, task.PrivateData.TokenId).Error)
				require.NoError(t, db.First(row, map[string]any{"key": row.Key}).Error)
				assert.Equal(t, 10000, user.Quota)
				assert.Zero(t, user.UsedQuota)
				assert.True(t, token.DeletedAt.Valid)
				assert.Equal(t, 9900, token.RemainQuota)
				assert.Equal(t, 100, row.TokenDisposedQuota)
				assert.Equal(t, "deleted_token_not_recredited", row.TokenRefundStatus)
				facts(t, row, 100, 0)
			})
			t.Run("provider_zero_bill_is_final_and_replayable", func(t *testing.T) {
				task, row := prepare(t, "provider-zero", TaskBillingSourceWallet, 90)
				task.PrivateData.BillingContext.ProviderBilling = &TaskProviderBillingSnapshot{Provider: TaskBillingProviderSeedanceDomestic, AsyncReconciliationRequired: true}
				accept(t, task)
				task.Status, task.BillingReconciliationPending, task.OpsUsagePending = TaskStatusSuccess, true, true
				require.NoError(t, task.Insert())
				require.True(t, task.BillingReconciliationPending, "hidden enqueue intent must survive accepted-task JSON reconstruction")
				require.True(t, task.OpsUsagePending)
				require.ErrorIs(t, RequestCNTaskAdjustment(task, 0, true), ErrCNConflict, "generic adjustment cannot finalize an asynchronous provider bill")
				enqueued, err := EnqueuePendingTaskBillingReconciliations(100)
				require.NoError(t, err)
				require.Positive(t, enqueued)
				var record TaskBillingReconciliation
				require.NoError(t, db.Where("task_id = ?", task.ID).First(&record).Error)
				require.NoError(t, db.Model(&record).Update("status", TaskBillingReconciliationProcessing).Error)
				for range 2 {
					_, err := SettleTaskBillingReconciliation(record.ID, TaskBillingReconciliationSettlement{ActualQuota: 0, TotalTokens: 0})
					require.NoError(t, err)
				}
				check(t, task, 0, 0, 1)
				facts(t, row, 90, 0)
			})
			t.Run("legacy_and_durable_tasks_share_user_lock", func(t *testing.T) {
				task, row := prepare(t, "mixed-lock-order", TaskBillingSourceWallet, 100)
				accept(t, task)
				require.NoError(t, task.Insert())
				var legacy []Task
				var reconIDs []int64
				for i := range 8 {
					old := Task{TaskID: fmt.Sprintf("mixed-legacy-%d", i), UserId: task.UserId, ChannelId: task.ChannelId, Platform: task.Platform, Quota: 50, Status: TaskStatusSuccess, PrivateData: TaskPrivateData{TokenId: task.PrivateData.TokenId, BillingSource: TaskBillingSourceWallet}}
					require.NoError(t, db.Create(&old).Error)
					legacy = append(legacy, old)
					if i%2 == 1 {
						record := TaskBillingReconciliation{TaskID: old.ID, ChannelID: old.ChannelId, Provider: TaskBillingProviderSeedanceDomestic, Status: TaskBillingReconciliationProcessing}
						require.NoError(t, db.Create(&record).Error)
						reconIDs = append(reconIDs, record.ID)
					}
				}
				require.NoError(t, db.Model(&User{}).Where("id = ?", task.UserId).Update("quota", gorm.Expr("quota - 400")).Error)
				require.NoError(t, db.Model(&Token{}).Where("id = ?", task.PrivateData.TokenId).Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota - 400"), "used_quota": gorm.Expr("used_quota + 400")}).Error)
				start := make(chan struct{})
				errs := make(chan error, 9)
				var wg sync.WaitGroup
				wg.Go(func() {
					<-start
					target := 60
					task.Status = TaskStatusSuccess
					task.PrepareCNFinancialTerminal(&target, "")
					_, err := task.UpdateWithStatus(TaskStatusSubmitted)
					if err == nil {
						err = CompleteCNTaskIntent(context.Background(), db, row.Key)
					}
					errs <- err
				})
				for i := range legacy {
					wg.Go(func() {
						<-start
						var err error
						if i%2 == 0 {
							_, err = ApplyTaskQuotaAdjustment(&legacy[i], 10)
						} else {
							_, err = SettleTaskBillingReconciliation(reconIDs[i/2], TaskBillingReconciliationSettlement{ActualQuota: 10, TotalTokens: 1})
						}
						errs <- err
					})
				}
				close(start)
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				var user User
				var token Token
				require.NoError(t, db.First(&user, task.UserId).Error)
				require.NoError(t, db.First(&token, task.PrivateData.TokenId).Error)
				assert.Equal(t, 10000-60-80, user.Quota)
				assert.Equal(t, 10000-60-80, token.RemainQuota)
				assert.Equal(t, 60+80, token.UsedQuota)
				for _, old := range legacy {
					var persisted Task
					require.NoError(t, db.First(&persisted, old.ID).Error)
					assert.Equal(t, 10, persisted.Quota)
				}
				facts(t, row, 100, 60)
			})
			// Deliver to the separately opened log DB, then replay after a lost
			// source acknowledgement. The log receipt must prevent a duplicate.
			logDB, err := gorm.Open(db.Dialector, &gorm.Config{})
			require.NoError(t, err)
			logPool, err := logDB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = logPool.Close() })
			require.NoError(t, logDB.AutoMigrate(&Log{}))
			require.NoError(t, logDB.Callback().Create().Before("gorm:create").Register("cn-log-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "logs" {
					tx.AddError(errors.New("injected log insert failure"))
				}
			}))
			require.Error(t, DeliverCNTaskEffects(context.Background(), db, logDB, 100))
			require.NoError(t, logDB.Callback().Create().Remove("cn-log-fail"))
			require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, logDB.Model(&CNMidjourneyLogReceipt{}).Count(&count).Error)
			assert.Zero(t, count, "log receipt must roll back with failed log")
			require.NoError(t, db.Model(&CNTaskEffect{}).Where("state = ?", "pending").Update("next_attempt_at", 0).Error)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("cn-log-ack-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_task_effects" {
					if values, ok := tx.Statement.Dest.(map[string]any); ok && values["state"] == "delivered" {
						tx.AddError(errors.New("injected lost log acknowledgement"))
					}
				}
			}))
			require.Error(t, DeliverCNTaskEffects(context.Background(), db, logDB, 100))
			require.NoError(t, db.Callback().Update().Remove("cn-log-ack-fail"))
			require.NoError(t, db.Model(&CNTaskEffect{}).Where("state = ?", "pending").Update("next_attempt_at", 0).Error)
			require.NoError(t, DeliverCNTaskEffects(context.Background(), db, logDB, 100))
			var delivered, before int64
			require.NoError(t, db.Model(&CNTaskEffect{}).Where("state = ?", "delivered").Count(&delivered).Error)
			require.Positive(t, delivered)
			require.NoError(t, logDB.Model(&Log{}).Count(&before).Error)
			assert.Equal(t, delivered, before)
			require.NoError(t, db.Model(&CNTaskEffect{}).Where("state = ?", "delivered").Update("state", "pending").Error)
			require.NoError(t, DeliverCNTaskEffects(context.Background(), db, logDB, 100))
			require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
			assert.Equal(t, before, count)
			_, err = ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			var stream CNFinanceStream
			require.NoError(t, db.First(&stream, 1).Error)
			proofBytes, err := cnBuildSourceProofs(db, &stream)
			require.NoError(t, err)
			var proof struct {
				Dependencies []CNDependency `json:"dependencies"`
			}
			require.NoError(t, common.Unmarshal(proofBytes, &proof))
			providers, legacyFound := 0, false
			for _, dependency := range proof.Dependencies {
				if strings.HasPrefix(dependency.SourceID, "ordinary_task_") {
					providers++
					assert.Equal(t, "task-single-settler-v1", dependency.Version)
					assert.Equal(t, []string{"ordinary-task/single-settler-v1"}, dependency.SupportedPaths)
				}
				if dependency.SourceID == "image_video_tasks" {
					legacyFound = true
					assert.Empty(t, dependency.SupportedPaths)
					assert.Contains(t, dependency.ExcludedPaths, "ordinary_task_single_settler_has_separate_proofs")
					assert.NotEqual(t, "complete", dependency.Status)
				}
			}
			require.Equal(t, len(CNTaskSupportedPlatforms()), providers)
			require.True(t, legacyFound)
			if output := os.Getenv("OPS_CN_TASK_SAMPLE_PATH"); output != "" {
				var publications []CNFinancePublication
				require.NoError(t, db.Where("fact_id IS NOT NULL").Order("sequence").Find(&publications).Error)
				var lines []string
				for _, pub := range publications {
					var f CNFact
					require.NoError(t, common.UnmarshalJsonStr(pub.Payload, &f))
					if f.RequestID != nil && (*f.RequestID == "request-correct" || *f.RequestID == "request-provider-zero") {
						lines = append(lines, pub.Payload)
					}
				}
				require.Len(t, lines, 5)
				require.NoError(t, os.WriteFile(output+"."+dialect+".jsonl", []byte(strings.Join(lines, "\n")+"\n"), 0600))
			}
		})
	}
}
