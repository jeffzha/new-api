package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createAgencyExpiryUser(t *testing.T, db *gorm.DB, username string) User {
	t.Helper()
	user := User{Username: username, Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AffCode: username, BillingMode: AgencyDurableBillingMode, FundingVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func addAgencyRedemptionForTest(t *testing.T, db *gorm.DB, user User, sourceID string, quota, expiresAt int64) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyAgencyRedemptionQuotaTx(tx, int64(user.Id), quota, sourceID, int64(user.Id), expiresAt)
	}))
}

func TestAgencyRedemptionLotsConsumeEarliestExpiryFirst(t *testing.T) {
	db := newAgencyFundingPriorityTestDB(t, "agency-redemption-expiry-priority")
	user := createAgencyExpiryUser(t, db, "redemption-expiry-priority")
	now := time.Now().Unix()

	// Creation order deliberately differs from expiry order. Non-expiring
	// redemption credit follows every expiring redemption lot.
	addAgencyRedemptionForTest(t, db, user, "expires-later", 50, now+7200)
	addAgencyRedemptionForTest(t, db, user, "never-expires", 50, 0)
	addAgencyRedemptionForTest(t, db, user, "expires-first", 50, now+3600)

	_, err := TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 120, "", "expiry-priority-charge", 120, true,
		&agencycontract.PricingSnapshot{FundingRuleVersion: agencycontract.FundingRuleVersionV2})
	require.NoError(t, err)
	var allocations []AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "expiry-priority-charge").Order("segment_no ASC").Find(&allocations).Error)
	require.Len(t, allocations, 3)

	var lots []AgencyFundingLot
	require.NoError(t, db.Where("id IN ?", []int64{allocations[0].LotID, allocations[1].LotID, allocations[2].LotID}).Find(&lots).Error)
	byID := make(map[int64]AgencyFundingLot, len(lots))
	for _, lot := range lots {
		byID[lot.ID] = lot
	}
	assert.Equal(t, "expires-first", byID[allocations[0].LotID].SourceID)
	assert.Equal(t, "expires-later", byID[allocations[1].LotID].SourceID)
	assert.Equal(t, "never-expires", byID[allocations[2].LotID].SourceID)
	assert.Equal(t, int64(20), allocations[2].NonpaidConsumed)
}

func TestAgencyRedemptionExpiryRemovesOnlyUnusedFrozenBalance(t *testing.T) {
	db := newAgencyFundingPriorityTestDB(t, "agency-redemption-expiry-sweep")
	require.NoError(t, db.AutoMigrate(&Redemption{}))
	user := createAgencyExpiryUser(t, db, "redemption-expiry-sweep")
	now := time.Now().Unix()

	redemption := Redemption{Name: "frozen-code", Key: "frozen-code-key", Status: common.RedemptionCodeStatusUsed, Quota: 80, ExpiredTime: now - 10}
	require.NoError(t, db.Create(&redemption).Error)
	addAgencyRedemptionForTest(t, db, user, "frozen-code", 80, redemption.ExpiredTime)
	require.NoError(t, db.Model(&redemption).Update("expired_time", now+86400).Error)

	users, expired, err := ExpireAgencyRedemptionLots(now, 100)
	require.NoError(t, err)
	assert.Equal(t, 1, users)
	assert.Equal(t, int64(80), expired)

	var refreshed User
	require.NoError(t, db.First(&refreshed, user.Id).Error)
	assert.Zero(t, refreshed.Quota)
	var account AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	assert.Zero(t, account.NonpaidAvailable)
	var lot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "frozen-code").First(&lot).Error)
	assert.Zero(t, lot.BonusAvailable)
	assert.Equal(t, int64(80), lot.BonusExpired)
	assert.Equal(t, now-10, lot.ExpiresAt)
	var fact AgencyTopupFact
	require.NoError(t, db.Where("source_id = ?", "frozen-code").First(&fact).Error)
	assert.Equal(t, int64(80), fact.ExpiredQuota)

	users, expired, err = ExpireAgencyRedemptionLots(now, 100)
	require.NoError(t, err)
	assert.Zero(t, users)
	assert.Zero(t, expired)
}

func TestAgencyReleaseDoesNotResurrectExpiredRedemption(t *testing.T) {
	db := newAgencyFundingPriorityTestDB(t, "agency-redemption-expired-release")
	user := createAgencyExpiryUser(t, db, "redemption-expired-release")
	addAgencyRedemptionForTest(t, db, user, "release-code", 100, time.Now().Unix()+3600)

	_, err := TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 100, "", "expired-release-charge", 100, true,
		&agencycontract.PricingSnapshot{FundingRuleVersion: agencycontract.FundingRuleVersionV2})
	require.NoError(t, err)
	require.NoError(t, db.Model(&AgencyFundingLot{}).Where("source_id = ?", "release-code").Update("expires_at", time.Now().Unix()-1).Error)

	paid, err := ReleaseUserQuotaAndAgency(user.Id, 100, "expired-release-charge")
	require.NoError(t, err)
	assert.Zero(t, paid)
	var refreshed User
	require.NoError(t, db.First(&refreshed, user.Id).Error)
	assert.Zero(t, refreshed.Quota)
	var account AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	assert.Zero(t, account.NonpaidAvailable)
	var lot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "release-code").First(&lot).Error)
	assert.Zero(t, lot.BonusConsumed)
	assert.Zero(t, lot.BonusAvailable)
	assert.Equal(t, int64(100), lot.BonusExpired)
	var fact AgencyTopupFact
	require.NoError(t, db.Where("source_id = ?", "release-code").First(&fact).Error)
	assert.Equal(t, int64(100), fact.ExpiredQuota)
}
