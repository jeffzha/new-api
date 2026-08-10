package maintenance_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/maintenance"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupRemovesOnlyExpiredEphemeralState(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	old := now.Add(-72 * time.Hour)
	future := now.Add(time.Hour)
	require.NoError(t, db.Create(&model.EntryTicket{TokenHash: "old", NewAPIUserID: 1, IdentityVersion: "v1", Surface: "workbench", ExpiresAt: old}).Error)
	require.NoError(t, db.Create(&model.EntryTicket{TokenHash: "active", NewAPIUserID: 2, IdentityVersion: "v1", Surface: "workbench", ExpiresAt: future}).Error)
	require.NoError(t, db.Create(&model.ServiceNonce{ServiceName: "adp", Nonce: "old", ExpiresAt: old}).Error)
	delivered := old
	require.NoError(t, db.Create(&model.ControlOutbox{
		EventKey: "delivered", EventType: "CACHE_INVALIDATE", PayloadJSON: `{}`, Status: model.OutboxStatusDelivered,
		NextRetryAt: old, DeliveredAt: &delivered,
	}).Error)

	result, err := maintenance.New(db).Cleanup(context.Background(), now, 24*time.Hour, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.EntryTickets)
	assert.Equal(t, int64(1), result.ServiceNonces)
	assert.Equal(t, int64(1), result.DeliveredOutbox)
	var tickets int64
	require.NoError(t, db.Model(&model.EntryTicket{}).Count(&tickets).Error)
	assert.Equal(t, int64(1), tickets)
}
