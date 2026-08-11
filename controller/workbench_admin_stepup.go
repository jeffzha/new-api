package controller

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/workbenchbridge"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

type workbenchAdminStepUpRequest struct {
	Method   string `json:"method"`
	Password string `json:"password,omitempty"`
}

func IssueWorkbenchAdminStepUpTicket(c *gin.Context) {
	config := workbenchbridge.ConfigFromEnvironment()
	if !config.Enabled {
		respondWorkbenchError(c, http.StatusNotFound, "workbench is disabled")
		return
	}
	tickets, err := workbenchbridge.NewControlClient(config)
	if err != nil {
		common.SysError("failed to initialize workbench control client: " + err.Error())
		respondWorkbenchError(c, http.StatusServiceUnavailable, "workbench identity service is unavailable")
		return
	}
	NewWorkbenchIdentityBridge(config, tickets, nil).AdminStepUpTicket(c)
}

func (bridge *WorkbenchIdentityBridge) AdminStepUpTicket(c *gin.Context) {
	if !sameOriginWorkbenchRequest(c.Request) {
		respondWorkbenchError(c, http.StatusForbidden, "a same-origin request is required")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request workbenchAdminStepUpRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondWorkbenchError(c, http.StatusBadRequest, "method is required")
		return
	}
	request.Method = strings.ToLower(strings.TrimSpace(request.Method))
	var authenticatedAt time.Time
	var amr, reauthNonce string
	switch request.Method {
	case "password":
		user, err := bridge.lookupStepUpUser(c.GetInt("id"))
		if err != nil || user == nil || user.Role != common.RoleRootUser || user.Status != common.UserStatusEnabled || user.Password == "" || !common.ValidatePasswordAndHash(request.Password, user.Password) {
			respondWorkbenchError(c, http.StatusForbidden, "administrator password verification failed")
			return
		}
		nonce := make([]byte, 32)
		if _, err = rand.Read(nonce); err != nil {
			respondWorkbenchError(c, http.StatusInternalServerError, "failed to create step-up proof")
			return
		}
		authenticatedAt, amr = time.Now().UTC(), "pwd"
		reauthNonce = base64.RawURLEncoding.EncodeToString(nonce)
	case "secure_verification":
		var ok bool
		var err error
		authenticatedAt, amr, reauthNonce, ok, err = consumeWorkbenchSecureVerification(c)
		if err != nil {
			respondWorkbenchError(c, http.StatusInternalServerError, "failed to consume secure verification")
			return
		}
		if !ok {
			respondWorkbenchError(c, http.StatusForbidden, "recent 2FA or Passkey verification is required")
			return
		}
	default:
		respondWorkbenchError(c, http.StatusBadRequest, "method must be password or secure_verification")
		return
	}
	bridge.issueSessionTicketWithProof(c, workbenchbridge.SurfaceAdmin, authenticatedAt, []string{amr}, reauthNonce)
}

func consumeWorkbenchSecureVerification(c *gin.Context) (time.Time, string, string, bool, error) {
	session := sessions.Default(c)
	verifiedAtRaw := session.Get(SecureVerificationSessionKey)
	methodRaw := session.Get(secureVerificationMethodSessionKey)
	session.Delete(SecureVerificationSessionKey)
	session.Delete(secureVerificationMethodSessionKey)
	if err := session.Save(); err != nil {
		return time.Time{}, "", "", false, err
	}
	verifiedAt, ok := verifiedAtRaw.(int64)
	method, methodOK := methodRaw.(string)
	if !ok || !methodOK {
		return time.Time{}, "", "", false, nil
	}
	now := time.Now().UTC()
	authenticatedAt := time.Unix(verifiedAt, 0).UTC()
	if authenticatedAt.After(now.Add(time.Minute)) || now.Sub(authenticatedAt) >= time.Duration(SecureVerificationTimeout)*time.Second {
		return time.Time{}, "", "", false, nil
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return time.Time{}, "", "", false, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	switch strings.ToLower(strings.TrimSpace(method)) {
	case secureVerificationMethod2FA:
		return authenticatedAt, "otp", nonce, true, nil
	case secureVerificationMethodPasskey:
		return authenticatedAt, "webauthn", nonce, true, nil
	default:
		return time.Time{}, "", "", false, nil
	}
}
