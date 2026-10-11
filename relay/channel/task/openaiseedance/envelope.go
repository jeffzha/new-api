package openaiseedance

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// responseEnvelope mirrors the task-record envelope returned by OpenAI-compatible
// gateways that wrap their own task object (for example Uzoom's
// /v1/video/generations/{task_id}). The provider status, result URL and usage
// live inside the outer "data" object, while the raw upstream response is
// nested one level deeper under "data.data".
type responseEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		TaskID     string `json:"task_id"`
		Status     string `json:"status"`
		Progress   string `json:"progress"`
		ResultURL  string `json:"result_url"`
		FailReason string `json:"fail_reason"`
		Data       *struct {
			Status  string `json:"status"`
			Content struct {
				VideoURL string `json:"video_url"`
			} `json:"content"`
			Usage struct {
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"data"`
	} `json:"data"`
}

// normalizeUpstreamTaskStatus maps the status values used by OpenAI-compatible
// video gateways onto the lowercase values handled by the status switch.
func normalizeUpstreamTaskStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending", "not_start", "submitted":
		return "queued"
	case "processing", "in_progress", "running":
		return "in_progress"
	case "completed", "succeeded", "success":
		return "completed"
	case "failed", "failure", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

// applyTaskEnvelope flattens a wrapped task record into responseTask. It reports
// false when raw is not a recognizable envelope so callers can keep their
// existing unwrapped handling.
func applyTaskEnvelope(raw []byte, out *responseTask) bool {
	var envelope responseEnvelope
	if err := common.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return false
	}
	data := envelope.Data
	status := normalizeUpstreamTaskStatus(data.Status)
	if status == "" && data.Data != nil {
		status = normalizeUpstreamTaskStatus(data.Data.Status)
	}
	if data.TaskID != "" {
		out.TaskID = strings.TrimSpace(data.TaskID)
	}
	out.Status = status
	out.Progress = parseProgressPercent(data.Progress)
	out.ResultURL = strings.TrimSpace(data.ResultURL)
	if data.Data != nil {
		if out.ResultURL == "" {
			out.ResultURL = strings.TrimSpace(data.Data.Content.VideoURL)
		}
		out.Usage.CompletionTokens = data.Data.Usage.CompletionTokens
		out.Usage.TotalTokens = data.Data.Usage.TotalTokens
	}
	if status == "failed" {
		out.Error = &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Message: strings.TrimSpace(data.FailReason)}
	}
	return status != ""
}

// parseProgressPercent reads the numeric portion of a "NN%" progress string.
func parseProgressPercent(raw string) int {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}
