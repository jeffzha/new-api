package agencyhub

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type rootPricingRequest struct {
	ExpectedRevision     int64                          `json:"expected_revision"`
	DefaultSettlementBPS int                            `json:"default_settlement_bps"`
	DefaultSalesBPS      int                            `json:"default_sales_bps"`
	MinSpreadBPS         int                            `json:"min_spread_bps"`
	SalesCapBPS          int                            `json:"sales_cap_bps"`
	ModelOverrides       []agencycontract.ModelOverride `json:"model_overrides"`
	Reason               string                         `json:"reason"`
}
type salesPricingRequest struct {
	ExpectedRevision    int64 `json:"expected_revision"`
	DefaultSalesBPS     int   `json:"default_sales_bps"`
	ModelSalesOverrides []struct {
		OriginModelName string `json:"origin_model_name"`
		SalesBPS        *int   `json:"sales_bps"`
	} `json:"model_sales_overrides"`
	Reason string `json:"reason"`
}

func (a *App) loadAgencyPolicy(agencyID int64) (model.Agency, agencycontract.Policy, error) {
	var agency model.Agency
	if err := a.db.First(&agency, agencyID).Error; err != nil {
		return agency, agencycontract.Policy{}, err
	}
	var row model.AgencyPricePolicyVersion
	if err := a.db.Where("id = ?", agency.CurrentPolicyVersionID).First(&row).Error; err != nil {
		return agency, agencycontract.Policy{}, err
	}
	var policy agencycontract.Policy
	if err := common.Unmarshal([]byte(row.PolicyJSON), &policy); err != nil {
		return agency, agencycontract.Policy{}, err
	}
	policy.Revision = row.Revision
	return agency, policy, nil
}
func (a *App) policyView(agency model.Agency, policy agencycontract.Policy) gin.H {
	return gin.H{"agency_id": agency.ID, "revision": policy.Revision, "default_settlement_bps": policy.DefaultSettlementBPS, "default_sales_bps": policy.DefaultSalesBPS, "min_spread_bps": policy.MinSpreadBPS, "sales_cap_bps": policy.SalesCapBPS, "model_overrides": policy.ModelOverrides}
}
func (a *App) ownAgency(c *gin.Context) (model.Agency, agencycontract.Policy, bool) {
	identity := currentIdentity(c)
	if identity == nil || identity.AgencyID == nil {
		respondError(c, http.StatusForbidden, "agency_required", "当前会话没有代理商范围", nil)
		return model.Agency{}, agencycontract.Policy{}, false
	}
	agency, policy, err := a.loadAgencyPolicy(*identity.AgencyID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return model.Agency{}, agencycontract.Policy{}, false
	}
	return agency, policy, true
}
func (a *App) getOwnPricing(c *gin.Context) {
	agency, policy, ok := a.ownAgency(c)
	if ok {
		respondOK(c, a.policyView(agency, policy))
	}
}
func (a *App) getRootPricing(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	agency, policy, err := a.loadAgencyPolicy(id)
	if err != nil {
		respondError(c, http.StatusNotFound, "not_found", "代理商或价格不存在", nil)
		return
	}
	respondOK(c, a.policyView(agency, policy))
}

func (a *App) getOwnPricingHistory(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.AgencyID == nil {
		respondError(c, http.StatusForbidden, "agency_required", "当前会话没有代理商范围", nil)
		return
	}
	a.getPricingHistory(c, *identity.AgencyID)
}

func (a *App) getRootPricingHistory(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	a.getPricingHistory(c, id)
}

func (a *App) getPricingHistory(c *gin.Context, agencyID int64) {
	var agency model.Agency
	if err := a.db.Select("id").First(&agency, agencyID).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "代理商不存在", nil)
		return
	}
	limit := 50
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	var rows []model.AgencyPricePolicyVersion
	if err := a.db.Where("agency_id = ?", agencyID).Order("revision DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取价格历史失败", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		var policy agencycontract.Policy
		if err := common.Unmarshal([]byte(row.PolicyJSON), &policy); err != nil {
			respondError(c, http.StatusInternalServerError, "invalid_policy", "价格历史数据损坏", nil)
			return
		}
		policy.Revision = row.Revision
		items = append(items, gin.H{
			"policy_version_id": row.ID,
			"revision":          row.Revision,
			"policy":            policy,
			"policy_hash":       row.PolicyHash,
			"created_by_type":   row.CreatedByType,
			"created_by_id":     row.CreatedByID,
			"reason":            row.Reason,
			"created_at_ms":     row.CreatedAtMS,
		})
	}
	respondOK(c, gin.H{"items": items})
}

func (a *App) listPublicModels(c *gin.Context) {
	query := strings.ToLower(strings.TrimSpace(c.Query("q")))
	limit := 200
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	var models []string
	db := a.db.Table("abilities").
		Select("DISTINCT abilities.model").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.enabled = ? AND channels.status = ?", true, common.ChannelStatusEnabled).
		Order("abilities.model ASC").
		Limit(limit)
	if query != "" {
		db = db.Where("LOWER(abilities.model) LIKE ?", "%"+query+"%")
	}
	if err := db.Pluck("abilities.model", &models).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取模型列表失败", nil)
		return
	}
	respondOK(c, gin.H{"items": models, "count": len(models)})
}

func (a *App) previewRootPricing(c *gin.Context) {
	var request rootPricingRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	policy := agencycontract.Policy{Revision: request.ExpectedRevision, DefaultSettlementBPS: request.DefaultSettlementBPS, DefaultSalesBPS: request.DefaultSalesBPS, MinSpreadBPS: request.MinSpreadBPS, SalesCapBPS: request.SalesCapBPS, ModelOverrides: request.ModelOverrides}
	if policy.SalesCapBPS == 0 {
		policy.SalesCapBPS = a.config.SalesCapBPS
	}
	if err := agencycontract.ValidatePolicy(policy); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", err.Error(), nil)
		return
	}
	respondOK(c, pricePreview(policy))
}
func pricePreview(policy agencycontract.Policy) gin.H {
	standard := int64(10000)
	resolved, _ := agencycontract.Resolve(policy, "preview")
	result, _ := agencycontract.Calculate(standard, resolved, standard, true)
	return gin.H{"standard_quota": standard, "customer_quota": result.ChargedQuota, "settlement_quota": result.SettlementCostQuota, "commission_quota": result.CommissionQuota, "sales_bps": resolved.SalesBPS, "settlement_bps": resolved.SettlementBPS}
}

func (a *App) previewSalesPricing(c *gin.Context) {
	agency, policy, ok := a.ownAgency(c)
	if !ok {
		return
	}
	_ = agency
	var request salesPricingRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	candidate, err := mergeSalesPolicy(policy, request)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", err.Error(), nil)
		return
	}
	if err := agencycontract.ValidatePolicy(candidate); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", err.Error(), nil)
		return
	}
	respondOK(c, pricePreview(candidate))
}
func (a *App) publishRootPricing(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	var request rootPricingRequest
	if err = common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	policy := agencycontract.Policy{DefaultSettlementBPS: request.DefaultSettlementBPS, DefaultSalesBPS: request.DefaultSalesBPS, MinSpreadBPS: request.MinSpreadBPS, SalesCapBPS: request.SalesCapBPS, ModelOverrides: request.ModelOverrides}
	if policy.SalesCapBPS == 0 {
		policy.SalesCapBPS = a.config.SalesCapBPS
	}
	if err = a.publishPolicy(c, id, request.ExpectedRevision, policy, request.Reason, ActorTypeRoot); err != nil {
		return
	}
}
func (a *App) publishSalesPricing(c *gin.Context) {
	agency, old, ok := a.ownAgency(c)
	if !ok {
		return
	}
	var request salesPricingRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	candidate, err := mergeSalesPolicy(old, request)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", err.Error(), nil)
		return
	}
	identity := currentIdentity(c)
	actorType := ActorTypeOperator
	if identity != nil && identity.ActorType == ActorTypeRoot {
		// Root may publish the sales half while explicitly acting within an
		// agency. Preserve the real actor in the immutable policy history.
		actorType = ActorTypeRoot
	}
	_ = a.publishPolicy(c, agency.ID, request.ExpectedRevision, candidate, request.Reason, actorType)
}

func mergeSalesPolicy(base agencycontract.Policy, request salesPricingRequest) (agencycontract.Policy, error) {
	candidate := base
	candidate.DefaultSalesBPS = request.DefaultSalesBPS
	byKey := make(map[string]agencycontract.ModelOverride, len(base.ModelOverrides)+len(request.ModelSalesOverrides))
	order := make([]string, 0, len(base.ModelOverrides)+len(request.ModelSalesOverrides))
	remember := func(key string) {
		if _, exists := byKey[key]; !exists {
			order = append(order, key)
		}
	}
	for _, override := range base.ModelOverrides {
		key, err := agencycontract.ModelKey(override.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, err
		}
		remember(key)
		byKey[key] = agencycontract.ModelOverride{OriginModelName: override.OriginModelName, SettlementBPS: override.SettlementBPS}
	}
	for _, override := range request.ModelSalesOverrides {
		key, err := agencycontract.ModelKey(override.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, err
		}
		remember(key)
		current := byKey[key]
		if current.OriginModelName == "" {
			current.OriginModelName = override.OriginModelName
		}
		current.SalesBPS = override.SalesBPS
		byKey[key] = current
	}
	candidate.ModelOverrides = make([]agencycontract.ModelOverride, 0, len(byKey))
	for _, key := range order {
		override := byKey[key]
		if override.SettlementBPS == nil && override.SalesBPS == nil {
			continue
		}
		candidate.ModelOverrides = append(candidate.ModelOverrides, override)
	}
	return candidate, nil
}

func (a *App) publishPolicy(c *gin.Context, agencyID, expectedRevision int64, policy agencycontract.Policy, reason, actorType string) error {
	if expectedRevision <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_revision_required", "必须提供当前价格版本", nil)
		return errors.New("expected revision required")
	}
	if err := agencycontract.ValidatePolicy(policy); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", err.Error(), nil)
		return err
	}
	identity := currentIdentity(c)
	if identity == nil {
		respondError(c, http.StatusUnauthorized, "unauthorized", "请先登录", nil)
		return errors.New("unauthorized")
	}
	now := time.Now().UnixMilli()
	var row model.AgencyPricePolicyVersion
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var agency model.Agency
		if err := tx.First(&agency, agencyID).Error; err != nil {
			return err
		}
		if agency.Status != AgencyStatusActive {
			return errors.New("agency is disabled")
		}
		if expectedRevision > 0 && agency.PriceRevision != expectedRevision {
			return agencycontract.ErrPolicyRevision
		}
		policy.Revision = agency.PriceRevision + 1
		encoded, err := common.Marshal(policy)
		if err != nil {
			return err
		}
		hash, err := agencycontract.CanonicalHash(policy)
		if err != nil {
			return err
		}
		row = model.AgencyPricePolicyVersion{AgencyID: agencyID, Revision: policy.Revision, PolicyJSON: string(encoded), PolicyHash: hash, CreatedByType: actorType, CreatedByID: identity.ActorID, Reason: reason, CreatedAtMS: now}
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		if err = a.writePolicyItems(tx, row.ID, policy); err != nil {
			return err
		}
		result := tx.Model(&agency).Where("id = ? AND price_revision = ?", agencyID, agency.PriceRevision).Updates(map[string]any{"current_policy_version_id": row.ID, "price_revision": policy.Revision, "version": agency.Version + 1, "updated_at": now / 1000})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agencycontract.ErrPolicyRevision
		}
		return recordAuditTx(tx, c, identity, "pricing.publish", "agency", stringID(agencyID), reason,
			map[string]any{"revision": agency.PriceRevision},
			map[string]any{"revision": policy.Revision, "policy_version_id": row.ID})
	})
	if err != nil {
		status := http.StatusInternalServerError
		code := "publish_failed"
		if errors.Is(err, agencycontract.ErrPolicyRevision) {
			status = http.StatusConflict
			code = "price_revision_conflict"
		}
		respondError(c, status, code, err.Error(), nil)
		return err
	}
	respondOK(c, gin.H{"policy_version_id": row.ID, "revision": policy.Revision, "committed_at_ms": now})
	return nil
}
