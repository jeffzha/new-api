package agencyhub

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func reconciliationConcurrentDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var dialector gorm.Dialector
	if dialect == "sqlite" {
		dialector = sqlite.Open(filepath.Join(t.TempDir(), "reconcile-race.sqlite") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	} else {
		dsn := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN"))
		if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
			t.Skip("isolated external test database is not configured")
		}
		if dialect == "mysql" {
			dialector = mysql.Open(dsn)
		} else {
			dialector = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, model.MigrateAgency(db))
	return db
}

// Concurrent reconciliation passes must converge on one open issue. This
// exercises the database uniqueness contract rather than relying on a
// count-then-insert race window.
func TestConcurrentReconciliationCreatesOneActiveIssueAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := reconciliationConcurrentDB(t, dialect)
			app := New(db, db, Config{})
			const objectType = "billing_outbox"
			objectID := common.GetUUID()
			t.Cleanup(func() {
				require.NoError(t, db.Where("object_type = ? AND object_id = ?", objectType, objectID).Delete(&model.AgencyReconciliationIssue{}).Error)
			})
			start, results := make(chan struct{}), make(chan error, 2)
			for worker := 0; worker < 2; worker++ {
				go func() {
					<-start
					_, err := app.createReconciliationIssue(context.Background(), objectType, objectID, "missing delivery")
					results <- err
				}()
			}
			close(start)
			require.NoError(t, <-results)
			require.NoError(t, <-results)
			var rows []model.AgencyReconciliationIssue
			require.NoError(t, db.Where("object_type = ? AND object_id = ? AND status = ?", objectType, objectID, "open").Find(&rows).Error)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].ActiveKey)
			assert.Equal(t, model.AgencyReconciliationActiveKey(objectType, objectID), *rows[0].ActiveKey)
		})
	}
}

func TestConcurrentReconciliationRepairReplaysOneAtomicResultAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := reconciliationConcurrentDB(t, dialect)
			app := New(db, db, Config{BasePath: "/agency", SessionIdle: time.Hour, SessionAbsolute: time.Hour})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
			root := model.User{Username: common.GetUUID()[:16], AffCode: common.GetUUID()[:16], Password: "unused-test-password", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
			require.NoError(t, db.Create(&root).Error)
			source := model.UserSession{SID: common.GetUUID(), UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: common.GetUUID(), LoginMethod: "password", LastActiveAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, db.Create(&source).Error)
			sessionToken, csrf, err := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
			require.NoError(t, err)
			publicKey, privateKey, err := ed25519.GenerateKey(nil)
			require.NoError(t, err)
			app.SetSSOPublicKey(publicKey)
			client := financeRootClient{app, privateKey, sessionToken, csrf, int64(root.Id), source.SID, source.Version}
			outbox := reconciliationOutboxFixture(t, db)
			issue := newReconciliationTestIssue(t, app, "billing_outbox", outbox.EventID)
			verification := reconciliationDetail(t, client, issue.ID)
			require.Equal(t, []string{"restore_delivery"}, verification.AllowedActions)
			body := reconciliationBody(t, "restore_delivery", "resolved", verification.EvidenceHash)
			path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
			proof := client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), common.GetUUID())
			key := common.GetUUID()
			var moneyBefore int64
			require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&moneyBefore).Error)
			start, results := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
			for worker := 0; worker < 2; worker++ {
				go func() {
					<-start
					results <- client.post(path, body, key, proof)
				}()
			}
			close(start)
			first, second := <-results, <-results
			// Serializable conflicts roll back proof/effects/audit together.
			// Retrying the same HTTP operation after both writers finish must
			// return the one committed result, without issuing a second proof.
			for _, response := range []*httptest.ResponseRecorder{first, second} {
				require.Contains(t, []int{http.StatusOK, http.StatusServiceUnavailable}, response.Code, response.Body.String())
			}
			if first.Code != http.StatusOK {
				first = client.post(path, body, key, proof)
			}
			if second.Code != http.StatusOK {
				second = client.post(path, body, key, proof)
			}
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			require.Equal(t, http.StatusOK, second.Code, second.Body.String())
			assert.Equal(t, first.Body.String(), second.Body.String())
			var deliveries []model.AgencyEventDelivery
			require.NoError(t, db.Where("event_id = ?", outbox.EventID).Find(&deliveries).Error)
			require.Len(t, deliveries, 1)
			assert.Equal(t, "pending", deliveries[0].Status)
			var audits, proofs, idempotency, moneyAfter int64
			require.NoError(t, db.Model(&model.AgencyAuditLog{}).Where("action = ? AND object_id = ?", "reconciliation.resolve", stringID(issue.ID)).Count(&audits).Error)
			require.NoError(t, db.Model(&model.AgencyVerificationUse{}).Where("object_id = ?", "reconciliation_issue:"+stringID(issue.ID)).Count(&proofs).Error)
			require.NoError(t, db.Model(&model.AgencyIdempotencyRecord{}).Where("action = ? AND resource_id = ?", "reconciliation.resolve", stringID(issue.ID)).Count(&idempotency).Error)
			require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&moneyAfter).Error)
			assert.Equal(t, int64(1), audits)
			assert.Equal(t, int64(1), proofs)
			assert.Equal(t, int64(1), idempotency)
			assert.Equal(t, moneyBefore, moneyAfter)
			var after model.AgencyReconciliationIssue
			require.NoError(t, db.First(&after, issue.ID).Error)
			assert.Equal(t, "resolved", after.Status)
			assert.Nil(t, after.ActiveKey)
			assert.Equal(t, outbox.EventID, after.RepairEventID)
			assert.NotEmpty(t, after.ResolutionEvidence)
		})
	}
}
