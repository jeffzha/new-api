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
)

const committedBatch = 50

// runCommitted is one bounded worker, independent of the cursor's ten-second
// commit-order window. Only post-commit hints can enter this lane. It never
// advances the durable cursor, so a crash, full queue or failed delivery falls
// back to ordinary scanning without skipping an earlier uncommitted log.
func (p *Pusher) runCommitted(ctx context.Context) {
	for {
		var first int
		select {
		case <-ctx.Done():
			return
		case first = <-opsnotify.Committed():
		}
		ids := []int{first}
		timer := time.NewTimer(100 * time.Millisecond)
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
		_ = p.sendCommitted(ctx, ids) // durable scanner owns retries and diagnostics
		// At most one fast HTTP batch per second, including during an outage.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
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
	rawEvents, err := common.Marshal(events)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(rawEvents)
	schema := "1"
	if p.Config.Spec == SpecV11 {
		schema = "1.1"
	}
	body, err := common.Marshal(batch{SchemaVersion: schema, BatchId: fmt.Sprintf("gw-committed-%d-%s", 1, hex.EncodeToString(sum[:16])), Events: events})
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return fmt.Errorf("committed usage batch exceeds limit")
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
