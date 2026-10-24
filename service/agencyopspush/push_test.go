package agencyopspush

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDistributorEventsAcrossDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open("file:distributor-events?mode=memory&cache=shared")
			case "mysql":
				dsn := os.Getenv("AGENCY_HUB_TEST_MYSQL_DSN")
				if dsn == "" || os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
					t.Skip("isolated MySQL is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("AGENCY_HUB_TEST_POSTGRES_DSN")
				if dsn == "" || os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
					t.Skip("isolated PostgreSQL is not configured")
				}
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			pool.SetMaxOpenConns(2)
			t.Setenv("OPS_DISTRIBUTOR_EVENTS_ENABLED", "false")
			require.NoError(t, db.Migrator().DropTable(model.AgencyModels()...))
			require.NoError(t, db.Migrator().DropTable(&model.User{}))
			// Start with the released business schema and an existing fact, then
			// upgrade twice without rewriting history or generating a full scan.
			models := []any{&model.AgencyUsageFact{}, &model.AgencyTopupFact{}, &model.AgencyCommissionLedger{}, &model.AgencyCommissionBalance{}, &model.AgencyWithdrawal{}, &model.AgencyAuditLog{}}
			for _, table := range append(models, &model.AgencyOpsEvent{}, &model.AgencyOpsObjectRevision{}, &model.AgencyOpsSenderLease{}) {
				require.NoError(t, db.Migrator().DropTable(table))
			}
			require.NoError(t, db.AutoMigrate(models...))
			existing := model.AgencyUsageFact{EventID: "old-fact", UserID: 301, ComponentID: "default", ModelKey: "model", OriginModelName: "gpt-4o"}
			require.NoError(t, db.Create(&existing).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.AgencyOpsEvent{}, &model.AgencyOpsObjectRevision{}, &model.AgencyOpsSenderLease{}))
			}
			require.True(t, db.Migrator().HasIndex(&model.AgencyOpsEvent{}, "idx_agency_ops_event_due"))
			require.NoError(t, db.AutoMigrate(&model.Agency{}))
			t.Setenv("OPS_DISTRIBUTOR_EVENTS_ENABLED", "true")
			tx := db.Begin()
			require.NoError(t, tx.Error)
			rolledBack := model.AgencyUsageFact{EventID: "rolled-back", UserID: 301, ComponentID: "default", ModelKey: "model", OriginModelName: "gpt-4o"}
			require.NoError(t, tx.Create(&rolledBack).Error)
			require.NoError(t, tx.Rollback().Error)
			var count int64
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Count(&count).Error)
			require.Zero(t, count)
			agency := int64(7)
			fact := model.AgencyUsageFact{EventID: "new-fact", UserID: 301, AgencyID: &agency, ComponentID: "default", ModelKey: "model", OriginModelName: "gpt-4o", ChargedQuota: 100}
			require.NoError(t, db.Create(&fact).Error)
			require.NoError(t, db.Model(&model.AgencyUsageFact{}).Where("id = ?", -1).Update("charged_quota", 80).Error)
			require.NoError(t, db.Model(&fact).Update("charged_quota", 90).Error)
			withdrawal := model.AgencyWithdrawal{RequestNo: "withdrawal-case", AgencyID: agency, CurrencyCode: "CNY", AmountMicros: 1000000, Status: "submitted", Version: 1, AccountSnapshot: "secret-account-snapshot", PaymentReference: "secret-bank-reference"}
			require.NoError(t, db.Create(&withdrawal).Error)
			require.NoError(t, db.Model(&withdrawal).Updates(map[string]any{"status": "payment_unknown", "version": 2}).Error)
			var events []model.AgencyOpsEvent
			require.NoError(t, db.Order("id").Find(&events).Error)
			require.Len(t, events, 4)
			assert.NotContains(t, events[2].Payload, "secret-account-snapshot")
			assert.NotContains(t, events[2].Payload, "secret-bank-reference")
			var updated map[string]any
			require.NoError(t, common.Unmarshal([]byte(events[1].Payload), &updated))
			assert.Equal(t, float64(2), updated["revision"])
			assert.Equal(t, float64(90), updated["record"].(map[string]any)["charged_quota"])
			// The receiver persists, but its first ACK is lost. Replay must use
			// exactly the same event IDs and must not create a second fact.
			received := map[string]bool{}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, path, r.URL.Path)
				assert.Equal(t, "fixture-private-key", r.Header.Get("X-Gateway-Key"))
				var batch struct {
					Events []struct {
						EventID string `json:"eventId"`
					} `json:"events"`
				}
				require.NoError(t, common.DecodeJson(r.Body, &batch))
				ids := []string{}
				for _, e := range batch.Events {
					received[e.EventID] = true
					ids = append(ids, e.EventID)
				}
				calls++
				if calls == 1 {
					w.WriteHeader(503)
					return
				}
				body, err := common.Marshal(map[string]any{"data": map[string]any{"accepted": ids, "durable": true}})
				require.NoError(t, err)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			sender, err := New(db, server.URL, "fixture-gateway", "fixture-private-key")
			require.NoError(t, err)
			now := time.Now()
			sender.Now = func() time.Time { return now }
			_, err = sender.Step(t.Context())
			require.NoError(t, err)
			assert.Len(t, received, 4)
			now = now.Add(5 * time.Second)
			_, err = sender.Step(t.Context())
			require.NoError(t, err)
			assert.Len(t, received, 4)
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Where("status = ?", "delivered").Count(&count).Error)
			assert.Equal(t, int64(4), count)
			_, err = sender.Step(t.Context())
			require.NoError(t, err)
			assert.Equal(t, 2, calls)
			var old model.AgencyUsageFact
			require.NoError(t, db.First(&old, existing.ID).Error)
			assert.Equal(t, "old-fact", old.EventID)
			// A management change takes priority over a backlog of ordinary facts.
			require.NoError(t, model.CaptureAgencyOpsChangeTx(db, "source_price", agency))
			var priority model.AgencyOpsEvent
			require.NoError(t, db.Where("priority = ?", 1).First(&priority).Error)
			assert.Contains(t, priority.Payload, `"kind":"source_price"`)
			// Exercise MySQL's >64 KiB payload storage rather than assuming TEXT
			// has the same capacity as PostgreSQL and SQLite.
			large := model.AgencyUsageFact{EventID: "large", UserID: 301, ComponentID: "default", ModelKey: "model", OriginModelName: "gpt-4o", CumulativeUsage: strings.Repeat("x", 70000)}
			require.NoError(t, db.Create(&large).Error)
			var largeEvent model.AgencyOpsEvent
			require.NoError(t, db.Where("payload LIKE ?", "%large%").First(&largeEvent).Error)
			assert.Greater(t, len(largeEvent.Payload), 70000)
			// A rejected batch must isolate its members even on the last retry,
			// not dead-letter valid neighbours without trying them individually.
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Where("status = ?", "queued").Updates(map[string]any{"attempts": 7}).Error)
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var batch struct {
					Events []map[string]any `json:"events"`
				}
				require.NoError(t, common.DecodeJson(r.Body, &batch))
				if len(batch.Events) > 1 || batch.Events[0]["kind"] == "source_price" {
					w.WriteHeader(400)
					return
				}
				body, err := common.Marshal(map[string]any{"data": map[string]any{"accepted": []any{batch.Events[0]["eventId"]}, "durable": true}})
				require.NoError(t, err)
				_, _ = w.Write(body)
			})
			now = now.Add(10 * time.Second)
			_, err = sender.Step(t.Context())
			require.NoError(t, err)
			require.NoError(t, db.First(&priority, priority.ID).Error)
			assert.Equal(t, "retry", priority.Status)
			now = now.Add(10 * time.Minute)
			for range 2 {
				_, err = sender.Step(t.Context())
				require.NoError(t, err)
			}
			require.NoError(t, db.First(&priority, priority.ID).Error)
			require.NoError(t, db.First(&largeEvent, largeEvent.ID).Error)
			assert.Equal(t, "dead", priority.Status)
			assert.Equal(t, "delivered", largeEvent.Status)
			// Exercise real local recharge/reservation/refund writers with event
			// capture enabled, including the conditional empty-model updates.
			t.Setenv("OPS_DISTRIBUTOR_EVENTS_ENABLED", "false")
			require.NoError(t, db.AutoMigrate(&model.User{}))
			require.NoError(t, model.MigrateAgency(db))
			require.NoError(t, model.MigrateAgency(db))
			t.Setenv("OPS_DISTRIBUTOR_EVENTS_ENABLED", "true")
			user := model.User{Username: "ops-event-funding-" + dialect, Password: "fixture-password-not-used",
				AffCode: "ops-funding-" + dialect, Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
				BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
			require.NoError(t, model.AgencyOpsTransaction(db, func(tx *gorm.DB) error {
				return model.RecordAgencyTopup(tx, int64(user.Id), "payment", "ops-funding-"+dialect, "payment_callback", 100, 0)
			}))
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Count(&count).Error)
			beforeReplay := count
			require.NoError(t, model.AgencyOpsTransaction(db, func(tx *gorm.DB) error {
				return model.RecordAgencyTopup(tx, int64(user.Id), "payment", "ops-funding-"+dialect, "payment_callback", 100, 0)
			}))
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Count(&count).Error)
			assert.Equal(t, beforeReplay, count, "idempotent recharge must not create another delivery")
			require.NoError(t, model.AgencyOpsTransaction(db, func(tx *gorm.DB) error {
				_, err := model.TryReserveUserQuotaAndAgencyTx(tx, user.Id, 50, "ops-charge-"+dialect, 50)
				return err
			}))
			require.NoError(t, model.AgencyOpsTransaction(db, func(tx *gorm.DB) error {
				_, _, err := model.ReverseAgencyTopupTx(tx, model.AgencyFundingReversalInput{
					RefundID: "ops-refund-" + dialect, SourceOperationID: "ops-funding-" + dialect, UserID: int64(user.Id), Quota: 100, CurrencyCode: "CNY"})
				return err
			}))
			var history []model.AgencyOpsEvent
			require.NoError(t, db.Where("id > ?", largeEvent.ID).Find(&history).Error)
			kinds := map[string]bool{}
			for _, event := range history {
				var payload map[string]any
				require.NoError(t, common.Unmarshal([]byte(event.Payload), &payload))
				kinds[payload["kind"].(string)] = true
			}
			for _, kind := range []string{"wallets", "funding", "fundingLots", "allocations", "topups", "reversals", "debts"} {
				assert.True(t, kinds[kind], "real workflow must publish %s", kind)
			}
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Count(&count).Error)
			beforeRollback := count
			err = model.AgencyOpsTransaction(db, func(tx *gorm.DB) error {
				if err := tx.Model(&model.AgencyFundingAccount{}).Where("user_id = ?", user.Id).Update("version", gorm.Expr("version + 1")).Error; err != nil {
					return err
				}
				return errors.New("fixture rollback")
			})
			require.Error(t, err)
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Count(&count).Error)
			assert.Equal(t, beforeRollback, count)
			// Losing the row lease while HTTP is in flight must make the old ACK
			// harmless. The next owner replays the same immutable event.
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Where("status IN ?", []string{"queued", "retry"}).
				Updates(map[string]any{"status": "delivered"}).Error)
			require.NoError(t, db.Model(&fact).Update("charged_quota", 85).Error)
			var leaseEvent model.AgencyOpsEvent
			require.NoError(t, db.Where("status = ?", "queued").First(&leaseEvent).Error)
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Where("id = ?", leaseEvent.ID).
					Updates(map[string]any{"lease_token": "replacement-owner", "lease_until": now.Add(15 * time.Second).UnixMilli()}).Error)
				body, err := common.Marshal(map[string]any{"data": map[string]any{"accepted": []string{leaseEvent.EventID}, "durable": true}})
				require.NoError(t, err)
				_, _ = w.Write(body)
			})
			_, err = sender.Step(t.Context())
			require.NoError(t, err)
			require.NoError(t, db.First(&leaseEvent, leaseEvent.ID).Error)
			assert.Equal(t, "leased", leaseEvent.Status)
			assert.Equal(t, "replacement-owner", leaseEvent.LeaseToken)
			now = now.Add(20 * time.Second)
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := common.Marshal(map[string]any{"data": map[string]any{"duplicates": []string{leaseEvent.EventID}, "durable": true}})
				require.NoError(t, err)
				_, _ = w.Write(body)
			})
			_, err = sender.Step(t.Context())
			require.NoError(t, err)
			require.NoError(t, db.First(&leaseEvent, leaseEvent.ID).Error)
			assert.Equal(t, "delivered", leaseEvent.Status)
			assert.Equal(t, 2, leaseEvent.Attempts)
			// Once reviewed, immutable events leave the nightly candidate range.
			// A later-committing smaller ID still remains independently eligible.
			reviewAt := time.Now().UnixMilli()
			require.NoError(t, db.Model(&model.AgencyOpsEvent{}).Where("id > ?", 0).Update("created_at_ms", reviewAt-1000).Error)
			page, err := model.ReadAgencyOpsReview(db, reviewAt)
			require.NoError(t, err)
			require.NotEmpty(t, page)
			acks := make([]model.AgencyOpsReview, 0, len(page))
			for _, event := range page {
				acks = append(acks, model.AgencyOpsReview{ID: event.ID, Hash: event.PayloadHash})
			}
			require.NoError(t, model.AcknowledgeAgencyOpsReview(db, acks, reviewAt))
			require.NoError(t, model.AcknowledgeAgencyOpsReview(db, acks, reviewAt))
			bad := []model.AgencyOpsReview{{ID: acks[0].ID, Hash: strings.Repeat("0", 64)}}
			require.Error(t, model.AcknowledgeAgencyOpsReview(db, bad, reviewAt))
			late := model.AgencyOpsEvent{ID: 8000002, EventID: "review-high", Payload: "{}", PayloadHash: strings.Repeat("a", 64), Status: "delivered", CreatedAtMS: reviewAt - 500}
			require.NoError(t, db.Create(&late).Error)
			require.NoError(t, model.AcknowledgeAgencyOpsReview(db, []model.AgencyOpsReview{{ID: late.ID, Hash: late.PayloadHash}}, reviewAt))
			late.ID, late.EventID = 8000001, "review-late-lower-id"
			require.NoError(t, db.Create(&late).Error)
			page, err = model.ReadAgencyOpsReview(db, reviewAt)
			require.NoError(t, err)
			require.Len(t, page, 1)
			assert.Equal(t, int64(8000001), page[0].ID)
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
			}
			t.Log(fmt.Sprintf("verified %s %s", dialect, version))
		})
	}
}
