package hwdramaproxy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// signVolcengineAssetRequest implements the Ark header-signing protocol, not
// AWS SigV4 (the algorithm name and signing-key derivation are different).
func signVolcengineAssetRequest(req *http.Request, accessKey, secretKey string, now time.Time) error {
	if req == nil || req.URL == nil {
		return errors.New("request URL is required")
	}
	if req.Method != http.MethodPost {
		return errors.New("Volcengine asset actions require POST")
	}
	if strings.TrimSpace(accessKey) == "" || strings.TrimSpace(secretKey) == "" {
		return errors.New("Volcengine AK/SK credentials are required")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(req.Body, (4<<20)+1))
		_ = req.Body.Close()
		if err != nil || len(body) > 4<<20 {
			return errors.New("asset request body is unreadable or exceeds 4 MiB")
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	bodyHash := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(bodyHash[:])
	date := now.UTC().Format("20060102T150405Z")
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	if hostname, port, err := net.SplitHostPort(host); err == nil && (port == "80" || port == "443") {
		host = hostname
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	contentType := strings.TrimSpace(req.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Content-Sha256", payloadHash)
	req.Header.Del("X-Security-Token")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	query, err := parseVolcengineQuery(req.URL.RawQuery)
	if err != nil {
		return err
	}
	req.URL.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	const signedHeaders = "content-type;host;x-content-sha256;x-date"
	canonicalHeaders := "content-type:" + contentType + "\n" +
		"host:" + host + "\n" + "x-content-sha256:" + payloadHash + "\n" + "x-date:" + date + "\n"
	canonicalRequest := strings.Join([]string{req.Method, path, req.URL.RawQuery, canonicalHeaders, signedHeaders, payloadHash}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	scope := date[:8] + "/cn-beijing/ark/request"
	stringToSign := "HMAC-SHA256\n" + date + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	signingKey := []byte(secretKey)
	for _, value := range []string{date[:8], "cn-beijing", "ark", "request", stringToSign} {
		mac := hmac.New(sha256.New, signingKey)
		_, _ = mac.Write([]byte(value))
		signingKey = mac.Sum(nil)
	}
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+accessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+hex.EncodeToString(signingKey))
	return nil
}

func parseVolcengineQuery(raw string) (url.Values, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("invalid asset request query")
	}
	for key, values := range query {
		if (key != "Action" && key != "Version") || len(values) != 1 {
			return nil, errors.New("asset query only allows one Action and Version")
		}
	}
	if !isVolcengineAssetAction(query.Get("Action")) || query.Get("Version") != "2024-01-01" {
		return nil, errors.New("unsupported asset Action or Version")
	}
	return query, nil
}

func isVolcengineAssetAction(action string) bool {
	switch action {
	case "CreateAssetGroup", "ListAssetGroups", "GetAssetGroup", "UpdateAssetGroup", "DeleteAssetGroup",
		"CreateAsset", "ListAssets", "GetAsset", "UpdateAsset", "DeleteAsset",
		"CreateVisualValidateSession", "GetVisualValidateResult":
		return true
	default:
		return false
	}
}
