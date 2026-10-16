package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open only the explicitly selected existing primary database. Operator startup
// does not migrate, create users, initialize Redis or start background workers.
func OpenCNIntegrityOperatorDB(ctx context.Context) (*gorm.DB, error) {
	dsn := os.Getenv("SQL_DSN")
	if dsn == "" {
		return nil, errors.New("operator requires explicit SQL_DSN")
	}
	if strings.HasPrefix(dsn, "local") {
		path := os.Getenv("SQLITE_PATH")
		if !filepath.IsAbs(path) || strings.Contains(path, "?") {
			return nil, errors.New("operator requires an absolute SQLite file path without DSN options")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("operator requires an existing SQLite file")
		}
		common.SQLitePath = path + "?mode=rw&_pragma=busy_timeout(30000)&_txlock=immediate"
	}
	db, kind, err := chooseDB("SQL_DSN", false, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return nil, errors.New("operator database open failed")
	}
	pool, err := db.DB()
	if err != nil {
		return nil, errors.New("operator database unavailable")
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, errors.New("operator database connection failed")
	}
	common.SetMainDatabaseType(kind)
	initCol()
	if err := cnInstallWalletValueGuard(db); err != nil {
		_ = pool.Close()
		return nil, errors.New("operator writer guard unavailable")
	}
	return db, nil
}
