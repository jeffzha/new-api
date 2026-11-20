// Package opsstorage contains SQL storage types shared by OPS integration state.
package opsstorage

import (
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// LargeText holds bounded OPS batches and snapshots. Declaring the MySQL type
// in the model prevents AutoMigrate from shrinking it to TEXT on every restart.
type LargeText string

func (LargeText) GormDataType() string { return "text" }

func (LargeText) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db.Dialector.Name() == "mysql" {
		return "mediumtext"
	}
	return "text"
}
