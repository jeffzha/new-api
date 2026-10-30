package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	TaskCursor     int64 `json:"task_cursor"`
	Matched        int   `json:"matched"`
	NextSyncAt     int64 `json:"next_sync_at,omitempty"`
	FailedAttempts int   `json:"failed_attempts,omitempty"`
}

type locoPalLogRateLimitError struct{ retryAt int64 }

func (e *locoPalLogRateLimitError) Error() string {
	return "upstream log endpoint returned HTTP 429; synchronization deferred"
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
	if err == nil && result.NextSyncAt > time.Now().Unix() {
		result.Matched = 0
		if finishErr := model.FinishSystemTask(task.TaskID, runner, model.SystemTaskStatusSucceeded, result, ""); finishErr != nil {
			common.SysError("finish deferred LocoPal log reference sync failed: " + finishErr.Error())
		}
		return
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
		result.FailedAttempts = min(result.FailedAttempts+1, 5)
		result.NextSyncAt = time.Now().Add(min(5*time.Minute*time.Duration(1<<(result.FailedAttempts-1)), time.Hour)).Unix()
		var rateLimit *locoPalLogRateLimitError
		if errors.As(err, &rateLimit) {
			result.NextSyncAt = max(result.NextSyncAt, rateLimit.retryAt)
		}
	} else {
		result.NextSyncAt, result.FailedAttempts = 0, 0
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
				var rateLimit *locoPalLogRateLimitError
				if errors.As(fetchErr, &rateLimit) {
					return matched, errors.Join(failures...)
				}
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
	if resp.StatusCode == http.StatusTooManyRequests {
		now := time.Now()
		retryAt := now.Add(5 * time.Minute)
		value := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if seconds, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil && seconds > 0 {
			retryAt = now.Add(time.Duration(min(seconds, 24*60*60)) * time.Second)
		} else if date, parseErr := http.ParseTime(value); parseErr == nil && date.After(now) {
			retryAt = date
			if retryAt.After(now.Add(24 * time.Hour)) {
				retryAt = now.Add(24 * time.Hour)
			}
		}
		return 0, &locoPalLogRateLimitError{retryAt: retryAt.Unix()}
	}
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
