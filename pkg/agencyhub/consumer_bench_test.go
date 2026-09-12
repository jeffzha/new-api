package agencyhub

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// BenchmarkAgencyConsumerProjectionPerDialect measures the sidecar billing
// consumer drain rate (events/s). It is the counterpart of the reserve-lane
// benchmarks: the 60 RPS design can only absorb a backlog as fast as the
// consumer projects deliveries into the commission/journal/daily projections.
//
// sqlite uses a fresh in-memory DB; mysql/postgres are enabled via the same
// AGENCY_HUB_RUN_EXTERNAL_DB_TESTS + *_DSN environment variables as the
// migration gates. A benchmark opening a real test database must be run with
// -run '^$' exactly as the reserve benchmarks.
func BenchmarkAgencyConsumerProjectionPerDialect(b *testing.B) {
	run := func(b *testing.B, dialect, dsn string) {
		app := consumerBenchApp(b, dialect, dsn)
		const batch = 1000
		const rounds = 5
		b.StopTimer()
		total := 0
		for r := 0; r < rounds; r++ {
			seedConsumerBatch(b, app, batch, r*batch)
			total += batch
		}
		b.ReportAllocs()
		b.StartTimer()
		start := time.Now()
		drained := drainConsumer(app, total)
		elapsed := time.Since(start)
		b.StopTimer()
		b.ReportMetric(float64(drained)/elapsed.Seconds(), "events/s")
		b.ReportMetric(elapsed.Seconds()*1000, "ms-drain")
	}

	b.Run("sqlite", func(b *testing.B) { run(b, "sqlite", "") })
	if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") == "1" {
		if m := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_MYSQL_DSN")); m != "" {
			b.Run("mysql", func(b *testing.B) { run(b, "mysql", m) })
		}
		if p := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_POSTGRES_DSN")); p != "" {
			b.Run("postgres", func(b *testing.B) { run(b, "postgres", p) })
		}
	}
}

func consumerBenchApp(b *testing.B, dialect, dsn string) *App {
	b.Helper()
	var (
		db  *gorm.DB
		err error
	)
	switch dialect {
	case "sqlite":
		db, err = gorm.Open(sqlite.Open("file:consumer-bench-"+strings.ReplaceAll(b.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	case "mysql":
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	case "postgres":
		db, err = gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	default:
		b.Fatalf("unknown dialect %q", dialect)
	}
	if err != nil {
		b.Fatalf("open %s: %v", dialect, err)
	}
	if err := model.MigrateAgency(db); err != nil {
		b.Fatalf("migrate %s: %v", dialect, err)
	}
	previousDB := model.DB
	model.DB = db
	b.Cleanup(func() { model.DB = previousDB })
	return New(db, db, Config{BasePath: "/agency", SessionIdle: time.Hour, SessionAbsolute: time.Hour})
}

func seedConsumerBatch(b *testing.B, app *App, total, base int) {
	b.Helper()
	now := time.Now().UnixMilli()
	nonce := time.Now().UnixNano()
	outboxes := make([]model.AgencyBillingOutbox, 0, total)
	deliveries := make([]model.AgencyEventDelivery, 0, total)
	for i := 0; i < total; i++ {
		agencyID := int64(9000 + (i % 8))
		event := agencycontract.BillingEvent{
			SchemaVersion:          agencycontract.SchemaVersion,
			EventID:                fmt.Sprintf("bench-consumer-%d-%d-%d", nonce, base, i),
			EventType:              "agency.billing_finalized",
			OperationID:            fmt.Sprintf("bench-oper-%d-%d-%d", nonce, base, i),
			UserID:                 int64(2_000_000_000 + i),
			AgencyID:               &agencyID,
			OriginModelName:        "gpt-4o",
			CurrencyCode:           "CNY",
			CommissionEligible:     true,
			CommissionAmountMicros: 15_000_000,
			OccurredAtMS:           now,
		}
		payload, err := common.Marshal(event)
		require.NoError(b, err)
		payloadHash, err := agencycontract.CanonicalHash(event)
		require.NoError(b, err)
		outboxes = append(outboxes, model.AgencyBillingOutbox{
			EventID:       event.EventID,
			OperationID:   event.OperationID,
			EventKind:     event.EventType,
			UserID:        event.UserID,
			MoneySeq:      int64(i),
			Payload:       string(payload),
			PayloadHash:   payloadHash,
			SchemaVersion: event.SchemaVersion,
			CreatedAtMS:   now,
		})
		deliveries = append(deliveries, model.AgencyEventDelivery{
			EventID:     event.EventID,
			Status:      "pending",
			NextRetryAt: 1,
			CreatedAt:   now / 1000,
		})
	}
	require.NoError(b, app.db.Create(&outboxes).Error)
	require.NoError(b, app.db.Create(&deliveries).Error)
}

func drainConsumer(app *App, want int) int {
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		_ = app.RunConsumerOnce(ctx, 100)
		var done int64
		if err := app.db.Model(&model.AgencyEventDelivery{}).Where("status = ?", "done").Count(&done).Error; err == nil && done >= int64(want) {
			return int(done)
		}
		if time.Now().After(deadline) {
			return int(done)
		}
	}
}
