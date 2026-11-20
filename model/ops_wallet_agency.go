package model

import (
	"errors"
	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// The source user lock prevents a binding change between quote and admission.
// A retry keeps the first durable snapshot, including an unbound snapshot.
func freezeOPSAgencyPricingTx(tx *gorm.DB, identity OPSWalletHoldIdentity) (string, error) {
	var active AgencyActiveUserBinding
	if tx.Migrator().HasTable(&AgencyActiveUserBinding{}) {
		err := tx.Where("user_id = ?", identity.UserID).First(&active).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
	}
	snapshot := identity.AgencyPricing
	if snapshot == nil {
		if active.BindingID != 0 {
			return "", ErrOPSWalletHoldConflict
		}
		return "null", nil
	}
	if snapshot.UserID != int64(identity.UserID) || snapshot.TokenID != int64(identity.TokenID) || snapshot.OriginModelName != identity.Model || snapshot.BindingID != active.BindingID || snapshot.AgencyID != active.AgencyID || snapshot.BindingRevision != active.Revision {
		return "", ErrOPSWalletHoldConflict
	}
	raw, err := common.Marshal(snapshot)
	if len(raw) > 32*1024 {
		return "", ErrOPSWalletHoldConflict
	}
	return string(raw), err
}

// CheckOPSAgencyBindingTx shares the admission lock with new model requests.
// The caller already holds the source user lock; the historical wallet never
// changes while a durable provisioning job blocks new admissions.
func CheckOPSAgencyBindingTx(tx *gorm.DB, user int) error {
	a, err := opsAdmissionTx(tx, user)
	if err != nil {
		return err
	}
	if a.PendingQuota != 0 {
		return errors.New("user has in-flight OPS calls; provisioning is blocked")
	}
	var count int64
	if err := tx.Model(&OPSWalletHold{}).Where("account_id = ? AND state IN ?", a.ID, []string{"reserved", "settled"}).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return errors.New("user has in-flight OPS calls; provisioning is blocked")
	}
	return nil
}
