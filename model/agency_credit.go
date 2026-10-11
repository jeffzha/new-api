package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

var ErrAgencyCreditLimitExceeded = errors.New("agency credit limit exceeded")

const (
	AgencyCreditStatusActive    = "active"
	AgencyCreditStatusSuspended = "suspended"
)

// ensureAgencyCreditAvailableTx enforces a contract only when one exists.
// Customers without a credit contract retain the historical wallet behavior.
func ensureAgencyCreditAvailableTx(tx *gorm.DB, userID, currentDebt, additionalDebt int64) error {
	var terms AgencyCreditTerms
	err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&terms).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if terms.Status != "" && terms.Status != AgencyCreditStatusActive {
		return ErrAgencyCreditLimitExceeded
	}
	var modelDebt int64
	if err := tx.Model(&AgencyFundingDebt{}).Where("user_id = ? AND debt_kind = ? AND outstanding_quota > 0", userID, "model_charge").Select("COALESCE(SUM(outstanding_quota), 0)").Scan(&modelDebt).Error; err != nil {
		return err
	}
	if terms.CreditLimit < 0 || modelDebt < 0 || additionalDebt < 0 || modelDebt > int64(^uint64(0)>>1)-additionalDebt || modelDebt+additionalDebt > terms.CreditLimit {
		return ErrAgencyCreditLimitExceeded
	}
	return nil
}

func NextAgencyRepaymentDate(now time.Time, repaymentDay int) time.Time {
	loc := now.Location()
	if loc == nil {
		loc = time.UTC
	}
	if repaymentDay < 1 || repaymentDay > 28 {
		repaymentDay = 1
	}
	return time.Date(now.Year(), now.Month(), repaymentDay, 0, 0, 0, 0, loc)
}
