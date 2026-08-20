package opsmonitor

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Attempt struct {
	requestID       string
	nodeName        string
	startedAt       time.Time
	attemptIndex    int
	channelID       int
	channelType     int
	keyIndex        int
	modelName       string
	group           string
	switchedChannel bool
	waitDuration    time.Duration
	lease           *concurrencyLease
}

func BeginAttempt(c *gin.Context, info *relaycommon.RelayInfo, channel *model.Channel) (*Attempt, *types.NewAPIError) {
	ObserveRelay(c, info)
	if info == nil || channel == nil {
		return nil, nil
	}
	keyIndex := model.OpsConcurrencyAllKeys
	if info.ChannelMeta != nil && info.ChannelIsMultiKey {
		keyIndex = info.ChannelMultiKeyIndex
	}
	observation := getObservation(c)
	switched := false
	if observation != nil {
		observation.mu.Lock()
		account := [2]int{channel.Id, keyIndex}
		_, alreadySeen := observation.seenAccounts[account]
		switched = len(observation.seenAccounts) > 0 && !alreadySeen
		observation.seenAccounts[account] = struct{}{}
		if switched {
			observation.switchCount++
		}
		observation.retryCount = info.RetryIndex
		observation.channelID = channel.Id
		observation.channelType = channel.Type
		observation.channelKeyIndex = keyIndex
		observation.mu.Unlock()
	}

	waitStarted := time.Now()
	lease, err := acquireConcurrency(c.Request.Context(), info.UserId, channel.Id, keyIndex, info.RequestId)
	waitDuration := time.Since(waitStarted)
	if err != nil {
		return nil, types.NewErrorWithStatusCode(
			err,
			types.ErrorCodeConcurrencyLimit,
			http.StatusTooManyRequests,
			types.ErrOptionWithSkipRetry(),
		)
	}
	attempt := &Attempt{
		requestID:       c.GetString(common.RequestIdKey),
		nodeName:        common.NodeName,
		startedAt:       time.Now(),
		attemptIndex:    info.RetryIndex,
		channelID:       channel.Id,
		channelType:     channel.Type,
		keyIndex:        keyIndex,
		modelName:       info.OriginModelName,
		group:           info.UsingGroup,
		switchedChannel: switched,
		waitDuration:    waitDuration,
		lease:           lease,
	}
	return attempt, nil
}

func (attempt *Attempt) FinishRelay(err *types.NewAPIError) {
	if attempt == nil {
		return
	}
	statusCode := http.StatusOK
	errorCode := ""
	success := err == nil
	if err != nil {
		statusCode = err.StatusCode
		errorCode = string(err.GetErrorCode())
	}
	attempt.finish(statusCode, errorCode, success)
}

func (attempt *Attempt) FinishTask(err *dto.TaskError) {
	if attempt == nil {
		return
	}
	statusCode := http.StatusOK
	errorCode := ""
	success := err == nil
	if err != nil {
		statusCode = err.StatusCode
		errorCode = err.Code
	}
	attempt.finish(statusCode, errorCode, success)
}

func (attempt *Attempt) finish(statusCode int, errorCode string, success bool) {
	completedAt := time.Now()
	tracked := attempt.lease != nil && attempt.lease.tracked
	if attempt.lease != nil {
		attempt.lease.release()
	}
	markRuntimeAvailability(attempt.channelID, attempt.keyIndex, statusCode)
	enqueueAttempt(model.OpsUpstreamAttempt{
		RequestID:          attempt.requestID,
		NodeName:           attempt.nodeName,
		StartedAtMs:        attempt.startedAt.UnixMilli(),
		CompletedAtMs:      completedAt.UnixMilli(),
		AttemptIndex:       attempt.attemptIndex,
		ChannelID:          attempt.channelID,
		ChannelType:        attempt.channelType,
		ChannelKeyIndex:    attempt.keyIndex,
		ModelName:          attempt.modelName,
		Group:              attempt.group,
		StatusCode:         statusCode,
		Success:            success,
		SwitchedChannel:    attempt.switchedChannel,
		DurationMs:         completedAt.Sub(attempt.startedAt).Milliseconds(),
		ErrorCode:          errorCode,
		ConcurrencyWaitMs:  attempt.waitDuration.Milliseconds(),
		ConcurrencyTracked: tracked,
	})
}

var errConcurrencyQueueFull = errors.New("channel concurrency queue is full")
var errConcurrencyQueueTimeout = errors.New("channel concurrency wait timed out")
