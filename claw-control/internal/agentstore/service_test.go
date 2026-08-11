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
		Metadata:         agentstore.Metadata{DisplayName: "Data analyst", Summary: "Analyze business data", Description: "Detailed description", Category: "analytics", Tags: []string{"analytics", "data"}},
		ExecutionEnabled: true, Actor: "admin:1", RequestID: "update-1",
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
		Metadata:         agentstore.Metadata{DisplayName: "Data 100%_! analyst v2", Summary: "Analyze business data", Description: "Detailed description", AvatarURL: "https://cdn.example/avatar.png", Category: "analytics", Tags: []string{"analytics"}},
		ExecutionEnabled: true, Actor: "admin:1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgentCatalogStatusPublished, metadataOnly.Status)
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
			assert.False(t, verified.Deployment.ExecutionEnabled, "unaccepted runtime profiles must remain fail-closed")
		})
	}
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
