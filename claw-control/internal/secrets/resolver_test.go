package secrets_test

import (
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvironmentResolverOnlyReadsProviderNamespace(t *testing.T) {
	t.Setenv("WORKBENCH_PROVIDER_CUSTOMER_APP_KEY", "provider-secret")
	t.Setenv("CLAW_ADMIN_TOKEN", "control-secret")
	resolver := secrets.EnvironmentResolver{}

	value, err := resolver.Resolve("env://WORKBENCH_PROVIDER_CUSTOMER_APP_KEY")
	require.NoError(t, err)
	assert.Equal(t, "provider-secret", value)

	for _, reference := range []string{
		"env://CLAW_ADMIN_TOKEN",
		"env://WORKBENCH_SERVICE_HMAC_SECRET",
		"env://DATABASE_URL",
		"env://workbench_provider_customer_app_key",
		"env://WORKBENCH_PROVIDER_",
		"env://WORKBENCH_PROVIDER_CUSTOMER-APP-KEY",
	} {
		_, err := resolver.Resolve(reference)
		assert.Error(t, err, reference)
	}
}
