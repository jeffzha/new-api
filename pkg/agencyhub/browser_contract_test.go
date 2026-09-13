package agencyhub

import (
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

func TestPricePreviewUsesTheoreticalCommissionAndKeepsDefaultsIndependentOfModelNames(t *testing.T) {
	for _, test := range []struct {
		name                string
		sales               int
		charged, commission int64
	}{
		{"discount", 9000, 9000, 1500},
		{"markup", 12000, 12000, 4500},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newFinanceRootClient(t)
			overrideSales, overrideSettlement := 15000, 0
			body, err := common.Marshal(rootPricingRequest{DefaultSettlementBPS: 7500, DefaultSalesBPS: test.sales, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "preview", SalesBPS: &overrideSales, SettlementBPS: &overrideSettlement}}})
			require.NoError(t, err)
			response := client.post("/agency/api/v1/root/agencies/0/pricing/preview", string(body), "", "")
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var envelope struct {
				Data struct {
					Standard   int64 `json:"standard_quota"`
					Customer   int64 `json:"customer_quota"`
					Settlement int64 `json:"settlement_quota"`
					Commission int64 `json:"commission_quota"`
					Models     []struct {
						Name       string `json:"origin_model_name"`
						Commission int64  `json:"commission_quota"`
					} `json:"model_previews"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &envelope))
			assert.Equal(t, int64(10000), envelope.Data.Standard)
			assert.Equal(t, test.charged, envelope.Data.Customer)
			assert.Equal(t, int64(7500), envelope.Data.Settlement)
			assert.Equal(t, test.commission, envelope.Data.Commission)
			require.Len(t, envelope.Data.Models, 1)
			assert.Equal(t, "preview", envelope.Data.Models[0].Name)
			assert.Equal(t, int64(15000), envelope.Data.Models[0].Commission)
		})
	}
}

func TestCreateAgencyPreservesExplicitZeroSpreadAndDefaultsMissingSpread(t *testing.T) {
	for _, test := range []struct {
		name   string
		spread string
		want   int
	}{
		{"explicit_zero", `,"min_spread_bps":0`, 0},
		{"omitted", "", 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newFinanceRootClient(t)
			t.Setenv("AGENCY_HUB_DELIVERY_KEY", strings.Repeat("01", 32))
			t.Setenv("AGENCY_HUB_DELIVERY_KEY_FILE", "")
			body := fmt.Sprintf(`{"display_name":"Explicit defaults","operator_username":"zero-spread","pricing":{"default_settlement_bps":7500,"default_sales_bps":9000,"sales_cap_bps":30000%s}}`, test.spread)
			proof := client.proof(t, body, "agency.create", "agency:new", "create-zero-spread")
			response := client.post("/agency/api/v1/root/agencies", body, "create-zero-spread", proof)
			require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
			var agency model.Agency
			require.NoError(t, client.app.db.First(&agency).Error)
			_, policy, err := client.app.loadAgencyPolicy(agency.ID)
			require.NoError(t, err)
			assert.Equal(t, test.want, policy.MinSpreadBPS)
		})
	}
}

func TestRootWithdrawalListRestoresOnlyOwnedUnexpiredPaymentLease(t *testing.T) {
	client := newFinanceRootClient(t)
	token := int64(1789000000123456789)
	for _, test := range []struct {
		name, owner, status string
		expires             int64
		visible             bool
	}{
		{"current-owner", fmt.Sprintf("root:%d", client.rootID), "paying", time.Now().Unix() + 300, true},
		{"another-owner", "root:9999", "paying", time.Now().Unix() + 300, false},
		{"expired", fmt.Sprintf("root:%d", client.rootID), "paying", time.Now().Unix() - 1, false},
		{"unknown-result", fmt.Sprintf("root:%d", client.rootID), "payment_unknown", time.Now().Unix() + 300, false},
	} {
		row := model.AgencyWithdrawal{RequestNo: test.name, AgencyID: 7, CurrencyCode: "CNY", AmountMicros: 10000, Status: test.status, Version: 1, PaymentLeaseOwner: test.owner, PaymentLeaseToken: token, PaymentLeaseUntil: test.expires}
		require.NoError(t, client.app.db.Create(&row).Error)
	}
	request := httptest.NewRequest(http.MethodGet, "/agency/api/v1/root/withdrawals", nil)
	request.AddCookie(&http.Cookie{Name: client.app.config.CookieName, Value: client.sessionToken})
	response := httptest.NewRecorder()
	client.app.Router().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var envelope struct {
		Data struct {
			Items []struct {
				RequestNo string `json:"request_no"`
				Token     string `json:"payment_lease_token"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Items, 4)
	for _, item := range envelope.Data.Items {
		if item.RequestNo == "current-owner" {
			assert.Equal(t, strconv.FormatInt(token, 10), item.Token)
		} else {
			assert.Empty(t, item.Token, item.RequestNo)
		}
	}
}
