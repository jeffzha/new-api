package agencyhub

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportWorkerAppliesUsageFilter(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agencyID := int64(17)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)).UnixMilli()
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{EventID: "evt-match", ComponentID: "default", UserID: 12, AgencyID: &agencyID, OriginModelName: "demo", BusinessStatus: "success", StandardQuota: 10, ChargedQuota: 12, CurrencyCode: "CNY", OccurredAtMS: start + 1000}).Error)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{EventID: "evt-other-user", ComponentID: "default", UserID: 99, AgencyID: &agencyID, OriginModelName: "demo", BusinessStatus: "success", StandardQuota: 10, ChargedQuota: 12, CurrencyCode: "CNY", OccurredAtMS: start + 2000}).Error)
	job := model.AgencyExportJob{ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agencyID, Kind: "usage", FilterJSON: `{"user_id":"12","start_date":"2026-09-01","end_date":"2026-09-02"}`, Status: "queued", ExpiresAt: time.Now().Add(time.Hour).Unix(), CreatedAtMS: time.Now().UnixMilli()}
	require.NoError(t, app.db.Create(&job).Error)
	processed, err := app.ProcessExportJobs(1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	var stored model.AgencyExportJob
	require.NoError(t, app.db.First(&stored, job.ID).Error)
	require.Equal(t, int64(1), stored.RowCount)
	data, err := os.ReadFile(app.config.ExportDir + "/" + stored.FileKey)
	require.NoError(t, err)
	require.Contains(t, string(data), "evt-match")
	require.NotContains(t, string(data), "evt-other-user")
}

func TestExportWorkerEscapesSpreadsheetFormulaText(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agencyID := int64(18)
	occurredAt := time.Date(2026, 9, 2, 8, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)).UnixMilli()
	require.NoError(t, app.db.Create(&model.AgencyCommissionLedger{
		EventID: "-commission-event", EntryType: "earned", AgencyID: agencyID,
		UserID: 12, OriginModelName: "=HYPERLINK(\"https://example.test\")",
		StandardQuota: 100, SettlementCostQuota: 80, CommissionQuota: 20,
		AmountMicros: -123456, CurrencyCode: "CNY", OccurredAtMS: occurredAt,
	}).Error)
	job := model.AgencyExportJob{ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agencyID, Kind: "commissions", FilterJSON: `{}`, Status: "queued", ExpiresAt: time.Now().Add(time.Hour).Unix(), CreatedAtMS: time.Now().UnixMilli()}
	require.NoError(t, app.db.Create(&job).Error)

	processed, err := app.ProcessExportJobs(1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	var stored model.AgencyExportJob
	require.NoError(t, app.db.First(&stored, job.ID).Error)
	data, err := os.ReadFile(app.config.ExportDir + "/" + stored.FileKey)
	require.NoError(t, err)

	require.Contains(t, string(data), "'-commission-event")
	require.Contains(t, string(data), "'=HYPERLINK")
	require.Contains(t, string(data), ",-123456,")
}

func TestParseExportFilterRejectsUnknownAndInvalidValues(t *testing.T) {
	_, err := parseExportFilter(`{"drop_table":true}`)
	require.Error(t, err)
	_, err = parseExportFilter(`{"user_id":-1}`)
	require.Error(t, err)
	app := newAgencyTestApp(t)
	_, err = applyExportFilter(app.db, "usage", map[string]string{"start_at": "bad"})
	require.Error(t, err)
}

func TestReportRangeSupportsInclusiveLocalDatesAndExactTimestamps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodGet, "/agency/api/v1/reports/summary?start_date=2026-09-01&end_date=2026-09-01", nil)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	start, end, err := reportRange(context)
	require.NoError(t, err)
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, location).UnixMilli(), start)
	require.Equal(t, time.Date(2026, 9, 2, 0, 0, 0, 0, location).UnixMilli(), end)

	request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/reports/summary?start_at=2026-09-01T00:00:00%2B08:00&end_at=2026-09-01T01:00:00%2B08:00", nil)
	context, _ = gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	start, end, err = reportRange(context)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, location).UnixMilli(), start)
	require.Equal(t, time.Date(2026, 9, 1, 1, 0, 0, 0, location).UnixMilli(), end)
}

func TestReportRangeRejectsMixedFiltersAndMissingEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, target := range []string{
		"/agency/api/v1/reports/summary?start_date=2026-09-01&start_at=2026-09-01T00:00:00Z&end_at=2026-09-02T00:00:00Z",
		"/agency/api/v1/reports/summary?start_at=2026-09-01T00:00:00Z",
		"/agency/api/v1/reports/summary?start_date=2026-09-02&end_date=2026-09-01",
	} {
		request := httptest.NewRequest(http.MethodGet, target, strings.NewReader(""))
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		_, _, err := reportRange(context)
		assert.Error(t, err, target)
	}
}

func TestCleanupExpiredExportJobsRemovesFileAndInvalidatesDownloadToken(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agencyID := int64(21)
	job := model.AgencyExportJob{
		ActorType:              ActorTypeOperator,
		ActorID:                4,
		AgencyID:               &agencyID,
		Kind:                   "usage",
		Status:                 "ready",
		FileKey:                "agency-export-expired.csv",
		FileHash:               "stale-hash",
		ExpiresAt:              time.Now().Add(-time.Minute).Unix(),
		DownloadTokenHash:      "token-hash",
		DownloadTokenExpiresAt: time.Now().Add(time.Minute).Unix(),
		DownloadTokenSessionID: 17,
		CreatedAtMS:            time.Now().Add(-time.Hour).UnixMilli(),
	}
	require.NoError(t, app.db.Create(&job).Error)
	require.NoError(t, os.WriteFile(app.config.ExportDir+"/"+job.FileKey, []byte("temporary export"), 0o600))

	cleaned, err := app.CleanupExpiredExportJobs(10)
	require.NoError(t, err)
	require.Equal(t, 1, cleaned)

	var stored model.AgencyExportJob
	require.NoError(t, app.db.First(&stored, job.ID).Error)
	require.Equal(t, "expired", stored.Status)
	require.Empty(t, stored.FileKey)
	require.Empty(t, stored.FileHash)
	require.Empty(t, stored.DownloadTokenHash)
	require.Zero(t, stored.DownloadTokenExpiresAt)
	require.Zero(t, stored.DownloadTokenSessionID)
	_, err = os.Stat(app.config.ExportDir + "/" + job.FileKey)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestExportPathRejectsTraversal(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	_, err := app.exportPath("../outside.csv")
	require.Error(t, err)
}

func TestDownloadExportRejectsTamperedFile(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agency, _, err := app.CreateAgency(1, "Export Integrity", "export-integrity", agencycontract.Policy{
		DefaultSettlementBPS: 7500,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
	})
	require.NoError(t, err)
	content := []byte("\xef\xbb\xbfvalue\nsafe\n")
	fileKey := "agency-export-integrity.csv"
	require.NoError(t, os.WriteFile(app.config.ExportDir+"/"+fileKey, content, 0o600))
	digest := sha256.Sum256(content)
	token := "download-token"
	job := model.AgencyExportJob{
		ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agency.ID,
		Kind: "usage", Status: "ready", FileKey: fileKey,
		FileHash: hex.EncodeToString(digest[:]), PermissionVersion: agency.StateRevision,
		DownloadTokenHash: tokenHash(token), DownloadTokenExpiresAt: time.Now().Add(time.Minute).Unix(),
		DownloadTokenSessionID: 7, ExpiresAt: time.Now().Add(time.Hour).Unix(), CreatedAtMS: time.Now().UnixMilli(),
	}
	require.NoError(t, app.db.Create(&job).Error)
	require.NoError(t, os.WriteFile(app.config.ExportDir+"/"+fileKey, []byte("\xef\xbb\xbfvalue\nforged\n"), 0o600))

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, router := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/exports/"+stringID(job.ID)+"/download?token="+token, nil)
	ctx.Params = gin.Params{{Key: "id", Value: stringID(job.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agency.ID, SessionID: 7})
	_ = router
	app.downloadExport(ctx)

	require.Equal(t, http.StatusGone, recorder.Code)
	require.Contains(t, recorder.Body.String(), "export_integrity_failed")
	require.True(t, strings.Contains(recorder.Body.String(), "完整性"))
}
