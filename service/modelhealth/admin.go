package modelhealth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type Plan struct {
	Template Template `json:"template"`
	Revision string   `json:"revision"`
	Rule     Rule     `json:"rule"`
	Scope    string   `json:"scope"`
	Kind     string   `json:"kind"`
	Ready    bool     `json:"ready"`
	Blocked  []string `json:"blocked"`
}

func GetPlan(ctx context.Context, s Selection) (Plan, error) {
	db := model.DB.WithContext(ctx)
	t, err := findTarget(db, s.Channel, s.Model, s.Endpoint)
	if err != nil {
		return Plan{}, err
	}
	p, err := readTemplate(db, t, s.Endpoint)
	if err != nil {
		return Plan{}, err
	}
	r, scope, err := effectiveRule(db, t, s.Endpoint)
	if err != nil {
		return Plan{}, err
	}
	worker, err := readWorkerState(db)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now().Unix()
	online := worker.active(now)
	blocked := []string{}
	if !online {
		blocked = append(blocked, "Monitoring worker is disabled")
	}
	if t.Paused {
		blocked = append(blocked, "Channel monitoring is paused")
	}
	if !p.validFor(t, s.Endpoint) {
		if p.Credential != "" && p.Binding == templateBinding(t, s.Endpoint) && !p.MinimumConfirmed {
			reason := p.DraftReason
			if reason == "" {
				reason = "Confirm the model-specific minimum request and cost ceiling"
			}
			blocked = append(blocked, reason)
		} else {
			blocked = append(blocked, "Template required")
		}
	} else {
		if online && !worker.hasProbe(p, t) {
			blocked = append(blocked, "Dedicated probe credential missing or not bound to this upstream")
		}
		b, err := readBudget(db)
		if err != nil {
			return Plan{}, err
		}
		limits, err := admissionBlocks(db, t, s.Endpoint, p, r, b, now)
		if err != nil {
			return Plan{}, err
		}
		blocked = append(blocked, limits...)
	}
	p.Selection = s
	return Plan{Template: p, Revision: templateRevision(p), Rule: r, Scope: scope, Kind: t.Type, Ready: len(blocked) == 0, Blocked: blocked}, nil
}

func GetBudget(ctx context.Context) (Budget, error) {
	return readBudget(model.DB.WithContext(ctx))
}

func readBudget(db *gorm.DB) (Budget, error) {
	b, err := model.ReadModelHealthConfig(db, "budget", Budget{Concurrent: 1, ChannelRPM: 1, ChannelTPM: 1024})
	if err != nil {
		return b, err
	}
	c, err := model.ReadModelHealthConfig(db, "cycle-budget", cycleBudgetConfig{})
	if c.Override {
		b.Run, b.Day, b.Month, b.Cycle = c.Run, c.Amount, c.Amount, true
	}
	return b, err
}

func GetRule(ctx context.Context, scope string) (Rule, error) {
	db := model.DB.WithContext(ctx)
	if scope != "" && scope != "global" {
		rule, _, err := resolveRule(db, scope, "type-"+scope)
		return rule, err
	}
	return model.ReadModelHealthConfig(db, "global", defaultRule)
}

const notificationPageSize = 20

type NotificationPage[T any] struct {
	Items   []T  `json:"items"`
	HasMore bool `json:"has_more"`
}

func ListDestinations(ctx context.Context, page int) (NotificationPage[Destination], error) {
	db := model.DB.WithContext(ctx).Offset((min(10000, max(1, page)) - 1) * notificationPageSize).Limit(notificationPageSize + 1)
	rows, err := destinations(db)
	return NotificationPage[Destination]{Items: rows[:min(notificationPageSize, len(rows))], HasMore: page < 10000 && len(rows) > notificationPageSize}, err
}

func ListDeliveries(ctx context.Context, page int) (NotificationPage[model.ModelHealthDelivery], error) {
	rows := []model.ModelHealthDelivery{}
	err := model.DB.WithContext(ctx).Select("id", "target", "event", "state", "created_at", "status", "attempts", "next_at").Order("created_at desc, id desc").Offset((min(10000, max(1, page)) - 1) * notificationPageSize).Limit(notificationPageSize + 1).Find(&rows).Error
	return NotificationPage[model.ModelHealthDelivery]{Items: rows[:min(notificationPageSize, len(rows))], HasMore: page < 10000 && len(rows) > notificationPageSize}, err
}

func ListRuns(ctx context.Context, source string, page int) ([]model.ModelHealthRun, error) {
	rows := []model.ModelHealthRun{}
	q := model.DB.WithContext(ctx).Select("id", "target", "channel_id", "model_name", "upstream_model", "endpoint", "source", "state", "stage", "reason", "status_code", "created_at", "started_at", "finished_at", "reservation_micro", "actual_micro", "request_id", "upstream_task_id")
	if source != "" {
		q = q.Where("source = ?", source)
	}
	err := q.Order("id desc").Offset((min(10000, max(1, page)) - 1) * 25).Limit(26).Find(&rows).Error
	return rows, err
}

func Reconcile(ctx context.Context, id int64, actual int64) error {
	if actual < 0 || actual > 1000000000000 {
		return invalid("Invalid verified cost")
	}
	return model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		q := tx.Model(&model.ModelHealthRun{}).Where("id = ? AND state = ? AND actual_micro IS NULL", id, "uncertain").Updates(map[string]any{"actual_micro": actual, "state": "failed"})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected == 0 {
			q = tx.Model(&model.ModelHealthRun{}).Where("id = ? AND state IN ? AND actual_micro IS NULL", id, []string{"failed", "succeeded"}).Update("actual_micro", actual)
			if q.Error != nil {
				return q.Error
			}
		}
		if q.RowsAffected == 0 {
			return invalid("This run cannot be reconciled")
		}
		return nil
	})
}

func Evidence(ctx context.Context, id int64) (string, error) {
	var r model.ModelHealthRun
	if err := model.DB.WithContext(ctx).First(&r, id).Error; err != nil {
		return "", err
	}
	var traces []Trace
	_ = common.UnmarshalJsonStr(r.Evidence, &traces)
	var b strings.Builder
	fmt.Fprintf(&b, "# 上游排查：模型健康检测\n\n## 事件摘要\n\n- 渠道: %q (#%d)\n- 平台模型: %q\n- 上游模型: %q\n- 接口: %q\n- 来源: %s\n- 阶段 / 状态: %s / %s\n- 原因: %q\n- 最后业务接口 HTTP: %s\n- 上游请求 ID: %s\n- 上游任务 ID: %s\n- 创建时间（北京时间）: %s\n", evidenceChannel(ctx, r.ChannelID), r.ChannelID, r.ModelName, r.UpstreamModel, r.Endpoint, r.Source, r.Stage, r.State, r.Reason, observedHTTP(r.StatusCode), available(r.RequestID), available(r.UpstreamTaskID), time.Unix(r.CreatedAt, 0).In(cst).Format(time.RFC3339))
	if r.StartedAt > 0 && r.FinishedAt >= r.StartedAt {
		fmt.Fprintf(&b, "- 检测总耗时（含轮询等待）: %d 秒\n", r.FinishedAt-r.StartedAt)
	}
	if r.Reason == "video_download_content_type_mismatch" {
		b.WriteString("\n成片数据有 MP4 `ftyp` 文件标识，但下载未返回 `video/mp4`。这可能触发客户端类型校验；仅凭文件标识不能确认完整视频能播放。\n")
	}
	b.WriteString("\n## 请求与响应时间线\n\n只保留排查所需字段。正文中的自由文本、凭据、签名链接和素材内容不对外提供。\n\n")
	if len(traces) == 0 || time.Now().Unix()-r.CreatedAt > 7*86400 {
		b.WriteString("未采集或已超过脱敏证据保留期。\n")
	} else {
		for _, s := range traces {
			fmt.Fprintf(&b, "### %s / %s\n\n- 请求: %s `%s`\n- 主机: %q\n- 响应: %s / %s\n", time.Unix(s.At, 0).In(cst).Format(time.RFC3339), s.Stage, s.Method, s.Path, available(s.Host), available(s.Protocol), observedHTTP(s.HTTP))
			if s.WaitMs != nil {
				fmt.Fprintf(&b, "- 等待响应头: %d ms\n", *s.WaitMs)
			}
			if s.DurationMs != nil {
				fmt.Fprintf(&b, "- 请求总耗时: %d ms\n", *s.DurationMs)
			}
			writeEvidenceJSON(&b, "请求头", s.RequestHeaders)
			writeEvidenceJSON(&b, "请求体", s.Request)
			writeEvidenceJSON(&b, "响应头", s.ResponseHeaders)
			writeEvidenceJSON(&b, "响应体/内容摘要", s.Response)
		}
	}
	b.WriteString("\n## 请上游协助核查\n\n1. 按时间、请求 ID 和任务 ID 核对是否受理、失败步骤和扣费。\n2. 如生成成功但下载失败，请核对对象存储及 CDN 的 HTTP 状态、Content-Type、Content-Range，并验证完整文件能下载、解码和播放。\n3. 请说明是否能修正已有成片及后续上传配置。\n\n重发 POST 可能创建新的付费任务，请勿直接重放。\n")
	return b.String(), nil
}

func available(v string) string {
	if v == "" {
		return "未采集"
	}
	return v
}

func evidenceChannel(ctx context.Context, id int) string {
	var channel model.Channel
	if err := model.DB.WithContext(ctx).Select("name").Take(&channel, "id = ?", id).Error; err != nil {
		return available("")
	}
	return available(channel.Name)
}

func UserEvidence(ctx context.Context, id int64) (string, error) {
	db := model.DB.WithContext(ctx)
	var event model.OpsRequestEvent
	err := db.Select("id", "request_id", "occurred_at_ms", "completed_at_ms", "channel_id", "model_name", "upstream_model_name", "endpoint_type", "method", "status_code", "upstream_status_code", "error_code", "error_owner", "task_id", "user_id", "retry_count", "success", "duration_ms", "ttft_ms", "has_ttft").First(&event, "id = ? AND channel_id > 0 AND model_name <> ?", id, "").Error
	if err != nil {
		return "", err
	}
	var attempts []model.OpsUpstreamAttempt
	if event.RequestID != "" {
		err = db.Select("channel_id", "attempt_index", "status_code", "started_at_ms", "duration_ms", "concurrency_wait_ms", "concurrency_tracked", "success").Where("request_id = ? AND channel_id = ?", event.RequestID, event.ChannelID).Order("attempt_index, id").Limit(20).Find(&attempts).Error
		if err != nil {
			return "", err
		}
	}
	var b strings.Builder
	upstreamTaskID := ""
	var artifactSummary any
	if identifier.MatchString(event.TaskID) && event.UserID > 0 {
		var tasks []model.Task
		err := db.Select("private_data", "status", "submit_time", "finish_time").Where("task_id = ? AND user_id = ? AND channel_id = ?", event.TaskID, event.UserID, event.ChannelID).Limit(2).Find(&tasks).Error
		if err == nil && len(tasks) == 1 {
			task := tasks[0]
			if identifier.MatchString(task.PrivateData.UpstreamTaskID) {
				upstreamTaskID = task.PrivateData.UpstreamTaskID
			}
			artifactSummary = map[string]any{"status": task.Status, "submitted_at": task.SubmitTime, "finished_at": task.FinishTime}
		}
	}
	fmt.Fprintf(&b, "# 用户调用排查证据\n\n- 平台请求 ID: %q\n- 时间: %s\n- 渠道: %q (#%d)\n- 模型: %q → %q\n- 接口类型: %q %q\n- 平台 HTTP: %d\n- 上游 HTTP: %s\n- 错误码: %q\n- 归因（采集时）: %q\n- 平台任务 ID: %q\n- 上游任务 ID: %q\n- 平台重试次数: %d\n\n", available(event.RequestID), time.UnixMilli(event.OccurredAtMs).In(cst).Format(time.RFC3339Nano), evidenceChannel(ctx, event.ChannelID), event.ChannelID, event.ModelName, event.UpstreamModelName, event.Method, event.EndpointType, event.StatusCode, observedHTTP(event.UpstreamStatusCode), available(event.ErrorCode), available(event.ErrorOwner), available(event.TaskID), available(upstreamTaskID), event.RetryCount)
	fmt.Fprintf(&b, "- 平台请求耗时: %d ms\n", event.DurationMs)
	if event.HasTTFT {
		fmt.Fprintf(&b, "- 首 Token 等待: %d ms\n", event.TTFTMs)
	}
	if len(attempts) > 0 {
		b.WriteString("\n## 本渠道的上游尝试（提交成功不是生成成功）\n\n")
		for _, attempt := range attempts {
			fmt.Fprintf(&b, "- %s · 渠道 %d · 尝试 %d · HTTP %s · %d ms\n", time.UnixMilli(attempt.StartedAtMs).In(cst).Format(time.RFC3339Nano), attempt.ChannelID, attempt.AttemptIndex+1, observedHTTP(attempt.StatusCode), attempt.DurationMs)
			if attempt.ConcurrencyTracked {
				fmt.Fprintf(&b, "  - 平台并发等待: %d ms\n", attempt.ConcurrencyWaitMs)
			}
		}
	}
	if artifactSummary != nil {
		writeEvidenceJSON(&b, "平台任务状态（非原始 HTTP 响应；时间为 Unix 秒）", artifactSummary)
	}
	b.WriteString("\n## HTTP 采集范围\n\n请求头、请求体、响应头、响应体：此用户调用历史未采集。不会导出客户原文、凭据或素材，也不会重发客户请求以补齐证据。\n\n## 请上游协助核查\n\n请按请求时间、模型、请求 ID 和任务 ID 核实是否受理、失败阶段、HTTP 响应及费用。涉及成片下载，请同时核查返回的 Content-Type 和部分/完整文件下载结果。\n")
	return b.String(), nil
}

func observedHTTP(status int) string {
	if status <= 0 {
		return "未采集"
	}
	return fmt.Sprint(status)
}
