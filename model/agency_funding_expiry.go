package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func agencyFundingLotIsExpired(lot AgencyFundingLot, now int64) bool {
	return agencyFundingSourcePriority(lot.SourceKind) == 0 && lot.ExpiresAt > 0 && lot.ExpiresAt < now
}

func addAgencyTopupExpiredQuotaTx(tx *gorm.DB, lot AgencyFundingLot, amount int64) error {
	if amount <= 0 || !isRedemptionSource(lot.SourceKind) {
		return nil
	}
	return tx.Model(&AgencyTopupFact{}).
		Where("user_id = ? AND source_id = ? AND funding_source IN ?", lot.UserID, lot.SourceID, []string{"redemption", "redeem", "redemption_code"}).
		Updates(map[string]any{"expired_quota": gorm.Expr("expired_quota + ?", amount), "expires_at": lot.ExpiresAt}).Error
}

// restoreAgencyBonusTx reverses a consumed or debt-repaid bonus component. An
// expired redemption remains part of the immutable refund provenance, but it
// moves to BonusExpired instead of becoming spendable again.
func restoreAgencyBonusTx(tx *gorm.DB, lot *AgencyFundingLot, amount int64, fromDebt bool, now int64) (bool, error) {
	if tx == nil || lot == nil || amount <= 0 {
		return false, ErrAgencyComponentRefundProvenance
	}
	updates := map[string]any{"version": lot.Version + 1}
	if fromDebt {
		if lot.BonusDebtRepaid < amount {
			return false, ErrAgencyComponentRefundProvenance
		}
		lot.BonusDebtRepaid -= amount
		updates["bonus_debt_repaid"] = lot.BonusDebtRepaid
	} else {
		if lot.BonusConsumed < amount {
			return false, ErrAgencyComponentRefundProvenance
		}
		lot.BonusConsumed -= amount
		updates["bonus_consumed"] = lot.BonusConsumed
	}
	expired := agencyFundingLotIsExpired(*lot, now)
	if expired {
		if lot.BonusExpired > int64(common.MaxQuota)-amount {
			return false, errors.New("agency redemption expiry balance overflow")
		}
		lot.BonusExpired += amount
		updates["bonus_expired"] = lot.BonusExpired
		if err := addAgencyTopupExpiredQuotaTx(tx, *lot, amount); err != nil {
			return false, err
		}
	} else {
		if lot.BonusAvailable > int64(common.MaxQuota)-amount {
			return false, errors.New("agency funding bonus balance overflow")
		}
		lot.BonusAvailable += amount
		updates["bonus_available"] = lot.BonusAvailable
	}
	lot.Version++
	result := tx.Model(lot).Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, ErrAgencyComponentRefundProvenance
	}
	return expired, nil
}

// expireAgencyRedemptionLotsTx removes unused expired redemption credit from
// both the compatibility wallet and its durable funding projection. The
// redemption deadline is frozen on the lot at redeem time, so later edits to
// the source code cannot extend credit that has already been issued.
func expireAgencyRedemptionLotsTx(tx *gorm.DB, userID, now int64) (int64, error) {
	if tx == nil || userID <= 0 || now <= 0 {
		return 0, ErrAgencyFundingUnavailable
	}
	var user User
	if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
		return 0, err
	}
	if user.BillingMode != AgencyDurableBillingMode {
		return 0, nil
	}
	if err := EnsureAgencyFundingAccount(tx, userID); err != nil {
		return 0, err
	}
	var account AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
		return 0, err
	}
	var lots []AgencyFundingLot
	if err := AgencyLockForUpdate(tx).
		Where("user_id = ? AND source_kind IN ? AND expires_at > 0 AND expires_at < ? AND bonus_available > 0",
			userID, []string{"redemption", "redeem", "redemption_code"}, now).
		Order("expires_at ASC, money_seq ASC, id ASC").Find(&lots).Error; err != nil {
		return 0, err
	}
	if len(lots) == 0 {
		return 0, nil
	}
	var expired int64
	for i := range lots {
		amount := lots[i].BonusAvailable
		if amount <= 0 || lots[i].BonusExpired > int64(common.MaxQuota)-amount || expired > int64(common.MaxQuota)-amount {
			return 0, errors.New("agency redemption expiry balance overflow")
		}
		lots[i].BonusAvailable = 0
		lots[i].BonusExpired += amount
		lots[i].Version++
		if err := tx.Model(&lots[i]).Updates(map[string]any{
			"bonus_available": 0, "bonus_expired": lots[i].BonusExpired, "version": lots[i].Version,
		}).Error; err != nil {
			return 0, err
		}
		expired += amount
		seq := account.MoneySeq + 1
		if seq <= account.MoneySeq {
			return 0, errors.New("agency funding sequence overflow")
		}
		account.MoneySeq = seq
		lotID := lots[i].ID
		operationID := fmt.Sprintf("redemption-expire:%d:%d", lots[i].ID, lots[i].ExpiresAt)
		if err := tx.Create(&AgencyFundingLedger{
			OperationID: operationID, EntryNo: 0, UserID: userID, MoneySeq: seq, SourceKind: "redemption_expired",
			LotID: &lotID, NonpaidDelta: -amount, PaidAfter: account.PaidAvailable,
			NonpaidAfter: account.NonpaidAvailable - expired, DebtAfter: account.DebtQuota, CreatedAtMS: time.Now().UnixMilli(),
		}).Error; err != nil {
			return 0, err
		}
		if err := addAgencyTopupExpiredQuotaTx(tx, lots[i], amount); err != nil {
			return 0, err
		}
	}
	if account.NonpaidAvailable < expired || int64(user.Quota)-expired < -int64(common.MaxQuota)-1 {
		return 0, errors.New("agency redemption expiry balance underflow")
	}
	if err := tx.Model(&User{}).Where("id = ?", userID).Update("quota", gorm.Expr("quota - ?", expired)).Error; err != nil {
		return 0, err
	}
	return expired, tx.Model(&account).Updates(map[string]any{
		"nonpaid_available": account.NonpaidAvailable - expired, "money_seq": account.MoneySeq,
		"version": account.Version + 1, "updated_at": time.Now().Unix(),
	}).Error
}

func expireAgencyRedemptionLotsForUser(userID, now int64) (int64, error) {
	var expired int64
	err := runAgencyFundingTransaction(func(tx *gorm.DB) error {
		var err error
		expired, err = expireAgencyRedemptionLotsTx(tx, userID, now)
		return err
	})
	if err == nil && expired > 0 && common.RedisEnabled {
		if cacheErr := InvalidateUserCache(int(userID)); cacheErr != nil {
			common.SysError("agency redemption expiry cache invalidation failed: " + cacheErr.Error())
		}
	}
	return expired, err
}

// ExpireAgencyRedemptionLots processes a bounded number of affected users.
// Lazy expiry at the spend boundary remains authoritative; this sweep keeps
// displayed balances current even when an account makes no new requests.
func ExpireAgencyRedemptionLots(now int64, limit int) (int, int64, error) {
	if DB == nil || now <= 0 {
		return 0, 0, ErrAgencyFundingUnavailable
	}
	// The master process may start the sweep before Agency Hub's first
	// migration has created its projection tables. Treat that startup window
	// as a no-op; the next tick retries after migration completes.
	if !DB.Migrator().HasTable(&AgencyFundingLot{}) {
		return 0, 0, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var userIDs []int64
	if err := DB.Model(&AgencyFundingLot{}).Distinct("user_id").
		Where("source_kind IN ? AND expires_at > 0 AND expires_at < ? AND bonus_available > 0",
			[]string{"redemption", "redeem", "redemption_code"}, now).
		Order("user_id ASC").Limit(limit).Pluck("user_id", &userIDs).Error; err != nil {
		return 0, 0, err
	}
	var total int64
	for _, userID := range userIDs {
		amount, err := expireAgencyRedemptionLotsForUser(userID, now)
		if err != nil {
			return len(userIDs), total, err
		}
		total += amount
	}
	return len(userIDs), total, nil
}

func isRedemptionSource(sourceKind string) bool {
	switch strings.ToLower(strings.TrimSpace(sourceKind)) {
	case "redemption", "redeem", "redemption_code":
		return true
	default:
		return false
	}
}
