package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// agencyFundingSource identifies money returned to availability in the current
// transaction. Lot zero is historical nonpaid funding with no lot provenance,
// never paid money. It follows identifiable lots, matching the legacy allocator.
type agencyFundingSource struct {
	LotID   int64
	Paid    int64
	Nonpaid int64
}

// repayAgencyFundingSourcesTx pays outstanding liabilities using only these
// proven sources. The caller owns the user/account lock and applies the returned
// bucket deltas in the same financial operation. Partial repayment is allowed;
// the exact repayment is min(proven source total, outstanding debt).
// Repayments retain their source
// even if the original charge is later refunded or the payment is charged back.
func repayAgencyFundingSourcesTx(tx *gorm.DB, userID int64, operationID string, moneySeq, debtQuota int64, sources []agencyFundingSource) (paid, nonpaid int64, err error) {
	if debtQuota < 0 || moneySeq <= 0 || operationID == "" {
		return 0, 0, ErrAgencyComponentRefundProvenance
	}
	byLot := make(map[int64]agencyFundingSource)
	var total int64
	for _, source := range sources {
		if source.LotID < 0 || source.Paid < 0 || source.Nonpaid < 0 || (source.LotID == 0 && source.Paid != 0) ||
			source.Paid > int64(common.MaxQuota)-total || source.Nonpaid > int64(common.MaxQuota)-total-source.Paid {
			return 0, 0, ErrAgencyComponentRefundProvenance
		}
		total += source.Paid + source.Nonpaid
		merged := byLot[source.LotID]
		merged.LotID = source.LotID
		merged.Paid += source.Paid
		merged.Nonpaid += source.Nonpaid
		byLot[source.LotID] = merged
	}
	if debtQuota == 0 || total == 0 {
		return 0, 0, nil
	}
	lotIDs := make([]int64, 0, len(byLot))
	for id := range byLot {
		if id > 0 {
			lotIDs = append(lotIDs, id)
		}
	}
	sort.Slice(lotIDs, func(i, j int) bool { return lotIDs[i] < lotIDs[j] })
	var lots []AgencyFundingLot
	if len(lotIDs) > 0 {
		if err := AgencyLockForUpdate(tx).Where("id IN ? AND user_id = ?", lotIDs, userID).Order("money_seq ASC, id ASC").Find(&lots).Error; err != nil {
			return 0, 0, err
		}
		if len(lots) != len(lotIDs) {
			return 0, 0, ErrAgencyComponentRefundProvenance
		}
	}
	ordered := make([]agencyFundingSource, 0, len(byLot))
	lotIndex := make(map[int64]int, len(lots))
	for i, lot := range lots {
		ordered = append(ordered, byLot[lot.ID])
		lotIndex[lot.ID] = i
	}
	if opening, ok := byLot[0]; ok {
		ordered = append(ordered, opening)
	}
	var debts []AgencyFundingDebt
	if err := AgencyLockForUpdate(tx).Where("user_id = ? AND outstanding_quota <> 0", userID).Order("id ASC").Find(&debts).Error; err != nil {
		return 0, 0, err
	}
	var outstanding int64
	for _, debt := range debts {
		if debt.OriginalQuota < 0 || debt.ReversedQuota < 0 || debt.ReversedQuota > debt.OriginalQuota || debt.OutstandingQuota < 0 ||
			debt.OutstandingQuota > debt.OriginalQuota-debt.ReversedQuota || debt.OutstandingQuota > debtQuota-outstanding {
			return 0, 0, ErrAgencyComponentRefundProvenance
		}
		outstanding += debt.OutstandingQuota
	}
	if outstanding != debtQuota {
		return 0, 0, ErrAgencyComponentRefundProvenance
	}
	debtIndex := 0
	// Match wallet funding priority: paid first, then nonpaid; FIFO within each.
	for _, kind := range []string{"paid", "nonpaid"} {
		for _, source := range ordered {
			available := source.Paid
			if kind == "nonpaid" {
				available = source.Nonpaid
			}
			for available > 0 && debtIndex < len(debts) {
				debt := &debts[debtIndex]
				take := min(available, debt.OutstandingQuota)
				var lotID *int64
				if source.LotID > 0 {
					lot := &lots[lotIndex[source.LotID]]
					updates := map[string]any{"version": lot.Version + 1}
					if kind == "paid" {
						if lot.PaidAvailable < take || lot.PaidDebtRepaid < 0 || lot.PaidDebtRepaid > int64(common.MaxQuota)-take {
							return 0, 0, ErrAgencyComponentRefundProvenance
						}
						lot.PaidAvailable -= take
						lot.PaidDebtRepaid += take
						updates["paid_available"], updates["paid_debt_repaid"] = lot.PaidAvailable, lot.PaidDebtRepaid
					} else {
						if lot.BonusAvailable < take || lot.BonusDebtRepaid < 0 || lot.BonusDebtRepaid > int64(common.MaxQuota)-take {
							return 0, 0, ErrAgencyComponentRefundProvenance
						}
						lot.BonusAvailable -= take
						lot.BonusDebtRepaid += take
						updates["bonus_available"], updates["bonus_debt_repaid"] = lot.BonusAvailable, lot.BonusDebtRepaid
					}
					result := tx.Model(lot).Updates(updates)
					if result.Error != nil {
						return 0, 0, result.Error
					}
					if result.RowsAffected != 1 {
						return 0, 0, ErrAgencyComponentRefundProvenance
					}
					id := lot.ID
					lotID = &id
				}
				debt.OutstandingQuota -= take
				result := tx.Model(debt).Update("outstanding_quota", debt.OutstandingQuota)
				if result.Error != nil {
					return 0, 0, result.Error
				}
				if result.RowsAffected != 1 {
					return 0, 0, ErrAgencyComponentRefundProvenance
				}
				digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%d", operationID, source.LotID, kind, debt.ID)))
				if err := tx.Create(&AgencyDebtRepayment{DebtID: debt.ID, RepaymentID: "repay:" + hex.EncodeToString(digest[:]),
					FundingLotID: lotID, SourceKind: kind, Quota: take, RestoredTotal: debt.OriginalQuota - debt.ReversedQuota - debt.OutstandingQuota,
					MoneySeq: moneySeq, CreatedAtMS: time.Now().UnixMilli()}).Error; err != nil {
					return 0, 0, err
				}
				if kind == "paid" {
					paid += take
				} else {
					nonpaid += take
				}
				available -= take
				if debt.OutstandingQuota == 0 {
					debtIndex++
				}
			}
		}
	}
	if paid+nonpaid != min(total, debtQuota) {
		return 0, 0, ErrAgencyComponentRefundProvenance
	}
	return paid, nonpaid, nil
}
