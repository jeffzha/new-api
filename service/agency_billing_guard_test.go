package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPreConsumeBillingRejectsDurableUserWithoutQuote(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-billing-guard-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.User{}))

	user := &model.User{
		Username:    "durable-without-quote",
		Password:    "not-used-password",
		Status:      common.UserStatusEnabled,
		BillingMode: model.AgencyDurableBillingMode,
	}
	require.NoError(t, db.Create(user).Error)

	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	apiErr := PreConsumeBilling(ginContext, 10, &relaycommon.RelayInfo{UserId: user.Id})
	require.Error(t, apiErr)
	require.Equal(t, "agency pricing unavailable for durable user", apiErr.Error())
}
