package model

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrCNRefundInvalid      = errors.New("invalid_payload")
	ErrCNRefundScope        = errors.New("scope_mismatch")
	ErrCNRefundStale        = errors.New("stale_version")
	ErrCNRefundConflict     = errors.New("refund_conflict")
	ErrCNRefundUnsupported  = errors.New("unsupported_funding")
	ErrCNRefundInsufficient = errors.New("insufficient_refundable")
	ErrCNRefundNonintegral  = errors.New("nonintegral_refund_quota")
	ErrCNRefundUnavailable  = errors.New("execution_unavailable")
	cnRefundID              = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	cnRefundHash            = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CNRefundReservation owns a single native payment's refund lane. Money is
// removed from native spendable balances, not merely recorded in this table.
type CNRefundReservation struct {
	Key                    string `gorm:"size:64;primaryKey"`
	PaymentKey             string `gorm:"size:64;not null;index"`
	SourceOperationID      string `gorm:"size:128;not null;index"`
	PaymentReference       string `gorm:"size:191;not null"`
	UserID                 int64  `gorm:"not null;index"`
	LotID                  int64  `gorm:"not null"`
	RequestJSON            string `gorm:"type:text;not null"`
	RequestHash            string `gorm:"size:64;not null"`
	Status                 string `gorm:"size:32;not null"`
	Version                int64  `gorm:"not null"`
	HeldQuota              int64  `gorm:"not null"`
	ReservedFundingVersion int64  `gorm:"not null"`
	ReservedLotVersion     int64  `gorm:"not null"`
	ProviderName           string `gorm:"size:128"`
	ProviderIdentity       string `gorm:"size:64"`
	ProviderReceiptID      string `gorm:"size:191"`
	ProviderEvidence       string `gorm:"type:text"`
	CanonicalFactID        string `gorm:"size:64"`
	CreatedAt              int64  `gorm:"not null"`
	UpdatedAt              int64  `gorm:"not null"`
}

type CNRefundMutation struct {
	Key            string `gorm:"size:64;primaryKey"`
	ReservationKey string `gorm:"size:64;not null;index"`
	RequestHash    string `gorm:"size:64;not null"`
	ResponseJSON   string `gorm:"type:text;not null"`
}

type CNRefundReserveRequest struct {
	TenantID               string `json:"tenantId"`
	GatewayID              string `json:"gatewayId"`
	OperationID            string `json:"operationId"`
	ApprovalID             string `json:"approvalId"`
	ApprovalHash           string `json:"approvalHash"`
	OriginalPaymentID      string `json:"originalPaymentId"`
	LotID                  int64  `json:"lotId"`
	UserID                 int64  `json:"userId"`
	Amount                 string `json:"amount"`
	Currency               string `json:"currency"`
	ExpectedFundingVersion int64  `json:"expectedFundingVersion"`
	ExpectedLotVersion     int64  `json:"expectedLotVersion"`
}

type CNRefundCommand struct {
	AttemptID       string `json:"attemptId"`
	TenantID        string `json:"tenantId"`
	GatewayID       string `json:"gatewayId"`
	OperationID     string `json:"operationId"`
	ApprovalID      string `json:"approvalId"`
	ApprovalHash    string `json:"approvalHash"`
	ExpectedVersion int64  `json:"expectedVersion"`
}

type CNRefundReceipt struct {
	ReservationID               string `json:"reservationId"`
	OperationID                 string `json:"operationId"`
	TenantID                    string `json:"tenantId"`
	GatewayID                   string `json:"gatewayId"`
	OriginalPaymentID           string `json:"originalPaymentId"`
	LotID                       int64  `json:"lotId"`
	ApprovalID                  string `json:"approvalId"`
	ApprovalHash                string `json:"approvalHash"`
	Amount                      string `json:"amount"`
	Currency                    string `json:"currency"`
	HeldQuota                   int64  `json:"heldQuota"`
	Status                      string `json:"status"`
	Version                     int64  `json:"version"`
	ReservedFundingVersion      int64  `json:"reservedFundingVersion"`
	ReservedLotVersion          int64  `json:"reservedLotVersion"`
	ChargingRespectsReservation bool   `json:"chargingRespectsReservation"`
	ProviderIdentity            string `json:"providerIdentity"`
	ProviderReceiptID           string `json:"providerReceiptId"`
	CanonicalFactID             string `json:"canonicalFactId"`
	PostingPolicy               string `json:"postingPolicy"`
}

type CNRefundBalance struct {
	UserID              int64  `json:"userId"`
	LotID               int64  `json:"lotId"`
	OriginalPaymentID   string `json:"originalPaymentId"`
	Currency            string `json:"currency"`
	AvailableRefundable string `json:"availableRefundable"`
	AvailableQuota      int64  `json:"availableQuota"`
	FundingVersion      int64  `json:"fundingVersion"`
	LotVersion          int64  `json:"lotVersion"`
}

// CNRefundProvider is a trusted server-side adapter. Submit MUST be idempotent
// for Identity across restarts/concurrent calls. Final responses MUST bind every
// intent field and be irrevocable. Query absence/pending is unknown, not failure.
// Adapters verify provider evidence; HTTP callers cannot supply provider results.
type CNRefundProvider interface {
	Name() string
	Submit(context.Context, CNRefundIntent) (CNRefundProviderResult, error)
	Query(context.Context, CNRefundIntent) (CNRefundProviderResult, error)
}

type CNRefundIntent struct {
	Identity          string
	OriginalPaymentID string
	PaymentReference  string
	Amount            string
	Currency          string
}

type CNRefundProviderResult struct {
	Intent      CNRefundIntent
	Status      string // succeeded, definitive_failed, or unknown_reconcile
	ReceiptID   string
	Evidence    string // immutable verified provider evidence, not user assertion
	ConfirmedAt time.Time
}

type CNRefundService struct {
	DB        *gorm.DB
	TenantID  string
	GatewayID string
	Provider  CNRefundProvider
}

func MigrateCNRefund(db *gorm.DB) error {
	return db.AutoMigrate(&CNRefundReservation{}, &CNRefundMutation{})
}

func (s *CNRefundService) scope(tenant, gateway, operation string) error {
	if s == nil || s.DB == nil || s.TenantID == "" || s.GatewayID == "" || tenant != s.TenantID || gateway != s.GatewayID {
		return ErrCNRefundScope
	}
	if !cnRefundID.MatchString(operation) {
		return ErrCNRefundInvalid
	}
	return nil
}

func (s *CNRefundService) key(operation string) string {
	return CNIdentity("cn-refund", s.TenantID, s.GatewayID, operation)
}

func cnRefundReceipt(row CNRefundReservation) (CNRefundReceipt, error) {
	var r CNRefundReserveRequest
	if err := common.UnmarshalJsonStr(row.RequestJSON, &r); err != nil {
		return CNRefundReceipt{}, err
	}
	return CNRefundReceipt{ReservationID: row.Key, OperationID: r.OperationID, TenantID: r.TenantID, GatewayID: r.GatewayID,
		OriginalPaymentID: r.OriginalPaymentID, LotID: r.LotID, ApprovalID: r.ApprovalID, ApprovalHash: r.ApprovalHash,
		Amount: r.Amount, Currency: r.Currency, HeldQuota: row.HeldQuota, Status: row.Status, Version: row.Version,
		ReservedFundingVersion: row.ReservedFundingVersion, ReservedLotVersion: row.ReservedLotVersion,
		ChargingRespectsReservation: true, ProviderIdentity: row.ProviderIdentity, ProviderReceiptID: row.ProviderReceiptID,
		CanonicalFactID: row.CanonicalFactID, PostingPolicy: "receipt_only_until_canonical_fact"}, nil
}

// Locks follow native consumption order; do not lock a refund before its user.
func cnRefundLockFunding(tx *gorm.DB, userID, lotID int64) (User, AgencyFundingAccount, AgencyFundingLot, error) {
	var u User
	var a AgencyFundingAccount
	var l AgencyFundingLot
	if err := lockForUpdate(tx).First(&u, userID).Error; err != nil {
		return u, a, l, err
	}
	if u.BillingMode != AgencyDurableBillingMode {
		return u, a, l, ErrCNRefundUnsupported
	}
	if err := lockForUpdate(tx).Where("user_id = ?", userID).First(&a).Error; err != nil {
		return u, a, l, err
	}
	if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", lotID, userID).First(&l).Error; err != nil {
		return u, a, l, err
	}
	return u, a, l, nil
}

func cnRefundPurchase(tx *gorm.DB, u User, a AgencyFundingAccount, l AgencyFundingLot) (AgencyTopupFact, decimal.Decimal, error) {
	var p AgencyTopupFact
	if err := lockForUpdate(tx).Where("source_operation_id = ? AND user_id = ?", l.SourceID, l.UserID).First(&p).Error; err != nil {
		return p, decimal.Zero, err
	}
	value, err := cnDecimal(p.ActualMoney)
	if err != nil || !value.IsPositive() || p.CurrencyCode != "CNY" || p.PaymentReference == "" ||
		p.PaymentStatus != "success" || p.RefundedQuota != 0 || p.PaidQuota != l.PaidInitial || p.FundingSource != l.SourceKind ||
		p.PaidQuota <= 0 || l.BonusInitial != 0 || l.PaidDebtRepaid != 0 ||
		agencyFundingLotIsLegacyUnknown(l) || a.ReconcileBlocked || a.DebtQuota != 0 ||
		int64(u.Quota) != a.PaidAvailable+a.NonpaidAvailable-a.DebtQuota {
		return p, decimal.Zero, ErrCNRefundUnsupported
	}
	return p, value, nil
}

// Refund cash follows the SAME frozen recognition amounts as the exporter,
// including its lot residual policy. Never reprice remaining quota independently.
func cnRefundUnconsumedCashTx(tx *gorm.DB, l AgencyFundingLot, paid decimal.Decimal) (decimal.Decimal, error) {
	var allocations []AgencyFundingAllocation
	if err := lockForUpdate(tx).Where("lot_id = ? AND user_id = ?", l.ID, l.UserID).Find(&allocations).Error; err != nil {
		return decimal.Zero, err
	}
	consumed, recognized := int64(0), decimal.Zero
	for _, a := range allocations {
		quota, _, _, err := agencyFundingAllocationActiveParts(a)
		if err != nil {
			return decimal.Zero, err
		}
		lotID := CNIdentity("agency-lot", l.SourceKind, l.SourceID, "paid")
		allocationID := CNIdentity("agency-allocation", a.ChargeID, strconv.Itoa(a.SegmentNo), a.ComponentID, lotID)
		var row CNFinanceOutbox
		err = lockForUpdate(tx).Where("chain_key = ?", CNIdentity("funding_allocation", allocationID, "0")).Order("revision DESC").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) && quota == 0 {
			continue
		}
		if err != nil {
			return decimal.Zero, ErrCNRefundUnsupported
		}
		var f CNFact
		if err = common.UnmarshalJsonStr(row.Payload, &f); err != nil {
			return decimal.Zero, err
		}
		if f.Cumulative.Quota == nil || *f.Cumulative.Quota != strconv.FormatInt(quota, 10) || f.Cumulative.Money == nil || f.Cumulative.Money.Currency != "CNY" || f.Quality != "verified_payment" {
			return decimal.Zero, ErrCNRefundUnsupported
		}
		value, err := cnDecimal(f.Cumulative.Money.Gross)
		if err != nil || value.IsNegative() || quota < 0 || quota > l.PaidInitial-consumed {
			return decimal.Zero, ErrCNRefundUnsupported
		}
		consumed += quota
		recognized = recognized.Add(value)
	}
	if consumed != l.PaidConsumed || recognized.GreaterThan(paid) {
		return decimal.Zero, ErrCNRefundUnsupported
	}
	var refunds []CNRefundReservation
	if err := lockForUpdate(tx).Where("user_id = ? AND source_operation_id = ?", l.UserID, l.SourceID).Find(&refunds).Error; err != nil {
		return decimal.Zero, err
	}
	refunded, held, revokedQuota, heldQuota := decimal.Zero, decimal.Zero, int64(0), int64(0)
	for _, row := range refunds {
		var r CNRefundReserveRequest
		if err := common.UnmarshalJsonStr(row.RequestJSON, &r); err != nil {
			return decimal.Zero, err
		}
		amount, err := cnDecimal(r.Amount)
		if err != nil || !amount.IsPositive() || r.Currency != "CNY" {
			return decimal.Zero, ErrCNRefundUnsupported
		}
		switch row.Status {
		case "succeeded":
			refunded = refunded.Add(amount)
			quota := amount.Mul(decimal.NewFromInt(l.PaidInitial)).DivRound(paid, 0)
			if quota.GreaterThan(decimal.NewFromInt(l.PaidInitial - revokedQuota)) {
				return decimal.Zero, ErrCNRefundUnsupported
			}
			revokedQuota += quota.IntPart()
		case "reserved", "executing", "unknown_reconcile":
			held = held.Add(amount)
			if row.HeldQuota <= 0 || row.HeldQuota > l.PaidInitial-heldQuota {
				return decimal.Zero, ErrCNRefundUnsupported
			}
			heldQuota += row.HeldQuota
		case "failed_released":
		default:
			return decimal.Zero, ErrCNRefundUnsupported
		}
	}
	available := paid.Sub(recognized).Sub(refunded).Sub(held)
	if available.IsNegative() || revokedQuota != l.PaidRevoked || heldQuota != l.PaidReserved ||
		!decimal.NewFromInt(l.PaidAvailable).Add(decimal.NewFromInt(l.PaidReserved)).Add(decimal.NewFromInt(l.PaidConsumed)).Add(decimal.NewFromInt(l.PaidRevoked)).Equal(decimal.NewFromInt(l.PaidInitial)) {
		return decimal.Zero, ErrCNRefundUnsupported
	}
	if err := cnRefundCheckValuePoolTx(tx, l, available, len(refunds) > 0); err != nil {
		return decimal.Zero, err
	}
	return available, nil
}

func (s *CNRefundService) Balance(ctx context.Context, tenant, gateway string, lotID int64) (CNRefundBalance, error) {
	var result CNRefundBalance
	if err := s.scope(tenant, gateway, "balance"); err != nil {
		return result, err
	}
	if lotID <= 0 || !CNFinanceCaptureEnabled() || common.RedisEnabled || common.BatchUpdateEnabled {
		return result, ErrCNRefundUnsupported
	}
	err := retryAgencyTransaction(func() error {
		return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var l AgencyFundingLot
			if err := tx.Select("id,user_id").First(&l, lotID).Error; err != nil {
				return err
			}
			u, a, l, err := cnRefundLockFunding(tx, l.UserID, lotID)
			if err != nil {
				return err
			}
			p, money, err := cnRefundPurchase(tx, u, a, l)
			if err != nil {
				return err
			}
			cash, err := cnRefundUnconsumedCashTx(tx, l, money)
			if err != nil {
				return err
			}
			result = CNRefundBalance{UserID: l.UserID, LotID: l.ID, OriginalPaymentID: CNIdentity("agency-payment", p.SourceOperationID), Currency: p.CurrencyCode,
				AvailableRefundable: cash.String(),
				AvailableQuota:      l.PaidAvailable, FundingVersion: a.Version, LotVersion: l.Version}
			return nil
		})
	})
	return result, err
}

func (s *CNRefundService) Reserve(ctx context.Context, r CNRefundReserveRequest) (CNRefundReceipt, error) {
	var result CNRefundReceipt
	if err := s.scope(r.TenantID, r.GatewayID, r.OperationID); err != nil {
		return result, err
	}
	money, err := cnDecimal(r.Amount)
	if err != nil || !money.IsPositive() || money.Exponent() < -2 || r.Currency != "CNY" || r.UserID <= 0 || r.LotID <= 0 ||
		r.ExpectedFundingVersion <= 0 || r.ExpectedLotVersion <= 0 || !cnRefundID.MatchString(r.ApprovalID) || !cnRefundHash.MatchString(r.ApprovalHash) || !cnRefundHash.MatchString(r.OriginalPaymentID) {
		return result, ErrCNRefundInvalid
	}
	if !CNFinanceCaptureEnabled() || common.RedisEnabled || common.BatchUpdateEnabled {
		return result, ErrCNRefundUnsupported
	}
	raw, err := common.Marshal(r)
	if err != nil {
		return result, err
	}
	key, hash := s.key(r.OperationID), CNIdentity(string(raw))
	err = retryAgencyTransaction(func() error {
		return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			u, a, l, err := cnRefundLockFunding(tx, r.UserID, r.LotID)
			if err != nil {
				return err
			}
			var mutation CNRefundMutation
			if err = lockForUpdate(tx).Where(map[string]any{"key": CNIdentity(key, "reserve")}).First(&mutation).Error; err == nil {
				if mutation.RequestHash != hash {
					return ErrCNRefundConflict
				}
				return common.UnmarshalJsonStr(mutation.ResponseJSON, &result)
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if a.Version != r.ExpectedFundingVersion || l.Version != r.ExpectedLotVersion {
				return ErrCNRefundStale
			}
			p, paid, err := cnRefundPurchase(tx, u, a, l)
			if err != nil {
				return err
			}
			cash, err := cnRefundUnconsumedCashTx(tx, l, paid)
			if err != nil {
				return err
			}
			if money.GreaterThan(cash) {
				return ErrCNRefundInsufficient
			}
			if r.OriginalPaymentID != CNIdentity("agency-payment", p.SourceOperationID) {
				return ErrCNRefundConflict
			}
			// Require exact integral quota; Div rounding must never authorize value.
			numerator := money.Mul(decimal.NewFromInt(l.PaidInitial))
			quotaDecimal := numerator.DivRound(paid, 0)
			if !quotaDecimal.Mul(paid).Equal(numerator) {
				return ErrCNRefundNonintegral
			}
			if !quotaDecimal.IsPositive() || quotaDecimal.GreaterThan(decimal.NewFromInt(l.PaidAvailable)) {
				return ErrCNRefundInsufficient
			}
			quota := quotaDecimal.IntPart() // bounded by the locked native int64 lot
			if quota > a.PaidAvailable || quota > int64(u.Quota) {
				return ErrCNRefundInsufficient
			}
			paymentKey := CNIdentity("cn-refund-payment", s.GatewayID, r.OriginalPaymentID)
			row := CNRefundReservation{Key: key, PaymentKey: paymentKey, SourceOperationID: p.SourceOperationID, PaymentReference: p.PaymentReference, UserID: r.UserID, LotID: r.LotID,
				RequestJSON: string(raw), RequestHash: hash, Status: "reserved", Version: 1, HeldQuota: quota, ReservedFundingVersion: a.Version + 1,
				ReservedLotVersion: l.Version + 1, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}
			if err = cnRefundReserveValueTx(tx, l, paid, money, quota, key); err != nil {
				return err
			}
			if err = cnRefundMoveHoldTx(tx, &u, &a, &l, quota, "reserve", r.OperationID); err != nil {
				return err
			}
			if err = tx.Create(&row).Error; err != nil {
				return ErrCNRefundConflict
			}
			result, err = cnRefundReceipt(row)
			if err != nil {
				return err
			}
			response, err := common.Marshal(result)
			if err != nil {
				return err
			}
			return tx.Create(&CNRefundMutation{Key: CNIdentity(key, "reserve"), ReservationKey: key, RequestHash: hash, ResponseJSON: string(response)}).Error
		})
	})
	return result, err
}

func (s *CNRefundService) Get(ctx context.Context, tenant, gateway, operation string) (CNRefundReceipt, error) {
	if err := s.scope(tenant, gateway, operation); err != nil {
		return CNRefundReceipt{}, err
	}
	var row CNRefundReservation
	if err := s.DB.WithContext(ctx).Where(map[string]any{"key": s.key(operation)}).First(&row).Error; err != nil {
		return CNRefundReceipt{}, err
	}
	return cnRefundReceipt(row)
}

// The native funding ledger records hold/release/settlement as distinct roles.
// A hold reduces wallet and available quota once; settlement never debits again.
func cnRefundMoveHoldTx(tx *gorm.DB, u *User, a *AgencyFundingAccount, l *AgencyFundingLot, quota int64, action, operation string) error {
	paidBefore, seqBefore := a.PaidAvailable, a.MoneySeq
	delta := int64(0)
	switch action {
	case "reserve":
		delta = -quota
		l.PaidReserved += quota
	case "release":
		delta = quota
		l.PaidReserved -= quota
	case "settle":
		l.PaidReserved -= quota
		l.PaidRevoked += quota
	default:
		return ErrCNRefundInvalid
	}
	if quota <= 0 || l.PaidReserved < 0 || l.PaidAvailable+delta < 0 || a.PaidAvailable+delta < 0 || int64(u.Quota)+delta < 0 ||
		int64(u.Quota)+delta > int64(common.MaxWalletQuota) || a.Version == int64(^uint64(0)>>1) || a.MoneySeq == int64(^uint64(0)>>1) || l.Version == int64(^uint64(0)>>1) {
		return ErrCNRefundInsufficient
	}
	if delta != 0 {
		if err := tx.Model(&User{}).Where("id = ?", u.Id).Update("quota", gorm.Expr("quota + ?", delta)).Error; err != nil {
			return err
		}
	}
	if err := tx.Model(a).Updates(map[string]any{"paid_available": a.PaidAvailable + delta, "version": a.Version + 1, "money_seq": a.MoneySeq + 1, "updated_at": time.Now().Unix()}).Error; err != nil {
		return err
	}
	if err := tx.Model(l).Updates(map[string]any{"paid_available": l.PaidAvailable + delta, "paid_reserved": l.PaidReserved, "paid_revoked": l.PaidRevoked, "version": l.Version + 1}).Error; err != nil {
		return err
	}
	return tx.Create(&AgencyFundingLedger{OperationID: CNIdentity("cn-refund-ledger", strconv.FormatInt(l.UserID, 10), strconv.FormatInt(l.ID, 10), operation, action), EntryNo: 0, UserID: l.UserID, MoneySeq: seqBefore + 1,
		SourceKind: "cn_refund_" + action, LotID: &l.ID, PaidDelta: delta, PaidAfter: paidBefore + delta, NonpaidAfter: a.NonpaidAvailable, DebtAfter: a.DebtQuota, CreatedAtMS: time.Now().UnixMilli()}).Error
}

// Native legacy reversal must not create a competing refund lane for a payment.
func cnRefundCheckNativeReversalTx(tx *gorm.DB, userID int64, source string) error {
	if !tx.Migrator().HasTable(&CNRefundReservation{}) {
		return nil
	}
	var count int64
	if err := tx.Model(&CNRefundReservation{}).Where("user_id = ? AND source_operation_id = ? AND status <> ?", userID, source, "failed_released").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrCNRefundConflict
	}
	return nil
}

func cnRefundCheckSpendTx(tx *gorm.DB, userID, amount, available int64) error {
	if amount <= available || !tx.Migrator().HasTable(&CNRefundReservation{}) {
		return nil
	}
	var count int64
	if err := tx.Model(&CNRefundReservation{}).Where("user_id = ? AND held_quota > 0", userID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrInsufficientAgencyWalletQuota
	}
	return nil
}

func cnRefundCommandKey(key, action, attempt string) string {
	return CNIdentity(key, action, attempt)
}

// V2 payment evidence RecordID is the native sourceOperationId, NOT lot.ID.
// Resolve via the immutable payment chain, then revalidate under native locks.
func (s *CNRefundService) BalanceByPayment(ctx context.Context, tenant, gateway, payment string) (CNRefundBalance, error) {
	if err := s.scope(tenant, gateway, "balance"); err != nil {
		return CNRefundBalance{}, err
	}
	if !cnRefundHash.MatchString(payment) {
		return CNRefundBalance{}, ErrCNRefundInvalid
	}
	var row CNFinanceOutbox
	if err := s.DB.WithContext(ctx).Where("chain_key = ?", CNIdentity("payment", payment, "0")).Order("revision ASC").First(&row).Error; err != nil {
		return CNRefundBalance{}, err
	}
	var f CNFact
	if err := common.UnmarshalJsonStr(row.Payload, &f); err != nil {
		return CNRefundBalance{}, err
	}
	if f.EconomicID != payment || len(f.SourceEvidence) != 1 || f.SourceEvidence[0].RecordType != "agency_topup_fact" {
		return CNRefundBalance{}, ErrCNRefundUnsupported
	}
	var lots []AgencyFundingLot
	if err := s.DB.WithContext(ctx).Where("source_id = ?", f.SourceEvidence[0].RecordID).Limit(2).Find(&lots).Error; err != nil {
		return CNRefundBalance{}, err
	}
	if len(lots) != 1 || CNIdentity("agency-payment", lots[0].SourceID) != payment {
		return CNRefundBalance{}, ErrCNRefundUnsupported
	}
	return s.Balance(ctx, tenant, gateway, lots[0].ID)
}
