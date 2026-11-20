package opswallet

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestUSDWalletAdmissionSurvivesRetriesAndReconcilesOnlyPostedCharges(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-hold-test")
	previousRedis, previousBatch, previousUnit := common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit
	common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = false, false, 500_000
	t.Cleanup(func() {
		common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = previousRedis, previousBatch, previousUnit
	})
	var driver gorm.Dialector = sqlite.Open("file:ops-wallet-holds?mode=memory&cache=shared")
	switch engine := os.Getenv("OPS_NATIVE_TEST_ENGINE"); engine {
	case "mysql":
		driver = mysql.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "postgres":
		driver = postgres.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "", "sqlite":
	default:
		t.Fatal("unsupported wallet admission database")
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	if db.Dialector.Name() == "sqlite" {
		pool.SetMaxOpenConns(1)
	} else {
		pool.SetMaxOpenConns(4)
	}
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseType(db.Dialector.Name()), common.DatabaseType(db.Dialector.Name()))
	t.Cleanup(func() { common.SetDatabaseTypes(previousMain, previousLog) })
	// Upgrade a populated released wallet schema; repeat the additive migration.
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	user := model.User{Username: "hold-test", Quota: 12345, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "test-only-hold-token", RemainQuota: 10000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&token).Error)
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.OPSWalletAdmission{}, &model.OPSWalletHold{}, &model.OPSWalletUsage{}))
	}
	identity := model.OPSWalletHoldIdentity{RequestID: "first", UserID: user.Id, TokenID: token.Id, Model: "model-test"}
	position := model.OPSWalletPosition{UserID: user.Id, EntryID: 10, Quota: 150}
	first, err := model.ReserveOPSWallet(db, identity, 100, position)
	require.NoError(t, err)
	replayed, err := model.ReserveOPSWallet(db.Session(&gorm.Session{NewDB: true}), identity, 100, position)
	require.NoError(t, err)
	assert.Equal(t, first, replayed, "a new process cannot reserve the same request twice")
	second := identity
	second.RequestID = "second"
	_, err = model.ReserveOPSWallet(db, second, 80, position)
	require.ErrorIs(t, err, model.ErrOPSInsufficientAvailable)
	require.NoError(t, model.FinalizeOPSWallet(db, identity.RequestID, user.Id, 60, false))
	require.NoError(t, model.FinalizeOPSWallet(db, identity.RequestID, user.Id, 60, false))
	require.ErrorIs(t, model.FinalizeOPSWallet(db, identity.RequestID, user.Id, 61, false), model.ErrOPSWalletHoldConflict)
	require.ErrorIs(t, model.FinalizeOPSWallet(db, identity.RequestID, user.Id, 0, true), model.ErrOPSWalletHoldConflict)
	_, err = model.ReserveOPSWallet(db, second, 60, position)
	require.ErrorIs(t, err, model.ErrOPSInsufficientAvailable, "settlement without an OPS debit must retain capacity")
	require.ErrorIs(t, model.ReconcileOPSWallet(db, identity.RequestID, user.Id, identity.Model, 11, position), model.ErrOPSBalanceStale)
	posted := model.OPSWalletPosition{UserID: user.Id, EntryID: 11, Quota: 90}
	require.ErrorIs(t, model.ReconcileOPSWallet(db, identity.RequestID, user.Id, "wrong-model", 11, posted), model.ErrOPSWalletHoldConflict)
	require.NoError(t, model.ReconcileOPSWallet(db, identity.RequestID, user.Id, identity.Model, 11, posted))
	require.NoError(t, model.ReconcileOPSWallet(db, identity.RequestID, user.Id, identity.Model, 11, posted))
	_, err = model.ReserveOPSWallet(db, second, 80, position)
	require.ErrorIs(t, err, model.ErrOPSBalanceStale, "an older node cannot reauthorize the pre-debit balance")
	_, err = model.ReserveOPSWallet(db, second, 80, posted)
	require.NoError(t, err)
	require.NoError(t, model.FinalizeOPSWallet(db, second.RequestID, user.Id, 0, true))
	require.NoError(t, model.FinalizeOPSWallet(db, second.RequestID, user.Id, 0, true))
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 9940, token.RemainQuota)
	assert.Equal(t, 60, token.UsedQuota)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 12345, user.Quota, "OPS admission must never write the source wallet")

	// Two instances race for the same snapshot: only one 75-quota request fits.
	position = posted
	position.EntryID, position.Quota = 12, 100
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"race-a", "race-b"} {
		wg.Go(func() {
			<-start
			i := identity
			i.RequestID = id
			_, err := model.ReserveOPSWallet(db.Session(&gorm.Session{NewDB: true}), i, 75, position)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, model.ErrOPSInsufficientAvailable)
			rejected++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, rejected)
	var account model.OPSWalletAdmission
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(75), account.PendingQuota)
	assert.True(t, db.Migrator().HasIndex(&model.OPSWalletHold{}, "idx_ops_hold_reconcile"))

	// Exercise the BillingSettler contract and a replacement reconciliation
	// worker against the same database, without mutating the local wallet.
	sessionUser := model.User{Username: "session-test", AffCode: "session-only", Quota: 54321, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&sessionUser).Error)
	sessionToken := model.Token{UserId: sessionUser.Id, Key: "test-only-session-token", RemainQuota: 1000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&sessionToken).Error)
	var balanceMu sync.Mutex
	balance, ledger := "0.0002", 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/api/v1/catalog/v1/balances/"+fmt.Sprint(sessionUser.Id), req.URL.Path)
		balanceMu.Lock()
		defer balanceMu.Unlock()
		_, err := fmt.Fprintf(w, `{"data":{"currency":"USD","userId":%q,"balance":%q,"balanceQuota":%d,"negative":false,"lastEntryId":%d,"updatedAt":"2026-11-20T01:00:00Z","station":{"quotaPerUnit":1000000,"usdExchangeRate":"1"}}}`, fmt.Sprint(sessionUser.Id), balance, map[string]int{"0.0002": 200, "0.00008": 80}[balance], ledger)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	reader := opsbilling.New(opsbilling.Config{BaseURL: server.URL, KeyID: "test-usd", Secret: "test-only-session-secret", Currency: "USD", Timeout: time.Second})
	info := &relaycommon.RelayInfo{RequestId: "session", UserId: sessionUser.Id, TokenId: sessionToken.Id, TokenKey: sessionToken.Key, OriginModelName: "model-test"}
	session, err := NewSession(t.Context(), db, reader, info, 75)
	require.NoError(t, err)
	assert.Equal(t, "ops", info.BillingSource)
	assert.Equal(t, 75, session.GetPreConsumedQuota())
	assert.True(t, session.NeedsRefund())
	usageEvent, err := common.Marshal(map[string]any{"requestId": "session", "userId": fmt.Sprint(sessionUser.Id), "model": "model-test", "status": "success", "currency": "USD", "amount": "0.00012", "extra": map[string]any{"billing_scope": opsauthority.Scope(), "project_relay_terminal": true, "recordKind": "usage", "direction": "debit"}})
	require.NoError(t, err)
	require.ErrorIs(t, session.Settle(60), model.ErrOPSWalletHoldConflict, "settlement requires a durable usage fact")
	rollbackUsage := errors.New("synthetic usage rollback")
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		if err := model.FinalizeOPSRelay(tx, "session", sessionUser.Id, 60, usageEvent); err != nil {
			return err
		}
		return rollbackUsage
	}), rollbackUsage)
	var outboxCount int64
	require.NoError(t, db.Model(&model.OPSWalletUsage{}).Count(&outboxCount).Error)
	assert.Zero(t, outboxCount)
	require.NoError(t, session.SettleUsage(60, usageEvent))
	require.NoError(t, session.SettleUsage(60, usageEvent))
	require.NoError(t, db.Model(&model.OPSWalletUsage{}).Count(&outboxCount).Error)
	assert.Equal(t, int64(1), outboxCount)
	require.ErrorIs(t, session.SettleUsage(60, append(append([]byte(nil), usageEvent...), ' ')), model.ErrOPSWalletHoldConflict)
	require.ErrorIs(t, session.Settle(61), model.ErrOPSWalletHoldConflict)
	assert.False(t, session.NeedsRefund())
	session.Refund(nil)
	worker := &Reconciler{DB: db, Reader: reader}
	require.NoError(t, worker.Step(t.Context()))
	var held model.OPSWalletAdmission
	require.NoError(t, db.Where("user_id = ?", sessionUser.Id).First(&held).Error)
	assert.Equal(t, int64(75), held.PendingQuota, "a pending receipt cannot release capacity")
	sent, err := common.Marshal(map[string]any{"events": []any{map[string]any{"requestId": "session", "userId": fmt.Sprint(sessionUser.Id), "model": "model-test"}}})
	require.NoError(t, err)
	receipt, err := common.Marshal(map[string]any{"data": map[string]any{"charges": []any{map[string]any{"currency": "USD", "requestId": "session", "userId": fmt.Sprint(sessionUser.Id), "model": "model-test", "status": "charged", "amount": "0.00012", "refundAmount": "0", "netAmount": "0.00012", "chargedAt": "2026-11-20T01:00:00Z", "ledgerEntryId": 21}}}})
	require.NoError(t, err)
	reader.AcceptPushReceipts(sent, receipt, time.Now())
	// The debit receipt alone is insufficient; only a covering live position
	// lets a new worker release the durable hold after a process restart.
	worker = &Reconciler{DB: db, Reader: reader}
	require.ErrorIs(t, worker.Step(t.Context()), model.ErrOPSBalanceStale)
	balanceMu.Lock()
	balance, ledger = "0.00008", 21
	balanceMu.Unlock()
	worker = &Reconciler{DB: db, Reader: reader}
	require.NoError(t, worker.Step(t.Context()))
	require.NoError(t, db.Where("user_id = ?", sessionUser.Id).First(&held).Error)
	assert.Zero(t, held.PendingQuota)
	require.NoError(t, db.First(&sessionToken, sessionToken.Id).Error)
	assert.Equal(t, 940, sessionToken.RemainQuota)
	require.NoError(t, db.First(&sessionUser, sessionUser.Id).Error)
	assert.Equal(t, 54321, sessionUser.Quota)

	// A terminal video commits its actual token budget and immutable debit
	// together, including retries after the provider status already committed.
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskBillingReconciliation{}))
	request := "terminal-task"
	taskIdentity := model.OPSWalletHoldIdentity{RequestID: request, ChargeRequestID: opsauthority.TaskRequestID(request), UserID: sessionUser.Id, TokenID: sessionToken.Id, Model: "model-test"}
	_, err = model.ReserveOPSWallet(db, taskIdentity, 30, model.OPSWalletPosition{UserID: sessionUser.Id, EntryID: 21, Quota: 40})
	require.NoError(t, err)
	require.NoError(t, model.BeginOPSTaskDispatch(db, request, sessionUser.Id, "public-terminal", strings.Repeat("a", 64), 8, "video"))
	require.ErrorIs(t, model.BeginOPSTaskDispatch(db, request, sessionUser.Id, "public-terminal", strings.Repeat("a", 64), 8, "video"), model.ErrOPSWalletHoldConflict)
	require.ErrorIs(t, model.FinalizeOPSWallet(db, request, sessionUser.Id, 0, true), model.ErrOPSWalletHoldConflict, "a transport failure after dispatch cannot cancel capacity")
	require.NoError(t, model.ResolveOPSTaskDispatch(db, request, sessionUser.Id, "upstream-terminal", false))
	require.NoError(t, model.ResolveOPSTaskDispatch(db, request, sessionUser.Id, "upstream-terminal", false))
	require.ErrorIs(t, model.ResolveOPSTaskDispatch(db, request, sessionUser.Id, "", true), model.ErrOPSWalletHoldConflict)
	task := model.Task{UserId: sessionUser.Id, TaskID: "public-terminal", ChannelId: 8, Platform: "video", Status: model.TaskStatusSuccess, Quota: 30, PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-terminal", BillingSource: "ops", BillingScope: opsauthority.Scope(), TokenId: sessionToken.Id, SettledTokens: 42, Execution: &model.TaskExecutionSnapshot{RequestID: request}}}
	require.NoError(t, db.Create(&task).Error)
	event := map[string]any{"requestId": taskIdentity.ChargeRequestID, "userId": fmt.Sprint(task.UserId), "model": taskIdentity.Model, "currency": "USD", "amount": "0.00004", "status": "success", "extra": map[string]any{"billing_scope": opsauthority.Scope(), "project_original_request": request, "project_task_terminal": true, "recordKind": "usage", "direction": "debit"}}
	frozen, err := common.Marshal(event)
	require.NoError(t, err)
	task.PrivateData.OPSTerminal = &model.OPSTaskTerminal{Quota: 20, Event: frozen}
	require.NoError(t, db.Model(&task).Update("private_data", task.PrivateData).Error)
	for _, column := range []string{"task_id", "platform", "channel_id"} {
		var original any = task.TaskID
		var invalid any = "foreign"
		if column == "platform" {
			original = task.Platform
		}
		if column == "channel_id" {
			original, invalid = task.ChannelId, task.ChannelId+1
		}
		require.NoError(t, db.Model(&task).Update(column, invalid).Error)
		require.ErrorIs(t, model.FinalizeOPSTask(db, &task, request, 20, frozen), model.ErrOPSWalletHoldConflict)
		require.NoError(t, db.Model(&task).Update(column, original).Error)
	}
	for _, mutation := range []struct {
		key   string
		value any
	}{
		{"currency", "CNY"}, {"amount", "0.00006"}, {"status", "error"},
		{"extra", map[string]any{"billing_scope": "foreign"}},
	} {
		old := event[mutation.key]
		event[mutation.key] = mutation.value
		invalid, marshalErr := common.Marshal(event)
		require.NoError(t, marshalErr)
		require.ErrorIs(t, model.FinalizeOPSTask(db, &task, request, 20, invalid), model.ErrOPSWalletHoldConflict)
		event[mutation.key] = old
	}
	reconciliation := model.TaskBillingReconciliation{TaskID: task.ID, Status: model.TaskBillingReconciliationProcessing}
	require.NoError(t, db.Create(&reconciliation).Error)
	require.ErrorIs(t, model.FinalizeOPSTask(db, &task, request, 20, frozen), model.ErrOPSWalletHoldConflict)
	require.NoError(t, db.Model(&reconciliation).Update("status", model.TaskBillingReconciliationSettled).Error)
	rollbackTask := errors.New("synthetic terminal persistence failure")
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		if err := model.FinalizeOPSTask(tx, &task, request, 20, frozen); err != nil {
			return err
		}
		return rollbackTask
	}), rollbackTask)
	assert.Empty(t, task.PrivateData.OpsUsage, "rolled-back settlement must not leak an unpublished event to its caller")
	assert.Equal(t, 30, task.Quota)
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.Empty(t, task.PrivateData.OpsUsage)
	assert.Equal(t, 30, task.Quota)
	hold, err := model.GetOPSWalletHold(db, request, task.UserId)
	require.NoError(t, err)
	assert.Equal(t, "reserved", hold.State)
	require.NoError(t, db.First(&sessionToken, sessionToken.Id).Error)
	assert.Equal(t, 910, sessionToken.RemainQuota)
	require.NoError(t, model.FinalizeOPSTask(db, &task, request, 20, frozen))
	require.NoError(t, model.FinalizeOPSTask(db.Session(&gorm.Session{NewDB: true}), &task, request, 20, frozen))
	formatted, err := common.IndentJson(frozen)
	require.NoError(t, err)
	require.NoError(t, model.FinalizeOPSTask(db, &task, request, 20, formatted))
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.JSONEq(t, string(frozen), string(task.PrivateData.OpsUsage))
	assert.Equal(t, int64(42), task.PrivateData.SettledTokens)
	assert.True(t, task.OpsUsagePending)
	assert.Equal(t, 20, task.Quota)
	hold, err = model.GetOPSWalletHold(db, request, task.UserId)
	require.NoError(t, err)
	assert.Equal(t, task.ID, hold.TaskID)
	assert.Equal(t, int64(30), hold.HeldQuota, "only an OPS debit receipt may release successful capacity")
	require.NoError(t, db.First(&sessionToken, sessionToken.Id).Error)
	assert.Equal(t, 920, sessionToken.RemainQuota)
	require.NoError(t, db.First(&sessionUser, sessionUser.Id).Error)
	assert.Equal(t, 54321, sessionUser.Quota)

	// Provider-bill completion owns its reconciliation row as well. The
	// ordinary local reconciliation API must fail closed for this source.
	providerRequest := "provider-task"
	providerIdentity := taskIdentity
	providerIdentity.RequestID, providerIdentity.ChargeRequestID = providerRequest, opsauthority.TaskRequestID(providerRequest)
	_, err = model.ReserveOPSWallet(db, providerIdentity, 8, model.OPSWalletPosition{UserID: sessionUser.Id, EntryID: 21, Quota: 40})
	require.NoError(t, err)
	require.NoError(t, model.BeginOPSTaskDispatch(db, providerRequest, sessionUser.Id, "public-provider", strings.Repeat("b", 64), 8, "video"))
	require.NoError(t, model.ResolveOPSTaskDispatch(db, providerRequest, sessionUser.Id, "upstream-provider", false))
	providerTask := model.Task{UserId: sessionUser.Id, TaskID: "public-provider", ChannelId: 8, Platform: "video", Status: model.TaskStatusSuccess, Quota: 8, PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-provider", BillingSource: "ops", BillingScope: opsauthority.Scope(), TokenId: sessionToken.Id, Execution: &model.TaskExecutionSnapshot{RequestID: providerRequest}, BillingContext: &model.TaskBillingContext{ProviderBilling: &model.TaskProviderBillingSnapshot{Provider: model.TaskBillingProviderOpenAISeedance, AsyncReconciliationRequired: true}}}}
	require.NoError(t, db.Create(&providerTask).Error)
	providerRecord := model.TaskBillingReconciliation{TaskID: providerTask.ID, ChannelID: providerTask.ChannelId, UpstreamTaskID: providerTask.GetUpstreamTaskID(), Provider: model.TaskBillingProviderOpenAISeedance, Status: model.TaskBillingReconciliationProcessing, Attempts: 1}
	require.NoError(t, db.Create(&providerRecord).Error)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	bill := model.TaskBillingReconciliationSettlement{ActualQuota: 6, TotalTokens: 100, SupplierPrice: "0.000012", SupplierAmountPaid: "0.000012"}
	_, err = model.SettleTaskBillingReconciliation(providerRecord.ID, bill)
	require.ErrorIs(t, err, model.ErrOPSWalletHoldConflict)
	_, err = model.ApplyTaskQuotaAdjustment(&providerTask, 6)
	require.ErrorIs(t, err, model.ErrOPSWalletHoldConflict)
	event["requestId"], event["amount"] = providerIdentity.ChargeRequestID, "0.000012"
	event["extra"].(map[string]any)["project_original_request"] = providerRequest
	providerEvent, err := common.Marshal(event)
	require.NoError(t, err)
	providerTask.PrivateData.OPSTerminal = &model.OPSTaskTerminal{Quota: 6, Event: providerEvent}
	foreignRecord := providerRecord
	foreignRecord.ChannelID++
	require.ErrorIs(t, model.SettleOPSTaskProviderBill(db, &providerTask, &foreignRecord, bill), model.ErrOPSWalletHoldConflict)
	foreignRecord = providerRecord
	foreignRecord.UpstreamTaskID = "foreign-upstream"
	require.ErrorIs(t, model.SettleOPSTaskProviderBill(db, &providerTask, &foreignRecord, bill), model.ErrOPSWalletHoldConflict)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		if err := model.SettleOPSTaskProviderBill(tx, &providerTask, &providerRecord, bill); err != nil {
			return err
		}
		return rollbackTask
	}), rollbackTask)
	assert.Empty(t, providerTask.PrivateData.OpsUsage)
	assert.Equal(t, 8, providerTask.Quota)
	require.NoError(t, model.SettleOPSTaskProviderBill(db, &providerTask, &providerRecord, bill))
	require.NoError(t, model.SettleOPSTaskProviderBill(db, &providerTask, &providerRecord, bill))
	changedBill := bill
	changedBill.SupplierAmountPaid = "0.1"
	require.ErrorIs(t, model.SettleOPSTaskProviderBill(db, &providerTask, &providerRecord, changedBill), model.ErrOPSWalletHoldConflict)
	require.NoError(t, db.First(&providerRecord, providerRecord.ID).Error)
	require.NoError(t, db.First(&providerTask, providerTask.ID).Error)
	assert.Equal(t, model.TaskBillingReconciliationSettled, providerRecord.Status)
	assert.Equal(t, 6, providerTask.Quota)
	assert.True(t, providerTask.OpsUsagePending)
	require.NoError(t, db.First(&sessionToken, sessionToken.Id).Error)
	assert.Equal(t, 914, sessionToken.RemainQuota)
	require.NoError(t, db.First(&sessionUser, sessionUser.Id).Error)
	assert.Equal(t, 54321, sessionUser.Quota)

	// Definitive provider failure refunds the API-key budget and releases
	// admission capacity atomically; error facts have no OPS debit receipt.
	failureUser := model.User{Username: "failure-test", AffCode: "failure-only", Quota: 34567}
	require.NoError(t, db.Create(&failureUser).Error)
	failureToken := model.Token{UserId: failureUser.Id, Key: "test-only-failure-token", RemainQuota: 1000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&failureToken).Error)
	failureRequest := "failed-video"
	failureIdentity := model.OPSWalletHoldIdentity{RequestID: failureRequest, ChargeRequestID: opsauthority.TaskRequestID(failureRequest), UserID: failureUser.Id, TokenID: failureToken.Id, Model: "model-test"}
	_, err = model.ReserveOPSWallet(db, failureIdentity, 40, model.OPSWalletPosition{UserID: failureUser.Id, EntryID: 1, Quota: 80})
	require.NoError(t, err)
	require.NoError(t, model.BeginOPSTaskDispatch(db, failureRequest, failureUser.Id, "public-failure", strings.Repeat("c", 64), 8, "video"))
	require.NoError(t, model.ResolveOPSTaskDispatch(db, failureRequest, failureUser.Id, "upstream-failure", false))
	failureEvent, err := common.Marshal(map[string]any{"requestId": failureIdentity.ChargeRequestID, "userId": fmt.Sprint(failureUser.Id), "model": failureIdentity.Model, "currency": "USD", "amount": "0", "status": "error", "extra": map[string]any{"billing_scope": opsauthority.Scope(), "project_original_request": failureRequest, "project_task_terminal": true, "recordKind": "error", "direction": "debit"}})
	require.NoError(t, err)
	failureTask := model.Task{UserId: failureUser.Id, TaskID: "public-failure", ChannelId: 8, Platform: "video", Status: model.TaskStatusFailure, Quota: 40, PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-failure", BillingSource: "ops", BillingScope: opsauthority.Scope(), TokenId: failureToken.Id, Execution: &model.TaskExecutionSnapshot{RequestID: failureRequest}, OPSTerminal: &model.OPSTaskTerminal{Quota: 0, Event: failureEvent}}}
	require.NoError(t, db.Create(&failureTask).Error)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		if err := model.FinalizeOPSTask(tx, &failureTask, failureRequest, 0, failureEvent); err != nil {
			return err
		}
		return rollbackTask
	}), rollbackTask)
	require.NoError(t, db.First(&failureTask, failureTask.ID).Error)
	failureHold, err := model.GetOPSWalletHold(db, failureRequest, failureUser.Id)
	require.NoError(t, err)
	assert.Equal(t, "reserved", failureHold.State)
	assert.Equal(t, int64(40), failureHold.HeldQuota)
	require.NoError(t, db.First(&failureToken, failureToken.Id).Error)
	assert.Equal(t, 960, failureToken.RemainQuota)
	for range 2 {
		require.NoError(t, model.FinalizeOPSTask(db, &failureTask, failureRequest, 0, failureEvent))
	}
	failureHold, err = model.GetOPSWalletHold(db, failureRequest, failureUser.Id)
	require.NoError(t, err)
	assert.Equal(t, "reconciled", failureHold.State)
	assert.Zero(t, failureHold.HeldQuota)
	var failureAccount model.OPSWalletAdmission
	require.NoError(t, db.Where("user_id = ?", failureUser.Id).First(&failureAccount).Error)
	assert.Zero(t, failureAccount.PendingQuota)
	require.NoError(t, db.First(&failureToken, failureToken.Id).Error)
	assert.Equal(t, 1000, failureToken.RemainQuota)
	assert.Zero(t, failureToken.UsedQuota)
	require.NoError(t, db.First(&failureTask, failureTask.ID).Error)
	assert.True(t, failureTask.OpsUsagePending)
	assert.JSONEq(t, string(failureEvent), string(failureTask.PrivateData.OpsUsage))
	require.NoError(t, db.First(&failureUser, failureUser.Id).Error)
	assert.Equal(t, 34567, failureUser.Quota)

	// An ambiguous commit failure after a response cannot turn into a refund.
	failedInfo := &relaycommon.RelayInfo{RequestId: "session-commit-failure", UserId: sessionUser.Id, TokenId: sessionToken.Id, TokenKey: sessionToken.Key, OriginModelName: "model-test"}
	failed, err := NewSession(t.Context(), db, reader, failedInfo, 1)
	require.NoError(t, err)
	badEvent := []byte(strings.ReplaceAll(string(usageEvent), `"session"`, `"session-commit-failure"`))
	require.Error(t, failed.SettleUsage(61, badEvent))
	assert.False(t, failed.NeedsRefund())
	failed.Refund(nil)
	unresolved, err := model.GetOPSWalletHold(db, failedInfo.RequestId, sessionUser.Id)
	require.NoError(t, err)
	assert.Equal(t, "reserved", unresolved.State)
	assert.Equal(t, int64(1), unresolved.HeldQuota)

	// A crash between persisting terminal evidence and token settlement is
	// recovered from the same bytes, without releasing unresolved capacity.
	recoveryIdentity := failureIdentity
	recoveryIdentity.RequestID, recoveryIdentity.ChargeRequestID = "relay-recovery", ""
	_, err = model.ReserveOPSWallet(db, recoveryIdentity, 10, model.OPSWalletPosition{UserID: failureUser.Id, EntryID: 1, Quota: 80})
	require.NoError(t, err)
	recoveryEvent, err := common.Marshal(map[string]any{"requestId": recoveryIdentity.RequestID, "userId": fmt.Sprint(failureUser.Id), "model": recoveryIdentity.Model, "currency": "USD", "amount": "0.000014", "status": "success", "extra": map[string]any{"billing_scope": opsauthority.Scope(), "project_relay_terminal": true, "recordKind": "usage", "direction": "debit"}})
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, model.StageOPSRelaySettlement(db, recoveryIdentity.RequestID, failureUser.Id, 7, recoveryEvent))
	}
	require.ErrorIs(t, model.FinalizeOPSWallet(db, recoveryIdentity.RequestID, failureUser.Id, 0, true), model.ErrOPSWalletHoldConflict)
	require.ErrorIs(t, model.StageOPSRelaySettlement(db, recoveryIdentity.RequestID, failureUser.Id, 8, recoveryEvent), model.ErrOPSWalletHoldConflict)
	worker = &Reconciler{DB: db.Session(&gorm.Session{NewDB: true}), Reader: reader}
	require.NoError(t, worker.Step(t.Context()))
	recovered, err := model.GetOPSWalletHold(db, recoveryIdentity.RequestID, failureUser.Id)
	require.NoError(t, err)
	assert.Equal(t, "settled", recovered.State)
	assert.Equal(t, int64(10), recovered.HeldQuota)
	assert.Equal(t, int64(7), recovered.ActualQuota)
	var recoveredUsage model.OPSWalletUsage
	require.NoError(t, db.Where("request_id = ?", recoveryIdentity.RequestID).First(&recoveredUsage).Error)
	assert.Equal(t, string(recoveryEvent), recoveredUsage.Payload)
	require.NoError(t, db.First(&failureToken, failureToken.Id).Error)
	assert.Equal(t, 993, failureToken.RemainQuota)
	worker = &Reconciler{DB: db, Reader: reader}
	require.NoError(t, worker.Step(t.Context()))
	require.NoError(t, db.First(&failureToken, failureToken.Id).Error)
	assert.Equal(t, 993, failureToken.RemainQuota)
}

func TestUSDTransactionalFundingRetainsOrderAndDelivery(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_BILLING_CURRENCY", "USD")
	t.Setenv("OPS_PUSH_CURRENCY", "USD")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-test")
	var driver gorm.Dialector = sqlite.Open("file:ops-wallet-funding?mode=memory&cache=shared")
	engine := os.Getenv("OPS_NATIVE_TEST_ENGINE")
	switch engine {
	case "mysql":
		driver = mysql.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "postgres":
		driver = postgres.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "", "sqlite":
	default:
		t.Fatal("unsupported funding test database")
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousBatch, previousUnit := common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseType(db.Dialector.Name()), common.DatabaseType(db.Dialector.Name()))
	common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = false, false, 500_000
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = previousRedis, previousBatch, previousUnit
	})
	// The released user/order schema already exists before adding the outbox.
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Redemption{}, &model.Log{}, &model.SubscriptionPlan{}, &model.SubscriptionOrder{}, &model.UserSubscription{}))
	user := model.User{Username: "funding-test", Quota: 2_500_000}
	require.NoError(t, db.Create(&user).Error)
	plan := model.SubscriptionPlan{Title: "local-only", Enabled: true, PriceAmount: 1, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1}
	require.NoError(t, db.Create(&plan).Error)
	require.ErrorIs(t, model.PurchaseSubscriptionWithBalance(user.Id, plan.Id), model.ErrOPSSubscriptionsUnsupported)
	_, err = model.AdminBindSubscription(user.Id, plan.Id, "test")
	require.ErrorIs(t, err, model.ErrOPSSubscriptionsUnsupported)
	order := model.TopUp{UserId: user.Id, TradeNo: "stripe-funding-test", Money: 10, Status: common.TopUpStatusPending}
	require.NoError(t, db.Create(&order).Error)
	require.NoError(t, db.AutoMigrate(&model.OPSWalletEvent{}))
	require.NoError(t, db.AutoMigrate(&model.OPSWalletEvent{}))
	rollback := errors.New("roll back business transaction")
	complete := func(tx *gorm.DB) error {
		if err := tx.Model(&order).Update("status", common.TopUpStatusSuccess).Error; err != nil {
			return err
		}
		return model.QueueOPSUSDFundingTx(tx, int64(user.Id), 5_000_000, "topup", "stripe", order.TradeNo, &model.OPSUSDFundingFacts{TradeNo: order.TradeNo, PaymentMethod: "stripe", PaidAmount: "10", PaidCurrency: "USD"})
	}
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		if err := complete(tx); err != nil {
			return err
		}
		return rollback
	}), rollback)
	rows, err := model.ReadOPSUSDFunding(db, 20)
	require.NoError(t, err)
	assert.Empty(t, rows)
	require.NoError(t, db.First(&order, order.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	require.NoError(t, db.Transaction(complete))
	require.NoError(t, db.Transaction(complete), "business replay retains one credit")
	rows, err = model.ReadOPSUSDFunding(db, 20)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	stored := rows[0]
	var envelope struct {
		Events []map[string]any `json:"events"`
	}
	require.NoError(t, common.UnmarshalJsonStr(stored.Payload, &envelope))
	require.Len(t, envelope.Events, 1)
	assert.Equal(t, "10", envelope.Events[0]["amount"])
	assert.Equal(t, "USD", envelope.Events[0]["currency"])
	assert.NotContains(t, envelope.Events[0], "balanceAfter")
	require.Error(t, db.Transaction(func(tx *gorm.DB) error {
		return model.QueueOPSUSDFundingTx(tx, int64(user.Id), 5_500_000, "topup", "stripe", order.TradeNo, nil)
	}), "reusing an order for another amount must fail")
	for _, quota := range []int64{-1, 0, common.MaxWalletQuota + 1} {
		require.Error(t, db.Transaction(func(tx *gorm.DB) error {
			return model.QueueOPSUSDFundingTx(tx, int64(user.Id), quota, "gift", "redemption", "invalid", nil)
		}))
	}
	responses := []string{"unavailable", `{"data":{"accepted":0,"duplicates":0,"rejected":[]}}`, `{"data":{"accepted":0,"duplicates":1,"rejected":[]}}`}
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, fundingPath, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		bodies = append(bodies, string(body))
		expected := opsclient.SignWithNonce("usd-funding-test", "test-only-funding-secret", "POST", fundingPath, body, r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"))
		assert.Equal(t, expected.Get("X-Signature"), r.Header.Get("X-Signature"))
		response := responses[0]
		responses = responses[1:]
		if response == "unavailable" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	client := opsclient.New(server.URL, "usd-funding-test", "test-only-funding-secret", time.Second)
	for range 2 {
		require.Error(t, DeliverFunding(t.Context(), db, client))
		rows, err := model.ReadOPSUSDFunding(db, 20)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, stored.Payload, rows[0].Payload)
	}
	require.NoError(t, DeliverFunding(t.Context(), db, client))
	require.Len(t, bodies, 3)
	assert.Equal(t, []string{stored.Payload, stored.Payload, stored.Payload}, bodies)
	rows, err = model.ReadOPSUSDFunding(db, 20)
	require.NoError(t, err)
	assert.Empty(t, rows)
	require.NoError(t, model.AcknowledgeOPSUSDFunding(db, stored, time.Now().Unix()), "lost local acknowledgement can be replayed")
	stored.Payload += " "
	require.Error(t, model.ValidateOPSUSDFunding(stored))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 2_500_000, user.Quota, "OPS funding must not also credit the local wallet")
	// Exercise the real payment and redemption transactions, not only outbox insertion.
	stripeOrder := model.TopUp{UserId: user.Id, TradeNo: "native-usd-stripe", Money: 10, Amount: 10, PaymentMethod: "stripe", PaymentProvider: "stripe", Status: common.TopUpStatusPending}
	require.NoError(t, db.Create(&stripeOrder).Error)
	payment := &model.TopupPaymentSnapshot{ActualMoney: "10", CurrencyCode: "USD", PaymentReference: "synthetic-payment"}
	require.NoError(t, model.Recharge(stripeOrder.TradeNo, "synthetic-customer", "127.0.0.1", payment))
	require.NoError(t, model.Recharge(stripeOrder.TradeNo, "synthetic-customer", "127.0.0.1", payment))
	changed := *payment
	changed.ActualMoney = "11"
	require.Error(t, model.Recharge(stripeOrder.TradeNo, "synthetic-customer", "127.0.0.1", &changed))
	require.NoError(t, db.First(&stripeOrder, stripeOrder.Id).Error)
	assert.Equal(t, common.TopUpStatusSuccess, stripeOrder.Status)
	rows, err = model.ReadOPSUSDFunding(db, 20)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a repeated Stripe callback cannot enqueue a second credit")
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Payload, &envelope))
	assert.Equal(t, "10", envelope.Events[0]["amount"])

	code := model.Redemption{Key: "test-only-unusable-secret-code", Status: common.RedemptionCodeStatusEnabled, Quota: 1_000_000}
	require.NoError(t, db.Create(&code).Error)
	quota, err := model.Redeem(code.Key, user.Id)
	require.NoError(t, err)
	assert.Equal(t, 1_000_000, quota)
	_, err = model.Redeem(code.Key, user.Id)
	require.Error(t, err)
	rows, err = model.ReadOPSUSDFunding(db, 20)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.NotContains(t, rows[1].Payload, code.Key, "redemption secrets never leave the source station")
	require.NoError(t, common.UnmarshalJsonStr(rows[1].Payload, &envelope))
	assert.Equal(t, "2", envelope.Events[0]["amount"])
	assert.Equal(t, "gift", envelope.Events[0]["extra"].(map[string]any)["recordKind"])

	expiring := model.Redemption{Key: "test-only-expiring-code", Status: common.RedemptionCodeStatusEnabled, Quota: 500_000, ExpiredTime: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, db.Create(&expiring).Error)
	_, err = model.Redeem(expiring.Key, user.Id)
	require.Error(t, err, "unsupported expiry cannot silently become permanent money")
	require.NoError(t, db.First(&expiring, expiring.Id).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, expiring.Status, "unsupported funding rolls back code consumption")
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 2_500_000, user.Quota)
	assert.Equal(t, "synthetic-customer", user.StripeCustomer)
}

func TestSignedAdjustmentRetainsOperationAndValidatesReceipt(t *testing.T) {
	a := Adjustment{RequestID: "adm-test", UserID: "15", Operation: "add", Amount: "0.000001", Operator: "admin", Reason: "test grant"}
	var first []byte
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/test"+Path, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		expected := opsclient.SignWithNonce("project", "test-adjustment-secret", "POST", Path, body, r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"))
		assert.Equal(t, expected.Get("X-Signature"), r.Header.Get("X-Signature"))
		if count == 0 {
			first = body
		} else {
			assert.Equal(t, first, body)
		}
		count++
		if count == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"requestId":"adm-test","userId":"15","operation":"add","amount":"0.000001","change":"0.000001","balanceBefore":"1","balance":"1.000001","balanceQuota":68493,"ledgerEntryId":7,"recordedAt":"2026-11-12T01:00:00Z","station":{"quotaPerUnit":500000,"usdExchangeRate":"7.3"}}}`))
	}))
	defer server.Close()
	c := opsclient.New(server.URL+"/test", "project", "test-adjustment-secret", time.Second)
	_, err := Adjust(context.Background(), c, a)
	require.Error(t, err)
	assert.False(t, CanEditAfterError(err))
	r, err := Adjust(context.Background(), c, a)
	require.NoError(t, err)
	assert.Equal(t, "1.000001", r.Balance)
	assert.Equal(t, int64(7), r.LedgerEntryID)
	var sent map[string]any
	require.NoError(t, common.Unmarshal(first, &sent))
	assert.NotContains(t, sent, "tenantId")
	assert.NotContains(t, sent, "ledgerTenant")
	a.Amount = "1e12"
	require.Error(t, Validate(a))
	a.Amount = "-1"
	require.Error(t, Validate(a))
}

func TestAdjustmentRejectsDifferentReceiptAndDefiniteFailures(t *testing.T) {
	a := Adjustment{RequestID: "adm-test", UserID: "15", Operation: "set", Amount: "0", Operator: "admin", Reason: "test reset"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"requestId":"foreign","userId":"15","balance":"0"}}`))
	}))
	defer server.Close()
	_, err := Adjust(context.Background(), opsclient.New(server.URL, "p", "test-adjustment-secret", time.Second), a)
	require.Error(t, err)
	assert.False(t, CanEditAfterError(err))
	assert.True(t, CanEditAfterError(&opsclient.ReadError{Status: 409, Code: "insufficient_balance"}))
	assert.False(t, CanEditAfterError(&opsclient.ReadError{Status: 429}))
	assert.False(t, CanEditAfterError(&opsclient.ReadError{Status: 409, Code: "request_id_conflict"}))
	assert.Equal(t, "运营平台余额不足，无法扣减该金额", ErrorMessage(&opsclient.ReadError{Status: 409, Code: "insufficient_balance"}))
}

func TestUSDAdjustmentValidatesNativeUnitsAndReplay(t *testing.T) {
	t.Setenv("OPS_BILLING_CURRENCY", "USD")
	a := Adjustment{RequestID: "usd-adjust", UserID: "15", Operation: "subtract", Amount: "0.25", Operator: "admin", Reason: "local contract test"}
	const receipt = `{"data":{"currency":"USD","requestId":"usd-adjust","userId":"15","operation":"subtract","amount":"0.25","change":"-0.25","balanceBefore":"10","balance":"9.75","balanceQuota":9750000,"ledgerEntryId":7,"recordedAt":"2026-11-19T01:00:00Z","station":{"quotaPerUnit":1000000,"usdExchangeRate":"1"},"replayed":true}}`
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"native USD", receipt, false},
		{"gateway units cannot replace wire units", strings.ReplaceAll(receipt, "9750000", "4875000"), true},
		{"CNY conversion rejected", strings.ReplaceAll(receipt, `"usdExchangeRate":"1"`, `"usdExchangeRate":"7.3"`), true},
		{"missing currency", strings.ReplaceAll(receipt, `"currency":"USD",`, ``), true},
		{"foreign currency", strings.ReplaceAll(receipt, `"currency":"USD"`, `"currency":"CNY"`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				expected := opsclient.SignWithNonce("usd-station", "test-usd-adjustment-secret", "POST", Path, body, req.Header.Get("X-Timestamp"), req.Header.Get("X-Nonce"))
				assert.Equal(t, expected.Get("X-Signature"), req.Header.Get("X-Signature"))
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			r, err := Adjust(context.Background(), opsclient.New(server.URL, "usd-station", "test-usd-adjustment-secret", time.Second), a)
			if tc.wantError {
				require.Error(t, err)
				assert.False(t, CanEditAfterError(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "USD", r.Currency)
			assert.Equal(t, "9.75", r.Balance)
			assert.True(t, r.Replayed)
		})
	}
}

func TestUSDCutoverRequiresApprovedPerUserOpeningAndNeverChangesCustody(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-cutover-test")
	previousUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousUnit })
	var driver gorm.Dialector = sqlite.Open("file:ops-cutover?mode=memory&cache=shared")
	switch engine := os.Getenv("OPS_NATIVE_TEST_ENGINE"); engine {
	case "mysql":
		driver = mysql.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "postgres":
		driver = postgres.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "", "sqlite":
	default:
		t.Fatal("unsupported cutover test database")
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseType(db.Dialector.Name()), common.DatabaseType(db.Dialector.Name()))
	t.Cleanup(func() { common.SetDatabaseTypes(previousMain, previousLog) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSubscription{}, &model.SubscriptionOrder{}, &model.AgencyFundingLot{}, &model.CNRefundReservation{}, &model.Task{}, &model.TaskBillingReconciliation{}, &model.Midjourney{}))
	user := model.User{Username: "opening-user", Quota: 5_000_000}
	require.NoError(t, db.Create(&user).Error)
	zero := model.User{Username: "new-after-opening", Quota: 0, AffCode: "new"}
	require.NoError(t, db.Create(&zero).Error)
	cutoff := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	since := cutoff.Format(time.RFC3339Nano)
	zeroCount := 0
	remoteCurrency, remoteOpening, remoteSince := "USD", "10", since
	remoteSource, remoteID, remoteUnit := "ops", fmt.Sprint(user.Id), int64(1_000_000)
	remoteEntry := 1
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			posts++
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		assert.Equal(t, `/api/v1/catalog/v1/wallets`, req.URL.Path)
		body, err := common.Marshal(map[string]any{"data": map[string]any{
			"currency": remoteCurrency, "opsKeepsBalanceSince": remoteSince,
			"station": map[string]any{"quotaPerUnit": remoteUnit, "usdExchangeRate": "1"},
			"items": []any{map[string]any{"userId": remoteID, "balanceSource": remoteSource, "openingBalance": remoteOpening, "openingEntryId": remoteEntry},
				map[string]any{"userId": fmt.Sprint(zero.Id), "balanceSource": "ops"}},
		}})
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	client := opsclient.New(server.URL, "opening-read", "test-only-opening-secret", time.Second)
	proof := CutoverApproval{SchemaVersion: 1, Scope: opsauthority.Scope(), URL: server.URL, KeyID: client.KeyID,
		Currency: "USD", GatewayQuotaPerUnit: 500_000, ApprovedBy: "local-test-operator", ApprovedAt: cutoff.Add(time.Minute).Format(time.RFC3339Nano),
		FrozenAt: cutoff.Add(-time.Minute).Format(time.RFC3339Nano), CustodySince: since, Differences: &zeroCount, MissingBalances: &zeroCount,
		Total: "10", Users: []OpeningBalance{{UserID: user.Id, Quota: 5_000_000}}}
	file := filepath.Join(t.TempDir(), "opening.json")
	t.Setenv("OPS_WALLET_OPENING_FILE", file)
	writeProof := func(p CutoverApproval) {
		raw, err := common.Marshal(p)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, raw, 0600))
		t.Setenv("OPS_WALLET_OPENING_SHA256", fmt.Sprintf("%x", sha256.Sum256(raw)))
	}
	writeProof(proof)
	require.ErrorContains(t, ValidateCutover(t.Context(), db, client), "guard is not active")
	require.NoError(t, model.FreezeOPSSourceWallet(db))
	require.NoError(t, ValidateCutover(t.Context(), db, client))
	for _, pending := range []struct {
		name string
		row  any
	}{
		{"active subscription", &model.UserSubscription{UserId: user.Id, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}},
		{"expired subscription awaiting group restoration", &model.UserSubscription{UserId: user.Id, Status: "active", EndTime: 1}},
		{"pending subscription checkout", &model.SubscriptionOrder{UserId: user.Id, TradeNo: "pending-cutover", Status: common.TopUpStatusPending}},
		{"expiring gift", &model.AgencyFundingLot{UserID: int64(user.Id), ExpiresAt: time.Now().Add(time.Hour).Unix(), BonusAvailable: 100}},
		{"unsettled funding reservation", &model.AgencyFundingLot{UserID: int64(user.Id), PaidReserved: 100}},
		{"unresolved refund", &model.CNRefundReservation{Key: "pending-refund", UserID: int64(user.Id), Status: "unknown_reconcile"}},
		{"inflight legacy task", &model.Task{TaskID: "legacy", UserId: user.Id, Status: model.TaskStatusInProgress}},
		{"provider bill awaiting reconciliation", &model.TaskBillingReconciliation{TaskID: 999, Status: model.TaskBillingReconciliationPending}},
		{"inflight midjourney", &model.Midjourney{UserId: user.Id, Status: "IN_PROGRESS", Progress: "50%"}},
	} {
		t.Run(pending.name, func(t *testing.T) {
			require.NoError(t, db.Create(pending.row).Error)
			require.ErrorContains(t, ValidateCutover(t.Context(), db, client), "source wallet is not ready")
			require.NoError(t, db.Delete(pending.row).Error)
		})
	}
	// Restarting an already active OPS deployment must allow its own inflight
	// tasks. Only the old local settlement paths need draining.
	opsTask := model.Task{TaskID: "ops-inflight", UserId: user.Id, Status: model.TaskStatusInProgress, PrivateData: model.TaskPrivateData{BillingSource: "ops", BillingScope: opsauthority.Scope()}}
	require.NoError(t, db.Create(&opsTask).Error)
	opsBill := model.TaskBillingReconciliation{TaskID: opsTask.ID, Status: model.TaskBillingReconciliationPending}
	require.NoError(t, db.Create(&opsBill).Error)
	require.NoError(t, ValidateCutover(t.Context(), db, client))
	require.NoError(t, db.Migrator().DropTable(&model.CNRefundReservation{}, &model.AgencyFundingLot{}))
	require.NoError(t, ValidateCutover(t.Context(), db, client), "optional schemas need not exist on a site that has never enabled them")
	for _, tc := range []struct {
		name   string
		change func(*CutoverApproval)
	}{
		{"wrong station", func(p *CutoverApproval) { p.Scope = "domestic" }},
		{"wrong currency", func(p *CutoverApproval) { p.Currency = "CNY" }},
		{"wrong total", func(p *CutoverApproval) { p.Total = "11" }},
		{"unapproved", func(p *CutoverApproval) { p.ApprovedBy = "" }},
		{"missing reconciliation", func(p *CutoverApproval) { p.Differences = nil }},
		{"duplicate identity", func(p *CutoverApproval) { p.Users = append(p.Users, p.Users[0]) }},
		{"wrong cutoff", func(p *CutoverApproval) { p.FrozenAt = cutoff.Add(2 * time.Minute).Format(time.RFC3339Nano) }},
		{"future approval", func(p *CutoverApproval) { p.ApprovedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := proof
			p.Users = append([]OpeningBalance(nil), proof.Users...)
			tc.change(&p)
			writeProof(p)
			require.Error(t, ValidateCutover(t.Context(), db, client))
		})
	}
	writeProof(proof)
	t.Setenv("OPS_WALLET_OPENING_SHA256", "unapproved-digest")
	require.Error(t, ValidateCutover(t.Context(), db, client))
	writeProof(proof)
	for _, tc := range []struct {
		name, opening, currency, source, id, since string
		unit                                       int64
		entry                                      int
	}{
		{"different user amount", "9", "USD", "ops", fmt.Sprint(user.Id), since, 1_000_000, 1},
		{"wrong currency", "10", "CNY", "ops", fmt.Sprint(user.Id), since, 1_000_000, 1},
		{"station still owns wallet", "10", "USD", "station", fmt.Sprint(user.Id), since, 1_000_000, 1},
		{"missing user", "10", "USD", "ops", "999", since, 1_000_000, 1},
		{"wrong custody period", "10", "USD", "ops", fmt.Sprint(user.Id), cutoff.Add(time.Second).Format(time.RFC3339Nano), 1_000_000, 1},
		{"wrong wire quota", "10", "USD", "ops", fmt.Sprint(user.Id), since, 500_000, 1},
		{"missing ledger evidence", "10", "USD", "ops", fmt.Sprint(user.Id), since, 1_000_000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remoteOpening, remoteCurrency, remoteSource, remoteID, remoteSince, remoteUnit, remoteEntry = tc.opening, tc.currency, tc.source, tc.id, tc.since, tc.unit, tc.entry
			require.Error(t, ValidateCutover(t.Context(), db, client))
		})
	}
	remoteOpening, remoteCurrency, remoteSource, remoteID, remoteSince, remoteUnit, remoteEntry = "10", "USD", "ops", fmt.Sprint(user.Id), since, 1_000_000, 1
	require.ErrorContains(t, db.Model(&user).Update("quota", user.Quota+1).Error, "ops_source_wallet_frozen")
	proof.Users[0].Quota++
	proof.Total = "10.000002"
	writeProof(proof)
	require.ErrorContains(t, ValidateCutover(t.Context(), db, client), "source wallet changed")
	proof.Users[0].Quota--
	proof.Total = "10"
	writeProof(proof)
	require.ErrorContains(t, db.Model(&zero).Update("quota", 1).Error, "ops_source_wallet_frozen")
	require.NoError(t, ValidateCutover(t.Context(), db, client))
	assert.Zero(t, posts, "startup verification must never POST wallet-custody")
}
