package agencyhub

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

type PricingSourceChange struct {
	AgencyID         int64  `json:"agency_id"`
	AgencyName       string `json:"agency_name"`
	Model            string `json:"model"`
	Field            string `json:"field"`
	Value            int    `json:"value_bps"`
	Before           string `json:"before_source"`
	After            string `json:"after_source"`
	EvidenceRevision int64  `json:"evidence_revision"`
}

type PricingSourceReport struct {
	PlanHash string                `json:"plan_hash"`
	Applied  bool                  `json:"applied"`
	Changes  []PricingSourceChange `json:"changes"`
	Skipped  []PricingSourceChange `json:"skipped_effective_price_changes"`
}

// RecoverPricingSources restores historical automatic provenance without changing
// stored or effective amounts. Legacy child creation requires its full generator shape.
func (a *App) RecoverPricingSources(ctx context.Context, expectedHash string, rootID int64) (PricingSourceReport, error) {
	report := PricingSourceReport{Changes: []PricingSourceChange{}, Skipped: []PricingSourceChange{}}
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
		if err := model.AgencyLockForUpdate(tx).Order("id ASC").Find(&agencies).Error; err != nil {
			return err
		}
		var versions []model.AgencyPricePolicyVersion
		if err := tx.Order("agency_id ASC, revision ASC").Find(&versions).Error; err != nil {
			return err
		}
		byAgency := map[int64][]model.AgencyPricePolicyVersion{}
		var platformHistory []model.AgencyPlatformPriceVersion
		if err := tx.Order("created_at_ms ASC, revision ASC").Find(&platformHistory).Error; err != nil {
			return err
		}
		rootAgencies := map[int64]bool{}
		for _, agency := range agencies {
			rootAgencies[agency.ID] = agency.ParentAgencyID == nil
		}
		var repairs []model.AgencyAuditLog
		if err := tx.Where("action = ? AND actor_type = ?", "pricing.repair", ActorTypeRoot).Find(&repairs).Error; err != nil {
			return err
		}
		for _, version := range versions {
			byAgency[version.AgencyID] = append(byAgency[version.AgencyID], version)
		}
		entries := []pricingRepairEntry{}
		for _, agency := range agencies {
			history := byAgency[agency.ID]
			if len(history) == 0 {
				return fmt.Errorf("agency %d has no policy history", agency.ID)
			}
			var current agencycontract.Policy
			found := false
			for _, version := range history {
				if version.ID == agency.CurrentPolicyVersionID {
					if err := common.UnmarshalJsonStr(version.PolicyJSON, &current); err != nil {
						return err
					}
					found = true
				}
			}
			if !found {
				return errors.New("current pricing version is missing")
			}
			current.Revision = agency.PriceRevision
			var generated *agencycontract.Policy
			first := history[0]
			if agency.ParentAgencyID != nil && first.Revision == 1 && first.CreatedByType == ActorTypeOperator {
				for _, parentVersion := range byAgency[*agency.ParentAgencyID] {
					if parentVersion.CreatedAtMS > first.CreatedAtMS {
						break
					}
					var policy agencycontract.Policy
					if err := common.UnmarshalJsonStr(parentVersion.PolicyJSON, &policy); err != nil {
						return err
					}
					generated = &policy
				}
				if generated != nil && rootAgencies[*agency.ParentAgencyID] {
					var historicalPlatform *agencycontract.PlatformPolicy
					for _, version := range platformHistory {
						if version.CreatedAtMS > first.CreatedAtMS {
							break
						}
						var policy agencycontract.PlatformPolicy
						if err := common.UnmarshalJsonStr(version.PolicyJSON, &policy); err != nil {
							return err
						}
						historicalPlatform = &policy
					}
					if historicalPlatform != nil {
						policy := legacySourceParentPolicy(*generated, *historicalPlatform)
						generated = &policy
					}
				}
			}
			after, changes, err := recoverAutomaticPriceSources(agency, current, history, generated, repairs...)
			if err != nil {
				return err
			}
			if agency.ParentAgencyID == nil {
				var skipped []PricingSourceChange
				after, changes, skipped, err = preserveEffectiveSourcePrices(current, after, platform, changes, agency.ID)
				if err != nil {
					return err
				}
				report.Skipped = append(report.Skipped, skipped...)
			}
			report.Changes = append(report.Changes, changes...)
			entries = append(entries, pricingRepairEntry{Agency: agency, Before: current, After: after})
		}
		report.PlanHash, err = agencycontract.CanonicalHash(struct {
			Entries  []pricingRepairEntry
			Changes  []PricingSourceChange
			Platform agencycontract.PlatformPolicy
		}{entries, report.Changes, platform})
		if err != nil || expectedHash == "" {
			return err
		}
		if expectedHash != report.PlanHash {
			return errors.New("pricing changed since dry run; export and review a new plan")
		}
		var root model.User
		if err := tx.Where("id = ? AND role >= ?", rootID, common.RoleRootUser).First(&root).Error; err != nil {
			return errors.New("a valid platform root actor is required")
		}
		for _, entry := range entries {
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
			now := time.Now()
			version := model.AgencyPricePolicyVersion{AgencyID: entry.Agency.ID, Revision: after.Revision, PolicyJSON: string(encoded), PolicyHash: hash, CreatedByType: ActorTypeRoot, CreatedByID: rootID, Reason: "恢复自动价格来源（金额不变）：" + report.PlanHash, CreatedAtMS: now.UnixMilli()}
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
			if err := recordAuditTx(tx, nil, &Identity{ActorType: ActorTypeRoot, ActorID: rootID}, "pricing.sources.recover", "agency", stringID(entry.Agency.ID), report.PlanHash, entry.Before, after); err != nil {
				return err
			}
		}
		report.Applied = true
		return nil
	})
	return report, err
}

func recoverAutomaticPriceSources(agency model.Agency, current agencycontract.Policy, history []model.AgencyPricePolicyVersion, creationParent *agencycontract.Policy, repairs ...model.AgencyAuditLog) (agencycontract.Policy, []PricingSourceChange, error) {
	encoded, err := common.Marshal(current)
	if err != nil {
		return current, nil, err
	}
	var after agencycontract.Policy
	if err := common.Unmarshal(encoded, &after); err != nil {
		return current, nil, err
	}
	type evidence struct {
		value    int
		source   string
		revision int64
	}
	known := map[string]evidence{}
	var previousRevision int64
	var previousPolicy agencycontract.Policy
	for _, version := range history {
		if version.Revision > current.Revision {
			break
		}
		if previousRevision != 0 && version.Revision != previousRevision+1 {
			clear(known)
			previousPolicy = agencycontract.Policy{}
		}
		previousRevision = version.Revision
		var policy agencycontract.Policy
		if err := common.UnmarshalJsonStr(version.PolicyJSON, &policy); err != nil {
			return current, nil, err
		}
		// The old generator wrote unmarked cost/sales pairs. Require its complete
		// creation shape and historical parent prices, not today's matching value.
		if version.Revision == 1 && version.CreatedByType == ActorTypeOperator && creationParent != nil && legacyGeneratedChildPolicy(policy, *creationParent) {
			for _, override := range policy.ModelOverrides {
				known[override.OriginModelName+":sales_bps"] = evidence{*override.SalesBPS, agencycontract.PriceSourceInherited, 1}
				known[override.OriginModelName+":settlement_bps"] = evidence{*override.SettlementBPS, agencycontract.PriceSourceInherited, 1}
			}
		}
		present := map[string]bool{}
		auditedRepair := false
		for _, audit := range repairs {
			if audit.Action != "pricing.repair" || audit.ActorType != ActorTypeRoot || audit.ObjectID != stringID(agency.ID) || audit.Reason == "" || version.Reason != "历史价格边界订正："+audit.Reason || version.CreatedByType != ActorTypeRoot || version.CreatedByID != audit.ActorID {
				continue
			}
			var before, repaired agencycontract.Policy
			if common.UnmarshalJsonStr(audit.BeforeJSON, &before) == nil && common.UnmarshalJsonStr(audit.AfterJSON, &repaired) == nil && reflect.DeepEqual(before, previousPolicy) && reflect.DeepEqual(repaired, policy) {
				auditedRepair = true
				break
			}
		}
		for _, override := range policy.ModelOverrides {
			for _, field := range []string{"sales_bps", "child_cost_bps", "settlement_bps"} {
				value, source := override.SalesBPS, override.SalesSource
				if field == "child_cost_bps" {
					value, source = override.ChildCostBPS, override.ChildCostSource
				} else if field == "settlement_bps" {
					value, source = override.SettlementBPS, override.SettlementSource
				}
				key := override.OriginModelName + ":" + field
				present[key] = true
				if value == nil {
					delete(known, key)
					continue
				}
				previous, exists := known[key]
				if source == agencycontract.PriceSourceDefault || source == agencycontract.PriceSourceInherited {
					known[key] = evidence{*value, source, version.Revision}
				} else if exists && auditedRepair && source == "" {
					previous.value = *value
					known[key] = previous
				} else if !exists || previous.value != *value {
					delete(known, key)
				}
			}
		}
		for key := range known {
			if !present[key] {
				delete(known, key)
			}
		}
		previousPolicy = policy
	}
	changes := []PricingSourceChange{}
	for i := range after.ModelOverrides {
		override := &after.ModelOverrides[i]
		for _, field := range []string{"sales_bps", "child_cost_bps", "settlement_bps"} {
			value, source := override.SalesBPS, &override.SalesSource
			if field == "child_cost_bps" {
				value, source = override.ChildCostBPS, &override.ChildCostSource
			} else if field == "settlement_bps" {
				value, source = override.SettlementBPS, &override.SettlementSource
			}
			proof, exists := known[override.OriginModelName+":"+field]
			if value == nil || !exists || *value != proof.value || *source == proof.source {
				continue
			}
			changes = append(changes, PricingSourceChange{AgencyID: agency.ID, AgencyName: agency.DisplayName, Model: override.OriginModelName, Field: field, Value: *value, Before: *source, After: proof.source, EvidenceRevision: proof.revision})
			*source = proof.source
		}
	}
	return after, changes, nil
}

// A provenance repair must not silently reprice a root policy when the live
// platform overlay recalculates automatic fields.
func preserveEffectiveSourcePrices(before, after agencycontract.Policy, platform agencycontract.PlatformPolicy, changes []PricingSourceChange, agencyIDs ...int64) (agencycontract.Policy, []PricingSourceChange, []PricingSourceChange, error) {
	var agencyID int64
	if len(agencyIDs) > 0 {
		agencyID = agencyIDs[0]
	}
	previous, err := agencycontract.ApplyPlatformPolicyForAgency(before, platform, agencyID)
	if err != nil {
		return before, nil, nil, err
	}
	kept, skipped := []PricingSourceChange{}, []PricingSourceChange{}
	for _, change := range changes {
		override := findModelOverride(after, change.Model)
		if change.Field == "sales_bps" {
			override.SalesSource = change.Before
		} else if change.Field == "settlement_bps" {
			override.SettlementSource = change.Before
		} else {
			override.ChildCostSource = change.Before
		}
	}
	for _, change := range changes {
		override := findModelOverride(after, change.Model)
		if change.Field == "sales_bps" {
			override.SalesSource = change.After
		} else if change.Field == "settlement_bps" {
			override.SettlementSource = change.After
		} else {
			override.ChildCostSource = change.After
		}
		candidate, applyErr := agencycontract.ApplyPlatformPolicyForAgency(after, platform, agencyID)
		oldPrice, oldErr := agencycontract.Resolve(previous, change.Model)
		newPrice, newErr := agencycontract.Resolve(candidate, change.Model)
		oldQuote, oldConfigured := directChildCost(previous, change.Model)
		newQuote, newConfigured := directChildCost(candidate, change.Model)
		priceChanged := oldPrice.SalesBPS != newPrice.SalesBPS || oldPrice.SettlementBPS != newPrice.SettlementBPS || oldQuote != newQuote || oldConfigured != newConfigured
		if applyErr != nil || oldErr != nil || newErr != nil || priceChanged {
			if change.Field == "sales_bps" {
				override.SalesSource = change.Before
			} else if change.Field == "settlement_bps" {
				override.SettlementSource = change.Before
			} else {
				override.ChildCostSource = change.Before
			}
			skipped = append(skipped, change)
			continue
		}
		kept = append(kept, change)
	}
	return after, kept, skipped, nil
}

func legacyGeneratedChildPolicy(child, parent agencycontract.Policy) bool {
	// This infers old generator output, not authorship of individual API inputs.
	if len(child.ModelOverrides) == 0 || child.DefaultChildCostBPS != 0 {
		return false
	}
	for _, override := range child.ModelOverrides {
		if override.SettlementBPS == nil || override.SalesBPS == nil || override.ChildCostBPS != nil || override.SettlementSource != "" || override.SalesSource != "" || override.ChildCostSource != "" {
			return false
		}
		quote, configured := directChildCost(parent, override.OriginModelName)
		parentPrice, err := agencycontract.Resolve(parent, override.OriginModelName)
		if err != nil || !configured || quote != *override.SettlementBPS {
			return false
		}
		sale, err := inheritedSalesBPS(parentPrice.SalesBPS, quote, child.MinSpreadBPS, child.SalesCapBPS)
		if err != nil || sale != *override.SalesBPS {
			return false
		}
	}
	return true
}

// Reproduce the historical overlay without today's default generation rules.
func legacySourceParentPolicy(parent agencycontract.Policy, platform agencycontract.PlatformPolicy) agencycontract.Policy {
	result := parent
	result.ModelOverrides = append([]agencycontract.ModelOverride(nil), parent.ModelOverrides...)
	for _, price := range platform.ModelPrices {
		cost, sale := price.AgencyCostBPS, price.DefaultSalesBPS
		if override := findModelOverride(result, price.OriginModelName); override != nil {
			override.SettlementBPS = &cost
			if override.SalesBPS == nil {
				override.SalesBPS = &sale
			}
		} else {
			result.ModelOverrides = append(result.ModelOverrides, agencycontract.ModelOverride{OriginModelName: price.OriginModelName, SettlementBPS: &cost, SalesBPS: &sale})
		}
	}
	return result
}
