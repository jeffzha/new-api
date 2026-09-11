package resellerhub

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type resellerRequest struct {
	Code               string `json:"code"`
	Name               string `json:"name"`
	Status             string `json:"status"`
	DefaultDiscountBPS int    `json:"default_discount_bps"`
	QuotaCarrierUserID int    `json:"quota_carrier_user_id"`
}

type membershipRequest struct {
	NewAPIUserID int    `json:"new_api_user_id"`
	Role         string `json:"role"`
}

func validResellerStatus(status string) bool {
	return status == "active" || status == "suspended" || status == "closed"
}

func validMembershipRole(role string) bool {
	return role == HubRoleResellerAdmin || role == HubRoleResellerViewer
}

func (a *App) validateDiscount(discount int) error {
	if discount < a.config.MinDiscountBPS || discount > a.config.MaxDiscountBPS {
		return errors.New("discount is outside the configured range")
	}
	return nil
}

func (a *App) validateQuotaCarrier(userID int, excludeResellerID int) (*model.User, error) {
	var user model.User
	if err := a.db.Where("id = ? AND status = ?", userID, common.UserStatusEnabled).First(&user).Error; err != nil {
		return nil, errors.New("quota carrier account not found or disabled")
	}
	if user.Role != common.RoleCommonUser {
		return nil, errors.New("quota carrier must be an enabled common user")
	}
	// A durable agency customer is governed by the agency funding ledger.
	// Reusing that account as the legacy reseller quota carrier would create a
	// second wallet mutation path that cannot produce the agency journal/lot
	// facts, so fail closed before the reseller is created or reassigned.
	if user.BillingMode == model.AgencyDurableBillingMode || user.BillingMode == model.AgencyProvisioningBillingMode {
		return nil, errors.New("quota carrier cannot be an agency-managed user")
	}
	var count int64
	query := a.db.Model(&Reseller{}).Where("quota_carrier_user_id = ?", userID)
	if excludeResellerID > 0 {
		query = query.Where("id <> ?", excludeResellerID)
	}
	if err := query.Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, errors.New("quota carrier account is already assigned")
	}
	return &user, nil
}

func (a *App) listResellers(c *gin.Context) {
	query := a.db.Model(&Reseller{})
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		query = query.Where("code = ? OR name LIKE ?", search, "%"+search+"%")
	}
	page := queryPositiveInt(c, "page", 1, 1000000)
	pageSize := queryPositiveInt(c, "page_size", 50, 200)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	var rows []Reseller
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		var carrier model.User
		_ = a.db.Select("id", "username", "display_name", "quota", "status").First(&carrier, row.QuotaCarrierUserID).Error
		var customerCount int64
		_ = a.db.Model(&Customer{}).Where("reseller_id = ?", row.ID).Count(&customerCount).Error
		items = append(items, gin.H{
			"id": row.ID, "code": row.Code, "name": row.Name, "status": row.Status,
			"default_discount_bps": row.DefaultDiscountBPS, "quota_carrier_user_id": row.QuotaCarrierUserID,
			"quota_carrier_username": carrier.Username, "carrier_quota": carrier.Quota,
			"customer_count": customerCount, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt,
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

func (a *App) createReseller(c *gin.Context) {
	var input resellerRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	if input.Status == "" {
		input.Status = "active"
	}
	if len(input.Code) > 64 || input.Name == "" || len(input.Name) > 128 || !validResellerStatus(input.Status) {
		respondError(c, http.StatusBadRequest, "invalid reseller fields")
		return
	}
	if err := a.validateDiscount(input.DefaultDiscountBPS); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.validateQuotaCarrier(input.QuotaCarrierUserID, 0); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	identity := currentIdentity(c)
	now := time.Now().Unix()
	row := Reseller{
		Code:               input.Code,
		Name:               input.Name,
		Status:             input.Status,
		DefaultDiscountBPS: input.DefaultDiscountBPS,
		QuotaCarrierUserID: input.QuotaCarrierUserID,
		CreatedByUserID:    identity.NewAPIUserID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		version := DiscountVersion{
			ResellerID: row.ID, CustomerID: 0, DiscountBPS: row.DefaultDiscountBPS,
			EffectiveAt: now, Reason: "initial reseller discount", CreatedByUserID: identity.NewAPIUserID, CreatedAt: now,
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		return appendAudit(tx, c, &row.ID, "reseller.create", "reseller", strconv.Itoa(row.ID), gin.H{"code": row.Code, "name": row.Name})
	})
	if err != nil {
		respondError(c, http.StatusConflict, err.Error())
		return
	}
	respondCreated(c, row)
}

func (a *App) getReseller(c *gin.Context) {
	id, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	var row Reseller
	if err := a.db.First(&row, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "reseller not found")
		return
	}
	var members []Membership
	_ = a.db.Where("reseller_id = ?", id).Order("id").Find(&members).Error
	memberViews := make([]gin.H, 0, len(members))
	for _, membership := range members {
		var user model.User
		_ = a.db.Select("id", "username", "display_name", "status").First(&user, membership.NewAPIUserID).Error
		memberViews = append(memberViews, gin.H{
			"id": membership.ID, "reseller_id": membership.ResellerID, "new_api_user_id": membership.NewAPIUserID,
			"username": user.Username, "display_name": user.DisplayName, "role": membership.Role,
			"status": membership.Status, "created_at": membership.CreatedAt, "updated_at": membership.UpdatedAt,
		})
	}
	var carrier model.User
	_ = a.db.Select("id", "username", "display_name", "quota", "status", "group").First(&carrier, row.QuotaCarrierUserID).Error
	respondOK(c, gin.H{"reseller": row, "members": memberViews, "quota_carrier": carrier})
}

func (a *App) updateReseller(c *gin.Context) {
	id, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	var row Reseller
	if err := a.db.First(&row, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "reseller not found")
		return
	}
	var input resellerRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if row.Status == "closed" && input.Status != "" && input.Status != "closed" {
		respondError(c, http.StatusConflict, "closed reseller cannot be reopened")
		return
	}
	updates := map[string]any{"updated_at": time.Now().Unix()}
	if name := strings.TrimSpace(input.Name); name != "" {
		updates["name"] = name
	}
	if input.Status != "" {
		if !validResellerStatus(input.Status) {
			respondError(c, http.StatusBadRequest, "invalid status")
			return
		}
		updates["status"] = input.Status
	}
	if input.DefaultDiscountBPS != 0 {
		if err := a.validateDiscount(input.DefaultDiscountBPS); err != nil {
			respondError(c, http.StatusBadRequest, err.Error())
			return
		}
		updates["default_discount_bps"] = input.DefaultDiscountBPS
	}
	if input.QuotaCarrierUserID != 0 && input.QuotaCarrierUserID != row.QuotaCarrierUserID {
		var mappingCount int64
		if err := a.db.Model(&CustomerToken{}).Where("reseller_id = ?", row.ID).Count(&mappingCount).Error; err != nil {
			respondError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if mappingCount > 0 {
			respondError(c, http.StatusConflict, "quota carrier cannot change after a customer token exists")
			return
		}
		if _, err := a.validateQuotaCarrier(input.QuotaCarrierUserID, row.ID); err != nil {
			respondError(c, http.StatusBadRequest, err.Error())
			return
		}
		updates["quota_carrier_user_id"] = input.QuotaCarrierUserID
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Reseller{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return err
		}
		if discount, changed := updates["default_discount_bps"].(int); changed {
			now := time.Now().Unix()
			if err := tx.Model(&DiscountVersion{}).Where("reseller_id = ? AND customer_id = ? AND ended_at IS NULL", row.ID, 0).Update("ended_at", now).Error; err != nil {
				return err
			}
			version := DiscountVersion{ResellerID: row.ID, CustomerID: 0, DiscountBPS: discount, EffectiveAt: now, Reason: "reseller default discount changed", CreatedByUserID: currentIdentity(c).NewAPIUserID, CreatedAt: now}
			if err := tx.Create(&version).Error; err != nil {
				return err
			}
		}
		if status, _ := updates["status"].(string); status == "suspended" || status == "closed" {
			if err := a.disableResellerTokens(tx, row.ID); err != nil {
				return err
			}
		}
		return appendAudit(tx, c, &row.ID, "reseller.update", "reseller", strconv.Itoa(row.ID), updates)
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.db.First(&row, row.ID).Error
	if input.Status == "suspended" || input.Status == "closed" {
		a.invalidateResellerTokenCaches(row.ID)
	}
	respondOK(c, row)
}

func (a *App) createMembership(c *gin.Context) {
	resellerID, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	var reseller Reseller
	if err := a.db.First(&reseller, resellerID).Error; err != nil {
		respondError(c, http.StatusNotFound, "reseller not found")
		return
	}
	var input membershipRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil || input.NewAPIUserID <= 0 || !validMembershipRole(input.Role) {
		respondError(c, http.StatusBadRequest, "invalid membership")
		return
	}
	var user model.User
	if err := a.db.Where("id = ? AND status = ?", input.NewAPIUserID, common.UserStatusEnabled).First(&user).Error; err != nil {
		respondError(c, http.StatusBadRequest, "user not found or disabled")
		return
	}
	if user.Role >= common.RoleRootUser {
		respondError(c, http.StatusBadRequest, "root users do not need reseller membership")
		return
	}
	now := time.Now().Unix()
	membership := Membership{ResellerID: resellerID, NewAPIUserID: input.NewAPIUserID, Role: input.Role, Status: membershipStatusActive, CreatedAt: now, UpdatedAt: now}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&membership).Error; err != nil {
			return err
		}
		return appendAudit(tx, c, &resellerID, "membership.create", "membership", strconv.Itoa(membership.ID), gin.H{"new_api_user_id": input.NewAPIUserID, "role": input.Role})
	})
	if err != nil {
		respondError(c, http.StatusConflict, err.Error())
		return
	}
	respondCreated(c, membership)
}

func (a *App) deleteMembership(c *gin.Context) {
	resellerID, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	membershipID, ok := parsePositiveID(c, "member_id")
	if !ok {
		return
	}
	var membership Membership
	if err := a.db.Where("id = ? AND reseller_id = ?", membershipID, resellerID).First(&membership).Error; err != nil {
		respondError(c, http.StatusNotFound, "membership not found")
		return
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&membership).Update("status", membershipStatusDisabled).Error; err != nil {
			return err
		}
		return appendAudit(tx, c, &resellerID, "membership.disable", "membership", strconv.Itoa(membership.ID), nil)
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	respondOK(c, gin.H{"id": membership.ID, "status": membershipStatusDisabled})
}

func (a *App) disableResellerTokens(tx *gorm.DB, resellerID int) error {
	var mappings []CustomerToken
	if err := tx.Where("reseller_id = ? AND status IN ?", resellerID, []string{"active", "retiring"}).Find(&mappings).Error; err != nil {
		return err
	}
	ids := make([]int, 0, len(mappings))
	for _, mapping := range mappings {
		ids = append(ids, mapping.NewAPITokenID)
	}
	if len(ids) == 0 {
		return nil
	}
	return tx.Model(&model.Token{}).Where("id IN ?", ids).Update("status", common.TokenStatusDisabled).Error
}

func (a *App) invalidateResellerTokenCaches(resellerID int) {
	var mappings []CustomerToken
	if err := a.db.Where("reseller_id = ?", resellerID).Find(&mappings).Error; err != nil {
		return
	}
	for _, mapping := range mappings {
		var token model.Token
		if err := a.db.Unscoped().Select("key").First(&token, mapping.NewAPITokenID).Error; err == nil {
			_ = model.InvalidateTokenCache(token.Key)
		}
	}
}

func (a *App) referenceData(c *gin.Context) {
	identity := currentIdentity(c)
	users := make([]gin.H, 0)
	if identity.HubRole == HubRoleSuperAdmin {
		var rows []model.User
		if err := a.db.Select("id", "username", "display_name", "role", "status", "quota", "group").Order("id").Find(&rows).Error; err != nil {
			respondError(c, http.StatusInternalServerError, err.Error())
			return
		}
		for _, row := range rows {
			users = append(users, gin.H{"id": row.Id, "username": row.Username, "display_name": row.DisplayName, "role": row.Role, "status": row.Status, "quota": row.Quota, "group": row.Group})
		}
	}
	var abilities []model.Ability
	_ = a.db.Where("enabled = ?", true).Find(&abilities).Error
	groupSet := map[string]struct{}{}
	modelSet := map[string]struct{}{}
	for _, ability := range abilities {
		groupSet[ability.Group] = struct{}{}
		modelSet[ability.Model] = struct{}{}
	}
	groups := make([]string, 0, len(groupSet))
	models := make([]string, 0, len(modelSet))
	for group := range groupSet {
		groups = append(groups, group)
	}
	for modelName := range modelSet {
		models = append(models, modelName)
	}
	sort.Strings(groups)
	sort.Strings(models)
	respondOK(c, gin.H{
		"users":  users,
		"groups": groups,
		"models": models,
		"limits": gin.H{
			"max_user_tokens":                operation_setting.GetMaxUserTokens(),
			"max_active_tokens_per_customer": 1,
			"unlimited_quota_allowed":        false,
			"min_discount_bps":               a.config.MinDiscountBPS,
			"max_discount_bps":               a.config.MaxDiscountBPS,
		},
	})
}
