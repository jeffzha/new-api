package agencyhub

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/require"
)

func TestProvisioningOnlyWorkerCompletesOPSBindingWithoutWalletChanges(t *testing.T) {
	app := newAgencyTestApp(t)
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "provision-worker-local")
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}, &model.OPSWalletAdmission{}, &model.OPSWalletHold{}))
	agency, _, err := app.CreateAgency(1, "Binding target", "binding-worker", agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000})
	require.NoError(t, err)
	user := model.User{Username: "binding-worker-user", BillingMode: "legacy", AuthVersion: 1, Quota: 42}
	require.NoError(t, app.db.Create(&user).Error)
	task := model.Task{TaskID: "binding-worker-drain", UserId: user.Id, Status: model.TaskStatusSubmitted}
	require.NoError(t, app.db.Create(&task).Error)
	job, _, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "User requested binding")
	require.NoError(t, err)
	require.NoError(t, app.RunProvisioningWorker(context.Background(), true))
	require.NoError(t, app.db.First(&job, job.ID).Error)
	require.Equal(t, provisioningBlocked, job.Status)
	require.NoError(t, app.db.Model(&task).Update("status", model.TaskStatusSuccess).Error)
	require.NoError(t, app.RunProvisioningWorker(context.Background(), true))
	require.NoError(t, app.db.First(&job, job.ID).Error)
	require.Equal(t, provisioningCompleted, job.Status)
	require.NoError(t, app.RunProvisioningWorker(context.Background(), true))
	var latest model.User
	require.NoError(t, app.db.First(&latest, user.Id).Error)
	require.Equal(t, user.Quota, latest.Quota)
	require.Equal(t, user.AuthVersion, latest.AuthVersion)
	require.Equal(t, user.BillingMode, latest.BillingMode)
	var bindings, funding int64
	require.NoError(t, app.db.Model(&model.AgencyActiveUserBinding{}).Where("user_id = ?", user.Id).Count(&bindings).Error)
	require.EqualValues(t, 1, bindings)
	require.NoError(t, app.db.Model(&model.AgencyFundingLedger{}).Where("user_id = ?", user.Id).Count(&funding).Error)
	require.Zero(t, funding)
}

func TestProvisioningOnlyWorkerRespectsCancellationAndOnboardingGate(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.Create(&model.AgencyProvisioningJob{UserID: 22, Status: provisioningQueued}).Error)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, app.RunProvisioningWorker(ctx, false))
	t.Setenv("AGENCY_ONBOARDING_ENABLED", "false")
	require.NoError(t, app.RunProvisioningWorker(context.Background(), true))
	var job model.AgencyProvisioningJob
	require.NoError(t, app.db.First(&job).Error)
	require.Equal(t, provisioningQueued, job.Status)
}

func TestProvisioningOnlyWorkerReportsDatabaseFailure(t *testing.T) {
	require.Error(t, (&App{}).RunProvisioningWorker(context.Background(), true))
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.Migrator().DropTable(&model.AgencyProvisioningJob{}))
	require.Error(t, app.RunProvisioningWorker(context.Background(), true))
}
