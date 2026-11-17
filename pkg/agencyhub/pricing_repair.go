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
	RestorationReview     []PricingRestorationReview `json:"restoration_review,omitempty"`
	PlanHash              string                     `json:"plan_hash"`
	Applied               bool                       `json:"applied"`
	AgencyCount           int                        `json:"agency_count"`
	CustomerOverrideCount int                        `json:"customer_override_count"`
	Changes               []PricingRepairChange      `json:"changes"`
}

type pricingRepairEntry struct {
	Agency model.Agency
	Before agencycontract.Policy
	After  agencycontract.Policy
}

type pricingRepairPlan struct {
	OriginalRepairHash string
	Platform           agencycontract.PlatformPolicy
	Entries            []pricingRepairEntry
	Customers          []model.AgencyCustomerSalesOverride
	CustomerAfter      []int
}

// RepairPricing is an offline operator command, not an HTTP endpoint. Dry runs
// never write. Applying requires the exact dry-run hash and records new immutable
// versions; historical billing snapshots and wallet amounts are never modified.
func (a *App) RepairPricing(ctx context.Context, expectedHash string, rootID int64) (PricingRepairReport, error) {
	return a.repairPricing(ctx, "", expectedHash, rootID)
}

type PricingRestorationReview struct {
	AgencyID int64  `json:"agency_id"`
	Model    string `json:"model"`
	Field    string `json:"field"`
	Original int    `json:"original_bps"`
	Current  int    `json:"current_bps"`
	Minimum  int    `json:"minimum_bps"`
	Restored bool   `json:"restored"`
	Reason   string `json:"reason"`
}

// RestoreNegotiatedPricing restores only explicit increases from one audited
// repair. It does not undo catalog initialization, later edits, or billing.
func (a *App) RestoreNegotiatedPricing(ctx context.Context, originalHash, expectedHash string, rootID int64) (PricingRepairReport, error) {
	if originalHash == "" {
		return PricingRepairReport{}, errors.New("original pricing repair hash is required")
	}
	return a.repairPricing(ctx, originalHash, expectedHash, rootID)
}

func (a *App) repairPricing(ctx context.Context, originalHash, expectedHash string, rootID int64) (PricingRepairReport, error) {
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
		plan := pricingRepairPlan{OriginalRepairHash: originalHash, Platform: platform, Customers: customers}
		repairAudits := map[string]model.AgencyAuditLog{}
		if originalHash != "" {
			var logs []model.AgencyAuditLog
			if err := tx.Where("reason = ? AND action = ?", originalHash, "pricing.repair").Order("id ASC").Find(&logs).Error; err != nil {
				return err
			}
			if len(logs) == 0 {
				return errors.New("original pricing repair audit records not found")
			}
			for _, log := range logs {
				repairAudits[log.ObjectID] = log
			}
		}
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
			candidate := stored
			restoreModels := map[string]bool{}
			if audit, ok := repairAudits[stringID(agency.ID)]; ok {
				var reviews []PricingRestorationReview
				var restores []PricingRepairChange
				candidate, reviews, restores, err = restoreNegotiatedFields(agency, stored, parent, audit)
				if err != nil {
					return err
				}
				report.RestorationReview = append(report.RestorationReview, reviews...)
				report.Changes = append(report.Changes, restores...)
				for _, change := range restores {
					restoreModels[change.Model] = true
				}
			}
			after, changes, err := repairAgencyPricePolicy(agency, candidate, platform, parent)
			if err != nil {
				return fmt.Errorf("agency %d: %w", agency.ID, err)
			}
			report.Changes = append(report.Changes, changes...)
			if originalHash != "" {
				// Restoration must not normalize unrelated prices. Only restored
				// models and descendants of a changed parent quote may propagate.
				if agency.ParentAgencyID != nil {
					for _, entry := range plan.Entries {
						if entry.Agency.ID != *agency.ParentAgencyID {
							continue
						}
						for _, override := range entry.After.ModelOverrides {
							beforeQuote, _ := directChildCost(entry.Before, override.OriginModelName)
							afterQuote, _ := directChildCost(entry.After, override.OriginModelName)
							if beforeQuote != afterQuote {
								restoreModels[override.OriginModelName] = true
							}
						}
					}
				}
				filtered := candidate
				filtered.ModelOverrides = append([]agencycontract.ModelOverride(nil), candidate.ModelOverrides...)
				for index, override := range filtered.ModelOverrides {
					if restoreModels[override.OriginModelName] {
						if replacement := findModelOverride(after, override.OriginModelName); replacement != nil {
							filtered.ModelOverrides[index] = *replacement
						}
					}
				}
				after = filtered
				retained := report.Changes[:len(report.Changes)-len(changes)]
				for _, change := range changes {
					if restoreModels[change.Model] && change.Model != "" {
						retained = append(retained, change)
					}
				}
				report.Changes = retained
				if err := agencycontract.ValidatePolicy(agencycontract.ConfirmedModelPolicy(after, platform)); err != nil {
					return err
				}
			}
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
			if originalHash != "" && sales != customer.SalesBPS {
				return fmt.Errorf("restoration would alter unrelated customer %d pricing; review separately", customer.UserID)
			}
			plan.CustomerAfter = append(plan.CustomerAfter, sales)
			if sales != customer.SalesBPS {
				before := customer.SalesBPS
				report.Changes = append(report.Changes, PricingRepairChange{AgencyID: customer.AgencyID, AgencyName: names[customer.AgencyID], UserID: customer.UserID, Model: customer.OriginModelName, Field: "customer_sales_bps", Before: &before, After: sales, Reason: "客户单独售价低于代理商成本与最低价差之和，提升至合法下限"})
			}
		}
		report.PlanHash, err = agencycontract.CanonicalHash(plan)
		validationPlatform := platform
		if originalHash != "" {
			changedModels := map[string]bool{}
			for _, change := range report.Changes {
				changedModels[change.Model] = true
			}
			validationPlatform.ModelPrices = nil
			for _, price := range platform.ModelPrices {
				if changedModels[price.OriginModelName] {
					validationPlatform.ModelPrices = append(validationPlatform.ModelPrices, price)
				}
			}
		}
		if validationErr := validatePricingBillingPaths(agencies, effective, validationPlatform); validationErr != nil {
			return validationErr
		}
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

func restoreNegotiatedFields(agency model.Agency, current agencycontract.Policy, parent *agencycontract.Policy, audit model.AgencyAuditLog) (agencycontract.Policy, []PricingRestorationReview, []PricingRepairChange, error) {
	var before, repaired agencycontract.Policy
	if err := common.UnmarshalJsonStr(audit.BeforeJSON, &before); err != nil {
		return current, nil, nil, err
	}
	if err := common.UnmarshalJsonStr(audit.AfterJSON, &repaired); err != nil {
		return current, nil, nil, err
	}
	encoded, err := common.Marshal(current)
	if err != nil {
		return current, nil, nil, err
	}
	var candidate agencycontract.Policy
	if err := common.Unmarshal(encoded, &candidate); err != nil {
		return current, nil, nil, err
	}
	reviews := []PricingRestorationReview{}
	changes := []PricingRepairChange{}
	for _, original := range before.ModelOverrides {
		raised := findModelOverride(repaired, original.OriginModelName)
		target := findModelOverride(candidate, original.OriginModelName)
		if raised == nil || target == nil {
			continue
		}
		for _, field := range []string{"settlement_bps", "sales_bps"} {
			old, repairedValue, value := original.SettlementBPS, raised.SettlementBPS, target.SettlementBPS
			source, repairedSource, currentSource := original.SettlementSource, raised.SettlementSource, target.SettlementSource
			if field == "sales_bps" {
				old, repairedValue, value = original.SalesBPS, raised.SalesBPS, target.SalesBPS
				source, repairedSource, currentSource = original.SalesSource, raised.SalesSource, target.SalesSource
			}
			if old == nil || repairedValue == nil || value == nil || *old >= *repairedValue {
				continue
			}
			review := PricingRestorationReview{AgencyID: agency.ID, Model: original.OriginModelName, Field: field, Original: *old, Current: *value}
			switch {
			case source != "" && source != agencycontract.PriceSourceManual:
				review.Reason = "原值属于自动继承，不按单独约定恢复"
			case current.Revision != repaired.Revision || *value != *repairedValue || currentSource != repairedSource:
				review.Reason = "订正后已修改或已恢复，保留当前值"
			case field == "settlement_bps" && parent == nil:
				review.Reason = "一级代理商成本由平台已确认报价决定，不属于默认加点限制"
			default:
				if field == "settlement_bps" {
					review.Minimum, _, err = minimumDirectChildCost(*parent, candidate, original.OriginModelName)
					if err != nil {
						return current, nil, nil, err
					}
				} else {
					resolved, resolveErr := agencycontract.Resolve(candidate, original.OriginModelName)
					if resolveErr != nil {
						return current, nil, nil, resolveErr
					}
					review.Minimum = resolved.SettlementBPS + candidate.MinSpreadBPS
				}
				if *old < review.Minimum {
					review.Reason = "原值仍低于实际成本＋最低价差，不可回退"
				} else {
					restored := *old
					if field == "settlement_bps" {
						target.SettlementBPS, target.SettlementSource = &restored, agencycontract.PriceSourceManual
					} else {
						target.SalesBPS, target.SalesSource = &restored, agencycontract.PriceSourceManual
					}
					review.Restored, review.Reason = true, "恢复原来合法的手动价格；默认下级报价不再作为硬性底价"
					previous := *value
					changes = append(changes, PricingRepairChange{AgencyID: agency.ID, AgencyName: agency.DisplayName, Model: original.OriginModelName, Field: field, Before: &previous, After: restored, Reason: review.Reason})
				}
			}
			reviews = append(reviews, review)
		}
	}
	return candidate, reviews, changes, nil
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
		floor, _, err := minimumDirectChildCost(*parent, result, "")
		if err != nil {
			return result, nil, err
		}
		cost := result.DefaultSettlementBPS
		if result.SettlementSource == agencycontract.PriceSourceInherited || result.SettlementSource == agencycontract.PriceSourceDefault {
			if quote, configured := directChildCost(*parent, ""); configured {
				cost = quote
			}
		}
		cost = max(cost, floor)
		if result.DefaultSettlementBPS != cost {
			before := result.DefaultSettlementBPS
			result.DefaultSettlementBPS = cost
			change("", "default_settlement_bps", &before, cost, "继承默认下级报价；单独报价仅校验上级成本与最低价差底线")
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
	// The tenant's negotiated root cost also owns the repair boundary.
	rootCosts := make(map[string]int)
	if parent == nil {
		costPolicy := stored
		costPolicy.DefaultSalesBPS = agencycontract.MaxCoefficientBPS
		costPolicy.DefaultChildCostBPS = 0
		costPolicy.SalesCapBPS = agencycontract.MaxCoefficientBPS
		costPolicy.ModelOverrides = nil
		effective, err := agencycontract.ApplyPlatformPolicyForAgency(costPolicy, platform, agency.ID)
		if err != nil {
			return result, nil, err
		}
		for _, override := range effective.ModelOverrides {
			if override.SettlementBPS != nil {
				rootCosts[override.OriginModelName] = *override.SettlementBPS
			}
		}
	}
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
			cost = rootCosts[name]
			if current.SettlementBPS == nil || *current.SettlementBPS != cost {
				set(name, "settlement_bps", &current.SettlementBPS, cost, "一级代理商成本对齐平台已确认的下级系数（不自动加0.05）")
			}
			current.SettlementSource = agencycontract.PriceSourceInherited
		} else {
			floor, _, err := minimumDirectChildCost(*parent, result, name)
			if err != nil {
				return result, nil, err
			}
			if current.SettlementBPS == nil || current.SettlementSource == agencycontract.PriceSourceInherited || current.SettlementSource == agencycontract.PriceSourceDefault {
				if quote, configured := directChildCost(*parent, name); configured {
					cost = quote
				}
				current.SettlementSource = agencycontract.PriceSourceInherited
			}
			cost = max(cost, floor)
			set(name, "settlement_bps", &current.SettlementBPS, cost, "继承默认下级报价；合法单独报价保留，底线为上级成本与最低价差之和")
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
		effective, err := agencycontract.ApplyPlatformPolicyForAgency(result, platform, agency.ID)
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
