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

// OpsPayoutAccount is transported only over the server-local bridge's stdin.
type OpsPayoutAccount = withdrawalAccountRequest

type OpsPayoutSnapshot struct {
	AgencyID    int64  `json:"agencyId"`
	AccountID   int64  `json:"id"`
	Version     int64  `json:"version"`
	Status      string `json:"status"`
	Ciphertext  string `json:"ciphertext"`
	Fingerprint string `json:"fingerprint"`
}

// ReadOpsPayoutSnapshots returns encrypted latest recipients, never plaintext.
func ReadOpsPayoutSnapshots(db *gorm.DB, ids []int64) ([]OpsPayoutSnapshot, error) {
	if len(ids) < 1 || len(ids) > 5 {
		return nil, errors.New("invalid bounded payout request")
	}
	var agencies []model.Agency
	if err := db.Where("id IN ?", ids).Find(&agencies).Error; err != nil {
		return nil, err
	}
	if len(agencies) != len(ids) {
		return nil, errors.New("payout target missing or duplicated")
	}
	var rows []model.AgencyWithdrawalAccount
	if err := db.Where("agency_id IN ? AND version = (SELECT MAX(p.version) FROM agency_hub_withdrawal_accounts p WHERE p.agency_id=agency_hub_withdrawal_accounts.agency_id)", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	byAgency := make(map[int64]model.AgencyWithdrawalAccount)
	for _, row := range rows {
		if _, exists := byAgency[row.AgencyID]; exists {
			return nil, errors.New("payout version ambiguity")
		}
		byAgency[row.AgencyID] = row
	}
	result := make([]OpsPayoutSnapshot, 0, len(ids))
	for _, id := range ids {
		row := byAgency[id]
		item := OpsPayoutSnapshot{AgencyID: id, AccountID: row.ID, Version: row.Version, Status: row.Status, Ciphertext: row.Ciphertext}
		if row.ID == 0 {
			item.Status = "absent"
		}
		body, err := common.Marshal(item)
		if err != nil {
			return nil, err
		}
		item.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(body))
		result = append(result, item)
	}
	return result, nil
}

// ApplyOpsPayout preserves old recipient rows, pending withdrawals and replay
// receipts. Only the replacement recipient is encrypted with the source key.
func ApplyOpsPayout(db *gorm.DB, id int64, account OpsPayoutAccount, status, expected, commandID string) (OpsPayoutSnapshot, error) {
	if id <= 0 || len(commandID) < 8 || len(commandID) > 160 || len(expected) != 64 || (status != "active" && status != "disabled") {
		return OpsPayoutSnapshot{}, errors.New("invalid payout command")
	}
	account.AccountType, account.AccountName = strings.TrimSpace(account.AccountType), strings.TrimSpace(account.AccountName)
	account.AccountNo, account.BankName = strings.TrimSpace(account.AccountNo), strings.TrimSpace(account.BankName)
	if status == "active" {
		if (account.AccountType != "bank" && account.AccountType != "corporate") || account.AccountName == "" || len(account.AccountName) > 128 || len(account.AccountNo) < 4 || len(account.AccountNo) > 128 || account.BankName == "" || len(account.BankName) > 256 {
			return OpsPayoutSnapshot{}, errors.New("invalid payout account")
		}
		account.Last4 = account.AccountNo[len(account.AccountNo)-4:]
	} else {
		account = OpsPayoutAccount{}
	}
	body, err := common.Marshal(map[string]any{"id": id, "account": account, "status": status, "expected": expected})
	if err != nil {
		return OpsPayoutSnapshot{}, err
	}
	bodyHash := fmt.Sprintf("%x", sha256.Sum256(body))
	scopeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.payout\n"+commandID)))
	var result OpsPayoutSnapshot
	err = db.Transaction(func(tx *gorm.DB) error {
		var agency model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&agency, id).Error; err != nil {
			return err
		}
		var receipt model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scopeHash).First(&receipt).Error; err == nil {
			if receipt.BodyHash != bodyHash {
				return errors.New("payout command identity conflict")
			}
			return common.UnmarshalJsonStr(receipt.ResponseJSON, &result)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		before, err := ReadOpsPayoutSnapshots(tx, []int64{id})
		if err != nil {
			return err
		}
		if before[0].Fingerprint != expected {
			return errors.New("payout revision conflict")
		}
		if status == "disabled" {
			if before[0].Status != "active" {
				return errors.New("payout account not active")
			}
			if err := tx.Model(&model.AgencyWithdrawalAccount{}).Where("id = ? AND agency_id = ?", before[0].AccountID, id).Update("status", "disabled").Error; err != nil {
				return err
			}
		} else {
			ciphertext, keyID, err := encryptPayoutAccount(account, id, before[0].Version+1)
			if err != nil {
				return err
			}
			if err := tx.Model(&model.AgencyWithdrawalAccount{}).Where("agency_id = ? AND status = ?", id, "active").Update("status", "superseded").Error; err != nil {
				return err
			}
			row := model.AgencyWithdrawalAccount{AgencyID: id, Version: before[0].Version + 1, Ciphertext: ciphertext, KeyID: keyID, Last4: account.Last4, Status: "active", CreatedAtMS: time.Now().UnixMilli()}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		items, err := ReadOpsPayoutSnapshots(tx, []int64{id})
		if err != nil {
			return err
		}
		result = items[0]
		// Financial account details must never be copied into audit history.
		if err := recordAuditTx(tx, nil, &Identity{ActorType: "tokenops"}, "withdrawal_account.tokenops", "agency", strconv.FormatInt(id, 10), commandID, nil, map[string]any{"version": result.Version, "status": result.Status}); err != nil {
			return err
		}
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: "tokenops", Action: "payout.apply", BodyHash: bodyHash, ResourceID: strconv.FormatInt(id, 10), ResultCode: 200, ResponseJSON: string(encoded), CreatedAt: now, ExpiresAt: now + 10*365*86400}).Error
	})
	return result, err
}
