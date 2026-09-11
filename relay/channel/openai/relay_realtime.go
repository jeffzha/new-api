package openai

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}

	info.IsStream = true
	clientConn := info.ClientWs
	targetConn := info.TargetWs

	clientClosed := make(chan struct{})
	targetClosed := make(chan struct{})
	sendChan := make(chan []byte, 100)
	receiveChan := make(chan []byte, 100)
	errChan := make(chan error, 2)

	usage := &dto.RealtimeUsage{}
	localUsage := &dto.RealtimeUsage{}
	sumUsage := &dto.RealtimeUsage{}
	// OpenAI Realtime response.done usage is cumulative for the session. Keep
	// the last accepted frame so repeated cumulative frames become a no-op and
	// only the positive component delta is reserved.
	var lastUpstreamUsage *dto.RealtimeUsage

	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in client reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := clientConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from client: %v", err)
					}
					close(clientClosed)
					return
				}

				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate {
					if realtimeEvent.Session != nil {
						if realtimeEvent.Session.Tools != nil {
							info.RealtimeTools = realtimeEvent.Session.Tools
						}
					}
				}

				textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
				if err != nil {
					errChan <- fmt.Errorf("error counting text token: %v", err)
					return
				}
				logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
				localUsage.TotalTokens += textToken + audioToken
				localUsage.InputTokens += textToken + audioToken
				localUsage.InputTokenDetails.TextTokens += textToken
				localUsage.InputTokenDetails.AudioTokens += audioToken

				err = helper.WssString(c, targetConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to target: %v", err)
					return
				}

				select {
				case sendChan <- message:
				default:
				}
			}
		}
	})

	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in target reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := targetConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from target: %v", err)
					}
					close(targetClosed)
					return
				}
				info.SetFirstResponseTime()
				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeResponseDone {
					realtimeUsage := realtimeEvent.Response.Usage
					if realtimeUsage != nil {
						current := *realtimeUsage
						// A provider retry may resend the exact same cumulative frame.
						// Forward it to the client but do not reserve or journal it.
						if lastUpstreamUsage != nil && current == *lastUpstreamUsage {
							usage = &dto.RealtimeUsage{}
						} else if lastUpstreamUsage != nil && !realtimeUsageMonotonic(&current, lastUpstreamUsage) {
							// A cumulative counter moving backwards is not a valid
							// delta frame. Preserve the last baseline so a later
							// retry cannot turn the rollback into a second charge.
							logger.LogWarn(c, "ignoring non-monotonic realtime usage frame")
							usage = &dto.RealtimeUsage{}
						} else {
							delta := realtimeUsageDelta(&current, lastUpstreamUsage)
							err := preConsumeUsageWithCumulative(c, info, &delta, sumUsage, &current)
							if err != nil {
								errChan <- fmt.Errorf("error consume usage: %v", err)
								return
							}
							last := current
							lastUpstreamUsage = &last
							usage = &dto.RealtimeUsage{}
							localUsage = &dto.RealtimeUsage{}
						}
					} else {
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							errChan <- fmt.Errorf("error counting text token: %v", err)
							return
						}
						logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						info.IsFirstRequest = false
						localUsage.InputTokens += textToken + audioToken
						localUsage.InputTokenDetails.TextTokens += textToken
						localUsage.InputTokenDetails.AudioTokens += audioToken
						err = preConsumeUsage(c, info, localUsage, sumUsage)
						if err != nil {
							errChan <- fmt.Errorf("error consume usage: %v", err)
							return
						}
						// 本次计费完成，清除
						localUsage = &dto.RealtimeUsage{}
						// print now usage
					}
					logger.LogInfo(c, fmt.Sprintf("realtime streaming sumUsage: %v", sumUsage))
					logger.LogInfo(c, fmt.Sprintf("realtime streaming localUsage: %v", localUsage))
					logger.LogInfo(c, fmt.Sprintf("realtime streaming localUsage: %v", localUsage))

				} else if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdated || realtimeEvent.Type == dto.RealtimeEventTypeSessionCreated {
					realtimeSession := realtimeEvent.Session
					if realtimeSession != nil {
						// update audio format
						info.InputAudioFormat = common.GetStringIfEmpty(realtimeSession.InputAudioFormat, info.InputAudioFormat)
						info.OutputAudioFormat = common.GetStringIfEmpty(realtimeSession.OutputAudioFormat, info.OutputAudioFormat)
					}
				} else {
					textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
					if err != nil {
						errChan <- fmt.Errorf("error counting text token: %v", err)
						return
					}
					logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.TotalTokens += textToken + audioToken
					localUsage.OutputTokens += textToken + audioToken
					localUsage.OutputTokenDetails.TextTokens += textToken
					localUsage.OutputTokenDetails.AudioTokens += audioToken
				}

				err = helper.WssString(c, clientConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to client: %v", err)
					return
				}

				select {
				case receiveChan <- message:
				default:
				}
			}
		}
	})

	select {
	case <-clientClosed:
	case <-targetClosed:
	case err := <-errChan:
		//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
		logger.LogError(c, "realtime error: "+err.Error())
	case <-c.Done():
	}

	if usage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, usage, sumUsage)
	}

	if localUsage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, localUsage, sumUsage)
	}

	// check usage total tokens, if 0, use local usage

	return nil, sumUsage
}

func preConsumeUsage(ctx *gin.Context, info *relaycommon.RelayInfo, usage *dto.RealtimeUsage, totalUsage *dto.RealtimeUsage) error {
	return preConsumeUsageWithCumulative(ctx, info, usage, totalUsage, nil)
}

// realtimeUsageDelta converts an upstream cumulative usage frame into the
// incremental segment charged by the gateway. Provider retries can resend a
// lower or identical cumulative value; those fields produce a zero delta and
// therefore cannot create a negative refund or duplicate charge.
func realtimeUsageDelta(current, previous *dto.RealtimeUsage) dto.RealtimeUsage {
	if current == nil {
		return dto.RealtimeUsage{}
	}
	if previous == nil {
		return *current
	}
	return dto.RealtimeUsage{
		TotalTokens:  nonNegativeUsageDelta(current.TotalTokens, previous.TotalTokens),
		InputTokens:  nonNegativeUsageDelta(current.InputTokens, previous.InputTokens),
		OutputTokens: nonNegativeUsageDelta(current.OutputTokens, previous.OutputTokens),
		InputTokenDetails: dto.InputTokenDetails{
			CachedTokens:         nonNegativeUsageDelta(current.InputTokenDetails.CachedTokens, previous.InputTokenDetails.CachedTokens),
			CacheWriteTokens:     nonNegativeUsageDelta(current.InputTokenDetails.CacheWriteTokens, previous.InputTokenDetails.CacheWriteTokens),
			CachedCreationTokens: nonNegativeUsageDelta(current.InputTokenDetails.CachedCreationTokens, previous.InputTokenDetails.CachedCreationTokens),
			TextTokens:           nonNegativeUsageDelta(current.InputTokenDetails.TextTokens, previous.InputTokenDetails.TextTokens),
			AudioTokens:          nonNegativeUsageDelta(current.InputTokenDetails.AudioTokens, previous.InputTokenDetails.AudioTokens),
			ImageTokens:          nonNegativeUsageDelta(current.InputTokenDetails.ImageTokens, previous.InputTokenDetails.ImageTokens),
		},
		OutputTokenDetails: dto.OutputTokenDetails{
			TextTokens:      nonNegativeUsageDelta(current.OutputTokenDetails.TextTokens, previous.OutputTokenDetails.TextTokens),
			AudioTokens:     nonNegativeUsageDelta(current.OutputTokenDetails.AudioTokens, previous.OutputTokenDetails.AudioTokens),
			ImageTokens:     nonNegativeUsageDelta(current.OutputTokenDetails.ImageTokens, previous.OutputTokenDetails.ImageTokens),
			ReasoningTokens: nonNegativeUsageDelta(current.OutputTokenDetails.ReasoningTokens, previous.OutputTokenDetails.ReasoningTokens),
		},
	}
}

func nonNegativeUsageDelta(current, previous int) int {
	if current <= 0 {
		return 0
	}
	if previous < 0 {
		return current
	}
	if current <= previous {
		return 0
	}
	return current - previous
}

func realtimeUsageMonotonic(current, previous *dto.RealtimeUsage) bool {
	if current == nil || previous == nil {
		return true
	}
	return current.TotalTokens >= previous.TotalTokens &&
		current.InputTokens >= previous.InputTokens &&
		current.OutputTokens >= previous.OutputTokens &&
		current.InputTokenDetails.CachedTokens >= previous.InputTokenDetails.CachedTokens &&
		current.InputTokenDetails.CacheWriteTokens >= previous.InputTokenDetails.CacheWriteTokens &&
		current.InputTokenDetails.CachedCreationTokens >= previous.InputTokenDetails.CachedCreationTokens &&
		current.InputTokenDetails.TextTokens >= previous.InputTokenDetails.TextTokens &&
		current.InputTokenDetails.AudioTokens >= previous.InputTokenDetails.AudioTokens &&
		current.InputTokenDetails.ImageTokens >= previous.InputTokenDetails.ImageTokens &&
		current.OutputTokenDetails.TextTokens >= previous.OutputTokenDetails.TextTokens &&
		current.OutputTokenDetails.AudioTokens >= previous.OutputTokenDetails.AudioTokens &&
		current.OutputTokenDetails.ImageTokens >= previous.OutputTokenDetails.ImageTokens &&
		current.OutputTokenDetails.ReasoningTokens >= previous.OutputTokenDetails.ReasoningTokens
}

func preConsumeUsageWithCumulative(ctx *gin.Context, info *relaycommon.RelayInfo, usage *dto.RealtimeUsage, totalUsage, cumulative *dto.RealtimeUsage) error {
	if usage == nil || totalUsage == nil {
		return fmt.Errorf("invalid usage pointer")
	}

	// Build the candidate cumulative frame before reserving. This lets the
	// durable usage hash reject a repeated upstream usage frame without first
	// changing either the wallet or the in-memory cumulative total.
	nextTotal := *totalUsage
	nextTotal.TotalTokens += usage.TotalTokens
	nextTotal.InputTokens += usage.InputTokens
	nextTotal.OutputTokens += usage.OutputTokens
	nextTotal.InputTokenDetails.CachedTokens += usage.InputTokenDetails.CachedTokens
	nextTotal.InputTokenDetails.CacheWriteTokens += usage.InputTokenDetails.CacheWriteTokens
	nextTotal.InputTokenDetails.CachedCreationTokens += usage.InputTokenDetails.CachedCreationTokens
	nextTotal.InputTokenDetails.TextTokens += usage.InputTokenDetails.TextTokens
	nextTotal.InputTokenDetails.AudioTokens += usage.InputTokenDetails.AudioTokens
	nextTotal.InputTokenDetails.ImageTokens += usage.InputTokenDetails.ImageTokens
	nextTotal.OutputTokenDetails.TextTokens += usage.OutputTokenDetails.TextTokens
	nextTotal.OutputTokenDetails.AudioTokens += usage.OutputTokenDetails.AudioTokens
	nextTotal.OutputTokenDetails.ImageTokens += usage.OutputTokenDetails.ImageTokens
	nextTotal.OutputTokenDetails.ReasoningTokens += usage.OutputTokenDetails.ReasoningTokens
	cumulativeForJournal := &nextTotal
	if cumulative != nil {
		cumulativeForJournal = cumulative
	}
	if info.AgencyPricing != nil {
		duplicate, err := service.AgencyRealtimeSegmentRecorded(info, usage, cumulativeForJournal)
		if err != nil {
			return err
		}
		if duplicate {
			return nil
		}
	}
	// Reserve first, then persist the immutable financial fact for this
	// successful segment. The local segment number is process-independent for
	// the connection because the journal's unique (charge_id, segment_no)
	// key makes retries idempotent.
	quota, paidAllocated, err := service.PreWssConsumeQuotaWithResult(ctx, info, usage)
	if err != nil {
		return err
	}
	if info.AgencyPricing != nil {
		segmentNo := info.AgencyRealtimeSegmentsRecorded
		if err := service.RecordAgencyRealtimeSegment(info, segmentNo, usage, cumulativeForJournal, int64(quota), paidAllocated, "success"); err != nil {
			return err
		}
	}
	*totalUsage = nextTotal
	return nil
}
