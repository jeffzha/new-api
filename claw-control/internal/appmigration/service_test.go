package appmigration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type migrationFixture struct {
	db       *gorm.DB
	resolver testutil.SecretResolver
	source   model.CustomerApp
	target   model.CustomerApp
	config   model.AppConfigVersion
	job      *model.AppMigrationJob
}

func TestMigrationReadinessRequiresEverySnapshottedActiveBinding(t *testing.T) {
	fixture := newMigrationFixture(t, 2)
	service := appmigration.New(fixture.db, fixture.resolver)
	now := time.Now().UTC()

	first, err := service.Claim(context.Background(), "worker-blue", 30*time.Second, now)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, fixture.target.ID, first.TargetAppProfileID)
	assert.Equal(t, fixture.target.AppID, first.Provider.AppID)
	assert.Equal(t, "target-app-key", first.Provider.AppKey)
	assert.Equal(t, "target-secret-id", first.Provider.SecretID)
	assert.Equal(t, "target-secret-key", first.Provider.SecretKey)

	var persisted model.AppMigrationMember
	require.NoError(t, fixture.db.Where("public_id = ?", first.MigrationMemberID).First(&persisted).Error)
	assert.NotEqual(t, first.LeaseToken, persisted.LeaseTokenHash)
	assert.Len(t, persisted.LeaseTokenHash, 64)

	firstReport := successfulReport(first, "agent-target-1")
	result, err := service.Report(context.Background(), firstReport, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, model.AppMigrationJobStatusRunning, result.JobStatus)
	assert.Equal(t, 1, result.SucceededMembers)

	// An exact terminal replay is idempotent.
	replayed, err := service.Report(context.Background(), firstReport, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, result, replayed)

	second, err := service.Claim(context.Background(), "worker-green", 30*time.Second, now.Add(3*time.Second))
	require.NoError(t, err)
	require.NotNil(t, second)
	_, err = service.Report(context.Background(), successfulReport(second, "agent-target-2"), now.Add(4*time.Second))
	require.NoError(t, err)

	var job model.AppMigrationJob
	require.NoError(t, fixture.db.First(&job, fixture.job.ID).Error)
	assert.Equal(t, model.AppMigrationJobStatusReady, job.Status)
	assert.Equal(t, 2, job.SucceededMembers)
	_, err = appmigration.AssertReadyForCutover(fixture.db, fixture.source, fixture.target)
	require.NoError(t, err)

	// A newly active binding after the snapshot invalidates readiness.
	late := model.IdentityBinding{
		PublicID: "binding-late", CustomerID: fixture.target.CustomerID, NewAPIUserID: 999,
		CanonicalSubject: "new-api:999", ADPAccountID: "account-late", ADPAccountVersion: 1,
		IdentityVersion: "v1", Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, fixture.db.Create(&late).Error)
	_, err = appmigration.AssertReadyForCutover(fixture.db, fixture.source, fixture.target)
	require.ErrorContains(t, err, "active binding")
}

func TestMigrationReportRejectsWrongTargetAndLeaseExpiryFailsClosed(t *testing.T) {
	fixture := newMigrationFixture(t, 1)
	service := appmigration.New(fixture.db, fixture.resolver)
	now := time.Now().UTC()

	claimed, err := service.Claim(context.Background(), "worker-blue", 10*time.Second, now)
	require.NoError(t, err)
	wrong := successfulReport(claimed, "agent-wrong")
	wrong.TargetConfigVersion++
	_, err = service.Report(context.Background(), wrong, now.Add(time.Second))
	require.ErrorContains(t, err, "target tuple")

	// Losing a response after CopyAgent may have created a provider Agent. The
	// expired lease is terminal provider_unknown and is never auto-replayed.
	next, err := service.Claim(context.Background(), "worker-green", 10*time.Second, now.Add(11*time.Second))
	require.NoError(t, err)
	require.Nil(t, next)
	var member model.AppMigrationMember
	require.NoError(t, fixture.db.Where("public_id = ?", claimed.MigrationMemberID).First(&member).Error)
	assert.Equal(t, model.AppMigrationMemberStatusFailed, member.Status)
	assert.Equal(t, "worker_lease_expired_provider_unknown", member.ErrorCode)
	var job model.AppMigrationJob
	require.NoError(t, fixture.db.First(&job, fixture.job.ID).Error)
	assert.Equal(t, model.AppMigrationJobStatusFailed, job.Status)
}

func TestProviderUnknownRetryIsReadbackOnlyAndNeverCopiesAgain(t *testing.T) {
	fixture := newMigrationFixture(t, 1)
	service := appmigration.New(fixture.db, fixture.resolver)
	now := time.Now().UTC()
	claimed, err := service.Claim(context.Background(), "worker-blue", 30*time.Second, now)
	require.NoError(t, err)
	_, err = service.Report(context.Background(), appmigration.ReportCommand{
		MigrationMemberID: claimed.MigrationMemberID, AttemptID: claimed.AttemptID, LeaseToken: claimed.LeaseToken,
		Status: model.AppMigrationMemberStatusFailed, TargetAgentID: "known-target-agent", ErrorCode: "provider_outcome_unknown",
	}, now.Add(time.Second))
	require.NoError(t, err)
	var job model.AppMigrationJob
	var member model.AppMigrationMember
	require.NoError(t, fixture.db.First(&job, fixture.job.ID).Error)
	require.NoError(t, fixture.db.Where("public_id = ?", claimed.MigrationMemberID).First(&member).Error)
	retried, err := service.RetryMember(context.Background(), appmigration.RetryMemberCommand{
		CustomerID: fixture.target.CustomerID, TargetCustomerAppID: fixture.target.ID,
		MigrationMemberID: member.PublicID, ExpectedJobRowVersion: job.RowVersion,
		ExpectedMemberVersion: member.RowVersion, Actor: "admin-a", Reason: "read back known Agent",
	})
	require.NoError(t, err)
	assert.Equal(t, "readback", retried.RecoveryMode)
	assert.Equal(t, "known-target-agent", retried.TargetAgentID)

	recovery, err := service.Claim(context.Background(), "worker-green", 30*time.Second, now.Add(2*time.Second))
	require.NoError(t, err)
	require.NotNil(t, recovery)
	assert.Equal(t, "readback", recovery.Mode)
	assert.Equal(t, "known-target-agent", recovery.KnownTargetAgentID)
}

func TestNonDynamicRuntimeClaimAndReadinessNeverRequirePerUserAgent(t *testing.T) {
	fixture := newMigrationFixture(t, 1)
	require.NoError(t, fixture.db.Where("job_id = ?", fixture.job.ID).Delete(&model.AppMigrationMember{}).Error)
	require.NoError(t, fixture.db.Delete(&model.AppMigrationJob{}, fixture.job.ID).Error)
	require.NoError(t, fixture.db.Model(&model.AppConfigVersion{}).Where("id = ?", fixture.config.ID).Update("template_agent_id", "").Error)
	require.NoError(t, fixture.db.Model(&model.AppVerification{}).Where("customer_app_id = ? AND app_config_version_id = ?", fixture.target.ID, fixture.config.ID).
		Updates(map[string]any{"app_mode": 1, "dynamic_agent_config": false, "template_agent_status": "not_required"}).Error)
	require.NoError(t, fixture.db.First(&fixture.config, fixture.config.ID).Error)
	job, err := appmigration.EnsureJob(fixture.db, fixture.target, fixture.config)
	require.NoError(t, err)
	service := appmigration.New(fixture.db, fixture.resolver)
	task, err := service.Claim(context.Background(), "worker-standard", 30*time.Second, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, 1, task.ProviderAppMode)
	assert.Equal(t, "standard_v2", task.RuntimeProfile)
	assert.True(t, task.ExecutionEnabled)
	assert.Equal(t, task.ProviderAppMode, task.Provider.ProviderAppMode)
	assert.Equal(t, task.RuntimeProfile, task.Provider.RuntimeProfile)
	assert.Equal(t, task.ExecutionEnabled, task.Provider.ExecutionEnabled)
	assert.Empty(t, task.Provider.TemplateAgentID)

	report := successfulReport(task, "")
	_, err = service.Report(context.Background(), report, time.Now().UTC().Add(time.Second))
	require.NoError(t, err)
	ready, err := appmigration.AssertReadyForCutover(fixture.db, fixture.source, fixture.target)
	require.NoError(t, err)
	assert.Equal(t, job.PublicID, ready.PublicID)
}

func TestNonDynamicRuntimeReportsRequireEmptyAgentAndReadbackEvidence(t *testing.T) {
	for _, testCase := range []struct {
		mode    int
		dynamic bool
		profile string
	}{
		{1, false, "standard_v2"},
		{2, false, "multi_agent_v2"},
		{3, false, "workflow_v2"},
		{4, false, "claw_static_v2"},
	} {
		t.Run(testCase.profile, func(t *testing.T) {
			fixture := newMigrationFixture(t, 1)
			require.NoError(t, fixture.db.Where("job_id = ?", fixture.job.ID).Delete(&model.AppMigrationMember{}).Error)
			require.NoError(t, fixture.db.Delete(&model.AppMigrationJob{}, fixture.job.ID).Error)
			require.NoError(t, fixture.db.Model(&model.AppVerification{}).
				Where("customer_app_id = ? AND app_config_version_id = ?", fixture.target.ID, fixture.config.ID).
				Updates(map[string]any{"app_mode": testCase.mode, "dynamic_agent_config": testCase.dynamic, "template_agent_status": "not_required"}).Error)
			_, err := appmigration.EnsureJob(fixture.db, fixture.target, fixture.config)
			require.NoError(t, err)
			service := appmigration.New(fixture.db, fixture.resolver)
			task, err := service.Claim(context.Background(), "worker-"+testCase.profile, 30*time.Second, time.Now().UTC())
			require.NoError(t, err)
			require.NotNil(t, task)
			assert.Equal(t, testCase.profile, task.RuntimeProfile)

			invalid := successfulReport(task, "")
			invalid.TargetReadbackHash = "sha256:" + strings.Repeat("a", 64)
			_, err = service.Report(context.Background(), invalid, time.Now().UTC().Add(time.Second))
			assert.ErrorContains(t, err, "readiness evidence")

			valid := successfulReport(task, "")
			_, err = service.Report(context.Background(), valid, time.Now().UTC().Add(2*time.Second))
			require.NoError(t, err)
		})
	}
}

func TestReplanSupersedesOldMemberSetAndOnlyNewGenerationIsClaimable(t *testing.T) {
	fixture := newMigrationFixture(t, 1)
	service := appmigration.New(fixture.db, fixture.resolver)
	late := model.IdentityBinding{
		PublicID: "binding-replan", CustomerID: fixture.target.CustomerID, NewAPIUserID: 88,
		CanonicalSubject: "new-api:88", ADPAccountID: "account-replan", ADPAccountVersion: 1,
		IdentityVersion: "v1", Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, fixture.db.Create(&late).Error)
	var current model.AppMigrationJob
	require.NoError(t, fixture.db.First(&current, fixture.job.ID).Error)
	replanned, err := service.Replan(context.Background(), appmigration.ReplanCommand{
		CustomerID: fixture.target.CustomerID, TargetCustomerAppID: fixture.target.ID,
		ExpectedTargetRowVersion: fixture.target.RowVersion, ExpectedJobRowVersion: current.RowVersion,
		Actor: "admin-a", Reason: "active membership changed",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), replanned.Generation)
	assert.Equal(t, 2, replanned.ExpectedMembers)
	require.NoError(t, fixture.db.First(&current, fixture.job.ID).Error)
	assert.Equal(t, model.AppMigrationJobStatusSuperseded, current.Status)

	claimed, err := service.Claim(context.Background(), "worker-blue", 30*time.Second, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, replanned.PublicID, claimed.MigrationJobID)
}

func successfulReport(task *appmigration.ClaimedTask, agentID string) appmigration.ReportCommand {
	report := appmigration.ReportCommand{
		MigrationMemberID: task.MigrationMemberID, AttemptID: task.AttemptID, LeaseToken: task.LeaseToken,
		Status: model.AppMigrationMemberStatusSucceeded, TargetAppProfileID: task.TargetAppProfileID,
		TargetConfigVersion: task.TargetConfigVersion, TargetConfigFingerprint: task.TargetConfigFingerprint,
		ProviderAppMode: task.ProviderAppMode, RuntimeProfile: task.RuntimeProfile, ExecutionEnabled: task.ExecutionEnabled,
		TargetAgentID: agentID,
	}
	if task.RuntimeProfile == "claw_dynamic_v2" {
		report.TargetReadbackHash = "sha256:" + strings.Repeat("a", 64)
	}
	return report
}

func newMigrationFixture(t *testing.T, activeBindings int) migrationFixture {
	t.Helper()
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_MIGRATION_APP_KEY":    "target-app-key",
		"env://WORKBENCH_PROVIDER_MIGRATION_SECRET_ID":  "target-secret-id",
		"env://WORKBENCH_PROVIDER_MIGRATION_SECRET_KEY": "target-secret-key",
	}
	credential := model.CredentialProfile{
		OwnerScope: "platform", ProviderEnvironment: model.ProviderChinaTencentADP, Name: "migration",
		SecretIDRef: "env://WORKBENCH_PROVIDER_MIGRATION_SECRET_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_MIGRATION_SECRET_KEY",
		Fingerprint:        secrets.CredentialPairFingerprint("target-secret-id", "target-secret-key"),
		FingerprintVersion: secrets.CanonicalFingerprintVersion, Status: model.CredentialStatusActive, Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&credential).Error)
	customer := model.Customer{CustomerCode: "migration-gate", DisplayName: "Migration", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	now := time.Now().UTC()
	source := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "source-app", DisplayName: "Source", Status: model.AppStatusActive, AuthEpoch: 2, RowVersion: 3,
	}
	require.NoError(t, db.Create(&source).Error)
	sourceConfig := model.AppConfigVersion{
		CustomerAppID: source.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "source-space", TemplateAgentID: "source-template",
		CredentialProfileID: &credential.ID, AppKeySecretRef: "env://WORKBENCH_PROVIDER_MIGRATION_APP_KEY",
		AppKeyFingerprint: secrets.AppKeyFingerprint("target-app-key"), AppKeyFingerprintVersion: secrets.CanonicalFingerprintVersion,
		RowVersion: 1, LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&sourceConfig).Error)
	source.CurrentConfigVersionID = &sourceConfig.ID
	require.NoError(t, db.Save(&source).Error)

	target := model.CustomerApp{
		CustomerID: customer.ID, Slot: "migration:target", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "target-app", DisplayName: "Target", Status: model.AppStatusVerified, AuthEpoch: 2, RowVersion: 2,
	}
	require.NoError(t, db.Create(&target).Error)
	config := model.AppConfigVersion{
		CustomerAppID: target.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "target-space", TemplateAgentID: "target-template",
		CredentialProfileID: &credential.ID, AppKeySecretRef: "env://WORKBENCH_PROVIDER_MIGRATION_APP_KEY",
		AppKeyFingerprint: secrets.AppKeyFingerprint("target-app-key"), AppKeyFingerprintVersion: secrets.CanonicalFingerprintVersion,
		RowVersion: 1, LimitsJSON: `{"customer_concurrency":2}`, CapabilitiesJSON: `["chat"]`, CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&config).Error)
	target.CurrentConfigVersionID = &config.ID
	require.NoError(t, db.Save(&target).Error)
	require.NoError(t, db.Create(&model.AppVerification{
		PublicID: "verify-migration-target", CustomerAppID: target.ID, AppConfigVersionID: config.ID,
		Result: "verified", AppMode: 4, ReleaseStatus: "published", TemplateAgentStatus: "available",
		DynamicAgentConfig: true, ProviderRequestIDsJSON: `["request-1"]`, SanitizedResponseHash: "sha256:" + strings.Repeat("b", 64),
		VerifiedBy: "test", VerifiedAt: now,
	}).Error)
	for index := 1; index <= activeBindings; index++ {
		identity := model.IdentityBinding{
			PublicID: "binding-" + string(rune('0'+index)), CustomerID: customer.ID, NewAPIUserID: int64(index),
			CanonicalSubject: "new-api:" + string(rune('0'+index)), ADPAccountID: "account-" + string(rune('0'+index)),
			ADPAccountVersion: 1, IdentityVersion: "v1", Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1,
		}
		require.NoError(t, db.Create(&identity).Error)
	}
	job, err := appmigration.EnsureJob(db, target, config)
	require.NoError(t, err)
	return migrationFixture{db: db, resolver: resolver, source: source, target: target, config: config, job: job}
}
