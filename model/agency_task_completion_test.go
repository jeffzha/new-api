package model

import (
	"errors"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func agencySubmittedTaskFixture(t *testing.T, basis AgencyTaskChargeBasis, reserve int, databases ...*gorm.DB) (*gorm.DB, Task, User, Token) {
	t.Helper()
	var db *gorm.DB
	if len(databases) > 0 {
		db = databases[0]
	} else {
		db = agencyDialectDB(t, "sqlite")
		pool, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Task{}, &TaskBillingReconciliation{}))
		require.NoError(t, MigrateAgency(db))
	}
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
	user := User{Username: "agency-task-customer", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := Token{UserId: user.Id, Key: "agency-task-token", Status: common.TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, int64(user.Id), "payment", "agency-task-payment", "payment_callback", 60, 40)
	}))
	pricing := &agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, SalesBPS: 8000, SettlementBPS: 5000,
		CommissionEligible: true, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	const chargeID = "agency-submitted-task-charge"
	_, err := TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, token.Id, reserve, token.Key, chargeID, int64(reserve), false, pricing)
	require.NoError(t, err)
	require.NoError(t, FreezeAgencyTaskChargeBasis(user.Id, chargeID, basis))
	require.NoError(t, BeginAgencyTaskSubmission(user.Id, chargeID, "agency-public-task", "request-hash", "/v1/videos"))
	require.NoError(t, RecordAgencyTaskSubmission(chargeID, "agency-public-task", "agency-provider-task"))
	task := Task{TaskID: "agency-public-task", UserId: user.Id, Status: TaskStatusInProgress, Progress: "50%", Quota: reserve,
		PrivateData: TaskPrivateData{TokenId: token.Id, BillingSource: TaskBillingSourceWallet, UpstreamTaskID: "agency-provider-task",
			BillingContext: &TaskBillingContext{AgencyChargeID: chargeID, AgencyPricing: pricing, ProviderBilling: basis.ProviderBilling}}}
	require.NoError(t, db.Create(&task).Error)
	return db, task, user, token
}

func TestAgencyTaskLifecycleAcrossDialects(t *testing.T) {
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
			db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion, ComponentBilling: true,
				Mode: "tokens", QuotaPerUnit: 10, ModelRatio: 1.25, OtherMultiplier: 2}, 100, tx)
			var journal AgencyBillingJournal
			require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
			assert.Equal(t, "submitted", journal.Status)
			var finalized int64
			require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("user_id = ? AND event_kind = ?", user.Id, "agency.billing_finalized").Count(&finalized).Error)
			assert.Zero(t, finalized, "acceptance must not create commission")
			task.Status, task.Progress, task.PrivateData.ResultURL = TaskStatusSuccess, "100%", "https://example.test/video.mp4"
			result, err := CompleteAgencyTask(&task, TaskStatusInProgress, 40)
			require.NoError(t, err)
			assert.True(t, result.Changed)
			assert.True(t, result.FinancialFinal)
			assert.Equal(t, -20, result.QuotaDelta)
			require.NoError(t, db.First(&user, user.Id).Error)
			require.NoError(t, db.First(&token, token.Id).Error)
			require.NoError(t, db.First(&journal, journal.ID).Error)
			assert.Equal(t, 20, user.Quota)
			assert.Equal(t, 20, token.RemainQuota)
			assert.Equal(t, 80, token.UsedQuota)
			assert.Equal(t, "finalized", journal.Status)
			assert.Equal(t, int64(80), journal.ChargedTotalQuota)
			assert.Equal(t, int64(50), journal.SettlementCostQuota)
			assert.Equal(t, int64(23), journal.CommissionAmountMicros, "paid-first final charge retains paid 60 of total 80; Round(30*60/80)=23")
			var outbox AgencyBillingOutbox
			require.NoError(t, db.Where("event_id = ?", task.PrivateData.BillingContext.AgencyBillingEventID).First(&outbox).Error)
			var event agencycontract.BillingEvent
			require.NoError(t, common.UnmarshalJsonStr(outbox.Payload, &event))
			assert.Equal(t, agencycontract.ComponentSchemaVersion, event.SchemaVersion)
			assert.True(t, event.FinancialFinal)
			require.Len(t, event.Components, 1)
			assert.Equal(t, int64(100), event.StandardQuota)
			replay, err := CompleteAgencyTask(&task, TaskStatusInProgress, 40)
			require.NoError(t, err)
			assert.False(t, replay.Changed)
			_, err = CompleteAgencyTask(&task, TaskStatusSuccess, 50)
			assert.ErrorIs(t, err, ErrAgencyChargeConflict)
			contradictory := task
			contradictory.Status = TaskStatusFailure
			_, err = CompleteAgencyTask(&contradictory, TaskStatusInProgress, 0)
			assert.ErrorIs(t, err, ErrAgencyChargeConflict)
			require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("user_id = ? AND event_kind = ?", user.Id, "agency.billing_finalized").Count(&finalized).Error)
			assert.Equal(t, int64(1), finalized)
		})
	}
}

func TestAgencyTaskFixedAndFreeChargesFinalizeWithZeroDelta(t *testing.T) {
	for _, test := range []struct {
		name    string
		price   float64
		reserve int
	}{{"fixed", 12.5, 100}, {"free", 0, 0}} {
		t.Run(test.name, func(t *testing.T) {
			db, task, _, _ := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
				Mode: "fixed", QuotaPerUnit: 10, ModelPrice: test.price, OtherMultiplier: 1}, test.reserve)
			task.Status = TaskStatusSuccess
			result, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
			require.NoError(t, err)
			assert.True(t, result.Changed)
			assert.True(t, result.FinancialFinal)
			assert.Zero(t, result.QuotaDelta)
			var journal AgencyBillingJournal
			require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
			assert.Equal(t, "finalized", journal.Status)
			assert.Equal(t, int64(test.reserve), journal.ChargedTotalQuota)
			assert.NotEmpty(t, task.PrivateData.BillingContext.AgencyBillingEventID)
		})
	}
}

func TestAgencyTaskConfirmedFailureCancelsOnce(t *testing.T) {
	db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		Mode: "fixed", QuotaPerUnit: 10, ModelPrice: 12.5, OtherMultiplier: 1}, 100)
	task.Status, task.FailReason = TaskStatusFailure, "provider confirmed generation failure"
	result, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, -100, result.QuotaDelta)
	replay, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	assert.Equal(t, "cancelled", journal.Status)
	assert.Zero(t, journal.CommissionAmountMicros)
	assert.Zero(t, journal.ChargedTotalQuota)
}

func TestAgencyTaskConfirmedFailureRestoresWalletAfterTokenDeletion(t *testing.T) {
	db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		Mode: "fixed", QuotaPerUnit: 10, ModelPrice: 12.5, OtherMultiplier: 1}, 100)
	require.NoError(t, db.Delete(&token).Error)
	task.Status, task.FailReason = TaskStatusFailure, "provider confirmed failure after token deletion"
	result, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
	require.NoError(t, err)
	assert.True(t, result.FinancialFinal)
	assert.Equal(t, -100, result.QuotaDelta)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 100, user.Quota)
	var deleted Token
	require.NoError(t, db.Unscoped().First(&deleted, token.Id).Error)
	assert.True(t, deleted.DeletedAt.Valid)
	assert.Equal(t, 100, deleted.UsedQuota, "the historical deleted token remains unchanged")
	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	assert.Equal(t, "cancelled", journal.Status)
	require.NotNil(t, journal.TokenID)
	assert.Equal(t, int64(token.Id), *journal.TokenID)
	var lots []AgencyFundingLot
	require.NoError(t, db.Where("user_id = ?", user.Id).Find(&lots).Error)
	require.Len(t, lots, 1)
	assert.Equal(t, int64(60), lots[0].PaidAvailable)
	assert.Equal(t, int64(40), lots[0].BonusAvailable)
}

func TestAgencyTaskFreezesEventSchemaAcrossRolloutSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			db, task, _, _ := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
				ComponentBilling: enabled, Mode: "fixed", QuotaPerUnit: 10, ModelPrice: 12.5, OtherMultiplier: 1}, 100)
			t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", strconv.FormatBool(!enabled))
			task.Status = TaskStatusSuccess
			_, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
			require.NoError(t, err)
			var outbox AgencyBillingOutbox
			require.NoError(t, db.Where("event_id = ?", task.PrivateData.BillingContext.AgencyBillingEventID).First(&outbox).Error)
			var event agencycontract.BillingEvent
			require.NoError(t, common.UnmarshalJsonStr(outbox.Payload, &event))
			if enabled {
				assert.Equal(t, agencycontract.ComponentSchemaVersion, event.SchemaVersion)
				assert.Len(t, event.Components, 1)
			} else {
				assert.Equal(t, agencycontract.SchemaVersion, event.SchemaVersion)
				assert.Empty(t, event.Components)
			}
		})
	}
}

func TestAgencyTaskFinalizationFailureRollsBackTaskAndAllMoney(t *testing.T) {
	db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		Mode: "tokens", QuotaPerUnit: 10, ModelRatio: 1.25, OtherMultiplier: 2}, 100)
	failure := errors.New("injected final outbox failure")
	const callback = "agency_test:task_terminal_outbox_failure"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (AgencyBillingOutbox{}).TableName() {
			tx.AddError(failure)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove(callback)) })
	task.Status = TaskStatusSuccess
	_, err := CompleteAgencyTask(&task, TaskStatusInProgress, 40)
	require.ErrorIs(t, err, failure)
	var persisted Task
	var journal AgencyBillingJournal
	require.NoError(t, db.First(&persisted, task.ID).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	assert.Equal(t, TaskStatus(TaskStatusInProgress), persisted.Status)
	assert.Equal(t, 100, persisted.Quota)
	assert.Zero(t, user.Quota)
	assert.Equal(t, 100, token.UsedQuota)
	assert.Equal(t, "submitted", journal.Status)
	var components int64
	require.NoError(t, db.Model(&AgencyChargeComponent{}).Where("charge_id = ?", journal.ChargeID).Count(&components).Error)
	assert.Zero(t, components)
	require.NoError(t, db.Callback().Create().Remove(callback))
	result, err := CompleteAgencyTask(&task, TaskStatusInProgress, 40)
	require.NoError(t, err)
	assert.True(t, result.FinancialFinal)
}

func TestAgencyTaskProviderBillFinalizesFromFrozenBasis(t *testing.T) {
	provider := &TaskProviderBillingSnapshot{Provider: TaskBillingProviderSeedanceDomestic, Currency: "CNY",
		UnitPricePerMillionTokens: "2.5", CNYPerUSD: "5", GroupRatio: 0.8, AsyncReconciliationRequired: true}
	db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		Mode: "cny_tokens", QuotaPerUnit: 1_000_000, OtherMultiplier: 1, ProviderBilling: provider}, 100)
	task.Status = TaskStatusSuccess
	accepted, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
	require.NoError(t, err)
	assert.True(t, accepted.BillingPending)
	assert.False(t, accepted.FinancialFinal)
	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	assert.Equal(t, "submitted", journal.Status)
	var receipt TaskBillingReconciliation
	require.NoError(t, db.Where("task_id = ?", task.ID).First(&receipt).Error)
	claimed, err := ClaimTaskBillingReconciliation(receipt.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	// A mutable resolver estimate is not allowed to replace the frozen units.
	input := TaskBillingReconciliationSettlement{ActualQuota: 777, TotalTokens: 200, SupplierAmountPaid: "0.0005"}
	settled, err := SettleTaskBillingReconciliation(receipt.ID, input)
	require.NoError(t, err)
	assert.True(t, settled.Applied)
	assert.Equal(t, -20, settled.QuotaDelta)
	assert.Equal(t, 80, settled.Task.Quota)
	replay, err := SettleTaskBillingReconciliation(receipt.ID, input)
	require.NoError(t, err)
	assert.False(t, replay.Applied)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&journal, journal.ID).Error)
	require.NoError(t, db.First(&receipt, receipt.ID).Error)
	assert.Equal(t, 20, user.Quota)
	assert.Equal(t, 20, token.RemainQuota)
	assert.Equal(t, "finalized", journal.Status)
	assert.Equal(t, int64(50), journal.SettlementCostQuota)
	assert.Equal(t, int64(23), journal.CommissionAmountMicros)
	assert.Equal(t, 80, receipt.ActualQuota)
	assert.Equal(t, TaskBillingReconciliationSettled, receipt.Status)
	statusReplay, err := CompleteAgencyTask(&task, TaskStatusSuccess, 0)
	require.NoError(t, err)
	assert.False(t, statusReplay.Changed)
	assert.True(t, statusReplay.FinancialFinal)
	_, err = CompleteAgencyTask(&task, TaskStatusSuccess, 201)
	assert.ErrorIs(t, err, ErrAgencyChargeConflict)
}

func TestAgencyTaskProviderBillCanRoundToZero(t *testing.T) {
	provider := &TaskProviderBillingSnapshot{Provider: TaskBillingProviderSeedanceDomestic, Currency: "CNY",
		UnitPricePerMillionTokens: "2.5", CNYPerUSD: "5", GroupRatio: 0.8, AsyncReconciliationRequired: true}
	db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		ComponentBilling: true, Mode: "cny_tokens", QuotaPerUnit: 1, OtherMultiplier: 1, ProviderBilling: provider}, 100)
	task.Status = TaskStatusSuccess
	_, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
	require.NoError(t, err)
	var receipt TaskBillingReconciliation
	require.NoError(t, db.Where("task_id = ?", task.ID).First(&receipt).Error)
	claimed, err := ClaimTaskBillingReconciliation(receipt.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	result, err := SettleTaskBillingReconciliation(receipt.ID, TaskBillingReconciliationSettlement{
		ActualQuota: 0, TotalTokens: 200, SupplierAmountPaid: "0.0005"})
	require.NoError(t, err)
	assert.True(t, result.Applied)
	assert.Equal(t, -100, result.QuotaDelta)
	assert.Zero(t, result.Task.Quota)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&receipt, receipt.ID).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Equal(t, TaskBillingReconciliationSettled, receipt.Status)
	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	assert.Equal(t, "finalized", journal.Status)
	assert.Zero(t, journal.ChargedTotalQuota)
	assert.Zero(t, journal.CommissionAmountMicros)
}

func TestAgencyTaskFixedChargeKeepsIntermediateRounding(t *testing.T) {
	basis := AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion, ComponentBilling: true,
		Mode: "fixed", QuotaPerUnit: 10, ModelPrice: 0.19, OtherMultiplier: 3}
	event, err := AgencyTaskFinalCharge(basis, agencycontract.PricingSnapshot{
		SalesBPS: 8000, SettlementBPS: 5000, CommissionEligible: true}, 0, false)
	require.NoError(t, err)
	assert.Equal(t, int64(3), event.StandardQuota)
	assert.Equal(t, int64(3), event.ChargedTotalQuota)
	assert.Zero(t, event.SettlementCostQuota, "cost truncates 0.19 * 10 * 0.5 before applying multiplier 3")
	assert.Equal(t, int64(3), event.TheoreticalCommissionQuota)
}

func TestAgencyTaskMissingUsageRemainsUnfinalized(t *testing.T) {
	db, task, user, token := agencySubmittedTaskFixture(t, AgencyTaskChargeBasis{Version: AgencyTaskChargeBasisVersion,
		Mode: "tokens", QuotaPerUnit: 10, ModelRatio: 1.25, OtherMultiplier: 2}, 100)
	task.Status = TaskStatusSuccess
	_, err := CompleteAgencyTask(&task, TaskStatusInProgress, 0)
	require.ErrorIs(t, err, ErrAgencyTaskUsagePending)
	require.NoError(t, MarkAgencyTaskReconcileRequired(&task, "final_usage_pending"))
	var persisted Task
	var journal AgencyBillingJournal
	require.NoError(t, db.First(&persisted, task.ID).Error)
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, TaskStatus(TaskStatusInProgress), persisted.Status)
	assert.Equal(t, "reconcile_required", journal.Status)
	assert.Equal(t, "final_usage_pending", journal.LastError)
	assert.Zero(t, user.Quota)
	assert.Equal(t, 100, token.UsedQuota)
	result, err := CompleteAgencyTask(&task, TaskStatusInProgress, 40)
	require.NoError(t, err)
	assert.True(t, result.FinancialFinal, "later authoritative usage can resolve the retained reservation")
}
