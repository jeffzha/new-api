package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWithdrawalStateMutationsRequireExpectedVersion(t *testing.T) {
	app := newAgencyTestApp(t)
	agency, _, err := app.CreateAgency(1, "Version Agency", "version_operator", agencycontract.Policy{
		DefaultSettlementBPS: 7500,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
	})
	require.NoError(t, err)
	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 11, AgencyID: &agency.ID}

	tests := []struct {
		name string
		call func(*gin.Context)
	}{
		{
			name: "cancel",
			call: app.cancelOwnWithdrawal,
		},
		{
			name: "mark paid",
			call: app.markWithdrawalPaid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			path := "/agency/api/v1/withdrawals/1/cancel"
			body := `{"reason":"retry"}`
			if test.name == "mark paid" {
				path = "/agency/api/v1/root/withdrawals/1/mark-paid"
				body = `{"payment_reference":"bank-ref"}`
			}
			ctx.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			ctx.Params = gin.Params{{Key: "id", Value: "1"}}
			ctx.Set("agency_identity", identity)
			test.call(ctx)
			require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "expected_version_required")
		})
	}
}

func TestWithdrawalAccountMutationsRequireExpectedVersion(t *testing.T) {
	app := newAgencyTestApp(t)
	agency, _, err := app.CreateAgency(1, "Account Version Agency", "account_version_operator", agencycontract.Policy{
		DefaultSettlementBPS: 7500,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
	})
	require.NoError(t, err)

	for _, test := range []struct {
		name string
		call func(*gin.Context)
		body string
	}{
		{name: "update", call: app.updateWithdrawalAccount, body: `{"account_type":"bank","account_name":"name","account_no":"1234"}`},
		{name: "disable", call: app.disableWithdrawalAccount, body: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/agency/api/v1/withdrawal-accounts/1", strings.NewReader(test.body))
			ctx.Params = gin.Params{{Key: "id", Value: "1"}}
			ctx.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 11, AgencyID: &agency.ID})
			test.call(ctx)
			require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "expected_version_required")
		})
	}
}
