package opsfees

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeeRecoveryDelayBoundsAndServerLimits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		failures int
		want     time.Duration
	}{
		{"brief server failure", &opsclient.ReadError{Status: 500}, 1, 3 * time.Second},
		{"second failure", &opsclient.ReadError{Status: 503}, 2, 6 * time.Second},
		{"network outage", errors.New("unavailable"), 3, 12 * time.Second},
		{"continued outage", errors.New("unavailable"), 1000, 5 * time.Minute},
		{"rate limited", &opsclient.ReadError{Status: 429}, 1, time.Minute},
		{"server retry hint", &opsclient.ReadError{Status: 503, RetryAfter: 90 * time.Second}, 1, 90 * time.Second},
		{"permission failure", &opsclient.ReadError{Status: 403}, 1, 5 * time.Minute},
		{"invalid credential", &opsclient.ReadError{Status: 401}, 1, 5 * time.Minute},
		{"bad request", &opsclient.ReadError{Status: 400}, 1, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, feeRecoveryDelay(tc.err, tc.failures, 3*time.Second)) })
	}
	assert.Equal(t, 10*time.Second, feeRecoveryDelay(errors.New("unavailable"), 1, 10*time.Second))
}

func TestFeeRecoverySharesScheduleAndResumesSameCursor(t *testing.T) {
	oldType := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(oldType); model.ConfigureOpsFees(nil) })
	dialect := os.Getenv("OPS_FEE_RECOVERY_TEST_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	db := testDB(t, dialect)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	require.NoError(t, model.MigrateOpsFees(db))
	now := time.Unix(1793692800, 0)
	require.NoError(t, db.Create(&model.Log{Type: 2, CreatedAt: now.Unix()}).Error)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "previous", r.URL.Query().Get("since"))
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write(feePage(t, "0.001746", "recovered", false))
	}))
	defer server.Close()
	cfg := Config{BaseURL: server.URL, KeyID: "gw", Secret: "fixture-secret-for-recovery", Connection: "recovery", ActiveInterval: 5 * time.Second, IdleInterval: 5 * time.Minute, Timeout: time.Second, RoundBudget: time.Second, MinRequestGap: 3 * time.Second, MaxPages: 1}
	require.NoError(t, db.Create(&model.OpsFeeSync{Connection: cfg.Connection, Cursor: "previous"}).Error)
	blue, green := New(db, db, cfg), New(db, db, cfg)
	blue.Now = func() time.Time { return now }
	green.Now = blue.Now
	wait, err := blue.Step(context.Background())
	require.Error(t, err)
	assert.Equal(t, 3*time.Second, wait)
	var state model.OpsFeeSync
	require.NoError(t, db.First(&state, "connection = ?", cfg.Connection).Error)
	assert.Equal(t, "previous", state.Cursor)
	assert.Empty(t, state.Owner)
	now = now.Add(2 * time.Second)
	_, err = green.Step(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "another slot must not bypass recovery schedule")
	now = now.Add(time.Second)
	_, err = green.Step(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	require.NoError(t, db.First(&state, "connection = ?", cfg.Connection).Error)
	assert.Equal(t, "recovered", state.Cursor)
	assert.Empty(t, state.LastError)
	assert.Zero(t, state.Failures)
	var fee model.OpsFee
	require.NoError(t, db.First(&fee, "connection = ?", cfg.Connection).Error)
	require.NotNil(t, fee.Amount)
	assert.Equal(t, "0.001746", *fee.Amount)
}
