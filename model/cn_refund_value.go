package model

import (
	"errors"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Depends on S4's CNFundingValueLot residual allocator. Refund holds are value
// allocations with a separate identity and no recognized_revenue posting. They
// leave the same pool that consumption uses, including throughout unknown I/O.
func cnRefundReserveValueTx(tx *gorm.DB, l AgencyFundingLot, paid, amount decimal.Decimal, quota int64, key string) error {
	lotID := CNIdentity("agency-lot", l.SourceKind, l.SourceID, "paid")
	value, err := cnFundingValueTx(tx, lotID, CNIdentity("cn-refund-value", key), l.PaidInitial, quota, paid)
	if err != nil {
		return err
	}
	if !value.Equal(amount) {
		return ErrCNRefundNonintegral
	}
	return nil
}

func cnRefundReleaseValueTx(tx *gorm.DB, l AgencyFundingLot, row CNRefundReservation) error {
	lotID := CNIdentity("agency-lot", l.SourceKind, l.SourceID, "paid")
	var pool CNFundingValueLot
	if err := lockForUpdate(tx).Where("lot_id = ?", lotID).First(&pool).Error; err != nil {
		return err
	}
	paid, err := cnDecimal(pool.PaidValue)
	if err != nil {
		return err
	}
	var allocation CNFundingValueAllocation
	if err := tx.Where("allocation_id = ?", CNIdentity("cn-refund-value", row.Key)).First(&allocation).Error; err != nil {
		return err
	}
	if allocation.Quota != row.HeldQuota || allocation.LotID != lotID {
		return ErrCNRefundConflict
	}
	_, err = cnFundingValueTx(tx, lotID, allocation.AllocationID, l.PaidInitial, 0, paid)
	return err
}

func cnRefundCheckValuePoolTx(tx *gorm.DB, l AgencyFundingLot, cash decimal.Decimal, hasRefunds bool) error {
	var pool CNFundingValueLot
	err := lockForUpdate(tx).Where("lot_id = ?", CNIdentity("agency-lot", l.SourceKind, l.SourceID, "paid")).First(&pool).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && !hasRefunds {
		return nil
	} // S4 can seed immutable pre-upgrade heads.
	if err != nil {
		return err
	}
	value, err := cnDecimal(pool.RemainingValue)
	if err != nil || !value.Equal(cash) || pool.RemainingQuota != l.PaidAvailable {
		return ErrCNRefundUnsupported
	}
	return nil
}
