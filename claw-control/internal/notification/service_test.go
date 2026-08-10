package notification_test

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/notification"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanNotificationWorkerIsPersistentAndIdempotent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	now := time.Now().UTC()
	customer := model.Customer{CustomerCode: "notify-customer", DisplayName: "Notify", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	prior := model.PlanPeriod{
		CustomerID: customer.ID, PlanVersionID: 1, StartAt: now.AddDate(0, -2, 0), EndAt: now.AddDate(0, -1, 0),
		AmountCNY: "10.00", PaymentMode: model.PaymentModeOfflineManual,
		PaymentStatus: model.PaymentStatusPaid, Status: model.PeriodStatusExpired,
		SnapshotJSON: `{}`, RowVersion: 1,
	}
	require.NoError(t, db.Create(&prior).Error)
	current := model.PlanPeriod{
		CustomerID: customer.ID, PlanVersionID: 1, StartAt: now.Add(-time.Hour), EndAt: now.Add(3 * 24 * time.Hour),
		AmountCNY: "10.00", PaymentMode: model.PaymentModeOfflineManual,
		PaymentStatus: model.PaymentStatusPaid, Status: model.PeriodStatusActive,
		SnapshotJSON: `{}`, RowVersion: 1,
	}
	require.NoError(t, db.Create(&current).Error)
	require.NoError(t, db.Create(&model.AdminAudit{
		CustomerID: &customer.ID, Actor: "period-worker", Action: "app.suspend.plan_inactive",
		ResourceType: "customer_app", ResourceID: "7", Result: "success", CreatedAt: now.Add(-time.Minute),
	}).Error)
	service := notification.New(db)

	require.NoError(t, service.Reconcile(now))
	require.NoError(t, service.Reconcile(now.Add(time.Minute)))
	items, err := service.List(notification.ListQuery{CustomerID: &customer.ID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, items, 3)
	types := []string{items[0].Type, items[1].Type, items[2].Type}
	assert.ElementsMatch(t, []string{"plan_expiring", "plan_renewed", "plan_suspended"}, types)

	read, err := service.MarkRead(items[0].PublicID, "admin-session:2", "request-read")
	require.NoError(t, err)
	assert.Equal(t, model.NotificationStatusRead, read.Status)
	readAgain, err := service.MarkRead(items[0].PublicID, "admin-session:2", "request-read-retry")
	require.NoError(t, err)
	assert.Equal(t, read.ReadAt, readAgain.ReadAt)
}
