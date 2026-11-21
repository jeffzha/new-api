package opswallet

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"gorm.io/gorm"
)

// ReplayReviewedCommissionInbox replays an explicitly reviewed user's bounded
// existing sequence under the same cursor lock as normal workers. It neither
// changes source evidence nor resets retry state or bypasses accounting checks.
func ReplayReviewedCommissionInbox(ctx context.Context, db *gorm.DB, user int, first, last int64) (int, error) {
	if !opsauthority.Enabled() || opsauthority.Scope() == "" || user <= 0 || first <= 0 || last < first || last-first >= 100 {
		return 0, errors.New("invalid reviewed commission bounds")
	}
	count := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cursor model.OPSAgencyCursor
		if err := model.AgencyLockForUpdate(tx).First(&cursor, "id = ?", model.OPSAgencyID("cursor", "commission")).Error; err != nil {
			return err
		}
		var earlier int64
		if err := tx.Model(&model.OPSAgencyPending{}).Where("scope = ? AND user_id = ? AND entry_id < ?", opsauthority.Scope(), user, first).Count(&earlier).Error; err != nil {
			return err
		}
		if earlier != 0 {
			return errors.New("earlier user sequence requires review")
		}
		var rows []model.OPSAgencyPending
		if err := tx.Where("scope = ? AND user_id = ? AND entry_id >= ? AND entry_id <= ?", opsauthority.Scope(), user, first, last).Order("entry_id").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			var entry CommissionEntry
			if common.Unmarshal([]byte(row.Payload), &entry) != nil || row.PayloadHash != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Payload))) {
				return errors.New("reviewed commission evidence invalid")
			}
			if err := ApplyCommissionEntryTx(tx, entry); err != nil {
				return err
			}
			if err := tx.Delete(&row).Error; err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
