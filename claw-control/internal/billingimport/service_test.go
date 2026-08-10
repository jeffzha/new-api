package billingimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/QuantumNous/new-api/claw-control/internal/usageaudit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var cleanBillingEvidenceScanner = evidence.ScannerFunc(func(context.Context, []byte) error { return nil })

type fakeBillingClient struct {
	mu          sync.Mutex
	pages       []*DetailPage
	adjustment  *AdjustmentResult
	err         error
	detailCalls int
	started     chan struct{}
	release     chan struct{}
}

func (client *fakeBillingClient) DescribeBillDetail(ctx context.Context, input DetailRequest) (*DetailPage, error) {
	client.mu.Lock()
	index := client.detailCalls
	client.detailCalls++
	started := client.started
	release := client.release
	err := client.err
	var page *DetailPage
	if index < len(client.pages) {
		page = client.pages[index]
	}
	client.mu.Unlock()
	if started != nil && index == 0 {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
	}
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, errors.New("unexpected_detail_page")
	}
	return page, nil
}

func (client *fakeBillingClient) DescribeBillAdjustInfo(context.Context, string) (*AdjustmentResult, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.err != nil {
		return nil, client.err
	}
	return client.adjustment, nil
}

func TestBillingImportPaginatesAggregatesExactlyAndCreatesOnlyUnverifiedAccountDraft(t *testing.T) {
	client := &fakeBillingClient{
		pages: []*DetailPage{
			{Details: []BillDetail{
				{BusinessCode: "p_adp", Components: []BillComponent{{RealCost: "1.00000001"}, {RealCost: "-0.10000001"}}},
				{BusinessCode: "p_adp", Components: []BillComponent{{RealCost: "2.345"}}},
			}, RequestID: "bill-query-1", Context: "next", Raw: json.RawMessage(`{"Response":{"RequestId":"bill-query-1"}}`)},
			{Details: []BillDetail{{BusinessCode: "p_adp", Components: []BillComponent{{RealCost: "0.000000009"}}}}, RequestID: "bill-query-2", Raw: json.RawMessage(`{"Response":{"RequestId":"bill-query-2"}}`)},
		},
		adjustment: &AdjustmentResult{Total: 1, Count: 1, RequestID: "adjust-query-1", Raw: json.RawMessage(`{"Response":{"RequestId":"adjust-query-1","Total":1,"Data":[{}]}}`)},
	}
	service, db := newTestService(t, client, Config{
		Enabled: true, PayerUIN: "10001", PageSize: 2, MaxPages: 10, MaxRecords: 20,
		MaxAttempts: 1, LeaseDuration: 5 * time.Minute, MaxEvidenceBytes: 1 << 20,
	})
	first, err := service.Create(CreateCommand{Month: "2026-08", BusinessCode: "p_adp", Actor: "admin", RequestID: "admin-request"})
	require.NoError(t, err)
	second, err := service.Create(CreateCommand{Month: "2026-08", BusinessCode: "p_adp", Actor: "other-admin", RequestID: "other-request"})
	require.NoError(t, err)
	assert.Equal(t, first.ImportID, second.ImportID)

	processed, err := service.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	result, err := service.Get(first.ImportID)
	require.NoError(t, err)
	assert.Equal(t, model.BillingImportStatusDraftCreated, result.Status)
	assert.Equal(t, "3.245000009", result.UpstreamCostCNY)
	assert.Equal(t, 2, result.DetailPageCount)
	assert.Equal(t, 3, result.DetailRecordCount)
	assert.Equal(t, []string{"bill-query-1", "bill-query-2"}, result.BillQueryRequestIDs)
	assert.Equal(t, []string{"adjust-query-1"}, result.AdjustQueryRequestIDs)
	assert.ElementsMatch(t, []string{"account_scope_allocation_unverified", "negative_bill_component", "billing_adjustment_present"}, result.ReviewReasons)
	assert.True(t, result.ManualReviewRequired)
	assert.True(t, result.AccountScoped)
	assert.Equal(t, model.AllocationUnverified, result.AllocationConfidence)
	assert.False(t, result.InvoiceMutation)
	assert.NotEmpty(t, result.EvidenceRef)
	assert.NotEmpty(t, result.UsageAuditID)

	var draft model.UsageAudit
	require.NoError(t, db.Where("public_id = ?", result.UsageAuditID).First(&draft).Error)
	assert.Nil(t, draft.CustomerID)
	assert.Nil(t, draft.CustomerAppID)
	assert.Nil(t, draft.PlanPeriodID)
	assert.Equal(t, model.AllocationUnverified, draft.AllocationConfidence)
	assert.Equal(t, model.UsageAuditStatusDraft, draft.Status)
	assert.Equal(t, "3.245000009", draft.UpstreamCostCNY)
	assert.Equal(t, result.EvidenceRef, draft.EvidenceRef)
	var invoiceCount int64
	require.NoError(t, db.Model(&model.CustomerInvoice{}).Count(&invoiceCount).Error)
	assert.Zero(t, invoiceCount)
}

func TestBillingImportClampsNegativeNetAndFailureStoresOnlySafeCode(t *testing.T) {
	failing := &fakeBillingClient{err: &ProviderError{Code: "AuthFailure.SecretIdNotFound", RequestID: "provider-query", Action: "DescribeBillDetail"}}
	service, db := newTestService(t, failing, Config{
		Enabled: true, PayerUIN: "10001", PageSize: 1, MaxPages: 2, MaxRecords: 2,
		MaxAttempts: 1, LeaseDuration: 5 * time.Minute, MaxEvidenceBytes: 1 << 20,
	})
	run, err := service.Create(CreateCommand{Month: "2026-08", BusinessCode: "p_adp", Actor: "admin"})
	require.NoError(t, err)
	processed, err := service.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	failed, err := service.Get(run.ImportID)
	require.NoError(t, err)
	assert.Equal(t, model.BillingImportStatusFailed, failed.Status)
	assert.Equal(t, "AuthFailure.SecretIdNotFound", failed.ErrorCode)
	assert.Equal(t, []string{"provider-query"}, failed.BillQueryRequestIDs)
	serialized, err := jsonx.Marshal(failed)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), "10001")

	failing.mu.Lock()
	failing.err = nil
	failing.pages = []*DetailPage{{
		Details:   []BillDetail{{BusinessCode: "p_adp", Components: []BillComponent{{RealCost: "-9.25"}}}},
		RequestID: "bill-negative", Raw: json.RawMessage(`{"Response":{"RequestId":"bill-negative"}}`),
	}, {RequestID: "bill-end", Raw: json.RawMessage(`{"Response":{"RequestId":"bill-end"}}`)}}
	failing.adjustment = &AdjustmentResult{RequestID: "adjust-none", Raw: json.RawMessage(`{"Response":{"RequestId":"adjust-none","Total":0,"Data":[]}}`)}
	failing.detailCalls = 0
	failing.mu.Unlock()
	_, err = service.Retry(run.ImportID, "review-admin", "retry-request")
	require.NoError(t, err)
	processed, err = service.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.False(t, processed, "provider-account cooldown must survive job release")
	service.clock = func() time.Time { return time.Date(2026, time.August, 9, 12, 0, 1, 0, time.UTC) }
	processed, err = service.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	completed, err := service.Get(run.ImportID)
	require.NoError(t, err)
	assert.Equal(t, "0", completed.UpstreamCostCNY)
	assert.Contains(t, completed.ReviewReasons, "negative_net_cost_clamped_to_zero")

	var draft model.UsageAudit
	require.NoError(t, db.Where("public_id = ?", completed.UsageAuditID).First(&draft).Error)
	assert.Equal(t, "0", draft.UpstreamCostCNY)
}

func TestBillingImportClaimAllowsOnlyOneReplicaToProcessARun(t *testing.T) {
	client := &fakeBillingClient{
		pages:      []*DetailPage{{RequestID: "bill-empty", Raw: json.RawMessage(`{"Response":{"RequestId":"bill-empty"}}`)}},
		adjustment: &AdjustmentResult{RequestID: "adjust-empty", Raw: json.RawMessage(`{"Response":{"RequestId":"adjust-empty","Total":0,"Data":[]}}`)},
		started:    make(chan struct{}), release: make(chan struct{}),
	}
	service, _ := newTestService(t, client, Config{
		Enabled: true, PayerUIN: "10001", PageSize: 1, MaxPages: 2, MaxRecords: 2,
		MaxAttempts: 1, LeaseDuration: 5 * time.Minute, MaxEvidenceBytes: 1 << 20,
	})
	_, err := service.Create(CreateCommand{Month: "2026-08", BusinessCode: "p_adp", Actor: "admin"})
	require.NoError(t, err)
	_, err = service.Create(CreateCommand{Month: "2026-08", BusinessCode: "p_other", Actor: "admin"})
	require.NoError(t, err)
	firstResult := make(chan error, 1)
	go func() {
		_, processErr := service.ProcessOne(context.Background())
		firstResult <- processErr
	}()
	<-client.started
	processed, err := service.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.False(t, processed)
	close(client.release)
	require.NoError(t, <-firstResult)
	client.mu.Lock()
	assert.Equal(t, 1, client.detailCalls)
	client.mu.Unlock()
}

func newTestService(t *testing.T, client Client, cfg Config) (*Service, *gorm.DB) {
	t.Helper()
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	evidenceService, err := evidence.New(db, t.TempDir(), bytes.Repeat([]byte{0x75}, 32), 1<<20, cleanBillingEvidenceScanner)
	require.NoError(t, err)
	service, err := New(db, client, evidenceService, usageaudit.New(db), cfg)
	require.NoError(t, err)
	service.clock = func() time.Time { return time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC) }
	return service, db
}
