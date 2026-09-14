package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAgencyFundingPriorityTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &TopUp{}))
	require.NoError(t, MigrateAgency(db))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	return db
}

func TestAgencyFundingConsumptionPriorityTracksRedemptionAdminAndWallet(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-funding-priority?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &TopUp{}))
	require.NoError(t, MigrateAgency(db))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	user := User{Username: "funding-priority-user", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1}
	require.NoError(t, db.Create(&user).Error)

	// Redemption and administrator adjustments are non-paid lots. A paid
	// checkout is added last so the three sources can be observed separately.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := ApplyAgencyQuotaDeltaWithSourceTx(tx, int64(user.Id), 100, "redemption", "redemption-42"); err != nil {
			return err
		}
		return ApplyAgencyQuotaDeltaWithSourceTx(tx, int64(user.Id), 100, "admin_grant", "admin-op-7")
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment_user", "wallet-payment-1", "payment_callback", 100, 0); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 100)).Error
	}))

	// The first 150 quota must spend redemption before admin grant, without
	// touching paid wallet funds.
	var paid int64
	paid, err = TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 150, "", "priority-charge-1", 150, true, &agencycontract.PricingSnapshot{FundingRuleVersion: agencycontract.FundingRuleVersionV2})
	require.NoError(t, err)
	assert.Zero(t, paid)
	var first []AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "priority-charge-1").Order("segment_no ASC").Find(&first).Error)
	require.Len(t, first, 2)
	assert.Equal(t, int64(100), first[0].NonpaidConsumed)
	assert.Equal(t, int64(50), first[1].NonpaidConsumed)
	var firstLots []AgencyFundingLot
	require.NoError(t, db.Where("id IN ?", []int64{first[0].LotID, first[1].LotID}).Order("money_seq ASC").Find(&firstLots).Error)
	require.Len(t, firstLots, 2)
	assert.Equal(t, "redemption", firstLots[0].SourceKind)
	assert.Equal(t, "admin_grant", firstLots[1].SourceKind)

	// The next 100 spends the remaining admin grant before the paid wallet.
	paid, err = TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 100, "", "priority-charge-2", 100, true, &agencycontract.PricingSnapshot{FundingRuleVersion: agencycontract.FundingRuleVersionV2})
	require.NoError(t, err)
	assert.Equal(t, int64(50), paid)
	var second []AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "priority-charge-2").Order("segment_no ASC").Find(&second).Error)
	require.Len(t, second, 2)
	assert.Equal(t, int64(50), second[0].NonpaidConsumed)
	assert.Equal(t, int64(50), second[1].Consumed)
}

func TestLegacyOpeningPaidAllocationRemainsPaid(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-legacy-opening-paid?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, MigrateAgency(db))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	user := User{Username: "legacy-opening-paid-user", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 100, Version: 1, MoneySeq: 1}).Error)
	require.NoError(t, db.Create(&AgencyFundingLot{UserID: int64(user.Id), SourceKind: "legacy_unknown", SourceID: "opening-1", CompletionSource: "migration", PaidInitial: 100, PaidAvailable: 100, MoneySeq: 1, Version: 1, CreatedAt: 1}).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := ReserveAgencyFundingTx(tx, int64(user.Id), "legacy-charge-1", 40)
		return err
	}))

	var allocation AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "legacy-charge-1").First(&allocation).Error)
	require.Equal(t, int64(40), allocation.Consumed)
	require.Zero(t, allocation.NonpaidConsumed)
	var account AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(60), account.PaidAvailable)
	require.Zero(t, account.NonpaidAvailable)
}

func TestFundingRuleVersionPreservesV1AndFreezesV2(t *testing.T) {
	db := newAgencyFundingPriorityTestDB(t, "agency-funding-rule-version")

	createWallet := func(username string) User {
		user := User{Username: username, Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: username, BillingMode: AgencyDurableBillingMode, FundingVersion: 1}
		require.NoError(t, db.Create(&user).Error)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			if err := ApplyAgencyQuotaDeltaWithSourceTx(tx, int64(user.Id), 100, "admin_grant", username+"-grant"); err != nil {
				return err
			}
			if err := RecordAgencyTopup(tx, int64(user.Id), "payment_self", username+"-payment", "payment_callback", 100, 0); err != nil {
				return err
			}
			return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 100)).Error
		}))
		return user
	}

	v1User := createWallet("funding-rule-v1-user")
	v1Snapshot := &agencycontract.PricingSnapshot{FundingRuleVersion: agencycontract.FundingRuleVersionV1}
	paid, err := TryReserveAgencyWalletAndTokenWithSnapshot(v1User.Id, 0, 50, "", "funding-rule-v1-charge", 50, true, v1Snapshot)
	require.NoError(t, err)
	assert.Equal(t, int64(50), paid)
	var v1Allocation AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "funding-rule-v1-charge").First(&v1Allocation).Error)
	assert.Equal(t, int64(50), v1Allocation.Consumed)
	assert.Zero(t, v1Allocation.NonpaidConsumed)

	v2User := createWallet("funding-rule-v2-user")
	v2Snapshot := &agencycontract.PricingSnapshot{FundingRuleVersion: agencycontract.FundingRuleVersionV2}
	paid, err = TryReserveAgencyWalletAndTokenWithSnapshot(v2User.Id, 0, 50, "", "funding-rule-v2-charge", 50, true, v2Snapshot)
	require.NoError(t, err)
	assert.Zero(t, paid)
	var v2Allocation AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "funding-rule-v2-charge").First(&v2Allocation).Error)
	assert.Zero(t, v2Allocation.Consumed)
	assert.Equal(t, int64(50), v2Allocation.NonpaidConsumed)

	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ? AND segment_no = 0", "funding-rule-v2-charge").First(&journal).Error)
	var frozen agencycontract.PricingSnapshot
	require.NoError(t, common.UnmarshalJsonStr(journal.PricingSnapshot, &frozen))
	assert.Equal(t, agencycontract.FundingRuleVersionV2, frozen.FundingRuleVersion)
}
