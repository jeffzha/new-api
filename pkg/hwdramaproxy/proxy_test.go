package hwdramaproxy

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeAPIKey(t *testing.T) {
	tests := []struct {
		name   string
		header string
		key    string
		ok     bool
	}{
		{name: "bearer sk key", header: "Bearer sk-abc123", key: "abc123", ok: true},
		{name: "lowercase bearer", header: "bearer sk-abc123", key: "abc123", ok: true},
		{name: "specific channel suffix", header: "sk-abc123-extra", key: "abc123", ok: true},
		{name: "bare key", header: "abc123", key: "abc123", ok: true},
		{name: "empty header", header: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := NormalizeAPIKey(tt.header)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.key, key)
		})
	}
}

func TestRouteDecisionFor(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		path          string
		pathKnown     bool
		methodAllowed bool
	}{
		{name: "list assets", method: http.MethodGet, path: "/api/v3/ark/assets", pathKnown: true, methodAllowed: true},
		{name: "create assets", method: http.MethodPost, path: "/api/v3/ark/assets", pathKnown: true, methodAllowed: true},
		{name: "asset group wrong method", method: http.MethodGet, path: "/api/v3/ark/assets/groups", pathKnown: true, methodAllowed: false},
		{name: "get asset by id", method: http.MethodGet, path: "/api/v3/ark/assets/asset-123", pathKnown: true, methodAllowed: true},
		{name: "post asset by id", method: http.MethodPost, path: "/api/v3/ark/assets/asset-123", pathKnown: true, methodAllowed: false},
		{name: "nested asset path rejected", method: http.MethodGet, path: "/api/v3/ark/assets/asset-123/extra", pathKnown: false, methodAllowed: false},
		{name: "real person asset", method: http.MethodGet, path: "/api/v3/ark/real-person/assets/asset-123", pathKnown: true, methodAllowed: true},
		{name: "validate session", method: http.MethodGet, path: "/api/v3/ark/real-person/validate/sessions/session-123", pathKnown: true, methodAllowed: true},
		{name: "seedance create asset", method: http.MethodPost, path: "/api/v3/open/CreateAsset", pathKnown: true, methodAllowed: true},
		{name: "seedance create validation session", method: http.MethodPost, path: "/api/v3/open/CreateVisualValidateSession", pathKnown: true, methodAllowed: true},
		{name: "seedance get validation result", method: http.MethodPost, path: "/api/v3/open/GetVisualValidateResult", pathKnown: true, methodAllowed: true},
		{name: "seedance create asset group", method: http.MethodPost, path: "/api/v3/open/CreateAssetGroup", pathKnown: true, methodAllowed: true},
		{name: "private seedance bill endpoint", method: http.MethodPost, path: "/api/v3/open/ListSplitBillDetail", pathKnown: false, methodAllowed: false},
		{name: "unknown path", method: http.MethodPost, path: "/api/v3/open/DeleteAsset", pathKnown: false, methodAllowed: false},
		{name: "aicc create asset group", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset-group/", pathKnown: true, methodAllowed: true},
		{name: "aicc create asset group without trailing slash", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset-group", pathKnown: true, methodAllowed: true},
		{name: "aicc create asset", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset/", pathKnown: true, methodAllowed: true},
		{name: "aicc create asset without trailing slash", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset", pathKnown: true, methodAllowed: true},
		{name: "aicc query assets", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset/query", pathKnown: true, methodAllowed: true},
		{name: "aicc query asset groups", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset-group/query", pathKnown: true, methodAllowed: true},
		{name: "aicc query assets wrong method", method: http.MethodGet, path: "/api/openapi-maas/exp/aicc/v2/asset/query", pathKnown: true, methodAllowed: false},
		{name: "aicc get asset", method: http.MethodGet, path: "/api/openapi-maas/exp/aicc/v2/asset/cmcc-asset-id", pathKnown: true, methodAllowed: true},
		{name: "aicc update asset", method: http.MethodPut, path: "/api/openapi-maas/exp/aicc/v2/asset/cmcc-asset-id", pathKnown: true, methodAllowed: true},
		{name: "aicc delete asset", method: http.MethodDelete, path: "/api/openapi-maas/exp/aicc/v2/asset/cmcc-asset-id", pathKnown: true, methodAllowed: true},
		{name: "aicc get asset group", method: http.MethodGet, path: "/api/openapi-maas/exp/aicc/v2/asset-group/cmcc-group-id", pathKnown: true, methodAllowed: true},
		{name: "aicc update asset group", method: http.MethodPut, path: "/api/openapi-maas/exp/aicc/v2/asset-group/cmcc-group-id", pathKnown: true, methodAllowed: true},
		{name: "aicc delete asset group", method: http.MethodDelete, path: "/api/openapi-maas/exp/aicc/v2/asset-group/cmcc-group-id", pathKnown: true, methodAllowed: true},
		{name: "aicc asset wrong method", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset/cmcc-asset-id", pathKnown: true, methodAllowed: false},
		{name: "aicc nested asset path rejected", method: http.MethodGet, path: "/api/openapi-maas/exp/aicc/v2/asset/cmcc-asset-id/extra", pathKnown: false, methodAllowed: false},
		{name: "aicc create real person session", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/real-person-auth/sessions", pathKnown: true, methodAllowed: true},
		{name: "aicc get real person asset group", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token", pathKnown: true, methodAllowed: true},
		{name: "aicc private path rejected", method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/private/billing", pathKnown: false, methodAllowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := RouteDecisionFor(tt.method, tt.path)
			assert.Equal(t, tt.pathKnown, decision.PathKnown)
			assert.Equal(t, tt.methodAllowed, decision.MethodAllowed)
		})
	}
}

func TestProxyPassesThroughAllowedRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "/api/v3/open/CreateAsset", r.URL.Path)
		assert.Equal(t, "x=1", r.URL.RawQuery)
		assert.Equal(t, "Bearer upstream-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.JSONEq(t, `{"hello":"world"}`, string(body))

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Trace", "trace-1")
		w.Header().Add("Set-Cookie", "PHPSESSID=upstream-session; Path=/; HttpOnly")
		w.Header().Add("Set-Cookie", "supplier-preference=value; Path=/")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	proxy, err := New(Config{
		UpstreamBaseURL: upstream.URL,
		UpstreamAPIKey:  "upstream-key",
		TokenLookup: func(key string) (bool, error) {
			assert.Equal(t, "clientkey", key)
			return true, nil
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v3/open/CreateAsset?x=1", stringsReader(`{"hello":"world"}`))
	req.Header.Set("Authorization", "Bearer sk-clientkey-extra")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp := httptest.NewRecorder()

	proxy.ServeHTTP(resp, req)

	assert.Equal(t, http.StatusAccepted, resp.Code)
	assert.Equal(t, "application/json", resp.Header().Get("Content-Type"))
	assert.Equal(t, "trace-1", resp.Header().Get("X-Upstream-Trace"))
	assert.Empty(t, resp.Header().Values("Set-Cookie"))
	assert.JSONEq(t, `{"ok":true}`, resp.Body.String())
}

func TestProxyUsesDynamicRouteConfig(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "/v1/assets/create", r.URL.Path)
		assert.Equal(t, "q=1", r.URL.RawQuery)
		assert.Equal(t, "Bearer dynamic-upstream-key", r.Header.Get("Authorization"))
		assert.JSONEq(t, `{"model":"doubao-seedance-2-0-fast-filter-off","url":"https://example.com/a.jpg"}`, string(body))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"asset-1"}`))
	}))
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "routes.yml")
	config := RoutesConfig{
		Version: 1,
		Actions: map[string]ActionConfig{
			"seedance_open_create_asset": {
				DownstreamMethod:      "POST",
				DownstreamPath:        "/api/v3/open/CreateAsset",
				DefaultUpstreamMethod: "POST",
				DefaultUpstreamPath:   "/api/v3/open/CreateAsset",
			},
		},
		Routes: []RouteConfig{
			{
				Name:              "wetoken",
				APIKeyIDs:         []int{7},
				ChannelID:         3,
				Models:            []string{"doubao-seedance-2-0-fast-filter-off"},
				UpstreamBaseURL:   upstream.URL,
				UpstreamAPIKeyEnv: "HWD_TEST_UPSTREAM_KEY",
				EnabledActions:    []string{"seedance_open_create_asset"},
				UpstreamActionOverrides: map[string]UpstreamActionConfig{
					"seedance_open_create_asset": {
						UpstreamMethod: "POST",
						UpstreamPath:   "/v1/assets/create",
					},
				},
			},
		},
	}
	require.NoError(t, SaveRoutesConfig(configPath, &config))
	t.Setenv("HWD_TEST_UPSTREAM_KEY", "dynamic-upstream-key")

	proxy, err := New(Config{
		RoutesConfigPath: configPath,
		UpstreamBaseURL:  "://legacy-value-is-ignored-in-dynamic-mode",
		TokenResolver: func(key string) (*TokenIdentity, error) {
			assert.Equal(t, "clientkey", key)
			return &TokenIdentity{ID: 7}, nil
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v3/open/CreateAsset?q=1", stringsReader(`{"model":"doubao-seedance-2-0-fast-filter-off","url":"https://example.com/a.jpg"}`))
	req.Header.Set("Authorization", "Bearer sk-clientkey")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	proxy.ServeHTTP(resp, req)

	assert.Equal(t, http.StatusCreated, resp.Code)
	assert.JSONEq(t, `{"id":"asset-1"}`, resp.Body.String())
}

func TestProxyUsesSeedanceLMDKeyWithoutForwardingClientAuthorization(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/asset/SdToolApi/CreateAsset", r.URL.Path)
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Equal(t, "supplier-secret", r.Header.Get("lmd-key"))
		_, _ = w.Write([]byte(`{"state":1,"data":{"id":"asset-1"}}`))
	}))
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "routes.yml")
	config := RoutesConfig{
		Version: 1,
		Actions: map[string]ActionConfig{
			"create_asset": {
				DownstreamMethod:      http.MethodPost,
				DownstreamPath:        "/api/v3/open/CreateAsset",
				DefaultUpstreamMethod: http.MethodPost,
				DefaultUpstreamPath:   "/asset/SdToolApi/CreateAsset",
			},
		},
		Routes: []RouteConfig{{
			Name:               "seedance-domestic-assets",
			AllAPIKeys:         true,
			ChannelID:          59,
			Models:             []string{WildcardModel},
			UpstreamBaseURL:    upstream.URL,
			UpstreamAPIKeyEnv:  "SEEDANCE_DOMESTIC_KEY",
			UpstreamAuthHeader: "lmd-key",
			EnabledActions:     []string{"create_asset"},
		}},
	}
	require.NoError(t, SaveRoutesConfig(configPath, &config))
	t.Setenv("SEEDANCE_DOMESTIC_KEY", "supplier-secret")
	proxy, err := New(Config{
		RoutesConfigPath: configPath,
		TokenResolver: func(string) (*TokenIdentity, error) {
			return &TokenIdentity{ID: 17}, nil
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v3/open/CreateAsset", stringsReader(`{"name":"asset"}`))
	req.Header.Set("Authorization", "Bearer sk-client-secret")
	resp := httptest.NewRecorder()

	proxy.ServeHTTP(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)
	assert.JSONEq(t, `{"state":1,"data":{"id":"asset-1"}}`, resp.Body.String())
}

func TestProxyClassifiesAffinityResponses(t *testing.T) {
	originalDB := model.DB
	testDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "asset-affinity.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, testDB.AutoMigrate(&model.AssetChannelBinding{}))
	model.DB = testDB
	t.Cleanup(func() {
		sqlDB, dbErr := testDB.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		model.DB = originalDB
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("Set-Cookie", "PHPSESSID=upstream-session; Path=/; HttpOnly")
		switch r.URL.Path {
		case "/business-error":
			_, _ = w.Write([]byte(`{"state":1,"data":{"Code":"InvalidParameter.WidthTooSmall","Message":"Width must be between 300px and 6000px.","Data":null},"error":null}`))
		case "/missing-id":
			_, _ = w.Write([]byte(`{"state":1,"data":{"Message":"still processing"},"error":null}`))
		case "/upstream-error":
			w.Header().Set("X-Upstream-Trace", "trace-422")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_asset","message":"asset rejected"}}`))
		case "/success":
			_, _ = w.Write([]byte(`{"state":1,"data":{"Id":"asset-affinity-1"},"error":null}`))
		case "/array-success":
			_, _ = w.Write([]byte(`{"state":"OK","body":{"data":[{"assetId":"asset-affinity-1"},{"assetId":"asset-affinity-2"}]}}`))
		case "/official-business-error":
			_, _ = w.Write([]byte(`{"requestId":"req-error","state":"ERROR","errorCode":"AssetNotFound","errorMessage":"asset not found","body":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	proxy, err := New(Config{
		UpstreamBaseURL: upstream.URL,
		UpstreamAPIKey:  "supplier-secret",
		TokenLookup: func(string) (bool, error) {
			return true, nil
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name            string
		upstreamPath    string
		status          int
		body            string
		upstreamTrace   string
		namespaceHeader string
		expectedBinding bool
		expectedCount   int64
		affinityField   string
		affinityPathID  string
	}{
		{
			name:          "business error encoded as HTTP 200",
			upstreamPath:  "/business-error",
			status:        http.StatusOK,
			body:          `{"state":1,"data":{"Code":"InvalidParameter.WidthTooSmall","Message":"Width must be between 300px and 6000px.","Data":null},"error":null}`,
			affinityField: "data.Id",
		},
		{
			name:          "unknown successful response without identifier",
			upstreamPath:  "/missing-id",
			status:        http.StatusOK,
			body:          `{"state":1,"data":{"Message":"still processing"},"error":null}`,
			affinityField: "data.Id",
		},
		{
			name:          "non 2xx response passes through",
			upstreamPath:  "/upstream-error",
			status:        http.StatusUnprocessableEntity,
			body:          `{"error":{"code":"invalid_asset","message":"asset rejected"}}`,
			upstreamTrace: "trace-422",
			affinityField: "data.Id",
		},
		{
			name:            "successful response persists affinity",
			upstreamPath:    "/success",
			status:          http.StatusOK,
			body:            `{"state":1,"data":{"Id":"asset-affinity-1"},"error":null}`,
			namespaceHeader: "seedance-domestic",
			expectedBinding: true,
			expectedCount:   1,
			affinityField:   "data.Id",
		},
		{
			name:            "array response persists every asset affinity",
			upstreamPath:    "/array-success",
			status:          http.StatusOK,
			body:            `{"state":"OK","body":{"data":[{"assetId":"asset-affinity-1"},{"assetId":"asset-affinity-2"}]}}`,
			namespaceHeader: "seedance-domestic",
			expectedBinding: true,
			expectedCount:   2,
			affinityField:   "body.data.#.assetId",
		},
		{
			name:           "official business error passes through without path affinity",
			upstreamPath:   "/official-business-error",
			status:         http.StatusOK,
			body:           `{"requestId":"req-error","state":"ERROR","errorCode":"AssetNotFound","errorMessage":"asset not found","body":null}`,
			affinityPathID: "asset-from-path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, model.DB.Exec("DELETE FROM asset_channel_bindings").Error)
			req := httptest.NewRequest(http.MethodPost, "/api/v3/open/CreateAsset", stringsReader(`{"Name":"asset"}`))
			resp := httptest.NewRecorder()
			proxy.proxyRequest(resp, req, RouteMatch{
				ChannelID:          59,
				UpstreamBaseURL:    proxy.upstream,
				UpstreamAPIKey:     "supplier-secret",
				UpstreamMethod:     http.MethodPost,
				UpstreamPath:       tt.upstreamPath,
				AssetNamespaceID:   "seedance-domestic",
				UpstreamAuthHeader: "lmd-key",
			}, 17, tt.affinityField, tt.affinityPathID, nil)

			assert.Equal(t, tt.status, resp.Code)
			assert.JSONEq(t, tt.body, resp.Body.String())
			assert.Equal(t, tt.upstreamTrace, resp.Header().Get("X-Upstream-Trace"))
			assert.Equal(t, tt.namespaceHeader, resp.Header().Get("X-New-Api-Asset-Namespace"))
			assert.Empty(t, resp.Header().Values("Set-Cookie"))

			var count int64
			require.NoError(t, model.DB.Model(&model.AssetChannelBinding{}).Count(&count).Error)
			if tt.expectedBinding {
				assert.Equal(t, tt.expectedCount, count)
				var binding model.AssetChannelBinding
				require.NoError(t, model.DB.Where("external_id = ?", "asset-affinity-1").First(&binding).Error)
				assert.Equal(t, "seedance-domestic", binding.NamespaceID)
				assert.Equal(t, 59, binding.ChannelID)
				assert.Equal(t, 17, binding.TokenID)
			} else {
				assert.Zero(t, count)
			}
		})
	}
}

func TestProxyRejectsInvalidInputs(t *testing.T) {
	proxy, err := New(Config{
		UpstreamBaseURL: "http://example.com",
		UpstreamAPIKey:  "upstream-key",
		TokenLookup: func(key string) (bool, error) {
			return key == "valid", nil
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name   string
		method string
		path   string
		auth   string
		status int
	}{
		{name: "missing key", method: http.MethodGet, path: "/api/v3/ark/assets", status: http.StatusUnauthorized},
		{name: "unknown key", method: http.MethodGet, path: "/api/v3/ark/assets", auth: "Bearer sk-missing", status: http.StatusUnauthorized},
		{name: "wrong method", method: http.MethodPatch, path: "/api/v3/ark/assets", auth: "Bearer sk-valid", status: http.StatusMethodNotAllowed},
		{name: "unknown path", method: http.MethodGet, path: "/api/v3/ark/not-assets", auth: "Bearer sk-valid", status: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			resp := httptest.NewRecorder()

			proxy.ServeHTTP(resp, req)

			assert.Equal(t, tt.status, resp.Code)
			assert.Equal(t, "application/json", resp.Header().Get("Content-Type"))
		})
	}
}

func TestProxyReturnsDatabaseError(t *testing.T) {
	proxy, err := New(Config{
		UpstreamBaseURL: "http://example.com",
		UpstreamAPIKey:  "upstream-key",
		TokenLookup: func(_ string) (bool, error) {
			return false, errors.New("database unavailable")
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v3/ark/assets", nil)
	req.Header.Set("Authorization", "Bearer sk-valid")
	resp := httptest.NewRecorder()

	proxy.ServeHTTP(resp, req)

	assert.Equal(t, http.StatusInternalServerError, resp.Code)
}

func TestResolveTokenInDatabaseRequiresUsableTokenAndEnabledOwner(t *testing.T) {
	tempDir := t.TempDir()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")
	originalSQLitePath := common.SQLitePath
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	t.Cleanup(func() {
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
		common.SQLitePath = originalSQLitePath
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		if model.DB != nil {
			_ = model.CloseDB()
		}
	})

	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	common.SQLitePath = filepath.Join(tempDir, "new-api.db")
	common.IsMasterNode = true
	common.RedisEnabled = false
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB

	enabledUser := model.User{
		Username: "enabled-owner",
		Password: "test-password",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		AffCode:  "enabled-owner-code",
	}
	disabledUser := model.User{
		Username: "disabled-owner",
		Password: "test-password",
		Status:   common.UserStatusDisabled,
		Group:    "default",
		AffCode:  "disabled-owner-code",
	}
	require.NoError(t, model.DB.Create(&enabledUser).Error)
	require.NoError(t, model.DB.Create(&disabledUser).Error)

	expiredAt := common.GetTimestamp() - 100
	tokens := []model.Token{
		{UserId: enabledUser.Id, Key: "valid", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
		{UserId: enabledUser.Id, Key: "unlimited", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true},
		{UserId: enabledUser.Id, Key: "disabled", Status: common.TokenStatusDisabled, ExpiredTime: -1, RemainQuota: 100},
		{UserId: enabledUser.Id, Key: "expired-status", Status: common.TokenStatusExpired, ExpiredTime: -1, RemainQuota: 100},
		{UserId: enabledUser.Id, Key: "expired-time", Status: common.TokenStatusEnabled, ExpiredTime: expiredAt, RemainQuota: 100},
		{UserId: enabledUser.Id, Key: "empty-quota", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 0},
		{UserId: disabledUser.Id, Key: "disabled-owner", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
		{UserId: enabledUser.Id, Key: "deleted", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
	}
	for i := range tokens {
		require.NoError(t, model.DB.Create(&tokens[i]).Error)
	}
	require.NoError(t, model.DB.Delete(&tokens[7]).Error)

	for _, key := range []string{"valid", "unlimited"} {
		identity, err := ResolveTokenInDatabase(key)
		require.NoError(t, err)
		require.NotNil(t, identity)
		assert.Equal(t, tokens[map[string]int{"valid": 0, "unlimited": 1}[key]].Id, identity.ID)
	}

	for _, key := range []string{
		"disabled",
		"expired-status",
		"expired-time",
		"empty-quota",
		"disabled-owner",
		"missing",
		"deleted",
	} {
		identity, err := ResolveTokenInDatabase(key)
		require.NoError(t, err)
		assert.Nil(t, identity, key)
	}
}

func stringsReader(s string) io.Reader {
	return strings.NewReader(s)
}
