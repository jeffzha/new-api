package resourcebinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ResourceAccount      = "account"
	ResourceAgent        = "agent"
	ResourceConversation = "conversation"
	ResourceWorkspace    = "workspace"
	ResourceFile         = "file"
)

var allowedParentTypes = map[string]map[string]struct{}{
	ResourceAccount:      {"": {}},
	ResourceAgent:        {ResourceAccount: {}},
	ResourceConversation: {ResourceAgent: {}},
	ResourceWorkspace:    {ResourceConversation: {}},
	// A file uploaded before a Turn belongs to the shadow account. A file
	// confirmed inside a provider workspace belongs to that workspace.
	ResourceFile: {ResourceAccount: {}, ResourceWorkspace: {}},
}

type Service struct{ db *gorm.DB }

type BindCommand struct {
	BindingID          string
	CanonicalSubject   string
	CustomerID         uint64
	ApplicationID      string
	AppProfileID       uint64
	ConfigVersion      int64
	ResourceType       string
	ResourceID         string
	ParentResourceType string
	ParentResourceID   string
	SourceService      string
	SourceEventID      string
	SourceVersion      int64
}

type BindResult struct {
	ResourceBindingID  string `json:"resource_binding_id"`
	BindingID          string `json:"binding_id"`
	ResourceType       string `json:"resource_type"`
	ResourceID         string `json:"resource_id"`
	ParentResourceType string `json:"parent_resource_type,omitempty"`
	ParentResourceID   string `json:"parent_resource_id,omitempty"`
	SourceVersion      int64  `json:"source_version"`
	Status             string `json:"status"`
	Idempotent         bool   `json:"idempotent"`
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Bind(ctx context.Context, command BindCommand) (*BindResult, error) {
	command.BindingID = strings.TrimSpace(command.BindingID)
	command.CanonicalSubject = strings.TrimSpace(command.CanonicalSubject)
	command.ApplicationID = strings.TrimSpace(command.ApplicationID)
	command.ResourceType = strings.ToLower(strings.TrimSpace(command.ResourceType))
	command.ResourceID = strings.TrimSpace(command.ResourceID)
	command.ParentResourceType = strings.ToLower(strings.TrimSpace(command.ParentResourceType))
	command.ParentResourceID = strings.TrimSpace(command.ParentResourceID)
	command.SourceService = strings.TrimSpace(command.SourceService)
	command.SourceEventID = strings.TrimSpace(command.SourceEventID)
	if err := validateCommand(command); err != nil {
		return nil, err
	}

	resourceKeyHash := hashKey(command.ResourceType, command.ResourceID)
	sourceEventKeyHash := hashKey(command.SourceService, command.SourceEventID)
	payloadHash := hashKey(
		command.BindingID, command.CanonicalSubject, fmt.Sprint(command.CustomerID),
		command.ApplicationID, fmt.Sprint(command.AppProfileID), fmt.Sprint(command.ConfigVersion),
		command.ResourceType, command.ResourceID, command.ParentResourceType,
		command.ParentResourceID, command.SourceService, command.SourceEventID,
		fmt.Sprint(command.SourceVersion),
	)

	var result model.ResourceBinding
	idempotent := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_event_key_hash = ?", sourceEventKeyHash).First(&result).Error; err == nil {
			if result.SourcePayloadHash != payloadHash || result.ResourceKeyHash != resourceKeyHash {
				return domain.Conflict("source event was already used with different resource data")
			}
			idempotent = true
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		identity, appConfig, err := validateScope(tx, command)
		if err != nil {
			return err
		}

		var parentID *uint64
		if command.ParentResourceType != "" {
			var parent model.ResourceBinding
			if err := tx.Where("resource_key_hash = ?", hashKey(command.ParentResourceType, command.ParentResourceID)).First(&parent).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domain.NotFound("parent resource binding not found")
				}
				return err
			}
			if parent.Status != model.ResourceBindingStatusActive || parent.IdentityBindingID != identity.ID ||
				parent.CustomerID != command.CustomerID || parent.CustomerAppID != command.AppProfileID ||
				parent.AppConfigVersionID != appConfig.ID {
				return domain.Forbidden("parent resource belongs to a different identity, customer App, or config version")
			}
			parentID = &parent.ID
		}

		candidate := model.ResourceBinding{
			PublicID:        "wrb_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			ResourceKeyHash: resourceKeyHash, SourceEventKeyHash: sourceEventKeyHash,
			IdentityBindingID: identity.ID, CustomerID: command.CustomerID,
			CustomerAppID: command.AppProfileID, AppConfigVersionID: appConfig.ID,
			ResourceType: command.ResourceType, ResourceID: command.ResourceID,
			ParentResourceBindingID: parentID, ParentResourceType: command.ParentResourceType,
			ParentResourceID: command.ParentResourceID, SourceService: command.SourceService,
			SourceEventID: command.SourceEventID, SourceVersion: command.SourceVersion,
			SourcePayloadHash: payloadHash, Status: model.ResourceBindingStatusActive,
		}
		create := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if create.Error != nil {
			return create.Error
		}
		if create.RowsAffected == 1 {
			result = candidate
			return nil
		}

		if err := tx.Where("source_event_key_hash = ?", sourceEventKeyHash).First(&result).Error; err == nil {
			if result.SourcePayloadHash != payloadHash || result.ResourceKeyHash != resourceKeyHash {
				return domain.Conflict("source event was already used with different resource data")
			}
			idempotent = true
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Where("resource_key_hash = ?", resourceKeyHash).First(&result).Error; err != nil {
			return err
		}
		return domain.Conflict("resource is already bound; retry with the original source event")
	})
	if err != nil {
		return nil, err
	}
	return &BindResult{
		ResourceBindingID: result.PublicID, BindingID: command.BindingID,
		ResourceType: result.ResourceType, ResourceID: result.ResourceID,
		ParentResourceType: result.ParentResourceType, ParentResourceID: result.ParentResourceID,
		SourceVersion: result.SourceVersion, Status: result.Status, Idempotent: idempotent,
	}, nil
}

func validateCommand(command BindCommand) error {
	for name, item := range map[string]struct {
		value string
		max   int
	}{
		"binding_id": {command.BindingID, 64}, "canonical_subject": {command.CanonicalSubject, 191},
		"application_id": {command.ApplicationID, 128}, "resource_id": {command.ResourceID, 255},
		"source_service": {command.SourceService, 64}, "source_event_id": {command.SourceEventID, 128},
	} {
		if !validIdentifier(item.value, item.max) {
			return domain.Invalid("%s is required, must be at most %d characters, and cannot contain control characters", name, item.max)
		}
	}
	if command.CustomerID == 0 || command.AppProfileID == 0 || command.ConfigVersion <= 0 || command.SourceVersion <= 0 {
		return domain.Invalid("customer_id, app_profile_id, config_version, and source_version must be positive")
	}
	allowedParents, ok := allowedParentTypes[command.ResourceType]
	if !ok {
		return domain.Invalid("unsupported resource_type")
	}
	if _, ok := allowedParents[command.ParentResourceType]; !ok {
		return domain.Invalid("%s resource has an invalid parent_resource_type", command.ResourceType)
	}
	if command.ParentResourceType == "" {
		if command.ParentResourceID != "" {
			return domain.Invalid("account resource cannot have a parent_resource_id")
		}
		return nil
	}
	if !validIdentifier(command.ParentResourceID, 255) {
		return domain.Invalid("parent_resource_id is required, must be at most 255 characters, and cannot contain control characters")
	}
	return nil
}

func validateScope(tx *gorm.DB, command BindCommand) (*model.IdentityBinding, *model.AppConfigVersion, error) {
	var identity model.IdentityBinding
	if err := tx.Where("public_id = ?", command.BindingID).First(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, domain.NotFound("identity binding not found")
		}
		return nil, nil, err
	}
	if identity.Status != model.IdentityStatusActive || identity.CanonicalSubject != command.CanonicalSubject || identity.CustomerID != command.CustomerID {
		return nil, nil, domain.Forbidden("identity binding does not match the canonical subject and customer")
	}
	var member model.CustomerMember
	if err := tx.Where("customer_id = ? AND new_api_user_id = ?", identity.CustomerID, identity.NewAPIUserID).First(&member).Error; err != nil {
		return nil, nil, domain.Forbidden("active customer membership not found")
	}
	if !model.ActiveMembership(member) {
		return nil, nil, domain.Forbidden("customer membership is not active")
	}
	var customer model.Customer
	if err := tx.First(&customer, command.CustomerID).Error; err != nil || customer.Status != model.CustomerStatusActive {
		return nil, nil, domain.Forbidden("customer is not active")
	}
	var app model.CustomerApp
	if err := tx.First(&app, command.AppProfileID).Error; err != nil {
		return nil, nil, domain.Forbidden("customer App not found")
	}
	if app.CustomerID != command.CustomerID || app.AppID != command.ApplicationID || app.Status != model.AppStatusActive || app.CurrentConfigVersionID == nil {
		return nil, nil, domain.Forbidden("customer App does not match the active binding scope")
	}
	var appConfig model.AppConfigVersion
	if err := tx.First(&appConfig, *app.CurrentConfigVersionID).Error; err != nil {
		return nil, nil, domain.Forbidden("current App config version not found")
	}
	if appConfig.CustomerAppID != app.ID || appConfig.ConfigVersion != command.ConfigVersion || appConfig.Status != model.AppConfigStatusVerified {
		return nil, nil, domain.Forbidden("App config version does not match the active customer App")
	}
	return &identity, &appConfig, nil
}

func validIdentifier(value string, maxLength int) bool {
	if value == "" || len(value) > maxLength || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func hashKey(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(digest, "%d:", len(part))
		_, _ = digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}
