package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrInvalidUserQuotaAdjustment = errors.New("invalid user quota adjustment")
	ErrUserQuotaPermission        = errors.New("cannot adjust quota for this user role")
)

// UserQuotaAdjustment is the immutable database snapshot of a committed manual
// adjustment. Pending relay deductions in the quota cache are not part of it.
type UserQuotaAdjustment struct {
	UserID   int
	Username string
	Before   int
	After    int
}

func AdjustUserQuota(userID, operatorRole int, mode string, value int) (*UserQuotaAdjustment, error) {
	return AdjustUserQuotaWithActor(userID, operatorRole, mode, value, 0)
}

func AdjustUserQuotaWithActor(userID, operatorRole int, mode string, value int, actorID int64) (*UserQuotaAdjustment, error) {
	if userID <= 0 || (mode != "add" && mode != "subtract" && mode != "override") {
		return nil, ErrInvalidUserQuotaAdjustment
	}
	if mode != "override" && value <= 0 {
		return nil, ErrInvalidUserQuotaAdjustment
	}
	if value > common.MaxWalletQuota || value < -common.MaxWalletQuota {
		return nil, ErrWalletQuotaLimitExceeded
	}

	var adjustment UserQuotaAdjustment
	var durable bool
	err := AgencyOpsTransaction(DB, func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if operatorRole != common.RoleRootUser && operatorRole <= user.Role {
			return ErrUserQuotaPermission
		}
		if user.Quota > common.MaxWalletQuota || user.Quota < -common.MaxWalletQuota {
			return ErrWalletQuotaLimitExceeded
		}
		quota := decimal.NewFromInt(int64(value))
		switch mode {
		case "add":
			quota = decimal.NewFromInt(int64(user.Quota)).Add(quota)
		case "subtract":
			quota = decimal.NewFromInt(int64(user.Quota)).Sub(quota)
		}
		after, err := common.WalletQuotaFromDecimalStrict(quota)
		if err != nil {
			return ErrWalletQuotaLimitExceeded
		}
		// An unchanged override is a successful operation, including on MySQL
		// configurations that count only changed rows in RowsAffected.
		durable = user.BillingMode == AgencyDurableBillingMode
		if after != user.Quota {
			if user.BillingMode == AgencyProvisioningBillingMode {
				return ErrAgencyProvisioning
			}
			if durable {
				source := "admin_adjustment"
				if mode == "override" {
					source = "admin_override"
				}
				if err := ApplyAgencyQuotaDeltaWithActorTx(tx, int64(userID), int64(after)-int64(user.Quota), source, actorID); err != nil {
					return err
				}
			} else {
				result := tx.Model(&User{}).Where("id = ?", userID).Update("quota", after)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return gorm.ErrRecordNotFound
				}
			}
		}
		adjustment = UserQuotaAdjustment{UserID: user.Id, Username: user.Username, Before: user.Quota, After: after}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Durable reservations are in the database; invalidate their cached snapshot.
	// Legacy reservations live in Redis, so preserve them using only the difference.
	delta := int64(adjustment.After) - int64(adjustment.Before)
	if delta != 0 {
		if durable {
			if err := InvalidateUserCache(userID); err != nil {
				common.SysError(fmt.Sprintf("failed to invalidate manual quota adjustment for user %d: %s", userID, err))
			}
			return &adjustment, nil
		}
		if err := cacheIncrUserQuota(userID, delta); err != nil {
			common.SysError(fmt.Sprintf("failed to sync manual quota adjustment for user %d: %s", userID, err))
		}
	}
	return &adjustment, nil
}
