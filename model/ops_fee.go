package model

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OpsFee is an independent OPS result. It never changes a log or wallet.
type OpsFee struct {
	Connection    string  `gorm:"primaryKey;type:varchar(64)" json:"-"`
	RequestID     string  `gorm:"primaryKey;type:varchar(160)" json:"-"`
	UserID        string  `gorm:"type:varchar(160);not null" json:"-"`
	Model         string  `gorm:"type:varchar(200);not null" json:"-"`
	Amount        *string `gorm:"type:varchar(100)" json:"amount"`
	StationAmount *string `gorm:"type:varchar(100)" json:"station_amount"`
	RefundAmount  *string `gorm:"type:varchar(100)" json:"refund_amount"`
	Difference    *string `gorm:"type:varchar(100)" json:"difference"`
	Currency      string  `gorm:"type:varchar(8);not null" json:"currency"`
	ChargedAt     string  `gorm:"type:varchar(40)" json:"charged_at"`
	FetchedAt     int64   `json:"fetched_at"`
	Status        string  `gorm:"type:varchar(32)" json:"status"`
}

type OpsFeeSync struct {
	Connection    string `gorm:"primaryKey;type:varchar(64)"`
	Cursor        string `gorm:"type:varchar(256)"`
	Owner         string `gorm:"type:varchar(64)"`
	LeaseUntil    int64
	NextRunAt     int64
	NextRequestAt int64
	LastSuccessAt int64
	LastError     string `gorm:"type:varchar(80)"`
	Failures      int
}

type OpsFeeRuntime struct {
	Connection string
	Since      int64
	Scope      string
}

var opsFeeRuntime atomic.Pointer[OpsFeeRuntime]
var OpsFeeWake = make(chan struct{}, 1)
var opsFeeLastWake atomic.Int64
var opsFeeLastActivity atomic.Int64

func NotifyOpsFees() {
	if !OpsFeesEnabled() {
		return
	}
	at := time.Now().Unix()
	opsFeeLastActivity.Store(at)
	previous := opsFeeLastWake.Load()
	if at-previous < 15 || !opsFeeLastWake.CompareAndSwap(previous, at) {
		return
	}
	select {
	case OpsFeeWake <- struct{}{}:
	default:
	}
}

func ConfigureOpsFees(config *OpsFeeRuntime) {
	opsFeeLastActivity.Store(0)
	opsFeeRuntime.Store(config)
}
func OpsFeesEnabled() bool { return opsFeeRuntime.Load() != nil }

func MigrateOpsFees(db *gorm.DB) error { return db.AutoMigrate(&OpsFee{}, &OpsFeeSync{}) }

func ClaimOpsFeeSync(ctx context.Context, db *gorm.DB, connection, owner string, at int64) (*OpsFeeSync, error) {
	db = db.WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&OpsFeeSync{Connection: connection}).Error; err != nil {
		return nil, err
	}
	result := db.Model(&OpsFeeSync{}).Where("connection = ? AND next_run_at <= ? AND (owner = '' OR lease_until < ?)", connection, at, at).Updates(map[string]any{"owner": owner, "lease_until": at + 45})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	var state OpsFeeSync
	err := db.Where("connection = ? AND owner = ?", connection, owner).First(&state).Error
	return &state, err
}

// Each page and its cursor commit together. A lost lease cannot advance progress.
func SaveOpsFeePage(ctx context.Context, db *gorm.DB, connection, owner, cursor string, rows []OpsFee, at int64) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state OpsFeeSync
		if e := lockForUpdate(tx).Where("connection = ? AND owner = ? AND lease_until >= ?", connection, owner, at).First(&state).Error; e != nil {
			return errors.New("fee lease lost")
		}
		r := tx.Model(&OpsFeeSync{}).Where("connection = ? AND owner = ?", connection, owner).Updates(map[string]any{"cursor": cursor, "last_success_at": at, "last_error": "", "failures": 0})
		if r.Error != nil {
			return r.Error
		}
		if len(rows) == 0 {
			return nil
		}
		for i := range rows {
			rows[i].Connection = connection
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "connection"}, {Name: "request_id"}}, DoUpdates: clause.AssignmentColumns([]string{"user_id", "model", "amount", "station_amount", "refund_amount", "difference", "currency", "charged_at", "fetched_at", "status"})}).CreateInBatches(rows, 50).Error
	})
}

func FinishOpsFeeSync(ctx context.Context, db *gorm.DB, connection, owner string, next int64, code string, failures int) error {
	return db.WithContext(ctx).Model(&OpsFeeSync{}).Where("connection = ? AND owner = ?", connection, owner).Updates(map[string]any{"owner": "", "lease_until": 0, "next_run_at": next, "last_error": code, "failures": failures}).Error
}

// Persist the throttle before network I/O, so restarts and lease takeovers
// cannot burst through the station's shared read quota.
func ReserveOpsFeeRequest(ctx context.Context, db *gorm.DB, connection, owner string, at, gapMillis int64) (bool, error) {
	r := db.WithContext(ctx).Model(&OpsFeeSync{}).Where("connection = ? AND owner = ? AND lease_until >= ? AND next_request_at <= ?", connection, owner, at/1000, at).Update("next_request_at", at+gapMillis)
	return r.RowsAffected == 1, r.Error
}

// Only the requested, already-authorized log page is enriched. One indexed batch
// query, including when the logs database is separate from the primary database.
func AttachOpsFees(ctx context.Context, logs []*Log) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	config := opsFeeRuntime.Load()
	if config == nil || len(logs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(logs))
	for _, log := range logs {
		if log.Type != LogTypeConsume {
			continue
		}
		log.OpsFee = &OpsFee{Status: "waiting", Currency: "CNY"}
		if log.RequestId != "" {
			ids = append(ids, log.RequestId)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var fees []OpsFee
	if err := DB.WithContext(ctx).Where("connection = ? AND request_id IN ?", config.Connection, ids).Find(&fees).Error; err != nil {
		return err
	}
	byID := make(map[string]*OpsFee, len(fees))
	for i := range fees {
		byID[fees[i].RequestID] = &fees[i]
	}
	for _, log := range logs {
		if log.OpsFee == nil {
			continue
		}
		fee := byID[log.RequestId]
		if fee != nil && fee.UserID == strconv.Itoa(log.UserId) && fee.Model == log.ModelName {
			log.OpsFee = fee
		}
	}
	return nil
}

func OpsFeesRecentlyActive(ctx context.Context, logDB *gorm.DB, at time.Time) bool {
	if recent := opsFeeLastActivity.Load(); recent > 0 && recent >= at.Add(-10*time.Minute).Unix() {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var row Log
	// Walk at most 50 newest records, instead of scanning unresolved history.
	var logs []Log
	if logDB.WithContext(ctx).Order("id DESC").Limit(50).Select("id,type,created_at,other").Find(&logs).Error != nil {
		return false
	}
	for _, v := range logs {
		if v.Type == LogTypeConsume && v.CreatedAt > row.CreatedAt {
			row = v
		}
	}
	return row.CreatedAt >= at.Add(-10*time.Minute).Unix()
}
