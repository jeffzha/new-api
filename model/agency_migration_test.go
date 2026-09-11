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
