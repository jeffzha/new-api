package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	approvalpkg "github.com/QuantumNous/new-api/claw-control/internal/approval"
	credentialpkg "github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/providerverify"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

var sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var appAliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,78}[a-z0-9]$`)

type Service struct {
	db                      *gorm.DB
	requireHighRiskApproval bool
	resolver                secrets.Resolver
}

type Limits = productpolicy.Limits

type SaveConfigCommand struct {
	CustomerID          uint64
	ExpectedVersion     int64
	ProviderEnvironment string
	Region              string
	SpaceID             string
	AppID               string
	TemplateAgentID     string
	CredentialProfileID *uint64
	AppKeySecretRef     string
	AppKeyFingerprint   string
	DisplayName         string
	Limits              Limits
	Capabilities        []string
	Actor               string
	RequestID           string
}

type SaveConfigResult struct {
	App     *model.CustomerApp      `json:"app"`
	Version *model.AppConfigVersion `json:"config_version"`
}

type CreateAdditionalCommand struct {
	Config SaveConfigCommand
	Alias  string
}

type SetDefaultCommand struct {
	CustomerID                    uint64
	Selector                      string
	ExpectedTargetVersion         int64
	ExpectedCurrentDefaultVersion int64
	Actor                         string
	RequestID                     string
}

type RecordVerificationCommand struct {
	CustomerID            uint64
	CustomerAppID         uint64
	ExpectedVersion       int64
	ConfigVersion         int64
	Result                string
	AppMode               int
	ReleaseStatus         string
	TemplateAgentStatus   string
	DynamicAgentConfig    bool
	ProviderRequestIDs    []string
	SanitizedResponseHash string
	ErrorCode             string
	ErrorMessage          string
	Actor                 string
	RequestID             string
}

type TransitionCommand struct {
	CustomerID      uint64
	ExpectedVersion int64
	Action          string
	Reason          string
	Actor           string
	RequestID       string
}

type VerifyPendingCommand struct {
	CustomerID      uint64
	CustomerAppID   uint64
	ExpectedVersion int64
	ConfigVersion   int64
	Actor           string
	RequestID       string
}

func New(db *gorm.DB, requireHighRiskApproval bool, resolver ...secrets.Resolver) *Service {
	var configured secrets.Resolver
	if len(resolver) > 0 {
		configured = resolver[0]
	}
	return &Service{db: db, requireHighRiskApproval: requireHighRiskApproval, resolver: configured}
}

func (s *Service) VerifyPending(ctx context.Context, command VerifyPendingCommand, resolver secrets.Resolver, verifier providerverify.Verifier) (*model.AppVerification, error) {
	if command.CustomerID == 0 || command.ExpectedVersion <= 0 || command.ConfigVersion <= 0 {
		return nil, domain.Invalid("customer_id, expected_version, and config_version are required")
	}
	if resolver == nil || verifier == nil {
		return nil, domain.Unavailable("trusted Tencent provider verification is not configured")
	}
	var stable model.CustomerApp
	var version model.AppConfigVersion
	var current model.AppConfigVersion
	var credential model.CredentialProfile
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("customer_id = ?", command.CustomerID)
		if command.CustomerAppID > 0 {
			query = query.Where("id = ?", command.CustomerAppID)
		} else {
			query = query.Where("slot = ?", "primary")
		}
		if err := query.First(&stable).Error; err != nil {
			return domain.NotFound("customer App not found")
		}
		if !selectableOrMigrationSlot(stable.Slot) {
			return domain.Conflict("target App has an unsupported slot")
		}
		if stable.RowVersion != command.ExpectedVersion {
			return domain.Conflict("App row version changed; expected %d, current %d", command.ExpectedVersion, stable.RowVersion)
		}
		if err := tx.Where("customer_app_id = ? AND config_version = ?", stable.ID, command.ConfigVersion).First(&version).Error; err != nil {
			return domain.NotFound("App config version not found")
		}
		if stable.PendingConfigVersionID == nil || *stable.PendingConfigVersionID != version.ID || version.Status != model.AppConfigStatusDraft {
			return domain.Conflict("App config version is not the current pending draft")
		}
		if version.CredentialProfileID == nil {
			return domain.Conflict("App config version has no credential profile")
		}
		if err := tx.First(&credential, *version.CredentialProfileID).Error; err != nil {
			return domain.NotFound("credential profile not found")
		}
		if credential.Status != model.CredentialStatusActive || credential.ProviderEnvironment != stable.ProviderEnvironment ||
			!credentialpkg.AvailableToCustomer(credential, stable.CustomerID) {
			return domain.Conflict("credential profile is inactive, provider-mismatched, or unavailable to this customer")
		}
		if stable.CurrentConfigVersionID != nil {
			if err := tx.First(&current, *stable.CurrentConfigVersionID).Error; err != nil {
				return domain.Conflict("current App config is unavailable")
			}
			configuredResolver := s.resolver
			if configuredResolver == nil {
				configuredResolver = resolver
			}
			if err := approvalpkg.ValidateAppCredentialChange(tx, configuredResolver, stable, current, version); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	configuredResolver := s.resolver
	if configuredResolver == nil {
		configuredResolver = resolver
	}
	appKey, err := secrets.ResolveAppKey(configuredResolver, version.AppKeySecretRef, version.AppKeyFingerprint, version.AppKeyFingerprintVersion)
	if err != nil {
		return nil, domain.Unavailable("provider AppKey integrity verification failed")
	}
	pair, err := secrets.ResolveCredentialPair(configuredResolver, credential.SecretIDRef, credential.SecretKeyRef, credential.Fingerprint, credential.FingerprintVersion)
	if err != nil {
		return nil, domain.Unavailable("provider credential integrity verification failed")
	}
	providerResult, err := verifier.Verify(ctx, providerverify.Target{
		ProviderEnvironment: stable.ProviderEnvironment,
		Region:              version.Region,
		SpaceID:             version.SpaceID,
		AppID:               stable.AppID,
		TemplateAgentID:     version.TemplateAgentID,
		AppKey:              appKey,
		SecretID:            pair.SecretID,
		SecretKey:           pair.SecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("verify Tencent ADP configuration: %w", err)
	}
	return s.RecordVerification(RecordVerificationCommand{
		CustomerID: command.CustomerID, CustomerAppID: stable.ID,
		ExpectedVersion: command.ExpectedVersion, ConfigVersion: command.ConfigVersion,
		Result: providerResult.Result, AppMode: providerResult.AppMode, ReleaseStatus: providerResult.ReleaseStatus,
		TemplateAgentStatus: providerResult.TemplateAgentStatus, DynamicAgentConfig: providerResult.DynamicAgentConfig,
		ProviderRequestIDs:    providerResult.ProviderRequestIDs,
		SanitizedResponseHash: providerResult.SanitizedResponseHash, ErrorCode: providerResult.ErrorCode,
		ErrorMessage: providerResult.ErrorMessage, Actor: command.Actor, RequestID: command.RequestID,
	})
}

func (s *Service) PrepareMigration(command SaveConfigCommand) (*SaveConfigResult, error) {
	if err := validateSaveConfig(&command); err != nil {
		return nil, err
	}
	if err := validateAppKeyFingerprint(s.resolver, command.AppKeySecretRef, command.AppKeyFingerprint); err != nil {
		return nil, err
	}
	if command.ExpectedVersion <= 0 {
		return nil, domain.Invalid("expected_version of the source primary App is required")
	}
	capabilitiesJSON, err := jsonx.Marshal(command.Capabilities)
	if err != nil {
		return nil, err
	}
	limitsJSON, err := jsonx.Marshal(command.Limits)
	if err != nil {
		return nil, err
	}
	result := &SaveConfigResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var customer model.Customer
		if err := database.ForUpdate(tx).First(&customer, command.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		if customer.Status != model.CustomerStatusActive {
			return domain.Conflict("only an active customer can prepare App migration")
		}
		var source model.CustomerApp
		if err := database.ForUpdate(tx).Where("customer_id = ? AND slot = ?", command.CustomerID, "primary").First(&source).Error; err != nil {
			return domain.NotFound("source primary App not found")
		}
		if source.RowVersion != command.ExpectedVersion {
			return domain.Conflict("source App row version changed; expected %d, current %d", command.ExpectedVersion, source.RowVersion)
		}
		if source.CurrentConfigVersionID == nil || source.Status == model.AppStatusArchived || source.AppID == command.AppID {
			return domain.Conflict("source App must be verified and target AppId must be different")
		}
		var pendingMigrations int64
		if err := tx.Model(&model.CustomerApp{}).Where("customer_id = ? AND slot LIKE ?", command.CustomerID, "migration:%").Count(&pendingMigrations).Error; err != nil {
			return err
		}
		if pendingMigrations > 0 {
			return domain.Conflict("customer already has a pending App migration")
		}
		var credential model.CredentialProfile
		if err := tx.First(&credential, *command.CredentialProfileID).Error; err != nil {
			return domain.NotFound("credential profile not found")
		}
		if credential.Status != model.CredentialStatusActive || credential.ProviderEnvironment != command.ProviderEnvironment ||
			!credentialpkg.AvailableToCustomer(credential, command.CustomerID) {
			return domain.Conflict("credential profile is inactive, provider-mismatched, or unavailable to this customer")
		}
		target := &model.CustomerApp{
			CustomerID: command.CustomerID, Slot: "migration:" + support.PublicID("app"),
			Selector: support.PublicID("aps"), Alias: "migration-" + support.PublicID("app"),
			ProviderEnvironment: command.ProviderEnvironment, AppID: command.AppID,
			DisplayName: command.DisplayName, Status: model.AppStatusDraft,
			AuthEpoch: 1, RowVersion: 1,
		}
		if err := tx.Create(target).Error; err != nil {
			return domain.Conflict("migration AppId is already assigned")
		}
		version := &model.AppConfigVersion{
			CustomerAppID: target.ID, ConfigVersion: 1, Status: model.AppConfigStatusDraft,
			Region: command.Region, SpaceID: command.SpaceID, TemplateAgentID: command.TemplateAgentID,
			CredentialProfileID: command.CredentialProfileID,
			AppKeySecretRef:     command.AppKeySecretRef, AppKeyFingerprint: command.AppKeyFingerprint,
			AppKeyFingerprintVersion: secrets.CanonicalFingerprintVersion, RowVersion: 1,
			LimitsJSON: string(limitsJSON), CapabilitiesJSON: string(capabilitiesJSON), CreatedBy: command.Actor,
		}
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		target.PendingConfigVersionID = &version.ID
		if err := tx.Save(target).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &command.CustomerID, command.Actor, "app.migration.prepare", "customer_app", support.ResourceID(target.ID), nil, target, "", command.RequestID); err != nil {
			return err
		}
		result.App, result.Version = target, version
		return nil
	})
	return result, err
}

func (s *Service) SaveConfig(command SaveConfigCommand) (*SaveConfigResult, error) {
	if err := validateSaveConfig(&command); err != nil {
		return nil, err
	}
	if err := validateAppKeyFingerprint(s.resolver, command.AppKeySecretRef, command.AppKeyFingerprint); err != nil {
		return nil, err
	}
	capabilitiesJSON, err := jsonx.Marshal(command.Capabilities)
	if err != nil {
		return nil, err
	}
	limitsJSON, err := jsonx.Marshal(command.Limits)
	if err != nil {
		return nil, err
	}
	result := &SaveConfigResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var customer model.Customer
		if err := database.ForUpdate(tx).First(&customer, command.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		if customer.Status == model.CustomerStatusDisabled || customer.Status == model.CustomerStatusArchived {
			return domain.Conflict("customer status %s does not allow App configuration", customer.Status)
		}
		var credential model.CredentialProfile
		if err := tx.First(&credential, *command.CredentialProfileID).Error; err != nil {
			return domain.NotFound("credential profile not found")
		}
		if credential.Status != model.CredentialStatusActive || credential.ProviderEnvironment != command.ProviderEnvironment ||
			!credentialpkg.AvailableToCustomer(credential, command.CustomerID) {
			return domain.Conflict("credential profile is inactive, provider-mismatched, or unavailable to this customer")
		}
		var stable model.CustomerApp
		find := database.ForUpdate(tx).Where("customer_id = ? AND slot = ?", command.CustomerID, "primary").First(&stable)
		isNew := find.Error == gorm.ErrRecordNotFound
		if find.Error != nil && !isNew {
			return find.Error
		}
		var before any
		if isNew {
			if command.ExpectedVersion != 0 {
				return domain.Conflict("expected_version must be 0 when creating the first App")
			}
			stable = model.CustomerApp{
				CustomerID:          command.CustomerID,
				Slot:                "primary",
				Selector:            support.PublicID("aps"),
				Alias:               "primary",
				ProviderEnvironment: command.ProviderEnvironment,
				AppID:               command.AppID,
				DisplayName:         command.DisplayName,
				Status:              model.AppStatusDraft,
				AuthEpoch:           1,
				RowVersion:          1,
			}
			if err := tx.Create(&stable).Error; err != nil {
				return domain.Conflict("AppId is already assigned or primary App already exists")
			}
		} else {
			beforeCopy := stable
			before = &beforeCopy
			if stable.RowVersion != command.ExpectedVersion {
				return domain.Conflict("App row version changed; expected %d, current %d", command.ExpectedVersion, stable.RowVersion)
			}
			if stable.Status == model.AppStatusArchived {
				return domain.Conflict("archived App cannot receive a new configuration")
			}
			if stable.PendingConfigVersionID != nil {
				return domain.Conflict("App already has a pending config version that must be verified or rejected first")
			}
			if stable.CurrentConfigVersionID != nil && (stable.AppID != command.AppID || stable.ProviderEnvironment != command.ProviderEnvironment) {
				return domain.Conflict("verified AppId/provider cannot be changed in place; create a migration App")
			}
			stable.AppID = command.AppID
			stable.ProviderEnvironment = command.ProviderEnvironment
			stable.DisplayName = command.DisplayName
			stable.RowVersion++
		}
		var maxVersion int64
		if err := tx.Model(&model.AppConfigVersion{}).Where("customer_app_id = ?", stable.ID).Select("COALESCE(MAX(config_version), 0)").Scan(&maxVersion).Error; err != nil {
			return err
		}
		version := &model.AppConfigVersion{
			CustomerAppID:            stable.ID,
			ConfigVersion:            maxVersion + 1,
			Status:                   model.AppConfigStatusDraft,
			Region:                   command.Region,
			SpaceID:                  command.SpaceID,
			TemplateAgentID:          command.TemplateAgentID,
			CredentialProfileID:      command.CredentialProfileID,
			AppKeySecretRef:          command.AppKeySecretRef,
			AppKeyFingerprint:        command.AppKeyFingerprint,
			AppKeyFingerprintVersion: secrets.CanonicalFingerprintVersion,
			RowVersion:               1,
			LimitsJSON:               string(limitsJSON),
			CapabilitiesJSON:         string(capabilitiesJSON),
			CreatedBy:                command.Actor,
		}
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		stable.PendingConfigVersionID = &version.ID
		if err := tx.Save(&stable).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &command.CustomerID, command.Actor, "app.config.create", "customer_app", support.ResourceID(stable.ID), before, &stable, "", command.RequestID); err != nil {
			return err
		}
		result.App = &stable
		result.Version = version
		return nil
	})
	return result, err
}

func (s *Service) RecordVerification(command RecordVerificationCommand) (*model.AppVerification, error) {
	if command.CustomerID == 0 || command.ExpectedVersion <= 0 || command.ConfigVersion <= 0 || strings.TrimSpace(command.Result) == "" {
		return nil, domain.Invalid("customer_id, expected_version, config_version, and result are required")
	}
	command.Result = strings.ToLower(strings.TrimSpace(command.Result))
	if command.Result != "verified" && command.Result != "invalid" {
		return nil, domain.Invalid("result must be verified or invalid")
	}
	validMode := command.AppMode >= 1 && command.AppMode <= 4
	templateValid := !command.DynamicAgentConfig || (command.AppMode == 4 && strings.EqualFold(command.TemplateAgentStatus, "available"))
	if command.Result == "verified" && (!validMode || strings.ToLower(command.ReleaseStatus) != "published" || !templateValid) {
		return nil, domain.Invalid("verified result requires provider AppMode 1-4, a published release, and a validated template Agent only for dynamic Claw")
	}
	command.SanitizedResponseHash = strings.ToLower(strings.TrimSpace(command.SanitizedResponseHash))
	if len(command.ProviderRequestIDs) == 0 || !sha256Pattern.MatchString(command.SanitizedResponseHash) {
		return nil, domain.Invalid("provider_request_ids and a sha256 sanitized_response_hash are required")
	}
	requestIDsJSON, err := jsonx.Marshal(command.ProviderRequestIDs)
	if err != nil {
		return nil, err
	}
	verification := &model.AppVerification{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var stable model.CustomerApp
		query := database.ForUpdate(tx).Where("customer_id = ?", command.CustomerID)
		if command.CustomerAppID > 0 {
			query = query.Where("id = ?", command.CustomerAppID)
		} else {
			query = query.Where("slot = ?", "primary")
		}
		if err := query.First(&stable).Error; err != nil {
			return domain.NotFound("customer App not found")
		}
		if !selectableOrMigrationSlot(stable.Slot) {
			return domain.Conflict("target App has an unsupported slot")
		}
		if stable.RowVersion != command.ExpectedVersion {
			return domain.Conflict("App row version changed; expected %d, current %d", command.ExpectedVersion, stable.RowVersion)
		}
		var version model.AppConfigVersion
		if err := database.ForUpdate(tx).Where("customer_app_id = ? AND config_version = ?", stable.ID, command.ConfigVersion).First(&version).Error; err != nil {
			return domain.NotFound("App config version not found")
		}
		if stable.PendingConfigVersionID == nil || *stable.PendingConfigVersionID != version.ID || version.Status != model.AppConfigStatusDraft {
			return domain.Conflict("App config version is not the current pending draft")
		}
		if command.Result == "verified" {
			if version.CredentialProfileID == nil {
				return domain.Conflict("App config version has no credential profile")
			}
			var credential model.CredentialProfile
			if err := tx.First(&credential, *version.CredentialProfileID).Error; err != nil {
				return domain.NotFound("credential profile not found")
			}
			if credential.Status != model.CredentialStatusActive || credential.ProviderEnvironment != stable.ProviderEnvironment ||
				!credentialpkg.AvailableToCustomer(credential, stable.CustomerID) {
				return domain.Conflict("credential profile is inactive, provider-mismatched, or unavailable to this customer")
			}
			if _, err := secrets.ResolveAppKey(s.resolver, version.AppKeySecretRef, version.AppKeyFingerprint, version.AppKeyFingerprintVersion); err != nil {
				return domain.Unavailable("provider AppKey integrity verification failed before cutover")
			}
			if _, err := secrets.ResolveCredentialPair(s.resolver, credential.SecretIDRef, credential.SecretKeyRef, credential.Fingerprint, credential.FingerprintVersion); err != nil {
				return domain.Unavailable("provider credential integrity verification failed before cutover")
			}
			if stable.CurrentConfigVersionID != nil {
				var current model.AppConfigVersion
				if err := database.ForUpdate(tx).First(&current, *stable.CurrentConfigVersionID).Error; err != nil {
					return domain.Conflict("current App config is unavailable")
				}
				if err := approvalpkg.ValidateAppCredentialChange(tx, s.resolver, stable, current, version); err != nil {
					return err
				}
			}
		}
		before := stable
		now := time.Now().UTC()
		verification = &model.AppVerification{
			PublicID:               support.PublicID("verify"),
			CustomerAppID:          stable.ID,
			AppConfigVersionID:     version.ID,
			Result:                 command.Result,
			AppMode:                command.AppMode,
			ReleaseStatus:          strings.ToLower(strings.TrimSpace(command.ReleaseStatus)),
			TemplateAgentStatus:    strings.ToLower(strings.TrimSpace(command.TemplateAgentStatus)),
			DynamicAgentConfig:     command.DynamicAgentConfig,
			ProviderRequestIDsJSON: string(requestIDsJSON),
			SanitizedResponseHash:  strings.TrimSpace(command.SanitizedResponseHash),
			ErrorCode:              strings.TrimSpace(command.ErrorCode),
			ErrorMessage:           strings.TrimSpace(command.ErrorMessage),
			VerifiedBy:             command.Actor,
			VerifiedAt:             now,
		}
		if err := tx.Create(verification).Error; err != nil {
			return err
		}
		if command.Result == "verified" {
			if stable.CurrentConfigVersionID != nil {
				if err := tx.Model(&model.AppConfigVersion{}).Where("id = ?", *stable.CurrentConfigVersionID).Updates(map[string]any{
					"status": model.AppConfigStatusSuperseded, "row_version": gorm.Expr("row_version + 1"),
				}).Error; err != nil {
					return err
				}
			}
			version.Status = model.AppConfigStatusVerified
			version.VerifiedAt = &now
			version.RowVersion++
			stable.CurrentConfigVersionID = &version.ID
			stable.PendingConfigVersionID = nil
			stable.VerifiedAt = &now
			stable.AuthEpoch++
			if stable.Status == model.AppStatusDraft {
				stable.Status = model.AppStatusVerified
			}
		} else {
			version.Status = model.AppConfigStatusInvalid
			version.RowVersion++
			stable.PendingConfigVersionID = nil
		}
		stable.RowVersion++
		if err := tx.Save(&version).Error; err != nil {
			return err
		}
		if err := tx.Save(&stable).Error; err != nil {
			return err
		}
		if command.Result == "verified" && strings.HasPrefix(stable.Slot, "migration:") {
			if _, err := appmigration.EnsureJob(tx, stable, version); err != nil {
				return err
			}
		}
		if command.Result == "verified" {
			if err := support.Enqueue(tx, &command.CustomerID, "CACHE_INVALIDATE", fmt.Sprintf("app:%d:epoch:%d", stable.ID, stable.AuthEpoch), map[string]any{
				"customer_id": command.CustomerID, "customer_app_id": stable.ID,
				"application_id": stable.AppID, "config_version": version.ConfigVersion, "auth_epoch": stable.AuthEpoch,
			}); err != nil {
				return err
			}
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "app.verification.record", "customer_app", support.ResourceID(stable.ID), &before, &stable, command.ErrorMessage, command.RequestID)
	})
	return verification, err
}

func (s *Service) CreateAdditional(command CreateAdditionalCommand) (*SaveConfigResult, error) {
	command.Alias = strings.ToLower(strings.TrimSpace(command.Alias))
	if !appAliasPattern.MatchString(command.Alias) || command.Alias == "primary" {
		return nil, domain.Invalid("alias must be 3-80 lowercase letters, digits, or hyphens and cannot be primary")
	}
	if err := validateSaveConfig(&command.Config); err != nil {
		return nil, err
	}
	if err := validateAppKeyFingerprint(s.resolver, command.Config.AppKeySecretRef, command.Config.AppKeyFingerprint); err != nil {
		return nil, err
	}
	if command.Config.ExpectedVersion != 0 {
		return nil, domain.Invalid("expected_version must be 0 when adding an App")
	}
	capabilitiesJSON, err := jsonx.Marshal(command.Config.Capabilities)
	if err != nil {
		return nil, err
	}
	limitsJSON, err := jsonx.Marshal(command.Config.Limits)
	if err != nil {
		return nil, err
	}
	result := &SaveConfigResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var customer model.Customer
		if err := database.ForUpdate(tx).First(&customer, command.Config.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		if customer.Status != model.CustomerStatusActive {
			return domain.Conflict("only an active customer can add an App")
		}
		var primary model.CustomerApp
		if err := database.ForUpdate(tx).Where("customer_id = ? AND slot = ?", customer.ID, "primary").First(&primary).Error; err != nil {
			return domain.Conflict("customer must have exactly one primary App before adding another")
		}
		if primary.CurrentConfigVersionID == nil {
			return domain.Conflict("primary App must have a verified current config before adding another App")
		}
		var primaryConfig model.AppConfigVersion
		if err := tx.First(&primaryConfig, *primary.CurrentConfigVersionID).Error; err != nil || primaryConfig.CredentialProfileID == nil {
			return domain.Conflict("primary App current credential is unavailable")
		}
		if command.Config.CredentialProfileID == nil || *command.Config.CredentialProfileID != *primaryConfig.CredentialProfileID {
			return domain.Conflict("a new additional App must initially use the primary App current credential; switch it later through app_credential_change approval")
		}
		var credential model.CredentialProfile
		if err := tx.First(&credential, *command.Config.CredentialProfileID).Error; err != nil {
			return domain.NotFound("credential profile not found")
		}
		if credential.Status != model.CredentialStatusActive || credential.ProviderEnvironment != command.Config.ProviderEnvironment ||
			!credentialpkg.AvailableToCustomer(credential, command.Config.CustomerID) {
			return domain.Conflict("credential profile is inactive, provider-mismatched, or unavailable to this customer")
		}
		selector := support.PublicID("aps")
		application := &model.CustomerApp{
			CustomerID: customer.ID, Slot: "app:" + selector, Selector: selector, Alias: command.Alias,
			ProviderEnvironment: command.Config.ProviderEnvironment, AppID: command.Config.AppID,
			DisplayName: command.Config.DisplayName, Status: model.AppStatusDraft, AuthEpoch: 1, RowVersion: 1,
		}
		if err := tx.Create(application).Error; err != nil {
			return domain.Conflict("App selector, alias, or provider AppId is already assigned")
		}
		version := &model.AppConfigVersion{
			CustomerAppID: application.ID, ConfigVersion: 1, Status: model.AppConfigStatusDraft,
			Region: command.Config.Region, SpaceID: command.Config.SpaceID,
			TemplateAgentID:     command.Config.TemplateAgentID,
			CredentialProfileID: command.Config.CredentialProfileID,
			AppKeySecretRef:     command.Config.AppKeySecretRef, AppKeyFingerprint: command.Config.AppKeyFingerprint,
			AppKeyFingerprintVersion: secrets.CanonicalFingerprintVersion, RowVersion: 1,
			LimitsJSON: string(limitsJSON), CapabilitiesJSON: string(capabilitiesJSON), CreatedBy: command.Config.Actor,
		}
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		application.PendingConfigVersionID = &version.ID
		if err := tx.Save(application).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &customer.ID, command.Config.Actor, "app.create", "customer_app", application.Selector, nil, application, "", command.Config.RequestID); err != nil {
			return err
		}
		result.App, result.Version = application, version
		return nil
	})
	return result, err
}

func (s *Service) SetDefault(command SetDefaultCommand) (*model.CustomerApp, error) {
	command.Selector = strings.TrimSpace(command.Selector)
	if command.CustomerID == 0 || command.Selector == "" || command.ExpectedTargetVersion <= 0 || command.ExpectedCurrentDefaultVersion <= 0 {
		return nil, domain.Invalid("customer_id, selector, and both expected versions are required")
	}
	var result model.CustomerApp
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current model.CustomerApp
		if err := database.ForUpdate(tx).Where("customer_id = ? AND slot = ?", command.CustomerID, "primary").First(&current).Error; err != nil {
			return domain.NotFound("current default App not found")
		}
		if err := database.ForUpdate(tx).Where("customer_id = ? AND selector = ?", command.CustomerID, command.Selector).First(&result).Error; err != nil {
			return domain.NotFound("target App selector not found")
		}
		if current.ID == result.ID {
			if current.RowVersion != command.ExpectedCurrentDefaultVersion || result.RowVersion != command.ExpectedTargetVersion {
				return domain.Conflict("default App row version changed")
			}
			return nil
		}
		if current.RowVersion != command.ExpectedCurrentDefaultVersion || result.RowVersion != command.ExpectedTargetVersion {
			return domain.Conflict("App row version changed while setting the default")
		}
		if !strings.HasPrefix(result.Slot, "app:") || result.CurrentConfigVersionID == nil || result.Status == model.AppStatusDraft || result.Status == model.AppStatusArchived {
			return domain.Conflict("target App is not an eligible selectable App")
		}
		currentBefore, targetBefore := current, result
		current.Slot = "app:" + current.Selector
		current.AuthEpoch++
		current.RowVersion++
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		result.Slot = "primary"
		result.AuthEpoch++
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		if err := support.Enqueue(tx, &command.CustomerID, "CACHE_INVALIDATE", fmt.Sprintf("app-default:%d:%d:%d", command.CustomerID, current.ID, result.ID), map[string]any{
			"customer_id": command.CustomerID, "previous_app_profile_id": current.ID, "default_app_profile_id": result.ID,
		}); err != nil {
			return err
		}
		if err := support.Audit(tx, &command.CustomerID, command.Actor, "app.default.remove", "customer_app", current.Selector, &currentBefore, &current, "", command.RequestID); err != nil {
			return err
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "app.default.set", "customer_app", result.Selector, &targetBefore, &result, "", command.RequestID)
	})
	return &result, err
}

func (s *Service) BySelector(customerID uint64, selector string) (*model.CustomerApp, error) {
	if customerID == 0 || strings.TrimSpace(selector) == "" {
		return nil, domain.Invalid("customer_id and selector are required")
	}
	var result model.CustomerApp
	if err := s.db.Where("customer_id = ? AND selector = ?", customerID, strings.TrimSpace(selector)).First(&result).Error; err != nil {
		return nil, domain.NotFound("customer App selector not found")
	}
	return &result, nil
}

func selectableOrMigrationSlot(slot string) bool {
	return slot == "primary" || strings.HasPrefix(slot, "app:") || strings.HasPrefix(slot, "migration:")
}

func (s *Service) Transition(command TransitionCommand) (*model.CustomerApp, error) {
	command.Action = strings.ToLower(strings.TrimSpace(command.Action))
	if command.CustomerID == 0 || command.ExpectedVersion <= 0 {
		return nil, domain.Invalid("customer_id and expected_version are required")
	}
	if command.Action == "disable" && s.requireHighRiskApproval {
		return nil, domain.Conflict("App disable requires an approved governance request")
	}
	var result model.CustomerApp
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("customer_id = ? AND slot = ?", command.CustomerID, "primary").First(&result).Error; err != nil {
			return domain.NotFound("customer App not found")
		}
		if result.RowVersion != command.ExpectedVersion {
			return domain.Conflict("App row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
		}
		before := result
		now := time.Now().UTC()
		switch command.Action {
		case "enable":
			if result.Status != model.AppStatusVerified && result.Status != model.AppStatusSuspended {
				return domain.Conflict("App in status %s cannot be enabled", result.Status)
			}
			if result.CurrentConfigVersionID == nil {
				return domain.Conflict("App has no verified config version")
			}
			var periods int64
			if err := tx.Model(&model.PlanPeriod{}).Where(
				"customer_id = ? AND status = ? AND payment_status = ? AND start_at <= ? AND end_at > ?",
				command.CustomerID, model.PeriodStatusActive, model.PaymentStatusPaid, now, now,
			).Count(&periods).Error; err != nil {
				return err
			}
			if periods == 0 {
				return domain.Conflict("customer has no active paid plan period")
			}
			result.Status = model.AppStatusActive
			result.EnabledAt = &now
			result.SuspendedAt = nil
		case "suspend":
			if result.Status != model.AppStatusActive {
				return domain.Conflict("only an active App can be suspended")
			}
			if strings.TrimSpace(command.Reason) == "" {
				return domain.Invalid("reason is required when suspending an App")
			}
			result.Status = model.AppStatusSuspended
			result.SuspendedAt = &now
		case "disable":
			if result.Status == model.AppStatusArchived {
				return domain.Conflict("archived App cannot be disabled")
			}
			if strings.TrimSpace(command.Reason) == "" {
				return domain.Invalid("reason is required when disabling an App")
			}
			result.Status = model.AppStatusDisabled
			result.DisabledAt = &now
		case "prepare":
			if result.Status != model.AppStatusDisabled || result.CurrentConfigVersionID == nil {
				return domain.Conflict("only a disabled App with verified configuration can return to verified")
			}
			result.Status = model.AppStatusVerified
			result.DisabledAt = nil
		default:
			return domain.Invalid("unsupported App transition %q", command.Action)
		}
		result.AuthEpoch++
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		if err := support.Enqueue(tx, &command.CustomerID, "CACHE_INVALIDATE", fmt.Sprintf("app:%d:epoch:%d", result.ID, result.AuthEpoch), map[string]any{
			"customer_id": command.CustomerID, "customer_app_id": result.ID, "application_id": result.AppID,
			"status": result.Status, "auth_epoch": result.AuthEpoch,
		}); err != nil {
			return err
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "app."+command.Action, "customer_app", support.ResourceID(result.ID), &before, &result, command.Reason, command.RequestID)
	})
	return &result, err
}

func validateSaveConfig(command *SaveConfigCommand) error {
	if command.CustomerID == 0 {
		return domain.Invalid("customer_id is required")
	}
	command.ProviderEnvironment = strings.ToLower(strings.TrimSpace(command.ProviderEnvironment))
	command.Region = strings.TrimSpace(command.Region)
	command.SpaceID = strings.TrimSpace(command.SpaceID)
	command.AppID = strings.TrimSpace(command.AppID)
	command.TemplateAgentID = strings.TrimSpace(command.TemplateAgentID)
	command.AppKeySecretRef = strings.TrimSpace(command.AppKeySecretRef)
	command.AppKeyFingerprint = strings.TrimSpace(command.AppKeyFingerprint)
	command.DisplayName = strings.TrimSpace(command.DisplayName)
	if command.ProviderEnvironment != model.ProviderChinaTencentCloud && command.ProviderEnvironment != model.ProviderChinaTencentADP {
		return domain.Invalid("provider_environment must be china_tencent_cloud or china_tencent_adp")
	}
	if command.ProviderEnvironment == "" || command.Region == "" || command.SpaceID == "" || command.AppID == "" || command.CredentialProfileID == nil || !secrets.ValidProviderReference(command.AppKeySecretRef) || command.AppKeyFingerprint == "" || command.DisplayName == "" {
		return domain.Invalid("provider environment, region, space, App, secret reference, fingerprint, and display name are required")
	}
	if len(command.AppID) > 128 {
		return domain.Invalid("app_id must be at most 128 characters")
	}
	if err := productpolicy.ValidateLimits(command.Limits); err != nil {
		return err
	}
	normalizedCapabilities, err := productpolicy.NormalizeCapabilities(command.Capabilities)
	if err != nil {
		return err
	}
	command.Capabilities = normalizedCapabilities
	return nil
}

func validateAppKeyFingerprint(resolver secrets.Resolver, reference, fingerprint string) error {
	if _, err := secrets.ResolveAppKey(resolver, reference, fingerprint, secrets.CanonicalFingerprintVersion); err != nil {
		if err == secrets.ErrProviderFingerprintInvalid {
			return domain.Invalid("AppKey fingerprint does not match server-resolved material")
		}
		return domain.Unavailable("provider AppKey material is unavailable")
	}
	return nil
}
