package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These fixtures must be independently frozen, reviewed and exported by Ops.
// This test never fabricates their reviewHash, snapshot identity or approval.
func TestCNIntegrityRealApprovedExportAcrossDialects(t *testing.T) {
	fixtureDir := os.Getenv("CN_INTEGRITY_OPS_APPROVED_FIXTURE_DIR")
	if fixtureDir == "" {
		t.Skip("requires independently approved Ops eligibility export files")
	}
	provenance, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	require.NoError(t, err)
	evidenceDir := filepath.Join("testdata", "cn-task", "integrity-v2-real")
	require.NoError(t, os.MkdirAll(evidenceDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(evidenceDir, "ops-fixture-provenance.json"), provenance, 0600))
	for _, key := range []string{"OPS_CN_FINANCE_V2_ENABLED", "OPS_CN_DURABLE_WALLET_ENABLED", "OPS_CN_DURABLE_TASK_ENABLED", "OPS_CN_WALLET_VALUE_ENABLED", "OPS_CN_INTEGRITY_CAPTURE_ENABLED"} {
		t.Setenv(key, "true")
	}
	t.Setenv("OPS_CN_INTEGRITY_WRITER_ID", "real-three-engine-writer")
	t.Setenv("OPS_CN_INTEGRITY_WRITER_BUILD", "s4-native-integrity-v2-test")
	oldBatch, oldRedis, oldUnit := common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit
	common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = false, false, 1
	t.Cleanup(func() {
		common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = oldBatch, oldRedis, oldUnit
	})
	for i, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixtureDir, dialect+".json"))
			require.NoError(t, err)
			approval, err := DecodeCNIntegrityEligibilitySnapshot(raw)
			require.NoError(t, err)
			streamID := fmt.Sprintf("11111111-1111-4111-8111-11111111110%d", i+1)
			epochID := fmt.Sprintf("22222222-2222-4222-8222-22222222220%d", i+1)
			require.Equal(t, streamID, approval.Body.StreamEpoch)
			require.Equal(t, epochID, approval.Body.IntegrityEpoch)
			require.Equal(t, "test-cn", approval.Body.GatewayID)
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Fatal("required real database unavailable")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			if dialect == "sqlite" {
				require.NoError(t, pool.Close())
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "real-export.db")), &gorm.Config{})
				require.NoError(t, err)
				pool, err = db.DB()
				require.NoError(t, err)
				pool.SetMaxOpenConns(1)
			}
			t.Cleanup(func() { _ = pool.Close() })
			oldDB, oldType := DB, common.MainDatabaseType()
			DB = db
			switch dialect {
			case "mysql":
				common.SetMainDatabaseType(common.DatabaseTypeMySQL)
			case "postgres":
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			default:
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			}
			t.Cleanup(func() { DB = oldDB; common.SetMainDatabaseType(oldType) })
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			var dbVersion string
			require.NoError(t, db.Raw(query).Scan(&dbVersion).Error)
			t.Log(dbVersion)
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &Task{}, &TaskBillingReconciliation{}, &TopUp{}, &Option{}, &Log{}))
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			require.NoError(t, db.Model(&CNFinanceStream{}).Where("id = ?", 1).Update("epoch", streamID).Error)
			for _, user := range []struct{ id, quota int }{{1001, -3}, {1002, 4}, {1003, 0}} {
				name := fmt.Sprintf("integrity-real-%d", user.id)
				require.NoError(t, db.Create(&User{Id: user.id, Username: name, AffCode: name, Quota: user.quota, Status: common.UserStatusEnabled}).Error)
				require.NoError(t, db.Create(&Token{Id: user.id, UserId: user.id, Key: name, RemainQuota: 10000, Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
				require.NoError(t, db.Create(&Channel{Id: user.id, Name: name}).Error)
			}
			_, err = PreConsumeCNBoundWallet("real-opening-hold", 1002, 1, 1002, false, false)
			require.NoError(t, err)
			epoch, err := PrepareCNIntegrityCapture(db, "real-test-local-operator-prepare")
			require.NoError(t, err)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&CNIntegrityEpoch{}).Where("epoch_id = ?", epoch.EpochID).Update("epoch_id", epochID).Error; err != nil {
					return err
				}
				return tx.Model(&CNIntegrityControlReceipt{}).Where("epoch_id = ?", epoch.EpochID).Update("epoch_id", epochID).Error
			}))
			require.NoError(t, ImportCNIntegrityEligibilitySnapshot(db, raw, "explicit-local-operator-import:"+approval.OpsInventoryID))
			for _, user := range []int{1001, 1002} {
				_, err := AdmitCNIntegrityUser(db, epochID, user, 1, approval.OpsInventoryID)
				require.NoError(t, err)
			}
			attestation := CNIntegrityDrainAttestation{InventoryID: "test-isolated-process-inventory", InventoryHash: "sha256:" + strings.Repeat("a", 64), OperatorEvidenceID: "test-no-external-writers-attestation", OldWritersDrained: true, RedisDisabled: true, BatchDisabled: true, ExternalSQLExcluded: true, UnguardedHandlesExcluded: true}
			writers := []CNIntegrityWriterDeclaration{{WriterID: "real-three-engine-writer", BuildID: "s4-native-integrity-v2-test", LeaseUntil: time.Now().UTC().Add(time.Hour).Format(CNTimeLayout), ProtocolVersion: cnIntegrityWriterProtocol, CapabilityPaths: []string{"quota_mutation", "source_switch", "task_reserve", "task_revision", "topup_credit", "wallet_final", "wallet_release", "wallet_reserve"}}}
			require.NoError(t, StartCNIntegrityCapture(db, epochID, 1, writers, attestation))
			request := CNIntegrityFreezeRequest{EpochID: epochID, ExpectedGeneration: 2, CoverageFrom: time.Now().UTC().Add(-time.Hour).Format(CNTimeLayout), CoverageTo: time.Now().UTC().Add(time.Hour).Format(CNTimeLayout)}
			freeze := func(want string) {
				sealed, err := FreezeCNIntegrity(context.Background(), db, request)
				require.NoError(t, err)
				var manifest map[string]any
				require.NoError(t, common.Unmarshal([]byte(sealed.Manifest), &manifest))
				require.Equal(t, want, manifest["status"])
				cnWriteIntegrityRawSample(t, db, sealed, dialect, dbVersion, raw)
				request.ExpectedParentProofID = sealed.ProofID
			}
			freeze("pending")
			credit := func(user, quota int, trade string) {
				order := TopUp{UserId: user, Amount: int64(quota), TradeNo: trade, PaymentProvider: PaymentProviderCreem, Status: common.TopUpStatusPending}
				require.NoError(t, db.Create(&order).Error)
				require.NoError(t, RechargeCreem(trade, "", "", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "real-receipt-" + trade}))
			}
			publish := func() { _, err := ReadCNFacts(context.Background(), db, "", 1000); require.NoError(t, err) }
			credit(1001, 5, "real-debt-topup")
			cnAssertIntegrityPublicationTransition(t, db, request)
			publish()
			freeze("complete")
			seed := &Task{TaskID: "real-revision-task", UserId: 1001, ChannelId: 1001, Platform: constant.TaskPlatform("alibaba"), Status: TaskStatusSubmitted, CreatedAt: time.Now().Unix()}
			task, err := PrepareCNTask(seed, "real-revision-request", TaskBillingSourceWallet, 1, 1001, false, false)
			require.NoError(t, err)
			seed.CNBillingKey, seed.Quota = task.Key, 1
			seed.PrivateData.TokenId, seed.PrivateData.BillingSource = 1001, TaskBillingSourceWallet
			seed.PrivateData.Execution = &TaskExecutionSnapshot{RequestID: task.RequestID}
			require.NoError(t, BeginCNTaskDispatch(task.Key, strings.Repeat("b", 64), 1001, "alibaba"))
			seed.PrivateData.UpstreamTaskID = "real-provider-task"
			require.NoError(t, RecordCNTaskUpstream(task.Key, seed.PrivateData.UpstreamTaskID))
			require.NoError(t, seed.Insert())
			for _, target := range []int{1, 0, 1} {
				require.NoError(t, RequestCNTaskAdjustment(seed, target, false))
				require.NoError(t, CompleteCNTaskIntent(context.Background(), db, task.Key))
				require.NoError(t, db.First(seed, seed.ID).Error)
				publish()
				freeze("complete")
			}
			_, err = AdmitCNIntegrityUser(db, epochID, 1003, 1, approval.OpsInventoryID)
			require.ErrorIs(t, err, ErrCNConflict)
			_, err = AdmitCNIntegrityUser(db, epochID, 1003, 2, approval.OpsInventoryID)
			require.NoError(t, err)
			credit(1003, 3, "real-later-admitted-topup")
			require.NoError(t, FinalizeCNBoundWallet("real-opening-hold", 1002, 5))
			publish()
			freeze("complete")
			require.NoError(t, StopCNIntegrityCapture(db, epochID, 2, "paused", "real-test-operator-pause"))
			request.ExpectedGeneration = 3
			credit(1003, 1, "real-paused-unknown-topup")
			publish()
			freeze("complete")
		})
	}
}

func cnAssertIntegrityPublicationTransition(t *testing.T, db *gorm.DB, request CNIntegrityFreezeRequest) {
	t.Helper()
	rollback := errors.New("rollback publication transition fixture")
	var headBefore CNIntegrityFreezeHead
	var epochBefore CNIntegrityEpoch
	require.NoError(t, db.First(&headBefore, 1).Error)
	require.NoError(t, db.Where("epoch_id = ?", request.EpochID).First(&epochBefore).Error)
	err := db.Transaction(func(tx *gorm.DB) error {
		pending, err := FreezeCNIntegrity(context.Background(), tx, request)
		require.NoError(t, err)
		var pendingManifest map[string]any
		require.NoError(t, common.Unmarshal([]byte(pending.Manifest), &pendingManifest))
		require.Equal(t, "pending", pendingManifest["status"])
		var before CNIntegritySnapshotCollection
		require.NoError(t, tx.Where("proof_id = ? AND collection = ?", pending.ProofID, "dispositions").First(&before).Error)
		_, err = ReadCNFacts(context.Background(), tx, "", 1000)
		require.NoError(t, err)
		request.ExpectedParentProofID = pending.ProofID
		complete, err := FreezeCNIntegrity(context.Background(), tx, request)
		require.NoError(t, err)
		var completeManifest map[string]any
		require.NoError(t, common.Unmarshal([]byte(complete.Manifest), &completeManifest))
		require.Equal(t, "complete", completeManifest["status"])
		var after CNIntegritySnapshotCollection
		require.NoError(t, tx.Where("proof_id = ? AND collection = ?", complete.ProofID, "dispositions").First(&after).Error)
		require.Equal(t, before.Payload, after.Payload, "publication cannot change captured dispositions")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var headAfter CNIntegrityFreezeHead
	var epochAfter CNIntegrityEpoch
	require.NoError(t, db.First(&headAfter, 1).Error)
	require.NoError(t, db.Where("epoch_id = ?", request.EpochID).First(&epochAfter).Error)
	require.Equal(t, headBefore, headAfter)
	require.Equal(t, epochBefore.ValueSequence, epochAfter.ValueSequence)
}

func cnWriteIntegrityRawSample(t *testing.T, db *gorm.DB, sealed *CNIntegritySnapshot, dialect, dbVersion string, approval []byte) {
	t.Helper()
	dir := filepath.Join("testdata", "cn-task", "integrity-v2-real", dialect, "batch-"+strconv.FormatInt(sealed.BatchSequence, 10))
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(sealed.Manifest), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ops-approved-eligibility.json"), approval, 0600))
	for _, name := range cnIntegrityCollections {
		ordinal := int64(0)
		for {
			page, err := ReadCNIntegrityPage(context.Background(), db, sealed.ProofID, name, ordinal, 2)
			require.NoError(t, err)
			raw, err := CNIntegrityExportJSON(page)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name+"-"+strconv.FormatInt(ordinal, 10)+".json"), raw, 0600))
			if page["nextOrdinal"].(*string) == nil {
				break
			}
			ordinal, err = strconv.ParseInt(*page["nextOrdinal"].(*string), 10, 64)
			require.NoError(t, err)
		}
	}
	index, err := ReadCNIntegrityIndex(context.Background(), db, "test-cn", sealed.FreezeSequence-1, 1)
	require.NoError(t, err)
	raw, err := CNIntegrityExportJSON(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), raw, 0600))
	raw, err = common.Marshal(map[string]any{"sourceTest": t.Name(), "databaseVersion": dbVersion, "proofId": sealed.ProofID, "nativeFixture": true, "productionData": false, "syntheticIntegration": true, "productionEligibility": false, "financialGrants": 0, "opsApprovalOrigin": "independently-frozen-reviewed-exported-Ops-fixture", "operatorImport": "explicit-local-operator-import"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0600))
}
