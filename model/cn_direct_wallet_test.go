package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNDirectWalletAtomicAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_DIRECT_WALLET_ENABLED", "true")
	oldRedis, oldBatch, oldUnit := common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit
	oldSettings := *operation_setting.GetGeneralSetting()
	common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = false, false, 100
	operation_setting.GetGeneralSetting().QuotaDisplayType = "USD"
	t.Cleanup(func() {
		common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = oldRedis, oldBatch, oldUnit
		*operation_setting.GetGeneralSetting() = oldSettings
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external DB not configured")
			}
			if dialect == "sqlite" {
				pool, err := db.DB()
				require.NoError(t, err)
				require.NoError(t, pool.Close())
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "direct.db")), &gorm.Config{})
				require.NoError(t, err)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			oldDB := DB
			DB = db
			t.Cleanup(func() { DB = oldDB })
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}))
			id := "d" + common.GetUUID()[:15]
			u := User{Username: id, AffCode: id, Status: common.UserStatusEnabled, Quota: 10000}
			require.NoError(t, db.Create(&u).Error)
			token := Token{UserId: u.Id, Key: id, Status: common.TokenStatusEnabled, RemainQuota: 10000}
			require.NoError(t, db.Create(&token).Error)
			channel := Channel{Name: id}
			require.NoError(t, db.Create(&channel).Error)
			check := func(wallet, tokenUsed, userUsed, requests int) {
				t.Helper()
				var actual User
				var credential Token
				var source Channel
				require.NoError(t, db.First(&actual, u.Id).Error)
				require.NoError(t, db.First(&credential, token.Id).Error)
				require.NoError(t, db.First(&source, channel.Id).Error)
				require.Equal(t, 10000-wallet, actual.Quota)
				require.Equal(t, 10000-tokenUsed, credential.RemainQuota)
				require.Equal(t, tokenUsed, credential.UsedQuota)
				require.Equal(t, userUsed, actual.UsedQuota)
				require.Equal(t, requests, actual.RequestCount)
				require.EqualValues(t, userUsed, source.UsedQuota)
			}
			// Upgrade an S4 schema while retaining a live wallet receipt and balances.
			require.NoError(t, db.Create(&CNWalletLifecycle{RequestID: id + "legacy", UserID: u.Id, Reserved: 7, State: "reserved", Version: 1, CreatedAt: time.Now().UTC().Format(CNTimeLayout)}).Error)
			require.NoError(t, db.Migrator().DropTable(&CNDirectWalletSegment{}, &CNDirectWallet{}))
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			var legacy CNWalletLifecycle
			require.NoError(t, db.Where("request_id = ?", id+"legacy").First(&legacy).Error)
			require.EqualValues(t, 7, legacy.Reserved)
			require.ErrorIs(t, BeginCNDirectWallet(id+"legacy", u.Id, token.Id, channel.Id, 7), ErrCNConflict)
			check(0, 0, 0, 0)
			common.RedisEnabled = true
			require.Error(t, BeginCNDirectWallet(id, u.Id, token.Id, channel.Id, 10))
			common.RedisEnabled = false
			common.BatchUpdateEnabled = true
			require.Error(t, BeginCNDirectWallet(id, u.Id, token.Id, channel.Id, 10))
			common.BatchUpdateEnabled = false
			require.Error(t, BeginCNDirectWallet("", u.Id, token.Id, channel.Id, 10))
			oldRequest := id + "old-direct"
			require.NoError(t, BeginCNDirectWallet(oldRequest, u.Id, token.Id, channel.Id, 30))
			for _, column := range []string{"token_unlimited", "token_binding_version", "token_adjustment_status", "token_disposed_quota"} {
				require.NoError(t, db.Migrator().DropColumn(&CNDirectWallet{}, column))
			}
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			require.ErrorIs(t, BeginCNDirectWallet(oldRequest, u.Id, token.Id, channel.Id, 30), ErrCNConflict)
			_, err = ApplyCNDirectWalletSegment(oldRequest, u.Id, 1, `{"total_tokens":1}`, 1)
			require.ErrorIs(t, err, ErrCNWalletBindingRequired)
			oldDirect, err := ReadCNDirectWallet(oldRequest, u.Id)
			require.NoError(t, err)
			require.Zero(t, oldDirect.TokenBindingVersion)
			require.NoError(t, RequestCNDirectWalletCancellation(oldRequest, u.Id))
			require.NoError(t, RecoverCNDirectWalletCancellations(context.Background(), db, 100))
			check(0, 0, 0, 0)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("direct-reserve-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_direct_wallets" {
					tx.AddError(errors.New("injected reserve receipt failure"))
				}
			}))
			require.Error(t, BeginCNDirectWallet(id, u.Id, token.Id, channel.Id, 10))
			check(0, 0, 0, 0)
			require.NoError(t, db.Callback().Create().Remove("direct-reserve-fail"))
			require.NoError(t, BeginCNDirectWallet(id, u.Id, token.Id, channel.Id, 10))
			require.NoError(t, BeginCNDirectWallet(id, u.Id, token.Id, channel.Id, 10))
			check(10, 10, 0, 0)
			require.ErrorIs(t, BeginCNDirectWallet(id, u.Id, token.Id, channel.Id, 11), ErrCNConflict)
			first, err := ApplyCNDirectWalletSegment(id, u.Id, 1, `{"total_tokens":20}`, 20)
			require.NoError(t, err)
			require.EqualValues(t, 30, first.Target)
			common.QuotaPerUnit = 9999 // Existing conversion must remain the original 100.
			replay, err := ApplyCNDirectWalletSegment(id, u.Id, 1, first.UsageJSON, 20)
			require.NoError(t, err)
			require.Equal(t, first, replay)
			_, err = ApplyCNDirectWalletSegment(id, u.Id, 1, first.UsageJSON, 21)
			require.ErrorIs(t, err, ErrCNConflict)
			_, err = ApplyCNDirectWalletSegment(id, u.Id, 1, `{"total_tokens":25}`, 5)
			require.ErrorIs(t, err, ErrCNConflict)
			check(30, 30, 0, 0)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("direct-segment-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_direct_wallet_segments" {
					tx.AddError(errors.New("injected segment receipt failure"))
				}
			}))
			_, err = ApplyCNDirectWalletSegment(id, u.Id, 2, `{"total_tokens":25}`, 5)
			require.Error(t, err)
			check(30, 30, 0, 0)
			require.NoError(t, db.Callback().Create().Remove("direct-segment-fail"))
			// A reconstructed DB handle has no in-memory quota baseline.
			DB = db.Session(&gorm.Session{NewDB: true})
			second, err := ApplyCNDirectWalletSegment(id, u.Id, 2, `{"total_tokens":25}`, 5)
			require.NoError(t, err)
			require.EqualValues(t, 35, second.Target)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("direct-final-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected final outbox failure"))
				}
			}))
			require.Error(t, FinalizeCNDirectWallet(id, u.Id, 25))
			check(35, 35, 0, 0)
			row, err := ReadCNDirectWallet(id, u.Id)
			require.NoError(t, err)
			require.Equal(t, "reserved", row.State)
			require.NoError(t, db.Callback().Create().Remove("direct-final-fail"))
			require.NoError(t, FinalizeCNDirectWallet(id, u.Id, 25))
			require.NoError(t, FinalizeCNDirectWallet(id, u.Id, 25))
			require.ErrorIs(t, FinalizeCNDirectWallet(id, u.Id, 26), ErrCNConflict)
			check(25, 25, 25, 1)
			// Publication is actual canonical output, and replay keeps its cursor/fact.
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			var fact CNFact
			for _, f := range page["items"].([]CNFact) {
				if f.RequestID != nil && *f.RequestID == id {
					fact = f
				}
			}
			require.NotEmpty(t, fact.FactID)
			require.Equal(t, "25", *fact.Cumulative.Quota)
			require.Equal(t, "0.25", fact.Cumulative.Money.Gross)
			require.Equal(t, "100", fact.Conversion["quotaPerUnit"])
			if output := os.Getenv("S4R_TEST_ARTIFACT_DIR"); output != "" {
				raw, err := common.Marshal(fact)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(output, "direct-wallet-canonical-"+dialect+".json"), raw, 0600))
			}
			through := page["nextCursor"].(string)
			again, err := ReadCNFacts(context.Background(), db, through, 1000)
			require.NoError(t, err)
			require.Empty(t, again["items"])
			require.NoError(t, FinalizeCNDirectWallet(id, u.Id, 25))
			check(25, 25, 25, 1)
			common.QuotaPerUnit = 100
			// Concurrent replay has one native debit and a permanent original receipt.
			parallel := id + "parallel"
			require.NoError(t, BeginCNDirectWallet(parallel, u.Id, token.Id, channel.Id, 0))
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for range 2 {
				wg.Go(func() {
					_, err := ApplyCNDirectWalletSegment(parallel, u.Id, 1, `{"total_tokens":10}`, 10)
					results <- err
				})
			}
			wg.Wait()
			close(results)
			var successes int
			for err := range results {
				if err == nil {
					successes++
				}
			}
			require.Positive(t, successes)
			_, err = ApplyCNDirectWalletSegment(parallel, u.Id, 1, `{"total_tokens":10}`, 10)
			require.NoError(t, err)
			require.NoError(t, RequestCNDirectWalletCancellation(parallel, u.Id))
			require.NoError(t, RecoverCNDirectWalletCancellations(context.Background(), db, 100))
			check(35, 35, 35, 2)
			// Cancel decision survives a failed transaction and recovery; successful
			// segments remain charged while only the initial reservation is released.
			cancel := id + "cancel"
			require.NoError(t, BeginCNDirectWallet(cancel, u.Id, token.Id, channel.Id, 40))
			_, err = ApplyCNDirectWalletSegment(cancel, u.Id, 1, `{"total_tokens":7}`, 7)
			require.NoError(t, err)
			require.NoError(t, MarkCNDirectWalletUsageGap(cancel, u.Id))
			require.NoError(t, RequestCNDirectWalletCancellation(cancel, u.Id))
			require.ErrorIs(t, FinalizeCNDirectWallet(cancel, u.Id, 7), ErrCNConflict)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("direct-cancel-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected cancel outbox failure"))
				}
			}))
			require.Error(t, RecoverCNDirectWalletCancellations(context.Background(), db, 100))
			check(82, 82, 35, 2)
			require.NoError(t, db.Callback().Create().Remove("direct-cancel-fail"))
			require.NoError(t, RecoverCNDirectWalletCancellations(context.Background(), db.Session(&gorm.Session{NewDB: true}), 100))
			require.NoError(t, RecoverCNDirectWalletCancellations(context.Background(), db, 100))
			check(42, 42, 42, 3)
			row, err = ReadCNDirectWallet(cancel, u.Id)
			require.NoError(t, err)
			require.Equal(t, "cancelled", row.State)
			require.NotEmpty(t, row.CoverageGap)
			// Missing conversion remains unknown even after valid settings return.
			operation_setting.GetGeneralSetting().QuotaDisplayType = "TOKENS"
			unknown := id + "unknown"
			require.NoError(t, BeginCNDirectWallet(unknown, u.Id, token.Id, channel.Id, 0))
			operation_setting.GetGeneralSetting().QuotaDisplayType = "USD"
			require.NoError(t, FinalizeCNDirectWallet(unknown, u.Id, 0))
			_, err = ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			var stream CNFinanceStream
			require.NoError(t, db.First(&stream, 1).Error)
			scan, err := cnDirectWalletProof(db, &stream, map[string]bool{})
			require.NoError(t, err)
			require.Positive(t, scan.dependency.UnknownCount)
			require.Positive(t, scan.dependency.PendingCount)
			var kept int64
			require.NoError(t, db.Model(&CNDirectWalletSegment{}).Where("request_id = ?", id).Count(&kept).Error)
			require.EqualValues(t, 2, kept)
			for _, deleted := range []string{"soft", "hard"} {
				request := id + deleted
				account := User{Username: request, AffCode: request, Quota: 10000, Status: common.UserStatusEnabled}
				require.NoError(t, db.Create(&account).Error)
				credential := Token{UserId: account.Id, Key: request, Status: common.TokenStatusEnabled, RemainQuota: 1000}
				require.NoError(t, db.Create(&credential).Error)
				require.NoError(t, BeginCNDirectWallet(request, account.Id, credential.Id, channel.Id, 40))
				_, err = ApplyCNDirectWalletSegment(request, account.Id, 1, `{"total_tokens":7}`, 7)
				require.NoError(t, err)
				query := db
				status := "deleted_token_not_recredited"
				if deleted == "hard" {
					query = db.Unscoped()
					status = "missing_token_not_recredited"
				}
				require.NoError(t, query.Delete(&Token{}, credential.Id).Error)
				_, err = ApplyCNDirectWalletSegment(request, account.Id, 2, `{"total_tokens":8}`, 1)
				require.Error(t, err)
				require.NoError(t, RequestCNDirectWalletCancellation(request, account.Id))
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("direct-deleted-outbox-fail", func(tx *gorm.DB) {
					if tx.Statement.Table == "cn_finance_outboxes" {
						tx.AddError(errors.New("injected deleted token disposition failure"))
					}
				}))
				require.Error(t, RecoverCNDirectWalletCancellations(context.Background(), db, 100))
				require.NoError(t, db.First(&account, account.Id).Error)
				require.Equal(t, 9953, account.Quota)
				disposition, err := ReadCNDirectWallet(request, account.Id)
				require.NoError(t, err)
				require.Zero(t, disposition.TokenDisposedQuota)
				require.NoError(t, db.Callback().Create().Remove("direct-deleted-outbox-fail"))
				require.NoError(t, RecoverCNDirectWalletCancellations(context.Background(), db.Session(&gorm.Session{NewDB: true}), 100))
				require.NoError(t, RecoverCNDirectWalletCancellations(context.Background(), db, 100))
				require.NoError(t, db.First(&account, account.Id).Error)
				require.Equal(t, 9993, account.Quota)
				require.Equal(t, 7, account.UsedQuota)
				require.Equal(t, 1, account.RequestCount)
				disposition, err = ReadCNDirectWallet(request, account.Id)
				require.NoError(t, err)
				require.Equal(t, "cancelled", disposition.State)
				require.Equal(t, status, disposition.TokenAdjustmentStatus)
				require.EqualValues(t, 40, disposition.TokenDisposedQuota)
				var count int64
				require.NoError(t, db.Model(&Token{}).Where("id = ?", credential.Id).Count(&count).Error)
				require.Zero(t, count)
				if deleted == "soft" {
					require.NoError(t, db.Unscoped().First(&credential, credential.Id).Error)
					require.Equal(t, 953, credential.RemainQuota)
					require.Equal(t, 47, credential.UsedQuota)
				} else {
					require.NoError(t, db.Unscoped().Model(&Token{}).Where("id = ?", credential.Id).Count(&count).Error)
					require.Zero(t, count)
				}
				var outbox CNFinanceOutbox
				require.NoError(t, db.Where("chain_key = ?", CNIdentity("charge", "request:"+request, "0")).First(&outbox).Error)
				var final CNFact
				require.NoError(t, common.UnmarshalJsonStr(outbox.Payload, &final))
				var evidenced bool
				for _, e := range final.SourceEvidence {
					evidenced = evidenced || e.RecordType == "cn_direct_wallet_token_disposition"
				}
				require.True(t, evidenced)
			}
			unlimited := Token{UserId: u.Id, Key: id + "unlimited", Status: common.TokenStatusEnabled, UnlimitedQuota: true}
			require.NoError(t, db.Create(&unlimited).Error)
			request := id + "unlimited"
			require.NoError(t, BeginCNDirectWallet(request, u.Id, unlimited.Id, channel.Id, 10))
			require.NoError(t, db.Model(&Token{}).Where("id = ?", unlimited.Id).Update("unlimited_quota", false).Error)
			_, err = ApplyCNDirectWalletSegment(request, u.Id, 1, `{"total_tokens":1}`, 1)
			require.ErrorIs(t, err, ErrCNConflict)
			require.NoError(t, db.Model(&Token{}).Where("id = ?", unlimited.Id).Update("unlimited_quota", true).Error)
			_, err = ApplyCNDirectWalletSegment(request, u.Id, 1, `{"total_tokens":1}`, 1)
			require.NoError(t, err)
			require.NoError(t, FinalizeCNDirectWallet(request, u.Id, 1))
			require.NoError(t, db.First(&unlimited, unlimited.Id).Error)
			require.Equal(t, -1, unlimited.RemainQuota)
			require.Equal(t, 1, unlimited.UsedQuota)
			t.Logf("%s atomic direct-wallet receipts, replay, rollback, cancellation recovery and published conversion PASS", dialect)
		})
	}
}
