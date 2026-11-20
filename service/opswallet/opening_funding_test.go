package opswallet

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpeningUnverifiedLegacyGapPreservesSourceAndDebt(t *testing.T) {
	db := migrationTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.AgencyFundingAccount{}, &model.AgencyFundingLot{}))
	account := model.AgencyFundingAccount{UserID: 16, DebtQuota: 404}
	require.NoError(t, db.Create(&account).Error)
	_, _, err := openingFundingLots(db, 16, 4999596)
	require.Error(t, err)
	t.Setenv("OPS_WALLET_LEGACY_OPENING_GAPS", "retain_unverified")
	lots, debt, err := openingFundingLots(db, 16, 4999596)
	require.NoError(t, err)
	require.Len(t, lots, 1)
	assert.EqualValues(t, 10000000, lots[0].Amount)
	assert.EqualValues(t, 808, debt)
	assert.Equal(t, "legacy_unknown", lots[0].Source)
	assert.False(t, lots[0].Paid)
	assert.False(t, lots[0].PaidBalance)
	var unchanged model.AgencyFundingAccount
	require.NoError(t, db.First(&unchanged, 16).Error)
	assert.Equal(t, account, unchanged)
	_, _, err = openingFundingLots(db, 16, -405)
	require.Error(t, err)
	require.NoError(t, db.Model(&account).Update("reconcile_blocked", true).Error)
	_, _, err = openingFundingLots(db, 16, 4999596)
	require.Error(t, err)
}
