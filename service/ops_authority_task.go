package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/tidwall/gjson"
)

// Freeze a credential-free terminal event in the existing task outbox. The
// project pusher delivers it durably; submission/adjustment logs are references
// only and must not create separate OPS charges.
func prepareProjectTaskUsage(task *model.Task, body []byte, result ...*relaycommon.TaskInfo) {
	if task == nil || task.PrivateData.BillingSource != "ops" || (task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure) || len(task.PrivateData.OpsUsage) > 0 {
		return
	}
	binding := task.PrivateData.OPSWalletBinding
	if binding == nil || binding.Version != 1 || binding.Target == "" {
		common.SysError("OPS task wallet binding missing; terminal delivery requires reconciliation")
		return
	}
	original := taskAgencyChargeID(task)
	if task.PrivateData.Execution != nil && task.PrivateData.Execution.RequestID != "" {
		original = task.PrivateData.Execution.RequestID
	}
	if original == "" {
		return
	}
	// A separate stable event avoids conflicting with submission/reference logs
	// collected by older product workers from the shared log database.
	id := fmt.Sprintf("pt-%x", sha256.Sum256([]byte(binding.Scope+"\x00"+original)))
	other := taskBillingOther(task)
	other.SetPublic("billing_scope", binding.Scope)
	other.SetPublic("project_task_terminal", true)
	other.SetPublic("project_original_request", original)
	root := gjson.ParseBytes(body)
	facts := map[string]any{}
	if len(result) > 0 && result[0] != nil {
		for key, value := range result[0].UsageFacts {
			facts[key] = value
		}
		if result[0].TotalTokens > 0 {
			facts["total_tokens"] = result[0].TotalTokens
		}
		if result[0].CompletionTokens > 0 {
			facts["completion_tokens"] = result[0].CompletionTokens
		}
	}
	if task.PrivateData.Execution != nil {
		other.SetPublic("request_path", task.PrivateData.Execution.RequestPath)
	}
	// Native final usage supersedes all submission estimates. Missing native
	// facts remain missing so OPS can report an incomplete price/usage result.
	if usage := root.Get("usage"); usage.IsObject() {
		usage.ForEach(func(k, v gjson.Result) bool {
			if v.Type == gjson.Number {
				facts[k.String()] = v.Value()
			}
			return true
		})
	}
	for _, key := range []string{"duration", "resolution", "generate_audio"} {
		if value := root.Get(key); value.Exists() {
			facts[key] = value.Value()
		}
	}
	if bc := task.PrivateData.BillingContext; bc != nil && bc.ProviderBilling != nil {
		facts["reference_video"] = bc.ProviderBilling.HasVideoInput
	}
	other.SetPublic("usage_facts", facts)
	kind := model.LogTypeConsume
	if task.Status == model.TaskStatusFailure {
		kind = model.LogTypeError
	}
	record := opspush.Event(&model.Log{UserId: task.UserId, Type: kind, RequestId: id, CreatedAt: task.SubmitTime, ModelName: taskModelName(task), TokenId: task.PrivateData.TokenId, ChannelId: task.ChannelId, Group: task.Group, Quota: task.Quota, Other: other.JSONString()}, "")
	if record == nil {
		common.SysError("project task terminal identity incomplete")
		return
	}
	// Failures have no retry-attempt suffix: this is the terminal receipt.
	record["requestId"] = id
	extra, _ := record["extra"].(map[string]json.RawMessage)
	if extra == nil {
		extra = map[string]json.RawMessage{}
	}
	recordKind := "usage"
	if kind == model.LogTypeError {
		recordKind = "error"
	}
	extra["recordKind"], _ = common.Marshal(recordKind)
	extra["direction"], _ = common.Marshal("debit")
	extra["project_original_request"], _ = common.Marshal(original)
	extra["project_task_terminal"], _ = common.Marshal(true)
	extra["billing_scope"], _ = common.Marshal(binding.Scope)
	record["extra"] = extra
	if encoded, err := common.Marshal(record); err == nil {
		task.PrivateData.OpsUsage = encoded
		task.OpsUsagePending = true
	}
}
