package model

import (
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

func cnWalletRowTx(tx *gorm.DB, request string, user int) (*CNWalletLifecycle, error) {
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
	row := CNWalletLifecycle{RequestID: request, UserID: user, State: "reserved", Version: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Currency: currency, QuotaPerUnit: unit, ExchangeRate: rate, ConversionVersion: CNIdentity("gateway-operation-settings-v1", unit, currency, rate)}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
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
		row, err := cnWalletRowTx(tx, request, user)
		if err != nil {
			return err
		}
		if row.State != "reserved" {
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
		row, err := cnWalletRowTx(tx, request, user)
		if err != nil {
			return err
		}
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
		f := cnBaseFact("charge", "request:"+request, "0", evidence.EvidenceID, "wallet", int64(user), time.Now().UTC(), evidence)
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
	})
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
