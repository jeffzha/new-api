package model

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

func CNDurableSubscriptionAvailable() bool {
	return CNFinanceCaptureEnabled() && os.Getenv("OPS_CN_DURABLE_SUBSCRIPTION_ENABLED") == "true" && !common.RedisEnabled && !common.BatchUpdateEnabled
}

// This permanent request receipt is independent of the legacy preconsume record
// cleanup. Reserved is an absolute target; Actual only becomes final in the
// same transaction as subscription/token quota and the immutable CN outbox.
type CNSubscriptionLifecycle struct {
	RequestID             string `gorm:"size:64;primaryKey"`
	UserID                int    `gorm:"not null;index"`
	SubscriptionID        int    `gorm:"not null;index"`
	UsageGeneration       int64  `gorm:"not null;default:0"`
	PeriodBound           bool   `gorm:"not null;default:false"`
	TokenAdjustmentStatus string `gorm:"size:40"`
	TokenDisposedQuota    int64  `gorm:"not null;default:0"`
	InitialReserve        int64  `gorm:"not null"`
	Reserved              int64  `gorm:"not null"`
	Actual                int64  `gorm:"not null"`
	TokenID               int    `gorm:"not null"`
	TokenUnlimited        bool   `gorm:"not null"`
	Playground            bool   `gorm:"not null"`
	State                 string `gorm:"size:32;not null;index"`
	Version               int64  `gorm:"not null"`
	Currency              string `gorm:"size:3"`
	QuotaPerUnit          string `gorm:"size:128"`
	ExchangeRate          string `gorm:"size:128"`
	ConversionVersion     string `gorm:"size:191"`
	CreatedAt             string `gorm:"size:40;not null"`
	FinalizedAt           string `gorm:"size:40"`
}

type CNSubscriptionCancellation CNWalletCancellation

type CNSubscriptionPeriod struct {
	SubscriptionID int   `gorm:"primaryKey;autoIncrement:false"`
	Generation     int64 `gorm:"primaryKey;autoIncrement:false"`
	AmountUsed     int64 `gorm:"not null"`
	AmountTotal    int64 `gorm:"not null"`
}

var ErrCNSubscriptionPeriodMissing = errors.New("subscription_period_identity_missing")

func cnLockSubscriptionUsersTx(tx *gorm.DB, users []int) error {
	if !tx.Migrator().HasTable(&CNSubscriptionPeriod{}) {
		return nil
	}
	slices.Sort(users)
	for _, user := range slices.Compact(users) {
		if err := tx.Model(&User{}).Where("id = ?", user).Update("quota", gorm.Expr("quota + 0")).Error; err != nil {
			return err
		}
	}
	return nil
}

func cnFreezeSubscriptionPeriodTx(tx *gorm.DB, sub *UserSubscription) error {
	if !tx.Migrator().HasTable(&CNSubscriptionPeriod{}) {
		return nil
	}
	return tx.Create(&CNSubscriptionPeriod{SubscriptionID: sub.Id, Generation: sub.UsageGeneration, AmountUsed: sub.AmountUsed, AmountTotal: sub.AmountTotal}).Error
}

func cnSubscriptionDeltaTx(tx *gorm.DB, row *CNSubscriptionLifecycle, delta int64) error {
	if !row.PeriodBound {
		return ErrCNSubscriptionPeriodMissing
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).First(&sub, row.SubscriptionID).Error; err != nil {
		return err
	}
	if sub.UserId != row.UserID {
		return ErrCNConflict
	}
	if sub.UsageGeneration == row.UsageGeneration {
		used := sub.AmountUsed + delta
		if used < 0 || used > int64(common.MaxWalletQuota) || (sub.AmountTotal > 0 && used > sub.AmountTotal) {
			return ErrWalletQuotaLimitExceeded
		}
		return tx.Model(&sub).Update("amount_used", used).Error
	}
	var period CNSubscriptionPeriod
	if err := lockForUpdate(tx).Where("subscription_id = ? AND generation = ?", row.SubscriptionID, row.UsageGeneration).First(&period).Error; err != nil {
		return errors.Join(ErrCNSubscriptionPeriodMissing, err)
	}
	used := period.AmountUsed + delta
	if used < 0 || used > int64(common.MaxWalletQuota) || (period.AmountTotal > 0 && used > period.AmountTotal) {
		return ErrWalletQuotaLimitExceeded
	}
	return tx.Model(&period).Update("amount_used", used).Error
}

func cnLockSubscriptionUserTx(tx *gorm.DB, request string, user int) error {
	if request == "" || len(request) > 64 || user <= 0 {
		return ErrCNConflict
	}
	if err := tx.Model(&User{}).Where("id = ?", user).Update("quota", gorm.Expr("quota + 0")).Error; err != nil {
		return err
	}
	var account User
	if err := lockForUpdate(tx).Select("id", "billing_mode").First(&account, user).Error; err != nil {
		return err
	}
	if account.BillingMode == AgencyDurableBillingMode || account.BillingMode == AgencyProvisioningBillingMode {
		return ErrAgencyFundingUnavailable
	}
	return nil
}

func cnSubscriptionRowTx(tx *gorm.DB, request string, user int) (*CNSubscriptionLifecycle, error) {
	if err := cnLockSubscriptionUserTx(tx, request, user); err != nil {
		return nil, err
	}
	var row CNSubscriptionLifecycle
	if err := lockForUpdate(tx).Where("request_id = ?", request).First(&row).Error; err != nil {
		return nil, err
	}
	if row.UserID != user {
		return nil, ErrCNConflict
	}
	return &row, nil
}

func cnSubscriptionTokenTx(tx *gorm.DB, row *CNSubscriptionLifecycle, delta int64, requireAvailable bool) error {
	if row.Playground || delta == 0 {
		return nil
	}
	var token Token
	err := lockForUpdate(tx.Unscoped()).Where("id = ?", row.TokenID).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && delta < 0 {
		row.TokenAdjustmentStatus = "missing_token_not_recredited"
		row.TokenDisposedQuota += -delta
		return nil
	}
	if err != nil {
		return err
	}
	if token.UserId != row.UserID {
		return ErrCNConflict
	}
	if token.DeletedAt.Valid {
		if delta < 0 {
			row.TokenAdjustmentStatus = "deleted_token_not_recredited"
			row.TokenDisposedQuota += -delta
			return nil
		}
		return ErrCNConflict
	}
	remain, used := int64(token.RemainQuota)-delta, int64(token.UsedQuota)+delta
	if requireAvailable && !row.TokenUnlimited && remain < 0 {
		return ErrInsufficientAgencyTokenQuota
	}
	if remain < -int64(common.MaxWalletQuota) || remain > int64(common.MaxWalletQuota) || used < -int64(common.MaxWalletQuota) || used > int64(common.MaxWalletQuota) {
		return ErrWalletQuotaLimitExceeded
	}
	result := tx.Model(&Token{}).Where("id = ? AND user_id = ?", row.TokenID, row.UserID).Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": common.GetTimestamp()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCNConflict
	}
	return nil
}

func PreConsumeCNSubscription(request string, user, amount, token int, unlimited, playground bool) (*SubscriptionPreConsumeResult, error) {
	if amount <= 0 || amount > common.MaxQuota || (!playground && token <= 0) {
		return nil, ErrCNConflict
	}
	var result *SubscriptionPreConsumeResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := cnLockSubscriptionUserTx(tx, request, user); err != nil {
			return err
		}
		var row CNSubscriptionLifecycle
		err := lockForUpdate(tx).Where("request_id = ?", request).First(&row).Error
		if err == nil {
			if row.UserID != user || row.InitialReserve != int64(amount) || row.TokenID != token || row.TokenUnlimited != unlimited || row.Playground != playground || row.State != "reserved" {
				return ErrCNConflict
			}
			var pending int64
			if err = tx.Model(&CNSubscriptionCancellation{}).Where("request_id = ?", request).Count(&pending).Error; err != nil {
				return err
			}
			if pending > 0 {
				return ErrCNConflict
			}
			var sub UserSubscription
			if err = lockForUpdate(tx).First(&sub, row.SubscriptionID).Error; err != nil {
				return err
			}
			result = &SubscriptionPreConsumeResult{UserSubscriptionId: row.SubscriptionID, PreConsumed: row.Reserved, AmountTotal: sub.AmountTotal, AmountUsedBefore: sub.AmountUsed, AmountUsedAfter: sub.AmountUsed}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var legacy int64
		if err = tx.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", request).Count(&legacy).Error; err != nil {
			return err
		}
		if legacy > 0 {
			return ErrCNConflict
		}
		result, err = preConsumeUserSubscriptionTx(tx, request, user, int64(amount), now)
		if err != nil {
			return err
		}
		var sub UserSubscription
		if err = lockForUpdate(tx).First(&sub, result.UserSubscriptionId).Error; err != nil {
			return err
		}
		unit := strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
		currency := operation_setting.GetQuotaDisplayType()
		if currency != "USD" && currency != "CNY" {
			currency = ""
		}
		rate := strconv.FormatFloat(operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate), 'f', -1, 64)
		row = CNSubscriptionLifecycle{RequestID: request, UserID: user, SubscriptionID: result.UserSubscriptionId, InitialReserve: int64(amount), Reserved: int64(amount), TokenID: token, TokenUnlimited: unlimited, Playground: playground, State: "reserved", Version: 1, Currency: currency, QuotaPerUnit: unit, ExchangeRate: rate, ConversionVersion: CNIdentity("gateway-operation-settings-v1", unit, currency, rate), CreatedAt: time.Now().UTC().Format(CNTimeLayout)}
		row.UsageGeneration, row.PeriodBound = sub.UsageGeneration, true
		if err = cnSubscriptionTokenTx(tx, &row, int64(amount), true); err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
	return result, err
}

func ReserveCNSubscription(request string, user, target int) error {
	if target < 0 || target > common.MaxQuota {
		return ErrCNConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnSubscriptionRowTx(tx, request, user)
		if err != nil {
			return err
		}
		if row.State != "reserved" {
			return ErrCNConflict
		}
		var pending int64
		if err = tx.Model(&CNSubscriptionCancellation{}).Where("request_id = ?", request).Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return ErrCNConflict
		}
		delta := int64(target) - row.Reserved
		if delta == 0 {
			return nil
		}
		if err = cnSubscriptionDeltaTx(tx, row, delta); err != nil {
			return err
		}
		if err = cnSubscriptionTokenTx(tx, row, delta, delta > 0); err != nil {
			return err
		}
		result := tx.Model(&CNSubscriptionLifecycle{}).Where("request_id = ? AND version = ?", request, row.Version).Updates(map[string]any{"reserved": target, "version": row.Version + 1, "token_adjustment_status": row.TokenAdjustmentStatus, "token_disposed_quota": row.TokenDisposedQuota})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCNConflict
		}
		return nil
	})
}

func finalizeCNSubscriptionTx(tx *gorm.DB, row *CNSubscriptionLifecycle, actual int, cancelled bool, occurred time.Time) error {
	state := "settled"
	if cancelled {
		state = "cancelled"
	}
	if row.State != "reserved" {
		if row.State == state && row.Actual == int64(actual) {
			return nil
		}
		return ErrCNConflict
	}
	delta := int64(actual) - row.Reserved
	if err := cnSubscriptionDeltaTx(tx, row, delta); err != nil {
		return err
	}
	if err := cnSubscriptionTokenTx(tx, row, delta, false); err != nil {
		return err
	}
	evidence := CNEvidence{System: "gateway-cn", RecordType: "cn_subscription_lifecycle_final", RecordID: row.RequestID, EvidenceID: CNIdentity("subscription-final", row.RequestID, state, strconv.Itoa(actual))}
	fact := cnBaseFact("charge", "request:"+row.RequestID, "0", evidence.EvidenceID, "subscription", int64(row.UserID), occurred, evidence)
	fact.RequestID = &row.RequestID
	fact.Status = "settled"
	fact.Quality = "settled_quota"
	fact.EffectiveAt = row.CreatedAt
	fact.SourcePeriod = row.CreatedAt[:7]
	if opsMoneyCurrency.MatchString(row.Currency) {
		fact.Conversion = map[string]any{"sourceUnit": "quota", "quotaPerUnit": row.QuotaPerUnit, "originalCurrency": "USD", "settlementCurrency": row.Currency, "exchangeRate": row.ExchangeRate, "fxVersion": row.ConversionVersion, "rounding": "HALF_EVEN_18"}
		fact.Cumulative.Money = cnQuotaMoney(int64(actual), fact.Conversion)
		fact.SourceEvidence = append(fact.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "gateway_operation_currency_snapshot", RecordID: row.ConversionVersion, EvidenceID: row.ConversionVersion})
	}
	if _, err := cnLockPublisher(tx); err != nil {
		return err
	}
	if err := cnStageChargeSnapshot(tx, &fact, int64(actual)); err != nil {
		return err
	}
	// The legacy record is optional after cleanup; this permanent lifecycle is
	// the authority for idempotency. No financial receipt is deleted by cleanup.
	if cancelled {
		if err := tx.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", row.RequestID).Update("status", "refunded").Error; err != nil {
			return err
		}
	}
	result := tx.Model(&CNSubscriptionLifecycle{}).Where("request_id = ? AND version = ?", row.RequestID, row.Version).Updates(map[string]any{"state": state, "actual": actual, "version": row.Version + 1, "finalized_at": occurred.UTC().Format(CNTimeLayout), "token_adjustment_status": row.TokenAdjustmentStatus, "token_disposed_quota": row.TokenDisposedQuota})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCNConflict
	}
	return nil
}

func FinalizeCNSubscription(request string, user, actual int) error {
	if actual < 0 || actual > common.MaxQuota {
		return ErrCNConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnSubscriptionRowTx(tx, request, user)
		if err != nil {
			return err
		}
		var pending int64
		if err = tx.Model(&CNSubscriptionCancellation{}).Where("request_id = ?", request).Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return ErrCNConflict
		}
		return finalizeCNSubscriptionTx(tx, row, actual, false, time.Now().UTC())
	})
}

func RequestCNSubscriptionCancellation(db *gorm.DB, request string, user int) error {
	return db.Transaction(func(tx *gorm.DB) error {
		row, err := cnSubscriptionRowTx(tx, request, user)
		if err != nil {
			return err
		}
		var prior CNSubscriptionCancellation
		err = tx.Where("request_id = ?", request).First(&prior).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if row.State != "reserved" {
			return ErrCNConflict
		}
		return tx.Create(&CNSubscriptionCancellation{RequestID: request, UserID: user, TokenID: row.TokenID, TokenQuota: row.Reserved, State: "pending", RequestedAt: time.Now().UTC().Format(CNTimeLayout)}).Error
	})
}

func CompleteCNSubscriptionCancellation(ctx context.Context, db *gorm.DB, request string) error {
	var identity CNSubscriptionCancellation
	if err := db.WithContext(ctx).Where("request_id = ?", request).First(&identity).Error; err != nil {
		return err
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := cnSubscriptionRowTx(tx, request, identity.UserID)
		if err != nil {
			return err
		}
		var intent CNSubscriptionCancellation
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
		if err = finalizeCNSubscriptionTx(tx, row, 0, true, occurred); err != nil {
			return err
		}
		return tx.Model(&CNSubscriptionCancellation{}).Where("request_id = ?", request).Updates(map[string]any{"state": "complete", "completed_at": time.Now().UTC().Format(CNTimeLayout), "last_error_code": ""}).Error
	})
	if err != nil {
		failure := db.WithContext(context.WithoutCancel(ctx)).Model(&CNSubscriptionCancellation{}).Where("request_id = ? AND state = ?", request, "pending").Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_attempt_at": time.Now().Unix() + 5, "last_error_code": "cancellation_transaction_failed"}).Error
		return errors.Join(err, failure)
	}
	return nil
}

func RecoverCNSubscriptionCancellations(ctx context.Context, db *gorm.DB, limit int) error {
	var intents []CNSubscriptionCancellation
	if err := db.WithContext(ctx).Where("state = ? AND next_attempt_at <= ?", "pending", time.Now().Unix()).Order("next_attempt_at, requested_at, request_id").Limit(min(max(limit, 1), 1000)).Find(&intents).Error; err != nil {
		return err
	}
	var failures []error
	for _, intent := range intents {
		if err := CompleteCNSubscriptionCancellation(ctx, db, intent.RequestID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
