package retention_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/retention"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type retentionClient struct {
	receipt retention.DeliveryReceipt
	err     error
	calls   []retention.DeliveryIntent
}

func (c *retentionClient) Deliver(_ context.Context, intent retention.DeliveryIntent) (retention.DeliveryReceipt, error) {
	c.calls = append(c.calls, intent)
	if c.err != nil {
		return retention.DeliveryReceipt{}, c.err
	}
	c.receipt.IntentID = intent.IntentID
	return c.receipt, nil
}

func TestRetentionDryRunAndExecutionDeleteOnlyOperationalCustomerData(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	archivedAt := now.AddDate(0, 0, -60)
	customer := model.Customer{
		CustomerCode: "retention-customer", DisplayName: "Retention",
		Status: model.CustomerStatusArchived, RowVersion: 2, ArchivedAt: &archivedAt,
	}
	require.NoError(t, db.Create(&customer).Error)
	member := model.CustomerMember{
		CustomerID: customer.ID, NewAPIUserID: 501, Role: "member",
		Status: model.MemberStatusDisabled, MembershipSlot: "historical:501", AuthEpoch: 2,
	}
	require.NoError(t, db.Create(&member).Error)
	identity := model.IdentityBinding{
		PublicID: "wid_retention", CustomerID: customer.ID, NewAPIUserID: 501,
		CanonicalSubject: "napi:prod:customer:retention:user:501", ADPAccountID: "adp-retention",
		ADPAccountVersion: 1, IdentityVersion: "v1", Status: model.IdentityStatusDisabled,
		AuthEpoch: 2, RowVersion: 2,
	}
	require.NoError(t, db.Create(&identity).Error)
	require.NoError(t, db.Create(&model.ADPAccountBinding{
		IdentityBindingID: identity.ID, ADPAccountID: identity.ADPAccountID,
		CustomerID: customer.ID, NewAPIUserID: 501,
	}).Error)
	app := model.CustomerApp{
		CustomerID: customer.ID, Slot: "archived:1", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "retention-app", DisplayName: "Archived App", Status: model.AppStatusArchived,
		AuthEpoch: 2, RowVersion: 2,
	}
	require.NoError(t, db.Create(&app).Error)
	config := model.AppConfigVersion{
		CustomerAppID: app.ID, ConfigVersion: 1, Status: model.AppConfigStatusSuperseded,
		Region: "ap-guangzhou", SpaceID: "space", TemplateAgentID: "template",
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_ARCHIVED_APP_KEY", AppKeyFingerprint: "sha256:test",
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&config).Error)
	require.NoError(t, db.Create(&model.AppVerification{
		PublicID: "verify_retention", CustomerAppID: app.ID, AppConfigVersionID: config.ID,
		Result: "verified", ProviderRequestIDsJSON: `[]`, SanitizedResponseHash: "sha256:" + strings.Repeat("a", 64),
		VerifiedBy: "test", VerifiedAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.ResourceBinding{
		PublicID: "wrb_retention", ResourceKeyHash: strings.Repeat("1", 64), SourceEventKeyHash: strings.Repeat("2", 64),
		IdentityBindingID: identity.ID, CustomerID: customer.ID, CustomerAppID: app.ID,
		AppConfigVersionID: config.ID, ResourceType: "account", ResourceID: "retention-resource",
		SourceService: "adp-backend", SourceEventID: "retention-event", SourceVersion: 1,
		SourcePayloadHash: strings.Repeat("3", 64), Status: model.ResourceBindingStatusActive,
	}).Error)
	require.NoError(t, db.Create(&model.ControlSession{
		TokenHash: "retention-session", IdentityBindingID: identity.ID, CustomerID: customer.ID,
		NewAPIUserID: 501, CustomerAppID: app.ID, AppConfigVersionID: config.ID,
		AccessMode: "readonly", AuthEpoch: 1, IdentityVersion: "v1",
		ExpiresAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Hour),
	}).Error)
	require.NoError(t, db.Create(&model.SSOTicket{
		TokenHash: "retention-sso", IdentityBindingID: identity.ID, CustomerID: customer.ID,
		NewAPIUserID: 501, CustomerAppID: app.ID, AppConfigVersionID: config.ID,
		AccessMode: "readonly", AuthEpoch: 1, IdentityVersion: "v1", ExpiresAt: now.Add(-time.Hour),
	}).Error)
	deliveredAt := now.Add(-time.Hour)
	require.NoError(t, db.Create(&model.ControlOutbox{
		EventKey: "retention-delivered", CustomerID: &customer.ID, EventType: "CACHE_INVALIDATE",
		PayloadJSON: `{}`, Status: model.OutboxStatusDelivered, NextRetryAt: now.Add(-time.Hour), DeliveredAt: &deliveredAt,
	}).Error)
	evidence := model.EvidenceObject{
		PublicID: "evidence_retention", CustomerID: &customer.ID, OriginalFilename: "legal.json",
		MIMEType: "application/json", SizeBytes: 2, ContentSHA256: "sha256:" + strings.Repeat("a", 64),
		StorageKey: strings.Repeat("a", 64), FormatVersion: 1, Status: model.EvidenceStatusActive, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&evidence).Error)
	period := model.PlanPeriod{
		CustomerID: customer.ID, PlanVersionID: 1, StartAt: now.AddDate(0, -2, 0), EndAt: now.AddDate(0, -1, 0),
		AmountCNY: "100.00", PaymentMode: model.PaymentModeOfflineManual,
		PaymentStatus: model.PaymentStatusPaid, Status: model.PeriodStatusExpired,
		SnapshotJSON: `{}`, RowVersion: 1,
	}
	require.NoError(t, db.Create(&period).Error)
	require.NoError(t, db.Create(&model.CustomerInvoice{
		InvoiceNumber: "CINV-RETENTION", PeriodID: period.ID, CustomerID: customer.ID,
		Description: "Legal invoice", FixedAmountCNY: "100.00", ManualAdjustmentCNY: "0.00",
		AmountCNY: "100.00", Status: model.InvoiceStatusPaid, EvidenceRef: evidence.PublicID, IssuedAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.UsageAudit{
		PublicID: "usage_retention", CustomerID: &customer.ID,
		PeriodStart: period.StartAt, PeriodEnd: period.EndAt, Source: "manual",
		AllocationConfidence: model.AllocationAccountOnly, UpstreamCostCNY: "10.00",
		UsageJSON: `{}`, EvidenceRef: evidence.PublicID, EvidenceHash: evidence.ContentSHA256,
		Status: model.UsageAuditStatusLocked, RowVersion: 1, CreatedBy: "test",
	}).Error)
	service := retention.New(db)
	policy, err := service.SetPolicy(retention.SetPolicyCommand{
		CustomerID: customer.ID, RetentionDays: 30, Actor: "admin-session:1", RequestID: "policy-request",
	})
	require.NoError(t, err)
	assert.False(t, policy.LegalHold)

	dryRun, err := service.DryRun(customer.ID, now, "admin-session:1", "dry-run-request")
	require.NoError(t, err)
	assert.Equal(t, int64(1), dryRun.Counts.ResourceBindings)
	assert.Equal(t, int64(1), dryRun.Counts.PreservedEvidence)
	assert.Equal(t, int64(1), dryRun.Counts.PreservedInvoices)
	assert.Equal(t, int64(1), dryRun.Counts.PreservedUsage)

	executed, err := service.Execute(retention.ExecuteCommand{
		RunID: dryRun.Run.PublicID, ExpectedVersion: dryRun.Run.RowVersion,
		Actor: "admin-session:2", RequestID: "execute-request",
	}, now)
	require.NoError(t, err)
	assert.Equal(t, model.RetentionRunStatusPending, executed.Run.Status)
	client := &retentionClient{receipt: retention.DeliveryReceipt{
		ReceiptID: "receipt-retention", Status: "completed", Counts: map[string]int{"conversations": 1},
	}}
	processed, err := retention.NewCoordinator(db, client).ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Len(t, client.calls, 1)
	assert.False(t, client.calls[0].LegalHold)
	retried, err := service.Execute(retention.ExecuteCommand{
		RunID: dryRun.Run.PublicID, ExpectedVersion: dryRun.Run.RowVersion,
		Actor: "admin-session:2", RequestID: "execute-retry",
	}, now)
	require.NoError(t, err)
	assert.Equal(t, model.RetentionRunStatusCompleted, retried.Run.Status)

	for _, item := range []any{
		&model.ResourceBinding{}, &model.ControlSession{}, &model.SSOTicket{},
		&model.ADPAccountBinding{}, &model.IdentityBinding{}, &model.CustomerMember{},
		&model.AppVerification{}, &model.AppConfigVersion{}, &model.CustomerApp{},
	} {
		var count int64
		require.NoError(t, db.Model(item).Count(&count).Error)
		assert.Zero(t, count)
	}
	for _, item := range []any{&model.EvidenceObject{}, &model.CustomerInvoice{}, &model.UsageAudit{}, &model.PlanPeriod{}} {
		var count int64
		require.NoError(t, db.Model(item).Count(&count).Error)
		assert.Equal(t, int64(1), count, "legal evidence, invoice, usage, and period records must survive ordinary retention cleanup")
	}
	var auditCount int64
	require.NoError(t, db.Model(&model.AdminAudit{}).Where("customer_id = ?", customer.ID).Count(&auditCount).Error)
	assert.GreaterOrEqual(t, auditCount, int64(3), "policy, dry-run, and execution audit records are retained")
}

func TestRetentionDeliveryFailureAndLegalHoldKeepLocalData(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	archivedAt := now.AddDate(0, 0, -60)
	customer := model.Customer{CustomerCode: "retention-delivery", DisplayName: "Retention", Status: model.CustomerStatusArchived, RowVersion: 1, ArchivedAt: &archivedAt}
	require.NoError(t, db.Create(&customer).Error)
	member := model.CustomerMember{CustomerID: customer.ID, NewAPIUserID: 800, Role: "member", Status: model.MemberStatusDisabled, MembershipSlot: "historical:800"}
	require.NoError(t, db.Create(&member).Error)
	service := retention.New(db)
	policy, err := service.SetPolicy(retention.SetPolicyCommand{CustomerID: customer.ID, RetentionDays: 30, Actor: "admin"})
	require.NoError(t, err)
	run, err := service.DryRun(customer.ID, now, "admin", "dry")
	require.NoError(t, err)
	_, err = service.Execute(retention.ExecuteCommand{RunID: run.Run.PublicID, ExpectedVersion: run.Run.RowVersion, Actor: "admin"}, now)
	require.NoError(t, err)

	failing := &retentionClient{err: errors.New("ADP unavailable")}
	processed, err := retention.NewCoordinator(db, failing).ProcessOne(context.Background())
	assert.True(t, processed)
	assert.ErrorContains(t, err, "unavailable")
	var memberCount int64
	require.NoError(t, db.Model(&model.CustomerMember{}).Where("customer_id = ?", customer.ID).Count(&memberCount).Error)
	assert.Equal(t, int64(1), memberCount)

	require.NoError(t, db.Model(&model.CustomerRetentionDelivery{}).Where("customer_id = ?", customer.ID).Update("next_retry_at", now).Error)
	_, err = service.SetPolicy(retention.SetPolicyCommand{CustomerID: customer.ID, ExpectedVersion: policy.RowVersion, RetentionDays: 30, LegalHold: true, Actor: "legal"})
	require.NoError(t, err)
	blocked := &retentionClient{receipt: retention.DeliveryReceipt{ReceiptID: "must-not-run", Status: "completed"}}
	processed, err = retention.NewCoordinator(db, blocked).ProcessOne(context.Background())
	assert.True(t, processed)
	assert.ErrorContains(t, err, "legal hold")
	assert.Empty(t, blocked.calls)
	require.NoError(t, db.Model(&model.CustomerMember{}).Where("customer_id = ?", customer.ID).Count(&memberCount).Error)
	assert.Equal(t, int64(1), memberCount)
}

func TestRetentionFailsClosedForActiveCustomerAndLegalHold(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	customer := model.Customer{CustomerCode: "active-retention", DisplayName: "Active", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	service := retention.New(db)
	policy, err := service.SetPolicy(retention.SetPolicyCommand{
		CustomerID: customer.ID, RetentionDays: 30, LegalHold: true, Actor: "admin", RequestID: "policy",
	})
	require.NoError(t, err)
	_, err = service.DryRun(customer.ID, now, "admin", "dry-run")
	assert.ErrorContains(t, err, "archived customers")
	archivedAt := now.AddDate(0, 0, -60)
	require.NoError(t, db.Model(&customer).Updates(map[string]any{"status": model.CustomerStatusArchived, "archived_at": archivedAt}).Error)
	_, err = service.DryRun(customer.ID, now, "admin", "dry-run")
	assert.ErrorContains(t, err, "legal hold")
	assert.True(t, policy.LegalHold)
}
