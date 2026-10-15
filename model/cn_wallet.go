package model

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This forward-only lane is enabled only when no legacy cache/batch writer can
// race the database balance. It changes persistence, never prices or quota math.
func CNDurableWalletAvailable() bool {
	return CNFinanceCaptureEnabled() && os.Getenv("OPS_CN_DURABLE_WALLET_ENABLED") == "true" && !common.BatchUpdateEnabled && !common.RedisEnabled
}

type CNWalletLifecycle struct {
	RequestID         string `gorm:"size:191;primaryKey"`
	UserID            int    `gorm:"not null;index"`
	Reserved          int64  `gorm:"not null"`
	Actual            int64  `gorm:"not null"`
	State             string `gorm:"size:32;not null;index"`
	Version           int64  `gorm:"not null"`
	Currency          string `gorm:"size:3"`
	QuotaPerUnit      string `gorm:"size:128"`
	ConversionVersion string `gorm:"size:191"`
	ExchangeRate      string `gorm:"size:128"`
	CreatedAt         string `gorm:"size:40;not null"`
}

func cnWalletRowTx(tx *gorm.DB, request string, user int, create bool) (*CNWalletLifecycle, error) {
	if request == "" || len(request) > 191 || user <= 0 {
		return nil, errors.New("invalid wallet identity")
	}
	// Acquire the writer lock before the read snapshot on SQLite, and the same
	// user lock used by atomic task settlement on MySQL/PostgreSQL.
	if err := tx.Model(&User{}).Where("id = ?", user).Update("quota", gorm.Expr("quota + 0")).Error; err != nil {
		return nil, err
	}
	var account User
	if err := lockForUpdate(tx).Select("id", "quota", "billing_mode").First(&account, user).Error; err != nil {
		return nil, err
	}
	if account.BillingMode == AgencyDurableBillingMode || account.BillingMode == AgencyProvisioningBillingMode {
		return nil, ErrAgencyFundingUnavailable
	}
	unit := strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
	currency := operation_setting.GetQuotaDisplayType()
	if currency != "USD" && currency != "CNY" {
		currency = ""
	}
	rate := strconv.FormatFloat(operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate), 'f', -1, 64)
	row := CNWalletLifecycle{RequestID: request, UserID: user, State: "reserved", Version: 1, CreatedAt: time.Now().UTC().Format(CNTimeLayout), Currency: currency, QuotaPerUnit: unit, ExchangeRate: rate, ConversionVersion: CNIdentity("gateway-operation-settings-v1", unit, currency, rate)}
	if create {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return nil, err
		}
	}
	if err := lockForUpdate(tx).Where("request_id = ?", request).First(&row).Error; err != nil {
		return nil, err
	}
	if row.UserID != user {
		return nil, ErrCNConflict
	}
	return &row, nil
}

func ReserveCNWallet(request string, user, target int, requireAvailable bool) (bool, error) {
	if target < 0 || int64(target) > int64(common.MaxQuota) {
		return false, errors.New("invalid wallet reserve")
	}
	reserved := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnWalletRowTx(tx, request, user, true)
		if err != nil {
			return err
		}
		if row.State != "reserved" {
			return ErrCNConflict
		}
		var intentCount int64
		if err = tx.Model(&CNWalletCancellation{}).Where("request_id = ?", request).Count(&intentCount).Error; err != nil {
			return err
		}
		if intentCount != 0 {
			return ErrCNConflict
		}
		delta := int64(target) - row.Reserved
		if delta != 0 {
			query := tx.Model(&User{}).Where("id = ?", user)
			if delta > 0 && requireAvailable {
				query = query.Where("quota >= ?", delta)
			}
			if delta < 0 {
				query = query.Where("quota <= ?", common.MaxWalletQuota+delta)
			}
			result := query.Update("quota", gorm.Expr("quota - ?", delta))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrInsufficientAgencyWalletQuota
			}
			if err = tx.Model(&CNWalletLifecycle{}).Where("request_id = ? AND version = ?", request, row.Version).Updates(map[string]any{"reserved": target, "version": row.Version + 1}).Error; err != nil {
				return err
			}
		}
		reserved = true
		return nil
	})
	if errors.Is(err, ErrInsufficientAgencyWalletQuota) {
		return false, nil
	}
	return reserved, err
}

func FinalizeCNWallet(request string, user, actual int, cancelled bool) error {
	if actual < 0 || int64(actual) > int64(common.MaxQuota) || (cancelled && actual != 0) {
		return errors.New("invalid wallet final amount")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var intentCount int64
		row, err := cnWalletRowTx(tx, request, user, false)
		if err != nil {
			return err
		}
		if err = tx.Model(&CNWalletCancellation{}).Where("request_id = ?", request).Count(&intentCount).Error; err != nil {
			return err
		}
		if intentCount != 0 {
			return ErrCNConflict
		}
		return finalizeCNWalletTx(tx, row, actual, cancelled, time.Now().UTC())
	})
}

func finalizeCNWalletTx(tx *gorm.DB, row *CNWalletLifecycle, actual int, cancelled bool, occurred time.Time) error {
	request, user := row.RequestID, row.UserID
	var err error
	state := "settled"
	if cancelled {
		state = "cancelled"
	}
	if row.State != "reserved" {
		if row.Actual == int64(actual) && row.State == state {
			return nil
		}
		return ErrCNConflict
	}
	delta := int64(actual) - row.Reserved
	if delta != 0 {
		query := tx.Model(&User{}).Where("id = ?", user)
		if delta < 0 {
			query = query.Where("quota <= ?", common.MaxWalletQuota+delta)
		}
		result := query.Update("quota", gorm.Expr("quota - ?", delta))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWalletQuotaLimitExceeded
		}
	}
	evidence := CNEvidence{System: "gateway-cn", RecordType: "cn_wallet_lifecycle_final", RecordID: request, EvidenceID: CNIdentity("wallet-final", request, state, strconv.Itoa(actual))}
	f := cnBaseFact("charge", "request:"+request, "0", evidence.EvidenceID, "wallet", int64(user), occurred, evidence)
	f.RequestID = &request
	f.Status = "settled"
	f.Quality = "settled_quota"
	f.EffectiveAt = row.CreatedAt
	f.SourcePeriod = row.CreatedAt[:7]
	if _, err = cnLockPublisher(tx); err != nil {
		return err
	}
	// The exact configured nominal conversion is frozen at reservation. A missing
	// conversion leaves actual quota known and money unknown; it is never income.
	if row.ConversionVersion != "" && opsMoneyCurrency.MatchString(row.Currency) {
		f.Conversion = map[string]any{"sourceUnit": "quota", "quotaPerUnit": row.QuotaPerUnit, "originalCurrency": "USD", "settlementCurrency": row.Currency, "exchangeRate": row.ExchangeRate, "fxVersion": row.ConversionVersion, "rounding": "HALF_EVEN_18"}
		f.Cumulative.Money = cnQuotaMoney(int64(actual), f.Conversion)
		f.SourceEvidence = append(f.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "gateway_operation_currency_snapshot", RecordID: row.ConversionVersion, EvidenceID: row.ConversionVersion})
	}
	if err = cnStageChargeSnapshot(tx, &f, int64(actual)); err != nil {
		return err
	}
	result := tx.Model(&CNWalletLifecycle{}).Where("request_id = ? AND version = ?", request, row.Version).Updates(map[string]any{"actual": actual, "state": state, "version": row.Version + 1})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCNConflict
	}
	return nil
}

// Only an explicit terminal decision authorizes recovery. Active reservations
// have no expiry and are never selected by the cancellation worker.
type CNWalletCancellation struct {
	RequestID     string `gorm:"size:191;primaryKey"`
	UserID        int    `gorm:"not null"`
	TokenID       int    `gorm:"not null"`
	TokenQuota    int64  `gorm:"not null"`
	State         string `gorm:"size:32;not null;index"`
	Attempts      int64  `gorm:"not null"`
	NextAttemptAt int64  `gorm:"not null;index"`
	LastErrorCode string `gorm:"size:64"`
	RequestedAt   string `gorm:"size:40;not null"`
	CompletedAt   string `gorm:"size:40"`
}

func RequestCNWalletCancellation(db *gorm.DB, request string, user, token, quota int) error {
	if quota < 0 || quota > common.MaxQuota || (quota > 0 && token <= 0) {
		return ErrCNConflict
	}
	return db.Transaction(func(tx *gorm.DB) error {
		row, err := cnWalletRowTx(tx, request, user, false)
		if err != nil {
			return err
		}
		var prior CNWalletCancellation
		err = tx.Where("request_id = ?", request).First(&prior).Error
		if err == nil {
			if prior.UserID != user || prior.TokenID != token || prior.TokenQuota != int64(quota) {
				return ErrCNConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if row.State != "reserved" {
			return ErrCNConflict
		}
		return tx.Create(&CNWalletCancellation{RequestID: request, UserID: user, TokenID: token, TokenQuota: int64(quota), State: "pending", RequestedAt: time.Now().UTC().Format(CNTimeLayout)}).Error
	})
}

func CompleteCNWalletCancellation(ctx context.Context, db *gorm.DB, request string) error {
	var identity CNWalletCancellation
	if err := db.WithContext(ctx).Where("request_id = ?", request).First(&identity).Error; err != nil {
		return err
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := cnWalletRowTx(tx, request, identity.UserID, false)
		if err != nil {
			return err
		}
		var intent CNWalletCancellation
		if err = lockForUpdate(tx).Where("request_id = ?", request).First(&intent).Error; err != nil {
			return err
		}
		if intent.State == "complete" {
			return nil
		}
		occurred, err := time.Parse(CNTimeLayout, intent.RequestedAt)
		if err != nil {
			return err
		}
		// Lock token before the publisher, in the same order on every retry.
		if intent.TokenQuota > 0 {
			var token Token
			if err = lockForUpdate(tx).Where("id = ? AND user_id = ?", intent.TokenID, intent.UserID).First(&token).Error; err != nil {
				return err
			}
			result := tx.Model(&Token{}).Where("id = ? AND user_id = ? AND remain_quota <= ?", intent.TokenID, intent.UserID, common.MaxWalletQuota-intent.TokenQuota).Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota + ?", intent.TokenQuota), "used_quota": gorm.Expr("used_quota - ?", intent.TokenQuota), "accessed_time": common.GetTimestamp()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrCNConflict
			}
		}
		if err = finalizeCNWalletTx(tx, row, 0, true, occurred); err != nil {
			return err
		}
		return tx.Model(&CNWalletCancellation{}).Where("request_id = ?", request).Updates(map[string]any{"state": "complete", "completed_at": time.Now().UTC().Format(CNTimeLayout), "last_error_code": ""}).Error
	})
	if err != nil {
		// Persist only a stable code: database errors can contain private values.
		failure := db.WithContext(context.WithoutCancel(ctx)).Model(&CNWalletCancellation{}).Where("request_id = ? AND state = ?", request, "pending").Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_attempt_at": time.Now().Unix() + 5, "last_error_code": "cancellation_transaction_failed"}).Error
		return errors.Join(err, failure)
	}
	return nil
}

func RecoverCNWalletCancellations(ctx context.Context, db *gorm.DB, limit int) error {
	var intents []CNWalletCancellation
	if err := db.WithContext(ctx).Where("state = ? AND next_attempt_at <= ?", "pending", time.Now().Unix()).Order("next_attempt_at, requested_at, request_id").Limit(min(max(limit, 1), 1000)).Find(&intents).Error; err != nil {
		return err
	}
	var failures []error
	for _, intent := range intents {
		if err := CompleteCNWalletCancellation(ctx, db, intent.RequestID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func RunCNWalletCancellationRecovery(ctx context.Context, db *gorm.DB) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := RecoverCNWalletCancellations(ctx, db, 100); err != nil {
			common.SysError("CN wallet cancellation recovery requires repair")
		}
		if err := RecoverCNSubscriptionCancellations(ctx, db, 100); err != nil {
			common.SysError("CN subscription cancellation recovery requires repair")
		}
		if err := RecoverCNDirectWalletCancellations(ctx, db, 100); err != nil {
			common.SysError("CN direct wallet cancellation recovery requires repair")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func cnQuotaMoney(quota int64, conversion map[string]any) *CNMoney {
	unitText, _ := conversion["quotaPerUnit"].(string)
	rateText, _ := conversion["exchangeRate"].(string)
	unit, e1 := cnDecimal(unitText)
	rate, e2 := cnDecimal(rateText)
	if e1 != nil || e2 != nil || !unit.IsPositive() || !rate.IsPositive() {
		return nil
	}
	currency, _ := conversion["settlementCurrency"].(string)
	originalCurrency, _ := conversion["originalCurrency"].(string)
	if !opsMoneyCurrency.MatchString(currency) || !opsMoneyCurrency.MatchString(originalCurrency) {
		return nil
	}
	q := decimal.NewFromInt(quota)
	m := cnUnknownTaxMoney(cnDivideHalfEven(q.Mul(rate), unit).String(), currency)
	m.OriginalCurrency = originalCurrency
	m.OriginalAmount = cnDivideHalfEven(q, unit).String()
	return m
}
