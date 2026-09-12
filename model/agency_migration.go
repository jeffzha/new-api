package model

import (
	"context"
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
	return nil
}

// AgencyLockForUpdate centralizes the dialect-safe row lock used by agency
// financial repositories. SQLite intentionally receives no FOR UPDATE clause.
func AgencyLockForUpdate(tx *gorm.DB) *gorm.DB {
	if tx == nil || tx.Dialector == nil || tx.Dialector.Name() == "sqlite" {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

// agencyKeyColumn returns the dialect-quoted token key column. initCol is
// normally reached through InitDB during application startup; calling it
// lazily keeps isolated unit tests that open their own database working.
func agencyKeyColumn() string {
	if commonKeyCol == "" {
		initCol()
	}
	return commonKeyCol
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
