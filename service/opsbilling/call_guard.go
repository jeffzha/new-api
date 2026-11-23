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
	value, err := r.callBalance(ctx, userID, 0, at)
	if err != nil && !errors.Is(err, ErrInsufficientBalance) {
		return model.OPSWalletPosition{}, err
	}
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
	_, err := r.callBalance(ctx, userID, estimatedQuota, at)
	return err
}

// callBalance returns the immutable individual read this caller joined. A
// completed invocation invalidates future admissions, not other callers
// already waiting on this read. The DB transaction still checks the ledger
// position and subtracts pending holds across every gateway instance.
func (r *Reader) callBalance(ctx context.Context, userID, estimatedQuota int, at time.Time) (*Balance, error) {
	started := time.Now()
	if userID <= 0 || estimatedQuota < 0 || ctx.Err() != nil {
		return nil, ErrBalanceUnavailable
	}
	id := strconv.Itoa(userID)
	r.mu.Lock()
	if ok, err := r.callDecision(id, estimatedQuota, at); ok {
		value := *r.balances[id].value
		r.mu.Unlock()
		return &value, err
	}
	if r.balanceFlights == nil {
		r.balanceFlights = map[string]*balanceFlight{}
	}
	flight := r.balanceFlights[id]
	owner := flight == nil
	if owner {
		if len(r.balanceFlights) >= cacheLimit {
			r.mu.Unlock()
			return nil, ErrBalanceUnavailable
		}
		flight = &balanceFlight{done: make(chan struct{})}
		r.balanceFlights[id] = flight
	}
	r.mu.Unlock()
	if owner {
		// The first caller owns the network read, but its cancellation must not
		// cancel other callers waiting on this same wallet. Keep a bounded,
		// detached context for the shared operation.
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		value, readAt, err := r.refreshBalance(readCtx, id, at)
		cancel()
		r.mu.Lock()
		flight.at, flight.err, flight.value = readAt, err, value
		delete(r.balanceFlights, id)
		close(flight.done)
		r.mu.Unlock()
	} else {
		select {
		case <-flight.done:
		case <-ctx.Done():
			return nil, ErrBalanceUnavailable
		}
	}
	if ctx.Err() != nil {
		return nil, ErrBalanceUnavailable
	}
	if flight.err != nil || flight.value == nil {
		return nil, ErrBalanceUnavailable
	}
	if at.Add(time.Since(started)).After(flight.at.Add(5 * time.Second)) {
		return nil, ErrBalanceUnavailable
	}
	return flight.value, r.balanceDecision(*flight.value, estimatedQuota)
}

// refreshBalance performs one authoritative per-user read while respecting
// the shared OPS request budget. It returns the logical timestamp used for the
// authorization decision, including any rate-limit wait.
func (r *Reader) refreshBalance(ctx context.Context, id string, at time.Time) (*Balance, time.Time, error) {
	started := time.Now()
	select {
	case r.readGate <- struct{}{}:
	case <-ctx.Done():
		return nil, at, ErrBalanceUnavailable
	}
	defer func() { <-r.readGate }()
	at = at.Add(time.Since(started))
	r.mu.Lock()
	if ok, _ := r.callDecision(id, 0, at); ok {
		value := *r.balances[id].value
		authorizedAt := r.balances[id].authorizedAt
		r.mu.Unlock()
		return &value, authorizedAt, nil
	}
	if len(r.balances) >= cacheLimit && r.balances[id] == nil {
		r.mu.Unlock()
		return nil, at, ErrBalanceUnavailable
	}
	wait := r.nextRead.Sub(at)
	if wait > 0 {
		r.mu.Unlock()
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, at, ErrBalanceUnavailable
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
	var value *Balance
	if err == nil {
		value, err = r.installBalance(raw, id, at)
	}
	if err != nil {
		r.mu.Lock()
		entry := r.balances[id]
		entry.failed = true
		entry.authorizedAt = time.Time{}
		var readErr *opsclient.ReadError
		entry.missing = errors.As(err, &readErr) && readErr.Status == 404
		r.backoffLocked(err, at)
		r.mu.Unlock()
		return nil, at, err
	}
	return value, at, nil
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
	return true, r.balanceDecision(*entry.value, estimatedQuota)
}

func (r *Reader) balanceDecision(value Balance, estimatedQuota int) error {
	quota, err := r.gatewayQuota(value)
	if err != nil {
		return ErrBalanceUnavailable
	}
	if value.Negative || quota <= 0 || quota < int64(estimatedQuota) {
		return ErrInsufficientBalance
	}
	return nil
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
