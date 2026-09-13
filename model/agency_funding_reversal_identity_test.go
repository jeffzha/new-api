package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAgencyFundingReversalImmutableEvidenceAcrossDialects(t *testing.T) {
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
			db, user, _ := agencyChargebackDebtIdentityFixture(t, connection, 100)
			require.NoError(t, db.Model(&AgencyTopupFact{}).Where("source_operation_id = ?", "original-payment").Update("currency_code", "CNY").Error)
			input := AgencyFundingReversalInput{RefundID: "immutable-refund", SourceOperationID: "original-payment", UserID: int64(user.Id), Quota: 25,
				CurrencyCode: "CNY", PaymentReference: "bank-payment-1", EvidenceRef: "verified-receipt-1", Reason: "confirmed chargeback"}
			var originalCharges []AgencyFundingReversalCharge
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var created bool
				originalCharges, created, err = ReverseAgencyTopupTx(tx, input)
				assert.True(t, created)
				return err
			}))
			require.Len(t, originalCharges, 1)
			for _, currency := range []string{"CNY", "", " cny "} {
				retry := input
				retry.CurrencyCode = currency
				retry.Reason = " confirmed chargeback "
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					charges, created, err := ReverseAgencyTopupTx(tx, retry)
					assert.False(t, created)
					assert.Equal(t, originalCharges, charges)
					return err
				}))
			}
			for _, field := range []string{"currency", "payment_reference", "evidence", "reason", "remove_evidence", "source", "quota"} {
				t.Run(field, func(t *testing.T) {
					changed := input
					switch field {
					case "currency":
						changed.CurrencyCode = "USD"
					case "payment_reference":
						changed.PaymentReference = "bank-payment-2"
					case "evidence":
						changed.EvidenceRef = "verified-receipt-2"
					case "reason":
						changed.Reason = "different operation"
					case "remove_evidence":
						changed.EvidenceRef = ""
					case "source":
						changed.SourceOperationID = "other-topup"
					case "quota":
						changed.Quota = 30
					}
					err := db.Transaction(func(tx *gorm.DB) error {
						_, _, err := ReverseAgencyTopupTx(tx, changed)
						return err
					})
					require.ErrorIs(t, err, ErrAgencyFundingReversalConflict)
				})
			}
			var wallet User
			var debt AgencyFundingDebt
			var fact AgencyTopupFact
			var receipt AgencyFundingReversal
			require.NoError(t, db.First(&wallet, user.Id).Error)
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&debt).Error)
			require.NoError(t, db.Where("source_operation_id = ?", input.SourceOperationID).First(&fact).Error)
			require.NoError(t, db.Where("refund_id = ?", input.RefundID).First(&receipt).Error)
			assert.Equal(t, -25, wallet.Quota)
			assert.Equal(t, int64(25), debt.OutstandingQuota)
			assert.Equal(t, int64(25), fact.RefundedQuota)
			assert.Equal(t, input.EvidenceRef, receipt.EvidenceRef)
			assert.Equal(t, input.PaymentReference, receipt.PaymentReference)
			assert.Equal(t, input.Reason, receipt.Reason)
			var events int64
			require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("event_kind = ? AND user_id = ?", "agency.funding_reversed", user.Id).Count(&events).Error)
			assert.Equal(t, int64(1), events)
		})
	}
}
