package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureKeyPairCreatesAndReusesMatchingKeys(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "private.pem")
	publicPath := filepath.Join(directory, "public.pem")

	require.NoError(t, ensureKeyPair(privatePath, publicPath))
	privateBefore, err := os.ReadFile(privatePath)
	require.NoError(t, err)
	publicBefore, err := os.ReadFile(publicPath)
	require.NoError(t, err)

	require.NoError(t, ensureKeyPair(privatePath, publicPath))
	privateAfter, err := os.ReadFile(privatePath)
	require.NoError(t, err)
	publicAfter, err := os.ReadFile(publicPath)
	require.NoError(t, err)
	assert.Equal(t, privateBefore, privateAfter)
	assert.Equal(t, publicBefore, publicAfter)
}

func TestEnsureKeyPairRejectsIncompletePair(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "private.pem")
	publicPath := filepath.Join(directory, "public.pem")
	require.NoError(t, ensureKeyPair(privatePath, publicPath))
	require.NoError(t, os.Remove(publicPath))

	assert.ErrorContains(t, ensureKeyPair(privatePath, publicPath), "incomplete")
}

func TestEnsureSecretCreatesAndReusesValidSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.key")
	require.NoError(t, ensureSecret(path))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	require.NoError(t, ensureSecret(path))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
