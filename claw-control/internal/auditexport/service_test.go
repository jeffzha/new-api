package auditexport_test

import (
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/auditexport"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportIsScopedPaginatedAuditedAndSpreadsheetSafe(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	firstCustomerID, secondCustomerID := uint64(11), uint64(22)
	fixtures := []model.AdminAudit{
		{CustomerID: &firstCustomerID, Actor: "=cmd|' /C calc'!A0", Action: "customer.update", ResourceType: "customer", ResourceID: "11", Result: "success", Reason: " @formula", CreatedAt: now.Add(-2 * time.Hour)},
		{CustomerID: &firstCustomerID, Actor: "safe-admin", Action: "app.enable", ResourceType: "customer_app", ResourceID: "12", Result: "success", CreatedAt: now.Add(-time.Hour)},
		{CustomerID: &secondCustomerID, Actor: "other-admin", Action: "app.enable", ResourceType: "customer_app", ResourceID: "22", Result: "success", CreatedAt: now.Add(-time.Hour)},
	}
	require.NoError(t, db.Create(&fixtures).Error)
	service := auditexport.New(db)

	result, err := service.Export(auditexport.Query{
		CustomerID: &firstCustomerID, StartAt: now.Add(-24 * time.Hour), EndAt: now,
		Limit: 1,
	}, "csv", "admin-session:1", "request-export")
	require.NoError(t, err)
	assert.NotZero(t, result.NextCursor)
	reader := csv.NewReader(strings.NewReader(string(result.Body)))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "'=cmd|' /C calc'!A0", records[1][2])
	assert.Equal(t, "' @formula", records[1][9])

	var exportAudit model.AdminAudit
	require.NoError(t, db.Where("action = ? AND request_id = ?", "admin.audit.export", "request-export").First(&exportAudit).Error)
	assert.Equal(t, result.ExportID, exportAudit.ResourceID)
	assert.Equal(t, &firstCustomerID, exportAudit.CustomerID)

	jsonResult, err := service.Export(auditexport.Query{
		CustomerID: &firstCustomerID, StartAt: now.Add(-24 * time.Hour), EndAt: now,
		AfterID: result.NextCursor, ThroughID: result.ThroughID, Limit: 100,
	}, "json", "admin-session:1", "request-export-json")
	require.NoError(t, err)
	assert.Contains(t, string(jsonResult.Body), `"actor":"safe-admin"`)
	assert.NotContains(t, string(jsonResult.Body), `"actor":"other-admin"`)
}

func TestExportRejectsUnsafeScope(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	service := auditexport.New(db)
	now := time.Now().UTC()
	_, err = service.Export(auditexport.Query{StartAt: now.Add(-400 * 24 * time.Hour), EndAt: now, Limit: 10}, "csv", "admin", "request")
	assert.Error(t, err)
	_, err = service.Export(auditexport.Query{StartAt: now.Add(-time.Hour), EndAt: now, Limit: 1001}, "csv", "admin", "request")
	assert.Error(t, err)
}
