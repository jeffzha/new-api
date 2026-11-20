package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// These tables describe commission funding only. They never credit, debit or
// overwrite either the OPS wallet or the historical Gateway user wallet.
type OPSAgencyCursor struct {
	ID      string `gorm:"size:80;primaryKey"`
	Source  string `gorm:"size:64;not null"`
	EntryID int64  `gorm:"not null"`
}

type OPSAgencyAccount struct {
	ID            string           `gorm:"size:64;primaryKey"`
	UserID        int              `gorm:"not null"`
	BalanceMicros int64            `gorm:"not null"`
	DebtMicros    int64            `gorm:"not null"`
	LotsJSON      AgencyOpsPayload `gorm:"not null"`
}

type OPSAgencyOpening struct {
	ID            string           `gorm:"size:64;primaryKey"`
	UserID        int              `gorm:"not null"`
	BalanceMicros int64            `gorm:"not null"`
	DebtMicros    int64            `gorm:"not null"`
	LotsJSON      AgencyOpsPayload `gorm:"not null"`
}

type OPSAgencyReceipt struct {
	ID          string           `gorm:"size:64;primaryKey"`
	PayloadHash string           `gorm:"size:64;not null"`
	Payload     AgencyOpsPayload `gorm:"not null"`
}

type OPSAgencyCharge struct {
	ID             string           `gorm:"size:64;primaryKey"`
	UserID         int              `gorm:"not null"`
	AmountMicros   int64            `gorm:"not null"`
	RefundedMicros int64            `gorm:"not null"`
	AllocationJSON AgencyOpsPayload `gorm:"not null"`
	EventJSON      AgencyOpsPayload `gorm:"not null"`
}

type OPSAgencyLot struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Paid        bool   `json:"paid"`
	PaidBalance bool   `json:"paidBalance,omitempty"`
	Amount      int64  `json:"amountMicros"`
	Sequence    int64  `json:"sequence"`
}

func OPSAgencyID(kind, ref string) string {
	return opsHoldDigest("agency-" + kind + "\x00" + opsauthority.Scope() + "\x00" + ref)
}

func opsHoldDigest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

// SortOPSAgencyLots preserves the source ledger's versioned funding order.
// Version 2 consumes non-paid funds first; version 1 retains paid-first.
func SortOPSAgencyLots(lots []OPSAgencyLot, paidFirst bool) {
	sort.SliceStable(lots, func(i, j int) bool {
		if paidFirst {
			left := lots[i].Paid || lots[i].PaidBalance
			right := lots[j].Paid || lots[j].PaidBalance
			if left != right {
				return left
			}
			if lots[i].Sequence != lots[j].Sequence {
				return lots[i].Sequence < lots[j].Sequence
			}
			return lots[i].ID < lots[j].ID
		}
		if lots[i].Paid != lots[j].Paid {
			return !lots[i].Paid
		}
		a, b := agencyFundingSourcePriority(lots[i].Source), agencyFundingSourcePriority(lots[j].Source)
		if a != b {
			return a < b
		}
		if lots[i].Sequence != lots[j].Sequence {
			return lots[i].Sequence < lots[j].Sequence
		}
		return lots[i].ID < lots[j].ID
	})
}

// OPSAgencyOpeningLots snapshots the remaining typed funds, rather than
// declaring the entire opening balance to be verified customer payment.
func OPSAgencyOpeningLots(tx *gorm.DB, user int, quota int64) ([]OPSAgencyLot, int64, error) {
	if user <= 0 || quota < -common.MaxWalletQuota || quota > common.MaxWalletQuota {
		return nil, 0, ErrOPSWalletHoldConflict
	}
	lots := []OPSAgencyLot{}
	if !tx.Migrator().HasTable(&AgencyFundingAccount{}) {
		if quota > 0 {
			lots = append(lots, OPSAgencyLot{ID: "opening-unknown", Source: "legacy_unknown", PaidBalance: true, Amount: quota * 2})
		}
		return lots, max(-quota*2, 0), nil
	}
	var account AgencyFundingAccount
	err := tx.First(&account, user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if quota > 0 {
			lots = append(lots, OPSAgencyLot{ID: "opening-unknown", Source: "legacy_unknown", PaidBalance: true, Amount: quota * 2})
		}
		return lots, max(-quota*2, 0), nil
	}
	if err != nil {
		return nil, 0, err
	}
	if account.PaidAvailable < 0 || account.PaidAvailable > common.MaxWalletQuota || account.NonpaidAvailable < 0 || account.NonpaidAvailable > common.MaxWalletQuota || account.DebtQuota < 0 || account.DebtQuota > common.MaxWalletQuota || account.PaidAvailable+account.NonpaidAvailable-account.DebtQuota != quota || account.ReconcileBlocked {
		return nil, 0, errors.New("opening funding account does not reconcile")
	}
	var source []AgencyFundingLot
	if err = tx.Where("user_id = ? AND (paid_available > 0 OR bonus_available > 0)", user).Order("money_seq,id").Limit(1001).Find(&source).Error; err != nil {
		return nil, 0, err
	}
	if len(source) > 1000 {
		return nil, 0, errors.New("opening funding lot limit exceeded")
	}
	paidTotal, bonusTotal := int64(0), int64(0)
	for _, lot := range source {
		if lot.PaidAvailable < 0 || lot.PaidAvailable > common.MaxWalletQuota-paidTotal || lot.BonusAvailable < 0 || lot.BonusAvailable > common.MaxWalletQuota-bonusTotal || lot.PaidReserved != 0 || lot.BonusReserved != 0 || lot.ExpiresAt != 0 {
			return nil, 0, errors.New("opening funding lot is not drained")
		}
		var proof struct {
			Amount    string `json:"actual_money"`
			Currency  string `json:"currency_code"`
			Reference string `json:"payment_reference"`
		}
		_ = common.Unmarshal([]byte(lot.SourceSnapshotJSON), &proof)
		paid, proofErr := decimal.Zero, errors.New("invalid payment amount")
		if proof.Amount != "" && len(proof.Amount) <= 32 && !strings.ContainsAny(proof.Amount, "eE+") {
			paid, proofErr = decimal.NewFromString(proof.Amount)
		}
		verified := proofErr == nil && paid.IsPositive() && proof.Currency == "USD" && proof.Reference != "" && !agencyFundingLotIsLegacyUnknown(lot)
		if verified {
			var fact AgencyTopupFact
			if err := tx.Where("source_operation_id = ? AND user_id = ?", lot.SourceID, user).First(&fact).Error; err != nil {
				return nil, 0, err
			}
			verified = fact.PaymentStatus == "success" && fact.CompletionSource != "admin_adjustment" && fact.ActualMoney == proof.Amount && fact.CurrencyCode == proof.Currency && fact.PaymentReference == proof.Reference && fact.PaidQuota == lot.PaidInitial && fact.QuotaConversionSnapshot != ""
			if verified {
				var conversion TopupQuotaConversion
				verified = common.Unmarshal([]byte(fact.QuotaConversionSnapshot), &conversion) == nil && conversion.CreditedQuota == fmt.Sprint(fact.CreditedQuota) && fact.CreditedQuota == fact.PaidQuota+fact.BonusQuota && paid.Mul(decimal.NewFromInt(500000)).GreaterThanOrEqual(decimal.NewFromInt(lot.PaidInitial))
			}
		}
		if lot.PaidAvailable > 0 {
			lots = append(lots, OPSAgencyLot{ID: fmt.Sprintf("opening-%020d-1paid", lot.ID), Source: lot.SourceKind, Paid: verified, PaidBalance: true, Amount: lot.PaidAvailable * 2, Sequence: lot.MoneySeq})
		}
		if lot.BonusAvailable > 0 {
			lots = append(lots, OPSAgencyLot{ID: fmt.Sprintf("opening-%020d-0bonus", lot.ID), Source: lot.SourceKind, Amount: lot.BonusAvailable * 2, Sequence: lot.MoneySeq})
		}
		paidTotal += lot.PaidAvailable
		bonusTotal += lot.BonusAvailable
	}
	if paidTotal != account.PaidAvailable || bonusTotal != account.NonpaidAvailable {
		return nil, 0, errors.New("opening funding lots do not reconcile")
	}
	SortOPSAgencyLots(lots, false)
	return lots, account.DebtQuota * 2, nil
}

// SaveOPSAgencyOpening is called only by the reviewed offline cutover.
func SaveOPSAgencyOpening(tx *gorm.DB, scope string, user int, quota, debt int64, lots []OPSAgencyLot) error {
	if scope == "" || len(scope) > 80 || user <= 0 || quota < -common.MaxWalletQuota || quota > common.MaxWalletQuota || debt < 0 || debt > common.MaxWalletQuota*2 || len(lots) > 2000 {
		return ErrOPSWalletHoldConflict
	}
	total := int64(0)
	seen := map[string]bool{}
	for _, lot := range lots {
		if lot.ID == "" || seen[lot.ID] || lot.Amount <= 0 || lot.Amount > common.MaxWalletQuota*4-total {
			return ErrOPSWalletHoldConflict
		}
		seen[lot.ID] = true
		total += lot.Amount
	}
	if total-debt != quota*2 {
		return ErrOPSWalletHoldConflict
	}
	raw, err := common.Marshal(lots)
	if err != nil {
		return err
	}
	row := OPSAgencyOpening{ID: opsHoldDigest("agency-account\x00" + scope + "\x00" + fmt.Sprint(user)), UserID: user, BalanceMicros: quota * 2, DebtMicros: debt, LotsJSON: AgencyOpsPayload(raw)}
	var existing OPSAgencyOpening
	err = tx.First(&existing, "id = ?", row.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&row).Error
	}
	if err != nil {
		return err
	}
	if existing != row {
		return errors.New("OPS commission opening changed")
	}
	return nil
}
