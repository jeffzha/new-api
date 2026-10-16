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
				{Revision: 1, DefaultSettlementBPS: 7000, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, ChildCostBPS: &quote, SalesBPS: &sale}}},
				{Revision: 1, DefaultSettlementBPS: 7000, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SalesBPS: &sale}}},
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
			plan, err := app.RepairPricing(ctx, "", 0)
			require.NoError(t, err)
			require.NotEmpty(t, plan.Changes)
			var count int64
			require.NoError(t, db.Model(&model.AgencyAuditLog{}).Count(&count).Error)
			assert.Zero(t, count, "dry run must never write")
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
		})
	}
}

func TestChildCostBoundaryExplainsDefaultMarkupAndManualQuote(t *testing.T) {
	cost, quote, childCost := 8000, 8500, 8000
	parent := agencycontract.Policy{DefaultSalesBPS: 10000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, ChildCostBPS: &quote, ChildCostSource: agencycontract.PriceSourceDefault}}}
	child := agencycontract.Policy{DefaultSalesBPS: 10000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &childCost}}}
	err := validateChildBoundary(parent, child)
	require.Error(t, err)
	assert.Contains(t, pricingErrorMessage(err), "0.8000 + 默认下级加价 0.0500（不是最低价差）")
	assert.Contains(t, pricingErrorMessage(err), "至少 0.8500")
	parent.ModelOverrides[0].ChildCostSource = agencycontract.PriceSourceManual
	assert.Contains(t, pricingErrorMessage(validateChildBoundary(parent, child)), "上级已设置的下级渠道报价")
}
