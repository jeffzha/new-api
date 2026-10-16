package agencyhub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPricingRepairAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			if dialect == "sqlite" {
				driver = sqlite.Open(filepath.Join(t.TempDir(), "repair.sqlite"))
			} else {
				dsn := os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("external database not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Agency{}, &model.AgencyPricePolicyVersion{}, &model.AgencyPricePolicyItem{}, &model.AgencyPlatformPriceState{}, &model.AgencyPlatformPriceVersion{}, &model.AgencyAuditLog{}, &model.AgencyCustomerSalesOverride{}))
			db = db.Begin()
			require.NoError(t, db.Error)
			t.Cleanup(func() { require.NoError(t, db.Rollback().Error) })
			app := New(db, db, Config{})
			actor := model.User{Username: "repair-root", Role: common.RoleRootUser}
			require.NoError(t, db.Create(&actor).Error)
			seedConfirmedPlatformPricing(t, app, agencycontract.PlatformModelPrice{OriginModelName: "model-a", ChannelCosts: []agencycontract.PlatformChannelCost{{ChannelID: 1, PlatformCostBPS: 7500}}, AgencyCostBPS: 8000, DefaultSalesBPS: 10000})
			root := model.Agency{Code: "repair-parent", InviteCode: "REPAIRP", DisplayName: "Parent", Depth: 1, PriceRevision: 1, Version: 1}
			require.NoError(t, db.Create(&root).Error)
			child := model.Agency{Code: "repair-child", InviteCode: "REPAIRC", DisplayName: "Child", Depth: 2, ParentAgencyID: &root.ID, PriceRevision: 1, Version: 1}
			require.NoError(t, db.Create(&child).Error)
			grandchild := model.Agency{Code: "repair-grandchild", InviteCode: "REPAIRG", DisplayName: "Grandchild", Depth: 3, ParentAgencyID: &child.ID, PriceRevision: 1, Version: 1}
			require.NoError(t, db.Create(&grandchild).Error)
			cost, sale, quote := 8000, 9000, 9700
			policies := []agencycontract.Policy{
				{Revision: 1, DefaultSettlementBPS: 7000, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SalesBPS: &sale}}},
				{Revision: 1, DefaultSettlementBPS: 7000, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SettlementSource: agencycontract.PriceSourceInherited, ChildCostBPS: &quote, SalesBPS: &sale}}},
				{Revision: 1, DefaultSettlementBPS: 7000, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SettlementSource: agencycontract.PriceSourceInherited, SalesBPS: &sale}}},
			}
			agencies := []*model.Agency{&root, &child, &grandchild}
			for i, agency := range agencies {
				encoded, err := common.Marshal(policies[i])
				require.NoError(t, err)
				version := model.AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: 1, PolicyJSON: string(encoded)}
				require.NoError(t, db.Create(&version).Error)
				agency.CurrentPolicyVersionID = version.ID
				require.NoError(t, db.Model(agency).Update("current_policy_version_id", version.ID).Error)
			}
			key, err := agencycontract.ModelKey("model-a")
			require.NoError(t, err)
			customers := []model.AgencyCustomerSalesOverride{
				{AgencyID: child.ID, UserID: 101, OriginModelName: "model-a", ModelKey: key, SalesBPS: 8500, Revision: 1},
				{AgencyID: grandchild.ID, UserID: 102, OriginModelName: "model-a", ModelKey: key, SalesBPS: 9000, Revision: 1},
			}
			require.NoError(t, db.Create(&customers).Error)
			ctx := context.Background()
			var auditsBefore int64
			require.NoError(t, db.Model(&model.AgencyAuditLog{}).Count(&auditsBefore).Error)
			plan, err := app.RepairPricing(ctx, "", 0)
			require.NoError(t, err)
			require.NotEmpty(t, plan.Changes)
			var count int64
			require.NoError(t, db.Model(&model.AgencyAuditLog{}).Count(&count).Error)
			assert.Equal(t, auditsBefore, count, "dry run must never write")
			_, err = app.RepairPricing(ctx, "stale-hash", int64(actor.Id))
			require.ErrorContains(t, err, "changed since dry run")
			_, err = app.RepairPricing(ctx, plan.PlanHash, 0)
			require.ErrorContains(t, err, "root actor")
			applied, err := app.RepairPricing(ctx, plan.PlanHash, int64(actor.Id))
			require.NoError(t, err)
			assert.True(t, applied.Applied)
			_, parentPolicy, err := app.loadAgencyPolicy(root.ID)
			require.NoError(t, err)
			assert.Equal(t, 9000, *findModelOverride(parentPolicy, "model-a").SalesBPS, "valid explicit sale is preserved")
			_, childPolicy, err := app.loadAgencyPolicy(child.ID)
			require.NoError(t, err)
			assert.Equal(t, 8500, *findModelOverride(childPolicy, "model-a").SettlementBPS)
			assert.Equal(t, 9700, *findModelOverride(childPolicy, "model-a").ChildCostBPS, "manual quote is preserved")
			_, grandPolicy, err := app.loadAgencyPolicy(grandchild.ID)
			require.NoError(t, err)
			assert.Equal(t, 9700, *findModelOverride(grandPolicy, "model-a").SettlementBPS)
			assert.Equal(t, 9700, *findModelOverride(grandPolicy, "model-a").SalesBPS)
			var exact model.AgencyCustomerSalesOverride
			require.NoError(t, db.First(&exact, customers[0].ID).Error)
			assert.Equal(t, 8500, exact.SalesBPS, "exact-cost customer exception remains legal")
			next, err := app.RepairPricing(ctx, "", 0)
			require.NoError(t, err)
			assert.Empty(t, next.Changes, "repair must be idempotent")
			appliedAgain, err := app.RepairPricing(ctx, next.PlanHash, int64(actor.Id))
			require.NoError(t, err)
			assert.Empty(t, appliedAgain.Changes)
			var versions int64
			require.NoError(t, db.Model(&model.AgencyPricePolicyVersion{}).Count(&versions).Error)
			assert.EqualValues(t, 6, versions, "no new versions on a no-op apply")
			require.NoError(t, db.Model(&model.Agency{}).Where("id = ?", root.ID).Update("price_revision", 3).Error)
			require.ErrorContains(t, app.RollbackPricingRepair(ctx, plan.PlanHash, int64(actor.Id)), "edited after repair")
			require.NoError(t, db.Model(&model.Agency{}).Where("id = ?", root.ID).Update("price_revision", 2).Error)
			require.NoError(t, app.RollbackPricingRepair(ctx, plan.PlanHash, int64(actor.Id)))
			for i, agency := range agencies {
				_, restored, err := app.loadAgencyPolicy(agency.ID)
				require.NoError(t, err)
				restored.Revision = 1
				assert.Equal(t, policies[i], restored, "rollback restores values without rewinding revisions")
			}
			var restoredCustomer model.AgencyCustomerSalesOverride
			require.NoError(t, db.First(&restoredCustomer, customers[1].ID).Error)
			assert.Equal(t, 9000, restoredCustomer.SalesBPS)
			require.ErrorContains(t, app.RollbackPricingRepair(ctx, plan.PlanHash, int64(actor.Id)), "already rolled back")
			baseline, err := app.RepairPricing(ctx, "", 0)
			require.NoError(t, err)
			_, err = app.RepairPricing(ctx, baseline.PlanHash, int64(actor.Id))
			require.NoError(t, err)
			// Model an old repair that raised a valid negotiated price from 0.82
			// to the default quote 0.85. Restore through the real transaction.
			_, current, err := app.loadAgencyPolicy(child.ID)
			require.NoError(t, err)
			oldJSON, err := common.Marshal(current)
			require.NoError(t, err)
			var original agencycontract.Policy
			require.NoError(t, common.Unmarshal(oldJSON, &original))
			originalCost, repairedCost := 8200, 8500
			findModelOverride(original, "model-a").SettlementBPS = &originalCost
			findModelOverride(original, "model-a").SettlementSource = agencycontract.PriceSourceManual
			findModelOverride(current, "model-a").SettlementBPS = &repairedCost
			findModelOverride(current, "model-a").SettlementSource = agencycontract.PriceSourceManual
			beforeJSON, err := common.Marshal(original)
			require.NoError(t, err)
			afterJSON, err := common.Marshal(current)
			require.NoError(t, err)
			var live model.Agency
			require.NoError(t, db.First(&live, child.ID).Error)
			require.NoError(t, db.Model(&model.AgencyPricePolicyVersion{}).Where("id = ?", live.CurrentPolicyVersionID).Update("policy_json", string(afterJSON)).Error)
			require.NoError(t, db.Create(&model.AgencyAuditLog{Action: "pricing.repair", ObjectID: stringID(child.ID), Reason: "old-default-floor", BeforeJSON: string(beforeJSON), AfterJSON: string(afterJSON)}).Error)
			restorePlan, err := app.RestoreNegotiatedPricing(ctx, "old-default-floor", "", 0)
			require.NoError(t, err)
			require.Len(t, restorePlan.RestorationReview, 1)
			assert.True(t, restorePlan.RestorationReview[0].Restored)
			_, err = app.RestoreNegotiatedPricing(ctx, "old-default-floor", "stale", int64(actor.Id))
			require.ErrorContains(t, err, "changed since dry run")
			_, err = app.RestoreNegotiatedPricing(ctx, "old-default-floor", restorePlan.PlanHash, 0)
			require.ErrorContains(t, err, "root actor")
			restoredPlan, err := app.RestoreNegotiatedPricing(ctx, "old-default-floor", restorePlan.PlanHash, int64(actor.Id))
			require.NoError(t, err)
			assert.True(t, restoredPlan.Applied)
			_, finalPolicy, err := app.loadAgencyPolicy(child.ID)
			require.NoError(t, err)
			assert.Equal(t, 8200, *findModelOverride(finalPolicy, "model-a").SettlementBPS)
			again, err := app.RestoreNegotiatedPricing(ctx, "old-default-floor", "", 0)
			require.NoError(t, err)
			assert.Empty(t, again.Changes)
			assert.False(t, again.RestorationReview[0].Restored)
		})
	}
}

func TestNegotiatedChildCostBoundaryAndPropagation(t *testing.T) {
	t.Run("deep minimum spread validates earlier negotiated edges", func(t *testing.T) {
		parentID, childID := int64(1), int64(2)
		agencies := []model.Agency{{ID: parentID, DisplayName: "A"}, {ID: childID, DisplayName: "B", ParentAgencyID: &parentID}, {ID: 3, DisplayName: "C", ParentAgencyID: &childID}}
		costA, costB, costC := 8000, 8200, 9000
		policies := map[int64]agencycontract.Policy{
			1: {DefaultSalesBPS: 10000, MinSpreadBPS: 200, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &costA}}},
			2: {DefaultSalesBPS: 10000, MinSpreadBPS: 200, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &costB}}},
			3: {DefaultSalesBPS: 10000, MinSpreadBPS: 500, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &costC}}},
		}
		platform := agencycontract.PlatformPolicy{ModelPrices: []agencycontract.PlatformModelPrice{{OriginModelName: "model-a", PlatformCostBPS: 7500, AgencyCostBPS: costA, DefaultSalesBPS: 10000}}}
		err := validatePricingBillingPaths(agencies, policies, platform)
		require.ErrorContains(t, err, "A → B → C")
		require.ErrorContains(t, err, "0.8500")
		previous := make(map[int64]agencycontract.Policy, len(policies))
		for id, policy := range policies {
			previous[id] = policy
		}
		require.NoError(t, validateChangedPricingBillingPaths(agencies, policies, platform, previous, platform), "unchanged conflict must not block unrelated edits")
		changed := policies[3]
		changed.MinSpreadBPS = 600
		policies[3] = changed
		require.ErrorContains(t, validateChangedPricingBillingPaths(agencies, policies, platform, previous, platform), "0.8600")
		policies[3] = previous[3]
		costB = 8500
		require.NoError(t, validatePricingBillingPaths(agencies, policies, platform))
	})
	for _, tc := range []struct {
		name               string
		original, revision int
		restore            bool
	}{{"legal discount", 8200, 2, true}, {"below actual floor", 8100, 2, false}, {"later edit", 8200, 3, false}} {
		t.Run(tc.name, func(t *testing.T) {
			cost, quote, raised := 8000, 8500, 8500
			parent := agencycontract.Policy{DefaultSalesBPS: 10000, MinSpreadBPS: 200, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, ChildCostBPS: &quote}}}
			original := agencycontract.Policy{Revision: 1, DefaultSalesBPS: 10000, MinSpreadBPS: 200, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &tc.original, SettlementSource: agencycontract.PriceSourceManual}}}
			repaired := agencycontract.Policy{Revision: 2, DefaultSalesBPS: 10000, MinSpreadBPS: 200, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &raised, SettlementSource: agencycontract.PriceSourceInherited}}}
			beforeJSON, err := common.Marshal(original)
			require.NoError(t, err)
			afterJSON, err := common.Marshal(repaired)
			require.NoError(t, err)
			current := repaired
			current.Revision = int64(tc.revision)
			result, reviews, changes, err := restoreNegotiatedFields(model.Agency{ID: 2}, current, &parent, model.AgencyAuditLog{BeforeJSON: string(beforeJSON), AfterJSON: string(afterJSON)})
			require.NoError(t, err)
			require.Len(t, reviews, 1)
			assert.Equal(t, tc.restore, reviews[0].Restored)
			if tc.restore {
				assert.Equal(t, tc.original, *result.ModelOverrides[0].SettlementBPS)
				assert.Equal(t, agencycontract.PriceSourceManual, result.ModelOverrides[0].SettlementSource)
				assert.Len(t, changes, 1)
			} else {
				assert.Equal(t, raised, *result.ModelOverrides[0].SettlementBPS)
				assert.Empty(t, changes)
			}
		})
	}
	for _, source := range []string{"", agencycontract.PriceSourceManual, agencycontract.PriceSourceDefault} {
		t.Run(string(source), func(t *testing.T) {
			cost, quote, childCost := 8000, 8500, 8200
			parent := agencycontract.Policy{DefaultSalesBPS: 10000, MinSpreadBPS: 200, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, ChildCostBPS: &quote, ChildCostSource: source}}}
			child := agencycontract.Policy{DefaultSettlementBPS: 200, DefaultSalesBPS: 10000, MinSpreadBPS: 200, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &childCost, SettlementSource: agencycontract.PriceSourceManual}}}
			require.NoError(t, validateChildBoundary(parent, child), "negotiated cost below the default quote is legal")
			synchronized, _, err := synchronizeChildCostPolicy(parent, child)
			require.NoError(t, err)
			assert.Equal(t, 8200, *findModelOverride(synchronized, "model-a").SettlementBPS)
			quote = 9000
			synchronized, _, err = synchronizeChildCostPolicy(parent, synchronized)
			require.NoError(t, err)
			assert.Equal(t, 8200, *findModelOverride(synchronized, "model-a").SettlementBPS, "new default quote must not overwrite negotiated cost")
			sale := 10000
			commission, err := agencycontract.CalculateTieredCommission(10000, []agencycontract.TierNode{{AgencyID: 1, CostBPS: cost, MinSpreadBPS: 200}, {AgencyID: 2, CostBPS: childCost, MinSpreadBPS: 200, SalesBPS: &sale}}, 7500, 0, 10000, true)
			require.NoError(t, err)
			assert.EqualValues(t, 10000, commission.PlatformCostQuota+commission.PlatformMarginQuota+commission.TotalCommissionQuota)
			childCost = 8100
			err = validateChildBoundary(parent, child)
			require.Error(t, err)
			assert.Contains(t, pricingErrorMessage(err), "最低允许 0.8200")
			assert.Contains(t, pricingErrorMessage(err), "0.8000 + 适用最低价差 0.0200")
			assert.Contains(t, pricingErrorMessage(err), "管理下级成本")
		})
	}
}
