package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CNMidjourneyEffect struct {
	ID            string `gorm:"size:64;primaryKey"`
	BillingKey    string `gorm:"size:64;not null;index"`
	Payload       string `gorm:"type:text;not null"`
	State         string `gorm:"size:32;not null;index"`
	Attempts      int
	NextAttemptAt int64  `gorm:"index"`
	LastErrorCode string `gorm:"size:64"`
}

// The delivery receipt and log share the log database transaction. A crash
// before acknowledging the primary outbox therefore cannot duplicate a log.
type CNMidjourneyLogReceipt struct {
	ID      string `gorm:"size:64;primaryKey"`
	Payload string `gorm:"type:text;not null"`
}

func cnStageMidjourneyEffectTx(tx *gorm.DB, receipt *CNMidjourneyBilling, state string, delta int, at time.Time) error {
	logType, quota := LogTypeConsume, delta
	if state != "charged" {
		logType, quota = LogTypeRefund, -delta
	}
	id := CNIdentity("midjourney-log", receipt.Key, state)
	other, err := common.Marshal(map[string]any{"task_id": receipt.MjID, "terminal_state": state, "reason": receipt.RefundReason, "token_refund_status": receipt.TokenRefundStatus, "token_disposed_quota": receipt.TokenDisposedQuota})
	if err != nil {
		return err
	}
	log := Log{UserId: receipt.UserID, CreatedAt: at.Unix(), Type: logType, ModelName: cnMidjourneyModelName(receipt.Action), Quota: quota, ChannelId: receipt.ChannelID, TokenId: receipt.TokenID, RequestId: id, Other: string(other)}
	payload, err := common.Marshal(log)
	if err != nil {
		return err
	}
	return tx.Create(&CNMidjourneyEffect{ID: id, BillingKey: receipt.Key, Payload: string(payload), State: "pending"}).Error
}

func DeliverCNMidjourneyEffects(ctx context.Context, db, logDB *gorm.DB, limit int) error {
	var rows []CNMidjourneyEffect
	if err := db.WithContext(ctx).Where("state = ? AND next_attempt_at <= ?", "pending", time.Now().Unix()).Order("next_attempt_at, id").Limit(min(max(limit, 1), 1000)).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	var setupErr error
	if logDB == nil || logDB.Dialector.Name() == "clickhouse" {
		setupErr = errors.New("midjourney durable log delivery requires a transactional log database")
	} else if !logDB.Migrator().HasTable(&CNMidjourneyLogReceipt{}) {
		setupErr = logDB.AutoMigrate(&CNMidjourneyLogReceipt{})
	}
	var failures []error
	for _, row := range rows {
		err := setupErr
		if err == nil {
			err = logDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				delivery := CNMidjourneyLogReceipt{ID: row.ID, Payload: row.Payload}
				result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&delivery)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 0 {
					if err := tx.First(&delivery, "id = ?", row.ID).Error; err != nil {
						return err
					}
					if delivery.Payload != row.Payload {
						return ErrCNConflict
					}
					return nil
				}
				var log Log
				if err := common.UnmarshalJsonStr(row.Payload, &log); err != nil {
					return err
				}
				if log.RequestId != row.ID {
					return ErrCNConflict
				}
				return tx.Create(&log).Error
			})
		}
		if err == nil {
			err = db.WithContext(ctx).Model(&CNMidjourneyEffect{}).Where("id = ? AND state = ?", row.ID, "pending").Updates(map[string]any{"state": "delivered", "last_error_code": ""}).Error
		}
		if err != nil {
			failures = append(failures, err)
			failure := db.WithContext(context.WithoutCancel(ctx)).Model(&CNMidjourneyEffect{}).Where("id = ? AND state = ?", row.ID, "pending").Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_attempt_at": time.Now().Unix() + 5, "last_error_code": "midjourney_log_delivery_failed"}).Error
			failures = append(failures, failure)
		}
	}
	return errors.Join(failures...)
}
