package model

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	mysqldsn "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// This bridge only provisions synthetic fixtures, performs native business
// operations, and reads evidence. All integrity control calls use the CLI exe.
func TestCNIntegrityOperatorAcceptanceFixture(t *testing.T) {
	action := os.Getenv("CN_OPERATOR_ACCEPTANCE_ACTION")
	if action == "" {
		t.Skip("opt-in: invoked by verify_integrity_operator_cli.py")
	}
	dir := os.Getenv("CN_OPERATOR_ACCEPTANCE_WORK")
	require.NotEmpty(t, dir)
	db, err := OpenCNIntegrityOperatorDB(context.Background())
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	DB = db
	common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = false, false, 1
	result := map[string]any{}
	switch action {
	case "provision":
		name := os.Getenv("CN_OPERATOR_ACCEPTANCE_DATABASE")
		require.Regexp(t, regexp.MustCompile(`^s4_operator_[a-f0-9]{12}$`), name)
		dsn := os.Getenv("SQL_DSN")
		switch common.MainDatabaseType() {
		case common.DatabaseTypeMySQL:
			config, err := mysqldsn.ParseDSN(dsn)
			require.NoError(t, err)
			require.Equal(t, "tcp", config.Net)
			require.Equal(t, "127.0.0.1:33174", config.Addr, "only the owned acceptance MySQL server")
			require.NoError(t, db.Exec("CREATE DATABASE "+name).Error)
			config.DBName = name
			dsn = config.FormatDSN()
		case common.DatabaseTypePostgreSQL:
			config, err := url.Parse(dsn)
			require.NoError(t, err)
			require.Equal(t, "127.0.0.1:55474", config.Host, "only the owned acceptance PostgreSQL server")
			require.NoError(t, db.Exec("CREATE DATABASE "+name).Error)
			config.Path = "/" + name
			dsn = config.String()
		default:
			t.Fatal("provision only supports the explicit owned external servers")
		}
		result["sqlDsn"] = dsn
	case "seed":
		require.False(t, db.Migrator().HasTable(&CNFinanceStream{}), "refuse to seed an existing Gateway database")
		require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &Task{}, &TaskBillingReconciliation{}, &TopUp{}, &Option{}, &Log{}))
		for range 2 {
			require.NoError(t, MigrateCNFinance(db, "test-cn"))
		}
		for _, id := range []int{1001, 1002} {
			name := "operator-cli-" + strconv.Itoa(id)
			require.NoError(t, db.Create(&User{Id: id, Username: name, AffCode: name, Status: common.UserStatusEnabled}).Error)
		}
		var stream CNFinanceStream
		require.NoError(t, db.First(&stream, 1).Error)
		result["streamEpoch"], result["gatewayId"] = stream.Epoch, stream.GatewayID
		query := "SELECT version()"
		if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
			query = "SELECT sqlite_version()"
		}
		var version string
		require.NoError(t, db.Raw(query).Scan(&version).Error)
		result["databaseVersion"] = version
	case "native":
		require.True(t, CNDurableWalletAvailable())
		require.True(t, cnWalletValueAvailable())
		require.NoError(t, db.Create(&TopUp{UserId: 1001, Amount: 5, TradeNo: "operator-cli-synthetic-topup", PaymentProvider: PaymentProviderCreem, Status: common.TopUpStatusPending}).Error)
		require.NoError(t, RechargeCreem("operator-cli-synthetic-topup", "", "", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "5", CurrencyCode: "USD", PaymentReference: "synthetic-no-external-payment"}))
		ok, err := ReserveCNWallet("operator-cli-synthetic-charge", 1001, 1, true)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, FinalizeCNWallet("operator-cli-synthetic-charge", 1001, 1, false))
		_, err = ReadCNFacts(context.Background(), db, "", 1000)
		require.NoError(t, err)
		var native User
		require.NoError(t, db.First(&native, 1001).Error)
		require.Equal(t, 4, native.Quota)
		result["nativeBalance"] = native.Quota
	case "inspect":
		for name, table := range map[string]struct {
			model any
			order string
		}{
			"epoch": {&CNIntegrityEpoch{}, "epoch_id"}, "stream": {&CNFinanceStream{}, "id"},
			"approvals": {&CNIntegrityEligibilityApproval{}, "snapshot_id"}, "admissions": {&CNIntegrityAdmission{}, "admission_id"},
			"writers": {&CNIntegrityWriter{}, "epoch_id, writer_id, lease_generation"}, "fences": {&CNIntegrityFence{}, "epoch_id, generation"},
			"snapshots": {&CNIntegritySnapshot{}, "proof_id"}, "controls": {&CNIntegrityControlReceipt{}, "epoch_id, generation"},
			"originals": {&CNIntegrityOriginal{}, "receipt_id"}, "snapshotCollections": {&CNIntegritySnapshotCollection{}, "proof_id, collection"},
			"freezeHead": {&CNIntegrityFreezeHead{}, "id"}, "gate": {&CNIntegrityGate{}, "id"},
		} {
			rows := []map[string]any{}
			require.NoError(t, db.Model(table.model).Order(table.order).Find(&rows).Error)
			raw, err := common.Marshal(rows)
			require.NoError(t, err)
			hash, err := CNIntegrityBodyHash(raw)
			require.NoError(t, err)
			result[name] = map[string]any{"count": len(rows), "contentHash": hash}
		}
	case "export":
		raw, err := os.ReadFile(filepath.Join(dir, "approved.json"))
		require.NoError(t, err)
		var snapshots []CNIntegritySnapshot
		require.NoError(t, db.Order("batch_sequence").Find(&snapshots).Error)
		for _, snapshot := range snapshots {
			cnWriteIntegrityRawSampleAt(t, db, &snapshot, filepath.Join(os.Getenv("CN_OPERATOR_ACCEPTANCE_OUTPUT"), "batch-"+strconv.FormatInt(snapshot.BatchSequence, 10)), "see-seed-result", raw, 500)
		}
		result["snapshots"] = len(snapshots)
	default:
		t.Fatalf("unknown fixture action %q", action)
	}
	raw, err := common.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture-result.json"), raw, 0600))
}
