package agencyhub

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type childPricingPropagationError struct {
	AgencyPath string
	Cause      error
}

func (e *childPricingPropagationError) Error() string {
	return "代理路径：" + e.AgencyPath + "；价格冲突：" + pricingErrorMessage(e.Cause)
}

func (e *childPricingPropagationError) Unwrap() error { return e.Cause }

type childCostBoundaryError struct {
	ModelName                 string
	Cost, Minimum, ParentCost int
	Generated                 bool
}

func (e *childCostBoundaryError) Error() string {
	name := e.ModelName
	if name == "" {
		name = "全局默认价格"
	}
	source := "上级已设置的下级渠道报价"
	if e.Generated {
		source = fmt.Sprintf("上级成本 %.4f + 默认下级加价 0.0500（不是最低价差）", float64(e.ParentCost)/10000)
	}
	return fmt.Sprintf("模型 %s：本次下级代理商成本系数 %.4f，低于上级下级渠道报价 %.4f；下限来源：%s。请在上级的“代理商管理 → 管理下级成本”将该模型设为至少 %.4f，或先在上级“价格策略 → 我的下级渠道价”调整报价；销售价格仍须不低于各自成本 + 最低价差。", name, float64(e.Cost)/10000, float64(e.Minimum)/10000, source, float64(e.Minimum)/10000)
}

// directChildCost resolves the cost a parent charges for one direct child
// model. A zero value means that the parent did not configure that cost.
func directChildCost(policy agencycontract.Policy, modelName string) (int, bool) {
	if override := findModelOverride(policy, modelName); override != nil && override.ChildCostBPS != nil {
		return *override.ChildCostBPS, *override.ChildCostBPS != 0
	}
	return policy.DefaultChildCostBPS, policy.DefaultChildCostBPS != 0
}

func validateChildBoundary(parent, child agencycontract.Policy) error {
	if child.MinSpreadBPS < parent.MinSpreadBPS {
		return errors.New("child minimum spread cannot be below parent minimum spread")
	}
	check := func(name string, cost, minimum int) error {
		if cost >= minimum {
			return nil
		}
		parentCost := parent.DefaultSettlementBPS
		source := parent.ChildCostSource
		if override := findModelOverride(parent, name); override != nil {
			if override.SettlementBPS != nil {
				parentCost = *override.SettlementBPS
			}
			if override.ChildCostBPS != nil {
				source = override.ChildCostSource
			}
		}
		return &childCostBoundaryError{ModelName: name, Cost: cost, Minimum: minimum, ParentCost: parentCost, Generated: source == agencycontract.PriceSourceDefault}
	}
	if err := check("", child.DefaultSettlementBPS, parent.DefaultChildCostBPS); err != nil {
		return err
	}
	names := make(map[string]struct{})
	for _, policy := range []agencycontract.Policy{parent, child} {
		for _, override := range policy.ModelOverrides {
			names[override.OriginModelName] = struct{}{}
		}
	}
	orderedNames := make([]string, 0, len(names))
	for name := range names {
		orderedNames = append(orderedNames, name)
	}
	slices.Sort(orderedNames)
	for _, name := range orderedNames {
		cost, _ := directChildCost(parent, name)
		resolved, err := agencycontract.Resolve(child, name)
		if err != nil {
			return err
		}
		if err := check(name, resolved.SettlementBPS, cost); err != nil {
			return err
		}
	}
	return nil
}

// synchronizeChildCostPolicy copies a parent's direct-child costs into the
// child's settlement boundary. Generated downstream and sales values follow
// the new cost; legacy/explicit values have no source marker and are therefore
// treated as manual values that must never be overwritten.
func synchronizeChildCostPolicy(parent, child agencycontract.Policy) (agencycontract.Policy, bool, error) {
	candidate := child
	candidate.ModelOverrides = append([]agencycontract.ModelOverride(nil), child.ModelOverrides...)
	changed := false
	cap := candidate.SalesCapBPS
	if cap == 0 {
		cap = agencycontract.DefaultSalesCapBPS
	}
	// A populated legacy value without a source marker is treated as an
	// explicit/manual value. Only an empty value or one previously generated
	// by propagation may be changed when the parent publishes again.
	if parent.DefaultChildCostBPS != 0 && (candidate.DefaultSettlementBPS == 0 || candidate.SettlementSource == agencycontract.PriceSourceInherited || candidate.SettlementSource == agencycontract.PriceSourceDefault) && candidate.DefaultSettlementBPS != parent.DefaultChildCostBPS {
		candidate.DefaultSettlementBPS = parent.DefaultChildCostBPS
		candidate.SettlementSource = agencycontract.PriceSourceInherited
		changed = true
	}
	if candidate.DefaultSettlementBPS != 0 {
		if candidate.DefaultChildCostBPS == 0 || candidate.ChildCostSource == agencycontract.PriceSourceDefault || candidate.ChildCostSource == agencycontract.PriceSourceInherited {
			defaultChild := candidate.DefaultSettlementBPS + agencycontract.DefaultChildMarkupBPS
			if defaultChild > cap {
				return agencycontract.Policy{}, false, errors.New("默认下级系数超过价格上限")
			}
			if candidate.DefaultChildCostBPS != defaultChild || candidate.ChildCostSource != agencycontract.PriceSourceDefault {
				candidate.DefaultChildCostBPS = defaultChild
				candidate.ChildCostSource = agencycontract.PriceSourceDefault
				changed = true
			}
		}
		if candidate.DefaultSalesBPS == 0 || candidate.SalesSource == agencycontract.PriceSourceDefault || candidate.SalesSource == agencycontract.PriceSourceInherited {
			defaultSales, err := inheritedSalesBPS(parent.DefaultSalesBPS, candidate.DefaultSettlementBPS, candidate.MinSpreadBPS, candidate.SalesCapBPS)
			if err != nil {
				return agencycontract.Policy{}, false, err
			}
			if candidate.DefaultSalesBPS != defaultSales || candidate.SalesSource != agencycontract.PriceSourceDefault {
				candidate.DefaultSalesBPS = defaultSales
				candidate.SalesSource = agencycontract.PriceSourceDefault
				changed = true
			}
		}
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
		value := afterCost
		current := &candidate.ModelOverrides[index]
		if current.SettlementBPS == nil || current.SettlementSource == agencycontract.PriceSourceInherited || current.SettlementSource == agencycontract.PriceSourceDefault {
			if current.SettlementBPS != nil && *current.SettlementBPS == value && current.SettlementSource == agencycontract.PriceSourceInherited {
				// Already synchronized.
			} else {
				current.SettlementBPS = &value
				current.SettlementSource = agencycontract.PriceSourceInherited
				changed = true
			}
		}
		effectiveCost := value
		if current.SettlementBPS != nil {
			effectiveCost = *current.SettlementBPS
		}
		if current.ChildCostBPS == nil || current.ChildCostSource == agencycontract.PriceSourceDefault || current.ChildCostSource == agencycontract.PriceSourceInherited {
			defaultChild := effectiveCost + agencycontract.DefaultChildMarkupBPS
			if defaultChild > cap {
				return agencycontract.Policy{}, false, errors.New("默认下级系数超过价格上限")
			}
			if current.ChildCostBPS == nil || *current.ChildCostBPS != defaultChild || current.ChildCostSource != agencycontract.PriceSourceDefault {
				current.ChildCostBPS = &defaultChild
				current.ChildCostSource = agencycontract.PriceSourceDefault
				changed = true
			}
		}
		if current.SalesBPS == nil || current.SalesSource == agencycontract.PriceSourceDefault || current.SalesSource == agencycontract.PriceSourceInherited {
			parentResolved, err := agencycontract.Resolve(parent, override.OriginModelName)
			if err != nil {
				return agencycontract.Policy{}, false, err
			}
			defaultSales, err := inheritedSalesBPS(parentResolved.SalesBPS, effectiveCost, candidate.MinSpreadBPS, candidate.SalesCapBPS)
			if err != nil {
				return agencycontract.Policy{}, false, err
			}
			if current.SalesBPS == nil || *current.SalesBPS != defaultSales || current.SalesSource != agencycontract.PriceSourceDefault {
				current.SalesBPS = &defaultSales
				current.SalesSource = agencycontract.PriceSourceDefault
				changed = true
			}
		}
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
		defaultChild := value + agencycontract.DefaultChildMarkupBPS
		if defaultChild > cap {
			return agencycontract.Policy{}, false, errors.New("默认下级系数超过价格上限")
		}
		parentResolved, err := agencycontract.Resolve(parent, override.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, false, err
		}
		defaultSales, err := inheritedSalesBPS(parentResolved.SalesBPS, value, candidate.MinSpreadBPS, candidate.SalesCapBPS)
		if err != nil {
			return agencycontract.Policy{}, false, err
		}
		candidate.ModelOverrides = append(candidate.ModelOverrides, agencycontract.ModelOverride{OriginModelName: override.OriginModelName, SettlementBPS: &value, ChildCostBPS: &defaultChild, SalesBPS: &defaultSales, SettlementSource: agencycontract.PriceSourceInherited, ChildCostSource: agencycontract.PriceSourceDefault, SalesSource: agencycontract.PriceSourceDefault})
		positions[key] = len(candidate.ModelOverrides) - 1
		changed = true
	}

	if err := agencycontract.ValidatePolicy(candidate); err != nil {
		return agencycontract.Policy{}, false, err
	}
	if err := validateChildBoundary(parent, candidate); err != nil {
		return agencycontract.Policy{}, false, err
	}
	return candidate, changed, nil
}

func (a *App) syncDirectChildPoliciesTx(tx *gorm.DB, c *gin.Context, identity *Identity, parent model.Agency, after agencycontract.Policy, reason, actorType string) error {
	return a.syncChildPoliciesTx(tx, c, identity, parent, after, reason, actorType, parent.DisplayName)
}

func (a *App) syncChildPoliciesTx(tx *gorm.DB, c *gin.Context, identity *Identity, parent model.Agency, after agencycontract.Policy, reason, actorType, parentPath string) error {
	if parent.Depth > maxAgencyDepth {
		return errors.New("agency hierarchy depth limit exceeded")
	}
	var children []model.Agency
	if err := model.AgencyLockForUpdate(tx).Where("parent_agency_id = ?", parent.ID).Order("id ASC").Find(&children).Error; err != nil {
		return err
	}
	for _, child := range children {
		childPath := parentPath + " → " + child.DisplayName
		var current model.AgencyPricePolicyVersion
		if err := tx.Where("id = ? AND agency_id = ?", child.CurrentPolicyVersionID, child.ID).First(&current).Error; err != nil {
			return err
		}
		var childPolicy agencycontract.Policy
		if err := common.Unmarshal([]byte(current.PolicyJSON), &childPolicy); err != nil {
			return &childPricingPropagationError{AgencyPath: childPath, Cause: errors.New("代理商价格策略数据异常")}
		}
		platform, err := model.LoadAgencyPlatformPolicy(tx)
		if err != nil {
			return err
		}
		candidate, changed, err := synchronizeChildCostPolicy(after, agencycontract.ConfirmedModelPolicy(childPolicy, platform))
		if err != nil {
			return &childPricingPropagationError{AgencyPath: childPath, Cause: err}
		}
		if err := validateCustomerSalesForPlatformTx(tx, child.ID, candidate, platform); err != nil {
			return &childPricingPropagationError{AgencyPath: childPath, Cause: err}
		}
		if !changed {
			if err := a.syncChildPoliciesTx(tx, c, identity, child, candidate, reason, actorType, childPath); err != nil {
				return err
			}
			continue
		}
		candidate = preservePendingModelOverrides(candidate, childPolicy, platform)
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
		child.CurrentPolicyVersionID = row.ID
		child.PriceRevision = candidate.Revision
		child.Version++
		if err := a.syncChildPoliciesTx(tx, c, identity, child, agencycontract.ConfirmedModelPolicy(candidate, platform), reason, actorType, childPath); err != nil {
			return err
		}
	}
	return nil
}

func preservePendingModelOverrides(candidate, stored agencycontract.Policy, platform agencycontract.PlatformPolicy) agencycontract.Policy {
	for _, override := range stored.ModelOverrides {
		if !agencycontract.ModelPricingConfirmed(platform, override.OriginModelName) {
			candidate.ModelOverrides = append(candidate.ModelOverrides, override)
		}
	}
	return candidate
}
