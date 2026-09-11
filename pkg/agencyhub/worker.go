package agencyhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

const deliveryLeaseName = "agency-billing-consumer"

func (a *App) StartBackground(ctx context.Context) {
	if a.db == nil {
		return
	}
	interval := 2 * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.CleanupExpiredDeliverySecrets(time.Now().Unix()); err != nil {
					common.SysError("agency delivery-secret cleanup failed: " + err.Error())
				}
				if a.config.CommissionEnabled {
					if err := a.RunConsumerOnce(ctx, 100); err != nil {
						common.SysError("agency billing consumer failed: " + err.Error())
					}
				}
				if _, err := a.ProcessProvisioningJobs(20); err != nil {
					common.SysError("agency provisioning worker failed: " + err.Error())
				}
			}
		}
	}()
}

// RunConsumerOnce claims a bounded batch through event_deliveries. The
// delivery row is the progress source; outbox IDs are only ordering hints and
// can never hide a late low-ID transaction.
func (a *App) RunConsumerOnce(ctx context.Context, limit int) error {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	now := time.Now().Unix()
	_ = a.db.Model(&model.AgencyEventDelivery{}).Where("status = ? AND lease_until < ?", "claimed", now).Updates(map[string]any{"status": "retry", "next_retry_at": now})
	var deliveries []model.AgencyEventDelivery
	if err := a.db.WithContext(ctx).Where("status IN ? AND next_retry_at <= ?", []string{"pending", "retry"}, now).Order("next_retry_at ASC, id ASC").Limit(limit).Find(&deliveries).Error; err != nil {
		return err
	}
	// A reversal depends on the original earned ledger entry. Delivery IDs
	// reflect commit timing rather than that dependency, so a reversal can
	// otherwise consume a retry and leave the batch partially settled. Order
	// known original events ahead of reversals while retaining stable ordering
	// within each class.
	if len(deliveries) > 1 {
		eventIDs := make([]string, 0, len(deliveries))
		for _, delivery := range deliveries {
			eventIDs = append(eventIDs, delivery.EventID)
		}
		var outboxes []model.AgencyBillingOutbox
		if err := a.db.WithContext(ctx).Where("event_id IN ?", eventIDs).Find(&outboxes).Error; err == nil {
			reversal := make(map[string]bool, len(outboxes))
			for _, outbox := range outboxes {
				reversal[outbox.EventID] = outbox.EventKind == "agency.billing_reversed"
			}
			sort.SliceStable(deliveries, func(i, j int) bool {
				if reversal[deliveries[i].EventID] != reversal[deliveries[j].EventID] {
					return !reversal[deliveries[i].EventID]
				}
				if deliveries[i].NextRetryAt != deliveries[j].NextRetryAt {
					return deliveries[i].NextRetryAt < deliveries[j].NextRetryAt
				}
				return deliveries[i].ID < deliveries[j].ID
			})
		}
	}
	for _, delivery := range deliveries {
		claimed, leaseToken, err := a.claimDelivery(delivery)
		if err != nil || !claimed {
			continue
		}
		delivery.Status = "claimed"
		delivery.LeaseOwner = a.config.InstanceID
		delivery.LeaseToken = leaseToken
		if err := a.processDelivery(ctx, delivery); err != nil {
			common.SysError("agency event delivery failed: " + err.Error())
		}
	}
	return nil
}

func (a *App) claimDelivery(delivery model.AgencyEventDelivery) (bool, int64, error) {
	now := time.Now().Unix()
	token := time.Now().UnixNano()
	result := a.db.Model(&model.AgencyEventDelivery{}).Where("id = ? AND status IN ? AND next_retry_at <= ?", delivery.ID, []string{"pending", "retry"}, now).Updates(map[string]any{"status": "claimed", "lease_owner": a.config.InstanceID, "lease_token": token, "lease_until": now + 60})
	return result.RowsAffected == 1, token, result.Error
}

func (a *App) processDelivery(ctx context.Context, delivery model.AgencyEventDelivery) error {
	var outbox model.AgencyBillingOutbox
	if err := a.db.WithContext(ctx).Where("event_id = ?", delivery.EventID).First(&outbox).Error; err != nil {
		return err
	}
	var event agencycontract.BillingEvent
	if err := common.Unmarshal([]byte(outbox.Payload), &event); err != nil {
		_ = a.markDelivery(delivery, "poison", err)
		return err
	}
	if event.EventID != outbox.EventID {
		err := errors.New("billing event id does not match outbox")
		_ = a.markDelivery(delivery, "poison", err)
		return err
	}
	if event.SchemaVersion != agencycontract.SchemaVersion {
		err := errors.New("unknown agency billing event schema version")
		_ = a.markDelivery(delivery, "poison", err)
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		_ = a.markDelivery(delivery, "poison", err)
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(outbox.PayloadHash), payloadHash) {
		err := errors.New("billing event outbox payload hash mismatch")
		_ = a.markDelivery(delivery, "poison", err)
		return err
	}
	if err := a.processBillingEventWithLease(ctx, event, delivery); err != nil {
		if strings.Contains(err.Error(), "payload hash conflict") {
			_ = a.markDelivery(delivery, "poison", err)
			return err
		}
		_ = a.markDelivery(delivery, "retry", err)
		return err
	}
	return nil
}

func (a *App) markDelivery(delivery model.AgencyEventDelivery, status string, deliveryErr error) error {
	return a.db.Transaction(func(tx *gorm.DB) error {
		if err := a.markDeliveryTx(tx, delivery, status, deliveryErr); err != nil {
			return err
		}
		if status == "poison" {
			return recordPoisonIssueTx(tx, delivery.EventID, deliveryErr)
		}
		return nil
	})
}

func recordPoisonIssueTx(tx *gorm.DB, eventID string, deliveryErr error) error {
	if tx == nil || strings.TrimSpace(eventID) == "" {
		return nil
	}
	var count int64
	if err := tx.Model(&model.AgencyReconciliationIssue{}).
		Where("object_type = ? AND object_id = ? AND status = ?", "billing_event", eventID, "open").
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	difference := "billing event delivery moved to poison"
	if deliveryErr != nil && strings.TrimSpace(deliveryErr.Error()) != "" {
		difference += ": " + deliveryErr.Error()
	}
	digest := sha256.Sum256([]byte(difference))
	now := time.Now().UnixMilli()
	return tx.Create(&model.AgencyReconciliationIssue{
		ObjectType:   "billing_event",
		ObjectID:     eventID,
		Difference:   difference,
		EvidenceHash: hex.EncodeToString(digest[:]),
		Status:       "open",
		CreatedAtMS:  now,
	}).Error
}

func (a *App) markDeliveryTx(tx *gorm.DB, delivery model.AgencyEventDelivery, status string, deliveryErr error) error {
	now := time.Now().Unix()
	updates := map[string]any{"status": status, "processed_at": nil, "last_error": "", "attempts": delivery.Attempts + 1, "lease_until": 0}
	if status == "done" {
		updates["processed_at"] = now
		updates["next_retry_at"] = 0
	} else {
		updates["next_retry_at"] = now + retryDelay(delivery.Attempts+1)
		if deliveryErr != nil {
			updates["last_error"] = deliveryErr.Error()
		}
	}
	if status == "poison" {
		updates["next_retry_at"] = now + 3600
	}
	result := tx.Model(&model.AgencyEventDelivery{}).Where("id = ? AND status = ? AND lease_owner = ? AND lease_token = ?", delivery.ID, "claimed", delivery.LeaseOwner, delivery.LeaseToken).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("agency event delivery lease lost")
	}
	return nil
}

func retryDelay(attempt int) int64 {
	if attempt < 1 {
		return 1
	}
	delay := int64(1) << min(attempt-1, 5)
	if delay > 60 {
		return 60
	}
	return delay
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// RebuildDelivery creates a missing delivery row only for an existing
// immutable outbox event. It never fabricates a financial event from a log.
func (a *App) RebuildDelivery(eventID string) error {
	if eventID == "" {
		return errors.New("event id is required")
	}
	var outbox model.AgencyBillingOutbox
	if err := a.db.Where("event_id = ?", eventID).First(&outbox).Error; err != nil {
		return err
	}
	var delivery model.AgencyEventDelivery
	if err := a.db.Where("event_id = ?", eventID).First(&delivery).Error; err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	now := time.Now().Unix()
	return a.db.Create(&model.AgencyEventDelivery{EventID: eventID, Status: "pending", NextRetryAt: now, CreatedAt: now}).Error
}
