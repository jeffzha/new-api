package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAgencyInviteControllerTest(t *testing.T) (*gorm.DB, *agencyhub.App) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRegisterEnabled := common.RegisterEnabled
	previousPasswordRegisterEnabled := common.PasswordRegisterEnabled
	previousEmailVerificationEnabled := common.EmailVerificationEnabled
	previousQuotaForNewUser := common.QuotaForNewUser
	previousGenerateDefaultToken := constant.GenerateDefaultToken

	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RegisterEnabled = true
	common.PasswordRegisterEnabled = true
	common.EmailVerificationEnabled = false
	common.QuotaForNewUser = 12345
	constant.GenerateDefaultToken = true

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Token{}, &model.Task{}))
	require.NoError(t, model.MigrateAgency(db))

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.RegisterEnabled = previousRegisterEnabled
		common.PasswordRegisterEnabled = previousPasswordRegisterEnabled
		common.EmailVerificationEnabled = previousEmailVerificationEnabled
		common.QuotaForNewUser = previousQuotaForNewUser
		constant.GenerateDefaultToken = previousGenerateDefaultToken
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	app := agencyhub.New(db, db, agencyhub.Config{BasePath: "/agency", PublicBaseURL: "https://gateway.example", SessionIdle: 30 * time.Minute, SessionAbsolute: time.Hour, MaxLoginAttempts: 5, SalesCapBPS: 30000, MinSpreadBPS: 500})
	return db, app
}

func agencyInviteTestPolicy() agencycontract.Policy {
	return agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
}

func postAgencyInviteRegister(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/user/register", Register)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestAgencyInviteRegistrationCreatesDurableUserBindingFundingAndDefaultToken(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	agency, _, err := app.CreateAgency(1, "Invite Agency", "invite_operator", agencyInviteTestPolicy())
	require.NoError(t, err)

	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"invite_customer","password":"password123","invite":%q}`, agency.InviteCode))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var user model.User
	require.NoError(t, db.Where("username = ?", "invite_customer").First(&user).Error)
	assert.Equal(t, 0, user.Quota)
	assert.Equal(t, model.AgencyDurableBillingMode, user.BillingMode)
	assert.EqualValues(t, 1, user.FundingVersion)
	assert.Zero(t, user.InviterId)

	var active model.AgencyActiveUserBinding
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&active).Error)
	assert.Equal(t, agency.ID, active.AgencyID)

	var binding model.AgencyUserBinding
	require.NoError(t, db.First(&binding, active.BindingID).Error)
	assert.Equal(t, "invite_register", binding.CreatedSource)
	assert.Equal(t, agency.InviteCode, binding.InviteSnapshot)

	var account model.AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	assert.Zero(t, account.PaidAvailable)
	assert.Zero(t, account.NonpaidAvailable)
	assert.Zero(t, account.DebtQuota)

	var token model.Token
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&token).Error)
	assert.True(t, token.UnlimitedQuota)
	assert.Equal(t, 500000, token.RemainQuota)
}

func TestAgencyInviteRegistrationRollsBackUserWhenInviteIsInvalidOrDisabled(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	agency, _, err := app.CreateAgency(1, "Disabled Agency", "disabled_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Agency{}).Where("id = ?", agency.ID).Update("status", agencyhub.AgencyStatusDisabled).Error)

	testCases := []struct {
		name     string
		username string
		invite   string
	}{
		{name: "invalid", username: "invalid_invite_user", invite: "NOTFOUND"},
		{name: "disabled", username: "disabled_invite_user", invite: agency.InviteCode},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"password":"password123","invite":%q}`, testCase.username, testCase.invite))
			require.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), "agency invitation is invalid or disabled")

			var userCount int64
			require.NoError(t, db.Model(&model.User{}).Where("username = ?", testCase.username).Count(&userCount).Error)
			assert.Zero(t, userCount)
			var tokenCount int64
			require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
			assert.Zero(t, tokenCount)
			var bindingCount int64
			require.NoError(t, db.Model(&model.AgencyUserBinding{}).Count(&bindingCount).Error)
			assert.Zero(t, bindingCount)
		})
	}
}

func TestAgencyInviteRegistrationRejectsLegacyAffiliateCode(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	agency, _, err := app.CreateAgency(1, "Affiliate Reject Agency", "affiliate_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	inviter := model.User{Username: "legacy_inviter", Password: "password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: "LEGACY"}
	require.NoError(t, db.Create(&inviter).Error)

	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"mixed_invite_customer","password":"password123","invite":%q,"aff_code":"LEGACY"}`, agency.InviteCode))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)

	var userCount int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", "mixed_invite_customer").Count(&userCount).Error)
	assert.Zero(t, userCount)
	var bindingCount int64
	require.NoError(t, db.Model(&model.AgencyUserBinding{}).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
}

func TestAgencyInviteRegistrationRejectsDuplicateUserBeforeBinding(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	agency, _, err := app.CreateAgency(1, "Duplicate Agency", "duplicate_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.User{Username: "duplicate_customer", Password: "password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: "DUP"}).Error)

	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"duplicate_customer","password":"password123","invite":%q}`, agency.InviteCode))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)

	var bindingCount int64
	require.NoError(t, db.Model(&model.AgencyUserBinding{}).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
	var tokenCount int64
	require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Zero(t, tokenCount)
}
