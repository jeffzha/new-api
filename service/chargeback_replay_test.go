package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A successful payment refund is an immutable external command. Retrying the
// same refund ID must not append another commission reversal, even though the
// funding model returns the original affected charges for inspection.
func TestFundingReversalReplayDoesNotDuplicateCommissionCompensation(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	db, err := gorm.Open(sqlite.Open("file:agency-chargeback-replay?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, model.MigrateAgency(db))
	agencyID, bindingID := int64(73), int64(730)
	user := model.User{Username: "chargeback-replay", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 0}
	require.NoError(t, db.Create(&user).Error)
	account := model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1, PaidAvailable: 0, MoneySeq: 2}
	require.NoError(t, db.Create(&account).Error)
	lot := model.AgencyFundingLot{UserID: int64(user.Id), SourceKind: "payment", SourceID: "replay-topup", CompletionSource: "payment_callback", PaidInitial: 100, PaidConsumed: 100, MoneySeq: 1, Version: 1, CreatedAt: 1}
	require.NoError(t, db.Create(&lot).Error)
	require.NoError(t, db.Create(&model.AgencyTopupFact{SourceOperationID: "replay-topup", UserID: int64(user.Id), AgencyID: &agencyID, BindingID: &bindingID, CreditedQuota: 100, PaidQuota: 100, PaymentStatus: "success", CurrencyCode: "CNY", CompletionSource: "payment_callback", OccurredAtMS: 1}).Error)
	charge := model.AgencyBillingJournal{ChargeID: "replay-charge", SegmentNo: 0, UserID: int64(user.Id), Status: "finalized", BusinessStatus: "success", DeliveryStatus: "done", PricingSnapshot: "{}", BillingBasis: "test", ChargedTotalQuota: 100, CommissionableQuota: 100, CommissionAmountMicros: 100, CurrencyCode: "CNY", Revision: 1, Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1}
	require.NoError(t, db.Create(&charge).Error)
	original := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: "replay-original-event", EventType: "agency.billing_finalized", FinancialChargeID: charge.ChargeID, OperationID: "replay-finalize", SegmentNo: 0, JournalRevision: 1, EventCount: 1, UserID: int64(user.Id), AgencyID: &agencyID, BindingID: &bindingID, OriginModelName: "model", BusinessStatus: "success", BillingStatus: "finalized", FinancialFinal: true, CurrencyCode: "CNY", ChargedTotalQuota: 100, CommissionableQuota: 100, CommissionAmountMicros: 100, CommissionEligible: true, OccurredAtMS: time.Now().UnixMilli()}
	payload, err := common.Marshal(original)
	require.NoError(t, err)
	hash, err := agencycontract.CanonicalHash(original)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyBillingOperation{ChargeID: charge.ChargeID, OperationID: original.OperationID, SegmentNo: 0, Revision: 1, Operation: "finalize", InputHash: hash, CommittedResult: string(payload), EventCount: 1, CreatedAtMS: original.OccurredAtMS}).Error)
	require.NoError(t, db.Create(&model.AgencyBillingOutbox{EventID: original.EventID, OperationID: original.OperationID, EventIndex: 0, EventCount: 1, EventKind: original.EventType, UserID: original.UserID, Payload: string(payload), PayloadHash: hash, SchemaVersion: original.SchemaVersion, CreatedAtMS: original.OccurredAtMS}).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAllocation{ChargeID: charge.ChargeID, SegmentNo: 0, ComponentID: "default", UserID: int64(user.Id), LotID: lot.ID, Consumed: 100, Version: 1}).Error)

	command := agencyFundingReverseCommand{OriginalOperationID: "replay-topup", RefundID: "replay-refund", UserID: int64(user.Id), RefundQuota: 25, CurrencyCode: "CNY", Reason: "payment_chargeback"}
	require.NoError(t, reverseAgencyTopup(command, 25))
	var first model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", charge.ChargeID).First(&first).Error)
	var firstCommission, firstReversals, firstOutbox int64
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", charge.ChargeID, "reverse").Count(&firstCommission).Error)
	require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Where("refund_id = ?", command.RefundID).Count(&firstReversals).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("event_kind = ?", "agency.billing_reversed").Count(&firstOutbox).Error)
	assert.Equal(t, int64(1), firstCommission)
	assert.Equal(t, int64(1), firstReversals)
	assert.Equal(t, int64(1), firstOutbox)
	assert.Equal(t, int64(25), first.ReversedQuota)
	assert.Equal(t, int64(25), first.ReversedCommissionQuota)

	// Same external refund ID is a successful no-op, including its complete
	// compensation output. All financial rows remain byte-for-byte counted.
	require.NoError(t, reverseAgencyTopup(command, 25))
	var second model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", charge.ChargeID).First(&second).Error)
	var secondCommission, secondReversals, secondOutbox int64
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", charge.ChargeID, "reverse").Count(&secondCommission).Error)
	require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Where("refund_id = ?", command.RefundID).Count(&secondReversals).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("event_kind = ?", "agency.billing_reversed").Count(&secondOutbox).Error)
	assert.Equal(t, first, second)
	assert.Equal(t, firstCommission, secondCommission)
	assert.Equal(t, firstReversals, secondReversals)
	assert.Equal(t, firstOutbox, secondOutbox)
	var wallet model.User
	var funding model.AgencyFundingAccount
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.First(&funding, user.Id).Error)
	assert.Equal(t, -25, wallet.Quota)
	assert.Equal(t, int64(25), funding.DebtQuota)

	conflict := command
	conflict.RefundQuota = 99
	require.ErrorIs(t, reverseAgencyTopup(conflict, 99), model.ErrAgencyFundingReversalConflict)
	conflict = command
	conflict.OriginalEventID = "agency-topup-other-payment"
	require.ErrorIs(t, reverseAgencyTopup(conflict, 25), model.ErrAgencyFundingReversalConflict)
	conflict.OriginalEventID = "unproven-reference"
	require.ErrorIs(t, reverseAgencyTopup(conflict, 25), model.ErrAgencyFundingReversalConflict)
	// The two reference forms may identify the same source, but must not
	// silently disagree about which original payment is being revoked.
	command.OriginalEventID = "agency-topup-replay-topup"
	require.NoError(t, reverseAgencyTopup(command, 25))
	var finalReversals int64
	require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Where("refund_id = ?", command.RefundID).Count(&finalReversals).Error)
	assert.Equal(t, firstReversals, finalReversals)
}
