package hwdramaproxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	mobileCloudSignatureMethod  = "HmacSHA256"
	mobileCloudSignatureVersion = "V2.0"
	mobileCloudSecretKeyPrefix  = "BC_SIGNATURE&"
)

var mobileCloudSignatureParams = map[string]bool{
	"AccessKey":        true,
	"Timestamp":        true,
	"SignatureMethod":  true,
	"SignatureVersion": true,
	"SignatureNonce":   true,
	"Signature":        true,
}

// signMobileCloudRequest mirrors ecloudsdkcore v1.0.6 AKSKCredential.Sign while
// keeping the proxy's context-aware HTTP client and TLS verification.
func signMobileCloudRequest(req *http.Request, accessKey string, secretKey string, now time.Time, nonce string) error {
	if req == nil || req.URL == nil {
		return errors.New("request URL is required")
	}
	accessKey = strings.TrimSpace(accessKey)
	secretKey = strings.TrimSpace(secretKey)
	nonce = strings.TrimSpace(nonce)
	if accessKey == "" || secretKey == "" {
		return errors.New("mobile cloud AK/SK credentials are required")
	}
	if nonce == "" {
		return errors.New("signature nonce is required")
	}

	params := make(map[string]string)
	for key, values := range req.URL.Query() {
		if mobileCloudSignatureParams[key] {
			continue
		}
		if len(values) > 1 {
			return fmt.Errorf("query parameter %q must not be repeated", key)
		}
		if len(values) == 1 {
			params[key] = values[0]
		}
	}
	params["AccessKey"] = accessKey
	params["Timestamp"] = now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02T15:04:05Z")
	params["SignatureMethod"] = mobileCloudSignatureMethod
	params["SignatureVersion"] = mobileCloudSignatureVersion
	params["SignatureNonce"] = nonce

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	canonicalParts := make([]string, 0, len(keys))
	for _, key := range keys {
		canonicalParts = append(canonicalParts, mobileCloudPercentEncode(key)+"="+mobileCloudPercentEncode(params[key]))
	}
	canonicalQuery := strings.Join(canonicalParts, "&")
	canonicalQueryHash := sha256.Sum256([]byte(canonicalQuery))

	unescapedPath, err := url.QueryUnescape(req.URL.EscapedPath())
	if err != nil {
		return fmt.Errorf("unescape request path: %w", err)
	}
	stringToSign := strings.ToUpper(req.Method) + "\n" +
		mobileCloudPercentEncode(unescapedPath) + "\n" +
		hex.EncodeToString(canonicalQueryHash[:])
	mac := hmac.New(sha256.New, []byte(mobileCloudSecretKeyPrefix+secretKey))
	_, _ = mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	req.URL.RawQuery = canonicalQuery + "&Signature=" + mobileCloudPercentEncode(signature)
	return nil
}

func newMobileCloudNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func mobileCloudPercentEncode(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}
