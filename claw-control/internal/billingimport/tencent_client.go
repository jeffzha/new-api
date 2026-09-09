package billingimport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
)

const (
	tencentBillingEndpoint = "https://billing.tencentcloudapi.com/"
	tencentBillingHost     = "billing.tencentcloudapi.com"
	tencentBillingVersion  = "2018-07-09"
	tencentBillingService  = "billing"
	tencentContentType     = "application/json; charset=utf-8"
	tencentCallInterval    = 200 * time.Millisecond
)

type Client interface {
	DescribeBillDetail(context.Context, DetailRequest) (*DetailPage, error)
	DescribeBillAdjustInfo(context.Context, string) (*AdjustmentResult, error)
}

type DetailRequest struct {
	Month        string
	BusinessCode string
	Offset       int
	Limit        int
	Context      string
}

type BillComponent struct {
	RealCost string `json:"RealCost"`
}

type BillDetail struct {
	BillID       string          `json:"BillId"`
	BusinessCode string          `json:"BusinessCode"`
	ProductCode  string          `json:"ProductCode"`
	ResourceID   string          `json:"ResourceId"`
	Components   []BillComponent `json:"ComponentSet"`
}

type DetailPage struct {
	Details   []BillDetail
	Total     int
	Context   string
	RequestID string
	Raw       json.RawMessage
}

type AdjustmentResult struct {
	Total     int
	Count     int
	RequestID string
	Raw       json.RawMessage
}

type ProviderError struct {
	Code      string
	RequestID string
	Action    string
}

func (e *ProviderError) Error() string { return "Tencent Billing request failed: " + e.Code }

type TencentClient struct {
	secretID   string
	secretKey  string
	payerUIN   string
	httpClient *http.Client
	maxBytes   int64
	clock      func() time.Time
	interval   time.Duration
	mu         sync.Mutex
	lastCall   time.Time
}

func NewTencentClient(secretID, secretKey, payerUIN string, timeout time.Duration, maxBytes int64) (*TencentClient, error) {
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("Tencent Billing timeout must be positive and at most 30 seconds")
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Tencent Billing redirects are forbidden")
		},
	}
	return newTencentClient(secretID, secretKey, payerUIN, client, maxBytes, time.Now, tencentCallInterval)
}

func newTencentClient(secretID, secretKey, payerUIN string, httpClient *http.Client, maxBytes int64, clock func() time.Time, interval time.Duration) (*TencentClient, error) {
	secretID = strings.TrimSpace(secretID)
	secretKey = strings.TrimSpace(secretKey)
	payerUIN = strings.TrimSpace(payerUIN)
	if secretID == "" || secretKey == "" || payerUIN == "" || httpClient == nil || clock == nil {
		return nil, errors.New("Tencent Billing credentials, payer UIN, HTTP client, and clock are required")
	}
	if maxBytes < 1024 || maxBytes > 16<<20 || interval < 0 {
		return nil, errors.New("Tencent Billing response limit or rate interval is invalid")
	}
	return &TencentClient{
		secretID: secretID, secretKey: secretKey, payerUIN: payerUIN, httpClient: httpClient,
		maxBytes: maxBytes, clock: clock, interval: interval,
	}, nil
}

func (c *TencentClient) DescribeBillDetail(ctx context.Context, input DetailRequest) (*DetailPage, error) {
	payload := map[string]any{
		"Offset": input.Offset, "Limit": input.Limit, "Month": input.Month,
		"NeedRecordNum": 1, "BusinessCode": input.BusinessCode, "PayerUin": c.payerUIN,
	}
	if input.Context != "" {
		payload["Context"] = input.Context
	}
	raw, err := c.call(ctx, "DescribeBillDetail", payload)
	if err != nil {
		setProviderErrorAction(err, "DescribeBillDetail")
		return nil, err
	}
	var envelope struct {
		Response struct {
			DetailSet []BillDetail `json:"DetailSet"`
			Total     int          `json:"Total"`
			Context   string       `json:"Context"`
			RequestID string       `json:"RequestId"`
			Error     *struct {
				Code string `json:"Code"`
			} `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := jsonx.Unmarshal(raw, &envelope); err != nil {
		return nil, &ProviderError{Code: "invalid_response"}
	}
	if envelope.Response.Error != nil {
		return nil, &ProviderError{Code: normalizeProviderCode(envelope.Response.Error.Code), RequestID: envelope.Response.RequestID, Action: "DescribeBillDetail"}
	}
	if envelope.Response.RequestID == "" || envelope.Response.Total < 0 {
		return nil, &ProviderError{Code: "invalid_response"}
	}
	return &DetailPage{
		Details: envelope.Response.DetailSet, Total: envelope.Response.Total, Context: envelope.Response.Context,
		RequestID: envelope.Response.RequestID, Raw: append(json.RawMessage(nil), raw...),
	}, nil
}

func (c *TencentClient) DescribeBillAdjustInfo(ctx context.Context, month string) (*AdjustmentResult, error) {
	raw, err := c.call(ctx, "DescribeBillAdjustInfo", map[string]any{"Month": month, "PayerUin": c.payerUIN})
	if err != nil {
		setProviderErrorAction(err, "DescribeBillAdjustInfo")
		return nil, err
	}
	var envelope struct {
		Response struct {
			Total     int               `json:"Total"`
			Data      []json.RawMessage `json:"Data"`
			RequestID string            `json:"RequestId"`
			Error     *struct {
				Code string `json:"Code"`
			} `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := jsonx.Unmarshal(raw, &envelope); err != nil {
		return nil, &ProviderError{Code: "invalid_response"}
	}
	if envelope.Response.Error != nil {
		return nil, &ProviderError{Code: normalizeProviderCode(envelope.Response.Error.Code), RequestID: envelope.Response.RequestID, Action: "DescribeBillAdjustInfo"}
	}
	if envelope.Response.RequestID == "" || envelope.Response.Total < 0 {
		return nil, &ProviderError{Code: "invalid_response"}
	}
	return &AdjustmentResult{
		Total: envelope.Response.Total, Count: len(envelope.Response.Data), RequestID: envelope.Response.RequestID,
		Raw: append(json.RawMessage(nil), raw...),
	}, nil
}

func (c *TencentClient) call(ctx context.Context, action string, payload any) ([]byte, error) {
	body, err := jsonx.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	now := c.clock().UTC()
	timestamp := now.Unix()
	date := now.Format("2006-01-02")
	payloadHash := hashHex(body)
	canonicalHeaders := "content-type:" + tencentContentType + "\nhost:" + tencentBillingHost + "\n"
	canonicalRequest := strings.Join([]string{"POST", "/", "", canonicalHeaders, "content-type;host", payloadHash}, "\n")
	scope := date + "/" + tencentBillingService + "/tc3_request"
	stringToSign := strings.Join([]string{"TC3-HMAC-SHA256", strconv.FormatInt(timestamp, 10), scope, hashHex([]byte(canonicalRequest))}, "\n")
	secretDate := hmacSHA256([]byte("TC3"+c.secretKey), date)
	secretService := hmacSHA256(secretDate, tencentBillingService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
	authorization := "TC3-HMAC-SHA256 Credential=" + c.secretID + "/" + scope +
		", SignedHeaders=content-type;host, Signature=" + signature

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tencentBillingEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Host = tencentBillingHost
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", tencentContentType)
	request.Header.Set("Host", tencentBillingHost)
	request.Header.Set("X-TC-Action", action)
	request.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("X-TC-Version", tencentBillingVersion)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, &ProviderError{Code: "transport_error"}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil {
		return nil, &ProviderError{Code: "response_read_error"}
	}
	if int64(len(responseBody)) > c.maxBytes {
		return nil, &ProviderError{Code: "response_too_large"}
	}
	if response.StatusCode != http.StatusOK {
		return nil, &ProviderError{Code: fmt.Sprintf("http_status_%d", response.StatusCode)}
	}
	return responseBody, nil
}

func (c *TencentClient) wait(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastCall.IsZero() {
		wait := c.interval - c.clock().Sub(c.lastCall)
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	c.lastCall = c.clock()
	return nil
}

func hashHex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func setProviderErrorAction(err error, action string) {
	var providerError *ProviderError
	if errors.As(err, &providerError) && providerError.Action == "" {
		providerError.Action = action
	}
}

func normalizeProviderCode(code string) string {
	code = strings.TrimSpace(code)
	if safeErrorPattern.MatchString(code) {
		return code
	}
	return "invalid_provider_error_code"
}
