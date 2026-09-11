package agencyhub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type withdrawalAccountRequest struct {
	AccountType string `json:"account_type"`
	AccountName string `json:"account_name"`
	AccountNo   string `json:"account_no"`
	BankName    string `json:"bank_name"`
	Last4       string `json:"last4"`
}

type withdrawalAccountUpdateRequest struct {
	withdrawalAccountRequest
	ExpectedVersion int64 `json:"expected_version"`
}

func payoutKey() ([]byte, string, error) {
	raw, err := payoutKeyMaterial()
	if err != nil {
		return nil, "", err
	}
	key, err := decodePayoutKey(raw)
	if err != nil {
		return nil, "", err
	}
	keyID := strings.TrimSpace(os.Getenv("AGENCY_PAYOUT_KEY_ID"))
	if keyID == "" {
		keyID = "agency-payout-v1"
	}
	if len(keyID) > 64 {
		return nil, "", errors.New("AGENCY_PAYOUT_KEY_ID is too long")
	}
	return key, keyID, nil
}

func payoutKeyMaterial() (string, error) {
	if path := strings.TrimSpace(os.Getenv("AGENCY_PAYOUT_KEY_FILE")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read AGENCY_PAYOUT_KEY_FILE: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(os.Getenv("AGENCY_PAYOUT_KEY")), nil
}

func decodePayoutKey(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("AGENCY_PAYOUT_KEY is required")
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		key, err = hex.DecodeString(strings.TrimSpace(raw))
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("payout key must be 32 bytes")
	}
	return key, nil
}

type payoutKeyCandidate struct {
	key   []byte
	keyID string
}

func payoutKeyCandidates() ([]payoutKeyCandidate, error) {
	current, keyID, err := payoutKey()
	if err != nil {
		return nil, err
	}
	candidates := []payoutKeyCandidate{{key: current, keyID: keyID}}
	path := strings.TrimSpace(os.Getenv("AGENCY_PAYOUT_OLD_KEYS_FILE"))
	if path == "" {
		return candidates, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read AGENCY_PAYOUT_OLD_KEYS_FILE: %w", err)
	}
	seen := map[string]bool{keyID: true}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, errors.New("old payout key must use key_id=value lines")
		}
		oldID := strings.TrimSpace(parts[0])
		if oldID == "" || len(oldID) > 64 || seen[oldID] {
			return nil, errors.New("invalid or duplicate old payout key id")
		}
		oldKey, err := decodePayoutKey(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid old payout key %q: %w", oldID, err)
		}
		candidates = append(candidates, payoutKeyCandidate{key: oldKey, keyID: oldID})
		seen[oldID] = true
	}
	return candidates, nil
}

func payoutAccountAAD(agencyID, version int64) []byte {
	return []byte(fmt.Sprintf("agency-payout-v1|agency=%d|version=%d|field=account", agencyID, version))
}

func encryptPayoutAccount(value any, agencyID, version int64) (string, string, error) {
	key, keyID, err := payoutKey()
	if err != nil {
		return "", "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	payload, err := common.Marshal(value)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", err
	}
	sealed := gcm.Seal(nonce, nonce, payload, payoutAccountAAD(agencyID, version))
	return base64.RawURLEncoding.EncodeToString(sealed), keyID, nil
}

func decryptPayoutAccount(ciphertext string, agencyID, version int64) (withdrawalAccountRequest, string, error) {
	candidates, err := payoutKeyCandidates()
	if err != nil {
		return withdrawalAccountRequest{}, "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(ciphertext))
	if err != nil {
		return withdrawalAccountRequest{}, "", errors.New("invalid payout account ciphertext")
	}
	for _, candidate := range candidates {
		block, err := aes.NewCipher(candidate.key)
		if err != nil {
			continue
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil || len(raw) <= gcm.NonceSize() {
			continue
		}
		plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], payoutAccountAAD(agencyID, version))
		if err != nil {
			continue
		}
		var account withdrawalAccountRequest
		if err := common.Unmarshal(plaintext, &account); err != nil {
			continue
		}
		return account, candidate.keyID, nil
	}
	return withdrawalAccountRequest{}, "", errors.New("payout account decryption failed")
}

func (a *App) listWithdrawalAccounts(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	var rows []model.AgencyWithdrawalAccount
	if err := a.db.Where("agency_id = ? AND status = ?", agency.ID, "active").Order("version DESC, id DESC").Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取收款账户失败", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"id": row.ID, "version": row.Version, "last4": row.Last4, "key_id": row.KeyID, "created_at_ms": row.CreatedAtMS})
	}
	respondOK(c, gin.H{"items": items})
}

func (a *App) createWithdrawalAccount(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	var request withdrawalAccountRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.AccountType) == "" || strings.TrimSpace(request.AccountName) == "" || len(strings.TrimSpace(request.AccountNo)) < 4 || len(request.AccountNo) > 128 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_account", "收款账户信息无效", nil)
		return
	}
	request.AccountType = strings.TrimSpace(request.AccountType)
	request.AccountName = strings.TrimSpace(request.AccountName)
	request.AccountNo = strings.TrimSpace(request.AccountNo)
	request.BankName = strings.TrimSpace(request.BankName)
	last4 := request.Last4
	if last4 == "" {
		last4 = request.AccountNo[len(request.AccountNo)-4:]
	}
	if len(last4) > 8 {
		last4 = last4[len(last4)-8:]
	}
	now := time.Now().UnixMilli()
	var account model.AgencyWithdrawalAccount
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var latest model.AgencyWithdrawalAccount
		version := int64(1)
		if err := tx.Where("agency_id = ?", agency.ID).Order("version DESC").First(&latest).Error; err == nil {
			version = latest.Version + 1
		}
		if err := tx.Model(&model.AgencyWithdrawalAccount{}).Where("agency_id = ? AND status = ?", agency.ID, "active").Update("status", "superseded").Error; err != nil {
			return err
		}
		ciphertext, keyID, err := encryptPayoutAccount(request, agency.ID, version)
		if err != nil {
			return err
		}
		account = model.AgencyWithdrawalAccount{AgencyID: agency.ID, Version: version, Ciphertext: ciphertext, KeyID: keyID, Last4: last4, Status: "active", CreatedAtMS: now}
		return tx.Create(&account).Error
	})
	if err != nil {
		status, code, message := http.StatusInternalServerError, "database_error", "保存收款账户失败"
		if strings.Contains(err.Error(), "AGENCY_PAYOUT_KEY") {
			status, code, message = http.StatusServiceUnavailable, "encryption_unavailable", "收款账户服务不可用"
		}
		respondError(c, status, code, message, nil)
		return
	}
	respondCreated(c, gin.H{"id": account.ID, "version": account.Version, "last4": account.Last4, "key_id": account.KeyID, "created_at_ms": account.CreatedAtMS})
}

func (a *App) updateWithdrawalAccount(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的账户ID", nil)
		return
	}
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	var request withdrawalAccountUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.AccountType) == "" || strings.TrimSpace(request.AccountName) == "" || len(strings.TrimSpace(request.AccountNo)) < 4 || len(request.AccountNo) > 128 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_account", "收款账户信息无效", nil)
		return
	}
	if request.ExpectedVersion <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前收款账户版本", nil)
		return
	}
	request.AccountType, request.AccountName = strings.TrimSpace(request.AccountType), strings.TrimSpace(request.AccountName)
	request.AccountNo, request.BankName = strings.TrimSpace(request.AccountNo), strings.TrimSpace(request.BankName)
	if request.Last4 == "" {
		request.Last4 = request.AccountNo[len(request.AccountNo)-4:]
	}
	accountPayload := request.withdrawalAccountRequest
	now := time.Now().UnixMilli()
	var account model.AgencyWithdrawalAccount
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var previous model.AgencyWithdrawalAccount
		if err := model.AgencyLockForUpdate(tx).Where("id = ? AND agency_id = ? AND status = ?", id, agency.ID, "active").First(&previous).Error; err != nil {
			return err
		}
		if previous.Version != request.ExpectedVersion {
			return errors.New("withdrawal account version conflict")
		}
		if err := tx.Model(&previous).Update("status", "superseded").Error; err != nil {
			return err
		}
		ciphertext, keyID, err := encryptPayoutAccount(accountPayload, agency.ID, previous.Version+1)
		if err != nil {
			return err
		}
		account = model.AgencyWithdrawalAccount{AgencyID: agency.ID, Version: previous.Version + 1, Ciphertext: ciphertext, KeyID: keyID, Last4: request.Last4, Status: "active", CreatedAtMS: now}
		return tx.Create(&account).Error
	})
	if err != nil {
		status, code, message := http.StatusConflict, "account_update_failed", "收款账户不存在或已停用"
		if strings.Contains(err.Error(), "AGENCY_PAYOUT_KEY") {
			status, code, message = http.StatusServiceUnavailable, "encryption_unavailable", "收款账户服务不可用"
		}
		respondError(c, status, code, message, nil)
		return
	}
	respondOK(c, gin.H{"id": account.ID, "version": account.Version, "last4": account.Last4, "key_id": account.KeyID, "created_at_ms": account.CreatedAtMS})
}

func (a *App) disableWithdrawalAccount(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的账户ID", nil)
		return
	}
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	var request struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if request.ExpectedVersion <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前收款账户版本", nil)
		return
	}
	result := a.db.Model(&model.AgencyWithdrawalAccount{}).Where("id = ? AND agency_id = ? AND status = ? AND version = ?", id, agency.ID, "active", request.ExpectedVersion).Update("status", "disabled")
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "停用收款账户失败", nil)
		return
	}
	if result.RowsAffected == 0 {
		var account model.AgencyWithdrawalAccount
		if err := a.db.Where("id = ? AND agency_id = ?", id, agency.ID).First(&account).Error; err == nil && account.Status == "active" {
			respondError(c, http.StatusConflict, "version_conflict", "收款账户已被其他操作修改", gin.H{"version": account.Version})
			return
		}
		respondError(c, http.StatusNotFound, "not_found", "收款账户不存在", nil)
		return
	}
	respondOK(c, gin.H{"status": "disabled"})
}

func (a *App) revealWithdrawalAccount(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的账户ID", nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可查看完整收款账户", nil)
		return
	}
	var account model.AgencyWithdrawalAccount
	if err := a.db.Where("id = ?", id).First(&account).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "收款账户不存在", nil)
		return
	}
	plain, keyID, err := decryptPayoutAccount(account.Ciphertext, account.AgencyID, account.Version)
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "decryption_failed", "收款账户解密失败", nil)
		return
	}
	now := time.Now().UnixMilli()
	audit := model.AgencyAuditLog{
		EventID:     "payout-account-reveal-" + requestID(c) + "-" + stringID(id),
		ActorType:   identity.ActorType,
		ActorID:     identity.ActorID,
		Action:      "withdrawal_account.reveal",
		ObjectType:  "withdrawal_account",
		ObjectID:    stringID(id),
		RequestID:   requestID(c),
		Reason:      "root payout account reveal",
		AfterJSON:   `{"revealed":true}`,
		SourceIP:    c.ClientIP(),
		CreatedAtMS: now,
	}
	if err := a.db.Create(&audit).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "audit_failed", "记录查看审计失败", nil)
		return
	}
	c.Header("Cache-Control", "no-store")
	respondOK(c, gin.H{
		"id":             account.ID,
		"agency_id":      account.AgencyID,
		"version":        account.Version,
		"account_type":   plain.AccountType,
		"account_name":   plain.AccountName,
		"account_no":     plain.AccountNo,
		"bank_name":      plain.BankName,
		"last4":          account.Last4,
		"key_id":         keyID,
		"revealed_at_ms": now,
	})
}
