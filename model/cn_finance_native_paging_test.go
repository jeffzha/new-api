package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNFundingValueHistoryIsPagedAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.Migrator().DropTable(&CNFundingValueAllocation{}, &CNFundingValueLot{}, &CNFinanceOutbox{}))
			require.NoError(t, db.AutoMigrate(&CNFundingValueAllocation{}, &CNFundingValueLot{}, &CNFinanceOutbox{}))
			for i := range 600 {
				fact := CNFact{Kind: "unrelated", Links: map[string][]string{"padding": {strings.Repeat("x", 4096)}}}
				if i == 5 || i == 260 || i == 550 {
					fact.Kind = "funding_allocation"
					fact.Funding = map[string]any{"lotId": "lot-test", "allocationId": "a"}
					q := "20"
					fact.Cumulative = CNMetric{Quota: &q, Money: &CNMoney{Gross: "2"}}
					if i == 5 {
						q = "40"
						fact.Cumulative.Money.Gross = "4"
					}
					if i == 550 {
						fact.Cumulative = CNMetric{}
					}
				}
				payload, err := common.Marshal(fact)
				require.NoError(t, err)
				revision := int64(i + 1)
				if i == 550 {
					revision = 1
				} // Later ID must not replace a higher revision.
				require.NoError(t, db.Create(&CNFinanceOutbox{FactID: fmt.Sprint(i), ChainKey: fmt.Sprint(i), Revision: revision, ForkKey: fmt.Sprint(i), Payload: string(payload), PayloadHash: "fixture", State: "pending"}).Error)
			}
			pages := 0
			require.NoError(t, db.Callback().Query().After("gorm:query").Register("verify_bounded_history", func(tx *gorm.DB) {
				if tx.Statement.Table != "cn_finance_outboxes" {
					return
				}
				limit, ok := tx.Statement.Clauses["LIMIT"]
				require.True(t, ok, "history read must have a bound")
				require.NotNil(t, limit.Expression)
				require.LessOrEqual(t, tx.RowsAffected, int64(256))
				pages++
			}))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				lot, err := cnFundingValueLotTx(tx, "lot-test", 100, decimal.NewFromInt(10))
				if err == nil {
					require.Equal(t, int64(80), lot.RemainingQuota)
					require.Equal(t, "8", lot.RemainingValue)
				}
				return err
			}))
			require.Equal(t, 3, pages)
			var allocation CNFundingValueAllocation
			require.NoError(t, db.First(&allocation).Error)
			require.Equal(t, int64(20), allocation.Quota)
			require.Equal(t, "2", allocation.Value)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				_, err := cnFundingValueLotTx(tx, "lot-test", 100, decimal.NewFromInt(10))
				return err
			}))
			require.Equal(t, 3, pages, "repeated initialization must not rescan history")
		})
	}
}
