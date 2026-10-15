package model

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCNIntegrityWireVectors(t *testing.T) {
	sharedBody, err := os.ReadFile("testdata/cn-task/integrity-v2-shared-jcs-vectors.json")
	require.NoError(t, err)
	var shared struct {
		Vectors []struct {
			Name      string `json:"name"`
			Input     string `json:"inputJson"`
			Canonical string `json:"canonical"`
		} `json:"vectors"`
	}
	require.NoError(t, common.Unmarshal(sharedBody, &shared))
	for _, vector := range shared.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			canonical, err := CNIntegrityCanonicalJSON([]byte(vector.Input))
			require.NoError(t, err)
			assert.Equal(t, vector.Canonical, string(canonical))
		})
	}
	body, err := os.ReadFile("testdata/cn-task/integrity-v2-jcs-vectors.json")
	require.NoError(t, err)
	var fixture struct {
		Vectors []struct {
			ID              string `json:"id"`
			InputBase64     string `json:"inputBase64"`
			CanonicalBase64 string `json:"canonicalBase64"`
			SHA256          string `json:"sha256"`
		} `json:"vectors"`
	}
	require.NoError(t, common.Unmarshal(body, &fixture))
	require.NotEmpty(t, fixture.Vectors)
	for _, vector := range fixture.Vectors {
		t.Run(vector.ID, func(t *testing.T) {
			raw, err := base64.StdEncoding.DecodeString(vector.InputBase64)
			require.NoError(t, err)
			expected, err := base64.StdEncoding.DecodeString(vector.CanonicalBase64)
			require.NoError(t, err)
			actual, err := CNIntegrityCanonicalJSON(raw)
			require.NoError(t, err)
			assert.Equal(t, expected, actual)
			hash, err := CNIntegrityBodyHash(raw)
			require.NoError(t, err)
			assert.Equal(t, vector.SHA256, hash)
		})
	}
	chain, lineage, err := CNIntegrityNativeIdentity("test-cn", 196, "wallet_reserve", "request-A")
	require.NoError(t, err)
	assert.Equal(t, "7b4865805493268eb6823b6acd230eeb192c36646708366006c3d5cfb93b12d2", chain)
	assert.Equal(t, "2943b021de504d6c5b33929231fb000ea7d26c1e3487061c6ef7dd2f6b6b0497", lineage)
	finalChain, finalLineage, err := CNIntegrityNativeIdentity("test-cn", 196, "wallet_final", "request-A")
	require.NoError(t, err)
	assert.Equal(t, chain, finalChain)
	assert.Equal(t, lineage, finalLineage)
	_, _, err = CNIntegrityNativeIdentity("test-cn", 196, "source_switch", "197")
	require.Error(t, err)
	for _, invalid := range []string{`{"a":1,"a":2}`, `{"s":"\ud800"}`, `{"n":1e9999}`} {
		_, err := CNIntegrityCanonicalJSON([]byte(invalid))
		require.Error(t, err)
	}
}
