package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCNForwardSourcesAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&AgencyBillingOutbox{}, &AgencyBillingOperation{}, &AgencyCommissionLedger{}, &TopUp{}))
			t.Setenv("OPS_CN_HISTORY_SCAN_ENABLED", "false")
			t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
			boundary := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
			require.NoError(t, db.Model(&CNFinanceStream{}).Where("id = ?", 1).Update("started_at", boundary.Format(CNTimeLayout)).Error)
			opsExportReceipt(t, db, 100, "historical", 1, 500000, "agency.billing_finalized")
			require.NoError(t, db.Model(&AgencyBillingOutbox{}).Where("id = ?", 100).Update("created_at_ms", boundary.Add(-time.Hour).UnixMilli()).Error)
			opsExportReceipt(t, db, 200, "forward", 1, 500000, "agency.billing_finalized")
			for _, key := range []string{"historical", "forward", "late"} {
				require.NoError(t, db.Create(&CNFinanceAssociation{ChargeID: key, RequestID: key + "-request", CustomerKey: "51"}).Error)
			}
			for i, at := range []time.Time{boundary.Add(-time.Hour), boundary.Add(time.Hour)} {
				name := []string{"historical", "forward"}[i]
				require.NoError(t, db.Create(&TopUp{TradeNo: name, Status: common.TopUpStatusSuccess, CompleteTime: at.Unix(), UserId: 51}).Error)
				require.NoError(t, db.Create(&AgencyCommissionLedger{EventID: name, ComponentID: "model", AgencyID: 1, UserID: 51, EntryType: "commission", CurrencyCode: "USD", AmountMicros: 1000000, OccurredAtMS: at.UnixMilli()}).Error)
			}
			var fixture struct {
				Items []CNFact `json:"items"`
			}
			require.NoError(t, common.Unmarshal(cnFixture(t, "finance-v2-facts.json"), &fixture))
			require.NoError(t, StageCNFactTx(db, fixture.Items[0]))
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			facts := page["items"].([]CNFact)
			require.Len(t, facts, 4, "forward charge, payment, commission and live staged fact")
			for _, f := range facts {
				assert.NotEqual(t, "historical", f.OperationID)
				assert.NotEqual(t, "op-historical-1", f.OperationID)
				assert.NotEqual(t, "topup:historical", f.EconomicID)
			}
			daily, err := ReadCNDaily(context.Background(), db, "2026-10-14", "")
			require.NoError(t, err)
			assert.EqualValues(t, 4, daily["rawVersionControls"].(map[string]any)["count"])
			assert.Equal(t, false, daily["backfillComplete"])
			replayed, err := ReadCNFacts(context.Background(), db, page["nextCursor"].(string), 1000)
			require.NoError(t, err)
			assert.Empty(t, replayed["items"])
			// A low-ID source committed later remains eligible.
			opsExportReceipt(t, db, 5, "late", 1, 250000, "agency.billing_finalized")
			late, err := ReadCNFacts(context.Background(), db, replayed["nextCursor"].(string), 1000)
			require.NoError(t, err)
			require.Len(t, late["items"].([]CNFact), 1)
			assert.Equal(t, "late-request", *late["items"].([]CNFact)[0].RequestID)
			t.Setenv("OPS_CN_HISTORY_SCAN_ENABLED", "")
			history, err := ReadCNFacts(context.Background(), db, late["nextCursor"].(string), 1000)
			require.NoError(t, err)
			assert.Len(t, history["items"].([]CNFact), 3)
		})
	}
}
