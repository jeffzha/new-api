package opsexport

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	RegisterV2(router.Group("/api/ops-export/v2"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/facts", nil))
	assert.Equal(t, 404, w.Code)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ops-export/v2/facts", nil))
	assert.Equal(t, 404, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"source_unavailable"`)
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
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.User{}, &model.Token{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "inventory", DisplayName: "Inventory user", Password: "s4-password-never-export", Email: "s4-email-never-export@example.test"}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "s4-token-secret-never-export", Name: "Inventory token"}).Error)
	require.NoError(t, db.Create(&model.Model{ModelName: "s4-hidden", Endpoints: `["openai"]`}).Error)
	require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "s4-hidden").Update("status", 0).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 1, Models: "s4-disabled", Status: 2, Key: "s4-channel-secret-never-export"}).Error)
	var fixture struct {
		Items []model.CNFact `json:"items"`
	}
	fixtureJSON, err := os.ReadFile(filepath.Join("..", "..", "model", "testdata", "cn-contract", "finance-v2-facts.json"))
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(fixtureJSON, &fixture))
	for _, fact := range fixture.Items {
		require.NoError(t, model.StageCNFactTx(db, fact))
	}
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
		{"/catalog", "Bearer " + token, 200},
		{"/inventory", "", 401}, {"/inventory", "Bearer management-token", 401},
		{"/inventory", "Bearer " + token, 200}, {"/inventory?limit=1", "Bearer " + token, 400},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1"+tc.path, nil)
		request.Header.Set("Authorization", tc.credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		assert.Equal(t, tc.status, w.Code, tc.path)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), token)
		assert.NotContains(t, w.Body.String(), "s4-channel-secret-never-export")
		assert.NotContains(t, w.Body.String(), "s4-password-never-export")
		assert.NotContains(t, w.Body.String(), "s4-email-never-export")
		assert.NotContains(t, w.Body.String(), "s4-token-secret-never-export")
		if directory := os.Getenv("S4_TEST_ARTIFACT_DIR"); directory != "" && (tc.path == "/catalog" || tc.path == "/inventory") && tc.status == 200 {
			require.NoError(t, os.MkdirAll(directory, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, strings.TrimPrefix(tc.path, "/")+".json"), w.Body.Bytes(), 0600))
		}
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
	for _, tc := range []struct {
		path, credential, code string
		status                 int
	}{
		{"/facts", "Bearer management-token", "unauthorized", 401},
		{"/facts?after=djI6MA", "Bearer " + token, "invalid_cursor", 400},
		{"/facts?limit=1&limit=2", "Bearer " + token, "invalid_payload", 400},
		{"/facts?extra=1", "Bearer " + token, "invalid_payload", 400},
		{"/daily?date=2026-02-30", "Bearer " + token, "invalid_payload", 400},
		{"/facts", "Bearer " + token, "", 200},
		{"/refund-capabilities", "Bearer " + token, "", 200},
		{"/daily?date=2026-10-14", "Bearer " + token, "", 200},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v2"+tc.path, nil)
		request.Header.Set("Authorization", tc.credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		require.Equal(t, tc.status, w.Code, w.Body.String())
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), token)
		var body map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &body))
		if tc.code != "" {
			failure := body["error"].(map[string]any)
			assert.Equal(t, tc.code, failure["code"])
			assert.Equal(t, false, failure["retryable"])
			assert.Nil(t, failure["operationId"])
		} else if tc.path == "/refund-capabilities" {
			assert.Equal(t, false, body["reservationAvailable"])
			assert.Equal(t, false, body["executionAvailable"])
			assert.Equal(t, false, body["manualVoucherEvidenceAvailable"])
		} else {
			assert.Equal(t, model.CNFinanceSource, body["sourceSystem"])
			if dependencies, ok := body["dependencies"].([]any); ok {
				for _, value := range dependencies {
					dependency := value.(map[string]any)
					from, to := dependency["coverageFrom"].(string), dependency["coverageTo"].(string)
					for _, timestamp := range []string{from, to} {
						parsed, err := time.Parse(model.CNTimeLayout, timestamp)
						require.NoError(t, err)
						assert.Equal(t, timestamp, parsed.UTC().Format(model.CNTimeLayout))
					}
					if from == to {
						assert.NotEqual(t, "complete", dependency["status"])
					}
				}
			}
		}
		if directory := os.Getenv("S4_TEST_ARTIFACT_DIR"); directory != "" && tc.status == 200 {
			require.NoError(t, os.MkdirAll(directory, 0700))
			name := strings.Split(strings.TrimPrefix(tc.path, "/"), "?")[0]
			require.NoError(t, os.WriteFile(filepath.Join(directory, name+".json"), w.Body.Bytes(), 0600))
		}
	}
}
