package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const relayUserLease = 5 * time.Minute

var acquireRelayUserScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
redis.call('EXPIRE', KEYS[1], ARGV[5])
return 1
`)

var releaseRelayUserScript = redis.NewScript(`return redis.call('ZREM', KEYS[1], ARGV[1])`)
var renewRelayUserScript = redis.NewScript(`
local expires = redis.call('ZSCORE', KEYS[1], ARGV[2])
if not expires or tonumber(expires) <= tonumber(ARGV[4]) then return 0 end
redis.call('ZADD', KEYS[1], 'XX', ARGV[1], ARGV[2])
redis.call('EXPIRE', KEYS[1], ARGV[3])
return 1
`)

// RelayUserConcurrency bounds simultaneous requests from one wallet before
// pre-consume. The default is disabled; a positive limit requires shared Redis.
func RelayUserConcurrency() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := common.GetEnvOrDefault("RELAY_USER_CONCURRENCY_LIMIT", 0)
		if limit <= 0 {
			c.Next()
			return
		}
		userID := c.GetInt("id")
		if userID <= 0 {
			abortWithOpenAiMessage(c, http.StatusUnauthorized, "User identity unavailable")
			return
		}
		if !common.RedisEnabled || common.RDB == nil {
			logger.LogWarn(c, "user concurrency admission unavailable; continuing without a lease")
			c.Next()
			return
		}
		key := "relay:user:active:" + strconv.Itoa(userID)
		requestID := c.GetString(common.RequestIdKey)
		if requestID == "" {
			requestID = common.NewRequestId()
		}
		now := time.Now()
		leaseSeconds := int(relayUserLease.Seconds())
		admitted, err := acquireRelayUserScript.Run(c.Request.Context(), common.RDB, []string{key},
			now.UnixMilli(), now.Add(relayUserLease).UnixMilli(), limit, requestID, leaseSeconds).Int()
		if err != nil {
			logger.LogWarn(c, "user concurrency admission failed; continuing without a lease")
			c.Next()
			return
		}
		if admitted == 0 {
			c.Header("Retry-After", "1")
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, "Too many simultaneous requests for this account", types.ErrorCodeConcurrencyLimit)
			return
		}
		stop := make(chan struct{})
		defer func() {
			close(stop)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = releaseRelayUserScript.Run(ctx, common.RDB, []string{key}, requestID).Err()
		}()
		go func() {
			ticker := time.NewTicker(relayUserLease / 3)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					now := time.Now()
					_ = renewRelayUserScript.Run(ctx, common.RDB, []string{key}, now.Add(relayUserLease).UnixMilli(), requestID, leaseSeconds, now.UnixMilli()).Err()
					cancel()
				}
			}
		}()
		c.Next()
	}
}
