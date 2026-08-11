package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/workbenchbridge"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maximumIdentityStatusBodyBytes = 4096

type WorkbenchUserLookup func(userID int) (*model.User, error)

type WorkbenchIdentityBridge struct {
	config     workbenchbridge.Config
	tickets    workbenchbridge.TicketIssuer
	lookupUser WorkbenchUserLookup
	now        func() time.Time
}

type workbenchAPIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

type workbenchSessionTicketData struct {
	Ticket    string `json:"ticket"`
	ExpiresAt int64  `json:"expires_at"`
	ExpiresIn int64  `json:"expires_in"`
}

type workbenchIdentityStatusRequest struct {
	UserID          int    `json:"user_id"`
	IdentityVersion string `json:"identity_version,omitempty"`
}

type workbenchIdentityStatusData struct {
	UserID          int    `json:"user_id"`
	Exists          bool   `json:"exists"`
	Enabled         bool   `json:"enabled"`
	IdentityVersion string `json:"identity_version,omitempty"`
	IsSuperAdmin    bool   `json:"is_super_admin"`
}

func NewWorkbenchIdentityBridge(
	config workbenchbridge.Config,
	tickets workbenchbridge.TicketIssuer,
	lookupUser WorkbenchUserLookup,
) *WorkbenchIdentityBridge {
	if lookupUser == nil {
		lookupUser = func(userID int) (*model.User, error) {
			return model.GetUserById(userID, false)
		}
	}
	return &WorkbenchIdentityBridge{
		config:     config,
		tickets:    tickets,
		lookupUser: lookupUser,
		now:        time.Now,
	}
}

func IssueWorkbenchSessionTicket(c *gin.Context) {
	config := workbenchbridge.ConfigFromEnvironment()
	if !config.Enabled {
		respondWorkbenchError(c, http.StatusNotFound, "workbench is disabled")
		return
	}
	tickets, err := workbenchbridge.NewControlClient(config)
	if err != nil {
		common.SysError("failed to initialize workbench ticket manager: " + err.Error())
		respondWorkbenchError(c, http.StatusServiceUnavailable, "workbench identity service is unavailable")
		return
	}
	NewWorkbenchIdentityBridge(config, tickets, nil).SessionTicket(c)
}

func IssueWorkbenchAdminSessionTicket(c *gin.Context) {
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
	NewWorkbenchIdentityBridge(config, tickets, nil).AdminSessionTicket(c)
}

func GetWorkbenchIdentityStatus(c *gin.Context) {
	config := workbenchbridge.ConfigFromEnvironment()
	NewWorkbenchIdentityBridge(config, nil, nil).IdentityStatus(c)
}

func GetWorkbenchAdminIdentityStatus(c *gin.Context) {
	config := workbenchbridge.ConfigFromEnvironment()
	NewWorkbenchIdentityBridge(config, nil, nil).AdminIdentityStatus(c)
}

func (bridge *WorkbenchIdentityBridge) SessionTicket(c *gin.Context) {
	bridge.issueSessionTicket(c, workbenchbridge.SurfaceWorkbench)
}

func (bridge *WorkbenchIdentityBridge) AdminSessionTicket(c *gin.Context) {
	bridge.issueSessionTicket(c, workbenchbridge.SurfaceAdmin)
}

func (bridge *WorkbenchIdentityBridge) issueSessionTicket(c *gin.Context, surface string) {
	bridge.issueSessionTicketWithProof(c, surface, time.Time{}, nil, "")
}

func (bridge *WorkbenchIdentityBridge) issueSessionTicketWithProof(c *gin.Context, surface string, authenticatedAt time.Time, amr []string, reauthNonce string) {
	if !bridge.config.Enabled {
		respondWorkbenchError(c, http.StatusNotFound, "workbench is disabled")
		return
	}
	if err := bridge.config.Validate(); err != nil || bridge.tickets == nil {
		respondWorkbenchError(c, http.StatusServiceUnavailable, "workbench identity service is unavailable")
		return
	}
	if c.GetBool("use_access_token") {
		respondWorkbenchError(c, http.StatusForbidden, "an authenticated web session is required")
		return
	}

	userID := c.GetInt("id")
	if userID <= 0 {
		respondWorkbenchError(c, http.StatusUnauthorized, "an authenticated web session is required")
		return
	}
	if !sameOriginWorkbenchRequest(c.Request) {
		respondWorkbenchError(c, http.StatusForbidden, "a same-origin request is required")
		return
	}
	user, err := bridge.lookupUser(userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondWorkbenchError(c, http.StatusUnauthorized, "user no longer exists")
		return
	}
	if err != nil {
		common.SysError("failed to validate workbench user: " + err.Error())
		respondWorkbenchError(c, http.StatusInternalServerError, "failed to validate user status")
		return
	}
	if user.Status != common.UserStatusEnabled {
		respondWorkbenchError(c, http.StatusForbidden, "user is disabled")
		return
	}
	if surface == workbenchbridge.SurfaceAdmin && user.Role != common.RoleRootUser {
		respondWorkbenchError(c, http.StatusForbidden, "a super administrator session is required")
		return
	}

	identityVersion := workbenchbridge.IdentityVersion(identityFromUser(user))
	issued, err := bridge.tickets.Issue(c.Request.Context(), workbenchbridge.TicketIssueRequest{
		UserID:          user.Id,
		IdentityVersion: identityVersion,
		Surface:         surface,
		IsSuperAdmin:    user.Role == common.RoleRootUser,
		AuthenticatedAt: authenticatedAt,
		AMR:             amr,
		ReauthNonce:     reauthNonce,
	})
	if err != nil {
		common.SysError("failed to issue workbench session ticket: " + err.Error())
		respondWorkbenchError(c, http.StatusServiceUnavailable, "failed to issue workbench session ticket")
		return
	}
	now := bridge.now().Unix()
	expiresIn := issued.ExpiresAt.Unix() - now
	if expiresIn < 0 {
		expiresIn = 0
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, workbenchAPIResponse{
		Success: true,
		Data: workbenchSessionTicketData{
			Ticket:    issued.Value,
			ExpiresAt: issued.ExpiresAt.Unix(),
			ExpiresIn: expiresIn,
		},
	})
}

func sameOriginWorkbenchRequest(request *http.Request) bool {
	originValue := strings.TrimSpace(request.Header.Get("Origin"))
	if originValue == "" {
		referer, err := url.Parse(strings.TrimSpace(request.Header.Get("Referer")))
		if err != nil || referer.Scheme == "" || referer.Host == "" {
			return false
		}
		originValue = referer.Scheme + "://" + referer.Host
	}
	origin, err := url.Parse(originValue)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Path != "" {
		return false
	}

	scheme := "http"
	if request.TLS != nil || strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	return strings.EqualFold(origin.Scheme, scheme) && strings.EqualFold(origin.Host, request.Host)
}

func (bridge *WorkbenchIdentityBridge) IdentityStatus(c *gin.Context) {
	bridge.identityStatus(c, false)
}

func (bridge *WorkbenchIdentityBridge) AdminIdentityStatus(c *gin.Context) {
	bridge.identityStatus(c, true)
}

func (bridge *WorkbenchIdentityBridge) identityStatus(c *gin.Context, requireSuperAdmin bool) {
	if !bridge.config.Enabled {
		respondWorkbenchError(c, http.StatusNotFound, "workbench is disabled")
		return
	}
	if err := bridge.config.ValidateInternalAuth(); err != nil {
		respondWorkbenchError(c, http.StatusServiceUnavailable, "workbench identity service is unavailable")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maximumIdentityStatusBodyBytes)
	body, err := c.GetRawData()
	if err != nil {
		respondWorkbenchError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if err = workbenchbridge.VerifyInternalRequest(
		bridge.config.ServiceHMACSecret,
		c.Request.Method,
		c.Request.URL.EscapedPath(),
		c.GetHeader(workbenchbridge.ContractVersionHeader),
		c.GetHeader(workbenchbridge.InternalTimestampHeader),
		c.GetHeader(workbenchbridge.InternalNonceHeader),
		c.GetHeader(workbenchbridge.InternalSignatureHeader),
		body,
		bridge.now(),
		bridge.config.InternalRequestSkew,
	); err != nil {
		respondWorkbenchError(c, http.StatusUnauthorized, "invalid internal request signature")
		return
	}

	var request workbenchIdentityStatusRequest
	if err = common.Unmarshal(body, &request); err != nil || request.UserID <= 0 {
		bridge.respondSignedIdentityStatus(c, http.StatusBadRequest, workbenchAPIResponse{Success: false, Message: "invalid user_id"})
		return
	}
	user, err := bridge.lookupUser(request.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		bridge.respondSignedIdentityStatus(c, http.StatusOK, workbenchAPIResponse{
			Success: true,
			Data: workbenchIdentityStatusData{
				UserID:  request.UserID,
				Exists:  false,
				Enabled: false,
			},
		})
		return
	}
	if err != nil {
		common.SysError("failed to query workbench identity status: " + err.Error())
		bridge.respondSignedIdentityStatus(c, http.StatusInternalServerError, workbenchAPIResponse{Success: false, Message: "failed to query user status"})
		return
	}

	identity := identityFromUser(user)
	identityVersion := workbenchbridge.IdentityVersion(identity)
	bridge.respondSignedIdentityStatus(c, http.StatusOK, workbenchAPIResponse{
		Success: true,
		Data: workbenchIdentityStatusData{
			UserID:          user.Id,
			Exists:          true,
			Enabled:         user.Status == common.UserStatusEnabled,
			IdentityVersion: identityVersion,
			IsSuperAdmin:    user.Role == common.RoleRootUser && (!requireSuperAdmin || request.IdentityVersion == identityVersion),
		},
	})
}

func (bridge *WorkbenchIdentityBridge) respondSignedIdentityStatus(c *gin.Context, status int, payload workbenchAPIResponse) {
	body, err := common.Marshal(payload)
	if err != nil {
		respondWorkbenchError(c, http.StatusInternalServerError, "failed to encode identity status")
		return
	}
	timestamp := bridge.now().Unix()
	requestNonce := c.GetHeader(workbenchbridge.InternalNonceHeader)
	c.Header("Cache-Control", "no-store")
	c.Header(workbenchbridge.ContractVersionHeader, workbenchbridge.ContractVersion)
	c.Header(workbenchbridge.ResponseTimestampHeader, strconv.FormatInt(timestamp, 10))
	c.Header(workbenchbridge.ResponseNonceHeader, requestNonce)
	c.Header(
		workbenchbridge.ResponseSignatureHeader,
		workbenchbridge.SignInternalResponse(
			bridge.config.ServiceHMACSecret,
			status,
			c.Request.URL.EscapedPath(),
			timestamp,
			requestNonce,
			body,
		),
	)
	c.Data(status, "application/json; charset=utf-8", body)
	c.Abort()
}

func identityFromUser(user *model.User) workbenchbridge.Identity {
	return workbenchbridge.Identity{
		UserID:      user.Id,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		Status:      user.Status,
		CreatedAt:   user.CreatedAt,
	}
}

func respondWorkbenchError(c *gin.Context, status int, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, workbenchAPIResponse{Success: false, Message: message})
	c.Abort()
}
