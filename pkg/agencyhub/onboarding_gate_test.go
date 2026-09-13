package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOnboardingPauseBlocksAdmissionsButAllowsCancellation(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	agency := model.Agency{Code: "paused-onboarding", InviteCode: "PAUSEDINVITE", DisplayName: "Paused agency", Status: AgencyStatusActive}
	require.NoError(t, app.db.Create(&agency).Error)
	user := model.User{Username: "paused-customer", Quota: 42, BillingMode: "legacy"}
	require.NoError(t, app.db.Create(&user).Error)
	job, _, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "Authorized opening")
	require.NoError(t, err)
	t.Setenv("AGENCY_ONBOARDING_ENABLED", "false")

	_, _, err = app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "Another opening")
	assert.ErrorIs(t, err, errAgencyOnboardingDisabled)
	processed, err := app.ProcessProvisioningJobs(10)
	require.NoError(t, err)
	assert.Zero(t, processed)
	require.NoError(t, app.db.First(&job, job.ID).Error)
	assert.Equal(t, provisioningQueued, job.Status)
	require.NoError(t, app.db.First(&user, user.Id).Error)
	assert.Equal(t, model.AgencyProvisioningBillingMode, user.BillingMode)
	assert.Equal(t, 42, user.Quota)

	err = app.db.Transaction(func(tx *gorm.DB) error {
		_, err := BindUserByInvite(tx, int64(user.Id), agency.InviteCode, "root_bind", 1)
		return err
	})
	assert.ErrorIs(t, err, errAgencyOnboardingDisabled, "a previously claimed worker must recheck admission at commit")
	var bindings int64
	require.NoError(t, app.db.Model(&model.AgencyActiveUserBinding{}).Count(&bindings).Error)
	assert.Zero(t, bindings)

	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/agency/api/v1/public/invitations/"+agency.InviteCode, nil))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"can_register":false`)
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

	require.NoError(t, app.cancelProvisioningJob(job.ID, "Cancel during maintenance", 2))
	require.NoError(t, app.db.First(&user, user.Id).Error)
	assert.Equal(t, "legacy", user.BillingMode)
	assert.Equal(t, 42, user.Quota)
}
