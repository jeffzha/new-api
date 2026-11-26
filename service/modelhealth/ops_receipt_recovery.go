package modelhealth

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/QuantumNous/new-api/service/opsfees"
	"gorm.io/gorm"
)

// Recovery is separate from UI reads: at most one original charge query per
// 30 seconds across HTTP slots, inside the shared OPS background read budget.
func recoverProbeOpsReceipts(ctx context.Context) error {
	if !model.OpsFeesEnabled() {
		return nil
	}
	c, err := opsfees.LoadConfig()
	if err != nil {
		return err
	}
	client := opsclient.New(c.BaseURL, c.KeyID, c.Secret, c.Timeout)
	if err := client.EnableReadBudget(); err != nil {
		return err
	}
	return syncProbeOpsReceipts(ctx, c, client, time.Now())
}

func syncProbeOpsReceipts(ctx context.Context, config opsfees.Config, client *opsclient.Client, at time.Time) error {
	type recoveryState struct {
		Next  int64
		After int64
	}
	var state recoveryState
	claimed := false
	key := "ops-recovery-" + hash(config.Connection)[:48]
	err := model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		var err error
		state, err = model.ReadModelHealthConfig(tx, key, recoveryState{})
		if err != nil || state.Next > at.Unix() {
			return err
		}
		state.Next = at.Add(30 * time.Second).Unix()
		claimed = true
		return model.SaveModelHealthConfig(tx, key, "internal", state)
	})
	if err != nil || !claimed {
		return err
	}
	policy, err := model.ReadModelHealthConfig(model.DB.WithContext(ctx), "auto-templates", AutoTemplatePolicy{})
	if err != nil || !policy.Enabled || policy.TokenID < 1 {
		return err
	}
	var token model.Token
	if err := model.DB.WithContext(ctx).Select("id", "user_id", "name").Where("id = ?", policy.TokenID).Find(&token).Error; err != nil {
		return err
	}
	if token.UserId <= 0 || !strings.HasPrefix(token.Name, "model-health/") {
		return nil
	}
	var runs []model.ModelHealthRun
	err = model.DB.WithContext(ctx).Table("model_health_runs AS h").Select("h.*").
		Joins("LEFT JOIN ops_fees AS f ON f.request_id = h.request_id AND f.connection = ?", config.Connection).
		Where("h.id > ? AND h.created_at >= ? AND h.state IN ? AND h.request_id <> ''", state.After, config.Since, []string{"succeeded", "failed"}).
		Where("f.request_id IS NULL OR f.amount IS NULL OR f.status <> ? OR (h.state = ? AND h.upstream_task_id <> '' AND f.refund_amount IS NULL AND EXISTS (SELECT 1 FROM tasks t WHERE t.task_id = h.upstream_task_id AND t.channel_id = h.channel_id AND t.status = ?))", "available", "failed", model.TaskStatusFailure).Order("h.id").Limit(25).Find(&runs).Error
	if err != nil {
		return err
	}
	for _, run := range runs {
		state.After = run.ID
		if !identifier.MatchString(run.RequestID) || run.ChannelID <= 0 {
			continue
		}
		if run.UpstreamTaskID != "" {
			var task model.Task
			if err := model.DB.WithContext(ctx).Where("task_id = ? AND channel_id = ? AND user_id = ?", run.UpstreamTaskID, run.ChannelID, token.UserId).Find(&task).Error; err != nil {
				return err
			}
			if task.ID == 0 || task.PrivateData.TokenId != token.Id || task.PrivateData.Execution == nil || task.PrivateData.Execution.RequestID != run.RequestID || task.Properties.OriginModelName != run.ModelName || task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure {
				continue
			}
		} else {
			if model.LOG_DB == nil {
				continue
			}
			var count int64
			if err := model.LOG_DB.WithContext(ctx).Model(&model.Log{}).Where("request_id = ? AND user_id = ? AND token_id = ? AND channel_id = ? AND model_name = ? AND type = ?", run.RequestID, token.UserId, token.Id, run.ChannelID, run.ModelName, model.LogTypeConsume).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				continue
			}
		}
		row, readErr := opsfees.RecoverReceipt(ctx, client, config.Connection, run.RequestID, strconv.Itoa(token.UserId), run.ModelName, at)
		if readErr != nil {
			if _, deferred := opsclient.ReadDeferred(readErr); deferred {
				state.After = run.ID - 1
			}
		} else if row != nil {
			readErr = opsfees.PersistRecoveredReceipt(ctx, *row)
		}
		if err := model.SaveModelHealthConfig(model.DB.WithContext(ctx), key, "internal", state); err != nil {
			return err
		}
		return readErr
	}
	if len(runs) < 25 {
		state.After = 0
	}
	return model.SaveModelHealthConfig(model.DB.WithContext(ctx), key, "internal", state)
}
