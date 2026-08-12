package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/approval"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/providerverify"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type mapResolver map[string]string

func (r mapResolver) Resolve(reference string) (string, error) {
	value, ok := r[reference]
	if !ok {
		return "", errors.New("missing test secret")
	}
	return value, nil
}

func providerSecrets() mapResolver {
	return mapResolver{
		"env://WORKBENCH_PROVIDER_APP_KEY":        "resolved-app-key",
		"env://WORKBENCH_PROVIDER_APP_KEY_2":      "resolved-app-key-2",
		"env://WORKBENCH_PROVIDER_SECRET_ID":      "resolved-secret-id",
		"env://WORKBENCH_PROVIDER_SECRET_KEY":     "resolved-secret-key",
		"env://WORKBENCH_PROVIDER_OWNER_ID":       "owner-id",
		"env://WORKBENCH_PROVIDER_OWNER_KEY":      "owner-key",
		"env://WORKBENCH_PROVIDER_SECOND_APP_KEY": "second-app-key",
	}
}

type recordingVerifier struct {
	target providerverify.Target
	result providerverify.Result
	err    error
}

func (v *recordingVerifier) Verify(_ context.Context, target providerverify.Target) (providerverify.Result, error) {
	v.target = target
	return v.result, v.err
}

func TestVerifyPendingResolvesServerSecretsAndPromotesVerifiedConfig(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := providerSecrets()
	apps := app.New(db, false, resolver)
	created, version, credentialProfile := createPendingConfig(t, db, apps)
	verifier := &recordingVerifier{result: providerverify.Result{
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs:    []string{"request-app", "request-agent"},
		SanitizedResponseHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}

	verification, err := apps.VerifyPending(context.Background(), app.VerifyPendingCommand{
		CustomerID: created.CustomerID, ExpectedVersion: created.RowVersion, ConfigVersion: version.ConfigVersion,
		Actor: "root:1", RequestID: "request-1",
	}, resolver, verifier)
	require.NoError(t, err)
	assert.Equal(t, "verified", verification.Result)
	assert.Equal(t, "resolved-app-key", verifier.target.AppKey)
	assert.Equal(t, "resolved-secret-id", verifier.target.SecretID)
	assert.Equal(t, "resolved-secret-key", verifier.target.SecretKey)
	assert.Equal(t, credentialProfile.ProviderEnvironment, verifier.target.ProviderEnvironment)

	var persisted model.CustomerApp
	require.NoError(t, db.First(&persisted, created.ID).Error)
	assert.Nil(t, persisted.PendingConfigVersionID)
	assert.NotNil(t, persisted.CurrentConfigVersionID)
}

func TestVerifyPendingLeavesDraftRetryableOnProviderTransportFailure(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := providerSecrets()
	apps := app.New(db, false, resolver)
	created, version, _ := createPendingConfig(t, db, apps)
	verifier := &recordingVerifier{err: errors.New("provider unavailable")}

	_, err = apps.VerifyPending(context.Background(), app.VerifyPendingCommand{
		CustomerID: created.CustomerID, ExpectedVersion: created.RowVersion, ConfigVersion: version.ConfigVersion,
		Actor: "root:1", RequestID: "request-1",
	}, resolver, verifier)
	assert.Error(t, err)

	var persisted model.CustomerApp
	require.NoError(t, db.First(&persisted, created.ID).Error)
	assert.Equal(t, created.PendingConfigVersionID, persisted.PendingConfigVersionID)
}

func TestSaveConfigAcceptsWriteOnlyAppKeyAndPersistsOnlyVaultReference(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	vault, err := secrets.NewVaultResolver(db, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	resolver := secrets.NewCompositeResolver(providerSecrets(), vault)
	createdCustomer, err := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.vault")).Create(customer.CreateCommand{
		CustomerCode: "vault-customer", DisplayName: "Vault Customer", Actor: "root:1",
	})
	require.NoError(t, err)
	profile, err := credential.New(db, resolver).Create(credential.CreateCommand{
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "vault-primary",
		SecretIDRef: "env://WORKBENCH_PROVIDER_SECRET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_SECRET_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("resolved-secret-id", "resolved-secret-key"), Actor: "root:1",
	})
	require.NoError(t, err)
	apps := app.New(db, false, resolver)
	result, err := apps.SaveConfig(app.SaveConfigCommand{
		CustomerID: createdCustomer.ID, ProviderEnvironment: model.ProviderChinaTencentCloud,
		Region: "ap-guangzhou", SpaceID: "default_space", AppID: "provider-app-vault",
		CredentialProfileID: &profile.ID, AppKey: "write-only-provider-app-key", DisplayName: "Vault App",
		Capabilities: []string{productpolicy.CapabilityChat}, Limits: productpolicy.Limits{
			CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 300,
			MaxReasoningRounds: 5, MaxOutputTokens: 1024,
		}, Actor: "root:1", RequestID: "vault-save",
	})
	require.NoError(t, err)
	assert.True(t, secrets.ValidVaultReference(result.Version.AppKeySecretRef))
	assert.NotContains(t, result.Version.AppKeySecretRef, "write-only-provider-app-key")
	resolved, err := secrets.ResolveAppKey(resolver, result.Version.AppKeySecretRef, result.Version.AppKeyFingerprint, result.Version.AppKeyFingerprintVersion)
	require.NoError(t, err)
	assert.Equal(t, "write-only-provider-app-key", resolved)

	var record model.ProviderSecret
	require.NoError(t, db.First(&record).Error)
	assert.Equal(t, createdCustomer.ID, record.CustomerID)
	assert.NotContains(t, record.Ciphertext, "write-only-provider-app-key")
}

func TestAppEnableRequiresAnActivePaidPlanPeriod(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := providerSecrets()
	apps := app.New(db, false, resolver)
	created, version, _ := createPendingConfig(t, db, apps)
	verifier := &recordingVerifier{result: providerverify.Result{
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs:    []string{"request-app", "request-agent"},
		SanitizedResponseHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	_, err = apps.VerifyPending(context.Background(), app.VerifyPendingCommand{
		CustomerID: created.CustomerID, ExpectedVersion: created.RowVersion, ConfigVersion: version.ConfigVersion,
		Actor: "root:1", RequestID: "request-1",
	}, resolver, verifier)
	require.NoError(t, err)
	require.NoError(t, db.First(created, created.ID).Error)

	now := time.Now().UTC()
	period := model.PlanPeriod{
		CustomerID: created.CustomerID, PlanVersionID: 1,
		StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), AmountCNY: "99.00",
		PaymentMode: model.PaymentModeOfflineManual, PaymentStatus: model.PaymentStatusPending,
		Status: model.PeriodStatusActive, SnapshotJSON: `{}`, RowVersion: 1,
	}
	require.NoError(t, db.Create(&period).Error)
	_, err = apps.Transition(app.TransitionCommand{
		CustomerID: created.CustomerID, ExpectedVersion: created.RowVersion, Action: "enable", Actor: "root:1",
	})
	assert.ErrorContains(t, err, "no active paid plan period")

	require.NoError(t, db.Model(&period).Update("payment_status", model.PaymentStatusPaid).Error)
	enabled, err := apps.Transition(app.TransitionCommand{
		CustomerID: created.CustomerID, ExpectedVersion: created.RowVersion, Action: "enable", Actor: "root:1",
	})
	require.NoError(t, err)
	assert.Equal(t, model.AppStatusActive, enabled.Status)
}

func TestAdditionalAppCanBeEnabledWithoutReplacingPrimaryApp(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customerRecord := model.Customer{CustomerCode: "multi-app-enable", DisplayName: "Multi App", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customerRecord).Error)
	primary := model.CustomerApp{CustomerID: customerRecord.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "primary-app", DisplayName: "Primary", Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 3}
	require.NoError(t, db.Create(&primary).Error)
	additional := model.CustomerApp{CustomerID: customerRecord.ID, Slot: "app:additional", Alias: "additional", ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "additional-app", DisplayName: "Additional", Status: model.AppStatusVerified, AuthEpoch: 1, RowVersion: 2}
	require.NoError(t, db.Create(&additional).Error)
	config := model.AppConfigVersion{CustomerAppID: additional.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified, Region: "ap-guangzhou", SpaceID: "default_space", AppKeySecretRef: "sealed://additional", AppKeyFingerprint: "sha256:additional", AppKeyFingerprintVersion: 1, RowVersion: 1, LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test"}
	require.NoError(t, db.Create(&config).Error)
	require.NoError(t, db.Model(&additional).Update("current_config_version_id", config.ID).Error)
	now := time.Now().UTC()
	period := model.PlanPeriod{CustomerID: customerRecord.ID, PlanVersionID: 1, StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), AmountCNY: "99.00", PaymentMode: model.PaymentModeOfflineManual, PaymentStatus: model.PaymentStatusPaid, Status: model.PeriodStatusActive, SnapshotJSON: `{}`, RowVersion: 1}
	require.NoError(t, db.Create(&period).Error)

	enabled, err := app.New(db, false).Transition(app.TransitionCommand{CustomerID: customerRecord.ID, CustomerAppID: additional.ID, ExpectedVersion: additional.RowVersion, Action: "enable", Actor: "root:1"})
	require.NoError(t, err)
	assert.Equal(t, model.AppStatusActive, enabled.Status)

	var persistedPrimary model.CustomerApp
	require.NoError(t, db.First(&persistedPrimary, primary.ID).Error)
	assert.Equal(t, "primary", persistedPrimary.Slot)
	assert.Equal(t, model.AppStatusActive, persistedPrimary.Status)
}

func TestProductionPolicyRejectsDirectAppDisable(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customerRecord := model.Customer{CustomerCode: "protected-disable", DisplayName: "Protected", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customerRecord).Error)
	appRecord := model.CustomerApp{
		CustomerID: customerRecord.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "protected-app", DisplayName: "Protected", Status: model.AppStatusActive,
		AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&appRecord).Error)
	_, err = app.New(db, true).Transition(app.TransitionCommand{
		CustomerID: customerRecord.ID, ExpectedVersion: appRecord.RowVersion,
		Action: "disable", Reason: "must use approval", Actor: "admin",
	})
	assert.ErrorContains(t, err, "approved governance request")
	require.NoError(t, db.First(&appRecord, appRecord.ID).Error)
	assert.Equal(t, model.AppStatusActive, appRecord.Status)
}

func TestAppConfigAcceptsReadOnlyCatalogCapabilities(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	apps := app.New(db, false, providerSecrets())
	created, _, _ := createPendingConfig(t, db, apps)
	var version model.AppConfigVersion
	require.NoError(t, db.First(&version, *created.PendingConfigVersionID).Error)
	var capabilities []string
	require.NoError(t, jsonx.Unmarshal([]byte(version.CapabilitiesJSON), &capabilities))
	assert.Equal(t, []string{
		productpolicy.CapabilityCatalogModels,
		productpolicy.CapabilityCatalogPlugins,
		productpolicy.CapabilityCatalogSkills,
		productpolicy.CapabilityChat,
	}, capabilities)

	normalized, err := productpolicy.NormalizeCapabilities([]string{
		productpolicy.CapabilityCatalogModels,
		productpolicy.CapabilityCatalogSkills,
		productpolicy.CapabilityCatalogPlugins,
	})
	require.NoError(t, err)
	for _, capability := range normalized {
		assert.True(t, productpolicy.IsCatalogReadCapability(capability))
		assert.False(t, productpolicy.IsExecutionCapability(capability))
	}
}

func TestSetDefaultSwapsStableSelectorsAndInvalidatesBothApps(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customer := model.Customer{CustomerCode: "multi-app", DisplayName: "Multi App", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	primary := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "default-upstream", DisplayName: "Default", Status: model.AppStatusActive, AuthEpoch: 2, RowVersion: 3,
	}
	require.NoError(t, db.Create(&primary).Error)
	secondary := model.CustomerApp{
		CustomerID: customer.ID, Slot: "app:secondary", Alias: "secondary",
		ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "secondary-upstream",
		DisplayName: "Secondary", Status: model.AppStatusActive, AuthEpoch: 4, RowVersion: 5,
	}
	require.NoError(t, db.Create(&secondary).Error)
	primaryConfig := model.AppConfigVersion{
		CustomerAppID: primary.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "default-space", TemplateAgentID: "default-agent",
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_DEFAULT_APP_KEY", AppKeyFingerprint: "sha256:default",
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test",
	}
	secondaryConfig := primaryConfig
	secondaryConfig.ID = 0
	secondaryConfig.CustomerAppID = secondary.ID
	secondaryConfig.SpaceID = "secondary-space"
	require.NoError(t, db.Create(&primaryConfig).Error)
	require.NoError(t, db.Create(&secondaryConfig).Error)
	require.NoError(t, db.Model(&primary).Update("current_config_version_id", primaryConfig.ID).Error)
	require.NoError(t, db.Model(&secondary).Update("current_config_version_id", secondaryConfig.ID).Error)
	primary.CurrentConfigVersionID = &primaryConfig.ID
	secondary.CurrentConfigVersionID = &secondaryConfig.ID
	primarySelector, secondarySelector := primary.Selector, secondary.Selector

	updated, err := app.New(db, false).SetDefault(app.SetDefaultCommand{
		CustomerID: customer.ID, Selector: secondary.Selector,
		ExpectedTargetVersion: secondary.RowVersion, ExpectedCurrentDefaultVersion: primary.RowVersion,
		Actor: "root:1", RequestID: "set-default",
	})
	require.NoError(t, err)
	assert.Equal(t, secondarySelector, updated.Selector)
	assert.Equal(t, "primary", updated.Slot)
	assert.Equal(t, secondary.AuthEpoch+1, updated.AuthEpoch)
	require.NoError(t, db.First(&primary, primary.ID).Error)
	assert.Equal(t, primarySelector, primary.Selector)
	assert.Equal(t, "app:"+primarySelector, primary.Slot)
	assert.Equal(t, int64(3), primary.AuthEpoch)
	var auditCount int64
	require.NoError(t, db.Model(&model.AdminAudit{}).Where("customer_id = ? AND action IN ?", customer.ID, []string{"app.default.remove", "app.default.set"}).Count(&auditCount).Error)
	assert.EqualValues(t, 2, auditCount)
}

func TestCreateAdditionalAppUsesServerGeneratedSelectorAndCustomerAlias(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := providerSecrets()
	apps := app.New(db, false, resolver)
	primary, primaryVersion, profile := createPendingConfig(t, db, apps)
	_, err = apps.RecordVerification(app.RecordVerificationCommand{
		CustomerID: primary.CustomerID, ExpectedVersion: primary.RowVersion, ConfigVersion: primaryVersion.ConfigVersion,
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs: []string{"primary-verify"}, SanitizedResponseHash: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Actor: "root:1",
	})
	require.NoError(t, err)
	require.NoError(t, db.First(primary, primary.ID).Error)
	result, err := apps.CreateAdditional(app.CreateAdditionalCommand{
		Alias: "research-space",
		Config: app.SaveConfigCommand{
			CustomerID: primary.CustomerID, ProviderEnvironment: model.ProviderChinaTencentCloud,
			Region: "ap-guangzhou", SpaceID: "space-2", AppID: "app-2", TemplateAgentID: "agent-2",
			CredentialProfileID: &profile.ID, AppKeySecretRef: "env://WORKBENCH_PROVIDER_APP_KEY_2",
			AppKeyFingerprint: secrets.AppKeyFingerprint("resolved-app-key-2"), DisplayName: "Research Space",
			Capabilities: []string{productpolicy.CapabilityChat}, Limits: productpolicy.Limits{
				CustomerConcurrency: 10, UserConcurrency: 1, MaxRuntimeSeconds: 900,
				MaxReasoningRounds: 20, MaxOutputTokens: 4096,
			}, Actor: "root:1", RequestID: "add-app",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.App.Selector)
	assert.Equal(t, "research-space", result.App.Alias)
	assert.Equal(t, "app:"+result.App.Selector, result.App.Slot)
	assert.Equal(t, model.AppStatusDraft, result.App.Status)
	assert.Equal(t, result.App.ID, result.Version.CustomerAppID)
	assert.NotEqual(t, primary.Selector, result.App.Selector)
}

func TestCreateAdditionalCannotBypassCredentialChangeApproval(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := providerSecrets()
	resolver["env://WORKBENCH_PROVIDER_ADDITIONAL_ID"] = "additional-id"
	resolver["env://WORKBENCH_PROVIDER_ADDITIONAL_KEY"] = "additional-key"
	resolver["env://WORKBENCH_PROVIDER_ADDITIONAL_APP_KEY"] = "additional-app-key"
	apps := app.New(db, false, resolver)
	primary, primaryVersion, _ := createPendingConfig(t, db, apps)
	_, err = apps.RecordVerification(app.RecordVerificationCommand{
		CustomerID: primary.CustomerID, ExpectedVersion: primary.RowVersion, ConfigVersion: primaryVersion.ConfigVersion,
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs: []string{"primary-verify"}, SanitizedResponseHash: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Actor: "admin-a",
	})
	require.NoError(t, err)
	require.NoError(t, db.First(primary, primary.ID).Error)
	different, err := credential.New(db, resolver).Create(credential.CreateCommand{
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "different-additional",
		SecretIDRef: "env://WORKBENCH_PROVIDER_ADDITIONAL_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_ADDITIONAL_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("additional-id", "additional-key"), Actor: "admin-a",
	})
	require.NoError(t, err)
	_, err = apps.CreateAdditional(app.CreateAdditionalCommand{
		Alias: "different-credential",
		Config: app.SaveConfigCommand{
			CustomerID: primary.CustomerID, ProviderEnvironment: primary.ProviderEnvironment,
			Region: "ap-guangzhou", SpaceID: "additional-space", AppID: "additional-app", TemplateAgentID: "additional-agent",
			CredentialProfileID: &different.ID, AppKeySecretRef: "env://WORKBENCH_PROVIDER_ADDITIONAL_APP_KEY",
			AppKeyFingerprint: secrets.AppKeyFingerprint("additional-app-key"), DisplayName: "Additional",
			Capabilities: []string{productpolicy.CapabilityChat}, Limits: productpolicy.Limits{
				CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 600, MaxReasoningRounds: 10, MaxOutputTokens: 4096,
			}, Actor: "admin-a",
		},
	})
	assert.ErrorContains(t, err, "must initially use the primary App current credential")
	var count int64
	require.NoError(t, db.Model(&model.CustomerApp{}).Where("customer_id = ?", primary.CustomerID).Count(&count).Error)
	assert.EqualValues(t, 1, count, "the rejected request must not create an additional App")
}

func TestAppConfigurationRejectsAnotherCustomersCredentialProfile(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customers := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test"))
	first, err := customers.Create(customer.CreateCommand{CustomerCode: "scope-first", DisplayName: "First", Actor: "test"})
	require.NoError(t, err)
	second, err := customers.Create(customer.CreateCommand{CustomerCode: "scope-second", DisplayName: "Second", Actor: "test"})
	require.NoError(t, err)
	ownerID := first.ID
	resolver := providerSecrets()
	ownedProfile, err := credential.New(db, resolver).Create(credential.CreateCommand{
		OwnerScope: credential.CustomerOwnerScope(ownerID), CustomerID: &ownerID,
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "owned",
		SecretIDRef: "env://WORKBENCH_PROVIDER_OWNER_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_OWNER_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("owner-id", "owner-key"), Actor: "test",
	})
	require.NoError(t, err)
	apps := app.New(db, false, resolver)
	input := app.SaveConfigCommand{
		CustomerID: second.ID, ProviderEnvironment: model.ProviderChinaTencentCloud,
		Region: "ap-guangzhou", SpaceID: "space-2", AppID: "app-2", TemplateAgentID: "agent-2",
		CredentialProfileID: &ownedProfile.ID, AppKeySecretRef: "env://WORKBENCH_PROVIDER_SECOND_APP_KEY",
		AppKeyFingerprint: secrets.AppKeyFingerprint("second-app-key"), DisplayName: "Second App",
		Capabilities: []string{productpolicy.CapabilityChat}, Limits: productpolicy.Limits{
			CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 600,
			MaxReasoningRounds: 10, MaxOutputTokens: 4096,
		}, Actor: "test",
	}
	_, err = apps.SaveConfig(input)
	assert.ErrorContains(t, err, "unavailable to this customer")

	input.CustomerID = first.ID
	input.AppID = "app-1"
	input.SpaceID = "space-1"
	input.DisplayName = "First App"
	_, err = apps.SaveConfig(input)
	require.NoError(t, err, "the owner customer must be able to use its profile")
}

func TestExistingAppCredentialChangeRequiresBoundTwoPersonApproval(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := providerSecrets()
	resolver["env://WORKBENCH_PROVIDER_TARGET_ID"] = "target-id"
	resolver["env://WORKBENCH_PROVIDER_TARGET_KEY"] = "target-key"
	apps := app.New(db, false, resolver)
	application, firstVersion, _ := createPendingConfig(t, db, apps)
	_, err = apps.RecordVerification(app.RecordVerificationCommand{
		CustomerID: application.CustomerID, ExpectedVersion: application.RowVersion, ConfigVersion: firstVersion.ConfigVersion,
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs: []string{"first-verify"}, SanitizedResponseHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Actor: "admin-a",
	})
	require.NoError(t, err, "the first App config remains a single-admin bootstrap")
	require.NoError(t, db.First(application, application.ID).Error)

	target, err := credential.New(db, resolver).Create(credential.CreateCommand{
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "target",
		SecretIDRef: "env://WORKBENCH_PROVIDER_TARGET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_TARGET_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("target-id", "target-key"), Actor: "admin-a",
	})
	require.NoError(t, err)
	pending, err := apps.SaveConfig(app.SaveConfigCommand{
		CustomerID: application.CustomerID, ExpectedVersion: application.RowVersion,
		ProviderEnvironment: application.ProviderEnvironment, Region: "ap-guangzhou", SpaceID: "space-1",
		AppID: application.AppID, TemplateAgentID: "agent-1", CredentialProfileID: &target.ID,
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_APP_KEY", AppKeyFingerprint: secrets.AppKeyFingerprint("resolved-app-key"),
		DisplayName: application.DisplayName, Capabilities: []string{productpolicy.CapabilityChat},
		Limits: productpolicy.Limits{CustomerConcurrency: 10, UserConcurrency: 1, MaxRuntimeSeconds: 900, MaxReasoningRounds: 20, MaxOutputTokens: 4096},
		Actor:  "admin-a",
	})
	require.NoError(t, err)
	verifier := &recordingVerifier{result: providerverify.Result{
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		ProviderRequestIDs: []string{"credential-change-verify"}, SanitizedResponseHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}
	_, err = apps.VerifyPending(context.Background(), app.VerifyPendingCommand{
		CustomerID: application.CustomerID, CustomerAppID: application.ID,
		ExpectedVersion: pending.App.RowVersion, ConfigVersion: pending.Version.ConfigVersion, Actor: "admin-a",
	}, resolver, verifier)
	assert.ErrorContains(t, err, "two-person approval")

	now := time.Now().UTC()
	approvals := approval.New(db, resolver)
	request, err := approvals.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionAppCredentialChange, CustomerID: &application.CustomerID,
		ResourceID: pending.Version.ID, RequestKey: "app-credential-change", Reason: "move to customer-approved credential",
		Actor: "admin-a", TTL: time.Hour,
	}, now)
	require.NoError(t, err)
	request, err = approvals.Approve(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: request.RowVersion, Actor: "admin-b",
	}, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.CredentialProfile{}).Where("id = ?", target.ID).Update("row_version", target.RowVersion+1).Error)
	_, err = approvals.Execute(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: request.RowVersion, Actor: "admin-c",
	}, now.Add(2*time.Minute))
	assert.ErrorContains(t, err, "tuple changed", "any bound row-version change must invalidate execution")
	require.NoError(t, db.Model(&model.CredentialProfile{}).Where("id = ?", target.ID).Update("row_version", target.RowVersion).Error)
	_, err = approvals.Execute(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: request.RowVersion, Actor: "admin-c",
	}, now.Add(3*time.Minute))
	require.NoError(t, err)

	verification, err := apps.VerifyPending(context.Background(), app.VerifyPendingCommand{
		CustomerID: application.CustomerID, CustomerAppID: application.ID,
		ExpectedVersion: pending.App.RowVersion, ConfigVersion: pending.Version.ConfigVersion, Actor: "admin-c",
	}, resolver, verifier)
	require.NoError(t, err)
	assert.Equal(t, "verified", verification.Result)
	var authorized model.AppConfigVersion
	require.NoError(t, db.First(&authorized, pending.Version.ID).Error)
	require.NotNil(t, authorized.CredentialChangeApprovalID)
}

func createPendingConfig(t *testing.T, db *gorm.DB, apps *app.Service) (*model.CustomerApp, *model.AppConfigVersion, *model.CredentialProfile) {
	t.Helper()
	createdCustomer, err := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test")).Create(customer.CreateCommand{
		CustomerCode: "customer-one", DisplayName: "Customer One", Actor: "test",
	})
	require.NoError(t, err)
	resolver := providerSecrets()
	profile, err := credential.New(db, resolver).Create(credential.CreateCommand{
		ProviderEnvironment: model.ProviderChinaTencentCloud,
		Name:                "primary", SecretIDRef: "env://WORKBENCH_PROVIDER_SECRET_ID",
		SecretKeyRef: "env://WORKBENCH_PROVIDER_SECRET_KEY", Fingerprint: secrets.CredentialPairFingerprint("resolved-secret-id", "resolved-secret-key"), Actor: "test",
	})
	require.NoError(t, err)
	result, err := apps.SaveConfig(app.SaveConfigCommand{
		CustomerID: createdCustomer.ID, ExpectedVersion: 0,
		ProviderEnvironment: model.ProviderChinaTencentCloud, Region: "ap-guangzhou", SpaceID: "space-1",
		AppID: "app-1", TemplateAgentID: "agent-1", CredentialProfileID: &profile.ID,
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_APP_KEY", AppKeyFingerprint: secrets.AppKeyFingerprint("resolved-app-key"), DisplayName: "Workbench",
		Capabilities: []string{
			productpolicy.CapabilityChat,
			productpolicy.CapabilityCatalogModels,
			productpolicy.CapabilityCatalogSkills,
			productpolicy.CapabilityCatalogPlugins,
		},
		Limits: productpolicy.Limits{
			CustomerConcurrency: 10, UserConcurrency: 1, MaxRuntimeSeconds: 900,
			MaxReasoningRounds: 20, MaxOutputTokens: 4096, WebSearchPerTurn: 0, MaxFileBytes: 0,
		},
		Actor: "test",
	})
	require.NoError(t, err)
	return result.App, result.Version, profile
}
