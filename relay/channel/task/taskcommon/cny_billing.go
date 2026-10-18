package taskcommon

import (
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

func QuotaFromCNYPerMillionTokens(totalTokens int64, snapshot *model.TaskProviderBillingSnapshot) (int, *common.QuotaClamp, error) {
	return QuotaFromCNYTask(totalTokens, snapshot)
}

func QuotaFromCNYTask(totalTokens int64, snapshot *model.TaskProviderBillingSnapshot) (int, *common.QuotaClamp, error) {
	if snapshot == nil {
		return 0, nil, fmt.Errorf("task provider billing snapshot is missing")
	}
	baseCharge, err := snapshot.CNYBaseChargeUSD(totalTokens)
	if err != nil {
		return 0, nil, err
	}
	if snapshot.GroupRatio < 0 || math.IsNaN(snapshot.GroupRatio) || math.IsInf(snapshot.GroupRatio, 0) {
		return 0, nil, fmt.Errorf("invalid frozen group ratio")
	}
	quotaDecimal := baseCharge.
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(snapshot.GroupRatio))
	quota, clamp := common.QuotaFromDecimalChecked(quotaDecimal)
	return quota, clamp, nil
}
