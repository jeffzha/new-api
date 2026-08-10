package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/marginreport"
)

const multipartEnvelopeBytes = 1 << 20

func (s *Server) uploadEvidence(w http.ResponseWriter, r *http.Request) {
	if s.services.Evidence == nil {
		writeError(w, r, domain.Unavailable("evidence store is unavailable"))
		return
	}
	maximumBodyBytes := s.services.Evidence.MaxUploadBytes() + multipartEnvelopeBytes
	r.Body = http.MaxBytesReader(w, r.Body, maximumBodyBytes)
	if err := r.ParseMultipartForm(maximumBodyBytes); err != nil {
		writeError(w, r, domain.Invalid("invalid or oversized evidence upload"))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var customerID *uint64
	if value := strings.TrimSpace(r.FormValue("customer_id")); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, r, domain.Invalid("customer_id must be positive"))
			return
		}
		customerID = &parsed
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, domain.Invalid("evidence file is required"))
		return
	}
	defer file.Close()
	result, err := s.services.Evidence.Upload(evidence.UploadCommand{
		CustomerID: customerID, Filename: header.Filename, DeclaredMIME: header.Header.Get("Content-Type"),
		Content: file, Actor: actor(r), RequestID: requestID(r), Context: r.Context(),
	})
	writeResult(w, r, http.StatusCreated, result, err)
}

func (s *Server) listEvidence(w http.ResponseWriter, r *http.Request) {
	if s.services.Evidence == nil {
		writeError(w, r, domain.Unavailable("evidence store is unavailable"))
		return
	}
	var customerID *uint64
	if value := strings.TrimSpace(r.URL.Query().Get("customer_id")); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, r, domain.Invalid("customer_id must be positive"))
			return
		}
		customerID = &parsed
	}
	result, err := s.services.Evidence.List(customerID, queryLimit(r))
	writeResult(w, r, http.StatusOK, result, err)
}

func (s *Server) downloadEvidence(w http.ResponseWriter, r *http.Request) {
	if s.services.Evidence == nil {
		writeError(w, r, domain.Unavailable("evidence store is unavailable"))
		return
	}
	result, err := s.services.Evidence.GetForAuthorizedDownload(r.PathValue("evidence_ref"), actor(r), requestID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", result.Metadata.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(result.Metadata.SizeBytes, 10))
	w.Header().Set("Content-Disposition", safeAttachmentDisposition(result.Metadata.Filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Content)
}

func (s *Server) marginReport(w http.ResponseWriter, r *http.Request) {
	if s.services.Margins == nil {
		writeError(w, r, domain.Unavailable("margin report is unavailable"))
		return
	}
	command := marginreport.Command{}
	if value := strings.TrimSpace(r.URL.Query().Get("customer_id")); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, r, domain.Invalid("customer_id must be positive"))
			return
		}
		command.CustomerID = &parsed
	}
	startValue := strings.TrimSpace(r.URL.Query().Get("start_at"))
	endValue := strings.TrimSpace(r.URL.Query().Get("end_at"))
	if startValue != "" {
		start, err := time.Parse(time.RFC3339, startValue)
		if err != nil {
			writeError(w, r, domain.Invalid("start_at must be RFC3339"))
			return
		}
		command.StartAt = &start
	}
	if endValue != "" {
		end, err := time.Parse(time.RFC3339, endValue)
		if err != nil {
			writeError(w, r, domain.Invalid("end_at must be RFC3339"))
			return
		}
		command.EndAt = &end
	}
	result, err := s.services.Margins.Build(command)
	writeResult(w, r, http.StatusOK, result, err)
}

func safeAttachmentDisposition(filename string) string {
	var fallback strings.Builder
	for _, value := range filename {
		if value < unicode.MaxASCII && (unicode.IsLetter(value) || unicode.IsDigit(value) || strings.ContainsRune("._-", value)) {
			fallback.WriteRune(value)
		} else {
			fallback.WriteByte('_')
		}
		if fallback.Len() >= 120 {
			break
		}
	}
	if fallback.Len() == 0 {
		fallback.WriteString("evidence")
	}
	encoded := strings.ReplaceAll(url.QueryEscape(filename), "+", "%20")
	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", fallback.String(), encoded)
}
