package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func agencyTaskReconciliationFixture(t *testing.T, databases ...*gorm.DB) (*gorm.DB, User, Token, Task, TaskBillingReconciliation, agencycontract.BillingEvent) {
	t.Helper()
	db, user, token, original := agencyComponentRefundFixture(t, 60, 40, []agencycontract.BillingComponent{
		{ComponentID: "model", ChargedTotalQuota: 80, CommissionableQuota: 80, SettlementCostQuota: 40, TheoreticalCommissionQuota: 40, CommissionEligible: true},
		{ComponentID: "fee", ChargedTotalQuota: 20, NoncommissionableQuota: 20},
	}, databases...)
	if len(databases) == 0 {
		require.NoError(t, db.AutoMigrate(&Task{}, &TaskBillingReconciliation{}))
	}
	task := Task{TaskID: "provider-reconciliation-public-task", UserId: user.Id, Quota: 100, Status: TaskStatusSuccess,
		Properties: Properties{OriginModelName: "provider-model", UpstreamModelName: "provider-model-v1"},
		PrivateData: TaskPrivateData{TokenId: token.Id, BillingSource: TaskBillingSourceWallet,
			BillingContext: &TaskBillingContext{AgencyChargeID: original.FinancialChargeID, AgencyBillingEventID: original.EventID}}}
	// A successful provider task retains its result alongside the billing context.
	task.SetData(map[string]string{"id": "provider-task-123", "status": "succeeded"})
	require.NoError(t, db.Create(&task).Error)
	record := TaskBillingReconciliation{TaskID: task.ID, Provider: TaskBillingProviderSeedanceDomestic,
		Status: TaskBillingReconciliationProcessing, UpstreamTaskID: "provider-task-123"}
	require.NoError(t, db.Create(&record).Error)
	return db, user, token, task, record, original
}

func TestAgencyTaskReconciliationComponentRefundAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			connection := agencyDialectDB(t, dialect)
			if connection == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := connection.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, connection.AutoMigrate(&User{}, &Token{}, &Task{}, &TaskBillingReconciliation{}))
			require.NoError(t, MigrateAgency(connection))
			tx := connection.Begin()
			require.NoError(t, tx.Error)
			t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
			db, user, token, task, record, original := agencyTaskReconciliationFixture(t, tx)
			input := TaskBillingReconciliationSettlement{ActualQuota: 60, TotalTokens: 1000, SupplierPrice: "2.5", SupplierDiscount: "1",
				SupplierAmountPaid: "0.0025", ExpenseTime: "2026-09-13T00:00:00Z"}
			result, err := SettleTaskBillingReconciliation(record.ID, input)
			require.NoError(t, err)
			require.True(t, result.Applied)
			assert.True(t, result.AgencyRefundCommitted)
			assert.Equal(t, 100, result.PreConsumedQuota)
			assert.Equal(t, -40, result.QuotaDelta)
			assert.Equal(t, 60, result.Task.Quota)
			var wallet User
			var persistedToken Token
			var journal AgencyBillingJournal
			var lot AgencyFundingLot
			require.NoError(t, db.First(&wallet, user.Id).Error)
			require.NoError(t, db.First(&persistedToken, token.Id).Error)
			require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
			require.NoError(t, db.Where("source_id = ?", "component-refund-source-1").First(&lot).Error)
			assert.Equal(t, 40, wallet.Quota)
			assert.Equal(t, -60, persistedToken.RemainQuota)
			assert.Equal(t, 60, persistedToken.UsedQuota)
			assert.Equal(t, int64(24), lot.PaidAvailable)
			assert.Equal(t, int64(16), lot.BonusAvailable)
			assert.Equal(t, int64(40), journal.ReversedQuota)
			assert.Equal(t, "partially_reversed", journal.Status)
			require.NoError(t, db.First(&task, task.ID).Error)
			assert.Equal(t, 60, task.Quota)
			assert.Equal(t, "provider-model", task.Properties.OriginModelName)
			assert.Equal(t, original.EventID, task.PrivateData.BillingContext.AgencyBillingEventID)
			require.NoError(t, db.First(&record, record.ID).Error)
			assert.Equal(t, TaskBillingReconciliationSettled, record.Status)
			assert.Equal(t, -40, record.QuotaDelta)
			assert.Equal(t, 100, record.PreConsumedQuota)
			replay, err := SettleTaskBillingReconciliation(record.ID, input)
			require.NoError(t, err)
			assert.False(t, replay.Applied)
			input.ActualQuota = 50
			_, err = SettleTaskBillingReconciliation(record.ID, input)
			require.ErrorIs(t, err, ErrAgencyChargeConflict)
			var reversals, deliveries int64
			require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "reverse").Count(&reversals).Error)
			require.NoError(t, db.Model(&AgencyEventDelivery{}).Where("event_id IN (?)", db.Model(&AgencyBillingOutbox{}).Select("event_id").Where("user_id = ? AND event_kind = ?", user.Id, "agency.billing_reversed")).Count(&deliveries).Error)
			assert.Equal(t, int64(1), reversals)
			assert.Equal(t, int64(1), deliveries)
		})
	}
}

func TestAgencyTaskReconciliationRejectsCorruptComponentEvidence(t *testing.T) {
	tests := []struct {
		name              string
		removeFinalize    bool
		removeComponents  bool
		forgetKnownEvent  bool
		mismatchCharge    bool
		mismatchEventID   bool
		mismatchPayload   bool
		downgradeFinalize bool
		targetQuota       int
	}{
		{name: "known_v2_event_without_finalize_or_components", removeFinalize: true, removeComponents: true, targetQuota: 120},
		{name: "components_without_finalize_or_known_event", removeFinalize: true, forgetKnownEvent: true, targetQuota: 120},
		{name: "known_event_belongs_to_another_charge", mismatchCharge: true, targetQuota: 60},
		{name: "known_event_id_differs_from_outbox_id", mismatchEventID: true, targetQuota: 60},
		{name: "known_event_payload_differs_from_finalize", mismatchPayload: true, targetQuota: 60},
		{name: "v2_event_with_legacy_finalize", downgradeFinalize: true, targetQuota: 120},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, _, _, task, record, original := agencyTaskReconciliationFixture(t)
			if test.removeFinalize {
				require.NoError(t, db.Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "finalize").Delete(&AgencyBillingOperation{}).Error)
			}
			if test.removeComponents {
				require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).Delete(&AgencyChargeComponent{}).Error)
			}
			if test.forgetKnownEvent {
				task.PrivateData.BillingContext.AgencyBillingEventID = ""
				require.NoError(t, db.Model(&task).Update("private_data", task.PrivateData).Error)
			}
			if test.mismatchCharge || test.mismatchEventID || test.mismatchPayload {
				corrupt := original
				if test.mismatchCharge {
					corrupt.FinancialChargeID = "unrelated-charge"
				}
				if test.mismatchEventID {
					corrupt.EventID = "unrelated-event"
				}
				if test.mismatchPayload {
					corrupt.BusinessStatus = "failed"
				}
				payload, err := common.Marshal(corrupt)
				require.NoError(t, err)
				require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("event_id = ?", original.EventID).Update("payload", string(payload)).Error)
			}
			if test.downgradeFinalize {
				legacy := original
				legacy.SchemaVersion = agencycontract.SchemaVersion
				legacy.Components = nil
				payload, err := common.Marshal(legacy)
				require.NoError(t, err)
				require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "finalize").Update("committed_result", string(payload)).Error)
			}
			// Capture persisted financial state after injecting the corruption.
			// Rejection must preserve the original problem for reconciliation,
			// without adding a legacy debit or partially restoring any source.
			snapshots := []struct {
				name   string
				before any
				after  any
			}{
				{"wallet", &[]User{}, &[]User{}},
				{"token", &[]Token{}, &[]Token{}},
				{"funding_account", &[]AgencyFundingAccount{}, &[]AgencyFundingAccount{}},
				{"funding_lots", &[]AgencyFundingLot{}, &[]AgencyFundingLot{}},
				{"funding_allocations", &[]AgencyFundingAllocation{}, &[]AgencyFundingAllocation{}},
				{"funding_ledger", &[]AgencyFundingLedger{}, &[]AgencyFundingLedger{}},
				{"components", &[]AgencyChargeComponent{}, &[]AgencyChargeComponent{}},
				{"component_funding", &[]AgencyComponentFunding{}, &[]AgencyComponentFunding{}},
				{"journal", &[]AgencyBillingJournal{}, &[]AgencyBillingJournal{}},
				{"operations", &[]AgencyBillingOperation{}, &[]AgencyBillingOperation{}},
				{"outbox", &[]AgencyBillingOutbox{}, &[]AgencyBillingOutbox{}},
				{"task", &[]Task{}, &[]Task{}},
				{"reconciliation_receipt", &[]TaskBillingReconciliation{}, &[]TaskBillingReconciliation{}},
			}
			for _, snapshot := range snapshots {
				require.NoError(t, db.Order(clause.OrderByColumn{Column: clause.PrimaryColumn}).Find(snapshot.before).Error, snapshot.name)
			}
			_, err := SettleTaskBillingReconciliation(record.ID, TaskBillingReconciliationSettlement{ActualQuota: test.targetQuota, TotalTokens: 1000})
			require.ErrorIs(t, err, ErrAgencyChargeConflict)
			for _, snapshot := range snapshots {
				require.NoError(t, db.Order(clause.OrderByColumn{Column: clause.PrimaryColumn}).Find(snapshot.after).Error, snapshot.name)
				assert.Equal(t, snapshot.before, snapshot.after, snapshot.name+" must remain unchanged")
			}
		})
	}
}

func TestAgencyTaskReconciliationPreservesUnfinalizedReservationAdjustment(t *testing.T) {
	for _, status := range []string{"reserved", "submitted"} {
		t.Run(status, func(t *testing.T) {
			db := agencyDialectDB(t, "sqlite")
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Task{}, &TaskBillingReconciliation{}))
			require.NoError(t, MigrateAgency(db))
			previousDB := DB
			DB = db
			t.Cleanup(func() { DB = previousDB })
			user := User{Username: "pending-task-user", Password: "password", Role: common.RoleCommonUser,
				Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
			require.NoError(t, db.Create(&user).Error)
			token := Token{UserId: user.Id, Key: "pending-task-token", Status: common.TokenStatusEnabled, UnlimitedQuota: true}
			require.NoError(t, db.Create(&token).Error)
			require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return RecordAgencyTopup(tx, int64(user.Id), "payment", "pending-task-payment", "payment_callback", 100, 0)
			}))
			const chargeID = "pending-task-charge"
			snapshot := agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, CommissionEligible: true,
				CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
			_, err = TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, token.Id, 60, token.Key, chargeID, 60, true, &snapshot)
			require.NoError(t, err)
			if status == "submitted" {
				require.NoError(t, BeginAgencyTaskSubmission(user.Id, chargeID, "pending-task-public", "request-hash", "/v1/videos"))
				require.NoError(t, RecordAgencyTaskSubmission(chargeID, "pending-task-public", "pending-task-provider"))
			}
			task := Task{TaskID: "pending-task-public", UserId: user.Id, Quota: 60, Status: TaskStatusSuccess,
				PrivateData: TaskPrivateData{TokenId: token.Id, BillingSource: TaskBillingSourceWallet,
					BillingContext: &TaskBillingContext{AgencyChargeID: chargeID}}}
			require.NoError(t, db.Create(&task).Error)
			record := TaskBillingReconciliation{TaskID: task.ID, Provider: TaskBillingProviderSeedanceDomestic,
				Status: TaskBillingReconciliationProcessing, UpstreamTaskID: "pending-task-provider"}
			require.NoError(t, db.Create(&record).Error)
			result, err := SettleTaskBillingReconciliation(record.ID, TaskBillingReconciliationSettlement{ActualQuota: 40, TotalTokens: 1000})
			require.NoError(t, err)
			assert.True(t, result.Applied)
			assert.False(t, result.AgencyRefundCommitted)
			assert.Equal(t, -20, result.QuotaDelta)
			require.NoError(t, db.First(&user, user.Id).Error)
			require.NoError(t, db.First(&token, token.Id).Error)
			require.NoError(t, db.First(&task, task.ID).Error)
			require.NoError(t, db.First(&record, record.ID).Error)
			var account AgencyFundingAccount
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
			assert.Equal(t, 60, user.Quota)
			assert.Equal(t, int64(60), account.PaidAvailable)
			assert.Equal(t, 40, token.UsedQuota)
			assert.Equal(t, -40, token.RemainQuota)
			assert.Equal(t, 40, task.Quota)
			assert.Equal(t, TaskBillingReconciliationSettled, record.Status)
			var reversals int64
			require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", chargeID, "reverse").Count(&reversals).Error)
			assert.Zero(t, reversals, "a reservation release must not manufacture a finalized model-sale refund")
		})
	}
}

func TestAgencyTaskReconciliationRejectsHigherFinalComponentBill(t *testing.T) {
	db, user, token, task, record, original := agencyTaskReconciliationFixture(t)
	_, err := SettleTaskBillingReconciliation(record.ID, TaskBillingReconciliationSettlement{ActualQuota: 120, TotalTokens: 1000})
	require.ErrorIs(t, err, ErrAgencyChargeConflict)
	var wallet User
	var persistedToken Token
	var journal AgencyBillingJournal
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.First(&persistedToken, token.Id).Error)
	require.NoError(t, db.First(&task, task.ID).Error)
	require.NoError(t, db.First(&record, record.ID).Error)
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
	assert.Zero(t, wallet.Quota)
	assert.Equal(t, 100, persistedToken.UsedQuota)
	assert.Equal(t, 100, task.Quota)
	assert.Equal(t, TaskBillingReconciliationProcessing, record.Status)
	assert.Equal(t, int64(100), journal.ChargedTotalQuota)
	assert.Zero(t, journal.ReversedQuota)
}

func TestAgencyTaskReconciliationUnchangedBillCompletesWithoutRefundEvent(t *testing.T) {
	db, _, _, task, record, original := agencyTaskReconciliationFixture(t)
	result, err := SettleTaskBillingReconciliation(record.ID, TaskBillingReconciliationSettlement{ActualQuota: 100, TotalTokens: 1000})
	require.NoError(t, err)
	assert.False(t, result.Applied)
	assert.False(t, result.AgencyRefundCommitted)
	assert.Zero(t, result.QuotaDelta)
	require.NoError(t, db.First(&record, record.ID).Error)
	assert.Equal(t, TaskBillingReconciliationSettled, record.Status)
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.Equal(t, 100, task.Quota)
	var reversals int64
	require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "reverse").Count(&reversals).Error)
	assert.Zero(t, reversals)
}

func TestAgencyTaskReconciliationTaskCASFailureRollsBackRefundAndReceipt(t *testing.T) {
	db, user, token, task, record, original := agencyTaskReconciliationFixture(t)
	failure := errors.New("injected task write failure")
	const callback = "agency_test:reconciliation_task_failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "tasks" {
			tx.AddError(failure)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(callback)) })
	input := TaskBillingReconciliationSettlement{ActualQuota: 60, TotalTokens: 1000}
	_, err := SettleTaskBillingReconciliation(record.ID, input)
	require.ErrorIs(t, err, failure)
	var wallet User
	var persistedToken Token
	var journal AgencyBillingJournal
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.First(&persistedToken, token.Id).Error)
	require.NoError(t, db.First(&task, task.ID).Error)
	require.NoError(t, db.First(&record, record.ID).Error)
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
	assert.Zero(t, wallet.Quota)
	assert.Equal(t, 100, persistedToken.UsedQuota)
	assert.Equal(t, 100, task.Quota)
	assert.Equal(t, TaskBillingReconciliationProcessing, record.Status)
	assert.Zero(t, journal.ReversedQuota)
	var reversals int64
	require.NoError(t, db.Model(&AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "reverse").Count(&reversals).Error)
	assert.Zero(t, reversals)
	require.NoError(t, db.Callback().Update().Remove(callback))
	result, err := SettleTaskBillingReconciliation(record.ID, input)
	require.NoError(t, err)
	assert.True(t, result.Applied)
}

func TestAgencyTaskReconciliationDeletedTokenRetainsChargeAndEvidence(t *testing.T) {
	db, user, token, task, record, original := agencyTaskReconciliationFixture(t)
	require.NoError(t, db.Delete(&token).Error)
	result, err := SettleTaskBillingReconciliation(record.ID, TaskBillingReconciliationSettlement{ActualQuota: 60, TotalTokens: 1000})
	require.NoError(t, err)
	assert.True(t, result.TokenUnavailable)
	assert.False(t, result.Applied)
	assert.Zero(t, result.QuotaDelta)
	var wallet User
	var journal AgencyBillingJournal
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.First(&task, task.ID).Error)
	require.NoError(t, db.First(&record, record.ID).Error)
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
	assert.Zero(t, wallet.Quota)
	assert.Equal(t, 100, task.Quota)
	assert.Zero(t, journal.ReversedQuota)
	assert.Equal(t, TaskBillingReconciliationSettled, record.Status)
	assert.Equal(t, 60, record.ActualQuota, "provider evidence retained even when refund is blocked")
	assert.Equal(t, 100, record.PreConsumedQuota)
	assert.Contains(t, record.LastError, "token unavailable")
}
