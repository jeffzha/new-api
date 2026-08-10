package httpapi

import (
	"net/http"

	"github.com/QuantumNous/new-api/claw-control/internal/billingimport"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
)

func (s *Server) createTencentBillingImport(w http.ResponseWriter, r *http.Request) {
	if s.services.BillingImports == nil {
		writeError(w, r, domain.Unavailable("Tencent Billing import is unavailable"))
		return
	}
	var body struct {
		Month        string `json:"month"`
		BusinessCode string `json:"business_code"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := s.services.BillingImports.Create(billingimport.CreateCommand{
		Month: body.Month, BusinessCode: body.BusinessCode, Actor: actor(r), RequestID: requestID(r),
	})
	writeResult(w, r, http.StatusAccepted, result, err)
}

func (s *Server) listTencentBillingImports(w http.ResponseWriter, r *http.Request) {
	if s.services.BillingImports == nil {
		writeError(w, r, domain.Unavailable("Tencent Billing import is unavailable"))
		return
	}
	result, err := s.services.BillingImports.List(queryLimit(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) getTencentBillingImport(w http.ResponseWriter, r *http.Request) {
	if s.services.BillingImports == nil {
		writeError(w, r, domain.Unavailable("Tencent Billing import is unavailable"))
		return
	}
	result, err := s.services.BillingImports.Get(r.PathValue("import_id"))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) retryTencentBillingImport(w http.ResponseWriter, r *http.Request) {
	if s.services.BillingImports == nil {
		writeError(w, r, domain.Unavailable("Tencent Billing import is unavailable"))
		return
	}
	result, err := s.services.BillingImports.Retry(r.PathValue("import_id"), actor(r), requestID(r))
	writeResult(w, r, http.StatusAccepted, result, err)
}
