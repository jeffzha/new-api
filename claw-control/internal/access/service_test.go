package access_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/identity"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/plan"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type acceptingIdentityVerifier struct{}

func (acceptingIdentityVerifier) Verify(_ context.Context, _ int64, _ string) error      { return nil }
func (acceptingIdentityVerifier) VerifyFresh(_ context.Context, _ int64, _ string) error { return nil }
func (acceptingIdentityVerifier) VerifyAdmin(_ context.Context, _ int64, _ string) error { return nil }

type rejectingIdentityVerifier struct{}

func (rejectingIdentityVerifier) Verify(_ context.Context, _ int64, _ string) error {
	return errors.New("new-api unavailable")
}
func (rejectingIdentityVerifier) VerifyFresh(_ context.Context, _ int64, _ string) error {
	return errors.New("new-api unavailable")
}
func (rejectingIdentityVerifier) VerifyAdmin(_ context.Context, _ int64, _ string) error {
	return errors.New("new-api unavailable")
}

func TestAppLifecycleTicketAndInternalContext(t *testing.T) {
	t.Setenv("WORKBENCH_PROVIDER_TEST_ADP_APP_KEY", "app-key-secret")
	t.Setenv("WORKBENCH_PROVIDER_TEST_TENCENT_SECRET_ID", "secret-id")
	t.Setenv("WORKBENCH_PROVIDER_TEST_TENCENT_SECRET_KEY", "secret-key")
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	identityVerifier := testutil.NewIdentityVerifier("v1.identity-fingerprint")
	customerService := customer.New(db, "prod", identityVerifier)
	identityService := identity.New(db)
	providerResolver := secrets.EnvironmentResolver{}
	credentialService := credential.New(db, providerResolver)
	appService := app.New(db, false, providerResolver)
	planService := plan.New(db)
	accessService := access.New(
		db, secrets.EnvironmentResolver{}, acceptingIdentityVerifier{},
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)

	createdCustomer, err := customerService.Create(customer.CreateCommand{
		CustomerCode: "access-test", DisplayName: "Access Test", Actor: "test",
	})
	require.NoError(t, err)
	membership, err := customerService.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: createdCustomer.ID, NewAPIUserID: 42, Role: "owner", Actor: "test",
	})
	require.NoError(t, err)
	credentialProfile, err := credentialService.Create(credential.CreateCommand{
		ProviderEnvironment: "china_tencent_cloud", Name: "primary",
		SecretIDRef: "env://WORKBENCH_PROVIDER_TEST_TENCENT_SECRET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_TEST_TENCENT_SECRET_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("secret-id", "secret-key"), Actor: "test",
	})
	require.NoError(t, err)
	saved, err := appService.SaveConfig(app.SaveConfigCommand{
		CustomerID: createdCustomer.ID, ProviderEnvironment: "china_tencent_cloud",
		Region: "ap-guangzhou", SpaceID: "space-1", AppID: "app-1", TemplateAgentID: "agent-1",
		CredentialProfileID: &credentialProfile.ID, AppKeySecretRef: "env://WORKBENCH_PROVIDER_TEST_ADP_APP_KEY",
		AppKeyFingerprint: secrets.AppKeyFingerprint("app-key-secret"), DisplayName: "Customer Claw",
		Limits: app.Limits{CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 600,
			MaxReasoningRounds: 20, MaxOutputTokens: 8192, WebSearchPerTurn: 5, MaxFileBytes: 10_000_000},
		Capabilities: []string{"chat", "files"}, Actor: "test",
	})
	require.NoError(t, err)
	_, err = appService.RecordVerification(app.RecordVerificationCommand{
		CustomerID: createdCustomer.ID, ExpectedVersion: saved.App.RowVersion,
		ConfigVersion: saved.Version.ConfigVersion, Result: "verified", AppMode: 4,
		ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs:    []string{"provider-request-1"},
		SanitizedResponseHash: "sha256:" + strings.Repeat("a", 64), Actor: "verifier",
	})
	require.NoError(t, err)
	var stableApp model.CustomerApp
	require.NoError(t, db.Where("customer_id = ?", createdCustomer.ID).First(&stableApp).Error)
	_, err = appService.Transition(app.TransitionCommand{
		CustomerID: createdCustomer.ID, ExpectedVersion: stableApp.RowVersion, Action: "enable", Actor: "test",
	})
	assert.Error(t, err, "App cannot be enabled before an active paid fixed plan exists")

	now := time.Now().UTC()
	published, err := planService.Publish(plan.PublishCommand{
		PlanCode: "access-plan", DisplayName: "Access Plan", MonthlyPriceCNY: "88.00",
		Capabilities: []string{"chat"}, Limits: app.Limits{
			CustomerConcurrency: 1, UserConcurrency: 1, MaxRuntimeSeconds: 300,
			MaxReasoningRounds: 10, MaxOutputTokens: 4096, WebSearchPerTurn: 0, MaxFileBytes: 5_000_000,
		},
		ValidFrom: now.Add(-time.Hour), Actor: "test",
	})
	require.NoError(t, err)
	periodStart := now.Add(-time.Minute)
	period, err := planService.CreatePeriod(plan.CreatePeriodCommand{
		CustomerID: createdCustomer.ID, PlanVersionID: published.Version.ID,
		StartAt: periodStart, EndAt: periodStart.AddDate(0, 1, 0), Actor: "test",
	})
	require.NoError(t, err)
	paymentCustomerID := createdCustomer.ID
	paymentEvidence := model.EvidenceObject{
		PublicID: "evidence_access_payment", CustomerID: &paymentCustomerID,
		OriginalFilename: "payment.json", MIMEType: "application/json", SizeBytes: 2,
		ContentSHA256: "sha256:" + strings.Repeat("d", 64),
		StorageKey:    strings.Repeat("d", 64), FormatVersion: 1,
		Status: model.EvidenceStatusActive, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&paymentEvidence).Error)
	_, err = planService.ConfirmPayment(plan.ConfirmPaymentCommand{
		PeriodID: period.ID, ExpectedVersion: period.RowVersion,
		PaymentEvidenceRef: paymentEvidence.PublicID, Actor: "test",
	})
	require.NoError(t, err)
	require.NoError(t, db.Where("customer_id = ?", createdCustomer.ID).First(&stableApp).Error)
	stableAppPtr, err := appService.Transition(app.TransitionCommand{
		CustomerID: createdCustomer.ID, ExpectedVersion: stableApp.RowVersion, Action: "enable", Actor: "test",
	})
	require.NoError(t, err)
	entryTicket, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: membership.Identity.NewAPIUserID, IdentityVersion: "v1.identity-fingerprint",
	})
	require.NoError(t, err)
	assert.Greater(t, entryTicket.ExpiresAt, time.Now().UTC().Unix(), "expires_at is Unix seconds")
	entered, err := accessService.Enter(context.Background(), access.EnterCommand{Ticket: entryTicket.Ticket})
	require.NoError(t, err)
	assert.NotEmpty(t, entered.SSOBrowserBinding)
	assert.Greater(t, entered.ADPSSOTicketExpiresAt.Unix(), time.Now().UTC().Unix())
	var storedSSOTicket model.SSOTicket
	require.NoError(t, db.Where("consumed_at IS NULL").First(&storedSSOTicket).Error)
	assert.NotEmpty(t, storedSSOTicket.BrowserBindingHash)
	assert.NotEqual(t, entered.SSOBrowserBinding, storedSSOTicket.BrowserBindingHash)
	_, err = accessService.Enter(context.Background(), access.EnterCommand{Ticket: entryTicket.Ticket})
	assert.Error(t, err, "entry ticket must be atomic and single-use")
	_, err = accessService.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: entered.ADPSSOTicket, BrowserBinding: "wrong-browser-binding", ConsumerService: "adp-backend",
	})
	assert.Error(t, err, "SSO ticket must reject a different browser without consuming the ticket")
	claims, err := accessService.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: entered.ADPSSOTicket, BrowserBinding: entered.SSOBrowserBinding, ConsumerService: "adp-backend",
	})
	require.NoError(t, err)
	assert.True(t, claims.Allowed)
	assert.Equal(t, "active", claims.AccessMode)
	confirmed, err := identityService.Confirm(identity.ConfirmCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		ADPAccountID: "adp-42", ADPAccountVersion: 1, Actor: "adp-backend",
	})
	require.NoError(t, err)
	effectiveEpoch := confirmed.AuthEpoch + membership.Member.AuthEpoch + stableAppPtr.AuthEpoch
	contextResult, err := accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "interactive",
	})
	require.NoError(t, err)
	assert.Equal(t, "app-key-secret", contextResult.AppKey)
	assert.Equal(t, "secret-id", contextResult.SecretID)
	assert.Equal(t, "secret-key", contextResult.SecretKey)
	assert.Equal(t, "Tencent", contextResult.Vendor)
	assert.Equal(t, "ChinaTencentCloud", contextResult.ServiceVendor)
	assert.Equal(t, []string{"chat"}, contextResult.Capabilities, "runtime capabilities are the App/plan intersection")
	assert.EqualValues(t, 1, contextResult.Limits.CustomerConcurrency, "runtime limits use the stricter App/plan value")
	assert.EqualValues(t, 300, contextResult.Limits.MaxRuntimeSeconds)
	assert.EqualValues(t, 0, contextResult.Limits.WebSearchPerTurn)
	assert.EqualValues(t, 5_000_000, contextResult.Limits.MaxFileBytes)
	assert.Nil(t, contextResult.ProviderAppMode, "legacy App context omits the additive Agent Store tuple")
	assert.Nil(t, contextResult.RuntimeProfile)
	assert.Nil(t, contextResult.ExecutionEnabled)

	catalogVersionID := "agv_access"
	item := model.AgentCatalogItem{ID: "agi_access", Slug: "access-agent", Status: model.AgentCatalogStatusPublished, CurrentVersionID: &catalogVersionID, RowVersion: 1, CreatedBy: "test"}
	require.NoError(t, db.Create(&item).Error)
	require.NoError(t, db.Create(&model.AgentCatalogVersion{
		ID: catalogVersionID, ItemID: item.ID, Generation: 1, DisplayName: "Access Agent", Summary: "Summary",
		Category: "general", TagsJSON: `[]`, MetadataSHA256: "sha256:" + strings.Repeat("e", 64), CreatedBy: "test",
	}).Error)
	require.NoError(t, db.Create(&model.CustomerAgentDeployment{
		ID: "agd_access", ItemID: item.ID, CustomerID: createdCustomer.ID, CustomerAppID: stableAppPtr.ID,
		VerifiedConfigVersionID: &saved.Version.ID, VerifiedConfigVersion: saved.Version.ConfigVersion,
		VerifiedAppAuthEpoch:   stableAppPtr.AuthEpoch,
		VerifiedCredentialHash: support.Hash(map[string]any{"app": saved.Version.AppKeyFingerprint, "credential": credentialProfile.Fingerprint}),
		ProviderAppMode:        4, RuntimeProfile: "claw_dynamic_v2", DynamicAgentConfig: true,
		ExecutionEnabled: true, CapabilitiesJSON: `["chat","history","dynamic_agent"]`, ProviderRequestIDsJSON: `[]`,
		Status: model.AgentDeploymentStatusActive, RowVersion: 1, VerifiedAt: &now,
	}).Error)
	storeContext, err := accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "interactive",
	})
	require.NoError(t, err)
	require.NotNil(t, storeContext.ProviderAppMode)
	require.NotNil(t, storeContext.RuntimeProfile)
	require.NotNil(t, storeContext.ExecutionEnabled)
	assert.Equal(t, 4, *storeContext.ProviderAppMode)
	assert.Equal(t, "claw_dynamic_v2", *storeContext.RuntimeProfile)
	assert.True(t, *storeContext.ExecutionEnabled)
	require.NoError(t, db.Where("item_id = ?", item.ID).Delete(&model.CustomerAgentDeployment{}).Error)
	require.NoError(t, db.Delete(&item).Error)
	require.NoError(t, db.Where("id = ?", catalogVersionID).Delete(&model.AgentCatalogVersion{}).Error)
	historicalApp := model.CustomerApp{
		CustomerID: createdCustomer.ID, Slot: "archived:999", ProviderEnvironment: model.ProviderChinaTencentCloud,
		AppID: "app-history", DisplayName: "Historical", Status: model.AppStatusArchived,
		AuthEpoch: 3, RowVersion: 1, ArchivedAt: &now,
	}
	require.NoError(t, db.Create(&historicalApp).Error)
	historicalConfig := model.AppConfigVersion{
		CustomerAppID: historicalApp.ID, ConfigVersion: 7, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "history-space", TemplateAgentID: "history-agent",
		CredentialProfileID: &credentialProfile.ID,
		AppKeySecretRef:     "env://WORKBENCH_PROVIDER_TEST_ADP_APP_KEY",
		AppKeyFingerprint:   secrets.AppKeyFingerprint("app-key-secret"), AppKeyFingerprintVersion: 1,
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&historicalConfig).Error)
	historicalApp.CurrentConfigVersionID = &historicalConfig.ID
	require.NoError(t, db.Save(&historicalApp).Error)
	require.NoError(t, db.Create(&model.AppMigrationLineage{
		PublicID: "lin_access", EventKey: "app-migration-cutover:history", CustomerID: createdCustomer.ID,
		MigrationJobID: 999, SourceCustomerAppID: historicalApp.ID,
		SourceAppConfigVersionID: historicalConfig.ID, SourceApplicationID: historicalApp.AppID,
		SourceProviderAppID: historicalApp.AppID,
		SourceConfigVersion: historicalConfig.ConfigVersion, TargetCustomerAppID: stableAppPtr.ID,
		TargetAppConfigVersionID: saved.Version.ID, TargetApplicationID: stableAppPtr.AppID,
		TargetProviderAppID:        stableAppPtr.AppID,
		TargetConfigVersion:        saved.Version.ConfigVersion,
		MigrationConfigFingerprint: "sha256:target", ActivatedAt: now,
	}).Error)
	historyContext, err := accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: historicalApp.ID,
		RequestedConfigVersion: historicalConfig.ConfigVersion,
		CurrentAppProfileID:    stableAppPtr.ID, CurrentConfigVersion: saved.Version.ConfigVersion,
		Purpose: "history_read",
	})
	require.NoError(t, err)
	assert.Equal(t, "app-history", historyContext.ApplicationID)
	assert.Equal(t, "history-space", historyContext.SpaceID)
	require.NotNil(t, historyContext.ProviderAppMode)
	require.NotNil(t, historyContext.RuntimeProfile)
	require.NotNil(t, historyContext.ExecutionEnabled)
	assert.Equal(t, 4, *historyContext.ProviderAppMode, "legacy lineages use the historical dynamic-Claw contract")
	assert.Equal(t, "claw_dynamic_v2", *historyContext.RuntimeProfile)
	assert.False(t, *historyContext.ExecutionEnabled, "history credentials must never enable provider execution")
	oldestApp := model.CustomerApp{
		CustomerID: createdCustomer.ID, Slot: "archived:998", ProviderEnvironment: model.ProviderChinaTencentCloud,
		AppID: "app-oldest", DisplayName: "Oldest", Status: model.AppStatusArchived,
		AuthEpoch: 2, RowVersion: 1, ArchivedAt: &now,
	}
	require.NoError(t, db.Create(&oldestApp).Error)
	oldestConfig := model.AppConfigVersion{
		CustomerAppID: oldestApp.ID, ConfigVersion: 3, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "oldest-space", CredentialProfileID: &credentialProfile.ID,
		AppKeySecretRef:   "env://WORKBENCH_PROVIDER_TEST_ADP_APP_KEY",
		AppKeyFingerprint: secrets.AppKeyFingerprint("app-key-secret"), AppKeyFingerprintVersion: 1,
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&oldestConfig).Error)
	oldestApp.CurrentConfigVersionID = &oldestConfig.ID
	require.NoError(t, db.Save(&oldestApp).Error)
	require.NoError(t, db.Create(&model.AppMigrationLineage{
		PublicID: "lin_access_oldest", EventKey: "app-migration-cutover:oldest", CustomerID: createdCustomer.ID,
		MigrationJobID: 998, SourceCustomerAppID: oldestApp.ID,
		SourceAppConfigVersionID: oldestConfig.ID, SourceApplicationID: oldestApp.AppID,
		SourceProviderAppID: oldestApp.AppID, SourceConfigVersion: oldestConfig.ConfigVersion,
		SourceProviderAppMode: 1, SourceRuntimeProfile: "standard_v2",
		TargetCustomerAppID: historicalApp.ID, TargetAppConfigVersionID: historicalConfig.ID,
		TargetApplicationID: historicalApp.AppID, TargetProviderAppID: historicalApp.AppID,
		TargetConfigVersion:        historicalConfig.ConfigVersion,
		MigrationConfigFingerprint: "sha256:history", ActivatedAt: now.Add(-time.Minute),
	}).Error)
	oldestContext, err := accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: oldestApp.ID,
		RequestedConfigVersion: oldestConfig.ConfigVersion,
		CurrentAppProfileID:    stableAppPtr.ID, CurrentConfigVersion: saved.Version.ConfigVersion,
		Purpose: "history_read",
	})
	require.NoError(t, err)
	assert.Equal(t, "app-oldest", oldestContext.ApplicationID)
	assert.Equal(t, 1, *oldestContext.ProviderAppMode)
	assert.Equal(t, "standard_v2", *oldestContext.RuntimeProfile)
	assert.False(t, *oldestContext.ExecutionEnabled)
	_, err = accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion,
		CurrentAppProfileID:    stableAppPtr.ID, CurrentConfigVersion: saved.Version.ConfigVersion,
		Purpose: "history_read",
	})
	assert.ErrorContains(t, err, "historical App context not found")
	_, err = accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: "napi:prod:customer:999:user:42",
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "interactive",
	})
	assert.ErrorContains(t, err, "auth_epoch is stale", "App credentials must be bound to the canonical subject")
	_, err = accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "scheduled_task",
	})
	assert.ErrorContains(t, err, "purpose is not enabled", "offline App credentials require the matching effective capability")
	foreignCustomer, err := customerService.Create(customer.CreateCommand{
		CustomerCode: "foreign-credential-owner", DisplayName: "Foreign Owner", Actor: "test",
	})
	require.NoError(t, err)
	foreignCustomerID := foreignCustomer.ID
	foreignCredential, err := credentialService.Create(credential.CreateCommand{
		OwnerScope: credential.CustomerOwnerScope(foreignCustomerID), CustomerID: &foreignCustomerID,
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "foreign",
		SecretIDRef: "env://WORKBENCH_PROVIDER_TEST_TENCENT_SECRET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_TEST_TENCENT_SECRET_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("secret-id", "secret-key"), Actor: "test",
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.AppConfigVersion{}).Where("id = ?", saved.Version.ID).Update("credential_profile_id", foreignCredential.ID).Error)
	_, err = accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: effectiveEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "interactive",
	})
	assert.ErrorContains(t, err, "credential profile is inactive or mismatched", "runtime resolution must reject a cross-customer profile even if storage was corrupted")
	require.NoError(t, db.Model(&model.AppConfigVersion{}).Where("id = ?", saved.Version.ID).Update("credential_profile_id", credentialProfile.ID).Error)
	serializedContext, err := jsonx.Marshal(contextResult.Limits)
	require.NoError(t, err)
	for _, limit := range []string{"customer_concurrency", "user_concurrency", "max_runtime_seconds", "max_reasoning_rounds", "max_output_tokens", "web_search_per_turn", "max_file_bytes"} {
		assert.Contains(t, string(serializedContext), `"`+limit+`"`)
	}
	browserConfig, err := accessService.ConfigForSession(context.Background(), entered.ControlSessionToken)
	require.NoError(t, err)
	assert.Equal(t, "Access Test", browserConfig.CustomerDisplayName)
	assert.Equal(t, contextResult.Capabilities, browserConfig.Capabilities)
	assert.Equal(t, contextResult.Limits, browserConfig.Limits)
	serializedConfig, err := jsonx.Marshal(browserConfig)
	require.NoError(t, err)
	for _, forbidden := range []string{"app_id", "app_profile_id", "space_id", "template_agent_id", "provider_environment", "region", "secret"} {
		assert.NotContains(t, string(serializedConfig), forbidden)
	}
	browserPlan, err := accessService.PlanForSession(context.Background(), entered.ControlSessionToken)
	require.NoError(t, err)
	assert.Equal(t, "88.00", browserPlan.AmountCNY)
	_, err = accessService.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: entered.ADPSSOTicket, BrowserBinding: entered.SSOBrowserBinding, ConsumerService: "adp-backend",
	})
	assert.Error(t, err, "SSO ticket must be atomic and single-use")
	require.NoError(t, db.Model(&model.PlanPeriod{}).Where("id = ?", period.ID).Updates(map[string]any{
		"end_at": time.Now().UTC().Add(-time.Minute), "status": model.PeriodStatusExpired,
	}).Error)
	expiredPlanEntry, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: membership.Identity.NewAPIUserID, IdentityVersion: "v1.identity-fingerprint",
	})
	require.NoError(t, err)
	expiredPlanSession, err := accessService.Enter(context.Background(), access.EnterCommand{Ticket: expiredPlanEntry.Ticket})
	require.NoError(t, err)
	expiredPlanClaims, err := accessService.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: expiredPlanSession.ADPSSOTicket, BrowserBinding: expiredPlanSession.SSOBrowserBinding,
		ConsumerService: "adp-backend",
	})
	require.NoError(t, err)
	assert.Equal(t, "readonly", expiredPlanClaims.AccessMode, "an expired plan must retain owned-history access")
	_, err = accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: expiredPlanClaims.AuthEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "interactive",
	})
	require.NoError(t, err)

	suspendedApp, err := appService.Transition(app.TransitionCommand{
		CustomerID: createdCustomer.ID, ExpectedVersion: stableAppPtr.RowVersion,
		Action: "suspend", Reason: "billing review", Actor: "test",
	})
	require.NoError(t, err)
	readonlyEntry, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: membership.Identity.NewAPIUserID, IdentityVersion: "v1.identity-fingerprint",
	})
	require.NoError(t, err)
	readonlySession, err := accessService.Enter(context.Background(), access.EnterCommand{Ticket: readonlyEntry.Ticket})
	require.NoError(t, err)
	readonlyClaims, err := accessService.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: readonlySession.ADPSSOTicket, BrowserBinding: readonlySession.SSOBrowserBinding,
		ConsumerService: "adp-backend",
	})
	require.NoError(t, err)
	assert.Equal(t, "readonly", readonlyClaims.AccessMode)
	assert.Equal(t, suspendedApp.AuthEpoch+confirmed.AuthEpoch+membership.Member.AuthEpoch, readonlyClaims.AuthEpoch)
	_, err = accessService.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: readonlyClaims.AuthEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "interactive",
	})
	require.NoError(t, err, "read-only sessions still need trusted App context for owned history")

	adminEntry, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 999, IdentityVersion: "v1.admin", Surface: "admin", IsSuperAdmin: true,
	})
	require.NoError(t, err)
	adminSession, err := accessService.Enter(context.Background(), access.EnterCommand{Ticket: adminEntry.Ticket})
	require.NoError(t, err)
	assert.Equal(t, "admin", adminSession.Surface)
	assert.Empty(t, adminSession.ADPSSOTicket, "admin surface must never mint an ADP SSO ticket")
	userID, err := accessService.AuthorizeAdminSession(context.Background(), adminSession.AdminSessionToken, "", false)
	require.NoError(t, err)
	assert.EqualValues(t, 999, userID)
	_, err = accessService.AuthorizeAdminSession(context.Background(), adminSession.AdminSessionToken, "wrong", true)
	assert.Error(t, err)
	_, err = accessService.AuthorizeAdminSession(context.Background(), adminSession.AdminSessionToken, adminSession.AdminCSRFToken, true)
	require.NoError(t, err)
	_, err = accessService.AuthorizeRecentAdminSession(context.Background(), adminSession.AdminSessionToken, 15*time.Minute)
	assert.ErrorContains(t, err, "recent administrator authentication is required", "ordinary admin entry must not become a step-up")

	reauthNonce := sha256.Sum256([]byte("fresh-admin-step-up"))
	stepUpCommand := access.IssueEntryTicketCommand{
		NewAPIUserID: 999, IdentityVersion: "v1.admin", Surface: "admin", IsSuperAdmin: true,
		AuthenticatedAt: time.Now().UTC(), AMR: []string{"webauthn"},
		ReauthNonce: base64.RawURLEncoding.EncodeToString(reauthNonce[:]),
	}
	stepUpEntry, err := accessService.IssueEntryTicket(stepUpCommand)
	require.NoError(t, err)
	stepUpSession, err := accessService.Enter(context.Background(), access.EnterCommand{Ticket: stepUpEntry.Ticket})
	require.NoError(t, err)
	_, err = accessService.AuthorizeRecentAdminSession(context.Background(), stepUpSession.AdminSessionToken, 15*time.Minute)
	require.NoError(t, err)
	_, err = accessService.IssueEntryTicket(stepUpCommand)
	assert.ErrorContains(t, err, "already been used", "step-up nonces cannot mint a second entry ticket")
	expiredNonce := sha256.Sum256([]byte("expired-admin-step-up"))
	stepUpCommand.ReauthNonce = base64.RawURLEncoding.EncodeToString(expiredNonce[:])
	stepUpCommand.AuthenticatedAt = time.Now().UTC().Add(-6 * time.Minute)
	_, err = accessService.IssueEntryTicket(stepUpCommand)
	assert.ErrorContains(t, err, "expired or invalid")

	failClosed := access.New(
		db, secrets.EnvironmentResolver{}, rejectingIdentityVerifier{},
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)
	_, err = failClosed.Authorize(context.Background(), access.AuthzCommand{
		BindingID: confirmed.PublicID, CanonicalSubject: confirmed.CanonicalSubject,
		AuthEpoch: effectiveEpoch, Method: "GET", ResourcePath: "/workbench/conversations",
	})
	assert.Error(t, err, "authz must fail closed when new-api identity status is unavailable")
	_, err = failClosed.AuthorizeAdminSession(context.Background(), adminSession.AdminSessionToken, "", false)
	assert.Error(t, err, "admin authorization must fail closed when new-api status is unavailable")
	_, err = failClosed.AppContext(context.Background(), access.AppContextCommand{
		BindingID: membership.Identity.PublicID, CanonicalSubject: membership.Identity.CanonicalSubject,
		AuthEpoch: readonlyClaims.AuthEpoch, RequestedAppProfileID: stableAppPtr.ID,
		RequestedConfigVersion: saved.Version.ConfigVersion, Purpose: "interactive",
	})
	assert.Error(t, err, "App credential release must fail closed when new-api status is unavailable")
}
