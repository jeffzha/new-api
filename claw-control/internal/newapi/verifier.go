package newapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
)

const maximumIdentityResponseBytes = 64 << 10

const (
	workbenchContractVersionHeader = "X-Workbench-Contract-Version"
	workbenchContractVersion       = "1"
)

type IdentityVerifier interface {
	Verify(ctx context.Context, userID int64, expectedIdentityVersion string) error
	VerifyFresh(ctx context.Context, userID int64, expectedIdentityVersion string) error
	VerifyAdmin(ctx context.Context, userID int64, expectedIdentityVersion string) error
}

type HTTPIdentityVerifier struct {
	endpoint      *url.URL
	adminEndpoint *url.URL
	secret        []byte
	client        *http.Client
	maxSkew       time.Duration
	cacheTTL      time.Duration
	cacheMu       sync.Mutex
	positiveCache map[string]time.Time
}

type identityStatusResponse struct {
	Success bool `json:"success"`
	Data    struct {
		UserID          int64  `json:"user_id"`
		Exists          bool   `json:"exists"`
		Enabled         bool   `json:"enabled"`
		IdentityVersion string `json:"identity_version"`
		IsSuperAdmin    bool   `json:"is_super_admin"`
	} `json:"data"`
}

type identityStatusRequest struct {
	UserID          int64  `json:"user_id"`
	IdentityVersion string `json:"identity_version"`
}

func NewHTTPIdentityVerifier(endpoint, adminEndpoint string, secret string, timeout, maxSkew time.Duration) (*HTTPIdentityVerifier, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || !validIdentityEndpoint(parsed, "/api/internal/workbench/identity-status") {
		return nil, fmt.Errorf("invalid new-api identity-status URL")
	}
	parsedAdmin, err := url.Parse(adminEndpoint)
	if err != nil || !validIdentityEndpoint(parsedAdmin, "/api/internal/workbench/admin-identity-status") {
		return nil, fmt.Errorf("invalid new-api admin identity-status URL")
	}
	if len(secret) < 32 || timeout <= 0 || maxSkew <= 0 {
		return nil, fmt.Errorf("invalid new-api identity verifier configuration")
	}
	return &HTTPIdentityVerifier{
		endpoint: parsed, adminEndpoint: parsedAdmin,
		secret: []byte(secret), client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}, maxSkew: maxSkew, cacheTTL: 15 * time.Second, positiveCache: make(map[string]time.Time),
	}, nil
}

func validIdentityEndpoint(endpoint *url.URL, expectedPath string) bool {
	return endpoint != nil && endpoint.Host != "" && endpoint.User == nil && endpoint.RawQuery == "" && endpoint.Fragment == "" &&
		(endpoint.Scheme == "http" || endpoint.Scheme == "https") && endpoint.Path == expectedPath
}

func (v *HTTPIdentityVerifier) Verify(ctx context.Context, userID int64, expectedIdentityVersion string) error {
	return v.verifyCached(ctx, v.endpoint, userID, expectedIdentityVersion, false)
}

// VerifyFresh bypasses the short positive cache before a caller releases
// provider credentials or performs another secret-bearing operation.
func (v *HTTPIdentityVerifier) VerifyFresh(ctx context.Context, userID int64, expectedIdentityVersion string) error {
	return v.verifyAt(ctx, v.endpoint, userID, expectedIdentityVersion, false)
}

func (v *HTTPIdentityVerifier) VerifyAdmin(ctx context.Context, userID int64, expectedIdentityVersion string) error {
	// Administrative sessions protect customer/App lifecycle and billing state.
	// Revalidate the current root identity for every sensitive operation instead
	// of accepting the short positive cache used by ordinary workbench traffic.
	return v.verifyAt(ctx, v.adminEndpoint, userID, expectedIdentityVersion, true)
}

// ResolveEnabled performs an uncached authoritative lookup for administrative
// membership provisioning and returns the current signed identity version.
func (v *HTTPIdentityVerifier) ResolveEnabled(ctx context.Context, userID int64) (string, error) {
	if userID <= 0 {
		return "", domain.Invalid("new-api user ID must be positive")
	}
	result, err := v.statusAt(ctx, v.endpoint, userID, "")
	if err != nil {
		return "", err
	}
	identityVersion := result.Data.IdentityVersion
	if !result.Success || result.Data.UserID != userID || !result.Data.Exists || !result.Data.Enabled ||
		identityVersion == "" || len(identityVersion) > 128 || strings.TrimSpace(identityVersion) != identityVersion {
		return "", domain.Forbidden("new-api identity is missing or disabled")
	}
	return identityVersion, nil
}

func (v *HTTPIdentityVerifier) verifyCached(ctx context.Context, endpoint *url.URL, userID int64, expectedIdentityVersion string, requireSuperAdmin bool) error {
	cacheKey := fmt.Sprintf("%t:%d:%s", requireSuperAdmin, userID, expectedIdentityVersion)
	now := time.Now().UTC()
	v.cacheMu.Lock()
	expiresAt, ok := v.positiveCache[cacheKey]
	if ok && now.Before(expiresAt) {
		v.cacheMu.Unlock()
		return nil
	}
	delete(v.positiveCache, cacheKey)
	v.cacheMu.Unlock()
	if err := v.verifyAt(ctx, endpoint, userID, expectedIdentityVersion, requireSuperAdmin); err != nil {
		return err
	}
	v.cacheMu.Lock()
	v.positiveCache[cacheKey] = now.Add(v.cacheTTL)
	v.cacheMu.Unlock()
	return nil
}

func (v *HTTPIdentityVerifier) verifyAt(ctx context.Context, endpoint *url.URL, userID int64, expectedIdentityVersion string, requireSuperAdmin bool) error {
	result, err := v.statusAt(ctx, endpoint, userID, expectedIdentityVersion)
	if err != nil {
		return err
	}
	if !result.Success || result.Data.UserID != userID || !result.Data.Exists || !result.Data.Enabled || result.Data.IdentityVersion != expectedIdentityVersion || (requireSuperAdmin && !result.Data.IsSuperAdmin) {
		return domain.Forbidden("new-api identity is missing, disabled, or changed")
	}
	return nil
}

func (v *HTTPIdentityVerifier) statusAt(ctx context.Context, endpoint *url.URL, userID int64, expectedIdentityVersion string) (*identityStatusResponse, error) {
	body, err := jsonx.Marshal(identityStatusRequest{UserID: userID, IdentityVersion: expectedIdentityVersion})
	if err != nil {
		return nil, err
	}
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate new-api identity-status nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	bodyHash := sha256.Sum256(body)
	path := endpoint.EscapedPath()
	canonical := strings.Join([]string{workbenchContractVersion, timestamp, nonce, http.MethodPost, path, hex.EncodeToString(bodyHash[:])}, "\n")
	mac := hmac.New(sha256.New, v.secret)
	_, _ = mac.Write([]byte(canonical))
	requestSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(workbenchContractVersionHeader, workbenchContractVersion)
	request.Header.Set("X-Workbench-Timestamp", timestamp)
	request.Header.Set("X-Workbench-Nonce", nonce)
	request.Header.Set("X-Workbench-Signature", requestSignature)
	response, err := v.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call new-api identity-status: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximumIdentityResponseBytes+1))
	if err != nil || len(responseBody) > maximumIdentityResponseBytes {
		return nil, fmt.Errorf("read new-api identity-status response")
	}
	if err := v.verifyResponse(response.StatusCode, path, nonce, response.Header, responseBody); err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("new-api identity-status returned HTTP %d", response.StatusCode)
	}
	var result identityStatusResponse
	if err := jsonx.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode new-api identity-status response: %w", err)
	}
	return &result, nil
}

func (v *HTTPIdentityVerifier) verifyResponse(status int, path, requestNonce string, header http.Header, body []byte) error {
	if strings.TrimSpace(header.Get(workbenchContractVersionHeader)) != workbenchContractVersion {
		return fmt.Errorf("unsupported new-api identity-status contract version")
	}
	timestamp := strings.TrimSpace(header.Get("X-Workbench-Response-Timestamp"))
	nonce := strings.TrimSpace(header.Get("X-Workbench-Response-Nonce"))
	signature := strings.ToLower(strings.TrimSpace(header.Get("X-Workbench-Response-Signature")))
	unixTime, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || nonce != requestNonce {
		return fmt.Errorf("invalid new-api identity-status response authentication")
	}
	now := time.Now().UTC()
	responseTime := time.Unix(unixTime, 0).UTC()
	if now.Sub(responseTime) > v.maxSkew || responseTime.Sub(now) > v.maxSkew {
		return fmt.Errorf("stale new-api identity-status response")
	}
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{workbenchContractVersion, strconv.Itoa(status), path, timestamp, requestNonce, hex.EncodeToString(bodyHash[:])}, "\n")
	mac := hmac.New(sha256.New, v.secret)
	_, _ = mac.Write([]byte(canonical))
	received, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(received, mac.Sum(nil)) {
		return fmt.Errorf("invalid new-api identity-status response signature")
	}
	return nil
}
