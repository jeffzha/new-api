package enterprisepolicyhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const (
	tokenOperationObjectSyncPath          = "/api/v1/gateway/objects/sync"
	tokenOperationUsageDetailsPath        = "/api/v1/gateway/usage-details"
	tokenOperationObjectSyncSchemaVersion = "gateway-objects-sync-v1"
	tokenOperationUsageDetailsVersion     = "usage-details-v1"
)

type tokenOperationObjectSyncRequest struct {
	Objects tokenOperationObjectInventory `json:"objects"`
}

type tokenOperationObjectInventory struct {
	Customers []map[string]any `json:"customers,omitempty"`
	Users     []map[string]any `json:"users,omitempty"`
	APIKeys   []map[string]any `json:"api_keys,omitempty"`
	Channels  []map[string]any `json:"channels,omitempty"`
	Models    []map[string]any `json:"models,omitempty"`
	Apps      []map[string]any `json:"apps,omitempty"`
	Projects  []map[string]any `json:"projects,omitempty"`
}

type TokenOperationObjectSyncResult struct {
	Enabled            bool           `json:"enabled"`
	ObjectSyncEnabled  bool           `json:"object_sync_enabled"`
	UsageEventsEnabled bool           `json:"usage_events_enabled"`
	BaseURL            string         `json:"base_url,omitempty"`
	Endpoint           string         `json:"endpoint,omitempty"`
	ConfiguredGateway  bool           `json:"configured_gateway_key"`
	ObjectCounts       map[string]int `json:"object_counts,omitempty"`
	StatusCode         int            `json:"status_code,omitempty"`
	Response           map[string]any `json:"response,omitempty"`
	Error              string         `json:"error,omitempty"`
}

type TokenOperationReadResult struct {
	Enabled           bool           `json:"enabled"`
	BaseURL           string         `json:"base_url,omitempty"`
	Endpoint          string         `json:"endpoint,omitempty"`
	ConfiguredGateway bool           `json:"configured_gateway_key"`
	StatusCode        int            `json:"status_code,omitempty"`
	Response          map[string]any `json:"response,omitempty"`
	Error             string         `json:"error,omitempty"`
}

func (a *App) tokenOperationStatus(c *gin.Context) {
	status := TokenOperationObjectSyncResult{
		Enabled:            a.config.TokenOperation.Enabled,
		ObjectSyncEnabled:  a.config.TokenOperation.ObjectSyncEnabled,
		UsageEventsEnabled: a.config.TokenOperation.UsageEventsEnabled,
		BaseURL:            a.config.TokenOperation.BaseURL,
		Endpoint:           tokenOperationObjectSyncEndpoint(a.config.TokenOperation.BaseURL),
		ConfiguredGateway:  a.config.TokenOperation.GatewayKey != "",
		Response: map[string]any{
			"last_object_sync_at":       a.getSetting("tokenop_last_object_sync_at"),
			"last_object_sync_status":   a.getSetting("tokenop_last_object_sync_status"),
			"last_object_sync_response": a.getSetting("tokenop_last_object_sync_response"),
		},
	}
	respondOK(c, status)
}

func (a *App) syncTokenOperationObjectsHandler(c *gin.Context) {
	result, err := a.SyncTokenOperationObjects(c.Request.Context())
	if err != nil {
		result.Error = err.Error()
		a.audit(c, "token_operation.sync_objects_failed", "token_operation", 0, nil, result)
		respondError(c, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(c, "token_operation.sync_objects", "token_operation", 0, nil, result)
	respondOK(c, result)
}

func (a *App) tokenOperationUsageDetailsHandler(c *gin.Context) {
	result, err := a.GetTokenOperationUsageDetails(c.Request.Context(), c.Request.URL.Query())
	if err != nil {
		result.Error = err.Error()
		respondError(c, http.StatusBadGateway, err.Error())
		return
	}
	respondOK(c, result)
}

func (a *App) SyncTokenOperationObjects(ctx context.Context) (TokenOperationObjectSyncResult, error) {
	cfg := a.config.TokenOperation
	result := TokenOperationObjectSyncResult{
		Enabled:            cfg.Enabled,
		ObjectSyncEnabled:  cfg.ObjectSyncEnabled,
		UsageEventsEnabled: cfg.UsageEventsEnabled,
		BaseURL:            cfg.BaseURL,
		Endpoint:           tokenOperationObjectSyncEndpoint(cfg.BaseURL),
		ConfiguredGateway:  cfg.GatewayKey != "",
	}
	if !cfg.Enabled {
		return result, errors.New("TokenOperation integration is disabled")
	}
	if !cfg.ObjectSyncEnabled {
		return result, errors.New("TokenOperation object sync is disabled")
	}
	if cfg.BaseURL == "" {
		return result, errors.New("EPH_TOKENOP_BASE_URL is required")
	}
	if cfg.GatewayKey == "" {
		return result, errors.New("EPH_TOKENOP_GATEWAY_KEY is required")
	}

	payload, counts, err := a.buildTokenOperationObjectSyncRequest()
	if err != nil {
		return result, err
	}
	result.ObjectCounts = counts

	body, err := common.Marshal(payload)
	if err != nil {
		return result, err
	}
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, result.Endpoint, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-gateway-key", cfg.GatewayKey)
	req.Header.Set("x-schema-version", tokenOperationObjectSyncSchemaVersion)
	req.Header.Set("idempotency-key", tokenOperationIdempotencyKey(body))

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, err
	}
	responsePayload := map[string]any{}
	if len(respBody) > 0 {
		if err := common.Unmarshal(respBody, &responsePayload); err != nil {
			responsePayload = map[string]any{"raw": tokenOperationPreview(string(respBody), 4096)}
		}
	}
	result.Response = responsePayload
	_ = a.setSetting("tokenop_last_object_sync_at", strconv.FormatInt(common.GetTimestamp(), 10))
	_ = a.setSetting("tokenop_last_object_sync_status", strconv.Itoa(resp.StatusCode))
	_ = a.setSetting("tokenop_last_object_sync_response", tokenOperationPreview(string(respBody), 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("TokenOperation object sync failed: status %d", resp.StatusCode)
	}
	return result, nil
}

func (a *App) GetTokenOperationUsageDetails(ctx context.Context, query url.Values) (TokenOperationReadResult, error) {
	cfg := a.config.TokenOperation
	endpoint := tokenOperationEndpoint(cfg.BaseURL, tokenOperationUsageDetailsPath)
	result := TokenOperationReadResult{
		Enabled:           cfg.Enabled,
		BaseURL:           cfg.BaseURL,
		Endpoint:          endpoint,
		ConfiguredGateway: cfg.GatewayKey != "",
	}
	if !cfg.Enabled {
		return result, errors.New("TokenOperation integration is disabled")
	}
	if cfg.BaseURL == "" {
		return result, errors.New("EPH_TOKENOP_BASE_URL is required")
	}
	if cfg.GatewayKey == "" {
		return result, errors.New("EPH_TOKENOP_GATEWAY_KEY is required")
	}

	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return result, err
	}
	filtered := url.Values{}
	for _, key := range []string{"from", "to", "downstreamCustomerKey", "downstreamCustomerType", "appId", "projectId", "employeeId", "endUserId", "apiKeyId", "limit", "after"} {
		for _, value := range query[key] {
			if strings.TrimSpace(value) != "" {
				filtered.Add(key, value)
			}
		}
	}
	if filtered.Get("limit") == "" {
		filtered.Set("limit", "100")
	}
	requestURL.RawQuery = filtered.Encode()

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-gateway-key", cfg.GatewayKey)
	req.Header.Set("x-schema-version", tokenOperationUsageDetailsVersion)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, err
	}
	responsePayload := map[string]any{}
	if len(respBody) > 0 {
		if err := common.Unmarshal(respBody, &responsePayload); err != nil {
			responsePayload = map[string]any{"raw": tokenOperationPreview(string(respBody), 4096)}
		}
	}
	result.Response = responsePayload
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("TokenOperation usage details failed: status %d", resp.StatusCode)
	}
	return result, nil
}

func (a *App) buildTokenOperationObjectSyncRequest() (tokenOperationObjectSyncRequest, map[string]int, error) {
	inventory := tokenOperationObjectInventory{
		Apps: []map[string]any{
			{
				"app_id":       "enterprise_policy_hub",
				"display_name": "Enterprise Policy Hub",
			},
		},
	}

	var orgs []OrgUnit
	if err := a.db.Order("id asc").Find(&orgs).Error; err != nil {
		return tokenOperationObjectSyncRequest{}, nil, err
	}
	orgByID := make(map[int]OrgUnit, len(orgs))
	newAPIUserIDs := map[int]struct{}{}
	for _, org := range orgs {
		orgByID[org.ID] = org
		if org.NewAPIUserID > 0 {
			newAPIUserIDs[org.NewAPIUserID] = struct{}{}
		}
		inventory.Projects = append(inventory.Projects, map[string]any{
			"project_id":        tokenOperationOrgUnitID(org.ID),
			"app_id":            "enterprise_policy_hub",
			"display_name":      org.Name,
			"parent_project_id": tokenOperationOptionalOrgUnitID(org.ParentID),
			"org_unit_code":     org.Code,
			"org_unit_type":     org.Type,
			"object_status":     tokenOperationObjectStatus(org.Status),
		})
	}

	var keys []EnterpriseKey
	if err := a.db.Order("id asc").Find(&keys).Error; err != nil {
		return tokenOperationObjectSyncRequest{}, nil, err
	}
	for _, key := range keys {
		if key.NewAPIUserID > 0 {
			newAPIUserIDs[key.NewAPIUserID] = struct{}{}
		}
	}

	usersByID, err := a.loadTokenOperationUsers(newAPIUserIDs)
	if err != nil {
		return tokenOperationObjectSyncRequest{}, nil, err
	}
	userIDs := sortedIntKeys(newAPIUserIDs)
	for _, userID := range userIDs {
		user := usersByID[userID]
		displayName := tokenOperationUserDisplayName(userID, user)
		gatewayID := strconv.Itoa(userID)
		inventory.Customers = append(inventory.Customers, map[string]any{
			"gateway_customer_id": gatewayID,
			"display_name":        displayName,
			"aliases":             []string{"newapi_user_id=" + gatewayID},
		})
		inventory.Users = append(inventory.Users, map[string]any{
			"gateway_user_id":     gatewayID,
			"gateway_customer_id": gatewayID,
			"display_name":        displayName,
			"aliases":             []string{"newapi_user_id=" + gatewayID},
		})
	}

	for _, key := range keys {
		if key.NewAPITokenID <= 0 {
			continue
		}
		parentUserID := key.NewAPIUserID
		if parentUserID == 0 {
			if org, ok := orgByID[key.OrgUnitID]; ok {
				parentUserID = org.NewAPIUserID
			}
		}
		row := map[string]any{
			"token_id":          strconv.Itoa(key.NewAPITokenID),
			"display_name":      key.Name,
			"object_status":     tokenOperationObjectStatus(key.Status),
			"environment":       key.Environment,
			"gateway_user_id":   tokenOperationOptionalIntID(parentUserID),
			"enterprise_key_id": strconv.Itoa(key.ID),
			"org_unit_id":       tokenOperationOrgUnitID(key.OrgUnitID),
			"project_id":        tokenOperationScopedID("project_id", key.ProjectID),
			"cost_center_id":    tokenOperationScopedID("cost_center_id", key.CostCenterID),
			"aliases": []string{
				"token_id=" + strconv.Itoa(key.NewAPITokenID),
				"enterprise_key_id=" + strconv.Itoa(key.ID),
			},
		}
		if parentUserID > 0 {
			row["gateway_customer_id"] = strconv.Itoa(parentUserID)
		}
		if key.KeyFingerprint != "" {
			row["api_key_fingerprint"] = key.KeyFingerprint
		}
		inventory.APIKeys = append(inventory.APIKeys, row)
	}

	channels, channelByID, err := a.loadTokenOperationChannels()
	if err != nil {
		return tokenOperationObjectSyncRequest{}, nil, err
	}
	for _, channel := range channels {
		channelType := constant.GetChannelTypeName(channel.Type)
		inventory.Channels = append(inventory.Channels, map[string]any{
			"channel_id":    strconv.Itoa(channel.Id),
			"display_name":  channel.Name,
			"vendor_name":   channelType,
			"channel_type":  channelType,
			"object_status": tokenOperationChannelObjectStatus(channel.Status),
			"aliases":       []string{"channel_id=" + strconv.Itoa(channel.Id), channelType},
		})
	}

	modelRows, err := a.buildTokenOperationModelObjects(channelByID)
	if err != nil {
		return tokenOperationObjectSyncRequest{}, nil, err
	}
	inventory.Models = modelRows

	counts := map[string]int{
		"customers": len(inventory.Customers),
		"users":     len(inventory.Users),
		"api_keys":  len(inventory.APIKeys),
		"channels":  len(inventory.Channels),
		"models":    len(inventory.Models),
		"apps":      len(inventory.Apps),
		"projects":  len(inventory.Projects),
	}
	return tokenOperationObjectSyncRequest{Objects: inventory}, counts, nil
}

func (a *App) loadTokenOperationUsers(ids map[int]struct{}) (map[int]model.User, error) {
	usersByID := make(map[int]model.User, len(ids))
	if len(ids) == 0 {
		return usersByID, nil
	}
	var users []model.User
	if err := a.newAPIDB.Where("id IN ?", sortedIntKeys(ids)).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		usersByID[user.Id] = user
	}
	return usersByID, nil
}

func (a *App) loadTokenOperationChannels() ([]model.Channel, map[int]model.Channel, error) {
	var channels []model.Channel
	if err := a.newAPIDB.Omit("key").Order("id asc").Find(&channels).Error; err != nil {
		return nil, nil, err
	}
	channelByID := make(map[int]model.Channel, len(channels))
	for _, channel := range channels {
		channelByID[channel.Id] = channel
	}
	return channels, channelByID, nil
}

func (a *App) buildTokenOperationModelObjects(channelByID map[int]model.Channel) ([]map[string]any, error) {
	var abilities []model.Ability
	if err := a.newAPIDB.Order("channel_id asc").Find(&abilities).Error; err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	rows := make([]map[string]any, 0, len(abilities))
	for _, ability := range abilities {
		channel := channelByID[ability.ChannelId]
		callType, confidence := inferTokenOperationCallType(ability.Model, channel.Type)
		key := strings.Join([]string{ability.Model, strconv.Itoa(ability.ChannelId), callType}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		channelType := constant.GetChannelTypeName(channel.Type)
		rows = append(rows, map[string]any{
			"model_name":              ability.Model,
			"channel_id":              strconv.Itoa(ability.ChannelId),
			"display_name":            ability.Model,
			"call_type":               callType,
			"vendor_name":             channelType,
			"object_status":           tokenOperationChannelObjectStatus(channel.Status),
			"mapping_hint_confidence": confidence,
			"aliases":                 []string{ability.Model, channelType + ":" + ability.Model},
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		left := fmt.Sprint(rows[i]["model_name"]) + "|" + fmt.Sprint(rows[i]["channel_id"])
		right := fmt.Sprint(rows[j]["model_name"]) + "|" + fmt.Sprint(rows[j]["channel_id"])
		return left < right
	})
	return rows, nil
}

func tokenOperationObjectStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case StatusEnabled, "active":
		return "active"
	case StatusDisabled:
		return "disabled"
	default:
		return "discovered"
	}
}

func tokenOperationChannelObjectStatus(status int) string {
	if status == common.ChannelStatusEnabled {
		return "active"
	}
	return "disabled"
}

func inferTokenOperationCallType(modelName string, channelType int) (string, string) {
	lower := strings.ToLower(strings.TrimSpace(modelName))
	switch channelType {
	case constant.ChannelTypeDoubaoVideo, constant.ChannelTypeSeedanceDomestic, constant.ChannelTypeMobileCloudSeedance, constant.ChannelTypeSora, constant.ChannelTypeKling, constant.ChannelTypeVidu:
		return "video_generation", "high"
	case constant.ChannelTypeSunoAPI:
		return "speech", "medium"
	}
	switch {
	case strings.Contains(lower, "seedance"),
		strings.Contains(lower, "sora"),
		strings.Contains(lower, "video"),
		strings.Contains(lower, "kling"),
		strings.Contains(lower, "vidu"),
		strings.Contains(lower, "hailuo"):
		return "video_generation", "medium"
	case strings.Contains(lower, "embedding"), strings.Contains(lower, "embed"):
		return "embedding", "medium"
	case strings.Contains(lower, "rerank"):
		return "rerank", "medium"
	case strings.Contains(lower, "tts"), strings.Contains(lower, "speech"):
		return "speech", "medium"
	case strings.Contains(lower, "whisper"), strings.Contains(lower, "transcription"):
		return "transcription", "medium"
	default:
		return "text_generation", "low"
	}
}

func tokenOperationObjectSyncEndpoint(baseURL string) string {
	return tokenOperationEndpoint(baseURL, tokenOperationObjectSyncPath)
}

func tokenOperationEndpoint(baseURL string, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + path
}

func tokenOperationIdempotencyKey(body []byte) string {
	sum := sha256.Sum256(body)
	return "eph-objects-sync-" + hex.EncodeToString(sum[:12])
}

func tokenOperationPreview(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func tokenOperationUserDisplayName(id int, user model.User) string {
	if user.DisplayName != "" {
		return user.DisplayName
	}
	if user.Username != "" {
		return user.Username
	}
	return "new-api user " + strconv.Itoa(id)
}

func tokenOperationOrgUnitID(id int) string {
	return tokenOperationScopedID("org_unit_id", id)
}

func tokenOperationScopedID(prefix string, id int) string {
	if id <= 0 {
		return ""
	}
	return prefix + "=" + strconv.Itoa(id)
}

func tokenOperationOptionalOrgUnitID(id *int) string {
	if id == nil || *id <= 0 {
		return ""
	}
	return tokenOperationOrgUnitID(*id)
}

func tokenOperationOptionalIntID(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func sortedIntKeys(values map[int]struct{}) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}

func (a *App) getSetting(key string) string {
	var setting Setting
	if err := a.db.First(&setting, "key = ?", key).Error; err != nil {
		return ""
	}
	return setting.Value
}
