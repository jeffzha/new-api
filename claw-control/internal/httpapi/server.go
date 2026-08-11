package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/adminquery"
	"github.com/QuantumNous/new-api/claw-control/internal/agentstore"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	"github.com/QuantumNous/new-api/claw-control/internal/approval"
	"github.com/QuantumNous/new-api/claw-control/internal/auditexport"
	"github.com/QuantumNous/new-api/claw-control/internal/billingimport"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/identity"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/marginreport"
	"github.com/QuantumNous/new-api/claw-control/internal/metrics"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/notification"
	"github.com/QuantumNous/new-api/claw-control/internal/plan"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/providerverify"
	"github.com/QuantumNous/new-api/claw-control/internal/resourcebinding"
	"github.com/QuantumNous/new-api/claw-control/internal/retention"
	"github.com/QuantumNous/new-api/claw-control/internal/secretintegrity"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/usageaudit"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxRequestBytes = 1 << 20

const (
	workbenchContractVersionHeader = "X-Workbench-Contract-Version"
	workbenchContractVersion       = "1"
)

const (
	bootstrapActor           = "emergency-bootstrap"
	maintenanceReenrollActor = "emergency-bootstrap:provider-fingerprint-reenroll"
)

type internalServiceContextKey struct{}
type adminSessionContextKey struct{}

type internalResponseBuffer struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newInternalResponseBuffer(initial http.Header) *internalResponseBuffer {
	header := make(http.Header, len(initial))
	for key, values := range initial {
		header[key] = append([]string(nil), values...)
	}
	return &internalResponseBuffer{header: header, status: http.StatusOK}
}

func (w *internalResponseBuffer) Header() http.Header { return w.header }

func (w *internalResponseBuffer) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *internalResponseBuffer) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(data)
}

type Services struct {
	DB               *gorm.DB
	Customers        *customer.Service
	Credentials      *credential.Service
	Identities       *identity.Service
	Apps             *app.Service
	AppMigrations    *appmigration.Service
	Plans            *plan.Service
	Usage            *usageaudit.Service
	Evidence         *evidence.Service
	Margins          *marginreport.Service
	Access           *access.Service
	Resources        *resourcebinding.Service
	AdminQueries     *adminquery.Service
	SecretResolver   secrets.Resolver
	ProviderVerifier providerverify.Verifier
	Notifications    *notification.Service
	AuditExport      *auditexport.Service
	Retention        *retention.Service
	Approvals        *approval.Service
	Metrics          *metrics.Registry
	BillingImports   *billingimport.Service
	SecretIntegrity  *secretintegrity.Service
	AgentStore       *agentstore.Service
}

type InternalAuth struct {
	ServiceKeys       map[string]string
	TimeSkew          time.Duration
	NewAPIServiceName string
	ADPServiceName    string
}

type PublicConfig struct {
	ADPSSORedirectPath string
	AdminRedirectPath  string
	AdminAssetDir      string
	AgentStoreEnabled  bool
}

type Server struct {
	services     Services
	adminToken   string
	internalAuth InternalAuth
	publicConfig PublicConfig
	handler      http.Handler
}

type errorResponse struct {
	Success bool     `json:"success"`
	Error   apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func New(services Services, adminToken string, internalAuth InternalAuth, publicConfig PublicConfig) *Server {
	server := &Server{services: services, adminToken: adminToken, internalAuth: internalAuth, publicConfig: publicConfig}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /readyz", server.ready)
	mux.Handle("POST /api/admin/workbench/secret-fingerprints/re-enroll", server.requireLoopbackBootstrap(http.HandlerFunc(server.reenrollSecretFingerprints)))
	mux.HandleFunc("GET /api/workbench/entry", server.enterWorkbench)
	mux.HandleFunc("GET /api/workbench/sso-preflight", server.workbenchSSOPreflight)
	mux.HandleFunc("GET /api/workbench/config", server.workbenchConfig)
	mux.HandleFunc("GET /api/workbench/plan", server.workbenchPlan)
	mux.HandleFunc("GET /api/workbench/selections", server.listWorkbenchSelections)
	mux.HandleFunc("POST /api/workbench/selections/choose", server.chooseWorkbenchSelection)
	mux.HandleFunc("GET /api/workbench/agent-store/status", server.agentStoreStatus)
	mux.HandleFunc("GET /api/workbench/agent-store", server.agentStoreCatalog)
	mux.HandleFunc("GET /api/workbench/agent-store/{slug}", server.agentStoreDetail)
	mux.HandleFunc("POST /api/workbench/agent-store/{slug}/launch", server.launchAgentStoreItem)
	if strings.TrimSpace(publicConfig.AdminAssetDir) != "" {
		adminAssets := adminSPAHandler(publicConfig.AdminAssetDir, publicConfig.AdminRedirectPath)
		mux.Handle(publicConfig.AdminRedirectPath, adminAssets)
		mux.Handle(publicConfig.AdminRedirectPath+"/", adminAssets)
	}

	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/workbench/dashboard", server.adminDashboard)
	admin.Handle("GET /api/admin/workbench/agent-store/items", server.requireAdminSession(http.HandlerFunc(server.listAgentStoreItems)))
	admin.Handle("POST /api/admin/workbench/agent-store/items", server.requireAdminSession(server.requireRecentAgentStoreAdmin(http.HandlerFunc(server.createAgentStoreItem))))
	admin.Handle("GET /api/admin/workbench/agent-store/items/{item_id}", server.requireAdminSession(http.HandlerFunc(server.getAgentStoreItem)))
	admin.Handle("PATCH /api/admin/workbench/agent-store/items/{item_id}", server.requireAdminSession(server.requireRecentAgentStoreAdmin(http.HandlerFunc(server.updateAgentStoreItem))))
	admin.Handle("POST /api/admin/workbench/agent-store/items/{item_id}/verify", server.requireAdminSession(server.requireRecentAgentStoreAdmin(http.HandlerFunc(server.verifyAgentStoreItem))))
	for _, action := range []string{"publish", "unpublish", "disable", "archive"} {
		admin.Handle("POST /api/admin/workbench/agent-store/items/{item_id}/"+action, server.requireAdminSession(server.requireRecentAgentStoreAdmin(http.HandlerFunc(server.transitionAgentStoreItem))))
	}
	admin.Handle("GET /api/admin/workbench/agent-store/items/{item_id}/audits", server.requireAdminSession(http.HandlerFunc(server.agentStoreAudits)))
	admin.HandleFunc("POST /api/admin/workbench/customers", server.createCustomer)
	admin.HandleFunc("GET /api/admin/workbench/customers", server.listCustomers)
	admin.HandleFunc("GET /api/admin/workbench/customers/{customer_id}", server.customerDetail)
	admin.HandleFunc("PATCH /api/admin/workbench/customers/{customer_id}", server.updateCustomer)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/archive", server.archiveCustomer)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/members", server.addMember)
	admin.HandleFunc("PATCH /api/admin/workbench/customers/{customer_id}/members/{user_id}", server.updateMemberRole)
	admin.HandleFunc("DELETE /api/admin/workbench/customers/{customer_id}/members/{user_id}", server.disableMember)
	admin.HandleFunc("PUT /api/admin/workbench/customers/{customer_id}/app", server.saveAppConfig)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/app/verify", server.verifyApp)
	admin.HandleFunc("GET /api/admin/workbench/customers/{customer_id}/apps", server.listCustomerApps)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/apps", server.createCustomerApp)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/apps/{selector}/verify", server.verifyCustomerApp)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/apps/{selector}/default", server.setDefaultCustomerApp)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/app-migrations", server.prepareAppMigration)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/app-migrations/{app_id}/verify", server.verifyAppMigration)
	admin.Handle("POST /api/admin/workbench/customers/{customer_id}/app-migrations/{app_id}/replan", server.requireAdminSession(http.HandlerFunc(server.replanAppMigration)))
	admin.Handle("POST /api/admin/workbench/customers/{customer_id}/app-migrations/{app_id}/members/{member_id}/retry", server.requireAdminSession(http.HandlerFunc(server.retryAppMigrationMember)))
	for _, action := range []string{"enable", "suspend", "disable", "prepare"} {
		admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/app/"+action, server.transitionApp)
	}
	admin.HandleFunc("GET /api/admin/workbench/plan-catalog", server.listPlanCatalog)
	admin.HandleFunc("POST /api/admin/workbench/plan-catalog", server.publishPlan)
	admin.HandleFunc("GET /api/admin/workbench/credential-profiles", server.listCredentialProfiles)
	admin.HandleFunc("POST /api/admin/workbench/credential-profiles", server.createCredentialProfile)
	admin.HandleFunc("POST /api/admin/workbench/credential-profiles/{profile_id}/rotations", server.stageCredentialRotation)
	admin.HandleFunc("POST /api/admin/workbench/credential-profiles/{profile_id}/retire", server.retireCredentialProfile)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/plan-periods", server.createPlanPeriod)
	admin.HandleFunc("POST /api/admin/workbench/plan-periods/{period_id}/confirm-payment", server.confirmPayment)
	admin.HandleFunc("POST /api/admin/workbench/plan-periods/{period_id}/cancel", server.cancelPlanPeriod)
	admin.HandleFunc("GET /api/admin/workbench/customers/{customer_id}/invoices", server.listInvoices)
	admin.HandleFunc("POST /api/admin/workbench/invoices/{invoice_id}/void", server.voidInvoice)
	admin.HandleFunc("POST /api/admin/workbench/usage-audits", server.createUsageAudit)
	admin.HandleFunc("GET /api/admin/workbench/usage-audits", server.listUsageAudits)
	admin.HandleFunc("POST /api/admin/workbench/usage-audits/{audit_id}/review", server.reviewUsageAudit)
	admin.HandleFunc("POST /api/admin/workbench/usage-audits/{audit_id}/revisions", server.reviseUsageAudit)
	admin.HandleFunc("POST /api/admin/workbench/tencent-billing-imports", server.createTencentBillingImport)
	admin.HandleFunc("GET /api/admin/workbench/tencent-billing-imports", server.listTencentBillingImports)
	admin.HandleFunc("GET /api/admin/workbench/tencent-billing-imports/{import_id}", server.getTencentBillingImport)
	admin.HandleFunc("POST /api/admin/workbench/tencent-billing-imports/{import_id}/retry", server.retryTencentBillingImport)
	admin.HandleFunc("POST /api/admin/workbench/evidence", server.uploadEvidence)
	admin.HandleFunc("GET /api/admin/workbench/evidence", server.listEvidence)
	admin.HandleFunc("GET /api/admin/workbench/evidence/{evidence_ref}", server.downloadEvidence)
	admin.HandleFunc("GET /api/admin/workbench/margin-report", server.marginReport)
	admin.HandleFunc("GET /api/admin/workbench/audits", server.listAdminAudits)
	admin.HandleFunc("GET /api/admin/workbench/audits/export", server.exportAdminAudits)
	admin.HandleFunc("GET /api/admin/workbench/notifications", server.listNotifications)
	admin.HandleFunc("POST /api/admin/workbench/notifications/{notification_id}/read", server.readNotification)
	admin.HandleFunc("GET /api/admin/workbench/customers/{customer_id}/retention-policy", server.getRetentionPolicy)
	admin.HandleFunc("PUT /api/admin/workbench/customers/{customer_id}/retention-policy", server.setRetentionPolicy)
	admin.HandleFunc("POST /api/admin/workbench/customers/{customer_id}/retention-runs", server.dryRunRetention)
	admin.HandleFunc("POST /api/admin/workbench/retention-runs/{run_id}/execute", server.executeRetention)
	admin.HandleFunc("GET /api/admin/workbench/approvals", server.listApprovals)
	admin.Handle("POST /api/admin/workbench/approvals", server.requireAdminSession(http.HandlerFunc(server.requestApproval)))
	admin.Handle("POST /api/admin/workbench/approvals/{approval_id}/approve", server.requireAdminSession(http.HandlerFunc(server.approveGovernance)))
	admin.Handle("POST /api/admin/workbench/approvals/{approval_id}/reject", server.requireAdminSession(http.HandlerFunc(server.rejectGovernance)))
	admin.Handle("POST /api/admin/workbench/approvals/{approval_id}/execute", server.requireAdminSession(http.HandlerFunc(server.executeGovernance)))
	mux.Handle("/api/admin/", server.requireAdmin(admin))

	internal := http.NewServeMux()
	internal.Handle("POST /api/internal/workbench/entry-tickets/issue", server.requireInternalService(http.HandlerFunc(server.issueEntryTicket), internalAuth.NewAPIServiceName))
	internal.Handle("POST /api/internal/workbench/tickets/consume", server.requireInternalService(http.HandlerFunc(server.consumeTicket), internalAuth.ADPServiceName))
	internal.Handle("POST /api/internal/workbench/authz", server.requireInternalService(http.HandlerFunc(server.authorize), internalAuth.ADPServiceName))
	internal.Handle("POST /api/internal/workbench/app-context", server.requireInternalService(http.HandlerFunc(server.appContext), internalAuth.ADPServiceName))
	internal.Handle("POST /api/internal/workbench/identities/confirm", server.requireInternalService(http.HandlerFunc(server.confirmIdentity), internalAuth.ADPServiceName))
	internal.Handle("POST /api/internal/workbench/resources/bind", server.requireInternalService(http.HandlerFunc(server.bindResource), internalAuth.ADPServiceName))
	internal.Handle("POST /api/internal/workbench/app-migrations/tasks/claim", server.requireInternalService(http.HandlerFunc(server.claimAppMigrationTask), internalAuth.ADPServiceName))
	internal.Handle("POST /api/internal/workbench/app-migrations/tasks/report", server.requireInternalService(http.HandlerFunc(server.reportAppMigrationTask), internalAuth.ADPServiceName))
	mux.Handle("/api/internal/", server.requireHMAC(internal))
	server.handler = requestIDMiddleware(mux)
	if services.Metrics != nil {
		server.handler = services.Metrics.Wrap(server.handler)
	}
	return server
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	sqlDB, err := s.services.DB.DB()
	if err == nil {
		err = sqlDB.PingContext(ctx)
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("database is not ready: %w", err))
		return
	}
	if s.services.SecretIntegrity == nil || s.services.SecretIntegrity.CheckContext(ctx) != nil {
		writeError(w, r, domain.Unavailable("provider secret integrity is not ready"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) reenrollSecretFingerprints(w http.ResponseWriter, r *http.Request) {
	if s.services.SecretIntegrity == nil {
		writeError(w, r, domain.Unavailable("provider secret integrity service is unavailable"))
		return
	}
	var body struct {
		Confirmation string `json:"confirmation"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.SecretIntegrity.ReenrollLegacy(secretintegrity.ReenrollCommand{
		Confirmation: body.Confirmation, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) enterWorkbench(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	result, err := s.services.Access.Enter(r.Context(), access.EnterCommand{
		Ticket: r.URL.Query().Get("ticket"), DeferSSO: s.publicConfig.AgentStoreEnabled,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if result.Surface == "admin" {
		maxAge := int(time.Until(result.AdminSessionExpiresAt).Seconds())
		if maxAge < 1 {
			writeError(w, r, domain.Forbidden("admin session expired during issuance"))
			return
		}
		cookiePath := "/api/admin/workbench"
		http.SetCookie(w, &http.Cookie{
			Name: "claw_admin_session", Value: result.AdminSessionToken,
			Path: cookiePath, Expires: result.AdminSessionExpiresAt, MaxAge: maxAge,
			HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
		})
		http.SetCookie(w, &http.Cookie{
			Name: "claw_admin_csrf", Value: result.AdminCSRFToken,
			Path: s.publicConfig.AdminRedirectPath, Expires: result.AdminSessionExpiresAt, MaxAge: maxAge,
			HttpOnly: false, Secure: true, SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, s.publicConfig.AdminRedirectPath, http.StatusFound)
		return
	}
	maxAge := int(time.Until(result.ControlSessionExpiresAt).Seconds())
	if maxAge < 1 {
		writeError(w, r, domain.Forbidden("control session expired during issuance"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "claw_control_session", Value: result.ControlSessionToken,
		Path: "/", Expires: result.ControlSessionExpiresAt, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	if s.publicConfig.AgentStoreEnabled {
		http.SetCookie(w, &http.Cookie{
			Name: "claw_control_csrf", Value: result.ControlCSRFToken,
			Path: "/agent-store", Expires: result.ControlSessionExpiresAt, MaxAge: maxAge,
			HttpOnly: false, Secure: true, SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/agent-store", http.StatusSeeOther)
		return
	}
	if result.SelectionRequired {
		http.Redirect(w, r, "/playground/select", http.StatusSeeOther)
		return
	}
	ssoMaxAge := int(time.Until(result.ADPSSOTicketExpiresAt).Seconds())
	if ssoMaxAge < 1 || result.SSOBrowserBinding == "" {
		writeError(w, r, domain.Forbidden("SSO browser binding expired during issuance"))
		return
	}
	setSSOBrowserBindingCookie(w, result.SSOBrowserBinding, result.ADPSSOTicketExpiresAt, ssoMaxAge)
	target := s.publicConfig.ADPSSORedirectPath + "?ticket=" + url.QueryEscape(result.ADPSSOTicket)
	http.Redirect(w, r, target, http.StatusFound)
}

func setSSOBrowserBindingCookie(w http.ResponseWriter, value string, expiresAt time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: "claw_sso_binding", Value: value,
		Path: "/workbench/auth/sso", Expires: expiresAt, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) workbenchConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie("claw_control_session")
	if err != nil {
		writeError(w, r, domain.Forbidden("control session is required"))
		return
	}
	result, err := s.services.Access.ConfigForSession(r.Context(), cookie.Value)
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) workbenchSSOPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie("claw_control_session")
	if err != nil {
		writeError(w, r, domain.Forbidden("control session is required"))
		return
	}
	if err = s.services.Access.AuthorizeSSOPreflight(r.Context(), cookie.Value); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workbenchPlan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie("claw_control_session")
	if err != nil {
		writeError(w, r, domain.Forbidden("control session is required"))
		return
	}
	result, err := s.services.Access.PlanForSession(r.Context(), cookie.Value)
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) listWorkbenchSelections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie("claw_control_session")
	if err != nil {
		writeError(w, r, domain.Forbidden("control session is required"))
		return
	}
	result, err := s.services.Access.ListSelectionOptions(r.Context(), cookie.Value)
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) chooseWorkbenchSelection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie("claw_control_session")
	if err != nil {
		writeError(w, r, domain.Forbidden("control session is required"))
		return
	}
	var body struct {
		SelectionToken string `json:"selection_token"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Access.SelectContext(r.Context(), cookie.Value, body.SelectionToken)
	if err != nil {
		writeError(w, r, err)
		return
	}
	maxAge := int(time.Until(result.ADPSSOTicketExpiresAt).Seconds())
	if maxAge < 1 || result.SSOBrowserBinding == "" {
		writeError(w, r, domain.Forbidden("SSO browser binding expired during selection"))
		return
	}
	setSSOBrowserBindingCookie(w, result.SSOBrowserBinding, result.ADPSSOTicketExpiresAt, maxAge)
	target := s.publicConfig.ADPSSORedirectPath + "?ticket=" + url.QueryEscape(result.ADPSSOTicket)
	writeResult(w, r, http.StatusOK, map[string]any{
		"redirect_url": target, "expires_at": result.ADPSSOTicketExpiresAt,
	}, nil)
}

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CustomerCode  string `json:"customer_code"`
		DisplayName   string `json:"display_name"`
		BillingUserID *int64 `json:"billing_user_id,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Customers.Create(customer.CreateCommand{
		CustomerCode: body.CustomerCode, DisplayName: body.DisplayName, BillingUserID: body.BillingUserID,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) listCustomers(w http.ResponseWriter, r *http.Request) {
	result, err := s.services.Customers.List(queryLimit(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) addMember(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		NewAPIUserID int64  `json:"new_api_user_id"`
		Role         string `json:"role"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Customers.AddMember(r.Context(), customer.AddMemberCommand{
		CustomerID: customerID, NewAPIUserID: body.NewAPIUserID, Role: body.Role,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) disableMember(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, r, domain.Invalid("user_id must be positive"))
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	err = s.services.Customers.DisableMember(customerID, userID, actor(r), reason, requestID(r))
	writeResult(w, r, http.StatusOK, map[string]any{"disabled": err == nil}, err)
}

func (s *Server) saveAppConfig(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion     int64      `json:"expected_version"`
		ProviderEnvironment string     `json:"provider_environment"`
		Region              string     `json:"region"`
		SpaceID             string     `json:"space_id"`
		AppID               string     `json:"app_id"`
		TemplateAgentID     string     `json:"template_agent_id"`
		CredentialProfileID *uint64    `json:"credential_profile_id,omitempty"`
		AppKeySecretRef     string     `json:"app_key_secret_ref"`
		AppKeyFingerprint   string     `json:"app_key_fingerprint"`
		DisplayName         string     `json:"display_name"`
		Limits              app.Limits `json:"limits"`
		Capabilities        []string   `json:"capabilities"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Apps.SaveConfig(app.SaveConfigCommand{
		CustomerID: customerID, ExpectedVersion: body.ExpectedVersion,
		ProviderEnvironment: body.ProviderEnvironment, Region: body.Region, SpaceID: body.SpaceID,
		AppID: body.AppID, TemplateAgentID: body.TemplateAgentID, CredentialProfileID: body.CredentialProfileID,
		AppKeySecretRef: body.AppKeySecretRef, AppKeyFingerprint: body.AppKeyFingerprint,
		DisplayName: body.DisplayName, Limits: body.Limits, Capabilities: body.Capabilities,
		Actor: actor(r), RequestID: requestID(r),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	projected, err := adminquery.ProjectAppDraft(result.App, result.Version)
	writeResult(w, r, http.StatusOK, projected, err)
}

func (s *Server) transitionApp(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Apps.Transition(app.TransitionCommand{
		CustomerID: customerID, ExpectedVersion: body.ExpectedVersion,
		Action: path.Base(r.URL.Path), Reason: body.Reason, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) publishPlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanCode        string               `json:"plan_code"`
		DisplayName     string               `json:"display_name"`
		MonthlyPriceCNY string               `json:"monthly_price_cny"`
		Capabilities    []string             `json:"capabilities"`
		Limits          productpolicy.Limits `json:"limits"`
		ValidFrom       time.Time            `json:"valid_from"`
		ValidTo         *time.Time           `json:"valid_to,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Plans.Publish(plan.PublishCommand{
		PlanCode: body.PlanCode, DisplayName: body.DisplayName, MonthlyPriceCNY: body.MonthlyPriceCNY,
		Capabilities: body.Capabilities, Limits: body.Limits, ValidFrom: body.ValidFrom, ValidTo: body.ValidTo,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) createCredentialProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OwnerScope          string  `json:"owner_scope"`
		CustomerID          *uint64 `json:"customer_id"`
		ProviderEnvironment string  `json:"provider_environment"`
		Name                string  `json:"name"`
		SecretIDRef         string  `json:"secret_id_ref"`
		SecretKeyRef        string  `json:"secret_key_ref"`
		Fingerprint         string  `json:"fingerprint"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Credentials.Create(credential.CreateCommand{
		OwnerScope: body.OwnerScope, CustomerID: body.CustomerID,
		ProviderEnvironment: body.ProviderEnvironment, Name: body.Name,
		SecretIDRef: body.SecretIDRef, SecretKeyRef: body.SecretKeyRef, Fingerprint: body.Fingerprint,
		Actor: actor(r), RequestID: requestID(r),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeResult(w, r, http.StatusCreated, adminquery.ProjectCredential(*result), nil)
}

func (s *Server) createPlanPeriod(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		PlanVersionID uint64    `json:"plan_version_id"`
		StartAt       time.Time `json:"period_start"`
		EndAt         time.Time `json:"period_end"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Plans.CreatePeriod(plan.CreatePeriodCommand{
		CustomerID: customerID, PlanVersionID: body.PlanVersionID, StartAt: body.StartAt, EndAt: body.EndAt,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) confirmPayment(w http.ResponseWriter, r *http.Request) {
	periodID, ok := pathUint64(w, r, "period_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion    int64  `json:"expected_version"`
		PaymentEvidenceRef string `json:"payment_evidence_ref"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Plans.ConfirmPayment(plan.ConfirmPaymentCommand{
		PeriodID: periodID, ExpectedVersion: body.ExpectedVersion, PaymentEvidenceRef: body.PaymentEvidenceRef,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) cancelPlanPeriod(w http.ResponseWriter, r *http.Request) {
	periodID, ok := pathUint64(w, r, "period_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Plans.CancelPeriod(plan.CancelPeriodCommand{
		PeriodID: periodID, ExpectedVersion: body.ExpectedVersion, Reason: body.Reason,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) listInvoices(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	result, err := s.services.Plans.ListInvoices(customerID, queryLimit(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) voidInvoice(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := pathUint64(w, r, "invoice_id")
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Plans.VoidInvoice(plan.VoidInvoiceCommand{
		InvoiceID: invoiceID, Reason: body.Reason, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) createUsageAudit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CustomerID           *uint64           `json:"customer_id,omitempty"`
		CustomerAppID        *uint64           `json:"customer_app_id,omitempty"`
		PlanPeriodID         *uint64           `json:"plan_period_id,omitempty"`
		PeriodStart          time.Time         `json:"period_start"`
		PeriodEnd            time.Time         `json:"period_end"`
		Source               string            `json:"source"`
		AllocationConfidence string            `json:"allocation_confidence"`
		ResourceIdentifier   string            `json:"resource_identifier,omitempty"`
		AllocationMethod     string            `json:"allocation_method,omitempty"`
		UpstreamCostCNY      string            `json:"upstream_cost_cny"`
		Usage                map[string]string `json:"usage"`
		Note                 string            `json:"note,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Usage.Create(usageaudit.CreateCommand{
		CustomerID: body.CustomerID, CustomerAppID: body.CustomerAppID, PlanPeriodID: body.PlanPeriodID,
		PeriodStart: body.PeriodStart, PeriodEnd: body.PeriodEnd, Source: body.Source,
		AllocationConfidence: body.AllocationConfidence, ResourceIdentifier: body.ResourceIdentifier,
		AllocationMethod: body.AllocationMethod, UpstreamCostCNY: body.UpstreamCostCNY,
		Usage: body.Usage, Note: body.Note, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) listUsageAudits(w http.ResponseWriter, r *http.Request) {
	var customerID *uint64
	if value := strings.TrimSpace(r.URL.Query().Get("customer_id")); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, r, domain.Invalid("customer_id must be positive"))
			return
		}
		customerID = &parsed
	}
	result, err := s.services.Usage.List(customerID, queryLimit(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) reviewUsageAudit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion      int64   `json:"expected_version"`
		CustomerID           *uint64 `json:"customer_id,omitempty"`
		CustomerAppID        *uint64 `json:"customer_app_id,omitempty"`
		PlanPeriodID         *uint64 `json:"plan_period_id,omitempty"`
		AllocationConfidence string  `json:"allocation_confidence"`
		ResourceIdentifier   string  `json:"resource_identifier,omitempty"`
		AllocationMethod     string  `json:"allocation_method,omitempty"`
		EvidenceRef          string  `json:"evidence_ref"`
		EvidenceHash         string  `json:"evidence_hash"`
		Note                 string  `json:"note,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Usage.Lock(usageaudit.LockCommand{
		AuditID: r.PathValue("audit_id"), ExpectedVersion: body.ExpectedVersion,
		CustomerID: body.CustomerID, CustomerAppID: body.CustomerAppID, PlanPeriodID: body.PlanPeriodID,
		AllocationConfidence: body.AllocationConfidence, ResourceIdentifier: body.ResourceIdentifier,
		AllocationMethod: body.AllocationMethod, EvidenceRef: body.EvidenceRef, EvidenceHash: body.EvidenceHash,
		Note: body.Note, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) reviseUsageAudit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion      int64             `json:"expected_version"`
		CustomerID           *uint64           `json:"customer_id,omitempty"`
		CustomerAppID        *uint64           `json:"customer_app_id,omitempty"`
		PlanPeriodID         *uint64           `json:"plan_period_id,omitempty"`
		AllocationConfidence string            `json:"allocation_confidence"`
		ResourceIdentifier   string            `json:"resource_identifier,omitempty"`
		AllocationMethod     string            `json:"allocation_method,omitempty"`
		UpstreamCostCNY      string            `json:"upstream_cost_cny"`
		Usage                map[string]string `json:"usage"`
		EvidenceRef          string            `json:"evidence_ref"`
		EvidenceHash         string            `json:"evidence_hash"`
		Note                 string            `json:"note,omitempty"`
		Reason               string            `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Usage.Revise(usageaudit.RevisionCommand{
		OriginalAuditID: r.PathValue("audit_id"), ExpectedVersion: body.ExpectedVersion,
		CustomerID: body.CustomerID, CustomerAppID: body.CustomerAppID, PlanPeriodID: body.PlanPeriodID,
		AllocationConfidence: body.AllocationConfidence, ResourceIdentifier: body.ResourceIdentifier,
		AllocationMethod: body.AllocationMethod, UpstreamCostCNY: body.UpstreamCostCNY, Usage: body.Usage,
		EvidenceRef: body.EvidenceRef, EvidenceHash: body.EvidenceHash, Note: body.Note, Reason: body.Reason,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) confirmIdentity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BindingID         string `json:"binding_id"`
		CanonicalSubject  string `json:"canonical_subject"`
		ADPAccountID      string `json:"adp_account_id"`
		ADPAccountVersion int64  `json:"adp_account_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Identities.Confirm(identity.ConfirmCommand{
		BindingID: body.BindingID, CanonicalSubject: body.CanonicalSubject,
		ADPAccountID: body.ADPAccountID, ADPAccountVersion: body.ADPAccountVersion,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) bindResource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BindingID          string `json:"binding_id"`
		CanonicalSubject   string `json:"canonical_subject"`
		CustomerID         uint64 `json:"customer_id"`
		ApplicationID      string `json:"application_id"`
		AppProfileID       uint64 `json:"app_profile_id"`
		ConfigVersion      int64  `json:"config_version"`
		ResourceType       string `json:"resource_type"`
		ResourceID         string `json:"resource_id"`
		ParentResourceType string `json:"parent_resource_type,omitempty"`
		ParentResourceID   string `json:"parent_resource_id,omitempty"`
		SourceEventID      string `json:"source_event_id"`
		SourceVersion      int64  `json:"source_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Resources.Bind(r.Context(), resourcebinding.BindCommand{
		BindingID: body.BindingID, CanonicalSubject: body.CanonicalSubject,
		CustomerID: body.CustomerID, ApplicationID: body.ApplicationID,
		AppProfileID: body.AppProfileID, ConfigVersion: body.ConfigVersion,
		ResourceType: body.ResourceType, ResourceID: body.ResourceID,
		ParentResourceType: body.ParentResourceType, ParentResourceID: body.ParentResourceID,
		SourceService: internalService(r), SourceEventID: body.SourceEventID,
		SourceVersion: body.SourceVersion,
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) issueEntryTicket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewAPIUserID    int64  `json:"new_api_user_id"`
		IdentityVersion string `json:"identity_version"`
		Surface         string `json:"surface"`
		IsSuperAdmin    bool   `json:"is_super_admin"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Access.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: body.NewAPIUserID, IdentityVersion: body.IdentityVersion,
		Surface: body.Surface, IsSuperAdmin: body.IsSuperAdmin,
	})
	w.Header().Set("Cache-Control", "no-store")
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) consumeTicket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ticket         string `json:"ticket"`
		BrowserBinding string `json:"browser_binding"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Access.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: body.Ticket, BrowserBinding: body.BrowserBinding,
		ConsumerService: internalService(r),
	})
	w.Header().Set("Cache-Control", "no-store")
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BindingID        string `json:"binding_id"`
		CanonicalSubject string `json:"canonical_subject"`
		AuthEpoch        int64  `json:"auth_epoch"`
		CustomerID       uint64 `json:"customer_id,omitempty"`
		AppProfileID     uint64 `json:"app_profile_id,omitempty"`
		ConfigVersion    int64  `json:"config_version,omitempty"`
		Method           string `json:"method"`
		ResourcePath     string `json:"resource_path"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Access.Authorize(r.Context(), access.AuthzCommand{
		BindingID: body.BindingID, CanonicalSubject: body.CanonicalSubject,
		AuthEpoch: body.AuthEpoch, CustomerID: body.CustomerID,
		AppProfileID: body.AppProfileID, ConfigVersion: body.ConfigVersion,
		Method: body.Method, ResourcePath: body.ResourcePath,
	})
	w.Header().Set("Cache-Control", "no-store")
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) appContext(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BindingID              string `json:"binding_id"`
		CanonicalSubject       string `json:"canonical_subject"`
		AuthEpoch              int64  `json:"auth_epoch"`
		RequestedAppProfileID  uint64 `json:"requested_app_profile_id"`
		RequestedConfigVersion int64  `json:"requested_config_version"`
		CurrentAppProfileID    uint64 `json:"current_app_profile_id,omitempty"`
		CurrentConfigVersion   int64  `json:"current_config_version,omitempty"`
		Purpose                string `json:"purpose"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Access.AppContext(r.Context(), access.AppContextCommand{
		BindingID: body.BindingID, CanonicalSubject: body.CanonicalSubject, AuthEpoch: body.AuthEpoch,
		RequestedAppProfileID: body.RequestedAppProfileID, RequestedConfigVersion: body.RequestedConfigVersion,
		CurrentAppProfileID: body.CurrentAppProfileID, CurrentConfigVersion: body.CurrentConfigVersion,
		Purpose: body.Purpose,
	})
	w.Header().Set("Cache-Control", "no-store")
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	expectedHash := sha256.Sum256([]byte(s.adminToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token, bearer := bearerToken(r.Header.Get("Authorization"))
		receivedHash := sha256.Sum256([]byte(token))
		if bearer && subtle.ConstantTimeCompare(receivedHash[:], expectedHash[:]) == 1 {
			r.Header.Set("X-Claw-Actor", bootstrapActor)
			next.ServeHTTP(w, r)
			return
		}
		sessionCookie, err := r.Cookie("claw_admin_session")
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Success: false, Error: apiError{
				Code: "unauthorized", Message: "admin session is required", RequestID: requestID(r),
			}})
			return
		}
		mutation := !readOnlyHTTPMethod(r.Method)
		csrfToken := ""
		if mutation {
			csrfHeader := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
			if csrfHeader == "" {
				writeJSON(w, http.StatusForbidden, errorResponse{Success: false, Error: apiError{
					Code: "forbidden", Message: "admin CSRF token is required", RequestID: requestID(r),
				}})
				return
			}
			csrfToken = csrfHeader
		}
		userID, err := s.services.Access.AuthorizeAdminSession(r.Context(), sessionCookie.Value, csrfToken, mutation)
		if err != nil {
			writeError(w, r, err)
			return
		}
		r.Header.Set("X-Claw-Actor", fmt.Sprintf("admin-session:%d", userID))
		ctx := context.WithValue(r.Context(), adminSessionContextKey{}, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireAdminSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if session, _ := r.Context().Value(adminSessionContextKey{}).(bool); !session {
			writeJSON(w, http.StatusForbidden, errorResponse{Success: false, Error: apiError{
				Code: "forbidden", Message: "a revocable administrator session is required", RequestID: requestID(r),
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireLoopbackBootstrap(next http.Handler) http.Handler {
	expectedHash := sha256.Sum256([]byte(s.adminToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		address := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
		if address == nil || !address.IsLoopback() {
			writeJSON(w, http.StatusForbidden, errorResponse{Success: false, Error: apiError{
				Code: "forbidden", Message: "provider-secret maintenance is available only over loopback", RequestID: requestID(r),
			}})
			return
		}
		token, bearer := bearerToken(r.Header.Get("Authorization"))
		receivedHash := sha256.Sum256([]byte(token))
		if !bearer || subtle.ConstantTimeCompare(receivedHash[:], expectedHash[:]) != 1 {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Success: false, Error: apiError{
				Code: "unauthorized", Message: "emergency bootstrap authorization is required", RequestID: requestID(r),
			}})
			return
		}
		r.Header.Set("X-Claw-Actor", maintenanceReenrollActor)
		next.ServeHTTP(w, r)
	})
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func (s *Server) requireHMAC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contractVersion := strings.TrimSpace(r.Header.Get(workbenchContractVersionHeader))
		service := strings.TrimSpace(r.Header.Get("X-Workbench-Service"))
		timestamp := strings.TrimSpace(r.Header.Get("X-Workbench-Timestamp"))
		nonce := strings.TrimSpace(r.Header.Get("X-Workbench-Nonce"))
		signature := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Workbench-Signature")))
		secret, trusted := s.internalAuth.ServiceKeys[service]
		unixTime, timeErr := strconv.ParseInt(timestamp, 10, 64)
		requestTime := time.Unix(unixTime, 0).UTC()
		now := time.Now().UTC()
		if contractVersion != workbenchContractVersion || !trusted || timeErr != nil || nonce == "" || len(nonce) > 96 || signature == "" ||
			now.Sub(requestTime) > s.internalAuth.TimeSkew || requestTime.Sub(now) > s.internalAuth.TimeSkew {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Success: false, Error: apiError{
				Code: "unauthorized", Message: "valid internal service signature is required", RequestID: requestID(r),
			}})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
		if err != nil || len(body) > maxRequestBytes {
			writeJSON(w, http.StatusBadRequest, errorResponse{Success: false, Error: apiError{
				Code: "invalid_request", Message: "request body is too large or unreadable", RequestID: requestID(r),
			}})
			return
		}
		bodyHash := sha256.Sum256(body)
		canonical := strings.Join([]string{workbenchContractVersion, r.Method, r.URL.Path, timestamp, nonce, hex.EncodeToString(bodyHash[:])}, "\n")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		expected := mac.Sum(nil)
		received, err := hex.DecodeString(signature)
		if err != nil || !hmac.Equal(received, expected) {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Success: false, Error: apiError{
				Code: "unauthorized", Message: "valid internal service signature is required", RequestID: requestID(r),
			}})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		response := newInternalResponseBuffer(w.Header())
		_ = s.services.DB.Where("expires_at < ?", now).Delete(&model.ServiceNonce{}).Error
		nonceRow := &model.ServiceNonce{ServiceName: service, Nonce: nonce, ExpiresAt: now.Add(2 * s.internalAuth.TimeSkew)}
		insert := s.services.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(nonceRow)
		if insert.Error != nil {
			writeError(response, r, insert.Error)
		} else if insert.RowsAffected != 1 {
			writeJSON(response, http.StatusUnauthorized, errorResponse{Success: false, Error: apiError{
				Code: "replayed_request", Message: "internal request nonce has already been used", RequestID: requestID(r),
			}})
		} else {
			ctx := context.WithValue(r.Context(), internalServiceContextKey{}, service)
			next.ServeHTTP(response, r.WithContext(ctx))
		}
		responseTimestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		responseBodyHash := sha256.Sum256(response.body.Bytes())
		responseCanonical := strings.Join([]string{
			workbenchContractVersion, strconv.Itoa(response.status), r.URL.Path, responseTimestamp, nonce, hex.EncodeToString(responseBodyHash[:]),
		}, "\n")
		responseMAC := hmac.New(sha256.New, []byte(secret))
		_, _ = responseMAC.Write([]byte(responseCanonical))
		for key, values := range response.header {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.Header().Set(workbenchContractVersionHeader, workbenchContractVersion)
		w.Header().Set("X-Workbench-Response-Timestamp", responseTimestamp)
		w.Header().Set("X-Workbench-Response-Nonce", nonce)
		w.Header().Set("X-Workbench-Response-Signature", hex.EncodeToString(responseMAC.Sum(nil)))
		w.WriteHeader(response.status)
		_, _ = w.Write(response.body.Bytes())
	})
}

func (s *Server) requireInternalService(next http.Handler, expected string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if internalService(r) != expected {
			writeJSON(w, http.StatusForbidden, errorResponse{Success: false, Error: apiError{
				Code: "forbidden", Message: "internal service is not authorized for this route", RequestID: requestID(r),
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = fmt.Sprintf("req_%d", time.Now().UTC().UnixNano())
			r.Header.Set("X-Request-ID", requestID)
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := jsonx.Decode(r.Body, destination); err != nil {
		writeError(w, r, domain.Invalid("invalid JSON request body: %v", err))
		return false
	}
	return true
}

func pathUint64(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	value, err := strconv.ParseUint(r.PathValue(name), 10, 64)
	if err != nil || value == 0 {
		writeError(w, r, domain.Invalid("%s must be positive", name))
		return 0, false
	}
	return value, true
}

func queryLimit(r *http.Request) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 100
	}
	return value
}

func actor(r *http.Request) string     { return strings.TrimSpace(r.Header.Get("X-Claw-Actor")) }
func requestID(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-Request-ID")) }

func internalService(r *http.Request) string {
	value, _ := r.Context().Value(internalServiceContextKey{}).(string)
	return value
}

func readOnlyHTTPMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func writeResult(w http.ResponseWriter, r *http.Request, status int, data any, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"success": true, "data": data})
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "internal server error"
	var domainError *domain.Error
	if errors.As(err, &domainError) {
		code = string(domainError.Kind)
		message = domainError.Message
		switch domainError.Kind {
		case domain.KindInvalid:
			status = http.StatusBadRequest
		case domain.KindNotFound:
			status = http.StatusNotFound
		case domain.KindConflict:
			status = http.StatusConflict
		case domain.KindForbidden:
			status = http.StatusForbidden
		case domain.KindUnavailable:
			status = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, status, errorResponse{Success: false, Error: apiError{Code: code, Message: message, RequestID: requestID(r)}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = jsonx.Encode(w, value)
}
