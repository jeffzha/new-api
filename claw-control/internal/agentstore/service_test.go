package agentstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/agentstore"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/providerverify"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type verifier struct{ result providerverify.Result }

func (v verifier) Verify(_ context.Context, _ providerverify.Target) (providerverify.Result, error) {
	return v.result, nil
}

func TestCatalogLifecycleAuthorizationAndLaunchSnapshot(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	fixture := createFixture(t, db)
	service := agentstore.New(db, fixture.resolver, verifier{result: providerverify.Result{
		Result: "verified", AppMode: 4, DynamicAgentConfig: true,
		ReleaseStatus: "published", TemplateAgentStatus: "available",
		DisplayName: "Trusted provider name", Description: "Provider description",
		ProviderRequestIDs:    []string{"provider-request-1", "provider-request-2"},
		SanitizedResponseHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}})

	created, err := service.Create(agentstore.CreateCommand{
		Slug: "data-analyst", Metadata: agentstore.Metadata{
			DisplayName: "Data analyst", Summary: "Analyze business data", Description: "Detailed description",
			AvatarURL: "https://cdn.example/avatar.png", Category: "analytics", Tags: []string{"Data", "analytics"},
		},
		CustomerID: fixture.customer.ID, CustomerAppID: fixture.app.ID, Actor: "admin:1", RequestID: "create-1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgentCatalogStatusDraft, created.Status)
	require.Len(t, created.Entitlements, 1)
	assert.Equal(t, "customer", created.Entitlements[0].SubjectType)

	verified, err := service.Verify(context.Background(), agentstore.TransitionCommand{
		ItemID: created.ItemID, ExpectedVersion: created.RowVersion, Actor: "admin:1", RequestID: "verify-1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgentCatalogStatusVerified, verified.Status)
	assert.Equal(t, 4, verified.Deployment.ProviderAppMode)
	assert.Equal(t, "claw_dynamic_v2", verified.Deployment.RuntimeProfile)
	assert.NotContains(t, fmt.Sprintf("%#v", verified.Deployment), "provider-request-1")

	updated, err := service.Update(agentstore.UpdateCommand{
		ItemID: verified.ItemID, ExpectedVersion: verified.RowVersion,
		Metadata: agentstore.Metadata{DisplayName: "Data analyst", Summary: "Analyze business data", Description: "Detailed description", Category: "analytics", Tags: []string{"analytics", "data"}},
		Actor:    "admin:1", RequestID: "update-1",
	})
	require.NoError(t, err)
	assert.False(t, updated.Deployment.ExecutionEnabled)
	updated, err = service.UpdateDeployment(agentstore.DeploymentCommand{
		ItemID: updated.ItemID, DeploymentID: updated.Deployment.DeploymentID,
		ExpectedDeploymentVersion: updated.Deployment.RowVersion, ExecutionEnabled: true, Actor: "admin:1",
	})
	require.NoError(t, err)
	assert.True(t, updated.Deployment.ExecutionEnabled)

	published, err := service.Transition(agentstore.TransitionCommand{
		ItemID: updated.ItemID, ExpectedVersion: updated.RowVersion, Action: "publish", Actor: "admin:1", RequestID: "publish-1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgentCatalogStatusPublished, published.Status)
	unpublished, err := service.Transition(agentstore.TransitionCommand{
		ItemID: published.ItemID, ExpectedVersion: published.RowVersion, Action: "unpublish", Actor: "admin:1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgentCatalogStatusUnpublished, unpublished.Status)
	assert.Equal(t, model.AgentDeploymentStatusVerified, unpublished.Deployment.Status)
	published, err = service.Transition(agentstore.TransitionCommand{
		ItemID: unpublished.ItemID, ExpectedVersion: unpublished.RowVersion, Action: "publish", Actor: "admin:1",
	})
	require.NoError(t, err)
	metadataOnly, err := service.Update(agentstore.UpdateCommand{
		ItemID: published.ItemID, ExpectedVersion: published.RowVersion,
		Metadata: agentstore.Metadata{DisplayName: "Data 100%_! analyst v2", Summary: "Analyze business data", Description: "Detailed description", AvatarURL: "https://cdn.example/avatar.png", Category: "analytics", Tags: []string{"analytics"}},
		Actor:    "admin:1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgentCatalogStatusPublished, metadataOnly.Status)
	assert.True(t, metadataOnly.Deployment.ExecutionEnabled, "metadata-only edits must preserve deployment execution gates")
	published, err = service.Transition(agentstore.TransitionCommand{
		ItemID: metadataOnly.ItemID, ExpectedVersion: metadataOnly.RowVersion, Action: "publish", Actor: "admin:1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), published.CurrentVersion.Generation)

	principal := access.SessionPrincipal{
		ControlSessionID: fixture.session.ID, CustomerID: fixture.customer.ID,
		NewAPIUserID: fixture.member.NewAPIUserID, IdentityBindingID: fixture.identity.ID,
		IdentityVersion: fixture.identity.IdentityVersion, Role: fixture.member.Role,
	}
	for index := 0; index < 25; index++ {
		itemID := fmt.Sprintf("agi_noise_%02d", index)
		versionID := fmt.Sprintf("agv_noise_%02d", index)
		deploymentID := fmt.Sprintf("agd_noise_%02d", index)
		require.NoError(t, db.Create(&model.AgentCatalogItem{
			ID: itemID, Slug: fmt.Sprintf("noise-agent-%02d", index), Status: model.AgentCatalogStatusPublished,
			CurrentVersionID: &versionID, SortOrder: -100 + index, RowVersion: 1, CreatedBy: "test",
		}).Error)
		require.NoError(t, db.Create(&model.AgentCatalogVersion{
			ID: versionID, ItemID: itemID, Generation: 1, DisplayName: "Noise", Summary: "analyst noise",
			Category: "analytics", TagsJSON: `[]`, MetadataSHA256: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		}).Error)
		require.NoError(t, db.Create(&model.CustomerAgentDeployment{
			ID: deploymentID, ItemID: itemID, CustomerID: fixture.customer.ID, CustomerAppID: uint64(1000 + index),
			Status: model.AgentDeploymentStatusActive, RowVersion: 1, ProviderAppMode: 4,
			RuntimeProfile: "claw_dynamic_v2", CapabilitiesJSON: `[]`, ProviderRequestIDsJSON: `[]`,
		}).Error)
		require.NoError(t, db.Create(&model.AgentCatalogEntitlement{
			ID: fmt.Sprintf("age_noise_%02d", index), DeploymentID: deploymentID, SubjectType: "user", SubjectRef: "999999",
			Status: model.AgentEntitlementStatusActive, ValidFrom: time.Now().UTC().Add(-time.Minute),
		}).Error)
	}
	plainItemID, plainVersionID, plainDeploymentID := "agi_plain", "agv_plain", "agd_plain"
	require.NoError(t, db.Create(&model.AgentCatalogItem{ID: plainItemID, Slug: "plain-agent", Status: model.AgentCatalogStatusPublished, CurrentVersionID: &plainVersionID, SortOrder: 10, RowVersion: 1, CreatedBy: "test"}).Error)
	require.NoError(t, db.Create(&model.AgentCatalogVersion{ID: plainVersionID, ItemID: plainItemID, Generation: 1, DisplayName: "Plain agent", Summary: "Ordinary", Category: "analytics", TagsJSON: `[]`, MetadataSHA256: "sha256:" + strings.Repeat("d", 64)}).Error)
	plainApp := fixture.app
	plainApp.ID, plainApp.AppID, plainApp.Slot, plainApp.CurrentConfigVersionID = 0, "plain-provider-app", "agent-store:plain", nil
	require.NoError(t, db.Create(&plainApp).Error)
	plainConfig := fixture.config
	plainConfig.ID, plainConfig.CustomerAppID = 0, plainApp.ID
	require.NoError(t, db.Create(&plainConfig).Error)
	plainApp.CurrentConfigVersionID = &plainConfig.ID
	require.NoError(t, db.Save(&plainApp).Error)
	configID := plainConfig.ID
	require.NoError(t, db.Create(&model.CustomerAgentDeployment{ID: plainDeploymentID, ItemID: plainItemID, CustomerID: fixture.customer.ID, CustomerAppID: plainApp.ID, VerifiedConfigVersionID: &configID, VerifiedConfigVersion: plainConfig.ConfigVersion, VerifiedAppAuthEpoch: plainApp.AuthEpoch, ProviderAppMode: 4, RuntimeProfile: "claw_dynamic_v2", ExecutionEnabled: true, CapabilitiesJSON: `[]`, ProviderRequestIDsJSON: `[]`, Status: model.AgentDeploymentStatusActive, RowVersion: 1}).Error)
	require.NoError(t, db.Create(&model.AgentCatalogEntitlement{ID: "age_plain", DeploymentID: plainDeploymentID, SubjectType: "customer", SubjectRef: fmt.Sprintf("%d", fixture.customer.ID), Status: model.AgentEntitlementStatusActive, ValidFrom: time.Now().UTC().Add(-time.Minute)}).Error)
	page, err := service.Catalog(principal, "", "analytics", "analyst")
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].Available)
	assert.Equal(t, "https://cdn.example/avatar.png", page.Items[0].AvatarURL)
	assert.Empty(t, page.Items[0].Description)
	for _, literal := range []string{"%", "_", "!"} {
		literalPage, literalErr := service.Catalog(principal, "", "analytics", literal)
		require.NoError(t, literalErr, literal)
		require.Len(t, literalPage.Items, 1, literal)
		assert.Equal(t, "data-analyst", literalPage.Items[0].Slug)
	}
	detail, err := service.Detail(principal, "data-analyst")
	require.NoError(t, err)
	assert.NotEmpty(t, detail.Description)

	grant, err := service.Launch(principal, fixture.sessionToken, "data-analyst", "launch-1")
	require.NoError(t, err)
	assert.NotEmpty(t, grant.SelectionToken)
	assert.True(t, grant.ExpiresAt.After(time.Now().UTC()))
	var nonce model.ContextSelectionNonce
	digest := sha256.Sum256([]byte(grant.SelectionToken))
	require.NoError(t, db.Where("token_hash = ?", hex.EncodeToString(digest[:])).First(&nonce).Error)
	assert.Equal(t, fixture.app.ID, nonce.CustomerAppID)
	assert.Equal(t, "agent_store_launch", nonce.Purpose)
	assert.Equal(t, published.ItemID, nonce.AgentCatalogItemID)
	assert.Equal(t, published.CurrentVersion.VersionID, nonce.CatalogVersionID)

	// Launch authorization is consumed in a second transaction by SelectContext.
	// The purpose-bound nonce must therefore fail closed if an administrator
	// unpublishes the catalog entry between those two atomic operations.
	unpublished, err = service.Transition(agentstore.TransitionCommand{
		ItemID: published.ItemID, ExpectedVersion: published.RowVersion, Action: "unpublish", Actor: "admin:1",
	})
	require.NoError(t, err)
	accessService := access.New(db, fixture.resolver, testutil.NewIdentityVerifier(fixture.identity.IdentityVersion), time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	_, err = accessService.SelectContext(context.Background(), fixture.sessionToken, grant.SelectionToken)
	assert.ErrorContains(t, err, "catalog selection is stale")
	var stillUnconsumed model.ContextSelectionNonce
	require.NoError(t, db.First(&stillUnconsumed, nonce.ID).Error)
	assert.Nil(t, stillUnconsumed.ConsumedAt)
	republished, err := service.Transition(agentstore.TransitionCommand{ItemID: unpublished.ItemID, ExpectedVersion: unpublished.RowVersion, Action: "publish", Actor: "admin:1"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.CustomerAgentDeployment{}).Where("id = ?", republished.Deployment.DeploymentID).Update("verified_config_version_id", nil).Error)
	_, err = service.Launch(principal, fixture.sessionToken, "data-analyst", "launch-corrupt")
	assert.ErrorContains(t, err, "temporarily unavailable")

	foreign := principal
	foreign.CustomerID++
	_, err = service.Detail(foreign, "data-analyst")
	assert.Error(t, err)
}

func TestCatalogAvatarRequiresSafeAbsoluteHTTPSURL(t *testing.T) {
	service := agentstore.New(nil, nil, nil)
	for _, value := range []string{"http://cdn.example/avatar.png", "javascript:alert(1)", "data:image/png;base64,AA", "/relative.png", "https://user:pass@cdn.example/avatar.png", "https://cdn.example/avatar.png#fragment"} {
		_, err := service.Create(agentstore.CreateCommand{
			Slug: "avatar-test", Metadata: agentstore.Metadata{DisplayName: "Avatar", Summary: "Summary", Category: "general", AvatarURL: value},
			CustomerID: 1, CustomerAppID: 1,
		})
		assert.Error(t, err, value)
	}
}

func TestOneCatalogItemSupportsIndependentCustomerDeploymentsAndEntitlements(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	fixture := createFixture(t, db)
	service := agentstore.New(db, fixture.resolver, verifier{result: providerverify.Result{
		Result: "verified", AppMode: 4, DynamicAgentConfig: true, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs: []string{"provider-multi"}, SanitizedResponseHash: "sha256:" + strings.Repeat("e", 64),
	}})
	created, err := service.Create(agentstore.CreateCommand{
		Slug: "shared-agent", Metadata: agentstore.Metadata{DisplayName: "Shared", Summary: "Shared agent", Category: "general"},
		CustomerID: fixture.customer.ID, CustomerAppID: fixture.app.ID, Actor: "admin:1",
	})
	require.NoError(t, err)

	secondCustomer := model.Customer{CustomerCode: "agent-store-second", DisplayName: "Second", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&secondCustomer).Error)
	var fixturePeriod model.PlanPeriod
	require.NoError(t, db.Where("customer_id = ?", fixture.customer.ID).First(&fixturePeriod).Error)
	fixturePeriod.ID = 0
	fixturePeriod.CustomerID = secondCustomer.ID
	require.NoError(t, db.Create(&fixturePeriod).Error)
	secondApp := model.CustomerApp{
		CustomerID: secondCustomer.ID, Slot: "primary", Selector: "aps_second", Alias: "primary",
		ProviderEnvironment: fixture.app.ProviderEnvironment, AppID: "provider-app-second", DisplayName: "Second App",
		Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&secondApp).Error)
	var secondCredential model.CredentialProfile
	require.NoError(t, db.First(&secondCredential, *fixture.config.CredentialProfileID).Error)
	secondCredential.ID = 0
	secondCredential.OwnerScope = fmt.Sprintf("customer:%d", secondCustomer.ID)
	secondCredential.CustomerID = &secondCustomer.ID
	secondCredential.Name = "provider-second"
	require.NoError(t, db.Create(&secondCredential).Error)
	secondConfig := fixture.config
	secondConfig.ID = 0
	secondConfig.CustomerAppID = secondApp.ID
	secondConfig.ConfigVersion = 1
	secondConfig.CredentialProfileID = &secondCredential.ID
	require.NoError(t, db.Create(&secondConfig).Error)
	secondApp.CurrentConfigVersionID = &secondConfig.ID
	require.NoError(t, db.Save(&secondApp).Error)

	withSecond, err := service.AddDeployment(agentstore.DeploymentCommand{
		ItemID: created.ItemID, ExpectedItemVersion: created.RowVersion,
		CustomerID: secondCustomer.ID, CustomerAppID: secondApp.ID,
		Entitlements: []agentstore.EntitlementInput{{SubjectType: "user", SubjectRef: "222"}}, Actor: "admin:1",
	})
	require.NoError(t, err)
	require.Len(t, withSecond.Deployments, 2)
	secondDeployment := withSecond.Deployments[1]
	require.Len(t, secondDeployment.Entitlements, 1)
	assert.Equal(t, "222", secondDeployment.Entitlements[0].SubjectRef)
	_, err = service.UpdateDeployment(agentstore.DeploymentCommand{
		ItemID: withSecond.ItemID, DeploymentID: withSecond.Deployments[0].DeploymentID,
		ExpectedDeploymentVersion: withSecond.Deployments[0].RowVersion, ExecutionEnabled: true, Actor: "admin:1",
	})
	assert.ErrorContains(t, err, "verified or active", "draft deployments must never retain an enabled execution gate")
	_, err = service.Verify(context.Background(), agentstore.TransitionCommand{
		ItemID: withSecond.ItemID, ExpectedVersion: withSecond.RowVersion, Actor: "admin:1",
	})
	assert.ErrorContains(t, err, "deployment_id is required", "legacy item-level verification must be unambiguous")

	firstVerified, err := service.Verify(context.Background(), agentstore.TransitionCommand{
		ItemID: withSecond.ItemID, DeploymentID: withSecond.Deployments[0].DeploymentID,
		ExpectedVersion: withSecond.RowVersion, ExpectedDeploymentVersion: withSecond.Deployments[0].RowVersion, Actor: "admin:1",
	})
	require.NoError(t, err)
	secondVerified, err := service.Verify(context.Background(), agentstore.TransitionCommand{
		ItemID: firstVerified.ItemID, DeploymentID: secondDeployment.DeploymentID,
		ExpectedVersion: firstVerified.RowVersion, ExpectedDeploymentVersion: secondDeployment.RowVersion, Actor: "admin:1",
	})
	require.NoError(t, err)
	published, err := service.Transition(agentstore.TransitionCommand{
		ItemID: secondVerified.ItemID, ExpectedVersion: secondVerified.RowVersion, Action: "publish", Actor: "admin:1",
	})
	require.NoError(t, err)
	require.Len(t, published.Deployments, 2)
	assert.Equal(t, model.AgentDeploymentStatusActive, published.Deployments[0].Status)
	assert.Equal(t, model.AgentDeploymentStatusActive, published.Deployments[1].Status)
	for index := range published.Deployments {
		published, err = service.UpdateDeployment(agentstore.DeploymentCommand{
			ItemID: published.ItemID, DeploymentID: published.Deployments[index].DeploymentID,
			ExpectedDeploymentVersion: published.Deployments[index].RowVersion, ExecutionEnabled: true, Actor: "admin:1",
		})
		require.NoError(t, err)
	}
	beforeMetadataDeployments := append([]agentstore.AdminDeployment(nil), published.Deployments...)
	metadataUpdated, err := service.Update(agentstore.UpdateCommand{
		ItemID: published.ItemID, ExpectedVersion: published.RowVersion,
		Metadata: agentstore.Metadata{DisplayName: "Shared updated", Summary: "Shared agent", Category: "general"},
		Actor:    "admin:1",
	})
	require.NoError(t, err)
	for index := range metadataUpdated.Deployments {
		assert.Equal(t, beforeMetadataDeployments[index].RowVersion, metadataUpdated.Deployments[index].RowVersion)
		assert.Equal(t, beforeMetadataDeployments[index].ExecutionEnabled, metadataUpdated.Deployments[index].ExecutionEnabled)
		assert.Equal(t, beforeMetadataDeployments[index].Entitlements, metadataUpdated.Deployments[index].Entitlements)
	}
	published = metadataUpdated
	auditNow := time.Now().UTC()
	require.NoError(t, db.Create(&model.AgentLaunchAudit{
		ID: "ala_first", DeploymentID: published.Deployments[0].DeploymentID,
		CustomerID: published.Deployments[0].CustomerID, NewAPIUserID: 101, Outcome: "launched", CreatedAt: auditNow,
	}).Error)
	require.NoError(t, db.Create(&model.AgentLaunchAudit{
		ID: "ala_second", DeploymentID: published.Deployments[1].DeploymentID,
		CustomerID: published.Deployments[1].CustomerID, NewAPIUserID: 222, Outcome: "launched", CreatedAt: auditNow.Add(-time.Second),
	}).Error)
	require.NoError(t, db.Create(&model.AgentLaunchAudit{
		ID: "ala_foreign", DeploymentID: "agd_foreign", CustomerID: 999, NewAPIUserID: 999,
		Outcome: "launched", CreatedAt: auditNow.Add(time.Second),
	}).Error)
	position := agentstore.AuditPosition{}
	launchDeploymentIDs := map[string]bool{}
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		audits, auditErr := service.AuditsPage(published.ItemID, 1, position)
		require.NoError(t, auditErr)
		for _, launch := range audits.LaunchAudits {
			launchDeploymentIDs[launch.DeploymentID] = true
		}
		if audits.Next == nil {
			break
		}
		position = *audits.Next
	}
	assert.True(t, launchDeploymentIDs[published.Deployments[0].DeploymentID])
	assert.True(t, launchDeploymentIDs[published.Deployments[1].DeploymentID])
	assert.False(t, launchDeploymentIDs["agd_foreign"])

	firstPrincipal := access.SessionPrincipal{CustomerID: fixture.customer.ID, NewAPIUserID: fixture.member.NewAPIUserID, Role: fixture.member.Role}
	firstPage, err := service.Catalog(firstPrincipal, "", "", "")
	require.NoError(t, err)
	require.Len(t, firstPage.Items, 1)
	secondPrincipal := access.SessionPrincipal{CustomerID: secondCustomer.ID, NewAPIUserID: 222, Role: "member"}
	secondPage, err := service.Catalog(secondPrincipal, "", "", "")
	require.NoError(t, err)
	require.Len(t, secondPage.Items, 1)

	disabled, err := service.DisableDeployment(agentstore.DeploymentCommand{
		ItemID: published.ItemID, DeploymentID: published.Deployments[1].DeploymentID,
		ExpectedDeploymentVersion: published.Deployments[1].RowVersion, Reason: "customer offboarding", Actor: "admin:1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgentDeploymentStatusActive, disabled.Deployments[0].Status)
	assert.Equal(t, model.AgentDeploymentStatusDisabled, disabled.Deployments[1].Status)
	firstPage, err = service.Catalog(firstPrincipal, "", "", "")
	require.NoError(t, err)
	require.Len(t, firstPage.Items, 1)
	secondPage, err = service.Catalog(secondPrincipal, "", "", "")
	require.NoError(t, err)
	assert.Empty(t, secondPage.Items)
}

func TestVerificationRecoversOnlyStaleWorkAndRejectsUnsafeProviderMetadata(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	fixture := createFixture(t, db)
	service := agentstore.New(db, fixture.resolver, verifier{result: providerverify.Result{
		Result: "verified", AppMode: 4, DynamicAgentConfig: true, ReleaseStatus: "published", TemplateAgentStatus: "available",
		DisplayName: strings.Repeat("x", 161), AvatarURL: "javascript:alert(1)",
		ProviderRequestIDs: []string{"provider-request"}, SanitizedResponseHash: "sha256:" + strings.Repeat("a", 64),
	}})
	created, err := service.Create(agentstore.CreateCommand{
		Slug: "stale-verification", Metadata: agentstore.Metadata{DisplayName: "Stale", Summary: "Summary", Category: "general"},
		CustomerID: fixture.customer.ID, CustomerAppID: fixture.app.ID, Actor: "admin:1",
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.AgentCatalogItem{}).Where("id = ?", created.ItemID).Updates(map[string]any{
		"status": model.AgentCatalogStatusVerifying, "updated_at": time.Now().UTC(),
	}).Error)
	_, err = service.Verify(context.Background(), agentstore.TransitionCommand{ItemID: created.ItemID, ExpectedVersion: created.RowVersion, Actor: "admin:1"})
	assert.ErrorContains(t, err, "already in progress")

	require.NoError(t, db.Model(&model.AgentCatalogItem{}).Where("id = ?", created.ItemID).Update("updated_at", time.Now().UTC().Add(-10*time.Minute)).Error)
	recovered, err := service.Verify(context.Background(), agentstore.TransitionCommand{ItemID: created.ItemID, ExpectedVersion: created.RowVersion, Actor: "admin:1"})
	require.NoError(t, err)
	assert.Equal(t, model.AgentCatalogStatusRejected, recovered.Status)
	assert.Equal(t, model.AgentDeploymentStatusDraft, recovered.Deployment.Status)
	assert.Empty(t, recovered.Deployment.ProviderDisplayName)
	assert.Empty(t, recovered.Deployment.ProviderAvatarURL)
	var stored model.AgentCatalogItem
	require.NoError(t, db.First(&stored, "id = ?", created.ItemID).Error)
	assert.NotEqual(t, model.AgentCatalogStatusVerifying, stored.Status)
}

func TestProviderModePolicyDoesNotTrustAdministratorOrRequireNonClawTemplate(t *testing.T) {
	for mode, profile := range map[int]string{1: "standard_v2", 2: "multi_agent_v2", 3: "workflow_v2", 4: "claw_static_v2"} {
		t.Run(profile, func(t *testing.T) {
			db, err := testutil.NewDatabase()
			require.NoError(t, err)
			fixture := createFixture(t, db)
			fixture.config.TemplateAgentID = ""
			require.NoError(t, db.Save(&fixture.config).Error)
			service := agentstore.New(db, fixture.resolver, verifier{result: providerverify.Result{
				Result: "verified", AppMode: mode, ReleaseStatus: "published", TemplateAgentStatus: "not_required",
				ProviderRequestIDs:    []string{"provider-request"},
				SanitizedResponseHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			}})
			created, err := service.Create(agentstore.CreateCommand{
				Slug: "mode-application", Metadata: agentstore.Metadata{DisplayName: "Application", Summary: "Summary", Category: "general"},
				CustomerID: fixture.customer.ID, CustomerAppID: fixture.app.ID, ExecutionEnabled: true,
			})
			require.NoError(t, err)
			verified, err := service.Verify(context.Background(), agentstore.TransitionCommand{ItemID: created.ItemID, ExpectedVersion: created.RowVersion})
			require.NoError(t, err)
			assert.Equal(t, profile, verified.Deployment.RuntimeProfile)
			assert.False(t, verified.Deployment.ExecutionEnabled, "verification never enables execution implicitly")
			enabled, err := service.UpdateDeployment(agentstore.DeploymentCommand{
				ItemID: verified.ItemID, DeploymentID: verified.Deployment.DeploymentID,
				ExpectedDeploymentVersion: verified.Deployment.RowVersion, ExecutionEnabled: true,
			})
			require.NoError(t, err)
			assert.True(t, enabled.Deployment.ExecutionEnabled)
		})
	}
}

func TestUnknownRuntimeProfileCannotEnableExecution(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	fixture := createFixture(t, db)
	service := agentstore.New(db, fixture.resolver, verifier{result: providerverify.Result{
		Result: "verified", AppMode: 4, DynamicAgentConfig: true, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs: []string{"provider-request"}, SanitizedResponseHash: "sha256:" + strings.Repeat("b", 64),
	}})
	created, err := service.Create(agentstore.CreateCommand{
		Slug: "unknown-runtime", Metadata: agentstore.Metadata{DisplayName: "Unknown", Summary: "Summary", Category: "general"},
		CustomerID: fixture.customer.ID, CustomerAppID: fixture.app.ID,
	})
	require.NoError(t, err)
	verified, err := service.Verify(context.Background(), agentstore.TransitionCommand{ItemID: created.ItemID, ExpectedVersion: created.RowVersion})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.CustomerAgentDeployment{}).Where("id = ?", verified.Deployment.DeploymentID).Update("runtime_profile", "unknown_v2").Error)
	_, err = service.UpdateDeployment(agentstore.DeploymentCommand{
		ItemID: verified.ItemID, DeploymentID: verified.Deployment.DeploymentID,
		ExpectedDeploymentVersion: verified.Deployment.RowVersion, ExecutionEnabled: true,
	})
	assert.ErrorContains(t, err, "does not allow execution")
}

type fixture struct {
	resolver     testutil.SecretResolver
	customer     model.Customer
	member       model.CustomerMember
	identity     model.IdentityBinding
	app          model.CustomerApp
	config       model.AppConfigVersion
	session      model.ControlSession
	sessionToken string
}

func createFixture(t *testing.T, db *gorm.DB) fixture {
	t.Helper()
	now := time.Now().UTC()
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_APP_KEY":    "app-key",
		"env://WORKBENCH_PROVIDER_SECRET_ID":  "secret-id",
		"env://WORKBENCH_PROVIDER_SECRET_KEY": "secret-key",
	}
	customer := model.Customer{CustomerCode: "customer-a", DisplayName: "Customer A", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	member := model.CustomerMember{CustomerID: customer.ID, NewAPIUserID: 101, Role: "member", Status: model.MemberStatusActive, MembershipSlot: model.MembershipSlot(customer.ID), AuthEpoch: 1}
	require.NoError(t, db.Create(&member).Error)
	identity := model.IdentityBinding{PublicID: "binding-a", CustomerID: customer.ID, NewAPIUserID: member.NewAPIUserID, CanonicalSubject: "customer-a:user:101", IdentityVersion: "identity-v1", Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1}
	require.NoError(t, db.Create(&identity).Error)
	credential := model.CredentialProfile{
		OwnerScope: fmt.Sprintf("customer:%d", customer.ID), CustomerID: &customer.ID,
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "provider", Status: model.CredentialStatusActive,
		SecretIDRef: "env://WORKBENCH_PROVIDER_SECRET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_SECRET_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("secret-id", "secret-key"), FingerprintVersion: secrets.CanonicalFingerprintVersion,
		Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&credential).Error)
	app := model.CustomerApp{CustomerID: customer.ID, Slot: "primary", Selector: "aps_fixture", Alias: "primary", ProviderEnvironment: model.ProviderChinaTencentCloud, AppID: "provider-app", DisplayName: "Provider app", Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1}
	require.NoError(t, db.Create(&app).Error)
	config := model.AppConfigVersion{
		CustomerAppID: app.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space-a", TemplateAgentID: "agent-a", CredentialProfileID: &credential.ID,
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_APP_KEY", AppKeyFingerprint: secrets.AppKeyFingerprint("app-key"), AppKeyFingerprintVersion: secrets.CanonicalFingerprintVersion,
		RowVersion: 1, LimitsJSON: `{}`, CapabilitiesJSON: `["chat"]`, CreatedBy: "admin:1", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&config).Error)
	app.CurrentConfigVersionID = &config.ID
	require.NoError(t, db.Save(&app).Error)
	planVersion := model.PlanVersion{PlanID: 1, Version: 1, Name: "Plan", MonthlyPriceCNY: "100", Currency: "CNY", CapabilitiesJSON: `["chat"]`, LimitsJSON: `{}`, Status: model.PlanStatusPublished, ValidFrom: now.Add(-time.Hour), PublishedBy: "admin", PublishedAt: now}
	require.NoError(t, db.Create(&planVersion).Error)
	period := model.PlanPeriod{CustomerID: customer.ID, PlanVersionID: planVersion.ID, StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), AmountCNY: "100", PaymentMode: model.PaymentModeOfflineManual, PaymentStatus: model.PaymentStatusPaid, Status: model.PeriodStatusActive, SnapshotJSON: `{}`, RowVersion: 1}
	require.NoError(t, db.Create(&period).Error)
	sessionToken := "control-session-token"
	sessionHash := sha256.Sum256([]byte(sessionToken))
	session := model.ControlSession{
		TokenHash: hex.EncodeToString(sessionHash[:]), SelectionState: model.ControlSessionStateSelected,
		IdentityBindingID: identity.ID, CustomerID: customer.ID, NewAPIUserID: member.NewAPIUserID,
		CustomerAppID: app.ID, AppConfigVersionID: config.ID, AccessMode: "active", AuthEpoch: 1,
		IdentityVersion: identity.IdentityVersion, ExpiresAt: now.Add(time.Hour), LastSeenAt: now,
	}
	require.NoError(t, db.Create(&session).Error)
	return fixture{resolver: resolver, customer: customer, member: member, identity: identity, app: app, config: config, session: session, sessionToken: sessionToken}
}
