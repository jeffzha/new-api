package model

import (
	"errors"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Caller holds the native user/account/lot locks. S4 owns pool conservation
// and immutable held -> released/retired transitions within this transaction.
func cnRefundReserveValueTx(tx *gorm.DB, l AgencyFundingLot, paid, amount decimal.Decimal, quota int64, key string) error {
	quoted, err := QuoteCNFundingValueTx(tx, l.ID, quota)
	if err != nil {
		return err
	}
	if quoted != amount.String() {
		return ErrCNRefundNonintegral
	}
	return ReserveCNFundingValueTx(tx, l.ID, key, quota, amount.String())
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
