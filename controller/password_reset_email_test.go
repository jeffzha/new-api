package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func passwordResetTestRouter(t *testing.T, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/api/reset_password", middleware.DisableCache(), middleware.CriticalRateLimit(), middleware.TurnstileCheck(), middleware.PasswordResetEmailRateLimit(), handler)
	return router
}

func passwordResetTestRequest(router http.Handler, email, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/reset_password?email="+url.QueryEscape(email)+"&turnstile=", nil)
	r.RemoteAddr = ip + ":12345"
	r.Header.Set("X-Forwarded-For", "203.0.113.250")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func configurePasswordResetTest(t *testing.T, useRedis bool) *miniredis.Miniredis {
	t.Helper()
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	previousRedis, previousClient := common.RedisEnabled, common.RDB
	previousCritical, previousTurnstile := common.CriticalRateLimitEnable, common.TurnstileCheckEnabled
	common.RedisEnabled, common.CriticalRateLimitEnable, common.TurnstileCheckEnabled = useRedis, false, false
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = previousRedis, previousClient
		common.CriticalRateLimitEnable, common.TurnstileCheckEnabled = previousCritical, previousTurnstile
	})
	if !useRedis {
		return nil
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	require.NoError(t, client.Ping(context.Background()).Err())
	common.RDB = client
	t.Cleanup(func() { _ = client.Close() })
	return server
}

func TestPasswordResetConcurrentRequestsSendOnlyOneEmail(t *testing.T) {
	for _, useRedis := range []bool{true, false} {
		t.Run(fmt.Sprintf("redis=%t", useRedis), func(t *testing.T) {
			user, _ := setupSecurityEnrollmentTest(t)
			configurePasswordResetTest(t, useRedis)
			email := fmt.Sprintf("reset-concurrency-%t@example.com", useRedis)
			require.NoError(t, model.DB.Model(user).Update("email", email).Error)
			mailbox := newSecurityMailbox(t)
			// Two independent middleware factories share one backend and protect
			// the actual SMTP side effect, even with general/CAPTCHA gates off.
			routers := []*gin.Engine{passwordResetTestRouter(t, SendPasswordResetEmail), passwordResetTestRouter(t, SendPasswordResetEmail)}
			responses := make([]*httptest.ResponseRecorder, 32)
			start := make(chan struct{})
			var group sync.WaitGroup
			for i := range responses {
				group.Go(func() {
					<-start
					address := email
					if i%2 == 0 {
						address = " " + strings.ToUpper(address) + " "
					}
					responses[i] = passwordResetTestRequest(routers[i%2], address, fmt.Sprintf("192.0.2.%d", i+1))
				})
			}
			close(start)
			group.Wait()
			accepted, limited := 0, 0
			for _, response := range responses {
				assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
				switch response.Code {
				case http.StatusOK:
					assert.Contains(t, response.Body.String(), `"success":true`)
					accepted++
				case http.StatusTooManyRequests:
					assert.NotEmpty(t, response.Header().Get("Retry-After"))
					limited++
				default:
					t.Errorf("unexpected status %d: %s", response.Code, response.Body.String())
				}
			}
			assert.Equal(t, 1, accepted)
			assert.Equal(t, len(responses)-1, limited)
			mailbox.mutex.Lock()
			assert.Len(t, mailbox.mail[email], 1)
			mailbox.mutex.Unlock()
		})
	}
}

func TestPasswordResetRecipientAndIPWindows(t *testing.T) {
	server := configurePasswordResetTest(t, true)
	router := passwordResetTestRouter(t, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	// Redis TTL rounds down to zero during the last second. A rejected
	// request must not restart the recipient's cooldown in that interval.
	assert.Equal(t, http.StatusNoContent, passwordResetTestRequest(router, "expiry@example.com", "198.51.100.150").Code)
	server.FastForward(59500 * time.Millisecond)
	assert.Equal(t, http.StatusTooManyRequests, passwordResetTestRequest(router, "expiry@example.com", "198.51.100.151").Code)
	server.FastForward(time.Second)
	assert.Equal(t, http.StatusNoContent, passwordResetTestRequest(router, "expiry@example.com", "198.51.100.152").Code)
	server.FlushAll()
	// An unknown address has exactly the same limits as an existing account;
	// no account lookup is needed before rejecting automated requests.
	email := "unknown@example.com"
	for i := range 5 {
		if i > 0 {
			server.FastForward(time.Minute)
		}
		assert.Equal(t, http.StatusNoContent, passwordResetTestRequest(router, email, fmt.Sprintf("198.51.100.%d", i+1)).Code)
	}
	server.FastForward(time.Minute)
	response := passwordResetTestRequest(router, email, "198.51.100.20")
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.Equal(t, "3300", response.Header().Get("Retry-After"))
	server.FastForward(55 * time.Minute)
	for i := range 5 {
		if i > 0 {
			server.FastForward(time.Minute)
		}
		assert.Equal(t, http.StatusNoContent, passwordResetTestRequest(router, email, fmt.Sprintf("198.51.100.%d", i+30)).Code)
	}
	server.FastForward(time.Hour)
	assert.Equal(t, http.StatusTooManyRequests, passwordResetTestRequest(router, email, "198.51.100.50").Code)
	server.FastForward(24 * time.Hour)
	assert.Equal(t, http.StatusNoContent, passwordResetTestRequest(router, email, "198.51.100.50").Code)

	server.FlushAll()
	for minute := range 4 {
		if minute > 0 {
			server.FastForward(time.Minute)
		}
		for i := range 5 {
			assert.Equal(t, http.StatusNoContent, passwordResetTestRequest(router, fmt.Sprintf("ip-%d-%d@example.com", minute, i), "203.0.113.20").Code)
		}
		assert.Equal(t, http.StatusTooManyRequests, passwordResetTestRequest(router, "overflow@example.com", "203.0.113.20").Code)
	}
	server.FastForward(time.Minute)
	assert.Equal(t, http.StatusTooManyRequests, passwordResetTestRequest(router, "overflow@example.com", "203.0.113.20").Code)
	// Rejecting one IP must not consume the recipient budget for another IP.
	assert.Equal(t, http.StatusNoContent, passwordResetTestRequest(router, "overflow@example.com", "203.0.113.21").Code)
}

func TestPasswordResetLimiterFailureAndInvalidInputCannotSend(t *testing.T) {
	configurePasswordResetTest(t, true)
	router := passwordResetTestRouter(t, func(c *gin.Context) { t.Error("rejected request reached email handler") })
	for _, email := range []string{"", "not-an-email", strings.Repeat("a", 255) + "@example.com"} {
		r := passwordResetTestRequest(router, email, "192.0.2.200")
		assert.Contains(t, r.Body.String(), `"success":false`)
	}
	for _, scenario := range []string{"nil client", "closed client"} {
		t.Run(scenario, func(t *testing.T) {
			client := common.RDB
			if scenario == "nil client" {
				common.RDB = nil
			} else {
				require.NoError(t, client.Close())
			}
			r := passwordResetTestRequest(router, "valid@example.com", "192.0.2.200")
			assert.Equal(t, http.StatusInternalServerError, r.Code)
			assert.Equal(t, "60", r.Header().Get("Retry-After"))
			common.RDB = client
		})
	}
}
