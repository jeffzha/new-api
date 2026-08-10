package plan_test

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/plan"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/QuantumNous/new-api/claw-control/internal/usageaudit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixedPeriodPaymentCreatesImmutableInvoice(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customerService := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test"))
	planService := plan.New(db)
	usageService := usageaudit.New(db)
	createdCustomer, err := customerService.Create(customer.CreateCommand{
		CustomerCode: "billing-test", DisplayName: "Billing Test", Actor: "test",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	published, err := planService.Publish(plan.PublishCommand{
		PlanCode: "claw-standard", DisplayName: "Claw Standard", MonthlyPriceCNY: "100.00",
		Capabilities: []string{"chat"}, Limits: productpolicy.Limits{
			CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 600,
			MaxReasoningRounds: 20, MaxOutputTokens: 8192, WebSearchPerTurn: 0, MaxFileBytes: 10_000_000,
		},
		ValidFrom: now.Add(-time.Hour), Actor: "test",
	})
	require.NoError(t, err)
	periodStart := now.Add(-time.Minute)
	period, err := planService.CreatePeriod(plan.CreatePeriodCommand{
		CustomerID: createdCustomer.ID, PlanVersionID: published.Version.ID,
		StartAt: periodStart, EndAt: periodStart.AddDate(0, 1, 0), Actor: "test",
	})
	require.NoError(t, err)
	_, err = planService.CreatePeriod(plan.CreatePeriodCommand{
		CustomerID: createdCustomer.ID, PlanVersionID: published.Version.ID,
		StartAt: now, EndAt: now.AddDate(0, 1, 0), Actor: "test",
	})
	assert.Error(t, err)

	_, err = planService.ConfirmPayment(plan.ConfirmPaymentCommand{
		PeriodID: period.ID, ExpectedVersion: period.RowVersion,
		PaymentEvidenceRef: "evidence_missing", Actor: "test",
	})
	assert.ErrorContains(t, err, "payment evidence not found")

	accountOnlyEvidence := model.EvidenceObject{
		PublicID: "evidence_account_only", OriginalFilename: "account.json",
		MIMEType: "application/json", SizeBytes: 2,
		ContentSHA256: "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StorageKey:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		FormatVersion: 1, Status: model.EvidenceStatusActive, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&accountOnlyEvidence).Error)
	_, err = planService.ConfirmPayment(plan.ConfirmPaymentCommand{
		PeriodID: period.ID, ExpectedVersion: period.RowVersion,
		PaymentEvidenceRef: accountOnlyEvidence.PublicID, Actor: "test",
	})
	assert.ErrorContains(t, err, "does not belong")

	otherCustomer, err := customerService.Create(customer.CreateCommand{
		CustomerCode: "billing-other", DisplayName: "Billing Other", Actor: "test",
	})
	require.NoError(t, err)
	otherCustomerID := otherCustomer.ID
	crossCustomerEvidence := model.EvidenceObject{
		PublicID: "evidence_other_customer", CustomerID: &otherCustomerID,
		OriginalFilename: "other.json", MIMEType: "application/json", SizeBytes: 2,
		ContentSHA256: "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		StorageKey:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		FormatVersion: 1, Status: model.EvidenceStatusActive, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&crossCustomerEvidence).Error)
	_, err = planService.ConfirmPayment(plan.ConfirmPaymentCommand{
		PeriodID: period.ID, ExpectedVersion: period.RowVersion,
		PaymentEvidenceRef: crossCustomerEvidence.PublicID, Actor: "test",
	})
	assert.ErrorContains(t, err, "does not belong")

	customerID := createdCustomer.ID
	paymentEvidence := model.EvidenceObject{
		PublicID: "evidence_payment_1", CustomerID: &customerID,
		OriginalFilename: "payment.json", MIMEType: "application/json", SizeBytes: 2,
		ContentSHA256: "sha256:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		StorageKey:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		FormatVersion: 1, Status: model.EvidenceStatusActive, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&paymentEvidence).Error)
	paid, err := planService.ConfirmPayment(plan.ConfirmPaymentCommand{
		PeriodID: period.ID, ExpectedVersion: period.RowVersion,
		PaymentEvidenceRef: paymentEvidence.PublicID, Actor: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, model.PeriodStatusActive, paid.Period.Status)
	assert.Equal(t, "100.00", paid.Invoice.AmountCNY)
	_, err = planService.VoidInvoice(plan.VoidInvoiceCommand{
		InvoiceID: paid.Invoice.ID, Reason: "premature accounting request", Actor: "test",
	})
	assert.ErrorContains(t, err, "period must be canceled", "a void must not silently revoke an active paid service period")
	activeApp := model.CustomerApp{
		CustomerID: createdCustomer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentCloud,
		AppID: "cancel-test-app", DisplayName: "Cancel Test App", Status: model.AppStatusActive,
		AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&activeApp).Error)
	canceled, err := planService.CancelPeriod(plan.CancelPeriodCommand{
		PeriodID: paid.Period.ID, ExpectedVersion: paid.Period.RowVersion,
		Reason: "customer contract ended", Actor: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, model.PeriodStatusCanceled, canceled.Status)
	assert.Equal(t, model.PaymentStatusPaid, canceled.PaymentStatus, "canceling service access must not rewrite paid billing evidence")
	assert.NotNil(t, canceled.CanceledAt)
	require.NoError(t, db.First(&activeApp, activeApp.ID).Error)
	assert.Equal(t, model.AppStatusSuspended, activeApp.Status, "canceling the only active period must revoke new App work")
	assert.EqualValues(t, 2, activeApp.AuthEpoch)
	_, err = planService.CancelPeriod(plan.CancelPeriodCommand{
		PeriodID: paid.Period.ID, ExpectedVersion: paid.Period.RowVersion,
		Reason: "stale retry", Actor: "test",
	})
	assert.ErrorContains(t, err, "row version changed")
	var unchangedInvoice model.CustomerInvoice
	require.NoError(t, db.First(&unchangedInvoice, paid.Invoice.ID).Error)
	assert.Equal(t, model.InvoiceStatusPaid, unchangedInvoice.Status, "period cancellation must not implicitly void or refund its invoice")
	voided, err := planService.VoidInvoice(plan.VoidInvoiceCommand{
		InvoiceID: paid.Invoice.ID, Reason: "contract canceled before service completion", Actor: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, model.InvoiceStatusVoid, voided.Status)
	assert.NotNil(t, voided.VoidAt)

	_, err = usageService.Create(usageaudit.CreateCommand{
		PeriodStart: now.Add(-time.Hour), PeriodEnd: now, Source: "tencent_console_manual",
		AllocationConfidence: model.AllocationAccountOnly, UpstreamCostCNY: "999.99",
		Usage: map[string]string{"runtime_minutes": "1234"}, Actor: "auditor",
	})
	require.NoError(t, err)
	invoices, err := planService.ListInvoices(createdCustomer.ID, 10)
	require.NoError(t, err)
	require.Len(t, invoices, 1)
	assert.Equal(t, "100.00", invoices[0].AmountCNY, "usage audit must never change a fixed invoice")
	assert.Equal(t, model.InvoiceStatusVoid, invoices[0].Status, "usage evidence must not restore a void invoice")
}

func TestPlanVersionPersistsCatalogCapabilitiesWithoutExecutionGrant(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	service := plan.New(db)
	result, err := service.Publish(plan.PublishCommand{
		PlanCode: "catalog-read", DisplayName: "Catalog Read", MonthlyPriceCNY: "20.00",
		Capabilities: []string{
			productpolicy.CapabilityCatalogModels,
			productpolicy.CapabilityCatalogSkills,
			productpolicy.CapabilityCatalogPlugins,
		},
		Limits: productpolicy.Limits{
			CustomerConcurrency: 1, UserConcurrency: 1, MaxRuntimeSeconds: 60,
			MaxReasoningRounds: 1, MaxOutputTokens: 100, MaxFileBytes: 0,
		},
		ValidFrom: time.Now().UTC(), Actor: "test",
	})
	require.NoError(t, err)
	var capabilities []string
	require.NoError(t, jsonx.Unmarshal([]byte(result.Version.CapabilitiesJSON), &capabilities))
	require.Len(t, capabilities, 3)
	for _, capability := range capabilities {
		assert.True(t, productpolicy.IsCatalogReadCapability(capability))
		assert.False(t, productpolicy.IsExecutionCapability(capability))
	}
}
