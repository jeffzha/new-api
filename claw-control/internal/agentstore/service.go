package agentstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	credentialpkg "github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/providerverify"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

const (
	launchTTL              = time.Minute
	cursorTTL              = 10 * time.Minute
	pageSize               = 24
	verificationStaleAfter = 5 * time.Minute
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

type Service struct {
	db       *gorm.DB
	resolver secrets.Resolver
	verifier providerverify.Verifier
}

func New(db *gorm.DB, resolver secrets.Resolver, verifier providerverify.Verifier) *Service {
	return &Service{db: db, resolver: resolver, verifier: verifier}
}

type Metadata struct {
	DisplayName string   `json:"display_name"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	AvatarURL   string   `json:"avatar_url,omitempty"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
}

type EntitlementInput struct {
	SubjectType string     `json:"subject_type"`
	SubjectRef  string     `json:"subject_ref"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
}

type CreateCommand struct {
	Slug             string
	Metadata         Metadata
	SortOrder        int
	Featured         bool
	CustomerID       uint64
	CustomerAppID    uint64
	ExecutionEnabled bool
	Entitlements     []EntitlementInput
	Actor            string
	RequestID        string
}

type UpdateCommand struct {
	ItemID           string
	ExpectedVersion  int64
	Metadata         Metadata
	SortOrder        int
	Featured         bool
	ExecutionEnabled bool
	Entitlements     []EntitlementInput
	Actor            string
	RequestID        string
}

type TransitionCommand struct {
	ItemID          string
	ExpectedVersion int64
	Action          string
	Reason          string
	Actor           string
	RequestID       string
}

type AdminVersion struct {
	VersionID   string   `json:"version_id"`
	Generation  int64    `json:"generation"`
	DisplayName string   `json:"display_name"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	AvatarURL   string   `json:"avatar_url,omitempty"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
}

type AdminDeployment struct {
	DeploymentID        string     `json:"deployment_id"`
	CustomerID          uint64     `json:"customer_id"`
	CustomerAppID       uint64     `json:"customer_app_id"`
	Status              string     `json:"status"`
	RowVersion          int64      `json:"row_version"`
	ProviderAppMode     int        `json:"provider_app_mode"`
	RuntimeProfile      string     `json:"runtime_profile"`
	DynamicAgentConfig  bool       `json:"dynamic_agent_config"`
	ExecutionEnabled    bool       `json:"execution_enabled"`
	VerifiedConfig      int64      `json:"verified_config_version"`
	VerifiedAt          *time.Time `json:"verified_at,omitempty"`
	ProviderDisplayName string     `json:"provider_display_name,omitempty"`
	ProviderDescription string     `json:"provider_description,omitempty"`
	ProviderAvatarURL   string     `json:"provider_avatar_url,omitempty"`
	Capabilities        []string   `json:"capabilities"`
}

type AdminItem struct {
	ItemID         string                          `json:"item_id"`
	Slug           string                          `json:"slug"`
	Status         string                          `json:"status"`
	RowVersion     int64                           `json:"row_version"`
	SortOrder      int                             `json:"sort_order"`
	Featured       bool                            `json:"featured"`
	CurrentVersion *AdminVersion                   `json:"current_version,omitempty"`
	DraftVersion   *AdminVersion                   `json:"draft_version,omitempty"`
	Deployment     AdminDeployment                 `json:"deployment"`
	Entitlements   []model.AgentCatalogEntitlement `json:"entitlements"`
}

type Card struct {
	ID             string   `json:"id"`
	Slug           string   `json:"slug"`
	DisplayName    string   `json:"display_name"`
	Summary        string   `json:"summary"`
	Description    string   `json:"description,omitempty"`
	AvatarURL      string   `json:"avatar_url,omitempty"`
	Category       string   `json:"category"`
	Tags           []string `json:"tags"`
	Featured       bool     `json:"featured"`
	AppMode        int      `json:"app_mode"`
	RuntimeProfile string   `json:"runtime_profile"`
	Capabilities   []string `json:"capabilities"`
	Available      bool     `json:"available"`
}

type CatalogPage struct {
	Items      []Card `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type LaunchGrant struct {
	SelectionToken string
	DeploymentID   string
	CustomerAppID  uint64
	ExpiresAt      time.Time
}

func (s *Service) Create(command CreateCommand) (*AdminItem, error) {
	command.Slug = strings.ToLower(strings.TrimSpace(command.Slug))
	metadata, err := normalizeMetadata(command.Metadata)
	if err != nil || !slugPattern.MatchString(command.Slug) || command.CustomerID == 0 || command.CustomerAppID == 0 {
		if err != nil {
			return nil, err
		}
		return nil, domain.Invalid("valid slug, customer_id, and customer_app_id are required")
	}
	command.Metadata = metadata
	now := time.Now().UTC()
	itemID := support.PublicID("agi")
	versionID := support.PublicID("agv")
	deploymentID := support.PublicID("agd")
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var app model.CustomerApp
		if err := tx.Where("id = ? AND customer_id = ?", command.CustomerAppID, command.CustomerID).First(&app).Error; err != nil {
			return domain.NotFound("customer App not found")
		}
		if app.Status == model.AppStatusArchived {
			return domain.Conflict("archived customer App cannot be deployed")
		}
		version, err := makeVersion(itemID, versionID, 1, command.Metadata, command.Actor, now)
		if err != nil {
			return err
		}
		item := model.AgentCatalogItem{
			ID: itemID, Slug: command.Slug, Status: model.AgentCatalogStatusDraft,
			DraftVersionID: &version.ID, SortOrder: command.SortOrder, Featured: command.Featured,
			RowVersion: 1, CreatedBy: command.Actor, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&item).Error; err != nil {
			return domain.Conflict("Agent Store slug is already in use")
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		deployment := model.CustomerAgentDeployment{
			ID: deploymentID, ItemID: item.ID, CustomerID: command.CustomerID, CustomerAppID: app.ID,
			Status: model.AgentDeploymentStatusDraft, RowVersion: 1, ExecutionEnabled: false,
			CapabilitiesJSON: "[]", ProviderRequestIDsJSON: "[]", CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&deployment).Error; err != nil {
			return domain.Conflict("customer App already has an Agent Store deployment")
		}
		if err := replaceEntitlements(tx, deployment.ID, command.CustomerID, command.Entitlements, now); err != nil {
			return err
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "agent_store.create", "agent_catalog_item", item.ID, nil, item, "", command.RequestID)
	})
	if err != nil {
		return nil, err
	}
	return s.AdminGet(itemID)
}

func (s *Service) Update(command UpdateCommand) (*AdminItem, error) {
	command.ItemID = strings.TrimSpace(command.ItemID)
	metadata, err := normalizeMetadata(command.Metadata)
	if err != nil || command.ItemID == "" || command.ExpectedVersion <= 0 {
		if err != nil {
			return nil, err
		}
		return nil, domain.Invalid("item_id and expected_version are required")
	}
	command.Metadata = metadata
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var item model.AgentCatalogItem
		if err := database.ForUpdate(tx).First(&item, "id = ?", command.ItemID).Error; err != nil {
			return domain.NotFound("Agent Store item not found")
		}
		if item.RowVersion != command.ExpectedVersion {
			return domain.Conflict("Agent Store item row version changed")
		}
		if item.Status == model.AgentCatalogStatusArchived || item.Status == model.AgentCatalogStatusDisabled {
			return domain.Conflict("Agent Store item cannot be edited in its current status")
		}
		var generation int64
		if err := tx.Model(&model.AgentCatalogVersion{}).Where("item_id = ?", item.ID).Select("COALESCE(MAX(generation), 0)").Scan(&generation).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		version, err := makeVersion(item.ID, support.PublicID("agv"), generation+1, command.Metadata, command.Actor, now)
		if err != nil {
			return err
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		before := item
		item.DraftVersionID = &version.ID
		item.SortOrder = command.SortOrder
		item.Featured = command.Featured
		item.RowVersion++
		item.UpdatedAt = now
		if item.CurrentVersionID == nil && item.Status != model.AgentCatalogStatusRejected && item.Status != model.AgentCatalogStatusVerified {
			item.Status = model.AgentCatalogStatusDraft
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		var deployment model.CustomerAgentDeployment
		if err := database.ForUpdate(tx).Where("item_id = ?", item.ID).First(&deployment).Error; err != nil {
			return err
		}
		deployment.ExecutionEnabled = command.ExecutionEnabled && executionProfileAllowed(deployment.RuntimeProfile)
		deployment.RowVersion++
		deployment.UpdatedAt = now
		if err := tx.Save(&deployment).Error; err != nil {
			return err
		}
		if command.Entitlements != nil {
			if err := replaceEntitlements(tx, deployment.ID, deployment.CustomerID, command.Entitlements, now); err != nil {
				return err
			}
		}
		return support.Audit(tx, &deployment.CustomerID, command.Actor, "agent_store.update", "agent_catalog_item", item.ID, &before, &item, "", command.RequestID)
	})
	if err != nil {
		return nil, err
	}
	return s.AdminGet(command.ItemID)
}

func (s *Service) Verify(ctx context.Context, command TransitionCommand) (*AdminItem, error) {
	if s.resolver == nil || s.verifier == nil || command.ExpectedVersion <= 0 {
		return nil, domain.Unavailable("trusted Agent Store provider verification is unavailable")
	}
	type snapshot struct {
		item       model.AgentCatalogItem
		deployment model.CustomerAgentDeployment
		app        model.CustomerApp
		config     model.AppConfigVersion
		credential model.CredentialProfile
	}
	var state snapshot
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).First(&state.item, "id = ?", command.ItemID).Error; err != nil {
			return domain.NotFound("Agent Store item not found")
		}
		now := time.Now().UTC()
		if state.item.RowVersion != command.ExpectedVersion || state.item.Status == model.AgentCatalogStatusArchived {
			return domain.Conflict("Agent Store item version or status does not allow verification")
		}
		if state.item.Status == model.AgentCatalogStatusVerifying {
			if !state.item.UpdatedAt.Before(now.Add(-verificationStaleAfter)) {
				return domain.Conflict("Agent Store provider verification is already in progress")
			}
			state.item.Status = model.AgentCatalogStatusRejected
			state.item.RowVersion++
			state.item.UpdatedAt = now
			if err := tx.Save(&state.item).Error; err != nil {
				return err
			}
		}
		if err := database.ForUpdate(tx).Where("item_id = ?", state.item.ID).First(&state.deployment).Error; err != nil {
			return domain.NotFound("Agent Store deployment not found")
		}
		if err := tx.Where("id = ? AND customer_id = ?", state.deployment.CustomerAppID, state.deployment.CustomerID).First(&state.app).Error; err != nil || state.app.CurrentConfigVersionID == nil {
			return domain.Conflict("customer App has no verified configuration")
		}
		if err := tx.First(&state.config, *state.app.CurrentConfigVersionID).Error; err != nil || state.config.CredentialProfileID == nil || state.config.Status != model.AppConfigStatusVerified {
			return domain.Conflict("customer App verified configuration is unavailable")
		}
		if err := tx.First(&state.credential, *state.config.CredentialProfileID).Error; err != nil {
			return domain.NotFound("credential profile not found")
		}
		if state.credential.Status != model.CredentialStatusActive || !credentialpkg.AvailableToCustomer(state.credential, state.deployment.CustomerID) {
			return domain.Conflict("credential profile is unavailable to this customer")
		}
		state.item.Status = model.AgentCatalogStatusVerifying
		state.item.RowVersion++
		state.item.UpdatedAt = now
		return tx.Save(&state.item).Error
	}); err != nil {
		return nil, err
	}
	appKey, err := secrets.ResolveAppKey(s.resolver, state.config.AppKeySecretRef, state.config.AppKeyFingerprint, state.config.AppKeyFingerprintVersion)
	if err != nil {
		s.rejectInterruptedVerification(state.item.ID, state.item.RowVersion)
		return nil, domain.Unavailable("provider AppKey integrity verification failed")
	}
	pair, err := secrets.ResolveCredentialPair(s.resolver, state.credential.SecretIDRef, state.credential.SecretKeyRef, state.credential.Fingerprint, state.credential.FingerprintVersion)
	if err != nil {
		s.rejectInterruptedVerification(state.item.ID, state.item.RowVersion)
		return nil, domain.Unavailable("provider credential integrity verification failed")
	}
	providerResult, err := s.verifier.Verify(ctx, providerverify.Target{
		ProviderEnvironment: state.app.ProviderEnvironment, Region: state.config.Region,
		SpaceID: state.config.SpaceID, AppID: state.app.AppID, TemplateAgentID: state.config.TemplateAgentID,
		AppKey: appKey, SecretID: pair.SecretID, SecretKey: pair.SecretKey,
	})
	if err != nil {
		s.rejectInterruptedVerification(state.item.ID, state.item.RowVersion)
		return nil, fmt.Errorf("verify Agent Store Tencent Application: %w", err)
	}
	if err := normalizeProviderSnapshot(&providerResult); err != nil {
		providerResult.Result = "invalid"
		providerResult.ErrorCode = "invalid_provider_metadata"
		providerResult.DisplayName = ""
		providerResult.Description = ""
		providerResult.AvatarURL = ""
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var item model.AgentCatalogItem
		if err := database.ForUpdate(tx).First(&item, "id = ?", state.item.ID).Error; err != nil {
			return err
		}
		var deployment model.CustomerAgentDeployment
		if err := database.ForUpdate(tx).Where("item_id = ?", item.ID).First(&deployment).Error; err != nil {
			return err
		}
		var app model.CustomerApp
		if err := tx.First(&app, state.app.ID).Error; err != nil {
			return err
		}
		if item.RowVersion != state.item.RowVersion || app.CurrentConfigVersionID == nil || *app.CurrentConfigVersionID != state.config.ID || app.AuthEpoch != state.app.AuthEpoch {
			return domain.Conflict("Agent Store verification context changed during provider readback")
		}
		before := deployment
		now := time.Now().UTC()
		deployment.ProviderAppMode = providerResult.AppMode
		deployment.DynamicAgentConfig = providerResult.DynamicAgentConfig
		deployment.RuntimeProfile = runtimeProfile(providerResult.AppMode, providerResult.DynamicAgentConfig)
		deployment.ExecutionEnabled = deployment.ExecutionEnabled && executionProfileAllowed(deployment.RuntimeProfile)
		deployment.ProviderDisplayName = providerResult.DisplayName
		deployment.ProviderDescription = providerResult.Description
		deployment.ProviderAvatarURL = providerResult.AvatarURL
		deployment.SanitizedResponseHash = providerResult.SanitizedResponseHash
		requestIDs, marshalErr := jsonx.Marshal(providerResult.ProviderRequestIDs)
		if marshalErr != nil {
			return marshalErr
		}
		deployment.ProviderRequestIDsJSON = string(requestIDs)
		capabilities, marshalErr := jsonx.Marshal(profileCapabilities(deployment.RuntimeProfile))
		if marshalErr != nil {
			return marshalErr
		}
		deployment.CapabilitiesJSON = string(capabilities)
		deployment.RowVersion++
		deployment.UpdatedAt = now
		if providerResult.Result == "verified" && deployment.RuntimeProfile != "" {
			deployment.Status = model.AgentDeploymentStatusVerified
			deployment.VerifiedConfigVersionID = &state.config.ID
			deployment.VerifiedConfigVersion = state.config.ConfigVersion
			deployment.VerifiedAppAuthEpoch = app.AuthEpoch
			deployment.VerifiedCredentialHash = support.Hash(map[string]any{"app": state.config.AppKeyFingerprint, "credential": state.credential.Fingerprint})
			deployment.VerifiedAt = &now
			item.Status = model.AgentCatalogStatusVerified
		} else {
			deployment.Status = model.AgentDeploymentStatusDraft
			deployment.ExecutionEnabled = false
			item.Status = model.AgentCatalogStatusRejected
		}
		item.RowVersion++
		item.UpdatedAt = now
		if err := tx.Save(&deployment).Error; err != nil {
			return err
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		return support.Audit(tx, &deployment.CustomerID, command.Actor, "agent_store.verify", "agent_catalog_item", item.ID, &before, &deployment, providerResult.ErrorCode, command.RequestID)
	})
	if err != nil {
		s.rejectInterruptedVerification(state.item.ID, state.item.RowVersion)
		return nil, err
	}
	return s.AdminGet(command.ItemID)
}

func (s *Service) rejectInterruptedVerification(itemID string, expectedVersion int64) {
	_ = s.db.Model(&model.AgentCatalogItem{}).
		Where("id = ? AND status = ? AND row_version = ?", itemID, model.AgentCatalogStatusVerifying, expectedVersion).
		Updates(map[string]any{"status": model.AgentCatalogStatusRejected, "row_version": gorm.Expr("row_version + 1"), "updated_at": time.Now().UTC()}).Error
}

func (s *Service) Transition(command TransitionCommand) (*AdminItem, error) {
	command.Action = strings.ToLower(strings.TrimSpace(command.Action))
	if command.ItemID == "" || command.ExpectedVersion <= 0 {
		return nil, domain.Invalid("item_id and expected_version are required")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var item model.AgentCatalogItem
		if err := database.ForUpdate(tx).First(&item, "id = ?", command.ItemID).Error; err != nil {
			return domain.NotFound("Agent Store item not found")
		}
		if item.RowVersion != command.ExpectedVersion {
			return domain.Conflict("Agent Store item row version changed")
		}
		var deployment model.CustomerAgentDeployment
		if err := database.ForUpdate(tx).Where("item_id = ?", item.ID).First(&deployment).Error; err != nil {
			return err
		}
		before := item
		now := time.Now().UTC()
		switch command.Action {
		case "publish":
			if item.Status != model.AgentCatalogStatusVerified && item.Status != model.AgentCatalogStatusUnpublished && item.Status != model.AgentCatalogStatusPublished {
				return domain.Conflict("only a verified or unpublished item can be published")
			}
			if item.DraftVersionID != nil {
				item.CurrentVersionID = item.DraftVersionID
				item.DraftVersionID = nil
			}
			deploymentReady := deployment.Status == model.AgentDeploymentStatusVerified ||
				(item.Status == model.AgentCatalogStatusPublished && deployment.Status == model.AgentDeploymentStatusActive)
			if item.CurrentVersionID == nil || !deploymentReady {
				return domain.Conflict("verified catalog metadata and deployment are required")
			}
			if err := validateDeploymentSnapshot(tx, &deployment); err != nil {
				return err
			}
			item.Status = model.AgentCatalogStatusPublished
			deployment.Status = model.AgentDeploymentStatusActive
		case "unpublish":
			if item.Status != model.AgentCatalogStatusPublished {
				return domain.Conflict("only a published item can be unpublished")
			}
			item.Status = model.AgentCatalogStatusUnpublished
			deployment.Status = model.AgentDeploymentStatusVerified
		case "disable":
			if item.Status == model.AgentCatalogStatusArchived {
				return domain.Conflict("archived item cannot be disabled")
			}
			if strings.TrimSpace(command.Reason) == "" {
				return domain.Invalid("reason is required")
			}
			item.Status = model.AgentCatalogStatusDisabled
			deployment.Status = model.AgentDeploymentStatusDisabled
			deployment.ExecutionEnabled = false
			if err := tx.Model(&model.ControlSession{}).Where("customer_app_id = ? AND revoked_at IS NULL", deployment.CustomerAppID).Update("revoked_at", now).Error; err != nil {
				return err
			}
		case "archive":
			if item.Status != model.AgentCatalogStatusDraft && item.Status != model.AgentCatalogStatusRejected && item.Status != model.AgentCatalogStatusUnpublished {
				return domain.Conflict("item must be draft, rejected, or unpublished before archive")
			}
			item.Status = model.AgentCatalogStatusArchived
			item.ArchivedAt = &now
			deployment.Status = model.AgentDeploymentStatusDisabled
			deployment.ExecutionEnabled = false
		default:
			return domain.Invalid("unsupported Agent Store transition")
		}
		item.RowVersion++
		item.UpdatedAt = now
		deployment.RowVersion++
		deployment.UpdatedAt = now
		if err := tx.Save(&deployment).Error; err != nil {
			return err
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		return support.Audit(tx, &deployment.CustomerID, command.Actor, "agent_store."+command.Action, "agent_catalog_item", item.ID, &before, &item, command.Reason, command.RequestID)
	})
	if err != nil {
		return nil, err
	}
	return s.AdminGet(command.ItemID)
}

func (s *Service) AdminList(limit int) ([]AdminItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var items []model.AgentCatalogItem
	if err := s.db.Order("sort_order asc, id asc").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	result := make([]AdminItem, 0, len(items))
	for index := range items {
		item, err := s.AdminGet(items[index].ID)
		if err != nil {
			return nil, err
		}
		result = append(result, *item)
	}
	return result, nil
}

func (s *Service) AdminGet(itemID string) (*AdminItem, error) {
	var item model.AgentCatalogItem
	if err := s.db.First(&item, "id = ?", strings.TrimSpace(itemID)).Error; err != nil {
		return nil, domain.NotFound("Agent Store item not found")
	}
	var deployment model.CustomerAgentDeployment
	if err := s.db.Where("item_id = ?", item.ID).First(&deployment).Error; err != nil {
		return nil, err
	}
	var entitlements []model.AgentCatalogEntitlement
	if err := s.db.Where("deployment_id = ?", deployment.ID).Order("subject_type asc, subject_ref asc").Find(&entitlements).Error; err != nil {
		return nil, err
	}
	result := &AdminItem{ItemID: item.ID, Slug: item.Slug, Status: item.Status, RowVersion: item.RowVersion, SortOrder: item.SortOrder, Featured: item.Featured, Entitlements: entitlements}
	result.Deployment = projectDeployment(deployment)
	if item.CurrentVersionID != nil {
		version, err := s.projectVersion(*item.CurrentVersionID)
		if err != nil {
			return nil, err
		}
		result.CurrentVersion = version
	}
	if item.DraftVersionID != nil {
		version, err := s.projectVersion(*item.DraftVersionID)
		if err != nil {
			return nil, err
		}
		result.DraftVersion = version
	}
	return result, nil
}

func (s *Service) Audits(itemID string, limit int) (map[string]any, error) {
	var deployment model.CustomerAgentDeployment
	if err := s.db.Where("item_id = ?", itemID).First(&deployment).Error; err != nil {
		return nil, domain.NotFound("Agent Store item not found")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var adminAudits []model.AdminAudit
	if err := s.db.Where("resource_type = ? AND resource_id = ?", "agent_catalog_item", itemID).Order("id desc").Limit(limit).Find(&adminAudits).Error; err != nil {
		return nil, err
	}
	var launchAudits []model.AgentLaunchAudit
	if err := s.db.Where("deployment_id = ?", deployment.ID).Order("created_at desc").Limit(limit).Find(&launchAudits).Error; err != nil {
		return nil, err
	}
	return map[string]any{"admin_audits": adminAudits, "launch_audits": launchAudits}, nil
}

func (s *Service) Catalog(principal access.SessionPrincipal, cursor, category, query string) (*CatalogPage, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	query = strings.ToLower(strings.TrimSpace(query))
	queryDigest := sha256.Sum256([]byte(category + "\x00" + query))
	queryHash := hex.EncodeToString(queryDigest[:])
	lastSort := -1 << 30
	lastID := ""
	if cursor != "" {
		digest := sha256.Sum256([]byte(cursor))
		var stored model.AgentCatalogCursor
		if err := s.db.Where("token_hash = ?", hex.EncodeToString(digest[:])).First(&stored).Error; err != nil || !time.Now().UTC().Before(stored.ExpiresAt) || stored.ControlSessionID != principal.ControlSessionID || stored.CustomerID != principal.CustomerID || stored.NewAPIUserID != principal.NewAPIUserID || stored.QueryHash != queryHash {
			return nil, domain.Invalid("catalog cursor is invalid or stale")
		}
		lastSort, lastID = stored.LastSortOrder, stored.LastItemID
	}
	now := time.Now().UTC()
	var period model.PlanPeriod
	if err := s.db.Where("customer_id = ? AND status = ? AND payment_status = ? AND start_at <= ? AND end_at > ?", principal.CustomerID, model.PeriodStatusActive, model.PaymentStatusPaid, now, now).
		Order("start_at desc").First(&period).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return &CatalogPage{Items: []Card{}}, nil
		}
		return nil, err
	}
	var rows []catalogRow
	db := s.db.Table("claw_agent_catalog_items AS item").
		Select("item.id, item.slug, item.featured, item.sort_order, version.display_name, version.summary, version.description, version.avatar_url, version.category, version.tags_json, deployment.id AS deployment_id, deployment.provider_app_mode, deployment.runtime_profile, deployment.capabilities_json, deployment.execution_enabled, deployment.customer_app_id, deployment.verified_config_version_id, deployment.verified_config_version, deployment.verified_app_auth_epoch, customer_app.status AS customer_app_status, customer_app.current_config_version_id AS current_app_config_version_id, customer_app.auth_epoch AS current_app_auth_epoch").
		Joins("JOIN claw_agent_catalog_versions AS version ON version.id = item.current_version_id").
		Joins("JOIN claw_customer_agent_deployments AS deployment ON deployment.item_id = item.id").
		Joins("LEFT JOIN claw_customer_apps AS customer_app ON customer_app.id = deployment.customer_app_id").
		Where("item.status = ? AND deployment.status = ? AND deployment.customer_id = ?", model.AgentCatalogStatusPublished, model.AgentDeploymentStatusActive, principal.CustomerID).
		Where(`EXISTS (
			SELECT 1 FROM claw_agent_catalog_entitlements AS entitlement
			WHERE entitlement.deployment_id = deployment.id
			  AND entitlement.status = ?
			  AND entitlement.valid_from <= ?
			  AND (entitlement.valid_until IS NULL OR entitlement.valid_until > ?)
			  AND ((entitlement.subject_type = ? AND entitlement.subject_ref = ?)
			    OR (entitlement.subject_type = ? AND entitlement.subject_ref = ?)
			    OR (entitlement.subject_type = ? AND entitlement.subject_ref = ?)
			    OR (entitlement.subject_type = ? AND entitlement.subject_ref = ?))
		)`, model.AgentEntitlementStatusActive, now, now,
			"customer", strconv.FormatUint(principal.CustomerID, 10),
			"user", strconv.FormatInt(principal.NewAPIUserID, 10),
			"role", principal.Role,
			"plan", strconv.FormatUint(period.PlanVersionID, 10))
	if category != "" {
		db = db.Where("LOWER(version.category) = ?", category)
	}
	if query != "" {
		escaped := strings.ReplaceAll(query, "!", "!!")
		escaped = strings.ReplaceAll(strings.ReplaceAll(escaped, "%", "!%"), "_", "!_")
		like := "%" + escaped + "%"
		db = db.Where("LOWER(version.display_name) LIKE ? ESCAPE '!' OR LOWER(version.summary) LIKE ? ESCAPE '!'", like, like)
	}
	if lastID != "" {
		db = db.Where("item.sort_order > ? OR (item.sort_order = ? AND item.id > ?)", lastSort, lastSort, lastID)
	}
	if err := db.Order("item.sort_order asc, item.id asc").Limit(pageSize + 1).Scan(&rows).Error; err != nil {
		return nil, err
	}
	page := &CatalogPage{Items: []Card{}}
	for index := 0; index < len(rows) && index < pageSize; index++ {
		available := rows[index].ExecutionEnabled && s.snapshotAvailable(rows[index], now)
		page.Items = append(page.Items, projectCard(rows[index], false, available))
	}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		next, err := s.createCursor(principal, queryHash, last.SortOrder, last.ID, now)
		if err != nil {
			return nil, err
		}
		page.NextCursor = next
	}
	return page, nil
}

func (s *Service) Detail(principal access.SessionPrincipal, slug string) (*Card, error) {
	row, err := s.authorizedRow(principal, slug)
	if err != nil {
		return nil, err
	}
	return ptr(projectCard(*row, true, row.ExecutionEnabled && s.snapshotAvailable(*row, time.Now().UTC()))), nil
}

func (s *Service) Launch(principal access.SessionPrincipal, sessionToken, slug, requestID string) (*LaunchGrant, error) {
	row, err := s.authorizedRow(principal, slug)
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(launchTTL)
	sessionDigest := sha256.Sum256([]byte(strings.TrimSpace(sessionToken)))
	tokenDigest := sha256.Sum256([]byte(token))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var session model.ControlSession
		if err := database.ForUpdate(tx).Where("token_hash = ?", hex.EncodeToString(sessionDigest[:])).First(&session).Error; err != nil {
			return domain.Forbidden("control session is invalid")
		}
		if session.ID != principal.ControlSessionID || session.NewAPIUserID != principal.NewAPIUserID || session.CustomerID != principal.CustomerID || session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
			return domain.Forbidden("control session is stale")
		}
		var item model.AgentCatalogItem
		if err := database.ForUpdate(tx).Where("id = ? AND status = ?", row.ID, model.AgentCatalogStatusPublished).First(&item).Error; err != nil {
			return domain.NotFound("Agent Store item not found")
		}
		if item.CurrentVersionID == nil {
			return domain.Forbidden("Agent Store catalog version is unavailable")
		}
		var deployment model.CustomerAgentDeployment
		if err := database.ForUpdate(tx).Where("id = ? AND customer_id = ? AND status = ?", row.DeploymentID, principal.CustomerID, model.AgentDeploymentStatusActive).First(&deployment).Error; err != nil {
			return domain.NotFound("Agent Store item not found")
		}
		if !deployment.ExecutionEnabled || deployment.VerifiedConfigVersionID == nil {
			return domain.Forbidden("Agent Store item is temporarily unavailable")
		}
		allowed, err := authorizedWithDB(tx, deployment.ID, principal, now)
		if err != nil || !allowed {
			return domain.NotFound("Agent Store item not found")
		}
		var identity model.IdentityBinding
		if err := tx.First(&identity, principal.IdentityBindingID).Error; err != nil || identity.CustomerID != principal.CustomerID || identity.NewAPIUserID != principal.NewAPIUserID || identity.Status != model.IdentityStatusActive {
			return domain.Forbidden("identity binding is unavailable")
		}
		var member model.CustomerMember
		if err := tx.Where("customer_id = ? AND new_api_user_id = ?", principal.CustomerID, principal.NewAPIUserID).First(&member).Error; err != nil || !model.ActiveMembership(member) {
			return domain.Forbidden("customer membership is unavailable")
		}
		var app model.CustomerApp
		if err := tx.Where("id = ? AND customer_id = ?", deployment.CustomerAppID, principal.CustomerID).First(&app).Error; err != nil || app.Status != model.AppStatusActive || app.CurrentConfigVersionID == nil || *app.CurrentConfigVersionID != *deployment.VerifiedConfigVersionID || app.AuthEpoch != deployment.VerifiedAppAuthEpoch {
			return domain.Forbidden("customer App deployment is stale")
		}
		var config model.AppConfigVersion
		if err := tx.First(&config, *app.CurrentConfigVersionID).Error; err != nil || config.ConfigVersion != deployment.VerifiedConfigVersion {
			return domain.Forbidden("customer App configuration is stale")
		}
		if expiresAt.After(session.ExpiresAt) {
			expiresAt = session.ExpiresAt
		}
		if err := tx.Create(&model.ContextSelectionNonce{
			TokenHash: hex.EncodeToString(tokenDigest[:]), ControlSessionID: session.ID,
			NewAPIUserID: principal.NewAPIUserID, IdentityBindingID: identity.ID, CustomerMemberID: member.ID,
			CustomerID: principal.CustomerID, CustomerAppID: app.ID, AppConfigVersionID: config.ID,
			IdentityVersion: identity.IdentityVersion, IdentityAuthEpoch: identity.AuthEpoch,
			MemberAuthEpoch: member.AuthEpoch, AppAuthEpoch: app.AuthEpoch, ExpiresAt: expiresAt,
			Purpose: "agent_store_launch", AgentCatalogItemID: item.ID, AgentDeploymentID: deployment.ID,
			CatalogVersionID: *item.CurrentVersionID, CatalogRowVersion: item.RowVersion, DeploymentVersion: deployment.RowVersion,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgentLaunchAudit{
			ID: support.PublicID("agl"), DeploymentID: deployment.ID, CustomerID: principal.CustomerID,
			NewAPIUserID: principal.NewAPIUserID, Outcome: "authorized", RequestID: requestID, CreatedAt: now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &LaunchGrant{SelectionToken: token, DeploymentID: row.DeploymentID, CustomerAppID: row.CustomerAppID, ExpiresAt: expiresAt}, nil
}

func (s *Service) RecordLaunchOutcome(deploymentID string, principal access.SessionPrincipal, outcome, reason, requestID string) error {
	if outcome != "launched" && outcome != "failed" {
		return domain.Invalid("invalid launch outcome")
	}
	return s.db.Create(&model.AgentLaunchAudit{
		ID: support.PublicID("agl"), DeploymentID: deploymentID, CustomerID: principal.CustomerID,
		NewAPIUserID: principal.NewAPIUserID, Outcome: outcome, ReasonCode: reason,
		RequestID: requestID, CreatedAt: time.Now().UTC(),
	}).Error
}

type catalogRow struct {
	ID                        string
	Slug                      string
	Featured                  bool
	SortOrder                 int
	DisplayName               string
	Summary                   string
	Description               string
	AvatarURL                 string
	Category                  string
	TagsJSON                  string
	DeploymentID              string
	ProviderAppMode           int
	RuntimeProfile            string
	CapabilitiesJSON          string
	ExecutionEnabled          bool
	CustomerAppID             uint64
	VerifiedConfigVersionID   *uint64
	VerifiedConfigVersion     int64
	VerifiedAppAuthEpoch      int64
	CustomerAppStatus         string
	CurrentAppConfigVersionID *uint64
	CurrentAppAuthEpoch       int64
}

func (s *Service) authorizedRow(principal access.SessionPrincipal, slug string) (*catalogRow, error) {
	var row catalogRow
	err := s.db.Table("claw_agent_catalog_items AS item").
		Select("item.id, item.slug, item.featured, item.sort_order, version.display_name, version.summary, version.description, version.avatar_url, version.category, version.tags_json, deployment.id AS deployment_id, deployment.provider_app_mode, deployment.runtime_profile, deployment.capabilities_json, deployment.execution_enabled, deployment.customer_app_id, deployment.verified_config_version_id, deployment.verified_config_version, deployment.verified_app_auth_epoch, customer_app.status AS customer_app_status, customer_app.current_config_version_id AS current_app_config_version_id, customer_app.auth_epoch AS current_app_auth_epoch").
		Joins("JOIN claw_agent_catalog_versions AS version ON version.id = item.current_version_id").
		Joins("JOIN claw_customer_agent_deployments AS deployment ON deployment.item_id = item.id").
		Joins("LEFT JOIN claw_customer_apps AS customer_app ON customer_app.id = deployment.customer_app_id").
		Where("item.slug = ? AND item.status = ? AND deployment.status = ? AND deployment.customer_id = ?", strings.ToLower(strings.TrimSpace(slug)), model.AgentCatalogStatusPublished, model.AgentDeploymentStatusActive, principal.CustomerID).
		First(&row).Error
	if err != nil {
		return nil, domain.NotFound("Agent Store item not found")
	}
	allowed, err := s.authorized(row.DeploymentID, principal, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, domain.NotFound("Agent Store item not found")
	}
	return &row, nil
}

func (s *Service) authorized(deploymentID string, principal access.SessionPrincipal, now time.Time) (bool, error) {
	return authorizedWithDB(s.db, deploymentID, principal, now)
}

func authorizedWithDB(db *gorm.DB, deploymentID string, principal access.SessionPrincipal, now time.Time) (bool, error) {
	var entitlements []model.AgentCatalogEntitlement
	if err := db.Where("deployment_id = ? AND status = ? AND valid_from <= ? AND (valid_until IS NULL OR valid_until > ?)", deploymentID, model.AgentEntitlementStatusActive, now, now).Find(&entitlements).Error; err != nil {
		return false, err
	}
	var period model.PlanPeriod
	periodResult := db.Where("customer_id = ? AND status = ? AND payment_status = ? AND start_at <= ? AND end_at > ?", principal.CustomerID, model.PeriodStatusActive, model.PaymentStatusPaid, now, now).Order("start_at desc").First(&period)
	if periodResult.Error != nil {
		return false, nil
	}
	for _, entitlement := range entitlements {
		switch entitlement.SubjectType {
		case "customer":
			if entitlement.SubjectRef == strconv.FormatUint(principal.CustomerID, 10) {
				return true, nil
			}
		case "user":
			if entitlement.SubjectRef == strconv.FormatInt(principal.NewAPIUserID, 10) {
				return true, nil
			}
		case "role":
			if entitlement.SubjectRef == principal.Role {
				return true, nil
			}
		case "plan":
			if entitlement.SubjectRef == strconv.FormatUint(period.PlanVersionID, 10) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) snapshotAvailable(row catalogRow, now time.Time) bool {
	return row.VerifiedConfigVersionID != nil && row.CurrentAppConfigVersionID != nil && row.CustomerAppStatus == model.AppStatusActive &&
		*row.CurrentAppConfigVersionID == *row.VerifiedConfigVersionID && row.CurrentAppAuthEpoch == row.VerifiedAppAuthEpoch
}

func validateDeploymentSnapshot(db *gorm.DB, deployment *model.CustomerAgentDeployment) error {
	if deployment.VerifiedConfigVersionID == nil || deployment.ProviderAppMode < 1 || deployment.ProviderAppMode > 4 || deployment.RuntimeProfile == "" || deployment.VerifiedAt == nil {
		return domain.Conflict("deployment has no valid provider verification")
	}
	var app model.CustomerApp
	if err := db.Where("id = ? AND customer_id = ?", deployment.CustomerAppID, deployment.CustomerID).First(&app).Error; err != nil || app.Status != model.AppStatusActive || app.CurrentConfigVersionID == nil || *app.CurrentConfigVersionID != *deployment.VerifiedConfigVersionID || app.AuthEpoch != deployment.VerifiedAppAuthEpoch {
		return domain.Conflict("customer App changed after deployment verification")
	}
	var config model.AppConfigVersion
	if err := db.First(&config, *app.CurrentConfigVersionID).Error; err != nil || config.ConfigVersion != deployment.VerifiedConfigVersion || config.CredentialProfileID == nil {
		return domain.Conflict("customer App configuration changed after deployment verification")
	}
	var credential model.CredentialProfile
	if err := db.First(&credential, *config.CredentialProfileID).Error; err != nil || credential.Status != model.CredentialStatusActive {
		return domain.Conflict("deployment credential is unavailable")
	}
	if support.Hash(map[string]any{"app": config.AppKeyFingerprint, "credential": credential.Fingerprint}) != deployment.VerifiedCredentialHash {
		return domain.Conflict("deployment credential fingerprint changed")
	}
	return nil
}

func makeVersion(itemID, versionID string, generation int64, metadata Metadata, actor string, now time.Time) (model.AgentCatalogVersion, error) {
	tags, err := jsonx.Marshal(metadata.Tags)
	if err != nil {
		return model.AgentCatalogVersion{}, err
	}
	hash := support.Hash(metadata)
	return model.AgentCatalogVersion{
		ID: versionID, ItemID: itemID, Generation: generation, DisplayName: metadata.DisplayName,
		Summary: metadata.Summary, Description: metadata.Description, AvatarURL: metadata.AvatarURL,
		Category: metadata.Category, TagsJSON: string(tags), MetadataSHA256: hash,
		CreatedBy: actor, CreatedAt: now,
	}, nil
}

func normalizeMetadata(metadata Metadata) (Metadata, error) {
	metadata.DisplayName = strings.TrimSpace(metadata.DisplayName)
	metadata.Summary = strings.TrimSpace(metadata.Summary)
	metadata.Description = strings.TrimSpace(metadata.Description)
	metadata.AvatarURL = strings.TrimSpace(metadata.AvatarURL)
	metadata.Category = strings.ToLower(strings.TrimSpace(metadata.Category))
	if metadata.DisplayName == "" || len(metadata.DisplayName) > 160 || metadata.Summary == "" || len(metadata.Summary) > 500 || len(metadata.Description) > 20000 || metadata.Category == "" || len(metadata.Category) > 96 || len(metadata.AvatarURL) > 1024 {
		return Metadata{}, domain.Invalid("Agent Store display metadata is incomplete or exceeds its bounds")
	}
	if metadata.AvatarURL != "" {
		if err := validateAvatarURL(metadata.AvatarURL); err != nil {
			return Metadata{}, domain.Invalid("avatar_url must be an absolute HTTPS URL without userinfo or fragment")
		}
	}
	seen := map[string]struct{}{}
	tags := make([]string, 0, len(metadata.Tags))
	for _, value := range metadata.Tags {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > 64 {
			return Metadata{}, domain.Invalid("Agent Store tags must be non-empty and at most 64 characters")
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			tags = append(tags, value)
		}
	}
	if len(tags) > 32 {
		return Metadata{}, domain.Invalid("Agent Store accepts at most 32 tags")
	}
	sort.Strings(tags)
	metadata.Tags = tags
	return metadata, nil
}

func normalizeProviderSnapshot(result *providerverify.Result) error {
	result.DisplayName = strings.TrimSpace(result.DisplayName)
	result.Description = strings.TrimSpace(result.Description)
	result.AvatarURL = strings.TrimSpace(result.AvatarURL)
	if len(result.DisplayName) > 160 || len(result.Description) > 1024 || len(result.AvatarURL) > 1024 {
		return domain.Invalid("provider display metadata exceeds supported bounds")
	}
	return validateAvatarURL(result.AvatarURL)
}

func validateAvatarURL(value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return domain.Invalid("avatar URL is invalid")
	}
	return nil
}

func replaceEntitlements(tx *gorm.DB, deploymentID string, customerID uint64, inputs []EntitlementInput, now time.Time) error {
	if err := tx.Where("deployment_id = ?", deploymentID).Delete(&model.AgentCatalogEntitlement{}).Error; err != nil {
		return err
	}
	if len(inputs) == 0 {
		inputs = []EntitlementInput{{SubjectType: "customer", SubjectRef: strconv.FormatUint(customerID, 10)}}
	}
	seen := map[string]struct{}{}
	for _, input := range inputs {
		input.SubjectType = strings.ToLower(strings.TrimSpace(input.SubjectType))
		input.SubjectRef = strings.TrimSpace(input.SubjectRef)
		if input.SubjectType != "customer" && input.SubjectType != "user" && input.SubjectType != "role" && input.SubjectType != "plan" {
			return domain.Invalid("entitlement subject_type must be customer, user, role, or plan")
		}
		if input.SubjectRef == "" || len(input.SubjectRef) > 191 || (input.ValidUntil != nil && input.ValidFrom != nil && !input.ValidUntil.After(*input.ValidFrom)) {
			return domain.Invalid("entitlement subject and validity window are invalid")
		}
		if input.SubjectType == "customer" && input.SubjectRef != strconv.FormatUint(customerID, 10) {
			return domain.Invalid("customer entitlement must match the deployment customer")
		}
		key := input.SubjectType + "\x00" + input.SubjectRef
		if _, duplicate := seen[key]; duplicate {
			return domain.Invalid("duplicate entitlement subject")
		}
		seen[key] = struct{}{}
		validFrom := now
		if input.ValidFrom != nil {
			validFrom = input.ValidFrom.UTC()
		}
		entitlement := model.AgentCatalogEntitlement{
			ID: support.PublicID("age"), DeploymentID: deploymentID, SubjectType: input.SubjectType,
			SubjectRef: input.SubjectRef, Status: model.AgentEntitlementStatusActive,
			ValidFrom: validFrom, ValidUntil: input.ValidUntil, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&entitlement).Error; err != nil {
			return err
		}
	}
	return nil
}

func runtimeProfile(mode int, dynamic bool) string {
	switch mode {
	case 1:
		return "standard_v2"
	case 2:
		return "multi_agent_v2"
	case 3:
		return "workflow_v2"
	case 4:
		if dynamic {
			return "claw_dynamic_v2"
		}
		return "claw_static_v2"
	default:
		return ""
	}
}

func executionProfileAllowed(profile string) bool { return profile == "claw_dynamic_v2" }

func profileCapabilities(profile string) []string {
	switch profile {
	case "standard_v2":
		return []string{"chat", "history", "references"}
	case "multi_agent_v2":
		return []string{"chat", "history", "procedure_events"}
	case "workflow_v2":
		return []string{"chat", "history", "workflow_events"}
	case "claw_static_v2":
		return []string{"chat", "history"}
	case "claw_dynamic_v2":
		return []string{"chat", "history", "dynamic_agent"}
	default:
		return []string{}
	}
}

func projectDeployment(deployment model.CustomerAgentDeployment) AdminDeployment {
	capabilities := []string{}
	_ = jsonx.Unmarshal([]byte(deployment.CapabilitiesJSON), &capabilities)
	return AdminDeployment{
		DeploymentID: deployment.ID, CustomerID: deployment.CustomerID, CustomerAppID: deployment.CustomerAppID,
		Status: deployment.Status, RowVersion: deployment.RowVersion, ProviderAppMode: deployment.ProviderAppMode,
		RuntimeProfile: deployment.RuntimeProfile, DynamicAgentConfig: deployment.DynamicAgentConfig,
		ExecutionEnabled: deployment.ExecutionEnabled, VerifiedConfig: deployment.VerifiedConfigVersion,
		VerifiedAt: deployment.VerifiedAt, ProviderDisplayName: deployment.ProviderDisplayName,
		ProviderDescription: deployment.ProviderDescription, ProviderAvatarURL: deployment.ProviderAvatarURL,
		Capabilities: capabilities,
	}
}

func (s *Service) projectVersion(versionID string) (*AdminVersion, error) {
	var version model.AgentCatalogVersion
	if err := s.db.First(&version, "id = ?", versionID).Error; err != nil {
		return nil, err
	}
	tags := []string{}
	if err := jsonx.Unmarshal([]byte(version.TagsJSON), &tags); err != nil {
		return nil, err
	}
	return &AdminVersion{
		VersionID: version.ID, Generation: version.Generation, DisplayName: version.DisplayName,
		Summary: version.Summary, Description: version.Description, AvatarURL: version.AvatarURL,
		Category: version.Category, Tags: tags,
	}, nil
}

func projectCard(row catalogRow, detail, available bool) Card {
	tags := []string{}
	capabilities := []string{}
	_ = jsonx.Unmarshal([]byte(row.TagsJSON), &tags)
	_ = jsonx.Unmarshal([]byte(row.CapabilitiesJSON), &capabilities)
	card := Card{
		ID: row.ID, Slug: row.Slug, DisplayName: row.DisplayName, Summary: row.Summary,
		AvatarURL: row.AvatarURL, Category: row.Category, Tags: tags, Featured: row.Featured,
		AppMode: row.ProviderAppMode, RuntimeProfile: row.RuntimeProfile,
		Capabilities: capabilities, Available: available,
	}
	if detail {
		card.Description = row.Description
	}
	return card
}

func (s *Service) createCursor(principal access.SessionPrincipal, queryHash string, sortOrder int, itemID string, now time.Time) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(token))
	err = s.db.Create(&model.AgentCatalogCursor{
		TokenHash: hex.EncodeToString(digest[:]), ControlSessionID: principal.ControlSessionID,
		CustomerID: principal.CustomerID, NewAPIUserID: principal.NewAPIUserID, QueryHash: queryHash,
		LastSortOrder: sortOrder, LastItemID: itemID, ExpiresAt: now.Add(cursorTTL), CreatedAt: now,
	}).Error
	return token, err
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate Agent Store opaque token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func ptr[T any](value T) *T { return &value }
