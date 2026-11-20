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

// OpenOPSWalletOperatorDB opens an explicitly selected existing database. It
// never runs application migrations, workers, Redis or payment callbacks.
func OpenOPSWalletOperatorDB(ctx context.Context) (*gorm.DB, error) {
	if os.Getenv("SQL_DSN") == "" {
		return nil, errors.New("explicit SQL_DSN required")
	}
	if strings.HasPrefix(os.Getenv("SQL_DSN"), "local") {
		path := os.Getenv("SQLITE_PATH")
		info, err := os.Stat(path)
		if !filepath.IsAbs(path) || strings.Contains(path, "?") || err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("existing absolute SQLite file required")
		}
		common.SQLitePath = path + "?mode=rw&_pragma=busy_timeout(30000)&_txlock=immediate"
	}
	db, _, err := chooseDB("SQL_DSN", false, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return nil, errors.New("wallet operator database unavailable")
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, errors.New("wallet operator database connection failed")
	}
	return db, nil
}

// FreezeOPSSourceWallet is an explicit offline operator action, never startup
// migration. Stop/drain every Gateway, Agency and payment writer first. Durable
// database triggers then reject even raw SQL from an accidentally restarted old
// binary. They remain after custody, preserving the historical source wallet.
// Schema administrators can remove triggers; this is not a DB-admin boundary.
func FreezeOPSSourceWallet(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite":
		for _, ddl := range []string{
			`CREATE TRIGGER IF NOT EXISTS ops_wallet_source_update BEFORE UPDATE OF quota,billing_mode ON users WHEN NEW.quota IS NOT OLD.quota OR NEW.billing_mode IS NOT OLD.billing_mode BEGIN SELECT RAISE(ABORT,'ops_source_wallet_frozen'); END`,
			`CREATE TRIGGER IF NOT EXISTS ops_wallet_source_insert BEFORE INSERT ON users WHEN NEW.quota IS NULL OR NEW.quota <> 0 BEGIN SELECT RAISE(ABORT,'ops_source_wallet_frozen'); END`,
			`CREATE TRIGGER IF NOT EXISTS ops_wallet_source_delete BEFORE DELETE ON users BEGIN SELECT RAISE(ABORT,'ops_source_wallet_frozen'); END`,
		} {
			if err := db.Exec(ddl).Error; err != nil {
				return err
			}
		}
	case "mysql":
		for _, trigger := range []struct{ name, event, condition string }{
			{"ops_wallet_source_update", "UPDATE", "NOT (NEW.quota <=> OLD.quota) OR NOT (NEW.billing_mode <=> OLD.billing_mode)"},
			{"ops_wallet_source_insert", "INSERT", "NEW.quota IS NULL OR NEW.quota <> 0"},
			{"ops_wallet_source_delete", "DELETE", "TRUE"},
		} {
			var count int64
			if err := db.Raw("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=DATABASE() AND TRIGGER_NAME=?", trigger.name).Scan(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				ddl := "CREATE TRIGGER " + trigger.name + " BEFORE " + trigger.event + " ON users FOR EACH ROW BEGIN IF " + trigger.condition + " THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='ops_source_wallet_frozen'; END IF; END"
				if err := db.Exec(ddl).Error; err != nil {
					return err
				}
			}
		}
	case "postgres":
		if err := db.Exec(`CREATE OR REPLACE FUNCTION ops_wallet_source_guard() RETURNS trigger AS $$ BEGIN
IF TG_OP='DELETE' THEN RAISE EXCEPTION 'ops_source_wallet_frozen'; END IF;
IF TG_OP='INSERT' THEN IF NEW.quota IS NULL OR NEW.quota<>0 THEN RAISE EXCEPTION 'ops_source_wallet_frozen'; END IF;
ELSIF NEW.quota IS DISTINCT FROM OLD.quota OR NEW.billing_mode IS DISTINCT FROM OLD.billing_mode THEN RAISE EXCEPTION 'ops_source_wallet_frozen'; END IF;
RETURN NEW; END $$ LANGUAGE plpgsql`).Error; err != nil {
			return err
		}
		if err := db.Exec(`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='ops_wallet_source_guard' AND tgrelid='users'::regclass) THEN CREATE TRIGGER ops_wallet_source_guard BEFORE INSERT OR UPDATE OR DELETE ON users FOR EACH ROW EXECUTE PROCEDURE ops_wallet_source_guard(); END IF; END $$`).Error; err != nil {
			return err
		}
	default:
		return errors.New("unsupported wallet source database")
	}
	return nil
}

// VerifyOPSSourceWalletFrozen exercises the database guard, including through a
// fresh operator connection. Each probe is rolled back even if a guard is absent.
func VerifyOPSSourceWalletFrozen(db *gorm.DB, userID int) error {
	for _, statement := range []string{
		"UPDATE users SET quota=quota+1 WHERE id=?",
		"UPDATE users SET billing_mode=CASE WHEN billing_mode='legacy' THEN 'agency_durable' ELSE 'legacy' END WHERE id=?",
		"INSERT INTO users (id,quota,username,aff_code,billing_mode) SELECT id,1,username,aff_code,billing_mode FROM users WHERE id=?",
		"DELETE FROM users WHERE id=?",
	} {
		verified := false
		err := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			err := tx.Exec(statement, userID).Error
			verified = err != nil && strings.Contains(err.Error(), "ops_source_wallet_frozen")
			return errors.New("rollback source guard probe")
		})
		if err == nil || !verified {
			return errors.New("durable source wallet guard is not active")
		}
	}
	return nil
}
