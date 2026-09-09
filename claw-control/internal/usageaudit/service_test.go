package usageaudit_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/QuantumNous/new-api/claw-control/internal/usageaudit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var cleanEvidenceScanner = evidence.ScannerFunc(func(context.Context, []byte) error { return nil })

func TestManualUsageAuditRequiresEvidenceAndLocksImmutably(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	service := usageaudit.New(db)
	evidenceService, err := evidence.New(db, t.TempDir(), bytes.Repeat([]byte{0x31}, 32), 1024, cleanEvidenceScanner)
	require.NoError(t, err)
	proof, err := evidenceService.Upload(evidence.UploadCommand{
		Filename: "account.json", DeclaredMIME: "application/json", Content: strings.NewReader(`{"cost":"12.30"}`), Actor: "collector",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	draft, err := service.Create(usageaudit.CreateCommand{
		PeriodStart: now.Add(-time.Hour), PeriodEnd: now, Source: "tencent_console_manual",
		AllocationConfidence: model.AllocationUnverified, UpstreamCostCNY: "12.30",
		Usage: map[string]string{"runtime_minutes": "60"}, Actor: "collector",
	})
	require.NoError(t, err)
	assert.Equal(t, model.UsageAuditStatusDraft, draft.Status)

	locked, err := service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: draft.RowVersion,
		AllocationConfidence: model.AllocationAccountOnly,
		EvidenceRef:          proof.EvidenceRef, EvidenceHash: proof.ContentHash,
		Actor: "reviewer",
	})
	require.NoError(t, err)
	assert.Equal(t, model.UsageAuditStatusLocked, locked.Status)

	_, err = service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: locked.RowVersion,
		AllocationConfidence: model.AllocationAccountOnly,
		EvidenceRef:          proof.EvidenceRef, EvidenceHash: proof.ContentHash,
		Actor: "reviewer",
	})
	assert.Error(t, err, "a locked audit must be immutable")
	revisionProof, err := evidenceService.Upload(evidence.UploadCommand{
		Filename: "account-correction.json", DeclaredMIME: "application/json",
		Content: strings.NewReader(`{"cost":"10.25","reason":"provider correction"}`), Actor: "reviewer",
	})
	require.NoError(t, err)
	revision, err := service.Revise(usageaudit.RevisionCommand{
		OriginalAuditID: locked.PublicID, ExpectedVersion: locked.RowVersion,
		AllocationConfidence: model.AllocationAccountOnly, UpstreamCostCNY: "10.25",
		Usage: map[string]string{"runtime_minutes": "50"}, EvidenceRef: revisionProof.EvidenceRef,
		EvidenceHash: revisionProof.ContentHash, Reason: "provider corrected the account statement", Actor: "reviewer",
	})
	require.NoError(t, err)
	assert.Equal(t, model.UsageAuditStatusLocked, revision.Replacement.Status)
	assert.Equal(t, "10.25", revision.Replacement.UpstreamCostCNY)
	assert.Equal(t, locked.ID, revision.Revision.OriginalAuditID)
	var superseded model.UsageAudit
	require.NoError(t, db.First(&superseded, locked.ID).Error)
	assert.Equal(t, model.UsageAuditStatusSuperseded, superseded.Status)
	assert.EqualValues(t, locked.RowVersion+1, superseded.RowVersion)
	_, err = service.Revise(usageaudit.RevisionCommand{
		OriginalAuditID: locked.PublicID, ExpectedVersion: superseded.RowVersion,
		AllocationConfidence: model.AllocationAccountOnly, UpstreamCostCNY: "9.00",
		Usage: map[string]string{}, EvidenceRef: revisionProof.EvidenceRef,
		EvidenceHash: revisionProof.ContentHash, Reason: "second rewrite", Actor: "reviewer",
	})
	assert.ErrorContains(t, err, "only a locked effective", "a superseded audit cannot be revised again")

	_, err = service.Create(usageaudit.CreateCommand{
		PeriodStart: now.Add(-time.Hour), PeriodEnd: now, Source: "tencent_console_manual",
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: "app-1",
		UpstreamCostCNY: "1.00", Actor: "collector",
	})
	assert.Error(t, err, "app_exact usage must identify a control-plane customer and App")
}

func TestAppExactUsageMustMatchSelectedProviderApp(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customer := model.Customer{
		CustomerCode: "usage-audit-customer", DisplayName: "Usage audit customer",
		Status: model.CustomerStatusActive, RowVersion: 1,
	}
	require.NoError(t, db.Create(&customer).Error)
	app := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: "prod",
		AppID: "provider-app-1", DisplayName: "Primary App",
		Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&app).Error)

	service := usageaudit.New(db)
	evidenceService, err := evidence.New(db, t.TempDir(), bytes.Repeat([]byte{0x32}, 32), 1024, cleanEvidenceScanner)
	require.NoError(t, err)
	proof, err := evidenceService.Upload(evidence.UploadCommand{
		CustomerID: &customer.ID, Filename: "app.json", DeclaredMIME: "application/json", Content: strings.NewReader(`{"app_id":"provider-app-1"}`), Actor: "collector",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	create := func(resourceIdentifier string) (*model.UsageAudit, error) {
		return service.Create(usageaudit.CreateCommand{
			CustomerID: &customer.ID, CustomerAppID: &app.ID,
			PeriodStart: now.Add(-time.Hour), PeriodEnd: now, Source: "tencent_console_manual",
			AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: resourceIdentifier,
			UpstreamCostCNY: "1.00", Usage: map[string]string{"runtime_minutes": "1"}, Actor: "collector",
		})
	}

	_, err = create("another-provider-app")
	assert.ErrorContains(t, err, "resource_identifier does not match")
	draft, err := create(app.AppID)
	require.NoError(t, err)

	_, err = service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: draft.RowVersion,
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: "another-provider-app",
		EvidenceRef: proof.EvidenceRef, EvidenceHash: proof.ContentHash,
		Actor: "reviewer",
	})
	assert.ErrorContains(t, err, "resource_identifier does not match")
	locked, err := service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: draft.RowVersion,
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: app.AppID,
		EvidenceRef: proof.EvidenceRef, EvidenceHash: proof.ContentHash,
		Actor: "reviewer",
	})
	require.NoError(t, err)
	assert.Equal(t, model.UsageAuditStatusLocked, locked.Status)
	unattributed, err := service.Create(usageaudit.CreateCommand{
		PeriodStart: now.Add(-time.Hour), PeriodEnd: now, Source: "tencent_fee_center_api",
		AllocationConfidence: model.AllocationUnverified, UpstreamCostCNY: "2.00",
		Usage: map[string]string{"runtime_minutes": "2"}, Actor: "collector",
	})
	require.NoError(t, err)
	attributed, err := service.Lock(usageaudit.LockCommand{
		AuditID: unattributed.PublicID, ExpectedVersion: unattributed.RowVersion,
		CustomerID: &customer.ID, CustomerAppID: &app.ID,
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: app.AppID,
		EvidenceRef: proof.EvidenceRef, EvidenceHash: proof.ContentHash, Actor: "reviewer",
	})
	require.NoError(t, err)
	require.NotNil(t, attributed.CustomerID)
	require.NotNil(t, attributed.CustomerAppID)
	assert.Equal(t, customer.ID, *attributed.CustomerID)
	assert.Equal(t, app.ID, *attributed.CustomerAppID)

	draft, err = create(app.AppID)
	require.NoError(t, err)
	_, err = service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: draft.RowVersion,
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: app.AppID,
		EvidenceRef: proof.EvidenceRef, EvidenceHash: "sha256:" + strings.Repeat("f", 64), Actor: "reviewer",
	})
	assert.ErrorContains(t, err, "does not match")
	_, err = service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: draft.RowVersion,
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: app.AppID,
		EvidenceRef: "evidence_missing", EvidenceHash: proof.ContentHash, Actor: "reviewer",
	})
	assert.ErrorContains(t, err, "uploaded evidence not found")
	accountProof, err := evidenceService.Upload(evidence.UploadCommand{
		Filename: "account.json", DeclaredMIME: "application/json", Content: strings.NewReader(`{"scope":"account"}`), Actor: "collector",
	})
	require.NoError(t, err)
	_, err = service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: draft.RowVersion,
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: app.AppID,
		EvidenceRef: accountProof.EvidenceRef, EvidenceHash: accountProof.ContentHash, Actor: "reviewer",
	})
	assert.ErrorContains(t, err, "does not belong")
	otherCustomer := model.Customer{CustomerCode: "other-evidence-customer", DisplayName: "Other evidence customer", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&otherCustomer).Error)
	otherProof, err := evidenceService.Upload(evidence.UploadCommand{
		CustomerID: &otherCustomer.ID, Filename: "other.json", DeclaredMIME: "application/json", Content: strings.NewReader(`{"scope":"other"}`), Actor: "collector",
	})
	require.NoError(t, err)
	_, err = service.Lock(usageaudit.LockCommand{
		AuditID: draft.PublicID, ExpectedVersion: draft.RowVersion,
		AllocationConfidence: model.AllocationAppExact, ResourceIdentifier: app.AppID,
		EvidenceRef: otherProof.EvidenceRef, EvidenceHash: otherProof.ContentHash, Actor: "reviewer",
	})
	assert.ErrorContains(t, err, "does not belong")
}
