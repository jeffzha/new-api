package httpapi

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
)

func (s *Server) claimAppMigrationTask(w http.ResponseWriter, r *http.Request) {
	if s.services.AppMigrations == nil {
		writeError(w, r, domain.Unavailable("App migration worker service is unavailable"))
		return
	}
	var body struct {
		WorkerID     string `json:"worker_id"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AppMigrations.Claim(r.Context(), body.WorkerID, time.Duration(body.LeaseSeconds)*time.Second, time.Now().UTC())
	w.Header().Set("Cache-Control", "no-store")
	writeResult(w, r, http.StatusOK, map[string]any{"task": result}, err)
}

func (s *Server) reportAppMigrationTask(w http.ResponseWriter, r *http.Request) {
	if s.services.AppMigrations == nil {
		writeError(w, r, domain.Unavailable("App migration worker service is unavailable"))
		return
	}
	var body struct {
		MigrationMemberID       string `json:"migration_member_id"`
		AttemptID               string `json:"attempt_id"`
		LeaseToken              string `json:"lease_token"`
		Status                  string `json:"status"`
		TargetAppProfileID      uint64 `json:"target_app_profile_id"`
		TargetConfigVersion     int64  `json:"target_config_version"`
		TargetConfigFingerprint string `json:"target_config_fingerprint"`
		TargetAgentID           string `json:"target_agent_id"`
		TargetReadbackHash      string `json:"target_readback_hash"`
		ErrorCode               string `json:"error_code"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.AppMigrations.Report(r.Context(), appmigration.ReportCommand{
		MigrationMemberID: body.MigrationMemberID, AttemptID: body.AttemptID, LeaseToken: body.LeaseToken,
		Status: body.Status, TargetAppProfileID: body.TargetAppProfileID,
		TargetConfigVersion: body.TargetConfigVersion, TargetConfigFingerprint: body.TargetConfigFingerprint,
		TargetAgentID: body.TargetAgentID, TargetReadbackHash: body.TargetReadbackHash, ErrorCode: body.ErrorCode,
	}, time.Now().UTC())
	w.Header().Set("Cache-Control", "no-store")
	writeResult(w, r, http.StatusOK, result, err)
}
