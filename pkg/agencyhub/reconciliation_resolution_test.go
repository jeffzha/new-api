package agencyhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func reconciliationDetail(t *testing.T, client financeRootClient, id int64) reconciliationVerification {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/agency/api/v1/root/reconciliation/issues/"+stringID(id), nil)
	request.AddCookie(&http.Cookie{Name: client.app.config.CookieName, Value: client.sessionToken})
	result := httptest.NewRecorder()
	client.app.Router().ServeHTTP(result, request)
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	var response struct {
		Data struct {
			Issue struct {
				ID string `json:"id"`
			} `json:"issue"`
			Verification reconciliationVerification `json:"verification"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(result.Body.Bytes(), &response))
	assert.Equal(t, stringID(id), response.Data.Issue.ID, "HTTP contract must never round large identifiers")
	return response.Data.Verification
}

func reconciliationBody(t *testing.T, action, status, hash string) string {
	t.Helper()
	encoded, err := common.Marshal(reconciliationResolutionRequest{Action: action, Status: status, ExpectedEvidenceHash: hash, Resolution: "Reviewed authoritative records and verified this invariant."})
	require.NoError(t, err)
	return string(encoded)
}

func newReconciliationTestIssue(t *testing.T, app *App, kind, id string) model.AgencyReconciliationIssue {
	t.Helper()
	count, err := app.createReconciliationIssue(context.Background(), kind, id, "detected discrepancy")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var issue model.AgencyReconciliationIssue
	require.NoError(t, app.db.Where("object_type = ? AND object_id = ? AND status = ?", kind, id, "open").First(&issue).Error)
	return issue
}

func TestReconciliationCannotHideMismatchAndRejectsStaleEvidence(t *testing.T) {
	client := newFinanceRootClient(t)
	balance := model.AgencyCommissionBalance{AgencyID: 71, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 90, Version: 1}
	require.NoError(t, client.app.db.Create(&balance).Error)
	issue := newReconciliationTestIssue(t, client.app, "commission_balance", "71:CNY")
	verification := reconciliationDetail(t, client, issue.ID)
	assert.Equal(t, "inconsistent", verification.State)
	assert.Empty(t, verification.AllowedActions)
	path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
	for _, status := range []string{"resolved", "ignored"} {
		body := reconciliationBody(t, "verify_resolved", status, verification.EvidenceHash)
		proof := client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "mismatch-"+status)
		result := client.post(path, body, "mismatch-key-"+status, proof)
		assert.Equal(t, http.StatusConflict, result.Code, result.Body.String())
		assert.Contains(t, result.Body.String(), "invariant_unresolved")
	}
	require.NoError(t, client.app.db.Model(&balance).Updates(map[string]any{"available_micros": 100, "version": 2}).Error)
	body := reconciliationBody(t, "verify_resolved", "resolved", verification.EvidenceHash)
	result := client.post(path, body, "stale-evidence-key", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "stale"))
	assert.Equal(t, http.StatusConflict, result.Code, result.Body.String())
	assert.Contains(t, result.Body.String(), "evidence_changed")
	require.NoError(t, client.app.db.First(&issue, issue.ID).Error)
	assert.Equal(t, "open", issue.Status)
	var used int64
	require.NoError(t, client.app.db.Model(&model.AgencyVerificationUse{}).Count(&used).Error)
	assert.Zero(t, used, "rejected correction must not consume proof outside its transaction")
}

func TestReconciliationRootProofIdempotencyAndRecurringHistory(t *testing.T) {
	client := newFinanceRootClient(t)
	require.NoError(t, client.app.db.Create(&model.AgencyCommissionBalance{AgencyID: 72, CurrencyCode: "CNY", EarnedMicros: 9007199254740993, AvailableMicros: 9007199254740993, Version: 1}).Error)
	issue := newReconciliationTestIssue(t, client.app, "commission_balance", "72:CNY")
	verification := reconciliationDetail(t, client, issue.ID)
	assert.Equal(t, "consistent", verification.State)
	assert.Contains(t, verification.Checks, reconciliationCheck{Name: "commission_balance_equation", Expected: "9007199254740993", Actual: "9007199254740993", Matched: true})
	path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
	body := reconciliationBody(t, "verify_resolved", "resolved", verification.EvidenceHash)
	for _, proof := range []string{"", client.proof(t, body, "agency.disable", "reconciliation_issue:"+stringID(issue.ID), "wrong-action"), client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:9999", "wrong-object")} {
		result := client.post(path, body, "protected-resolve-key", proof)
		assert.Equal(t, http.StatusForbidden, result.Code, result.Body.String())
	}
	proof := client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "valid-close")
	first := client.post(path, body, "protected-resolve-key", proof)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	replay := client.post(path, body, "protected-resolve-key", "")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	assert.Equal(t, first.Body.String(), replay.Body.String())
	different := reconciliationBody(t, "verify_resolved", "ignored", verification.EvidenceHash)
	conflict := client.post(path, different, "protected-resolve-key", client.proof(t, different, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "changed-body"))
	assert.Equal(t, http.StatusConflict, conflict.Code)
	assert.Contains(t, conflict.Body.String(), "idempotency_conflict")
	require.NoError(t, client.app.db.First(&issue, issue.ID).Error)
	assert.Equal(t, "resolved", issue.Status)
	assert.Nil(t, issue.ActiveKey)
	assert.Contains(t, issue.ResolutionEvidence, "commission_balance_equation")
	for recurrence := 0; recurrence < 2; recurrence++ {
		next := newReconciliationTestIssue(t, client.app, "commission_balance", "72:CNY")
		fresh := reconciliationDetail(t, client, next.ID)
		body := reconciliationBody(t, "verify_resolved", "ignored", fresh.EvidenceHash)
		object := "reconciliation_issue:" + stringID(next.ID)
		response := client.post("/agency/api/v1/root/reconciliation/issues/"+stringID(next.ID)+"/resolve", body, fmt.Sprintf("recurring-key-%d", recurrence), client.proof(t, body, "reconciliation.resolve", object, fmt.Sprintf("recurring-proof-%d", recurrence)))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	var rows []model.AgencyReconciliationIssue
	require.NoError(t, client.app.db.Where("object_type = ? AND object_id = ?", "commission_balance", "72:CNY").Find(&rows).Error)
	require.Len(t, rows, 3)
	for _, row := range rows {
		assert.NotEqual(t, "open", row.Status)
		assert.NotEmpty(t, row.ResolutionEvidence)
	}
	var audits int64
	require.NoError(t, client.app.db.Model(&model.AgencyAuditLog{}).Where("action = ?", "reconciliation.resolve").Count(&audits).Error)
	assert.Equal(t, int64(3), audits)
}

func TestReconciliationRestoresOnlyVerifiedMissingDeliveryWithoutMoneyChanges(t *testing.T) {
	for _, receiptStatus := range []string{"", "done"} {
		t.Run("receipt-"+receiptStatus, func(t *testing.T) {
			client := newFinanceRootClient(t)
			event := agencycontract.BillingEvent{EventID: "restore-event", OperationID: "restore-operation", EventType: "agency.billing_finalized", SchemaVersion: agencycontract.SchemaVersion, UserID: 1, MoneySeq: 1, EventCount: 1, OccurredAtMS: time.Now().UnixMilli()}
			payload, err := common.Marshal(event)
			require.NoError(t, err)
			hash, err := agencycontract.CanonicalHash(event)
			require.NoError(t, err)
			outbox := model.AgencyBillingOutbox{EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType, SchemaVersion: event.SchemaVersion, UserID: event.UserID, MoneySeq: 1, Payload: string(payload), PayloadHash: hash, EventCount: 1}
			require.NoError(t, client.app.db.Create(&outbox).Error)
			if receiptStatus != "" {
				require.NoError(t, client.app.db.Create(&model.AgencySourceEvent{EventID: event.EventID, SourceOperationID: event.OperationID, SchemaVersion: event.SchemaVersion, PayloadHash: hash, Payload: string(payload), UserID: 1, MoneySeq: 1, ProcessingStatus: receiptStatus}).Error)
			}
			issue := newReconciliationTestIssue(t, client.app, "billing_outbox", event.EventID)
			verification := reconciliationDetail(t, client, issue.ID)
			assert.Equal(t, "inconsistent", verification.State)
			assert.Equal(t, []string{"restore_delivery"}, verification.AllowedActions)
			body := reconciliationBody(t, "restore_delivery", "resolved", verification.EvidenceHash)
			path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
			proof := client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "restore-proof")
			response := client.post(path, body, "restore-delivery-key", proof)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			replay := client.post(path, body, "restore-delivery-key", proof)
			require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
			var deliveries []model.AgencyEventDelivery
			require.NoError(t, client.app.db.Find(&deliveries).Error)
			require.Len(t, deliveries, 1)
			if receiptStatus == "" {
				assert.Equal(t, "pending", deliveries[0].Status)
			} else {
				assert.Equal(t, "done", deliveries[0].Status)
			}
			var money int64
			require.NoError(t, client.app.db.Model(&model.AgencyCommissionLedger{}).Count(&money).Error)
			assert.Zero(t, money)
			require.NoError(t, client.app.db.First(&issue, issue.ID).Error)
			assert.Equal(t, event.EventID, issue.RepairEventID)
			assert.Contains(t, issue.ResolutionEvidence, "restore_delivery")
		})
	}
}

func TestReconciliationAuditFailureRollsBackProofClosureAndIdempotency(t *testing.T) {
	client := newFinanceRootClient(t)
	require.NoError(t, client.app.db.Create(&model.AgencyCommissionBalance{AgencyID: 75, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 100, Version: 1}).Error)
	issue := newReconciliationTestIssue(t, client.app, "commission_balance", "75:CNY")
	verification := reconciliationDetail(t, client, issue.ID)
	body := reconciliationBody(t, "verify_resolved", "resolved", verification.EvidenceHash)
	proof := client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "audit-rollback-proof")
	require.NoError(t, client.app.db.Callback().Create().Before("gorm:create").Register("test:reconcile-audit-failure", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AgencyAuditLog" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}))
	path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
	result := client.post(path, body, "rollback-resolution-key", proof)
	assert.Equal(t, http.StatusServiceUnavailable, result.Code, result.Body.String())
	require.NoError(t, client.app.db.Callback().Create().Remove("test:reconcile-audit-failure"))
	require.NoError(t, client.app.db.First(&issue, issue.ID).Error)
	assert.Equal(t, "open", issue.Status)
	for _, table := range []any{&model.AgencyVerificationUse{}, &model.AgencyIdempotencyRecord{}} {
		var count int64
		require.NoError(t, client.app.db.Model(table).Count(&count).Error)
		assert.Zero(t, count)
	}
	result = client.post(path, body, "rollback-resolution-key", proof)
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
}

func TestReconciliationDetailPreservesLargeIssueIDAndUnsupportedSource(t *testing.T) {
	client := newFinanceRootClient(t)
	issue := model.AgencyReconciliationIssue{ID: 9007199254740993, ObjectType: "unknown_financial_issue", ObjectID: "source", Difference: "requires external evidence", Status: "open"}
	require.NoError(t, client.app.db.Create(&issue).Error)
	verification := reconciliationDetail(t, client, issue.ID)
	assert.Equal(t, "unsupported", verification.State)
	assert.Empty(t, verification.AllowedActions)
	body := reconciliationBody(t, "verify_resolved", "ignored", verification.EvidenceHash)
	path := "/agency/api/v1/root/reconciliation/issues/" + strconv.FormatInt(issue.ID, 10) + "/resolve"
	result := client.post(path, body, "unsupported-issue-key", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "unsupported-proof"))
	assert.Equal(t, http.StatusConflict, result.Code, result.Body.String())
	assert.Contains(t, result.Body.String(), "invariant_unresolved")
}

func TestReconciliationLegacyClosedFindingsRemainBlockingUntilReverified(t *testing.T) {
	for _, legacyStatus := range []string{"ignored", "resolved"} {
		t.Run(legacyStatus, func(t *testing.T) {
			client := newFinanceRootClient(t)
			agency := model.Agency{Code: common.GetUUID(), InviteCode: common.GetUUID()[:16], DisplayName: "Legacy reconciliation", Status: AgencyStatusActive}
			require.NoError(t, client.app.db.Create(&agency).Error)
			account := model.AgencyWithdrawalAccount{AgencyID: agency.ID, Version: 1, Ciphertext: "frozen-account", KeyID: "test-key", Status: "active"}
			require.NoError(t, client.app.db.Create(&account).Error)
			hash := sha256.Sum256([]byte(account.Ciphertext))
			withdrawal := model.AgencyWithdrawal{AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 10, AccountID: account.ID, AccountVersion: account.Version, AccountSnapshot: account.Ciphertext, AccountSnapshotKeyID: account.KeyID, AccountSnapshotHash: hex.EncodeToString(hash[:])}
			balance := model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", EarnedMicros: 101, AvailableMicros: 90, LockedMicros: 10, Version: 1}
			require.NoError(t, client.app.db.Create(&balance).Error)
			issue := model.AgencyReconciliationIssue{ObjectType: "commission_balance", ObjectID: fmt.Sprintf("%d:CNY", agency.ID), Difference: "historical discrepancy", Status: legacyStatus, Resolution: "old unchecked explanation"}
			require.NoError(t, client.app.db.Create(&issue).Error)
			backlog, err := client.app.agencyBacklogStatus(context.Background())
			require.NoError(t, err)
			assert.Equal(t, int64(1), backlog["open_reconciliation_issues"])
			assert.EqualError(t, client.app.validateWithdrawalPayingGate(client.app.db, withdrawal), "agency has open reconciliation issues")
			bad := reconciliationDetail(t, client, issue.ID)
			assert.Empty(t, bad.AllowedActions)
			body := reconciliationBody(t, "verify_resolved", "resolved", bad.EvidenceHash)
			path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
			result := client.post(path, body, "legacy-bad-key", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "legacy-bad-proof"))
			assert.Equal(t, http.StatusConflict, result.Code, result.Body.String())
			assert.Contains(t, result.Body.String(), "invariant_unresolved")
			require.NoError(t, client.app.db.Model(&balance).Updates(map[string]any{"earned_micros": 100, "version": 2}).Error)
			fresh := reconciliationDetail(t, client, issue.ID)
			assert.Equal(t, []string{"verify_resolved"}, fresh.AllowedActions)
			body = reconciliationBody(t, "verify_resolved", "resolved", fresh.EvidenceHash)
			result = client.post(path, body, "legacy-good-key", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "legacy-good-proof"))
			require.Equal(t, http.StatusOK, result.Code, result.Body.String())
			require.NoError(t, client.app.db.First(&issue, issue.ID).Error)
			assert.Equal(t, "resolved", issue.Status)
			assert.Contains(t, issue.ResolutionEvidence, "old unchecked explanation", "re-verification must retain historical rationale")
			backlog, err = client.app.agencyBacklogStatus(context.Background())
			require.NoError(t, err)
			assert.Equal(t, int64(0), backlog["open_reconciliation_issues"])
			require.NoError(t, client.app.validateWithdrawalPayingGate(client.app.db, withdrawal))
		})
	}
}

func TestReconciliationDeliveryRepairRollsBackWithAuditAndRejectsIgnore(t *testing.T) {
	client := newFinanceRootClient(t)
	outbox := reconciliationOutboxFixture(t, client.app.db)
	issue := newReconciliationTestIssue(t, client.app, "billing_outbox", outbox.EventID)
	verification := reconciliationDetail(t, client, issue.ID)
	path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
	body := reconciliationBody(t, "restore_delivery", "ignored", verification.EvidenceHash)
	response := client.post(path, body, "reject-ignore-repair", client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "reject-ignore-proof"))
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	body = reconciliationBody(t, "restore_delivery", "resolved", verification.EvidenceHash)
	proof := client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "repair-rollback-proof")
	require.NoError(t, client.app.db.Callback().Create().Before("gorm:create").Register("test:repair-audit-failure", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AgencyAuditLog" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}))
	response = client.post(path, body, "repair-rollback-key", proof)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.NoError(t, client.app.db.Callback().Create().Remove("test:repair-audit-failure"))
	for _, table := range []any{&model.AgencyEventDelivery{}, &model.AgencyVerificationUse{}, &model.AgencyIdempotencyRecord{}, &model.AgencyAuditLog{}} {
		var count int64
		require.NoError(t, client.app.db.Model(table).Count(&count).Error)
		assert.Zero(t, count, "no partial repair, proof consumption or audit may survive rollback")
	}
	response = client.post(path, body, "repair-rollback-key", proof)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var delivery model.AgencyEventDelivery
	require.NoError(t, client.app.db.Where("event_id = ?", outbox.EventID).First(&delivery).Error)
	assert.Equal(t, "pending", delivery.Status)
}

func TestReconciliationRejectsPreviouslyUsedOrBodyAlteredProof(t *testing.T) {
	client := newFinanceRootClient(t)
	require.NoError(t, client.app.db.Create(&model.AgencyCommissionBalance{AgencyID: 97, CurrencyCode: "CNY", EarnedMicros: 100, AvailableMicros: 100}).Error)
	issue := newReconciliationTestIssue(t, client.app, "commission_balance", "97:CNY")
	v := reconciliationDetail(t, client, issue.ID)
	body := reconciliationBody(t, "verify_resolved", "resolved", v.EvidenceHash)
	proof := client.proof(t, body, "reconciliation.resolve", "reconciliation_issue:"+stringID(issue.ID), "reused-proof")
	path := "/agency/api/v1/root/reconciliation/issues/" + stringID(issue.ID) + "/resolve"
	altered := reconciliationBody(t, "verify_resolved", "ignored", v.EvidenceHash)
	result := client.post(path, altered, "altered-proof-key", proof)
	assert.Equal(t, http.StatusForbidden, result.Code, result.Body.String())
	now := time.Now().Unix()
	require.NoError(t, client.app.db.Create(&model.AgencyVerificationUse{JTI: tokenHash(proof), ActorType: ActorTypeRoot, ActorID: client.rootID, Action: "reconciliation.resolve", ObjectID: "reconciliation_issue:" + stringID(issue.ID), BodyHash: idempotencyHash(body), CreatedAt: now, ExpiresAt: now + 300, ConsumedAt: &now}).Error)
	result = client.post(path, body, "reused-proof-key", proof)
	assert.Equal(t, http.StatusForbidden, result.Code, result.Body.String())
	require.NoError(t, client.app.db.First(&issue, issue.ID).Error)
	assert.Equal(t, "open", issue.Status)
	var audits int64
	require.NoError(t, client.app.db.Model(&model.AgencyAuditLog{}).Count(&audits).Error)
	assert.Zero(t, audits)
}
