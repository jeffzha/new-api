package agencyhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAgencyHealthAndReadinessProbes(t *testing.T) {
	app := newAgencyTestApp(t)
	router := app.Router()

	liveness := httptest.NewRecorder()
	router.ServeHTTP(liveness, httptest.NewRequest(http.MethodGet, "/agency/livez", nil))
	require.Equal(t, http.StatusOK, liveness.Code)

	notReady := httptest.NewRecorder()
	router.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, notReady.Code)

	app.SetReady(true)
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
	require.Equal(t, http.StatusOK, ready.Code)
	var response struct {
		Status       string         `json:"status"`
		Ready        bool           `json:"ready"`
		Schema       map[string]any `json:"schema"`
		Capabilities map[string]any `json:"capabilities"`
		Backlog      map[string]any `json:"backlog"`
	}
	require.NoError(t, common.Unmarshal(ready.Body.Bytes(), &response))
	require.Equal(t, "ok", response.Status)
	require.True(t, response.Ready)
	require.NotEmpty(t, response.Schema)
	require.NotEmpty(t, response.Capabilities)
	require.NotEmpty(t, response.Backlog)
	require.Equal(t, true, response.Schema["ready"])
	require.Equal(t, true, response.Capabilities["agency_durable_v1"])
}

func TestAgencyOperationalStatusIncludesBacklog(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: "pending-event", Status: "pending", NextRetryAt: 1, CreatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: "retry-event", Status: "retry", NextRetryAt: 1, CreatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyReconciliationIssue{ObjectType: "journal", ObjectID: "charge-1", Difference: "balance mismatch", EvidenceHash: "issue-1", Status: "open", CreatedAtMS: 1}).Error)

	schema, err := app.agencySchemaStatus()
	require.NoError(t, err)
	require.Equal(t, true, schema["ready"])
	backlog, err := app.agencyBacklogStatus(context.Background())
	require.NoError(t, err)
	deliveries, ok := backlog["deliveries"].(gin.H)
	require.True(t, ok)
	require.Equal(t, int64(1), deliveries["pending"])
	require.Equal(t, int64(1), deliveries["retry"])
	require.Equal(t, int64(1), backlog["open_reconciliation_issues"])
}

func TestAgencyReadinessProjectionTimestampAcrossDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open("file:readiness-projection?mode=memory&cache=shared")
			if dialect != "sqlite" {
				dsn := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN"))
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("disposable external database not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, model.MigrateAgency(db))
			require.NoError(t, db.Where("1 = 1").Delete(&model.AgencySourceEvent{}).Error)
			app := New(db, db, Config{})
			backlog, err := app.agencyBacklogStatus(context.Background())
			require.NoError(t, err)
			require.Equal(t, int64(0), backlog["last_projected_at_ms"])
			// IDs and timestamps need not have the same order. Unfinished newer
			// events must not move the completed projection watermark forward.
			for i, item := range []struct {
				status string
				time   int64
			}{{"done", 8000}, {"done", 2000}, {"facts_done", 9000}, {"skipped", 7000}, {"pending", 99000}} {
				require.NoError(t, db.Create(&model.AgencySourceEvent{
					EventID: item.status + string(rune('a'+i)), SourceOperationID: "readiness",
					SchemaVersion: "1", PayloadHash: "test", UserID: 7,
					ProcessingStatus: item.status, Payload: "{}", CreatedAtMS: item.time,
				}).Error)
			}
			backlog, err = app.agencyBacklogStatus(context.Background())
			require.NoError(t, err)
			require.Equal(t, int64(9000), backlog["last_projected_at_ms"])
			require.NoError(t, db.Model(&model.AgencySourceEvent{}).Where("processing_status = ?", "pending").Update("processing_status", "done").Error)
			backlog, err = app.agencyBacklogStatus(context.Background())
			require.NoError(t, err)
			require.Equal(t, int64(99000), backlog["last_projected_at_ms"])
		})
	}
}
