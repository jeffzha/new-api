package modelhealth

import (
	"context"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// Only the leased executor resumes a completed, platform-owned task. This never
// queues a generation POST or alters reservations, receipts or wallet entries.
func recoverCompletedVideo(ctx context.Context, now int64) error {
	if os.Getenv("MODEL_HEALTH_GATEWAY_URL") == "" {
		return nil
	}
	return model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		last, err := model.ReadModelHealthConfig(tx, "video-recheck-at", int64(0))
		if err != nil || now-last < 30 {
			return err
		}
		var runs []model.ModelHealthRun
		cursor, err := model.ReadModelHealthConfig(tx, "video-recheck-cursor", int64(0))
		if err != nil {
			return err
		}
		if err := tx.Where("id > ? AND state IN ? AND endpoint = ? AND upstream_task_id <> ? AND created_at >= ? AND evidence NOT LIKE ?", cursor, []string{"failed", "uncertain"}, "openai-video", "", now-7*86400, "%original_failure%").Order("id").Limit(50).Find(&runs).Error; err != nil {
			return err
		}
		if len(runs) == 0 {
			if err := model.SaveModelHealthConfig(tx, "video-recheck-cursor", "recovery", int64(0)); err != nil {
				return err
			}
		}
		for _, run := range runs {
			if err := model.SaveModelHealthConfig(tx, "video-recheck-cursor", "recovery", run.ID); err != nil {
				return err
			}
			target, err := findTarget(tx, run.ChannelID, run.ModelName, run.Endpoint)
			if IsValidation(err) {
				continue
			}
			if err != nil {
				return err
			}
			upstream, err := checkedURL(target.base)
			if target.Paused || err != nil || upstream.RawQuery != "" {
				continue
			}
			var p Template
			if common.UnmarshalJsonStr(run.Template, &p) != nil || !p.validFor(target, run.Endpoint) || !gatewayProbePath(p, target) || p.PollPath != "/v1/videos/{task_id}" || p.ResultField != "" {
				continue
			}
			owned, err := completedProbeTask(tx, run, p, target)
			if err != nil {
				return err
			}
			if !owned {
				continue
			}
			var trace []Trace
			if common.UnmarshalJsonStr(run.Evidence, &trace) != nil && run.Evidence != "" {
				continue
			}
			trace = append(trace, Trace{At: now, Stage: "original_failure", Response: map[string]any{"state": run.State, "stage": run.Stage, "reason": run.Reason, "http": run.StatusCode, "finished_at": run.FinishedAt}})
			data, err := common.Marshal(trace)
			if err != nil {
				return err
			}
			result := tx.Model(&model.ModelHealthRun{}).Where("id = ? AND state = ? AND evidence = ?", run.ID, run.State, run.Evidence).Updates(map[string]any{"state": "rechecking", "stage": "polling", "reason": "", "next_poll": now, "lease_until": 0, "evidence": string(data)})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				return model.SaveModelHealthConfig(tx, "video-recheck-at", "recovery", now)
			}
		}
		return model.SaveModelHealthConfig(tx, "video-recheck-at", "recovery", now)
	})
}

func completedProbeTask(db *gorm.DB, run model.ModelHealthRun, p Template, target Target) (bool, error) {
	if !identifier.MatchString(run.RequestID) || !identifier.MatchString(run.UpstreamTaskID) {
		return false, nil
	}
	token := gatewayProbeToken(db, p, target)
	if token == nil {
		return false, nil
	}
	var tasks []model.Task
	if err := db.Where("task_id = ?", run.UpstreamTaskID).Limit(2).Find(&tasks).Error; err != nil {
		return false, err
	}
	if len(tasks) != 1 {
		return false, nil
	}
	task := tasks[0]
	return task.Status == model.TaskStatusSuccess && task.UserId == token.UserId && task.ChannelId == run.ChannelID &&
		task.PrivateData.TokenId == token.Id && task.PrivateData.Execution != nil && task.PrivateData.Execution.RequestID == run.RequestID &&
		task.Properties.OriginModelName == run.ModelName && task.Properties.UpstreamModelName == run.UpstreamModel, nil
}
