package migration_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/config"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/migration"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMigrationsOnConfiguredServerDatabases(t *testing.T) {
	testCases := []struct {
		name   string
		driver string
		envKey string
	}{
		{name: "mysql", driver: "mysql", envKey: "CLAW_TEST_MYSQL_DSN"},
		{name: "postgres", driver: "postgres", envKey: "CLAW_TEST_POSTGRES_DSN"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			dsn := os.Getenv(testCase.envKey)
			if dsn == "" {
				t.Skip(testCase.envKey + " is not configured")
			}
			db, err := database.Open(config.Config{
				DBDriver: testCase.driver, DBDSN: dsn,
				DBMaxOpenConns: 5, DBMaxIdleConns: 2, DBConnMaxLifetime: time.Minute,
			})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

			require.NoError(t, migration.Migrate(db))
			require.NoError(t, migration.Migrate(db), "all migrations must be idempotent")
			for _, table := range []any{
				&model.Customer{}, &model.CustomerMember{}, &model.IdentityBinding{}, &model.ADPAccountBinding{},
				&model.CustomerApp{}, &model.PlanPeriod{}, &model.ControlOutbox{},
				&model.ResourceBinding{}, &model.AdminSession{},
				&model.EvidenceObject{}, &model.ContextSelectionNonce{},
				&model.CredentialProfile{},
				&model.TencentBillingImportRun{},
				&model.TencentBillingImportCoordinator{},
				&model.UsageAuditRevision{},
			} {
				assert.True(t, db.Migrator().HasTable(table))
			}
			assert.True(t, db.Migrator().HasIndex(&model.CustomerMember{}, "idx_claw_member_user_slot"))

			customer := model.Customer{
				CustomerCode: "cross-db-" + testCase.name, DisplayName: "Cross DB " + testCase.name,
				Status: model.CustomerStatusActive, RowVersion: 1,
			}
			require.NoError(t, db.Where("customer_code = ?", customer.CustomerCode).FirstOrCreate(&customer).Error)
			profileName := fmt.Sprintf("cross-db-scope-%d", time.Now().UnixNano())
			resolver := testutil.SecretResolver{
				"env://WORKBENCH_PROVIDER_CROSS_DB_PLATFORM_ID": "platform-id", "env://WORKBENCH_PROVIDER_CROSS_DB_PLATFORM_KEY": "platform-key",
				"env://WORKBENCH_PROVIDER_CROSS_DB_CUSTOMER_ID": "customer-id", "env://WORKBENCH_PROVIDER_CROSS_DB_CUSTOMER_KEY": "customer-key",
			}
			platformProfile, err := credential.New(db, resolver).Create(credential.CreateCommand{
				ProviderEnvironment: model.ProviderChinaTencentCloud, Name: profileName,
				SecretIDRef: "env://WORKBENCH_PROVIDER_CROSS_DB_PLATFORM_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_CROSS_DB_PLATFORM_KEY",
				Fingerprint: secrets.CredentialPairFingerprint("platform-id", "platform-key"), Actor: "cross-db-test",
			})
			require.NoError(t, err)
			customerProfile, err := credential.New(db, resolver).Create(credential.CreateCommand{
				OwnerScope: credential.CustomerOwnerScope(customer.ID), CustomerID: &customer.ID,
				ProviderEnvironment: model.ProviderChinaTencentCloud, Name: profileName,
				SecretIDRef: "env://WORKBENCH_PROVIDER_CROSS_DB_CUSTOMER_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_CROSS_DB_CUSTOMER_KEY",
				Fingerprint: secrets.CredentialPairFingerprint("customer-id", "customer-key"), Actor: "cross-db-test",
			})
			require.NoError(t, err)
			assert.Equal(t, int64(1), platformProfile.Version)
			assert.Equal(t, int64(1), customerProfile.Version)
			evidenceService, err := evidence.New(db, t.TempDir(), bytes.Repeat([]byte{0x41}, 32), 1024,
				evidence.ScannerFunc(func(context.Context, []byte) error { return nil }))
			require.NoError(t, err)
			proof, err := evidenceService.Upload(evidence.UploadCommand{
				CustomerID: &customer.ID, Filename: "cross-db.txt", DeclaredMIME: "text/plain",
				Content: strings.NewReader("portable evidence"), Actor: "cross-db-test",
			})
			require.NoError(t, err)
			download, err := evidenceService.Get(proof.EvidenceRef)
			require.NoError(t, err)
			assert.Equal(t, "portable evidence", string(download.Content))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var locked model.Customer
				if err := database.ForUpdate(tx).First(&locked, customer.ID).Error; err != nil {
					return err
				}
				return tx.Model(&locked).Update("row_version", locked.RowVersion+1).Error
			}))
		})
	}
}
