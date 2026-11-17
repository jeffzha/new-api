package resellerhub

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Task{}, &model.Log{}))
	require.NoError(t, Migrate(db))
	return db
}

func quotaTestContext(actorID int) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/reseller/api/test", strings.NewReader("{}"))
	c.Set("reseller_hub_identity", &Identity{NewAPIUserID: actorID, HubRole: HubRoleResellerAdmin, ResellerID: 1})
	return c
}

func seedQuotaFixture(t *testing.T, db *gorm.DB, remainQuota, usedQuota, tokenStatus int) (Reseller, Customer, CustomerToken, model.Token) {
	t.Helper()
	user := model.User{Username: "carrier", Password: "not-used-password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 2000000, Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	reseller := Reseller{Code: "r1", Name: "R1", Status: ResellerStatusActive, DefaultDiscountBPS: 8500, QuotaCarrierUserID: user.Id, CreatedByUserID: 100}
	require.NoError(t, db.Create(&reseller).Error)
	customer := Customer{ResellerID: reseller.ID, DisplayName: "Customer", Status: CustomerStatusActive, CreatedByUserID: 1}
	require.NoError(t, db.Create(&customer).Error)
	token := model.Token{UserId: user.Id, Key: "quota-test-key", Status: tokenStatus, Name: "customer-key", CreatedTime: 1, ExpiredTime: -1, RemainQuota: remainQuota, UsedQuota: usedQuota, UnlimitedQuota: false, Group: "default"}
	require.NoError(t, db.Create(&token).Error)
	mapping := CustomerToken{ResellerID: reseller.ID, CustomerID: customer.ID, NewAPITokenID: token.Id, QuotaCarrierUserID: user.Id, Status: CustomerTokenStatusActive, EffectiveAt: 1, CreatedByUserID: 1}
	require.NoError(t, db.Create(&mapping).Error)
	require.NoError(t, db.Model(&customer).Update("active_token_mapping_id", mapping.ID).Error)
	customer.ActiveTokenMappingID = &mapping.ID
	return reseller, customer, mapping, token
}

func TestTokenQuotaAdjustmentIsIdempotentAndDoesNotChangeUsedQuota(t *testing.T) {
	db := openServiceTestDB(t)
	_, customer, mapping, _ := seedQuotaFixture(t, db, 1000, 77, common.TokenStatusEnabled)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	app := New(db, db, Config{RedisEventMarkerTTL: 86400})
	c := quotaTestContext(9)
	currency := CurrencyConfig{DisplayType: "USD", Symbol: "$", QuotaPerUnit: decimal.NewFromInt(500000), USDToDisplayRate: decimal.NewFromInt(1)}
	input := quotaAdjustmentRequest{Mode: "add", InputUnit: "quota", Amount: "500", Reason: "recharge", IdempotencyKey: "evt-add-1"}

	ledger, duplicate, err := app.applyTokenQuotaAdjustment(c, &customer, mapping, input, currency, 8500, 500, decimal.Zero)
	require.NoError(t, err)
	assert.False(t, duplicate)
	assert.Equal(t, ledgerStatusApplied, ledger.Status)
	assert.Equal(t, 1000, ledger.QuotaBefore)
	assert.Equal(t, 1500, ledger.QuotaAfter)
	assert.Equal(t, 77, ledger.UsedQuotaBefore)
	assert.Equal(t, 77, ledger.UsedQuotaAfter)

	ledger, duplicate, err = app.applyTokenQuotaAdjustment(c, &customer, mapping, input, currency, 8500, 500, decimal.Zero)
	require.NoError(t, err)
	assert.True(t, duplicate)
	assert.Equal(t, 1500, ledger.QuotaAfter)

	var token model.Token
	require.NoError(t, db.First(&token, mapping.NewAPITokenID).Error)
	assert.Equal(t, 1500, token.RemainQuota)
	assert.Equal(t, 77, token.UsedQuota)
	var ledgers int64
	require.NoError(t, db.Model(&QuotaLedger{}).Count(&ledgers).Error)
	assert.EqualValues(t, 1, ledgers)
}

func TestTokenQuotaAdjustmentRejectsReusedKeyWithDifferentPayload(t *testing.T) {
	db := openServiceTestDB(t)
	_, customer, mapping, _ := seedQuotaFixture(t, db, 1000, 77, common.TokenStatusEnabled)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	app := New(db, db, Config{RedisEventMarkerTTL: 86400})
	c := quotaTestContext(9)
	currency := CurrencyConfig{DisplayType: "USD", Symbol: "$", QuotaPerUnit: decimal.NewFromInt(500000), USDToDisplayRate: decimal.NewFromInt(1)}
	input := quotaAdjustmentRequest{Mode: "add", InputUnit: "quota", Amount: "500", Reason: "recharge", IdempotencyKey: "evt-payload-check"}

	_, _, err := app.applyTokenQuotaAdjustment(c, &customer, mapping, input, currency, 8500, 500, decimal.Zero)
	require.NoError(t, err)
	input.Amount = "750"
	_, _, err = app.applyTokenQuotaAdjustment(c, &customer, mapping, input, currency, 8500, 750, decimal.Zero)
	require.ErrorContains(t, err, "different quota adjustment")

	var token model.Token
	require.NoError(t, db.First(&token, mapping.NewAPITokenID).Error)
	assert.Equal(t, 1500, token.RemainQuota)
}

func TestTokenQuotaReversalCreatesOppositeLedgerAndCannotRepeat(t *testing.T) {
	db := openServiceTestDB(t)
	_, customer, mapping, _ := seedQuotaFixture(t, db, 1000, 77, common.TokenStatusEnabled)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	app := New(db, db, Config{RedisEventMarkerTTL: 86400})
	c := quotaTestContext(9)
	currency := CurrencyConfig{DisplayType: "USD", Symbol: "$", QuotaPerUnit: decimal.NewFromInt(500000), USDToDisplayRate: decimal.NewFromInt(1)}
	add := quotaAdjustmentRequest{Mode: "add", InputUnit: "quota", Amount: "500", Reason: "recharge", IdempotencyKey: "evt-original"}

	original, _, err := app.applyTokenQuotaAdjustment(c, &customer, mapping, add, currency, 8500, 500, decimal.Zero)
	require.NoError(t, err)
	reverse := quotaAdjustmentRequest{
		Mode: "subtract", InputUnit: "quota", Amount: "500", Reason: "reverse recharge",
		IdempotencyKey: "evt-reverse", ReversesEventID: original.EventID,
	}
	reversal, _, err := app.applyTokenQuotaAdjustment(c, &customer, mapping, reverse, currency, 8500, 500, decimal.Zero)
	require.NoError(t, err)
	require.NotNil(t, reversal.ReversesEventID)
	assert.Equal(t, original.EventID, *reversal.ReversesEventID)

	var storedOriginal QuotaLedger
	require.NoError(t, db.First(&storedOriginal, original.ID).Error)
	assert.Equal(t, QuotaLedgerStatusReversed, storedOriginal.Status)
	var token model.Token
	require.NoError(t, db.First(&token, mapping.NewAPITokenID).Error)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Equal(t, 77, token.UsedQuota)

	reverse.IdempotencyKey = "evt-reverse-again"
	_, _, err = app.applyTokenQuotaAdjustment(c, &customer, mapping, reverse, currency, 8500, 500, decimal.Zero)
	require.Error(t, err)
	require.NoError(t, db.First(&token, mapping.NewAPITokenID).Error)
	assert.Equal(t, 1000, token.RemainQuota)
}

func TestManualSubtractCannotCreateNegativeBalance(t *testing.T) {
	db := openServiceTestDB(t)
	_, customer, mapping, _ := seedQuotaFixture(t, db, 100, 25, common.TokenStatusEnabled)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	app := New(db, db, Config{RedisEventMarkerTTL: 86400})
	c := quotaTestContext(9)
	currency := CurrencyConfig{DisplayType: "USD", Symbol: "$", QuotaPerUnit: decimal.NewFromInt(500000), USDToDisplayRate: decimal.NewFromInt(1)}
	input := quotaAdjustmentRequest{Mode: "subtract", InputUnit: "quota", Amount: "101", Reason: "manual recovery", IdempotencyKey: "evt-subtract-1"}

	_, _, err := app.applyTokenQuotaAdjustment(c, &customer, mapping, input, currency, 8500, 101, decimal.Zero)
	require.Error(t, err)
	var token model.Token
	require.NoError(t, db.First(&token, mapping.NewAPITokenID).Error)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Equal(t, 25, token.UsedQuota)
	var ledgers int64
	require.NoError(t, db.Model(&QuotaLedger{}).Count(&ledgers).Error)
	assert.EqualValues(t, 0, ledgers)
}

func TestAddingQuotaReenablesOnlyExhaustedToken(t *testing.T) {
	db := openServiceTestDB(t)
	_, customer, mapping, _ := seedQuotaFixture(t, db, -20, 90, common.TokenStatusExhausted)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	app := New(db, db, Config{RedisEventMarkerTTL: 86400})
	c := quotaTestContext(9)
	currency := CurrencyConfig{DisplayType: "USD", Symbol: "$", QuotaPerUnit: decimal.NewFromInt(500000), USDToDisplayRate: decimal.NewFromInt(1)}
	input := quotaAdjustmentRequest{Mode: "add", InputUnit: "quota", Amount: "30", Reason: "debt paid", IdempotencyKey: "evt-debt-1"}

	_, _, err := app.applyTokenQuotaAdjustment(c, &customer, mapping, input, currency, 8500, 30, decimal.Zero)
	require.NoError(t, err)
	var token model.Token
	require.NoError(t, db.First(&token, mapping.NewAPITokenID).Error)
	assert.Equal(t, 10, token.RemainQuota)
	assert.Equal(t, common.TokenStatusEnabled, token.Status)
	assert.Equal(t, 90, token.UsedQuota)
}

func TestStaleReconciliationPausesNewQuotaWrites(t *testing.T) {
	db := openServiceTestDB(t)
	reseller, _, _, _ := seedQuotaFixture(t, db, 1000, 0, common.TokenStatusEnabled)
	ledger := QuotaLedger{
		EventID: "stale-event", IdempotencyKey: "stale-key", ResellerID: reseller.ID,
		TargetType: QuotaTargetToken, NewAPIUserID: reseller.QuotaCarrierUserID,
		Operation: QuotaOperationAdd, RequestedQuota: 1, QuotaDelta: 1,
		InputUnit: "quota", InputAmountDecimal: "1", CurrencyTypeSnapshot: "USD",
		CurrencySymbolSnapshot: "$", QuotaPerUnitSnapshot: "500000", USDToCurrencyRateSnapshot: "1",
		DiscountBPSSnapshot: 10000, Status: QuotaLedgerStatusReconcileRequired,
		Reason: "test", ActorUserID: 1, RequestID: "stale-request", CreatedAt: time.Now().Add(-10 * time.Minute).Unix(),
	}
	require.NoError(t, db.Create(&ledger).Error)
	app := New(db, db, Config{ConsistencyGrace: time.Minute})

	err := app.ensureQuotaWritesConverged(context.Background(), reseller.ID)
	require.ErrorContains(t, err, "paused")
}

func TestUserQuotaAdjustmentProtectsNativeFundingAcrossDialects(t *testing.T) {
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
			case "mysql":
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || os.Getenv("AGENCY_HUB_TEST_MYSQL_DSN") == "" {
					t.Skip("isolated external database not configured")
				}
				driver = mysql.Open(os.Getenv("AGENCY_HUB_TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || os.Getenv("AGENCY_HUB_TEST_POSTGRES_DSN") == "" {
					t.Skip("isolated external database not configured")
				}
				driver = postgres.Open(os.Getenv("AGENCY_HUB_TEST_POSTGRES_DSN"))
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&model.User{}, &QuotaLedger{}))
			app := New(db, db, Config{})
			currency := CurrencyConfig{DisplayType: "USD", Symbol: "$", QuotaPerUnit: decimal.NewFromInt(500000), USDToDisplayRate: decimal.NewFromInt(1)}
			for _, mode := range []string{"", model.AgencyDurableBillingMode, model.AgencyProvisioningBillingMode} {
				id := "guard" + common.GetUUID()[:15]
				user := model.User{Username: id, AffCode: id, Status: common.UserStatusEnabled, Quota: 100, BillingMode: mode}
				require.NoError(t, db.Create(&user).Error)
				input := quotaAdjustmentRequest{Mode: "add", InputUnit: "quota", Amount: "50", Reason: "test", IdempotencyKey: id}
				_, _, err := app.applyUserQuotaAdjustment(quotaTestContext(9), Reseller{ID: user.Id, QuotaCarrierUserID: user.Id}, input, currency, 50, decimal.Zero)
				var stored model.User
				require.NoError(t, db.First(&stored, user.Id).Error)
				var count int64
				require.NoError(t, db.Model(&QuotaLedger{}).Where("idempotency_key = ?", id).Count(&count).Error)
				if mode == "" {
					require.NoError(t, err)
					assert.Equal(t, 150, stored.Quota)
					assert.EqualValues(t, 1, count)
				} else {
					require.ErrorIs(t, err, model.ErrAgencyFundingUnavailable)
					assert.Equal(t, 100, stored.Quota)
					assert.Zero(t, count)
				}
			}
		})
	}
}
