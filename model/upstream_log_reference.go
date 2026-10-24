package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const LocoPalTaskPlatform = "cn-locopal-video"

// Empty UpstreamRequestID rows index accepted tasks; other rows retain provider
// settlement IDs without replacing the HTTP submission ID on the consume log.
// This table belongs to the primary DB, even when logs use a separate DB.
type UpstreamLogReference struct {
	Key               string `gorm:"size:64;primaryKey"`
	ChannelID         int
	UserID            int
	RequestID         string `gorm:"size:64;index"`
	TaskID            string `gorm:"size:191"`
	UpstreamTaskID    string `gorm:"size:191"`
	UpstreamRequestID string `gorm:"size:128;index"`
}

func upstreamLogReferenceKey(channel int, task, request string) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(channel) + "\x00" + task + "\x00" + request))
	return hex.EncodeToString(sum[:])
}

func saveUpstreamLogReference(ctx context.Context, row UpstreamLogReference) error {
	if row.ChannelID <= 0 || row.UserID <= 0 || row.RequestID == "" || len(row.RequestID) > 64 || row.TaskID == "" || len(row.TaskID) > 191 || row.UpstreamTaskID == "" || len(row.UpstreamTaskID) > 191 || len(row.UpstreamRequestID) > 128 {
		return errors.New("invalid upstream log reference")
	}
	row.Key = upstreamLogReferenceKey(row.ChannelID, row.UpstreamTaskID, row.UpstreamRequestID)
	if err := DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	var saved UpstreamLogReference
	if err := DB.WithContext(ctx).Where(map[string]any{"key": row.Key}).Take(&saved).Error; err != nil {
		return err
	}
	if saved != row {
		return errors.New("conflicting upstream log reference")
	}
	return nil
}

// IndexLocoPalTaskReferences advances a bounded keyset scan, not a JSON search
// on every log query. The caller persists the cursor only after success.
func IndexLocoPalTaskReferences(ctx context.Context, after int64) (int64, error) {
	var tasks []Task
	if err := DB.WithContext(ctx).Select("id", "task_id", "channel_id", "user_id", "private_data").
		Where("platform = ? AND id > ?", LocoPalTaskPlatform, after).Order("id asc").Limit(200).Find(&tasks).Error; err != nil {
		return after, err
	}
	for _, task := range tasks {
		if task.PrivateData.Execution != nil && task.PrivateData.Execution.RequestID != "" && task.PrivateData.UpstreamTaskID != "" {
			if err := saveUpstreamLogReference(ctx, UpstreamLogReference{ChannelID: task.ChannelId, UserID: task.UserId, RequestID: task.PrivateData.Execution.RequestID, TaskID: task.TaskID, UpstreamTaskID: task.PrivateData.UpstreamTaskID}); err != nil {
				return after, err
			}
		}
		after = task.ID
	}
	return after, nil
}

// LinkUpstreamSettlementRequest never infers identity from timestamps or prices.
// Unknown tasks remain unmatched and can be retried on the next sync pass.
func LinkUpstreamSettlementRequest(ctx context.Context, channel int, task, request string) (bool, error) {
	if task == "" || request == "" || len(task) > 191 || len(request) > 128 {
		return false, nil
	}
	var baseline UpstreamLogReference
	err := DB.WithContext(ctx).Where(map[string]any{"key": upstreamLogReferenceKey(channel, task, "")}).Take(&baseline).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	baseline.UpstreamRequestID = request
	return true, saveUpstreamLogReference(ctx, baseline)
}

func applyUpstreamLogFilter(tx *gorm.DB, request string) (*gorm.DB, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return tx, nil
	}
	condition := tx.Session(&gorm.Session{NewDB: true}).Where("logs.upstream_request_id = ?", request)
	if DB != nil {
		var refs []UpstreamLogReference
		if err := DB.Where("upstream_request_id = ?", request).Limit(101).Find(&refs).Error; err != nil {
			return nil, err
		}
		if len(refs) > 100 {
			return nil, errors.New("too many upstream request matches; contact administrator")
		}
		for _, ref := range refs {
			condition = condition.Or("(logs.request_id = ? AND logs.channel_id = ? AND logs.user_id = ?)", ref.RequestID, ref.ChannelID, ref.UserID)
		}
	}
	return tx.Where(condition), nil
}
