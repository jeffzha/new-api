package agencyhub

import (
	"errors"
	"fmt"
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
		modelRow.MinimumCostBPS, _, err = minimumDirectChildCost(parentEffective, policy, catalogRow.OriginModelName)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "invalid_pricing", "读取下级成本底线失败", nil)
			return
		}
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
	parent, parentPolicy, err := a.loadAgencyPolicy(*identity.AgencyID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取上级价格失败", nil)
		return
	}
	parentEffective, err := a.effectiveAgencyPolicy(parent, parentPolicy)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
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
	if request.DefaultSettlementBPS != nil && *request.DefaultSettlementBPS != candidate.DefaultSettlementBPS {
		candidate.DefaultSettlementBPS = *request.DefaultSettlementBPS
		candidate.SettlementSource = agencycontract.PriceSourceManual
		quote, sale := &candidate.DefaultChildCostBPS, &candidate.DefaultSalesBPS
		if err := refreshAutomaticCostDependents(candidate.DefaultSettlementBPS, parentEffective.DefaultSalesBPS, candidate.MinSpreadBPS, candidate.SalesCapBPS, &quote, &candidate.ChildCostSource, &sale, &candidate.SalesSource); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", childPricingErrorMessage(child, err), nil)
			return
		}
		candidate.DefaultChildCostBPS, candidate.DefaultSalesBPS = *quote, *sale
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
		previousCost := old.DefaultSettlementBPS
		if previous := findModelOverride(old, modelName); previous != nil && previous.SettlementBPS != nil {
			previousCost = *previous.SettlementBPS
		}
		if position, exists := positions[key]; exists {
			current := &candidate.ModelOverrides[position]
			updateManualCoefficient(&current.SettlementBPS, &current.SettlementSource, requested.SettlementBPS)
			if requested.ChildCostBPS != nil {
				updateManualCoefficient(&current.ChildCostBPS, &current.ChildCostSource, requested.ChildCostBPS)
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
			newCost := candidate.DefaultSettlementBPS
			if override.SettlementBPS != nil {
				newCost = *override.SettlementBPS
			}
			if newCost != previousCost {
				parentPrice, resolveErr := agencycontract.Resolve(parentEffective, modelName)
				if resolveErr == nil {
					resolveErr = refreshAutomaticCostDependents(newCost, parentPrice.SalesBPS, candidate.MinSpreadBPS, candidate.SalesCapBPS, &override.ChildCostBPS, &override.ChildCostSource, &override.SalesBPS, &override.SalesSource)
				}
				if resolveErr != nil {
					respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", childPricingErrorMessage(child, resolveErr), nil)
					return
				}
			}
		}
	}
	if err := agencycontract.ValidatePolicy(candidate); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", childPricingErrorMessage(child, err), nil)
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

// Recalculate dependent defaults only for a cost that was actually changed.
func refreshAutomaticCostDependents(cost, parentSales, spread, cap int, quote **int, quoteSource *string, sale **int, saleSource *string) error {
	if cap == 0 {
		cap = agencycontract.DefaultSalesCapBPS
	}
	if *quote == nil || **quote == 0 || *quoteSource == agencycontract.PriceSourceDefault || *quoteSource == agencycontract.PriceSourceInherited {
		value := cost + agencycontract.DefaultChildMarkupBPS
		*quote, *quoteSource = &value, agencycontract.PriceSourceDefault
	}
	if *sale == nil || *saleSource == agencycontract.PriceSourceDefault || *saleSource == agencycontract.PriceSourceInherited {
		value, err := inheritedSalesBPS(parentSales, cost, spread, cap)
		if err != nil {
			return err
		}
		*sale, *saleSource = &value, agencycontract.PriceSourceDefault
	}
	return nil
}

func childPricingErrorMessage(child model.Agency, err error) string {
	prefix := fmt.Sprintf("下级代理商“%s”（ID %d）：", child.DisplayName, child.ID)
	var coefficient *agencycontract.PolicyCoefficientError
	if !errors.As(err, &coefficient) {
		return prefix + pricingErrorMessage(err)
	}
	field := map[string]string{"child_cost_bps": "我的下级渠道价（系数）", "sales_bps": "销售价格（系数）", "settlement_bps": "我的成本（系数）", "default_child_cost_bps": "默认我的下级渠道价（系数）", "default_sales_bps": "默认销售价格（系数）", "default_settlement_bps": "默认我的成本（系数）"}[coefficient.Field]
	if field == "" || !coefficient.BelowCost {
		return prefix + pricingErrorMessage(err)
	}
	name := coefficient.OriginModelName
	if name == "" {
		name = "全局默认价格"
	}
	return prefix + fmt.Sprintf("模型 %s：本次成本调整后，“我的成本（系数）”为 %.4f；该代理商的“%s”为 %.4f，低于允许下限 %.4f = 成本 %.4f + 适用最低价差 %.4f。这里的 %.4f 是冲突字段的值，不是本次输入的成本。请到该代理商“价格策略 → %s”调整至至少 %.4f，或降低本次成本；手动价格不会自动覆盖，默认加点 0.0500 不是强制利润。本次未保存。", name, float64(coefficient.SettlementBPS)/10000, field, float64(coefficient.ValueBPS)/10000, float64(coefficient.MinimumBPS)/10000, float64(coefficient.SettlementBPS)/10000, float64(coefficient.MinSpreadBPS)/10000, float64(coefficient.ValueBPS)/10000, field, float64(coefficient.MinimumBPS)/10000)
}
