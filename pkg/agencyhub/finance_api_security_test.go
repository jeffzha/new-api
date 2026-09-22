package agencyhub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type financeRootClient struct {
	app           *App
	privateKey    ed25519.PrivateKey
	sessionToken  string
	csrf          string
	rootID        int64
	sourceSID     string
	sourceVersion int64
}

func newFinanceRootClient(t *testing.T) financeRootClient {
	t.Helper()
	app := newAgencyTestApp(t)
	_, privateKey, sessionToken, csrf, rootID, sourceSID, sourceVersion := newRootEvidence(t, app)
	return financeRootClient{app, privateKey, sessionToken, csrf, rootID, sourceSID, sourceVersion}
}

func (client financeRootClient) proof(t *testing.T, body, action, objectID, jti string) string {
	t.Helper()
	now := time.Now().Unix()
	proof, err := SignSSOTicket(client.privateKey, SSOTicketClaims{
		Issuer: "new-api", Audience: "agency-hub-verification", Subject: client.rootID,
		SourceSID: client.sourceSID, SessionVersion: client.sourceVersion, JTI: jti,
		KeyID: "test-key", Action: action, ObjectID: objectID, BodyHash: idempotencyHash(body),
		IssuedAt: now, NotBefore: now, ExpiresAt: now + 300,
	})
	require.NoError(t, err)
	return proof
}

func (client financeRootClient) post(path, body, key, proof string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", client.csrf)
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("X-Agency-Verification-Proof", proof)
	request.AddCookie(&http.Cookie{Name: client.app.config.CookieName, Value: client.sessionToken})
	recorder := httptest.NewRecorder()
	client.app.Router().ServeHTTP(recorder, request)
	return recorder
}

func TestPayoutRevealDoesNotPersistPlaintextAndEveryReadRequiresFreshProof(t *testing.T) {
	client := newFinanceRootClient(t)
	t.Setenv("AGENCY_PAYOUT_KEY", strings.Repeat("00", 32))
	t.Setenv("AGENCY_PAYOUT_KEY_FILE", "")
	plain := withdrawalAccountRequest{AccountType: "bank", AccountName: "Private Recipient", AccountNo: "6222000012345678", BankName: "Example Bank"}
	ciphertext, keyID, err := encryptPayoutAccount(plain, 19, 1)
	require.NoError(t, err)
	account := model.AgencyWithdrawalAccount{AgencyID: 19, Version: 1, Ciphertext: ciphertext, KeyID: keyID, Last4: "5678", Status: "active", CreatedAtMS: 1}
	require.NoError(t, client.app.db.Create(&account).Error)
	path := fmt.Sprintf("/agency/api/v1/root/withdrawal-accounts/%d/reveal", account.ID)
	object := "withdrawal_account:" + strconv.FormatInt(account.ID, 10)
	proof := client.proof(t, "{}", "withdrawal_account.reveal", object, "reveal-first")
	first := client.post(path, "{}", "same-reveal-key", proof)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Contains(t, first.Body.String(), plain.AccountNo)
	assert.Contains(t, first.Body.String(), plain.AccountName)
	assert.Equal(t, "no-store", first.Header().Get("Cache-Control"))

	for _, repeatedProof := range []string{"", proof} {
		replay := client.post(path, "{}", "same-reveal-key", repeatedProof)
		assert.Equal(t, http.StatusForbidden, replay.Code, replay.Body.String())
		assert.NotContains(t, replay.Body.String(), plain.AccountNo)
	}
	second := client.post(path, "{}", "same-reveal-key", client.proof(t, "{}", "withdrawal_account.reveal", object, "reveal-second"))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Contains(t, second.Body.String(), plain.AccountNo)

	var records []model.AgencyIdempotencyRecord
	require.NoError(t, client.app.db.Find(&records).Error)
	assert.Empty(t, records, "sensitive reveal responses must never enter the idempotency journal")
	var audits []model.AgencyAuditLog
	require.NoError(t, client.app.db.Where("action = ? AND object_id = ?", "withdrawal_account.reveal", strconv.FormatInt(account.ID, 10)).Find(&audits).Error)
	require.Len(t, audits, 2, "every successful fresh disclosure must be audited")
	for _, audit := range audits {
		assert.NotContains(t, audit.AfterJSON, plain.AccountName)
		assert.NotContains(t, audit.AfterJSON, plain.AccountNo)
	}
}

func TestPayoutRevealCannotReplayLegacyPlaintextJournalWithoutVerification(t *testing.T) {
	client := newFinanceRootClient(t)
	path := "/agency/api/v1/root/withdrawal-accounts/9/reveal"
	key := "legacy-sensitive-key"
	scope := sha256.Sum256([]byte("root:" + strconv.FormatInt(client.rootID, 10) + "::" + path + ":" + key))
	require.NoError(t, client.app.db.Create(&model.AgencyIdempotencyRecord{
		ScopeHash: hex.EncodeToString(scope[:]), ActorType: ActorTypeRoot, ActorID: client.rootID,
		Action: path, BodyHash: idempotencyHash("{}"), ResourceID: key, ResultCode: http.StatusOK,
		ResponseJSON: `{"success":true,"data":{"account_no":"legacy-secret-account"}}`, ExpiresAt: time.Now().Unix() + 300,
	}).Error)
	replay := client.post(path, "{}", key, "")
	assert.Equal(t, http.StatusForbidden, replay.Code, replay.Body.String())
	assert.NotContains(t, replay.Body.String(), "legacy-secret-account")
}

func TestRootWithdrawalAPIRejectsProofForAnotherActionOrObject(t *testing.T) {
	for _, endpoint := range []struct{ suffix, action string }{
		{"review", "withdrawal.review"}, {"reject", "withdrawal.reject"},
		{"transition", "withdrawal.transition"}, {"mark-paid", "withdrawal.mark_paid"},
	} {
		for _, wrongField := range []string{"action", "object"} {
			t.Run(endpoint.suffix+"/wrong_"+wrongField, func(t *testing.T) {
				client := newFinanceRootClient(t)
				withdrawal := model.AgencyWithdrawal{RequestNo: "protected-withdrawal", AgencyID: 19, Status: "reviewing", Version: 1, CurrencyCode: "CNY", AmountMicros: 1000}
				require.NoError(t, client.app.db.Create(&withdrawal).Error)
				object := "withdrawal:" + strconv.FormatInt(withdrawal.ID, 10)
				action := endpoint.action
				if wrongField == "action" {
					action = "withdrawal_account.reveal"
				} else {
					object = "withdrawal:9999"
				}
				body := `{"expected_version":1,"target_status":"approved","reason":"checked","payment_reference":"bank-reference"}`
				proof := client.proof(t, body, action, object, "wrong-finance-proof")
				response := client.post(fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/%s", withdrawal.ID, endpoint.suffix), body, "wrong-scope-key", proof)
				assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
				assert.Contains(t, response.Body.String(), "invalid_verification")
				require.NoError(t, client.app.db.First(&withdrawal, withdrawal.ID).Error)
				assert.Equal(t, "reviewing", withdrawal.Status)
				assert.Equal(t, int64(1), withdrawal.Version)
			})
		}
	}
}

func TestWithdrawalPaymentLeaseRoundTripsAsDecimalStringThroughAPIAndReplay(t *testing.T) {
	client := newFinanceRootClient(t)
	agency := model.Agency{Code: "lease-api", DisplayName: "Lease API", Status: AgencyStatusActive, InviteCode: "LEASEAPI", Version: 1}
	require.NoError(t, client.app.db.Create(&agency).Error)
	account := model.AgencyWithdrawalAccount{AgencyID: agency.ID, Version: 1, Ciphertext: "test-encrypted-snapshot", KeyID: "test-key", Last4: "1234", Status: "active", CreatedAtMS: 1}
	require.NoError(t, client.app.db.Create(&account).Error)
	hash := sha256.Sum256([]byte(account.Ciphertext))
	withdrawal := model.AgencyWithdrawal{RequestNo: "lease-api-request", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 1000, Status: "approved", Version: 1, AccountID: account.ID, AccountVersion: account.Version, AccountSnapshot: account.Ciphertext, AccountSnapshotHash: hex.EncodeToString(hash[:]), AccountSnapshotKeyID: account.KeyID}
	require.NoError(t, client.app.db.Create(&withdrawal).Error)
	require.NoError(t, client.app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", AvailableMicros: 100, LockedMicros: 1000, Version: 1}).Error)
	path := fmt.Sprintf("/agency/api/v1/root/withdrawals/%d", withdrawal.ID)
	object := "withdrawal:" + strconv.FormatInt(withdrawal.ID, 10)
	body := `{"expected_version":1,"target_status":"paying","reason":"original payment checked"}`
	first := client.post(path+"/transition", body, "start-payment-key", client.proof(t, body, "withdrawal.transition", object, "start-payment-proof"))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var leaseResponse struct {
		Data struct {
			Token string `json:"payment_lease_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(first.Body.Bytes(), &leaseResponse))
	require.NoError(t, client.app.db.First(&withdrawal, withdrawal.ID).Error)
	assert.Greater(t, withdrawal.PaymentLeaseToken, int64(1<<53))
	assert.Equal(t, strconv.FormatInt(withdrawal.PaymentLeaseToken, 10), leaseResponse.Data.Token)
	replay := client.post(path+"/transition", body, "start-payment-key", "")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	var replayed struct {
		Data struct {
			Token string `json:"payment_lease_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(replay.Body.Bytes(), &replayed))
	assert.Equal(t, leaseResponse.Data.Token, replayed.Data.Token)

	paidBytes, err := common.Marshal(map[string]any{"expected_version": withdrawal.Version, "payment_lease_token": leaseResponse.Data.Token, "payment_channel": "bank", "payment_reference": "verified-bank-reference"})
	require.NoError(t, err)
	paidBody := string(paidBytes)
	paid := client.post(path+"/mark-paid", paidBody, "mark-paid-payment-key", client.proof(t, paidBody, "withdrawal.mark_paid", object, "mark-paid-proof"))
	require.Equal(t, http.StatusOK, paid.Code, paid.Body.String())
	replayedPaid := client.post(path+"/mark-paid", paidBody, "mark-paid-payment-key", "")
	require.Equal(t, http.StatusOK, replayedPaid.Code, replayedPaid.Body.String())
	var paidEnvelope struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(replayedPaid.Body.Bytes(), &paidEnvelope))
	assert.Equal(t, "paid", paidEnvelope.Data.Status)
	var balance model.AgencyCommissionBalance
	require.NoError(t, client.app.db.Where("agency_id = ?", agency.ID).First(&balance).Error)
	assert.Equal(t, int64(0), balance.LockedMicros)
	assert.Equal(t, int64(1000), balance.PaidMicros)
	assert.Equal(t, int64(100), balance.AvailableMicros)
	var transitions int64
	require.NoError(t, client.app.db.Model(&model.AgencyWithdrawalTransition{}).Where("withdrawal_id = ?", withdrawal.ID).Count(&transitions).Error)
	assert.Equal(t, int64(2), transitions)
}

func TestWithdrawalInProgressIdempotencyReplyDoesNotExecuteMutation(t *testing.T) {
	client := newFinanceRootClient(t)
	withdrawal := model.AgencyWithdrawal{RequestNo: "pending-key-request", AgencyID: 19, CurrencyCode: "CNY", AmountMicros: 1000, Status: "submitted", Version: 1}
	require.NoError(t, client.app.db.Create(&withdrawal).Error)
	path := fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/transition", withdrawal.ID)
	body := `{"expected_version":1,"target_status":"reviewing","reason":"review"}`
	key := "pending-mutation-key"
	scope := sha256.Sum256([]byte("root:" + strconv.FormatInt(client.rootID, 10) + "::" + path + ":" + key))
	require.NoError(t, client.app.db.Create(&model.AgencyIdempotencyRecord{
		ScopeHash: hex.EncodeToString(scope[:]), ActorType: ActorTypeRoot, ActorID: client.rootID,
		Action: path, BodyHash: idempotencyHash(body), ResourceID: key, ExpiresAt: time.Now().Unix() + 300,
	}).Error)
	response := client.post(path, body, key, "")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var envelope struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &envelope))
	assert.Equal(t, "processing", envelope.Data.Status)
	require.NoError(t, client.app.db.First(&withdrawal, withdrawal.ID).Error)
	assert.Equal(t, "submitted", withdrawal.Status)
	assert.Equal(t, int64(1), withdrawal.Version)
}

func TestSalesPricePublicationUsesAuthenticatedSessionWithoutSecondPassword(t *testing.T) {
	client := newFinanceRootClient(t)
	agency, _, err := client.app.CreateAgency(client.rootID, "Sales proof agency", "sales-proof-operator", agencycontract.Policy{
		DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000,
	})
	require.NoError(t, err)
	require.NoError(t, client.app.db.Model(&model.AgencySession{}).Where("token_hash = ?", tokenHash(client.sessionToken)).Update("agency_id", agency.ID).Error)
	body := `{"expected_revision":1,"default_sales_bps":9500,"model_sales_overrides":[],"reason":"new sales prices"}`
	response := client.post("/agency/api/v1/pricing/sales/publish", body, "sales-publish-proof-key", "")
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var policies int64
	require.NoError(t, client.app.db.Model(&model.AgencyPricePolicyVersion{}).Where("agency_id = ?", agency.ID).Count(&policies).Error)
	assert.Equal(t, int64(2), policies)
}
