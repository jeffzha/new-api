package providerverify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
)

const (
	tencentADPEndpoint       = "https://adp.tencentcloudapi.com"
	tencentADPService        = "adp"
	tencentADPVersion        = "2026-05-20"
	maximumProviderBodyBytes = 2 << 20
)

type Target struct {
	ProviderEnvironment string
	Region              string
	SpaceID             string
	AppID               string
	TemplateAgentID     string
	AppKey              string
	SecretID            string
	SecretKey           string
}

type Result struct {
	Result                string
	AppMode               int
	DynamicAgentConfig    bool
	ReleaseStatus         string
	TemplateAgentStatus   string
	DisplayName           string
	Description           string
	AvatarURL             string
	ProviderRequestIDs    []string
	SanitizedResponseHash string
	ErrorCode             string
	ErrorMessage          string
}

type Verifier interface {
	Verify(ctx context.Context, target Target) (Result, error)
}

type TencentVerifier struct {
	endpoint *url.URL
	client   *http.Client
	now      func() time.Time
}

func NewTencentVerifier(timeout time.Duration) (*TencentVerifier, error) {
	return newTencentVerifier(tencentADPEndpoint, timeout, time.Now)
}

func newTencentVerifier(endpoint string, timeout time.Duration, now func() time.Time) (*TencentVerifier, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Tencent ADP verification endpoint")
	}
	isOfficial := parsed.Scheme == "https" && parsed.Host == "adp.tencentcloudapi.com"
	isLoopbackTest := parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost")
	if !isOfficial && !isLoopbackTest {
		return nil, fmt.Errorf("Tencent ADP verification endpoint must be the official HTTPS host")
	}
	if timeout <= 0 || now == nil {
		return nil, fmt.Errorf("invalid Tencent ADP verifier timeout or clock")
	}
	return &TencentVerifier{
		endpoint: parsed,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: now,
	}, nil
}

func (v *TencentVerifier) Verify(ctx context.Context, target Target) (Result, error) {
	if target.ProviderEnvironment != model.ProviderChinaTencentCloud && target.ProviderEnvironment != model.ProviderChinaTencentADP {
		return Result{}, fmt.Errorf("unsupported provider environment")
	}
	if strings.TrimSpace(target.Region) == "" || strings.TrimSpace(target.SpaceID) == "" || strings.TrimSpace(target.AppID) == "" ||
		target.AppKey == "" || target.SecretID == "" || target.SecretKey == "" {
		return Result{}, fmt.Errorf("incomplete Tencent ADP verification target")
	}

	appRequest := struct {
		AppID     string `json:"AppId"`
		Domain    int    `json:"Domain"`
		FieldMask struct {
			Paths []string `json:"Paths"`
		} `json:"FieldMask"`
	}{AppID: target.AppID, Domain: 2}
	appRequest.FieldMask.Paths = []string{"Metadata", "Status", "AppConfig", "SecretInfo"}
	var appResponse struct {
		Response struct {
			App struct {
				Metadata struct {
					AppID       string `json:"AppId"`
					AppMode     int    `json:"AppMode"`
					SpaceID     string `json:"SpaceId"`
					Name        string `json:"Name"`
					Description string `json:"Description"`
					AvatarURL   string `json:"Avatar"`
				} `json:"Metadata"`
				Config struct {
					Mode struct {
						ClawAgentConfig *struct {
							CustomConfig *struct {
								Enabled bool `json:"Enabled"`
							} `json:"CustomConfig"`
						} `json:"ClawAgentConfig"`
					} `json:"Mode"`
				} `json:"Config"`
				SecretInfo struct {
					AppKey string `json:"AppKey"`
				} `json:"SecretInfo"`
				Status struct {
					Status int `json:"Status"`
				} `json:"Status"`
			} `json:"App"`
			RequestID string `json:"RequestId"`
			Error     *struct {
				Code string `json:"Code"`
			} `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := v.call(ctx, "DescribeApp", target.Region, target.SecretID, target.SecretKey, appRequest, &appResponse); err != nil {
		return Result{}, err
	}
	if appResponse.Response.Error != nil {
		return Result{}, fmt.Errorf("Tencent ADP DescribeApp failed with code %s", safeProviderCode(appResponse.Response.Error.Code))
	}
	if appResponse.Response.RequestID == "" {
		return Result{}, fmt.Errorf("Tencent ADP DescribeApp response is missing RequestId")
	}

	appMode := appResponse.Response.App.Metadata.AppMode
	dynamicAgentConfig := appMode == 4 && appResponse.Response.App.Config.Mode.ClawAgentConfig != nil &&
		appResponse.Response.App.Config.Mode.ClawAgentConfig.CustomConfig != nil &&
		appResponse.Response.App.Config.Mode.ClawAgentConfig.CustomConfig.Enabled
	templateAgentStatus := "not_required"
	providerRequestIDs := []string{appResponse.Response.RequestID}
	if dynamicAgentConfig && strings.TrimSpace(target.TemplateAgentID) == "" {
		templateAgentStatus = "missing"
	}
	if dynamicAgentConfig && strings.TrimSpace(target.TemplateAgentID) != "" {
		agentRequest := struct {
			AppID   string `json:"AppId"`
			AgentID string `json:"AgentId"`
		}{AppID: target.AppID, AgentID: target.TemplateAgentID}
		var agentResponse struct {
			Response struct {
				Agent struct {
					AgentID string `json:"AgentId"`
				} `json:"Agent"`
				RequestID string `json:"RequestId"`
				Error     *struct {
					Code string `json:"Code"`
				} `json:"Error,omitempty"`
			} `json:"Response"`
		}
		if err := v.call(ctx, "DescribeAgentDetail", target.Region, target.SecretID, target.SecretKey, agentRequest, &agentResponse); err != nil {
			return Result{}, err
		}
		if agentResponse.Response.Error != nil {
			return Result{}, fmt.Errorf("Tencent ADP DescribeAgentDetail failed with code %s", safeProviderCode(agentResponse.Response.Error.Code))
		}
		if agentResponse.Response.RequestID == "" {
			return Result{}, fmt.Errorf("Tencent ADP DescribeAgentDetail response is missing RequestId")
		}
		providerRequestIDs = append(providerRequestIDs, agentResponse.Response.RequestID)
		templateAgentStatus = "unavailable"
		if agentResponse.Response.Agent.AgentID == target.TemplateAgentID {
			templateAgentStatus = "available"
		}
	}

	appIDMatches := appResponse.Response.App.Metadata.AppID == target.AppID
	spaceMatches := appResponse.Response.App.Metadata.SpaceID == target.SpaceID
	appKeyMatches := subtle.ConstantTimeCompare([]byte(appResponse.Response.App.SecretInfo.AppKey), []byte(target.AppKey)) == 1
	releaseStatus := "not_published"
	if appResponse.Response.App.Status.Status == 2 {
		releaseStatus = "published"
	}
	summary := struct {
		AppIDMatches        bool   `json:"app_id_matches"`
		AppKeyMatches       bool   `json:"app_key_matches"`
		AppMode             int    `json:"app_mode"`
		ReleaseStatus       string `json:"release_status"`
		SpaceMatches        bool   `json:"space_matches"`
		TemplateAgentStatus string `json:"template_agent_status"`
		DynamicAgentConfig  bool   `json:"dynamic_agent_config"`
	}{appIDMatches, appKeyMatches, appMode, releaseStatus, spaceMatches, templateAgentStatus, dynamicAgentConfig}
	summaryBytes, err := jsonx.Marshal(summary)
	if err != nil {
		return Result{}, err
	}
	summaryHash := sha256.Sum256(summaryBytes)
	result := Result{
		Result: "verified", AppMode: appMode, ReleaseStatus: releaseStatus,
		DynamicAgentConfig:    dynamicAgentConfig,
		TemplateAgentStatus:   templateAgentStatus,
		DisplayName:           appResponse.Response.App.Metadata.Name,
		Description:           appResponse.Response.App.Metadata.Description,
		AvatarURL:             appResponse.Response.App.Metadata.AvatarURL,
		ProviderRequestIDs:    providerRequestIDs,
		SanitizedResponseHash: "sha256:" + hex.EncodeToString(summaryHash[:]),
	}
	validMode := appMode >= 1 && appMode <= 4
	templateValid := !dynamicAgentConfig || templateAgentStatus == "available"
	if !appIDMatches || !spaceMatches || !appKeyMatches || !validMode || releaseStatus != "published" || !templateValid {
		result.Result = "invalid"
		result.ErrorCode = verificationMismatchCode(appIDMatches, spaceMatches, appKeyMatches, appMode, releaseStatus, templateAgentStatus, dynamicAgentConfig)
		result.ErrorMessage = "Tencent ADP resources do not match the pending workbench configuration"
	}
	return result, nil
}

func (v *TencentVerifier) call(ctx context.Context, action, region, secretID, secretKey string, input, output any) error {
	body, err := jsonx.Marshal(input)
	if err != nil {
		return err
	}
	now := v.now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	date := now.Format("2006-01-02")
	host := v.endpoint.Host
	contentType := "application/json; charset=utf-8"
	canonicalHeaders := "content-type:" + contentType + "\n" + "host:" + host + "\n" + "x-tc-action:" + strings.ToLower(action) + "\n"
	signedHeaders := "content-type;host;x-tc-action"
	bodyHash := sha256.Sum256(body)
	canonicalRequest := strings.Join([]string{
		http.MethodPost, "/", "", canonicalHeaders, signedHeaders, hex.EncodeToString(bodyHash[:]),
	}, "\n")
	scope := date + "/" + tencentADPService + "/tc3_request"
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{"TC3-HMAC-SHA256", timestamp, scope, hex.EncodeToString(requestHash[:])}, "\n")
	secretDate := hmacSHA256([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256(secretDate, tencentADPService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
	authorization := "TC3-HMAC-SHA256 Credential=" + secretID + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint.String()+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Host", host)
	request.Header.Set("X-TC-Action", action)
	request.Header.Set("X-TC-Region", region)
	request.Header.Set("X-TC-Timestamp", timestamp)
	request.Header.Set("X-TC-Version", tencentADPVersion)
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("call Tencent ADP %s: %w", action, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximumProviderBodyBytes+1))
	if err != nil || len(responseBody) > maximumProviderBodyBytes {
		return fmt.Errorf("read Tencent ADP %s response", action)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Tencent ADP %s returned HTTP %d", action, response.StatusCode)
	}
	if err := jsonx.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode Tencent ADP %s response: %w", action, err)
	}
	return nil
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func verificationMismatchCode(appIDMatches, spaceMatches, appKeyMatches bool, appMode int, releaseStatus, templateStatus string, dynamicAgentConfig bool) string {
	switch {
	case !appIDMatches:
		return "app_id_mismatch"
	case !spaceMatches:
		return "space_id_mismatch"
	case !appKeyMatches:
		return "app_key_mismatch"
	case appMode < 1 || appMode > 4:
		return "app_mode_mismatch"
	case releaseStatus != "published":
		return "app_not_published"
	case dynamicAgentConfig && templateStatus != "available":
		return "template_agent_mismatch"
	default:
		return "provider_mismatch"
	}
}

func safeProviderCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return "unknown"
	}
	for _, character := range value {
		if !(character == '.' || character == '_' || character == '-' || character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') {
			return "unknown"
		}
	}
	return value
}
