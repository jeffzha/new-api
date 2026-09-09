package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminSPAHandlerServesRoutesWithoutFallingBackForMissingAssets(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(directory, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "index.html"), []byte("admin shell"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "assets", "app.js"), []byte("script"), 0o600))
	handler := adminSPAHandler(directory, "/workbench/admin")

	for _, target := range []string{"/workbench/admin", "/workbench/admin/customers/42"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, "admin shell", response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		assert.NotEmpty(t, response.Header().Get("Content-Security-Policy"))
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/workbench/admin/assets/app.js", nil))
	assert.Equal(t, http.StatusOK, asset.Code)
	assert.Equal(t, "script", asset.Body.String())
	assert.Contains(t, asset.Header().Get("Cache-Control"), "immutable")

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/workbench/admin/assets/missing.js", nil))
	assert.Equal(t, http.StatusNotFound, missing.Code)
}
