package mobilecloudseedance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSDKClientManagerCachesByCredentialFingerprint(t *testing.T) {
	now := time.Unix(1000, 0)
	created := 0
	manager := newSDKClientManager(func(_, _, _ string) (sdkClient, error) {
		created++
		return &fakeSDKClient{createTaskID: "task"}, nil
	})
	manager.now = func() time.Time { return now }

	first, err := manager.Get("https://gateway.example.com/api/v3", "key-a", ModelName)
	require.NoError(t, err)
	second, err := manager.Get("https://gateway.example.com/api/v3", "key-a", ModelName)
	require.NoError(t, err)
	assert.Same(t, first, second)
	assert.Equal(t, 1, created)

	_, err = manager.Get("https://gateway.example.com/api/v3", "key-b", ModelName)
	require.NoError(t, err)
	assert.Equal(t, 2, created)

	now = now.Add(sdkClientTTL + time.Second)
	_, err = manager.Get("https://gateway.example.com/api/v3", "key-a", ModelName)
	require.NoError(t, err)
	assert.Equal(t, 3, created)
}
