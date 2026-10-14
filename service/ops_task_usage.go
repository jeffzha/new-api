package service

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

// prepareOpsTaskUsage runs before the terminal CAS. A crash after that CAS is
// recovered from the pending bit, without relying on another provider poll.
func prepareOpsTaskUsage(task *model.Task, channelType int, body []byte) {
	if opsbridge.Default == nil || task.Status != model.TaskStatusSuccess || task.TaskID == "" || len(task.PrivateData.OpsUsage) != 0 {
		return
	}
	id := "ops-task-" + task.TaskID
	record := map[string]any{"event_id": id, "request_id": id, "bridge_scope": opsbridge.Default.Scope(),
		"occurred_at": time.Unix(task.FinishTime, 0).UTC().Format(time.RFC3339),
		"status":      "completed", "terminal": true, "provider": "unsupported_task",
		"customer_context": map[string]any{"gateway_user_id": strconv.Itoa(task.UserId)},
		"routing_context": map[string]any{"channel_id": strconv.Itoa(task.ChannelId),
			"model_name": task.Properties.OriginModelName, "call_type": "video_generation"}}
	if task.Properties.UpstreamModelName != "" {
		record["routing_context"].(map[string]any)["upstream_model_name"] = task.Properties.UpstreamModelName
	}
	if task.PrivateData.TokenId > 0 {
		record["customer_context"].(map[string]any)["api_key_id"] = strconv.Itoa(task.PrivateData.TokenId)
	}
	if task.PrivateData.Execution != nil && task.PrivateData.Execution.RequestID != "" {
		record["request_id"] = task.PrivateData.Execution.RequestID
	}
	root := gjson.ParseBytes(body)
	usage := root.Get("usage")
	// Only these native protocols declare usage.total_tokens as their billed
	// video token quantity. Other task providers remain explicit repair gaps.
	if (channelType == constant.ChannelTypeDoubaoVideo || channelType == constant.ChannelTypeVolcEngine || channelType == constant.ChannelTypeOpenAISeedance) &&
		(root.Get("status").String() == "succeeded" || root.Get("status").String() == "completed") && usage.IsObject() && !root.Get("error").IsObject() {
		native := map[string]any{}
		usage.ForEach(func(key, value gjson.Result) bool {
			field := key.String()
			switch field {
			case "total_tokens", "completion_tokens":
				native[field] = nil
				if value.Type == gjson.Number {
					native[field] = value.Value()
				}
			case "tool_usage":
				tools := map[string]any{}
				if !value.IsObject() {
					native["unsupported_components"] = true
				}
				value.ForEach(func(tool, count gjson.Result) bool {
					if tool.String() == "web_search" && count.Type == gjson.Number {
						tools["web_search"] = count.Value()
					} else {
						native["unsupported_components"] = true
					}
					return true
				})
				native[field] = tools
			default:
				native["unsupported_components"] = true
			}
			return true
		})
		record["provider"], record["usage"] = "seedance_video", native
		factors := map[string]any{}
		// Missing conditions stay missing: neither requested nor estimated
		// duration/resolution is substituted for a final provider response.
		if duration := root.Get("duration"); duration.Type == gjson.Number {
			if value, err := decimal.NewFromString(duration.Raw); err == nil {
				ms := value.Mul(decimal.NewFromInt(1000))
				if ms.Equal(ms.Truncate(0)) && !ms.IsNegative() && ms.LessThanOrEqual(decimal.NewFromInt(9007199254740991)) {
					factors["duration_ms"] = ms.IntPart()
				}
			}
		}
		if resolution := root.Get("resolution"); resolution.Type == gjson.String {
			factors["resolution"] = resolution.String()
		}
		if bc := task.PrivateData.BillingContext; bc != nil && bc.ProviderBilling != nil {
			factors["has_reference_video"] = bc.ProviderBilling.HasVideoInput
		}
		record["factor_schema_version"], record["billing_factors"] = "1", factors
	}
	// Encoding failure must not prevent ordinary task settlement. This record
	// contains only bounded scalar fields and usage, never prompts or media.
	if encoded, err := common.Marshal(record); err == nil {
		task.PrivateData.OpsUsage = encoded
		task.OpsUsagePending = true
	}
}

// PrepareOpsTaskTerminal freezes terminal native usage before the task row is
// inserted or won by the polling CAS.
func PrepareOpsTaskTerminal(task *model.Task, channelType int, body []byte) {
	prepareOpsTaskUsage(task, channelType, body)
}

func drainOpsTaskUsage(ctx context.Context) {
	b := opsbridge.Default
	if b == nil || ctx.Err() != nil {
		return
	}
	var tasks []*model.Task
	if err := model.DB.WithContext(ctx).Where("ops_usage_pending = ? AND status = ?", true, model.TaskStatusSuccess).
		Order("id ASC").Limit(100).Find(&tasks).Error; err != nil {
		common.SysError("Ops task evidence scan failed")
		return
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		var record map[string]any
		if common.Unmarshal(task.PrivateData.OpsUsage, &record) != nil {
			common.SysError("Ops task evidence is invalid; repair required")
			continue
		}
		if record["bridge_scope"] != b.Scope() {
			common.SysError("Ops task evidence gateway scope mismatch; repair required")
			continue
		}
		var err error
		if record["provider"] == "unsupported_task" {
			err = b.TaskUsageGap(task.TaskID)
		} else {
			err = b.TaskSucceeded(task.TaskID, record)
		}
		if err != nil {
			common.SysError("Ops task evidence enqueue failed; durable retry retained")
			continue
		}
		// An interrupted acknowledgement replays the identical outbox payload.
		if err := model.DB.WithContext(ctx).Model(&model.Task{}).Where("id = ? AND ops_usage_pending = ?", task.ID, true).
			Update("ops_usage_pending", false).Error; err != nil {
			common.SysError("Ops task evidence acknowledgement failed; durable retry retained")
		}
	}
}

func finalizeOpsTaskReservation(task *model.Task) {
	b := opsbridge.Default
	if b == nil || task == nil || task.TaskID == "" {
		return
	}
	var err error
	if task.Status == model.TaskStatusFailure {
		err = b.TaskFailed(task.TaskID)
	} else if task.Status == model.TaskStatusSuccess && !task.OpsUsagePending {
		err = b.TaskUsageGap(task.TaskID)
	}
	if err != nil {
		common.SysError("Ops task terminal reservation update failed")
	}
}

// FinalizeOpsTaskReservation queues the terminal reservation transition only
// after the task's terminal state is durable.
func FinalizeOpsTaskReservation(task *model.Task) {
	finalizeOpsTaskReservation(task)
}
