package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommissionSummarySeparatesLifetimeTotalsFromWithdrawalBalances(t *testing.T) {
	for _, test := range []struct {
		name                                      string
		earned, reversed, available, locked, paid int64
		net                                       string
	}{
		{"zero", 0, 0, 0, 0, 0, "0"},
		{"refund_and_pending_withdrawal", 1000000000, 200000000, 400000000, 100000000, 300000000, "800000000"},
		{"refund_after_payout", 1000000000, 200000000, -100000000, 0, 900000000, "800000000"},
		{"beyond_javascript_integer_precision", 9007199254740993, 1, 9007199254740992, 0, 0, "9007199254740992"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newAgencyTestApp(t)
			policy := agencycontract.Policy{DefaultSalesBPS: 10000, SalesCapBPS: 30000}
			agency, _, err := app.CreateAgency(1, "Commission summary", "summary-operator", policy)
			require.NoError(t, err)
			otherAgency, _, err := app.CreateAgency(1, "Other summary", "other-summary-operator", policy)
			require.NoError(t, err)
			balance := model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY",
				EarnedMicros: test.earned, ReversedMicros: test.reversed, AvailableMicros: test.available,
				LockedMicros: test.locked, PaidMicros: test.paid, Version: 1}
			require.NoError(t, app.db.Create(&balance).Error)
			require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "USD", EarnedMicros: 1234567, AvailableMicros: 1234567, Version: 1}).Error)
			require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: otherAgency.ID, CurrencyCode: "EUR", EarnedMicros: 999, AvailableMicros: 999, Version: 1}).Error)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/commissions/summary", nil)
			c.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
			app.commissionSummary(c)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var response struct {
				Data struct {
					Items []struct {
						AgencyID     string `json:"agency_id"`
						Currency     string `json:"currency_code"`
						Earned       string `json:"earned_micros"`
						Reversed     string `json:"reversed_micros"`
						Net          string `json:"net_earned_micros"`
						Available    string `json:"available_micros"`
						Tax          string `json:"tax_micros"`
						Withdrawable string `json:"withdrawable_micros"`
						Locked       string `json:"locked_micros"`
						Paid         string `json:"paid_micros"`
					} `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Len(t, response.Data.Items, 2, "other agencies must not be included in lifetime totals")
			item := response.Data.Items[0]
			assert.Equal(t, strconv.FormatInt(agency.ID, 10), item.AgencyID)
			assert.Equal(t, "CNY", item.Currency)
			assert.Equal(t, strconv.FormatInt(test.earned, 10), item.Earned)
			assert.Equal(t, strconv.FormatInt(test.reversed, 10), item.Reversed)
			assert.Equal(t, test.net, item.Net)
			assert.Equal(t, strconv.FormatInt(test.available, 10), item.Available)
			tax, withdrawable := commissionTaxAndWithdrawable(balance)
			assert.Equal(t, strconv.FormatInt(tax, 10), item.Tax)
			assert.Equal(t, strconv.FormatInt(withdrawable, 10), item.Withdrawable)
			assert.Equal(t, strconv.FormatInt(test.locked, 10), item.Locked)
			assert.Equal(t, strconv.FormatInt(test.paid, 10), item.Paid)
			assert.Equal(t, "USD", response.Data.Items[1].Currency)
			assert.Equal(t, "1234567", response.Data.Items[1].Net, "currencies must not be combined")
		})
	}
}
