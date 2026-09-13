package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise the real drivers, including PostgreSQL's production simple protocol.
// Losing this JSON also loses the accepted billing identity used by refunds.
func TestTaskBillingJSONPersistenceAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, db.AutoMigrate(&Task{}))
			tx := db.Begin()
			require.NoError(t, tx.Error)
			t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
			task := Task{TaskID: common.GetUUID(), Status: TaskStatusSuccess,
				Properties: Properties{Input: "模型测试", OriginModelName: "Model-A", UpstreamModelName: "model-a"},
				PrivateData: TaskPrivateData{TokenId: 17, BillingSource: TaskBillingSourceWallet,
					BillingContext: &TaskBillingContext{AgencyChargeID: "accepted-charge", AgencyBillingEventID: "accepted-event"}}}
			task.SetData(map[string]string{"status": "succeeded", "id": "provider-task"})
			require.NoError(t, tx.Create(&task).Error)
			var persisted Task
			require.NoError(t, tx.First(&persisted, task.ID).Error)
			assert.Equal(t, task.Properties, persisted.Properties)
			assert.Equal(t, task.PrivateData, persisted.PrivateData)
			assert.JSONEq(t, string(task.Data), string(persisted.Data))
			publicJSON, err := common.Marshal(persisted)
			require.NoError(t, err)
			assert.NotContains(t, string(publicJSON), "accepted-charge")
			assert.NotContains(t, string(publicJSON), "accepted-event")

			// A later payload with omitted fields cannot retain the prior task's
			// private billing identity when database scan destinations are reused.
			task.Properties = Properties{OriginModelName: "Model-B"}
			task.PrivateData = TaskPrivateData{UpstreamTaskID: "replacement-task"}
			require.NoError(t, tx.Save(&task).Error)
			require.NoError(t, tx.First(&persisted, task.ID).Error)
			assert.Equal(t, task.Properties, persisted.Properties)
			assert.Equal(t, task.PrivateData, persisted.PrivateData)
			task.Properties, task.PrivateData = Properties{}, TaskPrivateData{}
			require.NoError(t, tx.Save(&task).Error)
			var empty Task
			require.NoError(t, tx.First(&empty, task.ID).Error)
			assert.Empty(t, empty.Properties)
			assert.Empty(t, empty.PrivateData)
		})
	}
}
