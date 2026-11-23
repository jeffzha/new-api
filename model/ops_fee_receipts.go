package model

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SaveOpsFeeReceipts fills the first readback gap using a verified push ACK.
// Existing feed results, including refunds/unconfirmed corrections, always win.
// Blue/green readers share durable results without changing a cursor or wallet.
func SaveOpsFeeReceipts(ctx context.Context, db *gorm.DB, connection string, rows []OpsFee) error {
	if connection == "" || len(rows) > 500 {
		return errors.New("invalid fee receipt batch")
	}
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if row.Connection != connection || row.RequestID == "" || row.UserID == "" || row.Model == "" || row.Status != "available" || row.Amount == nil || row.Currency != "USD" && row.Currency != "CNY" {
			return errors.New("invalid verified fee receipt")
		}
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 50).Error
}
