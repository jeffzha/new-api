package maas_seedance_sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/thirdparty/cmccseedance/volcengine_aicc_sdk"
	"github.com/QuantumNous/new-api/internal/thirdparty/cmccseedance/volcengine_aicc_sdk/common/log"
)

const (
	mappingQueryPath = "/mapping/query"
)

// MaasSeedanceClient is the top-level client for MaaS Seedance video generation.
type MaasSeedanceClient struct {
	volcClient         *volcengine_aicc_sdk.SeedanceClient
	maasModel          string // the resolved endpoint returned by /mapping/query, used as model in request body
	serviceVersion     string // service version, passed as service-version header
	enableVideoEncrypt bool
	logger             *log.Logger
}

// NewMaasSeedanceClient creates a new MaasSeedanceClient.
//
// maasBaseURL: the MaaS gateway base URL
// maasAPIKey:  the API key for authentication
// maasModel:   the virtual model name (resolved via /mapping/query)
// enableVideoEncrypt: whether to enable video file encryption
func NewMaasSeedanceClient(maasBaseURL, maasAPIKey, maasModel string, enableVideoEncrypt bool, opts ...MaasSeedanceClientOption) (*MaasSeedanceClient, error) {
	client := &MaasSeedanceClient{
		enableVideoEncrypt: enableVideoEncrypt,
		logger:             log.NewLogger(),
	}

	for _, opt := range opts {
		opt(client)
	}

	// Resolve actual endpoint: the endpoint from /mapping/query is used as the model field in the request body
	resolvedModel := client.resolveActualModel(maasBaseURL, maasAPIKey, maasModel)
	client.maasModel = resolvedModel

	seedanceOpts := []volcengine_aicc_sdk.SeedanceClientOption{
		volcengine_aicc_sdk.WithSeedanceBaseURL(maasBaseURL),
		volcengine_aicc_sdk.WithSeedanceAPIKey(maasAPIKey),
		volcengine_aicc_sdk.WithSeedanceEndpoint(resolvedModel),
		volcengine_aicc_sdk.WithSeedanceIsSecure(true),
		volcengine_aicc_sdk.WithSeedanceIsEnableVideoEncrypt(enableVideoEncrypt),
		volcengine_aicc_sdk.WithSeedanceTimeout(120.0),
		volcengine_aicc_sdk.WithSeedanceVideoFileStorageLocation("TOS"),
	}

	client.volcClient = volcengine_aicc_sdk.NewSeedanceClient(seedanceOpts...)

	client.logger.Info("[MaasSeedanceClient] initialized, baseUrl: %s, originalModel: %s, actualModel: %s, enableVideoEncrypt: %v, serviceVersion: %s",
		maasBaseURL, maasModel, resolvedModel, enableVideoEncrypt, client.serviceVersion)

	return client, nil
}

func (c *MaasSeedanceClient) resolveActualModel(baseURL, apiKey, model string) string {
	cleanBaseURL := strings.TrimRight(baseURL, "/")
	mappingURL := cleanBaseURL + mappingQueryPath

	c.logger.Info("[MaasSeedanceClient] Querying model mapping: url=%s, model=%s", mappingURL, model)

	requestBody := map[string]interface{}{
		"model": model,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		c.logger.Warning("[MaasSeedanceClient] Failed to marshal mapping query request: %v", err)
		return model
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("POST", mappingURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		c.logger.Warning("[MaasSeedanceClient] Failed to create mapping query request: %v", err)
		return model
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	if c.serviceVersion != "" {
		req.Header.Set("service-version", c.serviceVersion)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		c.logger.Warning("[MaasSeedanceClient] Failed to query model mapping, using original model: %s. Error: %v", model, err)
		return model
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.logger.Warning("[MaasSeedanceClient] Failed to decode mapping response, using original model: %s", model)
		return model
	}

	c.logger.Info("[MaasSeedanceClient] Mapping query response: status=%d, body=%v", resp.StatusCode, result)

	if resp.StatusCode == 200 {
		if endpoint, ok := result["endpoint"].(string); ok && endpoint != "" {
			c.logger.Info("[MaasSeedanceClient] Resolved model mapping: %s -> %s", model, endpoint)
			return endpoint
		}
		c.logger.Warning("[MaasSeedanceClient] No endpoint found in mapping response for model: %s, using original model", model)
	} else {
		c.logger.Warning("[MaasSeedanceClient] Mapping query failed with status: %d, using original model: %s", resp.StatusCode, model)
	}

	return model
}

// SetVideoFileEncryptKey sets or generates RSA key pair for video file encryption.
func (c *MaasSeedanceClient) SetVideoFileEncryptKey(publicKeyPath, privateKeyPath string) error {
	if !c.enableVideoEncrypt {
		c.logger.Warning("[MaasSeedanceClient] Current mode is plaintext, no need to set video encrypt key")
		return nil
	}

	if _, err := os.Stat(publicKeyPath); os.IsNotExist(err) {
		if _, err := os.Stat(privateKeyPath); os.IsNotExist(err) {
			c.logger.Info("[MaasSeedanceClient] Generating RSA key pair...")
			if err := c.volcClient.GenerateVideoFileEncryptKey(publicKeyPath, privateKeyPath); err != nil {
				return fmt.Errorf("failed to generate RSA key pair: %w", err)
			}
			c.logger.Info("[MaasSeedanceClient] RSA key pair generated successfully")
		}
	} else {
		c.logger.Info("[MaasSeedanceClient] Loading existing RSA key pair from: %s", publicKeyPath)
	}

	if err := c.volcClient.SetVideoFileEncryptKey(publicKeyPath, privateKeyPath); err != nil {
		return fmt.Errorf("failed to set video file encrypt key: %w", err)
	}

	return nil
}

// CreateVideoGenerationTask creates a video generation task.
func (c *MaasSeedanceClient) CreateVideoGenerationTask(data map[string]interface{}) (string, error) {
	data["model"] = c.maasModel
	c.logger.Info("[MaasSeedanceClient] Request body after adding model: %v", data)

	headers := make(map[string]string)
	if c.hasVideoInput(data) {
		headers["Input-Has-Video"] = "true"
	}
	if c.serviceVersion != "" {
		headers["service-version"] = c.serviceVersion
	}

	c.logger.Info("[MaasSeedanceClient] Creating video generation task, Input-Has-Video: %v, service-version: %s",
		headers["Input-Has-Video"] != "", c.serviceVersion)

	taskID, err := c.volcClient.CreateVideoGenerationTask(data, headers)
	if err != nil {
		return "", err
	}

	c.logger.Info("[MaasSeedanceClient] Task created successfully, taskId: %s", taskID)
	return taskID, nil
}

// QueryVideoGenerationTask queries the status of a video generation task.
func (c *MaasSeedanceClient) QueryVideoGenerationTask(taskID string) (map[string]interface{}, error) {
	c.logger.Info("[MaasSeedanceClient] Querying video generation task: %s", taskID)

	result, err := c.volcClient.QueryVideoGenerationTask(taskID)
	if err != nil {
		return nil, err
	}

	if result != nil {
		if status, ok := result["status"].(string); ok {
			c.logger.Info("[MaasSeedanceClient] Task status: %s", status)
		}
	}

	return result, nil
}

// QueryVideoGenerationTaskList queries the list of video generation tasks.
func (c *MaasSeedanceClient) QueryVideoGenerationTaskList(pageNum, pageSize int, status string, taskIDs []string, startTime, endTime string) (map[string]interface{}, error) {
	c.logger.Info("[MaasSeedanceClient] Querying video generation task list - pageNum: %d, pageSize: %d, status: %s",
		pageNum, pageSize, status)

	opts := volcengine_aicc_sdk.QueryVideoGenerationTaskListOptions{
		PageNum:  pageNum,
		PageSize: pageSize,
		Status:   status,
		TaskIDs:  taskIDs,
	}

	return c.volcClient.QueryVideoGenerationTaskList(opts)
}

// DownloadVideo downloads the video file for a completed task.
func (c *MaasSeedanceClient) DownloadVideo(taskID, localFilePath string) (bool, error) {
	c.logger.Info("[MaasSeedanceClient] Downloading video: taskId=%s, localPath=%s", taskID, localFilePath)

	success, err := c.volcClient.VideoFileDownload(taskID, localFilePath)
	if err != nil {
		return false, err
	}

	if success {
		c.logger.Info("[MaasSeedanceClient] Video downloaded successfully to: %s", localFilePath)
	} else {
		c.logger.Warning("[MaasSeedanceClient] Video download failed for taskId: %s", taskID)
	}

	return success, nil
}

// DeleteVideoGenerationTask deletes a video generation task.
func (c *MaasSeedanceClient) DeleteVideoGenerationTask(taskID string) (bool, error) {
	c.logger.Info("[MaasSeedanceClient] Deleting video generation task: %s", taskID)

	result, err := c.volcClient.DeleteVideoGenerationTask(taskID)
	if err != nil {
		return false, err
	}

	return result != nil, nil
}

func (c *MaasSeedanceClient) hasVideoInput(data map[string]interface{}) bool {
	content, ok := data["content"]
	if !ok {
		return false
	}

	contentList, ok := content.([]interface{})
	if !ok {
		return false
	}

	for _, item := range contentList {
		part, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if partType, ok := part["type"].(string); ok && partType == "video_url" {
			return true
		}
	}

	return false
}
