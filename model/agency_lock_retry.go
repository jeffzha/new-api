package model

import (
	"errors"
	"strings"
	"time"

	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

const agencyTransientLockMaxAttempts = 4

// isTransientLockError reports whether err is a deadlock or lock timeout that
// aborted the whole transaction and is safe to re-run. MySQL deadlocks and
// lock-wait timeouts (1213/1205), PostgreSQL 40P01/55P03 and SQLite
// busy/locked always roll the transaction back entirely, so nothing was
// committed and a retry is idempotent at the persistence boundary. Any other
// error (insufficient quota, validation, idempotent replay, drift) is returned
// as-is and must not be re-attempted.
func isTransientLockError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40P01" || pgErr.Code == "55P03"
	}
	var sqliteErr *sqlitedriver.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == 5 || sqliteErr.Code() == 6
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "SQLITE_LOCKED")
}

// retryAgencyTransaction re-runs a whole funding transaction when the database
// aborted it on a deadlock or lock timeout. Attempts back off 2/4/8 ms so
// concurrent survivors do not immediately collide on the same index gap again.
// The retry count is deliberately small: a persistent lock conflict must fail
// and be surfaced, not spin.
func retryAgencyTransaction(fn func() error) error {
	var err error
	for attempt := 0; attempt < agencyTransientLockMaxAttempts; attempt++ {
		err = fn()
		if err == nil || !isTransientLockError(err) {
			return err
		}
		if attempt+1 < agencyTransientLockMaxAttempts {
			time.Sleep(time.Duration(1<<uint(attempt)) * time.Millisecond)
		}
	}
	return err
}

// runAgencyFundingTransaction runs fn inside DB.Transaction and retries only
// transient deadlock/lock-timeout aborts. It is used by the per-request
// gateway reserve paths (design §22.4: sustained burst must not surface
// transient InnoDB deadlocks as user-visible failures).
func runAgencyFundingTransaction(fn func(tx *gorm.DB) error) error {
	return retryAgencyTransaction(func() error {
		return DB.Transaction(fn)
	})
}
