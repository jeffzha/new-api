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
	require.True(t, db.Migrator().HasTable(&AgencyReconciliationRun{}))
	require.True(t, db.Migrator().HasTable(&AgencyArchiveManifest{}))
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
			require.True(t, db.Migrator().HasTable(&Agency{}))
			require.True(t, db.Migrator().HasIndex(&AgencyTopupFact{}, "idx_agency_topup_agency_time"))
			require.True(t, db.Migrator().HasIndex(&AgencyTopupFact{}, "idx_agency_topup_user_time"))
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
