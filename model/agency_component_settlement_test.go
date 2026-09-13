package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func agencyComponentSettlementFixture(t *testing.T, dialect string) (*gorm.DB, User, agencycontract.BillingEvent) {
	t.Helper()
	db := agencyDialectDB(t, dialect)
	if db == nil {
		t.Skip("isolated external test database is not configured")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	previousDB, previousRedis := DB, common.RedisEnabled
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	DB, common.RedisEnabled = db, false
	common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
	t.Cleanup(func() {
		DB, common.RedisEnabled = previousDB, previousRedis
		common.SetDatabaseTypes(previousMain, previousLog)
		require.NoError(t, pool.Close())
	})
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "true")
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, MigrateAgency(db))
	prefix := "component-" + common.GetUUID()[:10]
	user := User{Username: prefix, AffCode: prefix, Status: common.UserStatusEnabled, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Quota: 150}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAgencyTopup(tx, int64(user.Id), "payment", prefix+"-first", "payment_callback", 40, 20); err != nil {
			return err
		}
		return RecordAgencyTopup(tx, int64(user.Id), "payment", prefix+"-second", "payment_callback", 60, 30)
	}))
	snapshot := &agencycontract.PricingSnapshot{AgencyID: 101, BindingID: 201, OriginModelName: "component-model",
		SettlementBPS: 7500, SalesBPS: 10000, CommissionEligible: true, CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	_, _, err = TryReserveAgencyWalletAndTokenWithSequence(user.Id, 0, 100, "", prefix, 100, true, snapshot)
	require.NoError(t, err)
	return db, user, agencycontract.BillingEvent{FinancialChargeID: prefix, UserID: int64(user.Id), BusinessStatus: "success",
		OriginModelName: "component-model", StandardQuota: 120, ChargedTotalQuota: 180,
		CommissionableQuota: 120, NoncommissionableQuota: 60, SettlementCostQuota: 90, TheoreticalCommissionQuota: 30}
}

func TestAgencyComponentSettlementAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, user, input := agencyComponentSettlementFixture(t, dialect)
			var originalAllocations []AgencyFundingAllocation
			require.NoError(t, db.Where("charge_id = ?", input.FinancialChargeID).Order("id").Find(&originalAllocations).Error)
			result, err := AgencyCommitWalletCharge(input, "")
			require.NoError(t, err)
			require.NoError(t, agencycontract.ValidateBillingComponents(result))
			assert.Equal(t, agencycontract.ComponentSchemaVersion, result.SchemaVersion)
			require.Len(t, result.Components, 2)
			assert.Equal(t, int64(67), result.Components[0].PaidAllocatedQuota)
			assert.Equal(t, int64(33), result.Components[0].NonpaidAllocatedQuota)
			assert.Equal(t, int64(20), result.Components[0].DebtAllocatedQuota)
			assert.Equal(t, int64(17), result.Components[0].CommissionQuota)
			assert.Equal(t, int64(33), result.Components[1].PaidAllocatedQuota)
			assert.Equal(t, int64(17), result.Components[1].NonpaidAllocatedQuota)
			assert.Equal(t, int64(10), result.Components[1].DebtAllocatedQuota)
			assert.Zero(t, result.Components[1].CommissionAmountMicros)
			assert.Equal(t, int64(17), result.CommissionAmountMicros)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, -30, user.Quota)
			var account AgencyFundingAccount
			require.NoError(t, db.First(&account, user.Id).Error)
			assert.Equal(t, int64(30), account.DebtQuota)
			assert.Equal(t, int64(user.Quota), account.PaidAvailable+account.NonpaidAvailable-account.DebtQuota)
			var components []AgencyChargeComponent
			require.NoError(t, db.Where("charge_id = ?", input.FinancialChargeID).Order("component_id").Find(&components).Error)
			require.Len(t, components, 2)
			var matrix []AgencyComponentFunding
			require.NoError(t, db.Where("charge_component_id = ?", components[0].ID).Order("id").Find(&matrix).Error)
			var paid []int64
			var paidIDs []int64
			for _, row := range matrix {
				if row.PaidQuota > 0 {
					paid = append(paid, row.PaidQuota)
					paidIDs = append(paidIDs, row.AllocationID)
				}
			}
			assert.Equal(t, []int64{40, 27}, paid, "paid source lots fill the first component in FIFO order")
			assert.Equal(t, []int64{originalAllocations[0].ID, originalAllocations[1].ID}, paidIDs, "retain original allocation identities used by chargeback provenance")
			var ops []AgencyBillingOperation
			require.NoError(t, db.Where("charge_id = ? AND operation = ?", input.FinancialChargeID, "finalize").Find(&ops).Error)
			require.Len(t, ops, 1)
			var committed agencycontract.BillingEvent
			require.NoError(t, common.UnmarshalJsonStr(ops[0].CommittedResult, &committed))
			assert.Equal(t, result, committed)
			replayed, err := AgencyCommitWalletCharge(input, "")
			require.NoError(t, err)
			assert.Equal(t, result, replayed)
			input.NoncommissionableQuota++
			input.ChargedTotalQuota++
			_, err = AgencyCommitWalletCharge(input, "")
			assert.ErrorIs(t, err, ErrAgencyChargeConflict)
		})
	}
}

func TestAgencyComponentSettlementPreservesExactIDsAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, _, input := agencyComponentSettlementFixture(t, dialect)
			input.StandardQuota, input.ChargedTotalQuota, input.CommissionableQuota = 120, 120, 120
			input.NoncommissionableQuota = 0
			input.Components = []agencycontract.BillingComponent{
				{ComponentID: "a", StandardQuota: 60, ChargedTotalQuota: 60, CommissionableQuota: 60, SettlementCostQuota: 45, TheoreticalCommissionQuota: 15, CommissionEligible: true},
				{ComponentID: "A", StandardQuota: 60, ChargedTotalQuota: 60, CommissionableQuota: 60, SettlementCostQuota: 45, TheoreticalCommissionQuota: 15, CommissionEligible: true},
			}
			result, err := AgencyCommitWalletCharge(input, "")
			require.NoError(t, err)
			require.Len(t, result.Components, 2)
			assert.Equal(t, "A", result.Components[0].ComponentID)
			assert.Equal(t, "a", result.Components[1].ComponentID)
			assert.Equal(t, "a", input.Components[0].ComponentID, "caller-owned ordering must not be mutated")
			input.Components[0], input.Components[1] = input.Components[1], input.Components[0]
			replay, err := AgencyCommitWalletCharge(input, "")
			require.NoError(t, err)
			assert.Equal(t, result, replay, "equivalent component order has one financial operation")
			var rows []AgencyChargeComponent
			require.NoError(t, db.Where("charge_id = ?", input.FinancialChargeID).Find(&rows).Error)
			require.Len(t, rows, 2)
			assert.NotEqual(t, rows[0].ComponentKey, rows[1].ComponentKey)
		})
	}
}

func TestAgencyComponentInvalidBasisRollsBackWalletAndJournal(t *testing.T) {
	db, user, input := agencyComponentSettlementFixture(t, "sqlite")
	input.TheoreticalCommissionQuota = 31
	_, err := AgencyCommitWalletCharge(input, "")
	require.Error(t, err)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 50, user.Quota, "failed settlement must retain the accepted reservation")
	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", input.FinancialChargeID).First(&journal).Error)
	assert.Equal(t, "reserved", journal.Status)
	for _, table := range []any{&AgencyChargeComponent{}, &AgencyBillingOperation{}} {
		var count int64
		query := db.Model(table).Where("charge_id = ?", input.FinancialChargeID)
		if _, ok := table.(*AgencyBillingOperation); ok {
			query = query.Where("operation = ?", "finalize")
		}
		require.NoError(t, query.Count(&count).Error)
		assert.Zero(t, count)
	}
}

func TestAgencyComponentProducerRolloutKeepsLegacyByDefault(t *testing.T) {
	db, _, input := agencyComponentSettlementFixture(t, "sqlite")
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "false")
	input.ChargedTotalQuota, input.CommissionableQuota, input.NoncommissionableQuota = 120, 120, 0
	result, err := AgencyCommitWalletCharge(input, "")
	require.NoError(t, err)
	assert.Equal(t, agencycontract.SchemaVersion, result.SchemaVersion)
	assert.Empty(t, result.Components)
	var count int64
	require.NoError(t, db.Model(&AgencyChargeComponent{}).Where("charge_id = ?", input.FinancialChargeID).Count(&count).Error)
	assert.Zero(t, count)
}
