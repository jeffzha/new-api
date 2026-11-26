package model

import (
	"context"
	"errors"
	"strconv"
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

type ModelHealthOpsReceipt struct {
	ID        int64
	CreatedAt int64
	RequestID string
	Amount    string
	UserID    string
	ChannelID int
	ModelName string
	TaskID    string
}

// Read only this deployment's normal OPS receipts for dedicated probe users.
// The same request in another OPS connection must never supply a detection fee.
func ModelHealthOpsReceipts(ctx context.Context) ([]ModelHealthOpsReceipt, error) {
	config := opsFeeRuntime.Load()
	if config == nil {
		return nil, nil
	}
	db := DB.WithContext(ctx)
	var tokens []Token
	if err := db.Select("id", "user_id").Where("name LIKE ?", "model-health/%").Find(&tokens).Error; err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	owners := make(map[int]int, len(tokens))
	ids := make([]string, len(tokens))
	for i, token := range tokens {
		owners[token.Id] = token.UserId
		ids[i] = strconv.Itoa(token.UserId)
	}
	var rows []ModelHealthOpsReceipt
	err := db.Table("model_health_runs AS h").
		Select("h.id, h.created_at, h.request_id, f.amount, f.user_id, h.channel_id, h.model_name, h.upstream_task_id AS task_id").
		Joins("JOIN ops_fees AS f ON f.request_id = h.request_id AND f.model = h.model_name AND f.connection = ?", config.Connection).
		Where("h.request_id <> '' AND h.channel_id > 0 AND f.user_id IN ? AND f.status = ? AND f.currency = ? AND f.amount IS NOT NULL", ids, "available", "CNY").
		Where("h.created_at >= ?", config.Since).
		Find(&rows).Error
	if err != nil || len(rows) == 0 || LOG_DB == nil {
		return nil, err
	}
	verified := make([]ModelHealthOpsReceipt, 0, len(rows))
	for start := 0; start < len(rows); start += 100 {
		batch := rows[start:min(start+100, len(rows))]
		requests := make([]string, len(batch))
		for i, row := range batch {
			requests[i] = row.RequestID
		}
		var logs []Log
		if err := LOG_DB.WithContext(ctx).Select("request_id", "user_id", "token_id", "channel_id", "model_name").Where("request_id IN ? AND type = ?", requests, LogTypeConsume).Find(&logs).Error; err != nil {
			return nil, err
		}
		byRequest := make(map[string][]Log, len(logs))
		for _, log := range logs {
			byRequest[log.RequestId] = append(byRequest[log.RequestId], log)
		}
		var taskIDs []string
		for _, row := range batch {
			if row.TaskID != "" {
				taskIDs = append(taskIDs, row.TaskID)
			}
		}
		var tasks []Task
		if len(taskIDs) > 0 {
			if err := db.Select("task_id", "channel_id", "user_id", "properties", "private_data", "status").Where("task_id IN ?", taskIDs).Find(&tasks).Error; err != nil {
				return nil, err
			}
		}
		byTask := make(map[string][]Task, len(tasks))
		for _, task := range tasks {
			byTask[task.TaskID] = append(byTask[task.TaskID], task)
		}
		for _, row := range batch {
			if row.TaskID != "" {
				matched := byTask[row.TaskID]
				if len(matched) == 1 {
					task := matched[0]
					if task.UserId > 0 && owners[task.PrivateData.TokenId] == task.UserId && row.UserID == strconv.Itoa(task.UserId) && row.ChannelID == task.ChannelId && row.ModelName == task.Properties.OriginModelName && task.PrivateData.Execution != nil && task.PrivateData.Execution.RequestID == row.RequestID && (task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure) {
						verified = append(verified, row)
					}
				}
				continue
			}
			for _, log := range byRequest[row.RequestID] {
				if log.UserId > 0 && owners[log.TokenId] == log.UserId && row.UserID == strconv.Itoa(log.UserId) && row.ChannelID == log.ChannelId && row.ModelName == log.ModelName {
					verified = append(verified, row)
					break
				}
			}
		}
	}
	return verified, nil
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
