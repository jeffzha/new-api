package hwdramaproxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutesConfigValidateRejectsDuplicateActionEndpoint(t *testing.T) {
	config := sampleRoutesConfig()
	config.Actions["duplicate"] = ActionConfig{
		DownstreamMethod:      "POST",
		DownstreamPath:        "/api/v3/open/CreateAsset",
		DefaultUpstreamMethod: "POST",
		DefaultUpstreamPath:   "/v1/assets/create",
	}

	err := config.Validate(func(string) string { return "upstream-key" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "share downstream endpoint")
}

func TestRoutesConfigValidateRejectsDuplicateRouteMatch(t *testing.T) {
	config := sampleRoutesConfig()
	config.Routes = append(config.Routes, RouteConfig{
		Name:              "duplicate-route",
		APIKeyIDs:         []int{7},
		ChannelID:         4,
		Models:            []string{"doubao-seedance-2-0-fast-filter-off"},
		UpstreamBaseURL:   "https://other.example.com",
		UpstreamAPIKeyEnv: "HWD_OTHER_API_KEY",
		EnabledActions:    []string{"seedance_open_create_asset"},
	})

	err := config.Validate(func(string) string { return "upstream-key" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "both match")
}

func TestRoutesConfigValidateRejectsFullUpstreamURL(t *testing.T) {
	config := sampleRoutesConfig()
	config.Routes[0].UpstreamActionOverrides = map[string]UpstreamActionConfig{
		"seedance_open_create_asset": {
			UpstreamMethod: "POST",
			UpstreamPath:   "https://evil.example.com/v1/assets/create",
		},
	}

	err := config.Validate(func(string) string { return "upstream-key" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a path")
}

func TestRoutesConfigRequiresNamespaceForAffinityResponse(t *testing.T) {
	config := sampleRoutesConfig()
	action := config.Actions["seedance_open_create_asset"]
	action.AffinityResponseField = "data.id"
	config.Actions["seedance_open_create_asset"] = action

	err := config.Validate(func(string) string { return "upstream-key" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "asset_namespace_id is required")
}

func TestRoutesConfigValidatesAffinityPathParameter(t *testing.T) {
	config := sampleRoutesConfig()
	action := config.Actions["seedance_open_create_asset"]
	action.AffinityPathParam = "asset_id"
	config.Actions["seedance_open_create_asset"] = action

	err := config.Validate(func(string) string { return "upstream-key" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not present in downstream_path")
}

func TestRuntimeRouterCarriesMobileCloudAKSKAuthentication(t *testing.T) {
	config := mobileCloudRoutesConfigForTest()
	router, err := BuildRuntimeRouter(config, func(key string) string {
		return map[string]string{
			"MOBILE_CLOUD_AK": "access-key",
			"MOBILE_CLOUD_SK": "secret-key",
		}[key]
	})
	require.NoError(t, err)
	action, decision := router.LookupAction(http.MethodGet, "/api/openapi-maas/exp/aicc/v2/asset/asset-1")
	require.True(t, decision.MethodAllowed)

	match, ok, err := router.Match(16, "", action)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, UpstreamAuthTypeMobileCloudAKSK, match.UpstreamAuthType)
	assert.Equal(t, "access-key", match.UpstreamAccessKey)
	assert.Equal(t, "secret-key", match.UpstreamSecretKey)
	assert.Empty(t, match.UpstreamAPIKey)
	assert.Equal(t, "/api/openapi-maas/exp/aicc/v2/asset/asset-1", match.UpstreamPath)
	assert.Equal(t, "customer-a", match.AssetScopeID)
}

func TestRoutesConfigAllowsMultipleTokensInOneMobileCloudAssetScope(t *testing.T) {
	config := mobileCloudRoutesConfigForTest()
	config.Routes[0].APIKeyIDs = []int{16, 17}

	err := config.Validate(func(string) string { return "credential" })

	require.NoError(t, err)
}

func TestRoutesConfigRejectsAllAPIKeysForMobileCloudAssetScope(t *testing.T) {
	config := mobileCloudRoutesConfigForTest()
	config.Routes[0].APIKeyIDs = nil
	config.Routes[0].AllAPIKeys = true

	err := config.Validate(func(string) string { return "credential" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not allow all_api_keys")
}

func TestRoutesConfigRequiresHTTPSForMobileCloudCredentials(t *testing.T) {
	config := mobileCloudRoutesConfigForTest()
	config.Routes[0].UpstreamBaseURL = "http://ecloud.10086.cn"

	err := config.Validate(func(string) string { return "credential" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an HTTPS upstream")
}

func TestRoutesConfigAllowsOneMobileCloudAccountAcrossIsolatedScopes(t *testing.T) {
	config := mobileCloudRoutesConfigForTest()
	secondRoute := config.Routes[0]
	secondRoute.Name = "second-customer"
	secondRoute.APIKeyIDs = []int{17}
	secondRoute.Models = []string{"another-model"}
	secondRoute.AssetScopeID = "customer-b"
	secondRoute.UpstreamAccessKeyEnv = "SECOND_MOBILE_CLOUD_AK"
	secondRoute.UpstreamSecretKeyEnv = "SECOND_MOBILE_CLOUD_SK"
	config.Routes = append(config.Routes, secondRoute)

	err := config.Validate(func(string) string { return "same-credential" })

	require.NoError(t, err)
}

func TestRoutesConfigRejectsOneTokenInConflictingMobileCloudScopes(t *testing.T) {
	config := mobileCloudRoutesConfigForTest()
	secondRoute := config.Routes[0]
	secondRoute.Name = "conflicting-customer"
	secondRoute.Models = []string{"another-model"}
	secondRoute.AssetScopeID = "customer-b"
	config.Routes = append(config.Routes, secondRoute)

	err := config.Validate(func(string) string { return "credential" })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting asset scopes")
}

func TestRoutesConfigRejectsPrivateSeedanceBillingEndpoint(t *testing.T) {
	t.Run("default path", func(t *testing.T) {
		config := sampleRoutesConfig()
		action := config.Actions["seedance_open_create_asset"]
		action.DefaultUpstreamPath = "/asset/SdToolApi/ListSplitBillDetail"
		config.Actions["seedance_open_create_asset"] = action

		err := config.Validate(func(string) string { return "upstream-key" })

		require.Error(t, err)
		assert.Contains(t, err.Error(), "private Seedance billing endpoint")
	})

	t.Run("route override", func(t *testing.T) {
		config := sampleRoutesConfig()
		config.Routes[0].UpstreamActionOverrides = map[string]UpstreamActionConfig{
			"seedance_open_create_asset": {
				UpstreamPath: "/ListSplitBillDetail",
			},
		}

		err := config.Validate(func(string) string { return "upstream-key" })

		require.Error(t, err)
		assert.Contains(t, err.Error(), "private Seedance billing endpoint")
	})

	t.Run("base URL path", func(t *testing.T) {
		config := sampleRoutesConfig()
		config.Routes[0].UpstreamBaseURL = "https://supplier.example.com/asset/SdToolApi/ListSplitBillDetail"
		action := config.Actions["seedance_open_create_asset"]
		action.DefaultUpstreamPath = "/"
		config.Actions["seedance_open_create_asset"] = action

		err := config.Validate(func(string) string { return "upstream-key" })

		require.Error(t, err)
		assert.Contains(t, err.Error(), "private Seedance billing endpoint")
	})
}

func TestRuntimeRouterCarriesCustomAuthenticationAndAffinityNamespace(t *testing.T) {
	config := sampleRoutesConfig()
	config.Routes[0].UpstreamAuthHeader = "lmd-key"
	config.Routes[0].UpstreamAuthPrefix = ""
	config.Routes[0].AssetNamespaceID = "seedance-cn"
	action := config.Actions["seedance_open_create_asset"]
	action.AffinityResponseField = "data.id"
	config.Actions["seedance_open_create_asset"] = action
	router, err := BuildRuntimeRouter(config, func(string) string { return "supplier-secret" })
	require.NoError(t, err)
	actionMatch, decision := router.LookupAction("POST", "/api/v3/open/CreateAsset")
	require.True(t, decision.MethodAllowed)

	match, ok, err := router.Match(7, "doubao-seedance-2-0-fast-filter-off", actionMatch)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "lmd-key", match.UpstreamAuthHeader)
	assert.Empty(t, match.UpstreamAuthPrefix)
	assert.Equal(t, "seedance-cn", match.AssetNamespaceID)
}

func TestRuntimeRouterMatchesExactModelBeforeWildcard(t *testing.T) {
	config := sampleRoutesConfig()
	config.Routes = append(config.Routes, RouteConfig{
		Name:              "wildcard-route",
		APIKeyIDs:         []int{7},
		ChannelID:         4,
		Models:            []string{WildcardModel},
		UpstreamBaseURL:   "https://wildcard.example.com",
		UpstreamAPIKeyEnv: "HWD_WILDCARD_API_KEY",
		EnabledActions:    []string{"seedance_open_create_asset"},
	})
	router, err := BuildRuntimeRouter(config, func(string) string { return "upstream-key" })
	require.NoError(t, err)

	action, decision := router.LookupAction("POST", "/api/v3/open/CreateAsset")
	require.True(t, decision.MethodAllowed)
	match, ok, err := router.Match(7, "doubao-seedance-2-0-fast-filter-off", action)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "wetoken-seedance-oversea", match.RouteName)
	assert.Equal(t, "https://wetoken.ai", match.UpstreamBaseURL.String())
}

func TestRuntimeRouterAppliesUpstreamOverrideAndPathParams(t *testing.T) {
	config := &RoutesConfig{
		Version: 1,
		Actions: map[string]ActionConfig{
			"get_asset": {
				DownstreamMethod:      "GET",
				DownstreamPath:        "/api/v3/ark/assets/{asset_id}",
				DefaultUpstreamMethod: "GET",
				DefaultUpstreamPath:   "/api/v3/ark/assets/{asset_id}",
			},
		},
		Routes: []RouteConfig{
			{
				Name:              "supplier-x",
				APIKeyIDs:         []int{11},
				ChannelID:         8,
				Models:            []string{WildcardModel},
				UpstreamBaseURL:   "https://supplier.example.com",
				UpstreamAPIKeyEnv: "HWD_SUPPLIER_API_KEY",
				EnabledActions:    []string{"get_asset"},
				UpstreamActionOverrides: map[string]UpstreamActionConfig{
					"get_asset": {
						UpstreamMethod: "POST",
						UpstreamPath:   "/v1/assets/{asset_id}/query",
					},
				},
			},
		},
	}
	router, err := BuildRuntimeRouter(config, func(string) string { return "upstream-key" })
	require.NoError(t, err)

	action, decision := router.LookupAction("GET", "/api/v3/ark/assets/asset-123")
	require.True(t, decision.MethodAllowed)
	match, ok, err := router.Match(11, "", action)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "POST", match.UpstreamMethod)
	assert.Equal(t, "/v1/assets/asset-123/query", match.UpstreamPath)
}

func sampleRoutesConfig() *RoutesConfig {
	return &RoutesConfig{
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
				Name:              "wetoken-seedance-oversea",
				APIKeyIDs:         []int{7},
				ChannelID:         3,
				Models:            []string{"doubao-seedance-2-0-fast-filter-off"},
				UpstreamBaseURL:   "https://wetoken.ai",
				UpstreamAPIKeyEnv: "HWD_WETOKEN_API_KEY",
				EnabledActions:    []string{"seedance_open_create_asset"},
			},
		},
	}
}

func mobileCloudRoutesConfigForTest() *RoutesConfig {
	return &RoutesConfig{
		Version: 1,
		Actions: map[string]ActionConfig{
			"mobile_cloud_asset_get": {
				DownstreamMethod:      http.MethodGet,
				DownstreamPath:        "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}",
				DefaultUpstreamMethod: http.MethodGet,
				DefaultUpstreamPath:   "/api/openapi-maas/exp/aicc/v2/asset/{asset_id}",
				AffinityResponseField: "body.assetId",
				AffinityPathParam:     "asset_id",
				ScopeOperation:        ScopeOperationAssetGet,
				ScopePathParam:        "asset_id",
			},
		},
		Routes: []RouteConfig{
			{
				Name:                 "mobile-cloud-assets",
				APIKeyIDs:            []int{16},
				ChannelID:            5,
				Models:               []string{WildcardModel},
				UpstreamBaseURL:      "https://ecloud.10086.cn",
				UpstreamAuthType:     UpstreamAuthTypeMobileCloudAKSK,
				UpstreamAccessKeyEnv: "MOBILE_CLOUD_AK",
				UpstreamSecretKeyEnv: "MOBILE_CLOUD_SK",
				AssetNamespaceID:     "mobile-cloud",
				AssetScopeID:         "customer-a",
				EnabledActions:       []string{"mobile_cloud_asset_get"},
			},
		},
	}
}
