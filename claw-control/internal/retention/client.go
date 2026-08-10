package retention

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/google/uuid"
)

const (
	retentionContractVersion       = "2"
	retentionContractVersionHeader = "X-Workbench-Contract-Version"
	retentionPath                  = "/api/internal/workbench/retention/intents"
	retentionMaxResponseBytes      = 1 << 20
)

type HTTPDeliveryClient struct {
	endpoint *url.URL
	secret   string
	client   *http.Client
	skew     time.Duration
	now      func() time.Time
}

func NewHTTPDeliveryClient(rawURL, secret string, timeout, skew time.Duration, allowHTTP bool) (*HTTPDeliveryClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != retentionPath {
		return nil, errors.New("ADP retention URL must be the exact internal retention endpoint")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return nil, errors.New("ADP retention URL must use HTTPS")
	}
	if len(secret) < 32 || timeout <= 0 || skew <= 0 {
		return nil, errors.New("ADP retention signing secret and positive timeout/skew are required")
	}
	return &HTTPDeliveryClient{
		endpoint: parsed, secret: secret, skew: skew, now: time.Now,
		client: &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *HTTPDeliveryClient) Deliver(ctx context.Context, intent DeliveryIntent) (DeliveryReceipt, error) {
	body, err := jsonx.Marshal(intent)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	now := c.now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nonce := uuid.NewString()
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{retentionContractVersion, http.MethodPost, retentionPath, timestamp, nonce, hex.EncodeToString(bodyHash[:])}, "\n")
	mac := hmac.New(sha256.New, []byte(c.secret))
	_, _ = mac.Write([]byte(canonical))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return DeliveryReceipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(retentionContractVersionHeader, retentionContractVersion)
	req.Header.Set("X-Workbench-Service", "claw-control")
	req.Header.Set("X-Workbench-Timestamp", timestamp)
	req.Header.Set("X-Workbench-Nonce", nonce)
	req.Header.Set("X-Workbench-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := c.client.Do(req)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, retentionMaxResponseBytes+1))
	if err != nil || len(responseBody) > retentionMaxResponseBytes {
		return DeliveryReceipt{}, errors.New("ADP retention response is unreadable or too large")
	}
	if err := c.verifyResponse(response, responseBody, nonce, now); err != nil {
		return DeliveryReceipt{}, err
	}
	if response.StatusCode != http.StatusOK {
		return DeliveryReceipt{}, fmt.Errorf("ADP retention delivery failed with status %d", response.StatusCode)
	}
	var envelope struct {
		Success bool            `json:"success"`
		Data    DeliveryReceipt `json:"data"`
	}
	if err := jsonx.Unmarshal(responseBody, &envelope); err != nil || !envelope.Success {
		return DeliveryReceipt{}, errors.New("ADP retention response is invalid")
	}
	return envelope.Data, nil
}

func (c *HTTPDeliveryClient) verifyResponse(response *http.Response, body []byte, nonce string, requestedAt time.Time) error {
	if response.Header.Get(retentionContractVersionHeader) != retentionContractVersion || response.Header.Get("X-Workbench-Response-Nonce") != nonce {
		return errors.New("ADP retention response authentication is invalid")
	}
	timestamp := strings.TrimSpace(response.Header.Get("X-Workbench-Response-Timestamp"))
	unixTime, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || requestedAt.Sub(time.Unix(unixTime, 0).UTC()) > c.skew || time.Unix(unixTime, 0).UTC().Sub(c.now().UTC()) > c.skew {
		return errors.New("ADP retention response authentication is invalid")
	}
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{retentionContractVersion, strconv.Itoa(response.StatusCode), retentionPath, timestamp, nonce, hex.EncodeToString(bodyHash[:])}, "\n")
	mac := hmac.New(sha256.New, []byte(c.secret))
	_, _ = mac.Write([]byte(canonical))
	received, err := hex.DecodeString(strings.TrimSpace(response.Header.Get("X-Workbench-Response-Signature")))
	if err != nil || !hmac.Equal(received, mac.Sum(nil)) {
		return errors.New("ADP retention response signature is invalid")
	}
	return nil
}
