package agencyhub

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Every source is committed before Reconcile opens its own page snapshots.
// Cleanup is scoped to this fixture: external dialect suites share a database
// with other tests, whose findings must neither fail these tests nor be erased.
type reconciliationScanFixture struct {
	db      *gorm.DB
	app     *App
	rows    []any
	targets []model.AgencyReconciliationIssue
}

func newReconciliationScanFixture(t *testing.T, dialect string) *reconciliationScanFixture {
	t.Helper()
	db := reconciliationConcurrentDB(t, dialect)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	f := &reconciliationScanFixture{db: db, app: New(db, db, Config{})}
	t.Cleanup(func() {
		for _, target := range f.targets {
			require.NoError(t, db.Where("object_type = ? AND object_id = ?", target.ObjectType, target.ObjectID).Delete(&model.AgencyReconciliationIssue{}).Error)
		}
		for i := len(f.rows) - 1; i >= 0; i-- {
			require.NoError(t, db.Unscoped().Delete(f.rows[i]).Error)
		}
	})
	return f
}

func (f *reconciliationScanFixture) create(t *testing.T, row any) {
	t.Helper()
	require.NoError(t, f.db.Create(row).Error)
	f.rows = append(f.rows, row)
}

func (f *reconciliationScanFixture) observe(kind, id string) {
	f.targets = append(f.targets, model.AgencyReconciliationIssue{ObjectType: kind, ObjectID: id})
}

func TestReconciliationScanRejectsConservingNegativeBucketsAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newReconciliationScanFixture(t, dialect)
			for _, account := range []model.AgencyFundingAccount{
				{PaidAvailable: -1, NonpaidAvailable: 101},
				{PaidAvailable: 101, NonpaidAvailable: -1},
				{PaidAvailable: 99, DebtQuota: -1},
			} {
				user := model.User{Username: common.GetUUID()[:16], AffCode: common.GetUUID()[:16], Quota: 100}
				f.create(t, &user)
				account.UserID, account.Version = int64(user.Id), 1
				f.create(t, &account)
				f.observe("funding_account", stringID(account.UserID))
			}
			for _, lot := range []model.AgencyFundingLot{
				{PaidInitial: 100, PaidAvailable: 101, PaidReserved: -1},
				{BonusInitial: 10, BonusAvailable: 11, BonusConsumed: -1},
			} {
				lot.UserID, lot.SourceKind, lot.SourceID, lot.Version = 718291, "topup", common.GetUUID(), 1
				f.create(t, &lot)
				f.observe("funding_lot", stringID(lot.ID))
			}
			for _, balance := range []model.AgencyCommissionBalance{
				{EarnedMicros: -1, AvailableMicros: -1},
				{EarnedMicros: 100, ReversedMicros: -1, AvailableMicros: 101},
				{EarnedMicros: 100, LockedMicros: -1, AvailableMicros: 101},
				{EarnedMicros: 100, PaidMicros: -1, AvailableMicros: 101},
			} {
				id, err := strconv.ParseInt(common.GetUUID()[:12], 16, 64)
				require.NoError(t, err)
				balance.AgencyID, balance.CurrencyCode, balance.Version = id, "CNY", 1
				f.create(t, &balance)
				f.observe("commission_balance", stringID(id)+":CNY")
				// The negative lock also violates outstanding withdrawal totals.
				f.observe("withdrawal_lock", stringID(id)+":CNY")
			}
			_, err := f.app.Reconcile(context.Background())
			require.NoError(t, err)
			for _, target := range f.targets {
				if target.ObjectType == "withdrawal_lock" {
					continue
				}
				var issues []model.AgencyReconciliationIssue
				require.NoError(t, f.db.Where("object_type = ? AND object_id = ? AND status = ?", target.ObjectType, target.ObjectID, "open").Find(&issues).Error)
				require.Len(t, issues, 1, "%s:%s must be found even though its equation balances", target.ObjectType, target.ObjectID)
				assert.NotEmpty(t, issues[0].Difference)
				assert.NotEmpty(t, issues[0].EvidenceHash)
			}
		})
	}
}

func TestReconciliationScanProductionEvidenceAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newReconciliationScanFixture(t, dialect)
			user := model.User{Username: common.GetUUID()[:16], AffCode: common.GetUUID()[:16], Quota: 100}
			f.create(t, &user)
			f.create(t, &model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 80, NonpaidAvailable: 30, DebtQuota: 10, MoneySeq: 3, Version: 2})
			f.observe("funding_account", stringID(int64(user.Id)))
			lot := model.AgencyFundingLot{UserID: int64(user.Id), SourceKind: "topup", SourceID: common.GetUUID(), PaidInitial: 100, PaidAvailable: 20, PaidReserved: 20, PaidConsumed: 20, PaidRevoked: 20, PaidDebtRepaid: 20, BonusInitial: 12, BonusAvailable: 4, BonusReserved: 3, BonusConsumed: 2, BonusRevoked: 1, BonusExpired: 2, Version: 1}
			f.create(t, &lot)
			f.observe("funding_lot", stringID(lot.ID))
			agencyID, err := strconv.ParseInt(common.GetUUID()[:12], 16, 64)
			require.NoError(t, err)
			binding := model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: agencyID, Revision: 2, InviteSnapshot: "SCAN", CreatedSource: "test"}
			f.create(t, &binding)
			f.create(t, &model.AgencyActiveUserBinding{UserID: binding.UserID, AgencyID: binding.AgencyID, BindingID: binding.ID, Revision: binding.Revision})
			f.observe("active_binding", stringID(binding.UserID))
			// Reversing commission after a payout creates legitimate available
			// debt. Positive locked funds are still explained by a withdrawal.
			f.create(t, &model.AgencyCommissionBalance{AgencyID: agencyID, CurrencyCode: "CNY", EarnedMicros: 100, ReversedMicros: 50, AvailableMicros: -70, LockedMicros: 20, PaidMicros: 100, Version: 3})
			f.create(t, &model.AgencyWithdrawal{RequestNo: common.GetUUID(), AgencyID: agencyID, CurrencyCode: "CNY", AmountMicros: 20, Status: "payment_unknown", Version: 1})
			f.observe("commission_balance", stringID(agencyID)+":CNY")
			f.observe("withdrawal_lock", stringID(agencyID)+":CNY")

			outboxes := make([]*model.AgencyBillingOutbox, 0, 3)
			receipts := make([]*model.AgencySourceEvent, 0, 2)
			for index, state := range []string{"pending", "done", "skipped"} {
				id := common.GetUUID()
				event := agencycontract.BillingEvent{EventID: id, OperationID: id, FinancialChargeID: id,
					SchemaVersion: agencycontract.SchemaVersion, EventType: "agency.topup_completed", JournalRevision: 1,
					UserID: int64(user.Id), MoneySeq: int64(index + 1), EventCount: 1, OccurredAtMS: time.Now().UnixMilli(),
					CommissionSkipReason: "topup_noncommissionable"}
				payload, err := common.Marshal(event)
				require.NoError(t, err)
				hash, err := agencycontract.CanonicalHash(event)
				require.NoError(t, err)
				outbox := &model.AgencyBillingOutbox{EventID: id, OperationID: id, SchemaVersion: event.SchemaVersion, EventKind: event.EventType,
					UserID: event.UserID, MoneySeq: event.MoneySeq, EventCount: 1, Payload: string(payload), PayloadHash: hash}
				f.create(t, outbox)
				f.create(t, &model.AgencyBillingOperation{ChargeID: id, OperationID: id, Revision: 1, Operation: "topup", InputHash: hash, CommittedResult: string(payload), MoneySeq: event.MoneySeq, EventCount: 1})
				deliveryState := "pending"
				if state != "pending" {
					deliveryState = "done"
					receipt := &model.AgencySourceEvent{EventID: id, SourceOperationID: id, JournalRevision: 1, SchemaVersion: event.SchemaVersion,
						PayloadHash: hash, Payload: string(payload), UserID: event.UserID, MoneySeq: event.MoneySeq, ProcessingStatus: state}
					f.create(t, receipt)
					receipts = append(receipts, receipt)
				}
				f.create(t, &model.AgencyEventDelivery{EventID: id, Status: deliveryState})
				f.observe("billing_operation", id)
				f.observe("billing_outbox", id)
				outboxes = append(outboxes, outbox)
			}
			_, err = f.app.Reconcile(context.Background())
			require.NoError(t, err)
			for _, target := range f.targets {
				var issues int64
				require.NoError(t, f.db.Model(&model.AgencyReconciliationIssue{}).Where("object_type = ? AND object_id = ?", target.ObjectType, target.ObjectID).Count(&issues).Error)
				assert.Zero(t, issues, "healthy %s:%s must not create a finding", target.ObjectType, target.ObjectID)
			}

			// Preserve the count but break exact operation identity and index;
			// counting rows alone cannot detect either corruption.
			require.NoError(t, f.db.Delete(receipts[0]).Error)
			wrongEventID := common.GetUUID()
			require.NoError(t, f.db.Model(outboxes[0]).Update("event_id", wrongEventID).Error)
			f.observe("billing_outbox", wrongEventID)
			require.NoError(t, f.db.Model(outboxes[2]).Update("event_index", 1).Error)
			_, err = f.app.Reconcile(context.Background())
			require.NoError(t, err)
			for _, target := range []model.AgencyReconciliationIssue{
				{ObjectType: "billing_outbox", ObjectID: outboxes[1].EventID},
				{ObjectType: "billing_operation", ObjectID: outboxes[0].OperationID},
				{ObjectType: "billing_operation", ObjectID: outboxes[2].OperationID},
			} {
				var issues []model.AgencyReconciliationIssue
				require.NoError(t, f.db.Where("object_type = ? AND object_id = ? AND status = ?", target.ObjectType, target.ObjectID, "open").Find(&issues).Error)
				require.Len(t, issues, 1, "scanner must find %s:%s", target.ObjectType, target.ObjectID)
				assert.NotEmpty(t, issues[0].EvidenceHash)
			}
		})
	}
}

func TestReconciliationScanFindsCorruptionBeyondFirstPageAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, kind := range []string{"funding_account", "commission_balance"} {
				t.Run(kind, func(t *testing.T) {
					f := newReconciliationScanFixture(t, dialect)
					// 204 valid records plus one corruption in the second page
					// protect both the user_id and id pagination contracts.
					const recordCount, badIndex = 205, 201
					ids := make([]string, 0, recordCount)
					var before int64
					if kind == "funding_account" {
						require.NoError(t, f.db.Model(&model.AgencyFundingAccount{}).Count(&before).Error)
						users := make([]model.User, recordCount)
						for index := range users {
							users[index] = model.User{Username: common.GetUUID()[:16], AffCode: common.GetUUID()[:16], Quota: 100}
						}
						f.create(t, &users)
						accounts := make([]model.AgencyFundingAccount, recordCount)
						for index, user := range users {
							accounts[index] = model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 100, MoneySeq: 1, Version: 1}
							ids = append(ids, stringID(int64(user.Id)))
						}
						accounts[badIndex].PaidAvailable, accounts[badIndex].NonpaidAvailable = -1, 101
						f.create(t, &accounts)
					} else {
						require.NoError(t, f.db.Model(&model.AgencyCommissionBalance{}).Count(&before).Error)
						balances := make([]model.AgencyCommissionBalance, recordCount)
						for index := range balances {
							id, err := strconv.ParseInt(common.GetUUID()[:12], 16, 64)
							require.NoError(t, err)
							balances[index] = model.AgencyCommissionBalance{AgencyID: id, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 100, Version: 1}
							ids = append(ids, stringID(id)+":CNY")
						}
						balances[badIndex].ReversedMicros, balances[badIndex].AvailableMicros = -1, 101
						f.create(t, &balances)
					}
					for _, id := range ids {
						f.observe(kind, id)
					}
					summary, err := f.app.Reconcile(context.Background())
					require.NoError(t, err)
					checked := summary.CheckedFundingAccounts
					if kind == "commission_balance" {
						checked = summary.CheckedCommissionBalances
					}
					assert.Equal(t, int(before)+recordCount, checked)
					var issues []model.AgencyReconciliationIssue
					require.NoError(t, f.db.Where("object_type = ? AND object_id IN ? AND status = ?", kind, ids, "open").Find(&issues).Error)
					require.Len(t, issues, 1)
					assert.Equal(t, ids[badIndex], issues[0].ObjectID)
				})
			}
		})
	}
}
