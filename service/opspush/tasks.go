package opspush

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/pkg/opsstorage"
	"gorm.io/gorm"
)

func (p *Pusher) buildRelayBatch(state *State) (bool, error) {
	var rows []model.OPSWalletUsage
	if err := p.DB.Where("scope = ? AND delivered_at = ?", p.Config.Scope, 0).Order("id").Limit(20).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	events := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var event map[string]any
		if common.UnmarshalJsonStr(row.Payload, &event) != nil {
			return false, errors.New("invalid OPS relay outbox")
		}
		extra, ok := event["extra"].(map[string]any)
		if !ok || extra["billing_scope"] != p.Config.Scope || extra["project_relay_terminal"] != true || event["requestId"] != row.RequestID || event["userId"] != strconv.Itoa(row.UserID) || event["currency"] != "USD" {
			return false, errors.New("invalid OPS relay outbox identity")
		}
		hold, err := model.GetOPSWalletHold(p.DB, row.RequestID, row.UserID)
		if err != nil {
			return false, err
		}
		if hold.ID != row.ID || hold.Model != event["model"] || hold.ActualQuota != int64(row.Quota) || hold.State != "settled" && hold.State != "reconciled" {
			return false, errors.New("OPS relay outbox reservation mismatch")
		}
		channel, _ := event["channelId"].(string)
		channelID, _ := strconv.Atoi(channel)
		meta, err := common.Marshal(extra)
		if err != nil {
			return false, err
		}
		if err := p.attachUpstream(&model.Log{ChannelId: channelID, Type: model.LogTypeConsume, Other: string(meta)}, event); err != nil {
			return false, err
		}
		events = append(events, event)
	}
	_, err := p.persistCustodyBatch(state, "relay", events)
	return err == nil, err
}

// Persist the largest bounded prefix. Unsent events stay in their durable
// outbox; a single oversized event stops with its evidence intact. Batch IDs
// cover the exact bytes selected, so a different prefix cannot reuse an ID.
func (p *Pusher) persistCustodyBatch(state *State, kind string, events []map[string]any) (int, error) {
	for count := len(events); count > 0; count-- {
		raw, err := common.Marshal(events[:count])
		if err != nil {
			return 0, err
		}
		id := fmt.Sprintf("custody-%s-%x", kind, sha256.Sum256(raw))
		body, err := common.Marshal(batch{SchemaVersion: "1.1", BatchId: id, Events: events[:count]})
		if err != nil {
			return 0, err
		}
		if len(body) > maxBody {
			continue
		}
		if err := p.save(map[string]any{"batch_id": id, "batch_body": string(body), "batch_last": state.Cursor, "attempts": 0}); err != nil {
			return 0, err
		}
		state.BatchId, state.BatchBody, state.BatchLast = id, opsstorage.LargeText(body), state.Cursor
		return count, nil
	}
	return 0, errors.New("OPS custody event exceeds delivery limit")
}

func (p *Pusher) acknowledgeRelayBatch(state *State) error {
	var sent batch
	if p.Config.Scope == "" || common.UnmarshalJsonStr(string(state.BatchBody), &sent) != nil {
		return errors.New("invalid OPS relay acknowledgement")
	}
	return p.DB.Transaction(func(tx *gorm.DB) error {
		for _, event := range sent.Events {
			request, _ := event["requestId"].(string)
			user, _ := event["userId"].(string)
			var row model.OPSWalletUsage
			if err := tx.Where("scope = ? AND request_id = ? AND user_id = ?", p.Config.Scope, request, user).First(&row).Error; err != nil {
				return err
			}
			if !matchesFrozenUsage([]byte(row.Payload), event, p.Config.Scope, false) {
				return errors.New("OPS relay acknowledgement changed identity")
			}
			if err := tx.Model(&model.OPSWalletUsage{}).Where("id = ? AND delivered_at = ?", row.ID, 0).Update("delivered_at", p.Now().Unix()).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Use the product task tables. The shared project wire contract does not imply
// that an existing overseas task may be moved to a project-prefixed table.
func (p *Pusher) buildTaskBatch(state *State) (bool, error) {
	pending := p.DB.Model(&model.TaskBillingReconciliation{}).Select("task_id").Where("status IN ?", []string{model.TaskBillingReconciliationPending, model.TaskBillingReconciliationProcessing})
	var tasks []model.Task
	if err := p.DB.Where("ops_usage_pending = ? AND status IN ? AND billing_reconciliation_pending = ? AND id > ?", true, []model.TaskStatus{model.TaskStatusSuccess, model.TaskStatusFailure}, false, p.taskAfter).
		Where("id NOT IN (?)", pending).Order("id").Limit(20).Find(&tasks).Error; err != nil {
		return false, err
	}
	if len(tasks) == 0 {
		p.taskAfter = 0
		return false, nil
	}
	events := make([]map[string]any, 0, len(tasks))
	var eventRows []int64
	for _, task := range tasks {
		if task.PrivateData.BillingSource != "ops" {
			continue // Preserve any old bridge evidence for its own worker.
		}
		if task.PrivateData.BillingScope != p.Config.Scope {
			return false, errors.New("invalid OPS terminal task owner")
		}
		if len(task.PrivateData.OpsUsage) == 0 {
			intent, execution := task.PrivateData.OPSTerminal, task.PrivateData.Execution
			if intent == nil || execution == nil {
				continue
			}
			if err := model.FinalizeOPSTask(p.DB, &task, execution.RequestID, intent.Quota, intent.Event); err != nil {
				return false, err
			}
			// Read the committed outbox; finalization must not publish an
			// in-memory snapshot from an uncommitted outer transaction.
			if err := p.DB.First(&task, task.ID).Error; err != nil {
				return false, err
			}
		}
		var event map[string]any
		if common.Unmarshal(task.PrivateData.OpsUsage, &event) != nil {
			return false, errors.New("invalid OPS terminal task outbox")
		}
		extra, ok := event["extra"].(map[string]any)
		if !ok || extra["billing_scope"] != p.Config.Scope || extra["project_task_terminal"] != true {
			return false, errors.New("invalid OPS terminal task scope")
		}
		original, _ := extra["project_original_request"].(string)
		if original == "" || event["requestId"] != opsauthority.TaskRequestID(original) || event["userId"] != strconv.Itoa(task.UserId) {
			return false, errors.New("invalid OPS terminal task identity")
		}
		// Old interrupted rows may still need financial recovery. Skip them
		// without starving later finalized tasks or releasing their capacity.
		hold, err := model.GetOPSWalletHold(p.DB, original, task.UserId)
		if err != nil {
			return false, err
		}
		if hold.State != "settled" && hold.State != "reconciled" {
			continue
		}
		if hold.Model != event["model"] || hold.ChargeRequestID != event["requestId"] || hold.TaskID != task.ID || hold.ActualQuota != int64(task.Quota) {
			return false, errors.New("terminal task reservation identity mismatch")
		}
		extra["project_task_row"] = strconv.FormatInt(task.ID, 10)
		other := model.NewLogOther()
		model.CaptureUpstreamLogReference(other, task.PrivateData.UpstreamBaseURL)
		if err := p.attachUpstream(&model.Log{ChannelId: task.ChannelId, Type: model.LogTypeConsume, Other: other.JSONString()}, event); err != nil {
			return false, err
		}
		events = append(events, event)
		eventRows = append(eventRows, task.ID)
	}
	if len(events) == 0 {
		p.taskAfter = tasks[len(tasks)-1].ID
		return false, nil
	}
	count, err := p.persistCustodyBatch(state, "task", events)
	if err != nil {
		return false, err
	}
	// Advance only across the persisted prefix. A truncated task is eligible
	// on the very next scan instead of waiting for a full cursor wrap.
	p.taskAfter = eventRows[count-1]
	return true, nil
}

func (p *Pusher) acknowledgeTaskBatch(state *State) error {
	var sent batch
	if p.Config.Scope == "" || common.UnmarshalJsonStr(string(state.BatchBody), &sent) != nil {
		return errors.New("invalid terminal task acknowledgement")
	}
	return p.DB.Transaction(func(tx *gorm.DB) error {
		for _, event := range sent.Events {
			extra, _ := event["extra"].(map[string]any)
			row, _ := extra["project_task_row"].(string)
			id, err := strconv.ParseInt(row, 10, 64)
			if err != nil || id <= 0 || extra["billing_scope"] != p.Config.Scope {
				return errors.New("terminal task acknowledgement identity missing")
			}
			var task model.Task
			if err := tx.First(&task, id).Error; err != nil {
				return err
			}
			if task.PrivateData.BillingSource != "ops" || task.PrivateData.BillingScope != p.Config.Scope || strconv.Itoa(task.UserId) != event["userId"] || !matchesFrozenUsage(task.PrivateData.OpsUsage, event, p.Config.Scope, true) {
				return errors.New("terminal task acknowledgement changed identity")
			}
			if err := tx.Model(&model.Task{}).Where("id = ? AND ops_usage_pending = ?", id, true).Update("ops_usage_pending", false).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Delivery may add upstream attribution, but cannot acknowledge different
// pricing, usage, ownership or money facts from the committed outbox.
func matchesFrozenUsage(payload []byte, sent map[string]any, scope string, task bool) bool {
	var frozen, delivered map[string]any
	raw, err := common.Marshal(sent)
	if err != nil || common.Unmarshal(payload, &frozen) != nil || common.Unmarshal(raw, &delivered) != nil {
		return false
	}
	frozenExtra, frozenOK := frozen["extra"].(map[string]any)
	deliveredExtra, deliveredOK := delivered["extra"].(map[string]any)
	if !frozenOK || !deliveredOK || frozen["currency"] != "USD" || frozenExtra["billing_scope"] != scope || deliveredExtra["billing_scope"] != scope {
		return false
	}
	for _, key := range []string{"base_url", "gateway_upstream_id", "ops_upstream_id"} {
		delete(frozenExtra, key)
		delete(deliveredExtra, key)
	}
	if task {
		delete(deliveredExtra, "project_task_row")
	}
	return reflect.DeepEqual(frozen, delivered)
}
