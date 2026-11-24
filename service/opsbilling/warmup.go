package opsbilling

import (
	"context"
	"strconv"
	"time"
)

// QueueBalances warms real OPS responses before anyone opens a user page.
// The existing single reader and rate limit perform all network requests.
func (r *Reader) QueueBalances(ids []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		key := strconv.Itoa(id)
		if entry := r.balances[key]; entry != nil {
			if entry.value == nil {
				entry.warm = true
			}
			continue
		}
		if len(r.balances) >= cacheLimit {
			return
		}
		r.balances[key] = &cachedBalance{warm: true}
	}
}

func (r *Reader) discoverUsers(ctx context.Context, at time.Time) error {
	if r.UserIDs == nil || at.Before(r.nextUsers) {
		return nil
	}
	r.nextUsers = at.Add(time.Minute)
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ids, err := r.UserIDs(readCtx, r.userCursor, 500)
	if err != nil {
		return err
	}
	r.QueueBalances(ids)
	for _, id := range ids {
		if id > r.userCursor {
			r.userCursor = id
		}
	}
	if len(ids) == 500 {
		r.nextUsers = at.Add(time.Second)
	}
	return nil
}
