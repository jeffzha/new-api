package opsbilling

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Snapshots are display caches, never a spending authorization or wallet write.
// Include the connection and project scope so shared Redis cannot mix wallets.
type balanceSnapshot struct {
	Value   Balance   `json:"value"`
	Checked time.Time `json:"checked"`
}

func (r *Reader) restoreSnapshot(ctx context.Context, scope string) {
	r.snapshotKey = fmt.Sprintf("ops-balance-display:v1:%x", sha256.Sum256([]byte(r.Client.BaseURL+"\n"+r.Client.KeyID+"\n"+scope+"\n"+r.Currency)))
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	raw, err := common.RDB.Get(ctx, r.snapshotKey).Bytes()
	if err != nil || len(raw) > 4*1024*1024 {
		return
	}
	var values map[string]balanceSnapshot
	if common.Unmarshal(raw, &values) != nil || len(values) > cacheLimit {
		return
	}
	at := time.Now()
	for id, row := range values {
		if id != row.Value.UserID || row.Value.validate() != nil || row.Checked.IsZero() || row.Checked.After(at.Add(time.Minute)) || at.Sub(row.Checked) > 24*time.Hour {
			continue
		}
		value := row.Value
		if _, err := r.gatewayQuota(value); err != nil {
			continue
		}
		r.balances[id] = &cachedBalance{value: &value, checked: row.Checked}
	}
}

func (r *Reader) saveSnapshot(ctx context.Context, at time.Time) {
	if r.snapshotKey == "" || !common.RedisEnabled || common.RDB == nil || at.Before(r.snapshotDue) {
		return
	}
	r.snapshotDue = at.Add(15 * time.Second)
	values := map[string]balanceSnapshot{}
	r.mu.Lock()
	for id, entry := range r.balances {
		if entry.value != nil && entry.complete && !entry.checked.IsZero() {
			values[id] = balanceSnapshot{Value: *entry.value, Checked: entry.checked}
		}
	}
	r.mu.Unlock()
	if len(values) == 0 {
		return
	}
	raw, err := common.Marshal(values)
	if err != nil || len(raw) > 4*1024*1024 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if common.RDB.Set(ctx, r.snapshotKey, raw, 24*time.Hour).Err() != nil {
		common.SysError("OPS balance display cache unavailable")
	}
}
