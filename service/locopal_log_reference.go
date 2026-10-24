package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

const locoPalLogSyncTaskType = "locopal_log_reference_sync"

type locoPalLogSyncResult struct {
	TaskCursor int64 `json:"task_cursor"`
	Matched    int   `json:"matched"`
}

type locoPalLogSyncHandler struct{}

func (locoPalLogSyncHandler) Type() string            { return locoPalLogSyncTaskType }
func (locoPalLogSyncHandler) Interval() time.Duration { return time.Minute }
func (locoPalLogSyncHandler) NewPayload() any         { return nil }
func (locoPalLogSyncHandler) Enabled() bool {
	if !common.GetEnvOrDefaultBool("LOCOPAL_LOG_REFERENCE_SYNC_ENABLED", true) {
		return false
	}
	var task model.Task
	return model.DB.Select("id").Where("platform = ?", model.LocoPalTaskPlatform).Limit(1).Find(&task).Error == nil && task.ID != 0
}

func init() { RegisterSystemTaskHandler(locoPalLogSyncHandler{}) }

func (locoPalLogSyncHandler) Run(ctx context.Context, task *model.SystemTask, runner string) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var previous model.SystemTask
	err := model.DB.WithContext(ctx).Select("result").Where("type = ? AND status IN ? AND result <> ?", locoPalLogSyncTaskType, []model.SystemTaskStatus{model.SystemTaskStatusSucceeded, model.SystemTaskStatusFailed}, "").Order("id desc").Take(&previous).Error
	var result locoPalLogSyncResult
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	} else if err == nil {
		err = common.UnmarshalJsonStr(previous.Result, &result)
	}
	if err == nil {
		result.TaskCursor, err = model.IndexLocoPalTaskReferences(ctx, result.TaskCursor)
	}
	result.Matched = 0
	if err == nil {
		result.Matched, err = syncLocoPalLogReferences(ctx)
	}
	status, message := model.SystemTaskStatusSucceeded, ""
	if err != nil {
		status, message = model.SystemTaskStatusFailed, err.Error()
	}
	if finishErr := model.FinishSystemTask(task.TaskID, runner, status, result, message); finishErr != nil {
		common.SysError("finish LocoPal log reference sync failed: " + finishErr.Error())
	}
}

func syncLocoPalLogReferences(ctx context.Context) (int, error) {
	var channels []model.Channel
	if err := model.DB.WithContext(ctx).Where("type = ?", constant.ChannelTypeTaskPlugin).Find(&channels).Error; err != nil {
		return 0, err
	}
	matched := 0
	var failures []error
	for _, channel := range channels {
		if err := ctx.Err(); err != nil {
			return matched, errors.Join(append(failures, err)...)
		}
		var settings dto.ChannelSettings
		if channel.Setting == nil || common.UnmarshalJsonStr(*channel.Setting, &settings) != nil || settings.TaskPluginKey != model.LocoPalTaskPlatform || channel.BaseURL == nil {
			continue
		}
		base, err := url.Parse(*channel.BaseURL)
		if err != nil || base.Scheme != "https" || !strings.EqualFold(base.Hostname(), "locopalai.com") || base.User != nil {
			continue
		}
		client, err := GetHttpClientWithProxySettings(settings.Proxy, settings)
		if err != nil {
			failures = append(failures, fmt.Errorf("channel %d: invalid log sync proxy configuration", channel.Id))
			continue
		}
		keys := channel.GetKeys()
		if len(keys) > 32 {
			failures = append(failures, fmt.Errorf("channel %d: log sync key limit exceeded", channel.Id))
			continue
		}
		for i, key := range keys {
			if err := ctx.Err(); err != nil {
				return matched, errors.Join(append(failures, err)...)
			}
			if strings.TrimSpace(key) == "" {
				continue
			}
			if state, exists := channel.ChannelInfo.MultiKeyStatusList[i]; channel.ChannelInfo.IsMultiKey && exists && state != common.ChannelStatusEnabled {
				continue
			}
			count, fetchErr := fetchLocoPalLogReferences(ctx, client, base.ResolveReference(&url.URL{Path: "/api/log/token"}).String(), strings.TrimSpace(key), channel.Id)
			matched += count
			if fetchErr != nil {
				failures = append(failures, fmt.Errorf("channel %d: %w", channel.Id, fetchErr))
			}
		}
	}
	return matched, errors.Join(failures...)
}

func fetchLocoPalLogReferences(ctx context.Context, client *http.Client, endpoint, key string, channel int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, errors.New("invalid log sync endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	// Never forward a channel credential through a provider redirect.
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := bounded.Do(req)
	if err != nil {
		return 0, errors.New("upstream log endpoint unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("upstream log endpoint returned HTTP %d", resp.StatusCode)
	}
	const maxBody = 8 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return 0, errors.New("upstream log response unreadable or too large")
	}
	var response struct {
		Success bool `json:"success"`
		Data    []struct {
			RequestID string          `json:"request_id"`
			Type      int             `json:"type"`
			Other     json.RawMessage `json:"other"`
		} `json:"data"`
	}
	if common.Unmarshal(body, &response) != nil || !response.Success || len(response.Data) > 2000 {
		return 0, errors.New("upstream log response invalid")
	}
	matched := 0
	for _, log := range response.Data {
		if log.Type != model.LogTypeConsume || len(log.Other) == 0 {
			continue
		}
		other := log.Other
		if common.GetJsonType(other) == "string" {
			var text string
			if common.Unmarshal(other, &text) != nil {
				continue
			}
			other = []byte(text)
		}
		var meta struct {
			TaskID string `json:"task_id"`
		}
		if common.Unmarshal(other, &meta) != nil {
			continue
		}
		linked, linkErr := model.LinkUpstreamSettlementRequest(ctx, channel, meta.TaskID, log.RequestID)
		if linkErr != nil {
			return matched, linkErr
		}
		if linked {
			matched++
		}
	}
	return matched, nil
}
