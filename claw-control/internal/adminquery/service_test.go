package adminquery_test

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/adminquery"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomerDetailProjectsSecretReferencesOutOfAdminResponses(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	created, err := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test")).Create(customer.CreateCommand{CustomerCode: "customer-one", DisplayName: "Customer One", Actor: "test"})
	require.NoError(t, err)
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_SECRET_ID": "secret-id", "env://WORKBENCH_PROVIDER_SECRET_KEY": "secret-key",
		"env://WORKBENCH_PROVIDER_APP_KEY": "app-key",
	}
	profile, err := credential.New(db, resolver).Create(credential.CreateCommand{
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "primary",
		SecretIDRef: "env://WORKBENCH_PROVIDER_SECRET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_SECRET_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("secret-id", "secret-key"), Actor: "test",
	})
	require.NoError(t, err)
	_, err = app.New(db, false, resolver).SaveConfig(app.SaveConfigCommand{
		CustomerID: created.ID, ProviderEnvironment: model.ProviderChinaTencentCloud,
		Region: "ap-guangzhou", SpaceID: "space-1", AppID: "app-1", TemplateAgentID: "agent-1",
		CredentialProfileID: &profile.ID, AppKeySecretRef: "env://WORKBENCH_PROVIDER_APP_KEY",
		AppKeyFingerprint: secrets.AppKeyFingerprint("app-key"), DisplayName: "Workbench", Capabilities: []string{productpolicy.CapabilityChat},
		Limits: productpolicy.Limits{CustomerConcurrency: 10, UserConcurrency: 1, MaxRuntimeSeconds: 900, MaxReasoningRounds: 20, MaxOutputTokens: 4096},
		Actor:  "test",
	})
	require.NoError(t, err)

	detail, err := adminquery.New(db).CustomerDetail(created.ID)
	require.NoError(t, err)
	encoded, err := jsonx.Marshal(detail)
	require.NoError(t, err)
	response := string(encoded)
	assert.NotContains(t, response, "WORKBENCH_PROVIDER_APP_KEY")
	assert.NotContains(t, response, "WORKBENCH_PROVIDER_SECRET_ID")
	assert.NotContains(t, response, secrets.AppKeyFingerprint("app-key"))

	profiles, err := adminquery.New(db).CredentialProfiles(10, nil)
	require.NoError(t, err)
	encoded, err = jsonx.Marshal(profiles)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(encoded), "WORKBENCH_PROVIDER_"))
}

func TestAppViewsPageReturnsEachAppsOwnConfigurationNumbers(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	created, err := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test")).Create(customer.CreateCommand{CustomerCode: "customer-app-list", DisplayName: "Customer App List", Actor: "test"})
	require.NoError(t, err)
	primary := model.CustomerApp{CustomerID: created.ID, Slot: "primary", Alias: "primary", ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "app-primary", DisplayName: "Primary", Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 2}
	require.NoError(t, db.Create(&primary).Error)
	additional := model.CustomerApp{CustomerID: created.ID, Slot: "app:additional", Alias: "additional", ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "app-additional", DisplayName: "Additional", Status: model.AppStatusDraft, AuthEpoch: 1, RowVersion: 1}
	require.NoError(t, db.Create(&additional).Error)
	current := model.AppConfigVersion{CustomerAppID: primary.ID, ConfigVersion: 4, Status: model.AppConfigStatusVerified, Region: "ap-guangzhou", SpaceID: "default_space", AppKeySecretRef: "sealed://primary", AppKeyFingerprint: "sha256:primary", AppKeyFingerprintVersion: 1, RowVersion: 1, LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test"}
	require.NoError(t, db.Create(&current).Error)
	pending := model.AppConfigVersion{CustomerAppID: additional.ID, ConfigVersion: 1, Status: model.AppConfigStatusDraft, Region: "ap-guangzhou", SpaceID: "default_space", AppKeySecretRef: "sealed://additional", AppKeyFingerprint: "sha256:additional", AppKeyFingerprintVersion: 1, RowVersion: 1, LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test"}
	require.NoError(t, db.Create(&pending).Error)
	require.NoError(t, db.Model(&primary).Updates(map[string]any{"current_config_version_id": current.ID}).Error)
	require.NoError(t, db.Model(&additional).Updates(map[string]any{"pending_config_version_id": pending.ID}).Error)

	page, err := adminquery.New(db).AppViewsPage(created.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	byID := map[uint64]adminquery.CustomerAppListView{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	require.NotNil(t, byID[primary.ID].CurrentConfigVersion)
	assert.Equal(t, int64(4), *byID[primary.ID].CurrentConfigVersion)
	assert.Nil(t, byID[primary.ID].PendingConfigVersion)
	require.NotNil(t, byID[additional.ID].PendingConfigVersion)
	assert.Equal(t, int64(1), *byID[additional.ID].PendingConfigVersion)
	assert.Nil(t, byID[additional.ID].CurrentConfigVersion)
}
