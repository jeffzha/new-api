package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
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
	s := &opsReservationSession{ctx: ctx, info: info, client: client}
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
	return s, nil
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
	if target <= s.reserved && s.reserved > 0 {
		return nil
	}
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
