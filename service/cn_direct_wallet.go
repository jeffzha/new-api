package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func CNDirectRealtimeRequested(info *relaycommon.RelayInfo) bool {
	if info != nil {
		switch info.Billing.(type) {
		case *cnDirectRealtimeSession, *cnDirectRelaySession:
			return true
		}
	}
	return info != nil && model.CNDirectWalletRequested() && info.AgencyPricing == nil && !model.IsAgencyDurableUser(info.UserId)
}

// Existing paid sessions retain ownership; this forward entry covers requests
// whose free-model quote deliberately skipped BillingSession.
func BeginCNDirectRelay(info *relaycommon.RelayInfo) error {
	if !CNDirectRealtimeRequested(info) {
		return nil
	}
	if info.RelayFormat == types.RelayFormatOpenAIRealtime {
		return BeginCNDirectRealtime(info, 0)
	}
	if !model.CNDirectWalletAvailable() || info.Billing != nil || info.FinalPreConsumedQuota != 0 || info.IsPlayground || (info.RelayFormat != types.RelayFormatOpenAI && info.RelayFormat != types.RelayFormatOpenAIAudio) || common.NormalizeBillingPreference(info.UserSetting.BillingPreference) != "wallet_only" {
		return model.ErrCNConflict
	}
	if err := model.BeginCNDirectRelayWallet(info.RequestId, info.UserId, info.TokenId, info.ChannelId); err != nil {
		return err
	}
	info.BillingSource = BillingSourceWallet
	info.Billing = &cnDirectRelaySession{info: info}
	return nil
}

type cnDirectRelaySession struct {
	info         *relaycommon.RelayInfo
	usage        string
	countRequest bool
}

func (s *cnDirectRelaySession) GetPreConsumedQuota() int { return 0 }
func (s *cnDirectRelaySession) Reserve(target int) error {
	if target == 0 {
		return nil
	}
	return model.ErrCNConflict
}
func (s *cnDirectRelaySession) NeedsRefund() bool {
	row, err := model.ReadCNDirectWallet(s.info.RequestId, s.info.UserId)
	return err == nil && row.State == "reserved"
}
func (s *cnDirectRelaySession) Refund(c *gin.Context) {
	(&cnDirectRealtimeSession{info: s.info}).Refund(c)
}
func (s *cnDirectRelaySession) Settle(actual int) error {
	_, err := postConsumeQuotaWithResult(s.info, actual, 0, false)
	return err
}

func BindCNDirectRelayUsage(info *relaycommon.RelayInfo, usage any, countRequest bool) (bool, error) {
	if info == nil {
		return false, nil
	}
	s, ok := info.Billing.(*cnDirectRelaySession)
	if !ok {
		return false, nil
	}
	if usage == nil {
		return true, model.ErrCNConflict
	}
	raw, err := common.Marshal(usage)
	if err != nil {
		return true, err
	}
	s.usage, s.countRequest = string(raw), countRequest
	return true, nil
}

func cnDirectRealtimeIdentity(info *relaycommon.RelayInfo) error {
	if !model.CNDirectWalletAvailable() || info == nil || info.RelayFormat != types.RelayFormatOpenAIRealtime || info.RequestId == "" || info.TokenId <= 0 || info.ChannelId <= 0 || info.IsPlayground || info.UsePrice || info.PriceData.UsePrice || info.TieredBillingSnapshot != nil || info.BillingSource == BillingSourceSubscription || common.NormalizeBillingPreference(info.UserSetting.BillingPreference) != "wallet_only" {
		return model.ErrCNConflict
	}
	return nil
}

func BeginCNDirectRealtime(info *relaycommon.RelayInfo, initial int) error {
	if err := cnDirectRealtimeIdentity(info); err != nil {
		return err
	}
	if info.Billing != nil {
		if _, ok := info.Billing.(*cnDirectRealtimeSession); !ok {
			return model.ErrCNConflict
		}
	}
	if err := model.BeginCNDirectWallet(info.RequestId, info.UserId, info.TokenId, info.ChannelId, initial); err != nil {
		return err
	}
	info.FinalPreConsumedQuota = initial
	info.BillingSource = BillingSourceWallet
	info.Billing = &cnDirectRealtimeSession{info: info, initial: initial}
	return nil
}

type cnDirectRealtimeSession struct {
	info    *relaycommon.RelayInfo
	initial int
}

func (s *cnDirectRealtimeSession) GetPreConsumedQuota() int { return s.initial }
func (s *cnDirectRealtimeSession) Reserve(target int) error {
	if target == s.initial {
		return nil
	}
	return model.ErrCNConflict
}
func (s *cnDirectRealtimeSession) NeedsRefund() bool {
	row, err := model.ReadCNDirectWallet(s.info.RequestId, s.info.UserId)
	return err == nil && row.State == "reserved"
}
func (s *cnDirectRealtimeSession) Refund(_ *gin.Context) {
	if err := model.RequestCNDirectWalletCancellation(s.info.RequestId, s.info.UserId); err != nil {
		common.SysError("CN direct wallet cancellation decision requires retry")
		return
	}
	if err := model.RecoverCNDirectWalletCancellations(context.Background(), model.DB, 100); err != nil {
		common.SysError("CN direct wallet cancellation pending recovery")
	}
}
func (s *cnDirectRealtimeSession) Settle(actual int) error {
	return model.FinalizeCNDirectWallet(s.info.RequestId, s.info.UserId, actual)
}

func cnDirectRealtimeAPIError(err error) *types.NewAPIError {
	return types.NewErrorWithStatusCode(err, types.ErrorCodeQueryDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

// response.done is a terminal absolute snapshot for one response, not a session
// counter. Different response IDs contribute independently in arrival order.
func cnDirectRealtimeSum(current, previous *dto.RealtimeUsage) (*dto.RealtimeUsage, error) {
	counts := func(u *dto.RealtimeUsage) []int {
		return []int{u.TotalTokens, u.InputTokens, u.OutputTokens, u.InputTokenDetails.CachedTokens, u.InputTokenDetails.CacheWriteTokens, u.InputTokenDetails.CachedCreationTokens, u.InputTokenDetails.TextTokens, u.InputTokenDetails.AudioTokens, u.InputTokenDetails.ImageTokens, u.OutputTokenDetails.TextTokens, u.OutputTokenDetails.AudioTokens, u.OutputTokenDetails.ImageTokens, u.OutputTokenDetails.ReasoningTokens}
	}
	if current == nil {
		return nil, model.ErrCNConflict
	}
	if previous == nil {
		previous = &dto.RealtimeUsage{}
	}
	before := counts(previous)
	for i, n := range counts(current) {
		if n < 0 || n > common.MaxQuota || before[i] < 0 || before[i] > common.MaxQuota-n {
			return nil, model.ErrCNConflict
		}
	}
	return &dto.RealtimeUsage{TotalTokens: current.TotalTokens + previous.TotalTokens, InputTokens: current.InputTokens + previous.InputTokens, OutputTokens: current.OutputTokens + previous.OutputTokens, InputTokenDetails: dto.InputTokenDetails{CachedTokens: current.InputTokenDetails.CachedTokens + previous.InputTokenDetails.CachedTokens, CacheWriteTokens: current.InputTokenDetails.CacheWriteTokens + previous.InputTokenDetails.CacheWriteTokens, CachedCreationTokens: current.InputTokenDetails.CachedCreationTokens + previous.InputTokenDetails.CachedCreationTokens, TextTokens: current.InputTokenDetails.TextTokens + previous.InputTokenDetails.TextTokens, AudioTokens: current.InputTokenDetails.AudioTokens + previous.InputTokenDetails.AudioTokens, ImageTokens: current.InputTokenDetails.ImageTokens + previous.InputTokenDetails.ImageTokens}, OutputTokenDetails: dto.OutputTokenDetails{TextTokens: current.OutputTokenDetails.TextTokens + previous.OutputTokenDetails.TextTokens, AudioTokens: current.OutputTokenDetails.AudioTokens + previous.OutputTokenDetails.AudioTokens, ImageTokens: current.OutputTokenDetails.ImageTokens + previous.OutputTokenDetails.ImageTokens, ReasoningTokens: current.OutputTokenDetails.ReasoningTokens + previous.OutputTokenDetails.ReasoningTokens}}, nil
}

func LoadCNRealtimeUsage(info *relaycommon.RelayInfo) (*dto.RealtimeUsage, error) {
	if err := cnDirectRealtimeIdentity(info); err != nil {
		return nil, err
	}
	row, err := model.ReadCNDirectWallet(info.RequestId, info.UserId)
	if err != nil {
		return nil, err
	}
	if row.TokenID != info.TokenId || row.ChannelID != info.ChannelId || row.UsageContract != "openai-response-done-v1" {
		return nil, model.ErrCNConflict
	}
	var usage dto.RealtimeUsage
	if row.UsageJSON != "" {
		if err = common.UnmarshalJsonStr(row.UsageJSON, &usage); err != nil {
			return nil, err
		}
	}
	info.RealtimePreConsumedQuota = int(row.SegmentQuota)
	return &usage, nil
}

func PreConsumeCNRealtimeResponse(ctx *gin.Context, info *relaycommon.RelayInfo, response string, current *dto.RealtimeUsage) (*dto.RealtimeUsage, error) {
	if err := cnDirectRealtimeIdentity(info); err != nil {
		return nil, err
	}
	raw, err := common.Marshal(current)
	if err != nil || current == nil || strings.TrimSpace(response) == "" || len(response) > 191 {
		return nil, model.ErrCNConflict
	}
	for range 3 {
		row, err := model.ReadCNDirectWallet(info.RequestId, info.UserId)
		if err != nil {
			return nil, err
		}
		if row.TokenID != info.TokenId || row.ChannelID != info.ChannelId || row.UsageContract != "openai-response-done-v1" {
			return nil, model.ErrCNConflict
		}
		if receipt, err := model.ReadCNDirectWalletResponse(info.RequestId, response); err == nil {
			// OpenAI provides no ordered revision number for terminal usage.
			// Conflicting terminal snapshots require reconciliation, never guessing.
			if receipt.ResponseID != response || receipt.UsageJSON != string(raw) {
				return nil, model.ErrCNConflict
			}
			return LoadCNRealtimeUsage(info)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if row.State != "reserved" {
			return nil, model.ErrCNConflict
		}
		var previous dto.RealtimeUsage
		if row.UsageJSON != "" {
			if err = common.UnmarshalJsonStr(row.UsageJSON, &previous); err != nil {
				return nil, err
			}
		}
		total, err := cnDirectRealtimeSum(current, &previous)
		if err != nil {
			return nil, err
		}
		group := info.UsingGroup
		if auto, ok := ctx.Get("auto_group"); ok {
			if selected, ok := auto.(string); ok {
				group = selected
				info.UsingGroup = selected
			}
		}
		groupRatio := ratio_setting.GetGroupRatio(group)
		if special, ok := ratio_setting.GetGroupGroupRatio(info.UserGroup, group); ok {
			groupRatio = special
		}
		modelRatio, _, _ := ratio_setting.GetModelRatio(info.OriginModelName)
		quota, clamp := calculateAudioQuota(QuotaInfo{InputDetails: TokenDetails{TextTokens: current.InputTokenDetails.TextTokens, AudioTokens: current.InputTokenDetails.AudioTokens}, OutputDetails: TokenDetails{TextTokens: current.OutputTokenDetails.TextTokens, AudioTokens: current.OutputTokenDetails.AudioTokens}, ModelName: info.OriginModelName, ModelRatio: modelRatio, GroupRatio: groupRatio})
		if clamp != nil {
			return nil, clamp
		}
		if current.TotalTokens == 0 {
			quota = 0
		}
		totalRaw, err := common.Marshal(total)
		if err != nil {
			return nil, err
		}
		_, err = model.ApplyCNDirectWalletResponse(info.RequestId, info.UserId, row.Version, response, string(raw), string(totalRaw), quota)
		if errors.Is(err, model.ErrCNConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return LoadCNRealtimeUsage(info)
	}
	return nil, model.ErrCNConflict
}

func FinalizeCNDirectRealtime(info *relaycommon.RelayInfo, usage *dto.RealtimeUsage) error {
	if err := cnDirectRealtimeIdentity(info); err != nil {
		return err
	}
	row, err := model.ReadCNDirectWallet(info.RequestId, info.UserId)
	if err != nil {
		return err
	}
	if row.TokenID != info.TokenId || row.ChannelID != info.ChannelId || row.UsageContract != "openai-response-done-v1" {
		return model.ErrCNConflict
	}
	raw, err := common.Marshal(usage)
	if err != nil || usage == nil {
		return model.ErrCNConflict
	}
	if row.UsageJSON != "" && row.UsageJSON != string(raw) {
		return model.ErrCNConflict
	}
	if row.UsageJSON == "" && *usage != (dto.RealtimeUsage{}) {
		return model.ErrCNConflict
	}
	// Each accepted response froze its live quota. Never reprice those receipts
	// using today's model settings or round the sum a second time.
	return model.FinalizeCNDirectWallet(info.RequestId, info.UserId, int(row.SegmentQuota))
}
