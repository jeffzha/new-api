package agencycontract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillingComponentCorrectionSupersedesAnOriginalEvent(t *testing.T) {
	event := componentContractFixture()
	event.EventType = BillingEventCorrected
	event.OriginalEventID = "component-contract-original"
	require.NoError(t, ValidateBillingComponents(event), "a correction carries finalized component amounts")

	missing := event
	missing.OriginalEventID = ""
	assert.Error(t, ValidateBillingComponents(missing), "a correction must name the event it supersedes")

	self := event
	self.OriginalEventID = self.EventID
	assert.Error(t, ValidateBillingComponents(self), "a correction cannot supersede itself")

	unknown := event
	unknown.EventType = "agency.billing_enriched"
	assert.Error(t, ValidateBillingComponents(unknown))
}
