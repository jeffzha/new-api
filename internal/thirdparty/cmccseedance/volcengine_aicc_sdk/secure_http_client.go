package volcengine_aicc_sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/internal/thirdparty/cmccseedance/volcengine_aicc_sdk/jeddak_secure_channel"
)

const AICCSDKVersion = "0.1.0"

// AICCConfig AICC 安全通信配置
type AICCConfig struct {
	AK                 string
	SK                 string
	AICCLlMServiceName string
	PolicyID           string
	Region             string
	TopURL             string
	TopService         string
	RAURL              string
	AuthToken          string
}

type AICCConfigOption func(*AICCConfig)

func WithAICCConfigAK(ak string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.AK = ak
	}
}

func WithAICCConfigSK(sk string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.SK = sk
	}
}

func WithAICCConfigAICCLlMServiceName(name string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.AICCLlMServiceName = name
	}
}

func WithAICCConfigPolicyID(policyID string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.PolicyID = policyID
	}
}

func WithAICCConfigRegion(region string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.Region = region
	}
}

func WithAICCConfigTopURL(topURL string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.TopURL = topURL
	}
}

func WithAICCConfigTopService(topService string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.TopService = topService
	}
}

func WithAICCConfigRAURL(raURL string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.RAURL = raURL
	}
}

func WithAICCConfigAuthToken(authToken string) AICCConfigOption {
	return func(c *AICCConfig) {
		c.AuthToken = authToken
	}
}

// NewAICCConfig 创建 AICC 配置实例
func NewAICCConfig(ak, sk string) *AICCConfig {
	return &AICCConfig{
		AK:                 ak,
		SK:                 sk,
		AICCLlMServiceName: "AICC.ConfidentialInference",
		PolicyID:           "router_policy",
		Region:             "cn-beijing",
		TopURL:             "pcc.volcengineapi.com",
		TopService:         "pcc",
	}
}

// NewAICCConfigWithOpts 使用选项模式创建 AICC 配置实例
func NewAICCConfigWithOpts(opts ...AICCConfigOption) *AICCConfig {
	config := &AICCConfig{
		AICCLlMServiceName: "AICC.ConfidentialInference",
		PolicyID:           "router_policy",
		AK:                 "",
		SK:                 "",
		Region:             "cn-beijing",
		TopURL:             "pcc.volcengineapi.com",
		TopService:         "pcc",
	}

	for _, opt := range opts {
		opt(config)
	}

	// 处理 RAURL：如果提供了 ra_url，取域名部分并拼接上 HTTP_RA_PATH
	httpRAPath := "v1/security/token"
	if config.RAURL != "" {
		parsed, err := url.Parse(config.RAURL)
		if err == nil {
			baseDomain := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
			config.RAURL = baseDomain + "/" + httpRAPath
		}
	}

	return config
}

// SecureHTTPClient 安全 HTTP 客户端，用于与 AICC 服务进行加密通信
type SecureHTTPClient struct {
	Client     *http.Client
	AICCConfig *AICCConfig
	jscClient  *jeddak_secure_channel.Client
}

// NewSecureHTTPClient 创建安全 HTTP 客户端实例
func NewSecureHTTPClient(aiccConfig *AICCConfig) *SecureHTTPClient {
	client := &http.Client{Timeout: 120 * time.Second}
	secureHTTPClient := &SecureHTTPClient{
		Client:     client,
		AICCConfig: aiccConfig,
	}

	if aiccConfig != nil {
		trueVal := true
		var jscConfig jeddak_secure_channel.ClientConfig

		// 根据 AK/SK 是否为空来决定 RaType
		if aiccConfig.AK == "" || aiccConfig.SK == "" {
			// AK/SK 为空时，使用 LOCAL 接口
			jscConfig = jeddak_secure_channel.ClientConfig{
				RaType:           jeddak_secure_channel.RA_TYPE_LOCAL,
				RaURL:            aiccConfig.RAURL,
				RaAuthToken:      aiccConfig.AuthToken,
				RaServiceName:    aiccConfig.AICCLlMServiceName,
				RaPolicyId:       aiccConfig.PolicyID,
				RaKeyNegotiation: &trueVal,
				RaNeedToken:      &trueVal,
			}
		} else {
			// AK/SK 不为空时，使用 TCA 接口
			topInfo := map[string]string{
				"ak":      aiccConfig.AK,
				"sk":      aiccConfig.SK,
				"region":  aiccConfig.Region,
				"url":     aiccConfig.TopURL,
				"service": aiccConfig.TopService,
			}

			topInfoJSON, err := json.Marshal(topInfo)
			if err != nil {
				fmt.Printf("Error marshaling topInfo: %v\n", err)
				panic("New secure http client failed")
			}

			jscConfig = jeddak_secure_channel.ClientConfig{
				RaType:           jeddak_secure_channel.RA_TYPE_TCA,
				RaServiceName:    aiccConfig.AICCLlMServiceName,
				RaPolicyId:       aiccConfig.PolicyID,
				BytedanceTopInfo: string(topInfoJSON),
				RaKeyNegotiation: &trueVal,
				RaNeedToken:      &trueVal,
			}
		}

		secureHTTPClient.jscClient = jeddak_secure_channel.NewClient(jscConfig)
		err := secureHTTPClient.jscClient.AttestServer()
		if err != nil {
			fmt.Printf("Error attesting server: %v\n", err)
			panic("New secure http client failed, attest server error")
		}
	}

	return secureHTTPClient
}

// Do 发送 HTTP 请求
func (c *SecureHTTPClient) Do(req *http.Request) (*http.Response, error) {
	var encryptResult *jeddak_secure_channel.EncryptResult
	var reqBody []byte

	if c.AICCConfig != nil && c.jscClient != nil {
		req.Header.Set("X-AICC-Encryption-Enable", "true")
		req.Header.Set("X-AICC-Encryption-SDK", "aicc")
		req.Header.Set("X-AICC-Encryption-Version", AICCSDKVersion)

		if req.Body != nil {
			reqBody, _ = io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewBuffer(reqBody))
		}

		var err error
		encryptResult, err = c.jscClient.EncryptBytesWithResponse(reqBody)
		if err != nil {
			fmt.Printf("Encryption error: %v\n", err)
			return nil, err
		}
		if encryptResult == nil {
			fmt.Println("EncryptBytesWithResponse returned nil")
		}

		if encryptResult != nil && encryptResult.Ciphertext != "" {
			req.Body = io.NopCloser(bytes.NewBufferString(encryptResult.Ciphertext))
			req.Header.Set("Content-Type", "application/json")
			req.ContentLength = int64(len(encryptResult.Ciphertext))
			req.Header.Del("Content-Length")
		}
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}

	if encryptResult != nil {
		isStream := false
		if req.Header.Get("Content-Type") == "application/json" {
			var requestBody map[string]interface{}
			if err := json.Unmarshal(reqBody, &requestBody); err == nil {
				if stream, ok := requestBody["stream"].(bool); ok && stream {
					isStream = true
				}
			}
		}

		// 对于 2xx 状态码，正常解密
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if !isStream {
				content, err := io.ReadAll(resp.Body)
				if err != nil {
					return nil, err
				}
				decryptedContent, err := encryptResult.ResponseKey.DecryptBytesString(string(content))
				if err != nil {
					fmt.Printf("Decryption error: %v\n", err)
					resp.Body = io.NopCloser(bytes.NewBuffer(content))
				} else {
					resp.Body = io.NopCloser(bytes.NewBuffer(decryptedContent))
				}
			} else {
				resp.Body = &streamDecryptReader{
					original:   resp.Body,
					encryptKey: encryptResult.ResponseKey,
				}
			}
		} else {
			// 对于非 2xx 状态码（错误消息），存在加密和不加密两种情况
			// 尝试解密，如果解密失败则忽略错误，保持原始响应
			if !isStream {
				content, err := io.ReadAll(resp.Body)
				if err != nil {
					return nil, err
				}
				decryptedContent, err := encryptResult.ResponseKey.DecryptBytesString(string(content))
				if err != nil {
					// 解密失败，保持原始响应
					resp.Body = io.NopCloser(bytes.NewBuffer(content))
				} else {
					resp.Body = io.NopCloser(bytes.NewBuffer(decryptedContent))
				}
			} else {
				resp.Body = &streamDecryptReader{
					original:   resp.Body,
					encryptKey: encryptResult.ResponseKey,
				}
			}
		}
	}

	return resp, nil
}

// streamDecryptReader implements streaming decryption for SSE responses.
// It mimics Python's DemoSyncByteStream implementation exactly.
type streamDecryptReader struct {
	original   io.ReadCloser
	encryptKey jeddak_secure_channel.ResponseKey
	decrypted  []byte // Buffer for decrypted data ready to be returned
	rawBuffer  []byte // Buffer for raw data from original stream
}

// decryptLine attempts to decrypt a line, mimicking Python's behavior.
// Python's decrypt() handles both encrypted and non-encrypted data gracefully.
func (r *streamDecryptReader) decryptLine(line []byte) []byte {
	if len(line) == 0 {
		return []byte{}
	}

	lineStr := string(line)

	hasNonce := bytes.Contains(line, []byte(`"nonce"`))
	hasMac := bytes.Contains(line, []byte(`"mac"`))
	hasCiphertext := bytes.Contains(line, []byte(`"ciphertext"`))
	hasEmptyCiphertext := bytes.Contains(line, []byte(`"ciphertext":""`))

	if !hasNonce || !hasMac || !hasCiphertext {
		return line
	}

	if hasEmptyCiphertext {
		return []byte{}
	}

	decrypted, err := r.encryptKey.DecryptBytesString(lineStr)
	if err != nil {
		return []byte{}
	}
	return decrypted
}

// Read reads and decrypts the response body line by line.
// This implementation exactly mimics Python's DemoSyncByteStream.__iter__ logic:
// 1. Accumulate data until a line ending is found
// 2. Strip the line ending, decrypt the line
// 3. Add back the appropriate line ending (\n for LF, nothing for CR)
func (r *streamDecryptReader) Read(p []byte) (n int, err error) {
	for {
		// Return decrypted data if available
		if len(r.decrypted) > 0 {
			n = copy(p, r.decrypted)
			r.decrypted = r.decrypted[n:]
			return n, nil
		}

		// Process raw buffer to find and decrypt lines
		for len(r.rawBuffer) > 0 {
			idx := bytes.IndexAny(r.rawBuffer, "\r\n")
			if idx != -1 {
				line := r.rawBuffer[:idx]
				separator := r.rawBuffer[idx]
				r.rawBuffer = r.rawBuffer[idx+1:]

				// Handle CRLF
				if separator == '\r' && len(r.rawBuffer) > 0 && r.rawBuffer[0] == '\n' {
					r.rawBuffer = r.rawBuffer[1:]
				}

				// Decrypt the line
				decryptedLine := r.decryptLine(line)

				// Add back line ending if present
				if separator == '\n' {
					decryptedLine = append(decryptedLine, '\n')
				}

				// If decrypted line is not empty, store it and return
				if len(decryptedLine) > 0 {
					r.decrypted = decryptedLine
					break
				}
				// If decrypted line is empty, continue to next line
				continue
			}

			// No complete line found in raw buffer, need more data
			break
		}

		// If we have decrypted data ready, return it
		if len(r.decrypted) > 0 {
			continue
		}

		// Read more data from original stream
		buf := make([]byte, 4096)
		readN, readErr := r.original.Read(buf)
		if readN > 0 {
			r.rawBuffer = append(r.rawBuffer, buf[:readN]...)
		}

		if readErr != nil {
			if readErr == io.EOF {
				// Process any remaining data in raw buffer
				if len(r.rawBuffer) > 0 {
					decryptedLine := r.decryptLine(r.rawBuffer)
					r.rawBuffer = nil
					if len(decryptedLine) > 0 {
						r.decrypted = decryptedLine
						continue
					}
				}
				return 0, io.EOF
			}
			return 0, readErr
		}
	}
}

// Close closes the response body
func (r *streamDecryptReader) Close() error {
	return r.original.Close()
}
