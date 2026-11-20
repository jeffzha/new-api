package opsfees

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestOpsFeeHistoryDatabaseMatrix(t *testing.T) {
	oldDB, oldType := model.DB, common.MainDatabaseType()
	t.Cleanup(func() { model.DB = oldDB; common.SetMainDatabaseType(oldType); model.ConfigureOpsFees(nil) })
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := testDB(t, dialect)
			model.DB = db
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			for range 2 {
				require.NoError(t, model.MigrateOpsFees(db))
			}
			original := model.Log{UserId: 15, Type: 2, RequestId: "req-1", ModelName: "sample", Quota: 9999, Other: "original-facts", CreatedAt: 1}
			require.NoError(t, db.Create(&original).Error)
			live := model.OpsFeeSync{Connection: "current", Cursor: "newest-live-cursor", NextRunAt: 100, LastSuccessAt: 99}
			require.NoError(t, db.Create(&live).Error)
			amount := "0.21"
			require.NoError(t, db.Create(&model.OpsFee{Connection: "current", RequestID: "req-1", UserID: "15", Model: "sample", Amount: &amount, Status: "available", Currency: "USD"}).Error)
			ctx, now := context.Background(), time.Unix(1793692800, 0)
			mode, calls := 200, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/api/v1/catalog/v1/fees", r.URL.Path)
				if mode != 200 {
					w.Header().Set("Retry-After", "180")
					w.WriteHeader(mode)
					return
				}
				cursor, more := "page-1", true
				var value any = "0.10"
				id := "req-1"
				if r.URL.Query().Get("since") == "page-1" {
					cursor, more, value, id = "page-2", false, nil, "req-unpriced"
				} else {
					require.Equal(t, "2026-11-01T00:00:00Z~0", r.URL.Query().Get("since"))
				}
				var page map[string]any
				require.NoError(t, common.Unmarshal(feePage(t, value, cursor, more), &page))
				data := page["data"].(map[string]any)
				data["currency"] = "USD"
				data["items"].([]any)[0].(map[string]any)["requestId"] = id
				raw, err := common.Marshal(page)
				require.NoError(t, err)
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			cfg := Config{BaseURL: server.URL, KeyID: "gw", Secret: "test-secret-at-least-sixteen", Connection: "current", Currency: "USD", Timeout: time.Second}
			w := New(db, db, cfg)
			w.Now = func() time.Time { return now }
			since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			r, err := w.HistoryStep(ctx, since, 10*time.Second)
			require.NoError(t, err)
			require.Equal(t, 1, r.Rows)
			require.False(t, r.Complete)
			// Separate processes share the history schedule; no multiplied polling.
			other := New(db, db, cfg)
			other.Now = w.Now
			_, err = other.HistoryStep(ctx, since, 10*time.Second)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			now = now.Add(10 * time.Second)
			mode = 429
			r, err = other.HistoryStep(ctx, since, 10*time.Second)
			require.Error(t, err)
			require.GreaterOrEqual(t, r.Wait, 180*time.Second)
			mode = 200
			now = now.Add(10 * time.Second)
			_, err = w.HistoryStep(ctx, since, 10*time.Second)
			require.NoError(t, err)
			require.Equal(t, 2, calls, "restart must honor shared backoff")
			now = now.Add(180 * time.Second)
			r, err = other.HistoryStep(ctx, since, 10*time.Second)
			require.NoError(t, err)
			require.True(t, r.Complete)
			_, err = w.HistoryStep(ctx, since, 10*time.Second)
			require.NoError(t, err)
			require.Equal(t, 3, calls, "completed history must not be replayed")
			var actualLive model.OpsFeeSync
			require.NoError(t, db.First(&actualLive, "connection = ?", "current").Error)
			require.Equal(t, live, actualLive, "history must preserve every live reader field")
			model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "current", Currency: "USD", Since: now.Unix()})
			logs := []*model.Log{&original, {UserId: 15, Type: 2, RequestId: "req-unpriced", ModelName: "sample"}, {UserId: 99, Type: 2, RequestId: "req-1", ModelName: "sample"}}
			require.NoError(t, model.AttachOpsFees(ctx, logs))
			require.Equal(t, "0.21", *logs[0].OpsFee.Amount, "old history cannot overwrite live corrections")
			require.Equal(t, "unconfirmed", logs[1].OpsFee.Status)
			require.Nil(t, logs[1].OpsFee.Amount)
			require.Equal(t, "waiting", logs[2].OpsFee.Status, "identity isolation remains enforced")
			var saved model.Log
			require.NoError(t, db.First(&saved, original.Id).Error)
			require.Equal(t, 9999, saved.Quota)
			require.Equal(t, "original-facts", saved.Other)
			// Lease loss and failed inserts cannot advance the historical cursor.
			_, err = model.ClaimOpsFeeSync(ctx, db, "rollback", "owner", now.Unix())
			require.NoError(t, err)
			require.NoError(t, db.Migrator().DropTable(&model.OpsFee{}))
			err = model.SaveOpsFeeHistoryPage(ctx, db, "rollback", "current", "owner", "bad", []model.OpsFee{{RequestID: "missing"}}, now.Unix(), now.Unix()+10, false)
			require.Error(t, err)
			var progress model.OpsFeeSync
			require.NoError(t, db.First(&progress, "connection = ?", "rollback").Error)
			require.Empty(t, progress.Cursor)
			require.Error(t, model.SaveOpsFeeHistoryPage(ctx, db, "rollback", "current", "wrong-owner", "bad", nil, now.Unix(), 0, false))
		})
	}
}

func TestOpsFeeHistoryRejectsInvalidOrWrongCurrency(t *testing.T) {
	oldType := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(oldType) })
	db := testDB(t, "sqlite")
	require.NoError(t, model.MigrateOpsFees(db))
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write(feePage(t, "0.1", "next", false))
	}))
	defer server.Close()
	w := New(db, db, Config{BaseURL: server.URL, KeyID: "gw", Secret: "test-secret-at-least-sixteen", Connection: "usd", Currency: "USD", Timeout: time.Second})
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	_, err := w.HistoryStep(context.Background(), since, time.Second)
	require.Error(t, err)
	require.Zero(t, calls)
	_, err = w.HistoryStep(context.Background(), since, 10*time.Second)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "invalid_response"))
	var count int64
	require.NoError(t, db.Model(&model.OpsFee{}).Count(&count).Error)
	require.Zero(t, count, "CNY fees must never enter a USD connection")
}
