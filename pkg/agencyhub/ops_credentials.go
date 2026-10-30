package agencyhub

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Host-local only: hashes are accepted on stdin, never returned or audited.
type OpsCredentialChange struct {
	OperatorID      int64  `json:"operatorId"`
	RootID          int64  `json:"rootUserId"`
	ExpectedVersion int64  `json:"expectedAuthVersion"`
	ExpectedHash    string `json:"expectedPasswordFingerprint"`
	PasswordHash    string `json:"passwordHash"`
	MustChange      bool   `json:"mustChangePassword"`
	CommandID       string `json:"commandId"`
}

type OpsCredentialReceipt struct {
	OperatorID  int64  `json:"operatorId"`
	AgencyID    int64  `json:"agencyId"`
	AuthVersion int64  `json:"authVersion"`
	Fingerprint string `json:"fingerprint"`
	MustChange  bool   `json:"mustChangePassword"`
}

func ApplyOpsCredential(db *gorm.DB, input OpsCredentialChange) (OpsCredentialReceipt, error) {
	if input.OperatorID <= 0 || input.RootID <= 0 || input.ExpectedVersion < 1 ||
		len(input.CommandID) < 8 || len(input.CommandID) > 160 || len(input.ExpectedHash) != 64 ||
		!portableOpsCredential.MatchString(input.PasswordHash) {
		return OpsCredentialReceipt{}, errors.New("invalid source credential command")
	}
	parts := strings.Split(input.PasswordHash, "$")
	for _, part := range parts[4:] {
		if _, err := base64.RawStdEncoding.Strict().DecodeString(part); err != nil {
			return OpsCredentialReceipt{}, errors.New("invalid source credential encoding")
		}
	}
	body, err := common.Marshal(input)
	if err != nil {
		return OpsCredentialReceipt{}, err
	}
	bodyHash := fmt.Sprintf("%x", sha256.Sum256(body))
	scopeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.credential\n"+input.CommandID)))
	var result OpsCredentialReceipt
	err = db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var root model.User
		if err := tx.Select("id,role,status").First(&root, input.RootID).Error; err != nil {
			return err
		}
		if root.Role != common.RoleRootUser || root.Status != common.UserStatusEnabled {
			return errors.New("source credential root is inactive")
		}
		var account model.AgencyOperatorAccount
		if err := model.AgencyLockForUpdate(tx).First(&account, input.OperatorID).Error; err != nil {
			return err
		}
		var prior model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scopeHash).First(&prior).Error; err == nil {
			if prior.BodyHash != bodyHash {
				return errors.New("source credential command identity conflict")
			}
			return common.UnmarshalJsonStr(prior.ResponseJSON, &result)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if account.AuthVersion != input.ExpectedVersion || fmt.Sprintf("%x", sha256.Sum256([]byte(account.PasswordHash))) != input.ExpectedHash {
			return errors.New("source credential revision conflict")
		}
		now := time.Now().Unix()
		if err := tx.Model(&account).Updates(map[string]any{"password_hash": input.PasswordHash,
			"auth_version": account.AuthVersion + 1, "must_change_password": input.MustChange,
			"failed_count": 0, "locked_until": 0, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.AgencySession{}).Where("actor_type = ? AND actor_id = ? AND revoked_at IS NULL", ActorTypeOperator, account.ID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.AgencyDeliverySecret{}).Where("operator_account_id = ? AND ciphertext <> ?", account.ID, "").Updates(map[string]any{"ciphertext": "", "expired_at": now}).Error; err != nil {
			return err
		}
		result = OpsCredentialReceipt{OperatorID: account.ID, AgencyID: account.AgencyID, AuthVersion: input.ExpectedVersion + 1,
			Fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(input.PasswordHash))), MustChange: input.MustChange}
		if err := recordAuditTx(tx, nil, &Identity{ActorType: "tokenops"}, "operator.tokenops.password_changed",
			"operator", strconv.FormatInt(account.ID, 10), input.CommandID, nil, map[string]any{"sessionsRevoked": true, "authVersion": result.AuthVersion}); err != nil {
			return err
		}
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: "tokenops",
			Action: "credential.apply", BodyHash: bodyHash, ResourceID: strconv.FormatInt(account.ID, 10),
			ResultCode: 200, ResponseJSON: string(encoded), CreatedAt: now, ExpiresAt: 253402300799}).Error
	})
	return result, err
}
