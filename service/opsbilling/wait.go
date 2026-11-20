package opsbilling

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/pkg/businessapi"
)

// WaitReceipt waits only for authenticated receipts installed by existing
// workers. It never polls SQL or OPS, starts no goroutine, and bounds retained
// HTTP requests even if OPS is unavailable. Callers must authorize the invocation.
func (r *Reader) WaitReceipt(ctx context.Context, requestID string, userID int, model string, wait time.Duration) businessapi.Billing {
	result := r.Receipt(requestID, userID, model, time.Now())
	if wait <= 0 || result.Status != "pending" || ctx.Err() != nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, min(wait, time.Second))
	defer cancel()
	key := chargeKey{requestID, strconv.Itoa(userID), model}
	r.mu.Lock()
	if r.waiters >= 1024 {
		r.mu.Unlock()
		return result
	}
	r.waiters++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.waiters--
		r.mu.Unlock()
	}()
	for {
		r.mu.Lock()
		entry := r.charges[key]
		if entry == nil {
			r.mu.Unlock()
			return result
		}
		if entry.changed == nil {
			entry.changed = make(chan struct{})
		}
		changed := entry.changed
		r.mu.Unlock()
		// Register before re-reading so a receipt arriving between these steps
		// cannot leave the request asleep until its deadline.
		result = r.Receipt(requestID, userID, model, time.Now())
		if result.Status != "pending" {
			return result
		}
		select {
		case <-ctx.Done():
			return r.Receipt(requestID, userID, model, time.Now())
		case <-changed:
			// Eviction wakes waiters too. Do not reinsert an evicted invocation
			// repeatedly under load; a later authorized query can reload it.
			r.mu.Lock()
			exists := r.charges[key] == entry
			r.mu.Unlock()
			if !exists {
				return result
			}
		}
	}
}
