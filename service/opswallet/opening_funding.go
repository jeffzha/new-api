package opswallet

import (
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// A legacy account created after its original credit may contain only debt.
// Explicitly retain that missing opening as unverified funds, without repairing
// the old ledger or treating the unexplained credit as commissionable payment.
// Real lots, blocked accounts and negative discrepancies retain the strict gate.
func openingFundingLots(db *gorm.DB, user int, quota int64) ([]model.OPSAgencyLot, int64, error) {
	lots, debt, original := model.OPSAgencyOpeningLots(db, user, quota)
	if original == nil || os.Getenv("OPS_WALLET_LEGACY_OPENING_GAPS") != "retain_unverified" {
		return lots, debt, original
	}
	var account model.AgencyFundingAccount
	if db.First(&account, user).Error != nil || account.ReconcileBlocked || account.PaidAvailable != 0 || account.NonpaidAvailable != 0 || account.DebtQuota < 0 || account.DebtQuota > common.MaxWalletQuota || quota < -common.MaxWalletQuota || quota > common.MaxWalletQuota {
		return nil, 0, original
	}
	var count int64
	if db.Model(&model.AgencyFundingLot{}).Where("user_id = ?", user).Count(&count).Error != nil || count != 0 {
		return nil, 0, original
	}
	missing := quota + account.DebtQuota
	if missing <= 0 || missing > common.MaxWalletQuota {
		return nil, 0, original
	}
	return []model.OPSAgencyLot{{ID: "opening-unverified-legacy-gap", Source: "legacy_unknown", Amount: missing * 2}}, account.DebtQuota * 2, nil
}
