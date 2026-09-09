package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

type DeliveryIntent struct {
	IntentID      string    `json:"intent_id"`
	CustomerID    uint64    `json:"customer_id"`
	PolicyVersion int64     `json:"policy_version"`
	CutoffAt      time.Time `json:"cutoff_at"`
	LegalHold     bool      `json:"legal_hold"`
}

type DeliveryReceipt struct {
	ReceiptID string         `json:"receipt_id"`
	IntentID  string         `json:"intent_id"`
	Status    string         `json:"status"`
	Counts    map[string]int `json:"counts"`
}

type DeliveryClient interface {
	Deliver(context.Context, DeliveryIntent) (DeliveryReceipt, error)
}

type Coordinator struct {
	db     *gorm.DB
	client DeliveryClient
	now    func() time.Time
}

func NewCoordinator(db *gorm.DB, client DeliveryClient) *Coordinator {
	return &Coordinator{db: db, client: client, now: time.Now}
}

// ProcessOne holds the policy row lock through the signed ADP call. Therefore a
// concurrent legal-hold change is serialized before or after deletion and can
// never race between the final hold check and ADP's exact-scope transaction.
func (c *Coordinator) ProcessOne(ctx context.Context) (bool, error) {
	if c.db == nil || c.client == nil {
		return false, errors.New("retention coordinator is not configured")
	}
	processed := false
	var deliveryErr error
	err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := c.now().UTC()
		var delivery model.CustomerRetentionDelivery
		query := database.ForUpdate(tx).
			Where("status = ? AND next_retry_at <= ?", model.RetentionDeliveryPending, now).
			Order("id asc").First(&delivery)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if query.Error != nil {
			return query.Error
		}
		processed = true
		var run model.CustomerRetentionRun
		if err := database.ForUpdate(tx).First(&run, delivery.RunID).Error; err != nil {
			return err
		}
		customer, policy, eligibilityErr := eligibleCustomer(tx, delivery.CustomerID, now)
		if eligibilityErr != nil {
			deliveryErr = eligibilityErr
		} else if policy.ID != delivery.PolicyID || policy.RowVersion != delivery.PolicyVersion || run.PolicyVersion != delivery.PolicyVersion {
			deliveryErr = domain.Conflict("retention policy changed after delivery was enqueued; create a new dry-run")
		} else if run.Status != model.RetentionRunStatusPending {
			deliveryErr = domain.Conflict("retention run is not pending external deletion")
		}
		if deliveryErr != nil {
			delivery.Attempts++
			delivery.Status = model.RetentionDeliveryBlocked
			delivery.LastError = coordinatorError(deliveryErr)
			if err := tx.Save(&delivery).Error; err != nil {
				return err
			}
			run.Status = model.RetentionRunStatusInvalid
			run.RowVersion++
			return tx.Save(&run).Error
		}

		receipt, err := c.client.Deliver(ctx, DeliveryIntent{
			IntentID: delivery.IntentID, CustomerID: delivery.CustomerID,
			PolicyVersion: delivery.PolicyVersion, CutoffAt: delivery.CutoffAt, LegalHold: false,
		})
		if err != nil {
			deliveryErr = err
			delivery.Attempts++
			delivery.NextRetryAt = now.Add(coordinatorRetryDelay(delivery.Attempts))
			delivery.LastError = coordinatorError(err)
			return tx.Save(&delivery).Error
		}
		if receipt.IntentID != delivery.IntentID || receipt.ReceiptID == "" || receipt.Status != "completed" {
			deliveryErr = errors.New("ADP retention receipt is invalid")
			delivery.Attempts++
			delivery.NextRetryAt = now.Add(coordinatorRetryDelay(delivery.Attempts))
			delivery.LastError = coordinatorError(deliveryErr)
			return tx.Save(&delivery).Error
		}
		counts, err := countCustomerData(tx, delivery.CustomerID)
		if err != nil {
			return err
		}
		if err := deleteCustomerOperationalData(tx, delivery.CustomerID); err != nil {
			return err
		}
		receiptJSON, err := jsonx.Marshal(receipt)
		if err != nil {
			return err
		}
		countsJSON, err := jsonx.Marshal(counts)
		if err != nil {
			return err
		}
		delivery.Status = model.RetentionDeliveryDelivered
		delivery.Attempts++
		delivery.ReceiptID = receipt.ReceiptID
		delivery.ReceiptJSON = string(receiptJSON)
		delivery.LastError = ""
		delivery.DeliveredAt = &now
		if err := tx.Save(&delivery).Error; err != nil {
			return err
		}
		run.CountsJSON = string(countsJSON)
		run.Status = model.RetentionRunStatusCompleted
		run.ExecutedAt = &now
		run.RowVersion++
		if err := tx.Save(&run).Error; err != nil {
			return err
		}
		return support.Audit(tx, &customer.ID, "retention-coordinator", "retention.delivery.receipt", "customer_retention_delivery", delivery.IntentID, nil, &delivery, "", receipt.ReceiptID)
	})
	if err != nil {
		return processed, err
	}
	return processed, deliveryErr
}

func coordinatorRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	delay := 5 * time.Second * time.Duration(1<<(attempt-1))
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

func coordinatorError(err error) string {
	value := strings.NewReplacer("\r", " ", "\n", " ").Replace(fmt.Sprint(err))
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
