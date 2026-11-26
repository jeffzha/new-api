package modelhealth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/doubao"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/QuantumNous/new-api/service/opsfees"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCycleBudgetHierarchyKeepsOverallCapAndCarriesUnknownCharges(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	sel, _ := seedHealth(t, db, "video")
	target, err := findTarget(db, sel.Channel, sel.Model, sel.Endpoint)
	require.NoError(t, err)
	global := CycleBudgetInput{Scope: "global", Override: true, Amount: 200000000, Run: 5000000, IntervalSeconds: 604800}
	require.NoError(t, SaveCycleBudget(ctx, global))
	require.NoError(t, SaveCycleBudget(ctx, CycleBudgetInput{Scope: "video", Override: true, Amount: 60000000, Run: 4000000, IntervalSeconds: 604800}))
	require.NoError(t, SaveCycleBudget(ctx, CycleBudgetInput{Selection: sel, Scope: "model", Override: true, Amount: 6000000, Run: 3000000, IntervalSeconds: 604800}))
	status, err := GetCycleBudget(ctx, "model", sel)
	require.NoError(t, err)
	assert.Equal(t, "model", status.Source)
	require.NoError(t, db.Create(&model.ModelHealthRun{Target: target.ID, Endpoint: sel.Endpoint, State: "succeeded", ReservationMicro: 4000000, ActualMicro: common.GetPointer(int64(4000000))}).Error)
	limits, err := cycleBudgetBlocks(db, target, sel.Endpoint, 3000000, time.Now().Unix(), 0)
	require.NoError(t, err)
	assert.Contains(t, limits, "Detection cycle budget is insufficient")
	require.NoError(t, SaveCycleBudget(ctx, CycleBudgetInput{Selection: sel, Scope: "model"}))
	status, err = GetCycleBudget(ctx, "model", sel)
	require.NoError(t, err)
	assert.Equal(t, "video", status.Source)
	require.NoError(t, SaveCycleBudget(ctx, CycleBudgetInput{Scope: "video"}))
	status, err = GetCycleBudget(ctx, "model", sel)
	require.NoError(t, err)
	assert.Equal(t, "global", status.Source)
	assert.Equal(t, int64(4000000), status.Used)
	anchor := status.Start
	global.Amount = 100000000
	require.NoError(t, SaveCycleBudget(ctx, global))
	status, err = GetCycleBudget(ctx, "global", Selection{})
	require.NoError(t, err)
	assert.Equal(t, anchor, status.Start, "editing the amount must not reset the cycle")
	assert.Equal(t, int64(4000000), status.Used)
	require.NoError(t, db.Create(&model.ModelHealthRun{State: "uncertain", Endpoint: sel.Endpoint, ReservationMicro: 99000000, CreatedAt: anchor - 86400}).Error)
	limits, err = cycleBudgetBlocks(db, target, sel.Endpoint, 1000000, anchor+604800, 0)
	require.NoError(t, err)
	assert.Empty(t, limits, "settled prior-cycle charges expire but unknown reservations carry over")
	limits, err = cycleBudgetBlocks(db, target, sel.Endpoint, 2000000, anchor+604800, 0)
	require.NoError(t, err)
	assert.Contains(t, limits, "Detection cycle budget is insufficient")
}

func TestCycleBudgetRejectsInvalidValuesAndCannotResetByChangingPeriod(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	input := CycleBudgetInput{Scope: "global", Override: true, Amount: 200000000, Run: 5000000, IntervalSeconds: 604800}
	require.NoError(t, SaveCycleBudget(ctx, input))
	for _, amount := range []int64{-1, 0, 1000000000001} {
		invalidInput := input
		invalidInput.Amount = amount
		require.Error(t, SaveCycleBudget(ctx, invalidInput))
	}
	require.NoError(t, db.Create(&model.ModelHealthRun{State: "succeeded", ReservationMicro: 100000000, ActualMicro: common.GetPointer(int64(100000000))}).Error)
	input.IntervalSeconds = 86400
	require.NoError(t, SaveCycleBudget(ctx, input))
	status, err := GetCycleBudget(ctx, "global", Selection{})
	require.NoError(t, err)
	assert.Equal(t, int64(100000000), status.Used)
	assert.Equal(t, 604800, status.IntervalSeconds, "the active period remains fixed until its next boundary")
	assert.Equal(t, 86400, status.NextIntervalSeconds)
}

func TestCycleBudgetAdmissionAndClaimShareTheCapWithoutDoubleReserving(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	sel, plan := seedHealth(t, db, "llm")
	rule := recommendedRule("llm")
	rule.PerDay = 12
	require.NoError(t, SaveRule(ctx, "llm", Selection{}, rule))
	global := CycleBudgetInput{Scope: "global", Override: true, Amount: 150, Run: 100, IntervalSeconds: 604800}
	require.NoError(t, SaveCycleBudget(ctx, global))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := Enqueue(ctx, sel, plan.Revision, plan.Template.MaxMicro); results <- err })
	}
	wg.Wait()
	close(results)
	var admitted int
	for err := range results {
		if err == nil {
			admitted++
		}
	}
	assert.Equal(t, 1, admitted)
	run, err := claim(ctx, time.Now().Unix())
	require.NoError(t, err)
	require.NotZero(t, run.ID, "claim must exclude its own reservation when checking the cap")
	require.Equal(t, "submitting", run.State)
	require.NoError(t, db.Model(&run).Updates(map[string]any{"state": "succeeded", "actual_micro": 60}).Error)
	_, err = Enqueue(ctx, sel, plan.Revision, plan.Template.MaxMicro)
	require.ErrorContains(t, err, "Detection cycle budget is insufficient", "legacy day/month limits must not admit a cycle overspend")
	global.Amount = 200
	require.NoError(t, SaveCycleBudget(ctx, global))
	queued, err := Enqueue(ctx, sel, plan.Revision, plan.Template.MaxMicro)
	require.NoError(t, err)
	global.Amount = 150
	require.NoError(t, SaveCycleBudget(ctx, global))
	run, err = claim(ctx, time.Now().Unix())
	require.NoError(t, err)
	require.Zero(t, run.ID, "reducing the cap must prevent an unsubmitted queued run from calling upstream")
	require.NoError(t, db.First(&queued, queued.ID).Error)
	assert.Equal(t, "skipped", queued.State)
	require.NotNil(t, queued.ActualMicro)
	assert.Zero(t, *queued.ActualMicro)
}

func TestCycleBudgetSharesOverallCapAcrossDifferentModelsAndTypes(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	text, textPlan := seedHealth(t, db, "llm")
	video, videoPlan := seedHealth(t, db, "video")
	require.NoError(t, SaveCycleBudget(ctx, CycleBudgetInput{Scope: "global", Override: true, Amount: 150, Run: 100, IntervalSeconds: 604800}))
	for _, selection := range []Selection{text, video} {
		require.NoError(t, SaveCycleBudget(ctx, CycleBudgetInput{Selection: selection, Scope: "model", Override: true, Amount: 1000, Run: 100, IntervalSeconds: 604800}))
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, probe := range []struct {
		selection Selection
		plan      Plan
	}{{text, textPlan}, {video, videoPlan}} {
		wg.Go(func() {
			_, err := Enqueue(ctx, probe.selection, probe.plan.Revision, probe.plan.Template.MaxMicro)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var admitted int
	for err := range results {
		if err == nil {
			admitted++
		} else {
			require.ErrorContains(t, err, "Detection cycle budget is insufficient")
		}
	}
	assert.Equal(t, 1, admitted, "different model overrides must still share one overall cap")
	status, err := GetCycleBudget(ctx, "global", Selection{})
	require.NoError(t, err)
	assert.Equal(t, int64(100), status.Used)
	assert.Equal(t, int64(50), status.GlobalRemaining)
}

func TestProbeBudgetReconcilesOnlySettledDedicatedPlatformCharges(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	previous := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = previous })
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.Token{}))
	t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Log{}, &model.Token{})) })
	token := model.Token{UserId: 12, Name: "model-health/receipt-fixture"}
	require.NoError(t, db.Create(&token).Error)
	runs := []model.ModelHealthRun{
		{ChannelID: 9, ModelName: "fixture", State: "succeeded", RequestID: "paid", ReservationMicro: 10000},
		{ChannelID: 9, ModelName: "fixture", State: "failed", RequestID: "unknown", ReservationMicro: 10000},
		{ChannelID: 9, ModelName: "fixture", State: "succeeded", RequestID: "pending", ReservationMicro: 10000},
		{ChannelID: 9, ModelName: "fixture", State: "succeeded", RequestID: "duplicate", ReservationMicro: 10000},
	}
	require.NoError(t, db.Create(&runs).Error)
	require.NoError(t, db.Model(&model.ModelHealthRun{}).Where("id IN ?", []int64{runs[0].ID, runs[1].ID, runs[2].ID, runs[3].ID}).Update("created_at", time.Now().Unix()-86400).Error)
	for _, entry := range []struct{ request, status, amount string }{{"paid", "settled", "0.0007738"}, {"pending", "pending", "0"}, {"duplicate", "settled", "0.01"}, {"duplicate", "settled", "0.02"}} {
		other, err := common.Marshal(map[string]any{"billing": map[string]any{"status": entry.status, "amount": entry.amount, "currency": "CNY", "source": "gateway", "quota": 53}})
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.Log{UserId: 12, TokenId: token.Id, ChannelId: 9, ModelName: "fixture", RequestId: entry.request, Type: model.LogTypeConsume, Quota: 53, Other: string(other)}).Error)
	}
	require.NoError(t, syncProbeBudgetReceipts(context.Background()))
	require.NoError(t, syncProbeBudgetReceipts(context.Background()))
	require.NoError(t, db.Order("id").Find(&runs).Error)
	require.NotNil(t, runs[0].ActualMicro)
	assert.Equal(t, int64(774), *runs[0].ActualMicro)
	for _, run := range runs[1:] {
		assert.Nil(t, run.ActualMicro)
	}
	used, err := spend(db, time.Now().Unix())
	require.NoError(t, err)
	assert.Equal(t, int64(30000), used, "only unresolved reservations carry across days")
	var receipts int64
	require.NoError(t, db.Model(&model.ModelHealthConfig{}).Where("kind = ?", "budget-receipt").Count(&receipts).Error)
	assert.Equal(t, int64(1), receipts, "repeated readers must reconcile exactly once")
}

func TestVideoBudgetWaitsForCommittedFinalSettlement(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.Task{}, &model.TaskBillingReconciliation{}))
	t.Cleanup(func() {
		require.NoError(t, db.Migrator().DropTable(&model.TaskBillingReconciliation{}, &model.Task{}, &model.Token{}))
	})
	token := model.Token{UserId: 12, Name: "model-health/video-receipt"}
	require.NoError(t, db.Create(&token).Error)
	run := model.ModelHealthRun{ChannelID: 9, ModelName: "fixture-video", State: "succeeded", RequestID: "video-request", UpstreamTaskID: "task-fixture", ReservationMicro: 5000000}
	template, err := common.Marshal(Template{QuotaPerUnit: "500000"})
	require.NoError(t, err)
	run.Template = string(template)
	require.NoError(t, db.Create(&run).Error)
	task := model.Task{TaskID: "task-fixture", ChannelId: 9, UserId: 12, Status: model.TaskStatusSuccess, Quota: 40000, PrivateData: model.TaskPrivateData{TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: run.RequestID}, BillingContext: &model.TaskBillingContext{OriginModelName: run.ModelName, ProviderBilling: &model.TaskProviderBillingSnapshot{Provider: model.TaskBillingProviderOpenAISeedance, Currency: "CNY", CNYPerUSD: "7.3"}}}}
	require.NoError(t, db.Create(&task).Error)
	receipt := model.TaskBillingReconciliation{TaskID: task.ID, ChannelID: 9, Provider: model.TaskBillingProviderOpenAISeedance, Status: model.TaskBillingReconciliationPending, ActualQuota: task.Quota}
	require.NoError(t, db.Create(&receipt).Error)
	require.NoError(t, syncProbeTaskBudgetReceipt(ctx, run))
	require.NoError(t, db.First(&run, run.ID).Error)
	assert.Nil(t, run.ActualMicro, "generation success and pre-consumption do not release the hold")
	require.NoError(t, db.Model(&receipt).Update("status", model.TaskBillingReconciliationSettled).Error)
	require.NoError(t, syncProbeTaskBudgetReceipt(ctx, run))
	require.NoError(t, syncProbeTaskBudgetReceipt(ctx, run))
	require.NoError(t, db.First(&run, run.ID).Error)
	require.NotNil(t, run.ActualMicro)
	assert.Equal(t, int64(584000), *run.ActualMicro)
	var count int64
	require.NoError(t, db.Model(&model.ModelHealthConfig{}).Where("kind = ?", "budget-receipt").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestLegacyDoubaoProbeHoldRequiresMatchingFinalAdjustment(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	previous := model.LOG_DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.Log{}))
	t.Cleanup(func() {
		model.LOG_DB = previous
		require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.Log{}))
	})
	token := model.Token{UserId: 273, Name: "model-health/video"}
	require.NoError(t, db.Create(&token).Error)
	run := model.ModelHealthRun{ChannelID: 2, ModelName: "doubao-seedance-2-0-260128", State: "succeeded", CreatedAt: 100, RequestID: "legacy-request", UpstreamTaskID: "legacy-task", Template: `{"quota_per_unit":"500000"}`}
	require.NoError(t, db.Create(&run).Error)
	task := model.Task{TaskID: run.UpstreamTaskID, ChannelId: run.ChannelID, UserId: 273, Status: model.TaskStatusSuccess, Quota: 40000, PrivateData: model.TaskPrivateData{TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: run.RequestID}, BillingContext: &model.TaskBillingContext{OriginModelName: run.ModelName, ProviderBilling: &model.TaskProviderBillingSnapshot{Provider: model.TaskBillingProviderDoubaoVideoCNY, Currency: "CNY", CNYPerUSD: "7.3"}}}}
	require.NoError(t, db.Create(&task).Error)
	for _, data := range []struct {
		channel int
		other   string
	}{
		{2, `{"task_id":"legacy-task","settlement_stage":"submit","actual_quota":40000}`},
		{3, `{"task_id":"legacy-task","settlement_stage":"settle","actual_quota":40000}`},
		{2, `{"task_id":"another-task","settlement_stage":"settle","actual_quota":40000}`},
		{2, `{"task_id":"legacy-task","settlement_stage":"settle","actual_quota":50000}`},
	} {
		require.NoError(t, db.Create(&model.Log{UserId: 273, TokenId: token.Id, ChannelId: data.channel, ModelName: run.ModelName, CreatedAt: 101, Type: model.LogTypeConsume, Other: data.other}).Error)
	}
	require.NoError(t, syncProbeTaskBudgetReceipt(context.Background(), run))
	require.NoError(t, db.First(&run, run.ID).Error)
	assert.Nil(t, run.ActualMicro)
	require.NoError(t, db.Create(&model.Log{UserId: 273, TokenId: token.Id, ChannelId: 2, ModelName: run.ModelName, CreatedAt: 102, Type: model.LogTypeRefund, Other: `{"task_id":"legacy-task","settlement_stage":"settle","actual_quota":40000}`}).Error)
	require.NoError(t, syncProbeTaskBudgetReceipt(context.Background(), run))
	require.NoError(t, db.First(&run, run.ID).Error)
	require.NotNil(t, run.ActualMicro)
	assert.Equal(t, int64(584000), *run.ActualMicro)
}

func TestDoubaoProbeHoldWaitsForFinalDurableWalletReceipt(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.CNTaskBilling{}))
	t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.CNTaskBilling{})) })
	token := model.Token{UserId: 273, Name: "model-health/video"}
	require.NoError(t, db.Create(&token).Error)
	template, err := common.Marshal(Template{QuotaPerUnit: "500000"})
	require.NoError(t, err)
	run := model.ModelHealthRun{ChannelID: 2, ModelName: "doubao-seedance-2-0-260128", State: "succeeded", RequestID: "doubao-request", UpstreamTaskID: "doubao-task", ReservationMicro: 5000000, Template: string(template)}
	require.NoError(t, db.Create(&run).Error)
	task := model.Task{TaskID: run.UpstreamTaskID, ChannelId: run.ChannelID, UserId: 273, Status: model.TaskStatusSuccess, Quota: 40000, CNBillingKey: "receipt", PrivateData: model.TaskPrivateData{TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: run.RequestID}, BillingContext: &model.TaskBillingContext{OriginModelName: run.ModelName, ProviderBilling: &model.TaskProviderBillingSnapshot{Provider: model.TaskBillingProviderDoubaoVideoCNY, Currency: "CNY", CNYPerUSD: "99"}}}}
	require.NoError(t, db.Create(&task).Error)
	receipt := model.CNTaskBilling{Key: task.CNBillingKey, State: "active", TaskID: task.ID, RequestID: run.RequestID, PublicTaskID: task.TaskID, UserID: task.UserId, TokenID: token.Id, ChannelID: run.ChannelID, Actual: task.Quota, Currency: "CNY", QuotaPerUnit: "500000", ExchangeRate: "7.3", FactID: "committed-fact"}
	require.NoError(t, syncProbeTaskBudgetReceipt(context.Background(), run), "a receipt not yet written must remain pending without blocking later reconciliation")
	require.NoError(t, db.Create(&receipt).Error)
	require.NoError(t, syncProbeTaskBudgetReceipt(context.Background(), run))
	require.NoError(t, db.First(&run, run.ID).Error)
	assert.Nil(t, run.ActualMicro, "successful generation does not confirm the wallet settlement")
	require.NoError(t, db.Model(&receipt).Updates(map[string]any{"state": "final", "actual": 50000}).Error)
	require.NoError(t, syncProbeTaskBudgetReceipt(context.Background(), run))
	require.NoError(t, db.First(&run, run.ID).Error)
	assert.Nil(t, run.ActualMicro, "a mismatched receipt must not release the hold")
	require.NoError(t, db.Model(&receipt).Update("actual", task.Quota).Error)
	require.NoError(t, syncProbeTaskBudgetReceipt(context.Background(), run))
	require.NoError(t, syncProbeTaskBudgetReceipt(context.Background(), run))
	require.NoError(t, db.First(&run, run.ID).Error)
	require.NotNil(t, run.ActualMicro)
	assert.Equal(t, int64(584000), *run.ActualMicro, "use the receipt's frozen FX rather than a current setting or estimate")
	var count int64
	require.NoError(t, db.Model(&model.ModelHealthConfig{}).Where("kind = ?", "budget-receipt").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestDeferredFirstVideoDetectionRemainsDueAfterBudgetDayChanges(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	sel, _ := seedHealth(t, db, "video")
	target, err := findTarget(db, sel.Channel, sel.Model, sel.Endpoint)
	require.NoError(t, err)
	rule := recommendedRule("video")
	today := dayStart(time.Now().Unix()) + 12*3600
	require.NoError(t, model.SaveModelHealthConfig(db, "s-"+hash(target.ID, sel.Endpoint)[:62], "media-schedule", today-3600))
	// Yesterday's confirmed spend exhausted that day's budget but not tomorrow's.
	require.NoError(t, db.Create(&model.ModelHealthRun{Target: "another-model", State: "succeeded", CreatedAt: dayStart(today) + 1, ReservationMicro: 200, ActualMicro: common.GetPointer(int64(200))}).Error)
	plan, err := GetPlan(context.Background(), sel)
	require.NoError(t, err)
	blocked, err := admissionBlocks(db, target, sel.Endpoint, plan.Template, rule, Budget{Run: 100, Day: 200, Month: 1000, Concurrent: 2, ChannelRPM: 2, ChannelTPM: 200}, today)
	require.NoError(t, err)
	assert.Contains(t, blocked, "Monitoring budget exhausted")
	tomorrow := today + 86400
	// A queued probe rejected before submission is not a completed detection.
	require.NoError(t, db.Create(&model.ModelHealthRun{Target: target.ID, ChannelID: sel.Channel, ModelName: sel.Model, Endpoint: sel.Endpoint, State: "skipped", CreatedAt: tomorrow, Reason: "budget_changed", ActualMicro: common.GetPointer(int64(0))}).Error)
	blocked, err = admissionBlocks(db, target, sel.Endpoint, plan.Template, rule, Budget{Run: 100, Day: 200, Month: 1000, Concurrent: 2, ChannelRPM: 2, ChannelTPM: 200}, tomorrow)
	require.NoError(t, err)
	assert.Empty(t, blocked)
	due, err := mediaScheduledAt(db, target, sel.Endpoint, rule, tomorrow)
	require.NoError(t, err)
	assert.Equal(t, tomorrow, due, "budget deferral must not reset the weekly interval")
}

func TestWeeklyScheduleServesOldestDeferredVideoBeforeEarlierChannel(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	first, _ := seedHealth(t, db, "video")
	second, plan := seedHealth(t, db, "video")
	rule := recommendedRule("video")
	rule.Enabled = true
	require.NoError(t, SaveRule(ctx, "video", Selection{}, rule))
	require.NoError(t, SaveBudget(ctx, Budget{Run: 100, Day: 200, Month: 1000, Concurrent: 1, ChannelRPM: 2, ChannelTPM: 200}))
	now := time.Now().Unix()
	for i, sel := range []Selection{first, second} {
		target, err := findTarget(db, sel.Channel, sel.Model, sel.Endpoint)
		require.NoError(t, err)
		require.NoError(t, model.SaveModelHealthConfig(db, "s-"+hash(target.ID, sel.Endpoint)[:62], "media-schedule", now-int64(i+1)*86400))
	}
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	_, err = acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	schedule(ctx, now)
	var runs []model.ModelHealthRun
	require.NoError(t, db.Order("id").Find(&runs).Error)
	require.Len(t, runs, 1)
	assert.Equal(t, second.Channel, runs[0].ChannelID, "the oldest deadline wins even when its channel is listed last")
	assert.Equal(t, plan.Template.MaxMicro, runs[0].ReservationMicro)
	// Once that attempt has a final charge, the untouched target still runs
	// instead of waiting for its next weekly slot.
	require.NoError(t, db.Model(&runs[0]).Updates(map[string]any{"state": "succeeded", "actual_micro": 100}).Error)
	schedule(ctx, now)
	require.NoError(t, db.Order("id").Find(&runs).Error)
	require.Len(t, runs, 2)
	assert.Equal(t, first.Channel, runs[1].ChannelID)
}

func TestSeedancePlatformProbeDownloadsOriginalTaskWithoutResubmission(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAISeedance, constant.ChannelTypeDoubaoVideo} {
		t.Run(strconv.Itoa(channelType), func(t *testing.T) {
			db := healthDB(t)
			migrateHealth(t, db)
			ctx := context.Background()
			_, _ = seedHealth(t, db, "video")
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.User{})) })
			t.Setenv("MODEL_HEALTH_GATEWAY_URL", "https://gateway.example")
			t.Setenv("MODEL_HEALTH_KEY_FIXTURE", "fixturekey")
			t.Setenv("MODEL_HEALTH_KEY_FIXTURE_ORIGIN", "https://gateway.example")
			user := model.User{Username: "media-probe", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(&user).Error)
			ch := model.Channel{Name: "Seedance fixture", Type: channelType, Status: 1, Key: "unused", Models: "doubao-seedance-2-0-260128", BaseURL: common.GetPointer("https://provider.example")}
			require.NoError(t, db.Create(&ch).Error)
			token := model.Token{UserId: user.Id, Name: "model-health/media", Key: "fixturekey", Status: 1, ExpiredTime: time.Now().Unix() + 3600, RemainQuota: 100, ModelLimitsEnabled: true, ModelLimits: ch.Models}
			require.NoError(t, db.Create(&token).Error)
			target := targetFor(&ch, ch.Models)
			p := builtinTemplate(target, "openai-video", AutoTemplatePolicy{Credential: "MODEL_HEALTH_KEY_FIXTURE", MaxMicro: 10000})
			assert.True(t, p.MinimumConfirmed)
			assert.Equal(t, int64(5000000), p.MaxMicro)
			assert.Equal(t, false, p.Body["generate_audio"])
			assert.Equal(t, "16:9", p.Body["ratio"])
			if channelType == constant.ChannelTypeDoubaoVideo {
				encoded, err := common.Marshal(p.Body)
				require.NoError(t, err)
				var request relaycommon.TaskSubmitReq
				require.NoError(t, common.Unmarshal(encoded, &request))
				request.Model = ch.Models
				c, _ := gin.CreateTestContext(nil)
				info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
				relaycommon.StoreTaskRequest(c, info, constant.TaskActionGenerate, request)
				body, err := (&doubao.TaskAdaptor{}).BuildRequestBody(c, info)
				require.NoError(t, err)
				var upstream struct {
					Duration      int    `json:"duration"`
					Resolution    string `json:"resolution"`
					Ratio         string `json:"ratio"`
					GenerateAudio *bool  `json:"generate_audio"`
				}
				require.NoError(t, common.DecodeJson(body, &upstream))
				assert.Equal(t, 4, upstream.Duration)
				assert.Equal(t, "720p", upstream.Resolution)
				assert.Equal(t, "16:9", upstream.Ratio)
				require.NotNil(t, upstream.GenerateAudio)
				assert.False(t, *upstream.GenerateAudio)
			}
			// Preserve the explicitly configured weekly schedule; budgets still gate it.
			rule := recommendedRule("video")
			rule.Enabled = true
			require.NoError(t, SaveRule(ctx, "video", Selection{}, rule))
			require.NoError(t, model.SaveModelHealthConfig(db, "p-"+hash(target.ID, "openai-video")[:62], "template", p))
			effective, _, err := effectiveRule(db, target, "openai-video")
			require.NoError(t, err)
			assert.True(t, effective.Enabled)
			assert.Equal(t, 604800, effective.IntervalSeconds)
			require.NoError(t, SaveTemplate(ctx, p))
			assert.False(t, fitsBudget(p, Budget{Run: 10000, Day: 100000, Month: 1000000, ChannelTPM: 256}), "video must not silently bypass the configured budget")
			require.NoError(t, SaveBudget(ctx, Budget{Run: 10000000, Day: 20000000, Month: 100000000, Concurrent: 1, ChannelRPM: 1, ChannelTPM: 256}))
			bindings, err := workerBindings(db)
			require.NoError(t, err)
			_, err = acquireWorker(ctx, "fixture", bindings)
			require.NoError(t, err)
			sel := p.Selection
			plan, err := GetPlan(ctx, sel)
			require.NoError(t, err)
			require.True(t, plan.Ready, plan.Blocked)
			run, err := Enqueue(ctx, sel, plan.Revision, p.MaxMicro)
			require.NoError(t, err)
			media, err := os.ReadFile(filepath.Join("..", "..", "web", "public", "images", "home", "underwater.mp4"))
			require.NoError(t, err)
			posts, downloads := 0, 0
			client := &http.Client{Transport: mockProvider(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "gateway.example", req.URL.Host)
				assert.Equal(t, "Bearer sk-fixturekey-"+strconv.Itoa(ch.Id), req.Header.Get("Authorization"))
				if req.Method == http.MethodPost {
					posts++
					return reply(200, `{"id":"task-fixture"}`), nil
				}
				if req.URL.Path == "/v1/videos/task-fixture/content" {
					downloads++
					if downloads == 1 {
						return reply(503, "temporary content failure"), nil
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(bytes.NewReader(media))}, nil
				}
				return reply(200, `{"status":"completed"}`), nil
			})}
			require.NoError(t, work(ctx, client, time.Now().Unix()))
			require.NoError(t, db.Model(&run).Update("next_poll", 0).Error)
			require.NoError(t, work(ctx, client, time.Now().Unix()))
			require.NoError(t, db.First(&run, run.ID).Error)
			assert.Equal(t, "polling", run.State)
			assert.Equal(t, 1, posts)
			require.NoError(t, db.Model(&run).Update("next_poll", 0).Error)
			require.NoError(t, work(ctx, client, time.Now().Unix()))
			require.NoError(t, db.First(&run, run.ID).Error)
			assert.Equal(t, "succeeded", run.State, run.Reason)
			assert.Equal(t, 1, posts)
			assert.Equal(t, 2, downloads)
			assert.Nil(t, run.ActualMicro, "generation success is not a cost receipt")
		})
	}
}

func TestDetectionCostsReusePersistedOPSFeesWithoutMixingConnectionsOrLocalCharges(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	previous := model.LOG_DB
	model.LOG_DB = db
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "domestic-product"})
	t.Cleanup(func() {
		model.LOG_DB = previous
		model.ConfigureOpsFees(nil)
		require.NoError(t, db.Migrator().DropTable(&model.Log{}, &model.Token{}, &model.OpsFee{}))
	})
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.Token{}, &model.OpsFee{}))
	token := model.Token{UserId: 273, Name: "model-health/fixture"}
	require.NoError(t, db.Create(&token).Error)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, cst)
	entries := []struct {
		request, amount, connection, status, currency, source string
		days, channel, user                                   int
	}{
		{"confirmed", "0.000249", "domestic-product", "available", "CNY", "manual", 0, 2, 273},
		{"previous-week", "0.1", "domestic-product", "available", "CNY", "scheduled", -8, 2, 273},
		{"wrong-connection", "99", "project", "available", "CNY", "manual", 0, 2, 273},
		{"wrong-channel", "99", "domestic-product", "available", "CNY", "manual", 0, 3, 273},
		{"wrong-user", "99", "domestic-product", "available", "CNY", "manual", 0, 2, 274},
		{"unconfirmed", "99", "domestic-product", "unconfirmed", "CNY", "manual", 0, 2, 273},
		{"wrong-currency", "99", "domestic-product", "available", "USD", "manual", 0, 2, 273},
		{"invalid-amount", "1e99999", "domestic-product", "available", "CNY", "manual", 0, 2, 273},
		{"zero", "0", "domestic-product", "available", "CNY", "manual", 0, 2, 273},
	}
	var confirmedID, zeroID int64
	for _, entry := range entries {
		run := model.ModelHealthRun{ChannelID: 2, ModelName: "fixture", RequestID: entry.request, Source: entry.source, State: "succeeded", CreatedAt: now.AddDate(0, 0, entry.days).Unix(), ActualMicro: common.GetPointer(int64(999999))}
		require.NoError(t, db.Create(&run).Error)
		if entry.request == "confirmed" {
			confirmedID = run.ID
		}
		if entry.request == "zero" {
			zeroID = run.ID
		}
		require.NoError(t, db.Create(&model.Log{RequestId: entry.request, UserId: 273, TokenId: token.Id, ModelName: run.ModelName, ChannelId: entry.channel, Type: model.LogTypeConsume}).Error)
		require.NoError(t, db.Create(&model.OpsFee{Connection: entry.connection, RequestID: entry.request, UserID: strconv.Itoa(entry.user), Model: run.ModelName, Amount: common.GetPointer(entry.amount), StationAmount: common.GetPointer("8"), Status: entry.status, Currency: entry.currency}).Error)
	}
	// A second OPS connection contains the same request; count the active one once.
	require.NoError(t, db.Create(&model.OpsFee{Connection: "project", RequestID: "confirmed", UserID: "273", Model: "fixture", Amount: common.GetPointer("99"), Status: "available", Currency: "CNY"}).Error)
	result, err := readPlatformOpsCosts(context.Background(), "manual", 1, now)
	require.NoError(t, err)
	assert.Equal(t, "confirmed", result.State)
	assert.Equal(t, map[int64]int64{confirmedID: 249, zeroID: 0}, result.Amounts)
	require.NotNil(t, result.WeekMicro)
	assert.Equal(t, int64(249), *result.WeekMicro)
	assert.Equal(t, int64(100249), *result.TotalMicro, "totals include all detection sources and pages")
	// A correction received through the normal feed is visible after persistence.
	require.NoError(t, db.Model(&model.OpsFee{}).Where("connection = ? AND request_id = ?", "domestic-product", "confirmed").Update("amount", "0.0003").Error)
	result, err = readPlatformOpsCosts(context.Background(), "manual", 1, now)
	require.NoError(t, err)
	assert.Equal(t, int64(300), result.Amounts[confirmedID])
	assert.Equal(t, int64(100300), *result.TotalMicro)
	// Async receipts are linked through the original task request, even when
	// settlement logs use another generated request ID.
	run := model.ModelHealthRun{ChannelID: 2, ModelName: "fixture", RequestID: "video-original", UpstreamTaskID: "video-public-id", State: "succeeded", Source: "manual", CreatedAt: now.Unix()}
	require.NoError(t, db.Create(&run).Error)
	task := model.Task{TaskID: run.UpstreamTaskID, ChannelId: 2, UserId: 273, Status: model.TaskStatusSuccess, Properties: model.Properties{OriginModelName: "fixture"}, PrivateData: model.TaskPrivateData{TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: run.RequestID}}}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&model.OpsFee{Connection: "domestic-product", RequestID: run.RequestID, UserID: "273", Model: "fixture", Amount: common.GetPointer("1.23"), Currency: "CNY", Status: "available"}).Error)
	result, err = readPlatformOpsCosts(context.Background(), "manual", 1, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1230000), result.Amounts[run.ID])
	require.NoError(t, db.Model(&task).Update("channel_id", 3).Error)
	result, err = readPlatformOpsCosts(context.Background(), "manual", 1, now)
	require.NoError(t, err)
	assert.NotContains(t, result.Amounts, run.ID)
}

func TestDetectionOPSRecoveryPersistsMissingReceiptsAndRefundsWithoutRebilling(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.Log{}, &model.OpsFee{}))
	previous := model.LOG_DB
	model.LOG_DB = db
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "domestic-product"})
	t.Cleanup(func() {
		model.LOG_DB = previous
		model.ConfigureOpsFees(nil)
		require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.Log{}, &model.OpsFee{}))
	})
	ctx := context.Background()
	token := model.Token{UserId: 273, Name: "model-health/fixture"}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, model.SaveModelHealthConfig(db, "auto-templates", "auto-templates", AutoTemplatePolicy{Enabled: true, TokenID: token.Id}))
	now := time.Now()
	runs := make(map[string]model.ModelHealthRun)
	for _, name := range []string{"wrong-owner", "missing", "refunded", "mismatch", "zero", "waiting", "invalid-amount"} {
		run := model.ModelHealthRun{ChannelID: 2, ModelName: "fixture", RequestID: name, State: "succeeded", Source: "manual", CreatedAt: now.Unix(), UpstreamTaskID: "task-" + name, ReservationMicro: 3000000}
		status := model.TaskStatus(model.TaskStatusSuccess)
		quota, owner := 10, token.UserId
		if name == "refunded" {
			run.State, status, quota = "failed", model.TaskStatusFailure, 0
		}
		if name == "wrong-owner" {
			owner++
		}
		require.NoError(t, db.Create(&run).Error)
		require.NoError(t, db.Create(&model.Task{TaskID: run.UpstreamTaskID, ChannelId: run.ChannelID, UserId: owner, Status: status, Quota: quota, Properties: model.Properties{OriginModelName: run.ModelName}, PrivateData: model.TaskPrivateData{TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: name}}}).Error)
		runs[name] = run
	}
	require.NoError(t, db.Create(&model.OpsFee{Connection: "domestic-product", RequestID: "refunded", UserID: "273", Model: "fixture", Amount: common.GetPointer("2"), StationAmount: common.GetPointer("2"), Currency: "CNY", Status: "available"}).Error)
	require.NoError(t, db.Create(&model.OpsFee{Connection: "project", RequestID: "missing", UserID: "273", Model: "fixture", Amount: common.GetPointer("99"), Currency: "CNY", Status: "available"}).Error)
	calls := 0
	client := opsclient.New("https://ops.example", "fixture", "local-receipt-fixture-secret", time.Second)
	client.HTTP = &http.Client{Transport: mockProvider(func(req *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, http.MethodGet, req.Method)
		assert.Empty(t, req.URL.RawQuery, "recovery must not rewind the normal fee feed")
		name := strings.TrimPrefix(req.URL.Path, "/api/v1/catalog/v1/charges/")
		assert.NotEqual(t, "wrong-owner", name)
		fee := opsfees.Fee{RequestID: name, UserID: "273", Model: "fixture", Amount: common.GetPointer("2.7181"), ChargedAt: now.UTC().Format(time.RFC3339)}
		if name == "refunded" {
			fee.Amount, fee.StationAmount, fee.RefundAmount = common.GetPointer("2"), common.GetPointer("0"), common.GetPointer("1.999996")
		}
		if name == "mismatch" {
			fee.UserID = "274"
		}
		if name == "zero" {
			fee.Amount = common.GetPointer("0")
		}
		if name == "invalid-amount" {
			fee.Amount = common.GetPointer("1e99999")
		}
		status := "station_billed"
		if name == "waiting" {
			status = "pending"
		}
		body, err := common.Marshal(map[string]any{"data": map[string]any{"requestId": name, "userId": "273", "status": status, "logFee": fee}})
		require.NoError(t, err)
		return reply(200, string(body)), nil
	})}
	config := opsfees.Config{Connection: "domestic-product"}
	require.NoError(t, syncProbeOpsReceipts(ctx, config, client, now))
	require.NoError(t, syncProbeOpsReceipts(ctx, config, client, now))
	assert.Equal(t, 1, calls, "another HTTP slot must not duplicate the recovery request")
	for i := 1; i <= 5; i++ {
		err := syncProbeOpsReceipts(ctx, config, client, now.Add(time.Duration(i)*31*time.Second))
		if i == 2 || i == 5 {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 6, calls)
	var rows []model.OpsFee
	require.NoError(t, db.Where("connection = ?", config.Connection).Order("request_id").Find(&rows).Error)
	require.Len(t, rows, 3)
	assert.Equal(t, "missing", rows[0].RequestID)
	assert.Equal(t, "2.7181", *rows[0].Amount)
	assert.Equal(t, "refunded", rows[1].RequestID)
	assert.Equal(t, "2", *rows[1].Amount, "OPS calculation remains separate from refunded net debit")
	assert.Equal(t, "0", *rows[1].StationAmount)
	assert.Equal(t, "1.999996", *rows[1].RefundAmount)
	assert.Equal(t, "zero", rows[2].RequestID)
	assert.Equal(t, "0", *rows[2].Amount)
	result, err := readPlatformOpsCosts(ctx, "manual", 1, now)
	require.NoError(t, err)
	assert.Equal(t, int64(2718100), result.Amounts[runs["missing"].ID])
	assert.Equal(t, int64(0), result.Amounts[runs["zero"].ID])
	assert.NotContains(t, result.Amounts, runs["mismatch"].ID)
	var refunded model.ModelHealthRun
	require.NoError(t, db.First(&refunded, runs["refunded"].ID).Error)
	assert.Nil(t, refunded.ActualMicro, "OPS display recovery must not release a budget hold")
	var task model.Task
	require.NoError(t, db.Where("task_id = ?", refunded.UpstreamTaskID).First(&task).Error)
	assert.Zero(t, task.Quota)
}

func TestGatewayProbeUsesPlatformKeyPublicModelAndPinnedChannel(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	sel, original := seedHealth(t, db, "text")
	const origin = "https://gateway.example"
	const key = "fixtureplatformkey"
	t.Setenv("MODEL_HEALTH_GATEWAY_URL", origin)
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE", key)
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE_ORIGIN", origin)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	t.Cleanup(func() {
		require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.User{}))
	})
	user := model.User{Username: "probe-fixture", Password: "not-a-login-password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Quota: 10000}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: key, Name: "model-health/fixture", Status: common.TokenStatusEnabled,
		ExpiredTime: time.Now().Unix() + 3600, RemainQuota: 1000, ModelLimitsEnabled: true, ModelLimits: sel.Model, Group: "default"}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", sel.Channel).Update("model_mapping", "{\"example-text\":\"upstream-private-name\"}").Error)
	p := original.Template
	require.NoError(t, SaveTemplate(ctx, p))
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	owned, err := acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	require.True(t, owned)
	plan, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	require.True(t, plan.Ready, plan.Blocked)
	run, err := Enqueue(ctx, sel, plan.Revision, p.MaxMicro)
	require.NoError(t, err)
	posts := 0
	client := &http.Client{Transport: mockProvider(func(req *http.Request) (*http.Response, error) {
		posts++
		assert.Equal(t, origin+"/v1/chat/completions", req.URL.String())
		assert.Equal(t, "Bearer sk-"+key+"-"+strconv.Itoa(sel.Channel), req.Header.Get("Authorization"))
		assert.Nil(t, req.GetBody, "paid submission must not be replayable by the transport")
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "\"model\":\"example-text\"")
		assert.NotContains(t, string(body), "upstream-private-name")
		response := reply(200, "{\"choices\":[{\"message\":{\"content\":\"OK\"}}]}")
		response.Header.Set(common.RequestIdKey, "gateway-request-1")
		return response, nil
	})}
	require.NoError(t, work(ctx, client, time.Now().Unix()))
	require.NoError(t, work(ctx, client, time.Now().Unix()))
	require.NoError(t, db.First(&run, run.ID).Error)
	assert.Equal(t, "succeeded", run.State)
	assert.Equal(t, "gateway-request-1", run.RequestID)
	assert.Equal(t, 1, posts)
	assert.NotContains(t, run.Evidence, key)

	for _, change := range []map[string]any{
		{"unlimited_quota": true}, {"expired_time": time.Now().Unix() - 1},
		{"model_limits_enabled": false}, {"model_limits": "unrelated-model"},
		{"cross_group_retry": true}, {"group": "auto"}, {"remain_quota": 0},
		{"name": "customer-token"},
	} {
		require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(change).Error)
		assert.Empty(t, gatewayProbeKey(db, p, Target{ChannelID: sel.Channel, Model: sel.Model, base: "https://provider.example"}), change)
		require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{
			"unlimited_quota": false, "expired_time": token.ExpiredTime, "model_limits_enabled": true,
			"model_limits": sel.Model, "cross_group_retry": false, "group": "default", "remain_quota": 1000, "name": token.Name,
		}).Error)
	}

	// Revocation and a wrong destination must withdraw readiness, not fall back
	// to sending a platform key directly to the channel's upstream.
	require.NoError(t, db.Model(&token).Update("status", common.TokenStatusDisabled).Error)
	bindings, err = workerBindings(db)
	require.NoError(t, err)
	_, err = acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	plan, err = GetPlan(ctx, sel)
	require.NoError(t, err)
	assert.False(t, plan.Ready)
	assert.False(t, bindings.hasProbe(p, Target{base: "https://provider.example"}))
	t.Setenv("MODEL_HEALTH_GATEWAY_URL", "https://other.example")
	assert.Empty(t, probeCredential(p, Target{base: "https://provider.example"}))
}

func TestAutomaticTemplatesCoverExistingAndNewModelsWithoutEnablingSchedules(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.User{})) })
	t.Setenv("MODEL_HEALTH_GATEWAY_URL", "https://gateway.example")
	t.Setenv("MODEL_HEALTH_KEY_PLATFORM", "fixtureplatformkey")
	t.Setenv("MODEL_HEALTH_KEY_PLATFORM_ORIGIN", "https://gateway.example")
	user := model.User{Username: "auto-probe", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "fixtureplatformkey", Name: "model-health/automatic", Status: common.TokenStatusEnabled, ExpiredTime: time.Now().Unix() + 3600, RemainQuota: 1000, ModelLimitsEnabled: true, ModelLimits: "existing", Group: "default"}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "DeepSeek", Type: constant.ChannelTypeDeepSeek, Status: common.ChannelStatusEnabled, BaseURL: common.GetPointer("https://provider.example"), Models: "deepseek-v4-flash,deepseek-v4-pro,qwen3-rerank,qwen3.7-text-embedding-flash,unknown-embedding", Group: "default"}
	require.NoError(t, db.Create(&channel).Error)
	cache := miniredis.RunT(t)
	priorRedis, priorRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled, common.RDB = priorRedis, priorRDB
	})
	stale := token
	cacheKey := "token:" + common.GenerateHMAC(token.Key)
	require.NoError(t, common.RDB.HSet(ctx, cacheKey, "Id", token.Id, "Name", token.Name, "ModelLimits", token.ModelLimits, "RemainQuota", 870, "UsedQuota", 130).Err())
	policy := AutoTemplatePolicy{Enabled: true, Credential: "MODEL_HEALTH_KEY_PLATFORM", TokenID: token.Id, MaxMicro: 10000}
	t.Setenv("MODEL_HEALTH_TEMPLATE_PROFILE_GATE", "true")
	require.NoError(t, SaveAutoTemplatePolicy(ctx, policy))
	var staged int64
	require.NoError(t, db.Model(&model.ModelHealthConfig{}).Where("kind = ?", "template").Count(&staged).Error)
	require.Zero(t, staged, "candidate must not publish contracts before profile activation")
	policy.Profile = defaultTemplateProfile
	require.NoError(t, SaveAutoTemplatePolicy(ctx, policy))
	policy.Profile = ""
	require.NoError(t, SaveAutoTemplatePolicy(ctx, policy))
	retained, err := GetAutoTemplatePolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, defaultTemplateProfile, retained.Profile, "existing UI saves retain the active profile")
	reloaded, err := model.GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.NotContains(t, stale.ModelLimits, "deepseek-v4-flash")
	assert.Contains(t, reloaded.ModelLimits, "deepseek-v4-flash", "preheated token cache must not retain the old allowlist")
	assert.Equal(t, 870, reloaded.RemainQuota, "allowlist changes must preserve unflushed cached charges")
	assert.Equal(t, 130, reloaded.UsedQuota)
	// Simulate Redis being unavailable after the DB commit, then retry.
	require.NoError(t, common.RDB.HSet(ctx, cacheKey, "ModelLimits", stale.ModelLimits).Err())
	require.NoError(t, SyncAutoTemplates(ctx))
	reloaded, err = model.GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Contains(t, reloaded.ModelLimits, "deepseek-v4-flash")
	assert.Equal(t, 870, reloaded.RemainQuota)
	require.NoError(t, SyncAutoTemplates(ctx))
	var count int64
	require.NoError(t, db.Model(&model.ModelHealthConfig{}).Where("kind = ?", "template").Count(&count).Error)
	assert.EqualValues(t, 5, count)
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	_, err = acquireWorker(ctx, "automatic-fixture", bindings)
	require.NoError(t, err)
	require.NoError(t, SaveBudget(ctx, Budget{Run: 10000, Day: 100000, Month: 1000000, Concurrent: 1, ChannelRPM: 1, ChannelTPM: 256}))
	for _, name := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		plan, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: name, Endpoint: "openai"})
		require.NoError(t, err)
		assert.True(t, plan.Ready, plan.Blocked)
		assert.False(t, plan.Rule.Enabled)
		assert.Equal(t, "low", plan.Template.Body["reasoning_effort"])
	}
	unknown, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: "unknown-embedding", Endpoint: "embeddings"})
	require.NoError(t, err)
	assert.False(t, unknown.Ready)
	assert.Contains(t, unknown.Blocked, "Confirm the expected embedding dimension")
	assert.NotEmpty(t, unknown.Template.Body)
	// A manual override survives periodic provisioning.
	sel := Selection{Channel: channel.Id, Model: "deepseek-v4-flash", Endpoint: "openai"}
	plan, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	custom := plan.Template
	custom.Body["max_tokens"] = float64(64)
	require.NoError(t, SaveTemplate(ctx, custom))
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("models", channel.Models+",future-chat-model").Error)
	// Simulate a concurrent charge before the synchronization: quota is preserved.
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("remain_quota", 900).Error)
	require.NoError(t, SyncAutoTemplates(ctx))
	plan, err = GetPlan(ctx, sel)
	require.NoError(t, err)
	assert.Equal(t, float64(64), plan.Template.Body["max_tokens"])
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Contains(t, token.ModelLimits, "future-chat-model")
	newPlan, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: "future-chat-model", Endpoint: "openai"})
	require.NoError(t, err)
	assert.True(t, newPlan.Template.MinimumConfirmed)
	assert.False(t, newPlan.Ready, "executor must refresh the exact per-target binding")
	// Generated profiles follow channel changes; manual overrides stay manual.
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{"base_url": "https://new-provider.example", "model_mapping": `{"deepseek-v4-pro":"mapped-pro"}`}).Error)
	require.NoError(t, SyncAutoTemplates(ctx))
	generated, err := GetPlan(ctx, Selection{Channel: channel.Id, Model: "deepseek-v4-pro", Endpoint: "openai"})
	require.NoError(t, err)
	assert.True(t, generated.Template.MinimumConfirmed)
	assert.Equal(t, defaultTemplateProfile, generated.Template.GeneratedProfile)
	manual, err := GetPlan(ctx, sel)
	require.NoError(t, err)
	assert.Empty(t, manual.Template.GeneratedProfile)
	assert.False(t, manual.Ready, "a manual template must be reviewed after the upstream changes")
	// Revocation is never undone by automatic provisioning.
	require.NoError(t, db.Model(&token).Update("status", common.TokenStatusDisabled).Error)
	assert.Error(t, SyncAutoTemplates(ctx))
	policy.Enabled = false
	assert.NoError(t, SaveAutoTemplatePolicy(ctx, policy))
}

func TestBuiltinTemplatesUseBoundedProtocolRequests(t *testing.T) {
	for _, tc := range []struct {
		kind, endpoint, name, path string
		ready                      bool
	}{
		{"llm", "openai", "deepseek-v4-flash", "/v1/chat/completions", true},
		{"llm", "anthropic", "claude-sonnet", "/v1/messages", true},
		{"llm", "openai-response", "codex-model", "/v1/responses", true},
		{"llm", "gemini", "gemini-model", "/v1beta/models/gemini-model:generateContent", true},
		{"embedding", "embeddings", "qwen3.7-text-embedding-flash", "/v1/embeddings", true},
		{"rerank", "jina-rerank", "qwen3-rerank", "/v1/rerank", true},
		{"image", "image-generation", "unknown-image", "/v1/images/generations", false},
		{"video", "openai-video", "unknown-video", "/v1/videos", false},
		{"tts", "audio_speech", "unknown-voice", "/v1/audio/speech", false},
		{"speech2text", "audio_transcription", "fun-asr-flash", "/v1/audio/transcriptions", false},
		{"audio", "audio_generation", "unknown-audio", "/v1/chat/completions", false},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			target := Target{ID: "fixture", Model: tc.name, Upstream: tc.name, Type: tc.kind, base: "https://provider.example"}
			p := builtinTemplate(target, tc.endpoint, AutoTemplatePolicy{Credential: "MODEL_HEALTH_KEY_PLATFORM", MaxMicro: 10000})
			assert.Equal(t, tc.path, p.Path)
			assert.Equal(t, tc.ready, p.validFor(target, tc.endpoint))
			assert.True(t, safeTemplateValue(p.Body))
			assert.NotEmpty(t, p.Body)
			assert.Equal(t, 128, p.MaxTokens)
		})
	}
}
