package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	TaskBillingReconciliationPending    = "pending"
	TaskBillingReconciliationProcessing = "processing"
	TaskBillingReconciliationSettled    = "settled"
	TaskBillingReconciliationNotNeeded  = "not_needed"
	TaskBillingProviderSeedanceDomestic = "seedance_domestic"
	TaskBillingProviderDoubaoVideoCNY   = "doubao_video_cny"
)

type TaskBillingReconciliation struct {
	ID                 int64  `json:"id" gorm:"primaryKey"`
	TaskID             int64  `json:"task_id" gorm:"uniqueIndex"`
	Provider           string `json:"provider" gorm:"type:varchar(40);index"`
	ChannelID          int    `json:"channel_id" gorm:"index"`
	UpstreamTaskID     string `json:"upstream_task_id" gorm:"type:varchar(191);index"`
	Status             string `json:"status" gorm:"type:varchar(20);index"`
	Attempts           int    `json:"attempts"`
	NextRetryAt        int64  `json:"next_retry_at" gorm:"index"`
	LastError          string `json:"last_error" gorm:"type:text"`
	TotalTokens        int64  `json:"total_tokens"`
	SupplierPrice      string `json:"supplier_price" gorm:"type:varchar(40)"`
	SupplierDiscount   string `json:"supplier_discount" gorm:"type:varchar(40)"`
	SupplierAmountPaid string `json:"supplier_amount_paid" gorm:"type:varchar(40)"`
	ExpenseTime        string `json:"expense_time" gorm:"type:varchar(40)"`
	PreConsumedQuota   int    `json:"pre_consumed_quota"`
	ActualQuota        int    `json:"actual_quota"`
	QuotaDelta         int    `json:"quota_delta"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type TaskBillingReconciliationSettlement struct {
	ActualQuota        int
	TotalTokens        int64
	SupplierPrice      string
	SupplierDiscount   string
	SupplierAmountPaid string
	ExpenseTime        string
}

type TaskBillingReconciliationSettlementResult struct {
	Task             *Task
	PreConsumedQuota int
	QuotaDelta       int
	Applied          bool
	WalletAdjusted   bool
	TokenUnavailable bool
	// AgencyRefundCommitted indicates a finalized component charge was
	// reversed through the source-aware cumulative refund transaction. The
	// service may still write usage logs, but must not emit another financial
	// refund event.
	AgencyRefundCommitted bool
}

func EnqueueTaskBillingReconciliation(task *Task, provider string) error {
	if task == nil || task.ID == 0 || provider == "" {
		return nil
	}
	now := time.Now().Unix()
	record := &TaskBillingReconciliation{
		TaskID:         task.ID,
		Provider:       provider,
		ChannelID:      task.ChannelId,
		UpstreamTaskID: task.GetUpstreamTaskID(),
		Status:         TaskBillingReconciliationPending,
		NextRetryAt:    now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(record).Error; err != nil {
		return err
	}
	if err := DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("billing_reconciliation_pending", false).Error; err != nil {
		return err
	}
	task.BillingReconciliationPending = false
	return nil
}

// EnqueuePendingTaskBillingReconciliations recovers durable enqueue intents
// left on tasks when creating the reconciliation row failed. Enqueue is
// idempotent, so a prior successful insert followed by a failed intent clear is
// safe to replay.
func EnqueuePendingTaskBillingReconciliations(limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	var tasks []*Task
	if err := DB.Where("billing_reconciliation_pending = ?", true).
		Order("id").
		Limit(limit).
		Find(&tasks).Error; err != nil {
		return 0, err
	}

	enqueued := 0
	var enqueueErrors []error
	for _, task := range tasks {
		providerBilling := task.PrivateData.BillingContext
		if providerBilling == nil || providerBilling.ProviderBilling == nil ||
			!providerBilling.ProviderBilling.AsyncReconciliationRequired || providerBilling.ProviderBilling.Provider == "" {
			enqueueErrors = append(enqueueErrors, fmt.Errorf("task %d has invalid billing reconciliation intent", task.ID))
			continue
		}
		if err := EnqueueTaskBillingReconciliation(task, providerBilling.ProviderBilling.Provider); err != nil {
			enqueueErrors = append(enqueueErrors, fmt.Errorf("enqueue task %d billing reconciliation: %w", task.ID, err))
			continue
		}
		enqueued++
	}
	return enqueued, errors.Join(enqueueErrors...)
}

func HasPendingTaskBillingReconciliations() bool {
	var taskID int64
	if err := DB.Model(&Task{}).
		Where("billing_reconciliation_pending = ?", true).
		Limit(1).
		Pluck("id", &taskID).Error; err == nil && taskID != 0 {
		return true
	}

	var id int64
	now := time.Now().Unix()
	err := DB.Model(&TaskBillingReconciliation{}).
		Where("(status = ? AND next_retry_at <= ?) OR (status = ? AND updated_at <= ?)",
			TaskBillingReconciliationPending, now,
			TaskBillingReconciliationProcessing, now-300).
		Limit(1).
		Pluck("id", &id).Error
	return err == nil && id != 0
}

func GetDueTaskBillingReconciliations(limit int) ([]*TaskBillingReconciliation, error) {
	if limit <= 0 {
		limit = 100
	}
	var records []*TaskBillingReconciliation
	now := time.Now().Unix()
	err := DB.Where("(status = ? AND next_retry_at <= ?) OR (status = ? AND updated_at <= ?)",
		TaskBillingReconciliationPending, now,
		TaskBillingReconciliationProcessing, now-300).
		Order("next_retry_at, id").
		Limit(limit).
		Find(&records).Error
	return records, err
}

func ClaimTaskBillingReconciliation(id int64) (bool, error) {
	now := time.Now().Unix()
	result := DB.Model(&TaskBillingReconciliation{}).
		Where("id = ? AND ((status = ? AND next_retry_at <= ?) OR (status = ? AND updated_at <= ?))",
			id,
			TaskBillingReconciliationPending, now,
			TaskBillingReconciliationProcessing, now-300).
		Updates(map[string]any{
			"status":     TaskBillingReconciliationProcessing,
			"attempts":   gorm.Expr("attempts + 1"),
			"updated_at": now,
		})
	return result.RowsAffected == 1, result.Error
}

func UpdateTaskBillingReconciliation(id int64, updates map[string]any) error {
	if updates == nil {
		updates = map[string]any{}
	}
	updates["updated_at"] = time.Now().Unix()
	return DB.Model(&TaskBillingReconciliation{}).Where("id = ?", id).Updates(updates).Error
}

// SettleTaskBillingReconciliation atomically applies the provider-confirmed
// quota delta to the funding source, API token, task, and unique reconciliation
// row. A retry can therefore observe either the complete settlement or none of
// it, never a partially applied balance adjustment.
func SettleTaskBillingReconciliation(id int64, settlement TaskBillingReconciliationSettlement) (*TaskBillingReconciliationSettlementResult, error) {
	if id <= 0 || settlement.ActualQuota < 0 || int64(settlement.ActualQuota) > int64(common.MaxQuota) || settlement.TotalTokens <= 0 {
		return nil, fmt.Errorf("invalid task billing settlement")
	}
	result := &TaskBillingReconciliationSettlementResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var record TaskBillingReconciliation
		if err := lockForUpdate(tx).Where("id = ?", id).First(&record).Error; err != nil {
			return err
		}
		var task Task
		if err := lockForUpdate(tx).Where("id = ?", record.TaskID).First(&task).Error; err != nil {
			return err
		}
		var firstCharge agencycontract.BillingEvent
		var firstJournal AgencyBillingJournal
		initialAgencyFinalization := false
		frozenAgencyBasis := false
		var billingMode string
		if err := tx.Model(&User{}).Where("id = ?", task.UserId).Pluck("billing_mode", &billingMode).Error; err != nil {
			return err
		}
		if billingMode == AgencyDurableBillingMode && (task.PrivateData.BillingSource != TaskBillingSourceSubscription || task.PrivateData.SubscriptionId <= 0) {
			journal, basis, pricing, basisErr := agencyTaskFrozenBasis(tx, &task)
			if basis.Version == AgencyTaskChargeBasisVersion {
				frozenAgencyBasis = true
				if basisErr != nil {
					return basisErr
				}
				if basis.ProviderBilling == nil || basis.ProviderBilling.Provider != record.Provider {
					return ErrAgencyChargeConflict
				}
				var err error
				firstCharge, err = AgencyTaskFinalCharge(basis, pricing, settlement.TotalTokens, false)
				if err != nil {
					return err
				}
				// Resolver evidence supplies usage. The accepted frozen basis
				// remains authoritative even if global quota units changed later.
				settlement.ActualQuota = int(firstCharge.ChargedTotalQuota)
				firstJournal = journal
				initialAgencyFinalization = journal.Status == "reserved" || journal.Status == "submitted" || journal.Status == "reconcile_required"
			}
		}
		if settlement.ActualQuota == 0 && !frozenAgencyBasis {
			return fmt.Errorf("zero provider bill requires a frozen agency task basis")
		}
		if record.Status == TaskBillingReconciliationSettled {
			if record.ActualQuota != settlement.ActualQuota || record.TotalTokens != settlement.TotalTokens ||
				record.SupplierPrice != settlement.SupplierPrice || record.SupplierDiscount != settlement.SupplierDiscount ||
				record.SupplierAmountPaid != settlement.SupplierAmountPaid || record.ExpenseTime != settlement.ExpenseTime {
				return ErrAgencyChargeConflict
			}
			return nil
		}
		if record.Status != TaskBillingReconciliationProcessing {
			return fmt.Errorf("task billing reconciliation %d is not claimed", id)
		}

		if task.Status != TaskStatusSuccess {
			return fmt.Errorf("task %d is not successful", task.ID)
		}
		result.Task = &task
		result.PreConsumedQuota = task.Quota
		result.QuotaDelta = settlement.ActualQuota - task.Quota
		result.WalletAdjusted = task.PrivateData.BillingSource != TaskBillingSourceSubscription || task.PrivateData.SubscriptionId <= 0

		// A finalized component charge is immutable. A lower confirmed total
		// uses the original cumulative refund; a larger total requires explicit
		// reconciliation instead of changing wallet allocations behind that
		// immutable event. This helper also validates a zero-delta replay.
		refundResult := TaskQuotaAdjustmentResult{Task: &task, PreConsumedQuota: task.Quota,
			AppliedQuota: task.Quota, QuotaDelta: result.QuotaDelta, TokenID: task.PrivateData.TokenId}
		agencyHandled := initialAgencyFinalization
		if initialAgencyFinalization {
			_, changed, tokenKey, err := finalizeAgencyTaskChargeTx(tx, &task, firstJournal, firstCharge)
			if err != nil {
				return err
			}
			updated := tx.Model(&Task{}).Where("id = ? AND quota = ?", task.ID, result.PreConsumedQuota).
				Updates(map[string]any{"quota": task.Quota, "private_data": task.PrivateData})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrAgencyChargeConflict
			}
			refundResult.Changed, refundResult.WalletAdjusted, refundResult.TokenKey = changed, true, tokenKey
		} else {
			var refundErr error
			agencyHandled, refundErr = applyAgencyTaskRefundTx(tx, &task, settlement.ActualQuota, &refundResult)
			if refundErr != nil {
				return refundErr
			}
		}
		if agencyHandled {
			result.QuotaDelta = refundResult.QuotaDelta
			result.TokenUnavailable = refundResult.TokenUnavailable
			result.AgencyRefundCommitted = refundResult.AgencyRefundCommitted
			result.Applied = refundResult.Changed
			result.WalletAdjusted = refundResult.WalletAdjusted
		}

		// A token may be soft-deleted after the task was accepted. Never
		// refund the wallet without a token to restore: retain a negative
		// adjustment at the pre-consumed amount and leave an audit marker on
		// the reconciliation row. Additional provider usage may still be
		// charged to the wallet, but a deleted token is never recreated.
		var token Token
		tokenPresent := task.PrivateData.TokenId <= 0
		if task.PrivateData.TokenId > 0 && !agencyHandled {
			tokenErr := lockForUpdate(tx).
				Where("id = ? AND user_id = ?", task.PrivateData.TokenId, task.UserId).
				First(&token).Error
			switch {
			case tokenErr == nil:
				tokenPresent = true
			case errors.Is(tokenErr, gorm.ErrRecordNotFound):
				result.TokenUnavailable = true
				if result.QuotaDelta < 0 {
					result.QuotaDelta = 0
					result.WalletAdjusted = false
				}
			default:
				return tokenErr
			}
		}

		if result.QuotaDelta != 0 && !agencyHandled {
			if result.WalletAdjusted {
				var billingMode string
				if err := tx.Model(&User{}).Where("id = ?", task.UserId).Pluck("billing_mode", &billingMode).Error; err != nil {
					return err
				}
				if billingMode == AgencyProvisioningBillingMode {
					return ErrAgencyProvisioning
				}
				if billingMode == AgencyDurableBillingMode {
					// Reconciliation must address the same stable charge used by
					// the initial reservation. The finalized event ID is distinct
					// and cannot be used to locate those allocations.
					chargeID := ""
					if task.PrivateData.BillingContext != nil {
						chargeID = task.PrivateData.BillingContext.AgencyChargeID
					}
					if chargeID == "" && task.PrivateData.BillingContext != nil {
						// Compatibility for tasks written before AgencyChargeID
						// was persisted. Such tasks may still have the old event ID.
						chargeID = task.PrivateData.BillingContext.AgencyBillingEventID
					}
					if chargeID == "" {
						chargeID = task.TaskID
					}
					if err := AdjustAgencyChargeTx(tx, task.UserId, result.QuotaDelta, chargeID, int64(settlement.ActualQuota)); err != nil {
						return fmt.Errorf("adjust agency funding for reconciliation: %w", err)
					}
				} else {
					update := tx.Model(&User{}).Where("id = ?", task.UserId).
						Update("quota", gorm.Expr("quota - ?", result.QuotaDelta))
					if update.Error != nil {
						return update.Error
					}
					if update.RowsAffected != 1 {
						return fmt.Errorf("task billing user %d not found", task.UserId)
					}
				}
			} else {
				var subscription UserSubscription
				if err := lockForUpdate(tx).Where("id = ?", task.PrivateData.SubscriptionId).First(&subscription).Error; err != nil {
					return err
				}
				newUsed := subscription.AmountUsed + int64(result.QuotaDelta)
				if newUsed < 0 {
					newUsed = 0
				}
				if subscription.AmountTotal > 0 && newUsed > subscription.AmountTotal {
					return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, subscription.AmountTotal)
				}
				if err := tx.Model(&subscription).Update("amount_used", newUsed).Error; err != nil {
					return err
				}
			}

			if tokenPresent && task.PrivateData.TokenId > 0 {
				afterRemain := int64(token.RemainQuota) - int64(result.QuotaDelta)
				afterUsed := int64(token.UsedQuota) + int64(result.QuotaDelta)
				if afterRemain > int64(common.MaxQuota) || afterRemain < -int64(common.MaxQuota)-1 ||
					afterUsed > int64(common.MaxQuota) || afterUsed < -int64(common.MaxQuota)-1 {
					return errors.New("task billing token quota arithmetic overflow")
				}
				updatedToken := tx.Model(&Token{}).
					Where("id = ? AND user_id = ?", token.Id, task.UserId).
					Updates(map[string]any{
						"remain_quota":  afterRemain,
						"used_quota":    afterUsed,
						"accessed_time": time.Now().Unix(),
					})
				if updatedToken.Error != nil {
					return updatedToken.Error
				}
				if updatedToken.RowsAffected != 1 {
					return fmt.Errorf("task billing token %d changed concurrently", token.Id)
				}
			}

			updatedTask := tx.Model(&Task{}).
				Where("id = ? AND quota = ?", task.ID, task.Quota).
				Update("quota", settlement.ActualQuota)
			if updatedTask.Error != nil {
				return updatedTask.Error
			}
			if updatedTask.RowsAffected != 1 {
				return fmt.Errorf("task quota changed concurrently")
			}
			result.Applied = true
		}

		updates := map[string]any{
			"status":               TaskBillingReconciliationSettled,
			"next_retry_at":        0,
			"last_error":           "",
			"total_tokens":         settlement.TotalTokens,
			"supplier_price":       settlement.SupplierPrice,
			"supplier_discount":    settlement.SupplierDiscount,
			"supplier_amount_paid": settlement.SupplierAmountPaid,
			"expense_time":         settlement.ExpenseTime,
			"pre_consumed_quota":   result.PreConsumedQuota,
			"actual_quota":         settlement.ActualQuota,
			"quota_delta":          result.QuotaDelta,
			"updated_at":           time.Now().Unix(),
		}
		if result.TokenUnavailable {
			updates["last_error"] = "token unavailable; historical quota retained"
		}
		updatedRecord := tx.Model(&TaskBillingReconciliation{}).
			Where("id = ? AND status = ?", id, TaskBillingReconciliationProcessing).
			Updates(updates)
		if updatedRecord.Error != nil {
			return updatedRecord.Error
		}
		if updatedRecord.RowsAffected != 1 {
			return fmt.Errorf("task billing reconciliation changed concurrently")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SyncTaskBillingReconciliationCaches mirrors a committed quota delta into
// Redis. Database correctness does not depend on this cache update.
func SyncTaskBillingReconciliationCaches(userID int, tokenID int, tokenKey string, quotaDelta int, walletAdjusted bool) error {
	if quotaDelta == 0 || !common.RedisEnabled {
		return nil
	}
	var cacheErrors []error
	if walletAdjusted {
		var err error
		if IsAgencyDurableUser(userID) {
			err = InvalidateUserCache(userID)
		} else {
			err = cacheIncrUserQuota(userID, -int64(quotaDelta))
		}
		if err != nil {
			cacheErrors = append(cacheErrors, err)
		}
	}
	if tokenID > 0 && tokenKey != "" {
		if _, err := cacheApplyTokenQuotaDelta(tokenID, tokenKey, -int64(quotaDelta)); err != nil {
			cacheErrors = append(cacheErrors, err)
		}
	}
	return errors.Join(cacheErrors...)
}
