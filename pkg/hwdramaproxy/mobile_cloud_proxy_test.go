package hwdramaproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMobileCloudProxyForwardsAllTwelveAssetAPIsWithAKSKSignature(t *testing.T) {
	setupMobileCloudScopeDB(t, "group-1", []string{"asset-1"}, []string{"token-1"})
	type receivedRequest struct {
		method        string
		path          string
		body          string
		authorization string
		query         map[string]string
	}
	var mutex sync.Mutex
	received := make([]receivedRequest, 0, 12)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		query := map[string]string{}
		for key, values := range r.URL.Query() {
			if len(values) > 0 {
				query[key] = values[0]
			}
		}
		mutex.Lock()
		received = append(received, receivedRequest{
			method:        r.Method,
			path:          r.URL.Path,
			body:          string(body),
			authorization: r.Header.Get("Authorization"),
			query:         query,
		})
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestId":"req-1","state":"OK","errorCode":"","errorMessage":"","body":""}`))
	}))
	defer upstream.Close()

	config := mobileCloudTwelveAPIConfig(upstream.URL)
	configPath := filepath.Join(t.TempDir(), "routes.yml")
	require.NoError(t, SaveRoutesConfig(configPath, config))
	t.Setenv("TEST_MOBILE_CLOUD_AK", "test-access-key")
	t.Setenv("TEST_MOBILE_CLOUD_SK", "test-secret-key")
	proxy, err := New(Config{
		RoutesConfigPath: configPath,
		TokenResolver: func(string) (*TokenIdentity, error) {
			return &TokenIdentity{ID: 16}, nil
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create asset", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset", body: `{"groupId":"group-1","assetName":"asset","assetUrl":"https://example.com/a.png","assetType":"Image"}`},
		{name: "list assets", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset/query", body: `{"pageNo":1,"pageSize":20,"groupIds":["group-1"]}`},
		{name: "get asset", method: http.MethodGet, path: "/api/openapi-maas/exp/aicc/v2/asset/asset-1"},
		{name: "update asset", method: http.MethodPut, path: "/api/openapi-maas/exp/aicc/v2/asset/asset-1", body: `{"assetName":"renamed"}`},
		{name: "delete asset", method: http.MethodDelete, path: "/api/openapi-maas/exp/aicc/v2/asset/asset-1"},
		{name: "create asset group", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset-group", body: `{"groupType":"AIGC","groupName":"group"}`},
		{name: "list asset groups", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset-group/query", body: `{"pageNo":1,"pageSize":20,"groupIds":["group-1"]}`},
		{name: "get asset group", method: http.MethodGet, path: "/api/openapi-maas/exp/aicc/v2/asset-group/group-1"},
		{name: "update asset group", method: http.MethodPut, path: "/api/openapi-maas/exp/aicc/v2/asset-group/group-1", body: `{"groupName":"renamed"}`},
		{name: "delete asset group", method: http.MethodDelete, path: "/api/openapi-maas/exp/aicc/v2/asset-group/group-1"},
		{name: "create real person session", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/real-person-auth/sessions"},
		{name: "get real person group", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token", body: `{"bytedToken":"token-1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			req.Header.Set("Authorization", "Bearer sk-client-secret")
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()

			proxy.ServeHTTP(resp, req)

			assert.Equal(t, http.StatusOK, resp.Code)
			assert.JSONEq(t, `{"requestId":"req-1","state":"OK","errorCode":"","errorMessage":"","body":""}`, resp.Body.String())
		})
	}

	mutex.Lock()
	defer mutex.Unlock()
	require.Len(t, received, len(tests))
	for i, call := range received {
		assert.Equal(t, tests[i].method, call.method)
		assert.Equal(t, tests[i].path, call.path)
		assert.JSONEq(t, emptyJSONObjectIfBlank(tests[i].body), emptyJSONObjectIfBlank(call.body))
		assert.Empty(t, call.authorization)
		assert.Equal(t, "test-access-key", call.query["AccessKey"])
		assert.Equal(t, mobileCloudSignatureMethod, call.query["SignatureMethod"])
		assert.Equal(t, mobileCloudSignatureVersion, call.query["SignatureVersion"])
		assert.Regexp(t, `^[0-9a-f]{32}$`, call.query["SignatureNonce"])
		assert.Regexp(t, `^[0-9a-f]{64}$`, call.query["Signature"])
		assert.NotEmpty(t, call.query["Timestamp"])
	}
}

func TestMobileCloudProxyPreservesBusinessErrorsAndEmptyCreateBody(t *testing.T) {
	setupMobileCloudScopeDB(t, "group-1", []string{"missing", "invalid"}, nil)
	responseBody := `{"requestId":"req-error","state":"ERROR","errorCode":"AssetNotFound","errorMessage":"asset not found","body":null}`
	upstreamStatus := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream-Trace", "trace-1")
		w.WriteHeader(upstreamStatus)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	config := mobileCloudTwelveAPIConfig(upstream.URL)
	getAction := config.Actions["mobile_cloud_asset_get"]
	getAction.AffinityPathParam = "asset_id"
	config.Actions["mobile_cloud_asset_get"] = getAction
	createAction := config.Actions["mobile_cloud_asset_create"]
	createAction.AffinityResponseField = "body"
	config.Actions["mobile_cloud_asset_create"] = createAction
	configPath := filepath.Join(t.TempDir(), "routes.yml")
	require.NoError(t, SaveRoutesConfig(configPath, config))
	t.Setenv("TEST_MOBILE_CLOUD_AK", "test-access-key")
	t.Setenv("TEST_MOBILE_CLOUD_SK", "test-secret-key")
	proxy, err := New(Config{
		RoutesConfigPath: configPath,
		TokenResolver: func(string) (*TokenIdentity, error) {
			return &TokenIdentity{ID: 16}, nil
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/openapi-maas/exp/aicc/v2/asset/missing", nil)
	req.Header.Set("Authorization", "Bearer sk-client-secret")
	resp := httptest.NewRecorder()
	proxy.ServeHTTP(resp, req)
	assert.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, responseBody, resp.Body.String())
	assert.Equal(t, "trace-1", resp.Header().Get("X-Upstream-Trace"))

	upstreamStatus = http.StatusUnprocessableEntity
	responseBody = `{"requestId":"req-422","state":"ERROR","errorCode":"InvalidParameter","errorMessage":"invalid asset","body":null}`
	req = httptest.NewRequest(http.MethodGet, "/api/openapi-maas/exp/aicc/v2/asset/invalid", nil)
	req.Header.Set("Authorization", "Bearer sk-client-secret")
	resp = httptest.NewRecorder()
	proxy.ServeHTTP(resp, req)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.Code)
	assert.Equal(t, responseBody, resp.Body.String())

	upstreamStatus = http.StatusOK
	responseBody = `{"requestId":"req-create","state":"OK","errorCode":"","errorMessage":"","body":""}`
	req = httptest.NewRequest(http.MethodPost, "/api/openapi-maas/exp/aicc/v2/asset", strings.NewReader(`{"groupId":"group-1","assetUrl":"https://example.com/a.png","assetType":"Image"}`))
	req.Header.Set("Authorization", "Bearer sk-client-secret")
	resp = httptest.NewRecorder()
	proxy.ServeHTTP(resp, req)
	assert.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, responseBody, resp.Body.String())
}

func TestMobileCloudProxyRejectsRedirectWithoutLeakingSignedURL(t *testing.T) {
	setupMobileCloudScopeDB(t, "group-1", []string{"asset-1"}, nil)
	requestCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.Redirect(w, r, "/redirect-target?AccessKey=should-not-leak", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()

	config := mobileCloudTwelveAPIConfig(upstream.URL)
	configPath := filepath.Join(t.TempDir(), "routes.yml")
	require.NoError(t, SaveRoutesConfig(configPath, config))
	t.Setenv("TEST_MOBILE_CLOUD_AK", "test-access-key")
	t.Setenv("TEST_MOBILE_CLOUD_SK", "test-secret-key")
	proxy, err := New(Config{
		RoutesConfigPath: configPath,
		TokenResolver: func(string) (*TokenIdentity, error) {
			return &TokenIdentity{ID: 16}, nil
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/openapi-maas/exp/aicc/v2/asset/asset-1", nil)
	req.Header.Set("Authorization", "Bearer sk-client-secret")
	resp := httptest.NewRecorder()
	proxy.ServeHTTP(resp, req)

	assert.Equal(t, http.StatusBadGateway, resp.Code)
	assert.JSONEq(t, `{"error":{"code":"upstream_error","message":"upstream redirect rejected"}}`, resp.Body.String())
	assert.Empty(t, resp.Header().Get("Location"))
	assert.Equal(t, 1, requestCount)
}

func TestMobileCloudRealPersonFlowSharesOneScopeButIsolatesAnother(t *testing.T) {
	setupMobileCloudScopeDB(t, "", nil, nil)
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/openapi-maas/exp/aicc/v2/real-person-auth/sessions":
			_, _ = w.Write([]byte(`{"requestId":"session-1","state":"OK","body":{"h5Link":"https://example.com/auth","expiresIn":300,"bytedToken":"byted-1"}}`))
		case "/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token":
			_, _ = w.Write([]byte(`{"requestId":"group-1","state":"OK","body":"group-face-1"}`))
		case "/api/openapi-maas/exp/aicc/v2/asset":
			_, _ = w.Write([]byte(`{"requestId":"asset-1","state":"OK","body":"asset-face-1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := mobileCloudTwelveAPIConfig(upstream.URL)
	config.Routes[0].APIKeyIDs = []int{16, 17, 19}
	isolatedRoute := config.Routes[0]
	isolatedRoute.Name = "mobile-cloud-assets-customer-b"
	isolatedRoute.APIKeyIDs = []int{18}
	isolatedRoute.AssetScopeID = "customer-b"
	config.Routes = append(config.Routes, isolatedRoute)
	configPath := filepath.Join(t.TempDir(), "routes.yml")
	require.NoError(t, SaveRoutesConfig(configPath, config))
	t.Setenv("TEST_MOBILE_CLOUD_AK", "test-access-key")
	t.Setenv("TEST_MOBILE_CLOUD_SK", "test-secret-key")
	proxy, err := New(Config{
		RoutesConfigPath: configPath,
		TokenResolver: func(key string) (*TokenIdentity, error) {
			return map[string]*TokenIdentity{
				"client16": {ID: 16},
				"client17": {ID: 17},
				"client18": {ID: 18},
			}[key], nil
		},
	})
	require.NoError(t, err)

	sessionRequest := httptest.NewRequest(http.MethodPost, "/api/openapi-maas/exp/aicc/v2/real-person-auth/sessions", nil)
	sessionRequest.Header.Set("Authorization", "Bearer sk-client16")
	sessionResponse := httptest.NewRecorder()
	proxy.ServeHTTP(sessionResponse, sessionRequest)
	require.Equal(t, http.StatusOK, sessionResponse.Code)

	groupRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token",
		strings.NewReader(`{"bytedToken":"byted-1"}`),
	)
	groupRequest.Header.Set("Authorization", "Bearer sk-client17")
	groupResponse := httptest.NewRecorder()
	proxy.ServeHTTP(groupResponse, groupRequest)
	require.Equal(t, http.StatusOK, groupResponse.Code)

	createAssetRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/openapi-maas/exp/aicc/v2/asset",
		strings.NewReader(`{"groupId":"group-face-1","assetName":"face","assetUrl":"https://example.com/face.png","assetType":"Image"}`),
	)
	createAssetRequest.Header.Set("Authorization", "Bearer sk-client17")
	createAssetResponse := httptest.NewRecorder()
	proxy.ServeHTTP(createAssetResponse, createAssetRequest)
	require.Equal(t, http.StatusOK, createAssetResponse.Code)

	channelID, found, err := model.ResolveAssetChannelBinding([]string{"asset-face-1"}, 19)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 5, channelID)

	isolatedRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token",
		strings.NewReader(`{"bytedToken":"byted-1"}`),
	)
	isolatedRequest.Header.Set("Authorization", "Bearer sk-client18")
	isolatedResponse := httptest.NewRecorder()
	proxy.ServeHTTP(isolatedResponse, isolatedRequest)
	assert.Equal(t, http.StatusForbidden, isolatedResponse.Code)
	assert.JSONEq(t, `{"error":{"code":"asset_scope_forbidden","message":"real-person authentication session does not belong to this API key scope"}}`, isolatedResponse.Body.String())
	assert.Equal(t, 3, upstreamCalls)
}

func mobileCloudTwelveAPIConfig(upstreamBaseURL string) *RoutesConfig {
	actions := map[string]ActionConfig{
		"mobile_cloud_asset_create":               {DownstreamMethod: http.MethodPost, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset", DefaultUpstreamMethod: http.MethodPost, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset", ScopeOperation: ScopeOperationAssetCreate},
		"mobile_cloud_asset_list":                 {DownstreamMethod: http.MethodPost, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/query", DefaultUpstreamMethod: http.MethodPost, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/query", ScopeOperation: ScopeOperationAssetList},
		"mobile_cloud_asset_get":                  {DownstreamMethod: http.MethodGet, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}", DefaultUpstreamMethod: http.MethodGet, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}", ScopeOperation: ScopeOperationAssetGet, ScopePathParam: "asset_id"},
		"mobile_cloud_asset_update":               {DownstreamMethod: http.MethodPut, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}", DefaultUpstreamMethod: http.MethodPut, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}", ScopeOperation: ScopeOperationAssetUpdate, ScopePathParam: "asset_id"},
		"mobile_cloud_asset_delete":               {DownstreamMethod: http.MethodDelete, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}", DefaultUpstreamMethod: http.MethodDelete, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}", ScopeOperation: ScopeOperationAssetDelete, ScopePathParam: "asset_id"},
		"mobile_cloud_asset_group_create":         {DownstreamMethod: http.MethodPost, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group", DefaultUpstreamMethod: http.MethodPost, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group", ScopeOperation: ScopeOperationAssetGroupCreate},
		"mobile_cloud_asset_group_list":           {DownstreamMethod: http.MethodPost, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/query", DefaultUpstreamMethod: http.MethodPost, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/query", ScopeOperation: ScopeOperationAssetGroupList},
		"mobile_cloud_asset_group_get":            {DownstreamMethod: http.MethodGet, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/{group_id}", DefaultUpstreamMethod: http.MethodGet, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/{group_id}", ScopeOperation: ScopeOperationAssetGroupGet, ScopePathParam: "group_id"},
		"mobile_cloud_asset_group_update":         {DownstreamMethod: http.MethodPut, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/{group_id}", DefaultUpstreamMethod: http.MethodPut, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/{group_id}", ScopeOperation: ScopeOperationAssetGroupUpdate, ScopePathParam: "group_id"},
		"mobile_cloud_asset_group_delete":         {DownstreamMethod: http.MethodDelete, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/{group_id}", DefaultUpstreamMethod: http.MethodDelete, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/asset-group/{group_id}", ScopeOperation: ScopeOperationAssetGroupDelete, ScopePathParam: "group_id"},
		"mobile_cloud_real_person_session_create": {DownstreamMethod: http.MethodPost, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/real-person-auth/sessions", DefaultUpstreamMethod: http.MethodPost, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/real-person-auth/sessions", ScopeOperation: ScopeOperationRealPersonSessionCreate},
		"mobile_cloud_real_person_group_get":      {DownstreamMethod: http.MethodPost, DownstreamPath: "/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token", DefaultUpstreamMethod: http.MethodPost, DefaultUpstreamPath: "/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token", ScopeOperation: ScopeOperationRealPersonGroupGet},
	}
	enabledActions := make([]string, 0, len(actions))
	for action := range actions {
		enabledActions = append(enabledActions, action)
	}
	return &RoutesConfig{
		Version: 1,
		Actions: actions,
		Routes: []RouteConfig{
			{
				Name:                 "mobile-cloud-assets",
				APIKeyIDs:            []int{16},
				ChannelID:            5,
				Models:               []string{WildcardModel},
				UpstreamBaseURL:      upstreamBaseURL,
				UpstreamAuthType:     UpstreamAuthTypeMobileCloudAKSK,
				UpstreamAccessKeyEnv: "TEST_MOBILE_CLOUD_AK",
				UpstreamSecretKeyEnv: "TEST_MOBILE_CLOUD_SK",
				AssetNamespaceID:     "mobile-cloud-test",
				AssetScopeID:         "customer-a",
				EnabledActions:       enabledActions,
			},
		},
	}
}

func setupMobileCloudScopeDB(t *testing.T, groupID string, assetIDs []string, bytedTokens []string) {
	t.Helper()
	originalDB := model.DB
	testDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "mobile-cloud-scope.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, testDB.AutoMigrate(
		&model.AssetChannelBinding{},
		&model.AssetScopeTokenBinding{},
		&model.AssetGroupScopeBinding{},
		&model.AssetAuthSessionBinding{},
	))
	model.DB = testDB
	t.Cleanup(func() {
		sqlDB, dbErr := testDB.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		model.DB = originalDB
	})
	if groupID != "" {
		require.NoError(t, model.UpsertAssetGroupScopeBinding(groupID, "mobile-cloud-test", "customer-a", 5, 16))
	}
	for _, assetID := range assetIDs {
		require.NoError(t, model.UpsertScopedAssetChannelBinding(assetID, groupID, "mobile-cloud-test", "customer-a", 5, 16))
	}
	for _, bytedToken := range bytedTokens {
		require.NoError(t, model.UpsertAssetAuthSessionBinding(bytedToken, "mobile-cloud-test", "customer-a", 5, 16, 3600))
	}
}

func emptyJSONObjectIfBlank(value string) string {
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return value
}
