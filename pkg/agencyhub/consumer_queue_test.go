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
