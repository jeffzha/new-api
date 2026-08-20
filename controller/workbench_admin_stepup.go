package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/workbenchbridge"
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
		identity, ok := middleware.GetSessionAuthIdentity(c)
		rawProof := strings.TrimSpace(c.GetHeader("X-Security-Proof"))
		if !ok || rawProof == "" {
			respondWorkbenchError(c, http.StatusForbidden, "recent 2FA or Passkey verification is required")
			return
		}
		details, err := service.VerifySecurityProofDetails(rawProof, identity, securityProofScopeWorkbenchStepUp, []string{secureVerificationMethod2FA, secureVerificationMethodPasskey})
		if err != nil {
			respondWorkbenchError(c, http.StatusForbidden, "recent 2FA or Passkey verification is required")
			return
		}
		digest := sha256.Sum256([]byte(rawProof))
		authenticatedAt = details.IssuedAt.UTC()
		amr = map[string]string{
			secureVerificationMethod2FA:     "otp",
			secureVerificationMethodPasskey: "webauthn",
		}[details.Method]
		reauthNonce = base64.RawURLEncoding.EncodeToString(digest[:])
	default:
		respondWorkbenchError(c, http.StatusBadRequest, "method must be password or secure_verification")
		return
	}
	bridge.issueSessionTicketWithProof(c, workbenchbridge.SurfaceAdmin, authenticatedAt, []string{amr}, reauthNonce)
}
