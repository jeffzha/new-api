package model

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This fixture preserves the deployed schema before nullable active keys. A
// closed history row must never block a later repair of the same object.
type legacyAgencyReconciliationIssue struct {
	ID           int64  `gorm:"primaryKey"`
	ObjectType   string `gorm:"size:64;not null;uniqueIndex:uidx_agency_reconcile_open,priority:1"`
	ObjectID     string `gorm:"size:191;not null;uniqueIndex:uidx_agency_reconcile_open,priority:2"`
	Difference   string `gorm:"type:text;not null"`
	EvidenceHash string `gorm:"size:128"`
	Status       string `gorm:"size:32;not null;uniqueIndex:uidx_agency_reconcile_open,priority:3;index:idx_agency_reconcile_status"`
	Resolution   string `gorm:"type:text"`
	ActorID      *int64
	CreatedAtMS  int64 `gorm:"not null"`
	ResolvedAtMS *int64
}

func (legacyAgencyReconciliationIssue) TableName() string {
	return AgencyTablePrefix + "reconciliation_issues"
}

func TestAgencyReconciliationLegacyMigrationPreservesHistoryAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, db.Migrator().DropTable(&AgencyReconciliationIssue{}))
			require.NoError(t, db.AutoMigrate(&legacyAgencyReconciliationIssue{}))
			actorID, resolvedAt := int64(9007199254740993), int64(1789228800000)
			legacy := []legacyAgencyReconciliationIssue{
				{ObjectType: "billing_outbox", ObjectID: "迁移-精确大小写-A", Difference: "missing delivery", EvidenceHash: "open-original-evidence", Status: "open", CreatedAtMS: resolvedAt - 1000},
				{ObjectType: "billing_outbox", ObjectID: "迁移-精确大小写-A", Difference: "old missing delivery", EvidenceHash: "resolved-original-evidence", Status: "resolved", Resolution: "original repair explanation", ActorID: &actorID, CreatedAtMS: resolvedAt - 2000, ResolvedAtMS: &resolvedAt},
				{ObjectType: "billing_outbox", ObjectID: "迁移-精确大小写-A", Difference: "older discrepancy", EvidenceHash: "ignored-original-evidence", Status: "ignored", Resolution: "legacy ignored explanation", ActorID: &actorID, CreatedAtMS: resolvedAt - 3000, ResolvedAtMS: &resolvedAt},
			}
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, MigrateAgency(db))
			require.NoError(t, MigrateAgency(db), "migration must be restartable without rewriting evidence")

			var preserved []legacyAgencyReconciliationIssue
			require.NoError(t, db.Order("id ASC").Find(&preserved).Error)
			assert.Equal(t, legacy, preserved)
			var current []AgencyReconciliationIssue
			require.NoError(t, db.Order("id ASC").Find(&current).Error)
			require.Len(t, current, 3)
			digest := sha256.Sum256([]byte("billing_outbox\x00迁移-精确大小写-A"))
			activeKey := hex.EncodeToString(digest[:])
			require.NotNil(t, current[0].ActiveKey)
			assert.Equal(t, activeKey, *current[0].ActiveKey)
			for _, historical := range current[1:] {
				assert.Nil(t, historical.ActiveKey)
				assert.Empty(t, historical.ResolutionEvidence, "migration must not invent verification evidence")
				assert.Empty(t, historical.RepairEventID)
			}

			duplicate := AgencyReconciliationIssue{ObjectType: legacy[0].ObjectType, ObjectID: legacy[0].ObjectID, Difference: "duplicate detector", EvidenceHash: "duplicate-evidence", Status: "open", ActiveKey: &activeKey, CreatedAtMS: resolvedAt}
			require.Error(t, db.Create(&duplicate).Error, "one object can have only one active discrepancy")
			require.NoError(t, db.Model(&AgencyReconciliationIssue{}).Where("id = ?", legacy[0].ID).Updates(map[string]any{"status": "resolved", "active_key": nil, "resolution": "verified restored delivery", "resolution_evidence": `{"state":"consistent"}`, "repair_event_id": "verified-event", "resolved_at_ms": resolvedAt}).Error)
			duplicate.ID = 0
			require.NoError(t, db.Create(&duplicate).Error, "closed historical rows must not prevent recurrence")
			require.NoError(t, db.Model(&duplicate).Updates(map[string]any{"status": "ignored", "active_key": nil, "resolution": "verified consistent", "resolution_evidence": `{"state":"consistent"}`, "resolved_at_ms": resolvedAt}).Error)
			require.NoError(t, MigrateAgency(db))
			var final []AgencyReconciliationIssue
			require.NoError(t, db.Order("id ASC").Find(&final).Error)
			require.Len(t, final, 4)
			assert.Equal(t, "resolved", final[0].Status)
			assert.Equal(t, "resolved", final[1].Status)
			assert.Equal(t, "ignored", final[2].Status)
			assert.Equal(t, "ignored", final[3].Status)
			assert.Equal(t, "verified-event", final[0].RepairEventID)
			assert.Equal(t, `{"state":"consistent"}`, final[0].ResolutionEvidence)
			for _, historical := range final {
				assert.Nil(t, historical.ActiveKey)
			}
		})
	}
}
