package agentstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/app"
	credentialpkg "github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

type ListingConfig struct {
	ProviderEnvironment string
	Region              string
	SpaceID             string
	AppID               string
	TemplateAgentID     string
	CredentialProfileID uint64
	AppKey              string
	Limits              productpolicy.Limits
	Capabilities        []string
}

type PrepareListingCommand struct {
	Slug                string
	Metadata            Metadata
	SortOrder           int
	Featured            bool
	AudienceScope       string
	SelectedCustomerIDs []uint64
	Config              ListingConfig
	Actor               string
	RequestID           string
}

type ListingPreview struct {
	Item         *AdminItem             `json:"item,omitempty"`
	Verification *model.AppVerification `json:"verification"`
}

func (s *Service) PrepareListing(ctx context.Context, command PrepareListingCommand) (*ListingPreview, error) {
	command.AudienceScope = normalizeAudienceScope(command.AudienceScope)
	if command.AudienceScope == "" || command.Config.CredentialProfileID == 0 {
		return nil, domain.Invalid("audience_scope and credential_profile_id are required")
	}
	selected := append([]uint64(nil), command.SelectedCustomerIDs...)
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	selected = uniqueCustomerIDs(selected)
	if command.AudienceScope == model.AgentAudienceSelectedCustomers && len(selected) == 0 {
		return nil, domain.Invalid("at least one selected customer is required")
	}
	var credential model.CredentialProfile
	if err := s.db.First(&credential, command.Config.CredentialProfileID).Error; err != nil || credential.Status != model.CredentialStatusActive || credential.OwnerScope != credentialpkg.PlatformOwnerScope {
		return nil, domain.Conflict("an active platform-scoped credential is required")
	}
	if credential.ProviderEnvironment != strings.ToLower(strings.TrimSpace(command.Config.ProviderEnvironment)) {
		return nil, domain.Conflict("credential provider does not match the Agent Store application")
	}
	ownerID, err := s.catalogRuntimeOwner(selected)
	if err != nil {
		return nil, err
	}
	appService := app.New(s.db, false, s.resolver)
	created, err := appService.CreateCatalog(app.CreateCatalogCommand{
		Slug: command.Slug,
		Config: app.SaveConfigCommand{
			CustomerID: ownerID, ProviderEnvironment: command.Config.ProviderEnvironment,
			Region: command.Config.Region, SpaceID: command.Config.SpaceID, AppID: command.Config.AppID,
			TemplateAgentID: command.Config.TemplateAgentID, CredentialProfileID: &command.Config.CredentialProfileID,
			AppKey: command.Config.AppKey, DisplayName: command.Metadata.DisplayName,
			Limits: command.Config.Limits, Capabilities: command.Config.Capabilities,
			Actor: command.Actor, RequestID: command.RequestID,
		},
	})
	if err != nil {
		return nil, err
	}
	entitlements := make([]EntitlementInput, 0, len(selected))
	if command.AudienceScope == model.AgentAudienceAllCustomers {
		entitlements = append(entitlements, EntitlementInput{SubjectType: "all_customers", SubjectRef: "*"})
	} else {
		for _, customerID := range selected {
			entitlements = append(entitlements, EntitlementInput{SubjectType: "customer", SubjectRef: support.ResourceID(customerID)})
		}
	}
	item, err := s.Create(CreateCommand{
		Slug: command.Slug, Metadata: command.Metadata, SortOrder: command.SortOrder, Featured: command.Featured,
		CustomerID: ownerID, CustomerAppID: created.App.ID, AudienceScope: command.AudienceScope,
		Entitlements: entitlements, Actor: command.Actor, RequestID: command.RequestID,
	})
	if err != nil {
		return nil, err
	}
	verification, err := appService.VerifyPending(ctx, app.VerifyPendingCommand{
		CustomerID: ownerID, CustomerAppID: created.App.ID, ExpectedVersion: created.App.RowVersion,
		ConfigVersion: created.Version.ConfigVersion, Actor: command.Actor, RequestID: command.RequestID,
	}, s.resolver, s.verifier)
	if err != nil {
		_ = s.discardPreparedListing(item.ItemID, created.App.ID, created.Version.ID)
		return nil, err
	}
	if verification.Result != "verified" {
		if err := s.discardPreparedListing(item.ItemID, created.App.ID, created.Version.ID); err != nil {
			return nil, err
		}
		return &ListingPreview{Verification: verification}, nil
	}
	fresh, err := s.AdminGet(item.ItemID)
	if err != nil {
		return nil, err
	}
	if len(fresh.Deployments) != 1 {
		return nil, domain.Conflict("unified Agent Store listing must have exactly one deployment")
	}
	verified, err := s.Verify(ctx, TransitionCommand{
		ItemID: fresh.ItemID, DeploymentID: fresh.Deployments[0].DeploymentID,
		ExpectedVersion: fresh.RowVersion, ExpectedDeploymentVersion: fresh.Deployments[0].RowVersion,
		Action: "verify", Actor: command.Actor, RequestID: command.RequestID,
	})
	if err != nil {
		_ = s.discardPreparedListing(item.ItemID, created.App.ID, created.Version.ID)
		return nil, err
	}
	return &ListingPreview{Item: verified, Verification: verification}, nil
}

func (s *Service) discardPreparedListing(itemID string, appID, configID uint64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var item model.AgentCatalogItem
		if err := database.ForUpdate(tx).First(&item, "id = ?", itemID).Error; err != nil {
			return err
		}
		if item.Status == model.AgentCatalogStatusPublished || item.Status == model.AgentCatalogStatusArchived {
			return domain.Conflict("published Agent Store listing cannot be discarded")
		}
		var config model.AppConfigVersion
		if err := tx.First(&config, configID).Error; err != nil || config.CustomerAppID != appID {
			return domain.Conflict("prepared Agent Store configuration is unavailable")
		}
		var deploymentIDs []string
		if err := tx.Model(&model.CustomerAgentDeployment{}).Where("item_id = ? AND customer_app_id = ?", item.ID, appID).Pluck("id", &deploymentIDs).Error; err != nil {
			return err
		}
		if len(deploymentIDs) != 1 {
			return domain.Conflict("prepared Agent Store deployment is unavailable")
		}
		if err := tx.Where("deployment_id IN ?", deploymentIDs).Delete(&model.AgentCatalogEntitlement{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", deploymentIDs).Delete(&model.CustomerAgentDeployment{}).Error; err != nil {
			return err
		}
		if err := tx.Where("item_id = ?", item.ID).Delete(&model.AgentCatalogVersion{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&item).Error; err != nil {
			return err
		}
		if err := tx.Where("customer_app_id = ?", appID).Delete(&model.AppVerification{}).Error; err != nil {
			return err
		}
		if err := tx.Where("customer_app_id = ?", appID).Delete(&model.AppConfigVersion{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.CustomerApp{}, appID).Error; err != nil {
			return err
		}
		if secrets.ValidVaultReference(config.AppKeySecretRef) {
			now := time.Now().UTC()
			if err := tx.Model(&model.ProviderSecret{}).Where("public_id = ? AND revoked_at IS NULL", strings.TrimPrefix(config.AppKeySecretRef, "vault://")).Update("revoked_at", now).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) PublishListing(itemID string, expectedItemVersion, expectedDeploymentVersion int64, actor, requestID string) (*AdminItem, error) {
	if strings.TrimSpace(itemID) == "" || expectedItemVersion <= 0 || expectedDeploymentVersion <= 0 {
		return nil, domain.Invalid("item_id and expected versions are required")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var item model.AgentCatalogItem
		if err := database.ForUpdate(tx).First(&item, "id = ?", itemID).Error; err != nil {
			return domain.NotFound("Agent Store item not found")
		}
		if item.RowVersion != expectedItemVersion || item.Status != model.AgentCatalogStatusVerified || item.DraftVersionID == nil {
			return domain.Conflict("only the current verified listing can be published")
		}
		var deployments []model.CustomerAgentDeployment
		if err := database.ForUpdate(tx).Where("item_id = ?", item.ID).Limit(2).Find(&deployments).Error; err != nil {
			return err
		}
		if len(deployments) != 1 || deployments[0].RowVersion != expectedDeploymentVersion || deployments[0].Status != model.AgentDeploymentStatusVerified {
			return domain.Conflict("listing deployment is not ready for publication")
		}
		deployment := deployments[0]
		if !executionProfileAllowed(deployment.RuntimeProfile) || deployment.VerifiedConfigVersionID == nil {
			return domain.Conflict("verified runtime profile cannot execute")
		}
		var runtimeApp model.CustomerApp
		if err := database.ForUpdate(tx).Where("id = ? AND customer_id = ?", deployment.CustomerAppID, deployment.CustomerID).First(&runtimeApp).Error; err != nil || runtimeApp.Status != model.AppStatusVerified || runtimeApp.CurrentConfigVersionID == nil || *runtimeApp.CurrentConfigVersionID != *deployment.VerifiedConfigVersionID {
			return domain.Conflict("verified Agent Store application changed before publication")
		}
		var config model.AppConfigVersion
		if err := tx.First(&config, *runtimeApp.CurrentConfigVersionID).Error; err != nil || config.ConfigVersion != deployment.VerifiedConfigVersion {
			return domain.Conflict("verified Agent Store configuration changed before publication")
		}
		if config.CredentialProfileID == nil {
			return domain.Conflict("verified Agent Store credential is unavailable")
		}
		var credential model.CredentialProfile
		if err := database.ForUpdate(tx).First(&credential, *config.CredentialProfileID).Error; err != nil || credential.Status != model.CredentialStatusActive || credential.OwnerScope != credentialpkg.PlatformOwnerScope || support.Hash(map[string]any{"app": config.AppKeyFingerprint, "credential": credential.Fingerprint}) != deployment.VerifiedCredentialHash {
			return domain.Conflict("verified Agent Store credential changed before publication")
		}
		beforeItem, beforeDeployment, beforeApp := item, deployment, runtimeApp
		now := time.Now().UTC()
		runtimeApp.Status = model.AppStatusActive
		runtimeApp.EnabledAt = &now
		runtimeApp.AuthEpoch++
		runtimeApp.RowVersion++
		deployment.VerifiedAppAuthEpoch = runtimeApp.AuthEpoch
		deployment.ExecutionEnabled = true
		deployment.Status = model.AgentDeploymentStatusActive
		deployment.RowVersion++
		deployment.UpdatedAt = now
		item.CurrentVersionID = item.DraftVersionID
		item.DraftVersionID = nil
		item.Status = model.AgentCatalogStatusPublished
		item.RowVersion++
		item.UpdatedAt = now
		if err := tx.Save(&runtimeApp).Error; err != nil {
			return err
		}
		if err := tx.Save(&deployment).Error; err != nil {
			return err
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		if err := support.Enqueue(tx, &runtimeApp.CustomerID, "CACHE_INVALIDATE", "agent-store-publish:"+item.ID, map[string]any{
			"customer_app_id": runtimeApp.ID, "application_id": runtimeApp.AppID, "auth_epoch": runtimeApp.AuthEpoch,
		}); err != nil {
			return err
		}
		if err := support.Audit(tx, &runtimeApp.CustomerID, actor, "agent_store.publish_application", "customer_app", support.ResourceID(runtimeApp.ID), &beforeApp, &runtimeApp, "", requestID); err != nil {
			return err
		}
		if err := support.Audit(tx, &runtimeApp.CustomerID, actor, "agent_store.publish_deployment", "agent_catalog_item", item.ID, &beforeDeployment, &deployment, "", requestID); err != nil {
			return err
		}
		return support.Audit(tx, nil, actor, "agent_store.publish", "agent_catalog_item", item.ID, &beforeItem, &item, "", requestID)
	})
	if err != nil {
		return nil, err
	}
	return s.AdminGet(itemID)
}

func (s *Service) catalogRuntimeOwner(selected []uint64) (uint64, error) {
	if len(selected) > 0 {
		var count int64
		if err := s.db.Model(&model.Customer{}).Where("id IN ? AND status = ?", selected, model.CustomerStatusActive).Count(&count).Error; err != nil {
			return 0, err
		}
		if count != int64(len(selected)) {
			return 0, domain.Invalid("selected customers must all be active")
		}
		return selected[0], nil
	}
	var owner model.Customer
	if err := s.db.Where("status = ?", model.CustomerStatusActive).Order("id asc").First(&owner).Error; err != nil {
		return 0, domain.Conflict("at least one active customer is required before listing an application")
	}
	return owner.ID, nil
}

func uniqueCustomerIDs(values []uint64) []uint64 {
	result := values[:0]
	for _, value := range values {
		if value == 0 || len(result) > 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}
