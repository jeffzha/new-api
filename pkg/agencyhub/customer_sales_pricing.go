package agencyhub

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type customerSalesPricingRequest struct {
	ModelName string `json:"model_name"`
	SalesBPS  int    `json:"sales_bps"`
	Reason    string `json:"reason"`
}

// customerSalesPricingBatchRequest replaces the customer's explicit overrides
// as one audited transaction. Nil values deliberately remove an override and
// let that row inherit the agency's effective model sales coefficient.
type customerSalesPricingBatchRequest struct {
	GlobalSalesBPS *int `json:"global_sales_bps"`
	Models         []struct {
		ModelName string `json:"model_name"`
		SalesBPS  *int   `json:"sales_bps"`
	} `json:"models"`
	Reason string `json:"reason"`
}

func customerSalesKey(modelName string) (string, string, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", "", nil
	}
	key, err := agencycontract.ModelKey(modelName)
	return key, modelName, err
}

func (a *App) validateCustomerSales(agencyID int64, modelName string, sales int) error {
	agency, policy, err := a.loadAgencyPolicy(agencyID)
	if err != nil {
		return err
	}
	if agency.Status != AgencyStatusActive {
		return errors.New("agency is disabled")
	}
	effective, err := a.effectiveAgencyPolicy(agency, policy)
	if err != nil {
		return err
	}
	if err := agencycontract.ValidateCoefficient(sales, effective.SalesCapBPS); err != nil {
		return err
	}
	return validateCustomerSalesPolicy(effective, modelName, sales)
}

func validateCustomerSalesPolicy(effective agencycontract.Policy, modelName string, sales int) error {
	if modelName != "" {
		resolved, err := agencycontract.Resolve(effective, modelName)
		if err != nil {
			return err
		}
		if sales < resolved.SettlementBPS+effective.MinSpreadBPS {
			return errors.New("customer sales coefficient is below agency cost plus minimum spread")
		}
		return nil
	}
	minimum := effective.DefaultSettlementBPS + effective.MinSpreadBPS
	for _, override := range effective.ModelOverrides {
		settlement := effective.DefaultSettlementBPS
		if override.SettlementBPS != nil {
			settlement = *override.SettlementBPS
		}
		if settlement+effective.MinSpreadBPS > minimum {
			minimum = settlement + effective.MinSpreadBPS
		}
	}
	if sales < minimum {
		return errors.New("customer-wide sales coefficient is below one or more model costs")
	}
	return nil
}

// validateCustomerSalesPolicyTx protects negotiated prices when agency costs change.
func validateCustomerSalesPolicyTx(tx *gorm.DB, agencyID int64, policy agencycontract.Policy) error {
	var overrides []model.AgencyCustomerSalesOverride
	if err := tx.Where("agency_id = ?", agencyID).Find(&overrides).Error; err != nil {
		return err
	}
	for _, override := range overrides {
		if err := validateCustomerSalesPolicy(policy, override.OriginModelName, override.SalesBPS); err != nil {
			return fmt.Errorf("客户 %d（模型 %s，销售系数 %.4f）：%s", override.UserID, override.OriginModelName, float64(override.SalesBPS)/10000, pricingErrorMessage(err))
		}
	}
	return nil
}

func (a *App) getCustomerSalesPricing(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的客户账号", nil)
		return
	}
	agency, ok := a.authorizedCustomer(c, userID, true)
	if !ok {
		return
	}
	key, name, err := customerSalesKey(c.Query("model_name"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_model", "模型名称无效", nil)
		return
	}
	var overrides []model.AgencyCustomerSalesOverride
	query := a.db.Where("agency_id = ? AND user_id = ?", agency.ID, userID)
	if name != "" {
		query = query.Where("model_key IN ?", []string{key, ""})
	}
	if err := query.Order("model_key ASC").Find(&overrides).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取客户销售价格失败", nil)
		return
	}
	loadedAgency, policy, err := a.loadAgencyPolicy(agency.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取代理商价格策略失败", nil)
		return
	}
	effective, err := a.effectiveAgencyPolicy(loadedAgency, policy)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取有效价格策略失败", nil)
		return
	}
	byKey := make(map[string]model.AgencyCustomerSalesOverride, len(overrides))
	for _, row := range overrides {
		byKey[row.ModelKey] = row
	}
	items := make([]gin.H, 0, len(effective.ModelOverrides)+1)
	if global, exists := byKey[""]; exists {
		items = append(items, gin.H{"origin_model_name": "", "model_key": "", "agency_cost_bps": effective.DefaultSettlementBPS, "inherited_sales_bps": effective.DefaultSalesBPS, "sales_bps": global.SalesBPS, "override_sales_bps": global.SalesBPS, "is_global": true})
	}
	for _, modelOverride := range effective.ModelOverrides {
		modelKey, keyErr := agencycontract.ModelKey(modelOverride.OriginModelName)
		if keyErr != nil {
			continue
		}
		settlement := effective.DefaultSettlementBPS
		sales := effective.DefaultSalesBPS
		if modelOverride.SettlementBPS != nil {
			settlement = *modelOverride.SettlementBPS
		}
		if modelOverride.SalesBPS != nil {
			sales = *modelOverride.SalesBPS
		}
		item := gin.H{"origin_model_name": modelOverride.OriginModelName, "model_key": modelKey, "agency_cost_bps": settlement, "inherited_sales_bps": sales, "sales_bps": sales, "override_sales_bps": nil}
		if override, exists := byKey[modelKey]; exists {
			item["sales_bps"] = override.SalesBPS
			item["override_sales_bps"] = override.SalesBPS
		}
		items = append(items, item)
	}
	respondOK(c, gin.H{
		"agency_id":               agency.ID,
		"user_id":                 userID,
		"default_sales_bps":       effective.DefaultSalesBPS,
		"default_agency_cost_bps": effective.DefaultSettlementBPS,
		"min_spread_bps":          effective.MinSpreadBPS,
		"sales_cap_bps":           effective.SalesCapBPS,
		"items":                   items,
	})
}

func (a *App) putCustomerSalesPricing(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的客户账号", nil)
		return
	}
	agency, ok := a.authorizedCustomer(c, userID, true)
	if !ok {
		return
	}
	var request customerSalesPricingRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if len(request.Reason) > 2000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reason", "调整原因不能超过2000字节", nil)
		return
	}
	key, name, err := customerSalesKey(request.ModelName)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_model", "模型名称无效", nil)
		return
	}
	if err := a.validateCustomerSales(agency.ID, name, request.SalesBPS); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_sales", pricingErrorMessage(err), nil)
		return
	}
	identity := currentIdentity(c)
	now := time.Now().UnixMilli()
	var row model.AgencyCustomerSalesOverride
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var locked model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&locked, agency.ID).Error; err != nil {
			return err
		}
		if err := (&App{db: tx}).validateCustomerSales(agency.ID, name, request.SalesBPS); err != nil {
			return err
		}
		var existing model.AgencyCustomerSalesOverride
		lookup := tx.Where("agency_id = ? AND user_id = ? AND model_key = ?", agency.ID, userID, key).First(&existing).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if errors.Is(lookup, gorm.ErrRecordNotFound) {
			row = model.AgencyCustomerSalesOverride{AgencyID: agency.ID, UserID: userID, ModelKey: key, OriginModelName: name, SalesBPS: request.SalesBPS, Revision: 1, CreatedByType: identity.ActorType, CreatedByID: identity.ActorID, CreatedAtMS: now, UpdatedAtMS: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			return recordAuditTx(tx, c, identity, "pricing.customer_sales.publish", "customer_sales", strconv.FormatInt(userID, 10), request.Reason, nil, gin.H{"agency_id": agency.ID, "user_id": userID, "model_name": name, "sales_bps": request.SalesBPS, "revision": row.Revision})
		}
		row = existing
		row.SalesBPS = request.SalesBPS
		row.Revision++
		row.UpdatedAtMS = now
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if auditErr := recordAuditTx(tx, c, identity, "pricing.customer_sales.publish", "customer_sales", strconv.FormatInt(userID, 10), request.Reason, nil, gin.H{"agency_id": agency.ID, "user_id": userID, "model_name": name, "sales_bps": request.SalesBPS, "revision": row.Revision}); auditErr != nil {
			return auditErr
		}
		return nil
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "保存客户销售价格失败", nil)
		return
	}
	respondOK(c, row)
}

func (a *App) putCustomerSalesPricingBatch(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的客户账号", nil)
		return
	}
	agency, ok := a.authorizedCustomer(c, userID, true)
	if !ok {
		return
	}
	var request customerSalesPricingBatchRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if len(request.Reason) > 2000 || len(request.Models) > 1000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_request", "调整原因不能超过2000字节，且模型数量不能超过1000个", nil)
		return
	}
	if request.GlobalSalesBPS != nil {
		if err := a.validateCustomerSales(agency.ID, "", *request.GlobalSalesBPS); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_sales", pricingErrorMessage(err), nil)
			return
		}
	}
	type normalizedOverride struct {
		key, name string
		sales     *int
	}
	normalized := make([]normalizedOverride, 0, len(request.Models))
	seen := make(map[string]struct{}, len(request.Models))
	for _, item := range request.Models {
		key, name, keyErr := customerSalesKey(item.ModelName)
		if keyErr != nil || name == "" {
			respondError(c, http.StatusBadRequest, "invalid_model", "模型名称无效", nil)
			return
		}
		if _, exists := seen[key]; exists {
			respondError(c, http.StatusUnprocessableEntity, "duplicate_model", "同一个模型只能填写一次", nil)
			return
		}
		seen[key] = struct{}{}
		if item.SalesBPS != nil {
			if validateErr := a.validateCustomerSales(agency.ID, name, *item.SalesBPS); validateErr != nil {
				respondError(c, http.StatusUnprocessableEntity, "invalid_sales", pricingErrorMessage(validateErr), nil)
				return
			}
		}
		normalized = append(normalized, normalizedOverride{key: key, name: name, sales: item.SalesBPS})
	}
	identity := currentIdentity(c)
	if identity == nil {
		respondError(c, http.StatusUnauthorized, "unauthorized", "请先登录", nil)
		return
	}
	now := time.Now().UnixMilli()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var locked model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&locked, agency.ID).Error; err != nil {
			return err
		}
		validator := &App{db: tx}
		if request.GlobalSalesBPS != nil {
			if err := validator.validateCustomerSales(agency.ID, "", *request.GlobalSalesBPS); err != nil {
				return err
			}
		}
		for _, item := range normalized {
			if item.sales != nil {
				if err := validator.validateCustomerSales(agency.ID, item.name, *item.sales); err != nil {
					return err
				}
			}
		}
		apply := func(key, name string, sales *int) error {
			var existing model.AgencyCustomerSalesOverride
			lookup := tx.Where("agency_id = ? AND user_id = ? AND model_key = ?", agency.ID, userID, key).First(&existing).Error
			if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
				return lookup
			}
			if sales == nil {
				if errors.Is(lookup, gorm.ErrRecordNotFound) {
					return nil
				}
				return tx.Where("agency_id = ? AND user_id = ? AND model_key = ?", agency.ID, userID, key).Delete(&model.AgencyCustomerSalesOverride{}).Error
			}
			if errors.Is(lookup, gorm.ErrRecordNotFound) {
				return tx.Create(&model.AgencyCustomerSalesOverride{AgencyID: agency.ID, UserID: userID, ModelKey: key, OriginModelName: name, SalesBPS: *sales, Revision: 1, CreatedByType: identity.ActorType, CreatedByID: identity.ActorID, CreatedAtMS: now, UpdatedAtMS: now}).Error
			}
			existing.SalesBPS, existing.Revision, existing.UpdatedAtMS = *sales, existing.Revision+1, now
			return tx.Save(&existing).Error
		}
		if err := apply("", "", request.GlobalSalesBPS); err != nil {
			return err
		}
		for _, item := range normalized {
			if err := apply(item.key, item.name, item.sales); err != nil {
				return err
			}
		}
		return recordAuditTx(tx, c, identity, "pricing.customer_sales.batch_publish", "customer_sales", strconv.FormatInt(userID, 10), request.Reason, nil, gin.H{"agency_id": agency.ID, "user_id": userID, "global_sales_bps": request.GlobalSalesBPS, "model_count": len(normalized)})
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "保存客户销售价格失败", nil)
		return
	}
	respondOK(c, gin.H{"updated": true})
}

func (a *App) deleteCustomerSalesPricing(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的客户账号", nil)
		return
	}
	agency, ok := a.authorizedCustomer(c, userID, true)
	if !ok {
		return
	}
	key, _, err := customerSalesKey(c.Query("model_name"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_model", "模型名称无效", nil)
		return
	}
	reason := strings.TrimSpace(c.Query("reason"))
	identity := currentIdentity(c)
	if identity == nil {
		respondError(c, http.StatusUnauthorized, "unauthorized", "请先登录", nil)
		return
	}
	var deleted int64
	err = a.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("agency_id = ? AND user_id = ? AND model_key = ?", agency.ID, userID, key).Delete(&model.AgencyCustomerSalesOverride{})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		if deleted == 0 {
			return nil
		}
		return recordAuditTx(tx, c, identity, "pricing.customer_sales.delete", "customer_sales", strconv.FormatInt(userID, 10), reason, nil, gin.H{"agency_id": agency.ID, "user_id": userID, "model_name": c.Query("model_name"), "deleted": true})
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "删除客户销售价格失败", nil)
		return
	}
	respondOK(c, gin.H{"deleted": deleted > 0})
}
