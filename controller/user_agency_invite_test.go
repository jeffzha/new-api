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
	t.Setenv("AGENCY_ONBOARDING_ENABLED", "true")
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRegisterEnabled := common.RegisterEnabled
	previousPasswordRegisterEnabled := common.PasswordRegisterEnabled
	previousEmailVerificationEnabled := common.EmailVerificationEnabled
	previousSMTPServer, previousSMTPPort := common.SMTPServer, common.SMTPPort
	previousSMTPAccount, previousSMTPFrom, previousSMTPToken := common.SMTPAccount, common.SMTPFrom, common.SMTPToken
	previousQuotaForNewUser := common.QuotaForNewUser
	previousGenerateDefaultToken := constant.GenerateDefaultToken

	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RegisterEnabled = true
	common.PasswordRegisterEnabled = true
	common.EmailVerificationEnabled = false
	common.SMTPServer, common.SMTPAccount, common.SMTPFrom, common.SMTPToken = "", "", "", ""
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
		common.SMTPServer, common.SMTPPort = previousSMTPServer, previousSMTPPort
		common.SMTPAccount, common.SMTPFrom, common.SMTPToken = previousSMTPAccount, previousSMTPFrom, previousSMTPToken
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
	common.EmailVerificationEnabled = true
	common.SMTPServer, common.SMTPPort = "smtp.example.com", 465
	common.SMTPAccount, common.SMTPFrom, common.SMTPToken = "sender@example.com", "sender@example.com", "secret"
	agency, _, err := app.CreateAgency(1, "Invite Agency", "invite_operator", agencyInviteTestPolicy())
	require.NoError(t, err)

	email := "invite.customer@example.com"
	code := "539172"
	common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose)
	t.Cleanup(func() {
		common.DeleteKey(email, common.EmailVerificationPurpose)
	})
	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"email":%q,"password":"password123","verification_code":%q,"invite":%q}`, email, email, code, agency.InviteCode))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var user model.User
	require.NoError(t, db.Where("username = ?", email).First(&user).Error)
	assert.Equal(t, email, user.Email)
	assert.False(t, common.VerifyCodeWithKey(email, code, common.EmailVerificationPurpose))
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

func TestRegistrationUsesVerifiedEmailAsAccountAndConsumesCode(t *testing.T) {
	db, _ := setupAgencyInviteControllerTest(t)
	common.EmailVerificationEnabled = true
	common.SMTPServer, common.SMTPPort = "smtp.example.com", 465
	common.SMTPAccount, common.SMTPFrom, common.SMTPToken = "sender@example.com", "sender@example.com", "secret"
	email := "verified.customer@example.com"
	code := "482913"
	common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose)
	t.Cleanup(func() {
		common.DeleteKey(email, common.EmailVerificationPurpose)
	})

	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"email":%q,"password":"password123","verification_code":%q}`, email, email, code))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"success":true`)

	var user model.User
	require.NoError(t, db.Where("username = ?", email).First(&user).Error)
	assert.Equal(t, email, user.Username)
	assert.Equal(t, email, user.Email)
	assert.Equal(t, "verified.customer", user.DisplayName)
	assert.False(t, common.VerifyCodeWithKey(email, code, common.EmailVerificationPurpose))
}

func TestRegistrationWithoutSMTPUsesEmailAccountWithoutCode(t *testing.T) {
	db, _ := setupAgencyInviteControllerTest(t)
	common.EmailVerificationEnabled = true
	email := "unverified.customer@example.com"

	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"password":"password123"}`, email))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"success":true`)

	var user model.User
	require.NoError(t, db.Where("username = ?", email).First(&user).Error)
	assert.Equal(t, email, user.Email)
	assert.Equal(t, "unverified.customer", user.DisplayName)
}

func TestPlatformRegistrationRejectsNonEmailAccount(t *testing.T) {
	db, _ := setupAgencyInviteControllerTest(t)
	common.EmailVerificationEnabled = false

	recorder := postAgencyInviteRegister(t, `{"username":"ordinary_username","password":"password123"}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"success":false`)

	var count int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", "ordinary_username").Count(&count).Error)
	assert.Zero(t, count)
}

func TestAgencyInviteRegistrationStillRejectsNonEmailAccount(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	agency, _, err := app.CreateAgency(1, "Email Only Customers", "ordinary_operator", agencyInviteTestPolicy())
	require.NoError(t, err)

	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"invite_customer","password":"password123","invite":%q}`, agency.InviteCode))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"success":false`)

	var count int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", "invite_customer").Count(&count).Error)
	assert.Zero(t, count)
}

func TestAgencyOnboardingPausePreservesOrdinaryRegistrationAndExistingMode(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	agency, _, err := app.CreateAgency(1, "Pause Agency", "pause_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	existingEmail := "existing.durable@example.com"
	pausedEmail := "paused.invitee@example.com"
	ordinaryEmail := "ordinary.customer@example.com"
	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"password":"password123","invite":%q}`, existingEmail, agency.InviteCode))
	require.Contains(t, recorder.Body.String(), `"success":true`)
	t.Setenv("AGENCY_ONBOARDING_ENABLED", "false")
	recorder = postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"password":"password123","invite":%q}`, pausedEmail, agency.InviteCode))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	var count int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", pausedEmail).Count(&count).Error)
	assert.Zero(t, count)
	recorder = postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"password":"password123"}`, ordinaryEmail))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	var durable model.User
	require.NoError(t, db.Where("username = ?", existingEmail).First(&durable).Error)
	assert.Equal(t, model.AgencyDurableBillingMode, durable.BillingMode)
	var ordinary model.User
	require.NoError(t, db.Where("username = ?", ordinaryEmail).First(&ordinary).Error)
	assert.NotEqual(t, model.AgencyDurableBillingMode, ordinary.BillingMode)
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
		{name: "invalid", username: "invalid.invite@example.com", invite: "NOTFOUND"},
		{name: "disabled", username: "disabled.invite@example.com", invite: agency.InviteCode},
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

	email := "mixed.invite@example.com"
	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"password":"password123","invite":%q,"aff_code":"LEGACY"}`, email, agency.InviteCode))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)

	var userCount int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", email).Count(&userCount).Error)
	assert.Zero(t, userCount)
	var bindingCount int64
	require.NoError(t, db.Model(&model.AgencyUserBinding{}).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
}

func TestAgencyInviteRegistrationRejectsDuplicateUserBeforeBinding(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	agency, _, err := app.CreateAgency(1, "Duplicate Agency", "duplicate_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	email := "duplicate.customer@example.com"
	require.NoError(t, db.Create(&model.User{Username: email, Email: email, Password: "password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: "DUP"}).Error)

	recorder := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":%q,"password":"password123","invite":%q}`, email, agency.InviteCode))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)

	var bindingCount int64
	require.NoError(t, db.Model(&model.AgencyUserBinding{}).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
	var tokenCount int64
	require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Zero(t, tokenCount)
}
