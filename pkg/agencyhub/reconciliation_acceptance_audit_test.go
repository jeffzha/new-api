//go:build agency_audit

package agencyhub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These opt-in acceptance tests express requirements from design §10.1/§16.2.
// They deliberately assert the required outcome, not the current defect.
func TestAgencyAuditReconciliationIssueBlocksPayment(t *testing.T) {
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "audit-payment", DisplayName: "Payment audit", Status: AgencyStatusActive, InviteCode: "AUDITPAY01", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	account := model.AgencyWithdrawalAccount{AgencyID: agency.ID, Version: 1, Ciphertext: "audit-encrypted-account", KeyID: "audit-key", Last4: "1234", Status: "active", CreatedAtMS: 1}
	require.NoError(t, app.db.Create(&account).Error)
	// The locked amount is backed by a withdrawal, but the commission equation
	// is broken: 10 + 100 != 200. Reconcile must prevent initiating payment.
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", EarnedMicros: 200, AvailableMicros: 10, LockedMicros: 100, Version: 1}).Error)
	hash := sha256.Sum256([]byte(account.Ciphertext))
	withdrawal := model.AgencyWithdrawal{RequestNo: "audit-payment", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 100, Status: "approved", Version: 1, AccountID: account.ID, AccountVersion: account.Version, AccountSnapshotHash: hex.EncodeToString(hash[:]), AccountSnapshot: account.Ciphertext, AccountSnapshotKeyID: account.KeyID, CreatedAtMS: 1, UpdatedAtMS: 1}
	require.NoError(t, app.db.Create(&withdrawal).Error)
	summary, err := app.Reconcile(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, summary.IssuesCreated)
	err = app.db.Transaction(func(tx *gorm.DB) error {
		return app.validateWithdrawalPayingGate(tx, withdrawal)
	})
	assert.Error(t, err, "an unresolved commission discrepancy must block starting a bank payment")
}

func TestAgencyAuditReconciliationDetectsUnbackedWithdrawalLock(t *testing.T) {
	app := newAgencyTestApp(t)
	// The aggregate equation balances, but there are no pending withdrawals
	// to explain the locked 20 micros. §16.2 requires both invariants.
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: 8, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 80, LockedMicros: 20, Version: 1}).Error)
	summary, err := app.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Positive(t, summary.IssuesCreated, "locked money without any pending withdrawal must be reported")
}

func TestAgencyAuditReconciliationDoesNotCompareDifferentCommittedVersions(t *testing.T) {
	// WAL allows a real writer to commit while reconciliation retains its
	// read snapshot, matching the production engines' snapshot behavior.
	db := reconciliationConcurrentDB(t, "sqlite")
	app := New(db, db, Config{})
	require.NoError(t, app.db.AutoMigrate(&model.User{}))
	user := model.User{Username: "audit-consistent-wallet", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, app.db.Create(&user).Error)
	require.NoError(t, app.db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 10, Version: 1}).Error)
	// Deterministically schedule an atomic, balanced wallet update immediately
	// after the funding-account read. There is no inconsistent committed state.
	var updateErr error
	updated := false
	callbackName := "agency_audit:balanced_wallet_update"
	require.NoError(t, app.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		_, inSnapshot := tx.Statement.ConnPool.(*sql.Tx)
		if updated || !inSnapshot || tx.Statement.Table != (model.AgencyFundingAccount{}).TableName() {
			return
		}
		updated = true
		updateErr = app.db.Transaction(func(writeTx *gorm.DB) error {
			if err := writeTx.Model(&model.User{}).Where("id = ?", user.Id).Update("quota", 9).Error; err != nil {
				return err
			}
			return writeTx.Model(&model.AgencyFundingAccount{}).Where("user_id = ?", user.Id).Updates(map[string]any{"paid_available": 9, "version": 2}).Error
		})
	}))
	t.Cleanup(func() { require.NoError(t, app.db.Callback().Query().Remove(callbackName)) })
	summary, err := app.Reconcile(context.Background())
	require.NoError(t, updateErr)
	require.True(t, updated, "the concurrent committed update must actually be exercised")
	require.NoError(t, err)
	assert.Zero(t, summary.IssuesCreated, "two valid wallet versions must not be combined into a false discrepancy")
}
