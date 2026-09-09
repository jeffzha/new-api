package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/claw-control/internal/adminquery"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
)

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	result, err := s.services.AdminQueries.Dashboard(queryLimit(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) customerDetail(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	result, err := s.services.AdminQueries.CustomerDetail(customerID)
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) updateCustomer(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		DisplayName     string `json:"display_name"`
		BillingUserID   *int64 `json:"billing_user_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Customers.Update(customer.UpdateCommand{
		CustomerID: customerID, ExpectedVersion: body.ExpectedVersion,
		DisplayName: body.DisplayName, BillingUserID: body.BillingUserID,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) updateMemberRole(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, r, domain.Invalid("user_id must be positive"))
		return
	}
	var body struct {
		Role              string `json:"role"`
		ExpectedAuthEpoch int64  `json:"expected_auth_epoch"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Customers.UpdateMemberRole(customer.UpdateMemberCommand{
		CustomerID: customerID, NewAPIUserID: userID, Role: body.Role,
		ExpectedAuthEpoch: body.ExpectedAuthEpoch, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) verifyApp(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
		ConfigVersion   int64 `json:"config_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Apps.VerifyPending(r.Context(), app.VerifyPendingCommand{
		CustomerID: customerID, ExpectedVersion: body.ExpectedVersion, ConfigVersion: body.ConfigVersion,
		Actor: actor(r), RequestID: requestID(r),
	}, s.services.SecretResolver, s.services.ProviderVerifier)
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) listCustomerApps(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	scope := "apps:" + strconv.FormatUint(customerID, 10)
	beforeID, limit, ok := adminPageRequest(w, r, scope)
	if !ok {
		return
	}
	page, err := s.services.AdminQueries.AppViewsPage(customerID, beforeID, limit)
	writePageResult(w, r, page.Items, scope, page.NextBeforeID, err)
}

func (s *Server) createCustomerApp(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		Alias               string     `json:"alias"`
		ProviderEnvironment string     `json:"provider_environment"`
		Region              string     `json:"region"`
		SpaceID             string     `json:"space_id"`
		AppID               string     `json:"app_id"`
		TemplateAgentID     string     `json:"template_agent_id"`
		CredentialProfileID *uint64    `json:"credential_profile_id"`
		AppKey              string     `json:"app_key,omitempty"`
		AppKeySecretRef     string     `json:"app_key_secret_ref"`
		AppKeyFingerprint   string     `json:"app_key_fingerprint"`
		DisplayName         string     `json:"display_name"`
		Limits              app.Limits `json:"limits"`
		Capabilities        []string   `json:"capabilities"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Apps.CreateAdditional(app.CreateAdditionalCommand{
		Alias: body.Alias,
		Config: app.SaveConfigCommand{
			CustomerID: customerID, ProviderEnvironment: body.ProviderEnvironment,
			Region: body.Region, SpaceID: body.SpaceID, AppID: body.AppID,
			TemplateAgentID: body.TemplateAgentID, CredentialProfileID: body.CredentialProfileID,
			AppKey: body.AppKey, AppKeySecretRef: body.AppKeySecretRef, AppKeyFingerprint: body.AppKeyFingerprint,
			DisplayName: body.DisplayName, Limits: body.Limits, Capabilities: body.Capabilities,
			Actor: actor(r), RequestID: requestID(r),
		},
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	projected, err := adminquery.ProjectAppDraft(result.App, result.Version)
	writeResult(w, r, http.StatusCreated, projected, err)
}

func (s *Server) verifyCustomerApp(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	application, err := s.services.Apps.BySelector(customerID, r.PathValue("selector"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
		ConfigVersion   int64 `json:"config_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Apps.VerifyPending(r.Context(), app.VerifyPendingCommand{
		CustomerID: customerID, CustomerAppID: application.ID,
		ExpectedVersion: body.ExpectedVersion, ConfigVersion: body.ConfigVersion,
		Actor: actor(r), RequestID: requestID(r),
	}, s.services.SecretResolver, s.services.ProviderVerifier)
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) setDefaultCustomerApp(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedTargetVersion         int64 `json:"expected_target_version"`
		ExpectedCurrentDefaultVersion int64 `json:"expected_current_default_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Apps.SetDefault(app.SetDefaultCommand{
		CustomerID: customerID, Selector: r.PathValue("selector"),
		ExpectedTargetVersion:         body.ExpectedTargetVersion,
		ExpectedCurrentDefaultVersion: body.ExpectedCurrentDefaultVersion,
		Actor:                         actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) transitionCustomerApp(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	application, err := s.services.Apps.BySelector(customerID, r.PathValue("selector"))
	if err != nil {
		writeError(w, r, err)
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
		CustomerID: customerID, CustomerAppID: application.ID,
		ExpectedVersion: body.ExpectedVersion, Action: r.PathValue("action"), Reason: body.Reason,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) listPlanCatalog(w http.ResponseWriter, r *http.Request) {
	const scope = "plan-catalog"
	beforeID, limit, ok := adminPageRequest(w, r, scope)
	if !ok {
		return
	}
	page, err := s.services.AdminQueries.PlanCatalogPage(beforeID, limit)
	writePageResult(w, r, page.Items, scope, page.NextBeforeID, err)
}

func (s *Server) listCredentialProfiles(w http.ResponseWriter, r *http.Request) {
	customerID, ok := optionalQueryUint64(w, r, "customer_id")
	if !ok {
		return
	}
	scope := "credential-profiles:all"
	if customerID != nil {
		scope = "credential-profiles:" + strconv.FormatUint(*customerID, 10)
	}
	beforeID, limit, ok := adminPageRequest(w, r, scope)
	if !ok {
		return
	}
	page, err := s.services.AdminQueries.CredentialProfilesPage(beforeID, limit, customerID)
	writePageResult(w, r, page.Items, scope, page.NextBeforeID, err)
}

func (s *Server) listAdminAudits(w http.ResponseWriter, r *http.Request) {
	var customerID *uint64
	if value := strings.TrimSpace(r.URL.Query().Get("customer_id")); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, r, domain.Invalid("customer_id must be positive"))
			return
		}
		customerID = &parsed
	}
	scope := "admin-audits:all"
	if customerID != nil {
		scope = "admin-audits:" + strconv.FormatUint(*customerID, 10)
	}
	beforeID, limit, ok := adminPageRequest(w, r, scope)
	if !ok {
		return
	}
	page, err := s.services.AdminQueries.AuditsPage(beforeID, limit, customerID)
	writePageResult(w, r, page.Items, scope, page.NextBeforeID, err)
}
