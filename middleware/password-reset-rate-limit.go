package middleware

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// The limits are independent of the optional general critical-request limiter.
// Check every bucket before reserving any send, atomically across all instances.
// A rejected attempt must not extend an existing recipient's cooldown.
const passwordResetRateScript = `
for i, key in ipairs(KEYS) do
  local count = tonumber(redis.call('GET', key) or '0')
  if count >= tonumber(ARGV[i * 2 - 1]) then
    local ttl = redis.call('TTL', key)
    if ttl < 0 then
      ttl = tonumber(ARGV[i * 2])
      redis.call('EXPIRE', key, ttl)
    end
    return {0, ttl}
  end
end
for i, key in ipairs(KEYS) do
  local count = redis.call('INCR', key)
  if count == 1 or redis.call('TTL', key) < 0 then
    redis.call('EXPIRE', key, ARGV[i * 2])
  end
end
return {1, 0}
`

type passwordResetBucket struct {
	key      string
	limit    int
	duration int64
}

var passwordResetMemoryLimiter common.InMemoryRateLimiter

// PasswordResetEmailRateLimit protects the email side effect even when CAPTCHA
// or general rate limiting is disabled. Reserve before looking up the account,
// so existing and unknown addresses receive the same limiter responses.
func PasswordResetEmailRateLimit() gin.HandlerFunc {
	passwordResetMemoryLimiter.Init(25 * time.Hour)
	return func(c *gin.Context) {
		email := model.NormalizeEmail(c.Query("email"))
		if len(email) > 254 || common.Validate.Var(email, "required,email") != nil {
			common.ApiErrorI18n(c, i18n.MsgInvalidParams)
			c.Abort()
			return
		}
		// Do not retain raw addresses or IPs in Redis keys or security logs.
		address := fmt.Sprintf("%x", sha256.Sum256([]byte(email)))
		ip := fmt.Sprintf("%x", sha256.Sum256([]byte(c.ClientIP())))
		buckets := []passwordResetBucket{
			{"passwordReset:v1:ip:minute:" + ip, 5, 60},
			{"passwordReset:v1:ip:hour:" + ip, 20, 3600},
			{"passwordReset:v1:email:minute:" + address, 1, 60},
			{"passwordReset:v1:email:hour:" + address, 5, 3600},
			{"passwordReset:v1:email:day:" + address, 10, 86400},
		}
		var wait int64
		if common.RedisEnabled {
			keys, args := make([]string, len(buckets)), make([]any, 0, len(buckets)*2)
			for i, bucket := range buckets {
				keys[i] = bucket.key
				args = append(args, bucket.limit, bucket.duration)
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			var values []any
			var err error
			if common.RDB != nil {
				values, err = common.RDB.Eval(ctx, passwordResetRateScript, keys, args...).Slice()
			}
			// An unavailable shared limiter must not become an unlimited or
			// per-instance fallback path during an attack.
			if err != nil || len(values) != 2 {
				logger.LogError(c.Request.Context(), "password reset email limiter unavailable")
				c.Header("Retry-After", "60")
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"success": false, "message": i18n.T(c, i18n.MsgRetryLater)})
				return
			}
			allowed, allowedErr := redisReplyInteger(values[0])
			remaining, ttlErr := redisReplyInteger(values[1])
			if allowedErr != nil || ttlErr != nil || (allowed != 0 && allowed != 1) {
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			if allowed == 0 {
				wait = max(remaining, 1)
			}
		} else {
			// Standalone installations without Redis retain a concurrency-safe
			// local guard. Blue/green deployments must share their Redis DB.
			for _, bucket := range buckets {
				if !passwordResetMemoryLimiter.Request(bucket.key, bucket.limit, bucket.duration) {
					wait = bucket.duration
					break
				}
			}
		}
		if wait > 0 {
			c.Header("Retry-After", strconv.FormatInt(wait, 10))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"success": false, "message": i18n.T(c, i18n.MsgRetryLater)})
			return
		}
		c.Next()
	}
}
