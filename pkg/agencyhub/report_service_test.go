package agencyhub

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
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

func TestReportSummaryMergesUsageAndCommissionsDeterministically(t *testing.T) {
	app := newAgencyTestApp(t)
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	agency, _, err := app.CreateAgency(1, "报表测试机构", "report_operator", policy)
	require.NoError(t, err)

	loc := time.FixedZone("Asia/Shanghai", 8*60*60)
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, loc).UnixMilli()
	usage := []model.AgencyUsageFact{
		{EventID: "r-usage-1", ComponentID: "default", UserID: 1, AgencyID: &agency.ID, OriginModelName: "alpha", ModelKey: "alpha", BusinessStatus: "success", ChargedQuota: 100, CurrencyCode: "CNY", OccurredAtMS: t0 + 1},
		{EventID: "r-usage-2", ComponentID: "default", UserID: 1, AgencyID: &agency.ID, OriginModelName: "alpha", ModelKey: "alpha", BusinessStatus: "success", ChargedQuota: 150, CurrencyCode: "CNY", OccurredAtMS: t0 + 2},
		{EventID: "r-usage-3", ComponentID: "default", UserID: 2, AgencyID: &agency.ID, OriginModelName: "beta", ModelKey: "beta", BusinessStatus: "success", ChargedQuota: 50, CurrencyCode: "USD", OccurredAtMS: t0 + 3},
		{EventID: "r-usage-4", ComponentID: "default", UserID: 3, AgencyID: &agency.ID, OriginModelName: "gamma", ModelKey: "gamma", BusinessStatus: "success", ChargedQuota: 10, CurrencyCode: "CNY", OccurredAtMS: t0 + 4},
	}
	require.NoError(t, app.db.Create(&usage).Error)

	ledgers := []model.AgencyCommissionLedger{
		{EventID: "r-comm-1", ComponentID: "default", EntryType: "earned", AgencyID: agency.ID, UserID: 1, OriginModelName: "alpha", CommissionQuota: 100, AmountMicros: 1000000, CurrencyCode: "CNY", OccurredAtMS: t0 + 5},
		{EventID: "r-comm-2", ComponentID: "default", EntryType: "earned", AgencyID: agency.ID, UserID: 2, OriginModelName: "beta", CommissionQuota: 50, AmountMicros: 200000, CurrencyCode: "USD", OccurredAtMS: t0 + 6},
		{EventID: "r-comm-3", ComponentID: "default", EntryType: "earned", AgencyID: agency.ID, UserID: 2, OriginModelName: "beta", CommissionQuota: 50, AmountMicros: 300000, CurrencyCode: "USD", OccurredAtMS: t0 + 7},
		{EventID: "r-rev-1", ComponentID: "default", EntryType: "reversal", AgencyID: agency.ID, UserID: 1, OriginModelName: "alpha", CommissionQuota: 12, AmountMicros: -120000, CurrencyCode: "CNY", OccurredAtMS: t0 + 8},
	}
	require.NoError(t, app.db.Create(&ledgers).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/reports/summary?start_date=2026-09-01&end_date=2026-09-02", nil)
	context.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
	app.reportSummary(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	type summaryItem struct {
		UserID           string `json:"user_id"`
		Model            string `json:"model"`
		CurrencyCode     string `json:"currency_code"`
		Calls            string `json:"calls"`
		ChargedQuota     string `json:"charged_quota"`
		CommissionMicros string `json:"commission_micros"`
		ReversalMicros   string `json:"reversal_micros"`
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			StartAtMS        string        `json:"start_at_ms"`
			EndAtMS          string        `json:"end_at_ms"`
			Calls            string        `json:"calls"`
			ChargedQuota     string        `json:"charged_quota"`
			CommissionMicros string        `json:"commission_micros"`
			ReversalMicros   string        `json:"reversal_micros"`
			Items            []summaryItem `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)

	// Usage + commission rows for the same (user, model, currency) merge into
	// a single item; reversal is tracked separately; all int64 fields are
	// serialized as strings so the JS frontend never loses precision.
	require.Equal(t, []summaryItem{
		{UserID: "1", Model: "alpha", CurrencyCode: "CNY", Calls: "2", ChargedQuota: "250", CommissionMicros: "1000000", ReversalMicros: "120000"},
		{UserID: "2", Model: "beta", CurrencyCode: "USD", Calls: "1", ChargedQuota: "50", CommissionMicros: "500000", ReversalMicros: "0"},
		{UserID: "3", Model: "gamma", CurrencyCode: "CNY", Calls: "1", ChargedQuota: "10", CommissionMicros: "0", ReversalMicros: "0"},
	}, response.Data.Items)

	require.Equal(t, "4", response.Data.Calls)
	require.Equal(t, "310", response.Data.ChargedQuota)
	require.Equal(t, "1500000", response.Data.CommissionMicros)
	require.Equal(t, "120000", response.Data.ReversalMicros)
	require.Equal(t, strconv.FormatInt(time.Date(2026, 9, 1, 0, 0, 0, 0, loc).UnixMilli(), 10), response.Data.StartAtMS)
}

func TestCommissionLedgerReturnsSnakeCaseStringMoney(t *testing.T) {
	app := newAgencyTestApp(t)
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	agency, _, err := app.CreateAgency(1, "流水精度机构", "ledger_operator", policy)
	require.NoError(t, err)

	originalID := int64(999)
	rows := []model.AgencyCommissionLedger{
		{EventID: "ledger-large-1", ComponentID: "default", EntryType: "earned", AgencyID: agency.ID, UserID: 42, OriginModelName: "demo", AmountMicros: 9007199254740993, CurrencyCode: "CNY", OccurredAtMS: 1735689600123, OriginalEntryID: &originalID},
		{EventID: "ledger-large-2", ComponentID: "default", EntryType: "reversal", AgencyID: agency.ID, UserID: 43, OriginModelName: "demo", AmountMicros: -9007199254740993, CurrencyCode: "CNY", OccurredAtMS: 1735689600456},
	}
	require.NoError(t, app.db.Create(&rows).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/commissions/ledger", nil)
	context.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
	app.commissionLedger(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	type ledgerItem struct {
		ID           int64   `json:"id"`
		EventID      string  `json:"event_id"`
		EntryType    string  `json:"entry_type"`
		OriginalID   *string `json:"original_entry_id"`
		UserID       string  `json:"user_id"`
		AmountMicros string  `json:"amount_micros"`
		CurrencyCode string  `json:"currency_code"`
		OccurredAtMS string  `json:"occurred_at_ms"`
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Items []ledgerItem `json:"items"`
			Total int64        `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, int64(2), response.Data.Total)

	// Money and timestamps are strings (preserving values beyond 2^53) and the
	// keys are snake_case so the agency-web ledger table renders real values.
	// Rows are ordered occurred_at_ms DESC, so the later reversal is first.
	require.Equal(t, "-9007199254740993", response.Data.Items[0].AmountMicros)
	require.Equal(t, "9007199254740993", response.Data.Items[1].AmountMicros)
	require.Equal(t, "1735689600456", response.Data.Items[0].OccurredAtMS)
	require.Equal(t, "43", response.Data.Items[0].UserID)
	require.Equal(t, "999", *response.Data.Items[1].OriginalID)
	require.Nil(t, response.Data.Items[0].OriginalID)
	require.NotContains(t, recorder.Body.String(), `"AmountMicros"`)
}

func TestReportSummaryEndDateBoundaryIsLocalDayInclusive(t *testing.T) {
	// acceptance §22.2「时区结束日边界」: a date-based range is inclusive of the
	// entire Asia/Shanghai end-day, so a fact at the end-day's last millisecond is
	// included while a fact one millisecond after local midnight on the next day is
	// excluded (reportRange returns [start 00:00, end+1 00:00) in local time).
	app := newAgencyTestApp(t)
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	agency, _, err := app.CreateAgency(1, "时区边界机构", "tz_operator", policy)
	require.NoError(t, err)

	loc := time.FixedZone("Asia/Shanghai", 8*60*60)
	includedStart := time.Date(2026, 9, 1, 0, 0, 0, 0, loc).UnixMilli()
	includedEndLast := time.Date(2026, 9, 2, 23, 59, 59, 999, loc).UnixMilli()
	excludedNextMidnight := time.Date(2026, 9, 3, 0, 0, 0, 0, loc).UnixMilli()
	excludedBeforeStart := time.Date(2026, 8, 31, 23, 59, 59, 999, loc).UnixMilli()
	facts := []model.AgencyUsageFact{
		{EventID: "tz-in-1", ComponentID: "default", UserID: 1, AgencyID: &agency.ID, OriginModelName: "boundary", ModelKey: "boundary", BusinessStatus: "success", ChargedQuota: 10, CurrencyCode: "CNY", OccurredAtMS: includedStart},
		{EventID: "tz-in-2", ComponentID: "default", UserID: 2, AgencyID: &agency.ID, OriginModelName: "boundary", ModelKey: "boundary", BusinessStatus: "success", ChargedQuota: 20, CurrencyCode: "CNY", OccurredAtMS: includedEndLast},
		{EventID: "tz-out-1", ComponentID: "default", UserID: 3, AgencyID: &agency.ID, OriginModelName: "boundary", ModelKey: "boundary", BusinessStatus: "success", ChargedQuota: 40, CurrencyCode: "CNY", OccurredAtMS: excludedNextMidnight},
		{EventID: "tz-out-2", ComponentID: "default", UserID: 4, AgencyID: &agency.ID, OriginModelName: "boundary", ModelKey: "boundary", BusinessStatus: "success", ChargedQuota: 80, CurrencyCode: "CNY", OccurredAtMS: excludedBeforeStart},
	}
	require.NoError(t, app.db.Create(&facts).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/reports/summary?start_date=2026-09-01&end_date=2026-09-02", nil)
	context.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
	app.reportSummary(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	type summaryItem struct {
		UserID       string `json:"user_id"`
		ChargedQuota string `json:"charged_quota"`
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Calls        string        `json:"calls"`
			ChargedQuota string        `json:"charged_quota"`
			Items        []summaryItem `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, "2", response.Data.Calls, "only facts inside [2026-09-01 00:00, 2026-09-03 00:00) Asia/Shanghai count")
	require.Equal(t, "30", response.Data.ChargedQuota)
	require.Len(t, response.Data.Items, 2)
	require.Equal(t, "1", response.Data.Items[0].UserID)
	require.Equal(t, "2", response.Data.Items[1].UserID)
}
