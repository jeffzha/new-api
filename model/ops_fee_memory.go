package model

import (
	"container/list"
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"golang.org/x/sync/singleflight"
)

func opsFeeScope() string {
	if c := opsFeeRuntime.Load(); c != nil {
		return c.Scope
	}
	if os.Getenv("OPS_WALLET_AUTHORITY") == "ops" {
		return os.Getenv("OPS_PROJECT_SCOPE")
	}
	return ""
}

// This bounded cache contains only authorized invocation identities and fee
// results. It never owns a wallet or treats a cache miss as a zero charge.
type opsFeeMemoryEntry struct {
	fee     OpsFee
	log     *Log
	feed    bool
	expires time.Time
	changed chan struct{}
}

type opsFeeMemory struct {
	sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	loads   singleflight.Group
	waiters int
}

func newOpsFeeMemory() *opsFeeMemory {
	return &opsFeeMemory{entries: map[string]*list.Element{}, order: list.New()}
}

func (m *opsFeeMemory) get(id string, at time.Time) (*OpsFee, chan struct{}) {
	m.Lock()
	defer m.Unlock()
	e := m.entries[id]
	if e == nil {
		return nil, nil
	}
	v := e.Value.(*opsFeeMemoryEntry)
	if !at.Before(v.expires) {
		delete(m.entries, id)
		m.order.Remove(e)
		close(v.changed)
		return nil, nil
	}
	m.order.MoveToBack(e)
	copy := v.fee
	return &copy, v.changed
}

func (m *opsFeeMemory) put(fee OpsFee, feed bool, at time.Time) {
	m.Lock()
	defer m.Unlock()
	var log *Log
	if old := m.entries[fee.RequestID]; old != nil {
		previous := old.Value.(*opsFeeMemoryEntry)
		if feed && previous.feed && fee.Version > 0 && previous.fee.Version > fee.Version {
			return
		}
		if previous.fee.UserID == fee.UserID && previous.fee.Model == fee.Model {
			log = previous.log
		}
		// A delayed acknowledgement must not undo a newer incremental correction.
		if fee.Status == "waiting" && previous.fee.Status != "waiting" {
			return
		}
		if fee.Status != "waiting" && previous.fee.Status != "waiting" {
			a, ea := time.Parse(time.RFC3339Nano, fee.ChargedAt)
			b, eb := time.Parse(time.RFC3339Nano, previous.fee.ChargedAt)
			if ea == nil && eb == nil && (a.Before(b) || a.Equal(b) && previous.feed && !feed) {
				return
			}
		}
		close(previous.changed)
		m.order.Remove(old)
	}
	ttl := 30 * time.Second
	e := m.order.PushBack(&opsFeeMemoryEntry{fee: fee, log: log, feed: feed, expires: at.Add(ttl), changed: make(chan struct{})})
	m.entries[fee.RequestID] = e
	if len(m.entries) > 16000 {
		old := m.order.Front()
		v := old.Value.(*opsFeeMemoryEntry)
		delete(m.entries, v.fee.RequestID)
		m.order.Remove(old)
		close(v.changed)
	}
}

func (m *opsFeeMemory) rememberLog(log *Log) {
	m.Lock()
	defer m.Unlock()
	if e := m.entries[log.RequestId]; e != nil {
		v := e.Value.(*opsFeeMemoryEntry)
		if v.fee.UserID == strconv.Itoa(log.UserId) && v.fee.Model == log.ModelName {
			var meta struct {
				Scope string `json:"billing_scope"`
			}
			other := "invalid"
			if log.Other == "" || common.UnmarshalJsonStr(log.Other, &meta) == nil {
				raw, _ := common.Marshal(meta)
				other = string(raw)
			}
			v.log = &Log{RequestId: log.RequestId, UserId: log.UserId, ModelName: log.ModelName, Type: log.Type, Other: other}
		}
	}
}

// Shared results are display-only and only apply to the configured log scope.
// An actual authorized log read is still required before returning any amount.
func sharedLogFee(c *OpsFeeRuntime, log *Log, at time.Time) *OpsFee {
	if c.SharedLogConnection == "" || log == nil || log.Type != LogTypeConsume {
		return nil
	}
	var meta struct {
		Scope string `json:"billing_scope"`
	}
	if log.Other != "" && common.UnmarshalJsonStr(log.Other, &meta) != nil || meta.Scope != c.SharedLogScope || meta.Scope == c.Scope {
		return nil
	}
	fee, _ := c.sharedMemory.get(log.RequestId, at)
	if fee == nil || fee.UserID != strconv.Itoa(log.UserId) || fee.Model != log.ModelName {
		return nil
	}
	// The normal feed continues to own later corrections and refunds.
	if primary, _ := c.memory.get(log.RequestId, at); primary != nil && primary.UserID == fee.UserID && primary.Model == fee.Model && primary.Status != "waiting" {
		a, ea := time.Parse(time.RFC3339Nano, primary.ChargedAt)
		b, eb := time.Parse(time.RFC3339Nano, fee.ChargedAt)
		newer := primary.Version > 0 && primary.Version >= fee.Version || primary.Version == 0 && primary.FetchedAt > fee.FetchedAt
		if ea == nil && eb == nil && (a.After(b) || a.Equal(b) && newer) {
			c.memory.rememberLog(log)
			return primary
		}
	}
	c.sharedMemory.rememberLog(log)
	return fee
}

// RememberOpsFeePending runs after a successful consume-log commit. New calls
// can return pending from memory while their existing usage worker contacts OPS.
func RememberOpsFeePending(log *Log) {
	c := opsFeeRuntime.Load()
	if c == nil || log == nil || log.Type != LogTypeConsume || log.RequestId == "" || log.UserId <= 0 {
		return
	}
	var meta struct {
		Scope string `json:"billing_scope"`
	}
	if log.Other != "" && common.UnmarshalJsonStr(log.Other, &meta) != nil || meta.Scope != c.Scope {
		return
	}
	c.memory.put(OpsFee{Connection: c.Connection, RequestID: log.RequestId, UserID: strconv.Itoa(log.UserId), Model: log.ModelName, Currency: "CNY", Status: "waiting"}, false, time.Now())
	c.memory.rememberLog(log)
	c.memory.Lock()
	if e := c.memory.entries[log.RequestId]; e != nil {
		e.Value.(*opsFeeMemoryEntry).expires = time.Now().Add(time.Minute)
	}
	c.memory.Unlock()
}

// LogFeeLogs resolves only identities proven by an actual log commit/read.
// Fees received from OPS alone never grant permission to read another log.
func LogFeeLogs(ctx context.Context, ids []string, userID int) ([]*Log, error) {
	c := opsFeeRuntime.Load()
	if c == nil {
		return nil, nil
	}
	logs := make([]*Log, 0, len(ids))
	missing := []string{}
	for _, id := range ids {
		c.memory.Lock()
		e := c.memory.entries[id]
		var log *Log
		if e != nil {
			v := e.Value.(*opsFeeMemoryEntry)
			if time.Now().Before(v.expires) && v.log != nil {
				copy := *v.log
				log = &copy
			}
		}
		c.memory.Unlock()
		if log == nil && c.SharedLogConnection != "" {
			c.sharedMemory.Lock()
			if e := c.sharedMemory.entries[id]; e != nil {
				v := e.Value.(*opsFeeMemoryEntry)
				if time.Now().Before(v.expires) && v.log != nil {
					copy := *v.log
					log = &copy
				}
			}
			c.sharedMemory.Unlock()
		}
		if log == nil {
			missing = append(missing, id)
		} else if userID == 0 || log.UserId == userID {
			logs = append(logs, log)
		}
	}
	if len(missing) == 0 {
		return logs, nil
	}
	key := strconv.Itoa(userID) + "\n" + strings.Join(missing, "\n")
	value, err, _ := c.memory.loads.Do(key, func() (any, error) {
		var rows []*Log
		q := LOG_DB.WithContext(ctx).Select("request_id,user_id,model_name,type,other").Where("request_id IN ? AND type = ?", missing, LogTypeConsume)
		if userID > 0 {
			q = q.Where("user_id = ?", userID)
		}
		if err := q.Limit(100).Find(&rows).Error; err != nil {
			return nil, err
		}
		if err := AttachOpsFees(ctx, rows); err != nil {
			return nil, err
		}
		for _, row := range rows {
			c.memory.rememberLog(row)
		}
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	for _, row := range value.([]*Log) {
		copy := *row
		logs = append(logs, &copy)
	}
	return logs, nil
}

// AcceptOpsFeeResults is used only after a validated OPS response or committed
// incremental page. Connection binding prevents product/project cross-wiring.
func AcceptOpsFeeResults(connection string, rows []OpsFee, feed bool) {
	version := time.Now().UnixNano()
	for i := range rows {
		rows[i].Version = version
	}
	AcceptSharedOpsFeeResults(connection, rows, feed)
	if c := opsFeeRuntime.Load(); c != nil && c.Connection == connection && c.ShareResults != nil && len(rows) > 0 {
		c.ShareResults(rows, feed)
	}
}

// Shared delivery never republishes its own messages.
func AcceptSharedOpsFeeResults(connection string, rows []OpsFee, feed bool) {
	c := opsFeeRuntime.Load()
	if c == nil {
		return
	}
	memory := c.memory
	if c.Connection != connection {
		if c.SharedLogConnection == "" || c.SharedLogConnection != connection {
			return
		}
		memory = c.sharedMemory
	}
	for _, row := range rows {
		if row.RequestID == "" || row.UserID == "" || row.Model == "" || row.Connection != "" && row.Connection != connection {
			continue
		}
		memory.put(row, feed, time.Now())
	}
}

// WaitOpsFee waits on a receipt notification, never on a database polling loop.
// HTTP request retention is capped even during an OPS outage.
func WaitOpsFee(ctx context.Context, log *Log, wait time.Duration) *OpsFee {
	c := opsFeeRuntime.Load()
	if c == nil || log == nil {
		return nil
	}
	read := func() (*OpsFee, chan struct{}) {
		fee, changed := c.memory.get(log.RequestId, time.Now())
		if fee == nil || fee.UserID != strconv.Itoa(log.UserId) || fee.Model != log.ModelName {
			return nil, nil
		}
		return fee, changed
	}
	fee, changed := read()
	if fee == nil || fee.Status != "waiting" || wait <= 0 || changed == nil {
		return fee
	}
	c.memory.Lock()
	if c.memory.waiters >= 1024 {
		c.memory.Unlock()
		return fee
	}
	c.memory.waiters++
	c.memory.Unlock()
	defer func() { c.memory.Lock(); c.memory.waiters--; c.memory.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, min(wait, time.Second))
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return fee
		case <-changed:
		}
		fee, changed = read()
		if fee == nil || fee.Status != "waiting" || changed == nil {
			return fee
		}
	}
}
