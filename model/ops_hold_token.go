package model

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// OPSHoldIntent couples the independent API-key budget to its remote money
// reservation. Deferred tasks retain this row until terminal key settlement.
type OPSHoldIntent struct {
	Gateway        string `gorm:"primaryKey;size:160;index:idx_ops_hold_release,priority:1"`
	Request        string `gorm:"primaryKey;size:160"`
	User           string `gorm:"size:160;not null"`
	Model          string `gorm:"size:300;not null"`
	CancelPending  bool   `gorm:"not null;index:idx_ops_hold_release,priority:2"`
	Dispatched     bool   `gorm:"not null"`
	TokenID        int    `gorm:"not null"`
	TokenQuota     int    `gorm:"not null"`
	Finalized      bool   `gorm:"not null"`
	Reconciled     bool   `gorm:"not null"`
	TaskID         string `gorm:"size:160;not null"`
	LeaseUntil     int64  `gorm:"not null"`
	Updated        int64  `gorm:"not null;index:idx_ops_hold_release,priority:3"`
	ReviewEvidence string `gorm:"size:500;not null"`
	ReviewedBy     string `gorm:"size:160;not null"`
	Resolution     string `gorm:"size:40;not null"`
	ResolvedAt     int64  `gorm:"not null"`
}

func (OPSHoldIntent) TableName() string { return "ops_wallet_hold_intents" }

// FinalizeOPSHoldTask commits task quota, API-key budget and the replay marker
// together. It never writes user money or releases an OPS wallet hold.
func FinalizeOPSHoldTask(ctx context.Context, db *gorm.DB, task *Task, target int) (*TaskQuotaAdjustmentResult, error) {
	if task == nil || task.ID <= 0 || target < 0 || target > common.MaxQuota || task.PrivateData.Execution == nil || task.PrivateData.OPSReservationGateway == "" || task.PrivateData.BillingSource != "ops" {
		return nil, errors.New("invalid OPS task settlement")
	}
	request := task.PrivateData.Execution.RequestID
	result := &TaskQuotaAdjustmentResult{Task: task, TokenID: task.PrivateData.TokenId}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intent OPSHoldIntent
		if err := lockForUpdate(tx).Where(map[string]any{"gateway": task.PrivateData.OPSReservationGateway, "request": request, "user": strconv.Itoa(task.UserId), "task_id": task.TaskID}).First(&intent).Error; err != nil {
			return err
		}
		var current Task
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", task.ID, task.UserId).First(&current).Error; err != nil {
			return err
		}
		if current.PrivateData.BillingSource != "ops" || current.PrivateData.OPSReservationGateway != intent.Gateway || current.PrivateData.Execution == nil || current.PrivateData.Execution.RequestID != request || (current.Status != TaskStatusSuccess && current.Status != TaskStatusFailure) || intent.CancelPending || intent.TokenID != current.PrivateData.TokenId {
			return errors.New("OPS task settlement identity mismatch")
		}
		if current.Status == TaskStatusFailure {
			target = 0
		}
		if intent.Finalized && intent.TokenQuota != target {
			return errors.New("OPS task settlement conflict")
		}
		result.PreConsumedQuota = current.Quota
		result.AppliedQuota = target
		result.QuotaDelta = target - current.Quota
		result.Changed = result.QuotaDelta != 0
		if !intent.Finalized && intent.TokenID > 0 {
			if err := AdjustOPSHoldTokenTx(tx, current.UserId, intent.TokenID, intent.TokenQuota, target, false); err != nil {
				return err
			}
			result.TokenAdjusted = intent.TokenQuota != target
		}
		if err := tx.Model(&Task{}).Where("id = ?", current.ID).Update("quota", target).Error; err != nil {
			return err
		}
		return tx.Model(&OPSHoldIntent{}).Where("gateway = ? AND request = ?", intent.Gateway, intent.Request).Updates(map[string]any{"token_quota": target, "finalized": true, "updated": time.Now().Unix()}).Error
	})
	if err != nil {
		return nil, err
	}
	task.Quota = result.AppliedQuota
	return result, nil
}

// LockOPSHoldTx shares the supported engines' wallet row-lock semantics.
func LockOPSHoldTx(tx *gorm.DB) *gorm.DB { return lockForUpdate(tx) }

// AdjustOPSHoldTokenTx keeps the independent API-key budget and its durable
// OPS hold intent in the caller's transaction. User money is never changed.
func AdjustOPSHoldTokenTx(tx *gorm.DB, user, id, previous, target int, requireAvailable bool) error {
	if previous < 0 || target < 0 || target > common.MaxQuota {
		return errors.New("invalid OPS token hold")
	}
	var token Token
	err := lockForUpdate(tx.Unscoped()).Where("id = ? AND user_id = ?", id, user).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && target == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	delta := target - previous
	if token.DeletedAt.Valid {
		if delta <= 0 {
			return nil
		}
		return errors.New("API key removed")
	}
	if requireAvailable && (token.Status != common.TokenStatusEnabled || token.ExpiredTime > 0 && token.ExpiredTime <= common.GetTimestamp()) {
		return errors.New("API key unavailable")
	}
	if delta > 0 && requireAvailable && !token.UnlimitedQuota && token.RemainQuota < delta {
		return ErrInsufficientAgencyTokenQuota
	}
	remain, used := int64(token.RemainQuota)-int64(delta), int64(token.UsedQuota)+int64(delta)
	if remain < -int64(common.MaxWalletQuota) || remain > int64(common.MaxWalletQuota) || used < -int64(common.MaxWalletQuota) || used > int64(common.MaxWalletQuota) {
		return ErrWalletQuotaLimitExceeded
	}
	if err := InvalidateTokenCache(token.Key); err != nil {
		return err
	}
	return tx.Unscoped().Model(&Token{}).Where("id = ? AND user_id = ?", id, user).Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": common.GetTimestamp()}).Error
}
