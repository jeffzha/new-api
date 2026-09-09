package billingimport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestTencentClientUsesFixedHTTPSAndOfficialTC3Signature(t *testing.T) {
	fixed := time.Unix(1700000000, 0).UTC()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.Equal(t, "https", request.URL.Scheme)
		assert.Equal(t, tencentBillingHost, request.URL.Host)
		assert.Equal(t, "/", request.URL.Path)
		assert.Equal(t, tencentBillingHost, request.Host)
		assert.Equal(t, "DescribeBillDetail", request.Header.Get("X-TC-Action"))
		assert.Equal(t, tencentBillingVersion, request.Header.Get("X-TC-Version"))
		assert.Equal(t, "1700000000", request.Header.Get("X-TC-Timestamp"))
		assert.Equal(t, tencentContentType, request.Header.Get("Content-Type"))
		assert.Equal(t, `{"BusinessCode":"p_adp","Limit":1,"Month":"2026-08","NeedRecordNum":1,"Offset":0,"PayerUin":"10001"}`, string(body))
		assert.Equal(t,
			"TC3-HMAC-SHA256 Credential=AKIDEXAMPLE/2023-11-14/billing/tc3_request, SignedHeaders=content-type;host, Signature=b6d4406fe4fcae26e1ec92be83e27a62fd80675c82b53f42d73232486ce5a47a",
			request.Header.Get("Authorization"),
		)
		response := `{"Response":{"DetailSet":[],"Total":0,"Context":"","RequestId":"query-request-1"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	client, err := newTencentClient(
		"AKIDEXAMPLE", "SECRETKEYEXAMPLE", "10001", &http.Client{Transport: transport},
		4096, func() time.Time { return fixed }, 0,
	)
	require.NoError(t, err)
	page, err := client.DescribeBillDetail(context.Background(), DetailRequest{Month: "2026-08", BusinessCode: "p_adp", Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, "query-request-1", page.RequestID)
}

func TestTencentClientBoundsResponsesAndNeverReturnsProviderMessage(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := `{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"message containing sensitive provider diagnostics"},"RequestId":"query-request-error"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	client, err := newTencentClient("AKIDEXAMPLE", "SECRETKEYEXAMPLE", "10001", &http.Client{Transport: transport}, 4096, time.Now, 0)
	require.NoError(t, err)
	_, err = client.DescribeBillAdjustInfo(context.Background(), "2026-08")
	var providerError *ProviderError
	require.ErrorAs(t, err, &providerError)
	assert.Equal(t, "AuthFailure.SecretIdNotFound", providerError.Code)
	assert.NotContains(t, err.Error(), "sensitive")

	oversized := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 4097))), Header: make(http.Header)}, nil
	})
	client, err = newTencentClient("AKIDEXAMPLE", "SECRETKEYEXAMPLE", "10001", &http.Client{Transport: oversized}, 4096, time.Now, 0)
	require.NoError(t, err)
	_, err = client.DescribeBillAdjustInfo(context.Background(), "2026-08")
	require.ErrorAs(t, err, &providerError)
	assert.Equal(t, "response_too_large", providerError.Code)
}
