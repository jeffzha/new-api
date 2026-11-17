package opspush

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
	"github.com/QuantumNous/new-api/service/opsfees"
)

const committedBatch = 250

// runCommitted is one bounded worker, independent of the cursor's ten-second
// commit-order window. Only post-commit hints can enter this lane. It never
// advances the durable cursor, so a crash, full queue or failed delivery falls
// back to ordinary scanning without skipping an earlier uncommitted log.
func (p *Pusher) runCommitted(ctx context.Context) {
	var nextBatch time.Time
	for {
		var first int
		select {
		case <-ctx.Done():
			return
		case first = <-opsnotify.Committed():
		}
		ids := []int{first}
		timer := time.NewTimer(max(25*time.Millisecond, time.Until(nextBatch)))
	collect:
		for len(ids) < committedBatch {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case id := <-opsnotify.Committed():
				ids = append(ids, id)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()
		if delay := time.Until(nextBatch); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		interval := 1500 * time.Millisecond
		if common.RedisEnabled && common.RDB != nil {
			interval = 800 * time.Millisecond
		}
		nextBatch = time.Now().Add(interval)
		_ = p.sendCommitted(ctx, ids) // durable scanner owns retries and diagnostics
	}
}

func (p *Pusher) sendCommitted(parent context.Context, ids []int) error {
	if len(ids) == 0 || len(ids) > committedBatch {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	types := []int{model.LogTypeConsume, model.LogTypeError}
	var logs []*model.Log
	if err := p.LogDB.WithContext(ctx).Where("id IN ? AND type IN ?", ids, types).Order("id").Find(&logs).Error; err != nil {
		return err
	}
	names := p.channelNames(logs)
	events, delivered := []map[string]any{}, []int{}
	for _, log := range logs {
		if !p.ownsLog(log) || p.deliveredFast(log.Id) {
			continue
		}
		meta := object(json.RawMessage(log.Other))
		if string(meta["is_task"]) == "true" {
			continue
		}
		event := Event(log, names[log.ChannelId])
		if p.Config.Spec == SpecV11 {
			event = EventV11(log, names[log.ChannelId], nil, CurrentConversion())
		}
		if event == nil {
			continue
		}
		if err := p.attachUpstream(log, event); err != nil {
			return err
		}
		events, delivered = append(events, event), append(delivered, log.Id)
	}
	if len(events) == 0 {
		return nil
	}
	var body []byte
	for {
		rawEvents, err := common.Marshal(events)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(rawEvents)
		schema := "1"
		if p.Config.Spec == SpecV11 {
			schema = "1.1"
		}
		body, err = common.Marshal(batch{SchemaVersion: schema, BatchId: fmt.Sprintf("gw-committed-%d-%s", 1, hex.EncodeToString(sum[:16])), Events: events})
		if err != nil {
			return err
		}
		if len(body) <= maxBody {
			break
		}
		if len(events) == 1 {
			return fmt.Errorf("committed usage batch exceeds limit")
		}
		// Bound HTTP without repeating SQL or spending another send budget.
		// Omitted IDs remain available to the durable scanner.
		keep := len(events) / 2
		events, delivered = events[:keep], delivered[:keep]
	}
	if common.RedisEnabled && common.RDB != nil {
		// One fleet-wide fast batch per interval, independent of which blue/
		// green slot handled calls. Busy/outage work stays in the SQL outbox.
		sum := sha256.Sum256([]byte(p.Config.URL + "\n" + p.Config.KeyID))
		for {
			gate, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			delay, err := common.RDB.Eval(gate, "if redis.call('SET',KEYS[1],'1','NX','PX',800) then return 0 else return redis.call('PTTL',KEYS[1]) end", []string{"ops-fast-push:" + hex.EncodeToString(sum[:])}).Int64()
			cancel()
			if err != nil {
				return err
			}
			if delay == 0 {
				break
			}
			timer := time.NewTimer(time.Duration(max(delay, 1)) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Config.URL+pushPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header = Sign(p.Config.KeyID, p.Config.Secret, http.MethodPost, pushPath, body, p.Now())
	request.Header.Set("Content-Type", "application/json")
	response, err := p.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(raw) > maxBody || response.StatusCode != http.StatusOK {
		return fmt.Errorf("committed usage acknowledgement unavailable")
	}
	var ack okBody
	if common.Unmarshal(raw, &ack) != nil || ack.Data.Accepted < 0 || ack.Data.Duplicates < 0 || len(ack.Data.Rejected) != 0 || ack.Data.Accepted+ack.Data.Duplicates != len(events) {
		return fmt.Errorf("committed usage acknowledgement incomplete")
	}
	opsfees.AcceptPushReceipts(p.Config.URL, p.Config.KeyID, p.Config.Secret, body, raw, p.Now())

	p.fastMu.Lock()
	defer p.fastMu.Unlock()
	if p.fastDelivered == nil {
		p.fastDelivered = map[int]time.Time{}
	}
	for id, at := range p.fastDelivered {
		if p.Now().Sub(at) > 2*time.Minute {
			delete(p.fastDelivered, id)
		}
	}
	for _, id := range delivered {
		if len(p.fastDelivered) < 2048 {
			p.fastDelivered[id] = p.Now()
		}
	}
	return nil
}

func (p *Pusher) deliveredFast(id int) bool {
	p.fastMu.Lock()
	defer p.fastMu.Unlock()
	at, ok := p.fastDelivered[id]
	if ok && p.Now().Sub(at) > 2*time.Minute {
		delete(p.fastDelivered, id)
		return false
	}
	return ok
}

// ownsLog prevents product and project processes sharing SQL logs from sending
// each other's usage. Missing scope belongs to the product; malformed scope
// never becomes a product event.
func (p *Pusher) ownsLog(log *model.Log) bool {
	raw := object(json.RawMessage(log.Other))["billing_scope"]
	if len(raw) == 0 {
		return true
	}
	var scope string
	return common.Unmarshal(raw, &scope) == nil && scope == ""
}
