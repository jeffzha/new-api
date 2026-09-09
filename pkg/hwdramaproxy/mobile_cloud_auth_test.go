package hwdramaproxy

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignMobileCloudRequestMatchesOfficialSDKV2Algorithm(t *testing.T) {
	values := url.Values{}
	values.Set("pageNo", "1")
	values.Set("keyword", "a b/+~*")
	req, err := http.NewRequest(
		http.MethodPost,
		"https://ecloud.10086.cn/api/openapi-maas/exp/aicc/v2/asset/query?"+values.Encode(),
		nil,
	)
	require.NoError(t, err)

	err = signMobileCloudRequest(
		req,
		"AKIDEXAMPLE",
		"SECRETEXAMPLE",
		time.Date(2026, time.July, 23, 18, 30, 45, 0, time.UTC),
		"0123456789abcdef0123456789abcdef",
	)

	require.NoError(t, err)
	assert.Equal(t,
		"AccessKey=AKIDEXAMPLE&SignatureMethod=HmacSHA256&SignatureNonce=0123456789abcdef0123456789abcdef&"+
			"SignatureVersion=V2.0&Timestamp=2026-07-24T02%3A30%3A45Z&keyword=a%20b%2F%2B~%2A&pageNo=1&"+
			"Signature=9b2c755b10ce1b68352bb9a956a66e586869efa62cac5bfb8ce22ffd7a7d95fe",
		req.URL.RawQuery,
	)
}

func TestSignMobileCloudRequestReplacesClientSuppliedSignatureParameters(t *testing.T) {
	req, err := http.NewRequest(
		http.MethodGet,
		"https://ecloud.10086.cn/api/openapi-maas/exp/aicc/v2/asset/asset-1?AccessKey=attacker&Signature=forged",
		nil,
	)
	require.NoError(t, err)

	err = signMobileCloudRequest(
		req,
		"trusted-access-key",
		"trusted-secret-key",
		time.Date(2026, time.July, 24, 0, 0, 0, 0, time.UTC),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)

	require.NoError(t, err)
	query := req.URL.Query()
	assert.Equal(t, "trusted-access-key", query.Get("AccessKey"))
	assert.NotEqual(t, "forged", query.Get("Signature"))
	assert.Equal(t, mobileCloudSignatureMethod, query.Get("SignatureMethod"))
}

func TestSignMobileCloudRequestRejectsRepeatedBusinessQueryParameter(t *testing.T) {
	req, err := http.NewRequest(
		http.MethodPost,
		"https://ecloud.10086.cn/api/openapi-maas/exp/aicc/v2/asset/query?pageNo=1&pageNo=2",
		nil,
	)
	require.NoError(t, err)

	err = signMobileCloudRequest(
		req,
		"access-key",
		"secret-key",
		time.Now(),
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be repeated")
}

func TestNewMobileCloudNonceIsSDKCompatible(t *testing.T) {
	first, err := newMobileCloudNonce()
	require.NoError(t, err)
	second, err := newMobileCloudNonce()
	require.NoError(t, err)

	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), first)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), second)
	assert.NotEqual(t, first, second)
}
