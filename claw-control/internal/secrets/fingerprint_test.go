package secrets_test

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalProviderFingerprintsAreDomainSeparatedAndVerified(t *testing.T) {
	credentialFingerprint := secrets.CredentialPairFingerprint("secret-id", "secret-key")
	appFingerprint := secrets.AppKeyFingerprint("secret-idsecret-key")
	assert.Equal(t, "sha256:6838322bda56c649f87020943f9bb7dedb2725c5d118f2426880abdedf11f104", credentialFingerprint)
	assert.Equal(t, "sha256:b981858ee8f25c6b2a99e0011149eab6b2aaf0b4e424e7635e324e51ec8c7dbf", appFingerprint)
	assert.NotEqual(t, credentialFingerprint, appFingerprint)

	resolver := testutil.SecretResolver{"env://WORKBENCH_PROVIDER_ID": "secret-id", "env://WORKBENCH_PROVIDER_KEY": "secret-key", "env://WORKBENCH_PROVIDER_APP": "secret-idsecret-key"}
	pair, err := secrets.ResolveCredentialPair(resolver, "env://WORKBENCH_PROVIDER_ID", "env://WORKBENCH_PROVIDER_KEY", credentialFingerprint, secrets.CanonicalFingerprintVersion)
	require.NoError(t, err)
	assert.Equal(t, "secret-id", pair.SecretID)
	appKey, err := secrets.ResolveAppKey(resolver, "env://WORKBENCH_PROVIDER_APP", appFingerprint, secrets.CanonicalFingerprintVersion)
	require.NoError(t, err)
	assert.Equal(t, "secret-idsecret-key", appKey)

	_, err = secrets.ResolveCredentialPair(resolver, "env://WORKBENCH_PROVIDER_ID", "env://WORKBENCH_PROVIDER_KEY", appFingerprint, secrets.CanonicalFingerprintVersion)
	assert.ErrorIs(t, err, secrets.ErrProviderFingerprintInvalid)
	_, err = secrets.ResolveAppKey(resolver, "env://WORKBENCH_PROVIDER_MISSING", appFingerprint, secrets.CanonicalFingerprintVersion)
	assert.True(t, errors.Is(err, secrets.ErrProviderSecretUnavailable))
	assert.NotContains(t, err.Error(), "WORKBENCH_PROVIDER")
}
