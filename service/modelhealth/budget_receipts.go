package modelhealth

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Reconcile only the platform key's settled charge. This releases the internal
// safety reservation; it does not create an OPS receipt or alter a wallet.
// A cursor bounds each cycle and revisits missing receipts without starving new runs.
func syncProbeBudgetReceipts(ctx context.Context) error {
	if model.LOG_DB == nil {
		return nil
	}
	db := model.DB.WithContext(ctx)
	cursor, err := model.ReadModelHealthConfig(db, "budget-receipt-cursor", int64(0))
	if err != nil {
		return err
	}
	var runs []model.ModelHealthRun
	if err := db.Where("id > ? AND actual_micro IS NULL AND state IN ?", cursor, []string{"succeeded", "failed"}).Order("id").Limit(25).Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		cursor = run.ID
		if !identifier.MatchString(run.RequestID) {
			continue
		}
		if run.UpstreamTaskID != "" {
			if err := syncProbeTaskBudgetReceipt(ctx, run); err != nil {
				return err
			}
			continue
		}
		var logs []model.Log
		if err := model.LOG_DB.WithContext(ctx).Select("id", "user_id", "token_id", "request_id", "channel_id", "model_name", "quota", "other").Where("request_id = ? AND channel_id = ? AND model_name = ? AND type = ?", run.RequestID, run.ChannelID, run.ModelName, model.LogTypeConsume).Limit(2).Find(&logs).Error; err != nil {
			return err
		}
		if len(logs) != 1 {
			continue
		}
		log := logs[0]
		var token model.Token
		if err := db.Select("id", "user_id", "name").Where("id = ? AND user_id = ?", log.TokenId, log.UserId).Limit(1).Find(&token).Error; err != nil {
			return err
		}
		if token.Id == 0 || !strings.HasPrefix(token.Name, "model-health/") {
			continue
		}
		var snapshot struct {
			IsTask          bool   `json:"is_task"`
			SettlementStage string `json:"settlement_stage"`
			Billing         struct {
				Status   string `json:"status"`
				Amount   string `json:"amount"`
				Currency string `json:"currency"`
				Source   string `json:"source"`
				Quota    *int   `json:"quota"`
			} `json:"billing"`
		}
		if common.UnmarshalJsonStr(log.Other, &snapshot) != nil {
			continue
		}
		// Asynchronous submission logs may describe only a pre-consumption.
		// Keep their reservation until a final task receipt is verified.
		if snapshot.IsTask || snapshot.SettlementStage != "" {
			continue
		}
		b := snapshot.Billing
		amount, err := decimal.NewFromString(b.Amount)
		if err != nil || b.Status != "settled" || b.Currency != "CNY" || b.Source != "gateway" || b.Quota == nil || *b.Quota != log.Quota || log.Quota < 0 || amount.IsNegative() || amount.GreaterThan(decimal.NewFromInt(1000000)) {
			continue
		}
		// Round upward for budget accounting so sub-micro charges never vanish.
		micro := amount.Mul(decimal.NewFromInt(1000000)).Ceil().IntPart()
		if err := model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
			q := tx.Model(&model.ModelHealthRun{}).Where("id = ? AND actual_micro IS NULL AND state IN ?", run.ID, []string{"succeeded", "failed"}).Update("actual_micro", micro)
			if q.Error != nil || q.RowsAffected == 0 {
				return q.Error
			}
			return model.SaveModelHealthConfig(tx, "receipt-"+strconv.FormatInt(run.ID, 10), "budget-receipt", map[string]any{"run_id": run.ID, "log_id": log.Id, "request_id": run.RequestID, "source": "platform_settled_log", "currency": b.Currency, "amount": b.Amount, "budget_micro": micro, "reconciled_at": time.Now().Unix()})
		}); err != nil {
			return err
		}
	}
	if len(runs) < 25 {
		cursor = 0
	}
	return model.SaveModelHealthConfig(db, "budget-receipt-cursor", "internal", cursor)
}

// Native video settlement has a durable receipt committed with the wallet
// adjustment. Submission logs alone cannot prove its final charge.
func syncProbeTaskBudgetReceipt(ctx context.Context, run model.ModelHealthRun) error {
	return model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		var tasks []model.Task
		if err := tx.Select("id", "task_id", "channel_id", "user_id", "status", "quota", "private_data", "billing_reconciliation_pending").Where("task_id = ? AND channel_id = ?", run.UpstreamTaskID, run.ChannelID).Limit(2).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != 1 {
			return nil
		}
		task := tasks[0]
		pd := task.PrivateData
		if task.Status != model.TaskStatusSuccess || task.BillingReconciliationPending || pd.Execution == nil || pd.Execution.RequestID != run.RequestID || pd.BillingContext == nil || pd.BillingContext.OriginModelName != run.ModelName || pd.BillingContext.ProviderBilling == nil {
			return nil
		}
		var token model.Token
		if err := tx.Select("id", "name").Where("id = ? AND user_id = ?", pd.TokenId, task.UserId).Limit(1).Find(&token).Error; err != nil {
			return err
		}
		if token.Id == 0 || !strings.HasPrefix(token.Name, "model-health/") {
			return nil
		}
		var receipts []model.TaskBillingReconciliation
		if err := tx.Where("task_id = ? AND channel_id = ? AND status = ?", task.ID, run.ChannelID, model.TaskBillingReconciliationSettled).Limit(2).Find(&receipts).Error; err != nil {
			return err
		}
		if len(receipts) != 1 || receipts[0].ActualQuota != task.Quota || task.Quota < 0 {
			return nil
		}
		provider := pd.BillingContext.ProviderBilling
		rate, err := decimal.NewFromString(provider.CNYPerUSD)
		var template Template
		if common.UnmarshalJsonStr(run.Template, &template) != nil {
			return nil
		}
		unit, unitErr := decimal.NewFromString(template.QuotaPerUnit)
		if quote := pd.BillingContext.AgencyPricing; quote != nil {
			// Durable agency billing freezes its own conversion at acceptance.
			if quote.CurrencyCode != "CNY" {
				return nil
			}
			unit, unitErr = decimal.NewFromString(quote.QuotaPerUnit)
			rate, err = decimal.NewFromString(quote.ExchangeRate)
		}
		if provider.Provider != model.TaskBillingProviderOpenAISeedance || receipts[0].Provider != provider.Provider || provider.Currency != "CNY" || err != nil || !rate.IsPositive() || rate.GreaterThan(decimal.NewFromInt(1000)) || unitErr != nil || !unit.IsPositive() {
			return nil
		}
		// Use the quota actually committed to the dedicated platform wallet,
		// converted using this task's frozen FX, never today's model price.
		amount := decimal.NewFromInt(int64(task.Quota)).Div(unit).Mul(rate)
		if amount.GreaterThan(decimal.NewFromInt(1000000)) {
			return nil
		}
		micro := amount.Mul(decimal.NewFromInt(1000000)).Ceil().IntPart()
		q := tx.Model(&model.ModelHealthRun{}).Where("id = ? AND actual_micro IS NULL AND state IN ?", run.ID, []string{"succeeded", "failed"}).Update("actual_micro", micro)
		if q.Error != nil || q.RowsAffected == 0 {
			return q.Error
		}
		return model.SaveModelHealthConfig(tx, "receipt-"+strconv.FormatInt(run.ID, 10), "budget-receipt", map[string]any{"run_id": run.ID, "task_id": task.TaskID, "settlement_id": receipts[0].ID, "request_id": run.RequestID, "source": "platform_task_settlement", "currency": "CNY", "amount": amount.String(), "budget_micro": micro, "reconciled_at": time.Now().Unix()})
	})
}
