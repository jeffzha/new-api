package opswallet

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func migrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "source.db"))
	switch engine := os.Getenv("OPS_NATIVE_TEST_ENGINE"); engine {
	case "mysql":
		driver = mysql.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "postgres":
		driver = postgres.Open(os.Getenv("OPS_NATIVE_TEST_DSN"))
	case "", "sqlite":
	default:
		t.Fatal("unsupported migration test database")
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	previous := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previous })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSubscription{}, &model.SubscriptionOrder{}, &model.Task{}, &model.TaskBillingReconciliation{}, &model.Midjourney{}))
	return db
}

func TestOpeningSourceFreezeSurvivesRestartAndSchemaUpgrade(t *testing.T) {
	db := migrationTestDB(t)
	user := model.User{Username: "opening-source", AffCode: "opening-source", Quota: -17, BillingMode: "wallet"}
	require.NoError(t, db.Create(&user).Error)
	// Existing populated schema, followed by repeated operator preparation and
	// application migrations: source balances and database guards must survive.
	for range 2 {
		require.NoError(t, model.FreezeOPSSourceWallet(db))
		require.NoError(t, db.AutoMigrate(&model.User{}))
	}
	require.NoError(t, model.VerifyOPSSourceWalletFrozen(db.Session(&gorm.Session{NewDB: true}), user.Id))
	require.ErrorContains(t, db.Exec("UPDATE users SET quota=quota-1 WHERE id=?", user.Id).Error, "ops_source_wallet_frozen")
	require.ErrorContains(t, db.Model(&user).Update("billing_mode", "agency").Error, "ops_source_wallet_frozen")
	require.ErrorContains(t, db.Create(&model.User{Username: "must-not-credit", AffCode: "nonzero", Quota: 1}).Error, "ops_source_wallet_frozen")
	require.ErrorContains(t, db.Unscoped().Delete(&user).Error, "ops_source_wallet_frozen")
	require.NoError(t, db.Model(&user).Update("display_name", "Updated profile").Error)
	require.NoError(t, db.Create(&model.User{Username: "new-zero", AffCode: "zero", Quota: 0}).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, -17, user.Quota)
	assert.Equal(t, "wallet", user.BillingMode)
}

func TestOpeningFreshSourceGuard(t *testing.T) {
	db := migrationTestDB(t)
	for range 2 {
		require.NoError(t, model.FreezeOPSSourceWallet(db))
		require.NoError(t, db.AutoMigrate(&model.User{}))
	}
	require.ErrorContains(t, db.Create(&model.User{Username: "old-registration-gift", AffCode: "old", Quota: 10}).Error, "ops_source_wallet_frozen")
	user := model.User{Username: "ops-registration", AffCode: "new", Quota: 0}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, model.VerifyOPSSourceWalletFrozen(db.Session(&gorm.Session{NewDB: true}), user.Id))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Zero(t, user.Quota)
}

// Exercise partial upload, restart, failed journal writes and the dangerous
// timeout-after-remote-commit case. Real OPS integration is tested separately.
func TestOpeningMigrationResumesWithoutDuplicateCustody(t *testing.T) {
	db := migrationTestDB(t)
	for id := 1; id <= 103; id++ {
		quota := id * 2
		if id == 2 {
			quota = -10
		}
		if id == 3 {
			quota = 0
		}
		u := model.User{Id: id, Username: fmt.Sprintf("opening-%d", id), AffCode: fmt.Sprint(id), Quota: quota, Status: common.UserStatusEnabled}
		if id == 4 {
			u.DeletedAt = gorm.DeletedAt{Time: time.Now().UTC().Truncate(time.Second), Valid: true}
		}
		require.NoError(t, db.Create(&u).Error)
	}
	balances := map[string]string{}
	batchBodies := map[string]string{}
	phase, uploads, custodyCalls := "", 0, 0
	failSecond, loseCustodyReply := true, true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/api/v1/ingest/v1/users":
			var batch struct {
				BatchID string
				Users   []struct {
					UserID  string
					Balance struct{ Amount string }
					Status  string
				}
			}
			require.NoError(t, common.DecodeJson(r.Body, &batch))
			encoded, _ := common.Marshal(batch)
			if old, ok := batchBodies[batch.BatchID]; ok {
				assert.Equal(t, old, string(encoded))
			}
			batchBodies[batch.BatchID] = string(encoded)
			uploads++
			if failSecond && uploads == 2 {
				w.WriteHeader(503)
				return
			}
			for _, u := range batch.Users {
				balances[u.UserID] = u.Balance.Amount
				if u.UserID == "4" {
					assert.Equal(t, "removed", u.Status)
				}
			}
			data = map[string]any{"accepted": len(batch.Users), "unchanged": 0}
		case "/api/v1/catalog/v1/wallet-custody":
			custodyCalls++
			phase = time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
			if loseCustodyReply {
				c, _, err := w.(http.Hijacker).Hijack()
				require.NoError(t, err)
				_ = c.Close()
				return
			}
			data = map[string]any{"holder": "ops", "since": phase}
		case "/api/v1/catalog/v1/wallets":
			items := []map[string]any{}
			for id, b := range balances {
				u := map[string]any{"userId": id, "balance": b, "balanceSource": "station", "unknownEntries": 0}
				if phase != "" {
					u["balanceSource"] = "ops"
					u["openingBalance"] = b
					u["openingEntryId"], _ = strconv.Atoi(id)
					u["balance"] = "999"
				}
				items = append(items, u)
			}
			data = map[string]any{"station": map[string]any{"quotaPerUnit": 1000000, "usdExchangeRate": "1"}, "currency": "USD", "items": items, "opsKeepsBalanceSince": phase}
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		raw, err := common.Marshal(map[string]any{"data": data})
		require.NoError(t, err)
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client := opsclient.New(server.URL, "opening-test", "ephemeral-opening-secret", time.Second)
	_, err := PrepareOpening(t.Context(), db, client, "overseas-test", "offline-run", false)
	require.Error(t, err)
	m, err := PrepareOpening(t.Context(), db, client, "overseas-test", "offline-run", true)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "journal.json")
	save := func(m *OpeningMigration) error { return SaveMigrationFile(path, m) }
	require.NoError(t, save(m))
	_, err = ApplyOpening(t.Context(), db, client, m, "wrong", "test-operator", save)
	require.Error(t, err)
	require.Zero(t, uploads)
	_, err = ApplyOpening(t.Context(), db, client, m, m.SnapshotSHA256, "test-operator", save)
	require.Error(t, err)
	require.Equal(t, 100, len(balances))
	require.Zero(t, custodyCalls)
	// Fresh process reads only the durable state file, with no mutable memory.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &m))
	failSecond = false
	_, err = ApplyOpening(t.Context(), db, client, m, m.SnapshotSHA256, "test-operator", func(*OpeningMigration) error { return errors.New("disk full") })
	require.ErrorContains(t, err, "disk full")
	require.Zero(t, custodyCalls)
	// Persisted state is still prepared, so exact profiles are safely replayed.
	require.NoError(t, common.Unmarshal(raw, &m))
	// A snapshot can match in total while individual wallets are wrong.
	// Reject each missing/duplicate/mismatched row before sending custody.
	check, err := readMigrationWallets(t.Context(), client)
	require.NoError(t, err)
	require.NoError(t, m.reconcile(check, false))
	original := check.Data.Items[0]
	wrong := "888"
	check.Data.Items[0].Balance = &wrong
	require.ErrorContains(t, m.reconcile(check, false), "balance differs")
	check.Data.Items[0] = original
	check.Data.Items[0].ID = "unapproved"
	require.ErrorContains(t, m.reconcile(check, false), "unapproved")
	check.Data.Items[0] = original
	check.Data.Items = check.Data.Items[1:]
	require.ErrorContains(t, m.reconcile(check, false), "coverage differs")
	require.Zero(t, custodyCalls)
	_, err = ApplyOpening(t.Context(), db, client, m, m.SnapshotSHA256, "test-operator", save)
	require.Error(t, err)
	require.Equal(t, 1, custodyCalls)
	raw, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &m))
	require.Equal(t, "custody_requested", m.Phase)
	proof, err := ApplyOpening(t.Context(), db, client, m, m.SnapshotSHA256, "test-operator", save)
	require.NoError(t, err)
	require.Equal(t, 103, len(proof.Users))
	require.Equal(t, 1, custodyCalls)
	_, err = ApplyOpening(t.Context(), db, client, m, m.SnapshotSHA256, "test-operator", save)
	require.NoError(t, err)
	require.Equal(t, 1, custodyCalls, "restart and duplicate apply must not create a new custody period")
	assert.Equal(t, 2, len(batchBodies))
	// A third party taking custody back is never silently reversed.
	phase = ""
	_, err = ApplyOpening(t.Context(), db, client, m, m.SnapshotSHA256, "test-operator", save)
	require.ErrorContains(t, err, "unresolved")
	require.Equal(t, 1, custodyCalls)
	require.Error(t, db.Exec("UPDATE users SET quota=0 WHERE id=1").Error)
	// Identity changes during the offline window invalidate the snapshot.
	require.NoError(t, db.Model(&model.User{}).Where("id=1").Update("username", "changed").Error)
	_, err = ApplyOpening(t.Context(), db, client, m, m.SnapshotSHA256, "test-operator", save)
	require.ErrorContains(t, err, "source users changed")
}
