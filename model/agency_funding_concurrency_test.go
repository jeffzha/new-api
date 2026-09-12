package model

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// agencyDialectDB opens a disposable database for the named dialect. External
// dialects are only opened when AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1 and the
// matching AGENCY_HUB_TEST_*_DSN variable is configured; otherwise they are
// skipped so the default SQLite run stays self contained.
func agencyDialectDB(t testing.TB, name string) *gorm.DB {
	var dsn string
	var dialector gorm.Dialector
	switch name {
	case "sqlite":
		dsn = "file:agency-funding-concurrency-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
		dialector = sqlite.Open(dsn)
	case "mysql":
		if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
			return nil
		}
		dsn = strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_MYSQL_DSN"))
		if dsn == "" {
			return nil
		}
		dialector = mysql.Open(dsn)
	case "postgres":
		if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
			return nil
		}
		dsn = strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_POSTGRES_DSN"))
		if dsn == "" {
			return nil
		}
		dialector = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	default:
		return nil
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	require.NoError(t, err)
	return db
}

// TestAgencyFundingConcurrentReserveIsConservedAcrossDialects runs the same
// core financial concurrency scenarios on SQLite, MySQL and PostgreSQL. Two
// simultaneous requests on the same customer must never allocate the paid
// balance twice; the wallet, token and funding projections must stay in exact
// agreement and money_seq must be a strictly increasing per-account
// serialization point (design §22.1「同机构两用户/同用户两Key并发」and §22.4
// 三DB 核心财务并发). SQLite serializes all writers at the database level, so
// that leg runs the same reserves sequentially; the true concurrent race is
// exercised on MySQL/PostgreSQL through the FOR UPDATE account row lock.
func TestAgencyFundingConcurrentReserveIsConservedAcrossDialects(t *testing.T) {
	for _, name := range []string{"sqlite", "mysql", "postgres"} {
		name := name
		db := agencyDialectDB(t, name)
		if db == nil {
			t.Logf("dialect %s not configured; skipped", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			previousDB := DB
			previousKeyCol := commonKeyCol
			DB = db
			if name == "postgres" {
				commonKeyCol = `"key"`
			} else {
				commonKeyCol = "`key`"
			}
			t.Cleanup(func() {
				DB = previousDB
				commonKeyCol = previousKeyCol
			})

			require.NoError(t, db.Migrator().DropTable(append(AgencyModels(), &User{}, &Token{})...))
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
			require.NoError(t, MigrateAgency(db))

			parallel := name != "sqlite"
			runFundingConservation(t, db, parallel)
			runWalletTokenConservation(t, db, parallel)
		})
	}
}

func runFundingConservation(t *testing.T, db *gorm.DB, parallel bool) {
	const (
		userID  = int64(987650)
		paid    = int64(100)
		resv    = int64(100)
		workers = 8
	)
	createUser := User{
		Id: int(userID), Username: "conc-funding-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AffCode:     "conc-funding-user-aff",
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 0,
	}
	require.NoError(t, db.Create(&createUser).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, userID, "payment", "conc-funding-topup", "payment_callback", paid, 0)
	}))

	reserve := func(i int) (int64, error) {
		_, seq, err := ReserveAgencyFundingWithSequence(userID, fmt.Sprintf("conc-funding-charge-%d", i), resv)
		return seq, err
	}
	sequences := make([]int64, workers)
	errs := make([]error, workers)
	if parallel {
		var wg sync.WaitGroup
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			i := i
			go func() {
				defer wg.Done()
				sequences[i], errs[i] = reserve(i)
			}()
		}
		wg.Wait()
	} else {
		for i := 0; i < workers; i++ {
			sequences[i], errs[i] = reserve(i)
		}
	}
	for i, err := range errs {
		require.NoError(t, err, "worker %d", i)
	}

	// Concurrent reserves serialize on the account row lock, so every charge
	// observes a distinct monotonic money_seq with no lost update. The same
	// ordering holds when SQLite runs them sequentially.
	expectedSeq := make([]int64, workers)
	for i := range expectedSeq {
		expectedSeq[i] = int64(i + 2)
	}
	gotSeq := append([]int64(nil), sequences...)
	sort.Slice(gotSeq, func(i, j int) bool { return gotSeq[i] < gotSeq[j] })
	require.Equal(t, expectedSeq, gotSeq)

	var account AgencyFundingAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&account).Error)
	require.Equal(t, int64(0), account.PaidAvailable)
	require.Equal(t, paid*(workers-1), account.DebtQuota)
	require.Equal(t, int64(workers+1), account.MoneySeq)

	var totalConsumed, totalDebt int64
	var allocations []AgencyFundingAllocation
	require.NoError(t, db.Where("user_id = ?", userID).Find(&allocations).Error)
	require.Len(t, allocations, workers)
	for _, row := range allocations {
		totalConsumed += row.Consumed
		totalDebt += row.DebtConsumed
	}
	require.Equal(t, paid, totalConsumed)
	require.Equal(t, paid*(workers-1), totalDebt)

	var lots []AgencyFundingLot
	require.NoError(t, db.Where("user_id = ?", userID).Find(&lots).Error)
	var lotAvailable, lotConsumed int64
	for _, lot := range lots {
		assert.Equal(t, lot.PaidInitial, lot.PaidAvailable+lot.PaidReserved+lot.PaidConsumed+lot.PaidRevoked, "lot %d conservation", lot.ID)
		lotAvailable += lot.PaidAvailable
		lotConsumed += lot.PaidConsumed
	}
	require.Equal(t, int64(0), lotAvailable)
	require.Equal(t, paid, lotConsumed)
}

func runWalletTokenConservation(t *testing.T, db *gorm.DB, parallel bool) {
	const userID = int64(987651)
	createUser := User{
		Id: int(userID), Username: "conc-wallet-token-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AffCode:     "conc-wallet-token-user-aff",
		BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 0,
	}
	require.NoError(t, db.Create(&createUser).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAgencyTopup(tx, userID, "payment", "conc-wallet-token-topup", "payment_callback", 100, 0)
	}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", userID).Update("quota", 160).Error)

	tokens := []Token{
		{UserId: int(userID), Key: "conc-wallet-token-key-1", Status: common.TokenStatusEnabled, RemainQuota: 100, UsedQuota: 0},
		{UserId: int(userID), Key: "conc-wallet-token-key-2", Status: common.TokenStatusEnabled, RemainQuota: 100, UsedQuota: 0},
	}
	for i := range tokens {
		require.NoError(t, db.Create(&tokens[i]).Error)
	}

	reserve := func(i int) (int64, error) {
		return TryReserveAgencyWalletAndToken(int(userID), tokens[i].Id, 80, tokens[i].Key, fmt.Sprintf("conc-wallet-token-charge-%d", i), 80, false)
	}
	paid := make([]int64, 2)
	errs := make([]error, 2)
	if parallel {
		var wg sync.WaitGroup
		wg.Add(2)
		for i := 0; i < 2; i++ {
			i := i
			go func() {
				defer wg.Done()
				paid[i], errs[i] = reserve(i)
			}()
		}
		wg.Wait()
	} else {
		for i := 0; i < 2; i++ {
			paid[i], errs[i] = reserve(i)
		}
	}
	for i, err := range errs {
		require.NoError(t, err, "key %d", i)
	}

	var user User
	require.NoError(t, db.First(&user, userID).Error)
	require.Equal(t, 0, user.Quota)
	for i := range tokens {
		var token Token
		require.NoError(t, db.First(&token, tokens[i].Id).Error)
		require.Equal(t, 80, token.UsedQuota)
		require.Equal(t, 20, token.RemainQuota)
	}

	var totalPaid int64
	for _, p := range paid {
		totalPaid += p
	}
	require.Equal(t, int64(100), totalPaid)
	var account AgencyFundingAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&account).Error)
	require.Equal(t, int64(0), account.PaidAvailable)
	require.Equal(t, int64(60), account.DebtQuota)
	var consumed int64
	var allocations []AgencyFundingAllocation
	require.NoError(t, db.Where("user_id = ?", userID).Find(&allocations).Error)
	for _, row := range allocations {
		consumed += row.Consumed
	}
	require.Equal(t, int64(100), consumed)
}

// BenchmarkAgencyFundingReservePerDialect measures sustained reserve
// throughput on each supported database as a local baseline for the §22.4
// capacity gate (60 events/s sustained, 120 peak). It drives the
// single-hot-account path ReserveAgencyFundingWithSequence, which is the
// serialization point the FOR UPDATE account row lock must keep at production
// RPS (design §15 单用户/单机构热点). The unit numbers here are a baseline
// only: the Go/No-Go gate still needs a dedicated load test with realistic
// concurrency plus backlog and disk-budget measurement in a production-like
// environment. ns/op is reported by the framework; reserves/s is converted
// from the same timed section.
func BenchmarkAgencyFundingReservePerDialect(b *testing.B) {
	for _, name := range []string{"sqlite", "mysql", "postgres"} {
		name := name
		db := agencyDialectDB(b, name)
		if db == nil {
			b.Logf("dialect %s not configured; skipped", name)
			continue
		}
		b.Run(name, func(b *testing.B) {
			previousDB := DB
			previousKeyCol := commonKeyCol
			DB = db
			if name == "postgres" {
				commonKeyCol = `"key"`
			} else {
				commonKeyCol = "`key`"
			}
			b.Cleanup(func() {
				DB = previousDB
				commonKeyCol = previousKeyCol
			})

			// Give each run an isolated schema and a dedicated high-id account
			// so repeated benchmark invocations never collide with the
			// conservation tests' uniqueness constraints.
			const userID = int64(987720)
			require.NoError(b, db.Migrator().DropTable(append(AgencyModels(), &User{}, &Token{})...))
			require.NoError(b, db.AutoMigrate(&User{}, &Token{}))
			require.NoError(b, MigrateAgency(db))
			require.NoError(b, db.Create(&User{
				Id: int(userID), Username: "bench-funding-user", Password: "password",
				Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
				AffCode:     fmt.Sprintf("bench-funding-user-aff-%s", name),
				BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 0,
			}).Error)
			require.NoError(b, db.Transaction(func(tx *gorm.DB) error {
				return RecordAgencyTopup(tx, userID, "payment", fmt.Sprintf("bench-funding-topup-%s", name), "payment_callback", 1<<40, 0)
			}))

			// Warm up once so connection pools and prepared statements are
			// ready before the timed section.
			if _, _, err := ReserveAgencyFundingWithSequence(userID, fmt.Sprintf("bm-warmup-%s", name), 1); err != nil {
				b.Fatalf("warmup reserve: %v", err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := ReserveAgencyFundingWithSequence(userID, fmt.Sprintf("bm-%s-%d", name, i), 1); err != nil {
					b.Fatalf("reserve %d: %v", i, err)
				}
			}
			b.StopTimer()

			// Per-reserve event amplification k for the storage-budget gate:
			// count durable allocation rows grown for the account and divide by
			// ops (account/lot rows are fixed-row updates, not growth).
			var allocRows int64
			if err := db.Model(&AgencyFundingAllocation{}).Where("user_id = ?", userID).Count(&allocRows).Error; err == nil && b.N > 0 {
				b.ReportMetric(float64(allocRows)/float64(b.N), "alloc-rows/op")
			}
			if elapsed := b.Elapsed().Seconds(); elapsed > 0 {
				b.ReportMetric(float64(b.N)/elapsed, "reserves/s")
			}
		})
	}
}

// BenchmarkAgencyFundingReserveConcurrentPerDialect drives the design §22.4
// capacity gate (60 events/s sustained, 120 peak) with real parallel workers
// per database. Three hotspot shapes are measured: a single hot user (one
// account row, fully serialized by the FOR UPDATE lock), a single agency burst
// (several users reserving against distinct account rows), and a mix of
// agencies. The gap between the single-user leg and the org legs is the
// headroom that spreading load across account rows provides. SQLite
// serializes all writers at the database level, so its legs run sequentially
// to avoid SQLITE_LOCKED; MySQL/PostgreSQL run b.RunParallel workers against
// the live row locks. Numbers are a local baseline; the Go/No-Go gate still
// requires production load, backlog and disk-budget measurement.
func BenchmarkAgencyFundingReserveConcurrentPerDialect(b *testing.B) {
	type scenario struct {
		name      string
		agency    int
		userCount int
	}
	scenarios := []scenario{
		{name: "single-user-hot", agency: 1, userCount: 1},
		{name: "single-org-hot", agency: 1, userCount: 8},
		{name: "multi-org-mixed", agency: 4, userCount: 16},
	}
	for _, name := range []string{"sqlite", "mysql", "postgres"} {
		name := name
		db := agencyDialectDB(b, name)
		if db == nil {
			b.Logf("dialect %s not configured; skipped", name)
			continue
		}
		b.Run(name, func(b *testing.B) {
			previousDB := DB
			previousKeyCol := commonKeyCol
			DB = db
			if name == "postgres" {
				commonKeyCol = `"key"`
			} else {
				commonKeyCol = "`key`"
			}
			b.Cleanup(func() {
				DB = previousDB
				commonKeyCol = previousKeyCol
			})

			for _, sc := range scenarios {
				sc := sc
				b.Run(sc.name, func(b *testing.B) {
					require.NoError(b, db.Migrator().DropTable(append(AgencyModels(), &User{}, &Token{})...))
					require.NoError(b, db.AutoMigrate(&User{}, &Token{}))
					require.NoError(b, MigrateAgency(db))

					const userBase = int64(987900)
					agencyIDs := make([]int64, 0, sc.agency)
					for a := 0; a < sc.agency; a++ {
						ag := Agency{
							Code:                   fmt.Sprintf("bench-org-%s-%s-%d", name, sc.name, a),
							DisplayName:            fmt.Sprintf("bench-org-%s-%s-%d", name, sc.name, a),
							Status:                 "active",
							InviteCode:             fmt.Sprintf("b%04d", a+1),
							CurrentPolicyVersionID: 1,
							PriceRevision:          1,
							StateRevision:          1,
							Version:                1,
							CreatedByType:          "root",
							CreatedByID:            1,
							CreatedAt:              1,
							UpdatedAt:              1,
						}
						require.NoError(b, db.Create(&ag).Error)
						agencyIDs = append(agencyIDs, ag.ID)
					}

					userIDs := make([]int64, 0, sc.userCount)
					for i := 0; i < sc.userCount; i++ {
						uid := userBase + int64(i)
						require.NoError(b, db.Create(&User{
							Id: int(uid), Username: fmt.Sprintf("bc-%s-%s-%d", name, sc.name, i), Password: "password",
							Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
							AffCode:     fmt.Sprintf("bca-%s-%s-%d", name, sc.name, i),
							BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 0,
						}).Error)
						hist := AgencyUserBinding{
							UserID: uid, AgencyID: agencyIDs[i%sc.agency], Revision: 1,
							InviteSnapshot: "bench", CreatedSource: "benchmark", EffectiveAtMS: 1,
						}
						require.NoError(b, db.Create(&hist).Error)
						require.NoError(b, db.Create(&AgencyActiveUserBinding{
							UserID: uid, BindingID: hist.ID, Revision: 1, AgencyID: agencyIDs[i%sc.agency], UpdatedAt: 1,
						}).Error)
						require.NoError(b, db.Transaction(func(tx *gorm.DB) error {
							return RecordAgencyTopup(tx, uid, "payment", fmt.Sprintf("bench-conc-topup-%s-%s-%d", name, sc.name, i), "payment_callback", 1<<40, 0)
						}))
						userIDs = append(userIDs, uid)
					}

					reserve := func(i int64) {
						uid := userIDs[int(i)%len(userIDs)]
						if _, _, err := ReserveAgencyFundingWithSequence(uid, fmt.Sprintf("%s-%d", common.NewRequestId(), i), 1); err != nil {
							b.Fatalf("reserve %d: %v", i, err)
						}
					}
					b.ResetTimer()
					if name == "sqlite" {
						for i := int64(0); i < int64(b.N); i++ {
							reserve(i)
						}
					} else {
						b.SetParallelism(2)
						var counter int64
						b.RunParallel(func(pb *testing.PB) {
							for pb.Next() {
								i := atomic.AddInt64(&counter, 1) - 1
								reserve(i)
							}
						})
					}
					b.StopTimer()

					if elapsed := b.Elapsed().Seconds(); elapsed > 0 {
						b.ReportMetric(float64(b.N)/elapsed, "reserves/s")
					}
					var allocRows int64
					if err := db.Model(&AgencyFundingAllocation{}).Where("user_id IN ?", userIDs).Count(&allocRows).Error; err == nil && b.N > 0 {
						b.ReportMetric(float64(allocRows)/float64(b.N), "alloc-rows/op")
					}
				})
			}
		})
	}
}
