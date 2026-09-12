package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createProvisioningQuotaUser(t *testing.T, quota int) User {
	t.Helper()
	user := User{
		Username:    "agency-provisioning-" + common.GetRandomString(8),
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       quota,
		AffCode:     "agency-prov-aff-" + common.GetRandomString(8),
		BillingMode: AgencyProvisioningBillingMode,
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func TestCreditTopUpQuotaRejectsProvisioningUser(t *testing.T) {
	truncateTables(t)
	user := createProvisioningQuotaUser(t, 100)

	err := DB.Transaction(func(tx *gorm.DB) error {
		return creditTopUpQuotaWithFunding(tx, user.Id, 50, nil, PaymentProviderStripe, "stripe-prov-guard")
	})

	require.ErrorIs(t, err, ErrAgencyProvisioning)
	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 100, stored.Quota)
}

func TestPurchaseSubscriptionWithBalanceRejectsProvisioningUser(t *testing.T) {
	truncateTables(t)
	allowBalancePay := true
	requiredQuota, err := calcSubscriptionBalanceQuota(1)
	require.NoError(t, err)
	user := createProvisioningQuotaUser(t, requiredQuota+1)
	plan := SubscriptionPlan{
		Title:           "provisioning guard",
		PriceAmount:     1,
		Enabled:         true,
		AllowBalancePay: &allowBalancePay,
	}
	require.NoError(t, DB.Create(&plan).Error)

	err = PurchaseSubscriptionWithBalance(user.Id, plan.Id)

	require.ErrorIs(t, err, ErrAgencyProvisioning)
	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, requiredQuota+1, stored.Quota)
	var orderCount int64
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("user_id = ?", user.Id).Count(&orderCount).Error)
	assert.Zero(t, orderCount)
}

func TestTransferAffQuotaToQuotaUsesDurableAgencyFunding(t *testing.T) {
	truncateTables(t)
	require.NoError(t, MigrateAgency(DB))

	transfer := int(common.QuotaPerUnit)
	user := User{
		Username:       "agency-aff-transfer-" + common.GetRandomString(8),
		Password:       "unused-password-hash",
		Role:           common.RoleCommonUser,
		Status:         common.UserStatusEnabled,
		Quota:          0,
		AffQuota:       transfer * 2,
		AffCode:        "agency-aff-transfer-" + common.GetRandomString(8),
		BillingMode:    AgencyDurableBillingMode,
		FundingVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)

	require.NoError(t, user.TransferAffQuotaToQuota(transfer))

	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, transfer, stored.Quota)
	assert.Equal(t, transfer, stored.AffQuota)

	var account AgencyFundingAccount
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(transfer), account.NonpaidAvailable)
	assert.Zero(t, account.PaidAvailable)
	assert.Zero(t, account.DebtQuota)

	var ledger AgencyFundingLedger
	require.NoError(t, DB.Where("user_id = ? AND source_kind = ?", user.Id, "affiliate_transfer").First(&ledger).Error)
	assert.Equal(t, int64(transfer), ledger.NonpaidDelta)
}

func TestAgencyQuotaDeltaInvalidatesDurableUserCache(t *testing.T) {
	truncateTables(t)
	require.NoError(t, MigrateAgency(DB))
	useUserCacheMiniRedis(t)

	user := User{
		Username:       "agency-cache-invalidate-" + common.GetRandomString(8),
		Password:       "unused-password-hash",
		Role:           common.RoleCommonUser,
		Status:         common.UserStatusEnabled,
		Quota:          0,
		AffCode:        "agency-cache-invalidate-" + common.GetRandomString(8),
		BillingMode:    AgencyDurableBillingMode,
		FundingVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, populateUserCache(user))
	exists, err := common.RDB.Exists(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), exists)

	require.NoError(t, ApplyAgencyQuotaDelta(int64(user.Id), 10, "cache_invalidation_test"))

	exists, err = common.RDB.Exists(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 10, stored.Quota)
}

func TestUserCheckinCreditsDurableFundingAtomically(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Checkin{}))
	require.NoError(t, MigrateAgency(DB))

	previous := *operation_setting.GetCheckinSetting()
	operation_setting.GetCheckinSetting().Enabled = true
	operation_setting.GetCheckinSetting().MinQuota = 10
	operation_setting.GetCheckinSetting().MaxQuota = 10
	userID := 0
	t.Cleanup(func() {
		*operation_setting.GetCheckinSetting() = previous
		if userID > 0 {
			DB.Where("user_id = ?", userID).Delete(&Checkin{})
		}
	})

	user := User{
		Id:             987654,
		Username:       "agency-checkin-" + common.GetRandomString(8),
		Password:       "unused-password-hash",
		Role:           common.RoleCommonUser,
		Status:         common.UserStatusEnabled,
		Quota:          0,
		AffCode:        "agency-checkin-" + common.GetRandomString(8),
		BillingMode:    AgencyDurableBillingMode,
		FundingVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)
	// The shared test schema may retain the production default for new users;
	// make the durable wallet opening balance explicit for this contract.
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("quota", 0).Error)
	userID = user.Id
	var before User
	require.NoError(t, DB.First(&before, user.Id).Error)

	_, err := UserCheckin(user.Id)
	require.NoError(t, err)

	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 10, stored.Quota)
	var account AgencyFundingAccount
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(10), account.NonpaidAvailable)
}

func TestSettleTaskBillingReconciliationRejectsProvisioningUser(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&TaskBillingReconciliation{}))
	user := createProvisioningQuotaUser(t, 1000)
	task := Task{
		UserId:    user.Id,
		TaskID:    "task-provisioning-guard",
		Status:    TaskStatusSuccess,
		Quota:     100,
		ChannelId: 1,
	}
	require.NoError(t, DB.Create(&task).Error)
	record := TaskBillingReconciliation{
		TaskID:      task.ID,
		Provider:    TaskBillingProviderSeedanceDomestic,
		ChannelID:   task.ChannelId,
		Status:      TaskBillingReconciliationProcessing,
		CreatedAt:   time.Now().Unix(),
		UpdatedAt:   time.Now().Unix(),
		NextRetryAt: time.Now().Unix(),
	}
	require.NoError(t, DB.Create(&record).Error)

	result, err := SettleTaskBillingReconciliation(record.ID, TaskBillingReconciliationSettlement{
		ActualQuota: 200,
		TotalTokens: 1000,
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAgencyProvisioning))
	assert.Nil(t, result)
	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 1000, stored.Quota)
	var storedTask Task
	require.NoError(t, DB.First(&storedTask, task.ID).Error)
	assert.Equal(t, 100, storedTask.Quota)
	var storedRecord TaskBillingReconciliation
	require.NoError(t, DB.First(&storedRecord, record.ID).Error)
	assert.Equal(t, TaskBillingReconciliationProcessing, storedRecord.Status)
}

func createDurableQuotaUser(t *testing.T, id int, usernamePrefix string, quota int) User {
	t.Helper()
	user := User{
		Id:             int(id),
		Username:       usernamePrefix + "-" + common.GetRandomString(8),
		Password:       "unused-password-hash",
		Role:           common.RoleCommonUser,
		Status:         common.UserStatusEnabled,
		Group:          "default",
		Quota:          quota,
		AffCode:        usernamePrefix + "-" + common.GetRandomString(8),
		BillingMode:    AgencyDurableBillingMode,
		FundingVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func TestAdminQuotaAddSubtractOverrideProjectsAgencyFunding(t *testing.T) {
	truncateTables(t)
	require.NoError(t, MigrateAgency(DB))

	user := createDurableQuotaUser(t, 920101, "agency-admin-quota", 100)

	require.NoError(t, IncreaseUserQuota(user.Id, 50, true))
	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 150, stored.Quota)
	var account AgencyFundingAccount
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(50), account.NonpaidAvailable)
	assert.Equal(t, int64(100), account.PaidAvailable)
	var ledger AgencyFundingLedger
	require.NoError(t, DB.Where("user_id = ? AND source_kind = ?", user.Id, "quota_grant").First(&ledger).Error)
	assert.Equal(t, int64(50), ledger.NonpaidDelta)

	require.NoError(t, DecreaseUserQuota(user.Id, 30, true))
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 120, stored.Quota)
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(20), account.NonpaidAvailable)
	var debit AgencyFundingLedger
	require.NoError(t, DB.Where("user_id = ? AND source_kind = ?", user.Id, "quota_debit").First(&debit).Error)
	assert.Equal(t, int64(-30), debit.NonpaidDelta)

	require.NoError(t, SetAgencyQuotaAbsolute(int64(user.Id), 200, "admin_override"))
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 200, stored.Quota)
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(100), account.NonpaidAvailable)
	var override AgencyFundingLedger
	require.NoError(t, DB.Where("user_id = ? AND source_kind = ?", user.Id, "admin_override").First(&override).Error)
	assert.Equal(t, int64(80), override.NonpaidDelta)
}

func TestAdminQuotaAddSubtractRejectsProvisioningUser(t *testing.T) {
	truncateTables(t)
	user := createProvisioningQuotaUser(t, 100)

	require.ErrorIs(t, IncreaseUserQuota(user.Id, 10, true), ErrAgencyProvisioning)
	require.ErrorIs(t, DecreaseUserQuota(user.Id, 10, true), ErrAgencyProvisioning)

	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 100, stored.Quota)
}

func TestAdminQuotaAddLeavesLegacyUserOnQuotaOnlyPath(t *testing.T) {
	truncateTables(t)
	user := User{
		Id:          920102,
		Username:    "agency-legacy-admin-" + common.GetRandomString(8),
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		Quota:       100,
		AffCode:     "agency-legacy-admin-" + common.GetRandomString(8),
		AuthVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)

	require.NoError(t, IncreaseUserQuota(user.Id, 50, true))
	require.NoError(t, DecreaseUserQuota(user.Id, 25, true))
	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 125, stored.Quota)

	var accounts int64
	require.NoError(t, DB.Model(&AgencyFundingAccount{}).Where("user_id = ?", user.Id).Count(&accounts).Error)
	assert.Zero(t, accounts)
}
