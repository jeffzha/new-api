package agencyhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
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
