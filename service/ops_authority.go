package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/gin-gonic/gin"
)

// Only API-key budgets are reserved locally. User money, subscriptions and
// distributor cash journals belong to OPS in this process.
type opsAuthoritySession struct {
	mu               sync.Mutex
	ctx              context.Context
	info             *relaycommon.RelayInfo
	reserved, actual int
	done, refunded   bool
}

func newOPSAuthoritySession(c *gin.Context, info *relaycommon.RelayInfo, quota int) (*opsAuthoritySession, error) {
	if info == nil || quota < 0 || quota > common.MaxQuota {
		return nil, errors.New("invalid project reservation")
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	if opsbilling.Default == nil {
		return nil, opsbilling.ErrBalanceUnavailable
	}
	client := opsbilling.Default.Client
	if info.OPSWalletBinding != nil && !info.OPSWalletBinding.Matches(client.BaseURL, client.KeyID, opsauthority.Scope()) {
		return nil, errors.New("accepted OPS wallet destination changed")
	}
	binding, err := opsauthority.NewBinding(client.BaseURL, client.KeyID, opsauthority.Scope())
	if err != nil {
		return nil, err
	}
	if err := opsbilling.Default.CheckQuota(ctx, info.UserId, quota, time.Now()); err != nil {
		return nil, err
	}
	s := &opsAuthoritySession{info: info, ctx: ctx}
	info.BillingSource = "ops"
	if info.OPSWalletBinding == nil {
		info.OPSWalletBinding = binding
	}
	if err := s.Reserve(quota); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *opsAuthoritySession) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}
func (s *opsAuthoritySession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.done && !s.refunded && s.reserved > 0
}
func (s *opsAuthoritySession) adjust(delta int) error {
	if delta == 0 || s.info.IsPlayground {
		return nil
	}
	if delta > 0 {
		return model.DecreaseTokenQuota(s.info.TokenId, s.info.TokenKey, delta)
	}
	return model.IncreaseTokenQuota(s.info.TokenId, s.info.TokenKey, -delta)
}
func (s *opsAuthoritySession) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target < 0 || target > common.MaxQuota || s.done || s.refunded {
		return errors.New("invalid project reservation state")
	}
	if target <= s.reserved {
		return nil
	}
	if opsbilling.Default == nil {
		return opsbilling.ErrBalanceUnavailable
	}
	if err := opsbilling.Default.CheckQuota(s.ctx, s.info.UserId, target, time.Now()); err != nil {
		return err
	}
	if !s.info.IsPlayground {
		if err := PreConsumeTokenQuota(s.info, target-s.reserved); err != nil {
			return err
		}
	}
	s.reserved = target
	s.info.FinalPreConsumedQuota = target
	return nil
}
func (s *opsAuthoritySession) Settle(actual int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actual < 0 || actual > common.MaxQuota || s.refunded {
		return errors.New("invalid project settlement")
	}
	if s.done {
		if s.actual != actual {
			return errors.New("project settlement conflict")
		}
		return nil
	}
	if err := s.adjust(actual - s.reserved); err != nil {
		return err
	}
	s.done, s.actual = true, actual
	return nil
}
func (s *opsAuthoritySession) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.refunded {
		return
	}
	if err := s.adjust(-s.reserved); err != nil {
		common.SysError("project token reservation refund failed: " + err.Error())
		return
	}
	s.refunded = true
}

func projectAuthority() bool { return opsauthority.Enabled() }

// An accepted call keeps its funding owner through settlement and retries.
// Process configuration selects new calls only; it cannot reroute an existing
// wallet/subscription debit or suppress its corresponding refund.
func opsWalletOwnsRequest(info *relaycommon.RelayInfo) bool {
	return info != nil && info.BillingSource == "ops"
}

// UsesOPSWallet selects the wallet before upstream acceptance, including a
// zero local price. Accepted requests retain their owner across flag changes.
func UsesOPSWallet(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	return opsWalletOwnsRequest(info) || (info.BillingSource == "" && projectAuthority())
}

func opsAuthorityAPIError(err error) *types.NewAPIError {
	if errors.Is(err, opsbilling.ErrInsufficientBalance) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
			types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	if errors.Is(err, opsbilling.ErrBalanceUnavailable) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeQueryDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
}
