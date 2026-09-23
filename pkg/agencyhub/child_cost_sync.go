package agencyhub

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type childPricingPropagationError struct {
	ChildName string
	Cause     error
}

func (e *childPricingPropagationError) Error() string {
	return "下级代理商 " + e.ChildName + " 无法同步成本：" + pricingErrorMessage(e.Cause)
}

func (e *childPricingPropagationError) Unwrap() error { return e.Cause }

// directChildCost resolves the cost a parent charges for one direct child
// model. A zero value means that the parent did not configure that cost.
func directChildCost(policy agencycontract.Policy, modelName string) (int, bool) {
	if override := findModelOverride(policy, modelName); override != nil && override.ChildCostBPS != nil {
		return *override.ChildCostBPS, *override.ChildCostBPS != 0
	}
	return policy.DefaultChildCostBPS, policy.DefaultChildCostBPS != 0
}

// synchronizeChildCostPolicy copies a parent's newly published direct-child
// costs into the child's own settlement costs. It never changes child sales or
// the child's cost for its own descendants.
func synchronizeChildCostPolicy(parent, child agencycontract.Policy) (agencycontract.Policy, bool, error) {
	candidate := child
	candidate.ModelOverrides = append([]agencycontract.ModelOverride(nil), child.ModelOverrides...)
	changed := false
	if parent.DefaultChildCostBPS != 0 && candidate.DefaultSettlementBPS != parent.DefaultChildCostBPS {
		candidate.DefaultSettlementBPS = parent.DefaultChildCostBPS
		changed = true
	}

	positions := make(map[string]int, len(candidate.ModelOverrides))
	for index, override := range candidate.ModelOverrides {
		key, err := agencycontract.ModelKey(override.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, false, err
		}
		positions[key] = index
		afterCost, afterConfigured := directChildCost(parent, override.OriginModelName)
		if !afterConfigured {
			continue
		}
		if override.SettlementBPS != nil && *override.SettlementBPS == afterCost {
			continue
		}
		value := afterCost
		candidate.ModelOverrides[index].SettlementBPS = &value
		changed = true
	}

	for _, override := range parent.ModelOverrides {
		afterCost, afterConfigured := directChildCost(parent, override.OriginModelName)
		if !afterConfigured {
			continue
		}
		key, err := agencycontract.ModelKey(override.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, false, err
		}
		if _, exists := positions[key]; exists {
			continue
		}
		value := afterCost
		candidate.ModelOverrides = append(candidate.ModelOverrides, agencycontract.ModelOverride{OriginModelName: override.OriginModelName, SettlementBPS: &value})
		positions[key] = len(candidate.ModelOverrides) - 1
		changed = true
	}

	if !changed {
		return child, false, nil
	}
	if err := agencycontract.ValidatePolicy(candidate); err != nil {
		return agencycontract.Policy{}, false, err
	}
	return candidate, true, nil
}

func (a *App) syncDirectChildPoliciesTx(tx *gorm.DB, c *gin.Context, identity *Identity, parent model.Agency, after agencycontract.Policy, reason, actorType string) error {
	var children []model.Agency
	if err := model.AgencyLockForUpdate(tx).Where("parent_agency_id = ?", parent.ID).Order("id ASC").Find(&children).Error; err != nil {
		return err
	}
	for _, child := range children {
		var current model.AgencyPricePolicyVersion
		if err := tx.Where("id = ? AND agency_id = ?", child.CurrentPolicyVersionID, child.ID).First(&current).Error; err != nil {
			return err
		}
		var childPolicy agencycontract.Policy
		if err := common.Unmarshal([]byte(current.PolicyJSON), &childPolicy); err != nil {
			return &childPricingPropagationError{ChildName: child.DisplayName, Cause: errors.New("代理商价格策略数据异常")}
		}
		candidate, changed, err := synchronizeChildCostPolicy(after, childPolicy)
		if err != nil {
			return &childPricingPropagationError{ChildName: child.DisplayName, Cause: err}
		}
		if !changed {
			continue
		}
		if child.Status != AgencyStatusActive {
			return &childPricingPropagationError{ChildName: child.DisplayName, Cause: errors.New("代理商已停用，请先启用后再同步成本")}
		}
		if err := validateCustomerSalesPolicyTx(tx, child.ID, candidate); err != nil {
			return &childPricingPropagationError{ChildName: child.DisplayName, Cause: err}
		}
		candidate.Revision = child.PriceRevision + 1
		encoded, err := common.Marshal(candidate)
		if err != nil {
			return err
		}
		hash, err := agencycontract.CanonicalHash(candidate)
		if err != nil {
			return err
		}
		childReason := "上级代理商同步成本"
		if reason != "" {
			childReason += "：" + reason
		}
		now := time.Now()
		row := model.AgencyPricePolicyVersion{AgencyID: child.ID, Revision: candidate.Revision, PolicyJSON: string(encoded), PolicyHash: hash, CreatedByType: actorType, CreatedByID: identity.ActorID, Reason: childReason, CreatedAtMS: now.UnixMilli()}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := a.writePolicyItems(tx, row.ID, candidate); err != nil {
			return err
		}
		result := tx.Model(&child).Where("id = ? AND price_revision = ?", child.ID, child.PriceRevision).Updates(map[string]any{"current_policy_version_id": row.ID, "price_revision": candidate.Revision, "version": child.Version + 1, "updated_at": now.Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agencycontract.ErrPolicyRevision
		}
		if err := recordAuditTx(tx, c, identity, "pricing.inherited_cost.sync", "agency", stringID(child.ID), childReason, map[string]any{"revision": childPolicy.Revision}, map[string]any{"revision": candidate.Revision, "policy_version_id": row.ID, "source_agency_id": parent.ID}); err != nil {
			return err
		}
	}
	return nil
}
