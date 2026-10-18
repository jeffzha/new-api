package hwdramaproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVolcengineAssetSigning(t *testing.T) {
	const body = `{"PageNumber":1,"PageSize":10,"Filter":{"GroupType":"AIGC"}}`
	req := httptest.NewRequest(http.MethodPost, "https://aimodel.szhtp.com:443/api/ry/v3?Version=2024-01-01&Action=ListAssetGroups", strings.NewReader(body))
	req.Header.Set("X-Security-Token", "client-session")
	now := time.Date(2026, 10, 16, 12, 5, 6, 0, time.FixedZone("CST", 8*60*60))
	require.NoError(t, signVolcengineAssetRequest(req, "AKIDEXAMPLE", "SECRETEXAMPLE", now))
	assert.Equal(t, "20261016T040506Z", req.Header.Get("X-Date"))
	assert.Equal(t, "06459f66690598092395f8271275a9372df0fe802622084d7b771deffab67d34", req.Header.Get("X-Content-Sha256"))
	// Independently calculated with Node crypto using the published Ark signing algorithm.
	assert.Equal(t, "HMAC-SHA256 Credential=AKIDEXAMPLE/20261016/cn-beijing/ark/request, SignedHeaders=content-type;host;x-content-sha256;x-date, Signature=3d8455ad543bda455bba2e8b206affab2debf04b5fa8b8711248eccf5b04da4d", req.Header.Get("Authorization"))
	assert.Equal(t, "Action=ListAssetGroups&Version=2024-01-01", req.URL.RawQuery)
	assert.Empty(t, req.Header.Get("X-Security-Token"))
	actual, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, body, string(actual))
	replay, err := req.GetBody()
	require.NoError(t, err)
	defer replay.Close()
	actual, err = io.ReadAll(replay)
	require.NoError(t, err)
	assert.Equal(t, body, string(actual))
}

func TestVolcengineAssetSigningRejectsUnsafeRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, query, body, ak, sk string
	}{
		{"missing credentials", "POST", "Action=GetAsset&Version=2024-01-01", `{}`, "", "secret"},
		{"wrong method", "GET", "Action=GetAsset&Version=2024-01-01", `{}`, "access", "secret"},
		{"private action", "POST", "Action=ListSplitBillDetail&Version=2024-01-01", `{}`, "access", "secret"},
		{"duplicate action", "POST", "Action=GetAsset&Action=CreateAsset&Version=2024-01-01", `{}`, "access", "secret"},
		{"extra query", "POST", "Action=GetAsset&Version=2024-01-01&key=client", `{}`, "access", "secret"},
		{"wrong version", "POST", "Action=GetAsset&Version=2025-01-01", `{}`, "access", "secret"},
		{"oversized body", "POST", "Action=GetAsset&Version=2024-01-01", strings.Repeat("x", (4<<20)+1), "access", "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "https://example.com/api/v3?"+tc.query, strings.NewReader(tc.body))
			require.Error(t, signVolcengineAssetRequest(req, tc.ak, tc.sk, time.Now()))
		})
	}
}

func TestVolcengineAssetRouteGuards(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*RoutesConfig)
		want string
	}{
		{"all customers", func(c *RoutesConfig) { c.Routes[0].AllAPIKeys = true }, "does not allow all_api_keys"},
		{"multiple tokens", func(c *RoutesConfig) { c.Routes[0].APIKeyIDs = []int{7, 8} }, "exactly one api_key_id"},
		{"unsupported group isolation", func(c *RoutesConfig) { c.Routes[0].AssetScopeID = "group-a" }, "no asset_scope_id"},
		{"plaintext transport", func(c *RoutesConfig) { c.Routes[0].UpstreamBaseURL = "http://supplier.example" }, "HTTPS"},
		{"missing credentials", func(c *RoutesConfig) { c.Routes[0].UpstreamSecretKeyEnv = "" }, "upstream_secret_key_env"},
		{"missing namespace", func(c *RoutesConfig) { c.Routes[0].AssetNamespaceID = "" }, "asset_namespace_id"},
		{"private action", func(c *RoutesConfig) {
			a := c.Actions["create_asset"]
			a.UpstreamAction = "ListSplitBillDetail"
			c.Actions["create_asset"] = a
		}, "supported Volcengine asset action"},
		{"wrong method override", func(c *RoutesConfig) {
			c.Routes[0].UpstreamActionOverrides = map[string]UpstreamActionConfig{"create_asset": {UpstreamMethod: "GET"}}
		}, "require POST"},
		{"mixed material endpoints", func(c *RoutesConfig) {
			a := c.Actions["create_asset"]
			a.DownstreamPath = "/api/v3/open/GetAsset"
			a.UpstreamAction = "GetAsset"
			a.DefaultUpstreamPath = "/api/v3"
			c.Actions["get_asset"] = a
			c.Routes[0].EnabledActions = append(c.Routes[0].EnabledActions, "get_asset")
		}, "conflicting material endpoints"},
		{"shared credentials", func(c *RoutesConfig) {
			r := c.Routes[0]
			r.Name = "other-customer"
			r.APIKeyIDs = []int{8}
			r.UpstreamAccessKeyEnv = "ALIAS_AK"
			c.Routes = append(c.Routes, r)
		}, "reuses Volcengine credentials"},
		{"conflicting channel", func(c *RoutesConfig) {
			r := c.Routes[0]
			r.Name = "other-channel"
			r.ChannelID++
			r.Models = []string{"other-model"}
			c.Routes = append(c.Routes, r)
		}, "reuses Volcengine credentials"},
		{"one token with different credentials", func(c *RoutesConfig) {
			r := c.Routes[0]
			r.Name = "other-account"
			r.UpstreamAccessKeyEnv = "OTHER_AK"
			r.Models = []string{"other-model"}
			c.Routes = append(c.Routes, r)
		}, "conflicting upstream accounts or channels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := volcengineRoutesConfigForTest("https://aimodel.szhtp.com")
			tc.edit(config)
			err := config.Validate(func(name string) string {
				if name == "OTHER_AK" {
					return "other-test-credential"
				}
				return "same-test-credential"
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestProxyRoutesVolcengineAssetRequests(t *testing.T) {
	calls := 0
	redirect := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/ry/v3", r.URL.Path)
		assert.Equal(t, "Action=CreateAsset&Version=2024-01-01", r.URL.RawQuery)
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "HMAC-SHA256 Credential=test-access/"))
		for _, header := range []string{"Cookie", "X-Api-Key", "New-Api-User", "X-Security-Token"} {
			assert.Empty(t, r.Header.Get(header), header)
		}
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"GroupId":"group-test","AssetType":"Image","URL":"https://example.com/image.png"}`, string(body))
		if redirect {
			http.Redirect(w, r, "/redirect-target", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte(`{"Result":{"Status":"Processing"}}`))
	}))
	defer upstream.Close()
	config := volcengineRoutesConfigForTest(upstream.URL)
	path := filepath.Join(t.TempDir(), "routes.yml")
	require.NoError(t, SaveRoutesConfig(path, config))
	t.Setenv("VOLC_TEST_AK", "test-access")
	t.Setenv("VOLC_TEST_SK", "test-secret")
	tokenID := 7
	proxy, err := New(Config{RoutesConfigPath: path, TokenResolver: func(string) (*TokenIdentity, error) {
		return &TokenIdentity{ID: tokenID}, nil
	}})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, query     string
		tokenID, status int
		redirect        bool
	}{
		{"allowed token", "", 7, http.StatusOK, false},
		{"another token", "", 8, http.StatusNotFound, false},
		{"client query override", "?Action=DeleteAsset", 7, http.StatusBadRequest, false},
		{"signed redirect rejected", "", 7, http.StatusBadGateway, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenID, redirect = tc.tokenID, tc.redirect
			req := httptest.NewRequest(http.MethodPost, "/api/v3/open/CreateAsset"+tc.query, strings.NewReader(`{"GroupId":"group-test","AssetType":"Image","URL":"https://example.com/image.png"}`))
			req.Header.Set("Authorization", "Bearer sk-clientkey")
			for _, header := range []string{"Cookie", "X-Api-Key", "New-Api-User", "X-Security-Token"} {
				req.Header.Set(header, "must-not-forward")
			}
			before := calls
			resp := httptest.NewRecorder()
			proxy.ServeHTTP(resp, req)
			assert.Equal(t, tc.status, resp.Code, resp.Body.String())
			if tc.status == http.StatusOK || tc.redirect {
				assert.Equal(t, before+1, calls)
			} else {
				assert.Equal(t, before, calls)
			}
		})
	}
}

func volcengineRoutesConfigForTest(baseURL string) *RoutesConfig {
	return volcengineAssetConfig(baseURL)
}

func volcengineAssetConfig(baseURL string) *RoutesConfig {
	return &RoutesConfig{
		Version: 1,
		Actions: map[string]ActionConfig{"create_asset": {
			DownstreamMethod: "POST", DownstreamPath: "/api/v3/open/CreateAsset",
			DefaultUpstreamMethod: "POST", DefaultUpstreamPath: "/api/ry/v3", UpstreamAction: "CreateAsset",
		}},
		Routes: []RouteConfig{{
			Name: "szhtp-test-assets", APIKeyIDs: []int{7}, ChannelID: 16, Models: []string{WildcardModel},
			UpstreamBaseURL: baseURL, UpstreamAuthType: UpstreamAuthTypeVolcengineAKSK,
			UpstreamAccessKeyEnv: "VOLC_TEST_AK", UpstreamSecretKeyEnv: "VOLC_TEST_SK",
			AssetNamespaceID: "szhtp-d-test", EnabledActions: []string{"create_asset"},
		}},
	}
}
