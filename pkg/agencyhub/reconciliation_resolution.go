package agencyhub

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type reconciliationResolutionRequest struct {
	Action               string `json:"action"`
	Status               string `json:"status"`
	ExpectedEvidenceHash string `json:"expected_evidence_hash"`
	Resolution           string `json:"resolution"`
}

var errReconciliationEvidenceChanged = errors.New("reconciliation evidence changed")
var errReconciliationUnresolved = errors.New("invariant is inconsistent or cannot be verified")
var errReconciliationClosed = errors.New("reconciliation issue is already closed")
var errReconciliationProof = errors.New("invalid or previously consumed root verification")
var errReconciliationIdempotency = errors.New("idempotency key already used with different request")

func (a *App) getReconciliationIssue(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的异常ID", nil)
		return
	}
	var issue model.AgencyReconciliationIssue
	var verification reconciliationVerification
	err = a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&issue, id).Error; err != nil {
			return err
		}
		var err error
		verification, err = reconciliationEvidence(tx, issue, false)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "not_found", "对账异常不存在", nil)
		} else {
			respondError(c, http.StatusServiceUnavailable, "verification_unavailable", "暂时无法核验权威数据", nil)
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	respondOK(c, gin.H{"issue": reconciliationIssueView(issue), "verification": verification})
}

func (a *App) resolveReconciliationIssue(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的异常ID", nil)
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		respondError(c, http.StatusBadRequest, "idempotency_key_required", "请提供有效的幂等键", nil)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求正文无效", nil)
		return
	}
	var request reconciliationResolutionRequest
	if err := common.Unmarshal(body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Resolution = strings.TrimSpace(request.Resolution)
	if (request.Action != "verify_resolved" && request.Action != "restore_delivery") || (request.Status != "resolved" && request.Status != "ignored") || (request.Action == "restore_delivery" && request.Status != "resolved") || request.Resolution == "" || len(request.Resolution) > 2000 || len(request.ExpectedEvidenceHash) != 64 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_resolution", "请提供核验方式、状态、证据指纹及处理说明", nil)
		return
	}
	digest := sha256.Sum256(normalizeIdempotencyBody(body))
	bodyHash := hex.EncodeToString(digest[:])
	scopeDigest := sha256.Sum256([]byte("reconciliation.resolve\x00" + stringID(identity.ActorID) + "\x00" + stringID(id) + "\x00" + key))
	scopeHash := hex.EncodeToString(scopeDigest[:])
	rawProof := strings.TrimSpace(c.GetHeader("X-Agency-Verification-Proof"))
	var response []byte
	var latest reconciliationVerification
	err = a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// First statement reserves SQLite's writer and serializes same-actor
		// retries before reading a snapshot on MySQL/PostgreSQL.
		if err := tx.Model(&model.AgencySession{}).Where("actor_type = ? AND actor_id = ?", ActorTypeRoot, identity.ActorID).UpdateColumn("last_seen_at", gorm.Expr("last_seen_at")).Error; err != nil {
			return err
		}
		var session model.AgencySession
		now := time.Now().Unix()
		if err := tx.Where("id = ? AND actor_type = ? AND actor_id = ? AND revoked_at IS NULL AND expires_at > ?", identity.SessionID, ActorTypeRoot, identity.ActorID, now).First(&session).Error; err != nil {
			return errReconciliationProof
		}
		checkApp := &App{db: tx}
		if err := checkApp.validateRootSourceSession(session); err != nil {
			return errReconciliationProof
		}
		var existing model.AgencyIdempotencyRecord
		lookup := tx.Where("scope_hash = ?", scopeHash).Limit(1).Find(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 1 {
			if existing.BodyHash != bodyHash {
				return errReconciliationIdempotency
			}
			if existing.ResultCode != http.StatusOK || existing.ResponseJSON == "" {
				return errReconciliationClosed
			}
			response = []byte(existing.ResponseJSON)
			return nil
		}
		claims, err := VerifySSOTicket(a.ssoPublicKey, rawProof, "new-api", "agency-hub-verification")
		if err != nil || claims.Subject != identity.ActorID || claims.SourceSID != session.SourceSID || claims.SessionVersion != session.SourceSessionVersion || claims.Action != "reconciliation.resolve" || claims.ObjectID != "reconciliation_issue:"+stringID(id) || claims.BodyHash != bodyHash {
			return errReconciliationProof
		}
		var issue model.AgencyReconciliationIssue
		if err := model.AgencyLockForUpdate(tx).First(&issue, id).Error; err != nil {
			return err
		}
		legacyClosed := (issue.Status == "resolved" || issue.Status == "ignored") && strings.TrimSpace(issue.ResolutionEvidence) == ""
		if issue.Status != "open" && !legacyClosed {
			return errReconciliationClosed
		}
		before, err := reconciliationEvidence(tx, issue, true)
		if err != nil {
			return err
		}
		latest = before
		if before.EvidenceHash != request.ExpectedEvidenceHash {
			return errReconciliationEvidenceChanged
		}
		allowed := false
		for _, action := range before.AllowedActions {
			if action == request.Action {
				allowed = true
			}
		}
		if !allowed {
			return errReconciliationUnresolved
		}
		used := model.AgencyVerificationUse{JTI: tokenHash(rawProof), ActorType: ActorTypeRoot, ActorID: identity.ActorID, SessionID: identity.SessionID, Action: claims.Action, ObjectID: claims.ObjectID, BodyHash: bodyHash, ExpiresAt: claims.ExpiresAt, CreatedAt: now, ConsumedAt: &now}
		proof := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "jti"}}, DoNothing: true}).Create(&used)
		if proof.Error != nil {
			return proof.Error
		}
		if proof.RowsAffected != 1 {
			return errReconciliationProof
		}
		repairEventID := ""
		if request.Action == "restore_delivery" {
			if issue.ObjectType != "billing_outbox" {
				return errReconciliationUnresolved
			}
			var receipt model.AgencySourceEvent
			lookup := tx.Where("event_id = ?", issue.ObjectID).Limit(1).Find(&receipt)
			if lookup.Error != nil {
				return lookup.Error
			}
			delivery := model.AgencyEventDelivery{EventID: issue.ObjectID, Status: "pending", CreatedAt: now}
			if lookup.RowsAffected == 1 {
				delivery.Status = "done"
				delivery.ProcessedAt = &now
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_id"}}, DoNothing: true}).Create(&delivery).Error; err != nil {
				return err
			}
			repairEventID = issue.ObjectID
		}
		after, err := reconciliationEvidence(tx, issue, true)
		if err != nil {
			return err
		}
		if after.State != "consistent" {
			return errReconciliationUnresolved
		}
		evidence, err := common.Marshal(gin.H{"action": request.Action, "previous_issue": reconciliationIssueView(issue), "before": before, "after": after})
		if err != nil {
			return err
		}
		finished := time.Now().UnixMilli()
		update := tx.Model(&model.AgencyReconciliationIssue{}).Where("id = ? AND status = ?", issue.ID, issue.Status).Updates(map[string]any{"status": request.Status, "active_key": nil, "resolution": request.Resolution, "resolution_evidence": string(evidence), "repair_event_id": repairEventID, "actor_id": identity.ActorID, "resolved_at_ms": finished})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errReconciliationClosed
		}
		issue.Status = request.Status
		issue.ActiveKey = nil
		issue.Resolution = request.Resolution
		issue.ResolutionEvidence = string(evidence)
		issue.RepairEventID = repairEventID
		issue.ActorID = &identity.ActorID
		issue.ResolvedAtMS = &finished
		if err := recordAuditTx(tx, c, identity, "reconciliation.resolve", "reconciliation_issue", stringID(issue.ID), request.Resolution, before, gin.H{"verification": after, "action": request.Action, "status": request.Status, "repair_event_id": repairEventID}); err != nil {
			return err
		}
		response, err = common.Marshal(apiResponse{Success: true, Data: gin.H{"issue": reconciliationIssueView(issue), "verification": after}, RequestID: requestID(c)})
		if err != nil {
			return err
		}
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: ActorTypeRoot, ActorID: identity.ActorID, Action: "reconciliation.resolve", BodyHash: bodyHash, ResourceID: stringID(issue.ID), ResultCode: http.StatusOK, ResponseJSON: string(response), ExpiresAt: now + 86400, CreatedAt: now}).Error
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		switch {
		case errors.Is(err, errReconciliationProof):
			respondError(c, http.StatusForbidden, "invalid_verification", "验证证明无效或已使用", nil)
		case errors.Is(err, gorm.ErrRecordNotFound):
			respondError(c, http.StatusNotFound, "not_found", "对账异常不存在", nil)
		case errors.Is(err, errReconciliationEvidenceChanged):
			respondError(c, http.StatusConflict, "evidence_changed", "权威数据已变化，请重新检查后确认", gin.H{"verification": latest})
		case errors.Is(err, errReconciliationUnresolved):
			respondError(c, http.StatusConflict, "invariant_unresolved", "差异仍然存在或无法核验，不能关闭或忽略", gin.H{"verification": latest})
		case errors.Is(err, errReconciliationIdempotency):
			respondError(c, http.StatusConflict, "idempotency_conflict", "幂等键已用于其他请求", nil)
		case errors.Is(err, errReconciliationClosed):
			respondError(c, http.StatusConflict, "issue_closed", "该异常已由其他操作处理", nil)
		default:
			respondError(c, http.StatusServiceUnavailable, "resolution_unavailable", "处理未提交，请稍后重新核验并重试", nil)
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/json; charset=utf-8", response)
}
