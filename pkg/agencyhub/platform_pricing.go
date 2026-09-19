package agencyhub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type platformPricingPublishRequest struct {
	ExpectedRevision int64                               `json:"expected_revision"`
	ModelPrices      []agencycontract.PlatformModelPrice `json:"model_prices"`
	Reason           string                              `json:"reason"`
}

type liveModelChannel struct {
	Model       string
	ChannelID   int
	ChannelName string
}

type catalogChannelCost struct {
	ChannelID       int    `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	Available       bool   `json:"available"`
	PlatformCostBPS *int   `json:"platform_cost_bps"`
}

type platformPricingCatalogRow struct {
	OriginModelName string               `json:"origin_model_name"`
	ChannelNames    []string             `json:"channel_names"`
	ChannelCosts    []catalogChannelCost `json:"channel_costs"`
	// PlatformCostBPS is a read-only legacy fallback for a policy published
	// before per-channel platform costs were introduced.
	PlatformCostBPS *int `json:"platform_cost_bps"`
	AgencyCostBPS   *int `json:"agency_cost_bps"`
	DefaultSalesBPS *int `json:"default_sales_bps"`
}

func (a *App) liveModelChannels() ([]liveModelChannel, error) {
	var rows []liveModelChannel
	err := a.db.Table("abilities").
		Select("abilities.model, channels.id AS channel_id, channels.name AS channel_name").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.enabled = ? AND channels.status = ?", true, common.ChannelStatusEnabled).
		Order("abilities.model ASC, channels.name ASC, channels.id ASC").
		Scan(&rows).Error
	return rows, err
}

func (a *App) getPlatformPricing(c *gin.Context) {
	policy, err := model.LoadAgencyPlatformPolicy(a.db)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取平台价格策略失败", nil)
		return
	}
	live, err := a.liveModelChannels()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取实时模型与渠道失败", nil)
		return
	}
	configured := make(map[string]agencycontract.PlatformModelPrice, len(policy.ModelPrices))
	for _, price := range policy.ModelPrices {
		configured[price.OriginModelName] = price
	}
	byModel := make(map[string]*platformPricingCatalogRow)
	order := make([]string, 0)
	for _, item := range live {
		row := byModel[item.Model]
		if row == nil {
			row = &platformPricingCatalogRow{OriginModelName: item.Model}
			if price, ok := configured[item.Model]; ok {
				agencyCost, defaultSales := price.AgencyCostBPS, price.DefaultSalesBPS
				row.AgencyCostBPS, row.DefaultSalesBPS = &agencyCost, &defaultSales
				if len(price.ChannelCosts) == 0 {
					legacyCost := price.PlatformCostBPS
					row.PlatformCostBPS = &legacyCost
				}
			}
			byModel[item.Model] = row
			order = append(order, item.Model)
		}
		if containsCatalogChannel(row.ChannelCosts, item.ChannelID) {
			continue
		}
		var channelCost *int
		if price, configuredNow := configured[item.Model]; configuredNow {
			channelCost = configuredChannelCost(price, item.ChannelID)
		}
		row.ChannelNames = append(row.ChannelNames, item.ChannelName)
		row.ChannelCosts = append(row.ChannelCosts, catalogChannelCost{ChannelID: item.ChannelID, ChannelName: item.ChannelName, Available: true, PlatformCostBPS: channelCost})
	}
	// Keep a configured row visible even if its channel was disabled after publication.
	for name, price := range configured {
		row := byModel[name]
		if row == nil {
			agencyCost, defaultSales := price.AgencyCostBPS, price.DefaultSalesBPS
			row = &platformPricingCatalogRow{OriginModelName: name, ChannelNames: []string{}, AgencyCostBPS: &agencyCost, DefaultSalesBPS: &defaultSales}
			if len(price.ChannelCosts) == 0 {
				legacyCost := price.PlatformCostBPS
				row.PlatformCostBPS = &legacyCost
			}
			byModel[name] = row
			order = append(order, name)
		}
		for _, cost := range price.ChannelCosts {
			if containsCatalogChannel(row.ChannelCosts, cost.ChannelID) {
				continue
			}
			costBPS := cost.PlatformCostBPS
			row.ChannelCosts = append(row.ChannelCosts, catalogChannelCost{ChannelID: cost.ChannelID, ChannelName: "渠道当前不可用 #" + strconv.Itoa(cost.ChannelID), Available: false, PlatformCostBPS: &costBPS})
		}
	}
	sort.Strings(order)
	items := make([]*platformPricingCatalogRow, 0, len(order))
	for _, name := range order {
		items = append(items, byModel[name])
	}
	respondOK(c, gin.H{"revision": policy.Revision, "items": items, "refreshed_at_ms": time.Now().UnixMilli()})
}

func containsCatalogChannel(costs []catalogChannelCost, channelID int) bool {
	for _, cost := range costs {
		if cost.ChannelID == channelID {
			return true
		}
	}
	return false
}

func configuredChannelCost(price agencycontract.PlatformModelPrice, channelID int) *int {
	if len(price.ChannelCosts) == 0 {
		cost := price.PlatformCostBPS
		return &cost
	}
	for _, cost := range price.ChannelCosts {
		if cost.ChannelID == channelID {
			configured := cost.PlatformCostBPS
			return &configured
		}
	}
	return nil
}

func (a *App) publishPlatformPricing(c *gin.Context) {
	var request platformPricingPublishRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		respondError(c, http.StatusUnprocessableEntity, "reason_required", "请填写本次价格调整原因", nil)
		return
	}
	policy := agencycontract.PlatformPolicy{ModelPrices: request.ModelPrices}
	if err := agencycontract.ValidatePlatformPolicy(policy); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
	previousPolicy, err := model.LoadAgencyPlatformPolicy(a.db)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取当前平台价格策略失败", nil)
		return
	}
	live, err := a.liveModelChannels()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取实时模型与渠道失败", nil)
		return
	}
	liveModels := make(map[string]struct{}, len(live))
	liveChannels := make(map[string]map[int]struct{}, len(live))
	for _, item := range live {
		liveModels[item.Model] = struct{}{}
		if liveChannels[item.Model] == nil {
			liveChannels[item.Model] = make(map[int]struct{})
		}
		liveChannels[item.Model][item.ChannelID] = struct{}{}
	}
	previousModels := make(map[string]struct{}, len(previousPolicy.ModelPrices))
	previousChannelCosts := make(map[string]map[int]struct{}, len(previousPolicy.ModelPrices))
	for _, price := range previousPolicy.ModelPrices {
		previousModels[price.OriginModelName] = struct{}{}
		for _, cost := range price.ChannelCosts {
			if previousChannelCosts[price.OriginModelName] == nil {
				previousChannelCosts[price.OriginModelName] = make(map[int]struct{})
			}
			previousChannelCosts[price.OriginModelName][cost.ChannelID] = struct{}{}
		}
	}
	for _, price := range policy.ModelPrices {
		_, liveNow := liveModels[price.OriginModelName]
		_, configuredBefore := previousModels[price.OriginModelName]
		if !liveNow && !configuredBefore {
			respondError(c, http.StatusConflict, "model_unavailable", "模型已不在启用渠道中，请刷新后重试："+price.OriginModelName, nil)
			return
		}
		if len(price.ChannelCosts) == 0 {
			continue
		}
		for _, cost := range price.ChannelCosts {
			_, liveNow := liveChannels[price.OriginModelName][cost.ChannelID]
			_, configuredBefore := previousChannelCosts[price.OriginModelName][cost.ChannelID]
			if liveNow || configuredBefore {
				continue
			}
			respondError(c, http.StatusConflict, "channel_unavailable", "渠道已不属于该模型或当前不可用，请刷新后重试。", nil)
			return
		}
		if active := liveChannels[price.OriginModelName]; len(active) > 0 {
			configured := make(map[int]struct{}, len(price.ChannelCosts))
			for _, cost := range price.ChannelCosts {
				configured[cost.ChannelID] = struct{}{}
			}
			for channelID := range active {
				if _, ok := configured[channelID]; !ok {
					respondError(c, http.StatusUnprocessableEntity, "channel_cost_required", "已配置的模型必须为每个启用渠道填写平台成本系数。", nil)
					return
				}
			}
		}
	}
	var activePolicies []struct {
		AgencyID    int64
		DisplayName string
		PolicyJSON  string
	}
	if err = a.db.Table((model.Agency{}).TableName() + " AS agency").
		Select("agency.id AS agency_id, agency.display_name, version.policy_json").
		Joins("JOIN " + (model.AgencyPricePolicyVersion{}).TableName() + " AS version ON version.id = agency.current_policy_version_id AND version.agency_id = agency.id").
		Scan(&activePolicies).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "校验代理商价格策略失败", nil)
		return
	}
	conflicts := make([]string, 0)
	for _, row := range activePolicies {
		var agencyPolicy agencycontract.Policy
		if decodeErr := common.Unmarshal([]byte(row.PolicyJSON), &agencyPolicy); decodeErr != nil {
			respondError(c, http.StatusConflict, "invalid_agency_pricing", "代理商价格策略数据异常："+row.DisplayName, nil)
			return
		}
		if _, applyErr := agencycontract.ApplyPlatformPolicy(agencyPolicy, policy); applyErr != nil {
			conflicts = append(conflicts, row.DisplayName)
		}
	}
	if len(conflicts) > 0 {
		respondError(c, http.StatusConflict, "agency_sales_below_cost", "以下代理商的销售系数低于新的代理商成本，请先调整："+strings.Join(conflicts, "、"), nil)
		return
	}
	identity := currentIdentity(c)
	now := time.Now().UnixMilli()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var state model.AgencyPlatformPriceState
		findErr := tx.First(&state, 1).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			if request.ExpectedRevision != 0 {
				return agencycontract.ErrPolicyRevision
			}
			state = model.AgencyPlatformPriceState{ID: 1}
		} else if findErr != nil {
			return findErr
		} else if state.Revision != request.ExpectedRevision {
			return agencycontract.ErrPolicyRevision
		}
		policy.Revision = request.ExpectedRevision + 1
		encoded, marshalErr := common.Marshal(policy)
		if marshalErr != nil {
			return marshalErr
		}
		digest := sha256.Sum256(encoded)
		version := model.AgencyPlatformPriceVersion{Revision: policy.Revision, PolicyJSON: string(encoded), PolicyHash: hex.EncodeToString(digest[:]), CreatedByID: identity.ActorID, Reason: request.Reason, CreatedAtMS: now}
		if createErr := tx.Create(&version).Error; createErr != nil {
			return createErr
		}
		if state.Revision == 0 {
			state.Revision, state.CurrentVersionID, state.UpdatedAtMS = policy.Revision, version.ID, now
			if createErr := tx.Create(&state).Error; createErr != nil {
				return createErr
			}
		} else {
			result := tx.Model(&state).Where("id = ? AND revision = ?", state.ID, request.ExpectedRevision).Updates(map[string]any{"revision": policy.Revision, "current_version_id": version.ID, "updated_at_ms": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return agencycontract.ErrPolicyRevision
			}
		}
		return recordAuditTx(tx, c, identity, "pricing.platform.publish", "platform_pricing", "current", request.Reason, gin.H{"revision": request.ExpectedRevision}, gin.H{"revision": policy.Revision, "model_count": len(policy.ModelPrices)})
	})
	if err != nil {
		if errors.Is(err, agencycontract.ErrPolicyRevision) {
			respondError(c, http.StatusConflict, "price_revision_conflict", "平台价格策略已更新，请刷新后重试", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, "publish_failed", "发布平台价格策略失败", nil)
		return
	}
	respondOK(c, gin.H{"revision": policy.Revision, "committed_at_ms": now})
}
