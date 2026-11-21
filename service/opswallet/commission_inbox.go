package opswallet

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func retainCommissionEntry(tx *gorm.DB, entry CommissionEntry) error {
	n, err := entry.EntryID.Int64()
	user, userErr := strconv.Atoi(entry.UserID)
	if err != nil || userErr != nil || n <= 0 || user <= 0 {
		return errors.New("OPS commission inbox identity invalid")
	}
	raw, err := common.Marshal(entry)
	if err != nil || len(raw) > 16384 {
		return errors.New("OPS commission inbox payload invalid")
	}
	row := model.OPSAgencyPending{ID: model.OPSAgencyID("pending", entry.EntryID.String()), Scope: opsauthority.Scope(), UserID: user, EntryID: n, PayloadHash: fmt.Sprintf("%x", sha256.Sum256(raw)), Payload: model.AgencyOpsPayload(raw)}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	var stored model.OPSAgencyPending
	if err := tx.First(&stored, "id = ?", row.ID).Error; err != nil {
		return err
	}
	if stored.Scope != row.Scope || stored.UserID != row.UserID || stored.EntryID != row.EntryID || stored.PayloadHash != row.PayloadHash || stored.Payload != row.Payload {
		return errors.New("OPS commission inbox evidence changed")
	}
	return nil
}

// Every pass is bounded and serialized by the existing source cursor. A
// mismatch retains evidence and backs off only the affected user. Newer
// entries for that user cannot bypass an unresolved balance sequence.
func (p *CommissionProjector) drainCommissionInbox(ctx context.Context, cursorID string) error {
	return p.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cursor model.OPSAgencyCursor
		if err := model.AgencyLockForUpdate(tx).First(&cursor, "id = ?", cursorID).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		var pending []model.OPSAgencyPending
		if err := tx.Table("ops_agency_pendings AS p").Where("p.scope = ? AND p.next_retry_at <= ?", opsauthority.Scope(), now).
			Where("NOT EXISTS (SELECT 1 FROM ops_agency_pendings b WHERE b.scope = p.scope AND b.user_id = p.user_id AND b.entry_id < p.entry_id AND b.next_retry_at > ?)", now).
			Order("p.entry_id").Limit(100).Find(&pending).Error; err != nil {
			return err
		}
		blocked := map[int]bool{}
		for _, row := range pending {
			if blocked[row.UserID] {
				continue
			}
			var entry CommissionEntry
			if common.Unmarshal([]byte(row.Payload), &entry) != nil || row.PayloadHash != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Payload))) {
				return errors.New("OPS commission retained evidence invalid")
			}
			// Savepoint rollback removes partial allocations before failure is
			// retained; unrelated users still commit in the outer transaction.
			err := tx.Transaction(func(inner *gorm.DB) error { return ApplyCommissionEntryTx(inner, entry) })
			if err == nil {
				if err := tx.Delete(&row).Error; err != nil {
					return err
				}
				continue
			}
			blocked[row.UserID] = true
			attempts := min(row.Attempts+1, 30)
			delay := min(30*(1<<min(attempts-1, 4)), 300)
			message := err.Error()
			if len(message) > 255 {
				message = "OPS commission accounting requires review"
			}
			if err := tx.Model(&row).Updates(map[string]any{"attempts": attempts, "last_error": message, "next_retry_at": now + int64(delay)}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
