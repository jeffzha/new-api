package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestCheckNotificationLimitUsesMemoryStoreBeforeRedisClientInitialization(t *testing.T) {
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	previousLimit := constant.NotifyLimitCount
	previousStore := make(map[any]any)
	notifyLimitStore.Range(func(key, value any) bool { previousStore[key] = value; return true })
	common.RedisEnabled, common.RDB = true, nil
	constant.NotifyLimitCount = 2
	notifyLimitStore.Clear()
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = previousEnabled, previousClient
		constant.NotifyLimitCount = previousLimit
		notifyLimitStore.Clear()
		for key, value := range previousStore {
			notifyLimitStore.Store(key, value)
		}
	})

	allowed, err := CheckNotificationLimit(71, "quota_exceed")
	require.NoError(t, err)
	require.True(t, allowed)
}
