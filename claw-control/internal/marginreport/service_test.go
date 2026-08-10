package marginreport_test

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/marginreport"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarginReportSeparatesCustomerAllocationFromAccountOnlyCost(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	customer := model.Customer{CustomerCode: "margin-customer", DisplayName: "Margin customer", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	invoice := model.CustomerInvoice{
		InvoiceNumber: "INV-MARGIN-1", PeriodID: 1001, CustomerID: customer.ID,
		Description: "Fixed monthly plan", FixedAmountCNY: "100.00", ManualAdjustmentCNY: "0.00", AmountCNY: "100.00",
		Status: model.InvoiceStatusPaid, IssuedAt: now.Add(-time.Hour),
	}
	require.NoError(t, db.Create(&invoice).Error)
	createAudit := func(id, confidence, cost, status string, customerID *uint64) {
		t.Helper()
		audit := model.UsageAudit{
			PublicID: id, CustomerID: customerID, PeriodStart: now.Add(-2 * time.Hour), PeriodEnd: now.Add(-time.Hour),
			Source: "test", AllocationConfidence: confidence, UpstreamCostCNY: cost, UsageJSON: "{}",
			Status: status, RowVersion: 1, CreatedBy: "test",
		}
		require.NoError(t, db.Create(&audit).Error)
	}
	createAudit("usage-exact", model.AllocationAppExact, "20.00", model.UsageAuditStatusLocked, &customer.ID)
	createAudit("usage-estimated", model.AllocationEstimatedAllocation, "5.00", model.UsageAuditStatusLocked, &customer.ID)
	createAudit("usage-account", model.AllocationAccountOnly, "7.00", model.UsageAuditStatusLocked, nil)
	createAudit("usage-unverified", model.AllocationUnverified, "3.00", model.UsageAuditStatusDraft, &customer.ID)

	service := marginreport.New(db)
	customerReport, err := service.Build(marginreport.Command{CustomerID: &customer.ID})
	require.NoError(t, err)
	assert.Equal(t, "100.00", customerReport.InvoicedRevenueCNY)
	assert.Equal(t, "25.00", customerReport.ReviewedCostCNY)
	assert.Equal(t, "75.00", customerReport.MarginCNY)
	assert.Equal(t, model.AllocationEstimatedAllocation, customerReport.Confidence)
	assert.Equal(t, "0.00", customerReport.Breakdown.AccountOnlyCostCNY)
	assert.False(t, customerReport.AccountOnlyAllocatedToCustomer)

	platformReport, err := service.Build(marginreport.Command{})
	require.NoError(t, err)
	assert.Equal(t, "32.00", platformReport.ReviewedCostCNY)
	assert.Equal(t, "68.00", platformReport.MarginCNY)
	assert.Equal(t, model.AllocationAccountOnly, platformReport.Confidence)
	assert.Equal(t, "7.00", platformReport.Breakdown.AccountOnlyCostCNY)
	assert.Equal(t, "3.00", platformReport.Breakdown.UnverifiedCostCNY)

	var unchanged model.CustomerInvoice
	require.NoError(t, db.First(&unchanged, invoice.ID).Error)
	assert.Equal(t, "100.00", unchanged.AmountCNY, "margin reporting must never change customer billing")
}

func TestMarginReportWithoutReviewedEvidenceIsUnverified(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	report, err := marginreport.New(db).Build(marginreport.Command{})
	require.NoError(t, err)
	assert.Equal(t, model.AllocationUnverified, report.Confidence)
	assert.Equal(t, "0.00", report.MarginCNY)
}
