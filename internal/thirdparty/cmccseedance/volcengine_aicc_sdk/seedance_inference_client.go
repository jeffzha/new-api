package volcengine_aicc_sdk

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/thirdparty/cmccseedance/volcengine_aicc_sdk/common"
	"github.com/QuantumNous/new-api/internal/thirdparty/cmccseedance/volcengine_aicc_sdk/common/log"
)

type SeedanceClient struct {
	BaseURL                  string
	APIKey                   string
	Endpoint                 string
	AICCConfig               *AICCConfig
	SecureHTTPClient         *SecureHTTPClient
	TOSUtils                 *common.TOSUtils
	IsSecure                 bool
	IsEnableVideoEncrypt     bool
	VideoFileStorageLocation string
	VideoFileEncryptPubKey   string
	VideoFileEncryptPrivKey  string
	RSAPrivateKey            *rsa.PrivateKey
	Timeout                  float64

	CreateTaskURL    string
	QueryTaskURL     string
	QueryTaskListURL string
	DeleteTaskURL    string
}

type SeedanceClientOption func(*SeedanceClient)

func WithSeedanceBaseURL(baseURL string) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.BaseURL = baseURL
	}
}

func WithSeedanceAPIKey(apiKey string) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.APIKey = apiKey
	}
}

func WithSeedanceEndpoint(endpoint string) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.Endpoint = endpoint
	}
}

func WithSeedanceAICCConfig(aiccConfig *AICCConfig) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.AICCConfig = aiccConfig
	}
}

func WithSeedanceIsSecure(isSecure bool) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.IsSecure = isSecure
	}
}

func WithSeedanceIsEnableVideoEncrypt(enable bool) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.IsEnableVideoEncrypt = enable
	}
}

func WithSeedanceVideoFileStorageLocation(location string) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.VideoFileStorageLocation = strings.ToUpper(location)
	}
}

func WithSeedanceTimeout(timeout float64) SeedanceClientOption {
	return func(c *SeedanceClient) {
		c.Timeout = timeout
	}
}

func NewSeedanceClient(opts ...SeedanceClientOption) *SeedanceClient {
	client := &SeedanceClient{
		IsSecure:                 true,
		IsEnableVideoEncrypt:     true,
		VideoFileStorageLocation: "TOS",
		Timeout:                  120.0,
		CreateTaskURL:            "/contents/generations/tasks",
		QueryTaskURL:             "/contents/generations/tasks",
		QueryTaskListURL:         "/contents/generations/tasks",
		DeleteTaskURL:            "/contents/generations/tasks",
	}

	for _, opt := range opts {
		opt(client)
	}

	client.TOSUtils = common.NewTOSUtils("")

	// 初始化 SecureHTTPClient
	if client.AICCConfig != nil {
		client.SecureHTTPClient = NewSecureHTTPClient(client.AICCConfig)
	} else {
		if client.IsSecure {
			aiccConfig := NewAICCConfigWithOpts(
				WithAICCConfigRAURL(client.BaseURL),
				WithAICCConfigAuthToken(client.APIKey),
			)
			client.SecureHTTPClient = NewSecureHTTPClient(aiccConfig)
		} else {
			client.SecureHTTPClient = NewSecureHTTPClient(nil)
		}
	}

	return client
}

func (c *SeedanceClient) getBaseURL() string {
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *SeedanceClient) SetVideoFileEncryptKey(pubKeyPath, privKeyPath string) error {
	if pubKeyPath != "" {
		data, err := os.ReadFile(pubKeyPath)
		if err == nil {
			c.VideoFileEncryptPubKey = string(data)
		}
	}

	if privKeyPath != "" {
		data, err := os.ReadFile(privKeyPath)
		if err == nil {
			c.VideoFileEncryptPrivKey = string(data)
			block, _ := pem.Decode(data)
			if block != nil {
				key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
				if err != nil {
					return fmt.Errorf("failed to parse private key: %v", err)
				}
				rsaKey, ok := key.(*rsa.PrivateKey)
				if !ok {
					return fmt.Errorf("failed to parse private key: not an RSA key")
				}
				c.RSAPrivateKey = rsaKey
			}
		}
	}

	return nil
}

func (c *SeedanceClient) GenerateVideoFileEncryptKey(pubKeyPath, privKeyPath string) error {
	if pubKeyPath == "" || privKeyPath == "" {
		return fmt.Errorf("pub_key_path and priv_key_path must be provided")
	}

	return c._generateKeyPair(pubKeyPath, privKeyPath)
}

func (c *SeedanceClient) _generateKeyPair(pubKeyPath, privKeyPath string) error {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("failed to generate RSA key pair: %w", err)
	}

	// Use PKCS#8 format for private key (same as Python's PrivateFormat.PKCS8)
	privKeyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %v", err)
	}
	privKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privKeyBytes,
	})
	if err := os.WriteFile(privKeyPath, privKeyPEM, 0600); err != nil {
		return fmt.Errorf("failed to write private key: %v", err)
	}

	// Use PKIX format for public key (same as Python's SubjectPublicKeyInfo)
	pubKeyBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("failed to marshal public key: %v", err)
	}
	pubKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubKeyBytes,
	})
	if err := os.WriteFile(pubKeyPath, pubKeyPEM, 0644); err != nil {
		return fmt.Errorf("failed to write public key: %v", err)
	}

	return nil
}

// CreateVideoGenerationTask creates a video generation task.
// Supports two calling patterns:
//   - CreateVideoGenerationTask(data) - uses nil for customHeader
//   - CreateVideoGenerationTask(data, customHeader) - uses provided customHeader
func (c *SeedanceClient) CreateVideoGenerationTask(data map[string]interface{}, customHeaderOptional ...map[string]string) (string, error) {
	if len(data) == 0 {
		return "", common.NewAICCException("data must be provided")
	}

	if _, ok := data["model"]; !ok {
		data["model"] = c.Endpoint
	}

	if c.IsEnableVideoEncrypt {
		if c.VideoFileEncryptPubKey == "" || c.VideoFileEncryptPrivKey == "" {
			return "", common.NewAICCException("pub_key and priv_key must be provided when Enable-TOS-Content-Result-Encryption is 'true'")
		}
	}

	baseURL := c.getBaseURL()
	url := baseURL + c.CreateTaskURL

	requestBodyJSON, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("error marshalling request body: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBodyJSON))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	// Add custom headers if provided
	if len(customHeaderOptional) > 0 && customHeaderOptional[0] != nil {
		for key, value := range customHeaderOptional[0] {
			req.Header.Set(key, value)
		}
	}

	if c.IsEnableVideoEncrypt {
		req.Header.Set("Enable-TOS-Content-Result-Encryption", "true")
		req.Header.Set("X-Encryption-Algorithm", "RSA_OAEP_4096_AES_256")
	}

	if c.VideoFileEncryptPubKey != "" {
		pkBase64 := base64.StdEncoding.EncodeToString([]byte(c.VideoFileEncryptPubKey))
		req.Header.Set("PK", pkBase64)
	}

	log.Info("[Request] Data: %s", string(requestBodyJSON))

	resp, err := c.SecureHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	if resp.StatusCode == 200 {
		var result map[string]interface{}
		if err := json.Unmarshal(responseBody, &result); err != nil {
			return "", fmt.Errorf("error unmarshalling response: %v", err)
		}
		if taskID, ok := result["id"].(string); ok {
			return taskID, nil
		}
		return "", nil
	}

	log.Error("[Error] url: %s, response: %s", url, string(responseBody))
	return "", common.NewAICCException(fmt.Sprintf("Failed to create video generation task: %s", string(responseBody)))
}

type VideoGenerationTaskByMessageOptions struct {
	Message       []interface{}
	GenerateAudio *bool
	Ratio         string
	Duration      int
	Watermark     *bool
}

func (c *SeedanceClient) CreateVideoGenerationTaskByMessage(opts VideoGenerationTaskByMessageOptions) (string, error) {
	if len(opts.Message) == 0 {
		return "", common.NewAICCException("message must be provided")
	}

	generateAudio := true
	if opts.GenerateAudio != nil {
		generateAudio = *opts.GenerateAudio
	}

	ratio := opts.Ratio
	if ratio == "" {
		ratio = "16:9"
	}

	duration := opts.Duration
	if duration == 0 {
		duration = 11
	}

	watermark := false
	if opts.Watermark != nil {
		watermark = *opts.Watermark
	}

	data := map[string]interface{}{
		"model":          c.Endpoint,
		"content":        opts.Message,
		"generate_audio": generateAudio,
		"ratio":          ratio,
		"duration":       duration,
		"watermark":      watermark,
	}

	return c.CreateVideoGenerationTask(data)
}

func (c *SeedanceClient) QueryVideoGenerationTask(taskID string) (map[string]interface{}, error) {
	if taskID == "" {
		return nil, common.NewAICCException("task_id must be provided")
	}

	baseURL := c.getBaseURL()
	url := baseURL + c.QueryTaskURL + "/" + taskID

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.SecureHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	if resp.StatusCode == 200 {
		var result map[string]interface{}
		if err := json.Unmarshal(responseBody, &result); err != nil {
			return nil, fmt.Errorf("error unmarshalling response: %v", err)
		}
		return result, nil
	}

	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		log.Error("[Error] url: %s, Exception=%v", url, err)
		return nil, common.NewAICCException("Failed to query video generation task")
	}
	return result, nil
}

type QueryVideoGenerationTaskListOptions struct {
	PageNum     int
	PageSize    int
	Status      string
	TaskIDs     []string
	Model       string
	ServiceTier string
}

func (c *SeedanceClient) QueryVideoGenerationTaskList(opts QueryVideoGenerationTaskListOptions) (map[string]interface{}, error) {
	baseURL := c.getBaseURL()

	var params []string

	if opts.PageNum > 0 {
		params = append(params, fmt.Sprintf("page_num=%d", opts.PageNum))
	}

	if opts.PageSize > 0 {
		params = append(params, fmt.Sprintf("page_size=%d", opts.PageSize))
	}

	if opts.Status != "" {
		params = append(params, fmt.Sprintf("filter.status=%s", opts.Status))
	}

	for _, taskID := range opts.TaskIDs {
		params = append(params, fmt.Sprintf("filter.task_ids=%s", taskID))
	}

	if opts.Model != "" {
		params = append(params, fmt.Sprintf("filter.model=%s", opts.Model))
	}

	if opts.ServiceTier != "" {
		params = append(params, fmt.Sprintf("filter.service_tier=%s", opts.ServiceTier))
	}

	url := baseURL + c.QueryTaskListURL
	if len(params) > 0 {
		url = url + "?" + strings.Join(params, "&")
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.SecureHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		log.Error("query_video_generation_task_list error: %v", err)
		return nil, common.NewAICCException("Failed to query video generation task list")
	}

	return result, nil
}

func (c *SeedanceClient) DeleteVideoGenerationTask(taskID string) (map[string]interface{}, error) {
	if taskID == "" {
		return nil, common.NewAICCException("task_id must be provided")
	}

	baseURL := c.getBaseURL()
	url := baseURL + c.DeleteTaskURL + "/" + taskID

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	log.Info("[Request] URL: %s", url)

	resp, err := c.SecureHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		log.Error("delete_video_generation_task error: %v", err)
		return nil, common.NewAICCException("Failed to delete_video_generation_task")
	}

	log.Info("[Response] Body: %s", string(responseBody))
	return result, nil
}

func (c *SeedanceClient) VideoFileDownload(taskID, videoFilePath string) (bool, error) {
	if taskID == "" {
		return false, common.NewAICCException("task_id must be provided")
	}

	if videoFilePath == "" {
		return false, common.NewAICCException("video_file_path must be provided")
	}

	result, err := c.QueryVideoGenerationTask(taskID)
	if err != nil {
		log.Error("[Error] Failed to query task, task_id=%s", taskID)
		return false, err
	}

	if result == nil {
		log.Error("[Error] Failed to query task, task_id=%s", taskID)
		return false, nil
	}

	status, _ := result["status"].(string)
	if status == "succeeded" {
		content, _ := result["content"].(map[string]interface{})
		if content == nil {
			log.Error("[Error] No content in response")
			return false, nil
		}

		videoURL, _ := content["video_url"].(string)
		if videoURL == "" {
			log.Error("[Error] No video_url in response")
			return false, nil
		}

		return c.downloadFileNormal(videoURL, videoFilePath)

	} else if status == "failed" {
		errorInfo, _ := result["error"].(string)
		if errorInfo == "" {
			errorInfo = "Unknown error"
		}
		log.Error("[Error] Task failed: %s", errorInfo)
		return false, nil

	} else if status == "pending" || status == "running" || status == "queued" {
		log.Info("[Waiting] Task is %s, please wait for it to finish...", status)
		return false, nil
	}

	log.Warning("[Warning] Unknown status: %s", status)
	return false, nil
}

func (c *SeedanceClient) VideoFileDownloadByURL(videoURL, videoFilePath string) (bool, error) {
	if videoURL == "" {
		return false, common.NewAICCException("video_url must be provided")
	}

	if videoFilePath == "" {
		return false, common.NewAICCException("video_file_path must be provided")
	}

	log.Info("\n[Downloading] Starting download and decryption...")

	return c.downloadFileNormal(videoURL, videoFilePath)
}

func (c *SeedanceClient) downloadFileNormal(videoURL, videoFilePath string) (bool, error) {
	if videoURL == "" {
		return false, common.NewAICCException("video_url must be provided")
	}

	if videoFilePath == "" {
		return false, common.NewAICCException("video_file_path must be provided")
	}

	fileInfo, err := os.Stat(videoFilePath)
	if err == nil && fileInfo.Size() > 0 {
		log.Error("[Error] File %s already exists and is not empty", videoFilePath)
		return false, nil
	}

	resp, err := http.Get(videoURL)
	if err != nil {
		log.Error("[Error] Download failed: %v", err)
		return false, err
	}
	defer resp.Body.Close()

	outFile, err := os.Create(videoFilePath)
	if err != nil {
		log.Error("[Error] Failed to create file: %v", err)
		return false, err
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		log.Error("[Error] Failed to write file: %v", err)
		return false, err
	}

	if c.IsEnableVideoEncrypt {
		if c.RSAPrivateKey == nil {
			log.Error("[Error] No private key set, cannot decrypt encrypted video")
			return false, nil
		}

		encryptedKey := resp.Header.Get("x-tos-meta-enc-dek")
		if encryptedKey == "" {
			log.Error("[Error] No encrypted_key in response")
			return false, nil
		}

		// URL decode then base64 decode
		unquotedKey, err := url.QueryUnescape(encryptedKey)
		if err != nil {
			log.Error("[Error] Failed to URL decode encrypted_key: %v", err)
			return false, err
		}

		encryptedKeyBytes, err := base64.StdEncoding.DecodeString(unquotedKey)
		if err != nil {
			log.Error("[Error] Failed to decode encrypted_key: %v", err)
			return false, err
		}

		decryptedKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, c.RSAPrivateKey, encryptedKeyBytes, nil)
		if err != nil {
			log.Error("[Error] Failed to decrypt encrypted key: %v", err)
			return false, err
		}

		tmpFilePath := videoFilePath + ".tmp"
		_, err = common.DecryptFile(decryptedKey, videoFilePath, tmpFilePath, "b")
		if err != nil {
			log.Error("[Error] Failed to decrypt file: %v", err)
			return false, err
		}

		os.Remove(videoFilePath)
		err = os.Rename(tmpFilePath, videoFilePath)
		if err != nil {
			log.Error("[Error] Failed to rename file: %v", err)
			return false, err
		}
	}

	return true, nil
}

func (c *SeedanceClient) decryptRSA(encryptedKey string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 encrypted key: %v", err)
	}

	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, c.RSAPrivateKey, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt RSA OAEP: %v", err)
	}

	return plaintext, nil
}

func (c *SeedanceClient) WaitForTaskCompletion(taskID string, pollInterval time.Duration, maxWait time.Duration) (map[string]interface{}, error) {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	if maxWait <= 0 {
		maxWait = 10 * time.Minute
	}

	startTime := time.Now()
	for {
		result, err := c.QueryVideoGenerationTask(taskID)
		if err != nil {
			return nil, err
		}

		status, _ := result["status"].(string)
		if status == "succeeded" || status == "failed" {
			return result, nil
		}

		if time.Since(startTime) >= maxWait {
			return nil, common.NewAICCException(fmt.Sprintf("Task %s did not complete within %v", taskID, maxWait))
		}

		log.Info("[Waiting] Task %s is %s, polling again in %v...", taskID, status, pollInterval)
		time.Sleep(pollInterval)
	}
}
