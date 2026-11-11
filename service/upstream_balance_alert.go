package service

import (
	"errors"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/upstreamalerts"
)

// Called only by the winner of a task's terminal status transition. The
// failure is an error event, separate from settlement/refund and never a charge.
func recordTaskUpstreamBalanceFailure(task *model.Task) {
	if task == nil || task.Status != model.TaskStatusFailure {
		return
	}
	providerError := types.NewOpenAIError(errors.New(task.FailReason), types.ErrorCodeBadResponse, 400)
	other := model.NewLogOther()
	requestID := task.TaskID
	if execution := task.PrivateData.Execution; execution != nil && execution.RequestID != "" {
		requestID = execution.RequestID
	}
	alert := upstreamalerts.FromRelayError(providerError)
	if alert == nil || task.ChannelId <= 0 {
		return
	}
	// A terminal task reason is not an HTTP failure. Do not fabricate an
	// upstream status code from the successful polling response.
	alert.StatusCode, alert.ErrorCode = 0, "task_failed"
	alert.RequestID, alert.ChannelID = requestID, task.ChannelId
	other.SetAdmin("upstream_alert", alert)
	other.SetPublic("task_id", task.TaskID)
	model.CaptureUpstreamLogReference(other, task.PrivateData.UpstreamBaseURL)
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId: task.UserId, LogType: model.LogTypeError, Content: "上游返回账户余额或付费额度不足",
		ChannelId: task.ChannelId, ModelName: taskModelName(task),
		TokenId: task.PrivateData.TokenId, Group: task.Group, Other: other,
	})
}
