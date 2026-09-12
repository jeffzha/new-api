package agencyhub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/require"
)

// TestDrainConsumerBudgetSustainsAbove60RPS guards the §22.4 invariant that a
// single scheduler tick must be able to drain more than 120 events (the 2s
// peak window). The previous single RunConsumerOnce(100) per tick capped
// sustained drain at 50/s, below the 60 RPS steady target.
func TestDrainConsumerBudgetSustainsAbove60RPS(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.CommissionEnabled = true
	now := time.Now().UnixMilli()
	agencyID := int64(30001)
	outboxes := make([]model.AgencyBillingOutbox, 0, 700)
	deliveries := make([]model.AgencyEventDelivery, 0, 700)
	for i := 0; i < 700; i++ {
		event := agencycontract.BillingEvent{
			SchemaVersion: agencycontract.SchemaVersion, EventID: fmt.Sprintf("drain-ev-%d", i),
			EventType: "agency.billing_finalized", OperationID: fmt.Sprintf("drain-op-%d", i),
			UserID: int64(1000000 + i), AgencyID: &agencyID, OriginModelName: "gpt-4o",
			CurrencyCode: "CNY", CommissionEligible: true, CommissionAmountMicros: 10, OccurredAtMS: now,
		}
		payload, err := common.Marshal(event)
		require.NoError(t, err)
		payloadHash, err := agencycontract.CanonicalHash(event)
		require.NoError(t, err)
		outboxes = append(outboxes, model.AgencyBillingOutbox{
			EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType,
			UserID: event.UserID, MoneySeq: int64(i), Payload: string(payload), PayloadHash: payloadHash,
			SchemaVersion: event.SchemaVersion, CreatedAtMS: now,
		})
		deliveries = append(deliveries, model.AgencyEventDelivery{EventID: event.EventID, Status: "pending", NextRetryAt: 1, CreatedAt: now / 1000})
	}
	require.NoError(t, app.db.Create(&outboxes).Error)
	require.NoError(t, app.db.Create(&deliveries).Error)

	require.NoError(t, app.drainConsumerBudget(context.Background()))

	var pending int64
	require.NoError(t, app.db.Model(&model.AgencyEventDelivery{}).Where("status <> ?", "done").Count(&pending).Error)
	require.Zero(t, pending, "one tick must drain a backlog larger than the 2s peak window")

	// An empty queue must return immediately without erroring.
	require.NoError(t, app.drainConsumerBudget(context.Background()))
}
