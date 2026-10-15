package model

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

func CNDirectWalletRequested() bool {
	return os.Getenv("OPS_CN_DURABLE_DIRECT_WALLET_ENABLED") == "true"
}

func CNDirectWalletAvailable() bool { return CNDirectWalletRequested() && CNDurableWalletAvailable() }

// Permanent receipts; neither request completion nor legacy cleanup deletes them.
type CNDirectWallet struct {
	RequestID             string `gorm:"size:191;primaryKey"`
	UserID                int    `gorm:"not null;index"`
	TokenID               int    `gorm:"not null"`
	TokenUnlimited        bool   `gorm:"not null;default:false"`
	TokenBindingVersion   int    `gorm:"not null;default:0"`
	TokenAdjustmentStatus string `gorm:"size:40"`
	TokenDisposedQuota    int64  `gorm:"not null;default:0"`
	UsageContract         string `gorm:"size:64"`
	Kind                  string `gorm:"size:32;not null"`
	ChannelID             int    `gorm:"not null"`
	InitialReserve        int64  `gorm:"not null"`
	Reserved              int64  `gorm:"not null"`
	SegmentQuota          int64  `gorm:"not null"`
	Actual                int64  `gorm:"not null"`
	Version               int64  `gorm:"not null"`
	State                 string `gorm:"size:32;not null;index"`
	UsageJSON             string `gorm:"type:text"`
	ConversionJSON        string `gorm:"type:text"`
	CreatedAt             string `gorm:"size:40;not null"`
	FinalizedAt           string `gorm:"size:40"`
	FinalHash             string `gorm:"size:64"`
	CancelRequestedAt     string `gorm:"size:40"`
	LastErrorCode         string `gorm:"size:64"`
	CoverageGap           string `gorm:"size:64"`
}

type CNDirectWalletSegment struct {
	ResponseID string `gorm:"size:191"`
	Key        string `gorm:"size:64;primaryKey"`
	RequestID  string `gorm:"size:191;not null;index"`
	SegmentID  string `gorm:"size:64;not null"`
	UsageJSON  string `gorm:"type:text;not null"`
	Quota      int64  `gorm:"not null"`
	Target     int64  `gorm:"not null"`
	Version    int64  `gorm:"not null"`
	CreatedAt  string `gorm:"size:40;not null"`
}

func cnDirectRowTx(tx *gorm.DB, request string, user int) (*CNDirectWallet, error) {
	if err := cnLockSubscriptionUserTx(tx, request, user); err != nil {
		return nil, err
	}
	var row CNDirectWallet
	if err := lockForUpdate(tx).Where("request_id = ?", request).First(&row).Error; err != nil {
		return nil, err
	}
	if row.UserID != user {
		return nil, ErrCNConflict
	}
	return &row, nil
}

// Begin precedes upstream I/O. Existing requests never acquire today's conversion.
func BeginCNDirectWallet(request string, user, token, channel, initial int) error {
	return beginCNDirectWallet(request, user, token, channel, initial, "realtime")
}

func BeginCNDirectRelayWallet(request string, user, token, channel int) error {
	return beginCNDirectWallet(request, user, token, channel, 0, "relay")
}

func beginCNDirectWallet(request string, user, token, channel, initial int, kind string) error {
	if !CNDirectWalletAvailable() || request == "" || len(request) > 64 || token <= 0 || channel <= 0 || initial < 0 || initial > common.MaxQuota {
		return ErrCNConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := cnLockSubscriptionUserTx(tx, request, user); err != nil {
			return err
		}
		var row CNDirectWallet
		err := tx.Where("request_id = ?", request).First(&row).Error
		if err == nil {
			if row.UserID != user || row.TokenID != token || row.ChannelID != channel || row.Kind != kind || row.InitialReserve != int64(initial) || row.State != "reserved" || row.TokenBindingVersion != 1 || (kind == "realtime" && row.UsageContract != "openai-response-done-v1") {
				return ErrCNConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		for _, table := range []any{&CNWalletLifecycle{}, &CNSubscriptionLifecycle{}} {
			var count int64
			if err = tx.Model(table).Where("request_id = ?", request).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return ErrCNConflict
			}
		}
		var credential Token
		if err = lockForUpdate(tx).Where("id = ? AND user_id = ? AND status = ?", token, user, common.TokenStatusEnabled).First(&credential).Error; err != nil {
			return err
		}
		var source Channel
		if err = tx.Select("id").First(&source, channel).Error; err != nil {
			return err
		}
		unit := strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
		currency := operation_setting.GetQuotaDisplayType()
		rate := strconv.FormatFloat(operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate), 'f', -1, 64)
		conversion := map[string]any{"sourceUnit": "quota", "quotaPerUnit": unit, "originalCurrency": "USD", "settlementCurrency": currency, "exchangeRate": rate, "fxVersion": CNIdentity("gateway-operation-settings-v1", unit, currency, rate), "rounding": "HALF_EVEN_18"}
		var frozen string
		if (currency == "USD" || currency == "CNY") && cnQuotaMoney(0, conversion) != nil {
			raw, err := common.Marshal(conversion)
			if err != nil {
				return err
			}
			frozen = string(raw)
		}
		row = CNDirectWallet{RequestID: request, UserID: user, TokenID: token, TokenUnlimited: credential.UnlimitedQuota, TokenBindingVersion: 1, ChannelID: channel, Kind: kind, InitialReserve: int64(initial), Reserved: int64(initial), State: "reserved", Version: 1, ConversionJSON: frozen, CreatedAt: time.Now().UTC().Format(CNTimeLayout)}
		if kind == "realtime" {
			row.UsageContract = "openai-response-done-v1"
		}
		if err = cnDirectQuotaTx(tx, &row, int64(initial), true); err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
}

func cnDirectQuotaTx(tx *gorm.DB, row *CNDirectWallet, delta int64, requireAvailable bool) error {
	var user User
	if err := tx.Select("id", "quota", "status").First(&user, row.UserID).Error; err != nil {
		return err
	}
	if delta == 0 && !requireAvailable {
		return nil
	}
	if row.TokenBindingVersion != 1 && (delta > 0 || requireAvailable) {
		return ErrCNWalletBindingRequired
	}
	var token Token
	err := lockForUpdate(tx.Unscoped()).Where("id = ?", row.TokenID).First(&token).Error
	status := ""
	if errors.Is(err, gorm.ErrRecordNotFound) && delta < 0 {
		status = "missing_token_not_recredited"
	} else if err != nil {
		return err
	} else if token.UserId != row.UserID {
		return ErrCNConflict
	} else if token.DeletedAt.Valid {
		if delta >= 0 {
			return ErrCNConflict
		}
		status = "deleted_token_not_recredited"
	}
	if status == "" && row.TokenBindingVersion == 1 && token.UnlimitedQuota != row.TokenUnlimited {
		return ErrCNConflict
	}
	if (requireAvailable || delta > 0) && (user.Status != common.UserStatusEnabled || token.Status != common.TokenStatusEnabled || (token.ExpiredTime > 0 && token.ExpiredTime < common.GetTimestamp())) {
		return ErrCNConflict
	}
	if delta == 0 {
		return nil
	}
	remaining, tokenRemaining, tokenUsed := int64(user.Quota)-delta, int64(token.RemainQuota)-delta, int64(token.UsedQuota)+delta
	if requireAvailable && (remaining < 0 || (!token.UnlimitedQuota && tokenRemaining < 0)) {
		return ErrInsufficientAgencyWalletQuota
	}
	for _, amount := range []int64{remaining, tokenRemaining, tokenUsed} {
		if amount < -int64(common.MaxWalletQuota) || amount > int64(common.MaxWalletQuota) {
			return ErrWalletQuotaLimitExceeded
		}
	}
	if err := tx.Model(&User{}).Where("id = ?", row.UserID).Update("quota", remaining).Error; err != nil {
		return err
	}
	if status != "" {
		row.TokenAdjustmentStatus = status
		row.TokenDisposedQuota += -delta
		return tx.Model(&CNDirectWallet{}).Where("request_id = ?", row.RequestID).Updates(map[string]any{"token_adjustment_status": status, "token_disposed_quota": row.TokenDisposedQuota}).Error
	}
	result := tx.Model(&Token{}).Where("id = ? AND user_id = ?", row.TokenID, row.UserID).Updates(map[string]any{"remain_quota": tokenRemaining, "used_quota": tokenUsed, "accessed_time": common.GetTimestamp()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCNConflict
	}
	return nil
}

func ReadCNDirectWallet(request string, user int) (*CNDirectWallet, error) {
	var row CNDirectWallet
	err := DB.Where("request_id = ? AND user_id = ?", request, user).First(&row).Error
	return &row, err
}

func ReadCNDirectWalletResponse(request, response string) (*CNDirectWalletSegment, error) {
	var receipt CNDirectWalletSegment
	err := DB.Where(map[string]any{"key": CNIdentity(request, CNIdentity("openai-response-done-v1", response))}).First(&receipt).Error
	return &receipt, err
}

func MarkCNDirectWalletUsageGap(request string, user int) error {
	return DB.Model(&CNDirectWallet{}).Where("request_id = ? AND user_id = ?", request, user).Update("coverage_gap", "unidentified_or_uncommitted_usage").Error
}

// The caller calculates only a new live segment. A replay returns its original
// absolute target even if the process, pricing configuration or head changed.
func ApplyCNDirectWalletResponse(request string, user int, expected int64, response, usage, total string, quota int) (*CNDirectWalletSegment, error) {
	if !CNDirectWalletAvailable() || strings.TrimSpace(response) == "" || len(response) > 191 || usage == "" || total == "" || quota < 0 || quota > common.MaxQuota {
		return nil, ErrCNConflict
	}
	segmentID := CNIdentity("openai-response-done-v1", response)
	key := CNIdentity(request, segmentID)
	var receipt CNDirectWalletSegment
	err := DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnDirectRowTx(tx, request, user)
		if err != nil {
			return err
		}
		err = tx.Where(map[string]any{"key": key}).First(&receipt).Error
		if err == nil {
			if receipt.ResponseID != response || receipt.UsageJSON != usage || receipt.Quota != int64(quota) {
				return ErrCNConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if row.UsageContract != "openai-response-done-v1" {
			return ErrCNWalletBindingRequired
		}
		if row.Kind != "realtime" || row.State != "reserved" || row.Version != expected || row.Reserved > int64(common.MaxQuota-quota) {
			return ErrCNConflict
		}
		if err = cnDirectQuotaTx(tx, row, int64(quota), true); err != nil {
			return err
		}
		receipt = CNDirectWalletSegment{Key: key, RequestID: request, ResponseID: response, SegmentID: segmentID, UsageJSON: usage, Quota: int64(quota), Target: row.Reserved + int64(quota), Version: row.Version + 1, CreatedAt: time.Now().UTC().Format(CNTimeLayout)}
		if err = tx.Create(&receipt).Error; err != nil {
			return err
		}
		return tx.Model(&CNDirectWallet{}).Where("request_id = ?", request).Updates(map[string]any{"reserved": receipt.Target, "segment_quota": row.SegmentQuota + int64(quota), "usage_json": total, "version": receipt.Version}).Error
	})
	return &receipt, err
}

func FinalizeCNDirectWallet(request string, user, actual int) error {
	if !CNDirectWalletAvailable() || actual < 0 || actual > common.MaxQuota {
		return ErrCNConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnDirectRowTx(tx, request, user)
		if err != nil {
			return err
		}
		if row.Kind == "realtime" && row.SegmentQuota != int64(actual) {
			return ErrCNConflict
		}
		return finalizeCNDirectWalletTx(tx, row, int64(actual), false, actual > 0)
	})
}

func FinalizeCNDirectRelayWallet(request string, user, actual int, usage string, countRequest bool) error {
	if !CNDirectWalletAvailable() || actual < 0 || actual > common.MaxQuota || usage == "" {
		return ErrCNConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnDirectRowTx(tx, request, user)
		if err != nil {
			return err
		}
		if row.Kind != "relay" {
			return ErrCNConflict
		}
		if row.State == "reserved" {
			row.UsageJSON = usage
		} else if row.UsageJSON != usage {
			return ErrCNConflict
		}
		return finalizeCNDirectWalletTx(tx, row, int64(actual), false, countRequest)
	})
}

func finalizeCNDirectWalletTx(tx *gorm.DB, row *CNDirectWallet, actual int64, cancelled, countRequest bool) error {
	state := "settled"
	if cancelled {
		state = "cancelled"
	}
	hash := CNIdentity(row.RequestID, strconv.FormatInt(actual, 10), state, row.UsageJSON, strconv.FormatBool(countRequest))
	if row.State == "settled" || row.State == "cancelled" {
		if row.FinalHash == hash {
			return nil
		}
		return ErrCNConflict
	}
	if row.State != "reserved" && !(cancelled && row.State == "cancel_pending") {
		return ErrCNConflict
	}
	if err := cnDirectQuotaTx(tx, row, actual-row.Reserved, false); err != nil {
		return err
	}
	if countRequest {
		result := tx.Model(&User{}).Where("id = ? AND used_quota <= ? AND request_count < ?", row.UserID, int64(common.MaxWalletQuota)-actual, common.MaxWalletQuota).Updates(map[string]any{"used_quota": gorm.Expr("used_quota + ?", actual), "request_count": gorm.Expr("request_count + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWalletQuotaLimitExceeded
		}
		if actual == 0 {
			var channel Channel
			if err := lockForUpdate(tx).Select("id").First(&channel, row.ChannelID).Error; err != nil {
				return err
			}
		} else {
			result = tx.Model(&Channel{}).Where("id = ? AND used_quota <= ?", row.ChannelID, int64(common.MaxWalletQuota)-actual).Update("used_quota", gorm.Expr("used_quota + ?", actual))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrCNConflict
			}
		}
	}
	now := time.Now().UTC()
	recordType := "cn_direct_wallet_final"
	if row.Kind == "realtime" && row.UsageContract == "openai-response-done-v1" {
		recordType = "cn_realtime_response_final"
	}
	evidence := CNEvidence{System: "gateway-cn", RecordType: recordType, RecordID: row.RequestID, EvidenceID: hash}
	fact := cnBaseFact("charge", "request:"+row.RequestID, "0", hash, "wallet", int64(row.UserID), now, evidence)
	fact.RequestID = &row.RequestID
	fact.Status = "settled"
	fact.Quality = "settled_quota"
	fact.EffectiveAt = row.CreatedAt
	fact.SourcePeriod = row.CreatedAt[:7]
	if row.TokenDisposedQuota > 0 {
		disposition := CNIdentity(row.RequestID, strconv.Itoa(row.TokenID), row.TokenAdjustmentStatus, strconv.FormatInt(row.TokenDisposedQuota, 10))
		fact.SourceEvidence = append(fact.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "cn_direct_wallet_token_disposition", RecordID: row.RequestID, EvidenceID: disposition})
	}
	if row.ConversionJSON != "" {
		if err := common.UnmarshalJsonStr(row.ConversionJSON, &fact.Conversion); err != nil {
			return err
		}
		fact.Cumulative.Money = cnQuotaMoney(actual, fact.Conversion)
		if fact.Cumulative.Money == nil {
			return ErrCNConflict
		}
		version, _ := fact.Conversion["fxVersion"].(string)
		fact.SourceEvidence = append(fact.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "gateway_operation_currency_snapshot", RecordID: version, EvidenceID: version})
	}
	if _, err := cnLockPublisher(tx); err != nil {
		return err
	}
	if err := cnStageChargeSnapshot(tx, &fact, actual); err != nil {
		return err
	}
	return tx.Model(&CNDirectWallet{}).Where("request_id = ?", row.RequestID).Updates(map[string]any{"state": state, "actual": actual, "usage_json": row.UsageJSON, "final_hash": hash, "finalized_at": now.Format(CNTimeLayout), "version": row.Version + 1, "last_error_code": ""}).Error
}

// Cancellation releases the unused initial reservation; successful segments
// remain chargeable. A persisted decision, never age, authorizes recovery.
func RequestCNDirectWalletCancellation(request string, user int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnDirectRowTx(tx, request, user)
		if err != nil {
			return err
		}
		if row.State != "reserved" {
			return nil
		}
		return tx.Model(&CNDirectWallet{}).Where("request_id = ?", request).Updates(map[string]any{"state": "cancel_pending", "cancel_requested_at": time.Now().UTC().Format(CNTimeLayout), "version": row.Version + 1}).Error
	})
}

func RecoverCNDirectWalletCancellations(ctx context.Context, db *gorm.DB, limit int) error {
	var rows []CNDirectWallet
	if err := db.WithContext(ctx).Where("state = ?", "cancel_pending").Order("request_id").Limit(min(max(limit, 1), 1000)).Find(&rows).Error; err != nil {
		return err
	}
	var failures []error
	for _, identity := range rows {
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			row, err := cnDirectRowTx(tx, identity.RequestID, identity.UserID)
			if err != nil {
				return err
			}
			if row.State != "cancel_pending" {
				return nil
			}
			return finalizeCNDirectWalletTx(tx, row, row.SegmentQuota, true, row.SegmentQuota > 0)
		})
		if err != nil {
			failure := db.WithContext(context.WithoutCancel(ctx)).Model(&CNDirectWallet{}).Where("request_id = ? AND state = ?", identity.RequestID, "cancel_pending").Update("last_error_code", "cancellation_transaction_failed").Error
			failures = append(failures, errors.Join(err, failure))
		}
	}
	return errors.Join(failures...)
}
