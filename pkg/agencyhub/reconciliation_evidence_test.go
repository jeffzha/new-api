package agencyhub

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func reconciliationOutboxFixture(t *testing.T, db *gorm.DB) model.AgencyBillingOutbox {
	t.Helper()
	id := common.GetUUID()
	event := agencycontract.BillingEvent{EventID: id, OperationID: id, SchemaVersion: agencycontract.SchemaVersion,
		EventType: "agency.topup_completed", FinancialChargeID: id, JournalRevision: 1, UserID: 718291, MoneySeq: 1, EventCount: 1,
		OccurredAtMS: time.Now().UnixMilli(), CommissionSkipReason: "topup_noncommissionable"}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	hash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	outbox := model.AgencyBillingOutbox{EventID: event.EventID, OperationID: event.OperationID, SchemaVersion: event.SchemaVersion,
		EventKind: event.EventType, UserID: event.UserID, MoneySeq: event.MoneySeq, EventCount: event.EventCount,
		Payload: string(payload), PayloadHash: hash}
	require.NoError(t, db.Create(&outbox).Error)
	return outbox
}

// Exercise the actual evidence queries on each supported database, including
// both read inspections and serializable mutation-time verification. Every
// fixture is rolled back; no external database is cleared or recreated here.
func TestReconciliationEvidenceAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector
			if dialect == "sqlite" {
				dialector = sqlite.Open(filepath.Join(t.TempDir(), "reconciliation.sqlite"))
			} else {
				dsn := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN"))
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("isolated external test database is not configured")
				}
				if dialect == "mysql" {
					dialector = mysql.Open(dsn)
				} else {
					dialector = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, model.MigrateAgency(db))
			require.NoError(t, db.AutoMigrate(&model.User{}))
			for _, kind := range []string{"funding_account", "commission_balance", "withdrawal_lock", "funding_lot", "active_binding", "billing_operation", "billing_outbox"} {
				for _, locked := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/locked=%t", kind, locked), func(t *testing.T) {
						isolation := sql.LevelRepeatableRead
						if locked {
							isolation = sql.LevelSerializable
						}
						tx := db.Begin(&sql.TxOptions{Isolation: isolation})
						require.NoError(t, tx.Error)
						t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
						issue := model.AgencyReconciliationIssue{ID: 9007199254740993, ObjectType: kind, Status: "open", EvidenceHash: "original-detection"}
						var source any
						var mutation map[string]any
						switch kind {
						case "funding_account":
							user := model.User{Username: common.GetUUID()[:12], AffCode: common.GetUUID()[:12], Quota: 100}
							require.NoError(t, tx.Create(&user).Error)
							account := model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 80, NonpaidAvailable: 30, DebtQuota: 10, MoneySeq: 3, Version: 2}
							require.NoError(t, tx.Create(&account).Error)
							issue.ObjectID = stringID(account.UserID)
							source, mutation = &account, map[string]any{"debt_quota": 11, "version": 3}
						case "commission_balance":
							balance := model.AgencyCommissionBalance{AgencyID: 718291, CurrencyCode: "CNY", EarnedMicros: 9007199254740993, AvailableMicros: 9007199254740990, LockedMicros: 1, PaidMicros: 2, Version: 1}
							require.NoError(t, tx.Create(&balance).Error)
							issue.ObjectID = "718291:CNY"
							source, mutation = &balance, map[string]any{"available_micros": 9007199254740989, "version": 2}
						case "withdrawal_lock":
							balance := model.AgencyCommissionBalance{AgencyID: 718291, CurrencyCode: "CNY", EarnedMicros: 60, LockedMicros: 60, Version: 1}
							require.NoError(t, tx.Create(&balance).Error)
							for index, status := range []string{"submitted", "reviewing", "approved", "on_hold", "paying", "payment_unknown", "paid", "cancelled", "rejected"} {
								require.NoError(t, tx.Create(&model.AgencyWithdrawal{RequestNo: common.GetUUID(), AgencyID: balance.AgencyID, CurrencyCode: "CNY", AmountMicros: 10, Status: status, Version: int64(index + 1)}).Error)
							}
							issue.ObjectID = "718291:CNY"
							source, mutation = &balance, map[string]any{"locked_micros": 59, "version": 2}
						case "funding_lot":
							lot := model.AgencyFundingLot{UserID: 718291, SourceKind: "topup", SourceID: common.GetUUID(), PaidInitial: 100, PaidAvailable: 20, PaidReserved: 20, PaidConsumed: 20, PaidRevoked: 20, PaidDebtRepaid: 20, BonusInitial: 12, BonusAvailable: 4, BonusReserved: 3, BonusConsumed: 2, BonusRevoked: 1, BonusExpired: 2, Version: 1}
							require.NoError(t, tx.Create(&lot).Error)
							issue.ObjectID = stringID(lot.ID)
							source, mutation = &lot, map[string]any{"bonus_available": 5, "version": 2}
						case "active_binding":
							binding := model.AgencyUserBinding{UserID: 718291, AgencyID: 718291, Revision: 2, InviteSnapshot: "BINDING", CreatedSource: "test"}
							require.NoError(t, tx.Create(&binding).Error)
							current := model.AgencyActiveUserBinding{UserID: binding.UserID, AgencyID: binding.AgencyID, BindingID: binding.ID, Revision: binding.Revision}
							require.NoError(t, tx.Create(&current).Error)
							issue.ObjectID = stringID(current.UserID)
							source, mutation = &current, map[string]any{"revision": 3}
						case "billing_operation":
							outbox := reconciliationOutboxFixture(t, tx)
							op := model.AgencyBillingOperation{ChargeID: outbox.OperationID, OperationID: outbox.OperationID, Revision: 1, Operation: "topup", InputHash: "source", CommittedResult: outbox.Payload, MoneySeq: outbox.MoneySeq, EventCount: 1}
							require.NoError(t, tx.Create(&op).Error)
							issue.ObjectID = op.OperationID
							source, mutation = &op, map[string]any{"money_seq": 2}
						case "billing_outbox":
							outbox := reconciliationOutboxFixture(t, tx)
							require.NoError(t, tx.Create(&model.AgencyEventDelivery{EventID: outbox.EventID, Status: "pending"}).Error)
							issue.ObjectID = outbox.EventID
							source, mutation = &outbox, map[string]any{"payload_hash": strings.Repeat("0", 64)}
						}
						before, err := reconciliationEvidence(tx, issue, locked)
						require.NoError(t, err)
						assert.Equal(t, "consistent", before.State, "%+v", before.Checks)
						assert.Equal(t, []string{"verify_resolved"}, before.AllowedActions)
						require.NoError(t, tx.Model(source).Updates(mutation).Error)
						after, err := reconciliationEvidence(tx, issue, locked)
						require.NoError(t, err)
						assert.Equal(t, "inconsistent", after.State, "%+v", after.Checks)
						assert.Empty(t, after.AllowedActions)
						assert.NotEqual(t, before.EvidenceHash, after.EvidenceHash)
						require.NoError(t, tx.Delete(source).Error)
						missing, err := reconciliationEvidence(tx, issue, locked)
						require.NoError(t, err)
						assert.Equal(t, "unsupported", missing.State)
						assert.Empty(t, missing.AllowedActions)
					})
				}
			}
		})
	}
}

func TestReconciliationRejectsConservingNegativeBucketsButAllowsCommissionDebt(t *testing.T) {
	client := newFinanceRootClient(t)
	for _, test := range []struct {
		name    string
		balance model.AgencyCommissionBalance
		state   string
	}{
		{"valid debt after payout reversal", model.AgencyCommissionBalance{AgencyID: 71, CurrencyCode: "CNY", EarnedMicros: 100, ReversedMicros: 50, PaidMicros: 100, AvailableMicros: -50}, "consistent"},
		{"negative locked hides available", model.AgencyCommissionBalance{AgencyID: 72, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 101, LockedMicros: -1}, "inconsistent"},
		{"negative reversal matches equation", model.AgencyCommissionBalance{AgencyID: 73, CurrencyCode: "CNY", EarnedMicros: 100, ReversedMicros: -1, AvailableMicros: 101}, "inconsistent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, client.app.db.Create(&test.balance).Error)
			issue := newReconciliationTestIssue(t, client.app, "commission_balance", fmt.Sprintf("%d:CNY", test.balance.AgencyID))
			assert.Equal(t, test.state, reconciliationDetail(t, client, issue.ID).State)
		})
	}
	lot := model.AgencyFundingLot{UserID: client.rootID, PaidInitial: 100, PaidAvailable: 101, PaidReserved: -1, Version: 1}
	require.NoError(t, client.app.db.Create(&lot).Error)
	issue := newReconciliationTestIssue(t, client.app, "funding_lot", stringID(lot.ID))
	assert.Equal(t, "inconsistent", reconciliationDetail(t, client, issue.ID).State)
	require.NoError(t, client.app.db.Model(&model.User{}).Where("id = ?", client.rootID).Update("quota", 100).Error)
	require.NoError(t, client.app.db.Create(&model.AgencyFundingAccount{UserID: client.rootID, PaidAvailable: 101, NonpaidAvailable: -1}).Error)
	issue = newReconciliationTestIssue(t, client.app, "funding_account", stringID(client.rootID))
	assert.Equal(t, "inconsistent", reconciliationDetail(t, client, issue.ID).State)
}

func TestReconciliationNeverRestoresCorruptOrUnfinishedOutboxEvidence(t *testing.T) {
	for _, flaw := range []string{"payload", "hash", "kind", "event_count", "receipt_hash", "receipt_processing"} {
		t.Run(flaw, func(t *testing.T) {
			client := newFinanceRootClient(t)
			outbox := reconciliationOutboxFixture(t, client.app.db)
			switch flaw {
			case "payload":
				require.NoError(t, client.app.db.Model(&outbox).Update("payload", "{").Error)
			case "hash":
				require.NoError(t, client.app.db.Model(&outbox).Update("payload_hash", strings.Repeat("0", 64)).Error)
			case "kind":
				require.NoError(t, client.app.db.Model(&outbox).Update("event_kind", "agency.billing_finalized").Error)
			case "event_count":
				require.NoError(t, client.app.db.Model(&outbox).Update("event_count", 2).Error)
			case "receipt_hash", "receipt_processing":
				receipt := model.AgencySourceEvent{EventID: outbox.EventID, SourceOperationID: outbox.OperationID, SchemaVersion: outbox.SchemaVersion, PayloadHash: outbox.PayloadHash, Payload: outbox.Payload, UserID: outbox.UserID, MoneySeq: outbox.MoneySeq, JournalRevision: 1, ProcessingStatus: "done"}
				if flaw == "receipt_hash" {
					receipt.PayloadHash = strings.Repeat("0", 64)
				} else {
					receipt.ProcessingStatus = "processing"
				}
				require.NoError(t, client.app.db.Create(&receipt).Error)
			}
			issue := newReconciliationTestIssue(t, client.app, "billing_outbox", outbox.EventID)
			v := reconciliationDetail(t, client, issue.ID)
			assert.Empty(t, v.AllowedActions)
			body := reconciliationBody(t, "restore_delivery", "resolved", v.EvidenceHash)
			response := client.post("/agency/api/v1/root/reconciliation/issues/"+stringID(issue.ID)+"/resolve", body, "reject-corrupt-delivery", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "corrupt-proof"))
			assert.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			var count int64
			require.NoError(t, client.app.db.Model(&model.AgencyEventDelivery{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}
