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

func (a *App) directChild(identity *Identity, childID int64) (model.Agency, agencycontract.Policy, error) {
	if identity == nil || identity.ActorType != ActorTypeOperator || identity.AgencyID == nil {
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
	child, policy, err := a.directChild(currentIdentity(c), childID)
	if err != nil {
		respondError(c, http.StatusNotFound, "not_found", "下级代理商或价格策略不存在", nil)
		return
	}
	view := a.policyView(child, policy)
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
	}
	for _, requested := range request.ModelCostOverrides {
		modelName := strings.TrimSpace(requested.OriginModelName)
		key, keyErr := agencycontract.ModelKey(modelName)
		if keyErr != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(keyErr), nil)
			return
		}
		if position, exists := positions[key]; exists {
			candidate.ModelOverrides[position].SettlementBPS = requested.SettlementBPS
			candidate.ModelOverrides[position].ChildCostBPS = requested.ChildCostBPS
		} else if requested.SettlementBPS != nil {
			positions[key] = len(candidate.ModelOverrides)
			candidate.ModelOverrides = append(candidate.ModelOverrides, agencycontract.ModelOverride{OriginModelName: modelName, SettlementBPS: requested.SettlementBPS, ChildCostBPS: requested.ChildCostBPS})
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
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
	if err := a.publishPolicy(c, child.ID, request.ExpectedRevision, candidate, request.Reason, ActorTypeOperator, false); err != nil {
		return
	}
}
