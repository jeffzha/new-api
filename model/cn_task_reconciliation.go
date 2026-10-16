package model

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type cnTaskProviderBill struct {
	RecordID   int64                               `json:"record_id"`
	Settlement TaskBillingReconciliationSettlement `json:"settlement"`
}

func settleCNTaskReconciliation(task *Task, id int64, settlement TaskBillingReconciliationSettlement) (*TaskBillingReconciliationSettlementResult, error) {
	if settlement.ActualQuota < 0 || settlement.ActualQuota > common.MaxQuota || settlement.TotalTokens < 0 {
		return nil, ErrCNConflict
	}
	before := task.Quota
	payload, err := common.Marshal(cnTaskProviderBill{RecordID: id, Settlement: settlement})
	if err != nil {
		return nil, err
	}
	err = cnTaskTransaction(context.Background(), DB, task.CNBillingKey, func(tx *gorm.DB, current *Task, row *CNTaskBilling) error {
		if current == nil || current.ID != task.ID || current.Status != TaskStatusSuccess {
			return ErrCNConflict
		}
		var record TaskBillingReconciliation
		if err := lockForUpdate(tx).First(&record, id).Error; err != nil {
			return err
		}
		if record.TaskID != task.ID || record.ChannelID != row.ChannelID || record.UpstreamTaskID != row.UpstreamID || current.PrivateData.BillingContext == nil || current.PrivateData.BillingContext.ProviderBilling == nil || current.PrivateData.BillingContext.ProviderBilling.Provider != record.Provider {
			return ErrCNConflict
		}
		if record.Status == TaskBillingReconciliationSettled {
			if row.ProviderBill != string(payload) {
				return ErrCNConflict
			}
			return nil
		}
		if record.Status != TaskBillingReconciliationProcessing || row.State != "active" || (row.ProviderBill != "" && row.ProviderBill != string(payload)) {
			return ErrCNConflict
		}
		return tx.Model(row).Updates(map[string]any{"provider_bill": string(payload), "pending": true, "target": settlement.ActualQuota, "terminal": true, "final_error": "", "next_attempt_at": int64(0)}).Error
	})
	if err != nil {
		return nil, err
	}
	if err := CompleteCNTaskIntent(context.Background(), DB, task.CNBillingKey); err != nil {
		return nil, err
	}
	if err := DB.First(task, task.ID).Error; err != nil {
		return nil, err
	}
	return &TaskBillingReconciliationSettlementResult{Task: task, PreConsumedQuota: before, QuotaDelta: task.Quota - before, Applied: true, WalletAdjusted: task.PrivateData.BillingSource == TaskBillingSourceWallet}, nil
}

func cnCompleteTaskProviderBillTx(tx *gorm.DB, task *Task, row *CNTaskBilling) error {
	if row.ProviderBill == "" {
		return nil
	}
	var bill cnTaskProviderBill
	if err := common.UnmarshalJsonStr(row.ProviderBill, &bill); err != nil || task == nil {
		return ErrCNConflict
	}
	var record TaskBillingReconciliation
	if err := lockForUpdate(tx).First(&record, bill.RecordID).Error; err != nil {
		return err
	}
	if record.TaskID != task.ID || task.Status != TaskStatusSuccess || row.Target != bill.Settlement.ActualQuota || (record.Status != TaskBillingReconciliationProcessing && record.Status != TaskBillingReconciliationPending) {
		return ErrCNConflict
	}
	updates := map[string]any{"status": TaskBillingReconciliationSettled, "next_retry_at": int64(0), "last_error": "", "total_tokens": bill.Settlement.TotalTokens, "supplier_price": bill.Settlement.SupplierPrice, "supplier_discount": bill.Settlement.SupplierDiscount, "supplier_amount_paid": bill.Settlement.SupplierAmountPaid, "expense_time": bill.Settlement.ExpenseTime, "pre_consumed_quota": task.Quota, "actual_quota": row.Target, "quota_delta": row.Target - task.Quota, "updated_at": time.Now().Unix()}
	if err := tx.Model(&record).Updates(updates).Error; err != nil {
		return err
	}
	return tx.Model(task).Update("billing_reconciliation_pending", false).Error
}
