package config_test

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityStatusURLsAreExactAndCredentialScopesAreSeparate(t *testing.T) {
	setValidEnvironment(t)
	_, err := config.Load()
	require.NoError(t, err)

	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "query", key: "CLAW_NEW_API_IDENTITY_STATUS_URL", value: "https://gateway.example.com/api/internal/workbench/identity-status?next=other"},
		{name: "userinfo", key: "CLAW_NEW_API_IDENTITY_STATUS_URL", value: "https://user@gateway.example.com/api/internal/workbench/identity-status"},
		{name: "wrong admin path", key: "CLAW_NEW_API_ADMIN_IDENTITY_STATUS_URL", value: "https://gateway.example.com/api/internal/workbench/identity-status"},
		{name: "secret reuse", key: "CLAW_NEW_API_IDENTITY_STATUS_HMAC_SECRET", value: "0123456789abcdef0123456789abcdef"},
		{name: "service secret reuse", key: "CLAW_INTERNAL_HMAC_KEYS", value: "adp-backend=0123456789abcdef0123456789abcdef,new-api-core=0123456789abcdef0123456789abcdef"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(testCase.key, testCase.value)
			_, err := config.Load()
			assert.Error(t, err)
		})
	}
}

func TestProductionRejectsSelfReportedProviderVerification(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("CLAW_ALLOW_UNTRUSTED_PROVIDER_VERIFICATION", "true")

	_, err := config.Load()
	assert.ErrorContains(t, err, "forbidden in production")
}

func TestEvidenceKeyAndScannerConfigurationAreStrict(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "short evidence key", key: "CLAW_EVIDENCE_MASTER_KEY", value: "c2hvcnQ="},
		{name: "reused HMAC key", key: "CLAW_EVIDENCE_MASTER_KEY", value: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="},
		{name: "invalid scanner address", key: "CLAW_EVIDENCE_CLAMAV_ADDR", value: "clamav-without-port"},
		{name: "oversized evidence ceiling", key: "CLAW_EVIDENCE_MAX_BYTES", value: "104857601"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(testCase.key, testCase.value)
			_, err := config.Load()
			assert.Error(t, err)
		})
	}
}

func TestRedirectPathsMustBeCanonicalLocalPaths(t *testing.T) {
	setValidEnvironment(t)
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "scheme relative", key: "CLAW_ADP_SSO_REDIRECT_PATH", value: "//evil.example/sso"},
		{name: "backslash", key: "CLAW_ADP_SSO_REDIRECT_PATH", value: `/\evil.example/sso`},
		{name: "query", key: "CLAW_ADP_SSO_REDIRECT_PATH", value: "/workbench/auth/sso?next=//evil.example"},
		{name: "fragment", key: "CLAW_ADMIN_REDIRECT_PATH", value: "/workbench/admin#other"},
		{name: "encoded path", key: "CLAW_ADMIN_REDIRECT_PATH", value: "/workbench/%61dmin"},
		{name: "unclean path", key: "CLAW_ADMIN_REDIRECT_PATH", value: "/workbench/../admin"},
		{name: "different SSO path", key: "CLAW_ADP_SSO_REDIRECT_PATH", value: "/workbench/auth/other"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(testCase.key, testCase.value)
			_, err := config.Load()
			assert.Error(t, err)
		})
	}
}

func TestMetricsListenerIsDisabledByDefaultAndRequiresAValidAddress(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("CLAW_METRICS_ADDR", "")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.MetricsAddr)

	t.Setenv("CLAW_METRICS_ADDR", ":9090")
	cfg, err = config.Load()
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.MetricsAddr)

	t.Setenv("CLAW_METRICS_ADDR", "https://public.example/metrics")
	_, err = config.Load()
	assert.ErrorContains(t, err, "CLAW_METRICS_ADDR")
}

func TestRetentionCoordinatorRequiresExactPrivateEndpointWhenEnabled(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("CLAW_RETENTION_COORDINATOR_INTERVAL", "5s")
	t.Setenv("CLAW_ADP_RETENTION_URL", "https://workbench-control.internal:8443/api/internal/workbench/retention/intents")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, cfg.RetentionCoordinatorInterval)

	for _, value := range []string{
		"http://workbench-control.internal:8443/api/internal/workbench/retention/intents",
		"https://user@workbench-control.internal:8443/api/internal/workbench/retention/intents",
		"https://workbench-control.internal:8443/api/internal/workbench/retention/intents?customer=7",
		"https://workbench-control.internal:8443/api/internal/workbench/retention/*",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CLAW_ADP_RETENTION_URL", value)
			_, loadErr := config.Load()
			assert.ErrorContains(t, loadErr, "CLAW_ADP_RETENTION_URL")
		})
	}
}

func TestTencentBillingImportIsDisabledByDefaultAndRequiresServerOnlyCredentials(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.False(t, cfg.BillingImportEnabled)
	assert.Empty(t, cfg.TencentBillingSecretID)

	t.Setenv("CLAW_TENCENT_BILLING_IMPORT_ENABLED", "true")
	_, err = config.Load()
	assert.ErrorContains(t, err, "SecretId")

	t.Setenv("CLAW_TENCENT_BILLING_SECRET_ID", "AKIDEXAMPLE")
	t.Setenv("CLAW_TENCENT_BILLING_SECRET_KEY", "SECRETKEYEXAMPLE-0123456789")
	t.Setenv("CLAW_TENCENT_BILLING_PAYER_UIN", "10001")
	cfg, err = config.Load()
	require.NoError(t, err)
	assert.True(t, cfg.BillingImportEnabled)
	assert.Equal(t, 300, cfg.BillingImportPageSize)

	for _, testCase := range []struct{ key, value string }{
		{key: "CLAW_TENCENT_BILLING_IMPORT_ENABLED", value: "yes"},
		{key: "CLAW_TENCENT_BILLING_PAYER_UIN", value: "payer-10001"},
		{key: "CLAW_TENCENT_BILLING_PAGE_SIZE", value: "301"},
		{key: "CLAW_TENCENT_BILLING_MAX_RECORDS", value: "200001"},
		{key: "CLAW_TENCENT_BILLING_MAX_RESPONSE_BYTES", value: "16777217"},
	} {
		t.Run(testCase.key, func(t *testing.T) {
			t.Setenv(testCase.key, testCase.value)
			_, loadErr := config.Load()
			assert.Error(t, loadErr)
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CLAW_ADMIN_TOKEN", "admin-bootstrap-secret-0123456789")
	t.Setenv("CLAW_INTERNAL_HMAC_KEYS", "adp-backend=0123456789abcdef0123456789abcdef,new-api-core=abcdef0123456789abcdef0123456789")
	t.Setenv("CLAW_NEW_API_SERVICE_NAME", "new-api-core")
	t.Setenv("CLAW_ADP_SERVICE_NAME", "adp-backend")
	t.Setenv("CLAW_ENVIRONMENT", "prod")
	t.Setenv("CLAW_NEW_API_IDENTITY_STATUS_URL", "https://gateway.example.com/api/internal/workbench/identity-status")
	t.Setenv("CLAW_NEW_API_ADMIN_IDENTITY_STATUS_URL", "https://gateway.example.com/api/internal/workbench/admin-identity-status")
	t.Setenv("CLAW_NEW_API_IDENTITY_STATUS_HMAC_SECRET", "fedcba9876543210fedcba9876543210")
	t.Setenv("CLAW_EVIDENCE_MASTER_KEY", "cXdlcnR5dWlvcGFzZGZnaGprbHp4Y3Zibm0xMjM0NTY=")
	t.Setenv("CLAW_REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("CLAW_REDIS_PASSWORD", "redis-test-password")
}
