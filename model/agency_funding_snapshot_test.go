package model

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidateAgencyPricingSnapshotAllowsPendingStandardSale(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-pricing-snapshot-pending?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Agency{}, &AgencyPricePolicyVersion{}, &AgencyUserBinding{}, &AgencyActiveUserBinding{}))

	const userID int64 = 901
	require.NoError(t, db.Create(&Agency{ID: 91, Code: "pending", Status: "active", CurrentPolicyVersionID: 911, StateRevision: 3}).Error)
	require.NoError(t, db.Create(&AgencyPricePolicyVersion{ID: 911, AgencyID: 91, Revision: 7, PolicyJSON: "{}"}).Error)
	require.NoError(t, db.Create(&AgencyUserBinding{ID: 912, UserID: userID, AgencyID: 91, Revision: 4}).Error)
	require.NoError(t, db.Create(&AgencyActiveUserBinding{UserID: userID, BindingID: 912, AgencyID: 91, Revision: 4}).Error)

	snapshot := &agencycontract.PricingSnapshot{AgencyID: 91, BindingID: 912, BindingRevision: 4, AgencyStateRevision: 3, PolicyVersionID: 911, PolicyRevision: 7, SalesBPS: 10000, CommissionEligible: false, EligibilityReason: "platform_pricing_pending"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ValidateAgencyPricingSnapshotTx(tx, userID, snapshot)
	}))

	snapshot.SalesBPS = 9000
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
		return ValidateAgencyPricingSnapshotTx(tx, userID, snapshot)
	}), "eligibility snapshot is stale")
}
