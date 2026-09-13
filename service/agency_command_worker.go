package service

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"gorm.io/gorm"
)

// The gateway accepts signed commands and executes them against the main DB.
// Current authorization, business changes and the permanent command result
// share one transaction; a crash cannot commit money without its receipt.
const (
	agencyCommandQueued     = "queued"
	agencyCommandProcessing = "processing"
	agencyCommandSucceeded  = "succeeded"
	agencyCommandFailed     = "failed"
	agencyCommandCancelled  = "cancelled"
	agencyCommandWorkerTick = 2 * time.Second
	agencyCommandLease      = 5 * time.Minute
)

type AgencyCommandWorkerSummary struct {
	Claimed   int
	Succeeded int
	Failed    int
}

type agencyFundingReverseCommand struct {
	OriginalEventID     string `json:"original_event_id"`
	OriginalOperationID string `json:"original_operation_id"`
	RefundID            string `json:"refund_id"`
	UserID              int64  `json:"user_id"`
	Quota               int64  `json:"quota"`
	RefundQuota         int64  `json:"refund_quota"`
	CurrencyCode        string `json:"currency_code"`
	PaymentReference    string `json:"payment_reference"`
	EvidenceRef         string `json:"evidence_ref"`
	Reason              string `json:"reason"`
}

type agencyProvisioningStartCommand struct {
	UserID              int64  `json:"user_id"`
	InviteCode          string `json:"invite_code"`
	ExpectedUserVersion int64  `json:"expected_user_version"`
	Reason              string `json:"reason"`
}

type agencyProvisioningCancelCommand struct {
	UserID int64  `json:"user_id"`
	Reason string `json:"reason"`
}

type agencyCommandCancelledError struct {
	err error
}

func (e *agencyCommandCancelledError) Error() string {
	if e == nil || e.err == nil {
		return "agency command authorization was revoked"
	}
	return e.err.Error()
}

func (e *agencyCommandCancelledError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// StartAgencyCommandWorker starts one lightweight worker per gateway process.
// Rows are claimed with a conditional update, so multiple gateway instances
// can safely run this loop against the same database.
func StartAgencyCommandWorker(ctx context.Context) {
	if model.DB == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(agencyCommandWorkerTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := RunAgencyCommandWorkerOnce(ctx, 20); err != nil {
					common.SysError("agency command worker failed: " + err.Error())
				}
			}
		}
	}()
}

// RunAgencyCommandWorkerOnce is exported for deterministic integration tests
// and for operators that prefer an externally scheduled worker.
func RunAgencyCommandWorkerOnce(ctx context.Context, limit int) (AgencyCommandWorkerSummary, error) {
	var summary AgencyCommandWorkerSummary
	if model.DB == nil {
		return summary, errors.New("main database is unavailable")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := time.Now().Unix()
	// A process crash must not leave a command in processing forever.
	if err := model.DB.WithContext(ctx).Model(&model.AgencyCommand{}).
		Where("status = ? AND updated_at < ?", agencyCommandProcessing, now-int64(agencyCommandLease/time.Second)).
		Updates(map[string]any{"status": agencyCommandQueued, "updated_at": now}).Error; err != nil {
		return summary, err
	}

	var commands []model.AgencyCommand
	if err := model.DB.WithContext(ctx).
		Where("status = ?", agencyCommandQueued).
		Order("id ASC").Limit(limit).Find(&commands).Error; err != nil {
		return summary, err
	}
	for _, candidate := range commands {
		claimed, err := claimAgencyCommand(candidate.ID)
		if err != nil {
			return summary, err
		}
		if !claimed {
			continue
		}
		summary.Claimed++
		if err := executeAgencyCommand(ctx, candidate); err != nil {
			summary.Failed++
			status, code := agencyCommandFailed, 500
			var cancelled *agencyCommandCancelledError
			if errors.As(err, &cancelled) {
				status, code = agencyCommandCancelled, 403
			}
			if setErr := setAgencyCommandResult(candidate.CommandID, status, code, nil, err.Error()); setErr != nil {
				return summary, errors.Join(err, setErr)
			}
			continue
		}
		summary.Succeeded++
	}
	return summary, nil
}

func claimAgencyCommand(id int64) (bool, error) {
	if id <= 0 {
		return false, errors.New("invalid agency command id")
	}
	result := model.DB.Model(&model.AgencyCommand{}).
		Where("id = ? AND status = ?", id, agencyCommandQueued).
		Updates(map[string]any{"status": agencyCommandProcessing, "updated_at": time.Now().Unix()})
	return result.RowsAffected == 1, result.Error
}

func executeAgencyCommand(ctx context.Context, command model.AgencyCommand) error {
	var fundingUserID int64
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.AgencyCommand
		if err := model.AgencyLockForUpdate(tx).Where("command_id = ?", command.CommandID).First(&current).Error; err != nil {
			return err
		}
		// Another worker may have finished after this worker read the queue.
		// Never execute from that stale envelope or overwrite its receipt.
		if current.Status == agencyCommandSucceeded || current.Status == agencyCommandFailed || current.Status == agencyCommandCancelled {
			return nil
		}
		if current.Status != agencyCommandProcessing {
			return errors.New("agency command is not claimed")
		}
		if err := executeAgencyCommandTx(tx, current); err != nil {
			return err
		}
		if current.Action == agencyhub.CommandActionFundingReverse {
			var payload agencyFundingReverseCommand
			if err := common.UnmarshalJsonStr(current.Payload, &payload); err != nil {
				return err
			}
			fundingUserID = payload.UserID
		}
		return nil
	})
	if err == nil && fundingUserID > 0 && common.RedisEnabled {
		if cacheErr := model.InvalidateUserCache(int(fundingUserID)); cacheErr != nil {
			common.SysError("agency command wallet cache invalidation: " + cacheErr.Error())
		}
	}
	return err
}

func executeAgencyCommandTx(tx *gorm.DB, command model.AgencyCommand) error {
	if err := verifyCurrentAgencyCommandTx(tx, command); err != nil {
		return &agencyCommandCancelledError{err: err}
	}
	switch command.Action {
	case agencyhub.CommandActionProvisioningStart:
		var payload agencyProvisioningStartCommand
		if err := common.Unmarshal([]byte(command.Payload), &payload); err != nil {
			return fmt.Errorf("invalid provisioning.start payload: %w", err)
		}
		if payload.UserID <= 0 {
			payload.UserID, _ = strconv.ParseInt(strings.TrimSpace(command.ObjectID), 10, 64)
		}
		if payload.UserID <= 0 || strings.TrimSpace(payload.InviteCode) == "" {
			return errors.New("provisioning.start requires user_id and invite_code")
		}
		// Provisioning's repository locks agency before the target user. Keep
		// that order while checking the expected version in this outer transaction.
		var agency model.Agency
		if err := model.AgencyLockForUpdate(tx).Where("invite_code = ?", payload.InviteCode).First(&agency).Error; err != nil {
			return err
		}
		var user model.User
		if err := model.AgencyLockForUpdate(tx).Select("id, auth_version").First(&user, payload.UserID).Error; err != nil {
			return err
		}
		if payload.ExpectedUserVersion <= 0 || payload.ExpectedUserVersion != command.ExpectedVersion {
			return errors.New("provisioning.start expected user version mismatch")
		}
		if user.AuthVersion != payload.ExpectedUserVersion {
			return fmt.Errorf("user auth version changed: expected %d, got %d", payload.ExpectedUserVersion, user.AuthVersion)
		}
		rootID, err := agencyCommandActorID(command.Actor)
		if err != nil {
			return err
		}
		hub := agencyhub.New(tx, model.LOG_DB, agencyhub.Config{BasePath: "/agency"})
		job, created, err := hub.EnqueueProvisioningJob(payload.UserID, payload.InviteCode, rootID, strings.TrimSpace(payload.Reason))
		if err != nil {
			return err
		}
		return setAgencyCommandResultTx(tx, command.CommandID, agencyCommandSucceeded, 200, map[string]any{
			"job_id": job.ID, "user_id": payload.UserID, "created": created, "status": job.Status,
		}, "")

	case agencyhub.CommandActionProvisioningCancel:
		var payload agencyProvisioningCancelCommand
		if err := common.Unmarshal([]byte(command.Payload), &payload); err != nil {
			return fmt.Errorf("invalid provisioning.cancel payload: %w", err)
		}
		jobID, err := strconv.ParseInt(strings.TrimSpace(command.ObjectID), 10, 64)
		if err != nil || jobID <= 0 {
			return errors.New("provisioning.cancel requires a numeric job object_id")
		}
		var job model.AgencyProvisioningJob
		if err := model.AgencyLockForUpdate(tx).Select("id, user_id, status, fencing_token").First(&job, jobID).Error; err != nil {
			return err
		}
		if job.FencingToken != command.ExpectedVersion {
			return errors.New("provisioning.cancel job version changed")
		}
		// The job object itself is authoritative for the target user. Older
		// hub command envelopes only carried a reason in the payload, so fill
		// the optional user_id from the loaded job before validating it.
		if payload.UserID <= 0 {
			payload.UserID = job.UserID
		}
		if job.UserID != payload.UserID {
			return errors.New("provisioning.cancel user_id does not match job")
		}
		if job.Status == "cancelled" {
			return setAgencyCommandResultTx(tx, command.CommandID, agencyCommandCancelled, 200, map[string]any{
				"job_id": jobID, "status": agencyCommandCancelled, "already_cancelled": true,
			}, "")
		}
		rootID, err := agencyCommandActorID(command.Actor)
		if err != nil {
			return err
		}
		hub := agencyhub.New(tx, model.LOG_DB, agencyhub.Config{BasePath: "/agency"})
		if err := hub.CancelProvisioningJob(jobID, strings.TrimSpace(payload.Reason), rootID); err != nil {
			return err
		}
		return setAgencyCommandResultTx(tx, command.CommandID, agencyCommandCancelled, 200, map[string]any{
			"job_id": jobID, "status": agencyCommandCancelled,
		}, "")

	case agencyhub.CommandActionFundingReverse:
		var payload agencyFundingReverseCommand
		if err := common.Unmarshal([]byte(command.Payload), &payload); err != nil {
			return fmt.Errorf("invalid funding.reverse payload: %w", err)
		}
		if payload.Quota != 0 && payload.RefundQuota != 0 && payload.Quota != payload.RefundQuota {
			return errors.New("funding.reverse quota aliases disagree")
		}
		quota := payload.RefundQuota
		if quota == 0 {
			quota = payload.Quota
		}
		if payload.UserID <= 0 || quota <= 0 {
			return errors.New("funding.reverse requires positive user_id and refund_quota")
		}
		if strings.TrimSpace(payload.RefundID) == "" {
			return errors.New("funding.reverse requires refund_id")
		}
		if strings.TrimSpace(payload.OriginalOperationID) == "" && strings.TrimSpace(payload.OriginalEventID) == "" {
			return errors.New("funding.reverse requires original funding reference")
		}
		if err := reverseAgencyTopupTx(tx, payload, quota); err != nil {
			return err
		}
		return setAgencyCommandResultTx(tx, command.CommandID, agencyCommandSucceeded, 200, map[string]any{
			"user_id": payload.UserID, "requested_quota": quota, "refunded_quota": quota,
		}, "")
	default:
		return fmt.Errorf("unsupported agency command action: %s", command.Action)
	}
}

func reverseAgencyTopup(payload agencyFundingReverseCommand, quota int64) error {
	apply := func() error {
		return model.DB.Transaction(func(tx *gorm.DB) error {
			return reverseAgencyTopupTx(tx, payload, quota)
		})
	}
	err := apply()
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		// Re-read a concurrent committed refund, including its immutable input.
		err = apply()
	}
	return err
}

func reverseAgencyTopupTx(tx *gorm.DB, payload agencyFundingReverseCommand, quota int64) error {
	sourceID := strings.TrimSpace(payload.OriginalOperationID)
	eventID := strings.TrimSpace(payload.OriginalEventID)
	if eventID != "" {
		if !strings.HasPrefix(eventID, "agency-topup-") {
			return model.ErrAgencyFundingReversalConflict
		}
		eventSource := strings.TrimPrefix(eventID, "agency-topup-")
		if sourceID != "" && sourceID != eventSource {
			return model.ErrAgencyFundingReversalConflict
		}
		sourceID = eventSource
	}
	if sourceID == "" || strings.TrimSpace(payload.RefundID) == "" {
		return errors.New("funding.reverse requires source id and refund_id")
	}
	charges, created, err := model.ReverseAgencyTopupTx(tx, model.AgencyFundingReversalInput{
		RefundID: payload.RefundID, SourceOperationID: sourceID, UserID: payload.UserID,
		Quota: quota, CurrencyCode: payload.CurrencyCode, PaymentReference: payload.PaymentReference,
		EvidenceRef: payload.EvidenceRef, Reason: payload.Reason,
	})
	if err != nil {
		return err
	}
	// A committed refund is immutable and idempotent. The model returns
	// the original affected charges for a replay so callers can inspect
	// them, but replaying those charges into the commission journal would
	// emit a second financial reversal. Only the transaction that created
	// the funding reversal may append its commission companions.
	if !created {
		return nil
	}
	// Funding and commission reversal are one financial operation.
	// Keep the sidecar projection out of this transaction only; the
	// gateway's commission journal/outbox must commit or roll back
	// together with users.quota and funding lots.
	for _, charge := range charges {
		if charge.Quota <= 0 || strings.TrimSpace(charge.ChargeID) == "" {
			continue
		}
		if err := RecordAgencyRefundByReferenceTx(tx, "", charge.ChargeID, charge.Quota, "payment_chargeback"); err != nil {
			return err
		}
	}
	return nil
}

func verifyCurrentAgencyCommandTx(tx *gorm.DB, command model.AgencyCommand) error {
	servicePublicKey, err := loadAgencyHubCommandServicePublicKey()
	if err != nil {
		return err
	}
	if err := agencyhub.VerifyCommandServiceSignature(servicePublicKey, agencyhub.AgencyCommandRequest{
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
	}, command.HubSignature); err != nil {
		return err
	}
	publicKey, err := loadAgencyCommandPublicKey()
	if err != nil {
		return err
	}
	claims, err := agencyhub.VerifySSOTicket(publicKey, command.RootProof, "new-api", "agency-gateway-command")
	if err != nil {
		return err
	}
	bodyHash, err := agencyhub.CommandBodyHash([]byte(command.Payload))
	if err != nil {
		return err
	}
	actorID, err := agencyCommandActorID(command.Actor)
	if err != nil {
		return err
	}
	if claims.Subject != actorID || claims.SourceSID != command.SourceSID ||
		claims.Action != command.Action || claims.CommandID != command.CommandID ||
		claims.ObjectID != command.ObjectID || claims.ExpectedVersion != command.ExpectedVersion ||
		claims.JTI != command.RootProofJTI ||
		!strings.EqualFold(claims.BodyHash, bodyHash) ||
		claims.IssuedAt != command.IssuedAt || claims.ExpiresAt != command.ExpiresAt ||
		claims.ExpiresAt <= time.Now().Unix() {
		return errors.New("agency command proof no longer matches current envelope")
	}
	var user model.User
	if err := model.AgencyLockForUpdate(tx).Select("id, role, status, auth_version").First(&user, actorID).Error; err != nil {
		return err
	}
	if user.Role != common.RoleRootUser || user.Status != common.UserStatusEnabled {
		return errors.New("root actor is disabled or no longer privileged")
	}
	var session model.UserSession
	if err := model.AgencyLockForUpdate(tx).Where("sid = ?", command.SourceSID).First(&session).Error; err != nil {
		return err
	}
	now := time.Now().Unix()
	if session.UserID != int(actorID) || session.Status != model.UserSessionStatusActive ||
		session.RevokedAt != 0 || session.ExpiresAt <= now ||
		session.Version != claims.SessionVersion || session.UserAuthVersion != claims.UserAuthVersion ||
		user.AuthVersion != claims.UserAuthVersion {
		return errors.New("source Root session is no longer valid")
	}
	return nil
}

func loadAgencyCommandPublicKey() (ed25519.PublicKey, error) {
	path := strings.TrimSpace(os.Getenv("AGENCY_SSO_PUBLIC_KEY_FILE"))
	if path != "" {
		return agencyhub.LoadSSOPublicKey(path)
	}
	// Development deployments commonly mount only the signing key. Derive its
	// public half locally; production should still mount the public key and
	// rotate it independently.
	privatePath := strings.TrimSpace(os.Getenv("AGENCY_SSO_PRIVATE_KEY_FILE"))
	if privatePath == "" {
		return nil, errors.New("AGENCY_SSO_PUBLIC_KEY_FILE is required")
	}
	data, err := os.ReadFile(privatePath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid agency SSO private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("agency SSO private key is not Ed25519")
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("failed to derive agency SSO public key")
	}
	return publicKey, nil
}

func loadAgencyHubCommandServicePublicKey() (ed25519.PublicKey, error) {
	path := strings.TrimSpace(os.Getenv("AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE"))
	if path == "" {
		return nil, errors.New("AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE is required")
	}
	return agencyhub.LoadSSOPublicKey(path)
}

func agencyCommandActorID(actor string) (int64, error) {
	actor = strings.TrimSpace(strings.TrimPrefix(actor, "root:"))
	id, err := strconv.ParseInt(actor, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid Root actor")
	}
	return id, nil
}

func setAgencyCommandResult(commandID, status string, code int, result any, lastError string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return setAgencyCommandResultTx(tx, commandID, status, code, result, lastError)
	})
}

func setAgencyCommandResultTx(tx *gorm.DB, commandID, status string, code int, result any, lastError string) error {
	now := time.Now().Unix()
	resultJSON := ""
	if result != nil {
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		resultJSON = string(encoded)
	}
	var command model.AgencyCommand
	if err := model.AgencyLockForUpdate(tx).Where("command_id = ?", commandID).First(&command).Error; err != nil {
		return err
	}
	if command.Status == agencyCommandSucceeded || command.Status == agencyCommandFailed || command.Status == agencyCommandCancelled {
		return nil
	}
	updates := map[string]any{
		"status": status, "result_code": code, "result_json": resultJSON,
		"last_error": strings.TrimSpace(lastError), "updated_at": now,
	}
	if status == agencyCommandSucceeded || status == agencyCommandFailed || status == agencyCommandCancelled {
		updates["completed_at"] = now
	}
	updated := tx.Model(&model.AgencyCommand{}).
		Where("id = ? AND status = ?", command.ID, agencyCommandProcessing).
		Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
