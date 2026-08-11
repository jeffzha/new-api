package appmigration

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

const (
	minimumLease = 10 * time.Second
	maximumLease = 2 * time.Minute
)

type Service struct {
	db       *gorm.DB
	resolver secrets.Resolver
}

type ProviderContext struct {
	Vendor           string `json:"vendor"`
	ServiceVendor    string `json:"service_vendor"`
	AppID            string `json:"app_id"`
	AppKey           string `json:"app_key"`
	SpaceID          string `json:"space_id"`
	TemplateAgentID  string `json:"template_agent_id"`
	SecretID         string `json:"secret_id"`
	SecretKey        string `json:"secret_key"`
	ProviderAppMode  int    `json:"provider_app_mode"`
	RuntimeProfile   string `json:"runtime_profile"`
	ExecutionEnabled bool   `json:"execution_enabled"`
}

type ClaimedTask struct {
	MigrationJobID          string          `json:"migration_job_id"`
	MigrationMemberID       string          `json:"migration_member_id"`
	AttemptID               string          `json:"attempt_id"`
	LeaseToken              string          `json:"lease_token"`
	LeaseExpiresAt          time.Time       `json:"lease_expires_at"`
	BindingID               string          `json:"binding_id"`
	ADPAccountID            string          `json:"adp_account_id"`
	CustomerID              uint64          `json:"customer_id"`
	SourceApplicationID     string          `json:"source_application_id"`
	TargetApplicationID     string          `json:"target_application_id"`
	TargetAppProfileID      uint64          `json:"target_app_profile_id"`
	TargetConfigVersion     int64           `json:"target_config_version"`
	TargetConfigFingerprint string          `json:"target_config_fingerprint"`
	ProviderAppMode         int             `json:"provider_app_mode"`
	RuntimeProfile          string          `json:"runtime_profile"`
	ExecutionEnabled        bool            `json:"execution_enabled"`
	Mode                    string          `json:"mode"`
	KnownTargetAgentID      string          `json:"known_target_agent_id,omitempty"`
	Provider                ProviderContext `json:"provider"`
}

type ReportCommand struct {
	MigrationMemberID       string
	AttemptID               string
	LeaseToken              string
	Status                  string
	TargetAppProfileID      uint64
	TargetConfigVersion     int64
	TargetConfigFingerprint string
	ProviderAppMode         int
	RuntimeProfile          string
	ExecutionEnabled        bool
	TargetAgentID           string
	TargetReadbackHash      string
	ErrorCode               string
}

type ReportResult struct {
	MigrationJobID    string `json:"migration_job_id"`
	MigrationMemberID string `json:"migration_member_id"`
	Status            string `json:"status"`
	JobStatus         string `json:"job_status"`
	ExpectedMembers   int    `json:"expected_members"`
	SucceededMembers  int    `json:"succeeded_members"`
	FailedMembers     int    `json:"failed_members"`
}

type ReplanCommand struct {
	CustomerID               uint64
	TargetCustomerAppID      uint64
	ExpectedTargetRowVersion int64
	ExpectedJobRowVersion    int64
	Actor                    string
	Reason                   string
	RequestID                string
}

type RetryMemberCommand struct {
	CustomerID            uint64
	TargetCustomerAppID   uint64
	MigrationMemberID     string
	ExpectedJobRowVersion int64
	ExpectedMemberVersion int64
	Actor                 string
	Reason                string
	RequestID             string
}

type configFingerprint struct {
	CustomerID                   uint64 `json:"customer_id"`
	TargetCustomerAppID          uint64 `json:"target_customer_app_id"`
	TargetApplicationID          string `json:"target_application_id"`
	ProviderEnvironment          string `json:"provider_environment"`
	TargetAppConfigVersionID     uint64 `json:"target_app_config_version_id"`
	TargetConfigVersion          int64  `json:"target_config_version"`
	Region                       string `json:"region"`
	SpaceID                      string `json:"space_id"`
	TemplateAgentID              string `json:"template_agent_id"`
	CredentialProfileID          uint64 `json:"credential_profile_id"`
	CredentialOwnerScope         string `json:"credential_owner_scope"`
	CredentialStatus             string `json:"credential_status"`
	CredentialRowVersion         int64  `json:"credential_row_version"`
	CredentialFingerprint        string `json:"credential_fingerprint"`
	CredentialFingerprintVersion int    `json:"credential_fingerprint_version"`
	AppKeyFingerprint            string `json:"app_key_fingerprint"`
	AppKeyFingerprintVersion     int    `json:"app_key_fingerprint_version"`
	LimitsJSON                   string `json:"limits_json"`
	CapabilitiesJSON             string `json:"capabilities_json"`
	ProviderAppMode              int    `json:"provider_app_mode"`
	RuntimeProfile               string `json:"runtime_profile"`
	ExecutionEnabled             bool   `json:"execution_enabled"`
}

type memberFingerprint struct {
	IdentityBindingID uint64 `json:"identity_binding_id"`
	BindingPublicID   string `json:"binding_id"`
	ADPAccountID      string `json:"adp_account_id"`
}

type providerRuntime struct {
	AppMode          int
	RuntimeProfile   string
	ExecutionEnabled bool
}

func New(db *gorm.DB, resolver secrets.Resolver) *Service {
	return &Service{db: db, resolver: resolver}
}

// EnsureJob snapshots all active identity bindings in the same transaction
// that verifies the target migration App. Replays are accepted only when the
// complete target tuple and member set remain identical.
func EnsureJob(tx *gorm.DB, target model.CustomerApp, version model.AppConfigVersion) (*model.AppMigrationJob, error) {
	if !strings.HasPrefix(target.Slot, "migration:") || target.CurrentConfigVersionID == nil || *target.CurrentConfigVersionID != version.ID || version.CredentialProfileID == nil {
		return nil, domain.Conflict("verified migration target is incomplete")
	}
	if target.ProviderEnvironment != model.ProviderChinaTencentADP {
		return nil, domain.Conflict("Agent rebuild supports only the Tencent ADP provider environment")
	}
	var source model.CustomerApp
	if err := database.ForUpdate(tx).Where("customer_id = ? AND slot = ?", target.CustomerID, "primary").First(&source).Error; err != nil {
		return nil, domain.Conflict("source primary App is unavailable for migration rebuild")
	}
	if source.ID == target.ID || source.CurrentConfigVersionID == nil || source.Status == model.AppStatusArchived {
		return nil, domain.Conflict("source primary App is unavailable for migration rebuild")
	}
	var credential model.CredentialProfile
	if err := tx.First(&credential, *version.CredentialProfileID).Error; err != nil {
		return nil, domain.Conflict("target migration credential profile is unavailable")
	}
	runtime, err := loadVerifiedRuntime(tx, target, version)
	if err != nil {
		return nil, err
	}
	fingerprint := targetFingerprint(target, version, credential, runtime)

	var identities []model.IdentityBinding
	if err := database.ForUpdate(tx).Where("customer_id = ? AND status = ?", target.CustomerID, model.IdentityStatusActive).Order("id asc").Find(&identities).Error; err != nil {
		return nil, err
	}
	members := make([]memberFingerprint, 0, len(identities))
	for _, identity := range identities {
		members = append(members, memberFingerprint{IdentityBindingID: identity.ID, BindingPublicID: identity.PublicID, ADPAccountID: identity.ADPAccountID})
	}
	memberSetHash := support.Hash(members)

	var existing model.AppMigrationJob
	err = database.ForUpdate(tx).Where("target_customer_app_id = ?", target.ID).Order("generation desc").First(&existing).Error
	if err == nil {
		if existing.CustomerID != target.CustomerID || existing.SourceCustomerAppID != source.ID || existing.TargetAppConfigVersionID != version.ID ||
			existing.TargetConfigVersion != version.ConfigVersion || existing.TargetCredentialProfileID != credential.ID ||
			existing.TargetProviderAppMode != runtime.AppMode || existing.TargetRuntimeProfile != runtime.RuntimeProfile || existing.TargetExecutionEnabled != runtime.ExecutionEnabled ||
			existing.TargetConfigFingerprint != fingerprint || existing.MemberSetFingerprint != memberSetHash || existing.ExpectedMembers != len(identities) {
			return nil, domain.Conflict("migration rebuild job no longer matches the verified target and active bindings")
		}
		return &existing, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	return createJobRows(tx, source, target, version, credential, runtime, identities, memberSetHash, 1)
}

func createJobRows(tx *gorm.DB, source, target model.CustomerApp, version model.AppConfigVersion, credential model.CredentialProfile, runtime providerRuntime, identities []model.IdentityBinding, memberSetHash string, generation int64) (*model.AppMigrationJob, error) {
	fingerprint := targetFingerprint(target, version, credential, runtime)
	jobStatus := model.AppMigrationJobStatusPending
	readyAt := (*time.Time)(nil)
	now := time.Now().UTC()
	if len(identities) == 0 {
		jobStatus = model.AppMigrationJobStatusReady
		readyAt = &now
	}
	job := &model.AppMigrationJob{
		PublicID: support.PublicID("amj"), CustomerID: target.CustomerID,
		SourceCustomerAppID: source.ID, TargetCustomerAppID: target.ID,
		Generation:               generation,
		TargetAppConfigVersionID: version.ID, TargetConfigVersion: version.ConfigVersion,
		TargetCredentialProfileID: credential.ID, TargetConfigFingerprint: fingerprint,
		TargetProviderAppMode: runtime.AppMode, TargetRuntimeProfile: runtime.RuntimeProfile, TargetExecutionEnabled: runtime.ExecutionEnabled,
		MemberSetFingerprint: memberSetHash, ExpectedMembers: len(identities), Status: jobStatus,
		RowVersion: 1, ReadyAt: readyAt,
	}
	if err := tx.Create(job).Error; err != nil {
		return nil, err
	}
	failed := 0
	for _, identity := range identities {
		status := model.AppMigrationMemberStatusPending
		errorCode := ""
		if strings.TrimSpace(identity.ADPAccountID) == "" {
			status = model.AppMigrationMemberStatusFailed
			errorCode = "adp_account_unconfirmed"
			failed++
		}
		member := model.AppMigrationMember{
			PublicID: support.PublicID("amm"), JobID: job.ID, IdentityBindingID: identity.ID,
			BindingPublicID: identity.PublicID, ADPAccountID: identity.ADPAccountID,
			TargetConfigFingerprint: fingerprint, Status: status, RecoveryMode: "copy", ErrorCode: errorCode, RowVersion: 1,
		}
		if err := tx.Create(&member).Error; err != nil {
			return nil, err
		}
	}
	if failed > 0 {
		job.FailedMembers = failed
		job.Status = model.AppMigrationJobStatusFailed
		job.RowVersion++
		if err := tx.Save(job).Error; err != nil {
			return nil, err
		}
	}
	if err := support.Audit(tx, &target.CustomerID, "system:app-migration", "app.migration.rebuild.prepare", "app_migration_job", job.PublicID, nil, job, "", ""); err != nil {
		return nil, err
	}
	return job, nil
}

func targetFingerprint(target model.CustomerApp, version model.AppConfigVersion, credential model.CredentialProfile, runtime providerRuntime) string {
	return support.Hash(configFingerprint{
		CustomerID: target.CustomerID, TargetCustomerAppID: target.ID, TargetApplicationID: target.AppID,
		ProviderEnvironment: target.ProviderEnvironment, TargetAppConfigVersionID: version.ID,
		TargetConfigVersion: version.ConfigVersion, Region: version.Region, SpaceID: version.SpaceID,
		TemplateAgentID: version.TemplateAgentID, CredentialProfileID: credential.ID,
		CredentialOwnerScope: credential.OwnerScope, CredentialStatus: credential.Status,
		CredentialRowVersion:  credential.RowVersion,
		CredentialFingerprint: credential.Fingerprint, CredentialFingerprintVersion: credential.FingerprintVersion,
		AppKeyFingerprint: version.AppKeyFingerprint, AppKeyFingerprintVersion: version.AppKeyFingerprintVersion,
		LimitsJSON: version.LimitsJSON, CapabilitiesJSON: version.CapabilitiesJSON,
		ProviderAppMode: runtime.AppMode, RuntimeProfile: runtime.RuntimeProfile, ExecutionEnabled: runtime.ExecutionEnabled,
	})
}

func loadVerifiedRuntime(tx *gorm.DB, target model.CustomerApp, version model.AppConfigVersion) (providerRuntime, error) {
	var verification model.AppVerification
	if err := tx.Where("customer_app_id = ? AND app_config_version_id = ? AND result = ?", target.ID, version.ID, "verified").
		Order("verified_at desc").Order("id desc").First(&verification).Error; err != nil {
		return providerRuntime{}, domain.Conflict("migration target has no verified provider runtime snapshot")
	}
	runtime := providerRuntime{AppMode: verification.AppMode, ExecutionEnabled: true}
	switch verification.AppMode {
	case 1:
		runtime.RuntimeProfile = "standard_v2"
	case 2:
		runtime.RuntimeProfile = "multi_agent_v2"
	case 3:
		runtime.RuntimeProfile = "workflow_v2"
	case 4:
		if verification.DynamicAgentConfig {
			if strings.TrimSpace(version.TemplateAgentID) == "" {
				return providerRuntime{}, domain.Conflict("dynamic Claw migration target has no verified template Agent")
			}
			runtime.RuntimeProfile = "claw_dynamic_v2"
		} else {
			runtime.RuntimeProfile = "claw_static_v2"
		}
	default:
		return providerRuntime{}, domain.Conflict("migration target provider AppMode is unsupported")
	}
	return runtime, nil
}

func (s *Service) Claim(ctx context.Context, workerID string, lease time.Duration, now time.Time) (*ClaimedTask, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 64 || lease < minimumLease || lease > maximumLease {
		return nil, domain.Invalid("worker_id and a lease between 10s and 2m are required")
	}
	now = now.UTC()
	var result *ClaimedTask
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A timed-out provider side effect is never automatically replayed: CopyAgent
		// may have succeeded even when the worker lost its response.
		var expired []model.AppMigrationMember
		if err := database.ForUpdate(tx).Where("status = ? AND lease_until <= ?", model.AppMigrationMemberStatusRunning, now).Find(&expired).Error; err != nil {
			return err
		}
		for index := range expired {
			expired[index].Status = model.AppMigrationMemberStatusFailed
			expired[index].ErrorCode = "worker_lease_expired_provider_unknown"
			expired[index].CompletedAt = &now
			expired[index].RowVersion++
			if err := tx.Save(&expired[index]).Error; err != nil {
				return err
			}
			if err := refreshJob(tx, expired[index].JobID, now); err != nil {
				return err
			}
		}

		var member model.AppMigrationMember
		claimableJobs := tx.Model(&model.AppMigrationJob{}).Select("id").Where("status IN ?", []string{model.AppMigrationJobStatusPending, model.AppMigrationJobStatusRunning})
		query := database.ForUpdate(tx).Where("status = ? AND job_id IN (?)", model.AppMigrationMemberStatusPending, claimableJobs).Order("id asc").Limit(1).First(&member)
		if query.Error == gorm.ErrRecordNotFound {
			return nil
		}
		if query.Error != nil {
			return query.Error
		}
		var job model.AppMigrationJob
		if err := database.ForUpdate(tx).First(&job, member.JobID).Error; err != nil {
			return err
		}
		if job.Status != model.AppMigrationJobStatusPending && job.Status != model.AppMigrationJobStatusRunning {
			return domain.Conflict("migration job is not claimable")
		}
		var identity model.IdentityBinding
		if err := database.ForUpdate(tx).First(&identity, member.IdentityBindingID).Error; err != nil || identity.Status != model.IdentityStatusActive ||
			identity.CustomerID != job.CustomerID || identity.PublicID != member.BindingPublicID || identity.ADPAccountID != member.ADPAccountID || identity.ADPAccountID == "" {
			member.Status = model.AppMigrationMemberStatusFailed
			member.ErrorCode = "identity_binding_changed"
			member.CompletedAt = &now
			if saveErr := tx.Save(&member).Error; saveErr != nil {
				return saveErr
			}
			return refreshJob(tx, job.ID, now)
		}
		target, version, credential, err := loadAndValidateTarget(tx, job)
		if err != nil {
			return err
		}
		var source model.CustomerApp
		if err := tx.First(&source, job.SourceCustomerAppID).Error; err != nil || source.CustomerID != job.CustomerID || source.Slot != "primary" {
			return domain.Conflict("migration source App changed before task claim")
		}
		appKey, err := secrets.ResolveAppKey(s.resolver, version.AppKeySecretRef, version.AppKeyFingerprint, version.AppKeyFingerprintVersion)
		if err != nil {
			return domain.Unavailable("target provider AppKey integrity verification failed")
		}
		pair, err := secrets.ResolveCredentialPair(s.resolver, credential.SecretIDRef, credential.SecretKeyRef, credential.Fingerprint, credential.FingerprintVersion)
		if err != nil {
			return domain.Unavailable("target provider credential integrity verification failed")
		}
		leaseToken, leaseHash, err := randomToken()
		if err != nil {
			return err
		}
		attemptID := support.PublicID("ama")
		leaseUntil := now.Add(lease)
		member.Status = model.AppMigrationMemberStatusRunning
		member.AttemptID = attemptID
		member.ClaimedBy = workerID
		member.LeaseTokenHash = leaseHash
		member.LeaseUntil = &leaseUntil
		member.AttemptCount++
		member.ErrorCode = ""
		member.RowVersion++
		if err := tx.Save(&member).Error; err != nil {
			return err
		}
		job.Status = model.AppMigrationJobStatusRunning
		job.RowVersion++
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		result = &ClaimedTask{
			MigrationJobID: job.PublicID, MigrationMemberID: member.PublicID,
			AttemptID: attemptID, LeaseToken: leaseToken, LeaseExpiresAt: leaseUntil,
			BindingID: member.BindingPublicID, ADPAccountID: member.ADPAccountID, CustomerID: job.CustomerID,
			SourceApplicationID: source.AppID, TargetApplicationID: target.AppID,
			TargetAppProfileID: target.ID, TargetConfigVersion: version.ConfigVersion,
			TargetConfigFingerprint: job.TargetConfigFingerprint,
			ProviderAppMode:         job.TargetProviderAppMode, RuntimeProfile: job.TargetRuntimeProfile,
			ExecutionEnabled: job.TargetExecutionEnabled,
			Mode:             member.RecoveryMode, KnownTargetAgentID: member.TargetAgentID,
			Provider: ProviderContext{Vendor: "Tencent", ServiceVendor: "ChinaTencentADP", AppID: target.AppID,
				AppKey: appKey, SpaceID: version.SpaceID, TemplateAgentID: version.TemplateAgentID,
				SecretID: pair.SecretID, SecretKey: pair.SecretKey,
				ProviderAppMode: job.TargetProviderAppMode, RuntimeProfile: job.TargetRuntimeProfile,
				ExecutionEnabled: job.TargetExecutionEnabled},
		}
		return nil
	})
	return result, err
}

func (s *Service) Report(ctx context.Context, command ReportCommand, now time.Time) (*ReportResult, error) {
	command.MigrationMemberID = strings.TrimSpace(command.MigrationMemberID)
	command.AttemptID = strings.TrimSpace(command.AttemptID)
	command.LeaseToken = strings.TrimSpace(command.LeaseToken)
	command.Status = strings.ToLower(strings.TrimSpace(command.Status))
	command.TargetConfigFingerprint = strings.TrimSpace(command.TargetConfigFingerprint)
	command.TargetAgentID = strings.TrimSpace(command.TargetAgentID)
	command.TargetReadbackHash = strings.ToLower(strings.TrimSpace(command.TargetReadbackHash))
	command.ErrorCode = strings.TrimSpace(command.ErrorCode)
	if command.MigrationMemberID == "" || command.AttemptID == "" || command.LeaseToken == "" ||
		(command.Status != model.AppMigrationMemberStatusSucceeded && command.Status != model.AppMigrationMemberStatusFailed) {
		return nil, domain.Invalid("migration member, attempt, lease token, and terminal status are required")
	}
	if command.Status == model.AppMigrationMemberStatusSucceeded {
		dynamicRuntime := command.RuntimeProfile == "claw_dynamic_v2"
		if command.TargetAppProfileID == 0 || command.TargetConfigVersion <= 0 || command.TargetConfigFingerprint == "" ||
			len(command.TargetAgentID) > 128 || command.ErrorCode != "" ||
			command.ProviderAppMode < 1 || command.ProviderAppMode > 4 || command.RuntimeProfile == "" || !command.ExecutionEnabled {
			return nil, domain.Invalid("successful migration report requires the exact target and provider-runtime tuple")
		}
		if (dynamicRuntime && (command.TargetAgentID == "" || !validSHA256(command.TargetReadbackHash))) ||
			(!dynamicRuntime && (command.TargetAgentID != "" || command.TargetReadbackHash != "")) {
			return nil, domain.Invalid("migration readiness evidence must match the verified runtime profile")
		}
	} else if command.ErrorCode == "" || len(command.ErrorCode) > 80 || command.TargetReadbackHash != "" || len(command.TargetAgentID) > 128 ||
		(command.TargetAgentID != "" && command.ErrorCode != "provider_outcome_unknown" && command.ErrorCode != "target_agent_readback_failed") {
		return nil, domain.Invalid("failed migration report requires a bounded error_code and only provider_unknown may retain a known AgentId")
	}
	now = now.UTC()
	var result ReportResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var member model.AppMigrationMember
		if err := database.ForUpdate(tx).Where("public_id = ?", command.MigrationMemberID).First(&member).Error; err != nil {
			return domain.NotFound("migration member task not found")
		}
		var job model.AppMigrationJob
		if err := database.ForUpdate(tx).First(&job, member.JobID).Error; err != nil {
			return err
		}
		reportHash := support.Hash(command)
		if member.Status == model.AppMigrationMemberStatusSucceeded || member.Status == model.AppMigrationMemberStatusFailed {
			if member.AttemptID == command.AttemptID && member.ReportHash == reportHash {
				return fillReportResult(tx, job.ID, member, &result)
			}
			return domain.Conflict("migration task already has a different terminal report")
		}
		if member.Status != model.AppMigrationMemberStatusRunning || member.AttemptID != command.AttemptID || member.LeaseUntil == nil || !member.LeaseUntil.After(now) {
			return domain.Conflict("migration task lease is unavailable or expired")
		}
		providedHash := sha256.Sum256([]byte(command.LeaseToken))
		storedHash, err := hex.DecodeString(member.LeaseTokenHash)
		if err != nil || !hmac.Equal(storedHash, providedHash[:]) {
			return domain.Forbidden("migration task lease token is invalid")
		}
		if command.TargetAppProfileID != 0 && (command.TargetAppProfileID != job.TargetCustomerAppID || command.TargetConfigVersion != job.TargetConfigVersion ||
			command.TargetConfigFingerprint != job.TargetConfigFingerprint || member.TargetConfigFingerprint != job.TargetConfigFingerprint) {
			return domain.Conflict("migration report target tuple does not match the claimed job")
		}
		if command.Status == model.AppMigrationMemberStatusSucceeded && (command.ProviderAppMode != job.TargetProviderAppMode ||
			command.RuntimeProfile != job.TargetRuntimeProfile || command.ExecutionEnabled != job.TargetExecutionEnabled ||
			(job.TargetRuntimeProfile == "claw_dynamic_v2" && command.TargetAgentID == "") ||
			(job.TargetRuntimeProfile != "claw_dynamic_v2" && command.TargetAgentID != "")) {
			return domain.Conflict("migration report provider runtime or Agent readiness does not match the claimed job")
		}
		if _, _, _, err := loadAndValidateTarget(tx, job); err != nil {
			return err
		}
		member.Status = command.Status
		member.TargetAgentID = command.TargetAgentID
		member.TargetReadbackHash = command.TargetReadbackHash
		member.ErrorCode = command.ErrorCode
		member.ReportHash = reportHash
		member.LeaseUntil = nil
		member.CompletedAt = &now
		member.RowVersion++
		if err := tx.Save(&member).Error; err != nil {
			return err
		}
		if err := refreshJob(tx, job.ID, now); err != nil {
			return err
		}
		return fillReportResult(tx, job.ID, member, &result)
	})
	return &result, err
}

// Replan supersedes the immutable member snapshot after an administrator has
// reviewed a normal active-membership change. Old jobs and member outcomes are
// retained for audit and can never be claimed again.
func (s *Service) Replan(ctx context.Context, command ReplanCommand) (*model.AppMigrationJob, error) {
	command.Actor = strings.TrimSpace(command.Actor)
	command.Reason = strings.TrimSpace(command.Reason)
	if command.CustomerID == 0 || command.TargetCustomerAppID == 0 || command.ExpectedTargetRowVersion <= 0 || command.ExpectedJobRowVersion <= 0 ||
		command.Actor == "" || command.Reason == "" || len(command.Reason) > 1000 {
		return nil, domain.Invalid("customer, target, expected versions, actor, and reason are required")
	}
	var result *model.AppMigrationJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var target model.CustomerApp
		if err := database.ForUpdate(tx).First(&target, command.TargetCustomerAppID).Error; err != nil || target.CustomerID != command.CustomerID ||
			target.RowVersion != command.ExpectedTargetRowVersion || !strings.HasPrefix(target.Slot, "migration:") || target.CurrentConfigVersionID == nil {
			return domain.Conflict("migration target changed before rebuild replan")
		}
		var current model.AppMigrationJob
		if err := database.ForUpdate(tx).Where("target_customer_app_id = ?", target.ID).Order("generation desc").First(&current).Error; err != nil || current.RowVersion != command.ExpectedJobRowVersion || current.Status == model.AppMigrationJobStatusSuperseded {
			return domain.Conflict("migration rebuild job changed before replan")
		}
		var version model.AppConfigVersion
		if err := database.ForUpdate(tx).First(&version, *target.CurrentConfigVersionID).Error; err != nil || version.CredentialProfileID == nil {
			return domain.Conflict("migration target config is unavailable")
		}
		var credential model.CredentialProfile
		if err := database.ForUpdate(tx).First(&credential, *version.CredentialProfileID).Error; err != nil {
			return domain.Conflict("migration target credential is unavailable")
		}
		runtime, err := loadVerifiedRuntime(tx, target, version)
		if err != nil {
			return err
		}
		if targetFingerprint(target, version, credential, runtime) != current.TargetConfigFingerprint || runtime.AppMode != current.TargetProviderAppMode ||
			runtime.RuntimeProfile != current.TargetRuntimeProfile || runtime.ExecutionEnabled != current.TargetExecutionEnabled {
			return domain.Conflict("migration target config changed before replan")
		}
		var source model.CustomerApp
		if err := database.ForUpdate(tx).First(&source, current.SourceCustomerAppID).Error; err != nil || source.CustomerID != target.CustomerID || source.Slot != "primary" {
			return domain.Conflict("migration source App changed before replan")
		}
		var identities []model.IdentityBinding
		if err := database.ForUpdate(tx).Where("customer_id = ? AND status = ?", target.CustomerID, model.IdentityStatusActive).Order("id asc").Find(&identities).Error; err != nil {
			return err
		}
		members := make([]memberFingerprint, 0, len(identities))
		for _, identity := range identities {
			members = append(members, memberFingerprint{IdentityBindingID: identity.ID, BindingPublicID: identity.PublicID, ADPAccountID: identity.ADPAccountID})
		}
		before := current
		current.Status = model.AppMigrationJobStatusSuperseded
		current.RowVersion++
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		created, err := createJobRows(tx, source, target, version, credential, runtime, identities, support.Hash(members), current.Generation+1)
		if err != nil {
			return err
		}
		if err := support.Audit(tx, &target.CustomerID, command.Actor, "app.migration.rebuild.replan", "app_migration_job", created.PublicID, &before, created, command.Reason, command.RequestID); err != nil {
			return err
		}
		result = created
		return nil
	})
	return result, err
}

// RetryMember resets only failures that are provably safe to replay. A
// provider-unknown CopyAgent is never copied again; it may be retried only as a
// Describe readback when the worker durably retained the returned AgentId.
func (s *Service) RetryMember(ctx context.Context, command RetryMemberCommand) (*model.AppMigrationMember, error) {
	command.MigrationMemberID = strings.TrimSpace(command.MigrationMemberID)
	command.Actor = strings.TrimSpace(command.Actor)
	command.Reason = strings.TrimSpace(command.Reason)
	if command.CustomerID == 0 || command.TargetCustomerAppID == 0 || command.MigrationMemberID == "" || command.ExpectedJobRowVersion <= 0 ||
		command.ExpectedMemberVersion <= 0 || command.Actor == "" || command.Reason == "" || len(command.Reason) > 1000 {
		return nil, domain.Invalid("migration target, member, expected versions, actor, and reason are required")
	}
	var result model.AppMigrationMember
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var member model.AppMigrationMember
		if err := database.ForUpdate(tx).Where("public_id = ?", command.MigrationMemberID).First(&member).Error; err != nil {
			return domain.NotFound("migration member not found")
		}
		var job model.AppMigrationJob
		if err := database.ForUpdate(tx).First(&job, member.JobID).Error; err != nil || job.CustomerID != command.CustomerID ||
			job.TargetCustomerAppID != command.TargetCustomerAppID || job.RowVersion != command.ExpectedJobRowVersion || job.Status == model.AppMigrationJobStatusSuperseded ||
			member.RowVersion != command.ExpectedMemberVersion || member.Status != model.AppMigrationMemberStatusFailed {
			return domain.Conflict("migration member or job changed before retry")
		}
		var identity model.IdentityBinding
		if err := database.ForUpdate(tx).First(&identity, member.IdentityBindingID).Error; err != nil || identity.Status != model.IdentityStatusActive ||
			identity.CustomerID != job.CustomerID || identity.PublicID != member.BindingPublicID || strings.TrimSpace(identity.ADPAccountID) == "" {
			return domain.Conflict("migration identity is not ready for retry")
		}
		mode := "copy"
		switch member.ErrorCode {
		case "adp_account_unconfirmed", "identity_binding_changed", "identity_binding_mismatch", "target_provider_context_mismatch":
			member.ADPAccountID = identity.ADPAccountID
		case "provider_outcome_unknown", "target_agent_readback_failed", "target_agent_binding_requires_reconciliation":
			if member.TargetAgentID == "" {
				return domain.Conflict("provider-unknown migration cannot be replayed without a known target AgentId")
			}
			mode = "readback"
		default:
			return domain.Conflict("migration failure is not safely retryable")
		}
		before := member
		member.Status = model.AppMigrationMemberStatusPending
		member.RecoveryMode = mode
		member.AttemptID = ""
		member.ClaimedBy = ""
		member.LeaseTokenHash = ""
		member.LeaseUntil = nil
		member.ReportHash = ""
		member.ErrorCode = ""
		member.CompletedAt = nil
		member.RowVersion++
		if err := tx.Save(&member).Error; err != nil {
			return err
		}
		if err := refreshJob(tx, job.ID, time.Now().UTC()); err != nil {
			return err
		}
		if err := support.Audit(tx, &job.CustomerID, command.Actor, "app.migration.member.retry", "app_migration_member", member.PublicID, &before, &member, command.Reason, command.RequestID); err != nil {
			return err
		}
		result = member
		return nil
	})
	return &result, err
}

// AssertReadyForCutover locks and revalidates the target tuple and every
// current active identity binding. It is safe to call inside the approval's
// cutover transaction.
func AssertReadyForCutover(tx *gorm.DB, source, target model.CustomerApp) (*model.AppMigrationJob, error) {
	var job model.AppMigrationJob
	if err := database.ForUpdate(tx).Where("target_customer_app_id = ?", target.ID).Order("generation desc").First(&job).Error; err != nil {
		return nil, domain.Conflict("migration Agent rebuild job is unavailable")
	}
	if job.CustomerID != target.CustomerID || job.SourceCustomerAppID != source.ID || job.Status != model.AppMigrationJobStatusReady ||
		job.ExpectedMembers != job.SucceededMembers || job.FailedMembers != 0 {
		return nil, domain.Conflict("all active bindings must finish target-App Agent rebuild before cutover")
	}
	if _, _, _, err := loadAndValidateTarget(tx, job); err != nil {
		return nil, err
	}
	var identities []model.IdentityBinding
	if err := database.ForUpdate(tx).Where("customer_id = ? AND status = ?", target.CustomerID, model.IdentityStatusActive).Order("id asc").Find(&identities).Error; err != nil {
		return nil, err
	}
	members := make([]memberFingerprint, 0, len(identities))
	for _, identity := range identities {
		members = append(members, memberFingerprint{IdentityBindingID: identity.ID, BindingPublicID: identity.PublicID, ADPAccountID: identity.ADPAccountID})
		var readiness model.AppMigrationMember
		if err := database.ForUpdate(tx).Where("job_id = ? AND identity_binding_id = ?", job.ID, identity.ID).First(&readiness).Error; err != nil ||
			readiness.Status != model.AppMigrationMemberStatusSucceeded || readiness.BindingPublicID != identity.PublicID || readiness.ADPAccountID != identity.ADPAccountID ||
			(job.TargetRuntimeProfile == "claw_dynamic_v2" && (readiness.TargetAgentID == "" || !validSHA256(readiness.TargetReadbackHash))) ||
			(job.TargetRuntimeProfile != "claw_dynamic_v2" && (readiness.TargetAgentID != "" || readiness.TargetReadbackHash != "")) ||
			readiness.TargetConfigFingerprint != job.TargetConfigFingerprint {
			return nil, domain.Conflict("an active binding has no verified target-App Agent rebuild")
		}
	}
	if len(identities) != job.ExpectedMembers || support.Hash(members) != job.MemberSetFingerprint {
		return nil, domain.Conflict("active binding set changed after migration rebuild")
	}
	return &job, nil
}

func loadAndValidateTarget(tx *gorm.DB, job model.AppMigrationJob) (model.CustomerApp, model.AppConfigVersion, model.CredentialProfile, error) {
	var target model.CustomerApp
	if err := database.ForUpdate(tx).First(&target, job.TargetCustomerAppID).Error; err != nil {
		return target, model.AppConfigVersion{}, model.CredentialProfile{}, domain.Conflict("migration target App is unavailable")
	}
	var version model.AppConfigVersion
	if err := database.ForUpdate(tx).First(&version, job.TargetAppConfigVersionID).Error; err != nil {
		return target, version, model.CredentialProfile{}, domain.Conflict("migration target config is unavailable")
	}
	var credential model.CredentialProfile
	if err := database.ForUpdate(tx).First(&credential, job.TargetCredentialProfileID).Error; err != nil {
		return target, version, credential, domain.Conflict("migration target credential profile is unavailable")
	}
	runtime, runtimeErr := loadVerifiedRuntime(tx, target, version)
	if !strings.HasPrefix(target.Slot, "migration:") || target.Status != model.AppStatusVerified || target.CurrentConfigVersionID == nil ||
		*target.CurrentConfigVersionID != version.ID || version.CustomerAppID != target.ID || version.Status != model.AppConfigStatusVerified ||
		version.ConfigVersion != job.TargetConfigVersion || version.CredentialProfileID == nil || *version.CredentialProfileID != credential.ID ||
		runtimeErr != nil || runtime.AppMode != job.TargetProviderAppMode || runtime.RuntimeProfile != job.TargetRuntimeProfile ||
		runtime.ExecutionEnabled != job.TargetExecutionEnabled || targetFingerprint(target, version, credential, runtime) != job.TargetConfigFingerprint {
		return target, version, credential, domain.Conflict("migration target profile or config changed after rebuild preparation")
	}
	return target, version, credential, nil
}

func refreshJob(tx *gorm.DB, jobID uint64, now time.Time) error {
	var job model.AppMigrationJob
	if err := database.ForUpdate(tx).First(&job, jobID).Error; err != nil {
		return err
	}
	var succeeded, failed int64
	if err := tx.Model(&model.AppMigrationMember{}).Where("job_id = ? AND status = ?", job.ID, model.AppMigrationMemberStatusSucceeded).Count(&succeeded).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.AppMigrationMember{}).Where("job_id = ? AND status = ?", job.ID, model.AppMigrationMemberStatusFailed).Count(&failed).Error; err != nil {
		return err
	}
	job.SucceededMembers = int(succeeded)
	job.FailedMembers = int(failed)
	job.ReadyAt = nil
	switch {
	case failed > 0:
		job.Status = model.AppMigrationJobStatusFailed
	case int(succeeded) == job.ExpectedMembers:
		job.Status = model.AppMigrationJobStatusReady
		job.ReadyAt = &now
	default:
		job.Status = model.AppMigrationJobStatusRunning
	}
	job.RowVersion++
	return tx.Save(&job).Error
}

func fillReportResult(tx *gorm.DB, jobID uint64, member model.AppMigrationMember, result *ReportResult) error {
	var job model.AppMigrationJob
	if err := tx.First(&job, jobID).Error; err != nil {
		return err
	}
	*result = ReportResult{MigrationJobID: job.PublicID, MigrationMemberID: member.PublicID, Status: member.Status,
		JobStatus: job.Status, ExpectedMembers: job.ExpectedMembers, SucceededMembers: job.SucceededMembers, FailedMembers: job.FailedMembers}
	return nil
}

func randomToken() (string, string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", "", fmt.Errorf("generate migration lease token: %w", err)
	}
	token := hex.EncodeToString(data)
	digest := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(digest[:]), nil
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
