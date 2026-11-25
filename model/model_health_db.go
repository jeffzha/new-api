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

// OpenModelHealthDB opens only the existing regional primary database. The
// worker never runs Gateway migrations, wallet initialization or option writes.
func OpenModelHealthDB(ctx context.Context) (*gorm.DB, error) {
	dsn := os.Getenv("SQL_DSN")
	if dsn == "" {
		return nil, errors.New("model health worker requires explicit SQL_DSN")
	}
	if strings.HasPrefix(dsn, "local") {
		path := os.Getenv("SQLITE_PATH")
		info, err := os.Stat(path)
		if dsn != "local" || !filepath.IsAbs(path) || strings.Contains(path, "?") || err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("model health worker requires an existing absolute SQLite file")
		}
		common.SQLitePath = path + "?mode=rw&_pragma=busy_timeout(5000)&_txlock=immediate"
	}
	db, kind, err := chooseDB("SQL_DSN", false, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return nil, errors.New("model health worker could not open the primary database")
	}
	pool, err := db.DB()
	if err != nil {
		return nil, errors.New("model health worker database pool unavailable")
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, errors.New("model health worker database connection failed")
	}
	for _, table := range []any{&ModelHealthConfig{}, &ModelHealthRun{}, &ModelHealthDelivery{}, &Channel{}, &Option{}, &OpsRequestEvent{}, &Task{}} {
		if !db.WithContext(ctx).Migrator().HasTable(table) {
			_ = pool.Close()
			return nil, errors.New("model health worker requires the initialized Gateway schema")
		}
	}
	common.SetMainDatabaseType(kind)
	initCol()
	return db, nil
}
