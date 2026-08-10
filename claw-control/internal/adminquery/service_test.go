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
	assert.Contains(t, response, secrets.AppKeyFingerprint("app-key"))

	profiles, err := adminquery.New(db).CredentialProfiles(10, nil)
	require.NoError(t, err)
	encoded, err = jsonx.Marshal(profiles)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(encoded), "WORKBENCH_PROVIDER_"))
}
