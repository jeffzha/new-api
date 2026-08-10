package approval

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	credentialpkg "github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MinimumTTL = 5 * time.Minute
	MaximumTTL = 24 * time.Hour
)

type Service struct {
	db       *gorm.DB
	resolver secrets.Resolver
}

type RequestCommand struct {
	ActionType  string
	CustomerID  *uint64
	ResourceID  uint64
	EvidenceRef string
	RequestKey  string
	Reason      string
	Actor       string
	RequestID   string
	TTL         time.Duration
}

type DecisionCommand struct {
	ApprovalID      string
	ExpectedVersion int64
	Actor           string
	Reason          string
	RequestID       string
}

type ListQuery struct {
	CustomerID *uint64
	Status     string
	BeforeID   uint64
	Limit      int
}

type actionPayload struct {
	CustomerID                        uint64 `json:"customer_id,omitempty"`
	ExpectedCustomerVersion           int64  `json:"expected_customer_version,omitempty"`
	SourceAppID                       uint64 `json:"source_app_id,omitempty"`
	TargetAppID                       uint64 `json:"target_app_id,omitempty"`
	ExpectedSourceVersion             int64  `json:"expected_source_version,omitempty"`
	ExpectedTargetVersion             int64  `json:"expected_target_version,omitempty"`
	CurrentCredentialID               uint64 `json:"current_credential_id,omitempty"`
	CandidateCredentialID             uint64 `json:"candidate_credential_id,omitempty"`
	ExpectedCurrentVersion            int64  `json:"expected_current_version,omitempty"`
	ExpectedCandidateVersion          int64  `json:"expected_candidate_version,omitempty"`
	CurrentConfigID                   uint64 `json:"current_config_id,omitempty"`
	CurrentConfigVersion              int64  `json:"current_config_version,omitempty"`
	ExpectedCurrentConfigRowVersion   int64  `json:"expected_current_config_row_version,omitempty"`
	PendingConfigID                   uint64 `json:"pending_config_id,omitempty"`
	PendingConfigVersion              int64  `json:"pending_config_version,omitempty"`
	ExpectedPendingConfigRowVersion   int64  `json:"expected_pending_config_row_version,omitempty"`
	AuthorizedPendingConfigRowVersion int64  `json:"authorized_pending_config_row_version,omitempty"`
	SourceCredentialID                uint64 `json:"source_credential_id,omitempty"`
	SourceCredentialRowVersion        int64  `json:"source_credential_row_version,omitempty"`
	TargetCredentialID                uint64 `json:"target_credential_id,omitempty"`
	TargetCredentialRowVersion        int64  `json:"target_credential_row_version,omitempty"`
	EvidenceRef                       string `json:"evidence_ref,omitempty"`
	MigrationJobID                    uint64 `json:"migration_job_id,omitempty"`
	MigrationConfigFingerprint        string `json:"migration_config_fingerprint,omitempty"`
	MigrationMemberSetFingerprint     string `json:"migration_member_set_fingerprint,omitempty"`
	Reason                            string `json:"reason"`
}

// ValidateAppCredentialChange rechecks the complete two-person authorization
// tuple immediately before provider verification and again before cutover.
// A first App configuration, or a pending config that keeps the current
// credential profile, does not require this approval.
func ValidateAppCredentialChange(tx *gorm.DB, resolver secrets.Resolver, application model.CustomerApp, current, pending model.AppConfigVersion) error {
	if current.ID == 0 {
		return nil
	}
	if current.CredentialProfileID == nil || pending.CredentialProfileID == nil {
		return domain.Conflict("App config credential profile is unavailable")
	}
	if *current.CredentialProfileID == *pending.CredentialProfileID {
		return nil
	}
	if pending.CredentialChangeApprovalID == nil {
		return domain.Forbidden("changing credential_profile_id requires an executed two-person approval")
	}
	var governance model.GovernanceApproval
	if err := tx.First(&governance, *pending.CredentialChangeApprovalID).Error; err != nil {
		return domain.Forbidden("credential-change approval is unavailable")
	}
	if governance.ActionType != model.ApprovalActionAppCredentialChange || governance.Status != model.ApprovalStatusExecuted ||
		governance.CustomerID == nil || *governance.CustomerID != application.CustomerID || governance.RequestedBy == governance.ApprovedBy {
		return domain.Forbidden("credential-change approval is invalid")
	}
	var payload actionPayload
	if err := jsonx.Unmarshal([]byte(governance.PayloadJSON), &payload); err != nil || support.Hash(payload) != governance.PayloadHash {
		return domain.Forbidden("credential-change approval payload integrity check failed")
	}
	var source, target model.CredentialProfile
	if err := tx.First(&source, *current.CredentialProfileID).Error; err != nil {
		return domain.Conflict("source credential profile is unavailable")
	}
	if err := tx.First(&target, *pending.CredentialProfileID).Error; err != nil {
		return domain.Conflict("target credential profile is unavailable")
	}
	var customer model.Customer
	if err := tx.First(&customer, application.CustomerID).Error; err != nil || customer.Status != model.CustomerStatusActive {
		return domain.Conflict("credential-change customer is unavailable")
	}
	if payload.CustomerID != application.CustomerID || payload.ExpectedCustomerVersion != customer.RowVersion ||
		payload.SourceAppID != application.ID || payload.ExpectedSourceVersion != application.RowVersion ||
		payload.CurrentConfigID != current.ID || payload.CurrentConfigVersion != current.ConfigVersion || payload.ExpectedCurrentConfigRowVersion != current.RowVersion ||
		payload.PendingConfigID != pending.ID || payload.PendingConfigVersion != pending.ConfigVersion || payload.AuthorizedPendingConfigRowVersion != pending.RowVersion ||
		payload.SourceCredentialID != source.ID || payload.SourceCredentialRowVersion != source.RowVersion ||
		payload.TargetCredentialID != target.ID || payload.TargetCredentialRowVersion != target.RowVersion ||
		application.CurrentConfigVersionID == nil || application.PendingConfigVersionID == nil ||
		*application.CurrentConfigVersionID != current.ID || *application.PendingConfigVersionID != pending.ID ||
		current.CustomerAppID != application.ID || pending.CustomerAppID != application.ID ||
		!credentialpkg.RuntimeEligible(source.Status) || target.Status != model.CredentialStatusActive ||
		source.ProviderEnvironment != application.ProviderEnvironment || target.ProviderEnvironment != application.ProviderEnvironment ||
		!credentialpkg.AvailableToCustomer(source, application.CustomerID) || !credentialpkg.AvailableToCustomer(target, application.CustomerID) {
		return domain.Conflict("authorized App credential-change tuple is stale")
	}
	if _, err := secrets.ResolveAppKey(resolver, pending.AppKeySecretRef, pending.AppKeyFingerprint, pending.AppKeyFingerprintVersion); err != nil {
		return domain.Unavailable("pending provider AppKey integrity verification failed")
	}
	if _, err := secrets.ResolveCredentialPair(resolver, source.SecretIDRef, source.SecretKeyRef, source.Fingerprint, source.FingerprintVersion); err != nil {
		return domain.Unavailable("source provider credential integrity verification failed")
	}
	if _, err := secrets.ResolveCredentialPair(resolver, target.SecretIDRef, target.SecretKeyRef, target.Fingerprint, target.FingerprintVersion); err != nil {
		return domain.Unavailable("target provider credential integrity verification failed")
	}
	return nil
}

func New(db *gorm.DB, resolver ...secrets.Resolver) *Service {
	var configured secrets.Resolver
	if len(resolver) > 0 {
		configured = resolver[0]
	}
	return &Service{db: db, resolver: configured}
}

func (s *Service) Request(command RequestCommand, now time.Time) (*model.GovernanceApproval, error) {
	command.ActionType = strings.ToLower(strings.TrimSpace(command.ActionType))
	command.RequestKey = strings.TrimSpace(command.RequestKey)
	command.Reason = strings.TrimSpace(command.Reason)
	command.EvidenceRef = strings.TrimSpace(command.EvidenceRef)
	command.Actor = strings.TrimSpace(command.Actor)
	if command.RequestKey == "" || len(command.RequestKey) > 128 || command.Reason == "" || len(command.Reason) > 1000 || command.Actor == "" {
		return nil, domain.Invalid("request_key, reason, and actor are required and must fit supported lengths")
	}
	if command.TTL < MinimumTTL || command.TTL > MaximumTTL {
		return nil, domain.Invalid("approval TTL must be between 5 minutes and 24 hours")
	}
	now = now.UTC()
	var result model.GovernanceApproval
	err := s.db.Transaction(func(tx *gorm.DB) error {
		payload, customerID, err := resolveRequestPayload(tx, command)
		if err != nil {
			return err
		}
		payloadJSON, err := jsonx.Marshal(payload)
		if err != nil {
			return err
		}
		payloadHash := support.Hash(payload)
		candidate := model.GovernanceApproval{
			PublicID: support.PublicID("apr"), RequestKey: command.RequestKey,
			CustomerID: customerID, ActionType: command.ActionType,
			PayloadJSON: string(payloadJSON), PayloadHash: payloadHash,
			Status: model.ApprovalStatusPending, RequestedBy: command.Actor,
			Reason: command.Reason, ExpiresAt: now.Add(command.TTL), RowVersion: 1,
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			if err := tx.Where("request_key = ?", command.RequestKey).First(&result).Error; err != nil {
				return err
			}
			if result.ActionType != command.ActionType || result.PayloadHash != payloadHash || result.RequestedBy != command.Actor {
				return domain.Conflict("approval request key was already used with different action data")
			}
			return nil
		}
		result = candidate
		return support.Audit(tx, customerID, command.Actor, "approval.request", "governance_approval", result.PublicID, nil, &result, command.Reason, command.RequestID)
	})
	return &result, err
}

func resolveRequestPayload(tx *gorm.DB, command RequestCommand) (actionPayload, *uint64, error) {
	payload := actionPayload{Reason: command.Reason, EvidenceRef: command.EvidenceRef}
	switch command.ActionType {
	case model.ApprovalActionAppDisable:
		if command.CustomerID == nil || *command.CustomerID == 0 {
			return payload, nil, domain.Invalid("customer_id is required for App disable approval")
		}
		var app model.CustomerApp
		if err := tx.Where("customer_id = ? AND slot = ?", *command.CustomerID, "primary").First(&app).Error; err != nil {
			return payload, nil, domain.NotFound("customer App not found")
		}
		if app.Status == model.AppStatusArchived || app.Status == model.AppStatusDisabled {
			return payload, nil, domain.Conflict("customer App is already disabled or archived")
		}
		payload.CustomerID = *command.CustomerID
		payload.SourceAppID = app.ID
		payload.ExpectedSourceVersion = app.RowVersion
		return payload, command.CustomerID, nil
	case model.ApprovalActionCredentialRotate:
		var candidate model.CredentialProfile
		if err := tx.First(&candidate, command.ResourceID).Error; err != nil {
			return payload, nil, domain.NotFound("staged credential profile not found")
		}
		if candidate.Status != model.CredentialStatusStaged || candidate.PreviousProfileID == nil {
			return payload, nil, domain.Conflict("credential profile is not a staged rotation")
		}
		var current model.CredentialProfile
		if err := tx.First(&current, *candidate.PreviousProfileID).Error; err != nil || current.Status != model.CredentialStatusActive {
			return payload, nil, domain.Conflict("current credential profile is unavailable or inactive")
		}
		if !credentialpkg.SameOwner(current, candidate) || !optionalIDMatches(command.CustomerID, candidate.CustomerID) {
			return payload, nil, domain.Conflict("credential rotation owner scope is invalid or mismatched")
		}
		payload.CurrentCredentialID = current.ID
		payload.CandidateCredentialID = candidate.ID
		payload.ExpectedCurrentVersion = current.RowVersion
		payload.ExpectedCandidateVersion = candidate.RowVersion
		return payload, candidate.CustomerID, nil
	case model.ApprovalActionCredentialRollback:
		var candidate model.CredentialProfile
		if err := tx.First(&candidate, command.ResourceID).Error; err != nil {
			return payload, nil, domain.NotFound("active rotated credential profile not found")
		}
		if candidate.Status != model.CredentialStatusActive || candidate.PreviousProfileID == nil {
			return payload, nil, domain.Conflict("credential profile is not an active rotation")
		}
		var previous model.CredentialProfile
		if err := tx.First(&previous, *candidate.PreviousProfileID).Error; err != nil || previous.Status != model.CredentialStatusRetiring {
			return payload, nil, domain.Conflict("previous credential profile is unavailable for rollback")
		}
		if !credentialpkg.SameOwner(previous, candidate) || !optionalIDMatches(command.CustomerID, candidate.CustomerID) {
			return payload, nil, domain.Conflict("credential rollback owner scope is invalid or mismatched")
		}
		payload.CurrentCredentialID = previous.ID
		payload.CandidateCredentialID = candidate.ID
		payload.ExpectedCurrentVersion = previous.RowVersion
		payload.ExpectedCandidateVersion = candidate.RowVersion
		return payload, candidate.CustomerID, nil
	case model.ApprovalActionAppIDMigration:
		if command.CustomerID == nil || *command.CustomerID == 0 || command.ResourceID == 0 || command.EvidenceRef == "" {
			return payload, nil, domain.Invalid("customer_id, target migration App, and evidence_ref are required")
		}
		var source model.CustomerApp
		if err := tx.Where("customer_id = ? AND slot = ?", *command.CustomerID, "primary").First(&source).Error; err != nil {
			return payload, nil, domain.NotFound("source customer App not found")
		}
		var target model.CustomerApp
		if err := tx.First(&target, command.ResourceID).Error; err != nil {
			return payload, nil, domain.NotFound("target migration App not found")
		}
		if target.CustomerID != *command.CustomerID || !strings.HasPrefix(target.Slot, "migration:") || target.Status != model.AppStatusVerified || target.CurrentConfigVersionID == nil {
			return payload, nil, domain.Conflict("target App is not a verified migration candidate")
		}
		job, err := appmigration.AssertReadyForCutover(tx, source, target)
		if err != nil {
			return payload, nil, err
		}
		var evidence model.EvidenceObject
		if err := tx.Where("public_id = ? AND status = ?", command.EvidenceRef, model.EvidenceStatusActive).First(&evidence).Error; err != nil || evidence.CustomerID == nil || *evidence.CustomerID != *command.CustomerID {
			return payload, nil, domain.Conflict("migration evidence is missing or belongs to another customer")
		}
		payload.CustomerID = *command.CustomerID
		payload.SourceAppID = source.ID
		payload.TargetAppID = target.ID
		payload.ExpectedSourceVersion = source.RowVersion
		payload.ExpectedTargetVersion = target.RowVersion
		payload.MigrationJobID = job.ID
		payload.MigrationConfigFingerprint = job.TargetConfigFingerprint
		payload.MigrationMemberSetFingerprint = job.MemberSetFingerprint
		return payload, command.CustomerID, nil
	case model.ApprovalActionAppCredentialChange:
		if command.CustomerID == nil || *command.CustomerID == 0 || command.ResourceID == 0 {
			return payload, nil, domain.Invalid("customer_id and pending App config are required")
		}
		var pending model.AppConfigVersion
		if err := tx.First(&pending, command.ResourceID).Error; err != nil {
			return payload, nil, domain.NotFound("pending App config not found")
		}
		var application model.CustomerApp
		if err := tx.Where("id = ? AND customer_id = ?", pending.CustomerAppID, *command.CustomerID).First(&application).Error; err != nil {
			return payload, nil, domain.NotFound("customer App not found")
		}
		if application.CurrentConfigVersionID == nil || application.PendingConfigVersionID == nil ||
			*application.PendingConfigVersionID != pending.ID || pending.Status != model.AppConfigStatusDraft {
			return payload, nil, domain.Conflict("App credential change requires a current config and the exact pending draft")
		}
		var customer model.Customer
		if err := tx.First(&customer, application.CustomerID).Error; err != nil || customer.Status != model.CustomerStatusActive {
			return payload, nil, domain.Conflict("credential-change customer is unavailable")
		}
		var current model.AppConfigVersion
		if err := tx.First(&current, *application.CurrentConfigVersionID).Error; err != nil {
			return payload, nil, domain.Conflict("current App config is unavailable")
		}
		if current.CredentialProfileID == nil || pending.CredentialProfileID == nil || *current.CredentialProfileID == *pending.CredentialProfileID {
			return payload, nil, domain.Conflict("pending App config does not change the credential profile")
		}
		var source, target model.CredentialProfile
		if err := tx.First(&source, *current.CredentialProfileID).Error; err != nil {
			return payload, nil, domain.Conflict("source credential profile is unavailable")
		}
		if err := tx.First(&target, *pending.CredentialProfileID).Error; err != nil {
			return payload, nil, domain.Conflict("target credential profile is unavailable")
		}
		if !credentialpkg.RuntimeEligible(source.Status) || target.Status != model.CredentialStatusActive ||
			source.ProviderEnvironment != application.ProviderEnvironment || target.ProviderEnvironment != application.ProviderEnvironment ||
			!credentialpkg.AvailableToCustomer(source, application.CustomerID) || !credentialpkg.AvailableToCustomer(target, application.CustomerID) {
			return payload, nil, domain.Conflict("credential change profiles are inactive, provider-mismatched, or unavailable to this customer")
		}
		payload.CustomerID = application.CustomerID
		payload.ExpectedCustomerVersion = customer.RowVersion
		payload.SourceAppID = application.ID
		payload.ExpectedSourceVersion = application.RowVersion
		payload.CurrentConfigID = current.ID
		payload.CurrentConfigVersion = current.ConfigVersion
		payload.ExpectedCurrentConfigRowVersion = current.RowVersion
		payload.PendingConfigID = pending.ID
		payload.PendingConfigVersion = pending.ConfigVersion
		payload.ExpectedPendingConfigRowVersion = pending.RowVersion
		payload.AuthorizedPendingConfigRowVersion = pending.RowVersion + 1
		payload.SourceCredentialID = source.ID
		payload.SourceCredentialRowVersion = source.RowVersion
		payload.TargetCredentialID = target.ID
		payload.TargetCredentialRowVersion = target.RowVersion
		return payload, command.CustomerID, nil
	default:
		return payload, nil, domain.Invalid("unsupported governance approval action")
	}
}

func (s *Service) Approve(command DecisionCommand, now time.Time) (*model.GovernanceApproval, error) {
	return s.decide(command, now, model.ApprovalStatusApproved)
}

func (s *Service) Reject(command DecisionCommand, now time.Time) (*model.GovernanceApproval, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.Reason == "" {
		return nil, domain.Invalid("decision reason is required")
	}
	return s.decide(command, now, model.ApprovalStatusRejected)
}

func (s *Service) decide(command DecisionCommand, now time.Time, decision string) (*model.GovernanceApproval, error) {
	command.ApprovalID = strings.TrimSpace(command.ApprovalID)
	command.Actor = strings.TrimSpace(command.Actor)
	if command.ApprovalID == "" || command.ExpectedVersion <= 0 || command.Actor == "" {
		return nil, domain.Invalid("approval_id, expected_version, and actor are required")
	}
	now = now.UTC()
	var result model.GovernanceApproval
	expired := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("public_id = ?", command.ApprovalID).First(&result).Error; err != nil {
			return domain.NotFound("governance approval not found")
		}
		if result.Status == decision {
			return nil
		}
		if result.RowVersion != command.ExpectedVersion {
			return domain.Conflict("approval row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
		}
		if result.Status != model.ApprovalStatusPending {
			return domain.Conflict("only a pending approval can be decided")
		}
		before := result
		if !now.Before(result.ExpiresAt) {
			result.Status = model.ApprovalStatusExpired
			result.RowVersion++
			expired = true
		} else if decision == model.ApprovalStatusApproved {
			if command.Actor == result.RequestedBy {
				return domain.Forbidden("approval requester and approver must be different administrators")
			}
			result.Status = decision
			result.ApprovedBy = command.Actor
			result.ApprovedAt = &now
			result.DecisionReason = strings.TrimSpace(command.Reason)
			result.RowVersion++
		} else {
			result.Status = decision
			result.RejectedBy = command.Actor
			result.RejectedAt = &now
			result.DecisionReason = command.Reason
			result.RowVersion++
		}
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		action := "approval." + decision
		if expired {
			action = "approval.expire"
		}
		return support.Audit(tx, result.CustomerID, command.Actor, action, "governance_approval", result.PublicID, &before, &result, result.DecisionReason, command.RequestID)
	})
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, domain.Conflict("governance approval expired")
	}
	return &result, nil
}

func (s *Service) Execute(command DecisionCommand, now time.Time) (*model.GovernanceApproval, error) {
	command.ApprovalID = strings.TrimSpace(command.ApprovalID)
	command.Actor = strings.TrimSpace(command.Actor)
	if command.ApprovalID == "" || command.ExpectedVersion <= 0 || command.Actor == "" {
		return nil, domain.Invalid("approval_id, expected_version, and actor are required")
	}
	now = now.UTC()
	var result model.GovernanceApproval
	expired := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("public_id = ?", command.ApprovalID).First(&result).Error; err != nil {
			return domain.NotFound("governance approval not found")
		}
		if result.Status == model.ApprovalStatusExecuted {
			return nil
		}
		if result.RowVersion != command.ExpectedVersion {
			return domain.Conflict("approval row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
		}
		if result.Status != model.ApprovalStatusApproved || result.ApprovedBy == "" || result.ApprovedBy == result.RequestedBy {
			return domain.Conflict("governance approval does not have a valid second-person approval")
		}
		if !now.Before(result.ExpiresAt) {
			before := result
			result.Status = model.ApprovalStatusExpired
			result.RowVersion++
			expired = true
			if err := tx.Save(&result).Error; err != nil {
				return err
			}
			return support.Audit(tx, result.CustomerID, command.Actor, "approval.expire", "governance_approval", result.PublicID, &before, &result, "expired before execution", command.RequestID)
		}
		var payload actionPayload
		if err := jsonx.Unmarshal([]byte(result.PayloadJSON), &payload); err != nil || support.Hash(payload) != result.PayloadHash {
			return fmt.Errorf("governance approval payload integrity check failed")
		}
		if err := executeAction(tx, s.resolver, result, payload, now, command.Actor, command.RequestID); err != nil {
			return err
		}
		before := result
		result.Status = model.ApprovalStatusExecuted
		result.ExecutedBy = command.Actor
		result.ExecutedAt = &now
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		return support.Audit(tx, result.CustomerID, command.Actor, "approval.execute", "governance_approval", result.PublicID, &before, &result, result.Reason, command.RequestID)
	})
	if err == nil && expired {
		return nil, domain.Conflict("governance approval expired before execution")
	}
	return &result, err
}

func executeAction(tx *gorm.DB, resolver secrets.Resolver, approval model.GovernanceApproval, payload actionPayload, now time.Time, actor, requestID string) error {
	switch approval.ActionType {
	case model.ApprovalActionAppDisable:
		var app model.CustomerApp
		if err := database.ForUpdate(tx).First(&app, payload.SourceAppID).Error; err != nil || app.CustomerID != payload.CustomerID || app.Slot != "primary" {
			return domain.Conflict("approved customer App is no longer the primary App")
		}
		if app.Status == model.AppStatusDisabled {
			return nil
		}
		if app.RowVersion != payload.ExpectedSourceVersion || app.Status == model.AppStatusArchived {
			return domain.Conflict("approved App disable target changed after request")
		}
		before := app
		app.Status = model.AppStatusDisabled
		app.DisabledAt = &now
		app.AuthEpoch++
		app.RowVersion++
		if err := tx.Save(&app).Error; err != nil {
			return err
		}
		if err := support.Enqueue(tx, &app.CustomerID, "CACHE_INVALIDATE", fmt.Sprintf("app:%d:epoch:%d", app.ID, app.AuthEpoch), map[string]any{
			"customer_id": app.CustomerID, "customer_app_id": app.ID, "application_id": app.AppID,
			"status": app.Status, "auth_epoch": app.AuthEpoch,
		}); err != nil {
			return err
		}
		return support.Audit(tx, &app.CustomerID, actor, "app.disable.approved", "customer_app", support.ResourceID(app.ID), &before, &app, approval.PublicID+": "+payload.Reason, requestID)
	case model.ApprovalActionCredentialRotate, model.ApprovalActionCredentialRollback:
		var current, candidate model.CredentialProfile
		if err := database.ForUpdate(tx).First(&current, payload.CurrentCredentialID).Error; err != nil {
			return domain.NotFound("current credential profile not found")
		}
		if err := database.ForUpdate(tx).First(&candidate, payload.CandidateCredentialID).Error; err != nil {
			return domain.NotFound("candidate credential profile not found")
		}
		if !credentialpkg.SameOwner(current, candidate) || !sameOptionalID(approval.CustomerID, candidate.CustomerID) {
			return domain.Conflict("credential approval owner scope changed or is invalid")
		}
		if _, err := secrets.ResolveCredentialPair(resolver, current.SecretIDRef, current.SecretKeyRef, current.Fingerprint, current.FingerprintVersion); err != nil {
			return domain.Unavailable("current provider credential integrity verification failed")
		}
		if _, err := secrets.ResolveCredentialPair(resolver, candidate.SecretIDRef, candidate.SecretKeyRef, candidate.Fingerprint, candidate.FingerprintVersion); err != nil {
			return domain.Unavailable("candidate provider credential integrity verification failed")
		}
		if approval.ActionType == model.ApprovalActionCredentialRotate {
			if candidate.Status == model.CredentialStatusActive && current.Status == model.CredentialStatusRetiring {
				return nil
			}
			if current.RowVersion != payload.ExpectedCurrentVersion || candidate.RowVersion != payload.ExpectedCandidateVersion || current.Status != model.CredentialStatusActive || candidate.Status != model.CredentialStatusStaged {
				return domain.Conflict("credential rotation target changed after request")
			}
			current.Status = model.CredentialStatusRetiring
			current.RowVersion++
			candidate.Status = model.CredentialStatusActive
			candidate.ActivatedAt = &now
			candidate.RowVersion++
		} else {
			if candidate.Status == model.CredentialStatusRetiring && current.Status == model.CredentialStatusActive {
				return nil
			}
			if current.RowVersion != payload.ExpectedCurrentVersion || candidate.RowVersion != payload.ExpectedCandidateVersion || current.Status != model.CredentialStatusRetiring || candidate.Status != model.CredentialStatusActive {
				return domain.Conflict("credential rollback target changed after request")
			}
			current.Status = model.CredentialStatusActive
			current.ActivatedAt = &now
			current.RowVersion++
			candidate.Status = model.CredentialStatusRetiring
			candidate.RowVersion++
		}
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		if err := tx.Save(&candidate).Error; err != nil {
			return err
		}
		return support.Audit(tx, candidate.CustomerID, actor, "credential."+approval.ActionType+".approved", "credential_profile", support.ResourceID(candidate.ID), nil, map[string]any{
			"approval_id": approval.PublicID, "current_profile_id": current.ID, "current_status": current.Status,
			"candidate_profile_id": candidate.ID, "candidate_status": candidate.Status,
			"owner_scope": candidate.OwnerScope, "customer_id": candidate.CustomerID,
		}, payload.Reason, requestID)
	case model.ApprovalActionAppCredentialChange:
		var customer model.Customer
		if err := database.ForUpdate(tx).First(&customer, payload.CustomerID).Error; err != nil {
			return domain.Conflict("approved credential-change customer is unavailable")
		}
		var application model.CustomerApp
		if err := database.ForUpdate(tx).First(&application, payload.SourceAppID).Error; err != nil {
			return domain.NotFound("approved customer App not found")
		}
		var current, pending model.AppConfigVersion
		if err := database.ForUpdate(tx).First(&current, payload.CurrentConfigID).Error; err != nil {
			return domain.Conflict("approved current App config is unavailable")
		}
		if err := database.ForUpdate(tx).First(&pending, payload.PendingConfigID).Error; err != nil {
			return domain.Conflict("approved pending App config is unavailable")
		}
		var source, target model.CredentialProfile
		if err := database.ForUpdate(tx).First(&source, payload.SourceCredentialID).Error; err != nil {
			return domain.Conflict("approved source credential is unavailable")
		}
		if err := database.ForUpdate(tx).First(&target, payload.TargetCredentialID).Error; err != nil {
			return domain.Conflict("approved target credential is unavailable")
		}
		if approval.CustomerID == nil || *approval.CustomerID != payload.CustomerID ||
			customer.RowVersion != payload.ExpectedCustomerVersion || customer.Status != model.CustomerStatusActive ||
			application.CustomerID != payload.CustomerID ||
			application.RowVersion != payload.ExpectedSourceVersion || application.CurrentConfigVersionID == nil || application.PendingConfigVersionID == nil ||
			*application.CurrentConfigVersionID != current.ID || *application.PendingConfigVersionID != pending.ID ||
			current.CustomerAppID != application.ID || pending.CustomerAppID != application.ID ||
			current.ConfigVersion != payload.CurrentConfigVersion || pending.ConfigVersion != payload.PendingConfigVersion ||
			current.RowVersion != payload.ExpectedCurrentConfigRowVersion || pending.RowVersion != payload.ExpectedPendingConfigRowVersion ||
			current.CredentialProfileID == nil || pending.CredentialProfileID == nil ||
			*current.CredentialProfileID != source.ID || *pending.CredentialProfileID != target.ID || source.ID == target.ID ||
			source.RowVersion != payload.SourceCredentialRowVersion || target.RowVersion != payload.TargetCredentialRowVersion ||
			pending.Status != model.AppConfigStatusDraft || !credentialpkg.RuntimeEligible(source.Status) || target.Status != model.CredentialStatusActive ||
			source.ProviderEnvironment != application.ProviderEnvironment || target.ProviderEnvironment != application.ProviderEnvironment ||
			!credentialpkg.AvailableToCustomer(source, application.CustomerID) || !credentialpkg.AvailableToCustomer(target, application.CustomerID) {
			return domain.Conflict("approved App credential-change tuple changed after request")
		}
		if _, err := secrets.ResolveAppKey(resolver, pending.AppKeySecretRef, pending.AppKeyFingerprint, pending.AppKeyFingerprintVersion); err != nil {
			return domain.Unavailable("pending provider AppKey integrity verification failed")
		}
		if _, err := secrets.ResolveCredentialPair(resolver, source.SecretIDRef, source.SecretKeyRef, source.Fingerprint, source.FingerprintVersion); err != nil {
			return domain.Unavailable("source provider credential integrity verification failed")
		}
		if _, err := secrets.ResolveCredentialPair(resolver, target.SecretIDRef, target.SecretKeyRef, target.Fingerprint, target.FingerprintVersion); err != nil {
			return domain.Unavailable("target provider credential integrity verification failed")
		}
		before := pending
		pending.CredentialChangeApprovalID = &approval.ID
		pending.RowVersion = payload.AuthorizedPendingConfigRowVersion
		updated := tx.Model(&model.AppConfigVersion{}).
			Where("id = ? AND row_version = ?", pending.ID, payload.ExpectedPendingConfigRowVersion).
			Updates(map[string]any{"credential_change_approval_id": approval.ID, "row_version": pending.RowVersion})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return domain.Conflict("pending App config changed during credential-change authorization")
		}
		return support.Audit(tx, &application.CustomerID, actor, "app.credential_change.authorize", "app_config_version", support.ResourceID(pending.ID), &before, &pending, approval.PublicID, requestID)
	case model.ApprovalActionAppIDMigration:
		var source, target model.CustomerApp
		if err := database.ForUpdate(tx).First(&source, payload.SourceAppID).Error; err != nil {
			return domain.NotFound("source customer App not found")
		}
		if err := database.ForUpdate(tx).First(&target, payload.TargetAppID).Error; err != nil {
			return domain.NotFound("target migration App not found")
		}
		if source.Status == model.AppStatusArchived && target.Slot == "primary" {
			return nil
		}
		if source.CustomerID != payload.CustomerID || target.CustomerID != payload.CustomerID || source.Slot != "primary" || !strings.HasPrefix(target.Slot, "migration:") || target.Status != model.AppStatusVerified || target.CurrentConfigVersionID == nil || source.RowVersion != payload.ExpectedSourceVersion || target.RowVersion != payload.ExpectedTargetVersion {
			return domain.Conflict("App migration target changed after request")
		}
		job, err := appmigration.AssertReadyForCutover(tx, source, target)
		if err != nil {
			return err
		}
		if job.ID != payload.MigrationJobID || job.TargetConfigFingerprint != payload.MigrationConfigFingerprint || job.MemberSetFingerprint != payload.MigrationMemberSetFingerprint {
			return domain.Conflict("App migration rebuild approval tuple changed after request")
		}
		if source.CurrentConfigVersionID == nil {
			return domain.Conflict("source App config is unavailable at migration cutover")
		}
		var sourceConfig, targetConfig model.AppConfigVersion
		if err := tx.First(&sourceConfig, *source.CurrentConfigVersionID).Error; err != nil || sourceConfig.CustomerAppID != source.ID {
			return domain.Conflict("source App config is unavailable at migration cutover")
		}
		if err := tx.First(&targetConfig, *target.CurrentConfigVersionID).Error; err != nil || targetConfig.CustomerAppID != target.ID {
			return domain.Conflict("target App config is unavailable at migration cutover")
		}
		var evidence model.EvidenceObject
		if err := tx.Where("public_id = ? AND status = ?", payload.EvidenceRef, model.EvidenceStatusActive).First(&evidence).Error; err != nil || evidence.CustomerID == nil || *evidence.CustomerID != payload.CustomerID {
			return domain.Conflict("migration evidence is unavailable at execution")
		}
		sourceBefore, targetBefore := source, target
		previousStatus := source.Status
		source.Slot = fmt.Sprintf("archived:%d", source.ID)
		source.Status = model.AppStatusArchived
		source.ArchivedAt = &now
		source.AuthEpoch++
		source.RowVersion++
		if err := tx.Save(&source).Error; err != nil {
			return err
		}
		target.Slot = "primary"
		target.Status = previousStatus
		if target.Status == model.AppStatusDraft || target.Status == model.AppStatusArchived {
			target.Status = model.AppStatusVerified
		}
		target.EnabledAt = nil
		target.SuspendedAt = nil
		target.DisabledAt = nil
		target.ArchivedAt = nil
		switch target.Status {
		case model.AppStatusActive:
			target.EnabledAt = &now
		case model.AppStatusSuspended:
			target.SuspendedAt = &now
		case model.AppStatusDisabled:
			target.DisabledAt = &now
		}
		target.AuthEpoch++
		target.RowVersion++
		if err := tx.Save(&target).Error; err != nil {
			return err
		}
		eventKey := fmt.Sprintf("app-migration-cutover:%d:%d:%d", source.ID, target.ID, job.ID)
		lineage := model.AppMigrationLineage{
			PublicID: support.PublicID("lin"), EventKey: eventKey, CustomerID: payload.CustomerID,
			MigrationJobID: job.ID, SourceCustomerAppID: source.ID,
			SourceAppConfigVersionID: sourceConfig.ID, SourceApplicationID: source.AppID,
			SourceProviderAppID: source.AppID,
			SourceConfigVersion: sourceConfig.ConfigVersion, TargetCustomerAppID: target.ID,
			TargetAppConfigVersionID: targetConfig.ID, TargetApplicationID: target.AppID,
			TargetProviderAppID:        target.AppID,
			TargetConfigVersion:        targetConfig.ConfigVersion,
			MigrationConfigFingerprint: job.TargetConfigFingerprint, ActivatedAt: now,
		}
		if err := tx.Create(&lineage).Error; err != nil {
			return err
		}
		if err := support.Enqueue(tx, &payload.CustomerID, "APP_MIGRATION_CUTOVER", eventKey, map[string]any{
			"customer_id": payload.CustomerID, "lineage_id": lineage.PublicID,
			"source_application_id":        lineage.SourceApplicationID,
			"source_provider_app_id":       lineage.SourceProviderAppID,
			"source_app_profile_id":        lineage.SourceCustomerAppID,
			"source_config_version":        lineage.SourceConfigVersion,
			"target_application_id":        lineage.TargetApplicationID,
			"target_provider_app_id":       lineage.TargetProviderAppID,
			"target_app_profile_id":        lineage.TargetCustomerAppID,
			"target_config_version":        lineage.TargetConfigVersion,
			"migration_job_id":             job.PublicID,
			"migration_config_fingerprint": lineage.MigrationConfigFingerprint,
		}); err != nil {
			return err
		}
		if err := support.Audit(tx, &payload.CustomerID, actor, "app.migration.source.archive", "customer_app", support.ResourceID(source.ID), &sourceBefore, &source, approval.PublicID, requestID); err != nil {
			return err
		}
		return support.Audit(tx, &payload.CustomerID, actor, "app.migration.cutover", "customer_app", support.ResourceID(target.ID), &targetBefore, &target, approval.PublicID+": "+payload.EvidenceRef, requestID)
	default:
		return domain.Invalid("unsupported governance approval action")
	}
}

func (s *Service) List(query ListQuery) ([]model.GovernanceApproval, error) {
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 100
	}
	db := s.db.Order("id desc").Limit(query.Limit)
	if query.CustomerID != nil {
		if *query.CustomerID == 0 {
			return nil, domain.Invalid("customer_id must be positive")
		}
		db = db.Where("customer_id = ?", *query.CustomerID)
	}
	if query.Status != "" {
		db = db.Where("status = ?", strings.ToLower(strings.TrimSpace(query.Status)))
	}
	if query.BeforeID > 0 {
		db = db.Where("id < ?", query.BeforeID)
	}
	var result []model.GovernanceApproval
	return result, db.Find(&result).Error
}

func sameOptionalID(first, second *uint64) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}

func optionalIDMatches(requested, actual *uint64) bool {
	return requested == nil || sameOptionalID(requested, actual)
}
