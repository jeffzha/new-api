package agencyhub

import (
	"encoding/csv"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestExportLeaseTakeoverDoesNotPublishOrDeleteSuccessorFile(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agencyID := int64(5)
	job := model.AgencyExportJob{ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agencyID, Kind: "usage", FilterJSON: `{}`, Status: "processing", LeaseOwner: "original-claim", Attempts: 1, LeaseUntil: time.Now().Unix() + 300, ExpiresAt: time.Now().Unix() + 3600}
	require.NoError(t, app.db.Create(&job).Error)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{AgencyID: &agencyID, UserID: 1, EventID: "lease-fact", ComponentID: "default", OriginModelName: "demo", OccurredAtMS: 1}).Error)
	successorPath := filepath.Join(app.config.ExportDir, "successor.csv")
	claimed := false
	require.NoError(t, app.db.Callback().Query().After("gorm:query").Register("test:take-export-lease", func(tx *gorm.DB) {
		if claimed || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "AgencyUsageFact" {
			return
		}
		claimed = true
		require.NoError(t, os.WriteFile(successorPath, []byte("successor-content"), 0600))
		require.NoError(t, app.db.Model(&model.AgencyExportJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": "ready", "lease_owner": "", "attempts": 2, "file_key": "successor.csv", "file_hash": "successor-hash"}).Error)
	}))
	t.Cleanup(func() { require.NoError(t, app.db.Callback().Query().Remove("test:take-export-lease")) })
	require.ErrorIs(t, app.materializeExport(&job), errExportLeaseLost)
	require.True(t, claimed)
	require.NoError(t, app.failExport(&job, errExportRowLimit))
	var stored model.AgencyExportJob
	require.NoError(t, app.db.First(&stored, job.ID).Error)
	assert.Equal(t, "ready", stored.Status)
	assert.Equal(t, "successor.csv", stored.FileKey)
	assert.Equal(t, "successor-hash", stored.FileHash)
	content, err := os.ReadFile(successorPath)
	require.NoError(t, err)
	assert.Equal(t, "successor-content", string(content))
	files, err := os.ReadDir(app.config.ExportDir)
	require.NoError(t, err)
	require.Len(t, files, 1, "the stale worker removes only its own attempt artifacts")
}

func TestExportLeaseRenewalRejectsExpiredOrReplacedClaim(t *testing.T) {
	for _, takeover := range []string{"expired", "owner", "attempt"} {
		t.Run(takeover, func(t *testing.T) {
			app := newAgencyTestApp(t)
			job := model.AgencyExportJob{Status: "processing", Kind: "usage", LeaseOwner: "claim", Attempts: 1, LeaseUntil: time.Now().Unix() + 5}
			require.NoError(t, app.db.Create(&job).Error)
			require.NoError(t, app.renewExportLease(&job))
			var stored model.AgencyExportJob
			require.NoError(t, app.db.First(&stored, job.ID).Error)
			assert.Greater(t, stored.LeaseUntil, job.LeaseUntil)
			change := map[string]any{"lease_until": time.Now().Unix() - 1}
			if takeover == "owner" {
				change = map[string]any{"lease_owner": "next-claim"}
			}
			if takeover == "attempt" {
				change = map[string]any{"attempts": 2}
			}
			require.NoError(t, app.db.Model(&stored).Updates(change).Error)
			require.ErrorIs(t, app.renewExportLease(&job), errExportLeaseLost)
			require.NoError(t, app.failExport(&job, errExportRowLimit))
			require.NoError(t, app.db.First(&stored, job.ID).Error)
			assert.Equal(t, "processing", stored.Status)
		})
	}
}

func TestQueuedExportPastExpiryIsNotMaterialized(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	id := int64(5)
	job := model.AgencyExportJob{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &id, Kind: "usage", FilterJSON: `{}`, Status: "queued", ExpiresAt: time.Now().Unix() - 1}
	require.NoError(t, app.db.Create(&job).Error)
	count, err := app.ProcessExportJobs(1)
	require.NoError(t, err)
	assert.Zero(t, count)
	require.NoError(t, app.db.First(&job, job.ID).Error)
	assert.Equal(t, "expired", job.Status)
	assert.Empty(t, job.FileKey)
}

func exportHTTP(t *testing.T, app *App, identity *Identity, method, path, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	parts := strings.Split(strings.Split(path, "?")[0], "/")
	if len(parts) > 1 {
		last := parts[len(parts)-1]
		if last == "download" {
			last = parts[len(parts)-2]
		}
		c.Params = gin.Params{{Key: "id", Value: last}}
	}
	c.Set("agency_identity", identity)
	handler(c)
	return w
}

func TestExportCreateListDownloadScopesAndSessionProof(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agency, _, err := app.CreateAgency(1, "导出机构", "export-owner", agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000})
	require.NoError(t, err)
	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agency.ID, SessionID: 7}
	require.NoError(t, app.db.Create(&model.AgencySession{ID: identity.SessionID, ActorType: identity.ActorType, ActorID: identity.ActorID, AgencyID: identity.AgencyID, TokenHash: "export-workflow-session", CSRFHash: "export-workflow-csrf", ExpiresAt: time.Now().Unix() + 3600}).Error)
	create := exportHTTP(t, app, identity, http.MethodPost, "/exports", `{"kind":"usage","filter":{"start_date":"2026-09-01","end_date":"2026-09-02","user_id":"9007199254740993"}}`, app.createExport)
	require.Equal(t, http.StatusAccepted, create.Code, create.Body.String())
	var created struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(create.Body.Bytes(), &created))
	assert.Equal(t, "queued", created.Data.Status)
	require.NotEmpty(t, created.Data.ID)
	assert.NotContains(t, create.Body.String(), "LeaseOwner")
	assert.NotContains(t, create.Body.String(), "DownloadTokenHash")
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 28800)).UnixMilli()
	other := agency.ID + 1
	rows := []model.AgencyUsageFact{
		{EventID: "included", ComponentID: "default", AgencyID: &agency.ID, UserID: 9007199254740993, OriginModelName: "demo", InputTokens: 3, CacheReadTokens: 2, OccurredAtMS: start, SkipReason: "nonpaid"},
		{EventID: "outside-end", ComponentID: "default", AgencyID: &agency.ID, UserID: 9007199254740993, OriginModelName: "demo", OccurredAtMS: start + 2*86400000},
		{EventID: "other-agency", ComponentID: "default", AgencyID: &other, UserID: 9007199254740993, OriginModelName: "demo", OccurredAtMS: start},
	}
	require.NoError(t, app.db.Create(&rows).Error)
	processed, err := app.ProcessExportJobs(1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	list := exportHTTP(t, app, identity, http.MethodGet, "/exports?page_size=1", "", app.listExports)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	assert.Contains(t, list.Body.String(), `"row_count":"1"`)
	assert.NotContains(t, list.Body.String(), "download_token")
	path := "/exports/" + created.Data.ID
	detail := exportHTTP(t, app, identity, http.MethodGet, path, "", app.getExport)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	var response struct {
		Data struct {
			Token string `json:"download_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(detail.Body.Bytes(), &response))
	require.NotEmpty(t, response.Data.Token)
	url := path + "/download?token=" + response.Data.Token
	otherSession := *identity
	otherSession.SessionID++
	denied := exportHTTP(t, app, &otherSession, http.MethodGet, url, "", app.downloadExport)
	assert.Equal(t, http.StatusUnauthorized, denied.Code)
	download := exportHTTP(t, app, identity, http.MethodGet, url, "", app.downloadExport)
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	assert.Equal(t, "no-store", download.Header().Get("Cache-Control"))
	assert.True(t, strings.HasPrefix(download.Body.String(), "\xef\xbb\xbf"))
	parsed, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(download.Body.String(), "\xef\xbb\xbf"))).ReadAll()
	require.NoError(t, err)
	require.Len(t, parsed, 2)
	assert.Equal(t, "'9007199254740993", parsed[1][1])
	assert.Contains(t, parsed[0], "cache_read_tokens")
	assert.Contains(t, parsed[1], "nonpaid")
	otherActor := *identity
	otherActor.ActorID++
	denied = exportHTTP(t, app, &otherActor, http.MethodGet, path, "", app.getExport)
	assert.Equal(t, http.StatusNotFound, denied.Code)
	list = exportHTTP(t, app, &otherActor, http.MethodGet, "/exports", "", app.listExports)
	assert.Contains(t, list.Body.String(), `"items":[]`)
	require.NoError(t, app.db.Model(&agency).Update("state_revision", agency.StateRevision+1).Error)
	denied = exportHTTP(t, app, identity, http.MethodGet, url, "", app.downloadExport)
	assert.Equal(t, http.StatusForbidden, denied.Code)
	assert.Contains(t, denied.Body.String(), "export_scope_changed")
	var audits []model.AgencyAuditLog
	require.NoError(t, app.db.Where("action IN ?", []string{"export.create", "export.download"}).Find(&audits).Error)
	require.Len(t, audits, 2)
	for _, audit := range audits {
		assert.NotContains(t, audit.AfterJSON, response.Data.Token)
	}
}

func TestExportCreateValidatesKindAndFilterAndUsesSevenLocalDays(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agency, _, err := app.CreateAgency(1, "导出筛选", "export-filters", agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000})
	require.NoError(t, err)
	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 3, AgencyID: &agency.ID, SessionID: 5}
	for _, body := range []string{
		`{"kind":"passwords","filter":{}}`,
		`{"kind":"usage","filter":{"agency_id":"2"}}`,
		`{"kind":"topups","filter":{"model":"private"}}`,
		`{"kind":"usage","filter":{"start_date":"2026-09-01"}}`,
		`{"kind":"usage","filter":{"user_id":9007199254740993}}`,
	} {
		result := exportHTTP(t, app, identity, http.MethodPost, "/exports", body, app.createExport)
		assert.Equal(t, http.StatusUnprocessableEntity, result.Code, body)
	}
	result := exportHTTP(t, app, identity, http.MethodPost, "/exports", `{"kind":"usage","filter":{}}`, app.createExport)
	require.Equal(t, http.StatusAccepted, result.Code, result.Body.String())
	var job model.AgencyExportJob
	require.NoError(t, app.db.First(&job).Error)
	filter, err := parseExportFilter(job.FilterJSON)
	require.NoError(t, err)
	today := time.Now().In(time.FixedZone("Asia/Shanghai", 28800))
	assert.Equal(t, today.Format("2006-01-02"), filter["end_date"])
	assert.Equal(t, today.AddDate(0, 0, -6).Format("2006-01-02"), filter["start_date"])
}

func TestExportListPaginationDoesNotExposeOtherActorsOrRevokedScope(t *testing.T) {
	app := newAgencyTestApp(t)
	agency, _, err := app.CreateAgency(1, "导出分页", "export-pages", agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000})
	require.NoError(t, err)
	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 3, AgencyID: &agency.ID, SessionID: 5}
	jobs := []model.AgencyExportJob{
		{ActorType: ActorTypeOperator, ActorID: 3, AgencyID: &agency.ID, PermissionVersion: agency.StateRevision, Status: "queued", Kind: "usage", FilterJSON: `{}`},
		{ActorType: ActorTypeOperator, ActorID: 3, AgencyID: &agency.ID, PermissionVersion: agency.StateRevision, Status: "queued", Kind: "topups", FilterJSON: `{}`},
		{ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agency.ID, PermissionVersion: agency.StateRevision, Status: "queued", Kind: "usage", FilterJSON: `{}`},
		{ActorType: ActorTypeOperator, ActorID: 3, AgencyID: &agency.ID, PermissionVersion: agency.StateRevision + 1, Status: "queued", Kind: "commissions", FilterJSON: `{}`},
	}
	require.NoError(t, app.db.Create(&jobs).Error)
	first := exportHTTP(t, app, identity, http.MethodGet, "/exports?page_size=1", "", app.listExports)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var page struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Total int `json:"total"`
			Meta  struct {
				Next string `json:"next_cursor"`
			} `json:"meta"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(first.Body.Bytes(), &page))
	require.Len(t, page.Data.Items, 1)
	assert.Equal(t, 2, page.Data.Total)
	assert.Equal(t, stringID(jobs[1].ID), page.Data.Items[0].ID)
	require.NotEmpty(t, page.Data.Meta.Next)
	second := exportHTTP(t, app, identity, http.MethodGet, "/exports?page_size=1&cursor="+page.Data.Meta.Next, "", app.listExports)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	var nextPage = page
	require.NoError(t, common.Unmarshal(second.Body.Bytes(), &nextPage))
	require.Len(t, nextPage.Data.Items, 1)
	assert.Equal(t, stringID(jobs[0].ID), nextPage.Data.Items[0].ID)
	assert.Empty(t, nextPage.Data.Meta.Next)
	otherActor := *identity
	otherActor.ActorID = 4
	denied := exportHTTP(t, app, &otherActor, http.MethodGet, "/exports?cursor="+page.Data.Meta.Next, "", app.listExports)
	assert.Equal(t, http.StatusBadRequest, denied.Code)
}

func TestExportRateLimitAndExpiredCredentialIssuance(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agency, _, err := app.CreateAgency(1, "导出限制", "export-limits", agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000})
	require.NoError(t, err)
	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 3, AgencyID: &agency.ID, SessionID: 5}
	jobs := make([]model.AgencyExportJob, 5)
	for i := range jobs {
		jobs[i] = model.AgencyExportJob{ActorType: ActorTypeOperator, ActorID: 3, AgencyID: &agency.ID, PermissionVersion: agency.StateRevision, Status: "ready", Kind: "usage", FilterJSON: `{}`, CreatedAtMS: time.Now().UnixMilli(), ExpiresAt: time.Now().Unix() - 1}
	}
	require.NoError(t, app.db.Create(&jobs).Error)
	result := exportHTTP(t, app, identity, http.MethodPost, "/exports", `{"kind":"usage","filter":{}}`, app.createExport)
	assert.Equal(t, http.StatusTooManyRequests, result.Code)
	assert.Equal(t, "60", result.Header().Get("Retry-After"))
	result = exportHTTP(t, app, identity, http.MethodGet, "/exports/"+stringID(jobs[0].ID), "", app.getExport)
	assert.Equal(t, http.StatusOK, result.Code)
	assert.Contains(t, result.Body.String(), `"status":"expired"`)
	assert.NotContains(t, result.Body.String(), "download_token")
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyExportJob{}).Count(&count).Error)
	assert.Equal(t, int64(5), count)
}

func TestExportFailureExplainsRowLimitWithoutExposingDatabaseErrors(t *testing.T) {
	app := newAgencyTestApp(t)
	job := model.AgencyExportJob{Status: "processing", Kind: "usage", FilterJSON: `{}`, LeaseOwner: "claim", Attempts: 1, LeaseUntil: time.Now().Unix() + 300}
	require.NoError(t, app.db.Create(&job).Error)
	require.NoError(t, app.failExport(&job, errExportRowLimit))
	require.NoError(t, app.db.First(&job, job.ID).Error)
	assert.Equal(t, "failed", job.Status)
	assert.Equal(t, "export_row_limit", exportJobView(job)["error_code"])
	assert.Empty(t, job.LeaseOwner)
}

func TestExportOrphanCleanupRequiresClaimedExpiredJobAndOldRegularFile(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	expiredAt := time.Now().Add(-time.Hour).Unix()
	jobs := []model.AgencyExportJob{
		{Kind: "usage", FilterJSON: `{}`, Status: "expired", Attempts: 2, ExpiresAt: expiredAt},
		{Kind: "usage", FilterJSON: `{}`, Status: "processing", Attempts: 1, ExpiresAt: expiredAt, LeaseOwner: "live", LeaseUntil: time.Now().Unix() + 300},
		{Kind: "usage", FilterJSON: `{}`, Status: "ready", Attempts: 1, ExpiresAt: time.Now().Unix() + 3600},
	}
	require.NoError(t, app.db.Create(&jobs).Error)
	name := func(jobID int64, attempt int, claim string, suffix string) string {
		return "agency-export-" + stringID(jobID) + "-" + stringID(int64(attempt)) + "-" + strings.Repeat(claim, 32) + suffix
	}
	files := []struct {
		name   string
		remove bool
		fresh  bool
	}{
		{name(jobs[0].ID, 1, "A", ".csv.tmp"), true, false},
		{name(jobs[0].ID, 2, "B", ".csv"), true, false},
		{name(jobs[0].ID, 3, "C", ".csv.tmp"), false, false},
		{name(jobs[0].ID, 1, "D", ".csv.tmp"), false, true},
		{name(jobs[1].ID, 1, "E", ".csv.tmp"), false, false},
		{name(jobs[2].ID, 1, "F", ".csv.tmp"), false, false},
		{name(jobs[2].ID+1, 1, "G", ".csv.tmp"), false, false},
		{"user-notes.csv.tmp", false, false},
		{"agency-export-" + stringID(jobs[0].ID) + "-1-not-a-real-claim.csv.tmp", false, false},
	}
	for _, file := range files {
		path := filepath.Join(app.config.ExportDir, file.name)
		require.NoError(t, os.WriteFile(path, []byte("fixture"), 0600))
		if !file.fresh {
			require.NoError(t, os.Chtimes(path, time.Unix(expiredAt-60, 0), time.Unix(expiredAt-60, 0)))
		}
	}
	directory := filepath.Join(app.config.ExportDir, name(jobs[0].ID, 1, "H", ".csv.tmp"))
	require.NoError(t, os.Mkdir(directory, 0700))
	removed, err := app.cleanupOrphanExportAttempts(10)
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
	for _, file := range files {
		_, statErr := os.Lstat(filepath.Join(app.config.ExportDir, file.name))
		if file.remove {
			assert.ErrorIs(t, statErr, os.ErrNotExist, file.name)
		} else {
			assert.NoError(t, statErr, file.name)
		}
	}
	_, err = os.Stat(directory)
	assert.NoError(t, err)
	removed, err = app.cleanupOrphanExportAttempts(10)
	require.NoError(t, err)
	assert.Zero(t, removed)
}

func TestExportOrphanCleanupLeavesSymlinksAndRejectsLinkedExportRoot(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	job := model.AgencyExportJob{Kind: "usage", Status: "expired", FilterJSON: `{}`, ExpiresAt: time.Now().Unix() - 3600, Attempts: 1}
	require.NoError(t, app.db.Create(&job).Error)
	outside := filepath.Join(t.TempDir(), "keep.txt")
	require.NoError(t, os.WriteFile(outside, []byte("must survive"), 0600))
	link := filepath.Join(app.config.ExportDir, "agency-export-"+stringID(job.ID)+"-1-"+strings.Repeat("A", 32)+".csv.tmp")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("OS does not permit test symlinks: %v", err)
	}
	removed, err := app.cleanupOrphanExportAttempts(10)
	require.NoError(t, err)
	assert.Zero(t, removed)
	_, err = os.Lstat(link)
	require.NoError(t, err)
	content, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "must survive", string(content))
	rootLink := filepath.Join(t.TempDir(), "linked-export-root")
	require.NoError(t, os.Symlink(app.config.ExportDir, rootLink))
	app.config.ExportDir = rootLink
	_, err = app.CleanupExpiredExportJobs(10)
	require.ErrorContains(t, err, "symbolic links")
	_, err = os.Stat(outside)
	assert.NoError(t, err)
}

func TestExportDownloadAdmissionRechecksAuthorityAndRollsBackFailedAudit(t *testing.T) {
	for _, scenario := range []string{"session_revoked", "scope_changed", "permission_changed", "credential_rotated", "audit_failed"} {
		t.Run(scenario, func(t *testing.T) {
			app := newAgencyTestApp(t)
			agency := model.Agency{Code: "export-auth", InviteCode: "export-auth", Status: AgencyStatusActive, StateRevision: 1}
			require.NoError(t, app.db.Create(&agency).Error)
			session := model.AgencySession{ActorType: ActorTypeRoot, ActorID: 4, AgencyID: &agency.ID, TokenHash: "auth-session", CSRFHash: "auth-csrf", ExpiresAt: time.Now().Unix() + 3600}
			require.NoError(t, app.db.Create(&session).Error)
			job := model.AgencyExportJob{ActorType: ActorTypeRoot, ActorID: 4, AgencyID: &agency.ID, PermissionVersion: 1, Kind: "usage", FilterJSON: `{}`, Status: "ready", FileKey: "verified.csv", FileHash: "verified", DownloadTokenHash: tokenHash("token"), DownloadTokenSessionID: session.ID, DownloadTokenExpiresAt: time.Now().Unix() + 600, ExpiresAt: time.Now().Unix() + 3600}
			require.NoError(t, app.db.Create(&job).Error)
			identity := &Identity{ActorType: ActorTypeRoot, ActorID: 4, AgencyID: &agency.ID, SessionID: session.ID}
			expected := errExportScopeChanged
			switch scenario {
			case "session_revoked":
				require.NoError(t, app.db.Model(&session).Update("revoked_at", time.Now().Unix()).Error)
				expected = errExportCredentialChanged
			case "scope_changed":
				require.NoError(t, app.db.Model(&session).Update("agency_id", nil).Error)
			case "permission_changed":
				require.NoError(t, app.db.Model(&agency).Update("state_revision", 2).Error)
			case "credential_rotated":
				require.NoError(t, app.db.Model(&model.AgencyExportJob{}).Where("id = ?", job.ID).Update("download_token_hash", tokenHash("other")).Error)
				expected = errExportCredentialChanged
			case "audit_failed":
				expected = errors.New("audit storage unavailable")
				require.NoError(t, app.db.Callback().Create().Before("gorm:create").Register("test:export-audit-failure", func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AgencyAuditLog" {
						tx.AddError(expected)
					}
				}))
				t.Cleanup(func() { require.NoError(t, app.db.Callback().Create().Remove("test:export-audit-failure")) })
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/exports/1/download", nil)
			require.ErrorIs(t, app.authorizeExportDownload(c, identity, &job, "token"), expected)
			var count int64
			require.NoError(t, app.db.Model(&model.AgencyAuditLog{}).Count(&count).Error)
			assert.Zero(t, count, "denial and failed audit never consume a committed admission")
		})
	}
}
