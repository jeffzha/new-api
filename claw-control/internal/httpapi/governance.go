package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/adminquery"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	"github.com/QuantumNous/new-api/claw-control/internal/approval"
	"github.com/QuantumNous/new-api/claw-control/internal/auditexport"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/notification"
	"github.com/QuantumNous/new-api/claw-control/internal/retention"
)

func (s *Server) archiveCustomer(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
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
	result, err := s.services.Customers.Archive(customer.ArchiveCommand{
		CustomerID: customerID, ExpectedVersion: body.ExpectedVersion,
		Actor: actor(r), Reason: body.Reason, RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) prepareAppMigration(w http.ResponseWriter, r *http.Request) {
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
		CredentialProfileID *uint64    `json:"credential_profile_id"`
		AppKeySecretRef     string     `json:"app_key_secret_ref"`
		AppKeyFingerprint   string     `json:"app_key_fingerprint"`
		DisplayName         string     `json:"display_name"`
		Limits              app.Limits `json:"limits"`
		Capabilities        []string   `json:"capabilities"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Apps.PrepareMigration(app.SaveConfigCommand{
		CustomerID: customerID, ExpectedVersion: body.ExpectedVersion,
		ProviderEnvironment: body.ProviderEnvironment, Region: body.Region,
		SpaceID: body.SpaceID, AppID: body.AppID, TemplateAgentID: body.TemplateAgentID,
		CredentialProfileID: body.CredentialProfileID, AppKeySecretRef: body.AppKeySecretRef,
		AppKeyFingerprint: body.AppKeyFingerprint, DisplayName: body.DisplayName,
		Limits: body.Limits, Capabilities: body.Capabilities,
		Actor: actor(r), RequestID: requestID(r),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	projected, err := adminquery.ProjectAppDraft(result.App, result.Version)
	writeResult(w, r, http.StatusCreated, projected, err)
}

func (s *Server) verifyAppMigration(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	appID, ok := pathUint64(w, r, "app_id")
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
		CustomerID: customerID, CustomerAppID: appID,
		ExpectedVersion: body.ExpectedVersion, ConfigVersion: body.ConfigVersion,
		Actor: actor(r), RequestID: requestID(r),
	}, s.services.SecretResolver, s.services.ProviderVerifier)
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) replanAppMigration(w http.ResponseWriter, r *http.Request) {
	if s.services.AppMigrations == nil {
		writeError(w, r, domain.Unavailable("App migration service is unavailable"))
		return
	}
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	appID, ok := pathUint64(w, r, "app_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedTargetVersion int64  `json:"expected_target_version"`
		ExpectedJobVersion    int64  `json:"expected_job_version"`
		Reason                string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AppMigrations.Replan(r.Context(), appmigration.ReplanCommand{
		CustomerID: customerID, TargetCustomerAppID: appID,
		ExpectedTargetRowVersion: body.ExpectedTargetVersion, ExpectedJobRowVersion: body.ExpectedJobVersion,
		Actor: actor(r), Reason: body.Reason, RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) retryAppMigrationMember(w http.ResponseWriter, r *http.Request) {
	if s.services.AppMigrations == nil {
		writeError(w, r, domain.Unavailable("App migration service is unavailable"))
		return
	}
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	appID, ok := pathUint64(w, r, "app_id")
	if !ok {
		return
	}
	memberID := strings.TrimSpace(r.PathValue("member_id"))
	if memberID == "" || len(memberID) > 64 {
		writeError(w, r, domain.Invalid("member_id is required"))
		return
	}
	var body struct {
		ExpectedJobVersion    int64  `json:"expected_job_version"`
		ExpectedMemberVersion int64  `json:"expected_member_version"`
		Reason                string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AppMigrations.RetryMember(r.Context(), appmigration.RetryMemberCommand{
		CustomerID: customerID, TargetCustomerAppID: appID, MigrationMemberID: memberID,
		ExpectedJobRowVersion: body.ExpectedJobVersion, ExpectedMemberVersion: body.ExpectedMemberVersion,
		Actor: actor(r), Reason: body.Reason, RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) stageCredentialRotation(w http.ResponseWriter, r *http.Request) {
	profileID, ok := pathUint64(w, r, "profile_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		SecretIDRef     string `json:"secret_id_ref"`
		SecretKeyRef    string `json:"secret_key_ref"`
		Fingerprint     string `json:"fingerprint"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Credentials.StageRotation(credential.StageRotationCommand{
		CurrentProfileID: profileID, ExpectedCurrentVersion: body.ExpectedVersion,
		SecretIDRef: body.SecretIDRef, SecretKeyRef: body.SecretKeyRef,
		Fingerprint: body.Fingerprint, Actor: actor(r), RequestID: requestID(r),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeResult(w, r, http.StatusCreated, adminquery.ProjectCredential(*result), nil)
}

func (s *Server) retireCredentialProfile(w http.ResponseWriter, r *http.Request) {
	profileID, ok := pathUint64(w, r, "profile_id")
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
	result, err := s.services.Credentials.Retire(credential.RetireCommand{
		ProfileID: profileID, ExpectedVersion: body.ExpectedVersion,
		Actor: actor(r), Reason: body.Reason, RequestID: requestID(r),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeResult(w, r, http.StatusOK, adminquery.ProjectCredential(*result), nil)
}

func (s *Server) exportAdminAudits(w http.ResponseWriter, r *http.Request) {
	if session, _ := r.Context().Value(adminSessionContextKey{}).(bool); !session {
		writeError(w, r, domain.Forbidden("audit export requires a current admin session"))
		return
	}
	startAt, err := time.Parse(time.RFC3339, strings.TrimSpace(r.URL.Query().Get("start")))
	if err != nil {
		writeError(w, r, domain.Invalid("start must be RFC3339"))
		return
	}
	endAt, err := time.Parse(time.RFC3339, strings.TrimSpace(r.URL.Query().Get("end")))
	if err != nil {
		writeError(w, r, domain.Invalid("end must be RFC3339"))
		return
	}
	customerID, ok := optionalQueryUint64(w, r, "customer_id")
	if !ok {
		return
	}
	afterID, ok := queryUint64(w, r, "after_id", false)
	if !ok {
		return
	}
	throughID, ok := queryUint64(w, r, "through_id", false)
	if !ok {
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 100
	}
	result, err := s.services.AuditExport.Export(auditexport.Query{
		CustomerID: customerID, StartAt: startAt, EndAt: endAt, AfterID: afterID, ThroughID: throughID, Limit: limit,
	}, r.URL.Query().Get("format"), actor(r), requestID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", result.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, result.Filename))
	w.Header().Set("X-Workbench-Export-ID", result.ExportID)
	w.Header().Set("X-Export-Through-ID", strconv.FormatUint(result.ThroughID, 10))
	if result.NextCursor > 0 {
		w.Header().Set("X-Next-After-ID", strconv.FormatUint(result.NextCursor, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Body)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	customerID, ok := optionalQueryUint64(w, r, "customer_id")
	if !ok {
		return
	}
	beforeID, ok := queryUint64(w, r, "before_id", false)
	if !ok {
		return
	}
	result, err := s.services.Notifications.List(notification.ListQuery{
		CustomerID: customerID, UnreadOnly: r.URL.Query().Get("unread") == "true",
		BeforeID: beforeID, Limit: queryLimit(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	result, err := s.services.Notifications.MarkRead(r.PathValue("notification_id"), actor(r), requestID(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) getRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	result, err := s.services.Retention.Policy(customerID)
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) setRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
		RetentionDays   int   `json:"retention_days"`
		LegalHold       bool  `json:"legal_hold"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Retention.SetPolicy(retention.SetPolicyCommand{
		CustomerID: customerID, ExpectedVersion: body.ExpectedVersion,
		RetentionDays: body.RetentionDays, LegalHold: body.LegalHold,
		Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) dryRunRetention(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathUint64(w, r, "customer_id")
	if !ok {
		return
	}
	result, err := s.services.Retention.DryRun(customerID, time.Now().UTC(), actor(r), requestID(r))
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) executeRetention(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Retention.Execute(retention.ExecuteCommand{
		RunID: r.PathValue("run_id"), ExpectedVersion: body.ExpectedVersion,
		Actor: actor(r), RequestID: requestID(r),
	}, time.Now().UTC())
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) requestApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActionType  string  `json:"action_type"`
		CustomerID  *uint64 `json:"customer_id,omitempty"`
		ResourceID  uint64  `json:"resource_id,omitempty"`
		EvidenceRef string  `json:"evidence_ref,omitempty"`
		RequestKey  string  `json:"request_key"`
		Reason      string  `json:"reason"`
		TTLSeconds  int64   `json:"ttl_seconds"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.Approvals.Request(approval.RequestCommand{
		ActionType: body.ActionType, CustomerID: body.CustomerID, ResourceID: body.ResourceID,
		EvidenceRef: body.EvidenceRef, RequestKey: body.RequestKey, Reason: body.Reason,
		Actor: actor(r), RequestID: requestID(r), TTL: time.Duration(body.TTLSeconds) * time.Second,
	}, time.Now().UTC())
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	customerID, ok := optionalQueryUint64(w, r, "customer_id")
	if !ok {
		return
	}
	beforeID, ok := queryUint64(w, r, "before_id", false)
	if !ok {
		return
	}
	result, err := s.services.Approvals.List(approval.ListQuery{
		CustomerID: customerID, Status: r.URL.Query().Get("status"), BeforeID: beforeID, Limit: queryLimit(r),
	})
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) approveGovernance(w http.ResponseWriter, r *http.Request) {
	s.governanceDecision(w, r, "approve")
}

func (s *Server) rejectGovernance(w http.ResponseWriter, r *http.Request) {
	s.governanceDecision(w, r, "reject")
}

func (s *Server) executeGovernance(w http.ResponseWriter, r *http.Request) {
	s.governanceDecision(w, r, "execute")
}

func (s *Server) governanceDecision(w http.ResponseWriter, r *http.Request, action string) {
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	command := approval.DecisionCommand{
		ApprovalID: r.PathValue("approval_id"), ExpectedVersion: body.ExpectedVersion,
		Actor: actor(r), Reason: body.Reason, RequestID: requestID(r),
	}
	var result any
	var err error
	switch action {
	case "approve":
		result, err = s.services.Approvals.Approve(command, time.Now().UTC())
	case "reject":
		result, err = s.services.Approvals.Reject(command, time.Now().UTC())
	case "execute":
		result, err = s.services.Approvals.Execute(command, time.Now().UTC())
	}
	writeResult(w, r, http.StatusOK, result, err)
}

func optionalQueryUint64(w http.ResponseWriter, r *http.Request, name string) (*uint64, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return nil, true
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		writeError(w, r, domain.Invalid("%s must be positive", name))
		return nil, false
	}
	return &parsed, true
}

func queryUint64(w http.ResponseWriter, r *http.Request, name string, required bool) (uint64, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" && !required {
		return 0, true
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || (required && parsed == 0) {
		writeError(w, r, domain.Invalid("%s must be a valid integer", name))
		return 0, false
	}
	return parsed, true
}
