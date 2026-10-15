package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
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
	require.NoError(t, db.Migrator().DropTable(AgencyModels()...))
	models := []any{&CNFinancePublication{}, &CNFinanceOutbox{}, &CNFinanceIntent{}, &CNFinanceAssociation{}, &CNFinanceGap{}, &CNFinanceStream{}, &CNWalletLifecycle{}, &OpsExportFact{}, &OpsExportPublisher{}, &AgencyBillingOutbox{}, &AgencyBillingOperation{}, &AgencyBillingJournal{}, &AgencyCommissionLedger{}, &AgencyFundingAllocation{}, &AgencyFundingLot{}, &AgencyTopupFact{}, &TopUp{}, &Task{}, &TaskBillingReconciliation{}, &User{}, &Token{}, &UserSubscription{}, &Model{}, &Vendor{}, &Channel{}, &Ability{}, &Option{}}
	require.NoError(t, db.Migrator().DropTable(models...))
	require.NoError(t, db.Migrator().DropTable(&CNWalletCancellation{}, &CNFinanceReceipt{}, &CNFundingValueAllocation{}, &CNFundingValueLot{}, &CNFundingValueHold{}, &CNSubscriptionLifecycle{}, &CNSubscriptionCancellation{}, &SubscriptionPreConsumeRecord{}, &SubscriptionPlan{}))
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
				ok, err := ReserveCNWallet("concurrent-final", 51, 0, false)
				require.NoError(t, err)
				require.True(t, ok)
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

func TestCNWalletCancellationRecoveryAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			if dialect == "sqlite" {
				initial, err := db.DB()
				require.NoError(t, err)
				require.NoError(t, initial.Close())
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cancellation.db")), &gorm.Config{})
				require.NoError(t, err)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
			previous := DB
			DB = db
			t.Cleanup(func() { DB = previous })
			require.NoError(t, db.Create(&User{Id: 52, Username: "cancel-user", Quota: 1000}).Error)
			require.NoError(t, db.Create(&Token{Id: 52, UserId: 52, Key: "cancel-test-only", RemainQuota: 900, UsedQuota: 100}).Error)
			ok, err := ReserveCNWallet("cancel-recover", 52, 100, true)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = ReserveCNWallet("active-no-terminal-intent", 52, 20, true)
			require.NoError(t, err)
			require.True(t, ok)
			// Upgrade the previous CN schema with live reserved lifecycle data.
			require.NoError(t, db.Migrator().DropTable(&CNWalletCancellation{}, &CNFinanceReceipt{}, &CNFundingValueLot{}, &CNFundingValueAllocation{}))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.Error(t, RequestCNWalletCancellation(db, "missing", 52, 52, 100))
			require.NoError(t, RequestCNWalletCancellation(db, "cancel-recover", 52, 52, 100))
			require.NoError(t, RequestCNWalletCancellation(db, "cancel-recover", 52, 52, 100))
			require.ErrorIs(t, RequestCNWalletCancellation(db, "cancel-recover", 52, 52, 99), ErrCNConflict)
			require.ErrorIs(t, FinalizeCNWallet("cancel-recover", 52, 100, false), ErrCNConflict)
			_, err = ReserveCNWallet("cancel-recover", 52, 200, false)
			require.ErrorIs(t, err, ErrCNConflict)
			for _, phase := range []string{"token", "outbox", "completion"} {
				callback := "s4-cancel-failure"
				inject := func(tx *gorm.DB) {
					if (phase == "token" && tx.Statement.Table == "tokens") || (phase == "outbox" && tx.Statement.Table == "cn_finance_outboxes") {
						tx.AddError(errors.New("injected cancellation failure"))
					}
					if phase == "completion" && tx.Statement.Table == "cn_wallet_cancellations" {
						if values, ok := tx.Statement.Dest.(map[string]any); ok && values["state"] == "complete" {
							tx.AddError(errors.New("injected marker failure"))
						}
					}
				}
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, inject))
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, inject))
				require.Error(t, CompleteCNWalletCancellation(context.Background(), db, "cancel-recover"))
				require.NoError(t, db.Callback().Create().Remove(callback))
				require.NoError(t, db.Callback().Update().Remove(callback))
				var user User
				var token Token
				var lifecycle CNWalletLifecycle
				var intent CNWalletCancellation
				require.NoError(t, db.First(&user, 52).Error)
				assert.Equal(t, 880, user.Quota)
				require.NoError(t, db.First(&token, 52).Error)
				assert.Equal(t, 900, token.RemainQuota)
				assert.Equal(t, 100, token.UsedQuota)
				require.NoError(t, db.Where("request_id = ?", "cancel-recover").First(&lifecycle).Error)
				assert.Equal(t, "reserved", lifecycle.State)
				require.NoError(t, db.Where("request_id = ?", "cancel-recover").First(&intent).Error)
				assert.Equal(t, "pending", intent.State)
				assert.NotEmpty(t, intent.LastErrorCode)
				assert.Positive(t, intent.Attempts)
				var count int64
				require.NoError(t, db.Model(&CNFinanceOutbox{}).Count(&count).Error)
				assert.Zero(t, count)
			}
			// Reconstruct the connection and migration with no BillingSession state.
			require.NoError(t, pool.Close())
			rebuilt, err := gorm.Open(db.Dialector, &gorm.Config{})
			require.NoError(t, err)
			rebuiltPool, err := rebuilt.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = rebuiltPool.Close() })
			require.NoError(t, MigrateCNFinance(rebuilt, "test-cn"))
			require.NoError(t, MigrateCNFinance(rebuilt, "test-cn"))
			require.NoError(t, rebuilt.Model(&CNWalletCancellation{}).Where("request_id = ?", "cancel-recover").Update("next_attempt_at", 0).Error)
			require.NoError(t, RecoverCNWalletCancellations(context.Background(), rebuilt, 100))
			require.NoError(t, RecoverCNWalletCancellations(context.Background(), rebuilt, 100))
			require.NoError(t, CompleteCNWalletCancellation(context.Background(), rebuilt, "cancel-recover"))
			var user User
			var token Token
			var intent CNWalletCancellation
			var active CNWalletLifecycle
			require.NoError(t, rebuilt.First(&user, 52).Error)
			assert.Equal(t, 980, user.Quota)
			require.NoError(t, rebuilt.First(&token, 52).Error)
			assert.Equal(t, 1000, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			require.NoError(t, rebuilt.Where("request_id = ?", "cancel-recover").First(&intent).Error)
			assert.Equal(t, "complete", intent.State)
			assert.Empty(t, intent.LastErrorCode)
			require.NoError(t, rebuilt.Where("request_id = ?", "active-no-terminal-intent").First(&active).Error)
			assert.Equal(t, "reserved", active.State)
			var facts []CNFinanceOutbox
			require.NoError(t, rebuilt.Find(&facts).Error)
			require.Len(t, facts, 1)
			var fact CNFact
			require.NoError(t, common.UnmarshalJsonStr(facts[0].Payload, &fact))
			assert.Equal(t, intent.RequestedAt, fact.OccurredAt)
		})
	}
}

func TestCNNativeFundingTransactionAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "true")
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			connection := agencyDialectDB(t, dialect)
			if connection == nil {
				t.Skip("disposable external database not configured")
			}
			pool, err := connection.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			cnReset(t, connection)
			require.NoError(t, connection.AutoMigrate(&User{}, &Token{}))
			require.NoError(t, MigrateAgency(connection))
			require.NoError(t, connection.Model(&CNFinanceStream{}).Where("id = ?", 1).Update("started_at", time.Now().UTC().Add(-time.Hour).Format(CNTimeLayout)).Error)
			db := connection.Begin()
			require.NoError(t, db.Error)
			t.Cleanup(func() { require.NoError(t, db.Rollback().Error) })
			previous := DB
			DB = db
			t.Cleanup(func() { DB = previous })
			user := User{Username: "s4-native-user", Quota: 100, BillingMode: AgencyDurableBillingMode, FundingVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return RecordAgencyTopup(tx, int64(user.Id), "payment", "s4-native-payment", "payment_callback", 100, 0, &TopupFundingSnapshot{Payment: &TopupPaymentSnapshot{ActualMoney: "80", CurrencyCode: "CNY", PaymentReference: "s4-test-provider-receipt"}})
			}))
			snapshot := &agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, SalesBPS: 10000, SettlementBPS: 10000, CommissionEligible: true, CurrencyCode: "CNY", QuotaPerUnit: "10", ExchangeRate: "7"}
			_, err = TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 60, "", "s4-native-charge", 60, true, snapshot)
			require.NoError(t, err)
			require.NoError(t, BindCNChargeRequest("s4-native-charge", "s4-native-request", user.Id))
			input := agencycontract.BillingEvent{UserID: int64(user.Id), FinancialChargeID: "s4-native-charge", BusinessStatus: "success", StandardQuota: 60, ChargedTotalQuota: 60, CommissionableQuota: 60, SettlementCostQuota: 60, Components: []agencycontract.BillingComponent{{ComponentID: "model", StandardQuota: 60, ChargedTotalQuota: 60, CommissionableQuota: 60, SettlementCostQuota: 60, CommissionEligible: true}}}
			rollback := errors.New("rollback final transaction")
			err = db.Transaction(func(tx *gorm.DB) error {
				_, err := AgencyCommitWalletChargeTx(tx, input, "")
				if err != nil {
					return err
				}
				return rollback
			})
			require.ErrorIs(t, err, rollback)
			var count int64
			require.NoError(t, db.Model(&CNFinanceOutbox{}).Count(&count).Error)
			assert.Zero(t, count, "money transaction rollback must remove staged funding facts")
			_, err = AgencyCommitWalletChargeTx(db, input, "")
			require.NoError(t, err)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			daily, err := ReadCNDaily(context.Background(), db, time.Now().UTC().Format("2006-01-02"), page["publishedThrough"].(string))
			require.NoError(t, err)
			complete := 0
			for _, dependency := range daily["dependencies"].([]CNDependency) {
				if dependency.Status == "complete" {
					complete++
					require.NotNil(t, dependency.CoverageTo)
					assert.Less(t, dependency.CoverageFrom, *dependency.CoverageTo)
				}
			}
			assert.Positive(t, complete, "supported forward paths must be able to prove nonempty coverage")
			values := map[string]string{}
			for _, f := range page["items"].([]CNFact) {
				if f.Cumulative.Money != nil {
					values[f.Kind] = f.Cumulative.Money.Gross
				}
			}
			assert.Equal(t, "80", values["payment"])
			assert.Equal(t, "80", values["funding_lot"])
			assert.Equal(t, "48", values["funding_allocation"], "paid purchase value differs from nominal charge")
			assert.Equal(t, "42", values["charge"])
			refund := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: "s4-native-charge", ComponentID: "model", CumulativeQuota: 20, RefundID: "s4-native-release", Reason: "test-quota-compensation"}
			_, err = AgencyRefundWalletChargeTx(db, refund, "")
			require.NoError(t, err)
			_, err = AgencyRefundWalletChargeTx(db, refund, "")
			require.NoError(t, err)
			page, err = ReadCNFacts(context.Background(), db, page["nextCursor"].(string), 1000)
			require.NoError(t, err)
			for _, f := range page["items"].([]CNFact) {
				assert.NotEqual(t, "payment_refund", f.Kind, "quota release cannot prove cash refund")
				if f.Kind == "funding_allocation" {
					assert.Equal(t, "32", f.Cumulative.Money.Gross)
					assert.Equal(t, "-16", f.Delta.Money.Gross)
					assert.NotNil(t, f.Funding["restoresAllocationFactId"])
				}
			}
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 60, user.Quota)
		})
	}
}

func TestCNFundingResidualAndEventDayAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "true")
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.Migrator().DropTable(AgencyModels()...))
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
			require.NoError(t, MigrateAgency(db))
			previous := DB
			DB = db
			t.Cleanup(func() { DB = previous })
			user := User{Username: "residual-user", Quota: 6, BillingMode: AgencyDurableBillingMode, FundingVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return RecordAgencyTopup(tx, int64(user.Id), "payment", "residual-payment", "payment_callback", 6, 0, &TopupFundingSnapshot{Payment: &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "residual-provider-receipt"}})
			}))
			// Seed committed receipt dates through the persistence boundary. The
			// legacy event deliberately keeps the original business occurrence.
			business := time.Date(2026, 10, 14, 23, 59, 0, 0, time.UTC)
			commitAt := business
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("s4-receipt-clock", func(tx *gorm.DB) {
				if receipt, ok := tx.Statement.Dest.(*CNFinanceReceipt); ok {
					receipt.OccurredAt = commitAt.Format(CNTimeLayout)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Create().Remove("s4-receipt-clock") })
			snapshot := &agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, SalesBPS: 10000, SettlementBPS: 10000, CurrencyCode: "USD", QuotaPerUnit: "6", ExchangeRate: "1"}
			commit := func(index int) error {
				charge := fmt.Sprintf("residual-charge-%d", index)
				if _, err := TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 1, "", charge, 1, true, snapshot); err != nil {
					return err
				}
				if err := BindCNChargeRequest(charge, charge+"-request", user.Id); err != nil {
					return err
				}
				input := agencycontract.BillingEvent{UserID: int64(user.Id), FinancialChargeID: charge, OccurredAtMS: business.UnixMilli(), BusinessStatus: "success", StandardQuota: 1, ChargedTotalQuota: 1, NoncommissionableQuota: 1, Components: []agencycontract.BillingComponent{{ComponentID: "model", StandardQuota: 1, ChargedTotalQuota: 1, NoncommissionableQuota: 1}}}
				return db.Transaction(func(tx *gorm.DB) error { _, err := AgencyCommitWalletChargeTx(tx, input, ""); return err })
			}
			for i := range 5 {
				require.NoError(t, commit(i))
			}
			_, err = TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 1, "", "residual-charge-5", 1, true, snapshot)
			require.NoError(t, err)
			// Failure after value allocation must roll back the lot and immutable
			// fact together. A retry consumes the original residual exactly once.
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("s4-value-rollback", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected value outbox failure"))
				}
			}))
			require.Error(t, commit(5))
			require.NoError(t, db.Callback().Create().Remove("s4-value-rollback"))
			var balance CNFundingValueLot
			require.NoError(t, db.First(&balance).Error)
			assert.Equal(t, "0.166666666666666665", balance.RemainingValue)
			assert.EqualValues(t, 1, balance.RemainingQuota)
			require.NoError(t, commit(5))
			require.NoError(t, db.First(&balance).Error)
			assert.Equal(t, "0", balance.RemainingValue)
			assert.Zero(t, balance.RemainingQuota)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			allocations := map[string]CNFact{}
			sum := decimal.Zero
			for _, fact := range page["items"].([]CNFact) {
				if fact.Kind == "funding_allocation" {
					allocations[fact.EconomicID] = fact
					value, err := cnDecimal(fact.Cumulative.Money.Gross)
					require.NoError(t, err)
					sum = sum.Add(value)
				}
			}
			require.Len(t, allocations, 6)
			assert.Equal(t, "1", sum.String())
			// Upgrade from immutable published heads without repricing history.
			require.NoError(t, db.Migrator().DropTable(&CNFundingValueAllocation{}, &CNFundingValueLot{}))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			commitAt = business.Add(2 * time.Minute)
			refund := AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: "residual-charge-5", ComponentID: "model", CumulativeQuota: 1, RefundID: "residual-release", Reason: "cross-day-release"}
			_, err = AgencyRefundWalletChargeTx(db, refund, "")
			require.NoError(t, err)
			require.NoError(t, db.First(&balance).Error)
			assert.Equal(t, "0.166666666666666665", balance.RemainingValue)
			require.NoError(t, commit(6))
			require.NoError(t, db.First(&balance).Error)
			assert.Equal(t, "0", balance.RemainingValue)
			changes, err := ReadCNFacts(context.Background(), db, page["nextCursor"].(string), 1000)
			require.NoError(t, err)
			for _, fact := range changes["items"].([]CNFact) {
				if fact.Kind == "charge" || fact.Kind == "funding_allocation" {
					assert.Equal(t, "2026-10-15", fact.OccurredAt[:10])
					assert.Equal(t, "2026-10-14", fact.EffectiveAt[:10])
					assert.Equal(t, "2026-10", fact.SourcePeriod)
				}
				if fact.Kind == "funding_allocation" && fact.PreviousFactID != nil {
					assert.Equal(t, "-0.166666666666666665", fact.Delta.Money.Gross)
				}
			}
			day14, err := ReadCNDaily(context.Background(), db, "2026-10-14", changes["publishedThrough"].(string))
			require.NoError(t, err)
			day15, err := ReadCNDaily(context.Background(), db, "2026-10-15", changes["publishedThrough"].(string))
			require.NoError(t, err)
			assert.Equal(t, "6", day14["economicNetControls"].(map[string]any)["byKind"].(map[string]*cnEconomicControl)["charge"].DeltaQuota)
			assert.Equal(t, "0", day15["economicNetControls"].(map[string]any)["byKind"].(map[string]*cnEconomicControl)["charge"].DeltaQuota)
			before, err := common.Marshal(changes["items"])
			require.NoError(t, err)
			_, err = AgencyRefundWalletChargeTx(db, refund, "")
			require.NoError(t, err)
			replay, err := ReadCNFacts(context.Background(), db, page["nextCursor"].(string), 1000)
			require.NoError(t, err)
			after, err := common.Marshal(replay["items"])
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after))
			// Release two independent frozen allocations and race their reuse.
			for i := range 2 {
				_, err = AgencyRefundWalletChargeTx(db, AgencyComponentRefundInput{UserID: int64(user.Id), ChargeID: fmt.Sprintf("residual-charge-%d", i), ComponentID: "model", CumulativeQuota: 1, RefundID: fmt.Sprintf("release-%d", i), Reason: "reuse"}, "")
				require.NoError(t, err)
			}
			if dialect == "sqlite" {
				pool.SetMaxOpenConns(1)
			}
			{
				var wg sync.WaitGroup
				errs := make(chan error, 2)
				for i := 7; i <= 8; i++ {
					wg.Go(func() { errs <- commit(i) })
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
			}
			require.NoError(t, db.First(&balance).Error)
			assert.Equal(t, "0", balance.RemainingValue)
			assert.Zero(t, balance.RemainingQuota)
			// Repeated migration preserves exact values and published history.
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			// Partial restoration returns the frozen share, then the next final
			// allocation receives exactly that remainder rather than a new ratio.
			partialLot := "partial-residual-lot"
			for _, step := range []struct {
				id       string
				quota    int64
				expected string
			}{{"a", 3, "0.5"}, {"b", 3, "0.5"}, {"a", 2, "0.333333333333333333"}, {"c", 1, "0.166666666666666667"}} {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					value, err := cnFundingValueTx(tx, partialLot, "partial-"+step.id, 6, step.quota, decimal.NewFromInt(1))
					if err == nil {
						assert.Equal(t, step.expected, value.String())
					}
					return err
				}))
			}
		})
	}
}

func TestCNLateCorrectionAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "false")
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.Migrator().DropTable(AgencyModels()...))
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
			require.NoError(t, MigrateAgency(db))
			previous := DB
			DB = db
			t.Cleanup(func() { DB = previous })
			user := User{Username: "late-correction", Quota: 100, BillingMode: AgencyDurableBillingMode, FundingVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return RecordAgencyTopup(tx, int64(user.Id), "payment", "late-correction-payment", "payment_callback", 100, 0, &TopupFundingSnapshot{Payment: &TopupPaymentSnapshot{ActualMoney: "100", CurrencyCode: "CNY", PaymentReference: "correction-receipt"}})
			}))
			business := time.Date(2026, 10, 14, 23, 58, 0, 0, time.UTC)
			commitAt := business
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("s4-correction-clock", func(tx *gorm.DB) {
				if receipt, ok := tx.Statement.Dest.(*CNFinanceReceipt); ok {
					receipt.OccurredAt = commitAt.Format(CNTimeLayout)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Create().Remove("s4-correction-clock") })
			snapshot := &agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, SalesBPS: 10000, SettlementBPS: 10000, CurrencyCode: "CNY", QuotaPerUnit: "1", ExchangeRate: "1"}
			_, err = TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, 0, 60, "", "late-charge", 60, true, snapshot)
			require.NoError(t, err)
			require.NoError(t, db.Model(&AgencyBillingJournal{}).Where("charge_id = ?", "late-charge").Update("created_at_ms", business.UnixMilli()).Error)
			require.NoError(t, BindCNChargeRequest("late-charge", "late-request", user.Id))
			_, err = AgencyCommitWalletChargeTx(db, agencycontract.BillingEvent{UserID: int64(user.Id), FinancialChargeID: "late-charge", OccurredAtMS: business.UnixMilli(), BusinessStatus: "success", StandardQuota: 60, ChargedTotalQuota: 60, NoncommissionableQuota: 60}, "")
			require.NoError(t, err)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			commitAt = business.Add(3 * time.Minute)
			require.NoError(t, AdjustAgencyCharge(user.Id, -20, "late-charge", 0))
			correction, err := CorrectAgencyTaskCharge("late-charge", 42, "cross UTC day")
			require.NoError(t, err)
			changes, err := ReadCNFacts(context.Background(), db, page["nextCursor"].(string), 1000)
			require.NoError(t, err)
			seen := map[string]bool{}
			for _, fact := range changes["items"].([]CNFact) {
				if fact.Kind != "charge" && fact.Kind != "funding_allocation" {
					continue
				}
				seen[fact.Kind] = true
				assert.Equal(t, "2026-10-15", fact.OccurredAt[:10])
				assert.Equal(t, "2026-10-14", fact.EffectiveAt[:10])
				assert.Equal(t, "2026-10", fact.SourcePeriod)
				assert.Equal(t, "-20", *fact.Delta.Quota)
			}
			require.True(t, seen["charge"])
			require.True(t, seen["funding_allocation"])
			for _, day := range []struct{ date, quota string }{{"2026-10-14", "60"}, {"2026-10-15", "-20"}} {
				daily, err := ReadCNDaily(context.Background(), db, day.date, changes["publishedThrough"].(string))
				require.NoError(t, err)
				controls := daily["economicNetControls"].(map[string]any)["byKind"].(map[string]*cnEconomicControl)
				assert.Equal(t, day.quota, controls["charge"].DeltaQuota)
				assert.Equal(t, day.quota, controls["funding_allocation"].DeltaQuota)
			}
			var legacy AgencyBillingOutbox
			require.NoError(t, db.Where("event_id = ?", correction.EventID).First(&legacy).Error)
			assert.Equal(t, business.UnixMilli(), legacy.CreatedAtMS)
			replayed, err := CorrectAgencyTaskCharge("late-charge", 42, "cross UTC day")
			require.NoError(t, err)
			assert.True(t, replayed.AlreadyApplied)
			replay, err := ReadCNFacts(context.Background(), db, page["nextCursor"].(string), 1000)
			require.NoError(t, err)
			before, err := common.Marshal(changes["items"])
			require.NoError(t, err)
			after, err := common.Marshal(replay["items"])
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after))
		})
	}
}

func TestCNOpsInventoryAcrossDialects(t *testing.T) {
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
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}))
			require.NoError(t, db.Create(&User{Id: 1, Username: "inventory-user", DisplayName: "User", AffCode: "one", Password: "private-password", Email: "private-email@example.test"}).Error)
			require.NoError(t, db.Create(&User{Id: 2, Username: "deleted-user", DisplayName: "Deleted", AffCode: "two"}).Error)
			require.NoError(t, db.Delete(&User{}, 2).Error)
			require.NoError(t, db.Create(&Token{Id: 1, UserId: 1, Name: "API", Key: "private-api-key"}).Error)
			require.NoError(t, db.Create(&Token{Id: 2, UserId: 2, Name: "Orphan", Key: "private-deleted-owner"}).Error)
			require.NoError(t, db.Create(&Token{Id: 3, UserId: 1, Name: "Deleted key", Key: "private-deleted-key"}).Error)
			require.NoError(t, db.Delete(&Token{}, 3).Error)
			require.NoError(t, db.Create(&Channel{Id: 1, Name: "Route", Key: "private-channel-key", Type: 1, Status: 2}).Error)
			encoded, err := ReadOpsInventory(context.Background(), db, "inventory-g1")
			require.NoError(t, err)
			for _, secret := range []string{"private-", "Deleted", "Orphan"} {
				assert.NotContains(t, string(encoded), secret)
			}
			var snapshot struct {
				SnapshotID string           `json:"snapshotId"`
				Types      []string         `json:"snapshotTypes"`
				Objects    []map[string]any `json:"objects"`
			}
			require.NoError(t, common.Unmarshal(encoded, &snapshot))
			require.Len(t, snapshot.Objects, 3)
			assert.Equal(t, []string{"user", "api_key", "channel"}, snapshot.Types)
			assert.Equal(t, "disabled", snapshot.Objects[2]["object_status"])
			repeated, err := ReadOpsInventory(context.Background(), db, "inventory-g1")
			require.NoError(t, err)
			assert.Equal(t, string(encoded), string(repeated))
			require.NoError(t, db.Model(&Token{}).Where("id = ?", 1).Update("name", "Renamed").Error)
			changed, err := ReadOpsInventory(context.Background(), db, "inventory-g1")
			require.NoError(t, err)
			assert.NotEqual(t, string(encoded), string(changed))
			channels := make([]Channel, 10000)
			for i := range channels {
				channels[i] = Channel{Id: i + 2, Name: "Capacity", Type: 1}
			}
			require.NoError(t, db.CreateInBatches(&channels, 100).Error)
			_, err = ReadOpsInventory(context.Background(), db, "inventory-g1")
			require.ErrorIs(t, err, ErrOpsInventoryTooLarge)
		})
	}
}

func TestCNFundingValueHoldAcrossDialects(t *testing.T) {
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
			require.NoError(t, db.AutoMigrate(&User{}, &AgencyFundingAccount{}, &AgencyFundingLot{}, &AgencyTopupFact{}))
			user := User{Username: "pool-hold-user", BillingMode: AgencyDurableBillingMode, Quota: 6}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
			lot := AgencyFundingLot{UserID: int64(user.Id), SourceKind: "payment", SourceID: "pool-payment", PaidInitial: 6, PaidAvailable: 6}
			require.NoError(t, db.Create(&lot).Error)
			require.NoError(t, db.Create(&AgencyTopupFact{UserID: int64(user.Id), SourceOperationID: lot.SourceID, PaymentStatus: "success", PaymentReference: "pool-receipt", ActualMoney: "1", CurrencyCode: "USD"}).Error)
			withLocks := func(fn func(*gorm.DB) error) error {
				return db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + 0")).Error; err != nil {
						return err
					}
					var account AgencyFundingAccount
					if err := lockForUpdate(tx).Where("user_id = ?", user.Id).First(&account).Error; err != nil {
						return err
					}
					var native AgencyFundingLot
					if err := lockForUpdate(tx).First(&native, lot.ID).Error; err != nil {
						return err
					}
					return fn(tx)
				})
			}
			lotID := CNIdentity("agency-lot", lot.SourceKind, lot.SourceID, "paid")
			require.Error(t, ReserveCNFundingValueTx(db, lot.ID, "not-a-transaction", 3, "0.5"))
			require.NoError(t, withLocks(func(tx *gorm.DB) error {
				value, err := cnFundingValueTx(tx, lotID, "consumed-first", 6, 1, decimal.NewFromInt(1))
				assert.Equal(t, "0.166666666666666667", value.String())
				return err
			}))
			require.NoError(t, db.Migrator().DropTable(&CNFundingValueHold{}))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, withLocks(func(tx *gorm.DB) error {
				quoted, err := QuoteCNFundingValueTx(tx, lot.ID, 3)
				if err != nil {
					return err
				}
				assert.Equal(t, "0.5", quoted)
				return ReserveCNFundingValueTx(tx, lot.ID, "cash-hold", 3, quoted)
			}))
			require.NoError(t, withLocks(func(tx *gorm.DB) error { return ReserveCNFundingValueTx(tx, lot.ID, "cash-hold", 3, "0.5") }))
			require.ErrorIs(t, withLocks(func(tx *gorm.DB) error { return ReserveCNFundingValueTx(tx, lot.ID, "cash-hold", 3, "0.500000") }), ErrCNConflict)
			require.ErrorIs(t, withLocks(func(tx *gorm.DB) error { return ReserveCNFundingValueTx(tx, lot.ID, "cash-hold", 2, "0.5") }), ErrCNConflict)
			rollback := errors.New("rollback retire")
			require.ErrorIs(t, withLocks(func(tx *gorm.DB) error {
				if err := RetireCNFundingValueTx(tx, "cash-hold"); err != nil {
					return err
				}
				return rollback
			}), rollback)
			var balance CNFundingValueLot
			require.NoError(t, db.Where("lot_id = ?", lotID).First(&balance).Error)
			assert.EqualValues(t, 3, balance.HeldQuota)
			assert.Equal(t, "0", balance.RetiredValue)
			require.NoError(t, withLocks(func(tx *gorm.DB) error { return RetireCNFundingValueTx(tx, "cash-hold") }))
			require.NoError(t, withLocks(func(tx *gorm.DB) error { return RetireCNFundingValueTx(tx, "cash-hold") }))
			require.ErrorIs(t, withLocks(func(tx *gorm.DB) error { return ReleaseCNFundingValueTx(tx, "cash-hold") }), ErrCNConflict)
			if dialect == "sqlite" {
				pool.SetMaxOpenConns(1)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for i := range 2 {
				wg.Go(func() {
					errs <- withLocks(func(tx *gorm.DB) error {
						_, err := cnFundingValueTx(tx, lotID, fmt.Sprintf("after-refund-%d", i), 6, 1, decimal.NewFromInt(1))
						return err
					})
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.NoError(t, db.Where("lot_id = ?", lotID).First(&balance).Error)
			assert.Equal(t, "0", balance.RemainingValue)
			assert.Zero(t, balance.RemainingQuota)
			assert.Equal(t, "0.5", balance.RetiredValue)
			var allocations []CNFundingValueAllocation
			require.NoError(t, db.Where("lot_id = ?", lotID).Find(&allocations).Error)
			sum := decimal.Zero
			for _, allocation := range allocations {
				value, err := cnDecimal(allocation.Value)
				require.NoError(t, err)
				sum = sum.Add(value)
			}
			assert.Equal(t, "0.5", sum.String(), "recognition equals net paid after cash retirement")
			// Restore the allocation values to test hold release and stale quotes.
			for _, allocation := range allocations {
				require.NoError(t, withLocks(func(tx *gorm.DB) error {
					_, err := cnFundingValueTx(tx, lotID, allocation.AllocationID, 6, 0, decimal.NewFromInt(1))
					return err
				}))
			}
			require.NoError(t, withLocks(func(tx *gorm.DB) error {
				quote, err := QuoteCNFundingValueTx(tx, lot.ID, 3)
				if err != nil {
					return err
				}
				assert.Equal(t, "0.5", quote)
				return ReserveCNFundingValueTx(tx, lot.ID, "release-hold", 3, quote)
			}))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
			require.NoError(t, withLocks(func(tx *gorm.DB) error { return ReleaseCNFundingValueTx(tx, "release-hold") }))
			require.NoError(t, withLocks(func(tx *gorm.DB) error { return ReleaseCNFundingValueTx(tx, "release-hold") }))
			require.NoError(t, withLocks(func(tx *gorm.DB) error { return ReserveCNFundingValueTx(tx, lot.ID, "release-hold", 3, "0.5") }))
			require.NoError(t, db.Where("lot_id = ?", lotID).First(&balance).Error)
			assert.Equal(t, "0.5", balance.RemainingValue)
			assert.Zero(t, balance.HeldQuota)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("s4-hold-marker-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_funding_value_holds" {
					tx.AddError(errors.New("injected hold record failure"))
				}
			}))
			require.Error(t, withLocks(func(tx *gorm.DB) error { return ReserveCNFundingValueTx(tx, lot.ID, "rollback-hold", 3, "0.5") }))
			require.NoError(t, db.Callback().Create().Remove("s4-hold-marker-failure"))
			require.NoError(t, db.Where("lot_id = ?", lotID).First(&balance).Error)
			assert.Equal(t, "0.5", balance.RemainingValue)
			assert.Zero(t, balance.HeldQuota)
			// Simultaneous full-pool holds cannot both reserve the same value.
			errs = make(chan error, 2)
			for i := range 2 {
				wg.Go(func() {
					errs <- withLocks(func(tx *gorm.DB) error {
						return ReserveCNFundingValueTx(tx, lot.ID, fmt.Sprintf("competing-%d", i), 3, "0.5")
					})
				})
			}
			wg.Wait()
			close(errs)
			success := 0
			for err := range errs {
				if err == nil {
					success++
				} else {
					require.ErrorIs(t, err, ErrCNConflict)
				}
			}
			assert.Equal(t, 1, success)
			// A final cash quote must carry the pool's exact residual, even when
			// it differs by one attounit from the original purchase proportion.
			lot = AgencyFundingLot{UserID: int64(user.Id), SourceKind: "payment", SourceID: "quote-residual", PaidInitial: 6, PaidAvailable: 6}
			require.NoError(t, db.Create(&lot).Error)
			require.NoError(t, db.Create(&AgencyTopupFact{UserID: int64(user.Id), SourceOperationID: lot.SourceID, PaymentStatus: "success", PaymentReference: "quote-receipt", ActualMoney: "1", CurrencyCode: "USD"}).Error)
			lotID = CNIdentity("agency-lot", lot.SourceKind, lot.SourceID, "paid")
			for i := range 2 {
				require.NoError(t, withLocks(func(tx *gorm.DB) error {
					_, err := cnFundingValueTx(tx, lotID, fmt.Sprintf("quote-consumed-%d", i), 6, 1, decimal.NewFromInt(1))
					return err
				}))
			}
			require.NoError(t, withLocks(func(tx *gorm.DB) error {
				quote, err := QuoteCNFundingValueTx(tx, lot.ID, 4)
				assert.Equal(t, "0.666666666666666666", quote)
				return err
			}))
			require.ErrorIs(t, withLocks(func(tx *gorm.DB) error {
				return ReserveCNFundingValueTx(tx, lot.ID, "stale-quote", 4, "0.666666666666666667")
			}), ErrCNConflict)
			require.NoError(t, withLocks(func(tx *gorm.DB) error {
				return ReserveCNFundingValueTx(tx, lot.ID, "exact-quote", 4, "0.666666666666666666")
			}))
		})
	}
}
