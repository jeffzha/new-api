package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetWalletBalanceBreakdownClassifiesAvailableFundingAndReconcilesQuota(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		db := agencyDialectDB(t, dialect)
		if db == nil {
			t.Logf("dialect %s not configured; skipped", dialect)
			continue
		}
		t.Run(dialect, func(t *testing.T) {
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &TopUp{}))
			require.NoError(t, MigrateAgency(db))
			previousDB := DB
			DB = db
			t.Cleanup(func() { DB = previousDB })

			lots := []AgencyFundingLot{
				{UserID: 42, SourceKind: "payment_self", SourceID: "pay-1", CompletionSource: "payment_callback", PaidAvailable: 100, BonusAvailable: 20},
				{UserID: 42, SourceKind: "quota_grant", SourceID: "grant-1", CompletionSource: "quota_grant", BonusAvailable: 50},
				{UserID: 42, SourceKind: "redemption", SourceID: "code-1", CompletionSource: "redemption", BonusAvailable: 60},
				{UserID: 42, SourceKind: "future_source", SourceID: "future-1", CompletionSource: "future", PaidAvailable: 30},
			}
			for i := range lots {
				lots[i].Version = 1
				lots[i].MoneySeq = int64(i + 1)
				require.NoError(t, db.Create(&lots[i]).Error)
			}

			balances, err := GetWalletBalanceBreakdown(42, 300, AgencyDurableBillingMode)
			require.NoError(t, err)
			assert.Equal(t, []WalletBalance{
				{Type: WalletBalanceTypeRecharge, Quota: 100},
				{Type: WalletBalanceTypeGift, Quota: 70},
				{Type: WalletBalanceTypeRedemption, Quota: 60},
				{Type: WalletBalanceTypeOther, Quota: 70},
			}, balances)
		})
	}
}

func TestGetWalletBalanceBreakdownUsesOtherForUnattributedLegacyQuota(t *testing.T) {
	balances, err := GetWalletBalanceBreakdown(7, 123, "legacy")
	require.NoError(t, err)
	assert.Equal(t, []WalletBalance{
		{Type: WalletBalanceTypeRecharge, Quota: 0},
		{Type: WalletBalanceTypeGift, Quota: 0},
		{Type: WalletBalanceTypeRedemption, Quota: 0},
		{Type: WalletBalanceTypeOther, Quota: 123},
	}, balances)
}

func TestGetWalletBalanceBreakdownOmitsZeroOtherBalance(t *testing.T) {
	db := newAgencyFundingPriorityTestDB(t, "wallet-balance-no-other")
	require.NoError(t, db.Create(&AgencyFundingLot{
		UserID: 9, SourceKind: "payment_self", SourceID: "pay-9", CompletionSource: "payment_callback",
		PaidAvailable: 80, Version: 1, MoneySeq: 1,
	}).Error)

	balances, err := GetWalletBalanceBreakdown(9, 80, AgencyDurableBillingMode)
	require.NoError(t, err)
	assert.Equal(t, []WalletBalance{
		{Type: WalletBalanceTypeRecharge, Quota: 80},
		{Type: WalletBalanceTypeGift, Quota: 0},
		{Type: WalletBalanceTypeRedemption, Quota: 0},
	}, balances)
}

func TestGetWalletBalanceBreakdownReconcilesOverstatedLotsWithoutNegativeCategory(t *testing.T) {
	db := newAgencyFundingPriorityTestDB(t, "wallet-balance-overstated-lots")
	require.NoError(t, db.Create(&AgencyFundingLot{
		UserID: 11, SourceKind: "redemption", SourceID: "code-11", CompletionSource: "redemption",
		BonusAvailable: 70, Version: 1, MoneySeq: 1,
	}).Error)
	require.NoError(t, db.Create(&AgencyFundingLot{
		UserID: 11, SourceKind: "payment_self", SourceID: "pay-11", CompletionSource: "payment_callback",
		PaidAvailable: 50, Version: 1, MoneySeq: 2,
	}).Error)

	balances, err := GetWalletBalanceBreakdown(11, 80, AgencyDurableBillingMode)
	require.NoError(t, err)
	assert.Equal(t, []WalletBalance{
		{Type: WalletBalanceTypeRecharge, Quota: 50},
		{Type: WalletBalanceTypeGift, Quota: 0},
		{Type: WalletBalanceTypeRedemption, Quota: 30},
	}, balances)
}
