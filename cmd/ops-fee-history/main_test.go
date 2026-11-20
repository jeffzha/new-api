package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHistoryOperatorUsesExistingDatabaseWithoutMoneyWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateOpsFees(db))
	require.NoError(t, db.Exec("CREATE TABLE users (id integer primary key, quota integer)").Error)
	require.NoError(t, db.Exec("INSERT INTO users (id,quota) VALUES (15,999999)").Error)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/catalog/v1/fees", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":{"currency":"USD","items":[],"nextCursor":null,"hasMore":false}}`))
	}))
	defer server.Close()
	for k, v := range map[string]string{"SQL_DSN": "local", "SQLITE_PATH": path, "OPS_FEES_URL": server.URL, "OPS_FEES_KEY_ID": "test-gw", "OPS_FEES_SECRET": "test-only-at-least-sixteen", "OPS_FEES_CURRENCY": "USD", "OPS_FEES_NAMESPACE": "operator-test", "OPS_FEES_SINCE": "", "OPS_FEES_ACTIVE_SECONDS": "", "OPS_FEES_IDLE_SECONDS": "", "OPS_FEES_TIMEOUT_SECONDS": "", "OPS_FEES_REQUEST_GAP_SECONDS": ""} {
		t.Setenv(k, v)
	}
	hash := sha256.Sum256([]byte(server.URL + "\ntest-gw\noperator-test\nUSD"))
	connection := hex.EncodeToString(hash[:])
	oldPath := common.SQLitePath
	t.Cleanup(func() { common.SQLitePath = oldPath })
	ctx := context.Background()
	require.Error(t, run(ctx, "2026-11-01T00:00:00Z", "wrong-target", 1, 10*time.Second))
	require.Equal(t, 0, calls)
	require.NoError(t, run(ctx, "2026-11-01T00:00:00Z", connection, 1, 10*time.Second))
	require.NoError(t, run(ctx, "2026-11-01T00:00:00Z", connection, 1, 10*time.Second))
	require.Equal(t, 1, calls, "completed command must be safe to repeat")
	var quota int
	require.NoError(t, db.Raw("SELECT quota FROM users WHERE id=15").Scan(&quota).Error)
	require.Equal(t, 999999, quota)
	require.False(t, db.Migrator().HasTable(&model.Log{}), "operator must never run application migrations")
	raw, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, raw.Close())
}
