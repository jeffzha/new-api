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
	"encoding/csv"
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
}
