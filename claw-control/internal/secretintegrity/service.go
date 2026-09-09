package secretintegrity

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

const ReenrollConfirmation = "rebind-current-runtime-secrets"

type Service struct {
	db       *gorm.DB
	resolver secrets.Resolver
}

type ReenrollCommand struct {
	Confirmation string
	Actor        string
	RequestID    string
}

type ReenrollResult struct {
	CredentialProfiles int `json:"credential_profiles"`
	AppConfigs         int `json:"app_configs"`
}

func New(db *gorm.DB, resolver secrets.Resolver) *Service {
	return &Service{db: db, resolver: resolver}
}

// Check verifies every credential and AppKey that may serve runtime traffic.
// It deliberately treats legacy (version zero) fingerprints as untrusted.
func (s *Service) Check() error {
	return s.CheckContext(context.Background())
}

func (s *Service) CheckContext(ctx context.Context) error {
	if s == nil || s.db == nil || s.resolver == nil {
		return domain.Unavailable("provider secret integrity service is unavailable")
	}
	var profiles []model.CredentialProfile
	if err := s.db.WithContext(ctx).Where("status IN ?", []string{model.CredentialStatusActive, model.CredentialStatusRetiring}).Find(&profiles).Error; err != nil {
		return domain.Unavailable("provider secret integrity check failed")
	}
	for index := range profiles {
		profile := profiles[index]
		if _, err := secrets.ResolveCredentialPair(s.resolver, profile.SecretIDRef, profile.SecretKeyRef, profile.Fingerprint, profile.FingerprintVersion); err != nil {
			return domain.Unavailable("provider secret integrity check failed")
		}
	}
	var apps []model.CustomerApp
	if err := s.db.WithContext(ctx).Where("current_config_version_id IS NOT NULL OR pending_config_version_id IS NOT NULL").Find(&apps).Error; err != nil {
		return domain.Unavailable("provider secret integrity check failed")
	}
	configIDs := make([]uint64, 0, len(apps)*2)
	for index := range apps {
		configIDs = append(configIDs, uniqueConfigIDs(apps[index].CurrentConfigVersionID, apps[index].PendingConfigVersionID)...)
	}
	if len(configIDs) == 0 {
		return nil
	}
	var configs []model.AppConfigVersion
	if err := s.db.WithContext(ctx).Where("id IN ?", configIDs).Find(&configs).Error; err != nil {
		return domain.Unavailable("provider secret integrity check failed")
	}
	byID := make(map[uint64]model.AppConfigVersion, len(configs))
	for index := range configs {
		byID[configs[index].ID] = configs[index]
	}
	for index := range apps {
		for _, configID := range uniqueConfigIDs(apps[index].CurrentConfigVersionID, apps[index].PendingConfigVersionID) {
			config, ok := byID[configID]
			if !ok || config.CustomerAppID != apps[index].ID {
				return domain.Unavailable("provider secret integrity check failed")
			}
			if _, err := secrets.ResolveAppKey(s.resolver, config.AppKeySecretRef, config.AppKeyFingerprint, config.AppKeyFingerprintVersion); err != nil {
				return domain.Unavailable("provider secret integrity check failed")
			}
		}
	}
	return nil
}

// ReenrollLegacy is the explicit, auditable escape hatch for pre-canonical
// fingerprints. It never overwrites a canonical version-one fingerprint: a
// mismatch there must be handled through rotation or a new App config.
func (s *Service) ReenrollLegacy(command ReenrollCommand) (*ReenrollResult, error) {
	if strings.TrimSpace(command.Confirmation) != ReenrollConfirmation || strings.TrimSpace(command.Actor) == "" {
		return nil, domain.Invalid("explicit provider-secret re-enrollment confirmation and actor are required")
	}
	if s == nil || s.db == nil || s.resolver == nil {
		return nil, domain.Unavailable("provider secret integrity service is unavailable")
	}
	result := &ReenrollResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var profiles []model.CredentialProfile
		if err := tx.Where("fingerprint_version = ? AND status IN ?", 0, []string{
			model.CredentialStatusActive, model.CredentialStatusRetiring, model.CredentialStatusStaged,
		}).Find(&profiles).Error; err != nil {
			return err
		}
		for index := range profiles {
			profile := &profiles[index]
			_, fingerprint, err := secrets.ResolveCredentialPairFingerprint(s.resolver, profile.SecretIDRef, profile.SecretKeyRef)
			if err != nil {
				return domain.Unavailable("legacy provider credential cannot be re-enrolled")
			}
			beforeVersion := profile.RowVersion
			profile.Fingerprint = fingerprint
			profile.FingerprintVersion = secrets.CanonicalFingerprintVersion
			profile.RowVersion++
			updated := tx.Model(&model.CredentialProfile{}).Where("id = ? AND row_version = ? AND fingerprint_version = ?", profile.ID, beforeVersion, 0).
				Updates(map[string]any{"fingerprint": profile.Fingerprint, "fingerprint_version": profile.FingerprintVersion, "row_version": profile.RowVersion})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return domain.Conflict("legacy provider credential changed during re-enrollment")
			}
			if err := support.Audit(tx, profile.CustomerID, command.Actor, "credential.fingerprint.reenroll", "credential_profile", support.ResourceID(profile.ID),
				map[string]any{"fingerprint_version": 0, "row_version": beforeVersion},
				map[string]any{"fingerprint_version": profile.FingerprintVersion, "row_version": profile.RowVersion}, "", command.RequestID); err != nil {
				return err
			}
			result.CredentialProfiles++
		}

		var apps []model.CustomerApp
		if err := tx.Where("current_config_version_id IS NOT NULL OR pending_config_version_id IS NOT NULL").Find(&apps).Error; err != nil {
			return err
		}
		seen := make(map[uint64]struct{})
		for index := range apps {
			for _, configID := range uniqueConfigIDs(apps[index].CurrentConfigVersionID, apps[index].PendingConfigVersionID) {
				if _, ok := seen[configID]; ok {
					continue
				}
				seen[configID] = struct{}{}
				var config model.AppConfigVersion
				if err := tx.First(&config, configID).Error; err != nil || config.CustomerAppID != apps[index].ID {
					return domain.Conflict("runtime App config is unavailable during re-enrollment")
				}
				if config.AppKeyFingerprintVersion != 0 {
					continue
				}
				_, fingerprint, err := secrets.ResolveAppKeyFingerprint(s.resolver, config.AppKeySecretRef)
				if err != nil {
					return domain.Unavailable("legacy provider AppKey cannot be re-enrolled")
				}
				beforeVersion := config.RowVersion
				config.AppKeyFingerprint = fingerprint
				config.AppKeyFingerprintVersion = secrets.CanonicalFingerprintVersion
				config.RowVersion++
				updated := tx.Model(&model.AppConfigVersion{}).Where("id = ? AND row_version = ? AND app_key_fingerprint_version = ?", config.ID, beforeVersion, 0).
					Updates(map[string]any{"app_key_fingerprint": config.AppKeyFingerprint, "app_key_fingerprint_version": config.AppKeyFingerprintVersion, "row_version": config.RowVersion})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return domain.Conflict("legacy provider AppKey changed during re-enrollment")
				}
				if err := support.Audit(tx, &apps[index].CustomerID, command.Actor, "app_key.fingerprint.reenroll", "app_config_version", support.ResourceID(config.ID),
					map[string]any{"fingerprint_version": 0, "row_version": beforeVersion},
					map[string]any{"fingerprint_version": config.AppKeyFingerprintVersion, "row_version": config.RowVersion}, "", command.RequestID); err != nil {
					return err
				}
				result.AppConfigs++
			}
		}
		return nil
	})
	return result, err
}

func uniqueConfigIDs(first, second *uint64) []uint64 {
	result := make([]uint64, 0, 2)
	if first != nil && *first > 0 {
		result = append(result, *first)
	}
	if second != nil && *second > 0 && (first == nil || *first != *second) {
		result = append(result, *second)
	}
	return result
}
