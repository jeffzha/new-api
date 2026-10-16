package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type unavailableOutput struct{}

var errOutputUnavailable = errors.New("injected output unavailable")

func (unavailableOutput) Write([]byte) (int, error) {
	return 0, errOutputUnavailable
}

type cancelAfterOutput struct {
	cancel    context.CancelFunc
	remaining int
}

func (out *cancelAfterOutput) Write(raw []byte) (int, error) {
	out.remaining--
	if out.remaining == 0 {
		out.cancel()
	}
	return len(raw), nil
}

func TestOperatorFreezeRecoveryAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			switch dialect {
			case "sqlite":
				path := filepath.Join(t.TempDir(), "operator.sqlite3")
				fixture, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
				require.NoError(t, err)
				pool, err := fixture.DB()
				require.NoError(t, err)
				require.NoError(t, pool.Close())
				t.Setenv("SQL_DSN", "local")
				t.Setenv("SQLITE_PATH", path)
			case "mysql":
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
					t.Skip("requires owned real MySQL test database")
				}
				t.Setenv("SQL_DSN", os.Getenv("AGENCY_HUB_TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
					t.Skip("requires owned real PostgreSQL test database")
				}
				t.Setenv("SQL_DSN", os.Getenv("AGENCY_HUB_TEST_OPERATOR_POSTGRES_DSN"))
			}
			ctx := context.Background()
			db, err := model.OpenCNIntegrityOperatorDB(ctx)
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			if dialect == "sqlite" {
				require.False(t, db.Migrator().HasTable(&model.CNIntegrityEpoch{}), "opening the operator must not migrate")
			}
			for range 2 {
				require.NoError(t, model.MigrateCNFinance(db, "test-cn"))
			}
			var countBefore int64
			require.NoError(t, db.Model(&model.CNIntegrityEpoch{}).Count(&countBefore).Error)
			var output bytes.Buffer
			require.NoError(t, run(ctx, []string{"prepare", "--gateway", "test-cn", "--evidence", "synthetic-operator-review"}, &output))
			var prepared map[string]any
			require.NoError(t, common.Unmarshal(output.Bytes(), &prepared))
			epoch := prepared["epochId"].(string)
			from, to := time.Now().UTC().Add(-time.Hour).Format(model.CNTimeLayout), time.Now().UTC().Format(model.CNTimeLayout)
			state := filepath.Join(t.TempDir(), "private-freeze.json")
			args := []string{"freeze", "--gateway", "test-cn", "--epoch", epoch, "--expected-generation", "1", "--evidence", "synthetic-freeze-review", "--from", from, "--to", to, "--state-file", state}
			require.ErrorIs(t, run(ctx, args, unavailableOutput{}), errOutputUnavailable, "freeze must commit before the injected output failure")
			var count int64
			require.NoError(t, db.Model(&model.CNIntegritySnapshot{}).Where("epoch_id = ?", epoch).Count(&count).Error)
			require.EqualValues(t, 1, count)
			output.Reset()
			require.NoError(t, run(ctx, args, &output))
			first := output.String()
			output.Reset()
			require.NoError(t, run(ctx, args, &output))
			require.Equal(t, first, output.String(), "completed single request is idempotent too")
			require.NoError(t, db.Model(&model.CNIntegritySnapshot{}).Where("epoch_id = ?", epoch).Count(&count).Error)
			require.EqualValues(t, 1, count)
			var journal freezeJournal
			raw, err := os.ReadFile(state)
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(raw, &journal))
			require.Nil(t, journal.Pending)
			require.NotEmpty(t, journal.LastProofID)
			require.Equal(t, "synthetic-freeze-review", journal.EvidenceID)
			loopArgs := []string{"freeze-loop", "--gateway", "test-cn", "--epoch", epoch, "--expected-generation", "1", "--evidence", "synthetic-loop-review", "--from", from, "--expected-parent", journal.LastProofID, "--interval", "1s", "--state-file", filepath.Join(t.TempDir(), "private-loop.json")}
			loopCtx, stop := context.WithCancel(ctx)
			require.ErrorIs(t, run(loopCtx, loopArgs, &cancelAfterOutput{cancel: stop, remaining: 2}), context.Canceled)
			require.NoError(t, db.Model(&model.CNIntegritySnapshot{}).Where("epoch_id = ?", epoch).Count(&count).Error)
			require.EqualValues(t, 3, count, "explicit loop seals children using its durable parent cursor")
			loopCtx, stop = context.WithCancel(ctx)
			require.ErrorIs(t, run(loopCtx, loopArgs, &cancelAfterOutput{cancel: stop, remaining: 1}), context.Canceled)
			require.NoError(t, db.Model(&model.CNIntegritySnapshot{}).Where("epoch_id = ?", epoch).Count(&count).Error)
			require.EqualValues(t, 4, count, "loop restart resumes its own cursor")
			require.NoError(t, run(ctx, []string{"stop", "--gateway", "test-cn", "--epoch", epoch, "--expected-generation", "1", "--state", "paused", "--evidence", "synthetic-operator-pause"}, &output))
			require.ErrorIs(t, run(ctx, loopArgs, &output), model.ErrCNConflict, "loop stops on control changes instead of silently adopting a generation")
			stale := append([]string(nil), args...)
			for i, arg := range stale {
				if arg == "--expected-generation" {
					stale[i+1] = "2"
				}
			}
			require.ErrorIs(t, run(ctx, stale, &output), model.ErrCNConflict)
			require.NoError(t, db.Model(&model.CNIntegrityEpoch{}).Count(&count).Error)
			require.Equal(t, countBefore+1, count)
		})
	}
}

func TestOperatorStartupRequiresExistingExplicitDatabase(t *testing.T) {
	t.Setenv("SQL_DSN", "")
	_, err := model.OpenCNIntegrityOperatorDB(context.Background())
	require.Error(t, err)
	t.Setenv("SQL_DSN", "local")
	path := filepath.Join(t.TempDir(), "must-not-create.sqlite3")
	t.Setenv("SQLITE_PATH", path)
	_, err = model.OpenCNIntegrityOperatorDB(context.Background())
	require.Error(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"help"}, &out))
	require.Contains(t, out.String(), "freeze-loop")
}
