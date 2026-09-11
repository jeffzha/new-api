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

func (a *App) createAgencyHTTP(c *gin.Context) {
	var request createAgencyRequest
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
	if request.Pricing.MinSpreadBPS == 0 {
		request.Pricing.MinSpreadBPS = a.config.MinSpreadBPS
	}
	if err := agencycontract.ValidatePolicy(request.Pricing); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", err.Error(), nil)
		return
	}
	identity := currentIdentity(c)
	if identity == nil {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	operationID := deliveryOperationID(c, identity)
	binding := deliveryBindingFromContext(c, identity, c.Request.URL.Path, "")
	agency, password, delivery, err := a.createAgency(identity.ActorID, request.DisplayName, request.OperatorUsername, request.Pricing, operationID, binding)
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
	agency, password, _, err := a.createAgency(rootID, displayName, operatorUsername, policy, "", deliveryBinding{})
	return agency, password, err
}

func (a *App) CreateAgencyWithDelivery(rootID int64, displayName, operatorUsername string, policy agencycontract.Policy, deliveryOperationID string) (model.Agency, string, model.AgencyDeliverySecret, error) {
	if strings.TrimSpace(deliveryOperationID) == "" {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, errors.New("delivery operation required")
	}
	return a.createAgency(rootID, displayName, operatorUsername, policy, deliveryOperationID, deliveryBinding{})
}

func (a *App) createAgency(rootID int64, displayName, operatorUsername string, policy agencycontract.Policy, deliveryOperationID string, binding deliveryBinding) (model.Agency, string, model.AgencyDeliverySecret, error) {
	if err := agencycontract.ValidatePolicy(policy); err != nil {
		return model.Agency{}, "", model.AgencyDeliverySecret{}, err
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
		agency = model.Agency{Code: code, DisplayName: displayName, Status: AgencyStatusActive, InviteCode: invite, PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: rootID, CreatedAt: now / 1000, UpdatedAt: now / 1000}
		if err := tx.Create(&agency).Error; err != nil {
			return err
		}
		policyRow := model.AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: 1, PolicyJSON: string(policyJSON), PolicyHash: policyHash, CreatedByType: ActorTypeRoot, CreatedByID: rootID, CreatedAtMS: now}
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
		if err := recordAuditTx(tx, nil, &Identity{ActorType: ActorTypeRoot, ActorID: rootID},
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
		items = append(items, model.AgencyPricePolicyItem{PolicyVersionID: policyID, Scope: "model", ModelKey: key, OriginModelName: override.OriginModelName, SettlementBPS: override.SettlementBPS, SalesBPS: override.SalesBPS, ResolvedSettlementBPS: settlement, ResolvedSalesBPS: sales})
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
	for _, agency := range agencies {
		views = append(views, agencyView{Agency: agency, InviteURL: a.inviteURL(agency.InviteCode), InviteQRURL: a.inviteQRURL(agency.InviteCode)})
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
			var balances []model.AgencyCommissionBalance
			if err := tx.Where("agency_id = ?", id).Find(&balances).Error; err != nil {
				return err
			}
			for _, balance := range balances {
				if err := holdUnpaidWithdrawals(tx, id, balance.CurrencyCode, "agency-disabled-"+strconv.FormatInt(id, 10), time.Now().UnixMilli()); err != nil {
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
		_ = a.db.Model(&model.AgencySession{}).Where("agency_id = ?", id).Update("revoked_at", now).Error
	}
	respondOK(c, gin.H{"status": status, "version": agency.Version + 1})
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
	respondOK(c, gin.H{"display_name": agency.DisplayName, "can_register": true})
}

func (a *App) publicInvitationQR(c *gin.Context) {
	code := strings.TrimSpace(c.Param("code"))
	var agency model.Agency
	if err := a.db.Where("invite_code = ? AND status = ?", code, AgencyStatusActive).First(&agency).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	png, err := qrcode.Encode(a.inviteURL(agency.InviteCode), qrcode.Medium, 512)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "image/png", png)
}

// BindUserByInvite is the gateway-side primitive used inside the user
// registration transaction. It is safe to call only after the core user row
// has been created and before the transaction commits.
func BindUserByInvite(tx *gorm.DB, userID int64, inviteCode, source string, rootID int64) (model.AgencyUserBinding, error) {
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
	if err := tx.Create(&model.AgencyFundingAccount{UserID: userID, PaidAvailable: 0, NonpaidAvailable: openingNonpaid, DebtQuota: openingDebt, MoneySeq: 0, Version: 1, OpeningSnapshot: string(opening), ReconcileBlocked: false, UpdatedAt: now / 1000}).Error; err != nil {
		return model.AgencyUserBinding{}, err
	}
	if openingNonpaid > 0 {
		lot := model.AgencyFundingLot{UserID: userID, SourceKind: "provisioning_opening", SourceID: fmt.Sprintf("provisioning-%d", userID), CompletionSource: "provisioning", BonusInitial: openingNonpaid, MoneySeq: 1, Version: 1, CreatedAt: now / 1000}
		if err := tx.Create(&lot).Error; err != nil {
			return model.AgencyUserBinding{}, err
		}
		lotID := lot.ID
		if err := tx.Create(&model.AgencyFundingLedger{OperationID: fmt.Sprintf("provisioning-opening-%d", userID), EntryNo: 0, UserID: userID, MoneySeq: 1, SourceKind: "provisioning_opening", LotID: &lotID, NonpaidDelta: openingNonpaid, NonpaidAfter: openingNonpaid, DebtAfter: openingDebt, AgencyID: &agency.ID, BindingID: &binding.ID, CreatedAtMS: now}).Error; err != nil {
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
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	job, created, err := a.enqueueProvisioningJob(int64(userID), strings.TrimSpace(request.InviteCode), identity.ActorID, strings.TrimSpace(request.Reason))
	if err != nil {
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
	respondAccepted(c, gin.H{"job_id": job.ID, "status": job.Status, "created": created, "blocking_tasks": job.BlockingTasks})
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
	respondOK(c, job)
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
	_ = c.ShouldBindJSON(&request)
	result := a.cancelProvisioningJob(id, request.Reason)
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
		TargetAgencyID          int64  `json:"target_agency_id"`
		ExpectedBindingRevision int64  `json:"expected_binding_revision"`
		Reason                  string `json:"reason"`
	}
	if err = c.ShouldBindJSON(&request); err != nil || request.TargetAgencyID <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if request.ExpectedBindingRevision <= 0 {
		respondError(c, http.StatusUnprocessableEntity, "expected_version_required", "必须提供当前归属版本", nil)
		return
	}
	identity := currentIdentity(c)
	var result model.AgencyActiveUserBinding
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var current model.AgencyActiveUserBinding
		if err := tx.Where("user_id = ?", userID).First(&current).Error; err != nil {
			return err
		}
		if request.ExpectedBindingRevision != current.Revision {
			return errors.New("binding revision conflict")
		}
		var oldBinding model.AgencyUserBinding
		if err := tx.Where("id = ? AND ended_at_ms IS NULL", current.BindingID).First(&oldBinding).Error; err != nil {
			return errors.New("active binding changed")
		}
		// Lock both agencies in a deterministic order. The source may be
		// disabled (Root can still move its customers), but the destination must
		// be active at commit time.
		agencyIDs := []int64{oldBinding.AgencyID, request.TargetAgencyID}
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
		target := agencies[request.TargetAgencyID]
		if target.Status != AgencyStatusActive {
			return errors.New("target agency is disabled")
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
	respondAccepted(c, result)
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
