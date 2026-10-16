package agencyhub

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
)

type childPricingRequest struct {
	ExpectedRevision     int64 `json:"expected_revision"`
	DefaultSettlementBPS *int  `json:"default_settlement_bps"`
	ModelCostOverrides   []struct {
		OriginModelName string `json:"origin_model_name"`
		SettlementBPS   *int   `json:"settlement_bps"`
		ChildCostBPS    *int   `json:"child_cost_bps"`
	} `json:"model_cost_overrides"`
	Reason string `json:"reason"`
}

type childPricingModel struct {
	OriginModelName string `json:"origin_model_name"`
	AgencyCostBPS   int    `json:"agency_cost_bps"`
	MinimumCostBPS  int    `json:"minimum_cost_bps"`
	SettlementBPS   *int   `json:"settlement_bps"`
	ChildCostBPS    *int   `json:"child_cost_bps"`
	VendorName      string `json:"vendor_name,omitempty"`
	VendorIcon      string `json:"vendor_icon,omitempty"`
}

func (a *App) directChild(identity *Identity, childID int64) (model.Agency, agencycontract.Policy, error) {
	if identity == nil || identity.AgencyID == nil || (identity.ActorType != ActorTypeOperator && identity.ActorType != ActorTypeRoot) {
		return model.Agency{}, agencycontract.Policy{}, errors.New("agency operator required")
	}
	var child model.Agency
	if err := a.db.Where("id = ? AND parent_agency_id = ?", childID, *identity.AgencyID).First(&child).Error; err != nil {
		return model.Agency{}, agencycontract.Policy{}, err
	}
	_, policy, err := a.loadAgencyPolicy(childID)
	if err != nil {
		return model.Agency{}, agencycontract.Policy{}, err
	}
	return child, policy, nil
}

func (a *App) getChildPricing(c *gin.Context) {
	childID, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商", nil)
		return
	}
	identity := currentIdentity(c)
	child, policy, err := a.directChild(identity, childID)
	if err != nil {
		respondError(c, http.StatusNotFound, "not_found", "下级代理商或价格策略不存在", nil)
		return
	}
	view := a.policyView(child, policy)
	parent, parentPolicy, err := a.loadAgencyPolicy(*identity.AgencyID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取当前代理商价格失败", nil)
		return
	}
	parentEffective, err := a.effectiveAgencyPolicy(parent, parentPolicy)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "invalid_pricing", "当前代理商价格策略无效", nil)
		return
	}
	// Keep child-cost editing on the same live model catalog as platform pricing.
	// Channel identity is intentionally not exposed to agency operators.
	catalog, catalogErr := LoadPlatformPricingCatalog(a.db)
	if catalogErr != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取实时模型与渠道失败", nil)
		return
	}
	models := make([]childPricingModel, 0, len(catalog.Items)+len(policy.ModelOverrides))
	for _, catalogRow := range catalog.Items {
		if (catalogRow.PricingConfirmed != nil && !*catalogRow.PricingConfirmed) || (catalogRow.AgencyCostConfirmed != nil && !*catalogRow.AgencyCostConfirmed) {
			continue
		}
		resolvedParent, resolveErr := agencycontract.Resolve(parentEffective, catalogRow.OriginModelName)
		if resolveErr != nil {
			respondError(c, http.StatusInternalServerError, "invalid_pricing", "当前代理商价格策略无效", nil)
			return
		}
		modelRow := childPricingModel{OriginModelName: catalogRow.OriginModelName, AgencyCostBPS: resolvedParent.SettlementBPS, VendorName: catalogRow.VendorName, VendorIcon: catalogRow.VendorIcon}
		modelRow.MinimumCostBPS, _ = directChildCost(parentEffective, catalogRow.OriginModelName)
		for _, override := range policy.ModelOverrides {
			if override.OriginModelName == catalogRow.OriginModelName {
				modelRow.SettlementBPS = override.SettlementBPS
				modelRow.ChildCostBPS = override.ChildCostBPS
				break
			}
		}
		models = append(models, modelRow)
	}
	view["model_overrides"] = models
	view["catalog_revision"] = catalog.Revision
	view["parent_agency_id"] = child.ParentAgencyID
	view["can_edit_cost"] = child.Status == AgencyStatusActive
	respondOK(c, view)
}

func (a *App) publishChildPricing(c *gin.Context) {
	childID, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商", nil)
		return
	}
	identity := currentIdentity(c)
	child, old, err := a.directChild(identity, childID)
	if err != nil {
		respondError(c, http.StatusNotFound, "not_found", "下级代理商或价格策略不存在", nil)
		return
	}
	if child.Status != AgencyStatusActive {
		respondError(c, http.StatusConflict, "agency_disabled", "下级代理商已停用", nil)
		return
	}
	var request childPricingRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if request.ExpectedRevision <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_revision_required", "必须提供当前价格版本", nil)
		return
	}
	if request.ExpectedRevision != old.Revision {
		respondError(c, http.StatusConflict, "price_revision_conflict", "价格版本已变化，请刷新后重试", nil)
		return
	}
	platform, err := model.LoadAgencyPlatformPolicy(a.db)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取平台价格策略失败", nil)
		return
	}

	candidate := old
	candidate.ModelOverrides = append([]agencycontract.ModelOverride(nil), old.ModelOverrides...)
	positions := make(map[string]int, len(candidate.ModelOverrides))
	for i, override := range candidate.ModelOverrides {
		key, keyErr := agencycontract.ModelKey(override.OriginModelName)
		if keyErr != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(keyErr), nil)
			return
		}
		positions[key] = i
	}
	if request.DefaultSettlementBPS != nil {
		candidate.DefaultSettlementBPS = *request.DefaultSettlementBPS
		candidate.SettlementSource = agencycontract.PriceSourceManual
	}
	for _, requested := range request.ModelCostOverrides {
		modelName := strings.TrimSpace(requested.OriginModelName)
		if !agencycontract.ModelPricingConfirmed(platform, modelName) {
			respondError(c, http.StatusUnprocessableEntity, "pricing_pending", "模型 "+modelName+"：平台成本和下级系数尚未确认，暂不可设置代理商价格", nil)
			return
		}
		key, keyErr := agencycontract.ModelKey(modelName)
		if keyErr != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(keyErr), nil)
			return
		}
		if position, exists := positions[key]; exists {
			candidate.ModelOverrides[position].SettlementBPS = requested.SettlementBPS
			if requested.ChildCostBPS != nil {
				candidate.ModelOverrides[position].ChildCostBPS = requested.ChildCostBPS
			}
			if requested.SettlementBPS != nil {
				candidate.ModelOverrides[position].SettlementSource = agencycontract.PriceSourceManual
			} else {
				candidate.ModelOverrides[position].SettlementSource = ""
			}
			if requested.ChildCostBPS != nil {
				candidate.ModelOverrides[position].ChildCostSource = agencycontract.PriceSourceManual
			}
		} else if requested.SettlementBPS != nil || requested.ChildCostBPS != nil {
			positions[key] = len(candidate.ModelOverrides)
			override := agencycontract.ModelOverride{OriginModelName: modelName, SettlementBPS: requested.SettlementBPS, ChildCostBPS: requested.ChildCostBPS, SettlementSource: agencycontract.PriceSourceManual}
			if requested.ChildCostBPS != nil {
				override.ChildCostSource = agencycontract.PriceSourceManual
			}
			candidate.ModelOverrides = append(candidate.ModelOverrides, override)
		}
		if position, exists := positions[key]; exists {
			override := &candidate.ModelOverrides[position]
			if override.ChildCostBPS == nil && override.SettlementBPS != nil {
				childCost := *override.SettlementBPS + agencycontract.DefaultChildMarkupBPS
				override.ChildCostBPS = &childCost
				override.ChildCostSource = agencycontract.PriceSourceDefault
			}
		}
	}
	if err := agencycontract.ValidatePolicy(candidate); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
	if child.ParentAgencyID == nil {
		respondError(c, http.StatusConflict, "invalid_parent", "下级代理商缺少上级关系", nil)
		return
	}
	if err := a.validateChildPolicy(*child.ParentAgencyID, candidate); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", "下级代理商“"+child.DisplayName+"”（ID "+stringID(child.ID)+"）："+pricingErrorMessage(err), nil)
		return
	}
	if err := a.publishPolicy(c, child.ID, request.ExpectedRevision, candidate, request.Reason, identity.ActorType, true); err != nil {
		return
	}
}
