package mobilecloudseedance

import (
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

func (a *TaskAdaptor) SupportsTaskBilling(channelType int, modelName string) bool {
	return channelType == constant.ChannelTypeMobileCloudSeedance && modelName == ModelName
}

func (a *TaskAdaptor) EstimateTaskBilling(c *gin.Context, info *relaycommon.RelayInfo) (*channel.TaskBillingEstimate, *dto.TaskError) {
	payload, err := requestFromContext(c)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusBadRequest)
	}

	videoCount := videoInputCount(payload)
	hasVideo := videoCount > 0
	unitPrice, ok := seedancepricing.GetUnitPriceCNY(
		seedancepricing.StandardSeedanceModel,
		payload.Resolution,
		hasVideo,
	)
	if !ok {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("CNY price is not configured for resolution %s", payload.Resolution),
			"model_price_error",
			http.StatusBadRequest,
		)
	}

	exchangeRate := operation_setting.USDExchangeRate
	if exchangeRate <= 0 || math.IsNaN(exchangeRate) || math.IsInf(exchangeRate, 0) {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("USD exchange rate must be positive and finite"),
			"model_price_error",
			http.StatusInternalServerError,
		)
	}
	groupRatio := info.PriceData.GroupRatioInfo.GroupRatio
	if groupRatio < 0 || math.IsNaN(groupRatio) || math.IsInf(groupRatio, 0) {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("group ratio must be non-negative and finite"),
			"model_price_error",
			http.StatusInternalServerError,
		)
	}

	duration := int64(5)
	if payload.Duration != nil {
		duration = int64(*payload.Duration)
		if duration == -1 {
			duration = 15
		}
	}
	if payload.Frames != nil {
		frameDuration := (int64(*payload.Frames) + 23) / 24
		if frameDuration > duration {
			duration = frameDuration
		}
	}
	if hasVideo {
		// The provider accepts at most three reference videos, each up to
		// fifteen seconds. Reserve the bounded maximum and reconcile against
		// usage.total_tokens after completion.
		duration += videoCount * 15
	}

	pixels := int64(1280 * 720)
	if payload.Resolution == "1080p" {
		pixels = 1920 * 1080
	}
	estimatedTokens := decimal.NewFromInt(duration).
		Mul(decimal.NewFromInt(pixels)).
		Mul(decimal.NewFromInt(24)).
		Div(decimal.NewFromInt(1024)).
		Ceil().
		IntPart()

	snapshot := &model.TaskProviderBillingSnapshot{
		Provider:                    billingProvider,
		Currency:                    "CNY",
		UnitPricePerMillionTokens:   unitPrice.String(),
		CNYPerUSD:                   strconv.FormatFloat(exchangeRate, 'f', -1, 64),
		GroupRatio:                  groupRatio,
		Resolution:                  payload.Resolution,
		HasVideoInput:               hasVideo,
		EstimatedTokens:             estimatedTokens,
		AsyncReconciliationRequired: false,
	}
	quota, clamp, err := taskcommon.QuotaFromCNYPerMillionTokens(estimatedTokens, snapshot)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusInternalServerError)
	}
	if clamp != nil {
		info.QuotaClamp = clamp
	}

	return &channel.TaskBillingEstimate{
		PriceData: types.PriceData{
			ModelPrice:     unitPrice.DivRound(decimal.NewFromFloat(exchangeRate), 12).InexactFloat64(),
			Quota:          quota,
			FreeModel:      groupRatio == 0,
			GroupRatioInfo: info.PriceData.GroupRatioInfo,
		},
		Snapshot: snapshot,
	}, nil
}

func (a *TaskAdaptor) AdjustBillingOnCompleteChecked(task *model.Task, taskResult *relaycommon.TaskInfo) (int, *common.QuotaClamp, bool, error) {
	if task == nil || taskResult == nil {
		return 0, nil, false, nil
	}
	billingContext := task.PrivateData.BillingContext
	if billingContext == nil || billingContext.ProviderBilling == nil {
		return 0, nil, false, nil
	}
	snapshot := billingContext.ProviderBilling
	if snapshot.Provider != billingProvider {
		return 0, nil, false, nil
	}
	if taskResult.TotalTokens <= 0 {
		return 0, nil, true, fmt.Errorf("Mobile Cloud Seedance succeeded without usage.total_tokens")
	}
	quota, clamp, err := taskcommon.QuotaFromCNYPerMillionTokens(int64(taskResult.TotalTokens), snapshot)
	return quota, clamp, true, err
}

func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	quota, _, handled, err := a.AdjustBillingOnCompleteChecked(task, taskResult)
	if err != nil || !handled {
		return 0
	}
	return quota
}
