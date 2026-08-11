package migration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/config"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/migration"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type legacyCredentialProfile struct {
	ID                  uint64 `gorm:"primaryKey"`
	ProviderEnvironment string `gorm:"type:varchar(48);uniqueIndex:idx_claw_credential_version,priority:1;not null"`
	Name                string `gorm:"type:varchar(120);uniqueIndex:idx_claw_credential_version,priority:2;not null"`
	SecretIDRef         string `gorm:"type:varchar(255);not null"`
	SecretKeyRef        string `gorm:"type:varchar(255);not null"`
	Fingerprint         string `gorm:"type:varchar(128);not null"`
	Status              string `gorm:"type:varchar(24);not null"`
	Version             int64  `gorm:"uniqueIndex:idx_claw_credential_version,priority:3;not null"`
	PreviousProfileID   *uint64
	RowVersion          int64
	ActivatedAt         *time.Time
	RotatedAt           *time.Time
	RetiredAt           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (legacyCredentialProfile) TableName() string { return "claw_credential_profiles" }

func TestSSOBrowserBindingMigrationIsAppliedAndIdempotent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	assert.True(t, db.Migrator().HasColumn(&model.SSOTicket{}, "BrowserBindingHash"))

	var count int64
	require.NoError(t, db.Model(&migration.SchemaMigration{}).
		Where("version = ?", migration.SSOBrowserBindingVersion).Count(&count).Error)
	assert.EqualValues(t, 1, count)

	require.NoError(t, migration.Migrate(db))
	require.NoError(t, db.Model(&migration.SchemaMigration{}).
		Where("version = ?", migration.SSOBrowserBindingVersion).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestAgentStoreMigrationCreatesPortableCatalogConstraints(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	require.NoError(t, migration.Migrate(db))
	var count int64
	require.NoError(t, db.Model(&migration.SchemaMigration{}).Where("version = ?", migration.AgentStoreVersion).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	for _, value := range []any{
		&model.AgentCatalogItem{}, &model.AgentCatalogVersion{}, &model.CustomerAgentDeployment{},
		&model.AgentCatalogEntitlement{}, &model.AgentLaunchAudit{}, &model.AgentCatalogCursor{},
	} {
		assert.True(t, db.Migrator().HasTable(value))
	}
	first := model.AgentCatalogItem{ID: "agi_one", Slug: "unique-agent", Status: model.AgentCatalogStatusDraft, RowVersion: 1, CreatedBy: "test"}
	second := model.AgentCatalogItem{ID: "agi_two", Slug: "unique-agent", Status: model.AgentCatalogStatusDraft, RowVersion: 1, CreatedBy: "test"}
	require.NoError(t, db.Create(&first).Error)
	assert.Error(t, db.Create(&second).Error)
}

func TestAgentStoreRuntimeContractUpgradesAlreadyMigratedSchemas(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	for _, column := range []string{"Purpose", "AgentCatalogItemID", "AgentDeploymentID", "CatalogVersionID", "CatalogRowVersion", "DeploymentVersion"} {
		require.NoError(t, db.Migrator().DropColumn(&model.ContextSelectionNonce{}, column), column)
	}
	for _, column := range []string{"TargetProviderAppMode", "TargetRuntimeProfile", "TargetExecutionEnabled"} {
		require.NoError(t, db.Migrator().DropColumn(&model.AppMigrationJob{}, column), column)
	}
	require.NoError(t, db.Where("version = ?", migration.AgentStoreRuntimeContractVersion).Delete(&migration.SchemaMigration{}).Error)
	require.NoError(t, migration.Migrate(db))

	for _, column := range []string{"Purpose", "AgentCatalogItemID", "AgentDeploymentID", "CatalogVersionID", "CatalogRowVersion", "DeploymentVersion"} {
		assert.True(t, db.Migrator().HasColumn(&model.ContextSelectionNonce{}, column), column)
	}
	for _, column := range []string{"TargetProviderAppMode", "TargetRuntimeProfile", "TargetExecutionEnabled"} {
		assert.True(t, db.Migrator().HasColumn(&model.AppMigrationJob{}, column), column)
	}
	nonce := model.ContextSelectionNonce{
		TokenHash: strings.Repeat("a", 64), ControlSessionID: 1, NewAPIUserID: 1, IdentityBindingID: 1,
		CustomerMemberID: 1, CustomerID: 1, CustomerAppID: 1, AppConfigVersionID: 1,
		IdentityVersion: "v1", IdentityAuthEpoch: 1, MemberAuthEpoch: 1, AppAuthEpoch: 1,
		Purpose: "agent_store_launch", AgentCatalogItemID: "agi_upgrade", AgentDeploymentID: "agd_upgrade",
		CatalogVersionID: "agv_upgrade", CatalogRowVersion: 2, DeploymentVersion: 3, ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	require.NoError(t, db.Create(&nonce).Error)
	var storedNonce model.ContextSelectionNonce
	require.NoError(t, db.First(&storedNonce, nonce.ID).Error)
	assert.Equal(t, nonce.Purpose, storedNonce.Purpose)
	assert.Equal(t, nonce.CatalogVersionID, storedNonce.CatalogVersionID)
	assert.Equal(t, nonce.DeploymentVersion, storedNonce.DeploymentVersion)

	job := model.AppMigrationJob{
		PublicID: "amj_upgrade", CustomerID: 1, SourceCustomerAppID: 1, TargetCustomerAppID: 2, Generation: 1,
		TargetAppConfigVersionID: 1, TargetConfigVersion: 1, TargetCredentialProfileID: 1,
		TargetConfigFingerprint: "sha256:" + strings.Repeat("b", 64), MemberSetFingerprint: "sha256:" + strings.Repeat("c", 64),
		TargetProviderAppMode: 4, TargetRuntimeProfile: "claw_dynamic_v2", TargetExecutionEnabled: true,
		Status: model.AppMigrationJobStatusPending, RowVersion: 1,
	}
	require.NoError(t, db.Create(&job).Error)
	var storedJob model.AppMigrationJob
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	assert.Equal(t, 4, storedJob.TargetProviderAppMode)
	assert.Equal(t, "claw_dynamic_v2", storedJob.TargetRuntimeProfile)
	assert.True(t, storedJob.TargetExecutionEnabled)
}

func TestEncryptedEvidenceMigrationIsAppliedAndIdempotent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	assert.True(t, db.Migrator().HasTable(&model.EvidenceObject{}))

	var count int64
	require.NoError(t, db.Model(&migration.SchemaMigration{}).
		Where("version = ?", migration.EvidenceStoreVersion).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, migration.Migrate(db))
}

func TestMultiContextMigrationIsAppliedAndIdempotent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	assert.True(t, db.Migrator().HasTable(&model.ContextSelectionNonce{}))
	assert.True(t, db.Migrator().HasColumn(&model.ControlSession{}, "SelectionState"))
	assert.True(t, db.Migrator().HasColumn(&model.SSOTicket{}, "ControlSessionID"))
	assert.True(t, db.Migrator().HasColumn(&model.CustomerApp{}, "Selector"))
	assert.True(t, db.Migrator().HasColumn(&model.CustomerApp{}, "Alias"))
	assert.False(t, db.Migrator().HasIndex(&model.CustomerMember{}, "idx_claw_member_primary"))

	var count int64
	require.NoError(t, db.Model(&migration.SchemaMigration{}).
		Where("version = ?", migration.MultiContextVersion).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, migration.Migrate(db))
}

func TestMembershipScopeMigrationEnforcesCustomerScopedUniqueness(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	require.NoError(t, db.Migrator().DropIndex(&model.CustomerMember{}, "idx_claw_member_user_slot"))
	require.NoError(t, db.Where("version = ?", migration.MembershipScopeVersion).Delete(&migration.SchemaMigration{}).Error)

	first := model.CustomerMember{
		CustomerID: 1, NewAPIUserID: 42, Role: "member", Status: model.MemberStatusActive,
		MembershipSlot: "primary", AuthEpoch: 1,
	}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, migration.Migrate(db))
	require.NoError(t, db.First(&first, first.ID).Error)
	assert.Equal(t, model.MembershipSlot(1), first.MembershipSlot)
	assert.True(t, db.Migrator().HasIndex(&model.CustomerMember{}, "idx_claw_member_user_slot"))

	duplicateScope := model.CustomerMember{
		CustomerID: 2, NewAPIUserID: 42, Role: "member", Status: model.MemberStatusActive,
		MembershipSlot: model.MembershipSlot(1), AuthEpoch: 1,
	}
	assert.Error(t, db.Create(&duplicateScope).Error, "the same user cannot reuse a customer membership scope")
	validOtherCustomer := model.CustomerMember{
		CustomerID: 2, NewAPIUserID: 42, Role: "member", Status: model.MemberStatusActive,
		MembershipSlot: model.MembershipSlot(2), AuthEpoch: 1,
	}
	require.NoError(t, db.Create(&validOtherCustomer).Error, "the same user may belong to a different customer scope")

	var count int64
	require.NoError(t, db.Model(&migration.SchemaMigration{}).
		Where("version = ?", migration.MembershipScopeVersion).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, migration.Migrate(db), "membership-scope migration must be idempotent")
}

func TestTencentBillingImportMigrationIsAppliedAndIdempotent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	assert.True(t, db.Migrator().HasTable(&model.TencentBillingImportRun{}))
	assert.True(t, db.Migrator().HasTable(&model.TencentBillingImportCoordinator{}))
	assert.True(t, db.Migrator().HasColumn(&model.UsageAudit{}, "ImportSourceKey"))

	var count int64
	require.NoError(t, db.Model(&migration.SchemaMigration{}).
		Where("version = ?", migration.TencentBillingImportVersion).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, migration.Migrate(db))
}

func TestUsageAuditRevisionMigrationIsAppliedAndIdempotent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	assert.True(t, db.Migrator().HasTable(&model.UsageAuditRevision{}))

	var count int64
	require.NoError(t, db.Model(&migration.SchemaMigration{}).
		Where("version = ?", migration.UsageAuditRevisionVersion).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, migration.Migrate(db))
}

func TestCredentialOwnerScopeMigrationBackfillsPlatformAndUsesScopeSafeUniqueness(t *testing.T) {
	db, err := database.Open(config.Config{
		DBDriver: "sqlite", DBDSN: fmt.Sprintf("file:credential-scope-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", time.Now().UnixNano()),
		DBMaxOpenConns: 1, DBMaxIdleConns: 1, DBConnMaxLifetime: time.Minute,
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&migration.SchemaMigration{}, &legacyCredentialProfile{}))
	legacy := legacyCredentialProfile{
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: "shared-name",
		SecretIDRef: "env://WORKBENCH_PROVIDER_PLATFORM_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_PLATFORM_KEY",
		Fingerprint: "sha256:platform", Status: model.CredentialStatusActive, Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&legacy).Error)
	for _, version := range []string{
		migration.InitialSchemaVersion, migration.InternalIdentityVersion, migration.CredentialIndexVersion,
		migration.IdentityVersion, migration.BrowserSessionVersion, migration.AdminSessionVersion,
		migration.ADPAccountBindingVersion, migration.SSOBrowserBindingVersion, migration.ResourceBindingVersion,
		migration.EvidenceStoreVersion, migration.GovernanceVersion, migration.MultiContextVersion,
		migration.TencentBillingImportVersion, migration.MembershipScopeVersion,
	} {
		require.NoError(t, db.Create(&migration.SchemaMigration{Version: version, AppliedAt: time.Now().UTC()}).Error)
	}

	require.NoError(t, migration.Migrate(db))
	require.NoError(t, migration.Migrate(db), "owner-scope migration must be idempotent")
	var migrated model.CredentialProfile
	require.NoError(t, db.First(&migrated, legacy.ID).Error)
	assert.Equal(t, credential.PlatformOwnerScope, migrated.OwnerScope)
	assert.Nil(t, migrated.CustomerID)
	assert.Equal(t, 0, migrated.FingerprintVersion, "legacy fingerprints remain explicitly untrusted until admin re-enrollment")
	assert.True(t, db.Migrator().HasColumn(&model.CredentialProfile{}, "OwnerScope"))
	assert.True(t, db.Migrator().HasColumn(&model.CredentialProfile{}, "CustomerID"))
	assert.True(t, db.Migrator().HasIndex(&model.CredentialProfile{}, "idx_claw_credential_owner_version"))

	customerID := uint64(42)
	customerProfile := model.CredentialProfile{
		OwnerScope: credential.CustomerOwnerScope(customerID), CustomerID: &customerID,
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: legacy.Name,
		SecretIDRef: "env://WORKBENCH_PROVIDER_CUSTOMER_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_CUSTOMER_KEY",
		Fingerprint: "sha256:customer", Status: model.CredentialStatusActive, Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&customerProfile).Error)
	assert.NotEqual(t, migrated.ID, customerProfile.ID)
	duplicate := customerProfile
	duplicate.ID = 0
	assert.Error(t, db.Create(&duplicate).Error, "owner/provider/name/version must be unique without nullable columns")
	invalidScope := customerProfile
	invalidScope.ID = 0
	invalidScope.Name = "invalid-empty-scope"
	invalidScope.OwnerScope = ""
	assert.Error(t, db.Create(&invalidScope).Error, "SQLite must enforce the non-empty owner scope constraint")
}
