package model

import (
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrCNWalletBindingRequired = errors.New("wallet token binding missing or incompatible")

// The binding is created before any financial leg. Old wallet-only lifecycle
// rows cannot acquire a token binding after the fact.
type CNWalletBinding struct {
	RequestID             string `gorm:"size:191;primaryKey"`
	UserID                int    `gorm:"not null"`
	TokenID               int    `gorm:"not null"`
	TokenUnlimited        bool   `gorm:"not null"`
	Playground            bool   `gorm:"not null"`
	InitialReserve        int64  `gorm:"not null"`
	TokenQuota            int64  `gorm:"not null"`
	TokenAdjustmentStatus string `gorm:"size:40"`
	TokenDisposedQuota    int64  `gorm:"not null;default:0"`
}

func cnWalletBindingTx(tx *gorm.DB, row *CNWalletLifecycle) (*CNWalletBinding, error) {
	var binding CNWalletBinding
	err := lockForUpdate(tx).Where("request_id = ?", row.RequestID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCNWalletBindingRequired
	}
	if err != nil {
		return nil, err
	}
	if binding.UserID != row.UserID {
		return nil, ErrCNConflict
	}
	if row.State == "reserved" && ((!binding.Playground && binding.TokenQuota != row.Reserved) || (binding.Playground && binding.TokenQuota != 0)) {
		return nil, ErrCNConflict
	}
	return &binding, nil
}

func cnRejectWalletOnlyMutationTx(tx *gorm.DB, request string) error {
	var count int64
	if err := tx.Model(&CNWalletBinding{}).Where("request_id = ?", request).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrCNWalletBindingRequired
	}
	return nil
}

func cnWalletBindingProofTx(tx *gorm.DB, row CNWalletLifecycle) (*CNWalletBinding, bool, error) {
	var binding CNWalletBinding
	err := tx.Where("request_id = ?", row.RequestID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	target := row.Reserved
	if row.State != "reserved" {
		target = row.Actual
	}
	if binding.Playground {
		target = 0
	}
	return &binding, binding.UserID != row.UserID || binding.TokenQuota != target, nil
}

func cnWalletTokenEvidenceTx(tx *gorm.DB, row *CNWalletLifecycle, fact *CNFact) error {
	var binding CNWalletBinding
	err := tx.Where("request_id = ?", row.RequestID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	evidence := CNIdentity(row.RequestID, strconv.Itoa(binding.UserID), strconv.Itoa(binding.TokenID), strconv.FormatBool(binding.TokenUnlimited), strconv.FormatBool(binding.Playground), strconv.FormatInt(binding.InitialReserve, 10), strconv.FormatInt(binding.TokenQuota, 10), binding.TokenAdjustmentStatus, strconv.FormatInt(binding.TokenDisposedQuota, 10))
	fact.SourceEvidence = append(fact.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "cn_wallet_token_binding", RecordID: row.RequestID, EvidenceID: evidence})
	return nil
}

func cnWalletBindingTokenTx(tx *gorm.DB, binding *CNWalletBinding, target int64, requireAvailable bool) error {
	if binding.Playground {
		if binding.TokenQuota != 0 {
			return ErrCNConflict
		}
		return nil
	}
	delta := target - binding.TokenQuota
	var token Token
	err := lockForUpdate(tx.Unscoped()).Where("id = ?", binding.TokenID).First(&token).Error
	status := ""
	if errors.Is(err, gorm.ErrRecordNotFound) && delta < 0 {
		status = "missing_token_not_recredited"
	} else if err != nil {
		return err
	} else if token.UserId != binding.UserID {
		return ErrCNConflict
	} else if token.DeletedAt.Valid {
		if delta >= 0 {
			return ErrCNConflict
		}
		status = "deleted_token_not_recredited"
	}
	if status != "" {
		binding.TokenAdjustmentStatus = status
		binding.TokenDisposedQuota += -delta
		binding.TokenQuota = target
		return tx.Model(&CNWalletBinding{}).Where("request_id = ?", binding.RequestID).Updates(map[string]any{"token_quota": target, "token_adjustment_status": status, "token_disposed_quota": binding.TokenDisposedQuota}).Error
	}
	if token.UnlimitedQuota != binding.TokenUnlimited {
		return ErrCNConflict
	}
	if (requireAvailable || delta > 0) && (token.Status != common.TokenStatusEnabled || (token.ExpiredTime > 0 && token.ExpiredTime < common.GetTimestamp())) {
		return ErrCNConflict
	}
	if delta == 0 {
		return nil
	}
	remain, used := int64(token.RemainQuota)-delta, int64(token.UsedQuota)+delta
	if requireAvailable && !binding.TokenUnlimited && remain < 0 {
		return ErrInsufficientAgencyTokenQuota
	}
	if remain < -int64(common.MaxWalletQuota) || remain > int64(common.MaxWalletQuota) || used < -int64(common.MaxWalletQuota) || used > int64(common.MaxWalletQuota) {
		return ErrWalletQuotaLimitExceeded
	}
	result := tx.Model(&Token{}).Where("id = ? AND user_id = ?", binding.TokenID, binding.UserID).Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": common.GetTimestamp()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCNConflict
	}
	binding.TokenQuota = target
	return tx.Model(&CNWalletBinding{}).Where("request_id = ?", binding.RequestID).Update("token_quota", target).Error
}

func PreConsumeCNBoundWallet(request string, user, initial, token int, unlimited, playground bool) (int, error) {
	if !CNDurableWalletAvailable() || request == "" || initial < 0 || initial > common.MaxQuota || (!playground && token <= 0) {
		return 0, ErrCNWalletBindingRequired
	}
	var reserved int
	err := DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnWalletRowTx(tx, request, user, false)
		fresh := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !fresh {
			return err
		}
		if fresh {
			for _, table := range []any{&CNDirectWallet{}, &CNSubscriptionLifecycle{}} {
				var count int64
				if err := tx.Model(table).Where("request_id = ?", request).Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					return ErrCNConflict
				}
			}
			row, err = cnWalletRowTx(tx, request, user, true)
			if err != nil {
				return err
			}
			binding := CNWalletBinding{RequestID: request, UserID: user, TokenID: token, TokenUnlimited: unlimited, Playground: playground, InitialReserve: int64(initial)}
			if err = tx.Create(&binding).Error; err != nil {
				return err
			}
		}
		binding, err := cnWalletBindingTx(tx, row)
		if err != nil {
			return err
		}
		if binding.TokenID != token || binding.TokenUnlimited != unlimited || binding.Playground != playground || binding.InitialReserve != int64(initial) || row.State != "reserved" {
			return ErrCNConflict
		}
		var cancellations int64
		if err = tx.Model(&CNWalletCancellation{}).Where("request_id = ?", request).Count(&cancellations).Error; err != nil {
			return err
		}
		if cancellations > 0 {
			return ErrCNConflict
		}
		if fresh {
			if err = reserveCNBoundWalletTx(tx, row, binding, int64(initial), true); err != nil {
				return err
			}
			reserved = initial
		} else {
			reserved = int(row.Reserved)
		}
		return nil
	})
	return reserved, err
}

func reserveCNBoundWalletTx(tx *gorm.DB, row *CNWalletLifecycle, binding *CNWalletBinding, target int64, requireAvailable bool) error {
	delta := target - row.Reserved
	if err := cnWalletValueDebitTx(tx, row.UserID, delta, true); err != nil {
		return err
	}
	if err := cnWalletBindingTokenTx(tx, binding, target, delta > 0 || requireAvailable); err != nil {
		return err
	}
	if delta == 0 {
		return nil
	}
	query := cnWalletValueWrite(tx).Model(&User{}).Where("id = ?", row.UserID)
	if delta > 0 && requireAvailable {
		query = query.Where("quota >= ?", delta)
	}
	if delta < 0 {
		query = query.Where("quota <= ?", int64(common.MaxWalletQuota)+delta)
	}
	result := query.Update("quota", gorm.Expr("quota - ?", delta))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInsufficientAgencyWalletQuota
	}
	result = tx.Model(&CNWalletLifecycle{}).Where("request_id = ? AND version = ?", row.RequestID, row.Version).Updates(map[string]any{"reserved": target, "version": row.Version + 1})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCNConflict
	}
	return cnFinishIntegrityCaptureTx(tx, row.UserID, "wallet", row.RequestID)
}

func ReserveCNBoundWallet(request string, user, target int, requireAvailable bool) error {
	if !CNDurableWalletAvailable() || target < 0 || target > common.MaxQuota {
		return ErrCNConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnWalletRowTx(tx, request, user, false)
		if err != nil {
			return err
		}
		binding, err := cnWalletBindingTx(tx, row)
		if err != nil {
			return err
		}
		var count int64
		if err = tx.Model(&CNWalletCancellation{}).Where("request_id = ?", request).Count(&count).Error; err != nil {
			return err
		}
		if row.State != "reserved" || count > 0 {
			return ErrCNConflict
		}
		return reserveCNBoundWalletTx(tx, row, binding, int64(target), requireAvailable)
	})
}

func FinalizeCNBoundWallet(request string, user, actual int) error {
	if !CNDurableWalletAvailable() || actual < 0 || actual > common.MaxQuota {
		return ErrCNConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		row, err := cnWalletRowTx(tx, request, user, false)
		if err != nil {
			return err
		}
		binding, err := cnWalletBindingTx(tx, row)
		if err != nil {
			return err
		}
		if row.State != "reserved" {
			return finalizeCNWalletTx(tx, row, actual, false, time.Now().UTC())
		}
		var count int64
		if err = tx.Model(&CNWalletCancellation{}).Where("request_id = ?", request).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrCNConflict
		}
		if err = cnWalletBindingTokenTx(tx, binding, int64(actual), false); err != nil {
			return err
		}
		return finalizeCNWalletTx(tx, row, actual, false, time.Now().UTC())
	})
}

func RequestCNBoundWalletCancellation(db *gorm.DB, request string, user int) error {
	return db.Transaction(func(tx *gorm.DB) error {
		row, err := cnWalletRowTx(tx, request, user, false)
		if err != nil {
			return err
		}
		binding, err := cnWalletBindingTx(tx, row)
		if err != nil {
			return err
		}
		var prior CNWalletCancellation
		if err = tx.Where("request_id = ?", request).First(&prior).Error; err == nil {
			if prior.UserID != user || prior.TokenID != binding.TokenID {
				return ErrCNConflict
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if row.State != "reserved" {
			return ErrCNConflict
		}
		return tx.Create(&CNWalletCancellation{RequestID: request, UserID: user, TokenID: binding.TokenID, TokenQuota: binding.TokenQuota, State: "pending", RequestedAt: time.Now().UTC().Format(CNTimeLayout)}).Error
	})
}
