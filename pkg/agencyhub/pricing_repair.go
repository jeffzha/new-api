package agencyhub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

type PricingRepairChange struct {
	AgencyID   int64  `json:"agency_id"`
	AgencyName string `json:"agency_name"`
	UserID     int64  `json:"user_id,omitempty"`
	Model      string `json:"model"`
	Field      string `json:"field"`
	Before     *int   `json:"before_bps"`
	After      int    `json:"after_bps"`
	Reason     string `json:"reason"`
}

type PricingRepairReport struct {
	PlanHash              string                `json:"plan_hash"`
	Applied               bool                  `json:"applied"`
	AgencyCount           int                   `json:"agency_count"`
	CustomerOverrideCount int                   `json:"customer_override_count"`
	Changes               []PricingRepairChange `json:"changes"`
}

type pricingRepairEntry struct {
	Agency model.Agency
	Before agencycontract.Policy
	After  agencycontract.Policy
}

type pricingRepairPlan struct {
	Platform      agencycontract.PlatformPolicy
	Entries       []pricingRepairEntry
	Customers     []model.AgencyCustomerSalesOverride
	CustomerAfter []int
}

// RepairPricing is an offline operator command, not an HTTP endpoint. Dry runs
// never write. Applying requires the exact dry-run hash and records new immutable
// versions; historical billing snapshots and wallet amounts are never modified.
func (a *App) RepairPricing(ctx context.Context, expectedHash string, rootID int64) (PricingRepairReport, error) {
	report := PricingRepairReport{Changes: []PricingRepairChange{}}
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state model.AgencyPlatformPriceState
		if err := model.AgencyLockForUpdate(tx).First(&state, 1).Error; err != nil {
			return err
		}
		platform, err := model.LoadAgencyPlatformPolicy(tx)
		if err != nil {
			return err
		}
		var agencies []model.Agency
		if err := model.AgencyLockForUpdate(tx).Order("depth ASC, id ASC").Find(&agencies).Error; err != nil {
			return err
		}
		var customers []model.AgencyCustomerSalesOverride
		if err := model.AgencyLockForUpdate(tx).Order("agency_id ASC, user_id ASC, id ASC").Find(&customers).Error; err != nil {
			return err
		}
		report.AgencyCount, report.CustomerOverrideCount = len(agencies), len(customers)
		plan := pricingRepairPlan{Platform: platform, Customers: customers}
		effective := map[int64]agencycontract.Policy{}
		names := map[int64]string{}
		for _, agency := range agencies {
			var version model.AgencyPricePolicyVersion
			if err := tx.Where("id = ? AND agency_id = ?", agency.CurrentPolicyVersionID, agency.ID).First(&version).Error; err != nil {
				return err
			}
			var stored agencycontract.Policy
			if err := common.UnmarshalJsonStr(version.PolicyJSON, &stored); err != nil {
				return err
			}
			stored.Revision = agency.PriceRevision
			var parent *agencycontract.Policy
			if agency.ParentAgencyID != nil {
				value, ok := effective[*agency.ParentAgencyID]
				if !ok {
					return errors.New("agency tree has a missing or incorrectly ordered parent")
				}
				parent = &value
			}
			after, changes, err := repairAgencyPricePolicy(agency, stored, platform, parent)
			if err != nil {
				return fmt.Errorf("agency %d: %w", agency.ID, err)
			}
			report.Changes = append(report.Changes, changes...)
			effective[agency.ID] = agencycontract.ConfirmedModelPolicy(after, platform)
			names[agency.ID] = agency.DisplayName
			plan.Entries = append(plan.Entries, pricingRepairEntry{Agency: agency, Before: stored, After: after})
		}
		for _, customer := range customers {
			policy, ok := effective[customer.AgencyID]
			if !ok {
				return errors.New("customer price references a missing agency")
			}
			minimum := 0
			if customer.OriginModelName == "" {
				minimum = policy.DefaultSettlementBPS + policy.MinSpreadBPS
				for _, price := range platform.ModelPrices {
					if !price.IsPricingConfirmed() {
						continue
					}
					resolved, err := agencycontract.Resolve(policy, price.OriginModelName)
					if err != nil {
						return err
					}
					minimum = max(minimum, resolved.SettlementBPS+policy.MinSpreadBPS)
				}
			} else if agencycontract.ModelPricingConfirmed(platform, customer.OriginModelName) {
				resolved, err := agencycontract.Resolve(policy, customer.OriginModelName)
				if err != nil {
					return err
				}
				minimum = resolved.SettlementBPS + policy.MinSpreadBPS
			}
			sales := customer.SalesBPS
			if customer.OriginModelName == "" || agencycontract.ModelPricingConfirmed(platform, customer.OriginModelName) {
				if err := validateCustomerSalesPolicy(policy, customer.OriginModelName, sales); err != nil {
					sales = max(sales, minimum)
				}
			}
			if err := agencycontract.ValidateCoefficient(sales, agencycontract.MaxCoefficientBPS); err != nil {
				return err
			}
			plan.CustomerAfter = append(plan.CustomerAfter, sales)
			if sales != customer.SalesBPS {
				before := customer.SalesBPS
				report.Changes = append(report.Changes, PricingRepairChange{AgencyID: customer.AgencyID, AgencyName: names[customer.AgencyID], UserID: customer.UserID, Model: customer.OriginModelName, Field: "customer_sales_bps", Before: &before, After: sales, Reason: "客户单独售价低于代理商成本与最低价差之和，提升至合法下限"})
			}
		}
		report.PlanHash, err = agencycontract.CanonicalHash(plan)
		if err != nil {
			return err
		}
		if expectedHash == "" {
			return nil
		}
		if expectedHash != report.PlanHash {
			return errors.New("pricing changed since dry run; export and review a new plan")
		}
		var root model.User
		if err := tx.Where("id = ? AND role >= ?", rootID, common.RoleRootUser).First(&root).Error; err != nil {
			return errors.New("a valid platform root actor is required")
		}
		identity := &Identity{ActorType: ActorTypeRoot, ActorID: rootID}
		now := time.Now()
		for _, entry := range plan.Entries {
			beforeHash, err := agencycontract.CanonicalHash(entry.Before)
			if err != nil {
				return err
			}
			afterHash, err := agencycontract.CanonicalHash(entry.After)
			if err != nil {
				return err
			}
			if beforeHash == afterHash {
				continue
			}
			after := entry.After
			after.Revision = entry.Agency.PriceRevision + 1
			encoded, err := common.Marshal(after)
			if err != nil {
				return err
			}
			hash, err := agencycontract.CanonicalHash(after)
			if err != nil {
				return err
			}
			version := model.AgencyPricePolicyVersion{AgencyID: entry.Agency.ID, Revision: after.Revision, PolicyJSON: string(encoded), PolicyHash: hash, CreatedByType: ActorTypeRoot, CreatedByID: rootID, Reason: "历史价格边界订正：" + report.PlanHash, CreatedAtMS: now.UnixMilli()}
			if err := tx.Create(&version).Error; err != nil {
				return err
			}
			if err := a.writePolicyItems(tx, version.ID, after); err != nil {
				return err
			}
			result := tx.Model(&model.Agency{}).Where("id = ? AND price_revision = ? AND current_policy_version_id = ?", entry.Agency.ID, entry.Agency.PriceRevision, entry.Agency.CurrentPolicyVersionID).Updates(map[string]any{"current_policy_version_id": version.ID, "price_revision": after.Revision, "version": entry.Agency.Version + 1, "updated_at": now.Unix()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return agencycontract.ErrPolicyRevision
			}
			if err := recordAuditTx(tx, nil, identity, "pricing.repair", "agency", stringID(entry.Agency.ID), report.PlanHash, entry.Before, after); err != nil {
				return err
			}
		}
		for i, customer := range customers {
			sales := plan.CustomerAfter[i]
			if sales == customer.SalesBPS {
				continue
			}
			result := tx.Model(&model.AgencyCustomerSalesOverride{}).Where("id = ? AND revision = ?", customer.ID, customer.Revision).Updates(map[string]any{"sales_bps": sales, "revision": customer.Revision + 1, "created_by_type": ActorTypeRoot, "created_by_id": rootID, "updated_at_ms": now.UnixMilli()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return agencycontract.ErrPolicyRevision
			}
			if err := recordAuditTx(tx, nil, identity, "pricing.customer.repair", "customer", stringID(customer.UserID), report.PlanHash, customer, map[string]any{"sales_bps": sales, "revision": customer.Revision + 1}); err != nil {
				return err
			}
		}
		report.Applied = true
		return nil
	})
	return report, err
}

func repairAgencyPricePolicy(agency model.Agency, stored agencycontract.Policy, platform agencycontract.PlatformPolicy, parent *agencycontract.Policy) (agencycontract.Policy, []PricingRepairChange, error) {
	encoded, err := common.Marshal(stored)
	if err != nil {
		return agencycontract.Policy{}, nil, err
	}
	var result agencycontract.Policy
	if err := common.Unmarshal(encoded, &result); err != nil {
		return result, nil, err
	}
	changes := []PricingRepairChange{}
	change := func(name, field string, before *int, after int, reason string) {
		if before != nil && *before == after {
			return
		}
		changes = append(changes, PricingRepairChange{AgencyID: agency.ID, AgencyName: agency.DisplayName, Model: name, Field: field, Before: before, After: after, Reason: reason})
	}
	set := func(name, field string, target **int, value int, reason string) {
		before := *target
		change(name, field, before, value, reason)
		copy := value
		*target = &copy
	}
	if parent != nil {
		if result.MinSpreadBPS < parent.MinSpreadBPS {
			before := result.MinSpreadBPS
			result.MinSpreadBPS = parent.MinSpreadBPS
			change("", "min_spread_bps", &before, result.MinSpreadBPS, "最低价差不能低于上级")
		}
		floor := parent.DefaultChildCostBPS
		if floor != 0 && (result.DefaultSettlementBPS < floor || result.SettlementSource == agencycontract.PriceSourceInherited || result.SettlementSource == agencycontract.PriceSourceDefault) {
			before := result.DefaultSettlementBPS
			result.DefaultSettlementBPS = floor
			change("", "default_settlement_bps", &before, floor, "对齐上级已生效的默认下级报价")
		}
	}
	if result.DefaultChildCostBPS != 0 && result.DefaultChildCostBPS < result.DefaultSettlementBPS+result.MinSpreadBPS {
		before := result.DefaultChildCostBPS
		result.DefaultChildCostBPS = result.DefaultSettlementBPS + result.MinSpreadBPS
		change("", "default_child_cost_bps", &before, result.DefaultChildCostBPS, "默认下级报价低于自身成本与最低价差之和")
	}
	if result.DefaultSalesBPS < result.DefaultSettlementBPS+result.MinSpreadBPS {
		before := result.DefaultSalesBPS
		result.DefaultSalesBPS = result.DefaultSettlementBPS + result.MinSpreadBPS
		change("", "default_sales_bps", &before, result.DefaultSalesBPS, "默认售价低于自身成本与最低价差之和")
	}
	prices := append([]agencycontract.PlatformModelPrice(nil), platform.ModelPrices...)
	sort.Slice(prices, func(i, j int) bool { return prices[i].OriginModelName < prices[j].OriginModelName })
	for _, price := range prices {
		if !price.IsPricingConfirmed() {
			continue
		}
		name := price.OriginModelName
		current := findModelOverride(result, name)
		if current == nil {
			result.ModelOverrides = append(result.ModelOverrides, agencycontract.ModelOverride{OriginModelName: name})
			current = &result.ModelOverrides[len(result.ModelOverrides)-1]
		}
		cost := result.DefaultSettlementBPS
		if current.SettlementBPS != nil {
			cost = *current.SettlementBPS
		}
		if parent == nil {
			cost = price.AgencyCostBPS
			if current.SettlementBPS == nil || *current.SettlementBPS != cost {
				set(name, "settlement_bps", &current.SettlementBPS, cost, "一级代理商成本对齐平台已确认的下级系数（不自动加0.05）")
			}
			current.SettlementSource = agencycontract.PriceSourceInherited
		} else {
			floor, configured := directChildCost(*parent, name)
			if configured && (cost < floor || current.SettlementBPS == nil || current.SettlementSource == agencycontract.PriceSourceInherited || current.SettlementSource == agencycontract.PriceSourceDefault) {
				cost = floor
				set(name, "settlement_bps", &current.SettlementBPS, cost, "下级成本不得低于上级已生效的下级报价；继承值同步上级报价")
				current.SettlementSource = agencycontract.PriceSourceInherited
			}
		}
		minimum := cost + result.MinSpreadBPS
		if current.ChildCostBPS == nil {
			set(name, "child_cost_bps", &current.ChildCostBPS, max(cost+agencycontract.DefaultChildMarkupBPS, minimum), "未填写下级报价，按成本+0.05自动补齐，并满足最低价差")
			current.ChildCostSource = agencycontract.PriceSourceDefault
		} else if *current.ChildCostBPS < minimum {
			set(name, "child_cost_bps", &current.ChildCostBPS, minimum, "下级报价低于自身成本与最低价差之和，提升至合法下限")
		} else if current.ChildCostSource == agencycontract.PriceSourceDefault || current.ChildCostSource == agencycontract.PriceSourceInherited {
			set(name, "child_cost_bps", &current.ChildCostBPS, max(cost+agencycontract.DefaultChildMarkupBPS, minimum), "自动下级报价随成本同步；合法手动值保持不变")
		}
		sales := result.DefaultSalesBPS
		if current.SalesBPS != nil {
			sales = *current.SalesBPS
		}
		if current.SalesBPS == nil || current.SalesSource == agencycontract.PriceSourceDefault || current.SalesSource == agencycontract.PriceSourceInherited {
			upstreamSale := price.DefaultSalesBPS
			if parent != nil {
				resolved, err := agencycontract.Resolve(*parent, name)
				if err != nil {
					return result, nil, err
				}
				upstreamSale = resolved.SalesBPS
			}
			if current.SalesBPS == nil && parent != nil {
				sales = max(sales, minimum)
			} else {
				sales = max(upstreamSale, minimum)
			}
			if parent == nil || current.SalesBPS != nil {
				current.SalesSource = agencycontract.PriceSourceDefault
			}
		}
		if sales < minimum {
			sales = minimum
		}
		if parent == nil || current.SalesBPS != nil || sales != result.DefaultSalesBPS {
			set(name, "sales_bps", &current.SalesBPS, sales, "售价低于成本边界时提升至合法下限；自动值遵循继承规则")
		}
	}
	confirmed := agencycontract.ConfirmedModelPolicy(result, platform)
	if err := agencycontract.ValidatePolicy(confirmed); err != nil {
		return result, nil, err
	}
	if parent != nil {
		if err := validateChildBoundary(*parent, confirmed); err != nil {
			return result, nil, err
		}
	} else {
		effective, err := agencycontract.ApplyPlatformPolicy(result, platform)
		if err != nil {
			return result, nil, err
		}
		for _, override := range effective.ModelOverrides {
			original := findModelOverride(result, override.OriginModelName)
			if original != nil && original.ChildCostBPS != nil && override.ChildCostBPS != nil && *original.ChildCostBPS != *override.ChildCostBPS {
				return result, nil, errors.New("effective platform overlay would change repaired child price")
			}
		}
	}
	return result, changes, nil
}

// RollbackPricingRepair restores only prices from a repair's audit records.
// It refuses to overwrite any price edited after the repair and never rewinds usage.
func (a *App) RollbackPricingRepair(ctx context.Context, planHash string, rootID int64) error {
	if planHash == "" {
		return errors.New("repair plan hash is required")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state model.AgencyPlatformPriceState
		if err := model.AgencyLockForUpdate(tx).First(&state, 1).Error; err != nil {
			return err
		}
		var root model.User
		if err := tx.Where("id = ? AND role >= ?", rootID, common.RoleRootUser).First(&root).Error; err != nil {
			return errors.New("a valid platform root actor is required")
		}
		var logs []model.AgencyAuditLog
		if err := tx.Where("reason = ? AND action IN ?", planHash, []string{"pricing.repair", "pricing.customer.repair"}).Order("id ASC").Find(&logs).Error; err != nil {
			return err
		}
		if len(logs) == 0 {
			return errors.New("repair audit records not found")
		}
		var already int64
		if err := tx.Model(&model.AgencyAuditLog{}).Where("reason = ? AND action = ?", planHash, "pricing.repair.rollback").Count(&already).Error; err != nil {
			return err
		}
		if already != 0 {
			return errors.New("this repair was already rolled back")
		}
		identity := &Identity{ActorType: ActorTypeRoot, ActorID: rootID}
		now := time.Now()
		for _, log := range logs {
			if log.Action == "pricing.repair" {
				var before, after agencycontract.Policy
				if err := common.UnmarshalJsonStr(log.BeforeJSON, &before); err != nil {
					return err
				}
				if err := common.UnmarshalJsonStr(log.AfterJSON, &after); err != nil {
					return err
				}
				var agency model.Agency
				if err := model.AgencyLockForUpdate(tx).Where("id = ?", log.ObjectID).First(&agency).Error; err != nil {
					return err
				}
				var version model.AgencyPricePolicyVersion
				if err := tx.First(&version, agency.CurrentPolicyVersionID).Error; err != nil {
					return err
				}
				expected, err := agencycontract.CanonicalHash(after)
				if err != nil {
					return err
				}
				var current agencycontract.Policy
				if err := common.UnmarshalJsonStr(version.PolicyJSON, &current); err != nil {
					return err
				}
				actual, err := agencycontract.CanonicalHash(current)
				if err != nil {
					return err
				}
				if actual != expected || agency.PriceRevision != after.Revision {
					return fmt.Errorf("agency %d was edited after repair; rollback refused", agency.ID)
				}
				before.Revision = agency.PriceRevision + 1
				encoded, err := common.Marshal(before)
				if err != nil {
					return err
				}
				hash, err := agencycontract.CanonicalHash(before)
				if err != nil {
					return err
				}
				restored := model.AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: before.Revision, PolicyJSON: string(encoded), PolicyHash: hash, CreatedByType: ActorTypeRoot, CreatedByID: rootID, Reason: "价格订正回退：" + planHash, CreatedAtMS: now.UnixMilli()}
				if err := tx.Create(&restored).Error; err != nil {
					return err
				}
				if err := a.writePolicyItems(tx, restored.ID, before); err != nil {
					return err
				}
				update := tx.Model(&agency).Where("price_revision = ?", agency.PriceRevision).Updates(map[string]any{"current_policy_version_id": restored.ID, "price_revision": before.Revision, "version": agency.Version + 1, "updated_at": now.Unix()})
				if update.Error != nil {
					return update.Error
				}
				if update.RowsAffected != 1 {
					return agencycontract.ErrPolicyRevision
				}
				if err := recordAuditTx(tx, nil, identity, "pricing.repair.rollback", "agency", log.ObjectID, planHash, after, before); err != nil {
					return err
				}
			} else {
				var before model.AgencyCustomerSalesOverride
				var after struct {
					SalesBPS int   `json:"sales_bps"`
					Revision int64 `json:"revision"`
				}
				if err := common.UnmarshalJsonStr(log.BeforeJSON, &before); err != nil {
					return err
				}
				if err := common.UnmarshalJsonStr(log.AfterJSON, &after); err != nil {
					return err
				}
				var current model.AgencyCustomerSalesOverride
				if err := model.AgencyLockForUpdate(tx).First(&current, before.ID).Error; err != nil {
					return err
				}
				if current.Revision != after.Revision || current.SalesBPS != after.SalesBPS {
					return fmt.Errorf("customer override %d was edited after repair; rollback refused", current.ID)
				}
				update := tx.Model(&current).Where("revision = ?", current.Revision).Updates(map[string]any{"sales_bps": before.SalesBPS, "revision": current.Revision + 1, "updated_at_ms": now.UnixMilli()})
				if update.Error != nil {
					return update.Error
				}
				if update.RowsAffected != 1 {
					return agencycontract.ErrPolicyRevision
				}
				if err := recordAuditTx(tx, nil, identity, "pricing.repair.rollback", "customer", log.ObjectID, planHash, current, before); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
