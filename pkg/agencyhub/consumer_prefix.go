package agencyhub

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errPriorMoneySequence = errors.New("agency event waiting for prior user money sequence")
var errConsumerPrefixWarming = errors.New("agency prior user money sequence verification in progress")

// WarmConsumerPrefixes verifies only predecessor receipts for the currently
// runnable queue. It never projects events or changes balances, deliveries,
// commissions or source receipts. This allows a bounded pre-release warmup
// of large histories without delaying the first live delivery by one tick
// for every page. A missing predecessor remains blocked and is not skipped.
func (a *App) WarmConsumerPrefixes(ctx context.Context) error {
	type target struct{ UserID, MoneySeq int64 }
	var targets []target
	err := a.db.WithContext(ctx).Table((model.AgencyBillingOutbox{}).TableName()+" AS outbox").
		Select("outbox.user_id, MIN(outbox.money_seq) AS money_seq").
		Joins("JOIN "+(model.AgencyEventDelivery{}).TableName()+" AS delivery ON delivery.event_id=outbox.event_id").
		Where("delivery.status IN ? AND outbox.money_seq>1", []string{"pending", "retry"}).
		Group("outbox.user_id").Limit(128).Scan(&targets).Error
	if err != nil {
		return err
	}
	for _, target := range targets {
		for {
			err = a.prepareConsumerPrefix(ctx, agencycontract.BillingEvent{UserID: target.UserID, MoneySeq: target.MoneySeq})
			if !errors.Is(err, errConsumerPrefixWarming) {
				if err != nil && !errors.Is(err, errPriorMoneySequence) {
					return err
				}
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	return nil
}

// prepareConsumerPrefix commits at most one bounded page of proof separately
// from projection. A failed or missing predecessor cannot roll back this
// bootstrap progress, and cannot be hidden by unrelated completed receipts.
func (a *App) prepareConsumerPrefix(ctx context.Context, event agencycontract.BillingEvent) error {
	if event.MoneySeq <= 1 {
		return nil
	}
	// A replay still reaches the projection's payload-hash check and leased
	// acknowledgement, but need not rebuild a historical prefix first.
	var completed model.AgencySourceEvent
	if err := a.db.WithContext(ctx).Select("id").Where("event_id=? AND processing_status IN ?", event.EventID, []string{"done", "skipped"}).Limit(1).Find(&completed).Error; err != nil {
		return err
	}
	if completed.ID != 0 {
		return nil
	}
	ready := false
	warming := false
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.AgencyConsumerPrefix{UserID: event.UserID}).Error; err != nil {
			return err
		}
		var prefix model.AgencyConsumerPrefix
		if err := model.AgencyLockForUpdate(tx).Where("user_id = ?", event.UserID).First(&prefix).Error; err != nil {
			return err
		}
		if prefix.VerifiedBefore >= event.MoneySeq {
			ready = true
			return nil
		}
		type receipt struct {
			ID, MoneySeq int64
			Complete     int
		}
		var rows []receipt
		err := tx.Table((model.AgencyBillingOutbox{}).TableName()+" AS prior").
			Select("prior.id, prior.money_seq, CASE WHEN receipt.id IS NOT NULL THEN 1 ELSE 0 END AS complete").
			Joins("LEFT JOIN "+(model.AgencySourceEvent{}).TableName()+" AS receipt ON receipt.event_id=prior.event_id AND receipt.user_id=prior.user_id AND receipt.money_seq=prior.money_seq AND receipt.payload_hash=prior.payload_hash AND receipt.processing_status IN ?", []string{"done", "skipped"}).
			Where("prior.user_id=? AND prior.money_seq>0 AND prior.money_seq>=? AND prior.money_seq<?", event.UserID, prefix.ScanSeq, event.MoneySeq).
			Where("prior.money_seq>? OR (prior.money_seq=? AND prior.id>?)", prefix.ScanSeq, prefix.ScanSeq, prefix.ScanID).
			Order("prior.money_seq ASC, prior.id ASC").Limit(512).Scan(&rows).Error
		if err != nil {
			return err
		}
		complete := true
		for _, row := range rows {
			if row.Complete == 0 {
				complete = false
				break
			}
			prefix.ScanSeq, prefix.ScanID = row.MoneySeq, row.ID
		}
		if complete && len(rows) < 512 {
			prefix.VerifiedBefore = event.MoneySeq
			ready = true
		}
		warming = complete && !ready
		return tx.Model(&model.AgencyConsumerPrefix{}).Where("user_id=?", event.UserID).Updates(map[string]any{"scan_seq": prefix.ScanSeq, "scan_id": prefix.ScanID, "verified_before": prefix.VerifiedBefore}).Error
	})
	if err != nil {
		return err
	}
	if !ready {
		if warming {
			return errConsumerPrefixWarming
		}
		return errPriorMoneySequence
	}
	return nil
}

func requireConsumerPrefix(tx *gorm.DB, event agencycontract.BillingEvent) error {
	if event.MoneySeq <= 1 {
		return nil
	}
	var prefix model.AgencyConsumerPrefix
	err := model.AgencyLockForUpdate(tx).Where("user_id=?", event.UserID).First(&prefix).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errPriorMoneySequence
	}
	if err != nil {
		return err
	}
	if prefix.VerifiedBefore < event.MoneySeq {
		return errPriorMoneySequence
	}
	return nil
}
