package model

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// This separate DB represents the payment provider's durable idempotency store.
// Rebuilding the adapter does not erase receipts or allow another payment.
type cnSimulatedPayment struct {
	Identity   string `gorm:"primaryKey;size:64"`
	IntentJSON string
	ResultJSON string
}

type cnSimulatedProvider struct {
	db          *gorm.DB
	status      string
	afterSubmit func(CNRefundIntent)
}

func (p *cnSimulatedProvider) Name() string { return "simulated-cn-refund-v1" }
func (p *cnSimulatedProvider) Submit(ctx context.Context, i CNRefundIntent) (CNRefundProviderResult, error) {
	raw, err := common.Marshal(i)
	if err != nil {
		return CNRefundProviderResult{}, err
	}
	result := CNRefundProviderResult{Intent: i, Status: p.status, ReceiptID: CNIdentity("simulated", i.Identity), Evidence: "simulated signed receipt for " + i.Identity, ConfirmedAt: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)}
	response, err := common.Marshal(result)
	if err != nil {
		return result, err
	}
	err = p.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&cnSimulatedPayment{Identity: i.Identity, IntentJSON: string(raw), ResultJSON: string(response)}).Error
	if err != nil {
		return result, err
	}
	if p.afterSubmit != nil {
		p.afterSubmit(i)
	}
	return p.Query(ctx, i)
}
func (p *cnSimulatedProvider) Query(ctx context.Context, i CNRefundIntent) (CNRefundProviderResult, error) {
	var row cnSimulatedPayment
	if err := p.db.WithContext(ctx).First(&row, "identity = ?", i.Identity).Error; err != nil {
		return CNRefundProviderResult{}, err
	}
	raw, err := common.Marshal(i)
	if err != nil || row.IntentJSON != string(raw) {
		return CNRefundProviderResult{}, ErrCNRefundConflict
	}
	var result CNRefundProviderResult
	err = common.UnmarshalJsonStr(row.ResultJSON, &result)
	return result, err
}

func cnRefundFixture(t *testing.T, dialect string) (*CNRefundService, CNRefundReserveRequest) {
	t.Helper()
	db := agencyDialectDB(t, dialect)
	if db == nil {
		t.Skip("isolated external database not configured")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	if dialect == "sqlite" {
		pool.SetMaxOpenConns(1)
	}
	db.Logger = db.Logger.LogMode(logger.Silent)
	oldDB, oldRedis, oldBatch := DB, common.RedisEnabled, common.BatchUpdateEnabled
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
	common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
	t.Cleanup(func() {
		DB, common.RedisEnabled, common.BatchUpdateEnabled = oldDB, oldRedis, oldBatch
		common.SetDatabaseTypes(oldMain, oldLog)
		_ = pool.Close()
	})
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, MigrateAgency(db))
	require.NoError(t, MigrateCNFinance(db, "refund-gateway"))
	require.NoError(t, MigrateCNRefund(db))
	require.NoError(t, MigrateCNRefund(db)) // repeat additive migration
	id := "r" + common.GetUUID()[:15]
	u := User{Username: id, AffCode: id, BillingMode: AgencyDurableBillingMode, FundingVersion: 1, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&u).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return creditTopUpQuotaWithFunding(tx, u.Id, 100, nil, "payment", id, &TopupFundingSnapshot{Payment: &TopupPaymentSnapshot{ActualMoney: "80", CurrencyCode: "CNY", PaymentReference: "provider-payment-" + id}})
	}))
	_, err = TryReserveUserQuotaAndAgency(u.Id, 25, id+"-charge", 25)
	require.NoError(t, err)
	event := agencycontract.BillingEvent{EventID: id + "-final", EventType: "agency.billing_finalized", FinancialChargeID: id + "-charge", UserID: int64(u.Id), JournalRevision: 1, OperationID: id + "-final"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return CaptureCNAgencyFundingTx(tx, event) }))
	var a AgencyFundingAccount
	var l AgencyFundingLot
	require.NoError(t, db.Where("user_id = ?", u.Id).First(&a).Error)
	require.NoError(t, db.Where("user_id = ? AND source_id = ?", u.Id, id).First(&l).Error)
	s := &CNRefundService{DB: db, TenantID: "refund-tenant", GatewayID: "refund-gateway"}
	r := CNRefundReserveRequest{TenantID: s.TenantID, GatewayID: s.GatewayID, OperationID: id, ApprovalID: "approval-" + id, ApprovalHash: strings.Repeat("a", 64), OriginalPaymentID: CNIdentity("agency-payment", id), LotID: l.ID, UserID: int64(u.Id), Amount: "60", Currency: "CNY", ExpectedFundingVersion: a.Version, ExpectedLotVersion: l.Version}
	return s, r
}

func cnRefundProviderFixture(t *testing.T) *cnSimulatedProvider {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "provider.db")), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&cnSimulatedPayment{}))
	return &cnSimulatedProvider{db: db, status: "succeeded"}
}

func TestCNRefundAtomicWorkflowAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s, r := cnRefundFixture(t, dialect)
			ctx := context.Background()
			balance, err := s.Balance(ctx, r.TenantID, r.GatewayID, r.LotID)
			require.NoError(t, err)
			assert.Equal(t, "60", balance.AvailableRefundable)
			for _, amount := range []string{"0", "-1", "60.001", "060", "6e1", "60.0"} {
				bad := r
				bad.Amount = amount
				_, err := s.Reserve(ctx, bad)
				assert.ErrorIs(t, err, ErrCNRefundInvalid)
			}
			bad := r
			bad.Currency = "USD"
			_, err = s.Reserve(ctx, bad)
			assert.ErrorIs(t, err, ErrCNRefundInvalid)
			bad = r
			bad.ExpectedFundingVersion++
			_, err = s.Reserve(ctx, bad)
			assert.ErrorIs(t, err, ErrCNRefundStale)
			bad = r
			bad.OriginalPaymentID = strings.Repeat("b", 64)
			_, err = s.Reserve(ctx, bad)
			assert.ErrorIs(t, err, ErrCNRefundConflict)
			bad = r
			bad.Amount = "61"
			_, err = s.Reserve(ctx, bad)
			assert.ErrorIs(t, err, ErrCNRefundInsufficient)
			reserve, err := s.Reserve(ctx, r)
			require.NoError(t, err)
			assert.EqualValues(t, 75, reserve.HeldQuota)
			assert.True(t, reserve.ChargingRespectsReservation)
			replay, err := s.Reserve(ctx, r)
			require.NoError(t, err)
			assert.Equal(t, reserve, replay)
			bad = r
			bad.Amount = "59.2"
			_, err = s.Reserve(ctx, bad)
			assert.ErrorIs(t, err, ErrCNRefundConflict)
			_, err = TryReserveUserQuotaAndAgency(int(r.UserID), 1, "after-hold", 1)
			assert.ErrorIs(t, err, ErrInsufficientAgencyWalletQuota)
			err = ApplyAgencyQuotaDelta(r.UserID, -1, "quota_debit")
			assert.ErrorIs(t, err, ErrInsufficientAgencyWalletQuota)
			_, err = AdjustUserQuota(int(r.UserID), common.RoleRootUser, "override", 100)
			assert.ErrorIs(t, err, ErrAgencyFundingUnavailable)
			err = s.DB.Transaction(func(tx *gorm.DB) error { return cnRefundCheckNativeReversalTx(tx, r.UserID, r.OperationID) })
			assert.ErrorIs(t, err, ErrCNRefundConflict)
			command := CNRefundCommand{AttemptID: "execute-1", TenantID: r.TenantID, GatewayID: r.GatewayID, OperationID: r.OperationID, ApprovalID: r.ApprovalID, ApprovalHash: r.ApprovalHash, ExpectedVersion: reserve.Version}
			_, err = s.Execute(ctx, command, false)
			assert.ErrorIs(t, err, ErrCNRefundUnavailable)
			provider := cnRefundProviderFixture(t)
			provider.status = "unknown_reconcile"
			// The Gateway DB has one SQLite connection. A committed read here
			// would deadlock if provider I/O were inside the money transaction.
			provider.afterSubmit = func(i CNRefundIntent) {
				latest, err := s.Get(ctx, r.TenantID, r.GatewayID, r.OperationID)
				require.NoError(t, err)
				assert.Equal(t, "executing", latest.Status)
				assert.Equal(t, i.Identity, latest.ProviderIdentity)
			}
			s.Provider = provider
			unknown, err := s.Execute(ctx, command, false)
			require.NoError(t, err)
			assert.Equal(t, "unknown_reconcile", unknown.Status)
			assert.EqualValues(t, 75, unknown.HeldQuota)
			original, err := s.Execute(ctx, command, false)
			require.NoError(t, err)
			assert.Equal(t, unknown, original)
			// Reconstruct service and provider: recovery reads durable identity.
			s = &CNRefundService{DB: s.DB, TenantID: r.TenantID, GatewayID: r.GatewayID, Provider: &cnSimulatedProvider{db: provider.db}}
			final := CNRefundProviderResult{Intent: CNRefundIntent{Identity: unknown.ProviderIdentity, OriginalPaymentID: r.OriginalPaymentID, PaymentReference: "provider-payment-" + r.OperationID, Amount: r.Amount, Currency: r.Currency}, Status: "succeeded", ReceiptID: "sandbox-final", Evidence: "sandbox verified final", ConfirmedAt: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)}
			raw, err := common.Marshal(final)
			require.NoError(t, err)
			require.NoError(t, provider.db.Model(&cnSimulatedPayment{}).Where("identity = ?", unknown.ProviderIdentity).Update("result_json", string(raw)).Error)
			command.ExpectedVersion = unknown.Version
			command.AttemptID = "query-final"
			paid, err := s.Execute(ctx, command, true)
			require.NoError(t, err)
			assert.Equal(t, "succeeded", paid.Status)
			assert.Zero(t, paid.HeldQuota)
			assert.NotEmpty(t, paid.CanonicalFactID)
			again, err := s.Execute(ctx, command, true)
			require.NoError(t, err)
			assert.Equal(t, paid, again)
			originalReserve, err := s.Reserve(ctx, r)
			require.NoError(t, err)
			assert.Equal(t, reserve, originalReserve)
			var row CNFinanceOutbox
			require.NoError(t, s.DB.Where("fact_id = ?", paid.CanonicalFactID).First(&row).Error)
			var fact CNFact
			require.NoError(t, common.UnmarshalJsonStr(row.Payload, &fact))
			assert.Equal(t, "payment_refund", fact.PostingRole)
			assert.Equal(t, "60", fact.Cumulative.Money.Gross)
			assert.Nil(t, fact.Recognition)
			assert.Nil(t, fact.Cumulative.Quota)
			var count int64
			require.NoError(t, s.DB.Model(&CNFinanceOutbox{}).Where("fact_id = ?", paid.CanonicalFactID).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			require.NoError(t, provider.db.Model(&cnSimulatedPayment{}).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			var lot AgencyFundingLot
			var user User
			require.NoError(t, s.DB.First(&lot, r.LotID).Error)
			require.NoError(t, s.DB.First(&user, r.UserID).Error)
			assert.EqualValues(t, 25, lot.PaidConsumed)
			assert.EqualValues(t, 75, lot.PaidRevoked)
			assert.Zero(t, lot.PaidReserved)
			assert.Zero(t, user.Quota)
		})
	}
}

func TestCNRefundFailureRollbackAndConcurrentSpend(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s, r := cnRefundFixture(t, dialect)
			ctx := context.Background()
			// Fault after the native balance writes must roll back all of them.
			callback := "cn-refund-test-rollback"
			require.NoError(t, s.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "cn_refund_mutations" {
					tx.AddError(errors.New("injected write failure"))
				}
			}))
			_, err := s.Reserve(ctx, r)
			require.Error(t, err)
			require.NoError(t, s.DB.Callback().Create().Remove(callback))
			balance, err := s.Balance(ctx, r.TenantID, r.GatewayID, r.LotID)
			require.NoError(t, err)
			assert.Equal(t, "60", balance.AvailableRefundable)
			reserve, err := s.Reserve(ctx, r)
			require.NoError(t, err)
			provider := cnRefundProviderFixture(t)
			provider.status = "definitive_failed"
			s.Provider = provider
			c := CNRefundCommand{AttemptID: "execute-failure", TenantID: r.TenantID, GatewayID: r.GatewayID, OperationID: r.OperationID, ApprovalID: r.ApprovalID, ApprovalHash: r.ApprovalHash, ExpectedVersion: reserve.Version}
			released, err := s.Execute(ctx, c, false)
			require.NoError(t, err)
			assert.Equal(t, "failed_released", released.Status)
			assert.Zero(t, released.HeldQuota)
			var user User
			require.NoError(t, s.DB.First(&user, r.UserID).Error)
			assert.Equal(t, 75, user.Quota)
			var count int64
			require.NoError(t, s.DB.Model(&CNFinanceOutbox{}).Where("fact_id = ?", CNIdentity("cn-refund-cash", reserve.ReservationID)).Count(&count).Error)
			assert.Zero(t, count)
			// A second payment has two competing reserve/spend transactions.
			s, r = cnRefundFixture(t, dialect)
			start := make(chan struct{})
			var wg sync.WaitGroup
			var reserveErr, spendErr error
			wg.Add(2)
			go func() { defer wg.Done(); <-start; _, reserveErr = s.Reserve(ctx, r) }()
			go func() {
				defer wg.Done()
				<-start
				spendErr = retryAgencyTransaction(func() error {
					_, err := TryReserveUserQuotaAndAgency(int(r.UserID), 1, fmt.Sprintf("race-%d", r.UserID), 1)
					return err
				})
			}()
			close(start)
			wg.Wait()
			assert.False(t, reserveErr == nil && spendErr == nil, "refund and spend cannot both consume the held last quota")
			var lot AgencyFundingLot
			require.NoError(t, s.DB.First(&lot, r.LotID).Error)
			assert.EqualValues(t, 100, lot.PaidAvailable+lot.PaidReserved+lot.PaidConsumed+lot.PaidRevoked)
		})
	}
}
