package opsbilling

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
)

// AdmissionPosition exposes only a fresh individual read, never the display
// cache. The shared DB admission transaction must still subtract pending holds.
func (r *Reader) AdmissionPosition(ctx context.Context, userID int, at time.Time) (model.OPSWalletPosition, error) {
	if err := r.CheckCall(ctx, userID, at); err != nil && !errors.Is(err, ErrInsufficientBalance) {
		return model.OPSWalletPosition{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := strconv.Itoa(userID)
	if ok, err := r.callDecision(id, 0, time.Now()); !ok || err != nil && !errors.Is(err, ErrInsufficientBalance) {
		if err == nil {
			err = ErrBalanceUnavailable
		}
		return model.OPSWalletPosition{}, err
	}
	value := r.balances[id].value
	quota, err := r.gatewayQuota(*value)
	if err != nil {
		return model.OPSWalletPosition{}, err
	}
	entry, err := value.LastEntryID.Int64()
	return model.OPSWalletPosition{UserID: userID, EntryID: entry, Quota: quota}, err
}

var ErrInsufficientBalance = errors.New("运营平台余额不足，请充值后再调用")
var ErrBalanceUnavailable = errors.New("暂时无法确认运营平台余额，请稍后重试")

// Authorization uses only a recent individual authoritative read, never a
// restored display snapshot or historical charge balanceAfter. All network
// reads share the worker's 30/min budget. No local user money is reserved.
func (r *Reader) CheckCall(ctx context.Context, userID int, at time.Time) error {
	return r.CheckQuota(ctx, userID, 0, at)
}

// CheckQuota applies the wallet preflight to OPS's balance in gateway quota
// units. It does not reserve money: OPS still settles accepted usage later.
func (r *Reader) CheckQuota(ctx context.Context, userID, estimatedQuota int, at time.Time) error {
	if userID <= 0 || estimatedQuota < 0 {
		return ErrBalanceUnavailable
	}
	id := strconv.Itoa(userID)
	r.mu.Lock()
	if ok, err := r.callDecision(id, estimatedQuota, at); ok {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	// A short cancellable wait shares the reader's budget without spinning,
	// rather than rejecting a healthy wallet just because a warmup is in flight.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	started := time.Now()
	select {
	case r.readGate <- struct{}{}:
	case <-ctx.Done():
		return ErrBalanceUnavailable
	}
	defer func() { <-r.readGate }()
	at = at.Add(time.Since(started))
	r.mu.Lock()
	if ok, err := r.callDecision(id, estimatedQuota, at); ok {
		r.mu.Unlock()
		return err
	}
	if len(r.balances) >= cacheLimit && r.balances[id] == nil {
		r.mu.Unlock()
		return ErrBalanceUnavailable
	}
	wait := r.nextRead.Sub(at)
	if wait > 0 {
		r.mu.Unlock()
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ErrBalanceUnavailable
		}
		at = at.Add(wait)
		r.mu.Lock()
	}
	if r.balances[id] == nil {
		r.balances[id] = &cachedBalance{}
	}
	r.nextRead = at.Add(2 * time.Second)
	r.mu.Unlock()
	raw, err := r.Client.Get(ctx, "/api/v1/catalog/v1/balances/"+id, at)
	if err == nil {
		err = r.applyBalance(raw, id, at)
	}
	if err != nil {
		r.mu.Lock()
		entry := r.balances[id]
		entry.failed = true
		entry.authorizedAt = time.Time{}
		r.backoffLocked(err, at)
		r.mu.Unlock()
		return ErrBalanceUnavailable
	}
	r.mu.Lock()
	ok, err := r.callDecision(id, estimatedQuota, at)
	r.mu.Unlock()
	if !ok {
		return ErrBalanceUnavailable
	}
	return err
}

// A completed invocation may have changed the authoritative balance. Force
// the next call to read it again rather than granting another five seconds.
func (r *Reader) InvalidateCall(userID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.balances[strconv.Itoa(userID)]; entry != nil {
		entry.authorizedAt = time.Time{}
	}
}

// Caller holds mu. A sub-quota negative decimal still blocks new calls.
func (r *Reader) callDecision(id string, estimatedQuota int, at time.Time) (bool, error) {
	entry := r.balances[id]
	if entry == nil || entry.value == nil || entry.failed || entry.authorizedAt.IsZero() || at.Before(entry.authorizedAt) || at.Sub(entry.authorizedAt) > 5*time.Second {
		return false, nil
	}
	quota, err := r.gatewayQuota(*entry.value)
	if err != nil {
		return true, ErrBalanceUnavailable
	}
	if entry.value.Negative || quota <= 0 || quota < int64(estimatedQuota) {
		return true, ErrInsufficientBalance
	}
	return true, nil
}

func (r *Reader) backoffLocked(err error, at time.Time) {
	wait := time.Minute
	var readErr *opsclient.ReadError
	if errors.As(err, &readErr) {
		wait = max(wait, readErr.RetryAfter)
		if readErr.Status == 401 || readErr.Status == 403 {
			wait = max(wait, 5*time.Minute)
		}
	}
	if !errors.As(err, &readErr) || readErr.Status == 401 || readErr.Status == 403 || readErr.Status == 429 || readErr.Status >= 500 {
		r.nextRead = at.Add(wait)
	}
}
