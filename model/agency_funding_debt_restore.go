package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// lockAgencyAllocationDebtTx prefers the exact allocation identity. Historical
// payment-chargeback rows pooled liabilities by top-up; they can be adopted
// only when that source has exactly one ever-revoked allocation. Repayments and
// fully cancelled rows remain relevant evidence, not just outstanding debts.
func lockAgencyAllocationDebtTx(tx *gorm.DB, allocation AgencyFundingAllocation, origin, kind string) (AgencyFundingDebt, error) {
	if allocation.UserID <= 0 || origin == "" || allocation.ID <= 0 {
		return AgencyFundingDebt{}, ErrAgencyComponentRefundProvenance
	}
	var exact []AgencyFundingDebt
	if err := AgencyLockForUpdate(tx).Where("user_id = ? AND origin_operation_id = ? AND debt_kind = ? AND allocation_id = ?",
		allocation.UserID, origin, kind, allocation.ID).Limit(2).Find(&exact).Error; err != nil {
		return AgencyFundingDebt{}, err
	}
	if len(exact) > 1 {
		return AgencyFundingDebt{}, ErrAgencyComponentRefundProvenance
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	var pooled []AgencyFundingDebt
	if err := AgencyLockForUpdate(tx).Where("user_id = ? AND origin_operation_id = ? AND debt_kind = ? AND allocation_id IS NULL",
		allocation.UserID, origin, kind).Limit(2).Find(&pooled).Error; err != nil {
		return AgencyFundingDebt{}, err
	}
	if len(pooled) == 0 {
		return AgencyFundingDebt{}, gorm.ErrRecordNotFound
	}
	if len(pooled) != 1 {
		return AgencyFundingDebt{}, ErrAgencyComponentRefundProvenance
	}
	if kind == "payment_chargeback" {
		lots := tx.Model(&AgencyFundingLot{}).Select("id").Where("user_id = ? AND source_id = ?", allocation.UserID, origin)
		var revoked []AgencyFundingAllocation
		if err := AgencyLockForUpdate(tx).Where("user_id = ? AND lot_id IN (?) AND (revoked_reserved_debt > 0 OR revoked_nonpaid > 0)",
			allocation.UserID, lots).Limit(2).Find(&revoked).Error; err != nil {
			return AgencyFundingDebt{}, err
		}
		if len(revoked) != 1 || revoked[0].ID != allocation.ID {
			return AgencyFundingDebt{}, ErrAgencyComponentRefundProvenance
		}
	}
	debt := pooled[0]
	result := tx.Model(&debt).Where("allocation_id IS NULL").Update("allocation_id", allocation.ID)
	if result.Error != nil {
		return AgencyFundingDebt{}, result.Error
	}
	if result.RowsAffected != 1 {
		return AgencyFundingDebt{}, ErrAgencyComponentRefundProvenance
	}
	debt.AllocationID = &allocation.ID
	return debt, nil
}

// restoreAgencyAllocationDebtTx cancels liability belonging to an exact
// allocation. Outstanding debt is reduced first; already repaid liability
// restores the actual repayment lots. It never recreates the revoked origin
// topup and never converts paid repayments into bonus quota. The caller adjusts
// account/wallet and allocation totals within the same transaction.
func restoreAgencyAllocationDebtTx(tx *gorm.DB, allocation AgencyFundingAllocation, amount int64, allowLegacy bool) (sources []agencyFundingSource, debtReduced, expiredNonpaid int64, err error) {
	if amount <= 0 || amount > allocation.DebtConsumed {
		return nil, 0, 0, ErrAgencyComponentRefundProvenance
	}
	origin, kind := fmt.Sprintf("allocation-%d", allocation.ID), "model_charge"
	if allocation.LotID > 0 {
		var originLot AgencyFundingLot
		if err := tx.First(&originLot, allocation.LotID).Error; err != nil {
			return nil, 0, 0, err
		}
		if originLot.UserID != allocation.UserID {
			return nil, 0, 0, ErrAgencyComponentRefundProvenance
		}
		origin, kind = originLot.SourceID, "payment_chargeback"
	}
	debt, err := lockAgencyAllocationDebtTx(tx, allocation, origin, kind)
	if allowLegacy && kind == "model_charge" && errors.Is(err, gorm.ErrRecordNotFound) {
		// Historical allocations predate debt-lot tracking. Their old release
		// path can only reduce still-outstanding account debt; the caller keeps
		// its nonnegative account invariant and cannot restore repaid sources.
		return nil, amount, 0, nil
	}
	if err != nil {
		return nil, 0, 0, ErrAgencyComponentRefundProvenance
	}
	if debt.OriginalQuota < 0 || debt.ReversedQuota < 0 || debt.ReversedQuota > debt.OriginalQuota ||
		amount > debt.OriginalQuota-debt.ReversedQuota || debt.OutstandingQuota < 0 || debt.OutstandingQuota > debt.OriginalQuota-debt.ReversedQuota ||
		debt.OriginalQuota-debt.ReversedQuota != allocation.DebtConsumed {
		return nil, 0, 0, ErrAgencyComponentRefundProvenance
	}
	debtReduced = min(amount, debt.OutstandingQuota)
	remaining := amount - debtReduced
	if remaining > 0 {
		var repayments []AgencyDebtRepayment
		if err := AgencyLockForUpdate(tx).Where("debt_id = ? AND quota > reversed_quota", debt.ID).Order("money_seq ASC, id ASC").Find(&repayments).Error; err != nil {
			return nil, 0, 0, err
		}
		for _, repayment := range repayments {
			if remaining == 0 {
				break
			}
			kind := repayment.SourceKind
			if kind == "" {
				kind = "paid" // historical repayments were exclusively paid
			}
			if repayment.ReversedQuota < 0 || repayment.Quota < repayment.ReversedQuota ||
				(kind != "paid" && kind != "nonpaid") || (repayment.FundingLotID == nil && kind != "nonpaid") {
				return nil, 0, 0, ErrAgencyComponentRefundProvenance
			}
			take := min(remaining, repayment.Quota-repayment.ReversedQuota)
			source := agencyFundingSource{}
			if kind == "paid" {
				source.Paid = take
			} else {
				source.Nonpaid = take
			}
			if repayment.FundingLotID != nil {
				var lot AgencyFundingLot
				if err := AgencyLockForUpdate(tx).First(&lot, *repayment.FundingLotID).Error; err != nil {
					return nil, 0, 0, err
				}
				if lot.UserID != allocation.UserID {
					return nil, 0, 0, ErrAgencyComponentRefundProvenance
				}
				updates := map[string]any{"version": lot.Version + 1}
				if kind == "paid" {
					if lot.PaidDebtRepaid < take || lot.PaidAvailable > int64(common.MaxQuota)-take {
						return nil, 0, 0, ErrAgencyComponentRefundProvenance
					}
					updates["paid_debt_repaid"], updates["paid_available"] = lot.PaidDebtRepaid-take, lot.PaidAvailable+take
				} else {
					expired, err := restoreAgencyBonusTx(tx, &lot, take, true, common.GetTimestamp())
					if err != nil {
						return nil, 0, 0, err
					}
					if expired {
						expiredNonpaid += take
						source.Nonpaid = 0
					}
				}
				if kind == "paid" {
					result := tx.Model(&lot).Updates(updates)
					if result.Error != nil {
						return nil, 0, 0, result.Error
					}
					if result.RowsAffected != 1 {
						return nil, 0, 0, ErrAgencyComponentRefundProvenance
					}
				}
				source.LotID = lot.ID
			}
			result := tx.Model(&repayment).Update("reversed_quota", repayment.ReversedQuota+take)
			if result.Error != nil {
				return nil, 0, 0, result.Error
			}
			if result.RowsAffected != 1 {
				return nil, 0, 0, ErrAgencyComponentRefundProvenance
			}
			sources = append(sources, source)
			remaining -= take
		}
		if remaining != 0 {
			return nil, 0, 0, ErrAgencyComponentRefundProvenance
		}
	}
	result := tx.Model(&debt).Updates(map[string]any{"outstanding_quota": debt.OutstandingQuota - debtReduced,
		"reversed_quota": debt.ReversedQuota + amount})
	if result.Error != nil {
		return nil, 0, 0, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, 0, 0, ErrAgencyComponentRefundProvenance
	}
	return sources, debtReduced, expiredNonpaid, nil
}
