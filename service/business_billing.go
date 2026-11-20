package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/gin-gonic/gin"
)

// Called only after the existing settlement chain returns. Never recalculate
// discounts here: actualQuota already includes the accepted customer policy.
func recordBusinessSettlement(c *gin.Context, info *relaycommon.RelayInfo, actualQuota int, settleErr *error) {
	if c == nil {
		return
	}
	value, tracked := c.Get(businessapi.ConversionKey)
	if !tracked {
		return
	}
	billing := businessapi.UnavailableBilling()
	if projectAuthority() {
		billing.Source, billing.Currency = "ops", opsbilling.Currency()
		billing.Status, billing.Reason = "pending", "awaiting_ops_charge"
		if *settleErr != nil {
			billing.Status, billing.Reason = "unavailable", "ops_settlement_requires_reconciliation"
		}
		c.Set(businessapi.BillingKey, billing)
		return
	}
	if *settleErr == nil {
		conversion, ok := value.(businessapi.Conversion)
		if info != nil && info.AgencyPricing != nil {
			snapshot := info.AgencyPricing
			conversion = businessapi.Conversion{Currency: snapshot.CurrencyCode, QuotaPerUnit: snapshot.QuotaPerUnit, ExchangeRate: snapshot.ExchangeRate}
			ok = true
		}
		if info != nil {
			if _, direct := info.Billing.(*cnDirectRelaySession); direct {
				row, err := model.ReadCNDirectWallet(info.RequestId, info.UserId)
				var frozen struct {
					Currency string `json:"settlementCurrency"`
					Unit     string `json:"quotaPerUnit"`
					Rate     string `json:"exchangeRate"`
				}
				ok = err == nil && row.State == "settled" && row.Actual == int64(actualQuota) && common.UnmarshalJsonStr(row.ConversionJSON, &frozen) == nil
				if ok {
					conversion = businessapi.Conversion{Currency: frozen.Currency, QuotaPerUnit: frozen.Unit, ExchangeRate: frozen.Rate}
				}
			}
		}
		if ok {
			if settled, err := businessapi.SettledBilling(int64(actualQuota), conversion); err == nil {
				billing = settled
			}
		}
	}
	c.Set(businessapi.BillingKey, billing)
}
