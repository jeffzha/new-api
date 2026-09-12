package model

import (
	"errors"
	"testing"

	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsTransientLockErrorClassifiesDialectErrors locks in the cross-dialect
// policy: only deadlock/lock-timeout/lock-busy aborts (which roll back the
// whole transaction) may be retried; duplicate-key, data, validation and
// business errors must pass through untouched.
func TestIsTransientLockErrorClassifiesDialectErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "mysql deadlock", err: &mysql.MySQLError{Number: 1213}, want: true},
		{name: "mysql lock wait timeout", err: &mysql.MySQLError{Number: 1205}, want: true},
		{name: "mysql duplicate entry", err: &mysql.MySQLError{Number: 1062}, want: false},
		{name: "postgres deadlock", err: &pgconn.PgError{Code: "40P01"}, want: true},
		{name: "postgres lock not available", err: &pgconn.PgError{Code: "55P03"}, want: true},
		{name: "postgres unique violation", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "sqlite busy text", err: errors.New("database is locked"), want: true},
		{name: "business", err: ErrInsufficientAgencyWalletQuota, want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTransientLockError(tc.err))
		})
	}
}

// TestRetryAgencyTransactionRetriesOnlyTransientLockErrors pins the retry
// contract: transient aborts are re-run up to the small budget, business
// errors return immediately, and exhausting the budget surfaces the last lock
// error instead of swallowing it.
func TestRetryAgencyTransactionRetriesOnlyTransientLockErrors(t *testing.T) {
	t.Run("succeeds after transient deadlocks", func(t *testing.T) {
		calls := 0
		err := retryAgencyTransaction(func() error {
			calls++
			if calls < 3 {
				return &mysql.MySQLError{Number: 1213}
			}
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, 3, calls)
	})
	t.Run("business error returns immediately", func(t *testing.T) {
		calls := 0
		err := retryAgencyTransaction(func() error {
			calls++
			return ErrInsufficientAgencyWalletQuota
		})
		require.ErrorIs(t, err, ErrInsufficientAgencyWalletQuota)
		require.Equal(t, 1, calls)
	})
	t.Run("exhausts budget and returns last lock error", func(t *testing.T) {
		calls := 0
		err := retryAgencyTransaction(func() error {
			calls++
			return &pgconn.PgError{Code: "40P01"}
		})
		require.Error(t, err)
		require.Equal(t, agencyTransientLockMaxAttempts, calls)
	})
}

var _ = sqlitedriver.Error{}
