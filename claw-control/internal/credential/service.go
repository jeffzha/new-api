package credential

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

type Service struct {
	db       *gorm.DB
	resolver secrets.Resolver
}

type CreateCommand struct {
	OwnerScope          string
	CustomerID          *uint64
	ProviderEnvironment string
	Name                string
	SecretIDRef         string
	SecretKeyRef        string
	Fingerprint         string
	Actor               string
	RequestID           string
}

type StageRotationCommand struct {
	CurrentProfileID       uint64
	ExpectedCurrentVersion int64
	SecretIDRef            string
	SecretKeyRef           string
	Fingerprint            string
	Actor                  string
	RequestID              string
}

type RetireCommand struct {
	ProfileID       uint64
	ExpectedVersion int64
	Actor           string
	Reason          string
	RequestID       string
}

func New(db *gorm.DB, resolver ...secrets.Resolver) *Service {
	var configured secrets.Resolver
	if len(resolver) > 0 {
		configured = resolver[0]
	}
	return &Service{db: db, resolver: configured}
}

func (s *Service) Create(command CreateCommand) (*model.CredentialProfile, error) {
	ownerScope, customerID, validScope := NormalizeOwnerScope(command.OwnerScope, command.CustomerID)
	if !validScope {
		return nil, domain.Invalid("owner_scope must be platform or customer:<customer_id>, with a matching positive customer_id")
	}
	command.OwnerScope, command.CustomerID = ownerScope, customerID
	command.ProviderEnvironment = strings.ToLower(strings.TrimSpace(command.ProviderEnvironment))
	command.Name = strings.TrimSpace(command.Name)
	command.SecretIDRef = strings.TrimSpace(command.SecretIDRef)
	command.SecretKeyRef = strings.TrimSpace(command.SecretKeyRef)
	command.Fingerprint = strings.TrimSpace(command.Fingerprint)
	if command.ProviderEnvironment != model.ProviderChinaTencentCloud && command.ProviderEnvironment != model.ProviderChinaTencentADP {
		return nil, domain.Invalid("provider_environment must be china_tencent_cloud or china_tencent_adp")
	}
	if command.ProviderEnvironment == "" || command.Name == "" || command.Fingerprint == "" ||
		!secrets.ValidProviderReference(command.SecretIDRef) || !secrets.ValidProviderReference(command.SecretKeyRef) {
		return nil, domain.Invalid("provider_environment, name, fingerprint, and env://WORKBENCH_PROVIDER_* secret references are required")
	}
	if err := validateCredentialFingerprint(s.resolver, command.SecretIDRef, command.SecretKeyRef, command.Fingerprint); err != nil {
		return nil, err
	}
	profile := &model.CredentialProfile{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if command.CustomerID != nil {
			var customerCount int64
			if err := tx.Model(&model.Customer{}).Where("id = ?", *command.CustomerID).Count(&customerCount).Error; err != nil {
				return err
			}
			if customerCount != 1 {
				return domain.NotFound("credential owner customer not found")
			}
		}
		var maxVersion int64
		if err := tx.Model(&model.CredentialProfile{}).
			Where("owner_scope = ? AND provider_environment = ? AND name = ?", command.OwnerScope, command.ProviderEnvironment, command.Name).
			Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
			return err
		}
		if maxVersion > 0 {
			return domain.Conflict("credential profile already exists; use the explicit rotation workflow")
		}
		now := time.Now().UTC()
		profile = &model.CredentialProfile{
			OwnerScope: command.OwnerScope, CustomerID: command.CustomerID,
			ProviderEnvironment: command.ProviderEnvironment,
			Name:                command.Name, SecretIDRef: command.SecretIDRef, SecretKeyRef: command.SecretKeyRef,
			Fingerprint: command.Fingerprint, FingerprintVersion: secrets.CanonicalFingerprintVersion,
			Status:  model.CredentialStatusActive,
			Version: maxVersion + 1, RowVersion: 1, ActivatedAt: &now, RotatedAt: &now,
		}
		if err := tx.Create(profile).Error; err != nil {
			return domain.Conflict("credential profile version could not be created")
		}
		return support.Audit(tx, profile.CustomerID, command.Actor, "credential.version.create", "credential_profile", support.ResourceID(profile.ID), nil, auditProjection(*profile), "", command.RequestID)
	})
	return profile, err
}

func (s *Service) StageRotation(command StageRotationCommand) (*model.CredentialProfile, error) {
	command.SecretIDRef = strings.TrimSpace(command.SecretIDRef)
	command.SecretKeyRef = strings.TrimSpace(command.SecretKeyRef)
	command.Fingerprint = strings.TrimSpace(command.Fingerprint)
	if command.CurrentProfileID == 0 || command.ExpectedCurrentVersion <= 0 || command.Fingerprint == "" ||
		!secrets.ValidProviderReference(command.SecretIDRef) || !secrets.ValidProviderReference(command.SecretKeyRef) {
		return nil, domain.Invalid("current_profile_id, expected_current_version, fingerprint, and env secret references are required")
	}
	if err := validateCredentialFingerprint(s.resolver, command.SecretIDRef, command.SecretKeyRef, command.Fingerprint); err != nil {
		return nil, err
	}
	var result model.CredentialProfile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current model.CredentialProfile
		if err := database.ForUpdate(tx).First(&current, command.CurrentProfileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NotFound("current credential profile not found")
			}
			return err
		}
		if current.RowVersion != command.ExpectedCurrentVersion {
			return domain.Conflict("credential row version changed; expected %d, current %d", command.ExpectedCurrentVersion, current.RowVersion)
		}
		if current.Status != model.CredentialStatusActive {
			return domain.Conflict("only an active credential profile can start rotation")
		}
		if !ValidOwner(current) {
			return domain.Conflict("credential profile has an invalid owner scope")
		}
		var staged int64
		if err := tx.Model(&model.CredentialProfile{}).Where(
			"owner_scope = ? AND provider_environment = ? AND name = ? AND status = ?",
			current.OwnerScope, current.ProviderEnvironment, current.Name, model.CredentialStatusStaged,
		).Count(&staged).Error; err != nil {
			return err
		}
		if staged > 0 {
			return domain.Conflict("credential profile already has a staged rotation")
		}
		var maxVersion int64
		if err := tx.Model(&model.CredentialProfile{}).Where(
			"owner_scope = ? AND provider_environment = ? AND name = ?", current.OwnerScope, current.ProviderEnvironment, current.Name,
		).Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		result = model.CredentialProfile{
			OwnerScope: current.OwnerScope, CustomerID: current.CustomerID,
			ProviderEnvironment: current.ProviderEnvironment, Name: current.Name,
			SecretIDRef: command.SecretIDRef, SecretKeyRef: command.SecretKeyRef,
			Fingerprint: command.Fingerprint, FingerprintVersion: secrets.CanonicalFingerprintVersion,
			Status:  model.CredentialStatusStaged,
			Version: maxVersion + 1, PreviousProfileID: &current.ID,
			RowVersion: 1, RotatedAt: &now,
		}
		if err := tx.Create(&result).Error; err != nil {
			return domain.Conflict("credential rotation could not be staged")
		}
		return support.Audit(tx, result.CustomerID, command.Actor, "credential.rotation.stage", "credential_profile", support.ResourceID(result.ID), nil, auditProjection(result), "", command.RequestID)
	})
	return &result, err
}

func (s *Service) Retire(command RetireCommand) (*model.CredentialProfile, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.ProfileID == 0 || command.ExpectedVersion <= 0 || command.Reason == "" {
		return nil, domain.Invalid("profile_id, expected_version, and reason are required")
	}
	var result model.CredentialProfile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).First(&result, command.ProfileID).Error; err != nil {
			return domain.NotFound("credential profile not found")
		}
		if result.Status == model.CredentialStatusRetired {
			return nil
		}
		if !ValidOwner(result) {
			return domain.Conflict("credential profile has an invalid owner scope")
		}
		if result.RowVersion != command.ExpectedVersion {
			return domain.Conflict("credential row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
		}
		if result.Status != model.CredentialStatusRetiring {
			return domain.Conflict("only a retiring credential profile can be retired")
		}
		var configIDs []uint64
		if err := tx.Model(&model.AppConfigVersion{}).Where("credential_profile_id = ?", result.ID).Pluck("id", &configIDs).Error; err != nil {
			return err
		}
		if len(configIDs) > 0 {
			var currentUses int64
			if err := tx.Model(&model.CustomerApp{}).Where(
				"current_config_version_id IN ? OR pending_config_version_id IN ?", configIDs, configIDs,
			).Count(&currentUses).Error; err != nil {
				return err
			}
			if currentUses > 0 {
				return domain.Conflict("credential is still referenced by a current or pending App configuration")
			}
		}
		before := result
		now := time.Now().UTC()
		result.Status = model.CredentialStatusRetired
		result.RetiredAt = &now
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		return support.Audit(tx, result.CustomerID, command.Actor, "credential.retire", "credential_profile", support.ResourceID(result.ID), auditProjection(before), auditProjection(result), command.Reason, command.RequestID)
	})
	return &result, err
}

func RuntimeEligible(status string) bool {
	return status == model.CredentialStatusActive || status == model.CredentialStatusRetiring
}

func auditProjection(profile model.CredentialProfile) map[string]any {
	return map[string]any{
		"id": profile.ID, "owner_scope": profile.OwnerScope, "customer_id": profile.CustomerID,
		"provider_environment": profile.ProviderEnvironment, "name": profile.Name,
		"fingerprint": profile.Fingerprint, "fingerprint_version": profile.FingerprintVersion,
		"status": profile.Status, "version": profile.Version,
		"previous_profile_id": profile.PreviousProfileID, "row_version": profile.RowVersion,
	}
}

func validateCredentialFingerprint(resolver secrets.Resolver, secretIDRef, secretKeyRef, fingerprint string) error {
	_, err := secrets.ResolveCredentialPair(resolver, secretIDRef, secretKeyRef, fingerprint, secrets.CanonicalFingerprintVersion)
	if errors.Is(err, secrets.ErrProviderSecretUnavailable) {
		return domain.Unavailable("provider credential material is unavailable")
	}
	if err != nil {
		return domain.Invalid("credential fingerprint does not match server-resolved material")
	}
	return nil
}
