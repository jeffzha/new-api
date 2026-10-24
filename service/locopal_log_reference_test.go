package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestLocoPalSettlementIDSearchAcrossDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open("file:locopal-reference?mode=memory&cache=shared")
			case "mysql":
				dsn := os.Getenv("LOCOPAL_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("isolated MySQL test DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("LOCOPAL_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("isolated PostgreSQL test DSN not configured")
				}
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			oldDB, oldLogDB := model.DB, model.LOG_DB
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB = db
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB; common.SetDatabaseTypes(oldMain, oldLog) })
			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)
			models := []any{&model.UpstreamLogReference{}, &model.Task{}, &model.Channel{}, &model.Log{}}
			require.NoError(t, db.Migrator().DropTable(models...))
			// Verify fresh schema creation, then upgrade existing released tasks/logs.
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.UpstreamLogReference{}))
			}
			require.NoError(t, db.Migrator().DropTable(&model.UpstreamLogReference{}))
			require.NoError(t, db.AutoMigrate(&model.Task{}, &model.Channel{}))
			// Exercise a separately configured log connection on every engine.
			logs, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			logSQL, err := logs.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, logSQL.Close()) })
			model.LOG_DB = logs
			require.NoError(t, logs.AutoMigrate(&model.Log{}))
			const local = "202610240616276865339308268d9d6gU95AmvT"
			const submit = "202610240616277142277758268d9d6qvkE5OSR"
			const settlement = "202610240625577800978368268d9d6RLbrxIxx"
			const upstreamTask = "task_YEso6UPjs4tIsaPSD9zG7qSnZR4kanGt"
			task := &model.Task{TaskID: "task_public", Platform: constant.TaskPlatform(model.LocoPalTaskPlatform), ChannelId: 40, UserId: 14, Quota: 313938, PrivateData: model.TaskPrivateData{UpstreamTaskID: upstreamTask, Execution: &model.TaskExecutionSnapshot{RequestID: local}}}
			require.NoError(t, db.Create(task).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 40, Name: "LocoPal Video", Key: "test-only"}).Error)
			require.NoError(t, logs.Create(&model.Log{Id: 323579, UserId: 14, ChannelId: 40, RequestId: local, UpstreamRequestId: submit, Type: model.LogTypeConsume, CreatedAt: 100, Quota: 313938, ModelName: "MiniMax-H3"}).Error)
			// Upgrade must preserve the task/log; repeated startup is idempotent.
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.UpstreamLogReference{}))
			}
			cursor, err := model.IndexLocoPalTaskReferences(context.Background(), 0)
			require.NoError(t, err)
			assert.Equal(t, task.ID, cursor)
			for range 2 {
				linked, err := model.LinkUpstreamSettlementRequest(context.Background(), 40, upstreamTask, settlement)
				require.NoError(t, err)
				assert.True(t, linked)
			}
			for _, id := range []string{submit, settlement, " " + settlement + " "} {
				rows, total, err := model.GetAllLogs(0, 0, 0, "", "", "", 0, 20, 0, "", "", id)
				require.NoError(t, err)
				require.EqualValues(t, 1, total)
				require.Len(t, rows, 1)
				assert.Equal(t, submit, rows[0].UpstreamRequestId)
				assert.Equal(t, 313938, rows[0].Quota)
				rows, total, err = model.GetUserLogs(14, 0, 0, 0, "", "", 0, 20, "", "", id)
				require.NoError(t, err)
				assert.EqualValues(t, 1, total)
				assert.Len(t, rows, 1)
				rows, _, _, err = model.GetConsumeLogsForExport(model.LogExportParams{UserID: 14, UpstreamRequestID: id})
				require.NoError(t, err)
				assert.Len(t, rows, 1)
			}
			_, total, err := model.GetUserLogs(15, 0, 0, 0, "", "", 0, 20, "", "", settlement)
			require.NoError(t, err)
			assert.Zero(t, total)
			_, total, err = model.GetAllLogs(0, 101, 0, "", "", "", 0, 20, 0, "", "", settlement)
			require.NoError(t, err)
			assert.Zero(t, total, "aliases must not bypass time filters")
			_, total, err = model.GetAllLogs(0, 0, 0, "", "", "", 0, 20, 41, "", "", settlement)
			require.NoError(t, err)
			assert.Zero(t, total)
			for _, id := range []string{"unknown", "' OR 1=1 --"} {
				_, total, err = model.GetAllLogs(0, 0, 0, "", "", "", 0, 20, 0, "", "", id)
				require.NoError(t, err)
				assert.Zero(t, total)
			}
			linked, err := model.LinkUpstreamSettlementRequest(context.Background(), 41, upstreamTask, settlement)
			require.NoError(t, err)
			assert.False(t, linked, "a different channel cannot adopt the task")
			// Conflicting ownership must not rewrite an existing mapping.
			task.ID = 0
			task.UserId = 15
			task.TaskID = "task_conflict"
			require.NoError(t, db.Create(task).Error)
			_, err = model.IndexLocoPalTaskReferences(context.Background(), cursor)
			require.ErrorContains(t, err, "conflicting upstream log reference")
			var stored model.Log
			require.NoError(t, logs.First(&stored, 323579).Error)
			assert.Equal(t, submit, stored.UpstreamRequestId)
			assert.Equal(t, 313938, stored.Quota)
			// Heterogeneous databases must not require cross-database SQL joins.
			separate, err := gorm.Open(sqlite.Open("file:locopal-separate-"+dialect+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			separateSQL, err := separate.DB()
			require.NoError(t, err)
			defer separateSQL.Close()
			require.NoError(t, separate.AutoMigrate(&model.Log{}))
			require.NoError(t, separate.Create(&stored).Error)
			model.LOG_DB = separate
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseTypeSQLite)
			rows, total, err := model.GetUserLogs(14, 0, 0, 0, "", "", 0, 20, "", "", settlement)
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, rows, 1)
			assert.Equal(t, 313938, rows[0].Quota)
		})
	}
}

func TestLocoPalLogSyncResumesAfterRestart(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.UpstreamLogReference{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("type = ?", locoPalLogSyncTaskType).Delete(&model.SystemTask{}).Error)
		require.NoError(t, model.DB.Exec("DELETE FROM upstream_log_references").Error)
	})
	task := &model.Task{TaskID: "task_restart", Platform: constant.TaskPlatform(model.LocoPalTaskPlatform), ChannelId: 40, UserId: 14, PrivateData: model.TaskPrivateData{UpstreamTaskID: "provider_restart", Execution: &model.TaskExecutionSnapshot{RequestID: "local_restart"}}}
	require.NoError(t, model.DB.Create(task).Error)
	for range 2 {
		scheduled, err := model.CreateSystemTask(locoPalLogSyncTaskType, nil, nil)
		require.NoError(t, err)
		claimed, ok, err := model.ClaimSystemTask(scheduled.ID, locoPalLogSyncTaskType, "test-runner", time.Now().Unix()+60)
		require.NoError(t, err)
		require.True(t, ok)
		locoPalLogSyncHandler{}.Run(context.Background(), claimed, "test-runner")
		finished, err := model.GetSystemTaskByTaskID(scheduled.TaskID)
		require.NoError(t, err)
		require.Equal(t, model.SystemTaskStatusSucceeded, finished.Status)
		var result locoPalLogSyncResult
		require.NoError(t, common.UnmarshalJsonStr(finished.Result, &result))
		assert.Equal(t, task.ID, result.TaskCursor)
	}
	linked, err := model.LinkUpstreamSettlementRequest(context.Background(), 40, "provider_restart", "billing_after_restart")
	require.NoError(t, err)
	assert.True(t, linked)
}

func TestFetchLocoPalSettlementReferences(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.UpstreamLogReference{}))
	t.Cleanup(func() { require.NoError(t, model.DB.Exec("DELETE FROM upstream_log_references").Error) })
	task := &model.Task{TaskID: "task_sync", Platform: constant.TaskPlatform(model.LocoPalTaskPlatform), ChannelId: 40, UserId: 14, PrivateData: model.TaskPrivateData{UpstreamTaskID: "task_provider", Execution: &model.TaskExecutionSnapshot{RequestID: "local-sync"}}}
	require.NoError(t, model.DB.Create(task).Error)
	_, err := model.IndexLocoPalTaskReferences(context.Background(), 0)
	require.NoError(t, err)
	const other = `{"task_id":"task_provider","content_parts":"private-content-must-not-persist"}`
	quotedOther, err := common.Marshal(other)
	require.NoError(t, err)
	for _, encodedOther := range []string{other, string(quotedOther)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer test-only", r.Header.Get("Authorization"))
			fmt.Fprintf(w, `{"success":true,"data":[{"request_id":"billing-sync","type":2,"other":%s},{"request_id":"unknown-task","type":2,"other":{"task_id":"foreign"}}]}`, encodedOther)
		}))
		count, err := fetchLocoPalLogReferences(context.Background(), server.Client(), server.URL, "test-only", 40)
		server.Close()
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	}
	for _, response := range []string{`<html>private-content</html>`, `{"success":false}`, `{"success":true,"data":{}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
		_, err := fetchLocoPalLogReferences(context.Background(), server.Client(), server.URL, "test-only", 40)
		server.Close()
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private-content")
	}
	t.Run("redirect does not leak credentials", func(t *testing.T) {
		called := false
		destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
		defer destination.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
		defer server.Close()
		_, err := fetchLocoPalLogReferences(context.Background(), server.Client(), server.URL, "test-only", 40)
		require.ErrorContains(t, err, "HTTP 302")
		assert.False(t, called)
	})
	var refs []model.UpstreamLogReference
	require.NoError(t, model.DB.Find(&refs).Error)
	assert.Len(t, refs, 2)
	encoded, err := common.Marshal(refs)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(encoded), "private-content"))
}
