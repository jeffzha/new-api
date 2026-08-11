package workbenchbridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlClientIssuesIdentityOnlyTicketAndVerifiesSignedResponse(t *testing.T) {
	secret := []byte("control-hmac-secret-0123456789abcdef")
	now := time.Unix(1_786_240_000, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, controlTicketIssuePath, request.URL.Path)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		timestamp, err := strconv.ParseInt(request.Header.Get(controlTimestampHeader), 10, 64)
		require.NoError(t, err)
		nonce := request.Header.Get(controlNonceHeader)
		assert.Equal(t, controlContractVersion, request.Header.Get(controlContractVersionHeader))
		assert.Equal(t, "new-api-core", request.Header.Get(controlServiceHeader))
		assert.Equal(t, signControlRequest(secret, http.MethodPost, controlTicketIssuePath, timestamp, nonce, body), request.Header.Get(controlSignatureHeader))
		assert.Contains(t, string(body), `"new_api_user_id":42`)
		assert.Contains(t, string(body), `"identity_version":"v1.identity"`)
		var payload map[string]any
		require.NoError(t, common.Unmarshal(body, &payload))
		assert.NotContains(t, string(body), "username")
		assert.NotContains(t, string(body), "role")
		assert.NotContains(t, string(body), "password")
		if payload["surface"] == SurfaceAdmin {
			assert.EqualValues(t, now.Unix(), payload["authenticated_at"])
			assert.Equal(t, []any{"webauthn"}, payload["amr"])
			assert.NotEmpty(t, payload["reauth_nonce"])
		} else {
			assert.Equal(t, SurfaceWorkbench, payload["surface"])
			assert.NotContains(t, payload, "authenticated_at")
			assert.NotContains(t, payload, "reauth_nonce")
		}

		responseBody, err := common.Marshal(map[string]any{
			"success": true,
			"data": map[string]any{
				"ticket":     "control-ticket",
				"expires_at": now.Add(time.Minute).Unix(),
			},
		})
		require.NoError(t, err)
		setSignedControlResponse(writer, secret, http.StatusOK, controlTicketIssuePath, now.Unix(), nonce, responseBody)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(responseBody)
	}))
	defer server.Close()

	client, err := NewControlClient(Config{
		Enabled:                  true,
		ControlURL:               server.URL,
		AllowInsecureControlHTTP: true,
		ControlHMACSecret:        secret,
		ControlServiceName:       "new-api-core",
		ControlTimeout:           time.Second,
	})
	require.NoError(t, err)
	client.now = func() time.Time { return now }

	issued, err := client.Issue(context.Background(), TicketIssueRequest{
		UserID: 42, IdentityVersion: "v1.identity", Surface: SurfaceWorkbench,
	})
	require.NoError(t, err)
	assert.Equal(t, "control-ticket", issued.Value)
	assert.Equal(t, now.Add(time.Minute), issued.ExpiresAt)
	proofNonce := sha256.Sum256([]byte("client-step-up-proof"))
	issued, err = client.Issue(context.Background(), TicketIssueRequest{
		UserID: 42, IdentityVersion: "v1.identity", Surface: SurfaceAdmin, IsSuperAdmin: true,
		AuthenticatedAt: now, AMR: []string{"webauthn"}, ReauthNonce: base64.RawURLEncoding.EncodeToString(proofNonce[:]),
	})
	require.NoError(t, err)
	assert.Equal(t, "control-ticket", issued.Value)
}

func TestControlClientRejectsTamperedResponse(t *testing.T) {
	secret := []byte("control-hmac-secret-0123456789abcdef")
	now := time.Unix(1_786_240_000, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		responseBody := []byte(`{"success":true,"data":{"ticket":"attacker-ticket","expires_at":"2026-08-09T12:00:00Z"}}`)
		setSignedControlResponse(writer, secret, http.StatusOK, controlTicketIssuePath, now.Unix(), request.Header.Get(controlNonceHeader), []byte("different body"))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(responseBody)
	}))
	defer server.Close()

	client, err := NewControlClient(Config{
		Enabled:                  true,
		ControlURL:               server.URL,
		AllowInsecureControlHTTP: true,
		ControlHMACSecret:        secret,
		ControlServiceName:       "new-api-core",
		ControlTimeout:           time.Second,
	})
	require.NoError(t, err)
	client.now = func() time.Time { return now }

	_, err = client.Issue(context.Background(), TicketIssueRequest{
		UserID: 42, IdentityVersion: "v1.identity", Surface: SurfaceWorkbench,
	})
	assert.ErrorIs(t, err, ErrControlUnavailable)
}

func setSignedControlResponse(writer http.ResponseWriter, secret []byte, status int, path string, timestamp int64, nonce string, body []byte) {
	bodyDigest := sha256.Sum256(body)
	canonical := fmt.Sprintf("%s\n%d\n%s\n%d\n%s\n%s", controlContractVersion, status, path, timestamp, nonce, hex.EncodeToString(bodyDigest[:]))
	signer := hmac.New(sha256.New, secret)
	_, _ = signer.Write([]byte(canonical))
	writer.Header().Set(controlContractVersionHeader, controlContractVersion)
	writer.Header().Set(controlResponseTimeHeader, strconv.FormatInt(timestamp, 10))
	writer.Header().Set(controlResponseNonceHeader, nonce)
	writer.Header().Set(controlResponseSignatureHead, hex.EncodeToString(signer.Sum(nil)))
}
