package service

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/tidwall/gjson"
)

// PrepareOPSAuthorityTask freezes the financial intent before the terminal
// status CAS. The pusher can finish a committed intent after a process crash.
func PrepareOPSAuthorityTask(adaptor TaskPollingAdaptor, task *model.Task, result *relaycommon.TaskInfo) {
	if task == nil || task.PrivateData.BillingSource != "ops" || task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure || task.PrivateData.OPSTerminal != nil || len(task.PrivateData.OpsUsage) > 0 {
		return
	}
	task.OpsUsagePending = true
	task.PrivateData.OPSBillingError = "terminal_pricing_requires_reconciliation"
	if !opsauthority.Enabled() || task.PrivateData.BillingScope != opsauthority.Scope() || task.PrivateData.Execution == nil || task.PrivateData.Execution.RequestID == "" {
		return
	}
	target := task.Quota
	bc := task.PrivateData.BillingContext
	switch {
	case task.Status == model.TaskStatusFailure:
		target = 0
	case bc == nil:
		return
	case bc.ProviderBilling != nil && bc.ProviderBilling.AsyncReconciliationRequired:
		task.PrivateData.OPSBillingError = "provider_bill_pending"
		return
	case result == nil:
		return
	case bc.TieredSnapshot != nil:
		settled, usage, err := EvaluateTaskCompletionUsage(bc.TieredSnapshot, result.UsageFacts)
		if err != nil || settled.Clamp != nil {
			return
		}
		target = settled.ActualQuotaAfterGroup
		bc.TieredSnapshot.UsageFacts, bc.TieredSnapshot.EstimatedTier = usage, settled.MatchedTier
	case bc.PerCallBilling:
	default:
		handled := false
		if checked, ok := adaptor.(TaskPollingCheckedBillingAdaptor); ok {
			quota, clamp, yes, err := checked.AdjustBillingOnCompleteChecked(task, result)
			if err != nil || clamp != nil {
				return
			}
			if yes {
				target, handled = quota, true
			}
		}
		if !handled && adaptor != nil {
			if quota := adaptor.AdjustBillingOnComplete(task, result); quota > 0 {
				target, handled = quota, true
			}
		}
		if !handled {
			tokens := result.TotalTokens
			if tokens == 0 {
				tokens = result.CompletionTokens
			}
			if tokens <= 0 || bc.ModelRatio <= 0 {
				return
			}
			multiplier := 1.0
			if pd := taskBillingContextPriceData(bc); pd != nil {
				multiplier = pd.OtherRatioMultiplier()
			}
			var clamp *common.QuotaClamp
			target, clamp = common.QuotaFromFloatChecked(float64(tokens) * bc.ModelRatio * bc.GroupRatio * multiplier)
			if clamp != nil {
				return
			}
		}
	}
	if target < 0 || target > common.MaxQuota || common.QuotaPerUnit != 500_000 {
		return
	}
	freezeOPSAuthorityTaskTerminal(task, target, result)
}

func freezeOPSAuthorityTaskTerminal(task *model.Task, target int, result *relaycommon.TaskInfo) {
	bc := task.PrivateData.BillingContext
	recordTaskSettledTokens(task, result)
	original := task.PrivateData.Execution.RequestID
	id := opsauthority.TaskRequestID(original)
	other := taskBillingOther(task)
	other.SetPublic("billing_scope", opsauthority.Scope())
	if path := task.PrivateData.Execution.RequestPath; path != "" {
		other.SetPublic("request_path", path)
	}
	facts := map[string]any{}
	if bc != nil && bc.UsageDimensions != nil {
		dimensions := bc.UsageDimensions
		if dimensions.Resolution != "" {
			facts["resolution"] = dimensions.Resolution
		}
		if dimensions.ReferenceVideo != nil {
			facts["reference_video"] = *dimensions.ReferenceVideo
		}
	}
	if result != nil {
		for key, value := range result.UsageFacts {
			facts[key] = value
		}
		if result.TotalTokens > 0 {
			facts["total_tokens"] = result.TotalTokens
		}
		if result.CompletionTokens > 0 {
			facts["completion_tokens"] = result.CompletionTokens
		}
	}
	root := gjson.ParseBytes(task.Data)
	for _, key := range []string{"total_tokens", "completion_tokens"} {
		if value := root.Get("usage." + key); value.Type == gjson.Number {
			facts[key] = value.Value()
		}
	}
	for _, key := range []string{"duration", "resolution", "generate_audio"} {
		if value := root.Get(key); value.Type == gjson.Number || value.Type == gjson.String || value.Type == gjson.True || value.Type == gjson.False {
			facts[key] = value.Value()
		}
	}
	if bc != nil && bc.ProviderBilling != nil {
		facts["reference_video"] = bc.ProviderBilling.HasVideoInput
	}
	other.SetPublic("usage_facts", facts)
	kind, recordKind := model.LogTypeConsume, "usage"
	if task.Status == model.TaskStatusFailure {
		kind, recordKind = model.LogTypeError, "error"
	}
	record, err := opspush.EventUSD(&model.Log{UserId: task.UserId, Type: kind, RequestId: id, CreatedAt: task.SubmitTime, ModelName: taskModelName(task), TokenId: task.PrivateData.TokenId, ChannelId: task.ChannelId, Group: task.Group, Quota: target, Other: other.JSONString()}, "", nil, opspush.Conversion{QuotaPerUnit: common.QuotaPerUnit, USDExchangeRate: 1})
	if err != nil || record == nil {
		return
	}
	record["requestId"] = id
	amount, err := opspush.USDAmount(int64(target), common.QuotaPerUnit)
	if err != nil {
		return
	}
	record["amount"] = amount
	extra := record["extra"].(map[string]any)
	extra["recordKind"], extra["direction"] = recordKind, "debit"
	extra["project_original_request"], extra["project_task_terminal"], extra["billing_scope"] = original, true, opsauthority.Scope()
	encoded, err := common.Marshal(record)
	if err != nil {
		return
	}
	task.PrivateData.OPSTerminal = &model.OPSTaskTerminal{Quota: target, Event: json.RawMessage(encoded)}
	task.PrivateData.OPSBillingError = ""
}

func finalizeOPSAuthorityTask(task *model.Task) error {
	if task == nil || task.PrivateData.BillingSource != "ops" {
		return nil
	}
	intent, execution := task.PrivateData.OPSTerminal, task.PrivateData.Execution
	if intent == nil || execution == nil {
		return errors.New("OPS terminal task financial evidence pending for task " + strconv.FormatInt(task.ID, 10))
	}
	if err := model.FinalizeOPSTask(model.DB, task, execution.RequestID, intent.Quota, intent.Event); err != nil {
		return err
	}
	opsnotify.Committed(task.PrivateData.BillingScope)
	return model.DB.First(task, task.ID).Error
}
