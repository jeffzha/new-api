package opspush

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
	"github.com/QuantumNous/new-api/service/opsfees"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func TestCommittedRelayPushPersistsFeeBeforeIncrementalReadback(t *testing.T) {
	p, db := pusherFixture(t, &opsStub{})
	previousDB, previousLogs := model.DB, model.LOG_DB
	previousCurrency := operation_setting.GetGeneralSetting().QuotaDisplayType
	previousRedis := common.RedisEnabled
	model.DB, model.LOG_DB, common.RedisEnabled = db, db, false
	operation_setting.GetGeneralSetting().QuotaDisplayType = "USD"
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled = previousDB, previousLogs, previousRedis
		operation_setting.GetGeneralSetting().QuotaDisplayType = previousCurrency
		model.ConfigureOpsFees(nil)
	})
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-usd")
	p.Config.Scope, p.Config.StateID = "overseas-usd", 2
	p.Config.Spec, p.Config.Currency, p.Config.UsageOnly = SpecV11, "USD", true
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskBillingReconciliation{}, &model.User{}, &model.Token{}, &model.OPSWalletAdmission{}, &model.OPSWalletHold{}, &model.OPSWalletUsage{}))
	user := model.User{Username: "instant-fee-test", AffCode: "instant-fee", Quota: 12345}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "test-only-instant-fee", RemainQuota: 1000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&token).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			// The feed deliberately has no results: only the push ACK can fill SQL.
			fmt.Fprint(w, `{"data":{"currency":"USD","items":[],"nextCursor":"","hasMore":false}}`)
			return
		}
		fmt.Fprintf(w, `{"data":{"accepted":1,"duplicates":0,"rejected":[],"charges":[{"requestId":"instant-relay","userId":"%d","status":"charged","logFee":{"requestId":"instant-relay","userId":"%d","model":"m","opsAmount":"0.00002","chargedAt":"2026-11-23T01:00:00Z"}}]}}`, user.Id, user.Id)
	}))
	defer server.Close()
	p.Config.URL = server.URL
	for k, v := range map[string]string{"OPS_FEES_ENABLED": "true", "OPS_FEES_URL": server.URL, "OPS_FEES_KEY_ID": p.Config.KeyID, "OPS_FEES_SECRET": p.Config.Secret, "OPS_FEES_CURRENCY": "USD", "OPS_FEES_NAMESPACE": t.Name()} {
		t.Setenv(k, v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feedCtx, stopFeed := context.WithCancel(context.Background())
	stopFeed() // Configure the real reader while keeping incremental GETs stopped.
	require.NoError(t, opsfees.StartConfigured(feedCtx))
	require.NoError(t, p.Migrate())
	// The running scanner is idle before the transaction creates the debit.
	require.Equal(t, idleWait, p.Step())
	wake := p.committedWake(ctx)
	_, err := model.ReserveOPSWallet(db, model.OPSWalletHoldIdentity{RequestID: "instant-relay", UserID: user.Id, TokenID: token.Id, Model: "m"}, 15, model.OPSWalletPosition{UserID: user.Id, EntryID: 1, Quota: 100})
	require.NoError(t, err)
	event, err := common.Marshal(map[string]any{"requestId": "instant-relay", "userId": fmt.Sprint(user.Id), "model": "m", "status": "success", "currency": "USD", "amount": "0.00002", "extra": map[string]any{"billing_scope": p.Config.Scope, "project_relay_terminal": true, "recordKind": "usage", "direction": "debit"}})
	require.NoError(t, err)
	require.NoError(t, model.FinalizeOPSRelay(db, "instant-relay", user.Id, 10, event))
	opsnotify.Committed(p.Config.Scope)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("committed debit did not wake the idle uploader")
	}
	require.Zero(t, p.Step())
	logs := []*model.Log{{Type: model.LogTypeConsume, UserId: user.Id, ModelName: "m", RequestId: "instant-relay"}}
	require.NoError(t, model.AttachOpsFees(ctx, logs))
	require.Equal(t, "available", logs[0].OpsFee.Status)
	require.Equal(t, "USD", logs[0].OpsFee.Currency)
	require.Equal(t, "0.00002", *logs[0].OpsFee.Amount)
	var stored model.OPSWalletUsage
	require.NoError(t, db.First(&stored).Error)
	require.Positive(t, stored.DeliveredAt)
	var current model.User
	require.NoError(t, db.First(&current, user.Id).Error)
	require.Equal(t, user.Quota, current.Quota, "fee display cannot debit the local wallet again")
}
