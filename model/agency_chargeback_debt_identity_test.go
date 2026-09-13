package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func agencyChargebackDebtIdentityFixture(t *testing.T, connection *gorm.DB, amounts ...int64) (*gorm.DB, User, []AgencyFundingAllocation) {
	t.Helper()
	db := connection.Begin()
	require.NoError(t, db.Error)
	t.Cleanup(func() { require.NoError(t, db.Rollback().Error) })
	var total int64
	for _, amount := range amounts {
		total += amount
	}
	user := User{Username: "debt-identity-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: int(total)}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), "payment", "original-payment", "payment_callback", total, 0)
	}))
	for i, amount := range amounts {
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			_, err := TryReserveUserQuotaAndAgencyTx(tx, user.Id, int(amount), fmt.Sprintf("charge-%d", i), amount)
			return err
		}))
	}
	var allocations []AgencyFundingAllocation
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&allocations).Error)
	require.Len(t, allocations, len(amounts))
	return db, user, allocations
}

func agencyDebtIdentityTopup(t *testing.T, db *gorm.DB, user User, source string, amount int64) AgencyFundingLot {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", source, "payment_callback", amount, 0); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", amount)).Error
	}))
	var lot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", source).First(&lot).Error)
	return lot
}

func agencyDebtIdentityChargeback(t *testing.T, db *gorm.DB, user User, refundID string, amount int64) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, created, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{UserID: int64(user.Id), SourceOperationID: "original-payment",
			RefundID: refundID, Quota: amount, Reason: "payment_chargeback", EvidenceRef: "verified-bank-receipt"})
		assert.True(t, created)
		return err
	}))
}

func testAgencyChargebackDebtPreservesOtherRepayment(t *testing.T, connection *gorm.DB) {
	for _, repayment := range []struct {
		name string
		a, b int64
	}{
		{name: "first_charge_partially_repaid", a: 40},
		{name: "both_charges_repaid_from_distinct_lots", a: 100, b: 40},
	} {
		t.Run(repayment.name, func(t *testing.T) {
			db, user, allocations := agencyChargebackDebtIdentityFixture(t, connection, 100, 100)
			agencyDebtIdentityChargeback(t, db, user, "original-chargeback", 200)
			var debts []AgencyFundingDebt
			require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&debts).Error)
			require.Len(t, debts, 2)
			for i, debt := range debts {
				require.NotNil(t, debt.AllocationID)
				assert.Equal(t, allocations[i].ID, *debt.AllocationID)
				assert.Equal(t, "original-payment", debt.OriginOperationID)
				assert.Equal(t, int64(100), debt.OriginalQuota)
			}
			lotA := agencyDebtIdentityTopup(t, db, user, "repay-charge-A", repayment.a)
			var lotB AgencyFundingLot
			if repayment.b > 0 {
				lotB = agencyDebtIdentityTopup(t, db, user, "repay-charge-B", repayment.b)
			}
			var repaymentA AgencyDebtRepayment
			require.NoError(t, db.Where("debt_id = ?", debts[0].ID).First(&repaymentA).Error)
			// Cancel B in two installments. Its unrepaid liability and its own
			// repayment are restored; A's earlier repayment must remain intact.
			for _, amount := range []int64{30, 70} {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					_, err := ReleaseUserQuotaAndAgencyTx(tx, user.Id, int(amount), allocations[1].ChargeID)
					return err
				}))
			}
			var storedRepaymentA AgencyDebtRepayment
			require.NoError(t, db.First(&storedRepaymentA, repaymentA.ID).Error)
			assert.Equal(t, repaymentA, storedRepaymentA)
			require.NoError(t, db.First(&lotA, lotA.ID).Error)
			assert.Equal(t, repayment.a, lotA.PaidDebtRepaid)
			assert.Zero(t, lotA.PaidAvailable)
			if repayment.b > 0 {
				require.NoError(t, db.First(&lotB, lotB.ID).Error)
				assert.Equal(t, repayment.b, lotB.PaidAvailable)
				assert.Zero(t, lotB.PaidDebtRepaid)
			}
			require.NoError(t, db.First(&debts[0], debts[0].ID).Error)
			require.NoError(t, db.First(&debts[1], debts[1].ID).Error)
			assert.Equal(t, int64(100)-repayment.a, debts[0].OutstandingQuota)
			assert.Zero(t, debts[0].ReversedQuota)
			assert.Zero(t, debts[1].OutstandingQuota)
			assert.Equal(t, int64(100), debts[1].ReversedQuota)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				_, err := ReleaseUserQuotaAndAgencyTx(tx, user.Id, 100, allocations[0].ChargeID)
				return err
			}))
			require.NoError(t, db.First(&lotA, lotA.ID).Error)
			assert.Equal(t, repayment.a, lotA.PaidAvailable)
			assert.Zero(t, lotA.PaidDebtRepaid)
			var account AgencyFundingAccount
			var wallet User
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
			require.NoError(t, db.First(&wallet, user.Id).Error)
			assert.Zero(t, account.DebtQuota)
			assert.Equal(t, repayment.a+repayment.b, account.PaidAvailable)
			assert.Equal(t, repayment.a+repayment.b, int64(wallet.Quota))
		})
	}
}

func testAgencyChargebackDebtAdoptsSingleLegacyRow(t *testing.T, connection *gorm.DB) {
	db, user, allocations := agencyChargebackDebtIdentityFixture(t, connection, 100)
	agencyDebtIdentityChargeback(t, db, user, "partial-one", 40)
	var debt AgencyFundingDebt
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&debt).Error)
	// Simulate the historical schema: the one row is fully repaid, but still
	// carries the original liability and must not be replaced by another row.
	require.NoError(t, db.Model(&debt).Update("allocation_id", nil).Error)
	lot := agencyDebtIdentityTopup(t, db, user, "legacy-repayment", 40)
	agencyDebtIdentityChargeback(t, db, user, "partial-two", 20)
	var restoredDebt AgencyFundingDebt
	require.NoError(t, db.First(&restoredDebt, debt.ID).Error)
	require.NotNil(t, restoredDebt.AllocationID)
	assert.Equal(t, allocations[0].ID, *restoredDebt.AllocationID)
	assert.Equal(t, int64(60), restoredDebt.OriginalQuota)
	assert.Equal(t, int64(20), restoredDebt.OutstandingQuota)
	var debtCount int64
	require.NoError(t, db.Model(&AgencyFundingDebt{}).Where("user_id = ?", user.Id).Count(&debtCount).Error)
	assert.Equal(t, int64(1), debtCount)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := ReleaseUserQuotaAndAgencyTx(tx, user.Id, 100, allocations[0].ChargeID)
		return err
	}))
	require.NoError(t, db.First(&lot, lot.ID).Error)
	assert.Equal(t, int64(40), lot.PaidAvailable)
	assert.Zero(t, lot.PaidDebtRepaid)
	var originalLot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "original-payment").First(&originalLot).Error)
	assert.Equal(t, int64(40), originalLot.PaidAvailable)
	assert.Equal(t, int64(60), originalLot.PaidRevoked)
}

func testAgencyChargebackDebtRejectsAmbiguousLegacyPool(t *testing.T, connection *gorm.DB) {
	db, user, allocations := agencyChargebackDebtIdentityFixture(t, connection, 100, 100)
	agencyDebtIdentityChargeback(t, db, user, "partial-one", 150)
	var debts []AgencyFundingDebt
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&debts).Error)
	require.Len(t, debts, 2)
	// Reconstruct the old pooled layout before making repayments. The test
	// data now has one source-level liability for two retained allocations.
	require.NoError(t, db.Model(&debts[0]).Updates(map[string]any{"allocation_id": nil, "original_quota": 150, "outstanding_quota": 150}).Error)
	require.NoError(t, db.Delete(&debts[1]).Error)
	agencyDebtIdentityTopup(t, db, user, "legacy-pooled-repayment", 100)
	var beforeAccount AgencyFundingAccount
	var beforeDebt AgencyFundingDebt
	var beforeUser User
	var beforeAllocations []AgencyFundingAllocation
	var beforeLots []AgencyFundingLot
	var beforeRepayments []AgencyDebtRepayment
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&beforeAccount).Error)
	require.NoError(t, db.First(&beforeDebt, debts[0].ID).Error)
	require.NoError(t, db.First(&beforeUser, user.Id).Error)
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&beforeAllocations).Error)
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&beforeLots).Error)
	require.NoError(t, db.Order("id").Find(&beforeRepayments).Error)
	for _, action := range []string{"cancel", "another_partial_chargeback"} {
		t.Run(action, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				if action == "cancel" {
					_, err := ReleaseUserQuotaAndAgencyTx(tx, user.Id, 100, allocations[1].ChargeID)
					return err
				}
				_, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{UserID: int64(user.Id), SourceOperationID: "original-payment",
					RefundID: "ambiguous-partial-two", Quota: 20, Reason: "payment_chargeback", EvidenceRef: "verified-bank-receipt"})
				return err
			})
			require.ErrorIs(t, err, ErrAgencyComponentRefundProvenance)
			var account AgencyFundingAccount
			var debt AgencyFundingDebt
			var wallet User
			var allocations []AgencyFundingAllocation
			var lots []AgencyFundingLot
			var repayments []AgencyDebtRepayment
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
			require.NoError(t, db.First(&debt, debts[0].ID).Error)
			require.NoError(t, db.First(&wallet, user.Id).Error)
			require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&allocations).Error)
			require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&lots).Error)
			require.NoError(t, db.Order("id").Find(&repayments).Error)
			assert.Equal(t, beforeAccount, account)
			assert.Equal(t, beforeDebt, debt)
			assert.Equal(t, beforeUser, wallet)
			assert.Equal(t, beforeAllocations, allocations)
			assert.Equal(t, beforeLots, lots)
			assert.Equal(t, beforeRepayments, repayments)
			var newCommands int64
			require.NoError(t, db.Model(&AgencyFundingReversal{}).Where("refund_id = ?", "ambiguous-partial-two").Count(&newCommands).Error)
			assert.Zero(t, newCommands)
		})
	}
}

func TestAgencyChargebackDebtIdentityAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			connection := agencyDialectDB(t, dialect)
			if connection == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := connection.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, connection.AutoMigrate(&User{}))
			require.NoError(t, MigrateAgency(connection))
			t.Run("exact_source", func(t *testing.T) { testAgencyChargebackDebtPreservesOtherRepayment(t, connection) })
			t.Run("single_legacy", func(t *testing.T) { testAgencyChargebackDebtAdoptsSingleLegacyRow(t, connection) })
			t.Run("ambiguous_legacy", func(t *testing.T) { testAgencyChargebackDebtRejectsAmbiguousLegacyPool(t, connection) })
		})
	}
}
