package opswallet

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Session implements the same BillingSettler contract as the project gateway.
// Only admission and API-key limits are local; OPS owns all customer money.
type Session struct {
	mu             sync.Mutex
	ctx            context.Context
	db             *gorm.DB
	reader         *opsbilling.Reader
	info           *relaycommon.RelayInfo
	identity       model.OPSWalletHoldIdentity
	tokenKey       string
	reserved       int
	done, refunded bool
	usageEvent     []byte
}

func NewSession(ctx context.Context, db *gorm.DB, reader *opsbilling.Reader, info *relaycommon.RelayInfo, quota int) (*Session, error) {
	if !opsauthority.Enabled() || db == nil || reader == nil || info == nil {
		return nil, opsbilling.ErrBalanceUnavailable
	}
	s := &Session{ctx: ctx, db: db, reader: reader, info: info, tokenKey: info.TokenKey, identity: model.OPSWalletHoldIdentity{RequestID: info.RequestId, UserID: info.UserId, TokenID: info.TokenId, TokenUnlimited: info.TokenUnlimited, Playground: info.IsPlayground, Model: info.GetBillingModelName()}}
	s.identity.AgencyPricing = info.AgencyPricing
	if info.TaskRelayInfo != nil {
		s.identity.ChargeRequestID = opsauthority.TaskRequestID(info.RequestId)
	}
	if err := s.Reserve(quota); err != nil {
		return nil, err
	}
	info.BillingSource = "ops"
	return s, nil
}

func (s *Session) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

func (s *Session) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.done && !s.refunded && len(s.usageEvent) == 0
}

func (s *Session) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.refunded || target < 0 || target > common.MaxQuota {
		return model.ErrOPSWalletHoldConflict
	}
	position, err := s.reader.AdmissionPosition(s.ctx, s.identity.UserID, time.Now())
	if err != nil {
		return err
	}
	hold, err := model.ReserveOPSWallet(s.db, s.identity, target, position)
	if errors.Is(err, model.ErrOPSBalanceStale) {
		s.reader.InvalidateCall(s.identity.UserID)
	}
	if err != nil {
		return err
	}
	s.reserved = int(hold.HeldQuota)
	s.info.FinalPreConsumedQuota = s.reserved
	if !s.identity.Playground {
		if err := model.InvalidateTokenCache(s.tokenKey); err != nil {
			common.SysError("OPS API-key reservation cache invalidation failed")
		}
	}
	return nil
}

func (s *Session) Settle(actual int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refunded {
		return model.ErrOPSWalletHoldConflict
	}
	// An async task has its own terminal outbox; it must never settle here.
	if s.identity.ChargeRequestID != "" || len(s.usageEvent) == 0 {
		return model.ErrOPSWalletHoldConflict
	}
	if err := model.StageOPSRelaySettlement(s.db, s.identity.RequestID, s.identity.UserID, actual, s.usageEvent); err != nil {
		return err
	}
	if err := model.FinalizeOPSRelay(s.db, s.identity.RequestID, s.identity.UserID, actual, s.usageEvent); err != nil {
		return err
	}
	s.done = true
	s.reader.InvalidateCall(s.identity.UserID)
	if !s.identity.Playground {
		if err := model.InvalidateTokenCache(s.tokenKey); err != nil {
			common.SysError("OPS API-key settlement cache invalidation failed")
		}
	}
	return nil
}

// SettleUsage freezes the exact event for retries within this request.
func (s *Session) SettleUsage(actual int, event []byte) error {
	s.mu.Lock()
	if len(s.usageEvent) == 0 {
		s.usageEvent = append([]byte(nil), event...)
	} else if string(s.usageEvent) != string(event) {
		s.mu.Unlock()
		return model.ErrOPSWalletHoldConflict
	}
	s.mu.Unlock()
	return s.Settle(actual)
}

func (s *Session) Refund(_ *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.refunded || len(s.usageEvent) != 0 {
		return
	}
	if err := model.FinalizeOPSWallet(s.db, s.identity.RequestID, s.identity.UserID, 0, true); err != nil {
		common.SysError("OPS API-key cancellation requires reconciliation")
		return
	}
	s.refunded = true
	if !s.identity.Playground {
		if err := model.InvalidateTokenCache(s.tokenKey); err != nil {
			common.SysError("OPS API-key cancellation cache invalidation failed")
		}
	}
}
