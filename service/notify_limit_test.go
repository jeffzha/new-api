package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestCheckNotificationLimitUsesMemoryStoreBeforeRedisClientInitialization(t *testing.T) {
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	previousLimit := constant.NotifyLimitCount
	key := "71:quota_exceed:" + time.Now().Format("2006010215")
	previous, existed := notifyLimitStore.LoadAndDelete(key)
	common.RedisEnabled, common.RDB = true, nil
	constant.NotifyLimitCount = 2
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = previousEnabled, previousClient
		constant.NotifyLimitCount = previousLimit
		notifyLimitStore.Delete(key)
		if existed {
			notifyLimitStore.Store(key, previous)
		}
	})

	allowed, err := CheckNotificationLimit(71, "quota_exceed")
	require.NoError(t, err)
	require.True(t, allowed)
}
