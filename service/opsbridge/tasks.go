package opsbridge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

type PendingTask struct {
	TaskID    string `json:"task_id"`
	RequestID string `json:"request_id"`
}

func TaskRelayType(path string) string {
	if strings.Contains(path, "/videos") || strings.Contains(path, "/video/generations") {
		return "video_generation"
	}
	return ""
}

// ReserveTask is called after adaptor validation, before wallet billing or I/O.
// The task id is already host-generated. Unknown provider outcomes keep their
// slot for reconciliation; they must never fall through to another submission.
func ReserveTask(c *gin.Context, taskID, modelName, kind string) error {
	b := Default
	if b == nil || !b.cfg.EnforceCalls {
		return nil
	}
	id := c.GetString(common.RequestIdKey)
	if prior := c.GetString("ops_task_request"); prior != "" {
		if prior != id {
			return errors.New("Ops task request identity changed")
		}
		return nil
	}
	channelType := common.GetContextKeyInt(c, constant.ContextKeyChannelType)
	if channelType != constant.ChannelTypeDoubaoVideo && channelType != constant.ChannelTypeVolcEngine && channelType != constant.ChannelTypeOpenAISeedance {
		return &HTTPError{Status: http.StatusConflict}
	}
	if id == "" || taskID == "" || modelName == "" || kind == "" || c.GetInt("id") <= 0 {
		return errors.New("Ops task identity is unavailable")
	}
	if err := b.setRequest(id, "task_reserving"); err != nil {
		return err
	}
	b.mu.Lock()
	b.state.Tasks[taskID] = PendingTask{TaskID: taskID, RequestID: id}
	err := b.saveLocked()
	b.mu.Unlock()
	if err != nil {
		return err
	}
	body := map[string]any{"request_id": id, "downstream_customer_key": strconv.Itoa(c.GetInt("id")),
		"execution_mode": "task", "routing_context": map[string]any{"model_name": modelName, "call_type": kind}}
	if token := c.GetInt("token_id"); token > 0 {
		body["api_key_id"] = strconv.Itoa(token)
	}
	var decision struct {
		Decision string `json:"decision"`
	}
	if err := b.request(c.Request.Context(), "POST", "call-reservations", id, body, &decision); err != nil {
		// Nothing was submitted. A lost admission response is safe to cancel.
		_ = b.Enqueue("call-reservations/cancel", id, map[string]any{"request_id": id})
		_ = b.setRequest(id, "blocked")
		return err
	}
	if decision.Decision != "allowed" {
		b.mu.Lock()
		delete(b.state.Tasks, taskID)
		b.mu.Unlock()
		_ = b.setRequest(id, "blocked")
		return &HTTPError{Status: http.StatusTooManyRequests}
	}
	b.mu.Lock()
	b.state.Requests[id] = "task_reserved"
	err = b.saveLocked()
	b.mu.Unlock()
	if err != nil {
		return err
	}
	c.Set("ops_task_request", id)
	return nil
}

// RecoverInterruptedTasks runs before the delivery worker starts. A reservation
// that never reached provider dispatch is safe to cancel; a dispatched request
// has an unknown provider outcome and must remain a repair item.
func (b *Bridge) RecoverInterruptedTasks() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, status := range b.state.Requests {
		switch status {
		case "task_reserving", "task_reserved":
			if err := b.enqueueLocked("call-reservations/cancel", id, map[string]any{"request_id": id}, id, "cancel_queued"); err != nil {
				return err
			}
		case "task_dispatched":
			b.state.Requests[id] = "task_reconciliation"
		}
	}
	return b.saveLocked()
}

func BeforeTaskDispatch(c *gin.Context) error {
	id := c.GetString("ops_task_request")
	if id == "" {
		return nil
	}
	if err := Default.setRequest(id, "task_dispatched"); err != nil {
		return err
	}
	c.Set("ops_task_attempted", true)
	return nil
}

func FinishTaskSubmission(c *gin.Context, durable bool) {
	id := c.GetString("ops_task_request")
	if id == "" || Default == nil {
		return
	}
	b := Default
	b.mu.Lock()
	defer b.mu.Unlock()
	var err error
	current := b.state.Requests[id]
	if current == "cancel_queued" || current == "usage_queued" || current == "task_reconciliation" {
		return
	}
	if durable {
		b.state.Requests[id] = "task_waiting"
		err = b.saveLocked()
	} else if !c.GetBool("ops_task_attempted") || c.GetBool("ops_task_rejected") {
		err = b.enqueueLocked("call-reservations/cancel", id, map[string]any{"request_id": id}, id, "cancel_queued")
	} else {
		b.state.Requests[id] = "task_reconciliation"
		err = b.saveLocked()
	}
	if err != nil {
		common.SysError("Ops task state requires durable recovery")
	}
}

func (b *Bridge) TaskSucceeded(taskID string, record map[string]any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	task, ok := b.state.Tasks[taskID]
	if !ok && !b.cfg.EnforceCalls {
		batchID := "usage-ops-task-" + taskID
		body := map[string]any{"batchId": batchID, "adapter": "new-api", "adapterVersion": "1", "processing": "async", "records": []any{record}}
		return b.enqueueLocked("native-usage/bulk", batchID, body, "ops-task-"+taskID, "usage_queued")
	}
	if !ok || task.RequestID == "" {
		return errors.New("Ops task reservation is unavailable")
	}
	record["request_id"] = task.RequestID
	batchID := "usage-ops-task-" + taskID
	body := map[string]any{"batchId": batchID, "adapter": "new-api", "adapterVersion": "1", "processing": "async", "records": []any{record}}
	return b.enqueueLocked("native-usage/bulk", batchID, body, task.RequestID, "usage_queued")
}

func (b *Bridge) TaskUsageGap(taskID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	task, ok := b.state.Tasks[taskID]
	if !ok && !b.cfg.EnforceCalls {
		b.state.Requests["ops-task-"+taskID] = "needs_usage_evidence"
		return b.saveLocked()
	}
	if !ok || task.RequestID == "" {
		return errors.New("Ops task reservation is unavailable")
	}
	b.state.Requests[task.RequestID] = "task_reconciliation"
	return b.saveLocked()
}

func (b *Bridge) TaskFailed(taskID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	task, ok := b.state.Tasks[taskID]
	if !ok && !b.cfg.EnforceCalls {
		return nil
	}
	if !ok || task.RequestID == "" {
		return errors.New("Ops task reservation is unavailable")
	}
	return b.enqueueLocked("call-reservations/cancel", task.RequestID, map[string]any{"request_id": task.RequestID}, task.RequestID, "cancel_queued")
}

func (b *Bridge) PendingTasks() []PendingTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]PendingTask, 0, len(b.state.Tasks))
	for _, task := range b.state.Tasks {
		status := b.state.Requests[task.RequestID]
		if status == "task_waiting" || status == "task_dispatched" || status == "task_reconciliation" || status == "task_reserved" {
			result = append(result, task)
		}
	}
	return result
}

func (b *Bridge) RenewTasks(ctx context.Context) error {
	var first error
	for _, task := range b.PendingTasks() {
		err := b.request(ctx, "POST", "call-reservations/renew", task.RequestID, map[string]any{"request_id": task.RequestID}, nil)
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
