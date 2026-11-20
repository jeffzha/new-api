/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package controller

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestLogExportQueryRestrictsUsernameToRoot(t *testing.T) {
	tests := []struct {
		name         string
		isAdmin      bool
		role         int
		userID       int
		wantUsername string
		wantUserID   int
	}{
		{name: "root can filter by username", isAdmin: true, role: common.RoleRootUser, wantUsername: "target-user"},
		{name: "admin username filter is ignored", isAdmin: true, role: common.RoleAdminUser},
		{name: "self export uses authenticated user", role: common.RoleCommonUser, userID: 42, wantUserID: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest("GET", "/api/log/export?username=%20target-user%20", nil)
			ctx.Set("role", tt.role)
			ctx.Set("id", tt.userID)

			params := logExportQuery(ctx, tt.isAdmin)

			require.Equal(t, tt.wantUsername, params.Username)
			require.Equal(t, tt.wantUserID, params.UserID)
		})
	}
}

func TestWriteLogExportRowFollowsFixedTemplateColumns(t *testing.T) {
	log := &model.Log{Username: "alice", CreatedAt: 1_758_000_000, ModelName: "deepseek", PromptTokens: 10, CompletionTokens: 5, Quota: 100}
	modelRatio, completionRatio, cacheRatio, cacheCreationRatio, groupRatio := 1.0, 2.0, 0.5, 0.25, 1.5
	row := logExportRow{
		Log:      log,
		Other:    logExportOther{QuotaPerUnit: 100, ModelRatio: &modelRatio, CompletionRatio: &completionRatio, CacheRatio: &cacheRatio, CacheCreationRatio: &cacheCreationRatio, GroupRatio: &groupRatio},
		Funding:  model.LogExportFunding{PaidQuota: 100},
		HasFunds: true,
	}
	values := logExportRowValues(row)
	require.Len(t, values, 19)
	require.Equal(t, "21.90000000", values[7])
	require.Equal(t, "43.80000000", values[8])
	require.Equal(t, "0.00000000", values[13])
	require.Equal(t, "7.30000000", values[14])
	require.Equal(t, "0.00000000", values[15])
	require.Equal(t, "否", values[16])
	require.Empty(t, values[17])
	require.Empty(t, values[18])
}

func TestLogExportRowShowsCustomerAgencyDiscount(t *testing.T) {
	other := logExportOther{QuotaPerUnit: 100}
	tests := []struct {
		name           string
		pricing        model.LogExportAgencyPricing
		wantActual     string
		wantDiscounted string
		wantRatio      string
		wantDiscount   string
	}{
		{name: "discount", pricing: model.LogExportAgencyPricing{StandardQuota: 125, ChargedTotalQuota: 100, SalesBPS: 8000, QuotaPerUnit: "100", ExchangeRate: "7.3"}, wantActual: "7.30000000", wantDiscounted: "是", wantRatio: "0.8000", wantDiscount: "1.82500000"},
		{name: "markup is not a discount", pricing: model.LogExportAgencyPricing{StandardQuota: 100, ChargedTotalQuota: 120, SalesBPS: 12000, QuotaPerUnit: "100", ExchangeRate: "7.3"}, wantActual: "8.76000000", wantDiscounted: "否", wantRatio: "1.2000", wantDiscount: "0.00000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := logExportRowValues(logExportRow{
				Log:   &model.Log{Username: "agency-customer", CreatedAt: 1_758_000_000, ModelName: "deepseek", Quota: 999},
				Other: other, AgencyPricing: tt.pricing, HasAgencyPricing: true,
			})
			require.Equal(t, tt.wantActual, values[12])
			require.Equal(t, tt.wantDiscounted, values[16])
			require.Equal(t, tt.wantRatio, values[17])
			require.Equal(t, tt.wantDiscount, values[18])
		})
	}
}

func TestBuildLogExportFormatsFollowTemplate(t *testing.T) {
	modelRatio, completionRatio := 1.0, 2.0
	document := logExportDocument{
		Rows: []logExportRow{{
			Log:   &model.Log{Username: "alice", CreatedAt: 1_758_000_000, ModelName: "deepseek-chat", PromptTokens: 10, CompletionTokens: 5, Quota: 100},
			Other: logExportOther{QuotaPerUnit: 100, ModelRatio: &modelRatio, CompletionRatio: &completionRatio},
		}},
		MonthTotal: map[string]float64{"2025-09": 7.3},
		Total:      7.3,
	}

	csvData, err := buildLogExportCSV(document)
	require.NoError(t, err)
	csvReader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(csvData, []byte("\xEF\xBB\xBF"))))
	csvReader.FieldsPerRecord = -1
	csvRows, err := csvReader.ReadAll()
	require.NoError(t, err)
	require.Equal(t, "按量消费明细", csvRows[0][0])
	require.Equal(t, logExportBillingNotice, csvRows[1][0])
	require.Equal(t, logExportHeaders, csvRows[2])
	require.Len(t, csvRows[3], 19)
	require.Equal(t, "2025-09 月度小计", csvRows[4][0])
	require.Equal(t, "本期消费金额", csvRows[5][0])

	xlsxData, err := buildLogExportXLSX(document)
	require.NoError(t, err)
	workbook, err := excelize.OpenReader(bytes.NewReader(xlsxData))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workbook.Close()) })
	require.Equal(t, "按量消费明细", mustCellValue(t, workbook, "账单明细", "A1"))
	require.Equal(t, logExportBillingNotice, mustCellValue(t, workbook, "账单明细", "A2"))
	require.Equal(t, "代理商优惠金额（元）", mustCellValue(t, workbook, "账单明细", "S3"))
	require.Equal(t, "alice", mustCellValue(t, workbook, "账单明细", "A4"))
	require.Equal(t, "2025-09 月度小计", mustCellValue(t, workbook, "账单明细", "A5"))
	merged, err := workbook.GetMergeCells("账单明细")
	require.NoError(t, err)
	require.Equal(t, "A1:S1", merged[0].GetStartAxis()+":"+merged[0].GetEndAxis())
	require.Equal(t, "A2:S2", merged[1].GetStartAxis()+":"+merged[1].GetEndAxis())
	require.Equal(t, "A5:L5", merged[2].GetStartAxis()+":"+merged[2].GetEndAxis())

	docxData, err := buildLogExportDOCX(document)
	require.NoError(t, err)
	archive, err := zip.NewReader(bytes.NewReader(docxData), int64(len(docxData)))
	require.NoError(t, err)
	documentXML := readZipEntry(t, archive, "word/document.xml")
	require.Contains(t, documentXML, "按量消费明细")
	require.Contains(t, documentXML, xmlEscape(logExportBillingNotice))
	require.Contains(t, documentXML, "代理商优惠金额（元）")
	require.Contains(t, documentXML, `w:gridSpan w:val="12"`)
	require.Contains(t, documentXML, `w:gridSpan w:val="19"`)

	pdfData, err := buildLogExportPDF(document)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pdfData, []byte("%PDF-1.4")))
	require.Contains(t, string(pdfData), "/BaseFont /STSong-Light")
	require.Contains(t, string(pdfData), pdfText("按量消费明细"))
	require.Contains(t, string(pdfData), pdfText(logExportBillingNotice))
}

func mustCellValue(t *testing.T, workbook *excelize.File, sheet, cell string) string {
	t.Helper()
	value, err := workbook.GetCellValue(sheet, cell)
	require.NoError(t, err)
	return value
}

func readZipEntry(t *testing.T, archive *zip.Reader, name string) string {
	t.Helper()
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		require.NoError(t, err)
		defer reader.Close()
		data, err := io.ReadAll(reader)
		require.NoError(t, err)
		return string(data)
	}
	t.Fatalf("zip entry %q not found", name)
	return ""
}

func TestGetConsumeLogsForExportLoadsFundingAcrossQueryBatches(t *testing.T) {
	dsn := "file:log-export-batches-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	testGetConsumeLogsForExportLoadsAgencyPricing(t, db, common.DatabaseTypeSQLite)
}

func TestGetConsumeLogsForExportExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		database common.DatabaseType
		open     func(string) gorm.Dialector
	}{
		{name: "mysql", dsn: strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN")), database: common.DatabaseTypeMySQL, open: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{name: "postgres", dsn: strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")), database: common.DatabaseTypePostgreSQL, open: func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.dsn == "" {
				t.Skipf("TEST_%s_DSN is not configured", strings.ToUpper(tt.name))
			}
			db, err := gorm.Open(tt.open(tt.dsn), &gorm.Config{})
			require.NoError(t, err)
			testGetConsumeLogsForExportLoadsAgencyPricing(t, db, tt.database)
		})
	}
}

func testGetConsumeLogsForExportLoadsAgencyPricing(t *testing.T, db *gorm.DB, databaseType common.DatabaseType) {
	t.Helper()
	require.NoError(t, db.Migrator().DropTable(&model.Log{}, &model.AgencyFundingAllocation{}, &model.AgencyBillingOperation{}))
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.AgencyFundingAllocation{}, &model.AgencyBillingOperation{}))
	t.Cleanup(func() {
		require.NoError(t, db.Migrator().DropTable(&model.Log{}, &model.AgencyFundingAllocation{}, &model.AgencyBillingOperation{}))
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(databaseType, databaseType)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
	})

	logs := make([]model.Log, 502)
	allocations := make([]model.AgencyFundingAllocation, 502)
	for i := range logs {
		requestID := fmt.Sprintf("bill-request-%03d", i)
		userID := 1
		if i == 501 {
			requestID = "bill-request-000"
			userID = 2
		}
		logs[i] = model.Log{Type: model.LogTypeConsume, UserId: userID, RequestId: requestID, CreatedAt: int64(i + 1)}
		allocations[i] = model.AgencyFundingAllocation{
			ChargeID: requestID, ComponentID: "default", UserID: int64(userID), Consumed: 1, Version: 1,
		}
	}
	require.NoError(t, db.CreateInBatches(logs, 100).Error)
	require.NoError(t, db.CreateInBatches(allocations, 100).Error)
	event := agencycontract.BillingEvent{FinancialChargeID: "bill-request-500", UserID: 1, StandardQuota: 100, ChargedTotalQuota: 80, SalesBPS: 8000, QuotaPerUnit: "100", ExchangeRate: "7.3"}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyBillingOperation{ChargeID: event.FinancialChargeID, SegmentNo: 0, Revision: 1, Operation: "finalize", InputHash: "billing-export-test", CommittedResult: string(payload), EventCount: 1, CreatedAtMS: 1}).Error)

	exported, funding, agencyPricing, err := model.GetConsumeLogsForExport(model.LogExportParams{IsAdmin: true})
	require.NoError(t, err)
	require.Len(t, exported, 502)
	require.Len(t, funding, 502)
	require.Equal(t, int64(1), funding[model.LogExportFundingKey{UserID: 1, RequestID: "bill-request-500"}].PaidQuota)
	require.Equal(t, int64(1), funding[model.LogExportFundingKey{UserID: 2, RequestID: "bill-request-000"}].PaidQuota)
	require.Equal(t, model.LogExportAgencyPricing{StandardQuota: 100, ChargedTotalQuota: 80, SalesBPS: 8000, QuotaPerUnit: "100", ExchangeRate: "7.3"}, agencyPricing[model.LogExportFundingKey{UserID: 1, RequestID: "bill-request-500"}])
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	for _, format := range []string{"csv", "xlsx"} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest("GET", "/api/log/self/export?format="+format, nil)
		writeLogExport(ctx, model.LogExportParams{UserID: 1})
		require.Equal(t, 200, recorder.Code, recorder.Body.String())
		var rows [][]string
		if format == "csv" {
			reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(recorder.Body.Bytes(), []byte("\xEF\xBB\xBF"))))
			reader.FieldsPerRecord = -1
			rows, err = reader.ReadAll()
		} else {
			workbook, openErr := excelize.OpenReader(bytes.NewReader(recorder.Body.Bytes()))
			require.NoError(t, openErr)
			rows, err = workbook.GetRows("账单明细")
			require.NoError(t, workbook.Close())
		}
		require.NoError(t, err)
		require.Len(t, rows, 506, format) // title, notice, header, 501 charges, month subtotal, total
		require.Equal(t, "5.84000000", rows[503][12], format)
		require.Equal(t, "是", rows[503][16], format)
		require.Equal(t, "0.8000", rows[503][17], format)
	}
}

func TestLogExportMultiDayBillExceedsLegacyLimit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:large-bill?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = nil, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		sqlDB, closeErr := db.DB()
		require.NoError(t, closeErr)
		require.NoError(t, sqlDB.Close())
	})
	start := int64(1790812800) // October 1 in both UTC and the production timezone.
	logs := make([]model.Log, model.LogExportMaxRows+1)
	for i := range logs {
		logs[i] = model.Log{Type: model.LogTypeConsume, UserId: 7, Username: "bill-user", CreatedAt: start + int64(i/40000)*86400 + int64(i%40000/301), PromptTokens: i, RequestId: fmt.Sprintf("bill-%d", i), Other: `{"quota_per_unit":100,"exchange_rate":1}`, Quota: 1}
	}
	require.NoError(t, db.CreateInBatches(logs, 100).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/api/log/self/export?format=csv", nil)
	writeLogExport(ctx, model.LogExportParams{UserID: 7, StartTimestamp: start, EndTimestamp: start + 3*86400})
	require.Equal(t, 200, recorder.Code, recorder.Body.String())
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(recorder.Body.Bytes(), []byte("\xEF\xBB\xBF"))))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	require.NoError(t, err)
	require.Equal(t, len(logs)+5, len(rows))
	for i := range logs {
		require.Equal(t, fmt.Sprint(i), rows[i+3][3])
	}
	require.Equal(t, "2026-10 月度小计", rows[len(rows)-2][0])
	require.Equal(t, rows[len(rows)-2][12], rows[len(rows)-1][12])

	xlsxFile, err := os.CreateTemp(t.TempDir(), "bill-*.xlsx")
	require.NoError(t, err)
	defer xlsxFile.Close()
	require.NoError(t, streamLogExportXLSX(context.Background(), model.LogExportParams{UserID: 7}, xlsxFile, t.TempDir()))
	info, err := xlsxFile.Stat()
	require.NoError(t, err)
	archive, err := zip.NewReader(xlsxFile, info.Size())
	require.NoError(t, err)
	var excelRows int
	for _, entry := range archive.File {
		if entry.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		input, err := entry.Open()
		require.NoError(t, err)
		defer input.Close()
		decoder := xml.NewDecoder(input)
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "row" {
				continue
			}
			var row struct {
				Cells []struct {
					Ref   string `xml:"r,attr"`
					Value string `xml:"v"`
					Text  string `xml:"is>t"`
				} `xml:"c"`
			}
			require.NoError(t, decoder.DecodeElement(&row, &start))
			excelRows++
			for _, cell := range row.Cells {
				if cell.Ref == "D100004" {
					require.Equal(t, "100000", cell.Value)
				}
				if cell.Ref == "A100006" {
					require.Equal(t, "本期消费金额", cell.Text)
				}
			}
		}
	}
	require.Equal(t, len(logs)+5, excelRows)

	canceled, cancel := context.WithCancel(context.Background())
	visited := 0
	err = model.WalkConsumeLogsForExport(canceled, model.LogExportParams{UserID: 7}, func(batch []*model.Log, _ map[model.LogExportFundingKey]model.LogExportFunding, _ map[model.LogExportFundingKey]model.LogExportAgencyPricing) error {
		visited += len(batch)
		cancel()
		return canceled.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, visited, len(logs))
	visited = 0
	err = model.WalkConsumeLogsForExport(canceled, model.LogExportParams{UserID: 7}, func(batch []*model.Log, _ map[model.LogExportFundingKey]model.LogExportFunding, _ map[model.LogExportFundingKey]model.LogExportAgencyPricing) error {
		visited += len(batch)
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, visited)
}

func TestLogExportFailureDoesNotLeavePartialBill(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	t.Setenv("TMP", temporary)
	t.Setenv("TEMP", temporary)
	oldDB := model.LOG_DB
	model.LOG_DB = nil
	t.Cleanup(func() { model.LOG_DB = oldDB })
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/api/log/self/export?format=xlsx", nil)
	writeLogExport(ctx, model.LogExportParams{UserID: 7})
	require.Equal(t, 500, recorder.Code)
	require.Empty(t, recorder.Header().Get("Content-Disposition"))
	entries, err := os.ReadDir(temporary)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Empty(t, logExportSlot)
}

func TestTypedLogExportHistoryPermissionsAndFormats(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var db *gorm.DB
			var err error
			switch dialect {
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN not set")
				}
				db, err = gorm.Open(mysql.Open(os.Getenv("TEST_MYSQL_DSN")), &gorm.Config{})
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN not set")
				}
				db, err = gorm.Open(postgres.Open(os.Getenv("TEST_POSTGRES_DSN")), &gorm.Config{})
			default:
				db, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			}
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			previousDB, previousLogDB := model.DB, model.LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
			})
			require.NoError(t, db.AutoMigrate(&model.Log{}, &model.TopUp{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Log{}, &model.TopUp{})) })
			logs := []model.Log{
				{Type: 2, Content: "consume", RequestId: "consume", Quota: 1, Other: `{"quota_per_unit":100}`},
				{Type: 1, Content: "self-payment", RequestId: "topup", Quota: 500, Other: `{"quota_source":"online_topup","trade_no":"paid-order"}`},
				{Type: 1, Content: "使用在线充值成功，充值金额: ¥10", RequestId: "legacy-topup"},
				{Type: 1, Content: "manual", RequestId: "adjust", Quota: 30, Other: `{"quota_source":"admin_adjustment","quota_direction":"debit","op":{"action":"user.quota_override","params":{"from":40,"to":10}},"admin_info":{"secret":"admin-secret"},"root_info":{"secret":"root-secret"}}`},
				{Type: 1, Content: "通过兑换码充值 ¥10 额度", RequestId: "legacy-redemption"},
				{Type: 3, Content: "legacy-manual", RequestId: "legacy-manual", Other: `{"op":{"action":"user.quota_add"}}`},
				{Type: 4, Content: "system-event", RequestId: "system"},
				{Type: 5, Content: "=HYPERLINK(test)", RequestId: "error", Other: `{"status_code":429,"error_code":"rate_limit","error_type":"upstream_error","request_path":"/v1/videos","root_info":{"secret":"root-secret"}}`},
				{Type: 6, RequestId: "refund", Quota: 40, Other: `{"task_id":"task-1","reason":"task failed; quota returned"}`},
				{Type: 1, Content: "subscription-not-topup", Other: `{"quota_source":"subscription_purchase"}`},
				{Type: 1, Content: "foreign-trade-owner", RequestId: "foreign-trade", Quota: 1, Other: `{"quota_source":"online_topup","trade_no":"other-order"}`},
			}
			for i := range logs {
				logs[i].UserId = 7
				logs[i].Username = "owner"
				logs[i].CreatedAt = int64(1790000000 + i)
				logs[i].ModelName = "example"
			}
			for _, typ := range []int{2, 1, 8, 4, 5, 6} {
				stored, other := typ, "{}"
				if typ == 8 {
					stored = 1
					other = `{"quota_source":"admin_adjustment"}`
				}
				if typ == 1 {
					other = `{"quota_source":"online_topup"}`
				}
				logs = append(logs, model.Log{Type: stored, UserId: 8, Username: "other", CreatedAt: 1790000100, Content: "other-user", Other: other})
			}
			require.NoError(t, db.CreateInBatches(logs, 50).Error)
			require.NoError(t, db.Create(&model.TopUp{UserId: 7, TradeNo: "paid-order", FundingSource: "payment_assisted", PaymentMethod: "wxpay", PaymentSnapshot: `{"actual_money":"7.30","currency_code":"CNY","payment_reference":"receipt-secret"}`, PaymentParams: "payment-secret"}).Error)
			require.NoError(t, db.Create(&model.TopUp{UserId: 8, TradeNo: "other-order", PaymentSnapshot: `{"actual_money":"999","currency_code":"USD"}`}).Error)
			for _, scenario := range []struct {
				typ, count int
				title      string
			}{
				{2, 1, "按量消费明细"}, {1, 3, "充值明细"}, {8, 3, "额度调整明细"}, {4, 1, "系统日志明细"}, {5, 1, "错误日志明细"}, {6, 1, "退款明细"},
			} {
				t.Run(fmt.Sprint(scenario.typ), func(t *testing.T) {
					for _, format := range []string{"csv", "xlsx", "docx", "pdf"} {
						recorder := httptest.NewRecorder()
						ctx, _ := gin.CreateTestContext(recorder)
						ctx.Request = httptest.NewRequest("GET", fmt.Sprintf("/api/log/self/export?type=%d&format=%s&username=other&user_id=8", scenario.typ, format), nil)
						ctx.Set("id", 7)
						ctx.Set("role", common.RoleCommonUser)
						GetUserLogsExport(ctx)
						require.Equal(t, 200, recorder.Code, recorder.Body.String())
						var rows [][]string
						switch format {
						case "csv":
							reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(recorder.Body.Bytes(), []byte{0xef, 0xbb, 0xbf})))
							reader.FieldsPerRecord = -1
							rows, err = reader.ReadAll()
							require.NoError(t, err)
						case "xlsx":
							book, openErr := excelize.OpenReader(bytes.NewReader(recorder.Body.Bytes()))
							require.NoError(t, openErr)
							rows, err = book.GetRows("账单明细")
							require.NoError(t, err)
							require.NoError(t, book.Close())
						case "docx":
							archive, openErr := zip.NewReader(bytes.NewReader(recorder.Body.Bytes()), int64(recorder.Body.Len()))
							require.NoError(t, openErr)
							xml := readZipEntry(t, archive, "word/document.xml")
							assert.Contains(t, xml, scenario.title)
							assert.NotContains(t, xml, "other-user")
							assert.NotContains(t, xml, "root-secret")
						case "pdf":
							assert.Contains(t, recorder.Body.String(), pdfText(scenario.title))
							assert.NotContains(t, recorder.Body.String(), "other-user")
						}
						if rows != nil {
							assert.Equal(t, scenario.title, rows[0][0])
							extra := 4
							if scenario.typ == 2 {
								extra++
							}
							require.Len(t, rows, scenario.count+extra)
							for _, row := range rows[3 : 3+scenario.count] {
								assert.Equal(t, "owner", row[0])
							}
							joined := fmt.Sprint(rows)
							for _, secret := range []string{"admin-secret", "root-secret", "payment-secret", "receipt-secret", "subscription-not-topup"} {
								assert.NotContains(t, joined, secret)
							}
							if scenario.typ == 1 {
								assert.Equal(t, "代充", rows[3][3])
								assert.Equal(t, "7.30", rows[3][6])
								assert.Equal(t, "CNY", rows[3][7])
								assert.Equal(t, "用户直充", rows[4][3])
								assert.Equal(t, "未记录", rows[5][6])
							}
							if scenario.typ == 8 {
								assert.Equal(t, "减少", rows[3][4])
								assert.Equal(t, "40", rows[3][6])
								assert.Equal(t, "10", rows[3][7])
							}
							if scenario.typ == 5 {
								assert.Equal(t, "429", rows[3][9])
								assert.Equal(t, "rate_limit", rows[3][10])
								assert.Equal(t, "'=HYPERLINK(test)", rows[3][13])
							}
							if scenario.typ == 6 {
								assert.Equal(t, "task failed; quota returned", rows[3][8])
							}
						}
					}
					for _, username := range []string{"", "owner", "other"} {
						found, _, _, findErr := model.GetConsumeLogsForExport(model.LogExportParams{IsAdmin: true, Username: username, LogType: scenario.typ})
						require.NoError(t, findErr)
						want := scenario.count
						if username == "" {
							want++
						} else if username == "other" {
							want = 1
						}
						assert.Len(t, found, want)
						recorder := httptest.NewRecorder()
						ctx, _ := gin.CreateTestContext(recorder)
						ctx.Request = httptest.NewRequest("GET", fmt.Sprintf("/api/log/export?type=%d&format=csv&username=%s", scenario.typ, username), nil)
						ctx.Set("role", common.RoleRootUser)
						GetAllLogsExport(ctx)
						require.Equal(t, 200, recorder.Code)
						reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(recorder.Body.String(), "\xEF\xBB\xBF")))
						reader.FieldsPerRecord = -1
						exported, err := reader.ReadAll()
						require.NoError(t, err)
						require.GreaterOrEqual(t, len(exported), 3+want)
						otherPresent := false
						for _, record := range exported[3 : 3+want] {
							if username != "" {
								assert.Equal(t, username, record[0])
							}
							otherPresent = otherPresent || record[0] == "other"
						}
						assert.Equal(t, username != "owner", otherPresent)
					}
				})
			}
		})
	}
}

func TestLogExportRejectsUnsupportedTypesBeforeReadingDatabase(t *testing.T) {
	for _, value := range []string{"0", "3", "7", "-1", "all", "99"} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest("GET", "/api/log/self/export?type="+value, nil)
		GetUserLogsExport(ctx)
		assert.Equal(t, 400, recorder.Code)
		assert.Empty(t, recorder.Header().Get("Content-Disposition"))
	}
}

func TestTypedLogExportBatchingAndSizeLimits(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = nil, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
	})
	logs := make([]model.Log, 501)
	for i := range logs {
		logs[i] = model.Log{Type: model.LogTypeError, UserId: 7, CreatedAt: 1790000000, RequestId: fmt.Sprintf("error-%03d", i), Content: "upstream-error"}
	}
	require.NoError(t, db.CreateInBatches(logs, 100).Error)
	params := model.LogExportParams{UserID: 7, LogType: model.LogTypeError}
	for _, format := range []string{"csv", "xlsx"} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest("GET", "/api/log/self/export?type=5&format="+format, nil)
		writeLogExport(ctx, params)
		require.Equal(t, 200, recorder.Code)
		var rows [][]string
		if format == "csv" {
			reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(recorder.Body.Bytes(), []byte{0xef, 0xbb, 0xbf})))
			reader.FieldsPerRecord = -1
			rows, err = reader.ReadAll()
		} else {
			book, openErr := excelize.OpenReader(bytes.NewReader(recorder.Body.Bytes()))
			require.NoError(t, openErr)
			rows, err = book.GetRows("账单明细")
			require.NoError(t, book.Close())
		}
		require.NoError(t, err)
		require.Len(t, rows, 505)
		for i := range logs {
			assert.Equal(t, fmt.Sprintf("error-%03d", i), rows[i+3][6])
		}
		assert.Equal(t, "本期记录数：501 条", rows[len(rows)-1][0])
	}
	canceled, cancel := context.WithCancel(context.Background())
	visited := 0
	err = model.WalkConsumeLogsForExport(canceled, params, func(batch []*model.Log, _ map[model.LogExportFundingKey]model.LogExportFunding, _ map[model.LogExportFundingKey]model.LogExportAgencyPricing) error {
		visited += len(batch)
		cancel()
		return canceled.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 500, visited)
	params.UserID = 99
	empty, err := loadLogExportDocument(context.Background(), params)
	require.NoError(t, err)
	assert.Empty(t, empty.Rows)
	assert.Zero(t, empty.Total)

	longDetail := strings.Repeat("错", excelize.TotalCellChars+1)
	require.NoError(t, db.Model(&model.Log{}).Where("id = ?", logs[0].Id).Update("content", longDetail).Error)
	for _, format := range []string{"csv", "xlsx"} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest("GET", "/api/log/self/export?type=5&format="+format, nil)
		writeLogExport(ctx, model.LogExportParams{UserID: 7})
		if format == "csv" {
			assert.Equal(t, 200, recorder.Code)
			assert.Contains(t, recorder.Body.String(), longDetail)
		} else {
			assert.Equal(t, 400, recorder.Code)
			assert.Contains(t, recorder.Body.String(), errLogExportExcelCell.Error())
			assert.Empty(t, recorder.Header().Get("Content-Disposition"))
		}
	}

	for _, format := range []string{"pdf", "docx"} {
		require.NoError(t, db.Model(&model.Log{}).Where("id = ?", logs[0].Id).Update("content", strings.Repeat("x", 8<<20)).Error)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest("GET", "/api/log/self/export?type=5&format="+format, nil)
		writeLogExport(ctx, model.LogExportParams{UserID: 7})
		assert.Equal(t, 400, recorder.Code)
		assert.Contains(t, recorder.Body.String(), errLogExportDocumentSize.Error())
		assert.Empty(t, recorder.Header().Get("Content-Disposition"))
	}
}

func TestLogExportPDFPreservesLongEventDetails(t *testing.T) {
	document := logExportDocument{LogType: model.LogTypeError, Total: 1, Rows: []logExportRow{{Log: &model.Log{Content: strings.Repeat("详情", 100) + "END"}}}}
	rows, err := logExportPDFRows(document, logExportSchemaFor(document.LogType))
	require.NoError(t, err)
	require.Greater(t, len(rows), 1)
	var detail strings.Builder
	for _, row := range rows {
		detail.WriteString(row[len(row)-1])
	}
	assert.Equal(t, document.Rows[0].Log.Content, detail.String())
	document.Rows[0].Log.Content = strings.Repeat("错", 1<<20)
	_, err = buildLogExportPDF(document)
	require.ErrorIs(t, err, errLogExportDocumentSize)

	document.Rows[0].Log.Content = "upstream <error> & invalid\x00\x1b\xff chars"
	data, err := buildLogExportDOCX(document)
	require.NoError(t, err)
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	documentXML := readZipEntry(t, archive, "word/document.xml")
	decoder := xml.NewDecoder(strings.NewReader(documentXML))
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if chars, ok := token.(xml.CharData); ok {
			text.Write(chars)
		}
	}
	assert.Contains(t, text.String(), "upstream <error> & invalid")
	assert.Contains(t, text.String(), " chars")
}
