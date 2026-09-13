package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"gorm.io/gorm"
)

// AgencyComponentKey keeps exact UTF-8 component identities independent of a
// database's case/accent-insensitive collation. Keep the original ID for audit.
func AgencyComponentKey(componentID string) string {
	digest := sha256.Sum256([]byte(componentID))
	return hex.EncodeToString(digest[:])
}

func (row *AgencyUsageFact) BeforeCreate(_ *gorm.DB) error {
	row.ComponentKey = AgencyComponentKey(row.ComponentID)
	return nil
}

func (row *AgencyCommissionLedger) BeforeCreate(_ *gorm.DB) error {
	row.ComponentKey = AgencyComponentKey(row.ComponentID)
	return nil
}

// migrateAgencyComponentIdentities is additive until the replacement unique
// constraints are built. Run with old Hub writers quiesced: they do not know
// how to populate the new hash column. No financial history is recalculated.
func migrateAgencyComponentIdentities(db *gorm.DB) error {
	for _, spec := range []struct {
		model                                 any
		table, replacement, previous, columns string
	}{
		{&AgencyUsageFact{}, (AgencyUsageFact{}).TableName(), "uidx_agency_usage_component_key", "uidx_agency_usage_event_component", "event_id, component_key"},
		{&AgencyCommissionLedger{}, (AgencyCommissionLedger{}).TableName(), "uidx_agency_commission_component_key", "uidx_agency_commission_event", "event_id, component_key, entry_type"},
	} {
		var rows []struct {
			ID          int64
			ComponentID string
		}
		var after int64
		for {
			rows = nil
			if err := db.Table(spec.table).Select("id, component_id").Where("id > ? AND (component_key IS NULL OR component_key = ?)", after, "").Order("id").Limit(200).Find(&rows).Error; err != nil {
				return fmt.Errorf("agency component identity backfill: %w", err)
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				if err := db.Table(spec.table).Where("id = ?", row.ID).Update("component_key", AgencyComponentKey(row.ComponentID)).Error; err != nil {
					return err
				}
				after = row.ID
			}
		}
		if !db.Migrator().HasIndex(spec.model, spec.replacement) {
			if err := db.Exec("CREATE UNIQUE INDEX " + spec.replacement + " ON " + spec.table + " (" + spec.columns + ")").Error; err != nil {
				return fmt.Errorf("agency component identity index: %w", err)
			}
		}
		if db.Migrator().HasIndex(spec.model, spec.previous) {
			if err := db.Migrator().DropIndex(spec.model, spec.previous); err != nil {
				return err
			}
		}
	}
	return nil
}
