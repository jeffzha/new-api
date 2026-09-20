package agencyhub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootManagementLookupUsesBusinessAccounts(t *testing.T) {
	client := newFinanceRootClient(t)
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	agency, _, err := client.app.CreateAgency(client.rootID, "演示代理商", "agency-operator", policy)
	require.NoError(t, err)
	user := model.User{Username: "customer-account", AffCode: "customer-account-aff", BillingMode: model.AgencyDurableBillingMode}
	require.NoError(t, client.app.db.Create(&user).Error)
	binding := model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: agency.ID, Revision: 1}
	require.NoError(t, client.app.db.Create(&binding).Error)
	require.NoError(t, client.app.db.Create(&model.AgencyActiveUserBinding{UserID: int64(user.Id), AgencyID: agency.ID, BindingID: binding.ID, Revision: binding.Revision}).Error)

	request := httptest.NewRequest(http.MethodGet, "/agency/api/v1/root/users/management?username=customer-account", nil)
	request.AddCookie(&http.Cookie{Name: client.app.config.CookieName, Value: client.sessionToken})
	management := httptest.NewRecorder()
	client.app.Router().ServeHTTP(management, request)
	require.Equal(t, http.StatusOK, management.Code, management.Body.String())
	assert.Contains(t, management.Body.String(), `"username":"customer-account"`)
	assert.Contains(t, management.Body.String(), `"agency_name":"演示代理商"`)
	assert.Contains(t, management.Body.String(), `"agency_operator_username":"agency-operator"`)

	request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/root/customers?page_size=30", nil)
	request.AddCookie(&http.Cookie{Name: client.app.config.CookieName, Value: client.sessionToken})
	customers := httptest.NewRecorder()
	client.app.Router().ServeHTTP(customers, request)
	require.Equal(t, http.StatusOK, customers.Code, customers.Body.String())
	assert.Contains(t, customers.Body.String(), `"agency_name":"演示代理商"`)
	assert.Contains(t, customers.Body.String(), `"agency_account":"agency-operator"`)

	request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/root/agencies/lookup?query=agency-operator", nil)
	request.AddCookie(&http.Cookie{Name: client.app.config.CookieName, Value: client.sessionToken})
	lookup := httptest.NewRecorder()
	client.app.Router().ServeHTTP(lookup, request)
	require.Equal(t, http.StatusOK, lookup.Code, lookup.Body.String())
	assert.Contains(t, lookup.Body.String(), `"display_name":"演示代理商"`)
	assert.Contains(t, lookup.Body.String(), `"operator_username":"agency-operator"`)
}

func TestCustomerTransferPreservesFundingHistoryAndRejectsStaleRevision(t *testing.T) {
	client := newFinanceRootClient(t)
	app := client.app
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	source, _, err := app.CreateAgency(client.rootID, "Source", "transfer_source", policy)
	require.NoError(t, err)
	target, _, err := app.CreateAgency(client.rootID, "Target", "transfer_target", policy)
	require.NoError(t, err)
	require.NoError(t, app.db.Model(&source).Update("status", AgencyStatusDisabled).Error)
	user := model.User{Username: "transfer-customer", AffCode: "transfer-customer-aff", BillingMode: model.AgencyDurableBillingMode, Quota: 42}
	require.NoError(t, app.db.Create(&user).Error)
	binding := model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: source.ID, Revision: 9007199254740993, EffectiveAtMS: 1000}
	require.NoError(t, app.db.Create(&binding).Error)
	require.NoError(t, app.db.Create(&model.AgencyActiveUserBinding{UserID: int64(user.Id), AgencyID: source.ID, BindingID: binding.ID, Revision: binding.Revision}).Error)
	funding := model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 42, MoneySeq: 7, Version: 8}
	require.NoError(t, app.db.Create(&funding).Error)
	usage := model.AgencyUsageFact{EventID: "old-agency-event", ComponentID: "text", UserID: int64(user.Id), AgencyID: &source.ID, BindingID: &binding.ID, ChargedQuota: 9}
	require.NoError(t, app.db.Create(&usage).Error)
	body := fmt.Sprintf(`{"target_agency_id":"%d","expected_binding_revision":"9007199254740993","reason":"Verified customer transfer"}`, target.ID)
	path := fmt.Sprintf("/agency/api/v1/root/users/%d/transfer", user.Id)
	object := fmt.Sprintf("user:%d", user.Id)
	proof := client.proof(t, body, "user.transfer", object, "transfer-one")
	response := client.post(path, body, "transfer-one", proof)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"revision":"9007199254740994"`)
	replay := client.post(path, body, "transfer-one", proof)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	stale := client.post(path, body, "transfer-stale", client.proof(t, body, "user.transfer", object, "transfer-stale"))
	assert.Equal(t, http.StatusConflict, stale.Code)
	var current model.AgencyActiveUserBinding
	require.NoError(t, app.db.First(&current, user.Id).Error)
	assert.Equal(t, target.ID, current.AgencyID)
	assert.Equal(t, binding.Revision+1, current.Revision)
	require.NoError(t, app.db.First(&binding, binding.ID).Error)
	require.NotNil(t, binding.EndedAtMS)
	assert.Equal(t, source.ID, binding.AgencyID)
	var after model.AgencyFundingAccount
	require.NoError(t, app.db.First(&after, user.Id).Error)
	assert.Equal(t, funding, after)
	require.NoError(t, app.db.First(&usage, usage.ID).Error)
	assert.Equal(t, source.ID, *usage.AgencyID)
	require.NoError(t, app.db.First(&user, user.Id).Error)
	assert.Equal(t, 42, user.Quota)
}

func TestCustomerTransferRejectsTargetWithoutPublishedPolicy(t *testing.T) {
	client := newFinanceRootClient(t)
	source := model.Agency{Code: "source-no-policy", InviteCode: "source-no-policy", Status: AgencyStatusActive}
	target := model.Agency{Code: "target-no-policy", InviteCode: "target-no-policy", Status: AgencyStatusActive}
	require.NoError(t, client.app.db.Create(&source).Error)
	require.NoError(t, client.app.db.Create(&target).Error)
	binding := model.AgencyUserBinding{UserID: 55, AgencyID: source.ID, Revision: 1}
	require.NoError(t, client.app.db.Create(&binding).Error)
	require.NoError(t, client.app.db.Create(&model.AgencyActiveUserBinding{UserID: 55, AgencyID: source.ID, BindingID: binding.ID, Revision: 1}).Error)
	body := fmt.Sprintf(`{"target_agency_id":"%d","expected_binding_revision":"1","reason":"Invalid policy test"}`, target.ID)
	response := client.post("/agency/api/v1/root/users/55/transfer", body, "no-policy", client.proof(t, body, "user.transfer", "user:55", "no-policy"))
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.NoError(t, client.app.db.First(&binding, binding.ID).Error)
	assert.Nil(t, binding.EndedAtMS, "rejecting the target must not close existing ownership")
}
