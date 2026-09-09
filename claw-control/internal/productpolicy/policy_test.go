package productpolicy_test

import (
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCapabilitiesUsesFixedAllowlist(t *testing.T) {
	capabilities, err := productpolicy.NormalizeCapabilities([]string{" Files ", "CHAT", "files"})
	require.NoError(t, err)
	assert.Equal(t, []string{"chat", "files"}, capabilities)

	_, err = productpolicy.NormalizeCapabilities([]string{"chat", "provider_admin"})
	assert.ErrorContains(t, err, "unsupported capability")

	_, err = productpolicy.NormalizeCapabilities([]string{"chat", " "})
	assert.ErrorContains(t, err, "capability cannot be empty")
}

func TestFileLimitMatchesADPEnforcementBoundary(t *testing.T) {
	limits := productpolicy.Limits{
		CustomerConcurrency: 1, UserConcurrency: 1, MaxRuntimeSeconds: 60,
		MaxReasoningRounds: 1, MaxOutputTokens: 1, MaxFileBytes: productpolicy.MaxFileBytes,
	}
	require.NoError(t, productpolicy.ValidateLimits(limits))
	limits.MaxFileBytes++
	assert.Error(t, productpolicy.ValidateLimits(limits))
}

func TestCatalogCapabilitiesAreReadOnlyAndIndependentFromExecution(t *testing.T) {
	catalog := []string{
		productpolicy.CapabilityCatalogModels,
		productpolicy.CapabilityCatalogSkills,
		productpolicy.CapabilityCatalogPlugins,
	}
	capabilities, err := productpolicy.NormalizeCapabilities(catalog)
	require.NoError(t, err)
	assert.ElementsMatch(t, catalog, capabilities)
	for _, capability := range catalog {
		assert.True(t, productpolicy.IsCatalogReadCapability(capability))
		assert.False(t, productpolicy.IsExecutionCapability(capability), "catalog visibility must not grant tool, connector, OAuth, or write execution")
	}
	for _, capability := range []string{
		productpolicy.CapabilityTools, productpolicy.CapabilityConnectors,
		productpolicy.CapabilityOAuth, productpolicy.CapabilityScheduledTasks,
		productpolicy.CapabilitySandbox,
	} {
		assert.True(t, productpolicy.IsExecutionCapability(capability))
		assert.False(t, productpolicy.IsCatalogReadCapability(capability))
	}
}
