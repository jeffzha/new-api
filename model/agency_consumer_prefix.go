package model

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// AgencyConsumerPrefix is a verified prefix of immutable outbox receipts, not
// a balance or a highest observed sequence. ScanSeq/ScanID retain bounded
// bootstrap progress even when a sequence contains more than one page.
type AgencyConsumerPrefix struct {
	UserID         int64 `gorm:"primaryKey;autoIncrement:false"`
	VerifiedBefore int64 `gorm:"not null"`
	ScanSeq        int64 `gorm:"not null"`
	ScanID         int64 `gorm:"not null"`
}

func (AgencyConsumerPrefix) TableName() string { return AgencyTablePrefix + "consumer_prefixes" }

// MigrateAgencyConsumerPrefix is an expand-only migration. Database triggers
// invalidate cached proofs for late inserts and repairs from older binaries
// as well as new ones. Writers serialize with prefix verification so even a
// transaction that commits a historical sequence late invalidates its proof.
func MigrateAgencyConsumerPrefix(db *gorm.DB) error {
	if db == nil {
		return errors.New("agency consumer prefix: nil database")
	}
	if err := db.AutoMigrate(&AgencyConsumerPrefix{}); err != nil {
		return err
	}
	prefix := (AgencyConsumerPrefix{}).TableName()
	outbox := (AgencyBillingOutbox{}).TableName()
	source := (AgencySourceEvent{}).TableName()
	reset := func(ref string) string {
		condition := "verified_before>" + ref + ".money_seq OR scan_seq>=" + ref + ".money_seq"
		return "UPDATE " + prefix + " SET user_id=user_id WHERE user_id=" + ref + ".user_id AND " + ref + ".money_seq>0;" +
			"UPDATE " + prefix + " SET verified_before=0, scan_seq=0, scan_id=0 WHERE user_id=" + ref + ".user_id AND " + ref + ".money_seq>0 AND (" + condition + ");"
	}
	changed := "OLD.event_id<>NEW.event_id OR OLD.user_id<>NEW.user_id OR OLD.money_seq<>NEW.money_seq OR OLD.payload_hash<>NEW.payload_hash"
	specs := []struct{ name, table, event, condition, body string }{
		{"agency_prefix_outbox_insert", outbox, "INSERT", "", reset("NEW")},
		{"agency_prefix_outbox_delete", outbox, "DELETE", "", reset("OLD")},
		{"agency_prefix_outbox_update", outbox, "UPDATE", changed, reset("OLD") + reset("NEW")},
		{"agency_prefix_source_delete", source, "DELETE", "", reset("OLD")},
		{"agency_prefix_source_update", source, "UPDATE", "(" + changed + ") OR (OLD.processing_status IN ('done','skipped') AND NEW.processing_status NOT IN ('done','skipped'))", reset("OLD") + reset("NEW")},
	}
	for _, spec := range specs {
		// Creating/locking the prefix on insertion closes the race between
		// first bootstrap and an old writer committing a late low sequence.
		if spec.name == "agency_prefix_outbox_insert" {
			insert := "INSERT INTO " + prefix + " (user_id,verified_before,scan_seq,scan_id) VALUES (NEW.user_id,0,0,0)"
			switch db.Dialector.Name() {
			case "sqlite":
				insert += " ON CONFLICT(user_id) DO NOTHING;"
			case "postgres":
				insert += " ON CONFLICT(user_id) DO NOTHING;"
			case "mysql":
				insert += " ON DUPLICATE KEY UPDATE user_id=user_id;"
			}
			spec.body = insert + spec.body
		}
		var statements []string
		switch db.Dialector.Name() {
		case "sqlite":
			when := ""
			if spec.condition != "" {
				when = " WHEN " + spec.condition
			}
			statements = []string{"CREATE TRIGGER IF NOT EXISTS " + spec.name + " AFTER " + spec.event + " ON " + spec.table + " FOR EACH ROW" + when + " BEGIN " + spec.body + " END"}
		case "mysql":
			var exists int64
			if err := db.Raw("SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=DATABASE() AND trigger_name=?", spec.name).Scan(&exists).Error; err != nil {
				return err
			}
			if exists > 0 {
				continue
			}
			body := spec.body
			if spec.condition != "" {
				body = "IF " + spec.condition + " THEN " + body + " END IF;"
			}
			statements = []string{"CREATE TRIGGER " + spec.name + " AFTER " + spec.event + " ON " + spec.table + " FOR EACH ROW BEGIN " + body + " END"}
		case "postgres":
			body := spec.body
			if spec.condition != "" {
				body = "IF " + spec.condition + " THEN " + body + " END IF;"
			}
			statements = []string{"CREATE OR REPLACE FUNCTION " + spec.name + "_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN " + body + " RETURN NULL; END; $$",
				"DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='" + spec.name + "' AND tgrelid='" + spec.table + "'::regclass) THEN CREATE TRIGGER " + spec.name + " AFTER " + spec.event + " ON " + spec.table + " FOR EACH ROW EXECUTE PROCEDURE " + spec.name + "_fn(); END IF; END $$"}
		default:
			return fmt.Errorf("agency consumer prefix: unsupported dialect %q", db.Dialector.Name())
		}
		for _, statement := range statements {
			if err := db.Exec(statement).Error; err != nil {
				return fmt.Errorf("agency consumer prefix trigger %s: %w", spec.name, err)
			}
		}
	}
	return nil
}
