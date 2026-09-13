package model

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The submission receipt is the durable boundary around provider I/O. These
// tests exercise the observable state transitions without requiring a live
// provider or a full gateway fixture.
func TestAgencyTaskSubmissionReceiptTransitionsAreIdempotent(t *testing.T) {
	dsn := "file:agency-task-submission-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
	require.NoError(t, db.AutoMigrate(&AgencyBillingJournal{}, &AgencyTaskSubmissionAttempt{}))
	require.NoError(t, db.Create(&AgencyBillingJournal{
		ChargeID: "charge-receipt-1", SegmentNo: 0, UserID: 42,
		Status: "reserved", PricingSnapshot: "{}", BillingBasis: "{}",
		BusinessStatus: "pending", DeliveryStatus: "pending", Version: 1,
	}).Error)

	require.NoError(t, BeginAgencyTaskSubmission(42, "charge-receipt-1", "public-task-1", "request-hash", "/v1/videos"))
	var attempt AgencyTaskSubmissionAttempt
	require.NoError(t, db.Where("charge_id = ?", "charge-receipt-1").First(&attempt).Error)
	require.Equal(t, "submitting", attempt.Status)
	require.NoError(t, RecordAgencyTaskSubmission("charge-receipt-1", "public-task-1", "provider-task-1"))
	// A replay of the provider receipt is a no-op.
	require.NoError(t, RecordAgencyTaskSubmission("charge-receipt-1", "public-task-1", "provider-task-1"))
	require.NoError(t, db.First(&attempt, attempt.ID).Error)
	require.Equal(t, "submitted", attempt.Status)
	require.Equal(t, "provider-task-1", attempt.ProviderTaskID)
	var journal AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", "charge-receipt-1").First(&journal).Error)
	require.Equal(t, "submitted", journal.Status)

	require.NoError(t, ResolveAgencyTaskSubmissionFailure("charge-receipt-1", false, "provider lookup required"))
	require.NoError(t, db.First(&attempt, attempt.ID).Error)
	require.Equal(t, "reconcile_required", attempt.Status)
	require.Equal(t, "unknown", attempt.EvidenceTrace)
	require.NoError(t, db.First(&journal, journal.ID).Error)
	require.Equal(t, "reconcile_required", journal.Status)
	// Unknown outcome may be retried by reconciliation, but never creates a
	// second receipt for the same charge/public task ID.
	require.ErrorIs(t, BeginAgencyTaskSubmission(42, "charge-receipt-1", "public-task-1", "request-hash", "/v1/videos"), ErrAgencyChargeConflict)
}

func TestAgencyTaskSubmissionDoesNotPersistProviderError(t *testing.T) {
	dsn := "file:agency-task-submission-redaction-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
	require.NoError(t, db.AutoMigrate(&AgencyBillingJournal{}, &AgencyTaskSubmissionAttempt{}))
	require.NoError(t, db.Create(&AgencyBillingJournal{ChargeID: "charge-redact", UserID: 7, Status: "reserved", PricingSnapshot: "{}", BillingBasis: "{}", Version: 1}).Error)
	require.NoError(t, BeginAgencyTaskSubmission(7, "charge-redact", "public-redact", "hash", "/v1/videos"))
	require.NoError(t, ResolveAgencyTaskSubmissionFailure("charge-redact", false, "dial tcp 10.0.0.1: secret-api-key"))
	var attempt AgencyTaskSubmissionAttempt
	require.NoError(t, db.First(&attempt).Error)
	require.Equal(t, "unknown", attempt.EvidenceTrace)
}
