package controller

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAgencyPricingFallbackTest(t *testing.T, billingMode string) (*gorm.DB, *model.User) {
	t.Helper()
	previousDB := model.DB
	dsn := "file:agency-pricing-fallback-" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	user := &model.User{
		Username:    "agency-pricing-fallback",
		Password:    "password",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		BillingMode: billingMode,
	}
	require.NoError(t, db.Create(user).Error)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, closeErr := db.DB()
		if closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db, user
}

func TestResolveAgencyPriceDoesNotFallbackDurableUserWhenAgencySchemaIsUnavailable(t *testing.T) {
	_, user := setupAgencyPricingFallbackTest(t, model.AgencyDurableBillingMode)
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{
		UserId:          user.Id,
		OriginModelName: "model-unavailable",
		StartTime:       time.Now(),
	}
	called := false
	_, err := resolveAgencyPrice(context, info, func() (hosttypes.PriceData, error) {
		called = true
		return hosttypes.PriceData{QuotaToPreConsume: 7}, nil
	})
	require.Error(t, err)
	assert.False(t, called, "durable users must fail closed when agency schema is unavailable")
	assert.Contains(t, err.Error(), "agency pricing")
}

func TestResolveAgencyPriceKeepsLegacyFallbackWhenAgencySchemaIsUnavailable(t *testing.T) {
	_, user := setupAgencyPricingFallbackTest(t, "")
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{
		UserId:          user.Id,
		OriginModelName: "legacy-model",
		StartTime:       time.Now(),
	}
	called := false
	price, err := resolveAgencyPrice(context, info, func() (hosttypes.PriceData, error) {
		called = true
		return hosttypes.PriceData{QuotaToPreConsume: 9}, nil
	})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, 9, price.QuotaToPreConsume)
}
