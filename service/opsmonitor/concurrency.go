package opsmonitor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"

	"github.com/go-redis/redis/v8"
)

type concurrencyScope struct {
	channelID int
	keyIndex  int
}

type concurrencyLease struct {
	id        string
	keys      []string
	tracked   bool
	localKeys []concurrencyScope
	local     bool
	stop      chan struct{}
	once      sync.Once
}

type localConcurrencyState struct {
	inUse   int
	waiting int
}

var (
	limitCache atomic.Value
	localMu    sync.Mutex
	localState = map[concurrencyScope]*localConcurrencyState{}
)

var acquireScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local expires = tonumber(ARGV[2])
local request_id = ARGV[3]
local capacity = tonumber(ARGV[4])
local queue_size = tonumber(ARGV[5])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZADD', KEYS[2], 'NX', now, request_id)
local waiting = redis.call('ZCARD', KEYS[2])
local rank = redis.call('ZRANK', KEYS[2], request_id)
local in_use = redis.call('ZCARD', KEYS[1])
if rank ~= false and rank < (capacity - in_use) then
  redis.call('ZREM', KEYS[2], request_id)
  redis.call('ZADD', KEYS[1], expires, request_id)
  redis.call('EXPIRE', KEYS[1], math.ceil((expires - now) / 1000) + 60)
  redis.call('EXPIRE', KEYS[2], math.ceil((expires - now) / 1000) + 60)
  return {1, in_use + 1, waiting - 1}
end
if queue_size >= 0 and waiting > queue_size then
  redis.call('ZREM', KEYS[2], request_id)
  return {-1, in_use, waiting - 1}
end
return {0, in_use, waiting}
`)

var observeScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local expires = tonumber(ARGV[2])
local request_id = ARGV[3]
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZADD', KEYS[1], expires, request_id)
redis.call('EXPIRE', KEYS[1], math.ceil((expires - now) / 1000) + 60)
return redis.call('ZCARD', KEYS[1])
`)

var releaseScript = redis.NewScript(`
for i, key in ipairs(KEYS) do
  redis.call('ZREM', key, ARGV[1])
end
return 1
`)

var renewScript = redis.NewScript(`
for i, key in ipairs(KEYS) do
  redis.call('ZADD', key, 'XX', ARGV[1], ARGV[2])
end
return 1
`)

func startConcurrency() {
	limitCache.Store(map[concurrencyScope]model.OpsConcurrencyLimit{})
	reloadConcurrencyLimits()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			reloadConcurrencyLimits()
		}
	}()
}

func ReloadConcurrencyLimits() {
	reloadConcurrencyLimits()
}

func reloadConcurrencyLimits() {
	limits, err := model.ListOpsConcurrencyLimits()
	if err != nil {
		common.SysError("load ops concurrency limits failed: " + err.Error())
		return
	}
	loaded := make(map[concurrencyScope]model.OpsConcurrencyLimit, len(limits))
	for _, limit := range limits {
		loaded[concurrencyScope{channelID: limit.ChannelID, keyIndex: limit.KeyIndex}] = limit
	}
	limitCache.Store(loaded)
}

func concurrencyLimit(channelID, keyIndex int) (model.OpsConcurrencyLimit, bool) {
	limits, _ := limitCache.Load().(map[concurrencyScope]model.OpsConcurrencyLimit)
	if limit, ok := limits[concurrencyScope{channelID: channelID, keyIndex: keyIndex}]; ok {
		return limit, true
	}
	limit, ok := limits[concurrencyScope{channelID: channelID, keyIndex: model.OpsConcurrencyAllKeys}]
	return limit, ok
}

func acquireConcurrency(ctx context.Context, userID, channelID, keyIndex int, requestID string) (*concurrencyLease, error) {
	if requestID == "" {
		requestID = common.GetRandomString(32)
	}
	setting := ops_monitor_setting.Get()
	limit, hasLimit := concurrencyLimit(channelID, keyIndex)
	enforce := setting.ConcurrencyEnforcementEnabled && hasLimit && limit.Enabled && limit.MaxConcurrency > 0
	leaseSeconds := setting.DefaultLeaseSeconds
	if hasLimit && limit.LeaseSeconds > 0 {
		leaseSeconds = limit.LeaseSeconds
	}
	if common.RedisEnabled && common.RDB != nil {
		lease, err := acquireRedisConcurrency(ctx, userID, channelID, keyIndex, requestID, leaseSeconds, enforce, limit)
		if err == nil {
			return lease, err
		}
		if enforce && !setting.ConcurrencyFailOpen {
			return nil, err
		}
		common.SysError("ops concurrency Redis failed open: " + err.Error())
		return acquireLocalConcurrency(ctx, channelID, keyIndex, requestID, false, limit)
	}
	return acquireLocalConcurrency(ctx, channelID, keyIndex, requestID, enforce, limit)
}

func acquireRedisConcurrency(ctx context.Context, userID, channelID, keyIndex int, requestID string, leaseSeconds int, enforce bool, limit model.OpsConcurrencyLimit) (*concurrencyLease, error) {
	channelKey := concurrencyLeaseKey(channelID, model.OpsConcurrencyAllKeys)
	keyKey := concurrencyLeaseKey(channelID, keyIndex)
	keys := []string{channelKey}
	if keyKey != channelKey {
		keys = append(keys, keyKey)
	}
	if userID > 0 {
		keys = append(keys, fmt.Sprintf("ops:concurrency:leases:user:%d", userID))
		_ = common.RDB.SAdd(ctx, "ops:concurrency:active_users", userID).Err()
	}

	if enforce {
		scopeKey := concurrencyLeaseKey(channelID, limit.KeyIndex)
		waiterKey := concurrencyWaiterKey(channelID, limit.KeyIndex)
		timeout := time.Duration(limit.QueueTimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		deadline := time.Now().Add(timeout)
		for {
			now := time.Now()
			expiresAt := now.Add(time.Duration(leaseSeconds) * time.Second).UnixMilli()
			result, err := acquireScript.Run(ctx, common.RDB, []string{scopeKey, waiterKey}, now.UnixMilli(), expiresAt, requestID, limit.MaxConcurrency, limit.QueueSize).Result()
			if err != nil {
				return nil, err
			}
			values, ok := result.([]interface{})
			if !ok || len(values) == 0 {
				return nil, errors.New("invalid concurrency script response")
			}
			state, _ := strconv.ParseInt(fmt.Sprint(values[0]), 10, 64)
			if state == 1 {
				if scopeKey != channelKey {
					if _, err = observeScript.Run(ctx, common.RDB, []string{channelKey}, now.UnixMilli(), expiresAt, requestID).Result(); err != nil {
						_, _ = releaseScript.Run(context.Background(), common.RDB, keys, requestID).Result()
						return nil, err
					}
				}
				for _, additionalKey := range keys {
					if additionalKey == scopeKey || additionalKey == channelKey {
						continue
					}
					if _, err = observeScript.Run(ctx, common.RDB, []string{additionalKey}, now.UnixMilli(), expiresAt, requestID).Result(); err != nil {
						_, _ = releaseScript.Run(context.Background(), common.RDB, keys, requestID).Result()
						return nil, err
					}
				}
				lease := &concurrencyLease{id: requestID, keys: keys, tracked: true, stop: make(chan struct{})}
				lease.startRenewal(leaseSeconds)
				return lease, nil
			}
			if state == -1 {
				return nil, errConcurrencyQueueFull
			}
			if time.Now().After(deadline) {
				_, _ = releaseScript.Run(context.Background(), common.RDB, []string{waiterKey}, requestID).Result()
				return nil, errConcurrencyQueueTimeout
			}
			select {
			case <-ctx.Done():
				_, _ = releaseScript.Run(context.Background(), common.RDB, []string{waiterKey}, requestID).Result()
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	now := time.Now()
	expiresAt := now.Add(time.Duration(leaseSeconds) * time.Second).UnixMilli()
	trackedKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, err := observeScript.Run(ctx, common.RDB, []string{key}, now.UnixMilli(), expiresAt, requestID).Result(); err != nil {
			if len(trackedKeys) > 0 {
				_, _ = releaseScript.Run(context.Background(), common.RDB, trackedKeys, requestID).Result()
			}
			return nil, err
		}
		trackedKeys = append(trackedKeys, key)
	}
	lease := &concurrencyLease{id: requestID, keys: keys, tracked: true, stop: make(chan struct{})}
	lease.startRenewal(leaseSeconds)
	return lease, nil
}

func acquireLocalConcurrency(ctx context.Context, channelID, keyIndex int, requestID string, enforce bool, limit model.OpsConcurrencyLimit) (*concurrencyLease, error) {
	enforcementScope := concurrencyScope{channelID: channelID, keyIndex: keyIndex}
	if enforce {
		enforcementScope.keyIndex = limit.KeyIndex
	}
	trackedScopes := []concurrencyScope{{channelID: channelID, keyIndex: model.OpsConcurrencyAllKeys}}
	keyScope := concurrencyScope{channelID: channelID, keyIndex: keyIndex}
	if keyScope != trackedScopes[0] {
		trackedScopes = append(trackedScopes, keyScope)
	}
	timeout := time.Duration(limit.QueueTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	queued := false
	for {
		localMu.Lock()
		state := localState[enforcementScope]
		if state == nil {
			state = &localConcurrencyState{}
			localState[enforcementScope] = state
		}
		if !enforce || state.inUse < limit.MaxConcurrency {
			if queued {
				state.waiting--
			}
			for _, scope := range trackedScopes {
				tracked := localState[scope]
				if tracked == nil {
					tracked = &localConcurrencyState{}
					localState[scope] = tracked
				}
				tracked.inUse++
			}
			localMu.Unlock()
			return &concurrencyLease{id: requestID, tracked: true, localKeys: trackedScopes, local: true}, nil
		}
		if !queued {
			if limit.QueueSize >= 0 && state.waiting >= limit.QueueSize {
				localMu.Unlock()
				return nil, errConcurrencyQueueFull
			}
			state.waiting++
			queued = true
		}
		localMu.Unlock()
		if time.Now().After(deadline) {
			localMu.Lock()
			state.waiting--
			localMu.Unlock()
			return nil, errConcurrencyQueueTimeout
		}
		select {
		case <-ctx.Done():
			localMu.Lock()
			state.waiting--
			localMu.Unlock()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (lease *concurrencyLease) startRenewal(leaseSeconds int) {
	if lease == nil || lease.local || len(lease.keys) == 0 {
		return
	}
	interval := time.Duration(leaseSeconds) * time.Second / 3
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-lease.stop:
				return
			case <-ticker.C:
				expiresAt := time.Now().Add(time.Duration(leaseSeconds) * time.Second).UnixMilli()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, _ = renewScript.Run(ctx, common.RDB, lease.keys, expiresAt, lease.id).Result()
				cancel()
			}
		}
	}()
}

func (lease *concurrencyLease) release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.stop != nil {
			close(lease.stop)
		}
		if lease.local {
			localMu.Lock()
			for _, scope := range lease.localKeys {
				if state := localState[scope]; state != nil && state.inUse > 0 {
					state.inUse--
				}
			}
			localMu.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = releaseScript.Run(ctx, common.RDB, lease.keys, lease.id).Result()
	})
}

func concurrencyLeaseKey(channelID, keyIndex int) string {
	if keyIndex == model.OpsConcurrencyAllKeys {
		return fmt.Sprintf("ops:concurrency:leases:channel:%d", channelID)
	}
	return fmt.Sprintf("ops:concurrency:leases:channel:%d:key:%d", channelID, keyIndex)
}

func concurrencyWaiterKey(channelID, keyIndex int) string {
	if keyIndex == model.OpsConcurrencyAllKeys {
		return fmt.Sprintf("ops:concurrency:waiters:channel:%d", channelID)
	}
	return fmt.Sprintf("ops:concurrency:waiters:channel:%d:key:%d", channelID, keyIndex)
}

func markRuntimeAvailability(channelID, keyIndex, statusCode int) {
	if !common.RedisEnabled || common.RDB == nil || statusCode < 400 {
		return
	}
	setting := ops_monitor_setting.Get()
	state := "temporary"
	ttl := setting.TemporaryUnschedulableSeconds
	if statusCode == 429 {
		state = "rate_limit"
		ttl = setting.RateLimitCooldownSeconds
	} else if statusCode == 529 {
		state = "overload"
		ttl = setting.OverloadCooldownSeconds
	} else if statusCode < 500 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	key := fmt.Sprintf("ops:availability:%s:channel:%d:key:%d", state, channelID, keyIndex)
	_ = common.RDB.Set(ctx, key, time.Now().Unix(), time.Duration(ttl)*time.Second).Err()
}
