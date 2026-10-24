package model

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNMidjourneyAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_MIDJOURNEY_ENABLED", "true")
	oldBatch, oldRedis, oldUnit, oldRate := common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit, operation_setting.USDExchangeRate
	oldDisplay := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.BatchUpdateEnabled, common.RedisEnabled = false, false
	common.QuotaPerUnit, operation_setting.USDExchangeRate = 10, 7
	operation_setting.GetGeneralSetting().QuotaDisplayType = "CNY"
	t.Cleanup(func() {
		common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit, operation_setting.USDExchangeRate = oldBatch, oldRedis, oldUnit, oldRate
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplay
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
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "mj.db")), &gorm.Config{})
				require.NoError(t, err)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			if dialect == "sqlite" {
				pool.SetMaxOpenConns(1)
			}
			t.Cleanup(func() { _ = pool.Close() })
			previous := DB
			DB = db
			t.Cleanup(func() { DB = previous })
			cnReset(t, db)
			require.NoError(t, db.Migrator().DropTable(&Log{}))
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &Midjourney{}, &Log{}))
			legacyTask := Midjourney{UserId: 81, MjId: "legacy-no-conversion", ChannelId: 81, Quota: 30, Status: "SUCCESS"}
			require.NoError(t, db.Create(&legacyTask).Error)
			require.NoError(t, db.Migrator().DropColumn(&Midjourney{}, "CNBillingKey"))
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			var upgradedLegacy Midjourney
			require.NoError(t, db.First(&upgradedLegacy, legacyTask.Id).Error)
			assert.Equal(t, legacyTask, upgradedLegacy)
			var legacyReceipts int64
			require.NoError(t, db.Model(&CNMidjourneyBilling{}).Where("task_id = ?", legacyTask.Id).Count(&legacyReceipts).Error)
			assert.Zero(t, legacyReceipts)
			require.NoError(t, db.Create(&User{Id: 81, Username: "mj-user", AffCode: "mj-user", Quota: 10000}).Error)
			require.NoError(t, db.Create(&Token{Id: 81, UserId: 81, Key: "mj-fixture-key", RemainQuota: 10000}).Error)
			require.NoError(t, db.Create(&Channel{Id: 81, Name: "mj-channel"}).Error)
			initial := &Midjourney{UserId: 81, MjId: "insert-retry", ChannelId: 81, Quota: 50}
			require.NoError(t, PrepareCNMidjourney(initial, 81, false, false))
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("mj-insert-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_midjourney_billings" {
					tx.AddError(errors.New("injected prepare receipt failure"))
				}
			}))
			require.Error(t, initial.Insert())
			require.NoError(t, db.Callback().Create().Remove("mj-insert-fail"))
			assert.Zero(t, initial.Id)
			assert.Equal(t, 50, initial.Quota)
			require.NoError(t, initial.Insert())
			require.NoError(t, RequestCNMidjourneyRefund(context.Background(), db, initial.Id, 81, initial.MjId, "insert-retry-cancel"))
			require.NoError(t, CompleteCNMidjourneyRefund(context.Background(), db, initial.CNBillingKey))
			prepare := func(id string, quota int) *Midjourney {
				t.Helper()
				task := &Midjourney{UserId: 81, MjId: id, ChannelId: 81, BillingChannelId: 81, Quota: quota, Action: "IMAGINE", Status: "SUBMITTED"}
				require.NoError(t, PrepareCNMidjourney(task, 81, false, false))
				require.NoError(t, task.Insert())
				assert.Zero(t, task.Quota)
				return task
			}
			check := func(used, requests int) {
				t.Helper()
				var user User
				var token Token
				var channel Channel
				require.NoError(t, db.First(&user, 81).Error)
				require.NoError(t, db.First(&token, 81).Error)
				require.NoError(t, db.First(&channel, 81).Error)
				assert.Equal(t, 10000-used, user.Quota)
				assert.Equal(t, used, user.UsedQuota)
				assert.Equal(t, requests, user.RequestCount)
				assert.Equal(t, 10000-used, token.RemainQuota)
				assert.Equal(t, used, token.UsedQuota)
				assert.EqualValues(t, used, channel.UsedQuota)
			}
			task := prepare("charge-refund", 100)
			common.QuotaPerUnit, operation_setting.USDExchangeRate = 20, 9
			require.NoError(t, SettleCNMidjourney(context.Background(), db, task.Id, 81, task.MjId))
			require.NoError(t, SettleCNMidjourney(context.Background(), db, task.Id, 81, task.MjId))
			check(100, 1)
			require.ErrorIs(t, SettleCNMidjourney(context.Background(), db, task.Id, 82, task.MjId), gorm.ErrRecordNotFound)
			require.NoError(t, RequestCNMidjourneyRefund(context.Background(), db, task.Id, 81, task.MjId, "failed"))
			require.NoError(t, CompleteCNMidjourneyRefund(context.Background(), db, task.CNBillingKey))
			require.NoError(t, CompleteCNMidjourneyRefund(context.Background(), db, task.CNBillingKey))
			check(0, 1)
			common.QuotaPerUnit, operation_setting.USDExchangeRate = 10, 7
			var outboxes []CNFinanceOutbox
			require.NoError(t, db.Where("chain_key = ?", CNIdentity("charge", "midjourney:81:charge-refund", "0")).Order("id").Find(&outboxes).Error)
			require.Len(t, outboxes, 2)
			for i, row := range outboxes {
				var fact CNFact
				require.NoError(t, common.UnmarshalJsonStr(row.Payload, &fact))
				assert.Equal(t, task.MjId, *fact.TaskID)
				if i == 0 {
					assert.Equal(t, "70", fact.Cumulative.Money.Gross)
					assert.Equal(t, "settled", fact.Status)
				} else {
					assert.Equal(t, "-70", fact.Delta.Money.Gross)
					assert.Equal(t, "reversed", fact.Status)
				}
			}
			// A zero actual success and its later refund remain distinct events.
			zero := prepare("zero-success", 0)
			require.NoError(t, SettleCNMidjourney(context.Background(), db, zero.Id, 81, zero.MjId))
			require.NoError(t, RequestCNMidjourneyRefund(context.Background(), db, zero.Id, 81, zero.MjId, "failed-zero"))
			require.NoError(t, CompleteCNMidjourneyRefund(context.Background(), db, zero.CNBillingKey))
			var zeroReceipt CNMidjourneyBilling
			require.NoError(t, db.First(&zeroReceipt, map[string]any{"key": zero.CNBillingKey}).Error)
			assert.Equal(t, "refunded", zeroReceipt.State)
			assert.EqualValues(t, 2, zeroReceipt.Version)
			cancel := prepare("before-charge-failure", 80)
			cancel.Status = "FAILURE"
			won, err := cancel.UpdateWithStatus("SUBMITTED")
			require.NoError(t, err)
			require.True(t, won)
			require.NoError(t, RecoverCNMidjourney(context.Background(), db, 100))
			var cancelled CNMidjourneyBilling
			require.NoError(t, db.First(&cancelled, map[string]any{"key": cancel.CNBillingKey}).Error)
			assert.Equal(t, "cancelled", cancelled.State)
			assert.EqualValues(t, 1, cancelled.Version)
			check(0, 2)
			// Every financial boundary rolls back; a cold worker retries the
			// persisted prepared operation rather than inferring from task quota.
			failure := prepare("recover-charge", 120)
			for _, table := range []string{"tokens", "users", "channels", "midjourneys", "cn_finance_outboxes", "cn_midjourney_billings", "cn_midjourney_effects"} {
				inject := func(tx *gorm.DB) {
					if tx.Statement.Table == table {
						tx.AddError(errors.New("injected financial write failure"))
					}
				}
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("mj-fail", inject))
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("mj-fail", inject))
				require.Error(t, SettleCNMidjourney(context.Background(), db, failure.Id, 81, failure.MjId))
				require.NoError(t, db.Callback().Create().Remove("mj-fail"))
				require.NoError(t, db.Callback().Update().Remove("mj-fail"))
				check(0, 2)
			}
			require.NoError(t, pool.Close())
			db, err = gorm.Open(db.Dialector, &gorm.Config{})
			require.NoError(t, err)
			DB = db
			reopened, err := db.DB()
			require.NoError(t, err)
			if dialect == "sqlite" {
				reopened.SetMaxOpenConns(1)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			t.Setenv("OPS_CN_DURABLE_MIDJOURNEY_ENABLED", "false")
			t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "false")
			require.NoError(t, RecoverCNMidjourney(context.Background(), db, 100))
			t.Setenv("OPS_CN_DURABLE_MIDJOURNEY_ENABLED", "true")
			t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
			check(120, 3)
			require.NoError(t, RequestCNMidjourneyRefund(context.Background(), db, failure.Id, 81, failure.MjId, "recover-refund"))
			for _, table := range []string{"tokens", "channels", "midjourneys", "cn_finance_outboxes", "cn_midjourney_billings", "cn_midjourney_effects"} {
				inject := func(tx *gorm.DB) {
					if tx.Statement.Table == table {
						if _, metadata := tx.Statement.Dest.(map[string]any); table != "cn_midjourney_billings" || !metadata {
							tx.AddError(errors.New("injected refund failure"))
						}
					}
				}
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("mj-refund-fail", inject))
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("mj-refund-fail", inject))
				require.Error(t, CompleteCNMidjourneyRefund(context.Background(), db, failure.CNBillingKey))
				require.NoError(t, db.Callback().Create().Remove("mj-refund-fail"))
				require.NoError(t, db.Callback().Update().Remove("mj-refund-fail"))
				check(120, 3)
				var pending CNMidjourneyBilling
				require.NoError(t, db.First(&pending, map[string]any{"key": failure.CNBillingKey}).Error)
				assert.Equal(t, "charged", pending.State)
				assert.Positive(t, pending.Attempts)
				assert.NotEmpty(t, pending.LastErrorCode)
			}
			require.NoError(t, db.Model(&CNMidjourneyBilling{}).Where(map[string]any{"key": failure.CNBillingKey}).Update("next_attempt_at", 0).Error)
			require.NoError(t, RecoverCNMidjourney(context.Background(), db, 100))
			check(0, 3)
			// Concurrent same-task settlement and cancellation can only debit
			// once, then return to zero; stale status writers cannot restore quota.
			race := prepare("settle-cancel-race", 40)
			var wg sync.WaitGroup
			results := make(chan error, 2)
			wg.Go(func() { results <- SettleCNMidjourney(context.Background(), db, race.Id, 81, race.MjId) })
			wg.Go(func() {
				err := RequestCNMidjourneyRefund(context.Background(), db, race.Id, 81, race.MjId, "race")
				if err == nil {
					err = CompleteCNMidjourneyRefund(context.Background(), db, race.CNBillingKey)
				}
				results <- err
			})
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					require.ErrorIs(t, err, ErrCNConflict)
				}
			}
			race.Quota, race.Status = 999, "SUCCESS"
			require.NoError(t, race.Update())
			var persisted Midjourney
			require.NoError(t, db.First(&persisted, race.Id).Error)
			assert.Zero(t, persisted.Quota)
			var user User
			require.NoError(t, db.First(&user, 81).Error)
			check(0, user.RequestCount)
			// Soft/hard-deleted tokens have an explicit amount disposition;
			// owner mismatch fails before any user refund and remains retryable.
			for _, hard := range []bool{false, true} {
				id := 82
				if hard {
					id = 83
				}
				require.NoError(t, db.Create(&Token{Id: id, UserId: 81, Key: fmt.Sprintf("mj-deleted-%d", id), RemainQuota: 1000}).Error)
				deleted := &Midjourney{UserId: 81, MjId: fmt.Sprintf("deleted-%d", id), ChannelId: 81, Quota: 30, Action: "IMAGINE"}
				require.NoError(t, PrepareCNMidjourney(deleted, id, false, false))
				require.NoError(t, deleted.Insert())
				require.NoError(t, SettleCNMidjourney(context.Background(), db, deleted.Id, 81, deleted.MjId))
				if hard {
					require.NoError(t, db.Unscoped().Delete(&Token{}, id).Error)
				} else {
					require.NoError(t, db.Delete(&Token{}, id).Error)
				}
				require.NoError(t, RequestCNMidjourneyRefund(context.Background(), db, deleted.Id, 81, deleted.MjId, "deleted"))
				if !hard {
					require.NoError(t, db.Unscoped().Model(&Token{}).Where("id = ?", id).Update("user_id", 999).Error)
					require.ErrorIs(t, CompleteCNMidjourneyRefund(context.Background(), db, deleted.CNBillingKey), ErrCNConflict)
					require.NoError(t, db.Unscoped().Model(&Token{}).Where("id = ?", id).Update("user_id", 81).Error)
				}
				require.NoError(t, CompleteCNMidjourneyRefund(context.Background(), db, deleted.CNBillingKey))
				var disposition CNMidjourneyBilling
				require.NoError(t, db.First(&disposition, map[string]any{"key": deleted.CNBillingKey}).Error)
				assert.Equal(t, "refunded", disposition.State)
				assert.Equal(t, 30, disposition.TokenDisposedQuota)
				assert.Contains(t, disposition.TokenRefundStatus, "token_not_recredited")
			}
			// A durable agency user cannot be adopted by the ordinary lane.
			blocked := prepare("ownership-fence", 10)
			var originalReceipt CNMidjourneyBilling
			require.NoError(t, db.First(&originalReceipt, map[string]any{"key": blocked.CNBillingKey}).Error)
			for _, malformed := range []map[string]any{{"state": "unknown"}, {"created_at": "bad"}, {"version": int64(1)}} {
				require.NoError(t, db.Model(&CNMidjourneyBilling{}).Where(map[string]any{"key": blocked.CNBillingKey}).Updates(malformed).Error)
				require.ErrorIs(t, SettleCNMidjourney(context.Background(), db, blocked.Id, 81, blocked.MjId), ErrCNConflict)
				require.ErrorIs(t, RequestCNMidjourneyRefund(context.Background(), db, blocked.Id, 81, blocked.MjId, "invalid"), ErrCNConflict)
				require.NoError(t, db.Save(&originalReceipt).Error)
				check(0, user.RequestCount+2)
			}
			require.NoError(t, db.Model(&User{}).Where("id = ?", 81).Update("billing_mode", AgencyDurableBillingMode).Error)
			require.ErrorIs(t, SettleCNMidjourney(context.Background(), db, blocked.Id, 81, blocked.MjId), ErrAgencyFundingUnavailable)
			require.NoError(t, db.Model(&User{}).Where("id = ?", 81).Update("billing_mode", "").Error)
			require.NoError(t, MjBulkUpdateByTaskIds([]int{blocked.Id}, map[string]any{"status": "FAILURE", "progress": "100%"}))
			require.NoError(t, RecoverCNMidjourney(context.Background(), db, 100))
			// Log commit succeeds but primary acknowledgement fails. Retry must
			// not duplicate even when the two databases cannot share a transaction.
			logDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "logs.db")), &gorm.Config{})
			require.NoError(t, err)
			logPool, err := logDB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = logPool.Close() })
			require.NoError(t, logDB.AutoMigrate(&Log{}))
			require.NoError(t, logDB.Callback().Create().Before("gorm:create").Register("mj-log-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "logs" {
					tx.AddError(errors.New("injected log transaction failure"))
				}
			}))
			require.Error(t, DeliverCNMidjourneyEffects(context.Background(), db, logDB, 100))
			require.NoError(t, logDB.Callback().Create().Remove("mj-log-fail"))
			var emptyReceipts int64
			require.NoError(t, logDB.Model(&CNMidjourneyLogReceipt{}).Count(&emptyReceipts).Error)
			assert.Zero(t, emptyReceipts)
			require.NoError(t, db.Model(&CNMidjourneyEffect{}).Where("state = ?", "pending").Update("next_attempt_at", 0).Error)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("mj-log-ack", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_midjourney_effects" {
					if values, ok := tx.Statement.Dest.(map[string]any); ok && values["state"] == "delivered" {
						tx.AddError(errors.New("injected outbox ack loss"))
					}
				}
			}))
			require.Error(t, DeliverCNMidjourneyEffects(context.Background(), db, logDB, 100))
			require.NoError(t, db.Callback().Update().Remove("mj-log-ack"))
			var logCount int64
			require.NoError(t, logDB.Model(&Log{}).Count(&logCount).Error)
			assert.Positive(t, logCount)
			require.NoError(t, db.Model(&CNMidjourneyEffect{}).Where("state = ?", "pending").Update("next_attempt_at", 0).Error)
			require.NoError(t, DeliverCNMidjourneyEffects(context.Background(), db, logDB, 100))
			var retriedCount int64
			require.NoError(t, logDB.Model(&Log{}).Count(&retriedCount).Error)
			assert.Equal(t, logCount, retriedCount)
			// Also verify the delivery transaction on each actual engine.
			require.NoError(t, db.Model(&CNMidjourneyEffect{}).Where("state = ?", "delivered").Update("state", "pending").Error)
			require.NoError(t, db.AutoMigrate(&CNMidjourneyLogReceipt{}))
			concurrentLogs := make(chan error, 2)
			for range 2 {
				wg.Go(func() { concurrentLogs <- DeliverCNMidjourneyEffects(context.Background(), db, db, 100) })
			}
			wg.Wait()
			close(concurrentLogs)
			for err := range concurrentLogs {
				require.NoError(t, err)
			}
			require.NoError(t, DeliverCNMidjourneyEffects(context.Background(), db, db, 100))
			var actualLogs, actualEffects int64
			require.NoError(t, db.Model(&Log{}).Count(&actualLogs).Error)
			require.NoError(t, db.Model(&CNMidjourneyEffect{}).Count(&actualEffects).Error)
			assert.Equal(t, actualEffects, actualLogs)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			assert.NotEmpty(t, page["items"])
			assert.EqualValues(t, 0, page["publicationPendingCount"])
			proofs, err := cnMidjourneyProofs(db, &CNFinanceStream{}, map[string]bool{})
			require.NoError(t, err)
			assert.Positive(t, proofs[0].dependency.PendingCount)
			assert.True(t, proofs[1].coverageGap)
			assert.EqualValues(t, 1, proofs[1].dependency.UnknownCount)
			// Duplicate public identity never creates a second billable task.
			duplicate := &Midjourney{UserId: 81, MjId: task.MjId, ChannelId: 81, Quota: 100}
			require.NoError(t, PrepareCNMidjourney(duplicate, 81, false, false))
			require.ErrorIs(t, duplicate.Insert(), ErrCNConflict)
		})
	}
}
