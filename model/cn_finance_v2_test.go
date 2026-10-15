package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func cnFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cn-contract", name))
	require.NoError(t, err)
	return data
}

func cnReset(t *testing.T, db *gorm.DB) {
	t.Helper()
	db.Logger = db.Logger.LogMode(logger.Silent)
	models := []any{&CNFinancePublication{}, &CNFinanceOutbox{}, &CNFinanceIntent{}, &CNFinanceAssociation{}, &CNFinanceGap{}, &CNFinanceStream{}, &CNWalletLifecycle{}, &OpsExportFact{}, &OpsExportPublisher{}, &AgencyBillingOutbox{}, &AgencyBillingOperation{}, &AgencyBillingJournal{}, &AgencyCommissionLedger{}, &AgencyFundingAllocation{}, &AgencyFundingLot{}, &AgencyTopupFact{}, &TopUp{}, &Task{}, &TaskBillingReconciliation{}, &User{}, &Token{}, &UserSubscription{}, &Model{}, &Vendor{}, &Channel{}, &Ability{}, &Option{}}
	require.NoError(t, db.Migrator().DropTable(models...))
	require.NoError(t, MigrateCNFinance(db, "test-cn"))
}

func TestCNFinanceContractAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			cnReset(t, db)
			var initial CNFinanceStream
			require.NoError(t, db.First(&initial, 1).Error)
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			var repeated CNFinanceStream
			require.NoError(t, db.First(&repeated, 1).Error)
			require.Equal(t, initial, repeated)
			require.Error(t, MigrateCNFinance(db, "wrong-gateway"))
			var fixture struct {
				Items []CNFact `json:"items"`
			}
			require.NoError(t, common.Unmarshal(cnFixture(t, "finance-v2-facts.json"), &fixture))
			require.Len(t, fixture.Items, 2)
			require.NoError(t, StageCNFactTx(db, fixture.Items[1]))
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			require.Empty(t, page["items"])
			assert.EqualValues(t, 1, page["publicationPendingCount"])
			require.NoError(t, StageCNFactTx(db, fixture.Items[0]))
			page, err = ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			facts := page["items"].([]CNFact)
			require.Len(t, facts, 2)
			assert.Equal(t, "fixture-charge-r1", facts[0].FactID)
			assert.Equal(t, "fixture-charge-r4", facts[1].FactID)
			require.NoError(t, StageCNFactTx(db, fixture.Items[0]))
			conflict := fixture.Items[0]
			conflict.OperationID = "conflicting-input"
			assert.ErrorIs(t, StageCNFactTx(db, conflict), ErrCNConflict)
			fork := fixture.Items[1]
			fork.FactID = "fork"
			fork.Revision = 9
			assert.ErrorIs(t, StageCNFactTx(db, fork), ErrCNConflict)
			cycle := fixture.Items[0]
			cycle.FactID = "cycle"
			cycle.PreviousFactID = &cycle.FactID
			assert.ErrorIs(t, StageCNFactTx(db, cycle), ErrCNConflict)
			through := page["publishedThrough"].(string)
			daily, err := ReadCNDaily(context.Background(), db, "2026-10-14", through)
			require.NoError(t, err)
			raw := daily["rawVersionControls"].(map[string]any)["byKind"].(map[string]*cnRawControl)["charge"]
			economic := daily["economicNetControls"].(map[string]any)["byKind"].(map[string]*cnEconomicControl)["charge"]
			assert.Equal(t, "18", raw.CumulativeGross["CNY"])
			assert.Equal(t, "8", economic.DeltaGross["CNY"])
			assert.Equal(t, "400000", economic.DeltaQuota)
			assert.Equal(t, "6.4", economic.RecognizedGross["CNY"])
			firstJSON, err := common.Marshal(daily)
			require.NoError(t, err)
			// A later checkpoint must not change an earlier watermark's controls.
			require.NoError(t, db.Create(&CNFinanceIntent{Key: "late-intent", Payload: "{}", State: "pending_mutation", CreatedAt: 1}).Error)
			resumed, err := ReadCNFacts(context.Background(), db, through, 1000)
			require.NoError(t, err)
			assert.NotEqual(t, through, resumed["nextCursor"])
			require.Empty(t, resumed["items"])
			again, err := ReadCNDaily(context.Background(), db, "2026-10-14", through)
			require.NoError(t, err)
			secondJSON, err := common.Marshal(again)
			require.NoError(t, err)
			assert.Equal(t, string(firstJSON), string(secondJSON))
			var stream CNFinanceStream
			require.NoError(t, db.First(&stream, 1).Error)
			for _, bad := range []string{"djI6Mg", CNCursor("other", 0), CNCursor(stream.Epoch, stream.Sequence+1), "Y25mMjp4OjAx", "===="} {
				_, err := ReadCNFacts(context.Background(), db, bad, 1000)
				assert.ErrorIs(t, err, ErrOpsExportCursor)
			}
			// Native source row 5 commits after source row 100. Neither v1 nor v2 may
			// use a source-row watermark that loses the late source.
			require.NoError(t, db.AutoMigrate(&AgencyBillingOutbox{}, &AgencyBillingOperation{}))
			require.NoError(t, MigrateOpsExport(db))
			opsExportReceipt(t, db, 100, "native-first", 1, 500000, "agency.billing_finalized")
			require.NoError(t, db.Create(&CNFinanceAssociation{ChargeID: "native-first", RequestID: "real-request-first", CustomerKey: "51"}).Error)
			_, v1Cursor, _, err := ReadOpsFacts(context.Background(), db, 0, 100)
			require.NoError(t, err)
			var v1Before []OpsExportFact
			require.NoError(t, db.Order("id").Find(&v1Before).Error)
			page, err = ReadCNFacts(context.Background(), db, through, 1000)
			require.NoError(t, err)
			cursor := page["nextCursor"].(string)
			opsExportReceipt(t, db, 5, "native-late", 1, 250000, "agency.billing_finalized")
			require.NoError(t, db.Create(&CNFinanceAssociation{ChargeID: "native-late", RequestID: "real-request-late", CustomerKey: "51"}).Error)
			page, err = ReadCNFacts(context.Background(), db, cursor, 1000)
			require.NoError(t, err)
			require.Len(t, page["items"].([]CNFact), 1)
			assert.Equal(t, "real-request-late", *page["items"].([]CNFact)[0].RequestID)
			var v1After []OpsExportFact
			require.NoError(t, db.Order("id").Find(&v1After).Error)
			assert.Equal(t, v1Before, v1After)
			v1, _, _, err := ReadOpsFacts(context.Background(), db, v1Cursor, 100)
			require.NoError(t, err)
			require.Len(t, v1, 1)
			// Funding examples use the same explicit predecessor algebra as charges.
			var funding struct {
				Items []CNFact `json:"items"`
			}
			require.NoError(t, common.Unmarshal(cnFixture(t, "funding-v2.json"), &funding))
			for _, f := range funding.Items {
				require.NoError(t, StageCNFactTx(db, f))
			}
			_, err = ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			// More than one publisher batch of missing predecessors must not hide
			// a later root, unrelated good fact, or isolate neither poison source.
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				for i := range 1001 {
					f := fixture.Items[1]
					f.FactID = fmt.Sprintf("waiting-%d", i)
					f.EconomicID = fmt.Sprintf("waiting-economic-%d", i)
					previous := fmt.Sprintf("late-root-%d", i)
					f.PreviousFactID = &previous
					if err := StageCNFactTx(tx, f); err != nil {
						return err
					}
				}
				return nil
			}))
			for _, identity := range []string{"late-root-0", "independent-good", "poison-json"} {
				f := fixture.Items[0]
				f.FactID, f.EconomicID = identity, identity
				if identity == "late-root-0" {
					f.EconomicID = "waiting-economic-0"
				}
				require.NoError(t, StageCNFactTx(db, f))
			}
			require.NoError(t, db.Model(&CNFinanceOutbox{}).Where("fact_id = ?", "poison-json").Update("payload", "broken-json").Error)
			require.NoError(t, db.Create(&AgencyBillingOutbox{ID: 200, EventID: "bad-native", EventKind: "agency.billing_finalized", Payload: "broken-json", PayloadHash: "invalid"}).Error)
			page, err = ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			var published []string
			for _, f := range page["items"].([]CNFact) {
				published = append(published, f.FactID)
			}
			assert.Contains(t, published, "late-root-0")
			assert.Contains(t, published, "waiting-0")
			assert.Contains(t, published, "independent-good")
			assert.NotContains(t, published, "poison-json")
			assert.EqualValues(t, 1001, page["publicationPendingCount"])
			var gap CNFinanceGap
			require.NoError(t, db.Where("id = ?", CNIdentity("source-gap", "agency_wallet", "bad-native")).First(&gap).Error)
			assert.False(t, gap.Resolved)
			cursor = page["publishedThrough"].(string)
			page, err = ReadCNFacts(context.Background(), db, cursor, 1000)
			require.NoError(t, err)
			assert.Equal(t, cursor, page["publishedThrough"], "unchanged checkpoint must not create endless pages")
			cnCheckCatalog(t, db)
		})
	}
}

func TestCNDurableWalletAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
	oldUnit, oldRate := common.QuotaPerUnit, operation_setting.USDExchangeRate
	oldDisplay := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit, operation_setting.USDExchangeRate = 10, 7
	operation_setting.GetGeneralSetting().QuotaDisplayType = "CNY"
	t.Cleanup(func() {
		common.QuotaPerUnit, operation_setting.USDExchangeRate = oldUnit, oldRate
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplay
	})
	oldBatch, oldRedis := common.BatchUpdateEnabled, common.RedisEnabled
	common.BatchUpdateEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() { common.BatchUpdateEnabled = oldBatch; common.RedisEnabled = oldRedis })
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			cnReset(t, db)
			previousDB := DB
			DB = db
			t.Cleanup(func() { DB = previousDB })
			require.NoError(t, db.AutoMigrate(&User{}, &Task{}, &Token{}, &UserSubscription{}))
			require.NoError(t, db.Create(&User{Id: 51, Username: "wallet-test", Quota: 1000}).Error)
			assert.True(t, CNDurableWalletAvailable())
			common.BatchUpdateEnabled = true
			assert.False(t, CNDurableWalletAvailable())
			common.BatchUpdateEnabled = false
			common.RedisEnabled = true
			assert.False(t, CNDurableWalletAvailable())
			common.RedisEnabled = false
			for _, tc := range []struct {
				request    string
				pre, final int
				cancel     bool
			}{{"zero-delta", 100, 100, false}, {"trusted", 0, 60, false}, {"supplement", 20, 50, false}, {"release", 80, 30, false}, {"failure", 40, 0, true}} {
				reserved, err := ReserveCNWallet(tc.request, 51, tc.pre, true)
				require.NoError(t, err)
				require.True(t, reserved)
				require.NoError(t, FinalizeCNWallet(tc.request, 51, tc.final, tc.cancel))
				require.NoError(t, FinalizeCNWallet(tc.request, 51, tc.final, tc.cancel))
			}
			var user User
			require.NoError(t, db.First(&user, 51).Error)
			assert.Equal(t, 760, user.Quota)
			// A reserve rollback restores only its own added amount.
			ok, err := ReserveCNWallet("reserve-rollback", 51, 20, true)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = ReserveCNWallet("reserve-rollback", 51, 40, true)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = ReserveCNWallet("reserve-rollback", 51, 20, false)
			require.NoError(t, err)
			require.True(t, ok)
			require.NoError(t, FinalizeCNWallet("reserve-rollback", 51, 20, false))
			// A rolled-back money transaction cannot leave either a debit or a fact.
			beforeCount := int64(0)
			require.NoError(t, db.Model(&CNFinanceOutbox{}).Count(&beforeCount).Error)
			ok, err = ReserveCNWallet("insufficient", 51, 10000, true)
			require.NoError(t, err)
			require.False(t, ok)
			var absent int64
			require.NoError(t, db.Model(&CNWalletLifecycle{}).Where("request_id = ?", "insufficient").Count(&absent).Error)
			assert.Zero(t, absent)
			var outboxCount int64
			require.NoError(t, db.Model(&CNFinanceOutbox{}).Count(&outboxCount).Error)
			assert.Equal(t, beforeCount, outboxCount)
			// A new process has no in-memory flags; the persisted lifecycle deduplicates.
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, FinalizeCNWallet("zero-delta", 51, 100, false))
			assert.ErrorIs(t, FinalizeCNWallet("zero-delta", 51, 101, false), ErrCNConflict)
			if dialect != "sqlite" {
				var wg sync.WaitGroup
				errs := make(chan error, 2)
				for range 2 {
					wg.Go(func() { errs <- FinalizeCNWallet("concurrent-final", 51, 10, false) })
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
			}
			task := Task{TaskID: "actual-task", UserId: 51, Quota: 100, Status: TaskStatusSuccess, PrivateData: TaskPrivateData{BillingSource: "wallet", Execution: &TaskExecutionSnapshot{RequestID: "zero-delta"}}}
			require.NoError(t, db.Create(&task).Error)
			common.QuotaPerUnit, operation_setting.USDExchangeRate = 20, 9
			result, err := ApplyTaskQuotaAdjustment(&task, 80)
			require.NoError(t, err)
			assert.Equal(t, -20, result.QuotaDelta)
			result, err = ApplyTaskQuotaAdjustment(&task, 80)
			require.NoError(t, err)
			assert.Zero(t, result.QuotaDelta)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			found := 0
			for _, f := range page["items"].([]CNFact) {
				if f.EconomicID == "request:zero-delta" {
					found++
					if f.Revision == 2 {
						assert.Equal(t, "-14", f.Delta.Money.Gross)
						assert.Equal(t, "-2", f.Delta.Money.OriginalAmount)
						assert.NotNil(t, f.PreviousFactID)
					}
				}
			}
			assert.Equal(t, 2, found)
			common.QuotaPerUnit, operation_setting.USDExchangeRate = 10, 7
			// Non-atomic legacy acknowledgments remain evidence gaps even without batch.
			key, err := BeginCNLegacyIntent("legacy", 51, "wallet", 0, 10)
			require.NoError(t, err)
			require.NoError(t, FinishCNLegacyIntent(key, true))
			var intent CNFinanceIntent
			require.NoError(t, db.Where(map[string]any{"key": key}).First(&intent).Error)
			assert.Equal(t, "acknowledged_unverified", intent.State)
		})
	}
	for _, tc := range []struct{ numerator, denominator, want string }{{"1", "2000000000000000000", "0"}, {"3", "2000000000000000000", "0.000000000000000002"}, {"-3", "2000000000000000000", "-0.000000000000000002"}} {
		a := decimal.RequireFromString(tc.numerator)
		b := decimal.RequireFromString(tc.denominator)
		assert.Equal(t, tc.want, cnDivideHalfEven(a, b).String())
	}
}

func cnCheckCatalog(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousKey := commonKeyCol
	t.Cleanup(func() { commonKeyCol = previousKey })
	commonKeyCol = "`key`"
	if db.Dialector.Name() == "postgres" {
		commonKeyCol = `"key"`
	}
	require.NoError(t, db.AutoMigrate(&Model{}, &Vendor{}, &Channel{}, &Ability{}, &Option{}))
	require.NoError(t, db.Create(&Model{ModelName: "hidden-model", Endpoints: `["openai"]`}).Error)
	require.NoError(t, db.Model(&Model{}).Where("model_name = ?", "hidden-model").Update("status", 0).Error)
	require.NoError(t, db.Create(&Channel{Id: 1, Key: "must-never-export-this-secret", Models: "disabled-model", Status: 2}).Error)
	require.NoError(t, db.Create(&Option{Key: "ModelPrice", Value: `{"price-only-model":2}`}).Error)
	read := func() map[string]any {
		body, err := ReadCNCatalog(context.Background(), db, "test-cn")
		require.NoError(t, err)
		assert.NotContains(t, string(body), "must-never-export-this-secret")
		var result map[string]any
		require.NoError(t, common.Unmarshal(body, &result))
		return result
	}
	first := read()
	found := map[string]map[string]any{}
	for _, item := range first["models"].([]any) {
		row := item.(map[string]any)
		found[row["model_name"].(string)] = row
	}
	require.Contains(t, found, "hidden-model")
	assert.Equal(t, "hidden", found["hidden-model"]["square_state"])
	assert.Contains(t, found["hidden-model"]["supported_endpoints"], "openai")
	assert.Equal(t, "unavailable", found["disabled-model"]["square_state"])
	assert.Equal(t, false, found["price-only-model"]["has_metadata"])
	require.NoError(t, db.Model(&Option{}).Where(commonKeyCol+" = ?", "ModelPrice").Update("value", `{"price-only-model":3}`).Error)
	second := read()
	assert.Equal(t, first["snapshotVersion"], second["snapshotVersion"])
	assert.NotEqual(t, first["pricing"], second["pricing"])
}

func TestCNFixtureInventory(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "cn-contract", "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 9)
	for _, file := range files {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		var body any
		require.NoError(t, common.Unmarshal(data, &body), fmt.Sprintf("fixture %s", file))
	}
	assert.False(t, CNRefundCapabilities("g")["executionAvailable"].(bool))
}
