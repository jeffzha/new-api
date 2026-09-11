package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecordAgencyTopupIsIdempotentButRejectsConflictingReplay(t *testing.T) {
	dsn := "file:agency-topup-idempotency-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, MigrateAgency(db))

	first := User{
		Username: "agency-topup-first", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AffCode: "agency-topup-first-aff", BillingMode: AgencyDurableBillingMode, FundingVersion: 1,
	}
	second := User{
		Username: "agency-topup-second", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AffCode: "agency-topup-second-aff", BillingMode: AgencyDurableBillingMode, FundingVersion: 1,
	}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Create(&second).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(first.Id), "payment", "topup-idempotent-1", "payment_callback", 100, 20)
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(first.Id), "payment", "topup-idempotent-1", "payment_callback", 100, 20)
	}))
	var lots int64
	require.NoError(t, db.Model(&AgencyFundingLot{}).Where("source_id = ?", "topup-idempotent-1").Count(&lots).Error)
	require.Equal(t, int64(1), lots)

	err = db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(first.Id), "payment", "topup-idempotent-1", "payment_callback", 101, 20)
	})
	require.ErrorIs(t, err, ErrAgencyTopupConflict)
	err = db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(second.Id), "payment", "topup-idempotent-1", "payment_callback", 100, 20)
	})
	require.ErrorIs(t, err, ErrAgencyTopupConflict)
	require.NoError(t, db.Model(&AgencyFundingLot{}).Where("source_id = ?", "topup-idempotent-1").Count(&lots).Error)
	require.Equal(t, int64(1), lots)
}

func TestReleaseAgencyWalletAndTokenRestoresTotalReservationForNonpaidQuota(t *testing.T) {
	dsn := "file:agency-wallet-token-release-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, MigrateAgency(db))

	previousDB := DB
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})

	user := User{
		Username: "agency-wallet-token-release-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 150,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-wallet-token-release", "payment_callback", 100, 50)
	}))
	token := Token{
		UserId: user.Id, Key: "agency-wallet-token-release-key",
		Status: common.TokenStatusEnabled, UnlimitedQuota: true,
	}
	require.NoError(t, db.Create(&token).Error)

	paid, err := TryReserveAgencyWalletAndToken(
		user.Id, token.Id, 120, token.Key, "charge-wallet-token-release", 120, true,
	)
	require.NoError(t, err)
	require.Equal(t, int64(100), paid)

	var reservedUser User
	require.NoError(t, db.First(&reservedUser, user.Id).Error)
	require.Equal(t, 30, reservedUser.Quota)
	var reservedToken Token
	require.NoError(t, db.First(&reservedToken, token.Id).Error)
	require.Equal(t, 120, reservedToken.UsedQuota)

	releasedPaid, err := ReleaseAgencyWalletAndToken(
		user.Id, token.Id, 120, token.Key, "charge-wallet-token-release",
	)
	require.NoError(t, err)
	require.Equal(t, int64(100), releasedPaid)

	var restoredUser User
	require.NoError(t, db.First(&restoredUser, user.Id).Error)
	require.Equal(t, 150, restoredUser.Quota)
	var restoredToken Token
	require.NoError(t, db.First(&restoredToken, token.Id).Error)
	require.Equal(t, 0, restoredToken.UsedQuota)
	require.Equal(t, 0, restoredToken.RemainQuota)

	var account AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(100), account.PaidAvailable)
	require.Equal(t, int64(50), account.NonpaidAvailable)
	require.Equal(t, int64(0), account.DebtQuota)
}

func TestLegacyAgencyWalletAndTokenReservationIsAtomic(t *testing.T) {
	dsn := "file:legacy-agency-wallet-token-atomic-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, MigrateAgency(db))

	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	user := User{
		Username: "legacy-agency-wallet-token-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		Quota: 100,
	}
	require.NoError(t, db.Create(&user).Error)
	token := Token{
		UserId: user.Id, Key: "legacy-agency-wallet-token-key",
		Status: common.TokenStatusEnabled, RemainQuota: 20, UsedQuota: 0,
	}
	require.NoError(t, db.Create(&token).Error)

	err = TryReserveUserQuotaAndAgencyWithToken(user.Id, token.Id, 30, token.Key, "legacy-charge-1", 30, false)
	require.ErrorIs(t, err, ErrInsufficientAgencyTokenQuota)

	var afterFailureUser User
	require.NoError(t, db.First(&afterFailureUser, user.Id).Error)
	require.Equal(t, 100, afterFailureUser.Quota)
	var afterFailureToken Token
	require.NoError(t, db.First(&afterFailureToken, token.Id).Error)
	require.Equal(t, 20, afterFailureToken.RemainQuota)
	require.Equal(t, 0, afterFailureToken.UsedQuota)

	require.NoError(t, TryReserveUserQuotaAndAgencyWithToken(user.Id, token.Id, 15, token.Key, "legacy-charge-1", 15, false))
	require.NoError(t, ReleaseUserQuotaAndAgencyWithToken(user.Id, token.Id, 15, token.Key, "legacy-charge-1"))

	var restoredUser User
	require.NoError(t, db.First(&restoredUser, user.Id).Error)
	require.Equal(t, 100, restoredUser.Quota)
	var restoredToken Token
	require.NoError(t, db.First(&restoredToken, token.Id).Error)
	require.Equal(t, 20, restoredToken.RemainQuota)
	require.Equal(t, 0, restoredToken.UsedQuota)
}

func TestReverseAgencyTopupCreatesDebtForConsumedPaidQuotaAndIsIdempotent(t *testing.T) {
	dsn := "file:agency-funding-reversal-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, MigrateAgency(db))

	user := User{
		Username: "agency-reversal-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 0,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-reversal-1", "payment_callback", 100, 0); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", 100).Error
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := TryReserveUserQuotaAndAgencyTx(tx, user.Id, 100, "charge-reversal-1", 100)
		return err
	}))
	input := AgencyFundingReversalInput{
		RefundID: "refund-reversal-1", SourceOperationID: "topup-reversal-1",
		UserID: int64(user.Id), Quota: 100, CurrencyCode: "CNY",
		PaymentReference: "pay-1", EvidenceRef: "evidence-1", Reason: "chargeback",
	}
	var charges []AgencyFundingReversalCharge
	var created bool
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		charges, created, err = ReverseAgencyTopupTx(tx, input)
		return err
	}))
	require.True(t, created)
	require.Len(t, charges, 1)
	require.Equal(t, "charge-reversal-1", charges[0].ChargeID)
	require.Equal(t, int64(100), charges[0].Quota)

	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, -100, stored.Quota)
	var account AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(0), account.PaidAvailable)
	require.Equal(t, int64(100), account.DebtQuota)
	var debt AgencyFundingDebt
	require.NoError(t, db.Where("origin_operation_id = ?", input.SourceOperationID).First(&debt).Error)
	require.Equal(t, int64(100), debt.OutstandingQuota)
	var allocation AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "charge-reversal-1").First(&allocation).Error)
	require.Equal(t, int64(100), allocation.RevokedReservedDebt)

	var duplicateCharges []AgencyFundingReversalCharge
	var duplicateCreated bool
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		duplicateCharges, duplicateCreated, err = ReverseAgencyTopupTx(tx, input)
		return err
	}))
	require.False(t, duplicateCreated)
	require.Equal(t, charges, duplicateCharges)
	var reversalCount int64
	require.NoError(t, db.Model(&AgencyFundingReversal{}).Where("refund_id = ?", input.RefundID).Count(&reversalCount).Error)
	require.Equal(t, int64(1), reversalCount)
	var chargeRecordCount int64
	require.NoError(t, db.Model(&AgencyFundingReversalChargeRecord{}).Where("refund_id = ?", input.RefundID).Count(&chargeRecordCount).Error)
	require.Equal(t, int64(1), chargeRecordCount)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 120)).Error; err != nil {
			return err
		}
		return RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-after-chargeback", "payment_callback", 120, 0)
	}))
	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, 20, stored.Quota)
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(20), account.PaidAvailable)
	require.Equal(t, int64(0), account.DebtQuota)
	require.NoError(t, db.Where("origin_operation_id = ?", input.SourceOperationID).First(&debt).Error)
	require.Equal(t, int64(0), debt.OutstandingQuota)
	var repayment AgencyDebtRepayment
	require.NoError(t, db.Where("debt_id = ?", debt.ID).First(&repayment).Error)
	require.Equal(t, int64(100), repayment.Quota)
}

func TestChargebackDebtIsClearedWhenTheReservedChargeIsCancelled(t *testing.T) {
	dsn := "file:agency-funding-chargeback-cancel-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, MigrateAgency(db))

	user := User{
		Username: "agency-chargeback-cancel-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 0,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-chargeback-cancel", "payment_callback", 100, 0); err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", 100).Error; err != nil {
			return err
		}
		paid, err := TryReserveUserQuotaAndAgencyTx(tx, user.Id, 100, "charge-chargeback-cancel", 100)
		if paid != 100 {
			return errors.New("expected full paid reservation")
		}
		return err
	}))
	var beforeReversalLot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "topup-chargeback-cancel").First(&beforeReversalLot).Error)
	require.Equal(t, int64(100), beforeReversalLot.PaidConsumed)
	require.Equal(t, int64(0), beforeReversalLot.PaidAvailable)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{
			RefundID: "refund-chargeback-cancel", SourceOperationID: "topup-chargeback-cancel",
			UserID: int64(user.Id), Quota: 100, CurrencyCode: "CNY", Reason: "chargeback",
		})
		return err
	}))

	var account AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(100), account.DebtQuota)
	var debt AgencyFundingDebt
	require.NoError(t, db.Where("origin_operation_id = ?", "topup-chargeback-cancel").First(&debt).Error)
	require.Equal(t, int64(100), debt.OutstandingQuota)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := ReleaseUserQuotaAndAgencyTx(tx, user.Id, 100, "charge-chargeback-cancel")
		return err
	}))

	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, 0, stored.Quota)
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(0), account.DebtQuota)
	require.NoError(t, db.First(&debt, debt.ID).Error)
	require.Equal(t, int64(0), debt.OutstandingQuota)
	var allocation AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "charge-chargeback-cancel").First(&allocation).Error)
	require.Equal(t, int64(0), allocation.DebtConsumed)
	var lot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "topup-chargeback-cancel").First(&lot).Error)
	require.Equal(t, int64(0), lot.PaidAvailable)
	require.Equal(t, int64(0), lot.PaidConsumed)
	require.Equal(t, int64(100), lot.PaidRevoked)
}

func TestReverseAgencyTopupMixedPaidBonusAllocationsAndOutbox(t *testing.T) {
	dsn := "file:agency-funding-mixed-reversal-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, MigrateAgency(db))

	user := User{
		Username: "agency-mixed-reversal-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 150,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-mixed-1", "payment_callback", 100, 50)
	}))
	// The gateway's wallet deduction and the typed funding allocation share
	// one transaction in production. This setup mirrors the resulting wallet
	// state before a 120-quota charge (100 paid + 20 bonus).
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := TryReserveUserQuotaAndAgencyTx(tx, user.Id, 120, "charge-mixed-1", 120)
		return err
	}))

	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, 30, stored.Quota)
	var allocationRows []AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "charge-mixed-1").Order("id ASC").Find(&allocationRows).Error)
	require.Len(t, allocationRows, 2)
	require.Equal(t, int64(100), allocationRows[0].Consumed)
	require.Equal(t, int64(20), allocationRows[1].NonpaidConsumed)

	input := AgencyFundingReversalInput{
		RefundID: "refund-mixed-1", SourceOperationID: "topup-mixed-1",
		UserID: int64(user.Id), Quota: 150, CurrencyCode: "CNY",
		PaymentReference: "pay-mixed-1", EvidenceRef: "evidence-mixed-1", Reason: "chargeback",
	}
	var charges []AgencyFundingReversalCharge
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		charges, _, err = ReverseAgencyTopupTx(tx, input)
		return err
	}))
	require.Len(t, charges, 1)
	require.Equal(t, "charge-mixed-1", charges[0].ChargeID)
	require.Equal(t, int64(100), charges[0].Quota)

	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, -120, stored.Quota)
	var account AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(0), account.PaidAvailable)
	require.Equal(t, int64(0), account.NonpaidAvailable)
	require.Equal(t, int64(120), account.DebtQuota)

	var lot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", input.SourceOperationID).First(&lot).Error)
	require.Equal(t, int64(100), lot.PaidRevoked)
	require.Equal(t, int64(50), lot.BonusRevoked)
	require.Equal(t, int64(0), lot.PaidConsumed)
	require.Equal(t, int64(0), lot.BonusConsumed)
	var paidAllocation, bonusAllocation AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ? AND consumed > 0", "charge-mixed-1").First(&paidAllocation).Error)
	require.Equal(t, int64(100), paidAllocation.RevokedReservedDebt)
	require.NoError(t, db.Where("charge_id = ? AND nonpaid_consumed > 0", "charge-mixed-1").First(&bonusAllocation).Error)
	require.Equal(t, int64(20), bonusAllocation.RevokedNonpaid)

	var outbox AgencyBillingOutbox
	require.NoError(t, db.Where("event_id = ?", "agency-funding-reversal-refund-mixed-1").First(&outbox).Error)
	require.Equal(t, "agency.funding_reversed", outbox.EventKind)
	require.Equal(t, "funding-reverse-refund-mixed-1", outbox.OperationID)
	var delivery AgencyEventDelivery
	require.NoError(t, db.Where("event_id = ?", outbox.EventID).First(&delivery).Error)
	require.Equal(t, "pending", delivery.Status)
}

func TestReverseAgencyTopupReversesDebtRepaymentFromTheSameFundingLot(t *testing.T) {
	dsn := "file:agency-funding-repayment-reversal-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, MigrateAgency(db))

	user := User{
		Username: "agency-repayment-reversal-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 100,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-repayment-source", "payment_callback", 100, 0); err != nil {
			return err
		}
		_, err := TryReserveUserQuotaAndAgencyTx(tx, user.Id, 100, "charge-repayment-source", 100)
		return err
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{
			RefundID: "refund-repayment-source", SourceOperationID: "topup-repayment-source",
			UserID: int64(user.Id), Quota: 100, CurrencyCode: "CNY",
		})
		return err
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-repayment-new", "payment_callback", 100, 0); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", 0).Error
	}))
	var debt AgencyFundingDebt
	require.NoError(t, db.Where("origin_operation_id = ?", "topup-repayment-source").First(&debt).Error)
	require.Equal(t, int64(0), debt.OutstandingQuota)
	var repayment AgencyDebtRepayment
	require.NoError(t, db.Where("funding_lot_id IS NOT NULL").Order("id DESC").First(&repayment).Error)
	require.Equal(t, int64(100), repayment.Quota)
	require.Equal(t, int64(0), repayment.ReversedQuota)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{
			RefundID: "refund-repayment-new", SourceOperationID: "topup-repayment-new",
			UserID: int64(user.Id), Quota: 100, CurrencyCode: "CNY",
		})
		return err
	}))
	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, -100, stored.Quota)
	require.NoError(t, db.First(&debt, debt.ID).Error)
	require.Equal(t, int64(100), debt.OutstandingQuota)
	require.NoError(t, db.First(&repayment, repayment.ID).Error)
	require.Equal(t, int64(100), repayment.ReversedQuota)
}

func TestReleaseAgencyWalletAndTokenIsAtomic(t *testing.T) {
	dsn := "file:agency-wallet-token-atomic-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, MigrateAgency(db))

	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	user := User{
		Username: "agency-wallet-token-atomic", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 100,
	}
	require.NoError(t, db.Create(&user).Error)
	token := Token{UserId: user.Id, Key: "sk-agency-wallet-token-atomic", RemainQuota: 200}
	require.NoError(t, db.Create(&token).Error)

	paid, err := TryReserveAgencyWalletAndToken(user.Id, token.Id, 40, token.Key, "realtime-atomic-1", 40, false)
	require.NoError(t, err)
	require.Equal(t, int64(40), paid)

	var storedUser User
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	require.Equal(t, 60, storedUser.Quota)
	var storedToken Token
	require.NoError(t, db.First(&storedToken, token.Id).Error)
	require.Equal(t, 160, storedToken.RemainQuota)
	require.Equal(t, 40, storedToken.UsedQuota)

	released, err := ReleaseAgencyWalletAndToken(user.Id, token.Id, 10, token.Key, "realtime-atomic-1")
	require.NoError(t, err)
	require.Equal(t, int64(10), released)
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	require.Equal(t, 70, storedUser.Quota)
	require.NoError(t, db.First(&storedToken, token.Id).Error)
	require.Equal(t, 170, storedToken.RemainQuota)
	require.Equal(t, 30, storedToken.UsedQuota)

	// A token accounting failure must roll back the wallet and funding release.
	require.NoError(t, db.Model(&Token{}).Where("id = ?", token.Id).Update("used_quota", 0).Error)
	_, err = ReleaseAgencyWalletAndToken(user.Id, token.Id, 10, token.Key, "realtime-atomic-1")
	require.ErrorIs(t, err, ErrAgencyInsufficientTokenUsage)
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	require.Equal(t, 70, storedUser.Quota)
	var allocation AgencyFundingAllocation
	require.NoError(t, db.Where("charge_id = ?", "realtime-atomic-1").First(&allocation).Error)
	require.Equal(t, int64(30), allocation.Consumed)
}
