package agencyhub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestExportConcurrentDownloadAdmissionSharesActorBudgetAcrossSessions(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var open gorm.Dialector
			if dialect == "sqlite" {
				open = sqlite.Open(filepath.Join(t.TempDir(), "download-admission.sqlite") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
			} else {
				dsn := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN"))
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("isolated external test database is not configured")
				}
				if dialect == "mysql" {
					open = mysql.Open(dsn)
				} else {
					open = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(open, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(4)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, model.MigrateAgency(db))
			app := New(db, db, Config{})
			actorID := time.Now().UnixNano()
			agencies := []model.Agency{
				{Code: "export-race-" + common.GetUUID(), InviteCode: common.GetUUID()[:16], DisplayName: "first", Status: AgencyStatusActive, StateRevision: 1},
				{Code: "export-race-" + common.GetUUID(), InviteCode: common.GetUUID()[:16], DisplayName: "second", Status: AgencyStatusActive, StateRevision: 1},
			}
			require.NoError(t, db.Create(&agencies).Error)
			t.Cleanup(func() {
				require.NoError(t, db.Where("id IN ?", []int64{agencies[0].ID, agencies[1].ID}).Delete(&model.Agency{}).Error)
			})
			sessions := []model.AgencySession{
				{ActorType: ActorTypeRoot, ActorID: actorID, AgencyID: &agencies[0].ID, TokenHash: common.GetUUID(), CSRFHash: common.GetUUID(), ExpiresAt: time.Now().Unix() + 3600},
				{ActorType: ActorTypeRoot, ActorID: actorID, AgencyID: &agencies[1].ID, TokenHash: common.GetUUID(), CSRFHash: common.GetUUID(), ExpiresAt: time.Now().Unix() + 3600},
			}
			require.NoError(t, db.Create(&sessions).Error)
			t.Cleanup(func() {
				require.NoError(t, db.Where("id IN ?", []int64{sessions[0].ID, sessions[1].ID}).Delete(&model.AgencySession{}).Error)
			})
			jobs := make([]model.AgencyExportJob, 2)
			for index := range jobs {
				jobs[index] = model.AgencyExportJob{ActorType: ActorTypeRoot, ActorID: actorID, AgencyID: &agencies[index].ID, PermissionVersion: 1, Kind: "usage", FilterJSON: `{}`, Status: "ready", FileKey: "verified.csv", FileHash: "verified-hash", DownloadTokenHash: tokenHash("token"), DownloadTokenSessionID: sessions[index].ID, DownloadTokenExpiresAt: time.Now().Unix() + 600, ExpiresAt: time.Now().Unix() + 3600}
			}
			require.NoError(t, db.Create(&jobs).Error)
			t.Cleanup(func() {
				require.NoError(t, db.Where("id IN ?", []int64{jobs[0].ID, jobs[1].ID}).Delete(&model.AgencyExportJob{}).Error)
			})
			audits := make([]model.AgencyAuditLog, 19)
			for index := range audits {
				audits[index] = model.AgencyAuditLog{EventID: "export-seed-" + common.GetUUID(), ActorType: ActorTypeRoot, ActorID: actorID, Action: "export.download", ObjectType: "export", ObjectID: stringID(jobs[0].ID), ActingAgencyID: &agencies[0].ID, CreatedAtMS: time.Now().UnixMilli()}
			}
			require.NoError(t, db.Create(&audits).Error)
			t.Cleanup(func() {
				require.NoError(t, db.Where("actor_type = ? AND actor_id = ?", ActorTypeRoot, actorID).Delete(&model.AgencyAuditLog{}).Error)
			})
			start := make(chan struct{})
			results := make(chan error, 2)
			for index := range jobs {
				go func(index int) {
					context, _ := gin.CreateTestContext(httptest.NewRecorder())
					context.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/exports/%d/download", jobs[index].ID), nil)
					identity := &Identity{ActorType: ActorTypeRoot, ActorID: actorID, AgencyID: &agencies[index].ID, SessionID: sessions[index].ID}
					<-start
					results <- app.authorizeExportDownload(context, identity, &jobs[index], "token")
				}(index)
			}
			close(start)
			first, second := <-results, <-results
			if first == nil {
				require.ErrorIs(t, second, errExportRateLimited)
			} else {
				require.ErrorIs(t, first, errExportRateLimited)
				require.NoError(t, second)
			}
			var count int64
			require.NoError(t, db.Model(&model.AgencyAuditLog{}).Where("actor_type = ? AND actor_id = ? AND action = ?", ActorTypeRoot, actorID, "export.download").Count(&count).Error)
			assert.Equal(t, int64(20), count, "two concurrent sessions spend exactly one remaining admission")
		})
	}
}
