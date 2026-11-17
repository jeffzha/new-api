package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func opsExportReceipt(t *testing.T, db *gorm.DB, id int64, charge string, revision, quota int64, kind string) {
	t.Helper()
	event := agencycontract.BillingEvent{EventID: fmt.Sprintf("%s-%d", charge, revision), OperationID: fmt.Sprintf("op-%s-%d", charge, revision),
		FinancialChargeID: charge, SegmentNo: 0, JournalRevision: revision, UserID: 51,
		EventType: kind, OccurredAtMS: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC).UnixMilli(),
		CurrencyCode: "USD", QuotaPerUnit: "500000", ExchangeRate: "1", ChargedTotalQuota: quota, FinancialFinal: true}
	encoded, err := common.Marshal(event)
	require.NoError(t, err)
	digest, err := agencycontract.CanonicalHash(&event)
	require.NoError(t, err)
	require.NoError(t, db.Create(&AgencyBillingOperation{ChargeID: charge, Revision: revision, Operation: kind,
		OperationID: event.OperationID, CommittedResult: string(encoded), InputHash: digest, CreatedAtMS: event.OccurredAtMS}).Error)
	require.NoError(t, db.Create(&AgencyBillingOutbox{ID: id, EventID: event.EventID, OperationID: event.OperationID, EventKind: kind,
		UserID: event.UserID, EventCount: 1, Payload: string(encoded), PayloadHash: digest, SchemaVersion: "fixture", CreatedAtMS: event.OccurredAtMS}).Error)
}

func TestOpsExportAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("external database not configured")
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			models := []any{&OpsExportFact{}, &OpsExportPublisher{}, &AgencyBillingOutbox{}, &AgencyBillingOperation{}}
			require.NoError(t, db.Migrator().DropTable(models...))
			// The existing released source schema and data precede our migration.
			require.NoError(t, db.AutoMigrate(&AgencyBillingOutbox{}, &AgencyBillingOperation{}))
			opsExportReceipt(t, db, 100, "charge-a", 2, 500000, "agency.billing_finalized")
			for range 2 {
				require.NoError(t, MigrateOpsExport(db))
			}
			ctx := context.Background()
			items, cursor, more, err := ReadOpsFacts(ctx, db, 0, 1)
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.Equal(t, "1", items[0]["amount"])
			assert.Equal(t, float64(500000), items[0]["quotaDelta"])
			assert.False(t, more)
			assert.NotContains(t, items[0], "recognizedAmount")
			assert.NotContains(t, items[0], "billing_basis")
			_, _, _, err = ReadOpsFacts(ctx, db, cursor+100, 10)
			assert.ErrorIs(t, err, ErrOpsExportCursor)
			// An older source ID becoming visible later must never be skipped.
			opsExportReceipt(t, db, 5, "charge-b", 1, 250000, "agency.billing_finalized")
			opsExportReceipt(t, db, 101, "charge-a", 3, 100000, "agency.billing_reversed")
			opsExportReceipt(t, db, 102, "charge-a", 4, 50000, "agency.billing_reversed")
			items, next, more, err := ReadOpsFacts(ctx, db, cursor, 10)
			require.NoError(t, err)
			require.Len(t, items, 3)
			assert.Greater(t, next, cursor)
			assert.False(t, more)
			assert.Equal(t, "0.8", items[1]["amount"])
			assert.Equal(t, "0.7", items[2]["amount"])
			assert.Equal(t, float64(-100000), items[1]["quotaDelta"])
			assert.Equal(t, float64(-50000), items[2]["quotaDelta"])
			daily, err := ReadOpsDaily(ctx, db, "2026-10-14")
			require.NoError(t, err)
			total := daily["totals"].(map[string]any)["charge"].(map[string]any)
			assert.Equal(t, int64(4), total["count"])
			assert.Equal(t, int64(600000), total["quotaDelta"])
			assert.Equal(t, "3", total["amountByCurrency"].(map[string]string)["USD"])
			assert.Equal(t, false, daily["sourceCoverageComplete"])
			assert.Equal(t, true, daily["backfillComplete"])
			// Restart migration preserves publication IDs, data and dedup indexes.
			require.NoError(t, MigrateOpsExport(db))
			again, err := ReadOpsDaily(ctx, db, "2026-10-14")
			require.NoError(t, err)
			assert.Equal(t, daily, again)
			var row OpsExportFact
			require.NoError(t, db.First(&row).Error)
			row.ID = 0
			assert.Error(t, db.Create(&row).Error)
			// Two platform processes publishing the same committed source serialize.
			opsExportReceipt(t, db, 103, "charge-c", 1, 100, "agency.billing_finalized")
			if dialect != "sqlite" {
				var wg sync.WaitGroup
				errs := make([]error, 2)
				for i := range 2 {
					wg.Go(func() { errs[i] = PublishOpsFacts(ctx, db) })
				}
				wg.Wait()
				for _, err := range errs {
					require.NoError(t, err)
				}
			} else {
				require.NoError(t, PublishOpsFacts(ctx, db))
			}
			items, next, more, err = ReadOpsFacts(ctx, db, next, 10)
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.False(t, more)
			// Corrections carry a cumulative amount and only the signed quota change.
			opsExportReceipt(t, db, 104, "charge-c", 2, 200, "agency.billing_corrected")
			corrected, correctedCursor, _, err := ReadOpsFacts(ctx, db, next, 10)
			require.NoError(t, err)
			require.Len(t, corrected, 1)
			assert.Equal(t, float64(100), corrected[0]["quotaDelta"])
			assert.Equal(t, "0.0004", corrected[0]["amount"])
			// Unknown denomination stays explicitly unknown, never invented cash.
			opsExportReceipt(t, db, 105, "charge-unknown", 1, 100, "agency.billing_finalized")
			var unknownSource AgencyBillingOutbox
			require.NoError(t, db.First(&unknownSource, 105).Error)
			var unknownEvent agencycontract.BillingEvent
			require.NoError(t, common.UnmarshalJsonStr(unknownSource.Payload, &unknownEvent))
			unknownEvent.ExchangeRate = ""
			unknownJSON, err := common.Marshal(unknownEvent)
			require.NoError(t, err)
			unknownHash, err := agencycontract.CanonicalHash(&unknownEvent)
			require.NoError(t, err)
			require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("id = ?", 105).Updates(map[string]any{"payload": string(unknownJSON), "payload_hash": unknownHash}).Error)
			require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("operation_id = ?", unknownEvent.OperationID).Update("committed_result", string(unknownJSON)).Error)
			unknownItems, _, _, err := ReadOpsFacts(ctx, db, correctedCursor, 10)
			require.NoError(t, err)
			require.Len(t, unknownItems, 1)
			assert.NotContains(t, unknownItems[0], "amount")
			// A corrupt immutable receipt cannot silently advance the cursor.
			opsExportReceipt(t, db, 106, "charge-b", 2, 300000, "agency.billing_reversed")
			assert.Error(t, PublishOpsFacts(ctx, db))
			var count int64
			require.NoError(t, db.Model(&OpsExportFact{}).Count(&count).Error)
			assert.Equal(t, int64(7), count)
			// A fresh empty install also survives repeat migration.
			require.NoError(t, db.Migrator().DropTable(models...))
			for range 2 {
				require.NoError(t, MigrateOpsExport(db))
			}
			assert.False(t, db.Migrator().HasTable(&AgencyBillingOutbox{}), "export must not create financial source tables")
		})
	}
}
