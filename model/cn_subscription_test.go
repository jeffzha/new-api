package model

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNSubscriptionLifecycleAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_SUBSCRIPTION_ENABLED", "true")
	oldBatch, oldRedis := common.BatchUpdateEnabled, common.RedisEnabled
	common.BatchUpdateEnabled, common.RedisEnabled = false, false
	t.Cleanup(func() { common.BatchUpdateEnabled, common.RedisEnabled = oldBatch, oldRedis })
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
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "subscription.db")), &gorm.Config{})
				require.NoError(t, err)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &UserSubscription{}, &SubscriptionPlan{}, &SubscriptionPreConsumeRecord{}))
			previous := DB
			DB = db
			t.Cleanup(func() { DB = previous })
			previousType := common.MainDatabaseType()
			switch dialect {
			case "mysql":
				common.SetMainDatabaseType(common.DatabaseTypeMySQL)
			case "postgres":
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			default:
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			}
			t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
			require.NoError(t, db.Create(&User{Id: 61, Username: "subscription-cn", Quota: 1000}).Error)
			require.NoError(t, db.Create(&Token{Id: 61, UserId: 61, Key: "subscription-test-key", RemainQuota: 10000}).Error)
			require.NoError(t, db.Create(&SubscriptionPlan{Id: 61, Title: "Subscription", TotalAmount: 10000, QuotaResetPeriod: SubscriptionResetNever}).Error)
			require.NoError(t, db.Create(&UserSubscription{Id: 61, UserId: 61, PlanId: 61, AmountTotal: 10000, Status: "active", StartTime: time.Now().Unix(), EndTime: time.Now().Add(24 * time.Hour).Unix()}).Error)
			// Upgrade the previous schema with a live legacy reservation. Existing
			// balances and request uniqueness must survive both startup migrations.
			require.NoError(t, db.Migrator().DropTable(&CNSubscriptionLifecycle{}, &CNSubscriptionCancellation{}))
			legacySub := UserSubscription{Id: 62, UserId: 62, PlanId: 61, AmountTotal: 1000, AmountUsed: 75, Status: "active"}
			legacyRecord := SubscriptionPreConsumeRecord{RequestId: "upgrade-legacy", UserId: 62, UserSubscriptionId: 62, PreConsumed: 75, Status: "consumed"}
			require.NoError(t, db.Create(&legacySub).Error)
			require.NoError(t, db.Create(&legacyRecord).Error)
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			var upgradedSub UserSubscription
			var upgradedRecord SubscriptionPreConsumeRecord
			require.NoError(t, db.First(&upgradedSub, 62).Error)
			require.NoError(t, db.Where("request_id = ?", "upgrade-legacy").First(&upgradedRecord).Error)
			assert.Equal(t, legacySub, upgradedSub)
			assert.Equal(t, legacyRecord, upgradedRecord)
			duplicate := legacyRecord
			duplicate.Id = 0
			require.Error(t, db.Create(&duplicate).Error)
			require.NoError(t, db.Delete(&legacyRecord).Error)
			check := func(used int64) {
				t.Helper()
				var sub UserSubscription
				var token Token
				require.NoError(t, DB.First(&sub, 61).Error)
				assert.Equal(t, used, sub.AmountUsed)
				require.NoError(t, DB.First(&token, 61).Error)
				assert.EqualValues(t, used, token.UsedQuota)
				assert.EqualValues(t, 10000-used, token.RemainQuota)
			}
			require.NoError(t, db.Model(&Token{}).Where("id = ?", 61).Update("remain_quota", 50).Error)
			_, err = PreConsumeCNSubscription("insufficient-token", 61, 100, 61, false, false)
			require.ErrorIs(t, err, ErrInsufficientAgencyTokenQuota)
			var noReserve CNSubscriptionLifecycle
			require.ErrorIs(t, db.Where("request_id = ?", "insufficient-token").First(&noReserve).Error, gorm.ErrRecordNotFound)
			require.NoError(t, db.Model(&Token{}).Where("id = ?", 61).Update("remain_quota", 10000).Error)
			check(0)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("s4-sub-reserve-marker", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_subscription_lifecycles" {
					tx.AddError(errors.New("injected initial receipt failure"))
				}
			}))
			_, err = PreConsumeCNSubscription("reserve-marker-retry", 61, 20, 61, false, false)
			require.Error(t, err)
			check(0)
			require.NoError(t, db.Callback().Create().Remove("s4-sub-reserve-marker"))
			_, err = PreConsumeCNSubscription("reserve-marker-retry", 61, 20, 61, false, false)
			require.NoError(t, err)
			require.NoError(t, FinalizeCNSubscription("reserve-marker-retry", 61, 0))
			check(0)
			for _, tc := range []struct {
				id          string
				pre, target int
			}{{"zero-delta", 100, 100}, {"actual-zero", 100, 0}, {"supplement", 100, 140}, {"release", 100, 60}} {
				_, err = PreConsumeCNSubscription(tc.id, 61, tc.pre, 61, false, false)
				require.NoError(t, err)
				_, err = PreConsumeCNSubscription(tc.id, 61, tc.pre, 61, false, false)
				require.NoError(t, err)
				require.NoError(t, FinalizeCNSubscription(tc.id, 61, tc.target))
				require.NoError(t, FinalizeCNSubscription(tc.id, 61, tc.target))
				require.ErrorIs(t, FinalizeCNSubscription(tc.id, 61, tc.target+1), ErrCNConflict)
			}
			check(300)
			_, err = PreConsumeCNSubscription("cancel-recover", 61, 80, 61, false, false)
			require.NoError(t, err)
			require.NoError(t, ReserveCNSubscription("cancel-recover", 61, 120))
			require.NoError(t, ReserveCNSubscription("cancel-recover", 61, 120))
			check(420)
			require.NoError(t, RequestCNSubscriptionCancellation(db, "cancel-recover", 61))
			require.ErrorIs(t, FinalizeCNSubscription("cancel-recover", 61, 120), ErrCNConflict)
			require.ErrorIs(t, ReserveCNSubscription("cancel-recover", 61, 160), ErrCNConflict)
			for _, phase := range []string{"subscription", "token", "outbox", "completion"} {
				callback := "s4-sub-failure"
				inject := func(tx *gorm.DB) {
					if phase == "subscription" && tx.Statement.Table == "user_subscriptions" || phase == "token" && tx.Statement.Table == "tokens" || phase == "outbox" && tx.Statement.Table == "cn_finance_outboxes" {
						tx.AddError(errors.New("injected subscription cancellation failure"))
					}
					if phase == "completion" && tx.Statement.Table == "cn_subscription_cancellations" {
						if values, ok := tx.Statement.Dest.(map[string]any); ok && values["state"] == "complete" {
							tx.AddError(errors.New("injected completion failure"))
						}
					}
				}
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, inject))
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, inject))
				require.Error(t, CompleteCNSubscriptionCancellation(context.Background(), db, "cancel-recover"))
				require.NoError(t, db.Callback().Create().Remove(callback))
				require.NoError(t, db.Callback().Update().Remove(callback))
				check(420)
				var intent CNSubscriptionCancellation
				require.NoError(t, db.Where("request_id = ?", "cancel-recover").First(&intent).Error)
				assert.Equal(t, "pending", intent.State)
				assert.NotEmpty(t, intent.LastErrorCode)
				assert.Positive(t, intent.Attempts)
				var lifecycle CNSubscriptionLifecycle
				require.NoError(t, db.Where("request_id = ?", "cancel-recover").First(&lifecycle).Error)
				assert.Equal(t, "reserved", lifecycle.State)
			}
			// Cleanup of legacy idempotency records cannot destroy financial replay.
			require.NoError(t, db.Where("request_id = ?", "cancel-recover").Delete(&SubscriptionPreConsumeRecord{}).Error)
			require.NoError(t, pool.Close())
			rebuilt, err := gorm.Open(db.Dialector, &gorm.Config{})
			require.NoError(t, err)
			rebuiltPool, err := rebuilt.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = rebuiltPool.Close() })
			DB, db = rebuilt, rebuilt
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, db.Model(&CNSubscriptionCancellation{}).Where("request_id = ?", "cancel-recover").Update("next_attempt_at", 0).Error)
			require.NoError(t, RecoverCNSubscriptionCancellations(context.Background(), db, 100))
			require.NoError(t, RecoverCNSubscriptionCancellations(context.Background(), db, 100))
			require.NoError(t, RefundSubscriptionPreConsume("cancel-recover"))
			check(300)
			_, err = PreConsumeUserSubscription("cancel-recover", 61, "", 0, 80)
			require.ErrorIs(t, err, ErrCNConflict)
			_, err = PreConsumeCNSubscription("cancel-recover", 61, 80, 61, false, false)
			require.ErrorIs(t, err, ErrCNConflict)
			// A final settlement failure cannot acknowledge the financial state.
			_, err = PreConsumeCNSubscription("settle-retry", 61, 40, 61, false, false)
			require.NoError(t, err)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("s4-final-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected final outbox failure"))
				}
			}))
			require.Error(t, FinalizeCNSubscription("settle-retry", 61, 50))
			check(340)
			require.NoError(t, db.Callback().Create().Remove("s4-final-failure"))
			require.NoError(t, FinalizeCNSubscription("settle-retry", 61, 50))
			check(350)
			// Same-request settle/cancel race has exactly one terminal winner.
			_, err = PreConsumeCNSubscription("race", 61, 50, 61, false, false)
			require.NoError(t, err)
			if dialect == "sqlite" {
				rebuiltPool.SetMaxOpenConns(1)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			wg.Go(func() { errs <- FinalizeCNSubscription("race", 61, 70) })
			wg.Go(func() {
				if err := RequestCNSubscriptionCancellation(db, "race", 61); err != nil {
					errs <- err
					return
				}
				errs <- CompleteCNSubscriptionCancellation(context.Background(), db, "race")
			})
			wg.Wait()
			close(errs)
			success := 0
			for err := range errs {
				if err == nil {
					success++
				} else {
					require.ErrorIs(t, err, ErrCNConflict)
				}
			}
			assert.Equal(t, 1, success)
			var race CNSubscriptionLifecycle
			require.NoError(t, db.Where("request_id = ?", "race").First(&race).Error)
			if race.State == "settled" {
				check(420)
			} else {
				assert.Equal(t, "cancelled", race.State)
				check(350)
			}
			// A separate active request never expires into a cancellation.
			_, err = PreConsumeCNSubscription("active", 61, 10, 61, false, false)
			require.NoError(t, err)
			require.NoError(t, RecoverCNSubscriptionCancellations(context.Background(), db, 100))
			var active CNSubscriptionLifecycle
			require.NoError(t, db.Where("request_id = ?", "active").First(&active).Error)
			assert.Equal(t, "reserved", active.State)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			seen := map[string]bool{}
			for _, fact := range page["items"].([]CNFact) {
				if fact.SourceEvidence[0].RecordType == "cn_subscription_lifecycle_final" {
					seen[*fact.RequestID] = true
					assert.Equal(t, "subscription", fact.BillingSource)
				}
			}
			for _, id := range []string{"zero-delta", "actual-zero", "cancel-recover", "settle-retry", "race"} {
				assert.True(t, seen[id], fmt.Sprintf("missing final receipt for %s", id))
			}
			assert.False(t, seen["active"])
			var beforeLegacy UserSubscription
			require.NoError(t, db.First(&beforeLegacy, 61).Error)
			_, err = PreConsumeUserSubscription("legacy-refund-atomic", 61, "", 0, 20)
			require.NoError(t, err)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("s4-legacy-refund-marker", func(tx *gorm.DB) {
				if tx.Statement.Table == "subscription_pre_consume_records" {
					tx.AddError(errors.New("injected legacy refund marker failure"))
				}
			}))
			require.Error(t, RefundSubscriptionPreConsume("legacy-refund-atomic"))
			require.NoError(t, db.Callback().Update().Remove("s4-legacy-refund-marker"))
			var afterLegacy UserSubscription
			require.NoError(t, db.First(&afterLegacy, 61).Error)
			assert.Equal(t, beforeLegacy.AmountUsed+20, afterLegacy.AmountUsed)
			require.NoError(t, RefundSubscriptionPreConsume("legacy-refund-atomic"))
			require.NoError(t, RefundSubscriptionPreConsume("legacy-refund-atomic"))
			require.NoError(t, db.First(&afterLegacy, 61).Error)
			assert.Equal(t, beforeLegacy.AmountUsed, afterLegacy.AmountUsed)
			// Missing/pending counts are observed rows, independent from writer
			// coverage gaps. Empty unsupported scopes must never auto-complete.
			require.NoError(t, db.Create(&CNFinanceIntent{Key: "pending", Payload: "{}", State: "pending_mutation", CreatedAt: time.Now().Unix()}).Error)
			require.NoError(t, db.Create(&CNFinanceIntent{Key: "ack", Payload: "{}", State: "acknowledged_unverified", CreatedAt: time.Now().Unix()}).Error)
			daily, err := ReadCNDaily(context.Background(), db, time.Now().UTC().Format("2006-01-02"), "")
			require.NoError(t, err)
			for _, dependency := range daily["dependencies"].([]CNDependency) {
				switch dependency.SourceID {
				case "ordinary_wallet_legacy_batch":
					assert.EqualValues(t, 1, dependency.PendingCount)
					assert.EqualValues(t, 1, dependency.UnknownCount)
				case "subscriptions_and_credit_schedules":
					assert.EqualValues(t, 1, dependency.UnknownCount)
					assert.Contains(t, dependency.ExcludedPaths, "writer_coverage_unproven")
				case "payment_fees":
					assert.Zero(t, dependency.UnknownCount)
					assert.Zero(t, dependency.PendingCount)
					assert.Equal(t, "gap", dependency.Status)
					assert.Nil(t, dependency.ProofID)
					assert.Nil(t, dependency.SourceCutoff)
				case "ordinary_subscription_durable_session":
					assert.Positive(t, dependency.PendingCount)
				}
			}
		})
	}
}
