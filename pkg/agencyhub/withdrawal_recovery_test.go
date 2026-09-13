package agencyhub

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithdrawalUnpaidRecoveryRequiresBankEvidenceAndRetainsLockedFunds(t *testing.T) {
	for _, initial := range []string{"payment_unknown", "on_hold"} {
		t.Run(initial, func(t *testing.T) {
			client := newFinanceRootClient(t)
			withdrawal := model.AgencyWithdrawal{RequestNo: "recover-" + initial, AgencyID: 19, Status: initial, PreviousStatus: "paying", Version: 4, CurrencyCode: "CNY", AmountMicros: 12000000, PaymentLeaseOwner: "root:old", PaymentLeaseToken: 12345, PaymentLeaseUntil: time.Now().Add(-time.Minute).Unix(), UpdatedAtMS: time.Now().Add(-time.Hour).UnixMilli()}
			require.NoError(t, client.app.db.Create(&withdrawal).Error)
			require.NoError(t, client.app.db.Create(&model.AgencyCommissionBalance{AgencyID: 19, CurrencyCode: "CNY", AvailableMicros: 3000000, LockedMicros: 12000000, Version: 1}).Error)
			object := fmt.Sprintf("withdrawal:%d", withdrawal.ID)
			path := fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/transition", withdrawal.ID)
			evidence := map[string]any{"outcome": "not_paid", "payment_channel": "example-bank", "original_payment_reference": "attempt-20260913-1", "bank_confirmation_reference": "bank-investigation-42", "confirmed_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
			body := map[string]any{"target_status": "approved", "expected_version": 4, "reason": "Bank confirmed the original transfer was rejected", "unpaid_evidence": evidence}
			encoded, err := common.Marshal(body)
			require.NoError(t, err)
			response := client.post(path, string(encoded), "recover-with-evidence", client.proof(t, string(encoded), "withdrawal.transition", object, "recover-proof"))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			replay := client.post(path, string(encoded), "recover-with-evidence", "")
			require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
			require.NoError(t, client.app.db.First(&withdrawal, withdrawal.ID).Error)
			assert.Equal(t, "approved", withdrawal.Status)
			assert.Equal(t, int64(5), withdrawal.Version)
			assert.Zero(t, withdrawal.PaymentLeaseToken)
			assert.Zero(t, withdrawal.PaymentLeaseUntil)
			assert.Empty(t, withdrawal.PaymentLeaseOwner)
			var balance model.AgencyCommissionBalance
			require.NoError(t, client.app.db.Where("agency_id = ?", 19).First(&balance).Error)
			assert.Equal(t, int64(3000000), balance.AvailableMicros)
			assert.Equal(t, int64(12000000), balance.LockedMicros)
			assert.Zero(t, balance.PaidMicros)
			var transitions []model.AgencyWithdrawalTransition
			require.NoError(t, client.app.db.Where("withdrawal_id = ?", withdrawal.ID).Find(&transitions).Error)
			require.Len(t, transitions, 1)
			assert.Equal(t, client.rootID, transitions[0].ActorID)
			var recorded map[string]any
			require.NoError(t, common.UnmarshalJsonStr(transitions[0].Evidence, &recorded))
			assert.Equal(t, body["reason"], recorded["reason"])
			assert.Equal(t, evidence, recorded["unpaid_evidence"])
			var audits []model.AgencyAuditLog
			require.NoError(t, client.app.db.Where("action = ?", "withdrawal.recover_unpaid").Find(&audits).Error)
			require.Len(t, audits, 1, "replaying a recovery must not duplicate the root audit")
			assert.Equal(t, client.rootID, audits[0].ActorID)
			assert.Contains(t, audits[0].AfterJSON, "bank-investigation-42")
			stale := client.post(path, string(encoded), "stale-recovery", client.proof(t, string(encoded), "withdrawal.transition", object, "stale-proof"))
			assert.Equal(t, http.StatusConflict, stale.Code)
		})
	}
}

func TestWithdrawalRecoveryRejectsMissingInvalidOrStaleEvidence(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong_outcome", "missing_bank_confirmation", "future_confirmation", "before_attempt", "overlong_reference", "already_paid_reference", "other_paid_reference"} {
		t.Run(scenario, func(t *testing.T) {
			client := newFinanceRootClient(t)
			withdrawal := model.AgencyWithdrawal{RequestNo: "invalid-recovery", AgencyID: 19, Status: "payment_unknown", Version: 1, CurrencyCode: "CNY", AmountMicros: 12000000, UpdatedAtMS: time.Now().Add(-time.Hour).UnixMilli()}
			if scenario == "already_paid_reference" {
				withdrawal.PaymentReference = "already-settled"
			}
			require.NoError(t, client.app.db.Create(&withdrawal).Error)
			if scenario == "other_paid_reference" {
				require.NoError(t, client.app.db.Create(&model.AgencyWithdrawalPaymentReference{PaymentChannel: "bank", PaymentReference: "original-attempt", WithdrawalID: 999}).Error)
			}
			evidence := map[string]any{"outcome": "not_paid", "payment_channel": "bank", "original_payment_reference": "original-attempt", "bank_confirmation_reference": "case-42", "confirmed_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
			switch scenario {
			case "wrong_outcome":
				evidence["outcome"] = "pending"
			case "missing_bank_confirmation":
				evidence["bank_confirmation_reference"] = " "
			case "future_confirmation":
				evidence["confirmed_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			case "before_attempt":
				evidence["confirmed_at"] = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
			case "overlong_reference":
				evidence["original_payment_reference"] = strings.Repeat("x", 192)
			}
			body := map[string]any{"target_status": "approved", "expected_version": 1, "reason": "investigation", "unpaid_evidence": evidence}
			if scenario == "missing" {
				delete(body, "unpaid_evidence")
			}
			encoded, err := common.Marshal(body)
			require.NoError(t, err)
			response := client.post(fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/transition", withdrawal.ID), string(encoded), "invalid-recover", client.proof(t, string(encoded), "withdrawal.transition", fmt.Sprintf("withdrawal:%d", withdrawal.ID), "invalid-proof"))
			assert.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			require.NoError(t, client.app.db.First(&withdrawal, withdrawal.ID).Error)
			assert.Equal(t, "payment_unknown", withdrawal.Status)
			assert.Equal(t, int64(1), withdrawal.Version)
			var transitions int64
			require.NoError(t, client.app.db.Model(&model.AgencyWithdrawalTransition{}).Count(&transitions).Error)
			assert.Zero(t, transitions)
		})
	}
}

func TestWithdrawalInvestigationCannotStealAnotherRootsActivePaymentLease(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint("expired=", expired), func(t *testing.T) {
			client := newFinanceRootClient(t)
			until := time.Now().Add(time.Minute).Unix()
			if expired {
				until = time.Now().Add(-time.Minute).Unix()
			}
			withdrawal := model.AgencyWithdrawal{RequestNo: "owned-payment", AgencyID: 19, Status: "paying", Version: 1, CurrencyCode: "CNY", AmountMicros: 12000000, PaymentLeaseOwner: "root:999", PaymentLeaseToken: 12345, PaymentLeaseUntil: until}
			require.NoError(t, client.app.db.Create(&withdrawal).Error)
			body := `{"expected_version":1,"target_status":"payment_unknown","reason":"Original payment needs investigation"}`
			response := client.post(fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/transition", withdrawal.ID), body, "investigate-key", client.proof(t, body, "withdrawal.transition", fmt.Sprintf("withdrawal:%d", withdrawal.ID), "investigate-proof"))
			require.NoError(t, client.app.db.First(&withdrawal, withdrawal.ID).Error)
			if expired {
				assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
				assert.Equal(t, "payment_unknown", withdrawal.Status)
				assert.Zero(t, withdrawal.PaymentLeaseToken)
			} else {
				assert.Equal(t, http.StatusConflict, response.Code, response.Body.String())
				assert.Equal(t, "paying", withdrawal.Status)
				assert.Equal(t, int64(12345), withdrawal.PaymentLeaseToken)
			}
		})
	}
}

func TestWithdrawalRootRejectionReleasesOnlyUnattemptedPayments(t *testing.T) {
	for _, scenario := range []struct {
		status, previous string
		allowed          bool
	}{{"approved", "", true}, {"on_hold", "approved", true}, {"on_hold", "paying", false}, {"on_hold", "payment_unknown", false}, {"payment_unknown", "paying", false}, {"paying", "approved", false}} {
		t.Run(scenario.status+"_"+scenario.previous, func(t *testing.T) {
			client := newFinanceRootClient(t)
			withdrawal := model.AgencyWithdrawal{RequestNo: "reject-request", AgencyID: 19, Status: scenario.status, PreviousStatus: scenario.previous, Version: 1, CurrencyCode: "CNY", AmountMicros: 12000000}
			require.NoError(t, client.app.db.Create(&withdrawal).Error)
			require.NoError(t, client.app.db.Create(&model.AgencyCommissionBalance{AgencyID: 19, CurrencyCode: "CNY", AvailableMicros: 3000000, LockedMicros: 12000000, Version: 1}).Error)
			body := `{"expected_version":1,"reason":"No payment has been attempted"}`
			path := fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/reject", withdrawal.ID)
			response := client.post(path, body, "reject-key", client.proof(t, body, "withdrawal.reject", fmt.Sprintf("withdrawal:%d", withdrawal.ID), "reject-proof"))
			var balance model.AgencyCommissionBalance
			require.NoError(t, client.app.db.Where("agency_id = ?", 19).First(&balance).Error)
			require.NoError(t, client.app.db.First(&withdrawal, withdrawal.ID).Error)
			if scenario.allowed {
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				assert.Equal(t, "rejected", withdrawal.Status)
				assert.Zero(t, balance.LockedMicros)
				assert.Equal(t, int64(15000000), balance.AvailableMicros)
				replay := client.post(path, body, "reject-key", "")
				assert.Equal(t, http.StatusOK, replay.Code)
				require.NoError(t, client.app.db.First(&balance, balance.ID).Error)
				assert.Equal(t, int64(15000000), balance.AvailableMicros)
			} else {
				assert.Equal(t, http.StatusConflict, response.Code)
				assert.Equal(t, scenario.status, withdrawal.Status)
				assert.Equal(t, int64(12000000), balance.LockedMicros)
				assert.Equal(t, int64(3000000), balance.AvailableMicros)
			}
		})
	}
}
