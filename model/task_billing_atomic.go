package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// TaskQuotaAdjustmentResult describes the durable portion of an asynchronous
// task adjustment. TokenUnavailable is deliberately not an error: a token may
// have been soft-deleted after the request was accepted. In that case a
// negative adjustment is retained at the pre-consumed amount instead of
// refunding the wallet and accidentally reviving the token.
type TaskQuotaAdjustmentResult struct {
	Task             *Task
	PreConsumedQuota int
	AppliedQuota     int
	QuotaDelta       int
	TokenID          int
	TokenKey         string
	TokenAdjusted    bool
	TokenUnavailable bool
	Changed          bool
	WalletAdjusted   bool
}

// ApplyTaskQuotaAdjustment atomically applies the final quota for a task to
// its funding source, token, and persisted task row. The task may be a
// not-yet-persisted fixture (ID == 0); in production asynchronous tasks are
// persisted and receive a compare-and-swap guard on quota.
//
// A missing token is treated as a historical settlement condition. A positive
// delta can still be charged to the wallet, but a negative delta is retained
// at the pre-consumed amount because refunding the wallet without restoring the
// deleted token would create a free credit. The caller can expose the
// TokenUnavailable flag in reconciliation/audit views.
func ApplyTaskQuotaAdjustment(task *Task, targetQuota int) (*TaskQuotaAdjustmentResult, error) {
	if task == nil || targetQuota < 0 {
		return nil, errors.New("invalid task quota adjustment")
	}
	if modelMax := int64(common.MaxQuota); int64(targetQuota) > modelMax {
		return nil, errors.New("task quota adjustment exceeds quota limit")
	}

	result := &TaskQuotaAdjustmentResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		current := task
		if task.ID > 0 {
			var persisted Task
			if err := lockForUpdate(tx).Where("id = ?", task.ID).First(&persisted).Error; err != nil {
				return err
			}
			if persisted.Quota != task.Quota {
				return fmt.Errorf("task quota changed concurrently")
			}
			current = &persisted
		}

		result.Task = current
		result.PreConsumedQuota = current.Quota
		result.AppliedQuota = current.Quota
		result.TokenID = current.PrivateData.TokenId
		result.QuotaDelta = targetQuota - current.Quota
		if result.QuotaDelta == 0 {
			result.AppliedQuota = current.Quota
			return nil
		}

		tokenPresent := current.PrivateData.TokenId <= 0
		var token Token
		if current.PrivateData.TokenId > 0 {
			err := lockForUpdate(tx).Where("id = ? AND user_id = ?", current.PrivateData.TokenId, current.UserId).First(&token).Error
			switch {
			case err == nil:
				tokenPresent = true
				result.TokenKey = token.Key
			case errors.Is(err, gorm.ErrRecordNotFound):
				result.TokenUnavailable = true
			default:
				return err
			}
		}

		// A deleted token cannot receive a refund. Keep the original
		// reservation and finish the task at that amount. Additional provider
		// usage is still charged to the wallet, since the wallet is the
		// authoritative customer liability.
		effectiveTarget := targetQuota
		if result.TokenUnavailable && result.QuotaDelta < 0 {
			effectiveTarget = current.Quota
			result.QuotaDelta = 0
		}
		result.AppliedQuota = effectiveTarget

		if result.QuotaDelta != 0 {
			if err := applyTaskFundingDeltaTx(tx, current, result.QuotaDelta, effectiveTarget); err != nil {
				return err
			}
			result.WalletAdjusted = current.PrivateData.BillingSource != TaskBillingSourceSubscription ||
				current.PrivateData.SubscriptionId <= 0

			if tokenPresent && current.PrivateData.TokenId > 0 {
				afterRemain := int64(token.RemainQuota) - int64(result.QuotaDelta)
				afterUsed := int64(token.UsedQuota) + int64(result.QuotaDelta)
				if afterRemain > int64(common.MaxQuota) || afterRemain < -int64(common.MaxQuota)-1 ||
					afterUsed > int64(common.MaxQuota) || afterUsed < -int64(common.MaxQuota)-1 {
					return errors.New("token quota arithmetic overflow")
				}
				updated := tx.Model(&Token{}).
					Where("id = ? AND user_id = ?", token.Id, current.UserId).
					Updates(map[string]any{
						"remain_quota":  afterRemain,
						"used_quota":    afterUsed,
						"accessed_time": time.Now().Unix(),
					})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return fmt.Errorf("task token %d changed concurrently", token.Id)
				}
				result.TokenAdjusted = true
			}

			if task.ID > 0 {
				updated := tx.Model(&Task{}).
					Where("id = ? AND quota = ?", task.ID, current.Quota).
					Update("quota", effectiveTarget)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return fmt.Errorf("task quota changed concurrently")
				}
			}
			current.Quota = effectiveTarget
			task.Quota = effectiveTarget
			result.Changed = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if result.Changed && common.RedisEnabled {
		if IsAgencyDurableUser(task.UserId) {
			if err := InvalidateUserCache(task.UserId); err != nil {
				common.SysLog("failed to invalidate task billing user cache: " + err.Error())
			}
		} else if err := cacheIncrUserQuota(task.UserId, -int64(result.QuotaDelta)); err != nil {
			common.SysLog("failed to sync task billing user cache: " + err.Error())
		}
		if result.TokenAdjusted && result.TokenKey != "" {
			if _, err := cacheApplyTokenQuotaDelta(result.TokenID, result.TokenKey, -int64(result.QuotaDelta)); err != nil {
				common.SysLog("failed to sync task billing token cache: " + err.Error())
			}
		}
	}
	return result, nil
}

func applyTaskFundingDeltaTx(tx *gorm.DB, task *Task, delta, targetQuota int) error {
	if task.PrivateData.BillingSource == TaskBillingSourceSubscription && task.PrivateData.SubscriptionId > 0 {
		var subscription UserSubscription
		if err := lockForUpdate(tx).Where("id = ?", task.PrivateData.SubscriptionId).First(&subscription).Error; err != nil {
			return err
		}
		newUsed := subscription.AmountUsed + int64(delta)
		if newUsed < 0 {
			newUsed = 0
		}
		if subscription.AmountTotal > 0 && newUsed > subscription.AmountTotal {
			return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, subscription.AmountTotal)
		}
		return tx.Model(&UserSubscription{}).Where("id = ?", subscription.Id).
			Update("amount_used", newUsed).Error
	}

	var billingMode string
	if err := tx.Model(&User{}).Where("id = ?", task.UserId).Pluck("billing_mode", &billingMode).Error; err != nil {
		return err
	}
	if billingMode == AgencyProvisioningBillingMode {
		return ErrAgencyProvisioning
	}
	if billingMode == AgencyDurableBillingMode {
		chargeID := ""
		if task.PrivateData.BillingContext != nil {
			chargeID = strings.TrimSpace(task.PrivateData.BillingContext.AgencyChargeID)
			if chargeID == "" {
				chargeID = strings.TrimSpace(task.PrivateData.BillingContext.AgencyBillingEventID)
			}
		}
		if chargeID == "" {
			chargeID = task.TaskID
		}
		if chargeID == "" {
			return ErrAgencyFundingUnavailable
		}
		return AdjustAgencyChargeTx(tx, task.UserId, delta, chargeID, int64(targetQuota))
	}

	var user User
	if err := AgencyLockForUpdate(tx).Select("id, quota").Where("id = ?", task.UserId).First(&user).Error; err != nil {
		return err
	}
	after := int64(user.Quota) - int64(delta)
	if after > int64(common.MaxQuota) || after < -int64(common.MaxQuota)-1 {
		return errors.New("user quota arithmetic overflow")
	}
	updated := tx.Model(&User{}).Where("id = ?", task.UserId).Update("quota", after)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
