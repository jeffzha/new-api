package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func agencyAsyncRefundFixture(t *testing.T) (*gorm.DB, *model.Task, model.User, model.Token, agencycontract.BillingEvent) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:agency-async-refund-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Task{}, &model.Channel{}, &model.Log{}))
	require.NoError(t, model.MigrateAgency(db))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		require.NoError(t, pool.Close())
	})
	user := model.User{Username: "async-refund-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 100, UsedQuota: 100, RequestCount: 1}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "async-refund-token-key", Status: common.TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "async-refund-channel", UsedQuota: 100}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.RecordAgencyTopup(tx, int64(user.Id), "payment", "async-refund-payment", "payment_callback", 60, 40)
	}))
	snapshot := agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, CommissionEligible: true,
		CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	_, err = model.TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, token.Id, 100, token.Key, "async-refund-charge", 100, false, &snapshot)
	require.NoError(t, err)
	original, err := model.AgencyCommitWalletCharge(agencycontract.BillingEvent{UserID: int64(user.Id), FinancialChargeID: "async-refund-charge",
		BusinessStatus: "success", BillingStatus: "settled", ChargedTotalQuota: 100, CommissionableQuota: 80, NoncommissionableQuota: 20,
		SettlementCostQuota: 40, TheoreticalCommissionQuota: 40,
		Components: []agencycontract.BillingComponent{
			{ComponentID: "model", ChargedTotalQuota: 80, CommissionableQuota: 80, SettlementCostQuota: 40, TheoreticalCommissionQuota: 40, CommissionEligible: true},
			{ComponentID: "fee", ChargedTotalQuota: 20, NoncommissionableQuota: 20},
		}}, token.Key)
	require.NoError(t, err)
	task := &model.Task{TaskID: "async-refund-public-task", UserId: user.Id, ChannelId: channel.Id, Quota: 100,
		Status: model.TaskStatusSuccess, Group: "default", PrivateData: model.TaskPrivateData{BillingSource: model.TaskBillingSourceWallet, TokenId: token.Id,
			BillingContext: &model.TaskBillingContext{AgencyChargeID: original.FinancialChargeID, AgencyBillingEventID: original.EventID}}}
	require.NoError(t, db.Create(task).Error)
	return db, task, user, token, original
}

func TestAgencyAsyncTaskRefundUsesOriginalCumulativeTargetAndOneAtomicEvent(t *testing.T) {
	db, task, user, token, original := agencyAsyncRefundFixture(t)
	ctx := context.Background()
	stale := *task
	RecalculateTaskQuota(ctx, task, 80, "first provider correction")
	assert.Equal(t, 80, task.Quota)
	// A stale polling copy must not refund the first 20 quota again.
	RecalculateTaskQuota(ctx, &stale, 80, "duplicate provider correction")
	RecalculateTaskQuota(ctx, task, 60, "second provider correction")
	assert.Equal(t, 60, task.Quota)
	var partial model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&partial).Error)
	assert.Equal(t, int64(40), partial.ReversedQuota, "20 then 40 cumulative, not 20 then 20")
	require.True(t, RefundTaskQuota(ctx, task, "confirmed full after-sale refund"))
	require.True(t, RefundTaskQuota(ctx, task, "duplicate full refund"))
	assert.Zero(t, task.Quota)
	var persisted model.Task
	var wallet model.User
	var finalToken model.Token
	var lot model.AgencyFundingLot
	var journal model.AgencyBillingJournal
	require.NoError(t, db.First(&persisted, task.ID).Error)
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.First(&finalToken, token.Id).Error)
	require.NoError(t, db.Where("source_id = ?", "async-refund-payment").First(&lot).Error)
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
	assert.Zero(t, persisted.Quota)
	assert.Equal(t, 100, wallet.Quota)
	assert.Zero(t, wallet.UsedQuota)
	assert.Equal(t, 1, wallet.RequestCount)
	assert.Equal(t, 100, finalToken.RemainQuota)
	assert.Zero(t, finalToken.UsedQuota)
	assert.Equal(t, int64(60), lot.PaidAvailable)
	assert.Equal(t, int64(40), lot.BonusAvailable)
	assert.Equal(t, int64(100), journal.ReversedQuota)
	assert.Equal(t, original.CommissionAmountMicros, journal.ReversedCommissionQuota)
	assert.Equal(t, "reversed", journal.Status)
	var operations []model.AgencyBillingOperation
	require.NoError(t, db.Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "reverse").Find(&operations).Error)
	require.Len(t, operations, 3)
	var reversed int64
	for _, operation := range operations {
		var event agencycontract.BillingEvent
		require.NoError(t, common.UnmarshalJsonStr(operation.CommittedResult, &event))
		require.NoError(t, agencycontract.ValidateBillingComponents(event))
		assert.Equal(t, original.EventID, event.OriginalEventID)
		reversed += event.ReversedCommissionAmountMicros
	}
	assert.Equal(t, original.CommissionAmountMicros, reversed)
}

func TestAgencyAsyncTaskRefundRollsBackFinancialCommitWhenTaskCASFails(t *testing.T) {
	db, task, user, token, original := agencyAsyncRefundFixture(t)
	failure := errors.New("injected task marker persistence failure")
	const callback = "agency_async_test:fail_task_update"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "tasks" {
			tx.AddError(failure)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(callback)) })
	assert.False(t, RefundTaskQuota(context.Background(), task, "full refund"))
	assert.Equal(t, 100, task.Quota)
	var wallet model.User
	var finalToken model.Token
	var persisted model.Task
	var lot model.AgencyFundingLot
	var journal model.AgencyBillingJournal
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.First(&finalToken, token.Id).Error)
	require.NoError(t, db.First(&persisted, task.ID).Error)
	require.NoError(t, db.Where("source_id = ?", "async-refund-payment").First(&lot).Error)
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
	assert.Zero(t, wallet.Quota)
	assert.Zero(t, finalToken.RemainQuota)
	assert.Equal(t, 100, finalToken.UsedQuota)
	assert.Equal(t, 100, persisted.Quota)
	assert.Equal(t, int64(60), lot.PaidConsumed)
	assert.Equal(t, int64(40), lot.BonusConsumed)
	assert.Zero(t, journal.ReversedQuota)
	var reversals int64
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", original.FinancialChargeID, "reverse").Count(&reversals).Error)
	assert.Zero(t, reversals)
	// A retry after restoring persistence completes once with the same target.
	require.NoError(t, db.Callback().Update().Remove(callback))
	require.True(t, RefundTaskQuota(context.Background(), task, "full refund retry"))
	assert.Zero(t, task.Quota)
}

func TestAgencyAsyncTaskRefundRetainsDeletedTokenAndRejectsPostFinalizationDebit(t *testing.T) {
	db, task, user, token, original := agencyAsyncRefundFixture(t)
	RecalculateTaskQuota(context.Background(), task, 120, "contradictory larger final usage")
	assert.Equal(t, 100, task.Quota)
	var wallet model.User
	require.NoError(t, db.First(&wallet, user.Id).Error)
	assert.Zero(t, wallet.Quota, "a finalized component basis cannot be incremented through legacy task adjustment")
	require.NoError(t, db.Delete(&model.Token{}, token.Id).Error)
	require.True(t, RefundTaskQuota(context.Background(), task, "deleted-token historical refund"))
	assert.Equal(t, 100, task.Quota)
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", original.FinancialChargeID).First(&journal).Error)
	assert.Zero(t, journal.ReversedQuota)
	require.NoError(t, db.First(&wallet, user.Id).Error)
	assert.Zero(t, wallet.Quota)
	var liveTokens int64
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Count(&liveTokens).Error)
	assert.Zero(t, liveTokens)
}
