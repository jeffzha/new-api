package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCNIntegrityThousandNativeExports(t *testing.T) {
	fixtureDir := os.Getenv("CN_INTEGRITY_1000_FIXTURE_DIR")
	if fixtureDir == "" {
		t.Skip("opt-in: requires real Ops-reviewed 1000-customer integration inventory")
	}
	approvalBytes, err := os.ReadFile(filepath.Join(fixtureDir, "sqlite.json"))
	require.NoError(t, err)
	approval, err := DecodeCNIntegrityEligibilitySnapshot(approvalBytes)
	require.NoError(t, err)
	require.Len(t, approval.Body.Customers, 1000)
	provenance, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	require.NoError(t, err)
	for _, key := range []string{"OPS_CN_FINANCE_V2_ENABLED", "OPS_CN_DURABLE_WALLET_ENABLED", "OPS_CN_DURABLE_TASK_ENABLED", "OPS_CN_WALLET_VALUE_ENABLED", "OPS_CN_INTEGRITY_CAPTURE_ENABLED"} {
		t.Setenv(key, "true")
	}
	t.Setenv("OPS_CN_INTEGRITY_WRITER_ID", "native-thousand-writer")
	t.Setenv("OPS_CN_INTEGRITY_WRITER_BUILD", "s4-thousand-integration")
	oldDB, oldType := DB, common.MainDatabaseType()
	oldBatch, oldRedis, oldUnit := common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit
	common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = false, false, 1
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = oldDB
		common.SetMainDatabaseType(oldType)
		common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = oldBatch, oldRedis, oldUnit
	})
	for _, shape := range []string{"distinct-1000-pairs", "shared-1000-charges"} {
		t.Run(shape, func(t *testing.T) {
			started := time.Now()
			dbPath := filepath.Join(t.TempDir(), "native-thousand.sqlite3")
			db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_txlock=immediate"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = pool.Close() })
			DB = db
			var dbVersion string
			require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&dbVersion).Error)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &Task{}, &TaskBillingReconciliation{}, &TopUp{}, &Option{}, &Log{}))
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, approval.Body.GatewayID))
			}
			require.NoError(t, db.Model(&CNFinanceStream{}).Where("id = ?", 1).Update("epoch", approval.Body.StreamEpoch).Error)
			epoch, err := PrepareCNIntegrityCapture(db, "synthetic-thousand-local-prepare")
			require.NoError(t, err)
			require.NoError(t, db.Model(&CNIntegrityEpoch{}).Where("epoch_id = ?", epoch.EpochID).Update("epoch_id", approval.Body.IntegrityEpoch).Error)
			require.NoError(t, db.Model(&CNIntegrityControlReceipt{}).Where("epoch_id = ?", epoch.EpochID).Update("epoch_id", approval.Body.IntegrityEpoch).Error)
			epoch.EpochID = approval.Body.IntegrityEpoch
			require.NoError(t, ImportCNIntegrityEligibilitySnapshot(db, approvalBytes, "synthetic-thousand-explicit-operator-import"))
			users := 1000
			if shape == "shared-1000-charges" {
				users = 1
			}
			for i := range users {
				user := 1001 + i
				name := fmt.Sprintf("native-thousand-%d", user)
				require.NoError(t, db.Create(&User{Id: user, Username: name, AffCode: name, Status: common.UserStatusEnabled}).Error)
				_, err := AdmitCNIntegrityUser(db, epoch.EpochID, user, 1, approval.OpsInventoryID)
				require.NoError(t, err)
			}
			attestation := CNIntegrityDrainAttestation{InventoryID: "synthetic-isolated-thousand-process", InventoryHash: "sha256:" + strings.Repeat("a", 64), OperatorEvidenceID: "synthetic-thousand-drain", OldWritersDrained: true, RedisDisabled: true, BatchDisabled: true, ExternalSQLExcluded: true, UnguardedHandlesExcluded: true}
			writers := []CNIntegrityWriterDeclaration{{WriterID: "native-thousand-writer", BuildID: "s4-thousand-integration", LeaseUntil: time.Now().UTC().Add(2 * time.Hour).Format(CNTimeLayout), ProtocolVersion: cnIntegrityWriterProtocol, CapabilityPaths: []string{"quota_mutation", "source_switch", "task_reserve", "task_revision", "topup_credit", "wallet_final", "wallet_release", "wallet_reserve"}}}
			require.NoError(t, StartCNIntegrityCapture(db, epoch.EpochID, 1, writers, attestation))
			for i := range users {
				user, quota := 1001+i, 1
				if users == 1 {
					quota = 1000
				}
				trade := fmt.Sprintf("thousand-native-topup-%d", user)
				require.NoError(t, db.Create(&TopUp{UserId: user, Amount: int64(quota), TradeNo: trade, PaymentProvider: PaymentProviderCreem, Status: common.TopUpStatusPending}).Error)
				require.NoError(t, RechargeCreem(trade, "", "", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "synthetic-actual-receipt-" + trade}))
			}
			for i := range 1000 {
				user := 1001
				if users > 1 {
					user += i
				}
				request := fmt.Sprintf("thousand-native-charge-%04d", i)
				ok, err := ReserveCNWallet(request, user, 1, true)
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, FinalizeCNWallet(request, user, 1, false))
				if (i+1)%100 == 0 {
					t.Logf("%s actual native charges=%d elapsed=%s", shape, i+1, time.Since(started))
				}
			}
			cursor := ""
			for {
				page, err := ReadCNFacts(context.Background(), db, cursor, 1000)
				require.NoError(t, err)
				cursor = page["nextCursor"].(string)
				if !page["hasMore"].(bool) {
					break
				}
			}
			request := CNIntegrityFreezeRequest{EpochID: epoch.EpochID, ExpectedGeneration: 2, CoverageFrom: started.UTC().Add(-time.Hour).Format(CNTimeLayout), CoverageTo: time.Now().UTC().Add(time.Hour).Format(CNTimeLayout)}
			sealed, err := FreezeCNIntegrity(context.Background(), db, request)
			require.NoError(t, err)
			var manifest map[string]any
			require.NoError(t, common.Unmarshal([]byte(sealed.Manifest), &manifest))
			require.Equal(t, "complete", manifest["status"])
			dir := filepath.Join("testdata", "cn-task", "integrity-v2-thousand", shape)
			cnWriteIntegrityRawSampleAt(t, db, sealed, dir, dbVersion, approvalBytes, 500)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ops-fixture-provenance.json"), provenance, 0600))
			t.Logf("%s sealed %s elapsed=%s", shape, sealed.ProofID, time.Since(started))
		})
	}
}
