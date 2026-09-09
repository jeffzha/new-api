package newapi_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/newapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testContractVersionHeader = "X-Workbench-Contract-Version"
	testContractVersion       = "1"
)

func TestIdentityVerifierRequiresSignedMatchingResponse(t *testing.T) {
	const secret = "abcdef0123456789abcdef0123456789"
	const identityVersion = "v1.identity"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testContractVersion, r.Header.Get(testContractVersionHeader))
		requestSignature := r.Header.Get("X-Workbench-Signature")
		assert.True(t, strings.HasPrefix(requestSignature, "sha256="))
		requestNonce := r.Header.Get("X-Workbench-Nonce")
		nonceBytes, err := hex.DecodeString(requestNonce)
		require.NoError(t, err)
		assert.Len(t, nonceBytes, 32)
		requestBody, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Contains(t, string(requestBody), `"identity_version":`)
		body := []byte(`{"success":true,"data":{"user_id":42,"exists":true,"enabled":true,"identity_version":"v1.identity","is_super_admin":true}}`)
		timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		bodyHash := sha256.Sum256(body)
		canonical := strings.Join([]string{
			testContractVersion, strconv.Itoa(http.StatusOK), r.URL.EscapedPath(), timestamp, requestNonce, hex.EncodeToString(bodyHash[:]),
		}, "\n")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		w.Header().Set("X-Workbench-Response-Timestamp", timestamp)
		w.Header().Set(testContractVersionHeader, testContractVersion)
		w.Header().Set("X-Workbench-Response-Nonce", requestNonce)
		w.Header().Set("X-Workbench-Response-Signature", hex.EncodeToString(mac.Sum(nil)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	verifier, err := newapi.NewHTTPIdentityVerifier(
		server.URL+"/api/internal/workbench/identity-status",
		server.URL+"/api/internal/workbench/admin-identity-status",
		secret, time.Second, time.Minute,
	)
	require.NoError(t, err)
	require.NoError(t, verifier.Verify(context.Background(), 42, identityVersion))
	require.NoError(t, verifier.VerifyAdmin(context.Background(), 42, identityVersion))
	assert.Error(t, verifier.VerifyAdmin(context.Background(), 42, "v1.other"))
}

func TestIdentityVerifierRejectsUnsignedBodyChange(t *testing.T) {
	const secret = "abcdef0123456789abcdef0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		w.Header().Set(testContractVersionHeader, testContractVersion)
		w.Header().Set("X-Workbench-Response-Timestamp", timestamp)
		w.Header().Set("X-Workbench-Response-Nonce", r.Header.Get("X-Workbench-Nonce"))
		w.Header().Set("X-Workbench-Response-Signature", strings.Repeat("0", 64))
		_, _ = w.Write([]byte(`{"success":true,"data":{"user_id":42,"exists":true,"enabled":true,"identity_version":"v1.identity"}}`))
	}))
	defer server.Close()
	verifier, err := newapi.NewHTTPIdentityVerifier(
		server.URL+"/api/internal/workbench/identity-status",
		server.URL+"/api/internal/workbench/admin-identity-status",
		secret, time.Second, time.Minute,
	)
	require.NoError(t, err)
	assert.Error(t, verifier.Verify(context.Background(), 42, "v1.identity"))
	_, err = verifier.ResolveEnabled(context.Background(), 42)
	assert.Error(t, err)
}

func TestIdentityVerifierRejectsMissingOrTamperedResponseNonce(t *testing.T) {
	const secret = "abcdef0123456789abcdef0123456789"
	body := []byte(`{"success":true,"data":{"user_id":42,"exists":true,"enabled":true,"identity_version":"v1.identity"}}`)
	for _, testCase := range []struct {
		name        string
		responseFor func(string) string
	}{
		{name: "missing", responseFor: func(string) string { return "" }},
		{name: "tampered", responseFor: func(nonce string) string {
			if nonce[0] == '0' {
				return "1" + nonce[1:]
			}
			return "0" + nonce[1:]
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestNonce := r.Header.Get("X-Workbench-Nonce")
				timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
				bodyHash := sha256.Sum256(body)
				canonical := strings.Join([]string{
					testContractVersion, strconv.Itoa(http.StatusOK), r.URL.EscapedPath(), timestamp, requestNonce, hex.EncodeToString(bodyHash[:]),
				}, "\n")
				mac := hmac.New(sha256.New, []byte(secret))
				_, _ = mac.Write([]byte(canonical))
				w.Header().Set(testContractVersionHeader, testContractVersion)
				w.Header().Set("X-Workbench-Response-Timestamp", timestamp)
				if responseNonce := testCase.responseFor(requestNonce); responseNonce != "" {
					w.Header().Set("X-Workbench-Response-Nonce", responseNonce)
				}
				w.Header().Set("X-Workbench-Response-Signature", hex.EncodeToString(mac.Sum(nil)))
				_, _ = w.Write(body)
			}))
			defer server.Close()
			verifier, err := newapi.NewHTTPIdentityVerifier(
				server.URL+"/api/internal/workbench/identity-status",
				server.URL+"/api/internal/workbench/admin-identity-status",
				secret, time.Second, time.Minute,
			)
			require.NoError(t, err)
			assert.Error(t, verifier.Verify(context.Background(), 42, "v1.identity"))
		})
	}
}

func TestIdentityVerifierRejectsResponseReplayForAnotherRequest(t *testing.T) {
	const secret = "abcdef0123456789abcdef0123456789"
	body := []byte(`{"success":true,"data":{"user_id":42,"exists":true,"enabled":true,"identity_version":"v1.current"}}`)
	var calls atomic.Int64
	var firstNonce, secondNonce, replayTimestamp, replaySignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNonce := r.Header.Get("X-Workbench-Nonce")
		if calls.Add(1) == 1 {
			firstNonce = requestNonce
			replayTimestamp = strconv.FormatInt(time.Now().UTC().Unix(), 10)
			bodyHash := sha256.Sum256(body)
			canonical := strings.Join([]string{
				testContractVersion, strconv.Itoa(http.StatusOK), r.URL.EscapedPath(), replayTimestamp, firstNonce, hex.EncodeToString(bodyHash[:]),
			}, "\n")
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write([]byte(canonical))
			replaySignature = hex.EncodeToString(mac.Sum(nil))
		} else {
			secondNonce = requestNonce
		}
		w.Header().Set(testContractVersionHeader, testContractVersion)
		w.Header().Set("X-Workbench-Response-Timestamp", replayTimestamp)
		w.Header().Set("X-Workbench-Response-Nonce", firstNonce)
		w.Header().Set("X-Workbench-Response-Signature", replaySignature)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	verifier, err := newapi.NewHTTPIdentityVerifier(
		server.URL+"/api/internal/workbench/identity-status",
		server.URL+"/api/internal/workbench/admin-identity-status",
		secret, time.Second, time.Minute,
	)
	require.NoError(t, err)
	identityVersion, err := verifier.ResolveEnabled(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "v1.current", identityVersion)
	_, err = verifier.ResolveEnabled(context.Background(), 42)
	assert.Error(t, err)
	assert.NotEmpty(t, firstNonce)
	assert.NotEmpty(t, secondNonce)
	assert.NotEqual(t, firstNonce, secondNonce, "every request must use a fresh unpredictable nonce")
}

func TestResolveEnabledIsUncachedAndFailClosed(t *testing.T) {
	const secret = "abcdef0123456789abcdef0123456789"
	var calls atomic.Int64
	statusBody := atomic.Value{}
	statusBody.Store([]byte(`{"success":true,"data":{"user_id":42,"exists":true,"enabled":true,"identity_version":"v1.current"}}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeSignedIdentityResponse(w, r, secret, http.StatusOK, statusBody.Load().([]byte))
	}))
	verifier, err := newapi.NewHTTPIdentityVerifier(
		server.URL+"/api/internal/workbench/identity-status",
		server.URL+"/api/internal/workbench/admin-identity-status",
		secret, time.Second, time.Minute,
	)
	require.NoError(t, err)

	for range 2 {
		identityVersion, resolveErr := verifier.ResolveEnabled(context.Background(), 42)
		require.NoError(t, resolveErr)
		assert.Equal(t, "v1.current", identityVersion)
	}
	assert.Equal(t, int64(2), calls.Load(), "membership provisioning must bypass the ordinary positive cache")

	for name, body := range map[string][]byte{
		"missing":  []byte(`{"success":true,"data":{"user_id":42,"exists":false,"enabled":false}}`),
		"disabled": []byte(`{"success":true,"data":{"user_id":42,"exists":true,"enabled":false,"identity_version":"v1.current"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			statusBody.Store(body)
			_, resolveErr := verifier.ResolveEnabled(context.Background(), 42)
			assert.Error(t, resolveErr)
		})
	}

	server.Close()
	_, err = verifier.ResolveEnabled(context.Background(), 42)
	assert.Error(t, err, "network failure must fail closed")
}

func TestSensitiveIdentityVerificationBypassesPositiveCache(t *testing.T) {
	const secret = "abcdef0123456789abcdef0123456789"
	const identityVersion = "v1.identity"
	var ordinaryCalls atomic.Int64
	var adminCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/internal/workbench/admin-identity-status" {
			adminCalls.Add(1)
		} else {
			ordinaryCalls.Add(1)
		}
		requestNonce := r.Header.Get("X-Workbench-Nonce")
		body := []byte(`{"success":true,"data":{"user_id":42,"exists":true,"enabled":true,"identity_version":"v1.identity","is_super_admin":true}}`)
		timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		bodyHash := sha256.Sum256(body)
		canonical := strings.Join([]string{
			testContractVersion, strconv.Itoa(http.StatusOK), r.URL.EscapedPath(), timestamp, requestNonce, hex.EncodeToString(bodyHash[:]),
		}, "\n")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		w.Header().Set("X-Workbench-Response-Timestamp", timestamp)
		w.Header().Set(testContractVersionHeader, testContractVersion)
		w.Header().Set("X-Workbench-Response-Nonce", requestNonce)
		w.Header().Set("X-Workbench-Response-Signature", hex.EncodeToString(mac.Sum(nil)))
		_, _ = w.Write(body)
	}))
	defer server.Close()
	verifier, err := newapi.NewHTTPIdentityVerifier(
		server.URL+"/api/internal/workbench/identity-status",
		server.URL+"/api/internal/workbench/admin-identity-status",
		secret, time.Second, time.Minute,
	)
	require.NoError(t, err)

	require.NoError(t, verifier.Verify(context.Background(), 42, identityVersion))
	require.NoError(t, verifier.Verify(context.Background(), 42, identityVersion))
	require.NoError(t, verifier.VerifyFresh(context.Background(), 42, identityVersion))
	require.NoError(t, verifier.VerifyFresh(context.Background(), 42, identityVersion))
	require.NoError(t, verifier.VerifyAdmin(context.Background(), 42, identityVersion))
	require.NoError(t, verifier.VerifyAdmin(context.Background(), 42, identityVersion))

	assert.Equal(t, int64(3), ordinaryCalls.Load(), "secret-bearing checks must bypass the ordinary positive cache")
	assert.Equal(t, int64(2), adminCalls.Load(), "admin checks must revalidate new-api root identity every time")
}

func TestIdentityVerifierRejectsUnsafeEndpointsAndRedirects(t *testing.T) {
	const secret = "abcdef0123456789abcdef0123456789"
	validAdmin := "https://example.com/api/internal/workbench/admin-identity-status"
	for _, endpoint := range []string{
		"https://user@example.com/api/internal/workbench/identity-status",
		"https://example.com/api/internal/workbench/identity-status?target=other",
		"https://example.com/api/internal/workbench/identity-status#fragment",
		"https://example.com/not-identity-status",
	} {
		_, err := newapi.NewHTTPIdentityVerifier(endpoint, validAdmin, secret, time.Second, time.Minute)
		assert.Error(t, err)
	}

	var redirectedRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	verifier, err := newapi.NewHTTPIdentityVerifier(
		redirector.URL+"/api/internal/workbench/identity-status",
		redirector.URL+"/api/internal/workbench/admin-identity-status",
		secret, time.Second, time.Minute,
	)
	require.NoError(t, err)
	assert.Error(t, verifier.Verify(context.Background(), 42, "v1.identity"))
	assert.Zero(t, redirectedRequests.Load(), "signed headers must never be forwarded across redirects")
}

func writeSignedIdentityResponse(w http.ResponseWriter, r *http.Request, secret string, status int, body []byte) {
	requestNonce := r.Header.Get("X-Workbench-Nonce")
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{
		testContractVersion, strconv.Itoa(status), r.URL.EscapedPath(), timestamp, requestNonce, hex.EncodeToString(bodyHash[:]),
	}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	w.Header().Set("X-Workbench-Response-Timestamp", timestamp)
	w.Header().Set(testContractVersionHeader, testContractVersion)
	w.Header().Set("X-Workbench-Response-Nonce", requestNonce)
	w.Header().Set("X-Workbench-Response-Signature", hex.EncodeToString(mac.Sum(nil)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
