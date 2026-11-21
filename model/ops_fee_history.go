package model

import (
	"context"
	"errors"
	"math"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OpsFeeHistoryComplete parks the independent history cursor permanently.
const OpsFeeHistoryComplete int64 = math.MaxInt64

// SaveOpsFeeHistoryPage fills missing rows and previously unconfirmed results.
// Verified amounts belong to the live reader and are never overwritten here.
// Progress and results commit together, without touching logs or money.
func SaveOpsFeeHistoryPage(ctx context.Context, db *gorm.DB, progress, connection, owner, cursor string, rows []OpsFee, at, next int64, complete bool) error {
	if progress == "" || connection == "" || progress == connection {
		return errors.New("history progress must be independent")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state OpsFeeSync
		if err := lockForUpdate(tx).Where("connection = ? AND owner = ? AND lease_until >= ?", progress, owner, at).First(&state).Error; err != nil {
			return errors.New("history lease lost")
		}
		if len(rows) > 0 {
			for i := range rows {
				rows[i].Connection = connection
				row := rows[i]
				if row.Status != "available" || row.Amount == nil {
					continue
				}
				// Atomic predicate also protects a concurrent immediate/live result,
				// mismatched identity, currency and a newer unresolved observation.
				if err := tx.Model(&OpsFee{}).Where("connection = ? AND request_id = ? AND user_id = ? AND model = ? AND currency = ? AND status = ? AND amount IS NULL AND fetched_at <= ?",
					connection, row.RequestID, row.UserID, row.Model, row.Currency, "unconfirmed", row.FetchedAt).Updates(map[string]any{
					"amount": row.Amount, "station_amount": row.StationAmount, "refund_amount": row.RefundAmount,
					"difference": row.Difference, "charged_at": row.ChargedAt, "fetched_at": row.FetchedAt, "status": row.Status,
				}).Error; err != nil {
					return err
				}
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 50).Error; err != nil {
				return err
			}
		}
		if complete {
			next = OpsFeeHistoryComplete
		}
		return tx.Model(&OpsFeeSync{}).Where("connection = ? AND owner = ?", progress, owner).Updates(map[string]any{
			"cursor": cursor, "last_success_at": at, "last_error": "", "failures": 0,
			"owner": "", "lease_until": 0, "next_run_at": next,
		}).Error
	})
}
