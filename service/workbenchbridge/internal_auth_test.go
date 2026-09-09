package workbenchbridge

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInternalRequestSignatureCoversMethodPathTimestampAndBody(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"user_id":42}`)
	nonce := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	signature := SignInternalRequest(secret, http.MethodPost, "/api/internal/workbench/identity-status", now.Unix(), nonce, body)

	require.NoError(t, VerifyInternalRequest(
		secret,
		http.MethodPost,
		"/api/internal/workbench/identity-status",
		ContractVersion,
		"1700000000",
		nonce,
		signature,
		body,
		now,
		time.Minute,
	))

	for _, testCase := range []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{name: "method", method: http.MethodGet, path: "/api/internal/workbench/identity-status", body: body},
		{name: "path", method: http.MethodPost, path: "/api/internal/workbench/other", body: body},
		{name: "body", method: http.MethodPost, path: "/api/internal/workbench/identity-status", body: []byte(`{"user_id":43}`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := VerifyInternalRequest(secret, testCase.method, testCase.path, ContractVersion, "1700000000", nonce, signature, testCase.body, now, time.Minute)
			assert.ErrorIs(t, err, ErrInvalidInternalSignature)
		})
	}
	for name, changedNonce := range map[string]string{
		"missing nonce":  "",
		"tampered nonce": "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			err := VerifyInternalRequest(secret, http.MethodPost, "/api/internal/workbench/identity-status", ContractVersion, "1700000000", changedNonce, signature, body, now, time.Minute)
			assert.ErrorIs(t, err, ErrInvalidInternalSignature)
		})
	}
}

func TestInternalRequestSignatureRejectsStaleTimestamp(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	signedAt := time.Unix(1_700_000_000, 0)
	body := []byte(`{"user_id":42}`)
	nonce := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	signature := SignInternalRequest(secret, http.MethodPost, "/api/internal/workbench/identity-status", signedAt.Unix(), nonce, body)

	err := VerifyInternalRequest(
		secret,
		http.MethodPost,
		"/api/internal/workbench/identity-status",
		ContractVersion,
		"1700000000",
		nonce,
		signature,
		body,
		signedAt.Add(time.Minute+time.Second),
		time.Minute,
	)
	assert.ErrorIs(t, err, ErrInvalidInternalSignature)
}

func TestInternalRequestRejectsMissingOrUnsupportedContractVersion(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"user_id":42}`)
	nonce := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	signature := SignInternalRequest(secret, http.MethodPost, "/api/internal/workbench/identity-status", now.Unix(), nonce, body)

	for _, version := range []string{"", "0", "2"} {
		err := VerifyInternalRequest(
			secret, http.MethodPost, "/api/internal/workbench/identity-status",
			version, "1700000000", nonce, signature, body, now, time.Minute,
		)
		assert.ErrorIs(t, err, ErrInvalidInternalSignature)
	}
}
