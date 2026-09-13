package agencyhub

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconciliationOperationRequiresExactCommittedOutboxEvent(t *testing.T) {
	for _, flaw := range []string{"event_identity", "payload", "stored_hash", "user_metadata", "multi_event_result"} {
		t.Run(flaw, func(t *testing.T) {
			client := newFinanceRootClient(t)
			outbox := reconciliationOutboxFixture(t, client.app.db)
			op := model.AgencyBillingOperation{ChargeID: outbox.OperationID, OperationID: outbox.OperationID, Revision: 1, Operation: "topup", InputHash: "source", CommittedResult: outbox.Payload, MoneySeq: outbox.MoneySeq, EventCount: 1}
			var event agencycontract.BillingEvent
			require.NoError(t, common.Unmarshal([]byte(outbox.Payload), &event))
			switch flaw {
			case "event_identity":
				event.EventID = "committed-event-with-no-outbox"
				payload, err := common.Marshal(event)
				require.NoError(t, err)
				op.CommittedResult = string(payload)
			case "payload":
				event.StandardQuota++
				payload, err := common.Marshal(event)
				require.NoError(t, err)
				hash, err := agencycontract.CanonicalHash(event)
				require.NoError(t, err)
				require.NoError(t, client.app.db.Model(&outbox).Updates(map[string]any{"payload": string(payload), "payload_hash": hash}).Error)
			case "stored_hash":
				require.NoError(t, client.app.db.Model(&outbox).Update("payload_hash", strings.Repeat("0", 64)).Error)
			case "user_metadata":
				require.NoError(t, client.app.db.Model(&outbox).Update("user_id", outbox.UserID+1).Error)
			case "multi_event_result":
				event.EventCount = 2
				payload, err := common.Marshal(event)
				require.NoError(t, err)
				op.EventCount, op.CommittedResult = 2, string(payload)
			}
			require.NoError(t, client.app.db.Create(&op).Error)
			issue := newReconciliationTestIssue(t, client.app, "billing_operation", op.OperationID)
			v := reconciliationDetail(t, client, issue.ID)
			if flaw == "multi_event_result" {
				assert.Equal(t, "unsupported", v.State, "no multiple-event result schema exists in the current writer")
			} else {
				assert.Equal(t, "inconsistent", v.State)
			}
			assert.Empty(t, v.AllowedActions)
			for _, status := range []string{"resolved", "ignored"} {
				body := reconciliationBody(t, "verify_resolved", status, v.EvidenceHash)
				result := client.post("/agency/api/v1/root/reconciliation/issues/"+stringID(issue.ID)+"/resolve", body, "reject-operation-"+status, client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "operation-proof-"+status))
				assert.Equal(t, http.StatusConflict, result.Code, result.Body.String())
				assert.Contains(t, result.Body.String(), "invariant_unresolved")
			}
			require.NoError(t, client.app.db.First(&issue, issue.ID).Error)
			assert.Equal(t, "open", issue.Status)
			var proofs int64
			require.NoError(t, client.app.db.Model(&model.AgencyVerificationUse{}).Count(&proofs).Error)
			assert.Zero(t, proofs)
		})
	}
}

func TestReconciliationDeliveryCompletionRequiresMatchingPermanentReceipt(t *testing.T) {
	for _, test := range []struct {
		status, receipt, expected string
	}{
		{"done", "", "inconsistent"},
		{"done", "processing", "inconsistent"},
		{"poison", "", "inconsistent"},
		{"unknown", "", "inconsistent"},
		{"done", "done", "consistent"},
		{"done", "skipped", "consistent"},
		{"pending", "", "consistent"},
		{"retry", "", "consistent"},
		{"claimed", "", "consistent"},
		// A replay can have a terminal permanent receipt before its delivery
		// is marked done. Consumer deduplication safely completes that delivery.
		{"pending", "done", "consistent"},
	} {
		t.Run(test.status+"/receipt="+test.receipt, func(t *testing.T) {
			client := newFinanceRootClient(t)
			outbox := reconciliationOutboxFixture(t, client.app.db)
			delivery := model.AgencyEventDelivery{EventID: outbox.EventID, Status: test.status}
			require.NoError(t, client.app.db.Create(&delivery).Error)
			if test.receipt != "" {
				require.NoError(t, client.app.db.Create(&model.AgencySourceEvent{EventID: outbox.EventID, SourceOperationID: outbox.OperationID, SchemaVersion: outbox.SchemaVersion, PayloadHash: outbox.PayloadHash, Payload: outbox.Payload, UserID: outbox.UserID, MoneySeq: outbox.MoneySeq, JournalRevision: 1, ProcessingStatus: test.receipt}).Error)
			}
			issue := newReconciliationTestIssue(t, client.app, "billing_outbox", outbox.EventID)
			v := reconciliationDetail(t, client, issue.ID)
			assert.Equal(t, test.expected, v.State)
			assert.NotContains(t, v.AllowedActions, "restore_delivery", "existing state must never be overwritten by missing-row repair")
			body := reconciliationBody(t, "verify_resolved", "resolved", v.EvidenceHash)
			result := client.post("/agency/api/v1/root/reconciliation/issues/"+stringID(issue.ID)+"/resolve", body, "delivery-state-key", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "delivery-state-proof"))
			if test.expected == "consistent" {
				assert.Equal(t, http.StatusOK, result.Code, result.Body.String())
			} else {
				assert.Empty(t, v.AllowedActions)
				assert.Equal(t, http.StatusConflict, result.Code, result.Body.String())
			}
			require.NoError(t, client.app.db.First(&delivery, delivery.ID).Error)
			assert.Equal(t, test.status, delivery.Status)
		})
	}
}

func TestReconciliationDeliveryStateChangeInvalidatesInspection(t *testing.T) {
	client := newFinanceRootClient(t)
	outbox := reconciliationOutboxFixture(t, client.app.db)
	delivery := model.AgencyEventDelivery{EventID: outbox.EventID, Status: "pending"}
	require.NoError(t, client.app.db.Create(&delivery).Error)
	issue := newReconciliationTestIssue(t, client.app, "billing_outbox", outbox.EventID)
	v := reconciliationDetail(t, client, issue.ID)
	require.NoError(t, client.app.db.Model(&delivery).Update("status", "poison").Error)
	body := reconciliationBody(t, "verify_resolved", "resolved", v.EvidenceHash)
	result := client.post("/agency/api/v1/root/reconciliation/issues/"+stringID(issue.ID)+"/resolve", body, "delivery-stale-state-key", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "delivery-stale-proof"))
	assert.Equal(t, http.StatusConflict, result.Code, result.Body.String())
	assert.Contains(t, result.Body.String(), "evidence_changed")
}
