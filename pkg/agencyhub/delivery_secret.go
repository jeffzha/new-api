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

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	deliverySecretTTL       = 5 * time.Minute
	deliveryAADSchema       = "agency-delivery-v2"
	deliveryFieldName       = "temporary_password"
	deliveryScopeContext    = "agency_idempotency_scope_hash"
	deliveryBodyHashContext = "agency_request_body_hash"
)

type deliveryBinding struct {
	SourceSID      string
	Action         string
	ObjectID       string
	BodyHash       string
	IdempotencyKey string
}

func (a *App) deliveryKey() ([]byte, string, error) {
	raw := strings.TrimSpace(os.Getenv("AGENCY_HUB_DELIVERY_KEY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("AGENCY_DELIVERY_KEY"))
	}
	path := strings.TrimSpace(a.config.DeliveryKeyFile)
	if raw == "" && path == "" {
		path = strings.TrimSpace(os.Getenv("AGENCY_HUB_DELIVERY_KEY_FILE"))
	}
	if raw == "" && path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", errors.New("AGENCY_HUB_DELIVERY_KEY_FILE is unreadable")
		}
		raw = strings.TrimSpace(string(data))
	}
	key, err := decodeDeliveryKey(raw)
	if err != nil {
		return nil, "", err
	}
	keyID := strings.TrimSpace(os.Getenv("AGENCY_HUB_DELIVERY_KEY_ID"))
	if keyID == "" {
		keyID = "agency-delivery-v1"
	}
	if len(keyID) > 64 {
		return nil, "", errors.New("AGENCY_HUB_DELIVERY_KEY_ID is too long")
	}
	return key, keyID, nil
}

func decodeDeliveryKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("AGENCY_HUB_DELIVERY_KEY is required")
	}
	for _, decode := range []func(string) ([]byte, error){
		base64.RawURLEncoding.DecodeString,
		base64.StdEncoding.DecodeString,
		hex.DecodeString,
	} {
		key, err := decode(raw)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	if len([]byte(raw)) == 32 {
		return []byte(raw), nil
	}
	return nil, errors.New("AGENCY_HUB_DELIVERY_KEY must be 32 bytes")
}

func deliveryAAD(operationID string, agencyID, operatorAccountID int64, schema string) []byte {
	return []byte(fmt.Sprintf("%s|operation=%s|agency=%d|account=%d|field=%s", schema, operationID, agencyID, operatorAccountID, deliveryFieldName))
}

func (a *App) encryptDeliverySecret(operationID, password string, aad []byte) (string, string, error) {
	key, keyID, err := a.deliveryKey()
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
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(password), aad)
	return base64.RawURLEncoding.EncodeToString(sealed), keyID, nil
}

func (a *App) decryptDeliverySecret(delivery model.AgencyDeliverySecret) (string, error) {
	key, _, err := a.deliveryKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(delivery.Ciphertext))
	if err != nil {
		return "", errors.New("invalid delivery ciphertext")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) <= gcm.NonceSize() {
		return "", errors.New("invalid delivery ciphertext")
	}
	var aad []byte
	if delivery.AADSchema == "" {
		// Rows written before AADSchema was introduced used operationID as
		// their associated data. Keep this narrow compatibility path so a
		// deployment can rotate the schema without making an active delivery
		// impossible to acknowledge.
		aad = []byte(delivery.OperationID)
	} else {
		aad = deliveryAAD(delivery.OperationID, delivery.AgencyID, delivery.OperatorAccountID, delivery.AADSchema)
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], aad)
	if err != nil {
		return "", errors.New("delivery secret decryption failed")
	}
	return string(plain), nil
}

func deliveryOperationID(c *gin.Context, identity *Identity) string {
	if value, ok := c.Get(deliveryScopeContext); ok {
		if scopeHash, ok := value.(string); ok && strings.TrimSpace(scopeHash) != "" {
			return scopeHash
		}
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	return tokenHash(identity.ActorType + ":" + stringID(identity.ActorID) + ":" + stringID(pointerID(identity.AgencyID)) + ":" + identity.SourceSID + ":" + c.Request.URL.Path + ":" + key)
}

func pointerID(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func deliveryBindingFromContext(c *gin.Context, identity *Identity, action, objectID string) deliveryBinding {
	bodyHash, _ := c.Get(deliveryBodyHashContext)
	bodyHashValue, _ := bodyHash.(string)
	return deliveryBinding{
		SourceSID:      identity.SourceSID,
		Action:         action,
		ObjectID:       objectID,
		BodyHash:       bodyHashValue,
		IdempotencyKey: strings.TrimSpace(c.GetHeader("Idempotency-Key")),
	}
}

func (a *App) createDeliverySecretTx(tx *gorm.DB, creatorRootID int64, operationID, password string, now int64, binding deliveryBinding, agencyID, operatorAccountID int64) (model.AgencyDeliverySecret, error) {
	if strings.TrimSpace(operationID) == "" {
		return model.AgencyDeliverySecret{}, errors.New("delivery operation required")
	}
	ciphertext, keyID, err := a.encryptDeliverySecret(operationID, password, deliveryAAD(operationID, agencyID, operatorAccountID, deliveryAADSchema))
	if err != nil {
		return model.AgencyDeliverySecret{}, err
	}
	if err := tx.Where("operation_id = ? AND (expires_at <= ? OR delivered_at IS NOT NULL)", operationID, now/1000).Delete(&model.AgencyDeliverySecret{}).Error; err != nil {
		return model.AgencyDeliverySecret{}, err
	}
	row := model.AgencyDeliverySecret{
		OperationID:       operationID,
		CreatorRootID:     creatorRootID,
		AgencyID:          agencyID,
		OperatorAccountID: operatorAccountID,
		SourceSID:         binding.SourceSID,
		Action:            binding.Action,
		ObjectID:          binding.ObjectID,
		BodyHash:          binding.BodyHash,
		IdempotencyKey:    binding.IdempotencyKey,
		Ciphertext:        ciphertext,
		KeyID:             keyID,
		AADSchema:         deliveryAADSchema,
		ExpiresAt:         now/1000 + int64(deliverySecretTTL/time.Second),
		CreatedAt:         now / 1000,
	}
	return row, tx.Create(&row).Error
}

func (a *App) validateDeliveryBinding(tx *gorm.DB, delivery model.AgencyDeliverySecret, identity *Identity) error {
	if delivery.CreatorRootID != identity.ActorID {
		return errDeliveryForbidden
	}
	// Recovery is bound to the Root actor and the source browser session that
	// authorized the original idempotent operation. A different session of
	// the same Root must not become a password-recovery capability.
	if delivery.SourceSID != "" && delivery.SourceSID != identity.SourceSID {
		return errDeliveryForbidden
	}
	scopeHash := strings.TrimPrefix(delivery.OperationID, "idem:")
	if scopeHash == "" {
		return errDeliveryBinding
	}
	var record model.AgencyIdempotencyRecord
	if err := tx.Where("scope_hash = ?", scopeHash).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errDeliveryBinding
		}
		return err
	}
	if record.ActorType != ActorTypeRoot || record.ActorID != identity.ActorID ||
		record.ResultCode < http.StatusOK || record.ResultCode >= http.StatusMultipleChoices {
		return errDeliveryBinding
	}
	if delivery.Action != "" && record.Action != delivery.Action {
		return errDeliveryBinding
	}
	if delivery.BodyHash != "" && record.BodyHash != delivery.BodyHash {
		return errDeliveryBinding
	}
	if delivery.IdempotencyKey != "" && record.ResourceID != delivery.IdempotencyKey {
		return errDeliveryBinding
	}
	base := a.config.BasePath + "/api/v1/root/"
	if record.Action != base+"agencies" && !strings.HasSuffix(record.Action, "/reset-password") {
		return errDeliveryBinding
	}
	return nil
}

func (a *App) acknowledgeDeliverySecret(c *gin.Context) {
	id, err := parseID(c.Param("delivery_id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的交付ID", nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可领取临时密码", nil)
		return
	}
	var request struct {
		OperationID string `json:"operation_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.OperationID) == "" {
		respondError(c, http.StatusBadRequest, "delivery_operation_required", "领取临时密码必须提供原创建/重置操作句柄", nil)
		return
	}
	now := time.Now().Unix()
	var password string
	var delivery model.AgencyDeliverySecret
	expired := false
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).First(&delivery, id).Error; err != nil {
			return err
		}
		if delivery.OperationID != strings.TrimSpace(request.OperationID) {
			return errDeliveryBinding
		}
		if err := a.validateDeliveryBinding(tx, delivery, identity); err != nil {
			return err
		}
		if delivery.DeliveredAt != nil {
			return errDeliveryConsumed
		}
		if delivery.ExpiredAt != nil || delivery.ExpiresAt <= now || delivery.Ciphertext == "" {
			deliveredAt := now
			if err := tx.Model(&delivery).Updates(map[string]any{"ciphertext": "", "expired_at": &deliveredAt}).Error; err != nil {
				return err
			}
			expired = true
			return nil
		}
		plain, err := a.decryptDeliverySecret(delivery)
		if err != nil {
			return err
		}
		deliveredAt := now
		if err := tx.Model(&delivery).Updates(map[string]any{"ciphertext": "", "delivered_at": &deliveredAt}).Error; err != nil {
			return err
		}
		password = plain
		return nil
	})
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		respondError(c, http.StatusNotFound, "delivery_not_found", "临时密码交付不存在", nil)
	case errors.Is(err, errDeliveryForbidden):
		respondError(c, http.StatusForbidden, "delivery_forbidden", "不能领取其他管理员创建的临时密码", nil)
	case errors.Is(err, errDeliveryConsumed):
		respondError(c, http.StatusConflict, "delivery_consumed", "临时密码已经领取", nil)
	case errors.Is(err, errDeliveryBinding):
		respondError(c, http.StatusConflict, "delivery_binding_invalid", "临时密码交付请求已失效，请重新创建或重置", nil)
	case err != nil:
		respondError(c, http.StatusServiceUnavailable, "delivery_unavailable", "临时密码交付服务不可用", nil)
	case expired:
		respondError(c, http.StatusGone, "delivery_expired", "临时密码已过期，请重新重置密码", nil)
	default:
		c.Header("Cache-Control", "no-store")
		respondOK(c, gin.H{"delivery_id": delivery.ID, "temporary_password": password, "expires_at": delivery.ExpiresAt})
	}
}

var (
	errDeliveryForbidden = errors.New("delivery forbidden")
	errDeliveryConsumed  = errors.New("delivery consumed")
	errDeliveryExpired   = errors.New("delivery expired")
	errDeliveryBinding   = errors.New("delivery binding invalid")
)

// CleanupExpiredDeliverySecrets destroys ciphertext while retaining a
// tombstone so a stale delivery id returns "expired" instead of accidentally
// looking like a new capability. It is safe to run concurrently and can be
// called by a periodic worker or an operator-triggered maintenance job.
func (a *App) CleanupExpiredDeliverySecrets(now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	expiredAt := now
	return a.db.Model(&model.AgencyDeliverySecret{}).
		Where("expires_at <= ? AND ciphertext <> ? AND delivered_at IS NULL", now, "").
		Updates(map[string]any{"ciphertext": "", "expired_at": &expiredAt}).Error
}
