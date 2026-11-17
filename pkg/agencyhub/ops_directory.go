package agencyhub

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type OpsDirectorySnapshot struct {
	AgencyID    int64  `json:"agencyId"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ParentID    *int64 `json:"parentId"`
	Depth       int    `json:"depth"`
	InviteCode  string `json:"inviteCode"`
	Fingerprint string `json:"fingerprint"`
}

// ReadOpsDirectorySnapshots excludes price revisions so a price publication
// cannot falsely conflict with an independent name or access-state edit.
func ReadOpsDirectorySnapshots(db *gorm.DB, ids []int64) ([]OpsDirectorySnapshot, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return nil, errors.New("invalid bounded directory request")
	}
	var rows []model.Agency
	if err := db.Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, errors.New("source directory target missing or duplicated")
	}
	items := make([]OpsDirectorySnapshot, 0, len(rows))
	for _, row := range rows {
		item := OpsDirectorySnapshot{AgencyID: row.ID, Name: row.DisplayName, Status: row.Status,
			ParentID: row.ParentAgencyID, Depth: row.Depth, InviteCode: row.InviteCode}
		body, err := common.Marshal(item)
		if err != nil {
			return nil, err
		}
		item.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(body))
		items = append(items, item)
	}
	return items, nil
}

// ApplyOpsDirectory uses the same agency locks, access revisions, subtree
// suspension and unpaid-withdrawal holds as the Hub administrator workflow.
// Hierarchy and invite changes are not silently reinterpreted as metadata.
func ApplyOpsDirectory(db *gorm.DB, id int64, name, status, expected, commandID string) (OpsDirectorySnapshot, error) {
	name = strings.TrimSpace(name)
	if id <= 0 || len(commandID) < 8 || len(commandID) > 160 || len(expected) != 64 ||
		name == "" || len([]rune(name)) > 191 || (status != AgencyStatusActive && status != AgencyStatusDisabled) {
		return OpsDirectorySnapshot{}, errors.New("invalid source directory command")
	}
	body, err := common.Marshal(map[string]any{"id": id, "name": name, "status": status, "expected": expected})
	if err != nil {
		return OpsDirectorySnapshot{}, err
	}
	bodyHash := fmt.Sprintf("%x", sha256.Sum256(body))
	scopeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.directory\n"+commandID)))
	var result OpsDirectorySnapshot
	err = db.Transaction(func(tx *gorm.DB) error {
		parentActive := true
		var target model.Agency
		if err := tx.First(&target, id).Error; err != nil {
			return err
		}
		if target.ParentAgencyID != nil {
			var parent model.Agency
			if err := model.AgencyLockForUpdate(tx).First(&parent, *target.ParentAgencyID).Error; err != nil {
				return err
			}
			parentActive = parent.Status == AgencyStatusActive
		}
		var agency model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&agency, id).Error; err != nil {
			return err
		}
		var receipt model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scopeHash).First(&receipt).Error; err == nil {
			if receipt.BodyHash != bodyHash {
				return errors.New("source directory command identity conflict")
			}
			return common.UnmarshalJsonStr(receipt.ResponseJSON, &result)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if status == AgencyStatusActive && !parentActive {
			return errors.New("source directory parent is disabled")
		}
		before, err := ReadOpsDirectorySnapshots(tx, []int64{id})
		if err != nil {
			return err
		}
		if before[0].Fingerprint != expected {
			return errors.New("source directory revision conflict")
		}
		now := time.Now().Unix()
		updates := map[string]any{"display_name": name, "version": agency.Version + 1, "updated_at": now}
		if status != agency.Status {
			updates["status"], updates["state_revision"] = status, agency.StateRevision+1
			updates["disabled_reason"], updates["disabled_at"] = "", nil
			if status == AgencyStatusDisabled {
				updates["disabled_reason"], updates["disabled_at"] = "TokenOps administrator suspension", now
			}
		}
		if err := tx.Model(&agency).Updates(updates).Error; err != nil {
			return err
		}
		if status == AgencyStatusDisabled && before[0].Status != status {
			ids, err := agencySubtreeIDsTx(tx, id)
			if err != nil {
				return err
			}
			if len(ids) > 1000 {
				return errors.New("source directory subtree exceeds bounded command")
			}
			for start := 0; start < len(ids); start += 100 {
				batch := ids[start:min(start+100, len(ids))]
				if err := tx.Model(&model.Agency{}).Where("id IN ? AND id <> ? AND status = ?", batch, id, AgencyStatusActive).
					Updates(map[string]any{"status": status, "state_revision": gorm.Expr("state_revision + 1"),
						"version": gorm.Expr("version + 1"), "disabled_reason": "ancestor_disabled", "disabled_at": now, "updated_at": now}).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&model.AgencySession{}).Where("agency_id IN ? AND revoked_at IS NULL", ids).Update("revoked_at", now).Error; err != nil {
				return err
			}
			var balances []model.AgencyCommissionBalance
			if err := tx.Where("agency_id IN ?", ids).Find(&balances).Error; err != nil {
				return err
			}
			for _, balance := range balances {
				if err := holdUnpaidWithdrawals(tx, balance.AgencyID, balance.CurrencyCode,
					"tokenops-directory-"+commandID, now*1000); err != nil {
					return err
				}
			}
		}
		items, err := ReadOpsDirectorySnapshots(tx, []int64{id})
		if err != nil {
			return err
		}
		result = items[0]
		if err := recordAuditTx(tx, nil, &Identity{ActorType: "tokenops"}, "agency.tokenops.directory", "agency",
			strconv.FormatInt(id, 10), commandID, before[0], result); err != nil {
			return err
		}
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: "tokenops",
			Action: "directory.apply", BodyHash: bodyHash, ResourceID: strconv.FormatInt(id, 10),
			ResultCode: 200, ResponseJSON: string(encoded), CreatedAt: now, ExpiresAt: now + 10*365*86400}).Error
	})
	return result, err
}
