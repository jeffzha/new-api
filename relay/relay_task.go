package relay

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/upstreamevent"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type TaskSubmitResult struct {
	UpstreamTaskID  string
	TaskData        []byte
	ClientResponse  any
	Platform        constant.TaskPlatform
	Quota           int
	Immediate       *relaycommon.TaskInfo
	PluginState     []byte
	Endpoint        *model.TaskEndpointSnapshot
	ProviderBilling *model.TaskProviderBillingSnapshot
	//PerCallPrice   types.PriceData
}

// agencyTaskResponseBuffer delays adaptor c.JSON output until the provider
// acceptance receipt is durable. Several task adaptors write the response as
// part of DoResponse, so buffering at this boundary keeps the crash window
// from acknowledging a task before its submission attempt is recorded.
type agencyTaskResponseBuffer struct {
	gin.ResponseWriter
	header http.Header
	body   bytes.Buffer
	status int
	wrote  bool
}

const agencyTaskResponseReleaseKey = "agency_task_response_release"

func newAgencyTaskResponseBuffer(w gin.ResponseWriter) *agencyTaskResponseBuffer {
	return &agencyTaskResponseBuffer{ResponseWriter: w, header: make(http.Header), status: http.StatusOK}
}
func (w *agencyTaskResponseBuffer) Header() http.Header { return w.header }
func (w *agencyTaskResponseBuffer) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
	}
}
func (w *agencyTaskResponseBuffer) WriteHeaderNow() { w.wrote = true }
func (w *agencyTaskResponseBuffer) Write(p []byte) (int, error) {
	w.wrote = true
	return w.body.Write(p)
}
func (w *agencyTaskResponseBuffer) WriteString(s string) (int, error) {
	w.wrote = true
	return w.body.WriteString(s)
}
func (w *agencyTaskResponseBuffer) Status() int              { return w.status }
func (w *agencyTaskResponseBuffer) Size() int                { return w.body.Len() }
func (w *agencyTaskResponseBuffer) Written() bool            { return w.wrote }
func (w *agencyTaskResponseBuffer) Flush()                   { w.wrote = true }
func (w *agencyTaskResponseBuffer) CloseNotify() <-chan bool { return w.ResponseWriter.CloseNotify() }
func (w *agencyTaskResponseBuffer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.Hijack()
}
func (w *agencyTaskResponseBuffer) Pusher() http.Pusher { return w.ResponseWriter.Pusher() }

func (w *agencyTaskResponseBuffer) commit() {
	for key, values := range w.header {
		w.ResponseWriter.Header()[key] = values
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.body.Bytes())
}

// ReleaseAgencyTaskResponse completes (or discards) the response captured by
// a durable task submission. The controller calls this only after the local
// task row has been persisted, so a client can never observe an accepted task
// whose public task record was lost.
func ReleaseAgencyTaskResponse(c *gin.Context, commit bool) {
	value, exists := c.Get(agencyTaskResponseReleaseKey)
	if !exists {
		return
	}
	delete(c.Keys, agencyTaskResponseReleaseKey)
	release, ok := value.(func(bool))
	if ok {
		release(commit)
	}
}

// ResolveOriginTask 处理基于已有任务的提交（remix / continuation）：
// 查找原始任务、从中提取模型名称、将渠道锁定到原始任务的渠道
// （通过 info.LockedChannel，重试时复用同一渠道并轮换 key），
// 以及提取 OtherRatios（时长、分辨率）。
// 该函数在控制器的重试循环之前调用一次，其结果通过 info 字段和上下文持久化。
func ResolveOriginTask(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	// 检测 remix action
	path := c.Request.URL.Path
	if strings.Contains(path, "/v1/videos/") && strings.HasSuffix(path, "/remix") {
		info.Action = constant.TaskActionRemix
	}

	// 提取 remix 任务的 video_id
	if info.Action == constant.TaskActionRemix {
		videoID := c.Param("video_id")
		if strings.TrimSpace(videoID) == "" {
			return service.TaskErrorWrapperLocal(fmt.Errorf("video_id is required"), "invalid_request", http.StatusBadRequest)
		}
		info.OriginTaskID = videoID
	}

	if info.OriginTaskID == "" {
		return nil
	}

	// 查找原始任务
	originTask, exist, err := model.GetByTaskId(info.UserId, info.OriginTaskID)
	if err != nil {
		return service.TaskErrorWrapper(err, "get_origin_task_failed", http.StatusInternalServerError)
	}
	if !exist {
		return service.TaskErrorWrapperLocal(errors.New("task_origin_not_exist"), "task_not_exist", http.StatusBadRequest)
	}

	// 从原始任务推导模型名称
	if info.OriginModelName == "" {
		if originTask.Properties.OriginModelName != "" {
			info.OriginModelName = originTask.Properties.OriginModelName
		} else if originTask.Properties.UpstreamModelName != "" {
			info.OriginModelName = originTask.Properties.UpstreamModelName
		} else {
			var taskData map[string]any
			_ = common.Unmarshal(originTask.Data, &taskData)
			if m, ok := taskData["model"].(string); ok && m != "" {
				info.OriginModelName = m
			}
		}
	}

	// 锁定到原始任务的渠道（重试时复用同一渠道，轮换 key）
	ch, err := model.GetChannelById(originTask.ChannelId, true)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "channel_not_found", http.StatusBadRequest)
	}
	if ch.Status != common.ChannelStatusEnabled {
		return service.TaskErrorWrapperLocal(errors.New("the channel of the origin task is disabled"), "task_channel_disable", http.StatusBadRequest)
	}
	info.LockedChannel = ch

	if originTask.ChannelId != info.ChannelId {
		key, _, newAPIError := ch.GetNextEnabledKey()
		if newAPIError != nil {
			return service.TaskErrorWrapper(newAPIError, "channel_no_available_key", newAPIError.StatusCode)
		}
		common.SetContextKey(c, constant.ContextKeyChannelKey, key)
		common.SetContextKey(c, constant.ContextKeyChannelType, ch.Type)
		common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, ch.GetBaseURL())
		common.SetContextKey(c, constant.ContextKeyChannelId, originTask.ChannelId)

		info.ChannelBaseUrl = ch.GetBaseURL()
		info.ChannelId = originTask.ChannelId
		info.ChannelType = ch.Type
		info.ApiKey = key
	}

	// 提取 remix 参数（时长、分辨率 → OtherRatios）
	if info.Action == constant.TaskActionRemix {
		if originTask.PrivateData.BillingContext != nil {
			// 新的 remix 逻辑：直接从原始任务的 BillingContext 中提取 OtherRatios（如果存在）
			for s, f := range originTask.PrivateData.BillingContext.OtherRatios {
				info.PriceData.AddOtherRatio(s, f)
			}
		} else {
			// 旧的 remix 逻辑：直接从 task data 解析 seconds 和 size（如果存在）
			var taskData map[string]any
			_ = common.Unmarshal(originTask.Data, &taskData)
			secondsStr, _ := taskData["seconds"].(string)
			seconds, _ := strconv.Atoi(secondsStr)
			if seconds <= 0 {
				seconds = 4
			}
			// 历史任务数据可能包含未经校验的时长，作为计费乘数前必须钳制
			if seconds > relaycommon.MaxTaskDurationSeconds {
				seconds = relaycommon.MaxTaskDurationSeconds
			}
			sizeStr, _ := taskData["size"].(string)
			info.PriceData.AddOtherRatio("seconds", float64(seconds))
			info.PriceData.AddOtherRatio("size", 1)
			if sizeStr == "1792x1024" || sizeStr == "1024x1792" {
				info.PriceData.AddOtherRatio("size", 1.666667)
			}
		}
	}

	return nil
}

// ApplyChannelPin copies plugin-declared origin-task facts from the prepare
// context onto RelayInfo and, when the resolved pin retries on the same
// channel, writes LockedChannel. ResolveOriginTask is unchanged.
func ApplyChannelPin(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if info == nil {
		return nil
	}
	if info.TaskRelayInfo == nil {
		info.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
	}
	if tasks, ok := common.GetContextKeyType[[]*model.Task](c, constant.ContextKeyOriginTasks); ok {
		refs := make([]relaycommon.OriginTaskRef, 0, len(tasks))
		for _, task := range tasks {
			if task == nil {
				continue
			}
			refs = append(refs, relaycommon.OriginTaskRef{
				TaskID:         task.TaskID,
				UpstreamTaskID: task.GetUpstreamTaskID(),
				Action:         task.Action,
				Status:         string(task.Status),
				Data:           append([]byte(nil), task.Data...),
			})
		}
		info.OriginTasks = refs
	}
	pin, found, _ := service.GetChannelConstraints(c).ResolvedPin()
	if !found || pin.RetryMode != dto.PinRetrySameChannel {
		return nil
	}
	ch, err := model.CacheGetChannel(pin.ChannelId)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "origin_task_channel_disabled", http.StatusBadRequest)
	}
	if ch.Status != common.ChannelStatusEnabled {
		return service.TaskErrorWrapperLocal(errors.New("the channel of the origin task is disabled"), "origin_task_channel_disabled", http.StatusBadRequest)
	}
	info.LockedChannel = ch
	return nil
}

// ApplyOriginTaskAffinity is the compatibility name for ApplyChannelPin.
func ApplyOriginTaskAffinity(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	return ApplyChannelPin(c, info)
}

// RelayTaskSubmit 完成 task 提交的全部流程（每次尝试调用一次）：
// 刷新渠道元数据 → 确定 platform/adaptor → 验证请求 →
// 估算计费(EstimateBilling) → 计算价格 → 预扣费（仅首次）→
// 构建/发送/解析上游请求 → 提交后计费调整(AdjustBillingOnSubmit)。
// 共享控制器编排负责未落库退款、最终额度预留、落库和结算。
func RelayTaskSubmit(c *gin.Context, info *relaycommon.RelayInfo) (*TaskSubmitResult, *dto.TaskError) {
	info.InitChannelMeta(c)

	// 1. 确定 platform → 创建适配器 → 验证请求
	platform := constant.TaskPlatform(c.GetString("platform"))
	if platform == "" {
		platform = GetTaskPlatform(c)
	}
	platform, adaptor := getTaskAdaptorForRequest(c, platform)
	if adaptor == nil {
		code, message := TaskPlatformUnavailableError(platform)
		return nil, service.TaskErrorWrapperLocal(errors.New(message), code, http.StatusBadRequest)
	}
	// buildSubmitRequest runs during validation and the unreleased plugin
	// contract exposes this host-generated id to that hook.
	if info.PublicTaskID == "" {
		info.PublicTaskID = model.GenerateTaskID()
	}
	adaptor.Init(info)
	// Plugin submit hooks run during ValidateRequestAndSetAction and cache the
	// upstream body. OriginModelName is already seeded on that line (protocol
	// resolved_task_model, legacy submit, or GenRelayInfo original_model), so
	// map before validation. The empty-name CoverTaskActionToModelName
	// synthesis happens after validate and cannot move; skip the late block
	// when early mapping ran so a chain is never applied twice.
	mappedBeforeValidate := info.OriginModelName != ""
	if mappedBeforeValidate {
		info.UpstreamModelName = info.OriginModelName
		if err := helper.ModelMappedHelper(c, info, nil); err != nil {
			return nil, service.TaskErrorWrapperLocal(err, "model_mapping_failed", http.StatusBadRequest)
		}
	}
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		return nil, taskErr
	}

	// 2. 确定模型名称
	modelName := info.OriginModelName
	if modelName == "" {
		modelName = service.CoverTaskActionToModelName(platform, info.Action)
	}

	if !mappedBeforeValidate {
		info.OriginModelName = modelName
		info.UpstreamModelName = modelName
		if err := helper.ModelMappedHelper(c, info, nil); err != nil {
			return nil, service.TaskErrorWrapperLocal(err, "model_mapping_failed", http.StatusBadRequest)
		}
	}

	// 4. 价格计算：基础模型价格
	info.OriginModelName = modelName
	var providerBilling *model.TaskProviderBillingSnapshot
	estimator, hasProviderBilling := adaptor.(channel.TaskBillingEstimator)
	hasProviderBilling = hasProviderBilling && estimator.SupportsTaskBilling(info.ChannelType, modelName)
	var snapshot *agencycontract.PricingSnapshot
	var agencyErr error
	// Synthetic/system relay contexts may not carry a persisted user yet
	// (for example protocol validation tests). They must continue through the
	// ordinary pricing path; production requests always have a positive user ID.
	if info.UserId > 0 {
		snapshot, agencyErr = service.AgencyQuoteForUser(info.UserId, info.TokenId, modelName, info.StartTime.UnixMilli())
	}
	if agencyErr != nil && !errors.Is(agencyErr, gorm.ErrRecordNotFound) {
		return nil, service.TaskErrorWrapperLocal(agencyErr, "agency_pricing_unavailable", http.StatusServiceUnavailable)
	}
	// A durable agency customer must never fall back to the legacy task
	// pricing/funding path when its binding or agency schema cannot be read.
	// Without this guard a missing active binding is indistinguishable from a
	// legacy customer here; subscription-backed tasks could then be accepted
	// without a frozen agency quote or commission fact.
	if agencyErr != nil && model.IsAgencyDurableUser(info.UserId) {
		return nil, service.TaskErrorWrapperLocal(
			fmt.Errorf("agency pricing unavailable for durable user: %w", agencyErr),
			"agency_pricing_unavailable",
			http.StatusServiceUnavailable,
		)
	}
	managed := snapshot != nil
	pluginKey := c.GetString("task_plugin_key")
	var pinnedPlugin pluginruntime.PinnedPlugin
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedPlugin); exists {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedPlugin); ok {
			pinnedPlugin = pinned
			if pinned.Plugin != nil {
				pluginKey = pinned.Plugin.Meta.Key
			}
		}
	}
	exprStr, exprExists := billing_setting.ResolveTaskBillingExpr(pluginKey, modelName, info.UpstreamModelName)
	useTiered := !hasProviderBilling && (exprExists || billing_setting.GetBillingMode(modelName) == billing_setting.BillingModeTieredExpr)
	if useTiered {
		provider, supported := adaptor.(channel.TaskUsageFactsProvider)
		if !exprExists || !supported {
			return nil, service.TaskErrorWrapper(fmt.Errorf("task model %s has no usage expression or meter", modelName), "model_price_error", http.StatusBadRequest)
		}
		if billingexpr.UsesFixedPricing(exprStr) {
			return nil, service.TaskErrorWrapper(errors.New("fixed pricing is not supported for task usage expressions"), "model_price_error", http.StatusBadRequest)
		}
		if pinnedPlugin.Plugin != nil && pinnedPlugin.Generation != nil &&
			(pinnedPlugin.Generation.SharedModel(modelName) || pinnedPlugin.Generation.SharedModel(info.UpstreamModelName)) {
			schema, _ := pinnedPlugin.Plugin.Meta.UsageForModel(info.UpstreamModelName)
			if !billing_setting.TaskExprCompatible(exprStr, schema) {
				return nil, service.TaskErrorWrapper(fmt.Errorf("task model %s pricing is not configured for plugin %s", modelName, pluginKey), "model_price_error", http.StatusBadRequest)
			}
		}
		var facts map[string]any
		var factsErr error
		if validated, ok := adaptor.(channel.TaskValidatedUsageFactsProvider); ok {
			facts, factsErr = validated.ExtractUsageFactsValidated(c, info)
		} else {
			facts = provider.ExtractUsageFacts(c, info)
		}
		if factsErr != nil {
			return nil, service.TaskErrorWrapperLocal(factsErr, "plugin_usage_invalid", http.StatusBadRequest)
		}
		cost, trace, runErr := billingexpr.RunExprWithRequest(exprStr, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
		if runErr != nil || cost < 0 {
			if runErr == nil {
				runErr = errors.New("negative task expression result")
			}
			return nil, service.TaskErrorWrapper(runErr, "model_price_error", http.StatusBadRequest)
		}
		groupRatioInfo := helper.HandleGroupRatio(c, info)
		quota, clamp := common.QuotaRoundChecked(cost * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		noteTaskQuotaClamp(info, clamp)
		info.PriceData = hosttypes.PriceData{Quota: quota, QuotaToPreConsume: quota, GroupRatioInfo: groupRatioInfo}
		info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: billing_setting.BillingModeTieredExpr, ModelName: modelName, ExprString: exprStr, ExprHash: billingexpr.ExprHashString(exprStr), GroupRatio: groupRatioInfo.GroupRatio, EstimatedQuotaBeforeGroup: cost * common.QuotaPerUnit, EstimatedQuotaAfterGroup: quota, EstimatedTier: trace.MatchedTier, QuotaPerUnit: common.QuotaPerUnit, ExprVersion: billingexpr.ExprVersion(exprStr), TaskUsageBilling: true, UsageFacts: facts}
	}
	var standardTaskPrice *channel.TaskBillingEstimate
	var standardPriceData hosttypes.PriceData
	if hasProviderBilling {
		if managed {
			c.Set(helper.AgencyRatioOverrideContextKey, float64(1))
			var taskErr *dto.TaskError
			standardTaskPrice, taskErr = estimator.EstimateTaskBilling(c, info)
			if taskErr != nil {
				return nil, taskErr
			}
			if standardTaskPrice == nil {
				return nil, service.TaskErrorWrapperLocal(errors.New("provider billing estimate is empty"), "model_price_error", http.StatusInternalServerError)
			}
			c.Set(helper.AgencyRatioOverrideContextKey, float64(snapshot.SalesBPS)/10000)
		}
		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
		estimate, taskErr := estimator.EstimateTaskBilling(c, info)
		if taskErr != nil {
			return nil, taskErr
		}
		if estimate == nil {
			return nil, service.TaskErrorWrapperLocal(errors.New("provider billing estimate is empty"), "model_price_error", http.StatusInternalServerError)
		}
		info.PriceData = estimate.PriceData
		providerBilling = estimate.Snapshot
		if managed {
			if err := service.AttachAgencyQuote(info, snapshot, int64(standardTaskPrice.PriceData.Quota)); err != nil {
				return nil, service.TaskErrorWrapperLocal(err, "agency_pricing_unavailable", http.StatusServiceUnavailable)
			}
		}
	} else if !useTiered {
		var err error
		if managed {
			c.Set(helper.AgencyRatioOverrideContextKey, float64(1))
			standardPriceData, err = helper.ModelPriceHelperPerCall(c, info)
			if err != nil {
				return nil, service.TaskErrorWrapper(err, "model_price_error", http.StatusBadRequest)
			}
			c.Set(helper.AgencyRatioOverrideContextKey, float64(snapshot.SalesBPS)/10000)
		}
		priceData, err := helper.ModelPriceHelperPerCall(c, info)
		if err != nil {
			return nil, service.TaskErrorWrapper(err, "model_price_error", http.StatusBadRequest)
		}
		info.PriceData = priceData
	}

	// 5. 计费估算：让适配器根据用户请求提供 OtherRatios（时长、分辨率等）
	//    必须在 ModelPriceHelperPerCall 之后调用（它会重建 PriceData）。
	//    ResolveOriginTask 可能已在 remix 路径中预设了 OtherRatios，此处合并。
	if !hasProviderBilling {
		if estimatedRatios := adaptor.EstimateBilling(c, info); len(estimatedRatios) > 0 {
			for k, v := range estimatedRatios {
				info.PriceData.AddOtherRatio(k, v)
				if managed {
					standardPriceData.AddOtherRatio(k, v)
				}
			}
		}
	}

	// 6. 将 OtherRatios 应用到基础额度（饱和转换，防止溢出成负数）
	if info.TieredBillingSnapshot == nil && !common.StringsContains(constant.TaskPricePatches, modelName) {
		quotaWithRatios := info.PriceData.ApplyOtherRatiosToFloat(float64(info.PriceData.Quota))
		quota, clamp := common.QuotaFromFloatChecked(quotaWithRatios)
		info.PriceData.Quota = quota
		noteTaskQuotaClamp(info, clamp)
	}
	if managed && !hasProviderBilling {
		standardQuotaWithRatios, clamp := common.QuotaFromFloatChecked(standardPriceData.ApplyOtherRatiosToFloat(float64(standardPriceData.Quota)))
		if clamp != nil {
			noteTaskQuotaClamp(info, clamp)
		}
		if err := service.AttachAgencyQuote(info, snapshot, int64(standardQuotaWithRatios)); err != nil {
			return nil, service.TaskErrorWrapperLocal(err, "agency_pricing_unavailable", http.StatusServiceUnavailable)
		}
	}

	// 7. 预扣费（仅首次 — 重试时 info.Billing 已存在，跳过）
	if info.Billing == nil && (!info.PriceData.FreeModel || (managed && model.IsAgencyDurableUser(info.UserId))) {
		info.ForcePreConsume = true
		if apiErr := service.PreConsumeBilling(c, info.PriceData.Quota, info); apiErr != nil {
			return nil, service.TaskErrorFromAPIError(apiErr)
		}
	}
	if managed && model.IsAgencyDurableUser(info.UserId) {
		basis := model.AgencyTaskChargeBasis{Version: model.AgencyTaskChargeBasisVersion,
			ComponentBilling: common.GetEnvOrDefaultBool("AGENCY_COMPONENT_BILLING_ENABLED", false),
			Mode:             "tokens", QuotaPerUnit: common.QuotaPerUnit, ModelPrice: info.PriceData.ModelPrice,
			ModelRatio: info.PriceData.ModelRatio, OtherMultiplier: info.PriceData.OtherRatioMultiplier(),
			IgnoreOtherRatios: common.StringsContains(constant.TaskPricePatches, modelName), ProviderBilling: providerBilling}
		if providerBilling != nil {
			basis.Mode = "cny_tokens"
		} else if info.PriceData.UsePrice || basis.IgnoreOtherRatios {
			basis.Mode = "fixed"
		}
		if err := model.FreezeAgencyTaskChargeBasis(info.UserId, info.RequestId, basis); err != nil {
			return nil, service.TaskErrorWrapperLocal(err, "agency_task_basis_unavailable", http.StatusServiceUnavailable)
		}
	}

	// 8. 构建请求体
	requestBody, err := adaptor.BuildRequestBody(c, info)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "build_request_failed", http.StatusInternalServerError)
	}
	requestBodyBytes, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "read_request_failed", http.StatusInternalServerError)
	}
	upstreamevent.EmitTaskSubmitRequest(c, info, requestBodyBytes)
	requestBody = bytes.NewReader(requestBodyBytes)
	// Durable agency tasks create an immutable submission receipt before any
	// provider I/O. This prevents an ambiguous transport failure from being
	// mistaken for a rejected request and retried as a second video.
	agencyAttempt := model.IsAgencyDurableUser(info.UserId) && info.Billing != nil
	if agencyAttempt {
		digest := sha256.Sum256(requestBodyBytes)
		if err := model.BeginAgencyTaskSubmission(info.UserId, info.RequestId, info.PublicTaskID,
			hex.EncodeToString(digest[:]), c.Request.URL.Path); err != nil {
			return nil, service.TaskErrorWrapperLocal(err, "agency_task_submission_record_failed", http.StatusServiceUnavailable)
		}
	}

	// 9. 发送请求
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		if agencyAttempt {
			_ = model.ResolveAgencyTaskSubmissionFailure(info.RequestId, false, "transport_error")
			c.Set("agency_task_reconcile_required", true)
		}
		return nil, service.TaskErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if resp == nil {
		return nil, service.TaskErrorWrapperLocal(errors.New("upstream returned an empty response"), "fail_to_fetch_task", http.StatusBadGateway)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		if agencyAttempt && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			_ = model.ResolveAgencyTaskSubmissionFailure(info.RequestId, true, "upstream_rejected")
			c.Set("agency_task_rejected", true)
		} else if agencyAttempt {
			_ = model.ResolveAgencyTaskSubmissionFailure(info.RequestId, false, "upstream_status_unknown")
			c.Set("agency_task_reconcile_required", true)
		}
		return nil, service.TaskErrorWrapper(fmt.Errorf("%s", string(responseBody)), "fail_to_fetch_task", resp.StatusCode)
	}

	// 10. 返回 OtherRatios 给下游（header 必须在 DoResponse 写 body 之前设置）
	otherRatios := info.PriceData.OtherRatios()
	if otherRatios == nil {
		otherRatios = map[string]float64{}
	}
	ratiosJSON, _ := common.Marshal(otherRatios)
	c.Header("X-New-Api-Other-Ratios", string(ratiosJSON))

	// 11. 解析响应. Durable agency responses are buffered until the provider
	// task ID receipt has been committed.
	parsed, taskErr := adaptor.ParseResponse(c, resp, info)
	if taskErr != nil {
		if agencyAttempt {
			_ = model.ResolveAgencyTaskSubmissionFailure(info.RequestId, false, "response_parse_error")
			c.Set("agency_task_reconcile_required", true)
		}
		return nil, taskErr
	}
	if parsed == nil {
		return nil, service.TaskErrorWrapperLocal(errors.New("task adaptor returned an empty response"), "plugin_submit_response_invalid", http.StatusBadGateway)
	}
	if agencyAttempt {
		if err := model.RecordAgencyTaskSubmission(info.RequestId, info.PublicTaskID, parsed.UpstreamTaskID); err != nil {
			// The provider has accepted the task, but the receipt could not be
			// persisted. Keep the reservation for reconciliation; never refund
			// or issue another provider submission automatically.
			_ = model.ResolveAgencyTaskSubmissionFailure(info.RequestId, false, "receipt_persist_failed")
			c.Set("agency_task_reconcile_required", true)
			return nil, service.TaskErrorWrapperLocal(err, "agency_task_submission_persist_failed", http.StatusServiceUnavailable)
		}
	}

	// 11. 提交后计费调整：让适配器根据上游实际返回调整 OtherRatios
	finalQuota := info.PriceData.Quota
	if parsed.Immediate != nil && parsed.Immediate.Status == model.TaskStatusFailure {
		finalQuota = 0
	} else if snap := info.TieredBillingSnapshot; snap != nil {
		if parsed.Immediate != nil && parsed.Immediate.Status == model.TaskStatusSuccess && len(parsed.Immediate.UsageFacts) > 0 {
			settlement, facts, err := service.EvaluateTaskCompletionUsage(snap, parsed.Immediate.UsageFacts)
			if err != nil {
				logger.LogWarn(c, fmt.Sprintf("task immediate usage settlement failed; retaining reserved quota: %v", err))
			} else {
				finalQuota = settlement.ActualQuotaAfterGroup
				snap.UsageFacts = facts
				snap.EstimatedTier = settlement.MatchedTier
				noteTaskQuotaClamp(info, settlement.Clamp)
			}
		}
	} else {
		if adjustedRatios := adaptor.AdjustBillingOnSubmit(info, parsed.TaskData); len(adjustedRatios) > 0 {
			if adjustedQuota, ok := recalcQuotaFromRatios(info, adjustedRatios); ok {
				// 基于调整后的 ratios 重新计算 quota
				finalQuota = adjustedQuota
				info.PriceData.ReplaceOtherRatios(adjustedRatios)
				info.PriceData.Quota = finalQuota
			}
		}
	}
	upstreamevent.EmitTaskSubmitResponse(c, info, parsed.UpstreamTaskID, parsed.TaskData, platform, finalQuota)

	var endpoint *model.TaskEndpointSnapshot
	if provider, ok := adaptor.(channel.TaskEndpointSnapshotProvider); ok {
		endpoint = provider.TaskEndpointSnapshot()
	}

	info.PriceData.Quota = finalQuota

	return &TaskSubmitResult{
		UpstreamTaskID:  parsed.UpstreamTaskID,
		TaskData:        parsed.TaskData,
		ClientResponse:  parsed.ClientResponse,
		Platform:        platform,
		Quota:           finalQuota,
		Immediate:       parsed.Immediate,
		PluginState:     parsed.PluginState,
		Endpoint:        endpoint,
		ProviderBilling: providerBilling,
	}, nil
}

// recalcQuotaFromRatios 根据 adjustedRatios 重新计算 quota。
// 公式: baseQuota × ∏(ratio) — 其中 baseQuota 是不含 OtherRatios 的基础额度。
func recalcQuotaFromRatios(info *relaycommon.RelayInfo, ratios map[string]float64) (int, bool) {
	// 从 PriceData 获取不含 OtherRatios 的基础价格
	baseQuota := info.PriceData.RemoveOtherRatiosFromFloat(float64(info.PriceData.Quota))
	priceData := info.PriceData
	if !priceData.ReplaceOtherRatios(ratios) {
		return 0, false
	}
	// 应用新的 ratios
	result := priceData.ApplyOtherRatiosToFloat(baseQuota)
	quota, clamp := common.QuotaFromFloatChecked(result)
	noteTaskQuotaClamp(info, clamp)
	return quota, true
}

// noteTaskQuotaClamp records the first quota saturation event onto the task's
// RelayInfo so LogTaskConsumption can surface it on the submit log's
// admin_info. First non-nil clamp wins.
func noteTaskQuotaClamp(info *relaycommon.RelayInfo, clamp *common.QuotaClamp) {
	if clamp == nil || info == nil {
		return
	}
	if info.QuotaClamp == nil {
		info.QuotaClamp = clamp
	}
}

var fetchRespBuilders = map[int]func(c *gin.Context) (respBody []byte, taskResp *dto.TaskError){
	relayconstant.RelayModeVideoFetchByID: videoFetchByIDRespBodyBuilder,
}

type seedanceDomesticTaskFetchResponse struct {
	Code string                        `json:"code"`
	Data seedanceDomesticTaskFetchData `json:"data"`
}

type seedanceDomesticTaskFetchData struct {
	TaskID     string  `json:"task_id"`
	Status     string  `json:"status"`
	FailReason string  `json:"fail_reason"`
	ResultURL  *string `json:"result_url"`
	SubmitTime int64   `json:"submit_time"`
	StartTime  *int64  `json:"start_time"`
	FinishTime *int64  `json:"finish_time"`
	Progress   string  `json:"progress"`
}

func RelayTaskFetch(c *gin.Context, relayMode int) (taskResp *dto.TaskError) {
	respBuilder, ok := fetchRespBuilders[relayMode]
	if !ok {
		taskResp = service.TaskErrorWrapperLocal(errors.New("invalid_relay_mode"), "invalid_relay_mode", http.StatusBadRequest)
	}

	respBody, taskErr := respBuilder(c)
	if taskErr != nil {
		return taskErr
	}
	if len(respBody) == 0 {
		respBody = []byte("{\"code\":\"success\",\"data\":null}")
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	_, err := io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError)
		return
	}
	return
}

func videoFetchByIDRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	taskId := c.Param("task_id")
	if taskId == "" {
		taskId = c.GetString("task_id")
	}
	userId := c.GetInt("id")

	originTask, exist, err := model.GetByTaskId(userId, taskId)
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "get_task_failed", http.StatusInternalServerError)
		return
	}
	if !exist {
		taskResp = service.TaskErrorWrapperLocal(errors.New("task_not_exist"), "task_not_exist", http.StatusBadRequest)
		return
	}

	isOpenAIVideoAPI := strings.HasPrefix(c.Request.RequestURI, "/v1/videos/")

	// Gemini/Vertex 支持实时查询：用户 fetch 时直接从上游拉取最新状态
	if realtimeResp := tryRealtimeFetch(originTask, isOpenAIVideoAPI); len(realtimeResp) > 0 {
		respBody = realtimeResp
		return
	}

	// OpenAI Video API 格式: 走各 adaptor 的 ConvertToOpenAIVideo
	if isOpenAIVideoAPI {
		adaptor := GetTaskAdaptor(originTask.Platform)
		if adaptor == nil {
			taskResp = service.TaskErrorWrapperLocal(fmt.Errorf("invalid channel id: %d", originTask.ChannelId), "invalid_channel_id", http.StatusBadRequest)
			return
		}
		if converter, ok := adaptor.(channel.OpenAIVideoConverter); ok {
			openAIVideoData, err := converter.ConvertToOpenAIVideo(originTask)
			if err != nil {
				taskResp = service.TaskErrorWrapper(err, "convert_to_openai_video_failed", http.StatusInternalServerError)
				return
			}
			respBody = openAIVideoData
			return
		}
		taskResp = service.TaskErrorWrapperLocal(fmt.Errorf("not_implemented:%s", originTask.Platform), "not_implemented", http.StatusNotImplemented)
		return
	}

	if originTask.Platform == constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeSeedanceDomestic)) {
		var resultURL *string
		if originTask.Status == model.TaskStatusSuccess {
			value := strings.TrimSpace(originTask.GetResultURL())
			if value != "" {
				resultURL = &value
			}
		}
		var startTime *int64
		if originTask.StartTime > 0 {
			startTime = &originTask.StartTime
		}
		var finishTime *int64
		if originTask.FinishTime > 0 {
			finishTime = &originTask.FinishTime
		}
		respBody, err = common.Marshal(seedanceDomesticTaskFetchResponse{
			Code: dto.TaskSuccessCode,
			Data: seedanceDomesticTaskFetchData{
				TaskID:     originTask.TaskID,
				Status:     string(originTask.Status),
				FailReason: originTask.FailReason,
				ResultURL:  resultURL,
				SubmitTime: originTask.SubmitTime,
				StartTime:  startTime,
				FinishTime: finishTime,
				Progress:   originTask.Progress,
			},
		})
		if err != nil {
			taskResp = service.TaskErrorWrapper(err, "marshal_response_failed", http.StatusInternalServerError)
		}
		return
	}

	// 通用 TaskDto 格式
	respBody, err = common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: TaskModel2Dto(originTask),
	})
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "marshal_response_failed", http.StatusInternalServerError)
	}
	return
}

// tryRealtimeFetch 尝试从上游实时拉取 Gemini/Vertex 任务状态。
// 仅当渠道类型为 Gemini 或 Vertex 时触发；其他渠道或出错时返回 nil。
// 当非 OpenAI Video API 时，还会构建自定义格式的响应体。
func tryRealtimeFetch(task *model.Task, isOpenAIVideoAPI bool) []byte {
	channelModel, err := model.GetChannelById(task.ChannelId, true)
	if err != nil {
		return nil
	}
	if channelModel.Type != constant.ChannelTypeVertexAi && channelModel.Type != constant.ChannelTypeGemini {
		return nil
	}

	baseURL := constant.GetChannelBaseURL(channelModel.Type)
	if channelModel.GetBaseURL() != "" {
		baseURL = channelModel.GetBaseURL()
	}
	proxy := channelModel.GetSetting().Proxy
	adaptor := GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channelModel.Type)))
	if adaptor == nil {
		return nil
	}

	resp, err := adaptor.FetchTask(baseURL, channelModel.Key, task, proxy)
	if err != nil || resp == nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	ti, err := adaptor.ParseTaskResult(task, resp, body)
	if err != nil || ti == nil {
		return nil
	}

	snap := task.Snapshot()

	// 将上游最新状态更新到 task
	if ti.Status != "" {
		task.Status = model.TaskStatus(ti.Status)
	}
	if ti.Progress != "" {
		task.Progress = ti.Progress
	}
	if strings.HasPrefix(ti.Url, "data:") {
		// data: URI — kept in Data, not ResultURL
	} else if ti.Url != "" {
		task.PrivateData.ResultURL = ti.Url
	} else if task.Status == model.TaskStatusSuccess {
		// No URL from adaptor — construct proxy URL using public task ID
		task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
	}

	if !snap.Equal(task.Snapshot()) {
		_, _ = task.UpdateWithStatus(snap.Status)
	}

	// OpenAI Video API 由调用者的 ConvertToOpenAIVideo 分支处理
	if isOpenAIVideoAPI {
		return nil
	}

	// 非 OpenAI Video API: 构建自定义格式响应
	format := detectVideoFormat(body)
	out := map[string]any{
		"error":    nil,
		"format":   format,
		"metadata": nil,
		"status":   mapTaskStatusToSimple(task.Status),
		"task_id":  task.TaskID,
		"url":      task.GetResultURL(),
	}
	respBody, _ := common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: out,
	})
	return respBody
}

// detectVideoFormat 从 Gemini/Vertex 原始响应中探测视频格式
func detectVideoFormat(rawBody []byte) string {
	var raw map[string]any
	if err := common.Unmarshal(rawBody, &raw); err != nil {
		return "mp4"
	}
	respObj, ok := raw["response"].(map[string]any)
	if !ok {
		return "mp4"
	}
	vids, ok := respObj["videos"].([]any)
	if !ok || len(vids) == 0 {
		return "mp4"
	}
	v0, ok := vids[0].(map[string]any)
	if !ok {
		return "mp4"
	}
	mt, ok := v0["mimeType"].(string)
	if !ok || mt == "" || strings.Contains(mt, "mp4") {
		return "mp4"
	}
	return mt
}

// mapTaskStatusToSimple 将内部 TaskStatus 映射为简化状态字符串
func mapTaskStatusToSimple(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusSuccess:
		return "succeeded"
	case model.TaskStatusFailure:
		return "failed"
	case model.TaskStatusQueued, model.TaskStatusSubmitted:
		return "queued"
	default:
		return "processing"
	}
}

func TaskModel2Dto(task *model.Task) *dto.TaskDto {
	return &dto.TaskDto{
		ID:         task.ID,
		CreatedAt:  task.CreatedAt,
		UpdatedAt:  task.UpdatedAt,
		TaskID:     task.TaskID,
		Platform:   string(task.Platform),
		UserId:     task.UserId,
		Group:      task.Group,
		ChannelId:  task.ChannelId,
		Quota:      task.Quota,
		Action:     constant.NormalizeTaskAction(task.Action),
		Status:     string(task.Status),
		FailReason: task.FailReason,
		ResultURL:  task.GetResultURL(),
		SubmitTime: task.SubmitTime,
		StartTime:  task.StartTime,
		FinishTime: task.FinishTime,
		Progress:   task.Progress,
		Properties: task.Properties,
		Username:   task.Username,
		Data:       task.Data,
	}
}
