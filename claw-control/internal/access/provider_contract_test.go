package access

import (
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestProviderEnvironmentMapsToADPResolverContract(t *testing.T) {
	testCases := []struct {
		environment string
		vendor      string
		ok          bool
	}{
		{environment: model.ProviderChinaTencentCloud, vendor: "ChinaTencentCloud", ok: true},
		{environment: model.ProviderChinaTencentADP, vendor: "ChinaTencentADP", ok: true},
		{environment: "arbitrary", vendor: "", ok: false},
	}
	for _, testCase := range testCases {
		vendor, ok := providerServiceVendor(testCase.environment)
		assert.Equal(t, testCase.vendor, vendor)
		assert.Equal(t, testCase.ok, ok)
	}
}
