package agencyhub

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestReconcileDetectsDurableInvariantBreaksAndIsIdempotent(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}))
	user := model.User{Username: "reconcile-broken", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, app.db.Create(&user).Error)
	require.NoError(t, app.db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 7, Version: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: 4, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 1, Version: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{EventID: "reconcile-missing-delivery", OperationID: "op-1", EventIndex: 0, EventCount: 1, EventKind: "agency.billing", UserID: int64(user.Id), Payload: "{}", PayloadHash: "hash", SchemaVersion: "v1", CreatedAtMS: 1}).Error)

	first, err := app.Reconcile(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, first.CheckedFundingAccounts)
	require.Equal(t, 1, first.CheckedCommissionBalances)
	require.Equal(t, 1, first.CheckedOutboxEvents)
	require.Equal(t, 1, first.MissingDeliveries)
	require.Equal(t, 3, first.IssuesCreated)
	require.Equal(t, int64(3), first.OpenIssuesAfter)

	second, err := app.Reconcile(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, second.IssuesCreated)
	require.Equal(t, int64(3), second.OpenIssuesAfter)
}

func TestReconcileHealthyProjectionHasNoIssues(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}))
	user := model.User{Username: "reconcile-healthy", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, app.db.Create(&user).Error)
	require.NoError(t, app.db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 10, Version: 1}).Error)
	history := model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: 8, Revision: 2, EffectiveAtMS: 1, CreatedAt: 1, InviteSnapshot: "INVITE", CreatedSource: "test"}
	require.NoError(t, app.db.Create(&history).Error)
	require.NoError(t, app.db.Create(&model.AgencyActiveUserBinding{UserID: int64(user.Id), BindingID: history.ID, AgencyID: history.AgencyID, Revision: history.Revision, UpdatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: 8, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 60, LockedMicros: 20, PaidMicros: 20, Version: 1}).Error)
	// Explain the locked commission with an outstanding withdrawal so the
	// reconciliation lock invariant is satisfied.
	require.NoError(t, app.db.Create(&model.AgencyWithdrawal{RequestNo: "reconcile-healthy-withdrawal", AgencyID: 8, CurrencyCode: "CNY", AmountMicros: 20, Status: "approved", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1}).Error)
	outbox := reconciliationOutboxFixture(t, app.db)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: outbox.EventID, Status: "pending", NextRetryAt: 0, CreatedAt: 1}).Error)

	summary, err := app.Reconcile(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, summary.CheckedFundingAccounts)
	require.Equal(t, 1, summary.CheckedCommissionBalances)
	require.Equal(t, 1, summary.CheckedBindings)
	require.Equal(t, 1, summary.CheckedOutboxEvents)
	require.Zero(t, summary.MissingDeliveries)
	require.Zero(t, summary.IssuesCreated)
	require.Zero(t, summary.OpenIssuesAfter)
}
