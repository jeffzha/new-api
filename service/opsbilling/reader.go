package opsbilling

import (
	"container/list"
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
)

const cacheLimit = 10000

type chargeKey struct{ requestID, userID, model string }
type cachedCharge struct {
	value                   *Charge
	checked, due, requested time.Time
	failed                  bool
	changed                 chan struct{}
	position                *list.Element
}
type cachedBalance struct {
	value                   *Balance
	checked, due, requested time.Time
	failed                  bool
	complete                bool
	missing                 bool
	warm                    bool
	authorizedAt            time.Time
}

type BalanceView struct {
	Status    string  `json:"status"`
	Source    string  `json:"source"`
	Currency  string  `json:"currency"`
	Amount    *string `json:"amount"`
	Quota     *int64  `json:"quota"`
	Negative  *bool   `json:"negative,omitempty"`
	CheckedAt string  `json:"checked_at,omitempty"`
	Stale     bool    `json:"stale"`
	Reason    string  `json:"reason,omitempty"`
}

type Reader struct {
	Currency              string
	Client                *opsclient.Client
	mu                    sync.Mutex
	readGate              chan struct{}
	charges               map[chargeKey]*cachedCharge
	chargeOrder           list.List
	waiters               int
	balances              map[string]*cachedBalance
	cursor                string
	nextRead, nextBalance time.Time
	lastWallet            bool
	snapshotKey           string
	snapshotDue           time.Time
	UserIDs               func(context.Context, int, int) ([]int, error)
	userCursor            int
	nextUsers             time.Time
}

func New(c Config) *Reader {
	if c.Currency == "" {
		c.Currency = "CNY"
	}
	return &Reader{Currency: c.Currency, Client: opsclient.New(c.BaseURL, c.KeyID, c.Secret, c.Timeout), readGate: make(chan struct{}, 1), charges: map[chargeKey]*cachedCharge{}, balances: map[string]*cachedBalance{}}
}

// Track only records an already authorized local invocation. HTTP always runs
// on the single worker; no waiting or goroutine is added to a relay request.
func (r *Reader) Track(requestID string, userID int, model string, at time.Time) bool {
	if requestID == "" || len(requestID) > 80 || !businessapi.ValidText(requestID, 80) || userID <= 0 || model == "" || len(model) > 200 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := chargeKey{requestID, strconv.Itoa(userID), model}
	if entry := r.charges[key]; entry != nil {
		entry.requested = at
		if entry.position != nil {
			r.chargeOrder.MoveToBack(entry.position)
		}
		return true
	}
	if len(r.charges) >= cacheLimit {
		oldest := r.chargeOrder.Front()
		if oldest == nil {
			return false
		}
		oldKey := oldest.Value.(chargeKey)
		if entry := r.charges[oldKey]; entry != nil && entry.changed != nil {
			close(entry.changed)
		}
		delete(r.charges, oldKey)
		r.chargeOrder.Remove(oldest)
	}
	r.charges[key] = &cachedCharge{requested: at, position: r.chargeOrder.PushBack(key)}
	return true
}

func (r *Reader) Receipt(requestID string, userID int, model string, at time.Time) businessapi.Billing {
	result := businessapi.Billing{Source: "ops", Currency: r.Currency, Status: "pending", Reason: "awaiting_ops_charge"}
	retry := 1
	result.RetryAfterSeconds = &retry
	if !r.Track(requestID, userID, model, at) {
		retry = 30
		result.Status = "unavailable"
		result.Reason = "ops_charge_queue_full_or_invalid"
		return result
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.charges[chargeKey{requestID, strconv.Itoa(userID), model}]
	if entry == nil {
		return result
	}
	freshUntil := entry.checked.Add(3 * time.Minute)
	if scheduled := entry.due.Add(time.Minute); scheduled.After(freshUntil) {
		freshUntil = scheduled
	}
	if entry.failed || entry.value != nil && at.After(freshUntil) {
		retry = 30
		result.Status = "unavailable"
		result.Reason = "ops_charge_read_unavailable"
		return result
	}
	if entry.value != nil {
		return entry.value.Receipt(entry.checked)
	}
	return result
}

// CachedLogFee never tracks a request or schedules an OPS read. Only an explicit
// canonical fee view can fill the gap before the incremental feed arrives.
func (r *Reader) CachedLogFee(requestID string, userID int, modelName string, at time.Time) *model.OpsFee {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.charges[chargeKey{requestID, strconv.Itoa(userID), modelName}]
	if entry == nil || entry.failed || entry.value == nil || (entry.value.Status != "charged" && entry.value.Status != "station_billed") || at.Sub(entry.checked) > 3*time.Minute {
		return nil
	}
	f := entry.value.LogFee
	if f == nil || f.RequestID != requestID || f.UserID != strconv.Itoa(userID) || f.Model != modelName || f.Amount == nil {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, f.ChargedAt); err != nil {
		return nil
	}
	for i, value := range []*string{f.Amount, f.StationAmount, f.RefundAmount, f.Difference} {
		if value != nil {
			if _, err := amount(*value, i == 3); err != nil {
				return nil
			}
		}
	}
	return &model.OpsFee{RequestID: requestID, UserID: f.UserID, Model: modelName, Amount: f.Amount, RefundAmount: f.RefundAmount, StationAmount: f.StationAmount, Difference: f.Difference, Currency: r.Currency, ChargedAt: f.ChargedAt, FetchedAt: entry.checked.Unix(), Status: "available"}
}

// Balance reads a local snapshot only. An absent user is queued for a bounded
// single-user refresh. Old values remain visible as stale, never as zero.
func (r *Reader) Balance(userID int, at time.Time) BalanceView {
	view := BalanceView{Status: "pending", Source: "ops", Currency: r.Currency, Reason: "awaiting_ops_balance"}
	if userID <= 0 {
		view.Status = "unavailable"
		view.Reason = "invalid_user"
		return view
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := strconv.Itoa(userID)
	entry := r.balances[id]
	if entry == nil {
		if len(r.balances) >= cacheLimit {
			view.Status = "unavailable"
			view.Reason = "ops_balance_cache_full"
			return view
		}
		entry = &cachedBalance{}
		r.balances[id] = entry
	}
	entry.requested = at
	if entry.value == nil || entry.checked.IsZero() {
		if entry.failed {
			view.Status = "unavailable"
			view.Reason = "ops_balance_read_unavailable"
			if entry.missing {
				view.Reason = "ops_balance_not_available"
			}
		}
		return view
	}
	view.Amount = &entry.value.Amount
	quota, err := r.gatewayQuota(*entry.value)
	if err != nil {
		view.Status, view.Reason = "unavailable", "invalid_ops_balance_conversion"
		view.Amount = nil
		return view
	}
	view.Quota = &quota
	view.Negative = &entry.value.Negative
	view.CheckedAt = entry.checked.UTC().Format(time.RFC3339)
	view.Stale = entry.failed || !entry.complete || at.Sub(entry.checked) > 3*time.Minute
	view.Status = "available"
	view.Reason = ""
	if view.Stale {
		view.Status = "stale"
		view.Reason = "ops_balance_read_unavailable"
	}
	return view
}

func (r *Reader) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := r.Step(ctx, time.Now()); err != nil {
			common.SysError("OPS billing reader: " + err.Error())
		}
		r.saveSnapshot(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Step sends at most one signed read. It serializes all reads in this module
// with a 2s gap (30/minute), leaving budget for existing price/fee readers.
func (r *Reader) Step(ctx context.Context, at time.Time) error {
	select {
	case r.readGate <- struct{}{}:
	case <-ctx.Done():
		r.mu.Lock()
		for _, entry := range r.balances {
			entry.failed = true
		}
		r.mu.Unlock()
		return ctx.Err()
	}
	defer func() { <-r.readGate }()
	if err := r.discoverUsers(ctx, at); err != nil {
		return err
	}
	r.mu.Lock()
	if at.Before(r.nextRead) {
		r.mu.Unlock()
		return nil
	}
	target := ""
	var selected chargeKey
	var oldest time.Time
	for key, entry := range r.charges {
		if at.Sub(entry.requested) > 10*time.Minute {
			if entry.position != nil {
				r.chargeOrder.Remove(entry.position)
			}
			if entry.changed != nil {
				close(entry.changed)
			}
			delete(r.charges, key)
			continue
		}
		if !at.Before(entry.due) && (target == "" || entry.due.Before(oldest)) {
			selected = key
			oldest = entry.due
			target = "/api/v1/catalog/v1/charges/" + url.PathEscape(key.requestID)
		}
	}
	userID := ""
	for id, entry := range r.balances {
		if (entry.warm || !entry.requested.IsZero() && at.Sub(entry.requested) <= 10*time.Minute) && !at.Before(entry.due) && (entry.value == nil || entry.value.LastEntryID == "0") {
			if userID != "" {
				previous := r.balances[userID]
				if previous.value == nil && entry.value != nil {
					continue
				}
				if (previous.value == nil) == (entry.value == nil) && (entry.due.After(previous.due) || entry.due.Equal(previous.due) && (entry.requested.Before(previous.requested) || entry.requested.Equal(previous.requested) && id < userID)) {
					continue
				}
			}
			userID = id
		}
	}
	wallet := !at.Before(r.nextBalance) && (!r.lastWallet || target == "")
	if wallet {
		userID = ""
		target = "/api/v1/catalog/v1/balances"
		if r.cursor != "" {
			target += "?since=" + url.QueryEscape(r.cursor)
		}
	} else if userID != "" && (!r.lastWallet || target == "") {
		target = "/api/v1/catalog/v1/balances/" + userID
	} else {
		userID = ""
	}
	if target == "" {
		r.mu.Unlock()
		return nil
	}
	r.nextRead = at.Add(2 * time.Second)
	r.lastWallet = wallet || userID != ""
	r.mu.Unlock()
	raw, err := r.Client.Get(ctx, target, at)
	if err == nil {
		switch {
		case wallet:
			err = r.applyPage(raw, at)
		case userID != "":
			err = r.applyBalance(raw, userID, at)
		default:
			err = r.applyCharge(raw, selected, at)
		}
	}
	if err == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	wait := time.Minute
	var readErr *opsclient.ReadError
	if errors.As(err, &readErr) {
		wait = max(wait, readErr.RetryAfter)
		if readErr.Status == 401 || readErr.Status == 403 {
			wait = max(wait, 5*time.Minute)
		}
	}
	// Missing users and individual receipt failures must not stall every user.
	// Authentication, throttling and service failures retain global backoff.
	globalFailure := !errors.As(err, &readErr) || readErr.Status == 401 || readErr.Status == 403 || readErr.Status == 429 || readErr.Status >= 500
	if globalFailure {
		r.nextRead = at.Add(wait)
	}
	switch {
	case wallet:
		r.nextBalance = at.Add(wait)
		for _, entry := range r.balances {
			entry.failed = true
		}
	case userID != "":
		entry := r.balances[userID]
		entry.failed = true
		entry.due = at.Add(wait)
		entry.missing = readErr != nil && readErr.Status == 404
	default:
		entry := r.charges[selected]
		if entry != nil && !at.Before(entry.checked) {
			entry.failed = true
			entry.due = at.Add(wait)
			if entry.changed != nil {
				close(entry.changed)
				entry.changed = nil
			}
		}
	}
	return err
}

func (r *Reader) applyCharge(raw []byte, key chargeKey, at time.Time) error {
	var envelope struct {
		Data *Charge `json:"data"`
	}
	if common.Unmarshal(raw, &envelope) != nil || envelope.Data == nil {
		return errors.New("invalid OPS charge response")
	}
	if err := envelope.Data.validate(key.requestID, key.userID, key.model); err != nil {
		return err
	}
	if envelope.Data.Currency != "" && envelope.Data.Currency != r.Currency {
		return errors.New("OPS charge currency mismatch")
	}
	envelope.Data.Currency = r.Currency
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.charges[key]
	if entry == nil || at.Before(entry.checked) {
		return nil
	}
	entry.value = envelope.Data
	entry.checked = at
	entry.failed = false
	if entry.changed != nil {
		close(entry.changed)
		entry.changed = make(chan struct{})
	}
	entry.due = at.Add(5 * time.Second)
	if envelope.Data.Status == "charged" {
		entry.due = at.Add(30 * time.Second)
	}
	if envelope.Data.Status == "failed" || envelope.Data.Status == "station_billed" {
		entry.due = at.Add(5 * time.Minute)
	}
	return nil
}

func (r *Reader) applyBalance(raw []byte, userID string, at time.Time) error {
	var envelope struct {
		Data *Balance `json:"data"`
	}
	if common.Unmarshal(raw, &envelope) != nil || envelope.Data == nil || envelope.Data.UserID != userID {
		return errors.New("invalid OPS balance response")
	}
	if err := envelope.Data.validate(); err != nil {
		return err
	}
	if _, err := r.gatewayQuota(*envelope.Data); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.balances[userID]
	previous := int64(0)
	if entry.value != nil {
		previous, _ = entry.value.LastEntryID.Int64()
	}
	current, _ := envelope.Data.LastEntryID.Int64()
	if current < previous || current == previous && entry.value != nil && !entry.value.samePosition(*envelope.Data) {
		return errors.New("OPS balance moved backwards")
	}
	entry.value = envelope.Data
	entry.checked = at
	entry.failed = false
	entry.missing = false
	entry.due = at.Add(time.Minute)
	entry.complete = true
	entry.warm = false
	entry.authorizedAt = at
	return nil
}

func (r *Reader) applyPage(raw []byte, at time.Time) error {
	var envelope struct {
		Data *struct {
			Items      []balanceEntry `json:"items"`
			NextCursor *string        `json:"nextCursor"`
			HasMore    *bool          `json:"hasMore"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &envelope) != nil || envelope.Data == nil || envelope.Data.HasMore == nil || envelope.Data.Items == nil || len(envelope.Data.Items) > 500 {
		return errors.New("invalid OPS balance page")
	}
	p := envelope.Data
	values := make([]Balance, 0, len(p.Items))
	last := int64(0)
	for _, entry := range p.Items {
		change, err := amount(entry.Amount, true)
		if err != nil {
			return err
		}
		switch entry.Kind {
		case "opening", "credit", "refund":
		case "charge":
			if change.IsPositive() {
				return errors.New("invalid OPS debit direction")
			}
		default:
			return errors.New("invalid OPS balance entry kind")
		}
		id, err := entry.EntryID.Int64()
		if err != nil || id <= last {
			return errors.New("invalid OPS balance entry order")
		}
		last = id
		d, err := amount(entry.BalanceAfter, true)
		if err != nil {
			return err
		}
		value := Balance{UserID: entry.UserID, Amount: entry.BalanceAfter, Quota: entry.Quota, Negative: d.IsNegative(), LastEntryID: entry.EntryID, UpdatedAt: entry.PostedAt}
		if err = value.validate(); err != nil {
			return err
		}
		if _, err = r.gatewayQuota(value); err != nil {
			return err
		}
		values = append(values, value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cursor := r.cursor
	if p.NextCursor != nil {
		cursor = *p.NextCursor
	}
	if len(values) > 0 {
		if cursor != strconv.FormatInt(last, 10) {
			return errors.New("invalid OPS balance cursor")
		}
		previous, _ := strconv.ParseInt(r.cursor, 10, 64)
		if last < previous {
			return errors.New("OPS balance cursor moved backwards")
		}
		if *p.HasMore && last == previous {
			return errors.New("OPS balance cursor did not advance")
		}
	} else if *p.HasMore || cursor != r.cursor {
		return errors.New("invalid empty OPS balance page")
	}
	newUsers := map[string]bool{}
	for _, value := range values {
		if r.balances[value.UserID] == nil {
			newUsers[value.UserID] = true
		} else if entry := r.balances[value.UserID]; entry.value != nil && entry.value.LastEntryID == value.LastEntryID && !entry.value.samePosition(value) {
			return errors.New("OPS balance entry conflict")
		}
	}
	if len(r.balances)+len(newUsers) > cacheLimit {
		return errors.New("OPS balance cache capacity exceeded")
	}
	for i := range values {
		value := &values[i]
		entry := r.balances[value.UserID]
		if entry == nil {
			entry = &cachedBalance{}
			r.balances[value.UserID] = entry
		}
		previous := int64(0)
		if entry.value != nil {
			previous, _ = entry.value.LastEntryID.Int64()
		}
		id, _ := value.LastEntryID.Int64()
		if id > previous {
			entry.value = value
			entry.complete = false
			entry.missing = false
			entry.warm = false
			entry.authorizedAt = time.Time{}
		}
	}
	r.cursor = cursor
	r.nextBalance = at.Add(time.Minute)
	if *p.HasMore {
		r.nextBalance = at.Add(3 * time.Second)
	} else {
		for _, entry := range r.balances {
			if entry.value != nil && !entry.missing {
				entry.checked = at
				entry.failed = false
				entry.complete = true
			}
		}
	}
	return nil
}

// OPS native wallets expose micro-units, not the gateway's quota units.
// Keep the wire quota for receipt validation and convert only at the boundary.
func (r *Reader) gatewayQuota(b Balance) (int64, error) {
	if b.Currency != "" && b.Currency != r.Currency {
		return 0, errors.New("OPS balance currency mismatch")
	}
	if b.Quota == nil || r.Currency != "CNY" && r.Currency != "USD" {
		return 0, errors.New("invalid OPS wallet currency")
	}
	if r.Currency == "CNY" {
		return *b.Quota, nil
	}
	n, err := amount(b.Amount, true)
	if err != nil || !n.Mul(decimal.NewFromInt(1_000_000)).Equal(decimal.NewFromInt(*b.Quota)) {
		return 0, errors.New("inconsistent native OPS balance quota")
	}
	if b.Station != nil && (b.Station.QuotaPerUnit != 1_000_000 || b.Station.ExchangeRate != "1") {
		return 0, errors.New("invalid native OPS wallet conversion")
	}
	unit := decimal.NewFromFloat(common.QuotaPerUnit)
	if !unit.IsPositive() {
		return 0, errors.New("invalid gateway quota unit")
	}
	q := n.Mul(unit).Truncate(0)
	if q.Abs().GreaterThan(decimal.NewFromInt(common.MaxWalletQuota)) {
		return 0, errors.New("OPS balance exceeds gateway quota limit")
	}
	return q.IntPart(), nil
}
