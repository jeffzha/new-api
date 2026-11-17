package opsbridge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
)

type BillingEventSource interface {
	NextOpsBillingEvent(context.Context, int64) (*BillingEvent, error)
}

type BillingEvent struct {
	Cursor      int64
	PayloadHash string
	SourceProof string
	RawPayload  string
}

func BillingSourceProof(rawPayload string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(rawPayload)))
}

// PublishBillingEvent first persists the immutable delivery and only then
// advances the source cursor. Replaying after either transition is harmless.
func (b *Bridge) PublishBillingEvent(ctx context.Context) (bool, error) {
	if b.billingSource == nil {
		return false, nil
	}
	b.mu.Lock()
	cursor := b.state.BillingCursor
	b.mu.Unlock()
	event, err := b.billingSource.NextOpsBillingEvent(ctx, cursor)
	if err != nil || event == nil {
		return false, err
	}
	if event.Cursor <= cursor || event.PayloadHash == "" || event.RawPayload == "" {
		return true, errors.New("invalid Ops billing source event")
	}
	if event.SourceProof == "" {
		return true, errors.New("invalid Ops billing event source proof")
	}
	// Raw JSON is transported as a string so durable bridge persistence never
	// coerces int64 values through float64 or changes source escaping. TokenOps
	// verifies these exact UTF-8 bytes before parsing the event.
	body := map[string]any{"sourceSystem": "new-api", "sourceProof": event.SourceProof, "rawEvents": []string{event.RawPayload}}
	key := "agency-billing-outbox:" + strconv.FormatInt(event.Cursor, 10) + ":" + event.PayloadHash
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.BillingCursor != cursor {
		return true, nil
	}
	if err := b.enqueueLocked("agency-center/billing-events", key, body, "", ""); err != nil {
		return true, err
	}
	b.state.BillingCursor = event.Cursor
	return true, b.saveLocked()
}
