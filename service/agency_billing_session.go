package service

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// AgencyBillingSession owns the durable wallet lifecycle. It never uses the
// legacy asynchronous refund or quota batch queues. Its in-memory flags are
// conveniences; the database journal and operation receipts decide idempotency.
type AgencyBillingSession struct {
	mu       sync.Mutex
	info     *relaycommon.RelayInfo
	reserved int
	closed   bool
}

func NewAgencyBillingSession(info *relaycommon.RelayInfo, quota int) (*AgencyBillingSession, *types.NewAPIError) {
	if info.AgencyPricing == nil || quota < 0 {
		return nil, types.NewError(fmt.Errorf("durable agency quote is required"), types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	if info.RequestId == "" {
		info.RequestId = common.NewRequestId()
	}
	session := &AgencyBillingSession{info: info}
	if err := session.reserve(quota); err != nil {
		status := http.StatusServiceUnavailable
		code := types.ErrorCodeUpdateDataError
		if err == model.ErrInsufficientAgencyWalletQuota {
			status, code = http.StatusForbidden, types.ErrorCodeInsufficientUserQuota
		} else if err == model.ErrInsufficientAgencyTokenQuota {
			status, code = http.StatusForbidden, types.ErrorCodePreConsumeTokenQuotaFailed
		}
		return nil, types.NewErrorWithStatusCode(err, code, status, types.ErrOptionWithSkipRetry())
	}
	info.BillingSource = BillingSourceWallet
	return session, nil
}

func (s *AgencyBillingSession) reserve(target int) error {
	paid, seq, err := model.TryReserveAgencyWalletAndTokenWithSequence(s.info.UserId, s.info.TokenId,
		target-s.reserved, s.info.TokenKey, s.info.RequestId, int64(target), s.info.TokenUnlimited, s.info.AgencyPricing)
	if err != nil {
		return err
	}
	s.info.AgencyPaidAllocatedQuota += paid
	s.info.AgencyMoneySeq = seq
	s.reserved = target
	s.info.FinalPreConsumedQuota = target
	return nil
}

func (s *AgencyBillingSession) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return model.ErrAgencyChargeConflict
	}
	if target <= s.reserved {
		return nil
	}
	return s.reserve(target)
}

func (s *AgencyBillingSession) Settle(actual int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Use the same business input and receipt as the outer settlement hook.
	// The durable operation verifies repeats (including a different amount or
	// an earlier cancellation); an in-memory closed flag is not that proof.
	if err := RecordAgencyBillingEvent(s.info, int64(actual), "success"); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func (s *AgencyBillingSession) Refund(_ *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if err := RecordAgencyBillingEvent(s.info, 0, "cancelled"); err != nil {
		common.SysError("agency cancellation remains pending: " + err.Error())
		return
	}
	s.closed = true
}

func (s *AgencyBillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}

func (s *AgencyBillingSession) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

var _ relaycommon.BillingSettler = (*AgencyBillingSession)(nil)
