package agencyhub

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type rootFundingReversalRequest struct {
	CommandID           string `json:"command_id"`
	ObjectID            string `json:"object_id"`
	ExpectedVersion     int64  `json:"expected_version"`
	RootProof           string `json:"root_proof"`
	OriginalEventID     string `json:"original_event_id"`
	OriginalOperationID string `json:"original_operation_id"`
	RefundID            string `json:"refund_id"`
	UserID              int64  `json:"user_id"`
	RefundQuota         int64  `json:"refund_quota"`
	CurrencyCode        string `json:"currency_code"`
	PaymentReference    string `json:"payment_reference"`
	EvidenceRef         string `json:"evidence_ref"`
	Reason              string `json:"reason"`
}

type fundingReversalPayload struct {
	OriginalEventID     string `json:"original_event_id,omitempty"`
	OriginalOperationID string `json:"original_operation_id,omitempty"`
	RefundID            string `json:"refund_id"`
	UserID              int64  `json:"user_id"`
	RefundQuota         int64  `json:"refund_quota"`
	CurrencyCode        string `json:"currency_code,omitempty"`
	PaymentReference    string `json:"payment_reference,omitempty"`
	EvidenceRef         string `json:"evidence_ref,omitempty"`
	Reason              string `json:"reason,omitempty"`
}

func (a *App) createFundingReversal(c *gin.Context) {
	var request rootFundingReversalRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可执行资金冲正", nil)
		return
	}
	payload, err := normalizeFundingReversalRequest(request)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reversal", err.Error(), nil)
		return
	}
	payloadBytes, err := common.Marshal(payload)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "marshal_failed", "资金冲正命令生成失败", nil)
		return
	}
	bodyHash, err := CommandBodyHash(payloadBytes)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reversal", err.Error(), nil)
		return
	}
	session, err := a.currentRootSourceSession(identity)
	if err != nil {
		respondError(c, http.StatusForbidden, "invalid_root_session", "Root登录态无效", nil)
		return
	}
	claims, err := a.verifyFundingReversalProof(request, identity, session.SourceSID, bodyHash)
	if err != nil {
		respondError(c, http.StatusForbidden, "invalid_root_proof", "Root资金冲正授权证明无效", nil)
		return
	}
	canonicalPayload, _ := CanonicalPayload(payloadBytes)
	command := model.AgencyCommand{
		CommandID:       strings.TrimSpace(request.CommandID),
		Action:          CommandActionFundingReverse,
		Actor:           "root:" + stringID(identity.ActorID),
		SourceSID:       session.SourceSID,
		ObjectID:        strings.TrimSpace(request.ObjectID),
		ExpectedVersion: request.ExpectedVersion,
		Payload:         string(canonicalPayload),
		BodyHash:        bodyHash,
		IssuedAt:        claims.IssuedAt,
		ExpiresAt:       claims.ExpiresAt,
		RootProof:       strings.TrimSpace(request.RootProof),
		RootProofJTI:    claims.JTI,
		Status:          CommandStatusQueued,
		CreatedAt:       time.Now().Unix(),
		UpdatedAt:       time.Now().Unix(),
	}
	signature, err := a.signCommandEnvelope(AgencyCommandRequest{
		CommandID:       command.CommandID,
		Action:          command.Action,
		Actor:           command.Actor,
		SourceSID:       command.SourceSID,
		ObjectID:        command.ObjectID,
		ExpectedVersion: command.ExpectedVersion,
		Payload:         json.RawMessage(command.Payload),
		IssuedAt:        command.IssuedAt,
		ExpiresAt:       command.ExpiresAt,
		BodyHash:        command.BodyHash,
		RootProof:       command.RootProof,
	})
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "command_signing_unavailable", "资金冲正命令签名服务不可用", nil)
		return
	}
	command.HubSignature = signature
	created, err := a.enqueueRootCommand(command)
	if err != nil {
		status := http.StatusInternalServerError
		code := "command_enqueue_failed"
		message := "资金冲正命令排队失败"
		if errors.Is(err, errCommandConflict) {
			status, code, message = http.StatusConflict, "command_conflict", "command_id已用于其他命令"
		}
		if errors.Is(err, errProofReplayed) {
			status, code, message = http.StatusConflict, "proof_replayed", "Root授权证明已用于其他命令"
		}
		respondError(c, status, code, message, nil)
		return
	}
	respondAccepted(c, commandResponse(created))
}

func normalizeFundingReversalRequest(request rootFundingReversalRequest) (fundingReversalPayload, error) {
	request.CommandID = strings.TrimSpace(request.CommandID)
	request.ObjectID = strings.TrimSpace(request.ObjectID)
	request.RootProof = strings.TrimSpace(request.RootProof)
	request.OriginalEventID = strings.TrimSpace(request.OriginalEventID)
	request.OriginalOperationID = strings.TrimSpace(request.OriginalOperationID)
	request.RefundID = strings.TrimSpace(request.RefundID)
	request.CurrencyCode = strings.TrimSpace(request.CurrencyCode)
	request.PaymentReference = strings.TrimSpace(request.PaymentReference)
	request.EvidenceRef = strings.TrimSpace(request.EvidenceRef)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.CommandID == "" || len(request.CommandID) > 128 || request.ObjectID == "" || len(request.ObjectID) > 191 || request.RootProof == "" {
		return fundingReversalPayload{}, errors.New("command_id, object_id and root_proof are required")
	}
	if request.ExpectedVersion < 0 {
		return fundingReversalPayload{}, errors.New("expected_version must not be negative")
	}
	if request.OriginalEventID == "" && request.OriginalOperationID == "" {
		return fundingReversalPayload{}, errors.New("original funding reference is required")
	}
	if request.RefundID == "" || len(request.RefundID) > 128 {
		return fundingReversalPayload{}, errors.New("refund_id is required")
	}
	if request.UserID <= 0 || request.RefundQuota <= 0 {
		return fundingReversalPayload{}, errors.New("user_id and refund_quota must be positive")
	}
	if len(request.OriginalEventID) > 128 || len(request.OriginalOperationID) > 128 || len(request.CurrencyCode) > 16 || len(request.PaymentReference) > 191 || len(request.EvidenceRef) > 191 || len(request.Reason) > 512 {
		return fundingReversalPayload{}, errors.New("reversal field is too long")
	}
	return fundingReversalPayload{
		OriginalEventID:     request.OriginalEventID,
		OriginalOperationID: request.OriginalOperationID,
		RefundID:            request.RefundID,
		UserID:              request.UserID,
		RefundQuota:         request.RefundQuota,
		CurrencyCode:        request.CurrencyCode,
		PaymentReference:    request.PaymentReference,
		EvidenceRef:         request.EvidenceRef,
		Reason:              request.Reason,
	}, nil
}

func (a *App) currentRootSourceSession(identity *Identity) (model.AgencySession, error) {
	if identity == nil || identity.SessionID <= 0 {
		return model.AgencySession{}, errors.New("missing agency session")
	}
	var session model.AgencySession
	if err := a.db.First(&session, identity.SessionID).Error; err != nil {
		return model.AgencySession{}, err
	}
	now := time.Now().Unix()
	if session.ActorType != ActorTypeRoot || session.ActorID != identity.ActorID || session.SourceSID == "" || session.ExpiresAt <= now || session.RevokedAt != nil {
		return model.AgencySession{}, errors.New("invalid agency session")
	}
	return session, nil
}

func (a *App) verifyFundingReversalProof(request rootFundingReversalRequest, identity *Identity, sourceSID, bodyHash string) (SSOTicketClaims, error) {
	if len(a.ssoPublicKey) != ed25519.PublicKeySize {
		return SSOTicketClaims{}, errors.New("root proof key is not configured")
	}
	claims, err := VerifySSOTicket(a.ssoPublicKey, strings.TrimSpace(request.RootProof), "new-api", "agency-gateway-command")
	if err != nil {
		return SSOTicketClaims{}, err
	}
	now := time.Now().Unix()
	if identity == nil || claims.Subject != identity.ActorID ||
		claims.SourceSID != sourceSID ||
		claims.Action != CommandActionFundingReverse ||
		claims.CommandID != strings.TrimSpace(request.CommandID) ||
		claims.ObjectID != strings.TrimSpace(request.ObjectID) ||
		claims.ExpectedVersion != request.ExpectedVersion ||
		!strings.EqualFold(claims.BodyHash, bodyHash) ||
		claims.ExpiresAt <= now ||
		claims.IssuedAt > now+int64(commandClockSkew/time.Second) {
		return SSOTicketClaims{}, errors.New("root proof command binding mismatch")
	}
	return claims, nil
}

func (a *App) enqueueRootCommand(command model.AgencyCommand) (model.AgencyCommand, error) {
	var stored model.AgencyCommand
	err := a.db.Where("command_id = ?", command.CommandID).First(&stored).Error
	if err == nil {
		req := AgencyCommandRequest{
			CommandID:       command.CommandID,
			Action:          command.Action,
			Actor:           command.Actor,
			SourceSID:       command.SourceSID,
			ObjectID:        command.ObjectID,
			ExpectedVersion: command.ExpectedVersion,
			Payload:         json.RawMessage(command.Payload),
			IssuedAt:        command.IssuedAt,
			ExpiresAt:       command.ExpiresAt,
			BodyHash:        command.BodyHash,
			RootProof:       command.RootProof,
		}
		if commandEnvelopeMatchesStored(req, stored, command.BodyHash) && stored.RootProofJTI == command.RootProofJTI {
			return stored, nil
		}
		return model.AgencyCommand{}, errCommandConflict
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AgencyCommand{}, err
	}
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var reused model.AgencyCommand
		if err := tx.Where("root_proof_jti = ?", command.RootProofJTI).First(&reused).Error; err == nil {
			return errProofReplayed
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&command).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			var raced model.AgencyCommand
			if lookupErr := a.db.Where("command_id = ?", command.CommandID).First(&raced).Error; lookupErr == nil {
				return raced, nil
			}
			return model.AgencyCommand{}, errCommandConflict
		}
		return model.AgencyCommand{}, err
	}
	return command, nil
}

var (
	errCommandConflict = errors.New("agency command conflict")
	errProofReplayed   = errors.New("agency command proof replayed")
)
