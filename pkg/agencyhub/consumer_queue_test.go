package agencyhub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func enqueueQueueEvent(t *testing.T, db *gorm.DB, userID, seq int64) (agencycontract.BillingEvent, model.AgencyEventDelivery) {
	t.Helper()
	id := common.GetUUID()
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: id, OperationID: id,
		EventType: "agency.funding_adjusted", UserID: userID, MoneySeq: seq, EventCount: 1, JournalRevision: 1,
		OccurredAtMS: time.Now().UnixMilli(), CommissionSkipReason: "noncommissionable_funding_adjustment"}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	hash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyBillingOutbox{EventID: id, OperationID: id, EventCount: 1,
		EventKind: event.EventType, UserID: userID, MoneySeq: seq, Payload: string(payload), PayloadHash: hash,
		SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS}).Error)
	delivery := model.AgencyEventDelivery{EventID: id, Status: "pending", NextRetryAt: 1, CreatedAt: 1}
	require.NoError(t, db.Create(&delivery).Error)
	return event, delivery
}

func TestConsumerQueueAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector
			if dialect == "sqlite" {
				dialector = sqlite.Open(filepath.Join(t.TempDir(), "queue.sqlite"))
			} else {
				dsn := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN"))
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("isolated external test database is not configured")
				}
				if dialect == "mysql" {
					dialector = mysql.Open(dsn)
				} else {
					dialector = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, model.MigrateAgency(db))
			for _, scenario := range []string{"reverse_backlog", "future_retry", "live_claim", "poison", "missing_delivery", "missing_receipt", "bad_hash", "same_sequence", "cancellation"} {
				t.Run(scenario, func(t *testing.T) {
					tx := db.Begin()
					require.NoError(t, tx.Error)
					t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
					app := New(tx, tx, Config{InstanceID: "queue-test", FactProjectionEnabled: true})
					const userID = int64(718881)
					if scenario == "cancellation" {
						_, delivery := enqueueQueueEvent(t, tx, userID, 1)
						ctx, cancel := context.WithCancel(t.Context())
						cancel()
						require.ErrorIs(t, app.RunConsumerOnce(ctx, 500), context.Canceled)
						require.NoError(t, tx.First(&delivery, delivery.ID).Error)
						assert.Equal(t, "pending", delivery.Status)
						assert.Zero(t, delivery.Attempts)
						return
					}
					if scenario == "reverse_backlog" {
						for seq := int64(700); seq >= 1; seq-- {
							_, delivery := enqueueQueueEvent(t, tx, userID, seq)
							// Backoff timestamps must not override the financial order.
							require.NoError(t, tx.Model(&delivery).Update("next_retry_at", 701-seq).Error)
						}
						require.NoError(t, app.RunConsumerOnce(t.Context(), 500))
						var completed int64
						require.NoError(t, tx.Model(&model.AgencyEventDelivery{}).Where("status = ?", "done").Count(&completed).Error)
						assert.Equal(t, int64(500), completed)
						require.NoError(t, app.drainConsumerBudget(t.Context()))
						require.NoError(t, app.drainConsumerBudget(t.Context()))
						var badAttempts int64
						require.NoError(t, tx.Model(&model.AgencyEventDelivery{}).Where("attempts <> 1 OR status <> ?", "done").Count(&badAttempts).Error)
						assert.Zero(t, badAttempts)
						require.NoError(t, tx.Model(&model.AgencySourceEvent{}).Count(&completed).Error)
						assert.Equal(t, int64(700), completed)
						return
					}
					if scenario == "same_sequence" {
						for range 3 {
							enqueueQueueEvent(t, tx, userID, 1)
						}
						enqueueQueueEvent(t, tx, userID, 2)
						// The envelope still projects exactly the authoritative amounts.
						original := componentBillingFixture()
						commitComponentBillingFixture(t, tx, original)
						commitComponentBillingFixture(t, tx, componentRefundFixture(original))
						app.config.CommissionEnabled = true
						require.NoError(t, app.drainConsumerBudget(t.Context()))
						require.NoError(t, app.drainConsumerBudget(t.Context()))
						var balance model.AgencyCommissionBalance
						require.NoError(t, tx.Where("agency_id = ?", *original.AgencyID).First(&balance).Error)
						assert.Equal(t, int64(150), balance.AvailableMicros)
						var usage model.AgencyDailyStat
						require.NoError(t, tx.Where("agency_id = ?", *original.AgencyID).First(&usage).Error)
						assert.Equal(t, original.ChargedTotalQuota, usage.ChargedQuota)
						assert.Equal(t, int64(50), usage.ReversalMicros)
						evidence, err := reconciliationEvidence(tx, model.AgencyReconciliationIssue{ObjectType: "billing_outbox", ObjectID: original.EventID}, false)
						require.NoError(t, err)
						assert.Equal(t, "consistent", evidence.State, "%+v", evidence)
						var notDone int64
						require.NoError(t, tx.Model(&model.AgencyEventDelivery{}).Where("status <> ?", "done").Count(&notDone).Error)
						assert.Zero(t, notDone)
						return
					}

					firstEvent, first := enqueueQueueEvent(t, tx, userID, 1)
					_, second := enqueueQueueEvent(t, tx, userID, 2)
					_, third := enqueueQueueEvent(t, tx, userID, 3)
					_, independent := enqueueQueueEvent(t, tx, userID+1, 1000)
					updates := map[string]any{}
					switch scenario {
					case "future_retry":
						updates = map[string]any{"status": "retry", "next_retry_at": time.Now().Unix() + 3600}
					case "live_claim":
						updates = map[string]any{"status": "claimed", "lease_owner": "other", "lease_token": 1, "lease_until": time.Now().Unix() + 3600}
					case "poison":
						updates = map[string]any{"status": "poison"}
					case "missing_delivery":
						require.NoError(t, tx.Delete(&first).Error)
					case "missing_receipt":
						updates = map[string]any{"status": "done"}
					case "bad_hash":
						require.NoError(t, tx.Model(&model.AgencyBillingOutbox{}).Where("event_id = ?", first.EventID).Update("payload_hash", "invalid").Error)
					}
					if len(updates) > 0 {
						require.NoError(t, tx.Model(&first).Updates(updates).Error)
					}
					limit := 500
					if scenario == "future_retry" || scenario == "live_claim" || scenario == "poison" {
						limit = 1
					}
					completed, err := app.runConsumerBatch(t.Context(), limit)
					require.NoError(t, err)
					assert.Equal(t, 1, completed, "other customers must continue")
					require.NoError(t, tx.First(&independent, independent.ID).Error)
					assert.Equal(t, "done", independent.Status)
					require.NoError(t, tx.First(&second, second.ID).Error)
					assert.NotEqual(t, "done", second.Status)
					expectedAttempts := 0
					if scenario == "missing_delivery" || scenario == "missing_receipt" {
						expectedAttempts = 1
						assert.Contains(t, second.LastError, "prior user money sequence")
					}
					assert.Equal(t, expectedAttempts, second.Attempts)
					require.NoError(t, tx.First(&third, third.ID).Error)
					assert.Zero(t, third.Attempts, "a failed predecessor must stop successors in the selected batch")
					if scenario == "bad_hash" {
						require.NoError(t, tx.First(&first, first.ID).Error)
						assert.Equal(t, "poison", first.Status)
						var issues int64
						require.NoError(t, tx.Model(&model.AgencyReconciliationIssue{}).Where("object_id = ?", first.EventID).Count(&issues).Error)
						assert.Equal(t, int64(1), issues)
						return
					}
					if scenario == "missing_receipt" {
						require.NoError(t, app.ProcessBillingEvent(firstEvent))
					} else if scenario == "missing_delivery" {
						require.NoError(t, app.RebuildDelivery(first.EventID))
					} else if scenario == "live_claim" {
						require.NoError(t, tx.Model(&first).Update("lease_until", time.Now().Unix()-1).Error)
					} else {
						require.NoError(t, tx.Model(&first).Updates(map[string]any{"status": "pending", "next_retry_at": 0}).Error)
					}
					require.NoError(t, tx.Model(&second).Update("next_retry_at", 0).Error)
					require.NoError(t, app.drainConsumerBudget(t.Context()))
					var receipts int64
					require.NoError(t, tx.Model(&model.AgencySourceEvent{}).Where("user_id = ?", userID).Count(&receipts).Error)
					assert.Equal(t, int64(3), receipts, fmt.Sprintf("%s must resume after its predecessor is resolved", scenario))
				})
			}
		})
	}
}

func TestConsumerPrefixAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector
			if dialect == "sqlite" {
				dialector = sqlite.Open(filepath.Join(t.TempDir(), "prefix.sqlite"))
			} else {
				dsn := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN"))
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("isolated external test database is not configured")
				}
				if dialect == "mysql" {
					dialector = mysql.Open(dsn)
				} else {
					dialector = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, db.AutoMigrate(&model.AgencyBillingOutbox{}, &model.AgencySourceEvent{}, &model.AgencyEventDelivery{}))
			const userID = int64(9917135)
			outboxes := make([]model.AgencyBillingOutbox, 0, 1100)
			receipts := make([]model.AgencySourceEvent, 0, 1100)
			for i := 1; i <= 1100; i++ {
				id := fmt.Sprintf("prefix-%s-%d", dialect, i)
				outboxes = append(outboxes, model.AgencyBillingOutbox{EventID: id, OperationID: id, EventKind: "agency.funding_adjusted", EventCount: 1, UserID: userID, MoneySeq: int64(i), Payload: "{}", PayloadHash: id, SchemaVersion: "1", CreatedAtMS: 1})
				receipts = append(receipts, model.AgencySourceEvent{EventID: id, SourceOperationID: id, UserID: userID, MoneySeq: int64(i), Payload: "{}", PayloadHash: id, SchemaVersion: "1", ProcessingStatus: "done", CreatedAtMS: 1})
			}
			require.NoError(t, db.CreateInBatches(outboxes, 100).Error)
			require.NoError(t, db.CreateInBatches(receipts, 100).Error)
			// Upgrade an existing released outbox and receipt table, then repeat.
			require.NoError(t, model.MigrateAgencyConsumerPrefix(db))
			require.NoError(t, model.MigrateAgencyConsumerPrefix(db))
			app := New(db, db, Config{FactProjectionEnabled: true})
			event := agencycontract.BillingEvent{UserID: userID, MoneySeq: 1101}
			require.ErrorIs(t, app.prepareConsumerPrefix(t.Context(), event), errConsumerPrefixWarming)
			var prefix model.AgencyConsumerPrefix
			require.NoError(t, db.Where("user_id=?", userID).First(&prefix).Error)
			assert.EqualValues(t, 512, prefix.ScanSeq)
			assert.Zero(t, prefix.VerifiedBefore)
			require.ErrorIs(t, app.prepareConsumerPrefix(t.Context(), event), errConsumerPrefixWarming)
			require.NoError(t, app.prepareConsumerPrefix(t.Context(), event))
			require.NoError(t, requireConsumerPrefix(db, event))
			require.NoError(t, model.MigrateAgencyConsumerPrefix(db))
			require.NoError(t, db.Where("user_id=?", userID).First(&prefix).Error)
			assert.EqualValues(t, 1101, prefix.VerifiedBefore)
			// Verified history does not need a new receipt query on every delivery.
			queryCount := 0
			require.NoError(t, db.Callback().Row().Before("gorm:row").Register("prefix-test-query-count", func(tx *gorm.DB) {
				if strings.Contains(tx.Statement.Table, "billing_outbox") {
					queryCount++
				}
			}))
			require.NoError(t, app.prepareConsumerPrefix(t.Context(), event))
			assert.Zero(t, queryCount)
			require.NoError(t, db.Callback().Row().Remove("prefix-test-query-count"))
			pending := outboxes[0]
			pending.ID, pending.MoneySeq = 0, 1101
			pending.EventID, pending.OperationID = "prefix-pending-"+dialect, "prefix-pending"
			require.NoError(t, db.Create(&pending).Error)
			delivery := model.AgencyEventDelivery{EventID: pending.EventID, Status: "pending"}
			require.NoError(t, db.Create(&delivery).Error)
			require.NoError(t, db.Model(&model.AgencyConsumerPrefix{}).Where("user_id=?", userID).Updates(map[string]any{"verified_before": 0, "scan_seq": 0, "scan_id": 0}).Error)
			require.NoError(t, New(db, db, Config{}).WarmConsumerPrefixes(t.Context()))
			require.NoError(t, requireConsumerPrefix(db, event))
			require.NoError(t, db.First(&delivery, delivery.ID).Error)
			assert.Equal(t, "pending", delivery.Status, "warmup only verifies receipts, never projects or acknowledges")
			// An old binary can insert historical events: the database invalidates
			// the proof even though its process knows nothing about the new table.
			late := model.AgencyBillingOutbox{EventID: "prefix-late-" + dialect, OperationID: "prefix-late", EventKind: "agency.funding_adjusted", EventCount: 1, UserID: userID, MoneySeq: 3, Payload: "{}", PayloadHash: "late", SchemaVersion: "1", CreatedAtMS: 1}
			require.NoError(t, db.Create(&late).Error)
			require.ErrorIs(t, requireConsumerPrefix(db, event), errPriorMoneySequence)
			require.ErrorIs(t, app.prepareConsumerPrefix(t.Context(), event), errPriorMoneySequence)
			require.NoError(t, db.Create(&model.AgencySourceEvent{EventID: late.EventID, SourceOperationID: late.OperationID, UserID: userID, MoneySeq: 3, Payload: "{}", PayloadHash: "wrong", SchemaVersion: "1", ProcessingStatus: "done", CreatedAtMS: 1}).Error)
			require.ErrorIs(t, app.prepareConsumerPrefix(t.Context(), event), errPriorMoneySequence, "a completed receipt for another payload is not proof")
			require.NoError(t, db.Model(&model.AgencySourceEvent{}).Where("event_id=?", late.EventID).Update("payload_hash", "late").Error)
			for range 3 {
				err = app.prepareConsumerPrefix(t.Context(), event)
				if err == nil {
					break
				}
				require.ErrorIs(t, err, errConsumerPrefixWarming)
			}
			require.NoError(t, err)
			// Repairs, deletes, and late same-sequence siblings must invalidate.
			for _, column := range []string{"processing_status", "payload_hash"} {
				value := "processing"
				restore := "done"
				if column == "payload_hash" {
					value = "changed"
					restore = outboxes[0].PayloadHash
				}
				require.NoError(t, db.Model(&model.AgencySourceEvent{}).Where("event_id=?", outboxes[0].EventID).Update(column, value).Error)
				require.ErrorIs(t, requireConsumerPrefix(db, event), errPriorMoneySequence)
				require.ErrorIs(t, app.prepareConsumerPrefix(t.Context(), event), errPriorMoneySequence)
				require.NoError(t, db.Model(&model.AgencySourceEvent{}).Where("event_id=?", outboxes[0].EventID).Update(column, restore).Error)
				for range 3 {
					err = app.prepareConsumerPrefix(t.Context(), event)
					if err == nil {
						break
					}
					require.ErrorIs(t, err, errConsumerPrefixWarming)
				}
				require.NoError(t, err)
			}
			require.NoError(t, db.Where("event_id=?", outboxes[0].EventID).Delete(&model.AgencySourceEvent{}).Error)
			require.ErrorIs(t, requireConsumerPrefix(db, event), errPriorMoneySequence)
			require.ErrorIs(t, app.prepareConsumerPrefix(t.Context(), event), errPriorMoneySequence)
			// Rollback does not advance a durable proof. Other users remain free.
			require.NoError(t, app.WarmConsumerPrefixes(t.Context()))
			require.ErrorIs(t, requireConsumerPrefix(db, event), errPriorMoneySequence)
			require.NoError(t, app.prepareConsumerPrefix(t.Context(), agencycontract.BillingEvent{UserID: userID + 1, MoneySeq: 5000}))
			require.Error(t, db.Transaction(func(tx *gorm.DB) error {
				require.NoError(t, tx.Model(&model.AgencyConsumerPrefix{}).Where("user_id=?", userID).Update("verified_before", 99999).Error)
				return fmt.Errorf("fixture rollback")
			}))
			require.ErrorIs(t, requireConsumerPrefix(db, event), errPriorMoneySequence)
			// A transaction that has inserted through an older writer must finish
			// before a first verification can certify that user's history.
			if dialect != "sqlite" {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				writer := db.WithContext(ctx).Begin()
				require.NoError(t, writer.Error)
				t.Cleanup(func() { _ = writer.Rollback().Error })
				concurrent := late
				concurrent.ID = 0
				concurrent.UserID = userID + 2
				concurrent.EventID += "-concurrent"
				concurrent.OperationID += "-concurrent"
				require.NoError(t, writer.Create(&concurrent).Error)
				started := make(chan struct{})
				workerDB := db.Session(&gorm.Session{NewDB: true})
				require.NoError(t, workerDB.Callback().Create().Before("gorm:create").Register("prefix-concurrent-create", func(tx *gorm.DB) {
					if tx.Statement.Table == (model.AgencyConsumerPrefix{}).TableName() {
						select {
						case <-started:
						default:
							close(started)
						}
					}
				}))
				result := make(chan error, 1)
				go func() {
					result <- New(workerDB, workerDB, Config{}).prepareConsumerPrefix(ctx, agencycontract.BillingEvent{UserID: concurrent.UserID, MoneySeq: 4})
				}()
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				require.NoError(t, writer.Commit().Error)
				require.ErrorIs(t, <-result, errPriorMoneySequence)
				require.NoError(t, workerDB.Callback().Create().Remove("prefix-concurrent-create"))
				require.NoError(t, db.Where("user_id=?", concurrent.UserID).Delete(&model.AgencyBillingOutbox{}).Error)
				require.NoError(t, db.Where("user_id=?", concurrent.UserID).Delete(&model.AgencyConsumerPrefix{}).Error)
			}
			require.NoError(t, db.Where("user_id=?", userID).Delete(&model.AgencyBillingOutbox{}).Error)
			require.NoError(t, db.Delete(&delivery).Error)
			require.NoError(t, db.Where("user_id=?", userID).Delete(&model.AgencySourceEvent{}).Error)
			require.NoError(t, db.Where("user_id IN ?", []int64{userID, userID + 1}).Delete(&model.AgencyConsumerPrefix{}).Error)
		})
	}
}
