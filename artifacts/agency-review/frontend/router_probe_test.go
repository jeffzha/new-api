//go:build agency_audit

package agencyhub

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrontendAuditOperatorProofIsSessionBound(t *testing.T) {
	app := newAgencyTestApp(t)
	t.Setenv("AGENCY_PAYOUT_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	agency, password, err := app.CreateAgency(1, "Proof audit", "proof-audit", agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000})
	require.NoError(t, err)
	var account model.AgencyOperatorAccount
	require.NoError(t, app.db.Where("agency_id = ?", agency.ID).First(&account).Error)
	require.NoError(t, app.db.Model(&account).Update("must_change_password", false).Error)
	now := time.Now().Unix()
	for _, token := range []string{"audit-session-a", "audit-session-b"} {
		require.NoError(t, app.db.Create(&model.AgencySession{TokenHash: tokenHash(token), ActorType: ActorTypeOperator, ActorID: account.ID, AgencyID: &agency.ID, AuthVersion: account.AuthVersion, CSRFHash: tokenHash("audit-csrf"), LastSeenAt: now, CreatedAt: now, ExpiresAt: now + 3600}).Error)
	}
	body := []byte(`{"account_type":"bank","account_name":"Synthetic","account_no":"000000001234"}`)
	verifyBody, err := common.Marshal(map[string]string{"password": password, "action": "withdrawal_account.create", "object_id": fmt.Sprintf("agency:%d", agency.ID), "body_hash": idempotencyHash(string(body))})
	require.NoError(t, err)
	verifyRequest := httptest.NewRequest(http.MethodPost, "/agency/api/v1/auth/verify", bytes.NewReader(verifyBody))
	verifyRequest.Header.Set("Content-Type", "application/json")
	verifyRequest.Header.Set("X-CSRF-Token", "audit-csrf")
	verifyRequest.Header.Set("Origin", "https://gateway.example")
	verifyRequest.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: "audit-session-a"})
	verified := httptest.NewRecorder()
	app.Router().ServeHTTP(verified, verifyRequest)
	require.Equal(t, http.StatusOK, verified.Code, verified.Body.String())
	var proof struct {
		Data struct {
			Proof string `json:"proof"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(verified.Body.Bytes(), &proof))
	request := httptest.NewRequest(http.MethodPost, "/agency/api/v1/withdrawal-accounts", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", "audit-csrf")
	request.Header.Set("Origin", "https://gateway.example")
	request.Header.Set("Idempotency-Key", "audit-cross-session")
	request.Header.Set("X-Agency-Verification-Proof", proof.Data.Proof)
	request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: "audit-session-b"})
	response := httptest.NewRecorder()
	app.Router().ServeHTTP(response, request)
	t.Logf("Proof issued in session A, used in session B: HTTP %d; %s", response.Code, response.Body.String())
	assert.Equal(t, http.StatusForbidden, response.Code, "high-risk proofs must bind the originating session")
}

func TestFrontendAuditWithdrawalFormUsesAcceptedIDType(t *testing.T) {
	var request struct {
		CurrencyCode string       `json:"currency_code"`
		AmountMicros decimalInt64 `json:"amount_micros"`
		AccountID    decimalInt64 `json:"account_id"`
	}
	// The exact numeric account_id emitted by static.go:45, also captured by
	// browser-probe.cjs, must satisfy the handler's string-only wire contract.
	err := common.Unmarshal([]byte(`{"currency_code":"CNY","amount_micros":"1000000","account_id":1}`), &request)
	assert.NoError(t, err, "the withdrawal form body must decode into createWithdrawal's request DTO")
}

// This isolated acceptance probe runs the actual served page against the actual
// Gin router and a synthetic in-memory SQLite database. It is an audit artifact,
// loaded with go test -overlay, not part of the production test suite.
func TestFrontendAcceptanceAudit(t *testing.T) {
	for _, actor := range []string{"root", "operator", "first-login"} {
		t.Run(actor, func(t *testing.T) {
			app := newAgencyTestApp(t)
			require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Task{}))
			root := model.User{Username: "audit-root", Password: "synthetic", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
			require.NoError(t, app.db.Create(&root).Error)
			source := model.UserSession{SID: "audit-root-sid", UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "audit-refresh", LoginMethod: "password", LastActiveAt: 1, ExpiresAt: 4102444800}
			require.NoError(t, app.db.Create(&source).Error)
			policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
			agency, _, err := app.CreateAgency(int64(root.Id), "Audit agency", "audit-operator", policy)
			require.NoError(t, err)
			customer := model.User{Username: "audit-customer", Password: "synthetic", Status: common.UserStatusEnabled, AffCode: "audit-customer"}
			require.NoError(t, app.db.Create(&customer).Error)
			now := time.Now().Unix()
			binding := model.AgencyUserBinding{UserID: int64(customer.Id), AgencyID: agency.ID, Revision: 1, CreatedSource: "audit", EffectiveAtMS: now * 1000, CreatedAt: now}
			require.NoError(t, app.db.Create(&binding).Error)
			require.NoError(t, app.db.Create(&model.AgencyActiveUserBinding{UserID: int64(customer.Id), AgencyID: agency.ID, BindingID: binding.ID, Revision: 1, UpdatedAt: now}).Error)
			token, csrf, err := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
			require.NoError(t, err)
			if actor != "root" {
				var account model.AgencyOperatorAccount
				require.NoError(t, app.db.Where("agency_id = ?", agency.ID).First(&account).Error)
				require.NoError(t, app.db.Model(&account).Update("must_change_password", actor == "first-login").Error)
				token, csrf = "audit-operator-session", "audit-csrf"
				require.NoError(t, app.db.Create(&model.AgencySession{TokenHash: tokenHash(token), ActorType: ActorTypeOperator, ActorID: account.ID, AgencyID: &agency.ID, AuthVersion: account.AuthVersion, CSRFHash: tokenHash(csrf), LastSeenAt: now, CreatedAt: now, ExpiresAt: now + 3600}).Error)
			}
			server := httptest.NewServer(app.Router())
			defer server.Close()
			app.config.PublicBaseURL = server.URL
			script, err := filepath.Abs("../../artifacts/agency-review/frontend/browser-probe.cjs")
			require.NoError(t, err)
			command := exec.Command("bun", script)
			command.Env = append(os.Environ(), "AGENCY_AUDIT_URL="+server.URL, "AGENCY_AUDIT_ACTOR="+actor, "AGENCY_AUDIT_SESSION="+token, "AGENCY_AUDIT_CSRF="+csrf)
			output, err := command.CombinedOutput()
			t.Log(string(output))
			assert.NoError(t, err, "the served UI must complete the documented workflows; see JSON evidence")
		})
	}
}
