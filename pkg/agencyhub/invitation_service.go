package agencyhub

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// invitationLinks shares the registration target with the public QR handler.
// Never guess an origin from Host/Forwarded headers or issue relative QR data.
func (a *App) invitationLinks(code string) (string, string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(a.config.PublicBaseURL), "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Opaque != "" {
		return "", "", errors.New("public invitation URL is not configured correctly")
	}
	if strings.TrimSpace(code) == "" || !strings.HasPrefix(a.config.BasePath, "/") || strings.ContainsAny(a.config.BasePath, "?#\\") {
		return "", "", errors.New("public invitation URL is not configured correctly")
	}
	prefix := strings.TrimRight(base.Path, "/")
	registration := *base
	registration.Path, registration.RawPath = prefix+"/register", ""
	registration.RawQuery = url.Values{"invite": []string{code}}.Encode()
	qr := *base
	qr.Path, qr.RawPath = prefix+strings.TrimRight(a.config.BasePath, "/")+"/api/v1/public/invitations/"+code+"/qr", ""
	// Invite codes are URL path segments; escaping also protects historical
	// records if a code contains characters beyond the generated alphabet.
	qr.RawPath = strings.TrimRight(base.EscapedPath(), "/") + strings.TrimRight(a.config.BasePath, "/") + "/api/v1/public/invitations/" + url.PathEscape(code) + "/qr"
	return registration.String(), qr.String(), nil
}

func (a *App) getOwnInvitation(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	identity := currentIdentity(c)
	if identity == nil || identity.AgencyID == nil || (identity.ActorType != ActorTypeOperator && identity.ActorType != ActorTypeRoot) {
		respondError(c, http.StatusForbidden, "agency_required", "当前会话没有代理商范围", nil)
		return
	}
	// Only the authenticated session chooses scope. URL/query/body agency IDs
	// are deliberately not consulted, including when Root manages an agency.
	var agency model.Agency
	if err := a.db.Where("id = ? AND status = ?", *identity.AgencyID, AgencyStatusActive).First(&agency).Error; err != nil {
		respondError(c, http.StatusForbidden, "agency_unavailable", "当前代理商不可用", nil)
		return
	}
	inviteURL, qrURL, err := a.invitationLinks(agency.InviteCode)
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "invitation_url_unavailable", "邀请链接尚未配置，请联系平台管理员", nil)
		return
	}
	respondOK(c, gin.H{"display_name": agency.DisplayName, "invite_code": agency.InviteCode, "invite_url": inviteURL, "invite_qr_url": qrURL})
}
