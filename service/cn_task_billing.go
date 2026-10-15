package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

type CNTaskSettler struct {
	key      string
	reserved int
	settled  bool
	mu       sync.Mutex
}

func CNTaskBillingKey(info *relaycommon.RelayInfo) string {
	if info != nil {
		if s, ok := info.Billing.(*CNTaskSettler); ok {
			return s.key
		}
	}
	return ""
}

func PreConsumeCNTaskBilling(info *relaycommon.RelayInfo, platform constant.TaskPlatform, provider *model.TaskProviderBillingSnapshot, quota int) *types.NewAPIError {
	if info == nil || info.ChannelMeta == nil || info.ChannelIsMultiKey || !model.CNDurableTaskAvailable() || model.CNTaskProvider(string(platform)) == "" || info.AgencyPricing != nil || model.IsAgencyDurableUser(info.UserId) || info.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(model.ErrCNConflict, types.ErrorCodeQueryDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	seed := model.InitTask(platform, info)
	seed.PrivateData.Key = info.ApiKey
	seed.PrivateData.Endpoint = &model.TaskEndpointSnapshot{BaseURL: info.ChannelBaseUrl}
	seed.PrivateData.BillingContext = &model.TaskBillingContext{ModelPrice: info.PriceData.ModelPrice, GroupRatio: info.PriceData.GroupRatioInfo.GroupRatio, ModelRatio: info.PriceData.ModelRatio, OtherRatios: info.PriceData.OtherRatios(), OriginModelName: info.OriginModelName, PerCallBilling: common.StringsContains(constant.TaskPricePatches, info.OriginModelName) || info.PriceData.UsePrice, TieredSnapshot: info.TieredBillingSnapshot, ProviderBilling: provider}
	try := func(source string) (*model.CNTaskBilling, error) {
		amount := quota
		if source == BillingSourceSubscription {
			amount = max(1, amount)
		}
		return model.PrepareCNTask(seed, info.RequestId, source, amount, info.TokenId, info.TokenUnlimited, info.IsPlayground)
	}
	insufficient := func(err error) bool {
		return errors.Is(err, model.ErrInsufficientAgencyWalletQuota) || (err != nil && (strings.Contains(err.Error(), "subscription quota insufficient") || strings.Contains(err.Error(), "no active subscription")))
	}
	pref := common.NormalizeBillingPreference(info.UserSetting.BillingPreference)
	var row *model.CNTaskBilling
	var err error
	switch pref {
	case "wallet_only":
		row, err = try(BillingSourceWallet)
	case "subscription_only":
		row, err = try(BillingSourceSubscription)
	case "wallet_first":
		row, err = try(BillingSourceWallet)
		if insufficient(err) {
			row, err = try(BillingSourceSubscription)
		}
	default:
		var has bool
		has, err = model.HasActiveUserSubscription(info.UserId)
		if err == nil && has {
			row, err = try(BillingSourceSubscription)
			if insufficient(err) {
				allow, checkErr := model.UserActiveSubscriptionsAllowWalletOverflow(info.UserId)
				if checkErr != nil {
					err = checkErr
				} else if allow {
					row, err = try(BillingSourceWallet)
				}
			}
		} else if err == nil {
			row, err = try(BillingSourceWallet)
		}
	}
	if err != nil {
		code, status := types.ErrorCodeQueryDataError, http.StatusServiceUnavailable
		if insufficient(err) {
			code, status = types.ErrorCodeInsufficientUserQuota, http.StatusForbidden
		} else if errors.Is(err, model.ErrInsufficientAgencyTokenQuota) {
			code, status = types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden
		}
		return types.NewErrorWithStatusCode(err, code, status, types.ErrOptionWithSkipRetry())
	}
	info.Billing = &CNTaskSettler{key: row.Key, reserved: row.HeldQuota}
	info.BillingSource, info.SubscriptionId, info.FinalPreConsumedQuota = row.Source, row.SubscriptionID, row.HeldQuota
	if row.Source == BillingSourceSubscription {
		info.SubscriptionPreConsumed = int64(row.HeldQuota)
		var sub model.UserSubscription
		if err := model.DB.First(&sub, row.SubscriptionID).Error; err == nil {
			info.SubscriptionAmountTotal, info.SubscriptionAmountUsedAfterPreConsume = sub.AmountTotal, sub.AmountUsed
		}
		if plan, err := model.GetSubscriptionPlanInfoByUserSubscriptionId(row.SubscriptionID); err == nil && plan != nil {
			info.SubscriptionPlanId, info.SubscriptionPlanTitle = plan.PlanId, plan.PlanTitle
		}
	}
	return nil
}

func (s *CNTaskSettler) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := model.ReserveCNTask(s.key, target); err != nil {
		return err
	}
	s.reserved = max(s.reserved, target)
	return nil
}

func (s *CNTaskSettler) Settle(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var row model.CNTaskBilling
	if err := model.DB.Where(map[string]any{"key": s.key}).First(&row).Error; err != nil {
		return err
	}
	if (row.State != "active" && row.State != "final") || row.SubmitQuota != target {
		return model.ErrCNConflict
	}
	s.settled = true
	return nil
}

func (s *CNTaskSettler) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return
	}
	if err := model.RequestCNTaskCancellation(s.key); err != nil {
		logger.LogWarn(c, "CN task funds retained for provider acceptance reconciliation")
		return
	}
	if err := model.CompleteCNTaskIntent(context.Background(), model.DB, s.key); err != nil {
		logger.LogWarn(c, "CN task cancellation pending durable recovery")
	}
}

func (s *CNTaskSettler) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.settled
}

func (s *CNTaskSettler) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

// Compute before terminal status CAS; the resulting target (or explicit error)
// is persisted by the model with that CAS, independently of another provider poll.
func PrepareCNTaskTerminal(adaptor TaskPollingAdaptor, task *model.Task, result *relaycommon.TaskInfo) {
	if task == nil || task.CNBillingKey == "" || (task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure) {
		return
	}
	target, failure := task.Quota, ""
	bc := task.PrivateData.BillingContext
	switch {
	case task.Status == model.TaskStatusFailure:
		target = 0
	case bc != nil && bc.ProviderBilling != nil && bc.ProviderBilling.AsyncReconciliationRequired:
		failure = "provider_bill_pending"
	case bc == nil:
		failure = "task_original_pricing_missing"
	case result == nil:
		failure = "task_completion_usage_missing"
	case bc.TieredSnapshot != nil:
		settled, _, err := EvaluateTaskCompletionUsage(bc.TieredSnapshot, result.UsageFacts)
		if err != nil {
			failure = "task_completion_expression_failed"
		} else if settled.Clamp != nil {
			failure = "task_completion_quota_clamped"
		} else {
			target = settled.ActualQuotaAfterGroup
		}
	case bc.PerCallBilling:
	default:
		handled := false
		if checked, ok := adaptor.(TaskPollingCheckedBillingAdaptor); ok {
			quota, clamp, yes, err := checked.AdjustBillingOnCompleteChecked(task, result)
			handled = yes
			if err != nil || clamp != nil {
				failure, handled = "task_provider_pricing_failed", true
			} else if yes {
				target = quota
			}
		}
		if !handled && adaptor != nil {
			if quota := adaptor.AdjustBillingOnComplete(task, result); quota > 0 {
				target, handled = quota, true
			}
		}
		if !handled {
			tokens := result.TotalTokens
			if tokens == 0 {
				tokens = result.CompletionTokens
			}
			if tokens > 0 && bc.ModelRatio > 0 {
				other := 1.0
				if pd := taskBillingContextPriceData(bc); pd != nil {
					other = pd.OtherRatioMultiplier()
				}
				var clamp *common.QuotaClamp
				target, clamp = common.QuotaFromFloatChecked(float64(tokens) * bc.ModelRatio * bc.GroupRatio * other)
				if clamp != nil {
					failure = "task_completion_quota_clamped"
				}
			}
		}
	}
	task.PrepareCNFinancialTerminal(&target, failure)
}

// A provider can only be probed by identities it actually exposes. Every
// registered task adaptor supports FetchTask with an upstream ID; there is no
// assumed lookup-by-request capability when the submit response itself was lost.
func ProbeCNTaskSubmission(ctx context.Context, key string) error {
	var row model.CNTaskBilling
	if err := model.DB.WithContext(ctx).Where(map[string]any{"key": key}).First(&row).Error; err != nil {
		return err
	}
	if row.TaskID != 0 || (row.State != "accepted" && row.State != "unknown") || row.Pending {
		return model.ErrCNConflict
	}
	if row.UpstreamID == "" {
		return fmt.Errorf("provider_lookup_requires_upstream_identity")
	}
	if GetTaskAdaptorFunc == nil {
		return fmt.Errorf("task adaptor unavailable")
	}
	adaptor := GetTaskAdaptorFunc(constant.TaskPlatform(row.Platform))
	if adaptor == nil {
		return fmt.Errorf("task adaptor unavailable")
	}
	var task model.Task
	if err := common.UnmarshalJsonStr(row.Seed, &task); err != nil {
		return err
	}
	if err := common.UnmarshalJsonStr(row.SeedPrivate, &task.PrivateData); err != nil {
		return err
	}
	task.CNBillingKey, task.Quota = key, row.HeldQuota
	task.PrivateData.UpstreamTaskID = row.UpstreamID
	channel, err := model.GetChannelById(row.ChannelID, true)
	if err != nil {
		return err
	}
	baseURL := channel.GetBaseURL()
	if task.PrivateData.Endpoint != nil && task.PrivateData.Endpoint.BaseURL != "" {
		baseURL = task.PrivateData.Endpoint.BaseURL
	}
	if err := model.ValidateCNTaskCredential(ctx, key, channel.Id, channel.Key); err != nil {
		return err
	}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ChannelBaseUrl: baseURL, ApiKey: channel.Key}})
	resp, err := adaptor.FetchTask(baseURL, channel.Key, &task, channel.GetSetting().Proxy)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("provider query returned no response")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("provider lookup status %d requires repair", resp.StatusCode)
	}
	result, err := adaptor.ParseTaskResult(&task, resp, body)
	if err != nil || result == nil || !knownPollStatus(model.TaskStatus(result.Status)) {
		return fmt.Errorf("provider lookup result requires repair")
	}
	// Reconstitute the accepted task at the actual held amount. Completion is
	// priced next against the frozen request basis, never today's configuration.
	task.Status, task.Progress = model.TaskStatusSubmitted, "0%"
	task.Data = redactVideoResponseBody(body)
	if err := task.InsertWithContext(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	task.Status, task.Progress, task.FailReason = model.TaskStatus(result.Status), result.Progress, result.Reason
	PrepareCNTaskTerminal(adaptor, &task, result)
	if _, err := task.UpdateWithStatus(model.TaskStatusSubmitted); err != nil {
		return err
	}
	return model.CompleteCNTaskIntent(context.WithoutCancel(ctx), model.DB, key)
}

func RunCNTaskSubmissionProbes(ctx context.Context, limit int) error {
	keys, err := model.ClaimCNTaskSubmissionProbes(ctx, limit)
	if err != nil {
		return err
	}
	var failures []error
	for _, key := range keys {
		if err := ProbeCNTaskSubmission(ctx, key); err != nil {
			failures = append(failures, err, model.MarkCNTaskRepair(context.WithoutCancel(ctx), key, "provider_submission_probe_failed"))
		}
	}
	return errors.Join(failures...)
}
