package agencyhub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
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
	type catalogRow struct {
		OriginModelName string   `json:"origin_model_name"`
		ChannelNames    []string `json:"channel_names"`
		PlatformCostBPS *int     `json:"platform_cost_bps"`
		AgencyCostBPS   *int     `json:"agency_cost_bps"`
		DefaultSalesBPS *int     `json:"default_sales_bps"`
	}
	byModel := make(map[string]*catalogRow)
	order := make([]string, 0)
	for _, item := range live {
		row := byModel[item.Model]
		if row == nil {
			row = &catalogRow{OriginModelName: item.Model}
			if price, ok := configured[item.Model]; ok {
				platformCost, agencyCost, defaultSales := price.PlatformCostBPS, price.AgencyCostBPS, price.DefaultSalesBPS
				row.PlatformCostBPS, row.AgencyCostBPS, row.DefaultSalesBPS = &platformCost, &agencyCost, &defaultSales
			}
			byModel[item.Model] = row
			order = append(order, item.Model)
		}
		if len(row.ChannelNames) == 0 || row.ChannelNames[len(row.ChannelNames)-1] != item.ChannelName {
			row.ChannelNames = append(row.ChannelNames, item.ChannelName)
		}
	}
	// Keep a configured row visible even if its channel was disabled after publication.
	for name, price := range configured {
		if byModel[name] != nil {
			continue
		}
		platformCost, agencyCost, defaultSales := price.PlatformCostBPS, price.AgencyCostBPS, price.DefaultSalesBPS
		byModel[name] = &catalogRow{OriginModelName: name, ChannelNames: []string{}, PlatformCostBPS: &platformCost, AgencyCostBPS: &agencyCost, DefaultSalesBPS: &defaultSales}
		order = append(order, name)
	}
	sort.Strings(order)
	items := make([]*catalogRow, 0, len(order))
	for _, name := range order {
		items = append(items, byModel[name])
	}
	respondOK(c, gin.H{"revision": policy.Revision, "items": items, "refreshed_at_ms": time.Now().UnixMilli()})
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
	for _, item := range live {
		liveModels[item.Model] = struct{}{}
	}
	previousModels := make(map[string]struct{}, len(previousPolicy.ModelPrices))
	for _, price := range previousPolicy.ModelPrices {
		previousModels[price.OriginModelName] = struct{}{}
	}
	for _, price := range policy.ModelPrices {
		if _, liveNow := liveModels[price.OriginModelName]; liveNow {
			continue
		}
		if _, configuredBefore := previousModels[price.OriginModelName]; configuredBefore {
			continue
		}
		respondError(c, http.StatusConflict, "model_unavailable", "模型已不在启用渠道中，请刷新后重试："+price.OriginModelName, nil)
		return
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
