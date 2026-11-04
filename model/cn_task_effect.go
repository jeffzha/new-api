package model

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CNTaskEffect CNMidjourneyEffect

func cnStageTaskEffectTx(tx *gorm.DB, task *Task, row *CNTaskBilling, delta int, at time.Time) error {
	id := CNIdentity("ordinary-task-log", row.Key, strconv.FormatInt(row.Version, 10))
	logType, quota := LogTypeConsume, delta
	if delta < 0 {
		logType, quota = LogTypeRefund, -delta
	}
	values := task.BillingLogFacts()
	// A task settled from the provider's bill reports the tokens on that bill.
	var bill cnTaskProviderBill
	if row.ProviderBill != "" && common.UnmarshalJsonStr(row.ProviderBill, &bill) == nil && bill.Settlement.TotalTokens > 0 {
		values["billed_tokens"] = bill.Settlement.TotalTokens
		delete(values, "estimated_tokens")
	}
	// settle is the final difference of a finished task; refund gives the whole charge back for a failed or cancelled one.
	stage := "settle"
	switch {
	case row.FactID == "":
		stage = "preconsume"
	case task == nil || task.Status == TaskStatusFailure:
		stage = "refund"
	}
	maps.Copy(values, map[string]any{"task_id": row.PublicTaskID, "request_id": row.RequestID, "token_refund_status": row.TokenRefundStatus,
		"token_disposed_quota": row.TokenDisposedQuota, "pre_consumed_quota": row.Actual, "actual_quota": row.Actual + delta, "settlement_stage": stage})
	if row.QuotaPerUnit != "" {
		values["quota_per_unit"] = row.QuotaPerUnit
	}
	if row.Currency != "" && row.ExchangeRate != "" {
		values["settlement_currency"], values["exchange_rate"] = row.Currency, row.ExchangeRate
	}
	if row.UpstreamID != "" {
		values["root_info"] = map[string]any{"upstream_task_id": row.UpstreamID}
	}
	if row.FinalError != "" {
		values["reason"] = row.FinalError
	}
	if task != nil && task.Status == TaskStatusFailure && task.FailReason != "" {
		values["reason"] = task.FailReason
	}
	other, err := common.Marshal(values)
	if err != nil {
		return err
	}
	log := Log{UserId: row.UserID, CreatedAt: at.Unix(), Type: logType, Quota: quota, ChannelId: row.ChannelID, TokenId: row.TokenID, RequestId: id, Other: string(other)}
	if task != nil {
		log.ModelName, log.Group = task.Properties.OriginModelName, task.Group
	}
	payload, err := common.Marshal(log)
	if err != nil {
		return err
	}
	return tx.Create(&CNTaskEffect{ID: id, BillingKey: row.Key, Payload: string(payload), State: "pending"}).Error
}

// Uses the reviewed MJ log-database receipt protocol with a separate source
// table and identity namespace, so source coverage cannot mix the two lanes.
func DeliverCNTaskEffects(ctx context.Context, db, logDB *gorm.DB, limit int) error {
	var rows []CNTaskEffect
	if err := db.WithContext(ctx).Where("state = ? AND next_attempt_at <= ?", "pending", time.Now().Unix()).Order("next_attempt_at, id").Limit(min(max(limit, 1), 1000)).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	var setupErr error
	if logDB == nil || logDB.Dialector.Name() == "clickhouse" {
		setupErr = errors.New("task durable log delivery requires a transactional log database")
	} else if !logDB.Migrator().HasTable(&CNMidjourneyLogReceipt{}) {
		setupErr = logDB.AutoMigrate(&CNMidjourneyLogReceipt{})
	}
	var failures []error
	for _, row := range rows {
		err := setupErr
		if err == nil {
			err = logDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				receipt := CNMidjourneyLogReceipt{ID: row.ID, Payload: row.Payload}
				result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 0 {
					if err := tx.First(&receipt, "id = ?", row.ID).Error; err != nil {
						return err
					}
					if receipt.Payload != row.Payload {
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
			err = db.WithContext(ctx).Model(&CNTaskEffect{}).Where("id = ? AND state = ?", row.ID, "pending").Updates(map[string]any{"state": "delivered", "last_error_code": ""}).Error
		}
		if err != nil {
			failures = append(failures, err, db.WithContext(context.WithoutCancel(ctx)).Model(&CNTaskEffect{}).Where("id = ? AND state = ?", row.ID, "pending").Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_attempt_at": time.Now().Unix() + 5, "last_error_code": "task_log_delivery_failed"}).Error)
		}
	}
	return errors.Join(failures...)
}
