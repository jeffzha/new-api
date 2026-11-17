package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrAgencyCreditLimitExceeded = errors.New("agency credit limit exceeded")
	// ErrAgencyCreditInvoiceClosed rejects a collection or write-off on a
	// statement that is already settled or already written off.
	ErrAgencyCreditInvoiceClosed = errors.New("agency credit invoice is not open")
	// ErrAgencyCreditSettlementExceedsDebt rejects a collection that is larger
	// than the outstanding funding debt it must repay. It means usage facts and
	// funding debt have diverged, so finance has to resolve that first.
	ErrAgencyCreditSettlementExceedsDebt = errors.New("agency credit settlement exceeds outstanding debt")
	// ErrAgencyCreditSettlementInvalid rejects a settlement without a positive
	// amount, an operator identity or a payment reference.
	ErrAgencyCreditSettlementInvalid = errors.New("agency credit settlement input is invalid")
)

const (
	AgencyCreditStatusActive    = "active"
	AgencyCreditStatusSuspended = "suspended"

	AgencyCreditInvoiceStatusIssued  = "issued"
	AgencyCreditInvoiceStatusOverdue = "overdue"
	AgencyCreditInvoiceStatusPaid    = "paid"
	AgencyCreditInvoiceStatusBadDebt = "bad_debt"

	AgencyCreditEmailStatusPending  = "pending"
	AgencyCreditEmailStatusSent     = "sent"
	AgencyCreditEmailStatusFailed   = "failed"
	AgencyCreditEmailStatusDisabled = "disabled"

	// AgencyCreditRepaymentDay is the single site-wide repayment day. Every
	// credit statement falls due on the first day of the month.
	AgencyCreditRepaymentDay = 1

	// AgencyDebtKindModelCharge marks ordinary credit usage debt; chargeback
	// and refund debt use other kinds.
	AgencyDebtKindModelCharge = "model_charge"

	// agencyCreditSettlementSourceKind labels the funding ledger and repayment
	// rows created by an externally collected credit statement. The column is
	// size:16, so this value must stay short.
	agencyCreditSettlementSourceKind = "external"
)

// AgencyCreditOverview is the read-only credit position shared by the main
// site wallet and the Agency Center customer views. All amounts remain integer
// quota units; callers decide how to present them as currency.
type AgencyCreditOverview struct {
	UserID            int64  `json:"user_id"`
	Configured        bool   `json:"configured"`
	Status            string `json:"status"`
	CreditLimit       int64  `json:"credit_limit"`
	UsedQuota         int64  `json:"used_quota"`
	AvailableQuota    int64  `json:"available_quota"`
	PaymentTermDays   int    `json:"payment_term_days"`
	RepaymentDay      int    `json:"repayment_day"`
	NextStatementAtMS int64  `json:"next_statement_at_ms"`
}

// ensureAgencyCreditAvailableTx enforces a contract only when one exists.
// Customers without a credit contract retain the historical wallet behavior.
func ensureAgencyCreditAvailableTx(tx *gorm.DB, userID, currentDebt, additionalDebt int64) error {
	var terms AgencyCreditTerms
	err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&terms).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if terms.Status != "" && terms.Status != AgencyCreditStatusActive {
		return ErrAgencyCreditLimitExceeded
	}
	var modelDebt int64
	if err := tx.Model(&AgencyFundingDebt{}).Where("user_id = ? AND debt_kind = ? AND outstanding_quota > 0", userID, AgencyDebtKindModelCharge).Select("COALESCE(SUM(outstanding_quota), 0)").Scan(&modelDebt).Error; err != nil {
		return err
	}
	if terms.CreditLimit < 0 || modelDebt < 0 || additionalDebt < 0 || modelDebt > int64(^uint64(0)>>1)-additionalDebt || modelDebt+additionalDebt > terms.CreditLimit {
		return ErrAgencyCreditLimitExceeded
	}
	return nil
}

// NextAgencyStatementAtMS returns the next unified repayment date: the first
// day of the month after now, in the caller's location.
func NextAgencyStatementAtMS(now time.Time) int64 {
	loc := now.Location()
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(now.Year(), now.Month(), AgencyCreditRepaymentDay, 0, 0, 0, 0, loc).AddDate(0, 1, 0).UnixMilli()
}

type agencyCreditDebtTotal struct {
	UserID           int64 `gorm:"column:user_id"`
	OutstandingQuota int64 `gorm:"column:outstanding_quota"`
}

// GetAgencyCreditOverview returns one customer's contract and outstanding
// model-charge debt. A missing Agency table is reported as "not configured" so
// a main-site-only database can still render the wallet.
func GetAgencyCreditOverview(userID int64) (*AgencyCreditOverview, error) {
	overviews, err := GetAgencyCreditOverviews([]int64{userID})
	if err != nil {
		return nil, err
	}
	overview, ok := overviews[userID]
	if !ok {
		overview = agencyCreditEmptyOverview(userID)
	}
	return &overview, nil
}

// GetAgencyCreditOverviews batches the contract plus debt lookup for customer
// lists. Users without a contract are still returned with their outstanding
// debt so an unconfigured customer with historical credit usage stays visible.
func GetAgencyCreditOverviews(userIDs []int64) (map[int64]AgencyCreditOverview, error) {
	result := make(map[int64]AgencyCreditOverview, len(userIDs))
	ids := make([]int64, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if _, ok := result[userID]; ok {
			continue
		}
		result[userID] = agencyCreditEmptyOverview(userID)
		ids = append(ids, userID)
	}
	if len(ids) == 0 || DB == nil || !DB.Migrator().HasTable(&AgencyCreditTerms{}) {
		return result, nil
	}
	nextStatement := NextAgencyStatementAtMS(time.Now())
	var terms []AgencyCreditTerms
	if err := DB.Where("user_id IN ?", ids).Find(&terms).Error; err != nil {
		return nil, err
	}
	for _, term := range terms {
		overview := result[term.UserID]
		overview.Configured = true
		overview.Status = term.Status
		overview.CreditLimit = term.CreditLimit
		overview.PaymentTermDays = term.PaymentTermDays
		result[term.UserID] = overview
	}
	if DB.Migrator().HasTable(&AgencyFundingDebt{}) {
		var totals []agencyCreditDebtTotal
		if err := DB.Model(&AgencyFundingDebt{}).
			Select("user_id, COALESCE(SUM(outstanding_quota), 0) AS outstanding_quota").
			Where("user_id IN ? AND debt_kind = ? AND outstanding_quota > 0", ids, AgencyDebtKindModelCharge).
			Group("user_id").
			Scan(&totals).Error; err != nil {
			return nil, err
		}
		for _, total := range totals {
			overview := result[total.UserID]
			overview.UsedQuota = total.OutstandingQuota
			result[total.UserID] = overview
		}
	}
	for userID, overview := range result {
		overview.NextStatementAtMS = nextStatement
		overview.RepaymentDay = AgencyCreditRepaymentDay
		if overview.Configured && overview.Status == AgencyCreditStatusActive {
			overview.AvailableQuota = max(int64(0), overview.CreditLimit-overview.UsedQuota)
		} else {
			overview.AvailableQuota = 0
		}
		result[userID] = overview
	}
	return result, nil
}

func agencyCreditEmptyOverview(userID int64) AgencyCreditOverview {
	return AgencyCreditOverview{
		UserID:       userID,
		Status:       AgencyCreditStatusSuspended,
		RepaymentDay: AgencyCreditRepaymentDay,
	}
}

// SettleAgencyCreditDebtTx applies an externally verified collection to the
// customer's outstanding model-charge debt, oldest debt first. It deliberately
// does not create wallet funding: the money arrived outside the platform, so
// the credit statement plus the funding ledger stay the source of truth.
//
// Attribution is intentionally per customer, not per statement period. Every
// monthly statement is a view over the same pool of delivered-but-unpaid
// usage, so a collection clears the oldest outstanding usage first. Finance
// still passes the statement's own remaining amount as amountQuota, which
// keeps the running total of open statement balances equal to the running
// total of outstanding model-charge debt.
func SettleAgencyCreditDebtTx(tx *gorm.DB, userID, amountQuota int64, operationID string) error {
	if tx == nil || userID <= 0 || amountQuota <= 0 || strings.TrimSpace(operationID) == "" {
		return ErrAgencyCreditSettlementInvalid
	}
	if err := EnsureAgencyFundingAccount(tx, userID); err != nil {
		return err
	}
	var account AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
		return err
	}
	var debts []AgencyFundingDebt
	if err := AgencyLockForUpdate(tx).Where("user_id = ? AND debt_kind = ? AND outstanding_quota > 0", userID, AgencyDebtKindModelCharge).Order("id ASC").Find(&debts).Error; err != nil {
		return err
	}
	var outstanding int64
	for _, debt := range debts {
		if debt.OriginalQuota < 0 || debt.ReversedQuota < 0 || debt.ReversedQuota > debt.OriginalQuota ||
			debt.OutstandingQuota < 0 || debt.OutstandingQuota > debt.OriginalQuota-debt.ReversedQuota {
			return ErrAgencyComponentRefundProvenance
		}
		outstanding += debt.OutstandingQuota
	}
	if outstanding < amountQuota || account.DebtQuota < amountQuota {
		return ErrAgencyCreditSettlementExceedsDebt
	}
	seq := account.MoneySeq + 1
	if seq <= account.MoneySeq {
		return errors.New("agency funding sequence overflow")
	}
	now := time.Now().UnixMilli()
	remaining := amountQuota
	for i := range debts {
		if remaining == 0 {
			break
		}
		debt := &debts[i]
		take := min(remaining, debt.OutstandingQuota)
		if take <= 0 {
			continue
		}
		outstandingAfter := debt.OutstandingQuota - take
		if err := tx.Model(&AgencyFundingDebt{}).Where("id = ?", debt.ID).Update("outstanding_quota", outstandingAfter).Error; err != nil {
			return err
		}
		if err := tx.Create(&AgencyDebtRepayment{
			DebtID:        debt.ID,
			RepaymentID:   fmt.Sprintf("credit-settlement:%s:%d", operationID, debt.ID),
			SourceKind:    agencyCreditSettlementSourceKind,
			Quota:         take,
			RestoredTotal: debt.OriginalQuota - debt.ReversedQuota - outstandingAfter,
			MoneySeq:      seq,
			CreatedAtMS:   now,
		}).Error; err != nil {
			return err
		}
		remaining -= take
	}
	if remaining != 0 {
		return ErrAgencyCreditSettlementExceedsDebt
	}
	debtAfter := account.DebtQuota - amountQuota
	if err := tx.Model(&AgencyFundingAccount{}).Where("user_id = ?", userID).Updates(map[string]any{
		"debt_quota": debtAfter,
		"money_seq":  seq,
		"version":    account.Version + 1,
		"updated_at": now / 1000,
	}).Error; err != nil {
		return err
	}
	return tx.Create(&AgencyFundingLedger{
		OperationID:  operationID,
		EntryNo:      0,
		UserID:       userID,
		MoneySeq:     seq,
		SourceKind:   agencyCreditSettlementSourceKind,
		DebtDelta:    -amountQuota,
		PaidAfter:    account.PaidAvailable,
		NonpaidAfter: account.NonpaidAvailable,
		DebtAfter:    debtAfter,
		CreatedAtMS:  now,
	}).Error
}

// MarkAgencyCreditInvoicePaidTx records an externally received repayment
// against an open statement. A partial payment keeps the statement open with
// the remaining balance so finance can collect the rest later.
func MarkAgencyCreditInvoicePaidTx(tx *gorm.DB, invoiceID, amountQuota, receivedBy int64, paymentReference string) (*AgencyCreditInvoice, error) {
	if tx == nil || invoiceID <= 0 || receivedBy <= 0 || strings.TrimSpace(paymentReference) == "" {
		return nil, ErrAgencyCreditSettlementInvalid
	}
	var invoice AgencyCreditInvoice
	if err := AgencyLockForUpdate(tx).Where("id = ?", invoiceID).First(&invoice).Error; err != nil {
		return nil, err
	}
	if invoice.Status != AgencyCreditInvoiceStatusIssued && invoice.Status != AgencyCreditInvoiceStatusOverdue {
		return nil, ErrAgencyCreditInvoiceClosed
	}
	remaining := invoice.AmountQuota - invoice.PaidQuota - invoice.BadDebtQuota
	if remaining <= 0 {
		return nil, ErrAgencyCreditInvoiceClosed
	}
	if amountQuota <= 0 {
		amountQuota = remaining
	}
	if amountQuota > remaining {
		return nil, ErrAgencyCreditSettlementExceedsDebt
	}
	operationID := fmt.Sprintf("credit-invoice-%d-%d", invoice.ID, invoice.PaidQuota+amountQuota)
	if err := SettleAgencyCreditDebtTx(tx, invoice.UserID, amountQuota, operationID); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	paidQuota := invoice.PaidQuota + amountQuota
	status := invoice.Status
	if paidQuota+invoice.BadDebtQuota >= invoice.AmountQuota {
		status = AgencyCreditInvoiceStatusPaid
	}
	reference := strings.TrimSpace(paymentReference)
	if err := tx.Model(&AgencyCreditInvoice{}).Where("id = ?", invoice.ID).Updates(map[string]any{
		"paid_quota":        paidQuota,
		"status":            status,
		"paid_at_ms":        now,
		"payment_reference": reference,
		"received_by":       receivedBy,
		"updated_at_ms":     now,
	}).Error; err != nil {
		return nil, err
	}
	invoice.PaidQuota = paidQuota
	invoice.Status = status
	invoice.PaidAtMS = &now
	invoice.PaymentReference = reference
	invoice.ReceivedBy = receivedBy
	invoice.UpdatedAtMS = now
	return &invoice, nil
}

// MarkAgencyCreditInvoiceBadDebtTx writes off the unpaid balance of an overdue
// statement and suspends the customer's credit line. The underlying funding
// debt is intentionally left in place: it is the historical evidence that the
// usage was delivered and never collected.
func MarkAgencyCreditInvoiceBadDebtTx(tx *gorm.DB, invoiceID, actorID int64, reason string) (*AgencyCreditInvoice, error) {
	trimmedReason := strings.TrimSpace(reason)
	if tx == nil || invoiceID <= 0 || actorID <= 0 || trimmedReason == "" {
		return nil, ErrAgencyCreditSettlementInvalid
	}
	var invoice AgencyCreditInvoice
	if err := AgencyLockForUpdate(tx).Where("id = ?", invoiceID).First(&invoice).Error; err != nil {
		return nil, err
	}
	if invoice.Status != AgencyCreditInvoiceStatusOverdue {
		return nil, ErrAgencyCreditInvoiceClosed
	}
	remaining := invoice.AmountQuota - invoice.PaidQuota - invoice.BadDebtQuota
	if remaining <= 0 {
		return nil, ErrAgencyCreditInvoiceClosed
	}
	now := time.Now().UnixMilli()
	badDebtQuota := invoice.BadDebtQuota + remaining
	if err := tx.Model(&AgencyCreditInvoice{}).Where("id = ?", invoice.ID).Updates(map[string]any{
		"bad_debt_quota":  badDebtQuota,
		"status":          AgencyCreditInvoiceStatusBadDebt,
		"bad_debt_at_ms":  now,
		"bad_debt_reason": trimmedReason,
		"received_by":     actorID,
		"updated_at_ms":   now,
	}).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&AgencyCreditTerms{}).Where("user_id = ? AND status <> ?", invoice.UserID, AgencyCreditStatusSuspended).Updates(map[string]any{
		"status":        AgencyCreditStatusSuspended,
		"updated_by":    actorID,
		"updated_at_ms": now,
	}).Error; err != nil {
		return nil, err
	}
	invoice.BadDebtQuota = badDebtQuota
	invoice.Status = AgencyCreditInvoiceStatusBadDebt
	invoice.BadDebtAtMS = &now
	invoice.BadDebtReason = trimmedReason
	invoice.ReceivedBy = actorID
	invoice.UpdatedAtMS = now
	return &invoice, nil
}
