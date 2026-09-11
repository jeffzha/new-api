package agencyhub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type idempotentWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *idempotentWriter) Write(data []byte) (int, error) {
	_, _ = w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *idempotentWriter) WriteString(value string) (int, error) {
	_, _ = w.body.WriteString(value)
	return w.ResponseWriter.WriteString(value)
}

func (a *App) idempotencyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead ||
			strings.HasPrefix(c.Request.URL.Path, a.config.BasePath+"/api/v1/auth/") ||
			isAgencyPreviewPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if len(key) < 8 || len(key) > 128 {
			respondError(c, http.StatusBadRequest, "idempotency_key_required", "写请求必须提供有效的 Idempotency-Key", nil)
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20+1))
		if err != nil || len(body) > 1<<20 {
			respondError(c, http.StatusRequestEntityTooLarge, "request_too_large", "请求正文过大", nil)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		identity := currentIdentity(c)
		if identity == nil {
			respondError(c, http.StatusUnauthorized, "unauthorized", "请先登录", nil)
			return
		}
		// Idempotency is about the logical request, not insignificant JSON
		// whitespace. Keep malformed bodies byte-addressed so the handler still
		// owns the format error and cannot accidentally alias two bad requests.
		bodyForHash := normalizeIdempotencyBody(body)
		bodyDigest := sha256.Sum256(bodyForHash)
		bodyHash := hex.EncodeToString(bodyDigest[:])
		agencyScope := ""
		if identity.AgencyID != nil {
			agencyScope = stringID(*identity.AgencyID)
		}
		scopeDigest := sha256.Sum256([]byte(identity.ActorType + ":" + stringID(identity.ActorID) + ":" + agencyScope + ":" + c.Request.URL.Path + ":" + key))
		scopeHash := hex.EncodeToString(scopeDigest[:])
		c.Set("agency_idempotency_scope_hash", scopeHash)
		c.Set("agency_request_body_hash", bodyHash)
		c.Set("agency_idempotency_key", key)
		now := time.Now().Unix()
		var record model.AgencyIdempotencyRecord
		lookupErr := a.db.Where("scope_hash = ?", scopeHash).First(&record).Error
		if lookupErr == nil && record.ExpiresAt > now {
			if record.BodyHash != bodyHash {
				respondError(c, http.StatusConflict, "idempotency_conflict", "相同幂等键的请求正文不同", nil)
				return
			}
			if record.ResultCode == 0 || record.ResponseJSON == "" {
				respondAccepted(c, gin.H{"operation_id": record.ResourceID, "status": "processing"})
				return
			}
			c.Data(record.ResultCode, "application/json; charset=utf-8", []byte(record.ResponseJSON))
			return
		}
		if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusInternalServerError, "database_error", "幂等记录读取失败", nil)
			return
		}
		if lookupErr == nil && record.ExpiresAt <= now {
			_ = a.db.Delete(&record).Error
		}
		if requiresAgencyVerification(c.Request.URL.Path) {
			if !a.consumeOperatorProof(c, bodyHash) {
				return
			}
		}
		record = model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: identity.ActorType, ActorID: identity.ActorID, Action: c.Request.URL.Path, BodyHash: bodyHash, ResourceID: key, ResultCode: 0, ExpiresAt: now + 24*60*60, CreatedAt: now}
		if err := a.db.Create(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				var raced model.AgencyIdempotencyRecord
				if lookupErr := a.db.Where("scope_hash = ?", scopeHash).First(&raced).Error; lookupErr == nil {
					if raced.BodyHash != bodyHash {
						respondError(c, http.StatusConflict, "idempotency_conflict", "相同幂等键的请求正文不同", nil)
						return
					}
					if raced.ResultCode > 0 && raced.ResponseJSON != "" {
						c.Data(raced.ResultCode, "application/json; charset=utf-8", []byte(raced.ResponseJSON))
						return
					}
					respondAccepted(c, gin.H{"operation_id": raced.ResourceID, "status": "processing"})
					return
				}
				// The unique-index winner may not be visible yet on a
				// replica/transaction boundary. Preserve the documented
				// processing response, but keep the submitted key as the
				// operation handle rather than inventing a second one.
				respondAccepted(c, gin.H{"operation_id": key, "status": "processing"})
				return
			}
			respondError(c, http.StatusInternalServerError, "database_error", "幂等记录写入失败", nil)
			return
		}
		writer := &idempotentWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()
		status := c.Writer.Status()
		if status == 0 {
			status = http.StatusOK
		}
		responseJSON := writer.body.String()
		var responseMap map[string]any
		if common.Unmarshal([]byte(responseJSON), &responseMap) == nil {
			redactIdempotencySecrets(responseMap)
			if encoded, marshalErr := common.Marshal(responseMap); marshalErr == nil {
				responseJSON = string(encoded)
			}
		}
		_ = a.db.Model(&model.AgencyIdempotencyRecord{}).Where("id = ? AND result_code = 0", record.ID).Updates(map[string]any{"result_code": status, "response_json": responseJSON}).Error
	}
}

func isAgencyPreviewPath(path string) bool {
	return strings.HasSuffix(path, "/pricing/preview") ||
		strings.HasSuffix(path, "/pricing/sales/preview")
}

func normalizeIdempotencyBody(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []byte{}
	}
	if canonical, err := canonicalJSON(trimmed, 0); err == nil {
		return canonical
	}
	return body
}

func requiresAgencyVerification(path string) bool {
	if strings.HasSuffix(path, "/enter") || strings.HasSuffix(path, "/leave-agency") {
		return false
	}
	for _, fragment := range []string{"/pricing/publish", "/root/agencies", "/root/deliveries/", "/root/users/", "/root/provisioning/", "/root/withdrawals/", "/root/reconciliation/", "/withdrawals", "/withdrawal-accounts"} {
		if strings.Contains(path, fragment) {
			return true
		}
	}
	return false
}

func (a *App) consumeOperatorProof(c *gin.Context, bodyHash string) bool {
	identity := currentIdentity(c)
	if identity == nil {
		respondError(c, http.StatusUnauthorized, "unauthorized", "请先登录", nil)
		return false
	}
	raw := strings.TrimSpace(c.GetHeader("X-Agency-Verification-Proof"))
	if raw == "" {
		respondError(c, http.StatusForbidden, "verification_required", "该操作需要二次验证", nil)
		return false
	}
	now := time.Now().Unix()
	if identity.ActorType != ActorTypeOperator {
		if len(a.ssoPublicKey) != 32 {
			respondError(c, http.StatusForbidden, "verification_required", "该操作需要管理员验证证明", nil)
			return false
		}
		claims, err := VerifySSOTicket(a.ssoPublicKey, raw, "new-api", "agency-hub-verification")
		var session model.AgencySession
		sessionErr := a.db.First(&session, identity.SessionID).Error
		if err != nil || sessionErr != nil || claims.Subject != identity.ActorID || claims.BodyHash != bodyHash || claims.SourceSID == "" || claims.SourceSID != session.SourceSID || claims.SessionVersion != session.SourceSessionVersion || !proofScopeMatchesRequest(c, identity, claims.Action, claims.ObjectID) {
			respondError(c, http.StatusForbidden, "invalid_verification", "验证证明无效", nil)
			return false
		}
		used := &model.AgencyVerificationUse{JTI: tokenHash(raw), ActorType: identity.ActorType, ActorID: identity.ActorID, Action: claims.Action, ObjectID: claims.ObjectID, BodyHash: bodyHash, ExpiresAt: claims.ExpiresAt, CreatedAt: now}
		if err := a.db.Create(used).Error; err != nil {
			respondError(c, http.StatusForbidden, "invalid_verification", "验证证明无效或已使用", nil)
			return false
		}
		return true
	}
	query := a.db.Model(&model.AgencyVerificationUse{}).
		Where("jti = ? AND actor_type = ? AND actor_id = ? AND body_hash = ? AND expires_at > ? AND consumed_at IS NULL", tokenHash(raw), identity.ActorType, identity.ActorID, bodyHash, now)
	if action, objectID, ok := expectedProofScope(c, identity); ok {
		query = query.Where("action = ? AND object_id = ?", action, objectID)
	}
	result := query.Updates(map[string]any{"consumed_at": now})
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "验证失败", nil)
		return false
	}
	if result.RowsAffected != 1 {
		respondError(c, http.StatusForbidden, "invalid_verification", "验证证明无效或已使用", nil)
		return false
	}
	return true
}

func proofScopeMatchesRequest(c *gin.Context, identity *Identity, action, objectID string) bool {
	if strings.Contains(c.Request.URL.Path, "/root/deliveries/") {
		return action == "delivery.ack" &&
			objectID == "delivery:"+strings.TrimSpace(c.Param("delivery_id"))
	}
	if expectedAction, expectedObjectID, ok := expectedProofScope(c, identity); ok {
		return action == expectedAction && objectID == expectedObjectID
	}
	return true
}

func expectedProofScope(c *gin.Context, identity *Identity) (string, string, bool) {
	if c == nil || identity == nil {
		return "", "", false
	}
	path := c.Request.URL.Path
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/withdrawals") {
		if identity.AgencyID == nil {
			return "", "", false
		}
		return "withdrawal.create", "agency:" + stringID(*identity.AgencyID), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/cancel") && strings.Contains(path, "/withdrawals/") {
		return "withdrawal.cancel", "withdrawal:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/pricing/sales/publish") {
		if identity.AgencyID == nil {
			return "", "", false
		}
		return "pricing.sales.publish", "agency:" + stringID(*identity.AgencyID), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/pricing/publish") && strings.Contains(path, "/root/agencies/") {
		return "pricing.root.publish", "agency:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/root/agencies") {
		return "agency.create", "agency:new", true
	}
	if c.Request.Method == http.MethodPatch && strings.Contains(path, "/root/agencies/") {
		return "agency.update", "agency:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/disable") && strings.Contains(path, "/root/agencies/") {
		return "agency.disable", "agency:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/enable") && strings.Contains(path, "/root/agencies/") {
		return "agency.enable", "agency:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/reset-password") && strings.Contains(path, "/root/agencies/") {
		return "agency.reset_password", "agency:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/bind") && strings.Contains(path, "/root/users/") {
		return "user.bind", "user:" + strings.TrimSpace(c.Param("user_id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/transfer") && strings.Contains(path, "/root/users/") {
		return "user.transfer", "user:" + strings.TrimSpace(c.Param("user_id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/cancel") && strings.Contains(path, "/root/provisioning/") {
		return "provisioning.cancel", "provisioning:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/resolve") && strings.Contains(path, "/root/reconciliation/issues/") {
		return "reconciliation.resolve", "reconciliation:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/withdrawal-accounts") {
		if identity.AgencyID == nil {
			return "", "", false
		}
		return "withdrawal_account.create", "agency:" + stringID(*identity.AgencyID), true
	}
	if c.Request.Method == http.MethodPatch && strings.Contains(path, "/withdrawal-accounts/") {
		return "withdrawal_account.update", "withdrawal_account:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/disable") && strings.Contains(path, "/withdrawal-accounts/") {
		return "withdrawal_account.disable", "withdrawal_account:" + strings.TrimSpace(c.Param("id")), true
	}
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/reveal") && strings.Contains(path, "/withdrawal-accounts/") {
		return "withdrawal_account.reveal", "withdrawal_account:" + strings.TrimSpace(c.Param("id")), true
	}
	return "", "", false
}

func redactIdempotencySecrets(value map[string]any) {
	for key, item := range value {
		if key == "temporary_password" || key == "password" || key == "proof" || key == "ticket" {
			value[key] = "[redacted]"
			continue
		}
		if nested, ok := item.(map[string]any); ok {
			redactIdempotencySecrets(nested)
			continue
		}
		if nested, ok := item.([]any); ok {
			for _, child := range nested {
				if childMap, ok := child.(map[string]any); ok {
					redactIdempotencySecrets(childMap)
				}
			}
		}
	}
}

func stringID(value int64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatInt(value, 10)
}
