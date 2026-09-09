package workbenchbridge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchFeatureFlagDefaultsToDisabled(t *testing.T) {
	t.Setenv("WORKBENCH_ENABLED", "")
	t.Setenv("WORKBENCH_CONTROL_URL", "")
	t.Setenv("WORKBENCH_CONTROL_HMAC_SECRET", "")
	t.Setenv("WORKBENCH_SERVICE_HMAC_SECRET", "")

	config := ConfigFromEnvironment()

	assert.False(t, config.Enabled)
	require.NoError(t, config.Validate())
}

func TestControlAndInternalServiceCredentialsAreIndependent(t *testing.T) {
	config := Config{
		Enabled:             true,
		ControlURL:          "https://claw-control.internal",
		ControlHMACSecret:   []byte("control-hmac-secret-0123456789abcdef"),
		ControlServiceName:  "new-api-core",
		ControlTimeout:      time.Second,
		ServiceHMACSecret:   []byte("service-hmac-secret-0123456789abcdef"),
		InternalRequestSkew: time.Minute,
	}
	require.NoError(t, config.Validate())

	missingControlCredential := config
	missingControlCredential.ControlHMACSecret = nil
	assert.ErrorIs(t, missingControlCredential.ValidateTicketIssuer(), ErrInvalidConfiguration)
	require.NoError(t, missingControlCredential.ValidateInternalAuth())

	missingInternalCredential := config
	missingInternalCredential.ServiceHMACSecret = nil
	require.NoError(t, missingInternalCredential.ValidateTicketIssuer())
	assert.ErrorIs(t, missingInternalCredential.ValidateInternalAuth(), ErrInvalidConfiguration)

	reusedCredential := config
	reusedCredential.ServiceHMACSecret = append([]byte(nil), config.ControlHMACSecret...)
	assert.ErrorIs(t, reusedCredential.Validate(), ErrInvalidConfiguration)
}

func TestControlURLRejectsCredentialsAndPathPrefixes(t *testing.T) {
	base := Config{
		Enabled:            true,
		ControlURL:         "https://claw-control.internal",
		ControlHMACSecret:  []byte("control-hmac-secret-0123456789abcdef"),
		ControlServiceName: "new-api-core",
		ControlTimeout:     time.Second,
	}

	for _, invalidURL := range []string{
		"https://user:password@claw-control.internal",
		"https://claw-control.internal/prefix",
		"https://claw-control.internal?target=other",
		"https://claw-control.internal#fragment",
	} {
		config := base
		config.ControlURL = invalidURL
		assert.ErrorIs(t, config.ValidateTicketIssuer(), ErrInvalidConfiguration, invalidURL)
	}
}

func TestControlURLRequiresHTTPSUnlessDevelopmentEscapeHatchIsExplicit(t *testing.T) {
	config := Config{
		Enabled: true, ControlURL: "http://claw-control.internal",
		ControlHMACSecret:  []byte("control-hmac-secret-0123456789abcdef"),
		ControlServiceName: "new-api-core", ControlTimeout: time.Second,
	}
	assert.ErrorIs(t, config.ValidateTicketIssuer(), ErrInvalidConfiguration)
	config.AllowInsecureControlHTTP = true
	require.NoError(t, config.ValidateTicketIssuer())
}
