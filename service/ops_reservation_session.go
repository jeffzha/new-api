package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/QuantumNous/new-api/service/opsreserve"
	"github.com/gin-gonic/gin"
)

// OPS owns the money hold and final debit. The gateway only reserves the
// independent API-key budget; no local user wallet is debited a second time.
type opsReservationSession struct {
	mu                         sync.Mutex
	ctx                        context.Context
	info                       *relaycommon.RelayInfo
	client                     *opsreserve.Client
	quote                      map[string]any
	reserved, actual           int
	done, refunded, dispatched bool
	uncertain                  bool
	journal                    bool
	realtimeInputQuota         int
	realtimeWindow             int
	lastRenew                  time.Time
}

func newOPSReservationSession(c *gin.Context, info *relaycommon.RelayInfo, quota int) (relaycommon.BillingSettler, *types.NewAPIError) {
	if info == nil || quota < 0 || quota > common.MaxQuota {
		return nil, reservationAPIError(opsreserve.ErrUnavailable)
	}
	client, err := opsreserve.Configured()
	if err != nil {
		return nil, reservationAPIError(err)
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	s := &opsReservationSession{ctx: ctx, info: info, client: client, realtimeWindow: quota}
	// Ask OPS to price the output limit too. Local quota estimates remain only
	// a conservative floor, never the authority for the final debit.
	var maxOutput uint
	switch request := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		if request.MaxTokens != nil {
			maxOutput = *request.MaxTokens
		}
		if request.MaxCompletionTokens != nil {
			maxOutput = max(maxOutput, *request.MaxCompletionTokens)
		}
	case *dto.OpenAIResponsesRequest:
		if request.MaxOutputTokens != nil {
			maxOutput = *request.MaxOutputTokens
		}
	case *dto.ClaudeRequest:
		if request.MaxTokens != nil {
			maxOutput = *request.MaxTokens
		}
	}
	if maxOutput > 0 {
		s.quote = map[string]any{"userId": strconv.Itoa(info.UserId), "model": info.GetOriginModelName(), "callType": "text", "inputTokens": info.GetEstimatePromptTokens(), "outputTokens": maxOutput, "at": info.StartTime.UTC().Format(time.RFC3339Nano)}
	}
	if err = s.Reserve(quota); err != nil {
		return nil, reservationAPIError(err)
	}
	if info.TaskRelayInfo != nil && info.PublicTaskID != "" {
		if err = client.AttachTask(ctx, info.RequestId, info.PublicTaskID); err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
			defer cancel()
			_ = client.Release(cleanup, info.RequestId)
			return nil, reservationAPIError(err)
		}
	}
	info.BillingSource = "ops"
	if c != nil && info.TaskRelayInfo == nil && info.RelayFormat != types.RelayFormatTask && info.RelayFormat != types.RelayFormatMjProxy && info.RelayFormat != types.RelayFormatOpenAIRealtime {
		s.journal = true
		c.Set(model.OPSReservationLogKey, func(log *model.Log) error {
			// The display log may use a local pricing alias. OPS owns prices
			// for the requested public model, which also owns the money hold.
			usage := *log
			usage.ModelName = info.GetOriginModelName()
			// The financial journal precedes optional display-log enrichment.
			// Capture its authenticated accounting scope before persisting facts.
			if os.Getenv("OPS_WALLET_AUTHORITY") == "ops" && os.Getenv("OPS_PROJECT_SCOPE") != "" {
				var other map[string]any
				if usage.Other != "" {
					if err := common.UnmarshalJsonStr(usage.Other, &other); err != nil {
						return err
					}
				}
				if other == nil {
					other = map[string]any{}
				}
				other["billing_scope"] = os.Getenv("OPS_PROJECT_SCOPE")
				raw, err := common.Marshal(other)
				if err != nil {
					return err
				}
				usage.Other = string(raw)
			}
			facts, err := common.Marshal(struct {
				Log         *model.Log
				Conversion  opspush.Conversion
				ChannelName string
			}{&usage, opspush.CurrentConversion(), c.GetString(string(constant.ContextKeyChannelName))})
			if err != nil {
				return err
			}
			journalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
			defer cancel()
			if err := model.FinalizeOPSHoldUsage(journalCtx, client.DB, client.Transport.KeyID, &usage, string(facts)); err != nil {
				return err
			}
			opsreserve.NotifyUsage(log.RequestId)
			return nil
		})
	}
	return s, nil
}

// RecoverOPSUsage rebuilds only from committed usage facts, including their
// original conversion snapshot. A lost display log is never treated as free.
func RecoverOPSUsage(ctx context.Context, row *opsreserve.Intent) error {
	var facts struct {
		Log         *model.Log
		Conversion  opspush.Conversion
		ChannelName string
	}
	if common.UnmarshalJsonStr(row.UsageLog, &facts) != nil || facts.Log == nil || facts.Log.RequestId != row.Request || strconv.Itoa(facts.Log.UserId) != row.User {
		return opsreserve.ErrUnavailable
	}
	client, err := opsreserve.Configured()
	if err != nil || client.Transport.KeyID != row.Gateway {
		return opsreserve.ErrUnavailable
	}
	event := opspush.EventV11(facts.Log, facts.ChannelName, nil, facts.Conversion)
	if event == nil {
		return opsreserve.ErrUnavailable
	}
	if err = opspush.AttachUpstream(client.DB, facts.Log, event); err != nil {
		return err
	}
	body, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": row.Request + "-reserved-usage", "events": []any{event}})
	if err != nil {
		return err
	}
	if err = client.DB.WithContext(ctx).Model(&model.OPSHoldIntent{}).Where("gateway = ? AND request = ? AND (usage_payload IS NULL OR usage_payload = ?)", row.Gateway, row.Request, "").Update("usage_payload", string(body)).Error; err != nil {
		return err
	}
	return client.DB.WithContext(ctx).Where("gateway = ? AND request = ?", row.Gateway, row.Request).First(row).Error
}

func reservationAPIError(err error) *types.NewAPIError {
	if errors.Is(err, opsreserve.ErrInsufficient) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return types.NewErrorWithStatusCode(err, types.ErrorCodeUpdateDataError, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
}

func (s *opsReservationSession) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

func (s *opsReservationSession) ReservationGateway() string { return s.client.Transport.KeyID }
func (s *opsReservationSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.done && !s.refunded
}

func (s *opsReservationSession) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target < 0 || target > common.MaxQuota || s.done || s.refunded {
		return opsreserve.ErrUnavailable
	}
	if target <= s.reserved && s.reserved > 0 && time.Since(s.lastRenew) < time.Minute {
		return nil
	}
	target = max(target, s.reserved)
	if err := s.client.Reserve(s.ctx, s.info.RequestId, s.info.UserId, s.info.GetOriginModelName(), target, s.quote); err != nil {
		return err
	}
	token, budget := s.info.TokenId, target
	if s.info.IsPlayground {
		token, budget = 0, 0
	}
	{
		if err := s.client.TokenBudget(s.ctx, s.info.RequestId, s.info.UserId, token, budget, false); err != nil {
			if s.reserved == 0 {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 4*time.Second)
				defer cancel()
				_ = s.client.Release(ctx, s.info.RequestId)
			}
			return err
		}
	}
	s.reserved = target
	s.lastRenew = time.Now()
	s.info.FinalPreConsumedQuota = target
	return nil
}

func (s *opsReservationSession) Settle(actual int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actual < 0 || actual > common.MaxQuota || s.refunded {
		return opsreserve.ErrUnavailable
	}
	if s.done {
		if actual != s.actual {
			return opsreserve.ErrUnavailable
		}
		return nil
	}
	token, budget := s.info.TokenId, actual
	if s.info.IsPlayground {
		token, budget = 0, 0
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 4*time.Second)
	defer cancel()
	if s.info.TaskRelayInfo != nil && s.info.PublicTaskID != "" {
		// Submission is not terminal usage. Persist the task association, keeping
		// both budgets until the durable terminal task transaction settles them.
		if err := s.client.AttachTask(ctx, s.info.RequestId, s.info.PublicTaskID); err != nil {
			return err
		}
		s.done, s.actual = true, actual
		return nil
	}
	if s.journal {
		// RecordConsumeLog journals the final usage facts and key adjustment in
		// one main-database transaction. Do not finalize the key ahead of it.
		s.done, s.actual = true, actual
		return nil
	}
	if err := s.client.TokenBudget(ctx, s.info.RequestId, s.info.UserId, token, budget, true); err != nil {
		return err
	}
	// The durable usage outbox, not a second money request, triggers the debit.
	// Keep the remote hold until OPS commits that debit, including on a timeout.
	s.done, s.actual = true, actual
	return nil
}

func (s *opsReservationSession) Refund(_ *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.refunded {
		return
	}
	if s.dispatched && (s.uncertain || s.info.LastError == nil || s.info.LastError.GetErrorCode() != types.ErrorCodeBadResponseStatusCode || s.info.LastError.StatusCode < 400 || s.info.LastError.StatusCode >= 500 || s.info.LastError.StatusCode == http.StatusRequestTimeout || s.info.LastError.StatusCode == http.StatusConflict) {
		// A timeout, canceled stream or lost response cannot prove the provider
		// did no work. Keep the money/key holds for explicit reconciliation.
		common.SysError("OPS hold retained for uncertain provider outcome: " + s.info.RequestId)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 4*time.Second)
	defer cancel()
	if err := s.client.Release(ctx, s.info.RequestId); err != nil {
		common.SysError("OPS money hold release queued for retry")
		return
	}
	s.refunded = true
}

// ObserveOPSReservationAttempt retains uncertainty across retries. A later
// definitive rejection cannot prove an earlier timed-out attempt did no work.
func ObserveOPSReservationAttempt(info *relaycommon.RelayInfo, err *types.NewAPIError) {
	if info == nil || err == nil {
		return
	}
	if s, ok := info.Billing.(*opsReservationSession); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err.GetErrorCode() != types.ErrorCodeBadResponseStatusCode || err.StatusCode < 400 || err.StatusCode >= 500 || err.StatusCode == http.StatusRequestTimeout || err.StatusCode == http.StatusConflict {
			s.uncertain = true
		}
	}
}

// BeginDispatch is called before any provider transport or task submission.
func BeginOPSReservationDispatch(info *relaycommon.RelayInfo) error {
	if info == nil {
		return nil
	}
	if s, ok := info.Billing.(*opsReservationSession); ok {
		if err := s.client.MarkDispatch(s.ctx, info.RequestId); err != nil {
			return err
		}
		s.mu.Lock()
		s.dispatched = true
		s.mu.Unlock()
	}
	return nil
}

// PrepareOPSRealtimeInput extends the session's hold before forwarding another
// client input. This is a rolling estimate, not a maximum provider-cost promise.
func PrepareOPSRealtimeInput(info *relaycommon.RelayInfo, text, audio int) error {
	if info == nil {
		return opsreserve.ErrUnavailable
	}
	s, ok := info.Billing.(*opsReservationSession)
	if !ok {
		return nil
	}
	if text < 0 || audio < 0 {
		return opsreserve.ErrUnavailable
	}
	quota, clamp := calculateAudioQuota(QuotaInfo{InputDetails: TokenDetails{TextTokens: text, AudioTokens: audio}, ModelName: info.OriginModelName, ModelRatio: info.PriceData.ModelRatio, GroupRatio: info.PriceData.GroupRatioInfo.GroupRatio})
	noteQuotaClamp(info, clamp)
	s.mu.Lock()
	previous, window, consumed := s.realtimeInputQuota, s.realtimeWindow, s.reserved
	s.mu.Unlock()
	if quota > common.MaxQuota-previous || window > common.MaxQuota-previous-quota {
		return opsreserve.ErrUnavailable
	}
	target := max(consumed, previous+quota+window)
	if err := s.Reserve(target); err != nil {
		return err
	}
	s.mu.Lock()
	s.realtimeInputQuota = previous + quota
	s.mu.Unlock()
	return nil
}

// OPSReservationActive also recognizes in-flight sessions after admission is
// disabled. Their holds still require the original settlement contract.
func OPSReservationActive(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	_, ok := info.Billing.(*opsReservationSession)
	return ok
}

// OPSReservationRequiresReconciliation forbids another provider dispatch when
// the previous attempt may already have created billable work.
func OPSReservationRequiresReconciliation(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	s, ok := info.Billing.(*opsReservationSession)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dispatched && s.uncertain
}
