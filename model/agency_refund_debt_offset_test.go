package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Accept before a provider reports a larger final bill, exercising the real
// overage/debt path instead of inserting an untraceable account debt number.
func agencyRefundDebtCharge(t *testing.T, user User, token Token, chargeID string, amount int64) agencycontract.BillingEvent {
	t.Helper()
	snapshot := agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, CommissionEligible: true,
		CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	_, err := TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, token.Id, 0, token.Key, chargeID, 0, true, &snapshot)
	require.NoError(t, err)
	event, err := AgencyCommitWalletCharge(agencycontract.BillingEvent{UserID: int64(user.Id), FinancialChargeID: chargeID,
		BusinessStatus: "success", BillingStatus: "settled", ChargedTotalQuota: amount, NoncommissionableQuota: amount,
		Components: []agencycontract.BillingComponent{{ComponentID: "fee", ChargedTotalQuota: amount, NoncommissionableQuota: amount}}}, token.Key)
	require.NoError(t, err)
	return event
}

func TestAgencyRefundDebtOffsetsAcrossDialects(t *testing.T) {
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
			for _, chargeback := range []bool{false, true} {
				t.Run(fmt.Sprintf("chargeback_%t", chargeback), func(t *testing.T) {
					tx := connection.Begin()
					require.NoError(t, tx.Error)
					t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
					db, user, token, original := agencyComponentRefundFixture(t, 60, 40, []agencycontract.BillingComponent{
						{ComponentID: "model", ChargedTotalQuota: 100, CommissionableQuota: 100, SettlementCostQuota: 60, TheoreticalCommissionQuota: 40, CommissionEligible: true},
					}, tx)
					debtCharge := agencyRefundDebtCharge(t, user, token, "other-charge", 80)
					var debt AgencyFundingDebt
					require.NoError(t, db.Where("user_id = ?", user.Id).First(&debt).Error)
					assert.Equal(t, int64(80), debt.OutstandingQuota)
					input := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
						RefundID: "offset-other-charge", CumulativeQuota: 100, Reason: "model_after_sale"}
					// Failure after the debt/source updates must roll all of them back.
					_, err := AgencyRefundWalletCharge(input, "wrong-token-key")
					require.Error(t, err)
					var failedRepayments int64
					require.NoError(t, db.Model(&AgencyDebtRepayment{}).Count(&failedRepayments).Error)
					assert.Zero(t, failedRepayments)
					require.NoError(t, db.First(&debt, debt.ID).Error)
					assert.Equal(t, int64(80), debt.OutstandingQuota)
					// Split refunds have the same final source result as one full
					// refund, while keeping each repayment attributable and replayable.
					input.CumulativeQuota, input.RefundID = 40, "offset-other-charge-partial"
					partial, err := AgencyRefundWalletCharge(input, token.Key)
					require.NoError(t, err)
					partialReplay, err := AgencyRefundWalletCharge(input, token.Key)
					require.NoError(t, err)
					assert.Equal(t, partial, partialReplay)
					input.CumulativeQuota, input.RefundID = 100, "offset-other-charge"
					event, err := AgencyRefundWalletCharge(input, token.Key)
					require.NoError(t, err)
					replay, err := AgencyRefundWalletCharge(input, token.Key)
					require.NoError(t, err)
					assert.Equal(t, event, replay)
					assert.Equal(t, original.CommissionAmountMicros, partial.ReversedCommissionAmountMicros+event.ReversedCommissionAmountMicros)
					var lot AgencyFundingLot
					require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&lot).Error)
					assert.Zero(t, lot.PaidAvailable)
					assert.Equal(t, int64(20), lot.BonusAvailable)
					assert.Equal(t, int64(60), lot.PaidDebtRepaid)
					assert.Equal(t, int64(20), lot.BonusDebtRepaid)
					var repayments []AgencyDebtRepayment
					require.NoError(t, db.Where("debt_id = ?", debt.ID).Order("id").Find(&repayments).Error)
					require.Len(t, repayments, 4)
					assert.Equal(t, "paid", repayments[0].SourceKind)
					assert.Equal(t, int64(24), repayments[0].Quota)
					assert.Equal(t, "nonpaid", repayments[1].SourceKind)
					assert.Equal(t, int64(16), repayments[1].Quota)
					assert.Equal(t, "paid", repayments[2].SourceKind)
					assert.Equal(t, int64(36), repayments[2].Quota)
					assert.Equal(t, "nonpaid", repayments[3].SourceKind)
					assert.Equal(t, int64(4), repayments[3].Quota)
					var ledger AgencyFundingLedger
					require.NoError(t, db.Where("operation_id = ?", event.OperationID).First(&ledger).Error)
					assert.Zero(t, ledger.PaidDelta)
					assert.Equal(t, int64(20), ledger.NonpaidDelta)
					assert.Equal(t, int64(-40), ledger.DebtDelta)
					if chargeback {
						payment := AgencyFundingReversalInput{UserID: int64(user.Id), SourceOperationID: "component-refund-source-1",
							RefundID: "revoke-returned-source", Quota: 30, EvidenceRef: "provider-receipt", Reason: "payment_refund"}
						for _, expectedCreated := range []bool{true, false} {
							require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
								charges, created, err := ReverseAgencyTopupTx(tx, payment)
								assert.Equal(t, expectedCreated, created)
								assert.Empty(t, charges) // No new commission: these funds repaid debt.
								return err
							}))
						}
						require.NoError(t, db.First(&debt, debt.ID).Error)
						assert.Equal(t, int64(10), debt.OutstandingQuota)
					}
					// Reversing the liability returns the real repayment sources; it
					// does not convert a bonus to paid or revive a revoked payment.
					for _, target := range []int64{20, 80} {
						refunded, err := AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: debtCharge.FinancialChargeID,
							RefundID: fmt.Sprintf("other-refund-%d", target), CumulativeQuota: target, Reason: "fee_refund"}, token.Key)
						require.NoError(t, err)
						assert.Zero(t, refunded.ReversedCommissionAmountMicros)
					}
					require.NoError(t, db.First(&lot, lot.ID).Error)
					var wallet User
					var account AgencyFundingAccount
					require.NoError(t, db.First(&wallet, user.Id).Error)
					require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
					wantPaid, wantNonpaid := int64(60), int64(40)
					if chargeback {
						wantPaid, wantNonpaid = 50, 20
						assert.Equal(t, int64(10), lot.PaidRevoked)
						assert.Equal(t, int64(20), lot.BonusRevoked)
					}
					assert.Equal(t, wantPaid, lot.PaidAvailable)
					assert.Equal(t, wantNonpaid, lot.BonusAvailable)
					assert.Zero(t, lot.PaidDebtRepaid)
					assert.Zero(t, lot.BonusDebtRepaid)
					assert.Equal(t, wantPaid, account.PaidAvailable)
					assert.Equal(t, wantNonpaid, account.NonpaidAvailable)
					assert.Zero(t, account.DebtQuota)
					assert.Equal(t, wantPaid+wantNonpaid, int64(wallet.Quota))
					var finalToken Token
					require.NoError(t, db.First(&finalToken, token.Id).Error)
					assert.Zero(t, finalToken.UsedQuota)
					assert.Zero(t, finalToken.RemainQuota)
				})
			}
			t.Run("negative_debt_must_not_be_ignored", func(t *testing.T) {
				tx := connection.Begin()
				require.NoError(t, tx.Error)
				t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
				db, user, token, original := agencyComponentRefundFixture(t, 60, 40, []agencycontract.BillingComponent{
					{ComponentID: "fee", ChargedTotalQuota: 100, NoncommissionableQuota: 100},
				}, tx)
				agencyRefundDebtCharge(t, user, token, "valid-other-debt", 80)
				// The valid positive row exactly matches the account total. A
				// query restricted to positive rows would hide this corruption
				// and incorrectly authorize a new repayment/refund operation.
				require.NoError(t, db.Create(&AgencyFundingDebt{UserID: int64(user.Id),
					OriginOperationID: "corrupt-negative-debt", DebtKind: "model_charge",
					OriginalQuota: 1, OutstandingQuota: -1}).Error)
				var beforeAccount AgencyFundingAccount
				var beforeWallet User
				var beforeToken Token
				var beforeLot AgencyFundingLot
				var beforeDebts []AgencyFundingDebt
				require.NoError(t, db.Where("user_id = ?", user.Id).First(&beforeAccount).Error)
				require.NoError(t, db.First(&beforeWallet, user.Id).Error)
				require.NoError(t, db.First(&beforeToken, token.Id).Error)
				require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&beforeLot).Error)
				require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&beforeDebts).Error)
				_, err := AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id),
					ChargeID: original.FinancialChargeID, RefundID: "refund-with-corrupt-debt", CumulativeQuota: 100}, token.Key)
				require.ErrorIs(t, err, ErrAgencyComponentRefundProvenance)
				var account AgencyFundingAccount
				var wallet User
				var finalToken Token
				var lot AgencyFundingLot
				var debts []AgencyFundingDebt
				require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
				require.NoError(t, db.First(&wallet, user.Id).Error)
				require.NoError(t, db.First(&finalToken, token.Id).Error)
				require.NoError(t, db.First(&lot, beforeLot.ID).Error)
				require.NoError(t, db.Where("user_id = ?", user.Id).Order("id").Find(&debts).Error)
				assert.Equal(t, beforeAccount, account)
				assert.Equal(t, beforeWallet, wallet)
				assert.Equal(t, beforeToken, finalToken)
				assert.Equal(t, beforeLot, lot)
				assert.Equal(t, beforeDebts, debts)
				var repayments, reversals, restoredRows int64
				require.NoError(t, db.Model(&AgencyDebtRepayment{}).Count(&repayments).Error)
				require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("event_kind = ?", "agency.billing_reversed").Count(&reversals).Error)
				require.NoError(t, db.Model(&AgencyComponentFunding{}).Where("restored_paid_quota <> 0 OR restored_nonpaid_quota <> 0 OR restored_debt_quota <> 0").Count(&restoredRows).Error)
				assert.Zero(t, repayments)
				assert.Zero(t, reversals)
				assert.Zero(t, restoredRows)
			})
		})
	}
}

func TestAgencyBonusTopupRepaysDebtAndRetainsSourceOnChargeback(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse_%t", reverse), func(t *testing.T) {
			db, user, token, original := agencyComponentRefundFixture(t, 0, 0, []agencycontract.BillingComponent{
				{ComponentID: "fee", ChargedTotalQuota: 100, NoncommissionableQuota: 100},
			})
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := RecordAgencyTopup(tx, int64(user.Id), "payment", "bonus-repayment", "payment_callback", 30, 70); err != nil {
					return err
				}
				return tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 100)).Error
			}))
			var lot AgencyFundingLot
			require.NoError(t, db.Where("source_id = ?", "bonus-repayment").First(&lot).Error)
			assert.Equal(t, int64(30), lot.PaidDebtRepaid)
			assert.Equal(t, int64(70), lot.BonusDebtRepaid)
			assert.Zero(t, lot.PaidAvailable)
			assert.Zero(t, lot.BonusAvailable)
			if reverse {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					charges, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{UserID: int64(user.Id), SourceOperationID: "bonus-repayment",
						RefundID: "reverse-bonus-repayment", Quota: 60, EvidenceRef: "verified-receipt", Reason: "payment_refund"})
					assert.Empty(t, charges)
					return err
				}))
			}
			event, err := AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: original.FinancialChargeID,
				RefundID: "refund-bonus-debt", CumulativeQuota: 100, Reason: "fee_refund"}, token.Key)
			require.NoError(t, err)
			assert.Zero(t, event.ReversedCommissionAmountMicros)
			require.NoError(t, db.First(&lot, lot.ID).Error)
			var account AgencyFundingAccount
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
			wantPaid, wantNonpaid := int64(30), int64(70)
			if reverse {
				wantPaid, wantNonpaid = 0, 40
				assert.Equal(t, int64(30), lot.PaidRevoked)
				assert.Equal(t, int64(30), lot.BonusRevoked)
			}
			assert.Equal(t, wantPaid, lot.PaidAvailable)
			assert.Equal(t, wantNonpaid, lot.BonusAvailable)
			assert.Equal(t, wantPaid, account.PaidAvailable)
			assert.Equal(t, wantNonpaid, account.NonpaidAvailable)
			assert.Zero(t, account.DebtQuota)
			assert.Zero(t, lot.PaidDebtRepaid)
			assert.Zero(t, lot.BonusDebtRepaid)
		})
	}
}

func TestAgencyPaymentChargebackRejectsComponentSourceAtomically(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprintf("paid_%t", paid), func(t *testing.T) {
			paidQuota, bonusQuota := int64(0), int64(120)
			if paid {
				paidQuota, bonusQuota = 120, 0
			}
			db, user, token, original := agencyComponentRefundFixture(t, paidQuota, bonusQuota, []agencycontract.BillingComponent{
				{ComponentID: "fee", ChargedTotalQuota: 100, NoncommissionableQuota: 100},
			})
			var beforeLot AgencyFundingLot
			var beforeAccount AgencyFundingAccount
			var beforeTopup AgencyTopupFact
			var beforeJournal AgencyBillingJournal
			require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&beforeLot).Error)
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&beforeAccount).Error)
			require.NoError(t, db.Where("source_operation_id = ?", "component-refund-source-1").First(&beforeTopup).Error)
			require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&beforeJournal).Error)
			// The first 20 can be revoked from availability, but the remaining
			// 10 are component-funded. Unsupported attribution must roll back
			// that first step too, even when the component earned no commission.
			err := db.Transaction(func(tx *gorm.DB) error {
				_, _, err := ReverseAgencyTopupTx(tx, AgencyFundingReversalInput{UserID: int64(user.Id),
					SourceOperationID: "component-refund-source-1", RefundID: "component-source-chargeback",
					Quota: 30, EvidenceRef: "verified-refund", Reason: "payment_chargeback"})
				return err
			})
			require.ErrorIs(t, err, ErrAgencyComponentRefundProvenance)
			var lot AgencyFundingLot
			var account AgencyFundingAccount
			var topup AgencyTopupFact
			var journal AgencyBillingJournal
			var wallet User
			require.NoError(t, db.First(&lot, beforeLot.ID).Error)
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
			require.NoError(t, db.First(&topup, beforeTopup.ID).Error)
			require.NoError(t, db.First(&journal, beforeJournal.ID).Error)
			require.NoError(t, db.First(&wallet, user.Id).Error)
			assert.Equal(t, beforeLot, lot)
			assert.Equal(t, beforeAccount, account)
			assert.Equal(t, beforeTopup, topup)
			assert.Equal(t, beforeJournal, journal)
			assert.Equal(t, 20, wallet.Quota)
			var reversals, debts int64
			require.NoError(t, db.Model(&AgencyFundingReversal{}).Count(&reversals).Error)
			require.NoError(t, db.Model(&AgencyFundingDebt{}).Count(&debts).Error)
			assert.Zero(t, reversals)
			assert.Zero(t, debts)
			_, err = AgencyRefundWalletCharge(AgencyComponentRefundInput{UserID: int64(user.Id),
				ChargeID: original.FinancialChargeID, RefundID: "model-refund-after-rejected-chargeback", CumulativeQuota: 100}, token.Key)
			require.NoError(t, err, "rejected payment reversal must not damage later legitimate model refund")
		})
	}
}
