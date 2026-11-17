package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNBoundWalletAtomicAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	t.Cleanup(func() { common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch })
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
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "bound.db")), &gorm.Config{})
				require.NoError(t, err)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			oldDB := DB
			DB = db
			t.Cleanup(func() { DB = oldDB })
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
			id := "b" + common.GetUUID()[:15]
			u := User{Username: id, AffCode: id, Quota: 10000, Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(&u).Error)
			token := Token{UserId: u.Id, Key: id, Status: common.TokenStatusEnabled, RemainQuota: 10000}
			require.NoError(t, db.Create(&token).Error)
			check := func(wallet, used int) {
				t.Helper()
				var actual User
				var credential Token
				require.NoError(t, db.First(&actual, u.Id).Error)
				require.NoError(t, db.First(&credential, token.Id).Error)
				require.Equal(t, 10000-wallet, actual.Quota)
				require.Equal(t, 10000-used, credential.RemainQuota)
				require.Equal(t, used, credential.UsedQuota)
			}
			ok, err := ReserveCNWallet(id+"old", u.Id, 10, true)
			require.NoError(t, err)
			require.True(t, ok)
			require.NoError(t, db.Migrator().DropTable(&CNWalletBinding{}))
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			_, err = PreConsumeCNBoundWallet(id+"old", u.Id, 10, token.Id, false, false)
			require.ErrorIs(t, err, ErrCNWalletBindingRequired)
			check(10, 0)
			_, err = PreConsumeCNBoundWallet(id+"bad", u.Id, 100, token.Id+100000, false, false)
			require.Error(t, err)
			check(10, 0)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("bound-initial-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_wallet_bindings" {
					tx.AddError(errors.New("injected binding failure"))
				}
			}))
			_, err = PreConsumeCNBoundWallet(id, u.Id, 100, token.Id, false, false)
			require.Error(t, err)
			check(10, 0)
			require.NoError(t, db.Callback().Create().Remove("bound-initial-fail"))
			reserved, err := PreConsumeCNBoundWallet(id, u.Id, 100, token.Id, false, false)
			require.NoError(t, err)
			require.Equal(t, 100, reserved)
			check(110, 100)
			_, err = ReserveCNWallet(id, u.Id, 150, false)
			require.ErrorIs(t, err, ErrCNWalletBindingRequired)
			require.ErrorIs(t, FinalizeCNWallet(id, u.Id, 100, false), ErrCNWalletBindingRequired)
			require.ErrorIs(t, RequestCNWalletCancellation(db, id, u.Id, token.Id, 100), ErrCNWalletBindingRequired)
			require.NoError(t, ReserveCNBoundWallet(id, u.Id, 150, true))
			require.NoError(t, ReserveCNBoundWallet(id, u.Id, 150, true))
			check(160, 150)
			reserved, err = PreConsumeCNBoundWallet(id, u.Id, 100, token.Id, false, false)
			require.NoError(t, err)
			require.Equal(t, 150, reserved)
			_, err = PreConsumeCNBoundWallet(id, u.Id, 101, token.Id, false, false)
			require.ErrorIs(t, err, ErrCNConflict)
			var persistedToken Token
			require.NoError(t, db.First(&persistedToken, token.Id).Error)
			require.NoError(t, db.Unscoped().Delete(&Token{}, token.Id).Error)
			require.Error(t, FinalizeCNBoundWallet(id, u.Id, 180))
			var actual User
			require.NoError(t, db.First(&actual, u.Id).Error)
			require.Equal(t, 9840, actual.Quota)
			require.NoError(t, db.Create(&persistedToken).Error)
			require.NoError(t, db.Model(&Token{}).Where("id = ?", token.Id).Update("status", common.TokenStatusDisabled).Error)
			require.Error(t, FinalizeCNBoundWallet(id, u.Id, 180))
			check(160, 150)
			require.NoError(t, db.Model(&Token{}).Where("id = ?", token.Id).Update("status", common.TokenStatusEnabled).Error)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("bound-final-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected final receipt failure"))
				}
			}))
			require.Error(t, FinalizeCNBoundWallet(id, u.Id, 180))
			check(160, 150)
			require.NoError(t, db.Callback().Create().Remove("bound-final-fail"))
			require.NoError(t, FinalizeCNBoundWallet(id, u.Id, 180))
			require.NoError(t, FinalizeCNBoundWallet(id, u.Id, 180))
			require.ErrorIs(t, FinalizeCNBoundWallet(id, u.Id, 181), ErrCNConflict)
			check(190, 180)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			var fact CNFact
			for _, f := range page["items"].([]CNFact) {
				if f.RequestID != nil && *f.RequestID == id {
					fact = f
				}
			}
			require.NotEmpty(t, fact.FactID)
			require.Equal(t, "180", *fact.Cumulative.Quota)
			var found bool
			for _, e := range fact.SourceEvidence {
				if e.RecordType == "cn_wallet_token_binding" {
					found = true
				}
			}
			require.True(t, found)
			if dir := os.Getenv("S4R_TEST_ARTIFACT_DIR"); dir != "" {
				raw, err := common.Marshal(fact)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "bound-wallet-canonical-"+dialect+".json"), raw, 0600))
			}
			// Zero initial reserve remains a persistent token binding, not a bypass.
			_, err = PreConsumeCNBoundWallet(id+"trusted", u.Id, 0, token.Id, false, false)
			require.NoError(t, err)
			require.NoError(t, FinalizeCNBoundWallet(id+"trusted", u.Id, 60))
			check(250, 240)
			_, err = PreConsumeCNBoundWallet(id+"play", u.Id, 30, 0, false, true)
			require.NoError(t, err)
			require.NoError(t, ReserveCNBoundWallet(id+"play", u.Id, 50, true))
			require.NoError(t, FinalizeCNBoundWallet(id+"play", u.Id, 20))
			check(270, 240)
			// Cancellation uses persisted extra-reserve amount and never caller memory.
			cancel := id + "cancel"
			_, err = PreConsumeCNBoundWallet(cancel, u.Id, 40, token.Id, false, false)
			require.NoError(t, err)
			require.NoError(t, ReserveCNBoundWallet(cancel, u.Id, 80, true))
			require.NoError(t, RequestCNBoundWalletCancellation(db, cancel, u.Id))
			check(350, 320)
			require.ErrorIs(t, ReserveCNBoundWallet(cancel, u.Id, 90, true), ErrCNConflict)
			require.ErrorIs(t, FinalizeCNBoundWallet(cancel, u.Id, 80), ErrCNConflict)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("bound-cancel-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_finance_outboxes" {
					tx.AddError(errors.New("injected cancellation failure"))
				}
			}))
			require.Error(t, CompleteCNWalletCancellation(context.Background(), db, cancel))
			check(350, 320)
			require.NoError(t, db.Callback().Create().Remove("bound-cancel-fail"))
			require.NoError(t, CompleteCNWalletCancellation(context.Background(), db.Session(&gorm.Session{NewDB: true}), cancel))
			require.NoError(t, CompleteCNWalletCancellation(context.Background(), db, cancel))
			check(270, 240)
			require.NoError(t, RequestCNBoundWalletCancellation(db, cancel, u.Id))
			require.NoError(t, CompleteCNWalletCancellation(context.Background(), db, cancel))
			check(270, 240)
			var old CNWalletLifecycle
			require.NoError(t, db.Where("request_id = ?", id+"old").First(&old).Error)
			_, unknown, err := cnWalletBindingProofTx(db, old)
			require.NoError(t, err)
			require.True(t, unknown)
			var binding CNWalletBinding
			require.NoError(t, db.Where("request_id = ?", cancel).First(&binding).Error)
			require.Zero(t, binding.TokenQuota)
			// Unlimited tokens keep their original tracked quota behavior, while
			// finite tokens must have enough quota for new reservations.
			unlimited := Token{UserId: u.Id, Key: id + "unlimited", Status: common.TokenStatusEnabled, UnlimitedQuota: true}
			require.NoError(t, db.Create(&unlimited).Error)
			_, err = PreConsumeCNBoundWallet(id+"unlimited", u.Id, 10, unlimited.Id, true, false)
			require.NoError(t, err)
			require.NoError(t, FinalizeCNBoundWallet(id+"unlimited", u.Id, 20))
			require.NoError(t, db.First(&unlimited, unlimited.Id).Error)
			require.Equal(t, -20, unlimited.RemainQuota)
			require.Equal(t, 20, unlimited.UsedQuota)
			finite := Token{UserId: u.Id, Key: id + "finite", Status: common.TokenStatusEnabled, RemainQuota: 5}
			require.NoError(t, db.Create(&finite).Error)
			_, err = PreConsumeCNBoundWallet(id+"finite", u.Id, 10, finite.Id, false, false)
			require.ErrorIs(t, err, ErrInsufficientAgencyTokenQuota)
			for _, deleted := range []string{"soft", "hard"} {
				request := id + deleted
				credential := Token{UserId: u.Id, Key: request, Status: common.TokenStatusEnabled, RemainQuota: 1000}
				require.NoError(t, db.Create(&credential).Error)
				_, err = PreConsumeCNBoundWallet(request, u.Id, 40, credential.Id, false, false)
				require.NoError(t, err)
				query := db
				status := "deleted_token_not_recredited"
				if deleted == "hard" {
					query = db.Unscoped()
					status = "missing_token_not_recredited"
				}
				require.NoError(t, query.Delete(&Token{}, credential.Id).Error)
				require.Error(t, FinalizeCNBoundWallet(request, u.Id, 41))
				require.NoError(t, RequestCNBoundWalletCancellation(db, request, u.Id))
				require.NoError(t, CompleteCNWalletCancellation(context.Background(), db, request))
				require.NoError(t, CompleteCNWalletCancellation(context.Background(), db, request))
				var disposition CNWalletBinding
				require.NoError(t, db.Where("request_id = ?", request).First(&disposition).Error)
				require.Equal(t, status, disposition.TokenAdjustmentStatus)
				require.Equal(t, int64(40), disposition.TokenDisposedQuota)
				require.Zero(t, disposition.TokenQuota)
				var count int64
				require.NoError(t, db.Model(&Token{}).Where("id = ?", credential.Id).Count(&count).Error)
				require.Zero(t, count)
				if deleted == "soft" {
					require.NoError(t, db.Unscoped().First(&credential, credential.Id).Error)
					require.Equal(t, 960, credential.RemainQuota)
					require.Equal(t, 40, credential.UsedQuota)
				} else {
					require.NoError(t, db.Unscoped().Model(&Token{}).Where("id = ?", credential.Id).Count(&count).Error)
					require.Zero(t, count)
				}
			}
			// Immutable flags cannot be changed by replay or by a live token edit.
			request := id + "flags"
			_, err = PreConsumeCNBoundWallet(request, u.Id, 10, token.Id, false, false)
			require.NoError(t, err)
			_, err = PreConsumeCNBoundWallet(request, u.Id, 10, token.Id, true, false)
			require.ErrorIs(t, err, ErrCNConflict)
			require.NoError(t, db.Model(&Token{}).Where("id = ?", token.Id).Update("unlimited_quota", true).Error)
			require.ErrorIs(t, ReserveCNBoundWallet(request, u.Id, 20, true), ErrCNConflict)
			require.NoError(t, db.Model(&Token{}).Where("id = ?", token.Id).Update("unlimited_quota", false).Error)
			require.NoError(t, RequestCNBoundWalletCancellation(db, request, u.Id))
			require.NoError(t, CompleteCNWalletCancellation(context.Background(), db, request))
		})
	}
}
