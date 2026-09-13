package agencyhub

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProvisioningOpeningBalancesRemainSpendableAndDebtRepayable(t *testing.T) {
	for _, quota := range []int{42, -25} {
		t.Run(map[int]string{42: "nonpaid", -25: "debt"}[quota], func(t *testing.T) {
			app := newAgencyTestApp(t)
			require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
			agency, _, err := app.CreateAgency(1, "Opening", "opening_operator", agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000})
			require.NoError(t, err)
			user := model.User{Username: "opening-user", Quota: quota, BillingMode: "legacy", AuthVersion: 1}
			require.NoError(t, app.db.Create(&user).Error)
			_, _, err = app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "Verified migration")
			require.NoError(t, err)
			_, err = app.ProcessProvisioningJobs(1)
			require.NoError(t, err)
			var account model.AgencyFundingAccount
			require.NoError(t, app.db.First(&account, user.Id).Error)
			var ledger model.AgencyFundingLedger
			require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&ledger).Error)
			assert.Equal(t, int64(1), account.MoneySeq)
			assert.Equal(t, account.MoneySeq, ledger.MoneySeq)
			if quota > 0 {
				var lot model.AgencyFundingLot
				require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&lot).Error)
				assert.Equal(t, int64(42), lot.BonusAvailable, "the migrated balance must exist on a usable lot")
				assert.Equal(t, int64(42), ledger.NonpaidDelta)
			} else {
				assert.Equal(t, int64(25), ledger.DebtDelta)
				// A real paid top-up must be able to repay the opening debt atomically.
				require.NoError(t, app.db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&model.User{}).Where("id = ?", user.Id).Update("quota", 5).Error; err != nil {
						return err
					}
					return model.RecordAgencyTopup(tx, int64(user.Id), "epay", "opening-repayment", "payment_callback", 30, 0)
				}))
				require.NoError(t, app.db.First(&account, user.Id).Error)
				assert.Equal(t, int64(0), account.DebtQuota)
				assert.Equal(t, int64(5), account.PaidAvailable)
				var repayment model.AgencyDebtRepayment
				require.NoError(t, app.db.First(&repayment).Error)
				assert.Equal(t, int64(25), repayment.Quota)
			}
			summary, err := app.Reconcile(context.Background())
			require.NoError(t, err)
			assert.Zero(t, summary.IssuesCreated, "opening balances and debt repayment must reconcile")
		})
	}
}

func TestProvisioningRejectsUnknownInvitationBeforeBlockingUser(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	user := model.User{Username: "invalid-invite-user", BillingMode: "legacy", Quota: 42}
	require.NoError(t, app.db.Create(&user).Error)
	_, _, err := app.enqueueProvisioningJob(int64(user.Id), "NONEXISTENT", 1, "Requested binding")
	require.Error(t, err)
	require.NoError(t, app.db.First(&user, user.Id).Error)
	assert.Equal(t, "legacy", user.BillingMode)
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyProvisioningJob{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestProvisioningStaleFailureCannotReleaseNewOwnersBarrier(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	user := model.User{Username: "stale-failure-user", BillingMode: model.AgencyProvisioningBillingMode}
	require.NoError(t, app.db.Create(&user).Error)
	job := model.AgencyProvisioningJob{UserID: int64(user.Id), Status: provisioningProcessing, FencingToken: 22}
	require.NoError(t, app.db.Create(&job).Error)
	err := app.markProvisioningFailed(job.ID, 21, errors.New("old worker failed"))
	require.Error(t, err)
	require.NoError(t, app.db.First(&job, job.ID).Error)
	assert.Equal(t, provisioningProcessing, job.Status)
	require.NoError(t, app.db.First(&user, user.Id).Error)
	assert.Equal(t, model.AgencyProvisioningBillingMode, user.BillingMode)
}

func TestProvisioningCanBeCancelledAgainAfterRetryWithoutLosingHistory(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	agency := model.Agency{Code: "retry-agency", InviteCode: "RETRYINVITE", Status: AgencyStatusActive}
	require.NoError(t, app.db.Create(&agency).Error)
	user := model.User{Username: "retry-user", BillingMode: "legacy"}
	require.NoError(t, app.db.Create(&user).Error)
	first, _, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "first migration attempt")
	require.NoError(t, err)
	require.NoError(t, app.cancelProvisioningJob(first.ID, "cancel first attempt", 1))
	retry, created, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "second migration attempt")
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, first.ID, retry.ID)
	require.NoError(t, app.cancelProvisioningJob(retry.ID, "cancel second attempt", 2))
	var jobs []model.AgencyProvisioningJob
	require.NoError(t, app.db.Where("user_id = ?", user.Id).Order("id ASC").Find(&jobs).Error)
	require.Len(t, jobs, 2)
	assert.Equal(t, provisioningCancelled, jobs[0].Status)
	assert.Equal(t, provisioningCancelled, jobs[1].Status)
	require.NoError(t, app.db.First(&user, user.Id).Error)
	assert.Equal(t, "legacy", user.BillingMode)
	var audit model.AgencyAuditLog
	require.NoError(t, app.db.Where("action = ? AND object_id = ?", "provisioning.cancel", fmt.Sprint(retry.ID)).First(&audit).Error)
	assert.Equal(t, int64(2), audit.ActorID, "record the cancelling administrator, not the original creator")
}
