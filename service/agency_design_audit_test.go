//go:build agency_audit

package service

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These opt-in acceptance probes assert the design document's contracts. They
// intentionally remain separate from the default suite while those contracts
// are incomplete: go test -tags agency_audit ./service -run TestAgencyDesign.
func agencyDesignAuditFixture(t *testing.T) (*gorm.DB, *model.User, *agencycontract.PricingSnapshot) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "audit.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB, previousRedis := model.DB, common.RedisEnabled
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() {
		model.DB, common.RedisEnabled = previousDB, previousRedis
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, model.MigrateAgency(db))
	user := &model.User{Username: "audit-user", BillingMode: model.AgencyDurableBillingMode,
		FundingVersion: 1, Status: common.UserStatusEnabled, Quota: 200}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.RecordAgencyTopup(tx, int64(user.Id), "payment", "audit-topup", "payment_callback", 100, 100)
	}))
	require.NoError(t, db.Create(&model.Agency{ID: 1, Code: "audit", DisplayName: "Audit", InviteCode: "0123456789",
		Status: "active", CurrentPolicyVersionID: 1, PriceRevision: 1, StateRevision: 1, Version: 1}).Error)
	require.NoError(t, db.Create(&model.AgencyPricePolicyVersion{ID: 1, AgencyID: 1, Revision: 1, PolicyJSON: "{}", PolicyHash: "audit"}).Error)
	require.NoError(t, db.Create(&model.AgencyUserBinding{ID: 1, UserID: int64(user.Id), AgencyID: 1, Revision: 1}).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: int64(user.Id), BindingID: 1, AgencyID: 1, Revision: 1}).Error)
	snapshot := &agencycontract.PricingSnapshot{AgencyID: 1, BindingID: 1, BindingRevision: 1,
		AgencyStateRevision: 1, PolicyVersionID: 1, PolicyRevision: 1, UserID: int64(user.Id),
		OriginModelName: "audit-model", ModelKey: "audit-model", SettlementBPS: 7500, SalesBPS: 9000,
		CommissionEligible: true, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	return db, user, snapshot
}

func TestAgencyDesignReservePersistsAcceptedJournal(t *testing.T) {
	db, user, snapshot := agencyDesignAuditFixture(t)
	_, _, err := model.TryReserveAgencyWalletAndTokenWithSequence(user.Id, 0, 100, "", "audit-reserve", 100, true, snapshot)
	require.NoError(t, err)
	var stored model.User
	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, 100, stored.Quota)
	var journal model.AgencyBillingJournal
	err = db.Where("charge_id = ?", "audit-reserve").First(&journal).Error
	require.NoError(t, err, "design 8.4: successful reserve must durably capture the accepted quote before upstream I/O")
	assert.Equal(t, "reserved", journal.Status)
	assert.NotEmpty(t, journal.PricingSnapshot)
}

func TestAgencyDesignDisabledAgencyKeepsCustomerCharging(t *testing.T) {
	db, user, snapshot := agencyDesignAuditFixture(t)
	require.NoError(t, db.Model(&model.Agency{}).Where("id = ?", 1).Updates(map[string]any{"status": "disabled", "state_revision": 2}).Error)
	snapshot.AgencyStateRevision = 2
	snapshot.CommissionEligible = false
	snapshot.EligibilityReason = "agency_disabled"
	_, _, err := model.TryReserveAgencyWalletAndTokenWithSequence(user.Id, 0, 90, "", "audit-disabled", 90, true, snapshot)
	require.NoError(t, err, "design 3.2/9.1: disabling an agency must preserve its customer's last published sales price and API access")
}

func TestAgencyDesignNoncommissionableDebitUsesPaidFirst(t *testing.T) {
	db, user, _ := agencyDesignAuditFixture(t)
	require.NoError(t, model.ApplyAgencyQuotaDelta(int64(user.Id), -50, "subscription_purchase"))
	var account model.AgencyFundingAccount
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(50), account.PaidAvailable, "design 2.4/8.6: every real wallet debit consumes paid funds first")
	assert.Equal(t, int64(100), account.NonpaidAvailable)
}

func TestAgencyDesignRealtimeCostUsesOriginalBasis(t *testing.T) {
	db, user, snapshot := agencyDesignAuditFixture(t)
	info := &relaycommon.RelayInfo{UserId: user.Id, RequestId: "audit-realtime",
		AgencyPricing: snapshot, AgencyStandardQuota: 1000, AgencyMoneySeq: 2, RequestURLPath: "/v1/realtime"}
	usage := &dto.RealtimeUsage{InputTokens: 1000, TotalTokens: 1000}
	require.NoError(t, RecordAgencyRealtimeSegment(info, 0, usage, usage, 900, 900, "success"))
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", info.RequestId).First(&journal).Error)
	assert.Equal(t, int64(750), journal.SettlementCostQuota, "design 2.1: Q=1000 and C=0.75 gives T=750; S must not be applied again to settlement cost")
	assert.Equal(t, int64(150), journal.TheoreticalCommissionQuota)
}

func TestAgencyDesignUnlimitedTokenPreservesInt32Bounds(t *testing.T) {
	db, user, snapshot := agencyDesignAuditFixture(t)
	token := &model.Token{UserId: user.Id, Key: "audit-key", UnlimitedQuota: true,
		RemainQuota: -common.MaxQuota, UsedQuota: common.MaxQuota, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(token).Error)
	_, _, err := model.TryReserveAgencyWalletAndTokenWithSequence(user.Id, token.Id, 10, token.Key, "audit-overflow", 10, true, snapshot)
	assert.Error(t, err, "design 8.6: unlimited bypasses quota availability only; accumulated storage bounds must still be enforced")
	var stored model.Token
	require.NoError(t, db.First(&stored, token.Id).Error)
	assert.LessOrEqual(t, int64(stored.UsedQuota), int64(common.MaxQuota))
	assert.GreaterOrEqual(t, int64(stored.RemainQuota), -int64(common.MaxQuota)-1)
}
