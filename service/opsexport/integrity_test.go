package opsexport

import (
	"context"
	"crypto/sha256"
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
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestIntegrityImmutableReadonlyHTTP(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:integrity-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, model.MigrateCNFinance(db, "integrity-http"))
	epoch, err := model.PrepareCNIntegrityCapture(db, "local-http-test-operator")
	require.NoError(t, err)
	now := time.Now().UTC()
	sealed, err := model.FreezeCNIntegrity(context.Background(), db, model.CNIntegrityFreezeRequest{EpochID: epoch.EpochID, ExpectedGeneration: 1, CoverageFrom: now.Add(-time.Hour).Format(model.CNTimeLayout), CoverageTo: now.Add(time.Hour).Format(model.CNTimeLayout)})
	require.NoError(t, err)
	token := strings.Repeat("readonly-export-", 3)
	previous := Default
	Default = &Server{DB: db, GatewayID: "integrity-http", TokenHash: sha256.Sum256([]byte(token))}
	t.Cleanup(func() { Default = previous })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterV2(router.Group("/api/ops-export/v2"))
	call := func(method, path, credential string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/api/ops-export/v2/funding-integrity"+path, nil)
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	manifestPath := "/snapshots/" + sealed.ProofID
	for _, path := range []string{"/snapshots", manifestPath, manifestPath + "/valueStates?ordinal=0&limit=1"} {
		response := call("GET", path, token)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		require.Equal(t, 401, call("GET", path, strings.Repeat("other-credential", 3)).Code)
		canonical, err := model.CNIntegrityCanonicalJSON(response.Body.Bytes())
		require.NoError(t, err)
		require.Equal(t, string(canonical), response.Body.String())
	}
	require.Equal(t, sealed.Manifest, call("GET", manifestPath, token).Body.String())
	for _, path := range []string{"/snapshots?after=00", "/snapshots?after=-1", "/snapshots?after=0&after=1", "/snapshots?foo=1", "/snapshots?limit=0", manifestPath + "?scan=true", manifestPath + "/valueStates?ordinal=1", manifestPath + "/unknown"} {
		require.Equal(t, 400, call("GET", path, token).Code, path)
	}
	require.Equal(t, 409, call("GET", "/snapshots?after=2", token).Code)
	require.Equal(t, 404, call("GET", "/snapshots/sha256:"+strings.Repeat("b", 64), token).Code)
	require.Equal(t, 404, call("POST", "/snapshots", token).Code)
	var count int64
	require.NoError(t, db.Model(&model.CNIntegritySnapshot{}).Count(&count).Error)
	require.EqualValues(t, 1, count, "GET never freezes current state")
	var index map[string]any
	require.NoError(t, common.Unmarshal(call("GET", "/snapshots", token).Body.Bytes(), &index))
	require.Equal(t, "1", index["observedThrough"])
	t.Run("nested_raw_messages_are_canonical_on_the_wire", func(t *testing.T) {
		body, err := common.Marshal(model.CNFact{FactID: "reconstructed-regression-only", Basis: "native"})
		require.NoError(t, err)
		payload := map[string]any{"rows": []map[string]any{{"body": common.RawMessage(body)}}}
		legacy, err := common.Marshal(payload)
		require.NoError(t, err)
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		integrityJSON(ctx, payload)
		canonical, err := model.CNIntegrityCanonicalJSON(legacy)
		require.NoError(t, err)
		require.NotEqual(t, string(legacy), string(canonical))
		require.Equal(t, string(canonical), response.Body.String())
		t.Log("reconstructed regression: old nested CNFact/RawMessage bytes differ from canonical HTTP bytes; original failed pages were not retained")
		if os.Getenv("CN_INTEGRITY_WRITE_EVIDENCE") == "1" {
			dir := filepath.Join("..", "..", "model", "testdata", "cn-task", "integrity-v2-noncanonical-regression")
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "reconstructed-legacy.json"), legacy, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "reconstructed-fixed.json"), response.Body.Bytes(), 0600))
		}
	})
}
