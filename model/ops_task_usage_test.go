package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpsTaskEvidenceMigrationAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("external database not configured")
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.Migrator().DropTable(&Task{}))
			require.NoError(t, db.AutoMigrate(&Task{}))
			// The released task schema has the same fields and private-data JSON,
			// but no Ops delivery flag. Existing JSON must survive the upgrade.
			require.NoError(t, db.Migrator().DropColumn(&Task{}, "OpsUsagePending"))
			legacy := &Task{TaskID: "legacy-ops-task", Status: TaskStatusSuccess,
				PrivateData: TaskPrivateData{UpstreamTaskID: "keep-existing-id"}, Quota: 37}
			require.NoError(t, db.Omit("OpsUsagePending").Create(legacy).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&Task{}))
			}
			var stored Task
			require.NoError(t, db.First(&stored, legacy.ID).Error)
			assert.Equal(t, 37, stored.Quota)
			assert.Equal(t, "keep-existing-id", stored.PrivateData.UpstreamTaskID)
			assert.False(t, stored.OpsUsagePending)
			stored.OpsUsagePending = true
			stored.PrivateData.OpsUsage = []byte(`{"event_id":"ops-task-legacy","usage":{"total_tokens":100}}`)
			result := db.Model(&stored).Where("status = ?", TaskStatusSuccess).Select("*").Updates(&stored)
			require.NoError(t, result.Error)
			assert.EqualValues(t, 1, result.RowsAffected)
			var pending []Task
			require.NoError(t, db.WithContext(context.Background()).Where("ops_usage_pending = ? AND status = ?", true, TaskStatusSuccess).
				Order("id ASC").Limit(100).Find(&pending).Error)
			require.Len(t, pending, 1)
			assert.JSONEq(t, string(stored.PrivateData.OpsUsage), string(pending[0].PrivateData.OpsUsage))
			require.NoError(t, db.Model(&Task{}).Where("id = ? AND ops_usage_pending = ?", stored.ID, true).Update("ops_usage_pending", false).Error)
			require.NoError(t, db.First(&stored, stored.ID).Error)
			assert.False(t, stored.OpsUsagePending)
			require.NoError(t, db.Migrator().DropTable(&Task{}))
			for range 2 {
				require.NoError(t, db.AutoMigrate(&Task{}))
			}
		})
	}
}
