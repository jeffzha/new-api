package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetChannelBindingPreservesTokenAndChannelAffinity(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&AssetChannelBinding{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM asset_channel_bindings") })

	require.NoError(t, UpsertAssetChannelBinding("asset-1", "seedance-cn", 59, 101))
	require.NoError(t, UpsertAssetChannelBinding("asset-2", "seedance-cn", 59, 101))

	channelID, found, err := ResolveAssetChannelBinding([]string{"asset-1", "asset-1", "asset-2"}, 101)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 59, channelID)

	_, _, err = ResolveAssetChannelBinding([]string{"asset-1"}, 202)
	assert.ErrorContains(t, err, "another API token")

	err = UpsertAssetChannelBinding("asset-1", "seedance-cn", 60, 101)
	assert.ErrorContains(t, err, "already bound")
}

func TestAssetChannelBindingRejectsPartialAndMixedReferences(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&AssetChannelBinding{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM asset_channel_bindings") })
	require.NoError(t, UpsertAssetChannelBinding("asset-a", "seedance-cn-a", 59, 101))
	require.NoError(t, UpsertAssetChannelBinding("asset-b", "seedance-cn-b", 60, 101))

	_, _, err := ResolveAssetChannelBinding([]string{"asset-a", "missing"}, 101)
	assert.ErrorContains(t, err, "no channel binding")

	_, _, err = ResolveAssetChannelBinding([]string{"asset-a", "asset-b"}, 101)
	assert.ErrorContains(t, err, "different upstream channels")
}

func TestScopedAssetBindingAllowsTokensInOneScopeAndRejectsOtherScopes(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(
		&AssetChannelBinding{},
		&AssetScopeTokenBinding{},
		&AssetGroupScopeBinding{},
		&AssetAuthSessionBinding{},
	))
	t.Cleanup(func() {
		DB.Exec("DELETE FROM asset_channel_bindings")
		DB.Exec("DELETE FROM asset_scope_token_bindings")
		DB.Exec("DELETE FROM asset_group_scope_bindings")
		DB.Exec("DELETE FROM asset_auth_session_bindings")
	})

	require.NoError(t, UpsertAssetScopeTokenBinding("mobile-cloud", "customer-a", 60, 101))
	require.NoError(t, UpsertAssetScopeTokenBinding("mobile-cloud", "customer-a", 60, 102))
	require.NoError(t, UpsertAssetScopeTokenBinding("mobile-cloud", "customer-b", 60, 201))
	require.NoError(t, UpsertAssetGroupScopeBinding("group-a", "mobile-cloud", "customer-a", 60, 101))
	require.NoError(t, UpsertScopedAssetChannelBinding("asset-a", "group-a", "mobile-cloud", "customer-a", 60, 101))

	channelID, found, err := ResolveAssetChannelBinding([]string{"asset-a"}, 102)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 60, channelID)

	_, _, err = ResolveAssetChannelBinding([]string{"asset-a"}, 201)
	assert.ErrorContains(t, err, "another asset scope")

	err = UpsertAssetScopeTokenBinding("mobile-cloud", "customer-b", 60, 101)
	assert.ErrorContains(t, err, "already assigned")
}

func TestAssetAuthSessionStoresOnlyHashAndEnforcesScope(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&AssetAuthSessionBinding{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM asset_auth_session_bindings") })

	require.NoError(t, UpsertAssetAuthSessionBinding(
		"sensitive-byted-token",
		"mobile-cloud",
		"customer-a",
		60,
		101,
		300,
	))

	var stored AssetAuthSessionBinding
	require.NoError(t, DB.First(&stored).Error)
	assert.NotEqual(t, "sensitive-byted-token", stored.TokenHash)
	assert.Len(t, stored.TokenHash, 64)

	allowed, err := AssetAuthSessionBelongsToScope("sensitive-byted-token", "mobile-cloud", "customer-a", 60)
	require.NoError(t, err)
	assert.True(t, allowed)

	allowed, err = AssetAuthSessionBelongsToScope("sensitive-byted-token", "mobile-cloud", "customer-b", 60)
	require.NoError(t, err)
	assert.False(t, allowed)
}
