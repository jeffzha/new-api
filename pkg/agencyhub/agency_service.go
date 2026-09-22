package agencyhub

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
	"gorm.io/gorm"
)

type createAgencyRequest struct {
	DisplayName      string                `json:"display_name"`
	OperatorUsername string                `json:"operator_username"`
	Pricing          agencycontract.Policy `json:"pricing"`
	Status           string                `json:"status"`
	ParentAgencyID   *int64                `json:"parent_agency_id,omitempty"`
}
type updateAgencyRequest struct {
	DisplayName     *string `json:"display_name"`
	Reason          *string `json:"reason"`
	ExpectedVersion int64   `json:"expected_version"`
}
type agencyView struct {
	model.Agency
	InviteURL        string `json:"invite_url"`
	InviteQRURL      string `json:"invite_qr_url"`
	OperatorUsername string `json:"operator_username,omitempty"`
}

type agencyHierarchyNode struct {
	ID               int64  `json:"id"`
	DisplayName      string `json:"display_name"`
	Status           string `json:"status"`
	Depth            int    `json:"depth"`
	ParentAgencyID   *int64 `json:"parent_agency_id,omitempty"`
	OperatorUsername string `json:"operator_username,omitempty"`
}

const maxAgencyDepth = 10

func minCoefficient(value, cap int) int {
	if cap <= 0 || value < 0 {
		return value
	}
	if value > cap {
		return cap
	}
	return value
}

func inheritedSalesBPS(inheritedSales, settlement, minSpread, cap int) (int, error) {
	minimum := settlement + minSpread
	if minimum > cap {
		return 0, errors.New("inherited sales coefficient exceeds sales cap")
	}
	return max(inheritedSales, minimum), nil
}

func validateAgencyParentTx(tx *gorm.DB, parentID *int64) (int, error) {
	if parentID == nil || *parentID == 0 {
		return 1, nil
	}
	var parent model.Agency
	if err := model.AgencyLockForUpdate(tx).First(&parent, *parentID).Error; err != nil {
		return 0, fmt.Errorf("parent agency not found")
	}
	if parent.Status != AgencyStatusActive {
		return 0, fmt.Errorf("parent agency is disabled")
	}
	depth := parent.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth >= maxAgencyDepth {
		return 0, fmt.Errorf("agency hierarchy depth limit is %d", maxAgencyDepth)
	}
	return depth + 1, nil
}

func validateChildPolicyTx(tx *gorm.DB, parentID int64, policy agencycontract.Policy) error {
	var parent model.Agency
	if err := tx.First(&parent, parentID).Error; err != nil {
		return errors.New("parent agency not found")
	}
	var row model.AgencyPricePolicyVersion
	if err := tx.Where("id = ?", parent.CurrentPolicyVersionID).First(&row).Error; err != nil {
		return errors.New("parent agency pricing is unavailable")
	}
	var parentPolicy agencycontract.Policy
	if err := common.Unmarshal([]byte(row.PolicyJSON), &parentPolicy); err != nil {
		return errors.New("parent agency pricing is invalid")
	}
	parentEffective := parentPolicy
	if parent.ParentAgencyID == nil {
		var err error
		parentEffective, err = (&App{db: tx}).effectivePolicy(parentPolicy)
		if err != nil {
			return err
		}
	}
	if policy.MinSpreadBPS == 0 {
		policy.MinSpreadBPS = parentEffective.MinSpreadBPS
	} else if policy.MinSpreadBPS < parentEffective.MinSpreadBPS {
		return errors.New("child minimum spread cannot be below parent minimum spread")
	}
	parentDefaultChildCost := parentEffective.DefaultChildCostBPS
	if parentDefaultChildCost == 0 {
		return errors.New("parent child agency cost is not configured; configure it before creating a child agency")
	}
	if policy.DefaultSettlementBPS < parentDefaultChildCost {
		return errors.New("child default cost must not be below parent child cost")
	}
	for _, childOverride := range policy.ModelOverrides {
		childResolved, resolveErr := agencycontract.Resolve(policy, childOverride.OriginModelName)
		if resolveErr != nil {
			return resolveErr
		}
		parentChildCost := parentDefaultChildCost
		if parentOverride := findModelOverride(parentEffective, childOverride.OriginModelName); parentOverride != nil && parentOverride.ChildCostBPS != nil {
			parentChildCost = *parentOverride.ChildCostBPS
		}
		if childResolved.SettlementBPS < parentChildCost {
			return fmt.Errorf("child cost for model %s must not be below parent child cost", childOverride.OriginModelName)
		}
	}
	return nil
}

func findModelOverride(policy agencycontract.Policy, modelName string) *agencycontract.ModelOverride {
	key, err := agencycontract.ModelKey(modelName)
	if err != nil {
		return nil
	}
	for i := range policy.ModelOverrides {
		overrideKey, keyErr := agencycontract.ModelKey(policy.ModelOverrides[i].OriginModelName)
		if keyErr == nil && overrideKey == key {
			return &policy.ModelOverrides[i]
		}
	}
	return nil
}

// inheritChildCostPolicy makes omitted model costs explicit. Without this
// normalization a child that only supplied a default cost could silently fall
// below a parent model-specific cost when that model was later requested.
func inheritChildCostPolicy(tx *gorm.DB, parentID int64, policy agencycontract.Policy) (agencycontract.Policy, error) {
	var parent model.Agency
	if err := tx.First(&parent, parentID).Error; err != nil {
		return agencycontract.Policy{}, errors.New("parent agency not found")
	}
	var row model.AgencyPricePolicyVersion
	if err := tx.Where("id = ?", parent.CurrentPolicyVersionID).First(&row).Error; err != nil {
		return agencycontract.Policy{}, errors.New("parent agency pricing is unavailable")
	}
	var parentPolicy agencycontract.Policy
	if err := common.Unmarshal([]byte(row.PolicyJSON), &parentPolicy); err != nil {
		return agencycontract.Policy{}, errors.New("parent agency pricing is invalid")
	}
	parentEffective := parentPolicy
	if parent.ParentAgencyID == nil {
		var err error
		parentEffective, err = (&App{db: tx}).effectivePolicy(parentPolicy)
		if err != nil {
			return agencycontract.Policy{}, err
		}
	}
	if policy.MinSpreadBPS == 0 {
		policy.MinSpreadBPS = parentEffective.MinSpreadBPS
	} else if policy.MinSpreadBPS < parentEffective.MinSpreadBPS {
		return agencycontract.Policy{}, errors.New("child minimum spread cannot be below parent minimum spread")
	}
	if policy.SalesCapBPS == 0 {
		policy.SalesCapBPS = parentEffective.SalesCapBPS
	}
	if policy.DefaultSettlementBPS == 0 {
		policy.DefaultSettlementBPS = parentEffective.DefaultChildCostBPS
	}
	if policy.DefaultSalesBPS == 0 || policy.DefaultSalesBPS == 10000 {
		var salesErr error
		policy.DefaultSalesBPS, salesErr = inheritedSalesBPS(parentEffective.DefaultSalesBPS, policy.DefaultSettlementBPS, policy.MinSpreadBPS, policy.SalesCapBPS)
		if salesErr != nil {
			return agencycontract.Policy{}, salesErr
		}
	}
	parentDefaultChildCost := parentEffective.DefaultChildCostBPS
	if parentDefaultChildCost == 0 {
		return agencycontract.Policy{}, errors.New("parent child agency cost is not configured; configure it before creating a child agency")
	}
	// A blank default child cost is intentional. It prevents silently adding
	// another spread at every hierarchy level; the parent must explicitly set
	// one before creating a child.
	positions := make(map[string]int, len(policy.ModelOverrides))
	for i, override := range policy.ModelOverrides {
		key, err := agencycontract.ModelKey(override.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, err
		}
		positions[key] = i
	}
	for _, parentOverride := range parentEffective.ModelOverrides {
		key, err := agencycontract.ModelKey(parentOverride.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, err
		}
		parentResolved, err := agencycontract.Resolve(parentEffective, parentOverride.OriginModelName)
		if err != nil {
			return agencycontract.Policy{}, err
		}
		position, exists := positions[key]
		if !exists {
			cost := parentDefaultChildCost
			if parentOverride.ChildCostBPS != nil {
				cost = *parentOverride.ChildCostBPS
			}
			cost = minCoefficient(cost, policy.SalesCapBPS)
			sales, salesErr := inheritedSalesBPS(parentResolved.SalesBPS, cost, policy.MinSpreadBPS, policy.SalesCapBPS)
			if salesErr != nil {
				return agencycontract.Policy{}, salesErr
			}
			policy.ModelOverrides = append(policy.ModelOverrides, agencycontract.ModelOverride{OriginModelName: parentOverride.OriginModelName, SettlementBPS: &cost, SalesBPS: &sales})
			positions[key] = len(policy.ModelOverrides) - 1
			continue
		}
		if policy.ModelOverrides[position].SettlementBPS == nil {
			cost := parentDefaultChildCost
			if parentOverride.ChildCostBPS != nil {
				cost = *parentOverride.ChildCostBPS
			}
			cost = minCoefficient(cost, policy.SalesCapBPS)
			policy.ModelOverrides[position].SettlementBPS = &cost
			if policy.ModelOverrides[position].SalesBPS == nil {
				sales, salesErr := inheritedSalesBPS(parentResolved.SalesBPS, cost, policy.MinSpreadBPS, policy.SalesCapBPS)
				if salesErr != nil {
					return agencycontract.Policy{}, salesErr
				}
				policy.ModelOverrides[position].SalesBPS = &sales
			}
		}
	}
	return policy, nil
}

func (a *App) createChildAgencyHTTP(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.AgencyID == nil || identity.ActorType != ActorTypeOperator {
		respondError(c, http.StatusForbidden, "agency_required", "只有代理商账号可以创建下级代理商", nil)
		return
	}
	var request createAgencyRequest
	request.Pricing = agencycontract.Policy{DefaultSalesBPS: 10000, MinSpreadBPS: a.config.MinSpreadBPS}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	parentID := *identity.AgencyID
	request.ParentAgencyID = &parentID
	parentAgency, _, parentErr := a.loadAgencyPolicy(parentID)
	if parentErr != nil || parentAgency.Status != AgencyStatusActive {
		respondError(c, http.StatusConflict, "parent_unavailable", "上级代理商当前不可用", nil)
		return
	}
	request.Pricing, parentErr = inheritChildCostPolicy(a.db, parentID, request.Pricing)
	if parentErr != nil {
		message := "上级代理商价格策略不可用"
		if parentErr.Error() == "parent child agency cost is not configured; configure it before creating a child agency" {
			message = pricingErrorMessage(parentErr)
		}
		respondError(c, http.StatusConflict, "parent_unavailable", message, nil)
		return
	}
	if err := agencycontract.ValidatePolicy(request.Pricing); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
	if err := a.validateChildPolicy(parentID, request.Pricing); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
	operationID := deliveryOperationID(c, identity)
	binding := deliveryBindingFromContext(c, identity, c.Request.URL.Path, "")
	agency, password, delivery, err := a.createAgency(identity.ActorID, strings.TrimSpace(request.DisplayName), strings.TrimSpace(request.OperatorUsername), request.Pricing, operationID, binding, request.ParentAgencyID, ActorTypeOperator)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "create_failed", err.Error(), nil)
		return
	}
	view := agencyView{Agency: agency, InviteURL: a.inviteURL(agency.InviteCode), InviteQRURL: a.inviteQRURL(agency.InviteCode), OperatorUsername: request.OperatorUsername}
	c.Header("Cache-Control", "no-store")
	respondCreated(c, gin.H{
		"agency":                        view,
		"agency_id":                     agency.ID,
		"invite_code":                   agency.InviteCode,
		"invite_url":                    view.InviteURL,
		"invite_qr_url":                 view.InviteQRURL,
		"delivery_id":                   delivery.ID,
		"delivery_operation_id":         delivery.OperationID,
		"temporary_password":            password,
		"temporary_password_expires_at": delivery.ExpiresAt,
	})
}

func (a *App) validateChildPolicy(parentID int64, policy agencycontract.Policy) error {
	return validateChildPolicyTx(a.db, parentID, policy)
}

func (a *App) listChildAgencies(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.AgencyID == nil {
		respondError(c, http.StatusForbidden, "agency_required", "当前会话没有代理商范围", nil)
		return
	}
	var rows []model.Agency
	if err := a.db.Where("parent_agency_id = ?", *identity.AgencyID).Order("id ASC").Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取下级代理商失败", nil)
		return
	}
	views := make([]agencyView, 0, len(rows))
	for _, agency := range rows {
		var account model.AgencyOperatorAccount
		_ = a.db.Select("username").Where("agency_id = ?", agency.ID).First(&account).Error
		views = append(views, agencyView{Agency: agency, InviteURL: a.inviteURL(agency.InviteCode), InviteQRURL: a.inviteQRURL(agency.InviteCode), OperatorUsername: account.Username})
	}
	respondOK(c, gin.H{"items": views})
}

// getAgencyHierarchy returns the current agency, its ancestor chain, and its
// direct children. Only the operator's own subtree is exposed; pricing and
// financial fields are intentionally omitted.
func (a *App) getAgencyHierarchy(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.AgencyID == nil || identity.ActorType != ActorTypeOperator {
		respondError(c, http.StatusForbidden, "agency_required", "当前会话没有代理商范围", nil)
		return
	}
	var current model.Agency
	if err := a.db.First(&current, *identity.AgencyID).Error; err != nil {
		respondError(c, http.StatusNotFound, "agency_not_found", "代理商不存在", nil)
		return
	}
	toNode := func(agency model.Agency) agencyHierarchyNode {
		node := agencyHierarchyNode{ID: agency.ID, DisplayName: agency.DisplayName, Status: agency.Status, Depth: agency.Depth, ParentAgencyID: agency.ParentAgencyID}
		var account model.AgencyOperatorAccount
		if a.db.Select("username").Where("agency_id = ?", agency.ID).First(&account).Error == nil {
			node.OperatorUsername = account.Username
		}
		return node
	}
	parents := make([]agencyHierarchyNode, 0, maxAgencyDepth-1)
	parentID := current.ParentAgencyID
	for parentID != nil && len(parents) < maxAgencyDepth {
		var parent model.Agency
		if err := a.db.First(&parent, *parentID).Error; err != nil {
			break
		}
		parents = append(parents, toNode(parent))
		parentID = parent.ParentAgencyID
	}
	children := make([]model.Agency, 0)
	if err := a.db.Where("parent_agency_id = ?", current.ID).Order("id ASC").Find(&children).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取下级代理商失败", nil)
		return
	}
	childNodes := make([]agencyHierarchyNode, 0, len(children))
	for _, child := range children {
		childNodes = append(childNodes, toNode(child))
	}
	respondOK(c, gin.H{"current": toNode(current), "parents": parents, "children": childNodes})
}

func (a *App) createAgencyHTTP(c *gin.Context) {
	// Default new agencies to the standard price. Decode over these defaults
	// so explicitly supplied coefficients, including zero, retain their meaning.
	request := createAgencyRequest{Pricing: agencycontract.Policy{DefaultSalesBPS: 10000, MinSpreadBPS: a.config.MinSpreadBPS}}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.OperatorUsername = strings.TrimSpace(request.OperatorUsername)
	if request.DisplayName == "" || len([]rune(request.DisplayName)) > 191 || request.OperatorUsername == "" {
		respondError(c, http.StatusUnprocessableEntity, "invalid_agency", "代理商名称和账号不能为空", nil)
		return
	}
	if request.Pricing.SalesCapBPS == 0 {
		request.Pricing.SalesCapBPS = a.config.SalesCapBPS
	}
	if request.ParentAgencyID != nil {
		inherited, inheritErr := inheritChildCostPolicy(a.db, *request.ParentAgencyID, request.Pricing)
		if inheritErr != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(inheritErr), nil)
			return
		}
		request.Pricing = inherited
	}
	if err := agencycontract.ValidatePolicy(request.Pricing); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
	// Root-created agencies receive the platform-owned procurement costs. A
	// child agency already carries an inherited cost snapshot and must not be
	// overwritten by the platform policy.
	if request.ParentAgencyID == nil {
		if _, err := a.effectivePolicy(request.Pricing); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
			return
		}
	}
	if request.ParentAgencyID != nil {
		if err := a.validateChildPolicy(*request.ParentAgencyID, request.Pricing); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
			return
		}
	}
	identity := currentIdentity(c)
	if identity == nil {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	operationID := deliveryOperationID(c, identity)
	binding := deliveryBindingFromContext(c, identity, c.Request.URL.Path, "")
	agency, password, delivery, err := a.createAgency(identity.ActorID, request.DisplayName, request.OperatorUsername, request.Pricing, operationID, binding, request.ParentAgencyID, ActorTypeRoot)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			status = http.StatusConflict
		}
		respondError(c, status, "create_failed", err.Error(), nil)
		return
	}
	view := agencyView{Agency: agency, InviteURL: a.inviteURL(agency.InviteCode), InviteQRURL: a.inviteQRURL(agency.InviteCode), OperatorUsername: request.OperatorUsername}
	c.Header("Cache-Control", "no-store")
	respondCreated(c, gin.H{"agency": view, "agency_id": agency.ID, "invite_code": agency.InviteCode, "invite_url": view.InviteURL, "invite_qr_url": view.InviteQRURL, "delivery_id": delivery.ID, "delivery_operation_id": delivery.OperationID, "temporary_password": password, "temporary_password_expires_at": delivery.ExpiresAt})
}

// CreateAgency creates the agency, operator, invite code, and first immutable
// policy in one transaction. The temporary password is returned only to the
// caller; only its hash is persisted.
func (a *App) CreateAgency(rootID int64, displayName, operatorUsername string, policy agencycontract.Policy) (model.Agency, string, error) {
	agency, password, _, err := a.createAgency(rootID, displayName, operatorUsername, policy, "", deliveryBinding{}, nil, ActorTypeRoot)
	return agency, password, err
}

func (a *App) CreateAgencyWithDelivery(rootID int64, displayName, operatorUsername string, policy agencycontract.Policy, deliveryOperationID string) (model.Agency, string, model.AgencyDeliverySecret, error) {
	if strings.TrimSpace(deliveryOperationID) == "" {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, errors.New("delivery operation required")
	}
	return a.createAgency(rootID, displayName, operatorUsername, policy, deliveryOperationID, deliveryBinding{}, nil, ActorTypeRoot)
}

func (a *App) createAgency(rootID int64, displayName, operatorUsername string, policy agencycontract.Policy, deliveryOperationID string, binding deliveryBinding, parentAgencyID *int64, creatorType string) (model.Agency, string, model.AgencyDeliverySecret, error) {
	if err := agencycontract.ValidatePolicy(policy); err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
	}
	if parentAgencyID == nil {
		effective, err := a.effectivePolicy(policy)
		if err != nil {
			return model.Agency{}, "", model.AgencyDeliverySecret{}, err
		}
		policy = effective
	}
	if rootID <= 0 {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, errors.New("root actor required")
	}
	password, err := randomToken(18)
	if err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
	}
	hash, err := common.Password2Hash(password)
	if err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
	}
	code, err := newCode(20)
	if err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
	}
	invite, err := newCode(10)
	if err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
	}
	policy.Revision = 1
	policyJSON, err := common.Marshal(policy)
	if err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
	}
	policyHash, err := agencycontract.CanonicalHash(policy)
	if err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
	}
	now := time.Now().UnixMilli()
	var agency model.Agency
	var delivery model.AgencyDeliverySecret
	err = a.db.Transaction(func(tx *gorm.DB) error {
		depth, err := validateAgencyParentTx(tx, parentAgencyID)
		if err != nil {
			return err
		}
		agency = model.Agency{ParentAgencyID: parentAgencyID, Depth: depth, Code: code, DisplayName: displayName, Status: AgencyStatusActive, InviteCode: invite, PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: creatorType, CreatedByID: rootID, CreatedAt: now / 1000, UpdatedAt: now / 1000}
		if err := tx.Create(&agency).Error; err != nil {
			return err
		}
		policyRow := model.AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: 1, PolicyJSON: string(policyJSON), PolicyHash: policyHash, CreatedByType: creatorType, CreatedByID: rootID, CreatedAtMS: now}
		if err := tx.Create(&policyRow).Error; err != nil {
			return err
		}
		if err := tx.Model(&agency).Updates(map[string]any{"current_policy_version_id": policyRow.ID}).Error; err != nil {
			return err
		}
		account := model.AgencyOperatorAccount{AgencyID: agency.ID, NormalizedUsername: strings.ToLower(operatorUsername), Username: operatorUsername, PasswordHash: hash, Status: OperatorStatusActive, MustChangePassword: true, AuthVersion: 1, CreatedAt: now / 1000, UpdatedAt: now / 1000}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		if err := a.writePolicyItems(tx, policyRow.ID, policy); err != nil {
			return err
		}
		if err := recordAuditTx(tx, nil, &Identity{ActorType: creatorType, ActorID: rootID},
			"agency.create", "agency", strconv.FormatInt(agency.ID, 10), "agency created",
			nil, map[string]any{"display_name": agency.DisplayName, "status": agency.Status, "policy_revision": policyRow.Revision}); err != nil {
			return err
		}
		if deliveryOperationID == "" {
			return nil
		}
		binding.ObjectID = "agency:" + strconv.FormatInt(agency.ID, 10)
		delivery, err = a.createDeliverySecretTx(tx, rootID, deliveryOperationID, password, now, binding, agency.ID, account.ID)
		return err
	})
	return agency, password, delivery, err
}

func (a *App) writePolicyItems(tx *gorm.DB, policyID int64, policy agencycontract.Policy) error {
	items := make([]model.AgencyPricePolicyItem, 0, len(policy.ModelOverrides)+1)
	for _, override := range policy.ModelOverrides {
		key, err := agencycontract.ModelKey(override.OriginModelName)
		if err != nil {
			return err
		}
		settlement, sales := policy.DefaultSettlementBPS, policy.DefaultSalesBPS
		if override.SettlementBPS != nil {
			settlement = *override.SettlementBPS
		}
		if override.SalesBPS != nil {
			sales = *override.SalesBPS
		}
		items = append(items, model.AgencyPricePolicyItem{PolicyVersionID: policyID, Scope: "model", ModelKey: key, OriginModelName: override.OriginModelName, SettlementBPS: override.SettlementBPS, ChildCostBPS: override.ChildCostBPS, SalesBPS: override.SalesBPS, ResolvedSettlementBPS: settlement, ResolvedSalesBPS: sales})
	}
	if len(items) == 0 {
		return nil
	}
	return tx.Create(&items).Error
}

func (a *App) listAgencies(c *gin.Context) {
	identity := currentIdentity(c)
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page同时使用", nil)
		return
	}
	size, err := cursorPageSize(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
		return
	}
	var cursor agencyCursor
	rawCursor := strings.TrimSpace(c.Query("cursor"))
	if rawCursor != "" {
		cursor, err = a.decodeCursor(rawCursor, c, "root_agencies", identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	query := a.db.Model(&model.Agency{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	if rawCursor != "" {
		query = query.Where("id < ?", cursor.PositionID)
	}
	var agencies []model.Agency
	if err := query.Order("id DESC").Limit(size + 1).Find(&agencies).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	hasMore := len(agencies) > size
	if hasMore {
		agencies = agencies[:size]
	}
	views := make([]agencyView, 0, len(agencies))
	operatorNames := make(map[int64]string, len(agencies))
	if len(agencies) > 0 {
		ids := make([]int64, 0, len(agencies))
		for _, agency := range agencies {
			ids = append(ids, agency.ID)
		}
		var accounts []model.AgencyOperatorAccount
		if err := a.db.Select("agency_id, username").Where("agency_id IN ?", ids).Find(&accounts).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", "读取代理商账号失败", nil)
			return
		}
		for _, account := range accounts {
			operatorNames[account.AgencyID] = account.Username
		}
	}
	for _, agency := range agencies {
		views = append(views, agencyView{Agency: agency, InviteURL: a.inviteURL(agency.InviteCode), InviteQRURL: a.inviteQRURL(agency.InviteCode), OperatorUsername: operatorNames[agency.ID]})
	}
	nextCursor := ""
	if hasMore && len(agencies) > 0 {
		last := agencies[len(agencies)-1]
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: "root_agencies", Scope: cursorScope(c, "root_agencies", identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID, PositionID: last.ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	respondOK(c, gin.H{"items": views, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}

// lookupAgency resolves an exact operator account or agency name for root-only
// management flows. It deliberately does not perform fuzzy search, avoiding a
// directory/enumeration endpoint while keeping internal IDs out of the UI.
func (a *App) lookupAgency(c *gin.Context) {
	reference := strings.TrimSpace(c.Query("query"))
	if reference == "" || len([]rune(reference)) > 191 {
		respondError(c, http.StatusBadRequest, "invalid_query", "请输入有效的代理商名称或账号", nil)
		return
	}
	var accounts []model.AgencyOperatorAccount
	if err := a.db.Select("agency_id, username").Where("username = ?", reference).Find(&accounts).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "查询代理商失败", nil)
		return
	}
	ids := make([]int64, 0, len(accounts))
	operatorNames := make(map[int64]string, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.AgencyID)
		operatorNames[account.AgencyID] = account.Username
	}
	if len(ids) == 0 {
		var agenciesByName []model.Agency
		if err := a.db.Where("display_name = ?", reference).Find(&agenciesByName).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", "查询代理商失败", nil)
			return
		}
		for _, agency := range agenciesByName {
			ids = append(ids, agency.ID)
		}
	}
	if len(ids) == 0 {
		respondOK(c, gin.H{"items": []agencyView{}})
		return
	}
	var agencies []model.Agency
	if err := a.db.Where("id IN ?", ids).Order("id ASC").Find(&agencies).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "查询代理商失败", nil)
		return
	}
	if len(operatorNames) < len(agencies) {
		var allAccounts []model.AgencyOperatorAccount
		if err := a.db.Select("agency_id, username").Where("agency_id IN ?", ids).Find(&allAccounts).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", "读取代理商账号失败", nil)
			return
		}
		for _, account := range allAccounts {
			operatorNames[account.AgencyID] = account.Username
		}
	}
	views := make([]agencyView, 0, len(agencies))
	for _, agency := range agencies {
		views = append(views, agencyView{Agency: agency, OperatorUsername: operatorNames[agency.ID]})
	}
	respondOK(c, gin.H{"items": views})
}
func (a *App) getAgency(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	var agency model.Agency
	if err = a.db.First(&agency, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "代理商不存在", nil)
		return
	}
	var account model.AgencyOperatorAccount
	_ = a.db.Where("agency_id = ?", id).First(&account).Error
	respondOK(c, agencyView{Agency: agency, InviteURL: a.inviteURL(agency.InviteCode), InviteQRURL: a.inviteQRURL(agency.InviteCode), OperatorUsername: account.Username})
}
func (a *App) updateAgency(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	var request updateAgencyRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if request.ExpectedVersion <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前代理商版本", nil)
		return
	}
	if request.DisplayName == nil && request.Reason == nil {
		respondError(c, http.StatusUnprocessableEntity, "no_changes", "至少提供一项修改", nil)
		return
	}
	if request.DisplayName != nil {
		name := strings.TrimSpace(*request.DisplayName)
		if name == "" || len([]rune(name)) > 191 {
			respondError(c, http.StatusUnprocessableEntity, "invalid_display_name", "代理商名称不能为空且长度不能超过191个字符", nil)
			return
		}
	}
	var agency model.Agency
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).First(&agency, id).Error; err != nil {
			return err
		}
		if request.ExpectedVersion != agency.Version {
			return fmt.Errorf("version conflict: %d", agency.Version)
		}
		updates := map[string]any{"version": agency.Version + 1, "updated_at": time.Now().Unix()}
		if request.DisplayName != nil {
			updates["display_name"] = strings.TrimSpace(*request.DisplayName)
		}
		if request.Reason != nil {
			updates["disabled_reason"] = strings.TrimSpace(*request.Reason)
		}
		result := tx.Model(&agency).Where("id = ? AND version = ?", id, agency.Version).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("version conflict: %d", agency.Version)
		}
		return recordAuditTx(tx, c, currentIdentity(c), "agency.update", "agency", strconv.FormatInt(id, 10), "agency updated",
			map[string]any{"display_name": agency.DisplayName, "version": agency.Version},
			map[string]any{"display_name": func() string {
				if request.DisplayName != nil {
					return strings.TrimSpace(*request.DisplayName)
				}
				return agency.DisplayName
			}(), "version": agency.Version + 1})
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusNotFound, "not_found", "代理商不存在", nil)
		return
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "version conflict:") {
			respondError(c, http.StatusConflict, "version_conflict", "代理商已被其他操作修改", gin.H{"version": agency.Version})
			return
		}
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	respondOK(c, gin.H{"version": agency.Version + 1})
}
func (a *App) disableAgency(c *gin.Context) { a.setAgencyStatus(c, AgencyStatusDisabled) }
func (a *App) enableAgency(c *gin.Context)  { a.setAgencyStatus(c, AgencyStatusActive) }
func (a *App) setAgencyStatus(c *gin.Context, status string) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	var request struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if request.ExpectedVersion <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前代理商版本", nil)
		return
	}
	var agency model.Agency
	now := time.Now().Unix()
	var affectedAgencyIDs []int64
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).First(&agency, id).Error; err != nil {
			return err
		}
		if request.ExpectedVersion != agency.Version {
			return fmt.Errorf("version conflict: %d", agency.Version)
		}
		if agency.Status == status {
			return fmt.Errorf("status already %s", status)
		}
		updates := map[string]any{"status": status, "version": agency.Version + 1, "state_revision": agency.StateRevision + 1, "updated_at": now}
		if status == AgencyStatusDisabled {
			updates["disabled_reason"] = request.Reason
			updates["disabled_at"] = now
		} else {
			updates["disabled_reason"] = ""
			updates["disabled_at"] = nil
		}
		if err := tx.Model(&agency).Updates(updates).Error; err != nil {
			return err
		}
		if status == AgencyStatusDisabled {
			// Disabling an ancestor suspends the complete subtree. This is
			// deliberately done in the same transaction as the root state
			// change so no descendant can accept a request in between.
			ids, descendantErr := agencySubtreeIDsTx(tx, agency.ID)
			if descendantErr != nil {
				return descendantErr
			}
			affectedAgencyIDs = ids
			for _, descendantID := range ids {
				if descendantID == agency.ID {
					continue
				}
				if err := tx.Model(&model.Agency{}).Where("id = ? AND status = ?", descendantID, AgencyStatusActive).Updates(map[string]any{
					"status": AgencyStatusDisabled, "state_revision": gorm.Expr("state_revision + 1"), "version": gorm.Expr("version + 1"), "disabled_reason": "ancestor_disabled", "disabled_at": now, "updated_at": now,
				}).Error; err != nil {
					return err
				}
			}
		}
		if status == AgencyStatusDisabled {
			var balances []model.AgencyCommissionBalance
			balanceIDs := affectedAgencyIDs
			if len(balanceIDs) == 0 {
				balanceIDs = []int64{id}
			}
			if err := tx.Where("agency_id IN ?", balanceIDs).Find(&balances).Error; err != nil {
				return err
			}
			for _, balance := range balances {
				if err := holdUnpaidWithdrawals(tx, balance.AgencyID, balance.CurrencyCode, "agency-disabled-"+strconv.FormatInt(balance.AgencyID, 10), time.Now().UnixMilli()); err != nil {
					return err
				}
			}
		}
		return recordAuditTx(tx, c, currentIdentity(c), "agency."+status, "agency", strconv.FormatInt(id, 10), request.Reason,
			map[string]any{"status": agency.Status, "version": agency.Version},
			map[string]any{"status": status, "version": agency.Version + 1, "state_revision": agency.StateRevision + 1})
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusNotFound, "not_found", "代理商不存在", nil)
		return
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "version conflict:") {
			respondError(c, http.StatusConflict, "version_conflict", "代理商已被其他操作修改", gin.H{"version": agency.Version})
			return
		}
		if strings.HasPrefix(err.Error(), "status already ") {
			respondError(c, http.StatusConflict, "status_unchanged", "代理商已经处于目标状态", gin.H{"status": status, "version": agency.Version})
			return
		}
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	if status == AgencyStatusDisabled {
		if len(affectedAgencyIDs) == 0 {
			affectedAgencyIDs = []int64{id}
		}
		_ = a.db.Model(&model.AgencySession{}).Where("agency_id IN ?", affectedAgencyIDs).Update("revoked_at", now).Error
	}
	respondOK(c, gin.H{"status": status, "version": agency.Version + 1})
}

func agencySubtreeIDsTx(tx *gorm.DB, rootID int64) ([]int64, error) {
	if rootID <= 0 {
		return nil, errors.New("invalid agency root")
	}
	ids := []int64{rootID}
	seen := map[int64]struct{}{rootID: {}}
	frontier := []int64{rootID}
	for len(frontier) > 0 {
		var children []model.Agency
		if err := tx.Select("id").Where("parent_agency_id IN ?", frontier).Find(&children).Error; err != nil {
			return nil, err
		}
		next := make([]int64, 0, len(children))
		for _, child := range children {
			if _, exists := seen[child.ID]; exists {
				continue
			}
			seen[child.ID] = struct{}{}
			ids = append(ids, child.ID)
			next = append(next, child.ID)
		}
		frontier = next
	}
	return ids, nil
}

func (a *App) resetPassword(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	password, err := randomToken(18)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "password_failed", "密码生成失败", nil)
		return
	}
	hash, err := common.Password2Hash(password)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "password_failed", "密码生成失败", nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	var account model.AgencyOperatorAccount
	var delivery model.AgencyDeliverySecret
	operationID := deliveryOperationID(c, identity)
	binding := deliveryBindingFromContext(c, identity, c.Request.URL.Path, "agency:"+strconv.FormatInt(id, 10))
	now := time.Now().UnixMilli()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).Where("agency_id = ?", id).First(&account).Error; err != nil {
			return err
		}
		if err := tx.Model(&account).Updates(map[string]any{"password_hash": hash, "must_change_password": true, "auth_version": account.AuthVersion + 1, "failed_count": 0, "locked_until": 0}).Error; err != nil {
			return err
		}
		if err := recordAuditTx(tx, c, identity, "agency.password_reset", "operator_account", strconv.FormatInt(account.ID, 10), "password reset",
			map[string]any{"auth_version": account.AuthVersion},
			map[string]any{"auth_version": account.AuthVersion + 1, "must_change_password": true}); err != nil {
			return err
		}
		created, err := a.createDeliverySecretTx(tx, identity.ActorID, operationID, password, now, binding, account.AgencyID, account.ID)
		if err != nil {
			return err
		}
		delivery = created
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusNotFound, "not_found", "代理商账号不存在", nil)
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	_ = a.db.Model(&model.AgencySession{}).Where("agency_id = ?", id).Update("revoked_at", time.Now().Unix()).Error
	c.Header("Cache-Control", "no-store")
	respondOK(c, gin.H{"delivery_id": delivery.ID, "delivery_operation_id": delivery.OperationID, "temporary_password": password, "temporary_password_expires_at": delivery.ExpiresAt})
}

func (a *App) inviteURL(code string) string {
	if a.config.PublicBaseURL == "" {
		return "/register?invite=" + code
	}
	return a.config.PublicBaseURL + "/register?invite=" + code
}
func (a *App) inviteQRURL(code string) string {
	base := a.config.PublicBaseURL
	if base == "" {
		base = ""
	}
	return strings.TrimRight(base, "/") + a.config.BasePath + "/api/v1/public/invitations/" + url.PathEscape(code) + "/qr"
}
func (a *App) publicInvitation(c *gin.Context) {
	code := strings.TrimSpace(c.Param("code"))
	var agency model.Agency
	if err := a.db.Where("invite_code = ?", code).First(&agency).Error; err != nil || agency.Status != AgencyStatusActive {
		respondError(c, http.StatusNotFound, "invite_not_found", "邀请码无效或已停用", nil)
		return
	}
	// The code is already present in the request URL.  Do not echo invite
	// delivery URLs or any agency/account fields from this anonymous endpoint:
	// the registration page only needs a display label and a boolean gate.
	c.Header("Cache-Control", "no-store")
	respondOK(c, gin.H{"display_name": agency.DisplayName, "can_register": common.AgencyOnboardingEnabled()})
}

func (a *App) publicInvitationQR(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	code := strings.TrimSpace(c.Param("code"))
	var agency model.Agency
	if err := a.db.Where("invite_code = ? AND status = ?", code, AgencyStatusActive).First(&agency).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	inviteURL, _, err := a.invitationLinks(agency.InviteCode)
	if err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	png, err := qrcode.Encode(inviteURL, qrcode.Medium, 512)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, "image/png", png)
}

// BindUserByInvite is the gateway-side primitive used inside the user
// registration transaction. It is safe to call only after the core user row
// has been created and before the transaction commits.
func BindUserByInvite(tx *gorm.DB, userID int64, inviteCode, source string, rootID int64) (model.AgencyUserBinding, error) {
	if !common.AgencyOnboardingEnabled() {
		return model.AgencyUserBinding{}, errAgencyOnboardingDisabled
	}
	if tx == nil || userID <= 0 || strings.TrimSpace(inviteCode) == "" {
		return model.AgencyUserBinding{}, errors.New("invalid invitation binding")
	}
	var agency model.Agency
	if err := model.AgencyLockForUpdate(tx).Where("invite_code = ?", inviteCode).First(&agency).Error; err != nil {
		return model.AgencyUserBinding{}, err
	}
	if agency.Status != AgencyStatusActive {
		return model.AgencyUserBinding{}, errors.New("agency invitation is disabled")
	}
	var user model.User
	if err := model.AgencyLockForUpdate(tx).Select("id, quota, billing_mode, funding_version").First(&user, userID).Error; err != nil {
		return model.AgencyUserBinding{}, err
	}
	var active model.AgencyActiveUserBinding
	if err := tx.Where("user_id = ?", userID).First(&active).Error; err == nil {
		return model.AgencyUserBinding{}, errors.New("user already has an agency binding")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AgencyUserBinding{}, err
	}
	now := time.Now().UnixMilli()
	binding := model.AgencyUserBinding{UserID: userID, AgencyID: agency.ID, Revision: 1, InviteSnapshot: agency.InviteCode, CreatedSource: source, EffectiveAtMS: now, RootActorID: rootID, CreatedAt: now / 1000}
	if err := tx.Create(&binding).Error; err != nil {
		return model.AgencyUserBinding{}, err
	}
	if err := tx.Create(&model.AgencyActiveUserBinding{UserID: userID, BindingID: binding.ID, Revision: 1, AgencyID: agency.ID, UpdatedAt: now / 1000}).Error; err != nil {
		return model.AgencyUserBinding{}, err
	}
	openingNonpaid := int64(0)
	openingDebt := int64(0)
	if source == "root_bind" {
		if user.BillingMode == model.AgencyDurableBillingMode {
			return model.AgencyUserBinding{}, errors.New("user is already durable")
		}
		if user.BillingMode != model.AgencyProvisioningBillingMode && user.BillingMode != "" && user.BillingMode != "legacy" {
			return model.AgencyUserBinding{}, errors.New("user billing mode does not permit provisioning")
		}
		var inFlight int64
		if err := tx.Model(&model.Task{}).Where("user_id = ? AND status NOT IN ?", userID, []model.TaskStatus{model.TaskStatusFailure, model.TaskStatusSuccess}).Count(&inFlight).Error; err != nil {
			return model.AgencyUserBinding{}, err
		}
		if inFlight > 0 {
			return model.AgencyUserBinding{}, errors.New("user has in-flight tasks; provisioning is blocked")
		}
		if user.Quota >= 0 {
			openingNonpaid = int64(user.Quota)
		} else {
			openingDebt = -int64(user.Quota)
		}
		if err := tx.Model(&model.User{}).Where("id = ? AND billing_mode <> ?", userID, model.AgencyDurableBillingMode).Updates(map[string]any{"billing_mode": model.AgencyDurableBillingMode, "funding_version": 1}).Error; err != nil {
			return model.AgencyUserBinding{}, err
		}
	}
	opening, err := common.Marshal(map[string]int64{"quota": int64(user.Quota), "paid": 0, "nonpaid": openingNonpaid, "debt": openingDebt})
	if err != nil {
		return model.AgencyUserBinding{}, err
	}
	openingSeq := int64(0)
	if openingNonpaid > 0 || openingDebt > 0 {
		openingSeq = 1
	}
	if err := tx.Create(&model.AgencyFundingAccount{UserID: userID, PaidAvailable: 0, NonpaidAvailable: openingNonpaid, DebtQuota: openingDebt, MoneySeq: openingSeq, Version: 1, OpeningSnapshot: string(opening), ReconcileBlocked: false, UpdatedAt: now / 1000}).Error; err != nil {
		return model.AgencyUserBinding{}, err
	}
	var openingLotID *int64
	if openingNonpaid > 0 {
		lot := model.AgencyFundingLot{UserID: userID, SourceKind: "provisioning_opening", SourceID: fmt.Sprintf("provisioning-%d", userID), CompletionSource: "provisioning", BonusInitial: openingNonpaid, BonusAvailable: openingNonpaid, MoneySeq: openingSeq, Version: 1, CreatedAt: now / 1000}
		if err := tx.Create(&lot).Error; err != nil {
			return model.AgencyUserBinding{}, err
		}
		openingLotID = &lot.ID
	}
	if openingDebt > 0 {
		if err := tx.Create(&model.AgencyFundingDebt{UserID: userID, OriginOperationID: fmt.Sprintf("provisioning-opening-%d", userID), DebtKind: "provisioning_opening", OriginalQuota: openingDebt, OutstandingQuota: openingDebt, CreatedAtMS: now}).Error; err != nil {
			return model.AgencyUserBinding{}, err
		}
	}
	if openingSeq > 0 {
		if err := tx.Create(&model.AgencyFundingLedger{OperationID: fmt.Sprintf("provisioning-opening-%d", userID), EntryNo: 0, UserID: userID, MoneySeq: openingSeq, SourceKind: "provisioning_opening", LotID: openingLotID, NonpaidDelta: openingNonpaid, DebtDelta: openingDebt, NonpaidAfter: openingNonpaid, DebtAfter: openingDebt, AgencyID: &agency.ID, BindingID: &binding.ID, CreatedAtMS: now}).Error; err != nil {
			return model.AgencyUserBinding{}, err
		}
	}
	return binding, nil
}

func (a *App) bindExistingUser(c *gin.Context) {
	userID, err := parseID(c.Param("user_id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return
	}
	var request struct {
		InviteCode string `json:"invite_code"`
		Reason     string `json:"reason"`
	}
	if err = c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || len(request.Reason) > 2000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reason", "必须提供绑定原因（不超过2000字节）", nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	job, created, err := a.enqueueProvisioningJob(int64(userID), strings.TrimSpace(request.InviteCode), identity.ActorID, strings.TrimSpace(request.Reason))
	if err != nil {
		if errors.Is(err, errAgencyOnboardingDisabled) {
			respondError(c, http.StatusServiceUnavailable, "onboarding_disabled", "代理商新用户开通暂时关闭", nil)
			return
		}
		status := http.StatusConflict
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		respondError(c, status, "bind_failed", err.Error(), nil)
		return
	}
	// A duplicate request returns the original durable job rather than
	// creating a second barrier.  The worker is intentionally asynchronous so
	// callers can inspect blocking tasks and cancel a long-running operation.
	respondAccepted(c, gin.H{"job_id": strconv.FormatInt(job.ID, 10), "status": job.Status, "created": created})
}

func (a *App) getProvisioning(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的任务ID", nil)
		return
	}
	var job model.AgencyProvisioningJob
	if err := a.db.First(&job, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	respondOK(c, provisioningView(job))
}

func (a *App) cancelProvisioning(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的任务ID", nil)
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 2000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reason", "必须提供取消原因（不超过2000字节）", nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	result := a.cancelProvisioningJob(id, request.Reason, identity.ActorID)
	if errors.Is(result, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	if result != nil {
		if strings.Contains(result.Error(), "not cancellable") {
			respondError(c, http.StatusConflict, "not_cancellable", "任务当前不可取消", nil)
		} else {
			respondError(c, http.StatusInternalServerError, "database_error", "取消任务失败", nil)
		}
		return
	}
	respondOK(c, gin.H{"status": "cancelled"})
}
func respondAccepted(c *gin.Context, data any) {
	c.JSON(http.StatusAccepted, apiResponse{Success: true, Data: data, RequestID: requestID(c)})
}

func (a *App) transferUser(c *gin.Context) {
	userID, err := parseID(c.Param("user_id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return
	}
	var request struct {
		TargetAgencyID          decimalInt64 `json:"target_agency_id"`
		ExpectedBindingRevision decimalInt64 `json:"expected_binding_revision"`
		Reason                  string       `json:"reason"`
	}
	if err = c.ShouldBindJSON(&request); err != nil || request.TargetAgencyID <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if request.ExpectedBindingRevision <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前归属版本", nil)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || len(request.Reason) > 2000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reason", "必须提供转移原因（不超过2000字节）", nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	targetAgencyID := int64(request.TargetAgencyID)
	var result model.AgencyActiveUserBinding
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var current model.AgencyActiveUserBinding
		if err := tx.Where("user_id = ?", userID).First(&current).Error; err != nil {
			return err
		}
		if int64(request.ExpectedBindingRevision) != current.Revision {
			return errors.New("binding revision conflict")
		}
		if targetAgencyID == current.AgencyID {
			return errors.New("target agency is the current agency")
		}
		var oldBinding model.AgencyUserBinding
		if err := tx.Where("id = ? AND ended_at_ms IS NULL", current.BindingID).First(&oldBinding).Error; err != nil {
			return errors.New("active binding changed")
		}
		// Lock both agencies in a deterministic order. The source may be
		// disabled (Root can still move its customers), but the destination must
		// be active at commit time.
		agencyIDs := []int64{oldBinding.AgencyID, targetAgencyID}
		if agencyIDs[0] > agencyIDs[1] {
			agencyIDs[0], agencyIDs[1] = agencyIDs[1], agencyIDs[0]
		}
		agencies := make(map[int64]model.Agency, 2)
		for _, agencyID := range agencyIDs {
			var agency model.Agency
			if err := model.AgencyLockForUpdate(tx).First(&agency, agencyID).Error; err != nil {
				return err
			}
			agencies[agencyID] = agency
		}
		// Re-read the pointer after agency locks and before changing history so
		// a concurrent transfer cannot create an ownership gap or overwrite a
		// newer revision.
		var lockedCurrent model.AgencyActiveUserBinding
		if err := model.AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&lockedCurrent).Error; err != nil {
			return err
		}
		if lockedCurrent.BindingID != current.BindingID || lockedCurrent.Revision != current.Revision {
			return errors.New("binding revision conflict")
		}
		if err := model.AgencyLockForUpdate(tx).Where("id = ? AND ended_at_ms IS NULL", lockedCurrent.BindingID).First(&oldBinding).Error; err != nil {
			return errors.New("active binding changed")
		}
		target := agencies[targetAgencyID]
		if target.Status != AgencyStatusActive {
			return errors.New("target agency is disabled")
		}
		var policyRow model.AgencyPricePolicyVersion
		if err := tx.Where("id = ? AND agency_id = ? AND revision = ?", target.CurrentPolicyVersionID, target.ID, target.PriceRevision).First(&policyRow).Error; err != nil {
			return errors.New("target agency has no valid published policy")
		}
		var policy agencycontract.Policy
		if err := common.Unmarshal([]byte(policyRow.PolicyJSON), &policy); err != nil {
			return errors.New("target agency policy is invalid")
		}
		if err := agencycontract.ValidatePolicy(policy); err != nil {
			return err
		}
		var account model.AgencyFundingAccount
		if err := model.AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
			return errors.New("customer funding account is unavailable")
		}
		if current.Revision == int64(^uint64(0)>>1) {
			return errors.New("binding revision exhausted")
		}
		now := time.Now().UnixMilli()
		ended := now
		if err := tx.Model(&model.AgencyUserBinding{}).Where("id = ? AND ended_at_ms IS NULL", current.BindingID).Updates(map[string]any{"ended_at_ms": ended}).Error; err != nil {
			return err
		}
		binding := &model.AgencyUserBinding{UserID: userID, AgencyID: target.ID, Revision: current.Revision + 1, InviteSnapshot: target.InviteCode, CreatedSource: "root_transfer", EffectiveAtMS: now, RootActorID: identity.ActorID, Reason: request.Reason, CreatedAt: now / 1000}
		if err := tx.Create(binding).Error; err != nil {
			return err
		}
		result = model.AgencyActiveUserBinding{UserID: userID, BindingID: binding.ID, Revision: binding.Revision, AgencyID: target.ID, UpdatedAt: now / 1000}
		if err := tx.Model(&model.AgencyActiveUserBinding{}).Where("user_id = ?", userID).Updates(map[string]any{"binding_id": binding.ID, "revision": binding.Revision, "agency_id": target.ID, "updated_at": now / 1000}).Error; err != nil {
			return err
		}
		return recordAuditTx(tx, c, identity, "user.transfer", "user_binding", strconv.FormatInt(userID, 10), request.Reason,
			map[string]any{"agency_id": oldBinding.AgencyID, "binding_id": current.BindingID, "revision": current.Revision},
			map[string]any{"agency_id": target.ID, "binding_id": binding.ID, "revision": binding.Revision})
	})
	if err != nil {
		respondError(c, http.StatusConflict, "transfer_failed", err.Error(), nil)
		return
	}
	respondOK(c, gin.H{"user_id": strconv.FormatInt(result.UserID, 10), "agency_id": strconv.FormatInt(result.AgencyID, 10), "binding_id": strconv.FormatInt(result.BindingID, 10), "revision": strconv.FormatInt(result.Revision, 10)})
}

func (a *App) enterAgency(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可代管", nil)
		return
	}
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的代理商ID", nil)
		return
	}
	var agency model.Agency
	if err = a.db.Where("id = ? AND status = ?", id, AgencyStatusActive).First(&agency).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "代理商不存在或已停用", nil)
		return
	}
	if err = a.db.Model(&model.AgencySession{}).Where("id = ? AND actor_type = ?", identity.SessionID, ActorTypeRoot).Update("agency_id", agency.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	identity.AgencyID = &agency.ID
	respondOK(c, gin.H{"agency_id": agency.ID, "acting_as": "root"})
}

func (a *App) leaveAgency(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	if err := a.db.Model(&model.AgencySession{}).Where("id = ? AND actor_type = ?", identity.SessionID, ActorTypeRoot).Update("agency_id", nil).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	identity.AgencyID = nil
	respondOK(c, gin.H{"acting_as": "root"})
}
