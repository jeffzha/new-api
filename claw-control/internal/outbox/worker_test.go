package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/outbox"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recordingPublisher struct {
	events []outbox.Event
	err    error
}

func (p *recordingPublisher) Publish(_ context.Context, event outbox.Event) error {
	p.events = append(p.events, event)
	return p.err
}

func TestWorkerDeliversPersistedEventAndMarksItComplete(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return support.Enqueue(tx, nil, "CACHE_INVALIDATE", "event-1", map[string]any{"auth_epoch": 2})
	}))
	publisher := &recordingPublisher{}
	worker := outbox.New(db, publisher)

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	require.Len(t, publisher.events, 1)
	assert.Equal(t, "event-1", publisher.events[0].EventKey)
	assert.JSONEq(t, `{"auth_epoch":2}`, string(publisher.events[0].Payload))

	var row model.ControlOutbox
	require.NoError(t, db.Where("event_key = ?", "event-1").First(&row).Error)
	assert.Equal(t, model.OutboxStatusDelivered, row.Status)
	assert.NotNil(t, row.DeliveredAt)
}

func TestWorkerRetriesFailedDeliveryWithoutLosingEvent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return support.Enqueue(tx, nil, "SESSION_REVOKE", "event-2", map[string]any{"user_id": 42})
	}))
	publisher := &recordingPublisher{err: errors.New("redis unavailable\nsecret-free")}
	worker := outbox.New(db, publisher)

	processed, err := worker.ProcessOne(context.Background())
	assert.Error(t, err)
	assert.True(t, processed)
	var row model.ControlOutbox
	require.NoError(t, db.Where("event_key = ?", "event-2").First(&row).Error)
	assert.Equal(t, model.OutboxStatusPending, row.Status)
	assert.Equal(t, 1, row.Attempts)
	assert.Greater(t, row.NextRetryAt, time.Now().UTC())
	assert.NotContains(t, row.LastError, "\n")
}
