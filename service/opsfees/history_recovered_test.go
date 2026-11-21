package opsfees

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestRecoveredHistoryFillsUnconfirmedWithoutReplacingVerifiedResults(t *testing.T) {
	old := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(old) })
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := testDB(t, dialect)
			require.NoError(t, model.MigrateOpsFees(db))
			ctx := context.Background()
			amount := "0.25"
			for _, id := range []string{"missing-fee", "live-verified", "wrong-user", "wrong-currency", "newer-wait"} {
				r := model.OpsFee{Connection: "current", RequestID: id, UserID: "15", Model: "sample", Currency: "USD", Status: "unconfirmed", FetchedAt: 10}
				switch id {
				case "live-verified":
					r.Amount, r.Status = &amount, "available"
				case "wrong-user":
					r.UserID = "99"
				case "wrong-currency":
					r.Currency = "CNY"
				case "newer-wait":
					r.FetchedAt = 100
				}
				require.NoError(t, db.Create(&r).Error)
			}
			recovered := "0.12"
			var rows []model.OpsFee
			for _, id := range []string{"missing-fee", "live-verified", "wrong-user", "wrong-currency", "newer-wait", "new-row"} {
				rows = append(rows, model.OpsFee{RequestID: id, UserID: "15", Model: "sample", Currency: "USD", Status: "available", Amount: &recovered, FetchedAt: 20})
			}
			for round := range 2 {
				progress := "history-" + string(rune('a'+round))
				_, err := model.ClaimOpsFeeSync(ctx, db, progress, "owner", 20)
				require.NoError(t, err)
				require.NoError(t, model.SaveOpsFeeHistoryPage(ctx, db, progress, "current", "owner", "done", rows, 20, 30, true))
			}
			for _, id := range []string{"missing-fee", "new-row", "live-verified", "wrong-user", "wrong-currency", "newer-wait"} {
				var got model.OpsFee
				require.NoError(t, db.First(&got, "connection = ? AND request_id = ?", "current", id).Error)
				switch id {
				case "missing-fee", "new-row":
					require.Equal(t, "available", got.Status)
					require.Equal(t, recovered, *got.Amount)
				case "live-verified":
					require.Equal(t, amount, *got.Amount)
				default:
					require.Equal(t, "unconfirmed", got.Status)
					require.Nil(t, got.Amount)
				}
			}
		})
	}
}
