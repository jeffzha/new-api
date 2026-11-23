// Package opsclient provides the bounded, signed station read transport.
package opsclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type Client struct {
	BaseURL, KeyID, Secret string
	HTTP                   *http.Client
	readBudget             bool
}
type ReadError struct {
	Status     int
	RetryAfter time.Duration
	Code       string
}

func (e *ReadError) Error() string { return "OPS read HTTP " + strconv.Itoa(e.Status) }

func ValidateBase(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return errors.New("invalid OPS base URL")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
	if u.Scheme != "https" && !(local && u.Scheme == "http") {
		return errors.New("OPS requires HTTPS")
	}
	if strings.Contains(u.Path, "//") || strings.Contains(u.Path, "..") {
		return errors.New("invalid OPS base path")
	}
	return nil
}

func New(base, key, secret string, timeout time.Duration) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), KeyID: key, Secret: secret, HTTP: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: timeout}}}
}

func Sign(key, secret, method, target string, body []byte, at time.Time) http.Header {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return SignWithNonce(key, secret, method, target, body, strconv.FormatInt(at.Unix(), 10), hex.EncodeToString(nonce))
}

func SignWithNonce(key, secret, method, target string, body []byte, stamp, nonce string) http.Header {
	digest := sha256.Sum256([]byte(secret))
	hash := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(hex.EncodeToString(digest[:])))
	mac.Write([]byte(method + "\n" + target + "\n" + stamp + "\n" + nonce + "\n" + hex.EncodeToString(hash[:])))
	h := make(http.Header)
	h.Set("X-Gateway-Id", key)
	h.Set("X-Timestamp", stamp)
	h.Set("X-Nonce", nonce)
	h.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	return h
}

func (c *Client) Get(ctx context.Context, path string, at time.Time) ([]byte, error) {
	raw, unchanged, err := c.GetVersioned(ctx, path, at)
	if unchanged {
		return nil, &ReadError{Status: http.StatusNotModified}
	}
	return raw, err
}

// GetVersioned also accepts 304 for callers retaining a versioned memory snapshot.
func (c *Client) GetVersioned(ctx context.Context, path string, at time.Time) ([]byte, bool, error) {
	return c.request(ctx, http.MethodGet, path, nil, at)
}

// Post never retries a money operation with a different body or operation ID.
// The caller retains the same requestId until OPS has confirmed the result.
func (c *Client) Post(ctx context.Context, path string, body []byte, at time.Time) ([]byte, error) {
	raw, _, err := c.request(ctx, http.MethodPost, path, body, at)
	return raw, err
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, at time.Time) ([]byte, bool, error) {
	if method == http.MethodGet && c.readBudget {
		if err := c.reserveRead(ctx); err != nil {
			return nil, false, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, false, errors.New("OPS request invalid")
	}
	// /test is an external proxy prefix. OPS verifies the application path after
	// stripping that configured prefix, including the original query string.
	req.Header = Sign(c.KeyID, c.Secret, method, path, body, at)
	req.Header.Set("Accept-Encoding", "identity")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false, errors.New("OPS read unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error json.RawMessage `json:"error"`
		}
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		_ = common.Unmarshal(errorBody, &failure)
		var code string
		if common.Unmarshal(failure.Error, &code) != nil {
			var detail struct {
				Code string `json:"code"`
			}
			_ = common.Unmarshal(failure.Error, &detail)
			code = detail.Code
		}
		retry, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		wait := time.Duration(max(0, min(retry, 3600))) * time.Second
		if t, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil && t.After(at) {
			wait = min(t.Sub(at), time.Hour)
		}
		if c.readBudget && resp.StatusCode == http.StatusTooManyRequests {
			c.coolReadBudget(ctx, wait)
		}
		return nil, false, &ReadError{Status: resp.StatusCode, RetryAfter: wait, Code: code}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil || len(raw) > 4*1024*1024 {
		return nil, false, errors.New("OPS response invalid")
	}
	return raw, false, nil
}
