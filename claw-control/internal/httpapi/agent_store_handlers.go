package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/agentstore"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
)

func (s *Server) agentStoreStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeResult(w, r, http.StatusOK, map[string]bool{"enabled": s.publicConfig.AgentStoreEnabled}, nil)
}

const recentAdminAuthAge = 15 * time.Minute

type agentStoreDeploymentInput struct {
	CustomerID       uint64 `json:"customer_id"`
	CustomerAppID    uint64 `json:"customer_app_id"`
	ExecutionEnabled bool   `json:"execution_enabled"`
	AudienceScope    string `json:"audience_scope"`
}

type createAgentStoreItemBody struct {
	Slug         string                        `json:"slug"`
	DisplayName  string                        `json:"display_name"`
	Summary      string                        `json:"summary"`
	Description  string                        `json:"description"`
	AvatarURL    string                        `json:"avatar_url"`
	Category     string                        `json:"category"`
	Tags         []string                      `json:"tags"`
	SortOrder    int                           `json:"sort_order"`
	Featured     bool                          `json:"featured"`
	Deployment   agentStoreDeploymentInput     `json:"deployment"`
	Entitlements []agentstore.EntitlementInput `json:"entitlements"`
}

func (s *Server) listAgentStoreItems(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	const scope = "agent-store-items"
	afterSortOrder, afterItemID, limit, ok := adminAgentStorePageRequest(w, r, scope)
	if !ok {
		return
	}
	page, err := s.services.AgentStore.AdminListPage(limit, afterSortOrder, afterItemID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "data": page.Items,
		"meta": map[string]any{"next_cursor": adminAgentStoreNextCursor(scope, page.NextSortOrder, page.NextItemID)},
	})
}

func (s *Server) createAgentStoreItem(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body createAgentStoreItemBody
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AgentStore.Create(agentstore.CreateCommand{
		Slug:      body.Slug,
		Metadata:  agentstore.Metadata{DisplayName: body.DisplayName, Summary: body.Summary, Description: body.Description, AvatarURL: body.AvatarURL, Category: body.Category, Tags: body.Tags},
		SortOrder: body.SortOrder, Featured: body.Featured,
		CustomerID: body.Deployment.CustomerID, CustomerAppID: body.Deployment.CustomerAppID,
		ExecutionEnabled: body.Deployment.ExecutionEnabled, AudienceScope: body.Deployment.AudienceScope, Entitlements: body.Entitlements,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

type unifiedAgentStoreListingBody struct {
	Slug                string               `json:"slug"`
	DisplayName         string               `json:"display_name"`
	Summary             string               `json:"summary"`
	Description         string               `json:"description"`
	AvatarURL           string               `json:"avatar_url"`
	Category            string               `json:"category"`
	Tags                []string             `json:"tags"`
	SortOrder           int                  `json:"sort_order"`
	Featured            bool                 `json:"featured"`
	AudienceScope       string               `json:"audience_scope"`
	SelectedCustomerIDs []uint64             `json:"selected_customer_ids"`
	ProviderEnvironment string               `json:"provider_environment"`
	Region              string               `json:"region"`
	SpaceID             string               `json:"space_id"`
	AppID               string               `json:"app_id"`
	AppKey              string               `json:"app_key"`
	TemplateAgentID     string               `json:"template_agent_id"`
	CredentialProfileID uint64               `json:"credential_profile_id"`
	Limits              productpolicy.Limits `json:"limits"`
	Capabilities        []string             `json:"capabilities"`
}

func (s *Server) verifyUnifiedAgentStoreListing(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body unifiedAgentStoreListingBody
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AgentStore.PrepareListing(r.Context(), agentstore.PrepareListingCommand{
		Slug:      body.Slug,
		Metadata:  agentstore.Metadata{DisplayName: body.DisplayName, Summary: body.Summary, Description: body.Description, AvatarURL: body.AvatarURL, Category: body.Category, Tags: body.Tags},
		SortOrder: body.SortOrder, Featured: body.Featured, AudienceScope: body.AudienceScope,
		SelectedCustomerIDs: body.SelectedCustomerIDs,
		Config: agentstore.ListingConfig{
			ProviderEnvironment: body.ProviderEnvironment, Region: body.Region, SpaceID: body.SpaceID,
			AppID: body.AppID, AppKey: body.AppKey, TemplateAgentID: body.TemplateAgentID,
			CredentialProfileID: body.CredentialProfileID, Limits: body.Limits, Capabilities: body.Capabilities,
		},
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) publishUnifiedAgentStoreListing(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body struct {
		ExpectedVersion           int64 `json:"expected_version"`
		ExpectedDeploymentVersion int64 `json:"expected_deployment_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AgentStore.PublishListing(r.PathValue("item_id"), body.ExpectedVersion, body.ExpectedDeploymentVersion, actor(r), requestID(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) getAgentStoreItem(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	result, err := s.services.AgentStore.AdminGet(r.PathValue("item_id"))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) updateAgentStoreItem(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	current, err := s.services.AgentStore.AdminGet(r.PathValue("item_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	base := current.DraftVersion
	if base == nil {
		base = current.CurrentVersion
	}
	if base == nil {
		writeError(w, r, domain.Conflict("Agent Store item has no editable metadata version"))
		return
	}
	var body struct {
		ExpectedVersion int64     `json:"expected_version"`
		DisplayName     *string   `json:"display_name"`
		Summary         *string   `json:"summary"`
		Description     *string   `json:"description"`
		AvatarURL       *string   `json:"avatar_url"`
		Category        *string   `json:"category"`
		Tags            *[]string `json:"tags"`
		SortOrder       *int      `json:"sort_order"`
		Featured        *bool     `json:"featured"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	metadata := agentstore.Metadata{DisplayName: base.DisplayName, Summary: base.Summary, Description: base.Description, AvatarURL: base.AvatarURL, Category: base.Category, Tags: base.Tags}
	if body.DisplayName != nil {
		metadata.DisplayName = *body.DisplayName
	}
	if body.Summary != nil {
		metadata.Summary = *body.Summary
	}
	if body.Description != nil {
		metadata.Description = *body.Description
	}
	if body.AvatarURL != nil {
		metadata.AvatarURL = *body.AvatarURL
	}
	if body.Category != nil {
		metadata.Category = *body.Category
	}
	if body.Tags != nil {
		metadata.Tags = *body.Tags
	}
	sortOrder, featured := current.SortOrder, current.Featured
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	if body.Featured != nil {
		featured = *body.Featured
	}
	result, err := s.services.AgentStore.Update(agentstore.UpdateCommand{
		ItemID: current.ItemID, ExpectedVersion: body.ExpectedVersion, Metadata: metadata,
		SortOrder: sortOrder, Featured: featured, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) verifyAgentStoreItem(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AgentStore.Verify(r.Context(), agentstore.TransitionCommand{
		ItemID: r.PathValue("item_id"), ExpectedVersion: body.ExpectedVersion,
		Action: "verify", Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) createAgentStoreDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body struct {
		ExpectedItemVersion int64                         `json:"expected_item_version"`
		CustomerID          uint64                        `json:"customer_id"`
		CustomerAppID       uint64                        `json:"customer_app_id"`
		Entitlements        []agentstore.EntitlementInput `json:"entitlements"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AgentStore.AddDeployment(agentstore.DeploymentCommand{
		ItemID: r.PathValue("item_id"), ExpectedItemVersion: body.ExpectedItemVersion,
		CustomerID: body.CustomerID, CustomerAppID: body.CustomerAppID, Entitlements: body.Entitlements,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) updateAgentStoreDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body struct {
		ExpectedDeploymentVersion int64                          `json:"expected_deployment_version"`
		ExecutionEnabled          bool                           `json:"execution_enabled"`
		Entitlements              *[]agentstore.EntitlementInput `json:"entitlements"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	var entitlements []agentstore.EntitlementInput
	if body.Entitlements != nil {
		entitlements = *body.Entitlements
	}
	result, err := s.services.AgentStore.UpdateDeployment(agentstore.DeploymentCommand{
		ItemID: r.PathValue("item_id"), DeploymentID: r.PathValue("deployment_id"),
		ExpectedDeploymentVersion: body.ExpectedDeploymentVersion, ExecutionEnabled: body.ExecutionEnabled,
		Entitlements: entitlements, ReplaceEntitlements: body.Entitlements != nil,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) verifyAgentStoreDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body struct {
		ExpectedVersion           int64 `json:"expected_version"`
		ExpectedDeploymentVersion int64 `json:"expected_deployment_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AgentStore.Verify(r.Context(), agentstore.TransitionCommand{
		ItemID: r.PathValue("item_id"), DeploymentID: r.PathValue("deployment_id"),
		ExpectedVersion: body.ExpectedVersion, ExpectedDeploymentVersion: body.ExpectedDeploymentVersion,
		Action: "verify", Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) disableAgentStoreDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body struct {
		ExpectedDeploymentVersion int64  `json:"expected_deployment_version"`
		Reason                    string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AgentStore.DisableDeployment(agentstore.DeploymentCommand{
		ItemID: r.PathValue("item_id"), DeploymentID: r.PathValue("deployment_id"),
		ExpectedDeploymentVersion: body.ExpectedDeploymentVersion, Reason: body.Reason,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) transitionAgentStoreItem(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/admin/workbench/agent-store/items/"+r.PathValue("item_id")+"/")
	result, err := s.services.AgentStore.Transition(agentstore.TransitionCommand{
		ItemID: r.PathValue("item_id"), ExpectedVersion: body.ExpectedVersion,
		Action: action, Reason: body.Reason, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) agentStoreAudits(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	itemID := r.PathValue("item_id")
	scope := "agent-store-audits:" + itemID
	position, limit, ok := adminAgentStoreAuditPageRequest(w, r, scope)
	if !ok {
		return
	}
	page, err := s.services.AgentStore.AuditsPage(itemID, limit, position)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"admin_audits": page.AdminAudits, "launch_audits": page.LaunchAudits},
		"meta":    map[string]any{"next_cursor": adminAgentStoreAuditNextCursor(scope, page.Next)},
	})
}

func (s *Server) agentStoreCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	principal, ok := s.agentStorePrincipal(w, r, false)
	if !ok {
		return
	}
	result, err := s.services.AgentStore.Catalog(*principal, r.URL.Query().Get("cursor"), r.URL.Query().Get("category"), r.URL.Query().Get("query"))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) agentStoreDetail(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	principal, ok := s.agentStorePrincipal(w, r, false)
	if !ok {
		return
	}
	result, err := s.services.AgentStore.Detail(*principal, r.PathValue("slug"))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) launchAgentStoreItem(w http.ResponseWriter, r *http.Request) {
	if !s.agentStoreAvailable(w, r) {
		return
	}
	principal, ok := s.agentStorePrincipal(w, r, true)
	if !ok {
		return
	}
	if r.Body != nil {
		var body map[string]any
		err := jsonx.Decode(r.Body, &body)
		if err != nil && err != io.EOF {
			writeError(w, r, domain.Invalid("launch body must be empty"))
			return
		}
		if len(body) != 0 {
			writeError(w, r, domain.Invalid("launch body must not override server-owned application context"))
			return
		}
	}
	sessionCookie, _ := r.Cookie("claw_control_session")
	grant, err := s.services.AgentStore.Launch(*principal, sessionCookie.Value, r.PathValue("slug"), requestID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	selection, err := s.services.Access.SelectContext(r.Context(), sessionCookie.Value, grant.SelectionToken)
	if err != nil {
		_ = s.services.AgentStore.RecordLaunchOutcome(grant.DeploymentID, *principal, "failed", "selection_failed", requestID(r))
		writeError(w, r, err)
		return
	}
	if err := s.services.AgentStore.RecordLaunchOutcome(grant.DeploymentID, *principal, "launched", "", requestID(r)); err != nil {
		writeError(w, r, err)
		return
	}
	maxAge := int(time.Until(selection.ADPSSOTicketExpiresAt).Seconds())
	if maxAge < 1 || selection.SSOBrowserBinding == "" {
		writeError(w, r, domain.Forbidden("SSO browser binding expired during Agent launch"))
		return
	}
	setSSOBrowserBindingCookie(w, selection.SSOBrowserBinding, selection.ADPSSOTicketExpiresAt, maxAge)
	target := s.publicConfig.ADPSSORedirectPath + "?ticket=" + url.QueryEscape(selection.ADPSSOTicket)
	writeResult(w, r, http.StatusOK, map[string]any{"redirect_url": target, "expires_at": selection.ADPSSOTicketExpiresAt}, nil)
}

func (s *Server) agentStorePrincipal(w http.ResponseWriter, r *http.Request, requireCSRF bool) (*access.SessionPrincipal, bool) {
	cookie, err := r.Cookie("claw_control_session")
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Success: false, Error: apiError{Code: "unauthorized", Message: "control session is required", RequestID: requestID(r)}})
		return nil, false
	}
	csrf := ""
	if requireCSRF {
		csrf = strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	}
	principal, err := s.services.Access.AuthorizeControlSession(r.Context(), cookie.Value, csrf, requireCSRF)
	if err != nil {
		writeError(w, r, err)
		return nil, false
	}
	return principal, true
}

func (s *Server) agentStoreAvailable(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if !s.publicConfig.AgentStoreEnabled || s.services.AgentStore == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Success: false, Error: apiError{Code: "not_found", Message: "resource not found", RequestID: requestID(r)}})
		return false
	}
	return true
}
