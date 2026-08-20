package model

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

func getTokenCacheKey(key string) string {
	return fmt.Sprintf("token:%s", common.GenerateHMAC(key))
}

func getTokenCacheFenceKey(key string) string {
	return fmt.Sprintf("token:fence:%s", common.GenerateHMAC(key))
}

func tokenCacheTTLSeconds() int {
	ttl := common.RedisKeyCacheSeconds()
	if ttl <= 0 {
		return 60
	}
	return ttl
}

// tokenCacheFenceSeconds must outlive a token mutation's database write plus
// any in-flight reader's DB-read-to-cache-init gap. The fence is not deleted
// after commit; it expires naturally so a reader holding a pre-mutation
// snapshot cannot publish it right after the mutation cleared the cache.
// While the fence exists readers simply serve the database without caching.
const tokenCacheFenceSeconds = 10

// invalidateTokenCacheForMutation is called before a token metadata mutation
// writes to the database: it raises the fence and drops the cached hash so no
// reader can act on (or re-publish) the pre-mutation state.
func invalidateTokenCacheForMutation(key string) error {
	if !common.RedisEnabled || key == "" {
		return nil
	}
	ctx := context.Background()
	err := common.RDB.Set(ctx, getTokenCacheFenceKey(key), 1, time.Duration(tokenCacheFenceSeconds)*time.Second).Err()
	if err != nil {
		return err
	}
	return common.RDB.Del(ctx, getTokenCacheKey(key)).Err()
}

// cacheInitToken publishes a database snapshot only when no mutation fence is
// active and the hash is cold. An existing hash only gets its TTL refreshed:
// its RemainQuota may already be ahead of this snapshot because atomic
// pre-consume decrements Redis first, so a snapshot must never overwrite any
// field of a live hash.
// 返回值：0=被 fence 拦截，1=完成初始化，2=哈希已存在，仅刷新 TTL。
func cacheInitToken(token Token) (int, error) {
	if !common.RedisEnabled {
		return 0, nil
	}
	allowIps := ""
	if token.AllowIps != nil {
		allowIps = *token.AllowIps
	}
	const script = `
if redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
if redis.call('EXISTS', KEYS[1]) == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[17])
  return 2
end
redis.call('HSET', KEYS[1],
  'Id', ARGV[1], 'UserId', ARGV[2], 'Status', ARGV[3], 'Name', ARGV[4],
  'CreatedTime', ARGV[5], 'AccessedTime', ARGV[6], 'ExpiredTime', ARGV[7],
  'UnlimitedQuota', ARGV[8], 'ModelLimitsEnabled', ARGV[9], 'ModelLimits', ARGV[10],
  'AllowIps', ARGV[11], 'Group', ARGV[12], 'CrossGroupRetry', ARGV[13],
  'AutoGroups', ARGV[14], 'RemainQuota', ARGV[15], 'UsedQuota', ARGV[16])
redis.call('EXPIRE', KEYS[1], ARGV[17])
return 1`

	return common.RDB.Eval(context.Background(), script, []string{
		getTokenCacheKey(token.Key), getTokenCacheFenceKey(token.Key),
	},
		token.Id, token.UserId, token.Status, token.Name,
		token.CreatedTime, token.AccessedTime, token.ExpiredTime,
		strconv.FormatBool(token.UnlimitedQuota), strconv.FormatBool(token.ModelLimitsEnabled),
		token.ModelLimits, allowIps, token.Group, strconv.FormatBool(token.CrossGroupRetry),
		token.AutoGroups, token.RemainQuota, token.UsedQuota,
		tokenCacheTTLSeconds(),
	).Int()
}

// cacheGetTokenByKey 从缓存读取 token；不完整的哈希（如仅有配额字段）会被拒绝。
func cacheGetTokenByKey(key string) (*Token, error) {
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var token Token
	if err := common.RedisHGetObj(getTokenCacheKey(key), &token); err != nil {
		return nil, err
	}
	if token.Id <= 0 {
		return nil, fmt.Errorf("token cache is incomplete")
	}
	token.Key = key
	return &token, nil
}

// UpdateTokenCacheAfterExternalWrite applies metadata changed by a sidecar while
// preserving quota deltas that may still be buffered by the main process.
func UpdateTokenCacheAfterExternalWrite(oldKey string, token Token, remainQuotaDelta int, replaceRemainQuota bool) error {
	if !common.RedisEnabled || common.RDB == nil || oldKey == "" {
		return nil
	}
	oldRedisKey := fmt.Sprintf("token:%s", common.GenerateHMAC(oldKey))
	newRedisKey := fmt.Sprintf("token:%s", common.GenerateHMAC(token.Key))
	replaceQuotaFlag := "0"
	if replaceRemainQuota {
		replaceQuotaFlag = "1"
	}
	script := `
if redis.call('EXISTS', KEYS[1]) == 0 then
    return 0
end
if KEYS[1] ~= KEYS[2] then
    redis.call('RENAME', KEYS[1], KEYS[2])
end
redis.call('HSET', KEYS[2],
    'UserId', ARGV[1],
    'Name', ARGV[2],
    'Status', ARGV[3],
    'ExpiredTime', ARGV[4],
    'UnlimitedQuota', ARGV[5],
    'ModelLimitsEnabled', ARGV[6],
    'ModelLimits', ARGV[7],
    'Group', ARGV[8])
if ARGV[10] == '1' then
    redis.call('HSET', KEYS[2], 'RemainQuota', ARGV[9])
elseif tonumber(ARGV[9]) ~= 0 then
    redis.call('HINCRBY', KEYS[2], 'RemainQuota', ARGV[9])
end
return 1
`
	_, err := common.RDB.Eval(context.Background(), script, []string{oldRedisKey, newRedisKey},
		fmt.Sprint(token.UserId),
		token.Name,
		fmt.Sprint(token.Status),
		fmt.Sprint(token.ExpiredTime),
		fmt.Sprint(token.UnlimitedQuota),
		fmt.Sprint(token.ModelLimitsEnabled),
		token.ModelLimits,
		token.Group,
		fmt.Sprint(remainQuotaDelta),
		replaceQuotaFlag,
	).Result()
	return err
}
