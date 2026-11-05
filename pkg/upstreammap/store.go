// Package upstreammap owns credential-free upstream identities and OPS bindings.
package upstreammap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"unicode"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Mapping struct {
	ID      int64  `json:"id" gorm:"primaryKey"`
	BaseURL string `json:"base_url" gorm:"type:text;not null"`
	URLHash string `json:"-" gorm:"type:varchar(64);not null;uniqueIndex"`
}

func (Mapping) TableName() string { return "ops_upstream_mappings" }

// A shared product/project database must not mix IDs issued by different OPS
// installations or station credentials. NULL means registration is pending.
type Binding struct {
	ID            int64  `json:"id" gorm:"primaryKey"`
	MappingID     int64  `json:"mapping_id" gorm:"not null;uniqueIndex:idx_ops_upstream_binding,priority:1"`
	Target        string `json:"-" gorm:"type:varchar(64);not null;uniqueIndex:idx_ops_upstream_binding,priority:2"`
	OpsUpstreamID *int64 `json:"ops_upstream_id"`
}

func (Binding) TableName() string { return "ops_upstream_bindings" }

type Reference struct {
	MappingID int64
	OpsID     *int64
}

var references atomic.Pointer[map[string]Reference]

func Refresh(db *gorm.DB, target string) error {
	var rows []struct {
		MappingID     int64
		BaseURL       string
		OpsUpstreamID *int64
	}
	if err := db.Table("ops_upstream_mappings AS m").Select("m.id AS mapping_id, m.base_url, b.ops_upstream_id").Joins("LEFT JOIN ops_upstream_bindings AS b ON b.mapping_id = m.id AND b.target = ?", target).Scan(&rows).Error; err != nil {
		return err
	}
	result := make(map[string]Reference, len(rows))
	for _, row := range rows {
		result[row.BaseURL] = Reference{MappingID: row.MappingID, OpsID: row.OpsUpstreamID}
	}
	references.Store(&result)
	return nil
}
func Cached(base string) (Reference, bool) {
	snapshot := references.Load()
	if snapshot == nil {
		return Reference{}, false
	}
	r, ok := (*snapshot)[base]
	return r, ok
}

func Migrate(db *gorm.DB) error { return db.AutoMigrate(&Mapping{}, &Binding{}) }
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func Target(base, key string) string { return Hash(strings.TrimRight(base, "/") + "\n" + key) }

// Normalize keeps paths and case-sensitive path components distinct. No query,
// userinfo or fragments are accepted because they may contain credentials.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "?#") || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("invalid upstream base URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || len(raw) > 2000 {
		return "", errors.New("invalid upstream base URL")
	}
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}

func Ensure(db *gorm.DB, raw string) (*Mapping, error) {
	normalized, err := Normalize(raw)
	if err != nil {
		return nil, err
	}
	row := Mapping{BaseURL: normalized, URLHash: Hash(normalized)}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	if err := db.Where("url_hash = ?", row.URLHash).First(&row).Error; err != nil {
		return nil, err
	}
	if row.BaseURL != normalized {
		return nil, errors.New("upstream URL identity conflict")
	}
	return &row, nil
}

func EnsureBinding(db *gorm.DB, id int64, target string) error {
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Binding{MappingID: id, Target: target}).Error
}
func Resolve(db *gorm.DB, raw, target string) (*Mapping, *int64, error) {
	normalized, err := Normalize(raw)
	if err != nil {
		return nil, nil, err
	}
	var row Mapping
	if err := db.Where("url_hash = ?", Hash(normalized)).First(&row).Error; err != nil {
		return nil, nil, err
	}
	var binding Binding
	err = db.Where("mapping_id = ? AND target = ?", row.ID, target).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &row, nil, nil
	}
	return &row, binding.OpsUpstreamID, err
}
