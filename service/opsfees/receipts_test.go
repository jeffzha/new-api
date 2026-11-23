package opsfees

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestPushFeeReceiptsDatabaseMatrix(t *testing.T) {
	previousDB, previousType := model.DB, common.MainDatabaseType()
	previousConfig := receiptConfig.Load()
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		receiptConfig.Store(previousConfig)
		model.ConfigureOpsFees(nil)
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := testDB(t, dialect)
			model.DB = db
			for range 2 {
				require.NoError(t, model.MigrateOpsFees(db))
			}
			c := Config{BaseURL: "https://ops.example.test", KeyID: "overseas", Secret: "isolated-test-secret-only", Connection: "usd", Currency: "USD"}
			receiptConfig.Store(&c)
			model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: c.Connection, Currency: c.Currency})
			sent := []byte(`{"events":[{"requestId":"r1","userId":"15","model":"video","currency":"USD"}]}`)
			ack := []byte(`{"data":{"charges":[{"requestId":"r1","userId":"15","model":"video","currency":"USD","status":"charged","logFee":{"requestId":"r1","userId":"15","model":"video","opsAmount":"0.0042","chargedAt":"2026-11-23T01:00:00Z"}}]}}`)
			at := time.Date(2026, 11, 23, 1, 0, 1, 0, time.UTC)
			for _, bad := range []string{
				strings.ReplaceAll(string(ack), `"USD"`, `"CNY"`),
				strings.ReplaceAll(string(ack), `"15"`, `"16"`),
				strings.ReplaceAll(string(ack), `"video"`, `"other"`),
				strings.ReplaceAll(string(ack), `"0.0042"`, `"-1"`),
				strings.ReplaceAll(string(ack), `"charged"`, `"pending"`),
				strings.ReplaceAll(string(ack), `"chargedAt":`, `"unknownTime":`),
			} {
				require.NoError(t, AcceptPushReceipts(c.BaseURL, c.KeyID, c.Secret, sent, []byte(bad), at))
			}
			require.NoError(t, AcceptPushReceipts(c.BaseURL, "domestic", c.Secret, sent, ack, at))
			require.NoError(t, AcceptPushReceipts(c.BaseURL, c.KeyID, "wrong-secret", sent, ack, at))
			require.NoError(t, AcceptPushReceipts(c.BaseURL, c.KeyID, c.Secret, []byte(strings.ReplaceAll(string(sent), `"USD"`, `"CNY"`)), ack, at))
			var count int64
			require.NoError(t, db.Model(&model.OpsFee{}).Count(&count).Error)
			require.Zero(t, count)
			for range 2 {
				require.NoError(t, AcceptPushReceipts(c.BaseURL, c.KeyID, c.Secret, sent, ack, at))
			}
			require.NoError(t, db.Model(&model.OpsFee{}).Count(&count).Error)
			require.EqualValues(t, 1, count, "retries must not duplicate fees")
			// Reconfigure to model another blue/green process or a restart. The
			// first visible fee comes from SQL before a feed page has been fetched.
			model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: c.Connection, Currency: c.Currency})
			logs := []*model.Log{{Type: 2, UserId: 15, ModelName: "video", RequestId: "r1"}}
			require.NoError(t, model.AttachOpsFees(context.Background(), logs))
			require.Equal(t, "0.0042", *logs[0].OpsFee.Amount)
			require.Equal(t, "USD", logs[0].OpsFee.Currency)
			// Incremental refund/correction is authoritative over a late ACK.
			_, err := model.ClaimOpsFeeSync(context.Background(), db, c.Connection, "reader", at.Unix())
			require.NoError(t, err)
			zero := "0"
			rows := []model.OpsFee{{Connection: c.Connection, RequestID: "r1", UserID: "15", Model: "video", Amount: &zero, Currency: "USD", Status: "available", ChargedAt: "2026-11-23T01:00:02Z"}}
			require.NoError(t, model.SaveOpsFeePage(context.Background(), db, c.Connection, "reader", "refund", rows, at.Unix()+2))
			require.NoError(t, AcceptPushReceipts(c.BaseURL, c.KeyID, c.Secret, sent, ack, at.Add(4*time.Second)))
			require.NoError(t, model.AttachOpsFees(context.Background(), logs))
			require.Equal(t, "0", *logs[0].OpsFee.Amount)
			var progress model.OpsFeeSync
			require.NoError(t, db.First(&progress, "connection = ?", c.Connection).Error)
			require.Equal(t, "refund", progress.Cursor, "push ACK cannot advance or rewind feed progress")
		})
	}
}
