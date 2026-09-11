package resellerhub

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestValidateQuotaCarrierRejectsAgencyManagedUser(t *testing.T) {
	db := openServiceTestDB(t)
	user := model.User{
		Username:    "agency-managed-carrier",
		Password:    "not-used-password",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		BillingMode: model.AgencyDurableBillingMode,
	}
	require.NoError(t, db.Create(&user).Error)

	app := New(db, db, Config{})
	_, err := app.validateQuotaCarrier(user.Id, 0)
	require.ErrorContains(t, err, "agency-managed")
}
