package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ModelHealthConfig stores independently editable rules, templates and targets.
// Credentials are references to worker environment variables, never values.
type ModelHealthConfig struct {
	ID    string `json:"id" gorm:"size:64;primaryKey"`
	Kind  string `json:"kind" gorm:"size:24"`
	Value string `json:"-" gorm:"type:text"`
}

type ModelHealthRun struct {
	ID               int64  `json:"id" gorm:"primaryKey"`
	Target           string `json:"target" gorm:"size:64"`
	ChannelID        int    `json:"channel_id"`
	ModelName        string `json:"model" gorm:"size:191"`
	UpstreamModel    string `json:"upstream" gorm:"size:191"`
	Endpoint         string `json:"endpoint" gorm:"size:48"`
	Source           string `json:"source" gorm:"size:16"`
	State            string `json:"state" gorm:"size:24"`
	Stage            string `json:"stage" gorm:"size:24"`
	Reason           string `json:"reason" gorm:"size:255"`
	StatusCode       int    `json:"status_code"`
	CreatedAt        int64  `json:"created_at"`
	StartedAt        int64  `json:"started_at"`
	FinishedAt       int64  `json:"finished_at"`
	NextPoll         int64  `json:"-"`
	LeaseUntil       int64  `json:"-"`
	ReservationMicro int64  `json:"reserved_micro"`
	TokenBudget      int    `json:"-"`
	ActualMicro      *int64 `json:"actual_micro"`
	RequestID        string `json:"request_id" gorm:"size:128"`
	UpstreamTaskID   string `json:"upstream_task_id" gorm:"size:191"`
	Template         string `json:"-" gorm:"type:text"`
	Evidence         string `json:"-" gorm:"type:text"`
}

type ModelHealthDelivery struct {
	ID         string `json:"id" gorm:"size:64;primaryKey"`
	Target     string `json:"target" gorm:"size:64"`
	Incident   string `json:"incident" gorm:"size:64"`
	Event      string `json:"event" gorm:"size:16"`
	State      string `json:"state" gorm:"size:16"`
	CreatedAt  int64  `json:"created_at"`
	Status     int    `json:"status_code"`
	Attempts   int    `json:"attempts"`
	NextAt     int64  `json:"next_at"`
	LeaseUntil int64  `json:"-"`
	Payload    string `json:"-" gorm:"type:text"`
}

// Serialize admission, worker claims and notification reservations across slots.
// The write is first so SQLite does not require upgrading a read transaction.
func ModelHealthTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := ModelHealthConfig{ID: "worker-lock", Kind: "internal"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Model(&row).UpdateColumn("value", time.Now().UTC().Format(time.RFC3339Nano)).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func ReadModelHealthConfig[T any](db *gorm.DB, id string, fallback T) (T, error) {
	var row ModelHealthConfig
	err := db.First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fallback, nil
	}
	if err != nil {
		return fallback, err
	}
	err = common.UnmarshalJsonStr(row.Value, &fallback)
	return fallback, err
}

func SaveModelHealthConfig(db *gorm.DB, id, kind string, value any) error {
	encoded, err := common.Marshal(value)
	if err != nil {
		return err
	}
	row := ModelHealthConfig{ID: id, Kind: kind, Value: string(encoded)}
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}
