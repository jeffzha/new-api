package agencyhub

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMultilevelAgencyMigrationBackfillsLegacyDepth(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-depth-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateAgency(db))
	agency := model.Agency{Code: "legacy", DisplayName: "Legacy", Status: AgencyStatusActive, InviteCode: "LEGACY", Depth: 0, PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&agency).Error)
	require.NoError(t, model.MigrateAgency(db))
	var got model.Agency
	require.NoError(t, db.First(&got, agency.ID).Error)
	require.Equal(t, 1, got.Depth)
}

func TestCustomerSalesOverridePersistsModelSpecificValue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-override-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateAgency(db))
	agency := model.Agency{Code: "override", DisplayName: "Override", Status: AgencyStatusActive, InviteCode: "OVERRIDE", Depth: 1, PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&agency).Error)
	modelName := "model-a"
	key, err := agencycontract.ModelKey(modelName)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyCustomerSalesOverride{AgencyID: agency.ID, UserID: 7, ModelKey: key, OriginModelName: modelName, SalesBPS: 9100, Revision: 1, CreatedByType: ActorTypeOperator, CreatedByID: 2, CreatedAtMS: 1, UpdatedAtMS: 1}).Error)
	var override model.AgencyCustomerSalesOverride
	require.NoError(t, db.Where("agency_id = ? AND user_id = ? AND model_key = ?", agency.ID, 7, key).First(&override).Error)
	require.Equal(t, 9100, override.SalesBPS)
}

func TestCustomerSalesPricingSeparatesInheritedAndCustomerOverride(t *testing.T) {
	app := newAgencyTestApp(t)
	modelName := "model-a"
	agencySales := 9200
	policy := agencycontract.Policy{
		DefaultSettlementBPS: 7000,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
		ModelOverrides:       []agencycontract.ModelOverride{{OriginModelName: modelName, SalesBPS: &agencySales}},
	}
	agency, _, err := app.CreateAgency(1, "Pricing display", "pricing-display", policy)
	require.NoError(t, err)
	modelKey, err := agencycontract.ModelKey(modelName)
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyUserBinding{UserID: 7, AgencyID: agency.ID, Revision: 1, InviteSnapshot: "TEST", CreatedSource: "test", EffectiveAtMS: 1, CreatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyCustomerSalesOverride{AgencyID: agency.ID, UserID: 7, ModelKey: modelKey, OriginModelName: modelName, SalesBPS: 9500, Revision: 1, CreatedByType: ActorTypeOperator, CreatedByID: 1, CreatedAtMS: 1, UpdatedAtMS: 1}).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/customers/7/pricing", nil)
	context.Params = gin.Params{{Key: "user_id", Value: "7"}}
	context.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
	app.getCustomerSalesPricing(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			DefaultSalesBPS int `json:"default_sales_bps"`
			Items           []struct {
				OriginModelName   string `json:"origin_model_name"`
				InheritedSalesBPS int    `json:"inherited_sales_bps"`
				SalesBPS          int    `json:"sales_bps"`
				OverrideSalesBPS  int    `json:"override_sales_bps"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, 9000, response.Data.DefaultSalesBPS)
	require.Len(t, response.Data.Items, 1)
	require.Equal(t, modelName, response.Data.Items[0].OriginModelName)
	require.Equal(t, 9200, response.Data.Items[0].InheritedSalesBPS)
	require.Equal(t, 9500, response.Data.Items[0].SalesBPS)
	require.Equal(t, 9500, response.Data.Items[0].OverrideSalesBPS)
}

func TestCustomerSalesPricingBatchPublishesAtomically(t *testing.T) {
	app := newAgencyTestApp(t)
	modelA, modelB := "model-a", "model-b"
	policy := agencycontract.Policy{
		DefaultSettlementBPS: 7000,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
		ModelOverrides: []agencycontract.ModelOverride{
			{OriginModelName: modelA},
			{OriginModelName: modelB},
		},
	}
	agency, _, err := app.CreateAgency(1, "Batch pricing", "batch-pricing", policy)
	require.NoError(t, err)
	keyA, err := agencycontract.ModelKey(modelA)
	require.NoError(t, err)
	keyB, err := agencycontract.ModelKey(modelB)
	require.NoError(t, err)
	const userID = int64(7)
	require.NoError(t, app.db.Create(&model.AgencyUserBinding{UserID: userID, AgencyID: agency.ID, Revision: 1, InviteSnapshot: "TEST", CreatedSource: "test", EffectiveAtMS: 1, CreatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyCustomerSalesOverride{AgencyID: agency.ID, UserID: userID, ModelKey: keyB, OriginModelName: modelB, SalesBPS: 9200, Revision: 1, CreatedByType: ActorTypeOperator, CreatedByID: 1, CreatedAtMS: 1, UpdatedAtMS: 1}).Error)

	globalSales, modelASales := 9300, 9400
	body, err := common.Marshal(gin.H{
		"global_sales_bps": globalSales,
		"models": []gin.H{
			{"model_name": modelA, "sales_bps": modelASales},
			{"model_name": modelB, "sales_bps": nil},
		},
		"reason": "batch customer pricing regression",
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/agency/api/v1/customers/7/pricing/batch", bytes.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Params = gin.Params{{Key: "user_id", Value: "7"}}
	context.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
	app.putCustomerSalesPricingBatch(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var saved []model.AgencyCustomerSalesOverride
	require.NoError(t, app.db.Where("agency_id = ? AND user_id = ?", agency.ID, userID).Order("model_key ASC").Find(&saved).Error)
	require.Len(t, saved, 2)
	byKey := make(map[string]model.AgencyCustomerSalesOverride, len(saved))
	for _, item := range saved {
		byKey[item.ModelKey] = item
	}
	require.Equal(t, globalSales, byKey[""].SalesBPS)
	require.Equal(t, modelASales, byKey[keyA].SalesBPS)
	_, deleted := byKey[keyB]
	require.False(t, deleted)
	var audit model.AgencyAuditLog
	require.NoError(t, app.db.Where("action = ?", "pricing.customer_sales.batch_publish").First(&audit).Error)

	invalidSales := 7400
	invalidBody, err := common.Marshal(gin.H{
		"models": []gin.H{{"model_name": modelA, "sales_bps": invalidSales}},
		"reason": "must reject below cost",
	})
	require.NoError(t, err)
	invalidRecorder := httptest.NewRecorder()
	invalidContext, _ := gin.CreateTestContext(invalidRecorder)
	invalidContext.Request = httptest.NewRequest(http.MethodPut, "/agency/api/v1/customers/7/pricing/batch", bytes.NewReader(invalidBody))
	invalidContext.Request.Header.Set("Content-Type", "application/json")
	invalidContext.Params = gin.Params{{Key: "user_id", Value: "7"}}
	invalidContext.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID})
	app.putCustomerSalesPricingBatch(invalidContext)
	require.Equal(t, http.StatusUnprocessableEntity, invalidRecorder.Code, invalidRecorder.Body.String())
	var retained model.AgencyCustomerSalesOverride
	require.NoError(t, app.db.Where("agency_id = ? AND user_id = ? AND model_key = ?", agency.ID, userID, keyA).First(&retained).Error)
	require.Equal(t, modelASales, retained.SalesBPS)
}

func TestCreateChildAgencyReturnsCredentialsAndHierarchy(t *testing.T) {
	t.Setenv("AGENCY_HUB_DELIVERY_KEY", "01234567890123456789012345678901")
	app := newAgencyTestApp(t)
	parentPolicy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultChildCostBPS: 8000, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	parent, _, err := app.CreateAgency(1, "Parent agency", "parent-operator", parentPolicy)
	require.NoError(t, err)
	var account model.AgencyOperatorAccount
	require.NoError(t, app.db.Where("agency_id = ?", parent.ID).First(&account).Error)
	body := []byte(`{"display_name":"Child agency","operator_username":"child-operator","pricing":{"default_settlement_bps":8000,"default_sales_bps":9000,"min_spread_bps":500,"sales_cap_bps":30000,"model_overrides":[]}}`)

	created := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(created)
	createContext.Request = httptest.NewRequest(http.MethodPost, "/agency/api/v1/children", bytes.NewReader(body))
	createContext.Request.Header.Set("Content-Type", "application/json")
	createContext.Request.Header.Set("Idempotency-Key", "child-agency-credentials")
	createContext.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: account.ID, AgencyID: &parent.ID})
	app.createChildAgencyHTTP(createContext)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	var createdResponse struct {
		Success bool `json:"success"`
		Data    struct {
			AgencyID                   int64  `json:"agency_id"`
			InviteURL                  string `json:"invite_url"`
			InviteQRURL                string `json:"invite_qr_url"`
			TemporaryPassword          string `json:"temporary_password"`
			TemporaryPasswordExpiresAt int64  `json:"temporary_password_expires_at"`
			DeliveryID                 int64  `json:"delivery_id"`
			DeliveryOperationID        string `json:"delivery_operation_id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(created.Body.Bytes(), &createdResponse))
	require.True(t, createdResponse.Success)
	require.NotZero(t, createdResponse.Data.AgencyID)
	require.NotEmpty(t, createdResponse.Data.TemporaryPassword)
	require.NotZero(t, createdResponse.Data.TemporaryPasswordExpiresAt)
	require.NotZero(t, createdResponse.Data.DeliveryID)
	require.NotEmpty(t, createdResponse.Data.DeliveryOperationID)
	require.Contains(t, createdResponse.Data.InviteURL, "/register?invite=")
	require.Contains(t, createdResponse.Data.InviteQRURL, "/qr")

	hierarchy := httptest.NewRecorder()
	hierarchyContext, _ := gin.CreateTestContext(hierarchy)
	hierarchyContext.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/hierarchy", nil)
	hierarchyContext.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: account.ID, AgencyID: &parent.ID})
	app.getAgencyHierarchy(hierarchyContext)
	require.Equal(t, http.StatusOK, hierarchy.Code, hierarchy.Body.String())
	var hierarchyResponse struct {
		Success bool `json:"success"`
		Data    struct {
			Current struct {
				ID int64 `json:"id"`
			} `json:"current"`
			Parents  []any `json:"parents"`
			Children []struct {
				ID               int64  `json:"id"`
				DisplayName      string `json:"display_name"`
				OperatorUsername string `json:"operator_username"`
				Depth            int    `json:"depth"`
			} `json:"children"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(hierarchy.Body.Bytes(), &hierarchyResponse))
	require.True(t, hierarchyResponse.Success)
	require.Equal(t, parent.ID, hierarchyResponse.Data.Current.ID)
	require.Empty(t, hierarchyResponse.Data.Parents)
	require.Len(t, hierarchyResponse.Data.Children, 1)
	require.Equal(t, createdResponse.Data.AgencyID, hierarchyResponse.Data.Children[0].ID)
	require.Equal(t, "Child agency", hierarchyResponse.Data.Children[0].DisplayName)
	require.Equal(t, "child-operator", hierarchyResponse.Data.Children[0].OperatorUsername)
	require.Equal(t, 2, hierarchyResponse.Data.Children[0].Depth)
}

func TestCreateChildAgencyRequiresParentChildCost(t *testing.T) {
	t.Setenv("AGENCY_HUB_DELIVERY_KEY", "01234567890123456789012345678901")
	app := newAgencyTestApp(t)
	parentPolicy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	parent, _, err := app.CreateAgency(1, "Parent without child cost", "parent-without-child-cost", parentPolicy)
	require.NoError(t, err)
	var account model.AgencyOperatorAccount
	require.NoError(t, app.db.Where("agency_id = ?", parent.ID).First(&account).Error)

	body := []byte("{\"display_name\":\"Child agency\",\"operator_username\":\"child-operator\"}")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/agency/api/v1/children", bytes.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set("Idempotency-Key", "child-agency-missing-cost")
	context.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: account.ID, AgencyID: &parent.ID})
	app.createChildAgencyHTTP(context)

	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, false, response["success"])
	errorData, ok := response["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "parent_unavailable", errorData["code"])
	require.Contains(t, errorData["message"], "先在价格策略中设置下一级代理商成本系数")
}

func TestChildAgencySalesInheritanceCoversInheritedCost(t *testing.T) {
	app := newAgencyTestApp(t)
	parentPolicy := agencycontract.Policy{
		DefaultSettlementBPS: 7000,
		DefaultChildCostBPS:  7800,
		DefaultSalesBPS:      8000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
		ModelOverrides: []agencycontract.ModelOverride{{
			OriginModelName: "model-a",
			SalesBPS:        func() *int { value := 8000; return &value }(),
		}},
	}
	parent, _, err := app.CreateAgency(1, "Parent sales inheritance", "parent-sales-inheritance", parentPolicy)
	require.NoError(t, err)

	childPolicy, err := inheritChildCostPolicy(app.db, parent.ID, agencycontract.Policy{
		DefaultSalesBPS: 10000,
		MinSpreadBPS:    500,
		SalesCapBPS:     30000,
	})
	require.NoError(t, err)
	require.Equal(t, 7800, childPolicy.DefaultSettlementBPS)
	require.Equal(t, 8300, childPolicy.DefaultSalesBPS)
	resolved, err := agencycontract.Resolve(childPolicy, "model-a")
	require.NoError(t, err)
	require.Equal(t, 7800, resolved.SettlementBPS)
	require.Equal(t, 8300, resolved.SalesBPS)
}

func TestAgencySubtreeIDsIncludesAllDescendants(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-subtree-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateAgency(db))
	root := model.Agency{Code: "tree-root", DisplayName: "Tree Root", Status: AgencyStatusActive, InviteCode: "TREE_ROOT", Depth: 1, PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&root).Error)
	child := model.Agency{ParentAgencyID: &root.ID, Code: "tree-child", DisplayName: "Tree Child", Status: AgencyStatusActive, InviteCode: "TREE_CHILD", Depth: 2, PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&child).Error)
	grandchild := model.Agency{ParentAgencyID: &child.ID, Code: "tree-grandchild", DisplayName: "Tree Grandchild", Status: AgencyStatusActive, InviteCode: "TREE_GRANDCHILD", Depth: 3, PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&grandchild).Error)
	ids, err := agencySubtreeIDsTx(db, root.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{root.ID, child.ID, grandchild.ID}, ids)
}
