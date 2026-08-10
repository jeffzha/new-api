package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/metrics"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryRendersPortableDatabaseGaugesAndBoundedCounters(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	registry := metrics.New(db)

	const workers = 20
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			registry.ObserveHTTP("/api/internal/workbench/authz", http.StatusForbidden)
			registry.IncControlEventFailure("publish")
		}()
	}
	group.Wait()
	registry.ObserveHTTP("/api/admin/workbench/customers/1/app/verify", http.StatusOK)
	registry.ObserveHTTP("/api/admin/workbench/customers/1/apps/primary/verify", http.StatusBadGateway)
	registry.ObserveHTTP("/api/internal/workbench/identities/confirm", http.StatusOK)

	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	output := registry.Render(now)
	assert.Contains(t, output, "workbench_active_sessions 0")
	assert.Contains(t, output, `workbench_customer_apps{status="active"} 0`)
	assert.Contains(t, output, `workbench_control_event_lag_seconds{stage="publish_queue"} 0`)
	assert.Contains(t, output, "workbench_usage_audit_missing_apps 0")
	assert.Contains(t, output, `workbench_authz_denied_total{reason="denied"} 20`)
	assert.Contains(t, output, `workbench_control_event_failures_total{stage="publish"} 20`)
	assert.Contains(t, output, `workbench_app_verify_total{result="success"} 1`)
	assert.Contains(t, output, `workbench_app_verify_total{result="failure"} 1`)
	assert.Contains(t, output, `workbench_identity_bind_total{result="success"} 1`)
	assert.Contains(t, output, `workbench_upstream_cost_cny{confidence="app_exact"} 0`)
	assert.Contains(t, output, `workbench_estimated_margin_cny{confidence="unverified"} 0`)
	assert.NotContains(t, output, "customer_id")
	assert.NotContains(t, output, "user_id")

	customer := model.Customer{CustomerCode: "metrics-customer", DisplayName: "Metrics", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	require.NoError(t, db.Create(&model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", Selector: "metrics-app", Alias: "primary",
		ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "provider-app-metrics",
		DisplayName: "Metrics App", Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
		CreatedAt: now.Add(-48 * time.Hour),
	}).Error)
	output = registry.Render(now)
	assert.Contains(t, output, "workbench_usage_audit_missing_apps 1")
	assert.Contains(t, output, "workbench_usage_audit_stale_days 2.000000")
}

func TestMetricsHandlerExposesOnlyExactInternalPath(t *testing.T) {
	registry := metrics.New(nil)
	handler := registry.Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.True(t, strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain; version=0.0.4"))

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
		httptest.NewRequest(http.MethodPost, "/internal/metrics", nil),
	} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code)
	}
}
