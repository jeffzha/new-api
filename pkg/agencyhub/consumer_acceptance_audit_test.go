//go:build agency_audit

package agencyhub

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Design §9.2 requires validating the authoritative journal before creating
// withdrawable commission, even when the outbox payload has a valid hash.
func TestAgencyAuditConsumerDoesNotPayWithoutCommittedJournal(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(12)
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "audit-unbacked-event",
		EventType: "agency.billing_finalized", FinancialChargeID: "audit-missing-journal",
		OperationID: "audit-missing-operation", JournalRevision: 2, MoneySeq: 1, EventCount: 1,
		UserID: 101, AgencyID: &agencyID, OriginModelName: "Hunyuan/hy3",
		BusinessStatus: "success", BillingStatus: "finalized", FinancialFinal: true,
		CurrencyCode: "CNY", QuotaPerUnit: "500000", ExchangeRate: "1",
		ChargedTotalQuota: 900, CommissionableQuota: 900, SettlementCostQuota: 750,
		TheoreticalCommissionQuota: 150, PaidAllocatedQuota: 600, CommissionQuota: 100,
		CommissionAmountMicros: 200, CommissionEligible: true, OccurredAtMS: 1,
	}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	hash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{
		EventID: event.EventID, OperationID: event.OperationID, EventCount: 1,
		EventKind: event.EventType, UserID: event.UserID, MoneySeq: event.MoneySeq,
		Payload: string(payload), PayloadHash: hash, SchemaVersion: event.SchemaVersion, CreatedAtMS: 1,
	}).Error)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: event.EventID, Status: "pending", CreatedAt: 1}).Error)
	require.NoError(t, app.RunConsumerOnce(context.Background(), 10))
	var entries int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", event.EventID).Count(&entries).Error)
	assert.Zero(t, entries, "a valid event hash is not proof that the wallet/journal transaction committed")
}
