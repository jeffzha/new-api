package agencyhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
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
			runChildCostEditRegression(t, app)
		})
	}
}

func runChildCostEditRegression(t *testing.T, app *App) {
	t.Helper()
	platform, err := model.LoadAgencyPlatformPolicy(app.db)
	require.NoError(t, err)
	platform.ModelPrices[0].ChannelCosts[0].PlatformCostBPS = 4000
	platform.ModelPrices[0].AgencyCostBPS = 4500
	platform.ModelPrices[0].DefaultSalesBPS = 5500
	encodedPlatform, err := common.Marshal(platform)
	require.NoError(t, err)
	require.NoError(t, app.db.Model(&model.AgencyPlatformPriceVersion{}).Where("revision = ?", platform.Revision).Update("policy_json", string(encodedPlatform)).Error)
	cost, quote, sale, otherCost := 4500, 7000, 5500, 6000
	parentPolicy := agencycontract.Policy{Revision: 1, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SettlementSource: agencycontract.PriceSourceManual, ChildCostBPS: &quote, SalesBPS: &sale}}}
	childCost, childQuote, childSale := 7000, 7500, 7000
	childPolicy := agencycontract.Policy{Revision: 1, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &childCost, ChildCostBPS: &childQuote, ChildCostSource: agencycontract.PriceSourceDefault, SalesBPS: &childSale, SalesSource: agencycontract.PriceSourceInherited}, {OriginModelName: "untouched-model", SettlementBPS: &otherCost}}}
	parent := model.Agency{Code: "edit-parent", InviteCode: "EDITP", DisplayName: "父代理", Depth: 1, Status: AgencyStatusActive, PriceRevision: 1, Version: 1}
	require.NoError(t, app.db.Create(&parent).Error)
	child := model.Agency{Code: "edit-child", InviteCode: "EDITC", DisplayName: "子代理", Depth: 2, ParentAgencyID: &parent.ID, Status: AgencyStatusActive, PriceRevision: 1, Version: 1}
	require.NoError(t, app.db.Create(&child).Error)
	for i, agency := range []*model.Agency{&parent, &child} {
		encoded, err := common.Marshal([]agencycontract.Policy{parentPolicy, childPolicy}[i])
		require.NoError(t, err)
		version := model.AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: 1, PolicyJSON: string(encoded)}
		require.NoError(t, app.db.Create(&version).Error)
		require.NoError(t, app.db.Model(agency).Update("current_policy_version_id", version.ID).Error)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/children/"+stringID(child.ID)+"/pricing", strings.NewReader(`{"expected_revision":1,"model_cost_overrides":[{"origin_model_name":"model-a","settlement_bps":7600}]}`))
	ctx.Params = gin.Params{{Key: "id", Value: stringID(child.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1, AgencyID: &parent.ID})
	app.publishChildPricing(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	_, actual, err := app.loadAgencyPolicy(child.ID)
	require.NoError(t, err)
	changed := findModelOverride(actual, "model-a")
	assert.Equal(t, 7600, *changed.SettlementBPS)
	assert.Equal(t, 8100, *changed.ChildCostBPS)
	assert.Equal(t, 7600, *changed.SalesBPS)
	assert.Equal(t, childPolicy.ModelOverrides[1], *findModelOverride(actual, "untouched-model"))
	// Source restoration is a separate, amount-preserving, audited operation.
	changed.ChildCostSource = agencycontract.PriceSourceManual
	actual.ModelOverrides[0] = *changed
	encoded, err := common.Marshal(actual)
	require.NoError(t, err)
	var live model.Agency
	require.NoError(t, app.db.First(&live, child.ID).Error)
	require.NoError(t, app.db.Model(&model.AgencyPricePolicyVersion{}).Where("id = ?", live.CurrentPolicyVersionID).Update("policy_json", string(encoded)).Error)
	// A genuine manual amount must not be replaced on the next cost edit.
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/children/"+stringID(child.ID)+"/pricing", strings.NewReader(`{"expected_revision":2,"model_cost_overrides":[{"origin_model_name":"model-a","settlement_bps":8200}]}`))
	ctx.Params = gin.Params{{Key: "id", Value: stringID(child.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1, AgencyID: &parent.ID})
	app.publishChildPricing(ctx)
	assert.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "子代理")
	assert.Contains(t, recorder.Body.String(), "我的下级渠道价")
	assert.Contains(t, recorder.Body.String(), "0.8100")
	assert.Contains(t, recorder.Body.String(), "0.8200")
	_, unchanged, err := app.loadAgencyPolicy(child.ID)
	require.NoError(t, err)
	assert.Equal(t, actual, unchanged)
	// Simulate the old full-table save changing only a source marker.
	automaticQuote := 5000
	parentPolicy.ModelOverrides[0].ChildCostBPS = &automaticQuote
	var parentVersion model.AgencyPricePolicyVersion
	require.NoError(t, app.db.Where("agency_id = ? AND revision = ?", parent.ID, 1).First(&parentVersion).Error)
	parentPolicy.ModelOverrides[0].ChildCostSource = agencycontract.PriceSourceDefault
	encoded, err = common.Marshal(parentPolicy)
	require.NoError(t, err)
	require.NoError(t, app.db.Model(&parentVersion).Update("policy_json", string(encoded)).Error)
	parentPolicy.Revision = 2
	parentPolicy.ModelOverrides[0].ChildCostSource = agencycontract.PriceSourceManual
	encoded, err = common.Marshal(parentPolicy)
	require.NoError(t, err)
	version := model.AgencyPricePolicyVersion{AgencyID: parent.ID, Revision: 2, PolicyJSON: string(encoded)}
	require.NoError(t, app.db.Create(&version).Error)
	require.NoError(t, app.db.Model(&parent).Updates(map[string]any{"current_policy_version_id": version.ID, "price_revision": 2}).Error)
	plan, err := app.RecoverPricingSources(context.Background(), "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Changes)
	_, err = app.RecoverPricingSources(context.Background(), "stale", 1)
	require.ErrorContains(t, err, "changed since dry run")
	_, err = app.RecoverPricingSources(context.Background(), plan.PlanHash, 0)
	require.ErrorContains(t, err, "root actor")
	var root model.User
	require.NoError(t, app.db.Where("role >= ?", common.RoleRootUser).First(&root).Error)
	applied, err := app.RecoverPricingSources(context.Background(), plan.PlanHash, int64(root.Id))
	require.NoError(t, err)
	assert.True(t, applied.Applied)
	_, recovered, err := app.loadAgencyPolicy(parent.ID)
	require.NoError(t, err)
	assert.Equal(t, automaticQuote, *recovered.ModelOverrides[0].ChildCostBPS)
	assert.Equal(t, agencycontract.PriceSourceDefault, recovered.ModelOverrides[0].ChildCostSource)
	next, err := app.RecoverPricingSources(context.Background(), "", 0)
	require.NoError(t, err)
	assert.Empty(t, next.Changes)
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

func TestAutomaticPriceSourceHistory(t *testing.T) {
	t.Run("historical platform overlay preserves generated child defaults", func(t *testing.T) {
		cost, sale, quote := 4500, 5500, 7000
		parent := agencycontract.Policy{DefaultSalesBPS: 10000, DefaultChildCostBPS: quote, SalesCapBPS: 30000}
		platform := agencycontract.PlatformPolicy{ModelPrices: []agencycontract.PlatformModelPrice{{OriginModelName: "model-a", AgencyCostBPS: cost, DefaultSalesBPS: sale}}}
		parent = legacySourceParentPolicy(parent, platform)
		child := agencycontract.Policy{Revision: 1, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &quote, SalesBPS: &quote}}}
		encoded, err := common.Marshal(child)
		require.NoError(t, err)
		result, changes, err := recoverAutomaticPriceSources(model.Agency{ID: 2}, child, []model.AgencyPricePolicyVersion{{Revision: 1, CreatedByType: ActorTypeOperator, PolicyJSON: string(encoded)}}, &parent)
		require.NoError(t, err)
		require.Len(t, changes, 2)
		assert.Equal(t, quote, *result.ModelOverrides[0].SettlementBPS)
		assert.Equal(t, quote, *result.ModelOverrides[0].SalesBPS)
		assert.Equal(t, agencycontract.PriceSourceInherited, result.ModelOverrides[0].SettlementSource)
		assert.Equal(t, agencycontract.PriceSourceInherited, result.ModelOverrides[0].SalesSource)
		child.ModelOverrides[0].SettlementSource = agencycontract.PriceSourceManual
		assert.False(t, legacyGeneratedChildPolicy(child, parent), "explicit manual provenance must never be inferred as generated")
	})
	t.Run("cost source restoration does not overwrite quote provenance", func(t *testing.T) {
		cost, quote, sale := 4500, 5000, 6000
		before := agencycontract.Policy{DefaultSalesBPS: 10000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SettlementSource: agencycontract.PriceSourceManual, ChildCostBPS: &quote, ChildCostSource: agencycontract.PriceSourceManual, SalesBPS: &sale, SalesSource: agencycontract.PriceSourceManual}}}
		after := before
		after.ModelOverrides = append([]agencycontract.ModelOverride(nil), before.ModelOverrides...)
		after.ModelOverrides[0].SettlementSource = agencycontract.PriceSourceInherited
		platform := agencycontract.PlatformPolicy{ModelPrices: []agencycontract.PlatformModelPrice{{OriginModelName: "model-a", PlatformCostBPS: 4000, AgencyCostBPS: cost, DefaultSalesBPS: sale}}}
		changes := []PricingSourceChange{{Model: "model-a", Field: "settlement_bps", Before: agencycontract.PriceSourceManual, After: agencycontract.PriceSourceInherited}}
		result, kept, skipped, err := preserveEffectiveSourcePrices(before, after, platform, changes)
		require.NoError(t, err)
		assert.Equal(t, changes, kept)
		assert.Empty(t, skipped)
		assert.Equal(t, agencycontract.PriceSourceInherited, result.ModelOverrides[0].SettlementSource)
		assert.Equal(t, agencycontract.PriceSourceManual, result.ModelOverrides[0].ChildCostSource)
	})
	t.Run("audited system repair preserves automatic lineage only", func(t *testing.T) {
		cost, sale, raised := 7000, 9000, 9300
		initial := agencycontract.Policy{Revision: 1, DefaultSalesBPS: 10000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SalesBPS: &sale, SalesSource: agencycontract.PriceSourceInherited}}}
		current := initial
		current.Revision = 2
		current.ModelOverrides = append([]agencycontract.ModelOverride(nil), initial.ModelOverrides...)
		current.ModelOverrides[0].SalesBPS = &raised
		current.ModelOverrides[0].SalesSource = ""
		beforeJSON, err := common.Marshal(initial)
		require.NoError(t, err)
		afterJSON, err := common.Marshal(current)
		require.NoError(t, err)
		history := []model.AgencyPricePolicyVersion{{Revision: 1, PolicyJSON: string(beforeJSON)}, {Revision: 2, PolicyJSON: string(afterJSON), CreatedByType: ActorTypeRoot, CreatedByID: 1, Reason: "历史价格边界订正：repair-hash"}}
		audit := model.AgencyAuditLog{Action: "pricing.repair", ActorType: ActorTypeRoot, ActorID: 1, ObjectID: "1", Reason: "repair-hash", BeforeJSON: string(beforeJSON), AfterJSON: string(afterJSON)}
		for _, valid := range []bool{false, true} {
			candidateAudit := audit
			if !valid {
				candidateAudit.ObjectID = "2"
			}
			result, changes, err := recoverAutomaticPriceSources(model.Agency{ID: 1}, current, history, nil, candidateAudit)
			require.NoError(t, err)
			assert.Equal(t, raised, *result.ModelOverrides[0].SalesBPS)
			if valid {
				assert.Len(t, changes, 1)
				assert.Equal(t, agencycontract.PriceSourceInherited, result.ModelOverrides[0].SalesSource)
			} else {
				assert.Empty(t, changes)
				assert.Equal(t, "", result.ModelOverrides[0].SalesSource)
			}
		}
	})
	t.Run("history gaps cannot prove automatic provenance", func(t *testing.T) {
		cost, sale := 7000, 10000
		initial := agencycontract.Policy{Revision: 1, DefaultSalesBPS: 10000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SalesBPS: &sale, SalesSource: agencycontract.PriceSourceDefault}}}
		current := initial
		current.Revision = 3
		current.ModelOverrides = append([]agencycontract.ModelOverride(nil), initial.ModelOverrides...)
		current.ModelOverrides[0].SalesSource = agencycontract.PriceSourceManual
		beforeJSON, err := common.Marshal(initial)
		require.NoError(t, err)
		afterJSON, err := common.Marshal(current)
		require.NoError(t, err)
		result, changes, err := recoverAutomaticPriceSources(model.Agency{ID: 1}, current, []model.AgencyPricePolicyVersion{{Revision: 1, PolicyJSON: string(beforeJSON)}, {Revision: 3, PolicyJSON: string(afterJSON)}}, nil)
		require.NoError(t, err)
		assert.Empty(t, changes)
		assert.Equal(t, current, result)
	})
	t.Run("source recovery cannot silently change effective prices", func(t *testing.T) {
		cost, quote, sale := 4500, 7000, 6000
		before := agencycontract.Policy{DefaultSalesBPS: 10000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, ChildCostBPS: &quote, ChildCostSource: agencycontract.PriceSourceManual, SalesBPS: &sale, SalesSource: agencycontract.PriceSourceManual}}}
		after := before
		after.ModelOverrides = append([]agencycontract.ModelOverride(nil), before.ModelOverrides...)
		after.ModelOverrides[0].ChildCostSource = agencycontract.PriceSourceDefault
		platform := agencycontract.PlatformPolicy{ModelPrices: []agencycontract.PlatformModelPrice{{OriginModelName: "model-a", PlatformCostBPS: 4000, AgencyCostBPS: cost, DefaultSalesBPS: 5500}}}
		changes := []PricingSourceChange{{Model: "model-a", Field: "child_cost_bps", Before: agencycontract.PriceSourceManual, After: agencycontract.PriceSourceDefault}}
		result, kept, skipped, err := preserveEffectiveSourcePrices(before, after, platform, changes)
		require.NoError(t, err)
		assert.Empty(t, kept)
		assert.Equal(t, changes, skipped)
		assert.Equal(t, before, result)
	})
	cost, quote, sale := 7000, 7500, 7000
	parent := agencycontract.Policy{DefaultSalesBPS: 5500, DefaultChildCostBPS: cost, SalesCapBPS: 30000}
	for _, tc := range []struct {
		name          string
		initialSource string
		changed       bool
		generated     bool
		want          string
	}{
		{"unchanged default relabelled", agencycontract.PriceSourceDefault, false, false, agencycontract.PriceSourceDefault},
		{"real manual edit retained", agencycontract.PriceSourceDefault, true, false, agencycontract.PriceSourceManual},
		{"unknown legacy retained", "", false, false, agencycontract.PriceSourceManual},
		{"old generated child sales", "", false, true, agencycontract.PriceSourceInherited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := agencycontract.Policy{Revision: 1, DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "model-a", SettlementBPS: &cost, SalesBPS: &sale, SalesSource: tc.initialSource}}}
			current := initial
			current.Revision = 2
			current.ModelOverrides = append([]agencycontract.ModelOverride(nil), initial.ModelOverrides...)
			current.ModelOverrides[0].SalesSource = agencycontract.PriceSourceManual
			current.ModelOverrides[0].ChildCostBPS = &quote
			if tc.changed {
				value := 8500
				current.ModelOverrides[0].SalesBPS = &value
			}
			beforeJSON, err := common.Marshal(initial)
			require.NoError(t, err)
			afterJSON, err := common.Marshal(current)
			require.NoError(t, err)
			history := []model.AgencyPricePolicyVersion{{Revision: 1, PolicyJSON: string(beforeJSON), CreatedByType: ActorTypeOperator}, {Revision: 2, PolicyJSON: string(afterJSON)}}
			var creationParent *agencycontract.Policy
			if tc.generated {
				creationParent = &parent
			}
			result, _, err := recoverAutomaticPriceSources(model.Agency{ID: 1}, current, history, creationParent)
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.ModelOverrides[0].SalesSource)
			assert.Equal(t, *current.ModelOverrides[0].SalesBPS, *result.ModelOverrides[0].SalesBPS)
			assert.Equal(t, *current.ModelOverrides[0].ChildCostBPS, *result.ModelOverrides[0].ChildCostBPS)
		})
	}
}
