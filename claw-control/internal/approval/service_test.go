package approval_test

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	"github.com/QuantumNous/new-api/claw-control/internal/approval"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppDisableRequiresDifferentApproverAndExecutesIdempotently(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	customer := model.Customer{CustomerCode: "approval-customer", DisplayName: "Approval", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	app := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "approval-app", DisplayName: "Approval App", Status: model.AppStatusActive,
		AuthEpoch: 3, RowVersion: 4,
	}
	require.NoError(t, db.Create(&app).Error)
	service := approval.New(db)

	request, err := service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionAppDisable, CustomerID: &customer.ID,
		RequestKey: "disable-request-1", Reason: "security response",
		Actor: "admin-requester", RequestID: "request-1", TTL: time.Hour,
	}, now)
	require.NoError(t, err)
	replayed, err := service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionAppDisable, CustomerID: &customer.ID,
		RequestKey: "disable-request-1", Reason: "security response",
		Actor: "admin-requester", RequestID: "request-retry", TTL: time.Hour,
	}, now)
	require.NoError(t, err)
	assert.Equal(t, request.PublicID, replayed.PublicID)

	_, err = service.Approve(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: request.RowVersion, Actor: "admin-requester",
	}, now.Add(time.Minute))
	assert.ErrorContains(t, err, "must be different")
	approved, err := service.Approve(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: request.RowVersion,
		Actor: "admin-approver", Reason: "verified incident", RequestID: "approve-1",
	}, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, model.ApprovalStatusApproved, approved.Status)
	executed, err := service.Execute(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: approved.RowVersion,
		Actor: "admin-operator", RequestID: "execute-1",
	}, now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, model.ApprovalStatusExecuted, executed.Status)
	retried, err := service.Execute(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: approved.RowVersion,
		Actor: "admin-operator", RequestID: "execute-retry",
	}, now.Add(3*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, executed.RowVersion, retried.RowVersion)
	require.NoError(t, db.First(&app, app.ID).Error)
	assert.Equal(t, model.AppStatusDisabled, app.Status)
	assert.Equal(t, int64(4), app.AuthEpoch)

	var actionAudits int64
	require.NoError(t, db.Model(&model.AdminAudit{}).Where("action IN ?", []string{
		"approval.request", "approval.approved", "app.disable.approved", "approval.execute",
	}).Count(&actionAudits).Error)
	assert.Equal(t, int64(4), actionAudits)
}

func TestApprovalExpiryAndRejectionAreTerminal(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	customer := model.Customer{CustomerCode: "approval-terminal", DisplayName: "Approval", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	apps := []model.CustomerApp{
		{CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "terminal-app", DisplayName: "App", Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1},
	}
	require.NoError(t, db.Create(&apps).Error)
	service := approval.New(db)
	expiring, err := service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionAppDisable, CustomerID: &customer.ID,
		RequestKey: "expiring-request", Reason: "test expiry", Actor: "admin-a", TTL: approval.MinimumTTL,
	}, now)
	require.NoError(t, err)
	_, err = service.Approve(approval.DecisionCommand{
		ApprovalID: expiring.PublicID, ExpectedVersion: expiring.RowVersion, Actor: "admin-b",
	}, now.Add(approval.MinimumTTL))
	assert.ErrorContains(t, err, "expired")
	require.NoError(t, db.Where("public_id = ?", expiring.PublicID).First(&expiring).Error)
	assert.Equal(t, model.ApprovalStatusExpired, expiring.Status)

	apps[0].AppID = "rejection-app"
	apps[0].ID = 0
	apps[0].Slot = "migration:rejection"
	apps[0].Status = model.AppStatusVerified
	require.NoError(t, db.Create(&apps[0]).Error)
	// A second customer provides an independent primary target for rejection.
	secondCustomer := model.Customer{CustomerCode: "approval-reject", DisplayName: "Reject", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&secondCustomer).Error)
	secondApp := model.CustomerApp{CustomerID: secondCustomer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP, AppID: "reject-primary", DisplayName: "App", Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1}
	require.NoError(t, db.Create(&secondApp).Error)
	rejected, err := service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionAppDisable, CustomerID: &secondCustomer.ID,
		RequestKey: "rejected-request", Reason: "test rejection", Actor: "admin-a", TTL: time.Hour,
	}, now)
	require.NoError(t, err)
	rejected, err = service.Reject(approval.DecisionCommand{
		ApprovalID: rejected.PublicID, ExpectedVersion: rejected.RowVersion,
		Actor: "admin-b", Reason: "not justified", RequestID: "reject",
	}, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, model.ApprovalStatusRejected, rejected.Status)
	_, err = service.Execute(approval.DecisionCommand{
		ApprovalID: rejected.PublicID, ExpectedVersion: rejected.RowVersion, Actor: "admin-c",
	}, now.Add(2*time.Minute))
	assert.Error(t, err)
}

func TestCredentialRotationRollbackAndRetirementStateMachine(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	owner := model.Customer{CustomerCode: "rotation-owner", DisplayName: "Rotation Owner", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&owner).Error)
	ownerID := owner.ID
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_ROTATION_V1_ID": "v1-id", "env://WORKBENCH_PROVIDER_ROTATION_V1_KEY": "v1-key",
		"env://WORKBENCH_PROVIDER_ROTATION_V2_ID": "v2-id", "env://WORKBENCH_PROVIDER_ROTATION_V2_KEY": "v2-key",
	}
	credentials := credential.New(db, resolver)
	current, err := credentials.Create(credential.CreateCommand{
		OwnerScope: credential.CustomerOwnerScope(ownerID), CustomerID: &ownerID,
		ProviderEnvironment: model.ProviderChinaTencentADP, Name: "rotation-test",
		SecretIDRef:  "env://WORKBENCH_PROVIDER_ROTATION_V1_ID",
		SecretKeyRef: "env://WORKBENCH_PROVIDER_ROTATION_V1_KEY",
		Fingerprint:  secrets.CredentialPairFingerprint("v1-id", "v1-key"), Actor: "admin-a",
	})
	require.NoError(t, err)
	candidate, err := credentials.StageRotation(credential.StageRotationCommand{
		CurrentProfileID: current.ID, ExpectedCurrentVersion: current.RowVersion,
		SecretIDRef:  "env://WORKBENCH_PROVIDER_ROTATION_V2_ID",
		SecretKeyRef: "env://WORKBENCH_PROVIDER_ROTATION_V2_KEY",
		Fingerprint:  secrets.CredentialPairFingerprint("v2-id", "v2-key"), Actor: "admin-a",
	})
	require.NoError(t, err)
	assert.Equal(t, model.CredentialStatusStaged, candidate.Status)
	service := approval.New(db, resolver)
	now := time.Now().UTC()
	rotation, err := service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionCredentialRotate, CustomerID: &ownerID, ResourceID: candidate.ID,
		RequestKey: "credential-rotation", Reason: "scheduled rotation",
		Actor: "admin-a", TTL: time.Hour,
	}, now)
	require.NoError(t, err)
	require.NotNil(t, rotation.CustomerID)
	assert.Equal(t, ownerID, *rotation.CustomerID)
	rotation, err = service.Approve(approval.DecisionCommand{
		ApprovalID: rotation.PublicID, ExpectedVersion: rotation.RowVersion, Actor: "admin-b",
	}, now.Add(time.Minute))
	require.NoError(t, err)
	_, err = service.Execute(approval.DecisionCommand{
		ApprovalID: rotation.PublicID, ExpectedVersion: rotation.RowVersion, Actor: "admin-c",
	}, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.NoError(t, db.First(&current, current.ID).Error)
	require.NoError(t, db.First(&candidate, candidate.ID).Error)
	assert.Equal(t, model.CredentialStatusRetiring, current.Status)
	assert.Equal(t, model.CredentialStatusActive, candidate.Status)

	rollback, err := service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionCredentialRollback, CustomerID: &ownerID, ResourceID: candidate.ID,
		RequestKey: "credential-rollback", Reason: "provider regression",
		Actor: "admin-c", TTL: time.Hour,
	}, now.Add(3*time.Minute))
	require.NoError(t, err)
	rollback, err = service.Approve(approval.DecisionCommand{
		ApprovalID: rollback.PublicID, ExpectedVersion: rollback.RowVersion, Actor: "admin-b",
	}, now.Add(4*time.Minute))
	require.NoError(t, err)
	_, err = service.Execute(approval.DecisionCommand{
		ApprovalID: rollback.PublicID, ExpectedVersion: rollback.RowVersion, Actor: "admin-a",
	}, now.Add(5*time.Minute))
	require.NoError(t, err)
	require.NoError(t, db.First(&current, current.ID).Error)
	require.NoError(t, db.First(&candidate, candidate.ID).Error)
	assert.Equal(t, model.CredentialStatusActive, current.Status)
	assert.Equal(t, model.CredentialStatusRetiring, candidate.Status)
	retired, err := credentials.Retire(credential.RetireCommand{
		ProfileID: candidate.ID, ExpectedVersion: candidate.RowVersion,
		Actor: "admin-a", Reason: "rollback complete",
	})
	require.NoError(t, err)
	assert.Equal(t, model.CredentialStatusRetired, retired.Status)
	var scopedAudits int64
	require.NoError(t, db.Model(&model.AdminAudit{}).Where("customer_id = ? AND action LIKE ?", ownerID, "credential.%").Count(&scopedAudits).Error)
	assert.GreaterOrEqual(t, scopedAudits, int64(5))
}

func TestVerifiedAppMigrationRequiresEvidenceAndTwoPersonCutover(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	customer := model.Customer{CustomerCode: "migration-approval", DisplayName: "Migration", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	source := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "source-app", DisplayName: "Source", Status: model.AppStatusActive, AuthEpoch: 2, RowVersion: 3,
	}
	require.NoError(t, db.Create(&source).Error)
	sourceConfig := model.AppConfigVersion{
		CustomerAppID: source.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "source-space", TemplateAgentID: "source-template",
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_SOURCE_APP_KEY", AppKeyFingerprint: "sha256:source",
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&sourceConfig).Error)
	source.CurrentConfigVersionID = &sourceConfig.ID
	require.NoError(t, db.Save(&source).Error)
	require.NoError(t, db.Create(&model.AppVerification{
		PublicID: "verify-approval-source", CustomerAppID: source.ID, AppConfigVersionID: sourceConfig.ID,
		Result: "verified", AppMode: 1, ReleaseStatus: "published", TemplateAgentStatus: "",
		ProviderRequestIDsJSON: `["request-source"]`, SanitizedResponseHash: "sha256:" + strings.Repeat("c", 64),
		VerifiedBy: "test", VerifiedAt: now,
	}).Error)
	target := model.CustomerApp{
		CustomerID: customer.ID, Slot: "migration:target", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "target-app", DisplayName: "Target", Status: model.AppStatusVerified, AuthEpoch: 2, RowVersion: 2,
	}
	require.NoError(t, db.Create(&target).Error)
	credential := model.CredentialProfile{
		OwnerScope: "platform", ProviderEnvironment: model.ProviderChinaTencentADP, Name: "migration-test",
		SecretIDRef: "env://WORKBENCH_PROVIDER_MIGRATION_SECRET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_MIGRATION_SECRET_KEY",
		Fingerprint: "sha256:credential", FingerprintVersion: 1, Status: model.CredentialStatusActive, Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&credential).Error)
	config := model.AppConfigVersion{
		CustomerAppID: target.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space", TemplateAgentID: "template",
		CredentialProfileID: &credential.ID,
		AppKeySecretRef:     "env://WORKBENCH_PROVIDER_MIGRATION_APP_KEY", AppKeyFingerprint: "sha256:test",
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&config).Error)
	target.CurrentConfigVersionID = &config.ID
	require.NoError(t, db.Save(&target).Error)
	require.NoError(t, db.Create(&model.AppVerification{
		PublicID: "verify-approval-target", CustomerAppID: target.ID, AppConfigVersionID: config.ID,
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		DynamicAgentConfig: true, ProviderRequestIDsJSON: `["request-approval"]`,
		SanitizedResponseHash: "sha256:" + strings.Repeat("d", 64), VerifiedBy: "test", VerifiedAt: now,
	}).Error)
	identity := model.IdentityBinding{
		PublicID: "migration-binding", CustomerID: customer.ID, NewAPIUserID: 77,
		CanonicalSubject: "new-api:77", ADPAccountID: "migration-account", ADPAccountVersion: 1,
		IdentityVersion: "v1", Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&identity).Error)
	job, err := appmigration.EnsureJob(db, target, config)
	require.NoError(t, err)
	evidence := model.EvidenceObject{
		PublicID: "evidence_migration", CustomerID: &customer.ID, OriginalFilename: "migration.json",
		MIMEType: "application/json", SizeBytes: 2, ContentSHA256: "sha256:" + strings.Repeat("a", 64),
		StorageKey: strings.Repeat("b", 64), FormatVersion: 1, Status: model.EvidenceStatusActive, CreatedBy: "admin-a",
	}
	require.NoError(t, db.Create(&evidence).Error)
	service := approval.New(db)
	_, err = service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionAppIDMigration, CustomerID: &customer.ID,
		ResourceID: target.ID, EvidenceRef: evidence.PublicID,
		RequestKey: "app-migration-too-early", Reason: "must not cut over early",
		Actor: "admin-a", TTL: time.Hour,
	}, now)
	require.ErrorContains(t, err, "all active bindings")
	var lineageCount int64
	require.NoError(t, db.Model(&model.AppMigrationLineage{}).Count(&lineageCount).Error)
	assert.Zero(t, lineageCount, "prepare/verification must not activate historical lineage")
	var readiness model.AppMigrationMember
	require.NoError(t, db.Where("job_id = ?", job.ID).First(&readiness).Error)
	readiness.Status = model.AppMigrationMemberStatusSucceeded
	readiness.TargetAgentID = "target-agent"
	readiness.TargetReadbackHash = "sha256:" + strings.Repeat("c", 64)
	readiness.CompletedAt = &now
	require.NoError(t, db.Save(&readiness).Error)
	job.Status = model.AppMigrationJobStatusReady
	job.SucceededMembers = 1
	job.ReadyAt = &now
	job.RowVersion++
	require.NoError(t, db.Save(job).Error)
	request, err := service.Request(approval.RequestCommand{
		ActionType: model.ApprovalActionAppIDMigration, CustomerID: &customer.ID,
		ResourceID: target.ID, EvidenceRef: evidence.PublicID,
		RequestKey: "app-migration-cutover", Reason: "approved provider migration",
		Actor: "admin-a", TTL: time.Hour,
	}, now)
	require.NoError(t, err)
	request, err = service.Approve(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: request.RowVersion, Actor: "admin-b",
	}, now.Add(time.Minute))
	require.NoError(t, err)
	_, err = service.Execute(approval.DecisionCommand{
		ApprovalID: request.PublicID, ExpectedVersion: request.RowVersion, Actor: "admin-c",
	}, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.NoError(t, db.First(&source, source.ID).Error)
	require.NoError(t, db.First(&target, target.ID).Error)
	assert.Equal(t, model.AppStatusArchived, source.Status)
	assert.Equal(t, "primary", target.Slot)
	assert.Equal(t, model.AppStatusActive, target.Status)
	var lineage model.AppMigrationLineage
	require.NoError(t, db.Where("migration_job_id = ?", job.ID).First(&lineage).Error)
	assert.Equal(t, source.ID, lineage.SourceCustomerAppID)
	assert.Equal(t, sourceConfig.ID, lineage.SourceAppConfigVersionID)
	assert.Equal(t, target.ID, lineage.TargetCustomerAppID)
	assert.Equal(t, config.ID, lineage.TargetAppConfigVersionID)
	assert.Equal(t, "source-app", lineage.SourceApplicationID)
	assert.Equal(t, "target-app", lineage.TargetApplicationID)
	assert.Equal(t, 1, lineage.SourceProviderAppMode)
	assert.Equal(t, "standard_v2", lineage.SourceRuntimeProfile)
	assert.False(t, lineage.SourceExecutionEnabled)
	var cutoverEvent model.ControlOutbox
	require.NoError(t, db.Where("event_key = ?", lineage.EventKey).First(&cutoverEvent).Error)
	assert.Equal(t, "APP_MIGRATION_CUTOVER", cutoverEvent.EventType)
	assert.Contains(t, cutoverEvent.PayloadJSON, `"lineage_id":"`+lineage.PublicID+`"`)
}
