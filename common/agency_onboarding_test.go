package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgencyOnboardingRequiresExplicitValidOptIn(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", false}, {"false", false}, {"tru", false}, {"0", false}, {"true", true}, {"  TRUE  ", true}, {"1", true}} {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv("AGENCY_ONBOARDING_ENABLED", tc.value)
			assert.Equal(t, tc.want, AgencyOnboardingEnabled())
		})
	}
}
