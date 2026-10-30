package model

import (
	"os"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMigrateAgencySQLiteIsIdempotentAndIndexesReportingFacts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-migration-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, MigrateAgency(db))
	require.NoError(t, MigrateAgency(db))

	for _, item := range AgencyModels() {
		statement := &gorm.Statement{DB: db}
		require.NoError(t, statement.Parse(item))
		require.NotNil(t, statement.Schema)
		require.True(t, strings.HasPrefix(statement.Schema.Table, AgencyTablePrefix))
		assert.True(t, db.Migrator().HasTable(statement.Schema.Table), statement.Schema.Table)
	}

	require.True(t, db.Migrator().HasIndex(&AgencyTopupFact{}, "idx_agency_topup_agency_time"))
	require.True(t, db.Migrator().HasIndex(&AgencyTopupFact{}, "idx_agency_topup_user_time"))
	require.True(t, db.Migrator().HasIndex(&AgencyOperatorIdentityBinding{}, "uidx_agency_operator_identity_operator"))
	require.True(t, db.Migrator().HasIndex(&AgencyOperatorIdentityBinding{}, "uidx_agency_operator_identity_user"))
	require.True(t, db.Migrator().HasIndex(&Agency{}, "idx_agency_depth_id"))
	require.True(t, db.Migrator().HasTable(&AgencyReconciliationRun{}))
	require.True(t, db.Migrator().HasTable(&AgencyArchiveManifest{}))
}

func TestMigrateAgencyWriterFenceUpgradesWithoutResettingState(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-writer-upgrade?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Agency{})) // Representative released schema without writer tables.
	require.False(t, db.Migrator().HasTable(&AgencyOpsWriterState{}))
	require.NoError(t, MigrateAgencyWriterFence(db))
	require.NoError(t, MigrateAgencyWriterFence(db))
	for _, table := range []any{&AgencyOpsWriterState{}, &AgencyOpsWriterTransition{}, &AgencyOpsSourceManifest{}} {
		assert.True(t, db.Migrator().HasTable(table))
	}
	require.False(t, db.Migrator().HasTable(&AgencyTopupFact{}), "narrow migration must not migrate unrelated agency data")
	var state AgencyOpsWriterState
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, AgencyWriterSourcePrimary, state.Mode)
	assert.EqualValues(t, 1, state.FenceVersion)
	require.NoError(t, db.Model(&state).Updates(map[string]any{"mode": AgencyWriterOpsSingle, "fence_version": 7}).Error)
	require.NoError(t, MigrateAgencyWriterFence(db))
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, AgencyWriterOpsSingle, state.Mode)
	assert.EqualValues(t, 7, state.FenceVersion)
	var count int64
	require.NoError(t, db.Model(&AgencyOpsWriterState{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestMigrateAgencyAddsOperatorIdentityBindingsToReleasedSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-identity-upgrade?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateAgency(db))
	require.NoError(t, db.Migrator().DropTable(&AgencyOperatorIdentityBinding{}))
	require.False(t, db.Migrator().HasTable(&AgencyOperatorIdentityBinding{}))

	require.NoError(t, MigrateAgency(db))
	require.NoError(t, MigrateAgency(db))
	require.True(t, db.Migrator().HasTable(&AgencyOperatorIdentityBinding{}))
	require.True(t, db.Migrator().HasIndex(&AgencyOperatorIdentityBinding{}, "uidx_agency_operator_identity_operator"))
	require.True(t, db.Migrator().HasIndex(&AgencyOperatorIdentityBinding{}, "uidx_agency_operator_identity_user"))

	first := AgencyOperatorIdentityBinding{OperatorAccountID: 1, AgencyID: 1, PlatformUserID: 10, CreatedAt: 1, UpdatedAt: 1}
	require.NoError(t, db.Create(&first).Error)
	require.Error(t, db.Create(&AgencyOperatorIdentityBinding{OperatorAccountID: 1, AgencyID: 2, PlatformUserID: 11, CreatedAt: 1, UpdatedAt: 1}).Error)
	require.Error(t, db.Create(&AgencyOperatorIdentityBinding{OperatorAccountID: 2, AgencyID: 2, PlatformUserID: 10, CreatedAt: 1, UpdatedAt: 1}).Error)
}

func TestMigrateAgencyAddsDirectoryPagingIndexToReleasedSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-directory-index-upgrade?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateAgency(db))

	parent := Agency{ID: 101, Depth: 1, Code: "legacy-parent", DisplayName: "Legacy parent", Status: "active", InviteCode: "legacy-parent-invite"}
	child := Agency{ID: 102, Depth: 2, ParentAgencyID: &parent.ID, Code: "legacy-child", DisplayName: "Legacy child", Status: "active", InviteCode: "legacy-child-invite"}
	require.NoError(t, db.Create(&parent).Error)
	require.NoError(t, db.Create(&child).Error)
	require.NoError(t, db.Migrator().DropIndex(&Agency{}, "idx_agency_depth_id"))
	require.False(t, db.Migrator().HasIndex(&Agency{}, "idx_agency_depth_id"))

	// Released databases already contain agencies; the upgrade must add only
	// the paging index and preserve their identity and hierarchy.
	require.NoError(t, MigrateAgency(db))
	require.NoError(t, MigrateAgency(db))
	require.True(t, db.Migrator().HasIndex(&Agency{}, "idx_agency_depth_id"))
	var rows []Agency
	require.NoError(t, db.Order("depth ASC, id ASC").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, parent.ID, rows[0].ID)
	assert.Equal(t, child.ID, rows[1].ID)
	assert.Equal(t, parent.ID, *rows[1].ParentAgencyID)
}

func TestMigrateAgencyExternalDatabaseCompatibility(t *testing.T) {
	if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
		t.Skip("set AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1 with a disposable test database to run")
	}

	tests := []struct {
		name string
		dsn  string
		open func(string) gorm.Dialector
	}{
		{
			name: "mysql",
			dsn:  strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_MYSQL_DSN")),
			open: func(dsn string) gorm.Dialector { return mysql.Open(dsn) },
		},
		{
			name: "postgres",
			dsn:  strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_POSTGRES_DSN")),
			open: func(dsn string) gorm.Dialector {
				return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.dsn == "" {
				t.Skipf("AGENCY_HUB_TEST_%s_DSN is not set", strings.ToUpper(test.name))
			}
			db, err := gorm.Open(test.open(test.dsn), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, MigrateAgency(db))
			require.NoError(t, MigrateAgency(db))
			require.NoError(t, MigrateAgencyWriterFence(db))
			require.NoError(t, MigrateAgencyWriterFence(db))
			require.True(t, db.Migrator().HasTable(&Agency{}))
			require.True(t, db.Migrator().HasIndex(&AgencyTopupFact{}, "idx_agency_topup_agency_time"))
			require.True(t, db.Migrator().HasIndex(&AgencyTopupFact{}, "idx_agency_topup_user_time"))
			require.True(t, db.Migrator().HasIndex(&AgencyOperatorIdentityBinding{}, "uidx_agency_operator_identity_operator"))
			require.True(t, db.Migrator().HasIndex(&AgencyOperatorIdentityBinding{}, "uidx_agency_operator_identity_user"))
			require.True(t, db.Migrator().HasIndex(&Agency{}, "idx_agency_depth_id"))
		})
	}
}

func TestMigrateAgencyPreservesCancelledHistoryAndAllowsAnotherCancelledAttempt(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-old-provisioning-index?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateAgency(db))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX uidx_agency_provision_user_status ON agency_hub_provisioning_jobs (user_id, status)").Error)
	first := AgencyProvisioningJob{UserID: 12, Status: "cancelled", Reason: "first attempt"}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, MigrateAgency(db))
	require.NoError(t, MigrateAgency(db))
	require.NoError(t, db.Create(&AgencyProvisioningJob{UserID: 12, Status: "cancelled", Reason: "second attempt"}).Error)
	var rows []AgencyProvisioningJob
	require.NoError(t, db.Order("id ASC").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, first.ID, rows[0].ID)
	assert.Equal(t, "first attempt", rows[0].Reason)
	assert.Equal(t, "second attempt", rows[1].Reason)
}

func TestAgencySourceEventIndexMigrationAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external test database is not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)
			require.NoError(t, db.Migrator().DropTable(&AgencySourceEvent{}))
			require.NoError(t, MigrateAgency(db))
			require.NoError(t, MigrateAgency(db))
			require.True(t, db.Migrator().HasIndex(&AgencySourceEvent{}, "idx_agency_source_operation_revision"))
			require.True(t, db.Migrator().HasIndex(&AgencySourceEvent{}, "idx_agency_source_user_status_seq"))
			require.True(t, db.Migrator().HasIndex(&AgencySourceEvent{}, "idx_agency_source_status_created"))
			for _, name := range []string{"uidx_agency_source_event", "idx_agency_source_user", "idx_agency_source_status"} {
				require.True(t, db.Migrator().HasIndex(&AgencySourceEvent{}, name), "existing index %s must be preserved", name)
			}

			// The released table has the same columns but lacks these indexes.
			require.NoError(t, db.Migrator().DropIndex(&AgencySourceEvent{}, "idx_agency_source_operation_revision"))
			require.NoError(t, db.Migrator().DropIndex(&AgencySourceEvent{}, "idx_agency_source_user_status_seq"))
			require.NoError(t, db.Migrator().DropIndex(&AgencySourceEvent{}, "idx_agency_source_status_created"))
			history := []AgencySourceEvent{
				{EventID: "index-original", SourceOperationID: "index-operation", JournalRevision: 1, SchemaVersion: "1", PayloadHash: "original-hash", UserID: 7, MoneySeq: 1, ProcessingStatus: "done", Payload: `{"event":"original"}`, CreatedAtMS: 1000},
				{EventID: "index-reversal", SourceOperationID: "index-operation", JournalRevision: 1, SchemaVersion: "1", PayloadHash: "reversal-hash", UserID: 7, MoneySeq: 1, ProcessingStatus: "skipped", Payload: `{"event":"reversal"}`, CreatedAtMS: 2000},
			}
			require.NoError(t, db.Create(&history).Error)
			require.NoError(t, MigrateAgency(db))
			require.NoError(t, MigrateAgency(db), "upgrading twice must preserve existing financial evidence")
			require.True(t, db.Migrator().HasIndex(&AgencySourceEvent{}, "idx_agency_source_operation_revision"))
			require.True(t, db.Migrator().HasIndex(&AgencySourceEvent{}, "idx_agency_source_user_status_seq"))
			require.True(t, db.Migrator().HasIndex(&AgencySourceEvent{}, "idx_agency_source_status_created"))
			var preserved []AgencySourceEvent
			require.NoError(t, db.Order("id ASC").Find(&preserved).Error)
			assert.Equal(t, history, preserved)

			var original AgencySourceEvent
			require.NoError(t, db.Where("source_operation_id = ? AND journal_revision = ? AND event_id <> ?", "index-operation", 1, "index-reversal").First(&original).Error)
			assert.Equal(t, history[0], original)
			var completed int64
			require.NoError(t, db.Model(&AgencySourceEvent{}).Where("user_id = ? AND money_seq > ? AND money_seq < ? AND processing_status IN ?", 7, 0, 2, []string{"done", "skipped"}).Count(&completed).Error)
			assert.Equal(t, int64(2), completed, "sequence dependency counts must preserve both source events")
			// Reversals can share the original operation and revision.
			next := AgencySourceEvent{EventID: "index-next-reversal", SourceOperationID: "index-operation", JournalRevision: 1, PayloadHash: "next-hash", UserID: 7, ProcessingStatus: "done", Payload: "{}", CreatedAtMS: 3000}
			require.NoError(t, db.Create(&next).Error, "the new index must not impose uniqueness on reversals")
			duplicate := next
			duplicate.ID = 0
			require.Error(t, db.Create(&duplicate).Error, "event IDs must remain unique")
		})
	}
}
