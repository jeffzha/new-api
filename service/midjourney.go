package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/QuantumNous/new-api/service/opsreserve"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

func CovertMjpActionToModelName(mjAction string) string {
	modelName := "mj_" + strings.ToLower(mjAction)
	if mjAction == constant.MjActionSwapFace {
		modelName = "swap_face"
	}
	return modelName
}

// BeginOPSMidjourneyBilling reserves before the legacy submission transport.
func BeginOPSMidjourneyBilling(c *gin.Context, info *relaycommon.RelayInfo, quota int) error {
	if !opsreserve.Enabled() {
		return nil
	}
	if info.Billing == nil {
		session, apiErr := newOPSReservationSession(c, info, quota)
		if apiErr != nil {
			return apiErr
		}
		info.Billing = session
	}
	if err := BeginOPSReservationDispatch(info); err != nil {
		info.Billing.Refund(c)
		return err
	}
	return nil
}

// ObserveOPSMidjourneyResponse distinguishes a documented rejection from an
// uncertain transport outcome. Only the former can release a dispatched hold.
func ObserveOPSMidjourneyResponse(info *relaycommon.RelayInfo, status, code int, err error) {
	if _, ok := info.Billing.(*opsReservationSession); !ok {
		return
	}
	if err != nil || status >= 500 || status == http.StatusRequestTimeout || code == 0 {
		info.LastError = relaytypes.NewError(errors.New("Midjourney provider outcome uncertain"), relaytypes.ErrorCodeDoRequestFailed)
	} else if status != http.StatusOK || code != 1 && code != 21 && code != 22 {
		info.LastError = relaytypes.NewErrorWithStatusCode(errors.New("Midjourney submission rejected"), relaytypes.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
	}
	ObserveOPSReservationAttempt(info, info.LastError)
}

func settleOPSMidjourney(ctx context.Context, task *model.Midjourney, refund bool, reason string) error {
	client, err := opsreserve.Configured()
	if err != nil || client.Transport.KeyID != task.OPSReservationGateway {
		return opsreserve.ErrUnavailable
	}
	var intent model.OPSHoldIntent
	if err = client.DB.WithContext(ctx).Where("gateway = ? AND request = ?", task.OPSReservationGateway, task.OPSReservationRequest).First(&intent).Error; err != nil {
		return err
	}
	payload := intent.UsagePayload
	if refund {
		payload = intent.RefundPayload
	}
	if payload == "" {
		other := model.NewLogOther()
		other.SetPublic("task_id", task.MjId)
		other.SetPublic("request_id", task.OPSReservationRequest)
		other.SetPublic("reason", reason)
		other.SetPublic("billing_source", "ops")
		other.SetPublic("image_count", 1)
		other.SetPublic("quota_per_unit", common.QuotaPerUnit)
		other.SetAdmin("upstream", map[string]any{"base_url": task.OPSReservationBaseURL})
		if scope := os.Getenv("OPS_PROJECT_SCOPE"); scope != "" {
			other.SetPublic("billing_scope", scope)
		}
		kind, occurred := model.LogTypeConsume, task.SubmitTime/1000
		if refund {
			kind, occurred = model.LogTypeRefund, time.Now().Unix()
		}
		log := &model.Log{Id: task.Id, UserId: task.UserId, Type: kind, RequestId: task.OPSReservationRequest, CreatedAt: occurred, ModelName: CovertMjpActionToModelName(task.Action), Quota: task.Quota, TokenId: task.TokenId, ChannelId: task.GetBillingChannelId(), Other: other.JSONString()}
		event := opspush.EventV11(log, "", nil, opspush.CurrentConversion())
		if event == nil {
			return opsreserve.ErrUnavailable
		}
		if err := opspush.AttachUpstream(client.DB, log, event); err != nil {
			return err
		}
		stage := "usage"
		if refund {
			stage = "refund"
		}
		raw, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": task.OPSReservationRequest + "-mj-" + stage, "events": []any{event}})
		if err != nil {
			return err
		}
		payload = string(raw)
	}
	if err = model.FinalizeOPSHoldMidjourney(ctx, client.DB, task, payload, refund); err != nil {
		return err
	}
	// Delivery failure leaves the immutable outbox for bounded recovery.
	if err = client.DeliverUsage(ctx, task.OPSReservationRequest); err != nil {
		common.SysError("OPS Midjourney usage pending durable delivery")
	}
	if refund {
		task.Quota = 0
	}
	return nil
}

// RecoverOPSMidjourney resumes only a persisted, provider-accepted submission.
// A missing task or an unknown transport outcome never proves a free call.
func RecoverOPSMidjourney(ctx context.Context, task *model.Midjourney) error {
	if task.Code != 1 && task.Code != 21 && task.Code != 22 {
		return opsreserve.ErrUnavailable
	}
	if err := settleOPSMidjourney(ctx, task, false, ""); err != nil {
		return err
	}
	if task.Status == "FAILURE" || task.FailReason != "" {
		return settleOPSMidjourney(ctx, task, true, task.FailReason)
	}
	return nil
}

// PrepareMidjourneyTaskBilling sets the durable refund marker before the task is inserted.
func PrepareMidjourneyTaskBilling(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, quota int, shouldBill bool) (bool, error) {
	if task == nil {
		return false, errors.New("Midjourney task is nil")
	}
	task.Quota = 0
	task.TokenId = 0
	task.BillingChannelId = 0
	task.CNBillingKey = ""
	if !shouldBill {
		return false, nil
	}
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	if quota < 0 {
		return false, errors.New("quota cannot be negative")
	}
	if relayInfo.BillingSource == BillingSourceSubscription {
		return false, errors.New("legacy Midjourney billing does not support subscriptions")
	}
	if relayInfo.AgencyPricing != nil && strings.TrimSpace(relayInfo.RequestId) == "" {
		relayInfo.RequestId = common.NewRequestId()
	}

	task.Quota = quota
	task.BillingChannelId = task.ChannelId
	if session, ok := relayInfo.Billing.(*opsReservationSession); ok {
		task.OPSReservationGateway = session.ReservationGateway()
		task.OPSReservationRequest = relayInfo.RequestId
		task.OPSReservationBaseURL = relayInfo.ChannelBaseUrl
		if task.OPSReservationBaseURL == "" {
			task.OPSReservationBaseURL = constant.GetChannelBaseURL(relayInfo.ChannelType)
		}
		task.TokenId = relayInfo.TokenId
		if relayInfo.IsPlayground {
			task.TokenId = 0
		}
		return true, nil
	}
	if relayInfo.AgencyPricing != nil {
		task.AgencyChargeID = relayInfo.RequestId
	}
	if relayInfo.ChannelMeta != nil && relayInfo.ChannelId > 0 {
		task.BillingChannelId = relayInfo.ChannelId
	}
	if model.CNMidjourneyRequested() {
		if relayInfo.AgencyPricing != nil || model.IsAgencyDurableUser(relayInfo.UserId) {
			return false, model.ErrAgencyFundingUnavailable
		}
		baseURL := relayInfo.ChannelBaseUrl
		if baseURL == "" {
			baseURL = constant.GetChannelBaseURL(relayInfo.ChannelType)
		}
		if err := model.PrepareCNMidjourney(task, relayInfo.TokenId, relayInfo.TokenUnlimited, relayInfo.IsPlayground, baseURL); err != nil {
			return false, err
		}
	}
	return true, nil
}

// SettleMidjourneyTaskBilling charges a persisted legacy task and records the applied stages.
func SettleMidjourneyTaskBilling(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, prepared bool) (bool, error) {
	if !prepared {
		return false, nil
	}
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	if task == nil || task.Id == 0 {
		return false, errors.New("Midjourney task must be persisted before billing")
	}
	if task.OPSReservationGateway != "" {
		if err := settleOPSMidjourney(context.Background(), task, false, ""); err != nil {
			return false, err
		}
		if session, ok := relayInfo.Billing.(*opsReservationSession); ok {
			session.mu.Lock()
			session.done, session.actual = true, task.Quota
			session.mu.Unlock()
		}
		return true, nil
	}
	if task.CNBillingKey != "" {
		if relayInfo.UserId != task.UserId || relayInfo.AgencyPricing != nil || relayInfo.BillingSource == BillingSourceSubscription {
			return false, model.ErrCNConflict
		}
		if err := model.SettleCNMidjourney(context.Background(), model.DB, task.Id, task.UserId, task.MjId); err != nil {
			return false, err
		}
		return true, nil
	}

	var result postConsumeQuotaResult
	var billingErr error
	// Durable agency customers must settle the wallet/funding projection and
	// token in the same database transaction. The legacy helper intentionally
	// keeps its historical behavior for unmanaged users.
	if relayInfo.AgencyPricing != nil &&
		relayInfo.RequestId != "" &&
		model.IsAgencyDurableUser(relayInfo.UserId) &&
		!relayInfo.IsPlayground {
		var paid int64
		var moneySeq int64
		paid, moneySeq, billingErr = model.TryReserveAgencyWalletAndTokenWithSequence(
			relayInfo.UserId,
			relayInfo.TokenId,
			task.Quota,
			relayInfo.TokenKey,
			relayInfo.RequestId,
			int64(task.Quota),
			relayInfo.TokenUnlimited,
			relayInfo.AgencyPricing,
		)
		if billingErr == nil {
			relayInfo.AgencyPaidAllocatedQuota += paid
			if moneySeq > 0 {
				relayInfo.AgencyMoneySeq = moneySeq
			}
			result.FundingApplied = true
			result.TokenApplied = true
		}
	} else {
		result, billingErr = postConsumeQuotaWithResult(relayInfo, task.Quota, 0, true)
	}
	if !result.FundingApplied {
		task.Quota = 0
		task.TokenId = 0
		task.BillingChannelId = 0
		task.AgencyBillingEventID = ""
		if updateErr := task.UpdateBillingState(); updateErr != nil {
			return false, errors.Join(billingErr, fmt.Errorf("clear Midjourney billing state: %w", updateErr))
		}
		return false, billingErr
	}

	task.TokenId = 0
	if result.TokenApplied {
		task.TokenId = relayInfo.TokenId
	}
	if err := RecordAgencyBillingEvent(relayInfo, int64(task.Quota), "success"); err != nil {
		if relayInfo.AgencyBillingEventID != "" {
			task.AgencyBillingEventID = relayInfo.AgencyBillingEventID
		}
		if updateErr := task.UpdateBillingState(); updateErr != nil {
			return true, errors.Join(billingErr, err, fmt.Errorf("update Midjourney agency billing marker: %w", updateErr))
		}
		return true, errors.Join(billingErr, err)
	}
	task.AgencyBillingEventID = relayInfo.AgencyBillingEventID
	if updateErr := task.UpdateBillingState(); updateErr != nil {
		return true, errors.Join(billingErr, fmt.Errorf("update Midjourney billing state: %w", updateErr))
	}
	return true, billingErr
}

// RefundMidjourneyQuota reverses every accounting element recorded for a billed legacy task.
func RefundMidjourneyQuota(ctx context.Context, task *model.Midjourney, reason string) bool {
	if task == nil {
		return false
	}
	if task.OPSReservationGateway != "" {
		if err := settleOPSMidjourney(ctx, task, true, reason); err != nil {
			logger.LogWarn(ctx, "OPS Midjourney refund pending reconciliation")
			return false
		}
		return true
	}
	if task.CNBillingKey != "" {
		if err := model.RequestCNMidjourneyRefund(ctx, model.DB, task.Id, task.UserId, task.MjId, reason); err != nil {
			logger.LogWarn(ctx, "CN Midjourney refund intent persistence failed")
			return false
		}
		if err := model.CompleteCNMidjourneyRefund(ctx, model.DB, task.CNBillingKey); err != nil {
			logger.LogWarn(ctx, "CN Midjourney refund pending durable recovery")
			return false
		}
		task.Quota = 0
		return true
	}
	quota := task.Quota
	if quota == 0 {
		return true
	}

	var fundingErr error
	tokenKey := ""
	if task.TokenId > 0 {
		tokenKey = resolveTokenKey(ctx, task.TokenId, task.MjId)
	}
	if task.AgencyChargeID != "" {
		if model.IsAgencyDurableUser(task.UserId) {
			_, fundingErr = model.ReleaseAgencyWalletAndToken(task.UserId, task.TokenId, quota, tokenKey, task.AgencyChargeID)
		} else {
			_, fundingErr = model.ReleaseUserQuotaAndAgency(task.UserId, quota, task.AgencyChargeID)
		}
	} else {
		fundingErr = model.IncreaseUserQuota(task.UserId, quota, false)
	}
	if fundingErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("退还 Midjourney 用户额度失败 task %s: %s", task.MjId, fundingErr.Error()))
		return false
	}
	if err := RecordAgencyMidjourneyRefundEvent(task, int64(quota), reason); err != nil {
		logger.LogError(ctx, fmt.Sprintf("记录代理商 Midjourney 佣金冲正失败 task %s: %s", task.MjId, err.Error()))
	}

	if task.TokenId > 0 && !(task.AgencyChargeID != "" && model.IsAgencyDurableUser(task.UserId)) {
		if tokenKey != "" {
			if err := model.IncreaseTokenQuota(task.TokenId, tokenKey, quota); err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("退还 Midjourney 令牌额度失败 task %s: %s", task.MjId, err.Error()))
			}
		}
	}

	billingChannelId := task.GetBillingChannelId()
	model.UpdateUserUsedQuota(task.UserId, -quota)
	model.UpdateChannelUsedQuota(billingChannelId, -quota)
	other := model.NewLogOther()
	other.SetPublic("task_id", task.MjId)
	other.SetPublic("reason", reason)
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: billingChannelId,
		ModelName: CovertMjpActionToModelName(task.Action),
		Quota:     quota,
		TokenId:   task.TokenId,
		Other:     other,
	})

	task.Quota = 0
	if err := task.UpdateBillingState(); err != nil {
		logger.LogError(ctx, fmt.Sprintf("Midjourney 退款成功但清除 quota 失败 task %s: %s", task.MjId, err.Error()))
	}
	return true
}

func GetMjRequestModel(relayMode int, midjRequest *dto.MidjourneyRequest) (string, *dto.MidjourneyResponse, bool) {
	action := ""
	if relayMode == relayconstant.RelayModeMidjourneyAction {
		// plus request
		err := CoverPlusActionToNormalAction(midjRequest)
		if err != nil {
			return "", err, false
		}
		action = midjRequest.Action
	} else {
		switch relayMode {
		case relayconstant.RelayModeMidjourneyImagine:
			action = constant.MjActionImagine
		case relayconstant.RelayModeMidjourneyVideo:
			action = constant.MjActionVideo
		case relayconstant.RelayModeMidjourneyEdits:
			action = constant.MjActionEdits
		case relayconstant.RelayModeMidjourneyDescribe:
			action = constant.MjActionDescribe
		case relayconstant.RelayModeMidjourneyBlend:
			action = constant.MjActionBlend
		case relayconstant.RelayModeMidjourneyShorten:
			action = constant.MjActionShorten
		case relayconstant.RelayModeMidjourneyChange:
			action = midjRequest.Action
		case relayconstant.RelayModeMidjourneyModal:
			action = constant.MjActionModal
		case relayconstant.RelayModeSwapFace:
			action = constant.MjActionSwapFace
		case relayconstant.RelayModeMidjourneyUpload:
			action = constant.MjActionUpload
		case relayconstant.RelayModeMidjourneySimpleChange:
			params := ConvertSimpleChangeParams(midjRequest.Content)
			if params == nil {
				return "", MidjourneyErrorWrapper(constant.MjRequestError, "invalid_request"), false
			}
			action = params.Action
		case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition, relayconstant.RelayModeMidjourneyNotify:
			return "", nil, true
		default:
			return "", MidjourneyErrorWrapper(constant.MjRequestError, "unknown_relay_action"), false
		}
	}
	modelName := CovertMjpActionToModelName(action)
	return modelName, nil, true
}

func CoverPlusActionToNormalAction(midjRequest *dto.MidjourneyRequest) *dto.MidjourneyResponse {
	// "customId": "MJ::JOB::upsample::2::3dbbd469-36af-4a0f-8f02-df6c579e7011"
	customId := midjRequest.CustomId
	if customId == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "custom_id_is_required")
	}
	splits := strings.Split(customId, "::")
	var action string
	if splits[1] == "JOB" {
		action = splits[2]
	} else {
		action = splits[1]
	}

	if action == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action")
	}
	if strings.Contains(action, "upsample") {
		index, err := strconv.Atoi(splits[3])
		if err != nil {
			return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
		}
		midjRequest.Index = index
		midjRequest.Action = constant.MjActionUpscale
	} else if strings.Contains(action, "variation") {
		midjRequest.Index = 1
		if action == "variation" {
			index, err := strconv.Atoi(splits[3])
			if err != nil {
				return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
			}
			midjRequest.Index = index
			midjRequest.Action = constant.MjActionVariation
		} else if action == "low_variation" {
			midjRequest.Action = constant.MjActionLowVariation
		} else if action == "high_variation" {
			midjRequest.Action = constant.MjActionHighVariation
		}
	} else if strings.Contains(action, "pan") {
		midjRequest.Action = constant.MjActionPan
		midjRequest.Index = 1
	} else if strings.Contains(action, "reroll") {
		midjRequest.Action = constant.MjActionReRoll
		midjRequest.Index = 1
	} else if action == "Outpaint" {
		midjRequest.Action = constant.MjActionZoom
		midjRequest.Index = 1
	} else if action == "CustomZoom" {
		midjRequest.Action = constant.MjActionCustomZoom
		midjRequest.Index = 1
	} else if action == "Inpaint" {
		midjRequest.Action = constant.MjActionInPaint
		midjRequest.Index = 1
	} else {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action:"+customId)
	}
	return nil
}

func ConvertSimpleChangeParams(content string) *dto.MidjourneyRequest {
	split := strings.Split(content, " ")
	if len(split) != 2 {
		return nil
	}

	action := strings.ToLower(split[1])
	changeParams := &dto.MidjourneyRequest{}
	changeParams.TaskId = split[0]

	if action[0] == 'u' {
		changeParams.Action = "UPSCALE"
	} else if action[0] == 'v' {
		changeParams.Action = "VARIATION"
	} else if action == "r" {
		changeParams.Action = "REROLL"
		return changeParams
	} else {
		return nil
	}

	index, err := strconv.Atoi(action[1:2])
	if err != nil || index < 1 || index > 4 {
		return nil
	}
	changeParams.Index = index
	return changeParams
}

func DoMidjourneyHttpRequest(c *gin.Context, timeout time.Duration, fullRequestURL string) (*dto.MidjourneyResponseWithStatusCode, []byte, error) {
	var nullBytes []byte
	//var requestBody io.Reader
	//requestBody = c.Request.Body
	// read request body to json, delete accountFilter and notifyHook
	var mapResult map[string]any
	// if get request, no need to read request body
	if c.Request.Method != "GET" {
		err := common.DecodeJson(c.Request.Body, &mapResult)
		if err != nil {
			return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "read_request_body_failed", http.StatusInternalServerError), nullBytes, err
		}
		if !setting.MjAccountFilterEnabled {
			delete(mapResult, "accountFilter")
		}
		if !setting.MjNotifyEnabled {
			delete(mapResult, "notifyHook")
		}
		//req, err := http.NewRequest(c.Request.Method, fullRequestURL, requestBody)
		// make new request with mapResult
	}
	if setting.MjModeClearEnabled {
		if prompt, ok := mapResult["prompt"].(string); ok {
			prompt = strings.Replace(prompt, "--fast", "", -1)
			prompt = strings.Replace(prompt, "--relax", "", -1)
			prompt = strings.Replace(prompt, "--turbo", "", -1)

			mapResult["prompt"] = prompt
		}
	}
	reqBody, err := common.Marshal(mapResult)
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "marshal_request_body_failed", http.StatusInternalServerError), nullBytes, err
	}
	req, err := http.NewRequest(c.Request.Method, fullRequestURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "create_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// 使用带有超时的 context 创建新的请求
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	req.Header.Set("Accept", c.Request.Header.Get("Accept"))
	auth := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	if auth != "" {
		auth = strings.TrimPrefix(auth, "Bearer ")
		req.Header.Set("mj-api-secret", auth)
	}
	defer cancel()
	resp, err := GetHttpClient().Do(req)
	if err != nil {
		common.SysLog("do request failed: " + err.Error())
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "do_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	statusCode := resp.StatusCode
	//if statusCode != 200  {
	//	return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "bad_response_status_code", statusCode), nullBytes, nil
	//}
	err = req.Body.Close()
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "close_request_body_failed", statusCode), nullBytes, err
	}
	err = c.Request.Body.Close()
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "close_request_body_failed", statusCode), nullBytes, err
	}
	var midjResponse dto.MidjourneyResponse
	var midjourneyUploadsResponse dto.MidjourneyUploadResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "read_response_body_failed", statusCode), nullBytes, err
	}
	CloseResponseBodyGracefully(resp)
	logger.LogDebug(c, "midjourney response body: %s", responseBody)
	if len(responseBody) == 0 {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "empty_response_body", statusCode), responseBody, nil
	} else {
		err = common.Unmarshal(responseBody, &midjResponse)
		if err != nil {
			err2 := common.Unmarshal(responseBody, &midjourneyUploadsResponse)
			if err2 != nil {
				return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "unmarshal_response_body_failed", statusCode), responseBody, err
			}
		}
	}
	//for k, v := range resp.Header {
	//	c.Writer.Header().Set(k, v[0])
	//}
	return &dto.MidjourneyResponseWithStatusCode{
		StatusCode: statusCode,
		Response:   midjResponse,
	}, responseBody, nil
}
