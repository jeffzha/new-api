package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MigrateAgency creates only the agency_hub schema. It is intentionally not
// called from the normal new-api startup migration; deploy/agency-hub runs it
// explicitly with a migration-capable database user.
func MigrateAgency(db *gorm.DB) error {
	if db == nil {
		return errors.New("agency migration: nil database")
	}
	models := AgencyModels()
	for _, item := range models {
		statement := &gorm.Statement{DB: db}
		if err := statement.Parse(item); err != nil {
			return fmt.Errorf("agency migration: parse model: %w", err)
		}
		if statement.Schema == nil {
			return fmt.Errorf("agency migration: model %T has no schema", item)
		}
		if !strings.HasPrefix(statement.Schema.Table, AgencyTablePrefix) {
			return fmt.Errorf("agency migration: table %q outside %q", statement.Schema.Table, AgencyTablePrefix)
		}
	}
	if err := db.AutoMigrate(models...); err != nil {
		return fmt.Errorf("agency migration: %w", err)
	}
	if err := migrateAgencyComponentIdentities(db); err != nil {
		return err
	}
	// A user can have multiple historical failed/cancelled attempts. Active
	// jobs are serialized under the core user row lock, not by (user,status):
	// that old uniqueness constraint made a second cancellation impossible.
	if db.Migrator().HasIndex(&AgencyProvisioningJob{}, "uidx_agency_provision_user_status") {
		if err := db.Migrator().DropIndex(&AgencyProvisioningJob{}, "uidx_agency_provision_user_status"); err != nil {
			return fmt.Errorf("agency migration: provisioning history index: %w", err)
		}
	}
	// Backfill the nullable open-issue key before dropping the legacy index.
	// Never rewrite historical difference/resolution evidence during migration.
	var issues []AgencyReconciliationIssue
	if err := db.Where("status = ? AND active_key IS NULL", "open").
		FindInBatches(&issues, 500, func(tx *gorm.DB, _ int) error {
			for _, issue := range issues {
				key := AgencyReconciliationActiveKey(issue.ObjectType, issue.ObjectID)
				if err := tx.Model(&AgencyReconciliationIssue{}).
					Where("id = ? AND status = ? AND active_key IS NULL", issue.ID, "open").
					Update("active_key", key).Error; err != nil {
					return err
				}
			}
			return nil
		}).Error; err != nil {
		return fmt.Errorf("agency migration: reconciliation open keys: %w", err)
	}
	// SQLite cannot add a UNIQUE column with ALTER TABLE. The model definition
	// creates a plain nullable column first; create the unique index separately
	// after backfill, which is portable across SQLite, MySQL and PostgreSQL.
	if !db.Migrator().HasIndex(&AgencyReconciliationIssue{}, "uidx_agency_reconcile_active") {
		if err := db.Exec("CREATE UNIQUE INDEX uidx_agency_reconcile_active ON " + (AgencyReconciliationIssue{}).TableName() + " (active_key)").Error; err != nil {
			return fmt.Errorf("agency migration: reconciliation active-key unique index: %w", err)
		}
	}
	// Preserve the old protection until its replacement exists, including on
	// databases where DDL commits separately from the migration transaction.
	if db.Migrator().HasIndex(&AgencyReconciliationIssue{}, "uidx_agency_reconcile_open") {
		if err := db.Migrator().DropIndex(&AgencyReconciliationIssue{}, "uidx_agency_reconcile_open"); err != nil {
			return fmt.Errorf("agency migration: reconciliation history index: %w", err)
		}
	}
	return nil
}

// AgencyReconciliationActiveKey identifies one open discrepancy per object,
// independent of the evidence observed on each subsequent scan.
func AgencyReconciliationActiveKey(objectType, objectID string) string {
	digest := sha256.Sum256([]byte(objectType + "\x00" + objectID))
	return hex.EncodeToString(digest[:])
}

// AgencyLockForUpdate centralizes the dialect-safe row lock used by agency
// financial repositories. SQLite intentionally receives no FOR UPDATE clause.
func AgencyLockForUpdate(tx *gorm.DB) *gorm.DB {
	if tx == nil || tx.Dialector == nil || tx.Dialector.Name() == "sqlite" {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

func MigrateAgencyWithTimeout(db *gorm.DB, timeout time.Duration) error {
	if timeout <= 0 {
		return MigrateAgency(db)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return MigrateAgency(tx)
	})
}
