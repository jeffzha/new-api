package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useRateLimitMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()

	previousRedisEnabled := common.RedisEnabled
	previousRedisClient := common.RDB
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	require.NoError(t, redisClient.Ping(context.Background()).Err())

	common.RedisEnabled = true
	common.RDB = redisClient
	t.Cleanup(func() {
		_ = redisClient.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedisClient
	})

	return redisServer, redisClient
}

func performRateLimitRequest(router http.Handler, path string, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestRedisIPRateLimiterThresholdTTLAndNamespace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/limited", rateLimitFactory(2, 37, "TEST"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	remoteAddr := "192.0.2.10:12345"
	legacyKey := "rateLimit:TEST192.0.2.10"
	_, err := redisServer.Push(legacyKey, "legacy-list-entry")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/limited", remoteAddr).Code)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/limited", remoteAddr).Code)
	limitedResponse := performRateLimitRequest(router, "/limited", remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, limitedResponse.Code)
	assert.Equal(t, "37", limitedResponse.Header().Get("Retry-After"))

	key := redisIPRateLimitKey("TEST", "192.0.2.10")
	count, err := redisServer.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "3", count)
	assert.Equal(t, 37*time.Second, redisServer.TTL(key))
	assert.True(t, redisServer.Exists(legacyKey), "the v2 counter must not touch an old list key")
}

func TestRedisUserRateLimiterUsesSharedFixedWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)

	router := gin.New()
	router.GET(
		"/limited",
		func(c *gin.Context) { c.Set("id", 42) },
		userRateLimitFactory(1, 23, "USER"),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)

	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/limited", "192.0.2.20:12345").Code)
	assert.Equal(t, http.StatusTooManyRequests, performRateLimitRequest(router, "/limited", "198.51.100.20:12345").Code)

	key := redisUserRateLimitKey("USER", 42)
	assert.True(t, redisServer.Exists(key))
	assert.Equal(t, 23*time.Second, redisServer.TTL(key))
}

func TestRelayUserConcurrencyLimitsAndReleasesByUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, client := useRateLimitMiniRedis(t)
	t.Setenv("RELAY_USER_CONCURRENCY_LIMIT", "1")

	entered := make(chan struct{})
	release := make(chan struct{})
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set("id", 143)
		c.Set(common.RequestIdKey, c.GetHeader("X-Request-Id"))
	}, RelayUserConcurrency(), func(c *gin.Context) {
		if c.GetHeader("X-Request-Id") == "first" {
			close(entered)
			<-release
		}
		c.Status(http.StatusNoContent)
	})
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request.Header.Set("X-Request-Id", "first")
		router.ServeHTTP(recorder, request)
		firstDone <- recorder
	}()
	<-entered
	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("X-Request-Id", "second")
	router.ServeHTTP(second, request)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	assert.Equal(t, "1", second.Header().Get("Retry-After"))
	assert.Contains(t, second.Body.String(), "concurrency_limit")
	assert.True(t, redisServer.Exists("relay:user:active:143"))
	close(release)
	assert.Equal(t, http.StatusNoContent, (<-firstDone).Code)
	count, err := client.ZCard(context.Background(), "relay:user:active:143").Result()
	require.NoError(t, err)
	assert.Zero(t, count)
	third := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("X-Request-Id", "third")
	router.ServeHTTP(third, request)
	assert.Equal(t, http.StatusNoContent, third.Code)
}

func TestRelayUserConcurrencyDisabledPreservesRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("RELAY_USER_CONCURRENCY_LIMIT", "0")
	router := gin.New()
	router.POST("/v1/chat/completions", RelayUserConcurrency(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assert.Equal(t, http.StatusNoContent, response.Code)
}

func TestRelayUserConcurrencyFailsOpenWhenRedisUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, client := useRateLimitMiniRedis(t)
	t.Setenv("RELAY_USER_CONCURRENCY_LIMIT", "1")
	require.NoError(t, client.Close())
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) { c.Set("id", 143) }, RelayUserConcurrency(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	assert.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestRelayUserConcurrencyScopesWalletsAndRemovesExpiredLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server, client := useRateLimitMiniRedis(t)
	t.Setenv("RELAY_USER_CONCURRENCY_LIMIT", "1")
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		userID, _ := strconv.Atoi(c.GetHeader("X-User-Id"))
		c.Set("id", userID)
		c.Set(common.RequestIdKey, c.GetHeader("X-Request-Id"))
	}, RelayUserConcurrency(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	key := "relay:user:active:143"
	now := time.Now()
	require.NoError(t, client.ZAdd(context.Background(), key, &redis.Z{Score: float64(now.Add(-time.Second).UnixMilli()), Member: "abandoned"}).Err())
	request := func(user, id string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-User-Id", user)
		req.Header.Set("X-Request-Id", id)
		router.ServeHTTP(recorder, req)
		return recorder
	}
	assert.Equal(t, http.StatusNoContent, request("143", "first").Code)
	assert.Equal(t, http.StatusNoContent, request("144", "second").Code)
	assert.False(t, server.Exists(key))
	assert.Equal(t, http.StatusNoContent, request("143", "third").Code)
}

func TestRelayUserConcurrencyLeaseRenewalCannotResurrectReleasedRequest(t *testing.T) {
	_, client := useRateLimitMiniRedis(t)
	ctx := context.Background()
	key := "relay:user:active:143"
	const leaseMillis = int64(300000)
	admitted, err := acquireRelayUserScript.Run(ctx, client, []string{key}, int64(1000), int64(1000)+leaseMillis, 1, "first", 300).Int()
	require.NoError(t, err)
	require.Equal(t, 1, admitted)
	admitted, err = renewRelayUserScript.Run(ctx, client, []string{key}, int64(2000)+leaseMillis, "first", 300, int64(2000)).Int()
	require.NoError(t, err)
	assert.Equal(t, 1, admitted)
	admitted, err = acquireRelayUserScript.Run(ctx, client, []string{key}, int64(2000), int64(2000)+leaseMillis, 1, "second", 300).Int()
	require.NoError(t, err)
	assert.Zero(t, admitted)
	require.NoError(t, releaseRelayUserScript.Run(ctx, client, []string{key}, "first").Err())
	admitted, err = renewRelayUserScript.Run(ctx, client, []string{key}, int64(3000)+leaseMillis, "first", 300, int64(3000)).Int()
	require.NoError(t, err)
	assert.Zero(t, admitted)
	admitted, err = acquireRelayUserScript.Run(ctx, client, []string{key}, int64(3000), int64(3000)+leaseMillis, 1, "second", 300).Int()
	require.NoError(t, err)
	assert.Equal(t, 1, admitted)
}

func TestGlobalAPIRateLimitDoesNotGateCaddyStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, client := useRateLimitMiniRedis(t)
	previousEnabled, previousLimit, previousDuration := common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration
	common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = true, 1, 60
	t.Cleanup(func() {
		common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = previousEnabled, previousLimit, previousDuration
	})
	router := gin.New()
	router.Use(GlobalAPIRateLimit())
	router.GET("/api/status", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/api/other", func(c *gin.Context) { c.Status(http.StatusOK) })
	remote := "192.0.2.50:12345"
	assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/api/status", remote).Code)
	assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/api/status", remote).Code)
	count, err := client.Exists(context.Background(), redisIPRateLimitKey("GA", "192.0.2.50")).Result()
	require.NoError(t, err)
	assert.Zero(t, count)
	assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/api/other", remote).Code)
	assert.Equal(t, http.StatusTooManyRequests, performRateLimitRequest(router, "/api/other", remote).Code)
	assert.Equal(t, http.StatusTooManyRequests, performRateLimitRequest(router, "/api/status/test", remote).Code)
	require.NoError(t, client.Close())
	assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/api/status", remote).Code)
}

func TestRedisEmailVerificationRateLimiterPreservesResponseAndTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/verify", EmailVerificationRateLimit(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	remoteAddr := "192.0.2.30:12345"
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/verify", remoteAddr).Code)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/verify", remoteAddr).Code)
	response := performRateLimitRequest(router, "/verify", remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.JSONEq(t, `{"success":false,"message":"发送过于频繁，请等待 30 秒后再试"}`, response.Body.String())

	key := redisIPRateLimitKey(EmailVerificationRateLimitMark, "192.0.2.30")
	assert.True(t, redisServer.Exists(key))
	assert.Equal(t, time.Duration(EmailVerificationDuration)*time.Second, redisServer.TTL(key))
}

func TestRedisFixedWindowIsAtomicUnderConcurrency(t *testing.T) {
	redisServer, _ := useRateLimitMiniRedis(t)
	const (
		requestCount = 20
		maximumCount = 7
		duration     = int64(41)
	)
	key := redisIPRateLimitKey("CONCURRENT", "192.0.2.40")

	var allowedCount atomic.Int64
	errorsFound := make(chan error, requestCount)
	var waitGroup sync.WaitGroup
	waitGroup.Add(requestCount)
	for range requestCount {
		go func() {
			defer waitGroup.Done()
			allowed, _, _, err := redisFixedWindowTake(context.Background(), key, maximumCount, duration)
			if err != nil {
				errorsFound <- err
				return
			}
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	waitGroup.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}

	assert.Equal(t, int64(maximumCount), allowedCount.Load())
	count, err := redisServer.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "20", count)
	assert.Equal(t, time.Duration(duration)*time.Second, redisServer.TTL(key))
}

func TestRedisFixedWindowResetsAtBoundary(t *testing.T) {
	redisServer, _ := useRateLimitMiniRedis(t)
	const duration = int64(10)
	key := redisIPRateLimitKey("BOUNDARY", "192.0.2.50")

	for range 2 {
		allowed, _, _, err := redisFixedWindowTake(context.Background(), key, 2, duration)
		require.NoError(t, err)
		assert.True(t, allowed)
	}
	allowed, _, _, err := redisFixedWindowTake(context.Background(), key, 2, duration)
	require.NoError(t, err)
	assert.False(t, allowed)

	// This reset is intentional fixed-window behavior. A client can consume one
	// full allowance immediately before and another immediately after a boundary.
	redisServer.FastForward(time.Duration(duration) * time.Second)
	for range 2 {
		allowed, _, _, err = redisFixedWindowTake(context.Background(), key, 2, duration)
		require.NoError(t, err)
		assert.True(t, allowed)
	}
}

func TestRedisFixedWindowRepairsCounterWithoutTTL(t *testing.T) {
	redisServer, _ := useRateLimitMiniRedis(t)
	const duration = int64(29)
	key := redisIPRateLimitKey("MISSING-TTL", "192.0.2.51")
	redisServer.Set(key, "5")

	allowed, count, ttl, err := redisFixedWindowTake(context.Background(), key, 3, duration)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, int64(6), count)
	assert.Equal(t, duration, ttl)
	assert.Equal(t, time.Duration(duration)*time.Second, redisServer.TTL(key))

	redisServer.FastForward(time.Duration(duration) * time.Second)
	assert.False(t, redisServer.Exists(key), "a recovered counter must not remain permanently rate-limited")
}

func TestRedisFailurePolicies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, redisClient := useRateLimitMiniRedis(t)
	require.NoError(t, redisClient.Close())

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/ip", rateLimitFactory(1, 30, "FAIL-IP"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET(
		"/user",
		func(c *gin.Context) { c.Set("id", 7) },
		userRateLimitFactory(1, 30, "FAIL-USER"),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)
	router.GET("/email", EmailVerificationRateLimit(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	ipResponse := performRateLimitRequest(router, "/ip", "192.0.2.60:12345")
	assert.Equal(t, http.StatusInternalServerError, ipResponse.Code)
	assert.Empty(t, ipResponse.Body.String())
	userResponse := performRateLimitRequest(router, "/user", "192.0.2.61:12345")
	assert.Equal(t, http.StatusInternalServerError, userResponse.Code)
	assert.Empty(t, userResponse.Body.String())
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/email", "192.0.2.62:12345").Code)
}
