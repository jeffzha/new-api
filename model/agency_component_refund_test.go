package model

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func agencyComponentRefundFixture(t *testing.T, paid, bonus int64, components []agencycontract.BillingComponent, databases ...*gorm.DB) (*gorm.DB, User, Token, agencycontract.BillingEvent) {
	t.Helper()
	var db *gorm.DB
	if len(databases) > 0 {
		db = databases[0]
	} else {
		var err error
		db, err = gorm.Open(sqlite.Open("file:component-refund-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
		require.NoError(t, err)
	}
	if len(databases) == 0 {
		require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
		require.NoError(t, MigrateAgency(db))
	}
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	user := User{Username: "component-refund-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: int(paid + bonus)}
	require.NoError(t, db.Create(&user).Error)
	token := Token{UserId: user.Id, Key: "component-refund-key", Status: common.TokenStatusEnabled, UnlimitedQuota: true}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), "payment", "component-refund-source-1", "payment_callback", paid, bonus)
	}))
	input := agencycontract.BillingEvent{UserID: int64(user.Id), FinancialChargeID: "component-refund-charge",
		BusinessStatus: "success", BillingStatus: "settled", Components: components}
	for _, component := range components {
		input.StandardQuota += component.StandardQuota
		input.ChargedTotalQuota += component.ChargedTotalQuota
		input.CommissionableQuota += component.CommissionableQuota
		input.NoncommissionableQuota += component.NoncommissionableQuota
		input.SettlementCostQuota += component.SettlementCostQuota
		input.TheoreticalCommissionQuota += component.TheoreticalCommissionQuota
	}
	snapshot := agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, CommissionEligible: true,
		CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	reserve := min(input.ChargedTotalQuota, paid+bonus)
	_, err := TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, token.Id, int(reserve), token.Key, input.FinancialChargeID, reserve, true, &snapshot)
	require.NoError(t, err)
	event, err := AgencyCommitWalletCharge(input, token.Key)
	require.NoError(t, err)
	require.Equal(t, agencycontract.ComponentSchemaVersion, event.SchemaVersion)
	return db, user, token, event
}

func TestAgencyComponentRefundAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			connection := agencyDialectDB(t, dialect)
			if connection == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := connection.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, connection.AutoMigrate(&User{}, &Token{}))
			require.NoError(t, MigrateAgency(connection))
			db := connection.Begin()
			require.NoError(t, db.Error)
			t.Cleanup(func() { require.NoError(t, db.Rollback().Error) })
			_, user, token, original := agencyComponentRefundFixture(t, 60, 40, []agencycontract.BillingComponent{
				{ComponentID: "model", ChargedTotalQuota: 80, CommissionableQuota: 80, SettlementCostQuota: 40, TheoreticalCommissionQuota: 40, CommissionEligible: true},
				{ComponentID: "fee", ChargedTotalQuota: 20, NoncommissionableQuota: 20},
			}, db)
			for _, target := range []int64{50, 100} {
				input := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
					RefundID: fmt.Sprintf("refund-%d", target), CumulativeQuota: target, Reason: "model_after_sale"}
				event, err := AgencyRefundWalletCharge(input, token.Key)
				require.NoError(t, err)
				replay, err := AgencyRefundWalletCharge(input, token.Key)
				require.NoError(t, err)
				assert.Equal(t, event.EventID, replay.EventID)
				assert.Equal(t, original.AgencyID, event.AgencyID)
				assert.Equal(t, int64(12), event.ReversedCommissionAmountMicros)
			}
			var wallet User
			var lot AgencyFundingLot
			require.NoError(t, db.First(&wallet, user.Id).Error)
			require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&lot).Error)
			assert.Equal(t, 100, wallet.Quota)
			assert.Equal(t, int64(60), lot.PaidAvailable)
			assert.Equal(t, int64(40), lot.BonusAvailable)
			var count int64
			require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("operation = ? AND charge_id = ?", "reverse", original.FinancialChargeID).Count(&count).Error)
			assert.Equal(t, int64(2), count)
		})
	}
}

func TestAgencyComponentRefundPreservesSourcesAndCommissionAcrossCumulativeRefunds(t *testing.T) {
	db, user, token, original := agencyComponentRefundFixture(t, 60, 40, []agencycontract.BillingComponent{
		{ComponentID: "model", StandardQuota: 80, ChargedTotalQuota: 80, CommissionableQuota: 80, SettlementCostQuota: 40, TheoreticalCommissionQuota: 40, CommissionEligible: true},
		{ComponentID: "fee", ChargedTotalQuota: 20, NoncommissionableQuota: 20},
	})
	require.Equal(t, int64(24), original.CommissionAmountMicros)
	var originalMatrix []AgencyComponentFunding
	require.NoError(t, db.Order("id").Find(&originalMatrix).Error)
	var reversed int64
	for _, amount := range []int64{1, 3, 50, 99, 100} {
		input := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
			RefundID: fmt.Sprintf("refund-%d", amount), CumulativeQuota: amount, Reason: "model_after_sale"}
		event, err := AgencyRefundWalletCharge(input, token.Key)
		require.NoError(t, err)
		require.NoError(t, agencycontract.ValidateBillingComponents(event))
		assert.Equal(t, original.EventID, event.OriginalEventID)
		assert.Equal(t, original.AgencyID, event.AgencyID)
		assert.Equal(t, original.CurrencyCode, event.CurrencyCode)
		reversed += event.ReversedCommissionAmountMicros
		replay, err := AgencyRefundWalletCharge(input, token.Key)
		require.NoError(t, err)
		assert.Equal(t, event, replay)
		input.CumulativeQuota--
		_, err = AgencyRefundWalletCharge(input, token.Key)
		assert.ErrorIs(t, err, ErrAgencyChargeConflict)
		var wallet User
		var account AgencyFundingAccount
		require.NoError(t, db.First(&wallet, user.Id).Error)
		require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
		assert.Equal(t, amount, int64(wallet.Quota))
		assert.Equal(t, amount, account.PaidAvailable+account.NonpaidAvailable-account.DebtQuota)
	}
	assert.Equal(t, original.CommissionAmountMicros, reversed)
	var lot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&lot).Error)
	assert.Equal(t, int64(60), lot.PaidAvailable)
	assert.Equal(t, int64(40), lot.BonusAvailable)
	assert.Zero(t, lot.PaidConsumed)
	assert.Zero(t, lot.BonusConsumed)
	var finalToken Token
	require.NoError(t, db.First(&finalToken, token.Id).Error)
	assert.Zero(t, finalToken.UsedQuota)
	assert.Zero(t, finalToken.RemainQuota)
	var matrix []AgencyComponentFunding
	require.NoError(t, db.Order("id").Find(&matrix).Error)
	require.Len(t, matrix, len(originalMatrix))
	for i, row := range matrix {
		assert.Equal(t, originalMatrix[i].AllocationID, row.AllocationID)
		assert.Equal(t, originalMatrix[i].LotID, row.LotID)
		assert.Equal(t, row.PaidQuota, row.RestoredPaidQuota)
		assert.Equal(t, row.NonpaidQuota, row.RestoredNonpaidQuota)
	}
	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
	assert.Equal(t, "reversed", journal.Status)
	assert.Equal(t, int64(100), journal.ReversedQuota)
	assert.Equal(t, int64(24), journal.ReversedCommissionQuota)
	var operations, ledgers, outbox, deliveries int64
	require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "reverse").Count(&operations).Error)
	require.NoError(t, db.Model(&AgencyFundingLedger{}).Where("source_kind = ?", "billing_reverse").Count(&ledgers).Error)
	require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("event_kind = ?", "agency.billing_reversed").Count(&outbox).Error)
	require.NoError(t, db.Model(&AgencyEventDelivery{}).Where("event_id IN (?)", db.Model(&AgencyBillingOutbox{}).Select("event_id").Where("event_kind = ?", "agency.billing_reversed")).Count(&deliveries).Error)
	assert.Equal(t, int64(5), operations)
	assert.Equal(t, operations, ledgers)
	assert.Equal(t, operations, outbox)
	assert.Equal(t, operations, deliveries)
}

func TestAgencyComponentRefundNamedFeeNeverReversesModelCommission(t *testing.T) {
	db, user, token, original := agencyComponentRefundFixture(t, 100, 0, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 80, CommissionableQuota: 80, SettlementCostQuota: 40, TheoreticalCommissionQuota: 40, CommissionEligible: true},
		{ComponentID: "fee", ChargedTotalQuota: 20, NoncommissionableQuota: 20},
	})
	// Current ownership/currency may change; the command cannot supply either.
	require.NoError(t, db.Model(&AgencyBillingJournal{}).Where("charge_id = ?", original.FinancialChargeID).Update("currency_code", "USD").Error)
	input := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
		RefundID: "refund-fee", ComponentID: "fee", CumulativeQuota: 20, Reason: "fee_refund"}
	event, err := AgencyRefundWalletCharge(input, token.Key)
	require.NoError(t, err)
	assert.Zero(t, event.ReversedCommissionAmountMicros)
	assert.Equal(t, int64(20), event.NoncommissionableQuota)
	assert.Equal(t, "TOKENS", event.CurrencyCode)
	assert.Equal(t, original.AgencyID, event.AgencyID)
	input.RefundID, input.ComponentID, input.CumulativeQuota = "refund-model", "model", 80
	event, err = AgencyRefundWalletCharge(input, token.Key)
	require.NoError(t, err)
	assert.Equal(t, int64(40), event.ReversedCommissionAmountMicros)
	assert.Equal(t, "reversed", event.BillingStatus)
}

func TestAgencyComponentRefundRollsBackForRevokedSourcesOrUnrelatedDebt(t *testing.T) {
	for _, provenance := range []string{"revoked", "unrelated_debt", "token_conflict", "commission_watermark"} {
		t.Run(provenance, func(t *testing.T) {
			db, user, token, original := agencyComponentRefundFixture(t, 100, 0, []agencycontract.BillingComponent{
				{ComponentID: "model", ChargedTotalQuota: 100, CommissionableQuota: 100, SettlementCostQuota: 60, TheoreticalCommissionQuota: 40, CommissionEligible: true},
			})
			if provenance == "revoked" {
				require.NoError(t, db.Model(&AgencyFundingAllocation{}).Where("charge_id = ?", original.FinancialChargeID).Update("reversed", 10).Error)
			} else if provenance == "unrelated_debt" {
				require.NoError(t, db.Model(&AgencyFundingAccount{}).Where("user_id = ?", user.Id).Update("debt_quota", 10).Error)
			} else if provenance == "commission_watermark" {
				require.NoError(t, db.Model(&AgencyChargeComponent{}).Where("charge_id = ?", original.FinancialChargeID).Update("reversed_commission_quota", -1).Error)
			}
			input := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
				RefundID: "blocked-refund", CumulativeQuota: 100, Reason: "model_after_sale"}
			key := token.Key
			if provenance == "token_conflict" {
				key = "other-key"
			}
			_, err := AgencyRefundWalletCharge(input, key)
			require.Error(t, err)
			var wallet User
			var component AgencyChargeComponent
			var lot AgencyFundingLot
			require.NoError(t, db.First(&wallet, user.Id).Error)
			require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&component).Error)
			require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&lot).Error)
			assert.Zero(t, wallet.Quota)
			assert.Zero(t, component.RefundedQuota)
			assert.Zero(t, lot.PaidAvailable)
			assert.Equal(t, int64(100), lot.PaidConsumed)
			var count int64
			require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("event_kind = ?", "agency.billing_reversed").Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestAgencyComponentRefundUsesOriginalLotFIFOAndLeavesOtherChargesUntouched(t *testing.T) {
	db, user, token, firstCharge := agencyComponentRefundFixture(t, 40, 0, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 40, CommissionableQuota: 40, SettlementCostQuota: 20, TheoreticalCommissionQuota: 20, CommissionEligible: true},
	})
	for i, amount := range []int64{30, 40} {
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			if err := RecordAgencyTopup(tx, int64(user.Id), "payment", fmt.Sprintf("fifo-source-%d", i), "payment_callback", amount, 0); err != nil {
				return err
			}
			return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", amount)).Error
		}))
	}
	snapshot := agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, CommissionEligible: true, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	_, err := TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, token.Id, 70, token.Key, "fifo-charge", 70, true, &snapshot)
	require.NoError(t, err)
	_, err = AgencyCommitWalletCharge(agencycontract.BillingEvent{UserID: int64(user.Id), FinancialChargeID: "fifo-charge", BusinessStatus: "success", BillingStatus: "settled",
		ChargedTotalQuota: 70, CommissionableQuota: 70, SettlementCostQuota: 40, TheoreticalCommissionQuota: 30,
		Components: []agencycontract.BillingComponent{{ComponentID: "model", ChargedTotalQuota: 70, CommissionableQuota: 70, SettlementCostQuota: 40, TheoreticalCommissionQuota: 30, CommissionEligible: true}}}, token.Key)
	require.NoError(t, err)
	_, err = AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: "fifo-charge", RefundID: "fifo-partial", CumulativeQuota: 40, Reason: "model_after_sale"}, token.Key)
	require.NoError(t, err)
	var lots []AgencyFundingLot
	require.NoError(t, db.Where("source_id IN ?", []string{"fifo-source-0", "fifo-source-1"}).Order("money_seq ASC, id ASC").Find(&lots).Error)
	require.Len(t, lots, 2)
	assert.Equal(t, int64(30), lots[0].PaidAvailable)
	assert.Equal(t, int64(10), lots[1].PaidAvailable)
	var firstLot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&firstLot).Error)
	assert.Zero(t, firstLot.PaidAvailable)
	assert.Equal(t, int64(40), firstLot.PaidConsumed)
	var firstJournal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", firstCharge.FinancialChargeID).First(&firstJournal).Error)
	assert.Zero(t, firstJournal.ReversedQuota)
	_, err = AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: "fifo-charge", RefundID: "fifo-full", CumulativeQuota: 70, Reason: "model_after_sale"}, token.Key)
	require.NoError(t, err)
	var storedToken Token
	require.NoError(t, db.First(&storedToken, token.Id).Error)
	assert.Equal(t, 40, storedToken.UsedQuota)
}

func TestAgencyComponentRefundReducesOnlyItsOriginalOutstandingDebt(t *testing.T) {
	db, user, token, original := agencyComponentRefundFixture(t, 0, 0, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 100, CommissionableQuota: 100, SettlementCostQuota: 60, TheoreticalCommissionQuota: 40, CommissionEligible: true},
	})
	for _, target := range []int64{25, 60, 100} {
		input := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
			RefundID: fmt.Sprintf("debt-refund-%d", target), CumulativeQuota: target, Reason: "model_after_sale"}
		event, err := AgencyRefundWalletCharge(input, token.Key)
		require.NoError(t, err)
		assert.Zero(t, event.ReversedCommissionAmountMicros)
		var account AgencyFundingAccount
		var debt AgencyFundingDebt
		var wallet User
		require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
		require.NoError(t, db.Where("user_id = ? AND debt_kind = ?", user.Id, "model_charge").First(&debt).Error)
		require.NoError(t, db.First(&wallet, user.Id).Error)
		assert.Equal(t, 100-target, account.DebtQuota)
		assert.Equal(t, 100-target, debt.OutstandingQuota)
		assert.Equal(t, target, debt.ReversedQuota)
		assert.Equal(t, target-100, int64(wallet.Quota))
		assert.Zero(t, account.PaidAvailable)
		assert.Zero(t, account.NonpaidAvailable)
	}
}

func TestAgencyReservationCancelAfterChargebackRestoresActualRepaymentSource(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-reservation-cancel-repaid?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, MigrateAgency(db))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	user := User{Username: "debt-cancel-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := Token{UserId: user.Id, Key: "debt-cancel-key", Status: common.TokenStatusEnabled, UnlimitedQuota: true}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), "payment", "component-refund-source-1", "payment_callback", 100, 0)
	}))
	_, err = TryReserveAgencyWalletAndToken(user.Id, token.Id, 100, token.Key, "cancel-charge", 100, true)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{UserID: int64(user.Id), RefundID: "revoke-original-source",
			SourceOperationID: "component-refund-source-1", Quota: 100, Reason: "payment_chargeback", EvidenceRef: "bank-receipt",
			CurrencyCode: "CNY", PaymentReference: "original-payment"})
		return err
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", "new-repayment-source", "payment_callback", 100, 0); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 100)).Error
	}))
	_, err = ReleaseAgencyWalletAndToken(user.Id, token.Id, 100, token.Key, "cancel-charge")
	require.NoError(t, err)
	var oldLot, newLot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&oldLot).Error)
	require.NoError(t, db.Where("source_id = ?", "new-repayment-source").First(&newLot).Error)
	assert.Zero(t, oldLot.PaidAvailable)
	assert.Equal(t, int64(100), oldLot.PaidRevoked)
	assert.Equal(t, int64(100), newLot.PaidAvailable)
	assert.Zero(t, newLot.PaidDebtRepaid)
	assert.Zero(t, newLot.BonusAvailable)
	var account AgencyFundingAccount
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(100), account.PaidAvailable)
	assert.Zero(t, account.DebtQuota)
}

func TestAgencyComponentRefundRejectsLegacySourceReleaseBeforeWalletMutation(t *testing.T) {
	db, user, token, original := agencyComponentRefundFixture(t, 100, 0, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 100, CommissionableQuota: 100, SettlementCostQuota: 60, TheoreticalCommissionQuota: 40, CommissionEligible: true},
	})
	_, err := ReleaseAgencyWalletAndToken(user.Id, token.Id, 50, token.Key, original.FinancialChargeID)
	require.ErrorIs(t, err, ErrAgencyComponentRefundProvenance)
	var wallet User
	var lot AgencyFundingLot
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&lot).Error)
	assert.Zero(t, wallet.Quota)
	assert.Zero(t, lot.PaidAvailable)
	assert.Equal(t, int64(100), lot.PaidConsumed)
}

func TestAgencyComponentRefundRestoresRepaidDebtToActualRepaymentLot(t *testing.T) {
	db, user, token, original := agencyComponentRefundFixture(t, 10, 0, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 100, CommissionableQuota: 100, SettlementCostQuota: 60, TheoreticalCommissionQuota: 40, CommissionEligible: true},
	})
	var debt AgencyFundingDebt
	require.NoError(t, db.Where("user_id = ? AND debt_kind = ?", user.Id, "model_charge").First(&debt).Error)
	assert.Equal(t, int64(90), debt.OriginalQuota)
	assert.Equal(t, int64(90), debt.OutstandingQuota)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", "actual-debt-repayment", "payment_callback", 90, 0); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 90)).Error
	}))
	input := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
		RefundID: "refund-repaid-debt", CumulativeQuota: 100, Reason: "model_after_sale"}
	event, err := AgencyRefundWalletCharge(input, token.Key)
	require.NoError(t, err)
	assert.Equal(t, int64(4), event.ReversedCommissionAmountMicros)
	require.Len(t, event.Components, 1)
	assert.Equal(t, int64(90), event.Components[0].DebtAllocatedQuota)
	var repaymentLot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "actual-debt-repayment").First(&repaymentLot).Error)
	assert.Equal(t, int64(90), repaymentLot.PaidAvailable)
	assert.Zero(t, repaymentLot.PaidDebtRepaid)
	assert.Zero(t, repaymentLot.BonusAvailable)
	var originalLot AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&originalLot).Error)
	assert.Equal(t, int64(10), originalLot.PaidAvailable)
	require.NoError(t, db.First(&debt, debt.ID).Error)
	assert.Zero(t, debt.OutstandingQuota)
	assert.Equal(t, int64(90), debt.ReversedQuota)
	var wallet User
	require.NoError(t, db.First(&wallet, user.Id).Error)
	assert.Equal(t, 100, wallet.Quota)
	var ledger AgencyFundingLedger
	require.NoError(t, db.Where("operation_id = ?", event.OperationID).First(&ledger).Error)
	assert.Equal(t, int64(100), ledger.PaidDelta)
	assert.Zero(t, ledger.DebtDelta)
	// Refunding the repayment topup afterwards revokes restored availability,
	// without recreating the original, already-cancelled model debt.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{UserID: int64(user.Id), RefundID: "reverse-repaid-source",
			SourceOperationID: "actual-debt-repayment", Quota: 90, Reason: "payment_refund", EvidenceRef: "payment-provider-receipt", CurrencyCode: "CNY", PaymentReference: "repayment-payment"})
		return err
	}))
	require.NoError(t, db.First(&debt, debt.ID).Error)
	assert.Zero(t, debt.OutstandingQuota)
}

func TestAgencyComponentRefundDoesNotResurrectExpiredRedemption(t *testing.T) {
	db, user, token, original := agencyComponentRefundFixture(t, 0, 100, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 100, CommissionableQuota: 100, SettlementCostQuota: 60, TheoreticalCommissionQuota: 40, CommissionEligible: true},
	})
	expiresAt := time.Now().Unix() - 1
	require.NoError(t, db.Model(&AgencyFundingLot{}).Where("source_id = ?", "component-refund-source-1").
		Updates(map[string]any{"source_kind": "redemption", "expires_at": expiresAt}).Error)
	require.NoError(t, db.Model(&AgencyTopupFact{}).Where("source_id = ?", "component-refund-source-1").
		Updates(map[string]any{"funding_source": "redemption", "expires_at": expiresAt}).Error)

	event, err := AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
		RefundID: "expired-redemption-refund", CumulativeQuota: 100, Reason: "model_after_sale"}, token.Key)
	require.NoError(t, err)
	assert.Equal(t, int64(100), event.ChargedTotalQuota)

	var wallet User
	var account AgencyFundingAccount
	var lot AgencyFundingLot
	var fact AgencyTopupFact
	var storedToken Token
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
	require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&lot).Error)
	require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&fact).Error)
	require.NoError(t, db.First(&storedToken, token.Id).Error)
	assert.Zero(t, wallet.Quota)
	assert.Zero(t, account.NonpaidAvailable)
	assert.Zero(t, lot.BonusAvailable)
	assert.Zero(t, lot.BonusConsumed)
	assert.Equal(t, int64(100), lot.BonusExpired)
	assert.Equal(t, int64(100), fact.ExpiredQuota)
	assert.Zero(t, storedToken.UsedQuota)
}

func TestAgencyComponentRefundKeepsExpiredRedemptionDebtRepaymentUnavailable(t *testing.T) {
	db, user, token, original := agencyComponentRefundFixture(t, 0, 0, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 100, CommissionableQuota: 100, SettlementCostQuota: 60, TheoreticalCommissionQuota: 40, CommissionEligible: true},
	})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "redemption", "expired-debt-redemption", "redemption", 0, 100); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 100)).Error
	}))
	expiresAt := time.Now().Unix() - 1
	require.NoError(t, db.Model(&AgencyFundingLot{}).Where("source_id = ?", "expired-debt-redemption").Update("expires_at", expiresAt).Error)
	require.NoError(t, db.Model(&AgencyTopupFact{}).Where("source_id = ?", "expired-debt-redemption").Update("expires_at", expiresAt).Error)

	_, err := AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
		RefundID: "expired-debt-redemption-refund", CumulativeQuota: 100, Reason: "model_after_sale"}, token.Key)
	require.NoError(t, err)

	var wallet User
	var account AgencyFundingAccount
	var lot AgencyFundingLot
	var fact AgencyTopupFact
	var debt AgencyFundingDebt
	var storedToken Token
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
	require.NoError(t, db.Where("source_id = ?", "expired-debt-redemption").First(&lot).Error)
	require.NoError(t, db.Where("source_id = ?", "expired-debt-redemption").First(&fact).Error)
	require.NoError(t, db.Where("user_id = ? AND debt_kind = ?", user.Id, "model_charge").First(&debt).Error)
	require.NoError(t, db.First(&storedToken, token.Id).Error)
	assert.Zero(t, wallet.Quota)
	assert.Zero(t, account.NonpaidAvailable)
	assert.Zero(t, account.DebtQuota)
	assert.Zero(t, debt.OutstandingQuota)
	assert.Equal(t, int64(100), debt.ReversedQuota)
	assert.Zero(t, lot.BonusAvailable)
	assert.Zero(t, lot.BonusDebtRepaid)
	assert.Equal(t, int64(100), lot.BonusExpired)
	assert.Equal(t, int64(100), fact.ExpiredQuota)
	assert.Zero(t, storedToken.UsedQuota)
}
