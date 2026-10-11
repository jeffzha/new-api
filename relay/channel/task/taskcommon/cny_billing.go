package taskcommon

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

func QuotaFromCNYPerMillionTokens(totalTokens int64, snapshot *model.TaskProviderBillingSnapshot) (int, *common.QuotaClamp, error) {
	if snapshot == nil {
		return 0, nil, fmt.Errorf("task provider billing snapshot is missing")
	}
	if strings.TrimSpace(snapshot.Provider) == "" {
		return 0, nil, fmt.Errorf("task provider billing provider is missing")
	}
	if snapshot.Currency != "CNY" {
		return 0, nil, fmt.Errorf("task provider billing currency must be CNY")
	}
	unitPrice, err := decimal.NewFromString(snapshot.UnitPricePerMillionTokens)
	if err != nil || !unitPrice.IsPositive() {
		return 0, nil, fmt.Errorf("invalid frozen CNY unit price")
	}
	exchangeRate, err := decimal.NewFromString(snapshot.CNYPerUSD)
	if err != nil || !exchangeRate.IsPositive() {
		return 0, nil, fmt.Errorf("invalid frozen CNY/USD exchange rate")
	}
	if totalTokens <= 0 {
		return 0, nil, fmt.Errorf("total tokens must be positive")
	}
	if snapshot.GroupRatio < 0 || math.IsNaN(snapshot.GroupRatio) || math.IsInf(snapshot.GroupRatio, 0) {
		return 0, nil, fmt.Errorf("invalid frozen group ratio")
	}
	quotaDecimal := decimal.NewFromInt(totalTokens).
		Div(decimal.NewFromInt(1_000_000)).
		Mul(unitPrice).
		Div(exchangeRate).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(snapshot.GroupRatio))
	quota, clamp := common.QuotaFromDecimalChecked(quotaDecimal)
	return quota, clamp, nil
}

// QuotaFromProviderPerMillionTokens converts a frozen provider price to the
// platform's quota units. New overseas Seedance snapshots use USD directly;
// legacy CNY snapshots retain the historical exchange-rate conversion above.
func QuotaFromProviderPerMillionTokens(totalTokens int64, snapshot *model.TaskProviderBillingSnapshot) (int, *common.QuotaClamp, error) {
	if snapshot == nil {
		return 0, nil, fmt.Errorf("task provider billing snapshot is missing")
	}
	if snapshot.Currency == "CNY" {
		return QuotaFromCNYPerMillionTokens(totalTokens, snapshot)
	}
	if strings.TrimSpace(snapshot.Provider) == "" {
		return 0, nil, fmt.Errorf("task provider billing provider is missing")
	}
	if snapshot.Currency != "USD" {
		return 0, nil, fmt.Errorf("unsupported task provider billing currency %q", snapshot.Currency)
	}
	unitPrice, err := decimal.NewFromString(snapshot.UnitPricePerMillionTokens)
	if err != nil || !unitPrice.IsPositive() {
		return 0, nil, fmt.Errorf("invalid frozen USD unit price")
	}
	if totalTokens <= 0 {
		return 0, nil, fmt.Errorf("total tokens must be positive")
	}
	if snapshot.GroupRatio < 0 || math.IsNaN(snapshot.GroupRatio) || math.IsInf(snapshot.GroupRatio, 0) {
		return 0, nil, fmt.Errorf("invalid frozen group ratio")
	}
	quotaDecimal := decimal.NewFromInt(totalTokens).
		Div(decimal.NewFromInt(1_000_000)).
		Mul(unitPrice).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(snapshot.GroupRatio))
	quota, clamp := common.QuotaFromDecimalChecked(quotaDecimal)
	return quota, clamp, nil
}
