package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAgencyComponentIdentityMigrationAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, MigrateAgency(db))
			prefix := "identity-" + common.GetUUID()[:12]
			usage := AgencyUsageFact{EventID: prefix, ComponentID: "default", UserID: 101, OriginModelName: "history-model", ModelKey: "history-key", ChargedQuota: 123, CurrencyCode: "CNY"}
			entry := AgencyCommissionLedger{EventID: prefix, ComponentID: "default", EntryType: "earned", AgencyID: 102, AmountMicros: 9007199254740993, CurrencyCode: "CNY"}
			// Simulate historical rows written before ComponentKey existed.
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&usage).Error)
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&entry).Error)
			require.NoError(t, MigrateAgency(db))
			require.NoError(t, MigrateAgency(db))
			var storedUsage AgencyUsageFact
			var storedEntry AgencyCommissionLedger
			require.NoError(t, db.First(&storedUsage, usage.ID).Error)
			require.NoError(t, db.First(&storedEntry, entry.ID).Error)
			usage.ComponentKey, entry.ComponentKey = AgencyComponentKey("default"), AgencyComponentKey("default")
			assert.Equal(t, usage, storedUsage, "migration preserves original usage values and identity")
			assert.Equal(t, entry, storedEntry, "migration must not reprice or truncate historical commission")
			for _, id := range []string{"Model", "model"} {
				row := AgencyCommissionLedger{EventID: prefix + "-new", ComponentID: id, EntryType: "earned", AgencyID: 102, AmountMicros: 7, CurrencyCode: "CNY"}
				require.NoError(t, db.Create(&row).Error)
				fact := AgencyUsageFact{EventID: prefix + "-new", ComponentID: id, UserID: 101, OriginModelName: "history-model", ModelKey: "history-key", ChargedQuota: 10}
				require.NoError(t, db.Create(&fact).Error)
			}
			duplicate := AgencyCommissionLedger{EventID: prefix + "-new", ComponentID: "model", EntryType: "earned", AgencyID: 102, AmountMicros: 7, CurrencyCode: "CNY"}
			assert.Error(t, db.Create(&duplicate).Error, "same exact component must still be unique")
		})
	}
}
