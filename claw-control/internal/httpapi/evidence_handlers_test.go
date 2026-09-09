package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/httpapi"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/marginreport"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type evidenceIdentityVerifier struct{}

func (evidenceIdentityVerifier) Verify(context.Context, int64, string) error      { return nil }
func (evidenceIdentityVerifier) VerifyFresh(context.Context, int64, string) error { return nil }
func (evidenceIdentityVerifier) VerifyAdmin(context.Context, int64, string) error { return nil }

func TestEvidenceUploadAndAuthorizedDownloadUseAdminSessionBoundary(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	evidenceService, err := evidence.New(db, t.TempDir(), bytes.Repeat([]byte{0x71}, 32), 1024, evidence.ScannerFunc(func(context.Context, []byte) error { return nil }))
	require.NoError(t, err)
	accessService := access.New(db, secrets.EnvironmentResolver{}, evidenceIdentityVerifier{}, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	nonce := sha256.Sum256([]byte("evidence-admin-step-up"))
	entry, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 77, IdentityVersion: "admin.v1", Surface: "admin", IsSuperAdmin: true,
		AuthenticatedAt: time.Now().UTC(), AMR: []string{"otp"}, ReauthNonce: base64.RawURLEncoding.EncodeToString(nonce[:]),
	})
	require.NoError(t, err)
	server := httpapi.New(httpapi.Services{
		DB: db, Access: accessService, Evidence: evidenceService, Margins: marginreport.New(db),
	}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{
		ADPSSORedirectPath: "/workbench/auth/sso", AdminRedirectPath: "/workbench/admin",
	})
	entryRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/entry?ticket="+entry.Ticket, nil)
	entryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(entryResponse, entryRequest)
	require.Equal(t, http.StatusFound, entryResponse.Code)
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range entryResponse.Result().Cookies() {
		if cookie.Name == "claw_admin_session" {
			sessionCookie = cookie
		}
		if cookie.Name == "claw_admin_csrf" {
			csrfCookie = cookie
		}
	}
	require.NotNil(t, sessionCookie)
	require.NotNil(t, csrfCookie)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="file"; filename="proof.json"`)
	partHeader.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write([]byte(`{"source":"console"}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	withoutCSRF := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/evidence", bytes.NewReader(body.Bytes()))
	withoutCSRF.Header.Set("Content-Type", writer.FormDataContentType())
	withoutCSRF.AddCookie(sessionCookie)
	withoutCSRFResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(withoutCSRFResponse, withoutCSRF)
	assert.Equal(t, http.StatusForbidden, withoutCSRFResponse.Code)
	assert.Equal(t, "no-store", withoutCSRFResponse.Header().Get("Cache-Control"))

	upload := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/evidence", bytes.NewReader(body.Bytes()))
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	upload.Header.Set("X-CSRF-Token", csrfCookie.Value)
	upload.AddCookie(sessionCookie)
	uploadResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(uploadResponse, upload)
	require.Equal(t, http.StatusCreated, uploadResponse.Code, uploadResponse.Body.String())
	assert.Equal(t, "no-store", uploadResponse.Header().Get("Cache-Control"))
	var envelope struct {
		Success bool              `json:"success"`
		Data    evidence.Metadata `json:"data"`
	}
	require.NoError(t, jsonx.Unmarshal(uploadResponse.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/evidence/"+envelope.Data.EvidenceRef, nil)
	unauthorizedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorizedResponse, unauthorized)
	assert.Equal(t, http.StatusUnauthorized, unauthorizedResponse.Code)

	download := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/evidence/"+envelope.Data.EvidenceRef, nil)
	download.Header.Set("X-Request-ID", "req-download-evidence")
	download.AddCookie(sessionCookie)
	downloadResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(downloadResponse, download)
	require.Equal(t, http.StatusOK, downloadResponse.Code)
	assert.Equal(t, `{"source":"console"}`, downloadResponse.Body.String())
	assert.Equal(t, "no-store", downloadResponse.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", downloadResponse.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, downloadResponse.Header().Get("Content-Disposition"), "attachment;")
	var downloadAudit model.AdminAudit
	require.NoError(t, db.Where("action = ? AND resource_id = ?", "evidence.download", envelope.Data.EvidenceRef).First(&downloadAudit).Error)
	assert.Equal(t, "admin-session:77", downloadAudit.Actor)
	assert.Equal(t, "req-download-evidence", downloadAudit.RequestID)
}

func TestInternalMarginReportIsReadOnlyAndNoStore(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	accessService, sessionCookie, _ := issueAdminSession(t, db, secrets.EnvironmentResolver{}, 78)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService, Margins: marginreport.New(db)}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})
	request := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/margin-report", nil)
	request.AddCookie(sessionCookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.Contains(t, response.Body.String(), `"confidence":"unverified"`)
}
