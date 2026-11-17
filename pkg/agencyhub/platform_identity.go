package agencyhub

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type platformLinkRequest struct {
	Proof     string `json:"proof"`
	Password  string `json:"password"`
	RequestID string `json:"request_id"`
}

type platformUnlinkRequest struct {
	Password string `json:"password"`
}

var (
	errPlatformLinkConflict      = errors.New("platform or agency account is already linked")
	errPlatformLinkProofInvalid  = errors.New("platform link proof is invalid")
	errOperatorReauthFailed      = errors.New("operator reauthentication failed")
	errOperatorReauthRateLimited = errors.New("operator reauthentication rate limited")
)

func (a *App) linkPlatformAccount(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeOperator {
		respondError(c, http.StatusForbidden, "operator_required", "仅代理商账号可关联主站账号", nil)
		return
	}
	var request platformLinkRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	if strings.TrimSpace(request.Proof) == "" || request.Password == "" || len(request.Password) > 256 || request.RequestID == "" || len(request.RequestID) > 128 {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	var account model.AgencyOperatorAccount
	if err := a.db.First(&account, identity.ActorID).Error; err != nil || account.Status != OperatorStatusActive {
		respondError(c, http.StatusUnauthorized, "invalid_credentials", "代理商密码错误", nil)
		return
	}
	if err := a.verifyOperatorReauthentication(&account, request.Password); errors.Is(err, errOperatorReauthRateLimited) {
		respondError(c, http.StatusTooManyRequests, "account_locked", "密码验证失败次数过多，请稍后重试", nil)
		return
	} else if err != nil {
		respondError(c, http.StatusUnauthorized, "invalid_credentials", "代理商密码错误", nil)
		return
	}
	if len(a.ssoPublicKey) != ed25519.PublicKeySize {
		respondError(c, http.StatusServiceUnavailable, "sso_unconfigured", "主站账号关联尚未配置", nil)
		return
	}
	claims, err := VerifySSOTicket(a.ssoPublicKey, request.Proof, "new-api", "agency-hub-account-link")
	if err != nil || claims.ActorType != ActorTypePlatformUser || claims.Subject <= 0 || claims.SourceSID == "" || claims.SessionVersion <= 0 || claims.ObjectID != request.RequestID {
		respondError(c, http.StatusUnauthorized, "invalid_link_proof", "主站账号验证已失效", nil)
		return
	}
	now := time.Now().Unix()
	var platformUsername string
	var created bool
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := consumeSSOTicket(tx, claims, now); err != nil {
			return err
		}
		var source model.UserSession
		if err := tx.Where("sid = ? AND user_id = ? AND version = ?", claims.SourceSID, claims.Subject, claims.SessionVersion).First(&source).Error; err != nil {
			return errPlatformLinkProofInvalid
		}
		if source.Status != model.UserSessionStatusActive || source.RevokedAt != 0 || source.ExpiresAt <= now || source.UserAuthVersion != claims.UserAuthVersion {
			return errPlatformLinkProofInvalid
		}
		var user model.User
		if err := tx.Select("id, username, role, status, auth_version").First(&user, claims.Subject).Error; err != nil {
			return errPlatformLinkProofInvalid
		}
		if user.Status != common.UserStatusEnabled || user.Role == common.RoleRootUser || user.AuthVersion != source.UserAuthVersion {
			return errPlatformLinkProofInvalid
		}
		platformUsername = user.Username
		var existing model.AgencyOperatorIdentityBinding
		lookup := model.AgencyLockForUpdate(tx).Where("operator_account_id = ? OR platform_user_id = ?", account.ID, int64(user.Id)).First(&existing)
		if lookup.Error == nil {
			if existing.OperatorAccountID == account.ID && existing.PlatformUserID == int64(user.Id) && existing.AgencyID == account.AgencyID {
				return nil
			}
			return errPlatformLinkConflict
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		binding := model.AgencyOperatorIdentityBinding{OperatorAccountID: account.ID, AgencyID: account.AgencyID, PlatformUserID: int64(user.Id), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&binding).Error; err != nil {
			return errPlatformLinkConflict
		}
		created = true
		return recordAuditTx(tx, c, identity, "auth.platform_account.link", "operator_account", stringID(account.ID), "operator linked an authenticated platform account", nil, map[string]any{"platform_username": user.Username})
	})
	if errors.Is(err, errSSOTicketConsumed) || errors.Is(err, errPlatformLinkProofInvalid) {
		respondError(c, http.StatusUnauthorized, "invalid_link_proof", "主站账号验证已使用或失效", nil)
		return
	}
	if errors.Is(err, errPlatformLinkConflict) {
		respondError(c, http.StatusConflict, "link_failed", "关联失败：主站账号或代理商账号已被其他账号关联", nil)
		return
	}
	if err != nil {
		common.SysError("failed to link platform account: " + err.Error())
		respondError(c, http.StatusInternalServerError, "database_error", "关联失败", nil)
		return
	}
	respondOK(c, gin.H{"linked": true, "created": created, "username": platformUsername})
}

func (a *App) unlinkPlatformAccount(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeOperator {
		respondError(c, http.StatusForbidden, "operator_required", "仅代理商账号可解除主站关联", nil)
		return
	}
	var request platformUnlinkRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Password == "" || len(request.Password) > 256 {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	var account model.AgencyOperatorAccount
	if err := a.db.First(&account, identity.ActorID).Error; err != nil || account.Status != OperatorStatusActive {
		respondError(c, http.StatusUnauthorized, "invalid_credentials", "代理商密码错误", nil)
		return
	}
	if err := a.verifyOperatorReauthentication(&account, request.Password); errors.Is(err, errOperatorReauthRateLimited) {
		respondError(c, http.StatusTooManyRequests, "account_locked", "密码验证失败次数过多，请稍后重试", nil)
		return
	} else if err != nil {
		respondError(c, http.StatusUnauthorized, "invalid_credentials", "代理商密码错误", nil)
		return
	}
	now := time.Now().Unix()
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var binding model.AgencyOperatorIdentityBinding
		if err := model.AgencyLockForUpdate(tx).Where("operator_account_id = ?", account.ID).First(&binding).Error; err != nil {
			return err
		}
		var user model.User
		_ = tx.Select("username").First(&user, binding.PlatformUserID).Error
		if err := tx.Delete(&binding).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.AgencySession{}).
			Where("actor_type = ? AND actor_id = ? AND source_user_id = ? AND revoked_at IS NULL", ActorTypeOperator, account.ID, binding.PlatformUserID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		return recordAuditTx(tx, c, identity, "auth.platform_account.unlink", "operator_account", stringID(account.ID), "operator unlinked a platform account", map[string]any{"platform_username": user.Username}, nil)
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusNotFound, "link_not_found", "当前代理商账号尚未关联主站账号", nil)
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "解除关联失败", nil)
		return
	}
	respondOK(c, gin.H{"linked": false})
}

func (a *App) verifyOperatorReauthentication(account *model.AgencyOperatorAccount, password string) error {
	now := time.Now().Unix()
	if account.LockedUntil > now {
		return errOperatorReauthRateLimited
	}
	if !common.ValidatePasswordAndHash(password, account.PasswordHash) {
		failedCount := account.FailedCount + 1
		updates := map[string]any{"failed_count": failedCount}
		if failedCount >= a.config.MaxLoginAttempts {
			updates["failed_count"] = 0
			updates["locked_until"] = now + int64(a.config.LoginLockout/time.Second)
		}
		_ = a.db.Model(&model.AgencyOperatorAccount{}).Where("id = ?", account.ID).Updates(updates).Error
		return errOperatorReauthFailed
	}
	if account.FailedCount != 0 || account.LockedUntil != 0 {
		_ = a.db.Model(&model.AgencyOperatorAccount{}).Where("id = ?", account.ID).Updates(map[string]any{"failed_count": 0, "locked_until": 0}).Error
	}
	return nil
}

func consumeSSOTicket(tx *gorm.DB, claims SSOTicketClaims, now int64) error {
	var used model.AgencySSOTicketUse
	if err := tx.Where("jti = ?", claims.JTI).First(&used).Error; err == nil {
		return errSSOTicketConsumed
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&model.AgencySSOTicketUse{JTI: claims.JTI, ActorID: claims.Subject, SourceSID: claims.SourceSID, ExpiresAt: claims.ExpiresAt, ConsumedAt: now, CreatedAt: now}).Error
}
