package access

import (
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestViewerIsReadOnlyEvenWhenTheAppAndPlanAreActive(t *testing.T) {
	testCases := []struct {
		name             string
		appStatus        string
		role             string
		hasCurrentPeriod bool
		expected         string
	}{
		{name: "member active", appStatus: model.AppStatusActive, role: "member", hasCurrentPeriod: true, expected: "active"},
		{name: "viewer read only", appStatus: model.AppStatusActive, role: "viewer", hasCurrentPeriod: true, expected: "readonly"},
		{name: "expired plan", appStatus: model.AppStatusActive, role: "owner", hasCurrentPeriod: false, expected: "readonly"},
		{name: "suspended App", appStatus: model.AppStatusSuspended, role: "owner", hasCurrentPeriod: true, expected: "readonly"},
		{name: "disabled App", appStatus: model.AppStatusDisabled, role: "owner", hasCurrentPeriod: true, expected: "disabled"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, effectiveAccessMode(testCase.appStatus, testCase.role, testCase.hasCurrentPeriod))
		})
	}
}
