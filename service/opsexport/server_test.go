package opsexport

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOpsExportCredentialAndCursorBoundary(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	Default = nil
	db, err := gorm.Open(sqlite.Open("file:ops-export-http?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyBillingOutbox{}, &model.AgencyBillingOperation{}))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/facts", nil))
	assert.Equal(t, 404, w.Code)
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", "invalid")
	require.Error(t, StartConfigured(db))
	token := strings.Repeat("fixture-", 8)
	digest := sha256.Sum256([]byte(token))
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", fmt.Sprintf("%x", digest))
	t.Setenv("OPS_GATEWAY_ID", "g1")
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "g2")
	require.Error(t, StartConfigured(db))
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "g1")
	require.NoError(t, StartConfigured(db))
	for _, tc := range []struct {
		path, credential string
		status           int
	}{
		{"/facts", "", 401}, {"/facts", "Bearer management-token", 401},
		{"/facts", "Bearer " + token + "wrong", 401}, {"/facts", "Bearer " + token, 200},
		{"/facts?limit=1001", "Bearer " + token, 400}, {"/facts?limit=0", "Bearer " + token, 400},
		{"/facts?limit=1&limit=2", "Bearer " + token, 400}, {"/facts?after=bad", "Bearer " + token, 400},
		{"/facts?after=djI6LTE", "Bearer " + token, 400}, {"/daily?date=2026-02-30", "Bearer " + token, 400},
		{"/daily?date=2026-10-14", "Bearer " + token, 200},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1"+tc.path, nil)
		request.Header.Set("Authorization", tc.credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		assert.Equal(t, tc.status, w.Code, tc.path)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), token)
		if tc.path == "/facts" && tc.status == 200 {
			var result map[string]any
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
			assert.Equal(t, "djI6MA", result["nextCursor"])
			assert.Equal(t, "g1", result["gatewayId"])
		}
	}
	w = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ops-export/v1/facts", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, request)
	assert.Equal(t, 404, w.Code)
}
