package service

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestCheckNotificationLimitUsesMemoryStoreBeforeRedisClientInitialization(t *testing.T) {
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	previousLimit := constant.NotifyLimitCount
	previousStore := notifyLimitStore
	common.RedisEnabled, common.RDB = true, nil
	constant.NotifyLimitCount = 2
	notifyLimitStore = sync.Map{}
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = previousEnabled, previousClient
		constant.NotifyLimitCount = previousLimit
		notifyLimitStore = previousStore
	})

	allowed, err := CheckNotificationLimit(71, "quota_exceed")
	require.NoError(t, err)
	require.True(t, allowed)
}
