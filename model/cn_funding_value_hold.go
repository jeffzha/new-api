package model

import (
	"errors"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// These hooks only account for an already-authorized native paid-lot hold.
// Callers own the same transaction and user -> account -> native lot locks,
// and must atomically update their native spendable/held quota projections.
// No hook calls a provider or authorizes releasing an unknown payment outcome.
type CNFundingValueHold struct {
	OperationID string `gorm:"size:191;primaryKey"`
	NativeLotID int64  `gorm:"not null"`
	LotID       string `gorm:"size:64;not null;index"`
	Quota       int64  `gorm:"not null"`
	Value       string `gorm:"size:128;not null"`
	State       string `gorm:"size:16;not null"`
}

func GetCNFundingValueTx(tx *gorm.DB, nativeLotID int64) (*CNFundingValueLot, error) {
	if tx == nil || tx.Statement == nil || nativeLotID <= 0 {
		return nil, ErrCNConflict
	}
	if _, ok := tx.Statement.ConnPool.(interface {
		Commit() error
		Rollback() error
	}); !ok {
		return nil, errors.New("funding value requires caller transaction")
	}
	var lot AgencyFundingLot
	if err := lockForUpdate(tx).First(&lot, nativeLotID).Error; err != nil {
		return nil, err
	}
	var purchase AgencyTopupFact
	if err := tx.Where("source_operation_id = ? AND user_id = ?", lot.SourceID, lot.UserID).First(&purchase).Error; err != nil {
		return nil, err
	}
	if purchase.PaymentStatus != "success" || purchase.PaymentReference == "" || !opsMoneyCurrency.MatchString(purchase.CurrencyCode) {
		return nil, ErrCNConflict
	}
	amount, err := cnDecimal(purchase.ActualMoney)
	if err != nil {
		return nil, err
	}
	balance, err := cnFundingValueLotTx(tx, CNIdentity("agency-lot", lot.SourceKind, lot.SourceID, "paid"), lot.PaidInitial, amount)
	if err != nil {
		return nil, err
	}
	if err = cnValidateFundingValueTx(tx, balance); err != nil {
		return nil, err
	}
	return balance, nil
}

// Quote the exact current pool share under the caller's financial locks. The
// final available quota receives the residual, including any previous refunds.
func QuoteCNFundingValueTx(tx *gorm.DB, nativeLotID, quota int64) (string, error) {
	balance, err := GetCNFundingValueTx(tx, nativeLotID)
	if err != nil {
		return "", err
	}
	if quota <= 0 || quota > balance.RemainingQuota {
		return "", ErrCNConflict
	}
	remaining, err := cnDecimal(balance.RemainingValue)
	if err != nil {
		return "", err
	}
	paid, err := cnDecimal(balance.PaidValue)
	if err != nil {
		return "", err
	}
	value := cnDivideHalfEven(paid.Mul(decimal.NewFromInt(quota)), decimal.NewFromInt(balance.InitialQuota))
	if quota == balance.RemainingQuota || value.GreaterThan(remaining) {
		value = remaining
	}
	return value.String(), nil
}

func ReserveCNFundingValueTx(tx *gorm.DB, nativeLotID int64, operationID string, quota int64, value string) error {
	if operationID == "" || len(operationID) > 191 || quota <= 0 {
		return ErrCNConflict
	}
	amount, err := cnDecimal(value)
	if err != nil || amount.IsNegative() {
		return ErrCNConflict
	}
	balance, err := GetCNFundingValueTx(tx, nativeLotID)
	if err != nil {
		return err
	}
	var prior CNFundingValueHold
	err = tx.Where("operation_id = ?", operationID).First(&prior).Error
	if err == nil {
		if prior.NativeLotID != nativeLotID || prior.Quota != quota || prior.Value != amount.String() {
			return ErrCNConflict
		}
		// Replay cannot re-hold a released or retired operation.
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	quoted, err := QuoteCNFundingValueTx(tx, nativeLotID, quota)
	if err != nil {
		return err
	}
	if quoted != amount.String() {
		return ErrCNConflict
	}
	remaining, err := cnDecimal(balance.RemainingValue)
	if err != nil {
		return err
	}
	held, err := cnDecimal(balance.HeldValue)
	if err != nil {
		return err
	}
	balance.RemainingQuota -= quota
	balance.RemainingValue = remaining.Sub(amount).String()
	balance.HeldQuota += quota
	balance.HeldValue = held.Add(amount).String()
	if err = tx.Model(&CNFundingValueLot{}).Where("lot_id = ?", balance.LotID).Updates(map[string]any{"remaining_quota": balance.RemainingQuota, "remaining_value": balance.RemainingValue, "held_quota": balance.HeldQuota, "held_value": balance.HeldValue}).Error; err != nil {
		return err
	}
	if err = tx.Create(&CNFundingValueHold{OperationID: operationID, NativeLotID: nativeLotID, LotID: balance.LotID, Quota: quota, Value: amount.String(), State: "held"}).Error; err != nil {
		return err
	}
	return cnValidateFundingValueTx(tx, balance)
}

func ReleaseCNFundingValueTx(tx *gorm.DB, operationID string) error {
	return cnFinishFundingValueHoldTx(tx, operationID, "released")
}

func RetireCNFundingValueTx(tx *gorm.DB, operationID string) error {
	return cnFinishFundingValueHoldTx(tx, operationID, "retired")
}

func cnFinishFundingValueHoldTx(tx *gorm.DB, operationID, state string) error {
	if tx == nil || operationID == "" || len(operationID) > 191 {
		return ErrCNConflict
	}
	var hold CNFundingValueHold
	if err := tx.Where("operation_id = ?", operationID).First(&hold).Error; err != nil {
		return err
	}
	balance, err := GetCNFundingValueTx(tx, hold.NativeLotID)
	if err != nil {
		return err
	}
	if err = lockForUpdate(tx).Where("operation_id = ?", operationID).First(&hold).Error; err != nil {
		return err
	}
	if hold.State == state {
		return nil
	}
	if hold.State != "held" || hold.LotID != balance.LotID || hold.Quota > balance.HeldQuota {
		return ErrCNConflict
	}
	amount, err := cnDecimal(hold.Value)
	if err != nil {
		return err
	}
	held, err := cnDecimal(balance.HeldValue)
	if err != nil {
		return err
	}
	if amount.GreaterThan(held) {
		return ErrCNConflict
	}
	balance.HeldQuota -= hold.Quota
	balance.HeldValue = held.Sub(amount).String()
	if state == "released" {
		remaining, err := cnDecimal(balance.RemainingValue)
		if err != nil {
			return err
		}
		balance.RemainingQuota += hold.Quota
		balance.RemainingValue = remaining.Add(amount).String()
	} else {
		retired, err := cnDecimal(balance.RetiredValue)
		if err != nil {
			return err
		}
		balance.RetiredQuota += hold.Quota
		balance.RetiredValue = retired.Add(amount).String()
	}
	if err = tx.Model(&CNFundingValueLot{}).Where("lot_id = ?", balance.LotID).Updates(map[string]any{"remaining_quota": balance.RemainingQuota, "remaining_value": balance.RemainingValue, "held_quota": balance.HeldQuota, "held_value": balance.HeldValue, "retired_quota": balance.RetiredQuota, "retired_value": balance.RetiredValue}).Error; err != nil {
		return err
	}
	if err = tx.Model(&CNFundingValueHold{}).Where("operation_id = ? AND state = ?", operationID, "held").Update("state", state).Error; err != nil {
		return err
	}
	return cnValidateFundingValueTx(tx, balance)
}

func cnValidateFundingValueTx(tx *gorm.DB, balance *CNFundingValueLot) error {
	quota := int64(0)
	value := decimal.Zero
	for _, bucket := range []struct {
		quota int64
		value string
	}{{balance.RemainingQuota, balance.RemainingValue}, {balance.HeldQuota, balance.HeldValue}, {balance.RetiredQuota, balance.RetiredValue}} {
		amount, err := cnDecimal(bucket.value)
		if err != nil || amount.IsNegative() || bucket.quota < 0 || bucket.quota > balance.InitialQuota-quota {
			return ErrCNConflict
		}
		quota += bucket.quota
		value = value.Add(amount)
	}
	var allocations []CNFundingValueAllocation
	if err := lockForUpdate(tx).Where("lot_id = ?", balance.LotID).Find(&allocations).Error; err != nil {
		return err
	}
	for _, allocation := range allocations {
		amount, err := cnDecimal(allocation.Value)
		if err != nil || amount.IsNegative() || allocation.Quota < 0 || allocation.Quota > balance.InitialQuota-quota {
			return ErrCNConflict
		}
		quota += allocation.Quota
		value = value.Add(amount)
	}
	paid, err := cnDecimal(balance.PaidValue)
	if err != nil || quota != balance.InitialQuota || !value.Equal(paid) {
		return ErrCNConflict
	}
	return nil
}
