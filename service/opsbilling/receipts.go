package opsbilling

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// AcceptAdjustment updates only a display snapshot, never the shared wallet.
func (r *Reader) AcceptAdjustment(userID, value string, quota, entryID int64, recordedAt string) {
	n, err := amount(value, true)
	if err != nil {
		return
	}
	b := Balance{UserID: userID, Amount: value, Quota: &quota, Negative: n.IsNegative(), LastEntryID: json.Number(strconv.FormatInt(entryID, 10)), UpdatedAt: recordedAt}
	if b.validate() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.balances[userID]
	if e == nil {
		if len(r.balances) >= cacheLimit {
			return
		}
		e = &cachedBalance{}
		r.balances[userID] = e
	}
	if e.value != nil {
		previous, _ := e.value.LastEntryID.Int64()
		if entryID < previous || entryID == previous && !e.value.samePosition(b) {
			return
		}
	}
	e.value, e.checked, e.complete, e.failed, e.missing = &b, time.Now(), true, false, false
	e.due, e.authorizedAt = time.Time{}, time.Time{}
}

// AcceptPushReceipts consumes the already authenticated push response. Only
// receipts matching the exact sent request/user/model are accepted. Pending
// receipts still use the bounded background reader; historical balanceAfter
// is never installed as a current wallet balance.
func (r *Reader) AcceptPushReceipts(sent, response []byte, at time.Time) {
	var batch struct {
		Events []struct {
			RequestID string `json:"requestId"`
			UserID    string `json:"userId"`
			Model     string `json:"model"`
		} `json:"events"`
	}
	var envelope struct {
		Data struct {
			Charges []Charge `json:"charges"`
		} `json:"data"`
	}
	if common.Unmarshal(sent, &batch) != nil || common.Unmarshal(response, &envelope) != nil {
		return
	}
	identities := map[string]chargeKey{}
	for _, e := range batch.Events {
		identities[e.RequestID] = chargeKey{e.RequestID, e.UserID, e.Model}
	}
	for _, c := range envelope.Data.Charges {
		key, found := identities[c.RequestID]
		if !found || c.validate(key.requestID, key.userID, key.model) != nil {
			continue
		}
		user, err := strconv.Atoi(key.userID)
		if err != nil || !r.Track(key.requestID, user, key.model, at) {
			continue
		}
		raw, err := common.Marshal(map[string]any{"data": c})
		if err == nil {
			_ = r.applyCharge(raw, key, at)
		}
		r.InvalidateCall(user)
	}
}
