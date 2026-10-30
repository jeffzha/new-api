package controller

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/uac"
	"github.com/gin-gonic/gin"
)

func UACStart(c *gin.Context) {
	setAuthNoStore(c)
	if !uac.Enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	authorization, binding, err := uac.Default.Start(c.Request.Context())
	if err != nil {
		logger.LogWarn(c.Request.Context(), "UAC discovery failed; check issuer connectivity and certificate")
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "暂时无法连接统一认证中心，请联系运维人员检查认证地址和证书。"})
		return
	}
	writeUACCookie(c, "new_api_uac_login", binding, 300)
	c.Redirect(http.StatusFound, authorization)
}

func UACCallback(c *gin.Context) {
	setAuthNoStore(c)
	if !uac.Enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	binding, _ := c.Cookie("new_api_uac_login")
	writeUACCookie(c, "new_api_uac_login", "", -1)
	session, err := uac.Default.Complete(c.Request.Context(), c.Query("state"), binding, c.Query("code"))
	if err != nil {
		logger.LogWarn(c.Request.Context(), "UAC callback rejected; protocol validation failed")
		c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in?uac_error=authentication")
		return
	}
	user, err := model.ResolveUACUser(uac.Default.Config.Issuer, session.Identity.Subject, session.Identity.Name, uac.Default.Config.InitialRole)
	if err != nil {
		logger.LogWarn(c.Request.Context(), "UAC callback rejected; local operator account unavailable")
		c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in?uac_error=account")
		return
	}
	// Preserve the existing local factor policy. A UAC login may not bypass an
	// enrolled local factor; its challenge is completed by the existing form.
	challenge, err := service.StartLoginVerification(user, "uac")
	if err != nil {
		c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in?uac_error=verification")
		return
	}
	if challenge != nil {
		ticket, err := uac.Default.HoldLogin(challenge, challenge.FlowToken, session)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		writeUACCookie(c, "new_api_uac_handoff", ticket, 300)
		c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in?uac_complete=1")
		return
	}
	bundle, err := service.CreateLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "uac", c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in?uac_error=session")
		return
	}
	if err := uac.Default.Attach(bundle.Session.SID, session); err != nil {
		_, _ = model.RevokeUserSession(user.Id, bundle.Session.SID, "uac_session_unavailable")
		c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in?uac_error=session")
		return
	}
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	c.Set("login_method", "uac")
	model.UpdateUserLastLoginAt(user.Id)
	recordLoginAudit(user, c)
	c.Redirect(http.StatusFound, uac.Prefix()+"/dashboard")
}

func UACComplete(c *gin.Context) {
	setAuthNoStore(c)
	if !uac.Enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	ticket, _ := c.Cookie("new_api_uac_handoff")
	writeUACCookie(c, "new_api_uac_handoff", "", -1)
	handoff, err := uac.Default.TakeLogin(ticket)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": handoff.Data})
}

// UACLogout runs after the existing cookie origin guard and revokes the local
// refresh session before returning the provider's logout URL to the browser.
func UACLogout(c *gin.Context) {
	setAuthNoStore(c)
	if !uac.Enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	raw, _ := c.Cookie(uac.RefreshCookie())
	sid, ok := service.RefreshTokenSID(raw)
	if !ok {
		service.ClearRefreshCookie(c)
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}
	expectedSID := c.GetHeader("X-Auth-Session")
	if expectedSID != "" && expectedSID != sid {
		writeAuthSessionError(c, service.ErrLoginSessionMismatch)
		return
	}
	if _, err := service.RevokeProjectLogin(raw, expectedSID); err != nil {
		writeAuthSessionError(c, err)
		return
	}
	service.ClearRefreshCookie(c)
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("UAC local logout completed (session_id=%s)", sid))
	state, err := uac.Random()
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	writeUACCookie(c, "new_api_uac_logout", state, 300)
	endpoint, err := uac.Default.Logout(c.Request.Context(), sid, state)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "本平台已退出，统一认证中心暂时无法连接。"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"logout_url": endpoint}})
}

func UACLogoutCallback(c *gin.Context) {
	setAuthNoStore(c)
	if !uac.Enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	state, _ := c.Cookie("new_api_uac_logout")
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(c.Query("state"))) != 1 {
		c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in")
		return
	}
	writeUACCookie(c, "new_api_uac_logout", "", -1)
	c.Redirect(http.StatusFound, uac.Prefix()+"/sign-in")
}

func writeUACCookie(c *gin.Context, name, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{Name: name, Value: value, Path: uac.Prefix() + "/api/oauth/uac", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: time.Now().Add(time.Duration(maxAge) * time.Second)})
}
