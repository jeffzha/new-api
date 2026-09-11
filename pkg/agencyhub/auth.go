package agencyhub

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var dummyHash struct {
	sync.Once
	value string
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nonce    string `json:"nonce"`
}
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (a *App) authNonce(c *gin.Context) {
	nonce, err := randomToken(24)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "nonce_failed", "unable to create nonce", nil)
		return
	}
	c.SetCookie("agency_login_nonce", nonce, 300, a.config.BasePath, "", true, true)
	respondOK(c, gin.H{"nonce": nonce, "expires_in": 300})
}

func (a *App) login(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "invalid login request", nil)
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	if request.Username == "" || len(request.Password) < 1 || len(request.Password) > 256 {
		respondError(c, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误", nil)
		return
	}
	if nonce, err := c.Cookie("agency_login_nonce"); err != nil || nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(request.Nonce)) != 1 {
		respondError(c, http.StatusUnauthorized, "invalid_nonce", "登录请求已失效", nil)
		return
	}
	c.SetCookie("agency_login_nonce", "", -1, a.config.BasePath, "", true, true)
	normalized := strings.ToLower(request.Username)
	var account model.AgencyOperatorAccount
	err := a.db.Where("normalized_username = ?", normalized).First(&account).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusInternalServerError, "database_error", "登录失败", nil)
		return
	}
	now := time.Now().Unix()
	validPassword := false
	if err == nil {
		validPassword = common.ValidatePasswordAndHash(request.Password, account.PasswordHash)
	} else {
		dummyHash.Do(func() { dummyHash.value, _ = common.Password2Hash("agency-invalid-password") })
		_ = common.ValidatePasswordAndHash(request.Password, dummyHash.value)
	}
	if err != nil || !validPassword || account.Status != OperatorStatusActive {
		if err == nil {
			updates := map[string]any{"failed_count": account.FailedCount + 1}
			if account.FailedCount+1 >= a.config.MaxLoginAttempts {
				updates["locked_until"] = now + int64(a.config.LoginLockout/time.Second)
				updates["failed_count"] = 0
			}
			_ = a.db.Model(&model.AgencyOperatorAccount{}).Where("id = ?", account.ID).Updates(updates).Error
		}
		respondError(c, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误", nil)
		return
	}
	if account.LockedUntil > now {
		respondError(c, http.StatusTooManyRequests, "account_locked", "登录失败次数过多，请稍后重试", nil)
		return
	}
	var agency model.Agency
	if err = a.db.First(&agency, account.AgencyID).Error; err != nil || agency.Status != AgencyStatusActive {
		respondError(c, http.StatusForbidden, "agency_disabled", "代理商已停用", nil)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "session_failed", "登录失败", nil)
		return
	}
	sessionToken, err := randomToken(32)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "session_failed", "登录失败", nil)
		return
	}
	session := &model.AgencySession{TokenHash: tokenHash(sessionToken), ActorType: ActorTypeOperator, ActorID: account.ID, AgencyID: &account.AgencyID, AuthVersion: account.AuthVersion, CSRFHash: tokenHash(csrf), LastSeenAt: now, CreatedAt: now, ExpiresAt: now + int64(a.config.SessionAbsolute/time.Second)}
	if err = a.db.Create(session).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "session_failed", "登录失败", nil)
		return
	}
	_ = a.db.Model(&model.AgencyOperatorAccount{}).Where("id = ?", account.ID).Updates(map[string]any{"failed_count": 0, "locked_until": 0, "last_login_at": now}).Error
	a.setSessionCookies(c, sessionToken, csrf, session.ExpiresAt)
	respondOK(c, gin.H{"must_change_password": account.MustChangePassword, "expires_at": session.ExpiresAt})
}

func (a *App) setSessionCookies(c *gin.Context, sessionToken, csrf string, expiresAt int64) {
	maxAge := int(expiresAt - time.Now().Unix())
	if maxAge < 0 {
		maxAge = 0
	}
	c.SetCookie(a.config.CookieName, sessionToken, maxAge, a.config.BasePath, "", true, true)
	c.SetCookie("agency_csrf", csrf, maxAge, a.config.BasePath, "", true, false)
}

func (a *App) sessionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(a.config.CookieName)
		if err != nil || token == "" {
			respondError(c, http.StatusUnauthorized, "unauthorized", "请先登录", nil)
			return
		}
		var session model.AgencySession
		if err = a.db.Where("token_hash = ?", tokenHash(token)).First(&session).Error; err != nil || session.RevokedAt != nil {
			respondError(c, http.StatusUnauthorized, "unauthorized", "会话已失效", nil)
			return
		}
		now := time.Now().Unix()
		if session.ExpiresAt <= now || session.LastSeenAt+int64(a.config.SessionIdle/time.Second) <= now {
			respondError(c, http.StatusUnauthorized, "session_expired", "会话已过期", nil)
			return
		}
		identity := &Identity{
			ActorType:            session.ActorType,
			ActorID:              session.ActorID,
			AgencyID:             session.AgencyID,
			SessionID:            session.ID,
			SourceSID:            session.SourceSID,
			SourceSessionVersion: session.SourceSessionVersion,
		}
		if session.ActorType == ActorTypeOperator {
			var account model.AgencyOperatorAccount
			if err = a.db.First(&account, session.ActorID).Error; err != nil || account.Status != OperatorStatusActive || account.AuthVersion != session.AuthVersion {
				respondError(c, http.StatusUnauthorized, "unauthorized", "账号状态已改变", nil)
				return
			}
			identity.Username = account.Username
			identity.MustChangePassword = account.MustChangePassword
			if session.AgencyID == nil {
				respondError(c, http.StatusUnauthorized, "unauthorized", "账号范围无效", nil)
				return
			}
			var agency model.Agency
			if err = a.db.First(&agency, *session.AgencyID).Error; err != nil || agency.Status != AgencyStatusActive {
				respondError(c, http.StatusForbidden, "agency_disabled", "代理商已停用", nil)
				return
			}
		} else if session.ActorType != ActorTypeRoot {
			respondError(c, http.StatusUnauthorized, "unauthorized", "会话类型无效", nil)
			return
		} else if err := a.validateRootSourceSession(session); err != nil {
			// A source dashboard logout, role change, auth-version bump, or
			// expiry must immediately invalidate the sidecar session too.
			_ = a.db.Model(&model.AgencySession{}).Where("id = ?", session.ID).Update("revoked_at", now).Error
			respondError(c, http.StatusUnauthorized, "source_session_invalid", "源管理员会话已失效", nil)
			return
		}
		if identity.ActorType == ActorTypeOperator && identity.MustChangePassword && !passwordChangeAllowedPath(c.Request.URL.Path) {
			respondError(c, http.StatusForbidden, "password_change_required", "首次登录后必须先修改密码", nil)
			return
		}
		c.Set("agency_identity", identity)
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			csrf := c.GetHeader("X-CSRF-Token")
			if csrf == "" || subtle.ConstantTimeCompare([]byte(tokenHash(csrf)), []byte(session.CSRFHash)) != 1 {
				respondError(c, http.StatusForbidden, "csrf_failed", "CSRF校验失败", nil)
				return
			}
			if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" && a.config.PublicBaseURL != "" && !sameConfiguredOrigin(origin, a.config.PublicBaseURL) {
				respondError(c, http.StatusForbidden, "origin_failed", "请求来源不受信任", nil)
				return
			}
		}
		_ = a.db.Model(&model.AgencySession{}).Where("id = ?", session.ID).Update("last_seen_at", now).Error
		c.Next()
	}
}

func passwordChangeAllowedPath(path string) bool {
	return strings.HasSuffix(path, "/auth/me") ||
		strings.HasSuffix(path, "/auth/change-password") ||
		strings.HasSuffix(path, "/auth/logout")
}

func sameConfiguredOrigin(origin, configured string) bool {
	o, err1 := url.Parse(strings.TrimSpace(origin))
	c, err2 := url.Parse(strings.TrimSpace(configured))
	if err1 != nil || err2 != nil || o.Scheme == "" || o.Host == "" || c.Scheme == "" || c.Host == "" {
		return false
	}
	return strings.EqualFold(o.Scheme, c.Scheme) && strings.EqualFold(o.Host, c.Host)
}

func (a *App) validateRootSourceSession(session model.AgencySession) error {
	if session.SourceSID == "" || session.SourceUserID <= 0 || session.SourceSessionVersion <= 0 {
		return errors.New("missing source session binding")
	}
	var source model.UserSession
	if err := a.db.Where("sid = ? AND user_id = ?", session.SourceSID, session.SourceUserID).First(&source).Error; err != nil {
		return err
	}
	now := time.Now().Unix()
	if source.Status != model.UserSessionStatusActive || source.RevokedAt != 0 || source.ExpiresAt <= now || source.Version != session.SourceSessionVersion {
		return errors.New("source session is inactive")
	}
	var user model.User
	if err := a.db.Select("id, role, status, auth_version").First(&user, session.SourceUserID).Error; err != nil {
		return err
	}
	if user.Role != common.RoleRootUser || user.Status != common.UserStatusEnabled || user.AuthVersion != source.UserAuthVersion {
		return errors.New("source root identity changed")
	}
	return nil
}

func currentIdentity(c *gin.Context) *Identity {
	value, ok := c.Get("agency_identity")
	if !ok {
		return nil
	}
	identity, _ := value.(*Identity)
	return identity
}
func (a *App) requireRoot() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := currentIdentity(c)
		if identity == nil || identity.ActorType != ActorTypeRoot {
			respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
			return
		}
		c.Next()
	}
}

// CreateRootSession is used by the host new-api integration after validating a
// real browser session. It intentionally accepts the root identity as an
// argument instead of trusting a PAT or an arbitrary HTTP header.
func (a *App) CreateRootSession(rootUserID int64, sourceSID string, sourceSessionVersion int64) (token, csrf string, err error) {
	if rootUserID <= 0 || strings.TrimSpace(sourceSID) == "" {
		return "", "", errors.New("root browser session required")
	}
	token, err = randomToken(32)
	if err != nil {
		return "", "", err
	}
	csrf, err = randomToken(24)
	if err != nil {
		return "", "", err
	}
	now := time.Now().Unix()
	session := &model.AgencySession{TokenHash: tokenHash(token), ActorType: ActorTypeRoot, ActorID: rootUserID, SourceSID: sourceSID, SourceUserID: rootUserID, SourceSessionVersion: sourceSessionVersion, AuthVersion: 1, CSRFHash: tokenHash(csrf), LastSeenAt: now, CreatedAt: now, ExpiresAt: now + int64(a.config.SessionAbsolute/time.Second)}
	if err = a.db.Create(session).Error; err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

func (a *App) me(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil {
		respondError(c, http.StatusUnauthorized, "unauthorized", "请先登录", nil)
		return
	}
	respondOK(c, identity)
}
func (a *App) logout(c *gin.Context) {
	identity := currentIdentity(c)
	now := time.Now().Unix()
	if identity != nil {
		_ = a.db.Model(&model.AgencySession{}).Where("id = ?", identity.SessionID).Update("revoked_at", now).Error
	}
	c.SetCookie(a.config.CookieName, "", -1, a.config.BasePath, "", true, true)
	c.SetCookie("agency_csrf", "", -1, a.config.BasePath, "", true, false)
	respondOK(c, gin.H{"logged_out": true})
}

func (a *App) changePassword(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeOperator {
		respondError(c, http.StatusForbidden, "operator_required", "仅代理商账号可修改密码", nil)
		return
	}
	var request changePasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	if len(request.NewPassword) < 12 || len(request.NewPassword) > 72 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_password", "密码长度必须为12至72字节", nil)
		return
	}
	hash, err := common.Password2Hash(request.NewPassword)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "password_failed", "密码处理失败", nil)
		return
	}
	now := time.Now().Unix()
	var nextAuthVersion int64
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var account model.AgencyOperatorAccount
		if err := model.AgencyLockForUpdate(tx).First(&account, identity.ActorID).Error; err != nil {
			return err
		}
		if account.Status != OperatorStatusActive || !common.ValidatePasswordAndHash(request.CurrentPassword, account.PasswordHash) {
			return errors.New("invalid credentials")
		}
		nextAuthVersion = account.AuthVersion + 1
		if err := tx.Model(&account).Updates(map[string]any{
			"password_hash":        hash,
			"must_change_password": false,
			"auth_version":         nextAuthVersion,
			"failed_count":         0,
			"locked_until":         0,
			"updated_at":           now,
		}).Error; err != nil {
			return err
		}
		// Keep the session which performed the password change alive, while
		// invalidating every other session created with the old credential.
		if err := tx.Model(&model.AgencySession{}).
			Where("actor_type = ? AND actor_id = ? AND id <> ? AND revoked_at IS NULL", ActorTypeOperator, identity.ActorID, identity.SessionID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&model.AgencySession{}).
			Where("id = ? AND actor_type = ? AND actor_id = ?", identity.SessionID, ActorTypeOperator, identity.ActorID).
			Update("auth_version", nextAuthVersion).Error
	})
	if err != nil {
		if err.Error() == "invalid credentials" {
			respondError(c, http.StatusUnauthorized, "invalid_credentials", "当前密码错误", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, "database_error", "密码修改失败", nil)
		return
	}
	identity.MustChangePassword = false
	respondOK(c, gin.H{"changed": true})
}

func (a *App) operatorVerify(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeOperator {
		respondError(c, http.StatusForbidden, "operator_required", "仅代理商账号可验证", nil)
		return
	}
	var request struct {
		Password string `json:"password"`
		Action   string `json:"action"`
		ObjectID string `json:"object_id"`
		BodyHash string `json:"body_hash"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Action = strings.TrimSpace(request.Action)
	request.ObjectID = strings.TrimSpace(request.ObjectID)
	request.BodyHash = strings.TrimSpace(request.BodyHash)
	if request.Action == "" || len(request.Action) > 128 || request.ObjectID == "" || len(request.ObjectID) > 191 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_verification", "验证范围无效", nil)
		return
	}
	if decoded, err := hex.DecodeString(request.BodyHash); err != nil || len(decoded) != 32 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_verification", "body_hash必须是SHA-256十六进制摘要", nil)
		return
	}
	var account model.AgencyOperatorAccount
	if err := a.db.First(&account, identity.ActorID).Error; err != nil || !common.ValidatePasswordAndHash(request.Password, account.PasswordHash) {
		respondError(c, http.StatusUnauthorized, "invalid_credentials", "密码错误", nil)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "verification_failed", "验证失败", nil)
		return
	}
	hash := tokenHash(token)
	now := time.Now().Unix()
	expiry := now + 300
	if err = a.db.Create(&model.AgencyVerificationUse{JTI: hash, ActorType: ActorTypeOperator, ActorID: identity.ActorID, Action: request.Action, ObjectID: request.ObjectID, BodyHash: strings.ToLower(request.BodyHash), ExpiresAt: expiry, CreatedAt: now}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "验证失败", nil)
		return
	}
	c.Header("Cache-Control", "no-store")
	respondOK(c, gin.H{"proof": token, "expires_at": expiry})
}
