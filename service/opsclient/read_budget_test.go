package opsclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func TestReadBudgetSharedAcrossInstancesResourcesAndCallPriority(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	before, enabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = redisClient, true
	t.Cleanup(func() { redisClient.Close(); common.RDB, common.RedisEnabled = before, enabled })
	var requests atomic.Int64
	ops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write([]byte("{}")) }))
	defer ops.Close()
	clients := []*Client{New(ops.URL, "same-credential", "test-only", time.Second), New(ops.URL, "same-credential", "test-only", time.Second), New(ops.URL, "same-credential", "test-only", time.Second)}
	for _, c := range clients {
		c.EnableReadBudget()
	}
	ctx := context.Background()
	// Concurrent instances cannot each spend the entire background allowance.
	var workers sync.WaitGroup
	var succeeded atomic.Int64
	for i := range 60 {
		workers.Go(func() {
			_, err := clients[i%len(clients)].Get(ctx, "/api/v1/catalog/v1/fees", time.Now())
			if err == nil {
				succeeded.Add(1)
				return
			}
			_, deferred := ReadDeferred(err)
			if !deferred {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	workers.Wait()
	require.Equal(t, int64(backgroundReadLimit), succeeded.Load())
	require.Equal(t, int64(backgroundReadLimit), requests.Load())
	_, err := clients[1].Get(ctx, "/api/v1/catalog/v1/prices", time.Now())
	wait, deferred := ReadDeferred(err)
	require.True(t, deferred)
	require.Positive(t, wait)
	for range readBudgetLimit - backgroundReadLimit {
		_, err = clients[2].Get(CallBalanceContext(ctx), "/api/v1/catalog/v1/balances/15", time.Now())
		require.NoError(t, err)
	}
	_, err = clients[0].Get(CallBalanceContext(ctx), "/api/v1/catalog/v1/balances/15", time.Now())
	_, deferred = ReadDeferred(err)
	require.True(t, deferred)
	require.Equal(t, int64(readBudgetLimit), requests.Load())
	// Writes and another credential do not consume this read-only allowance.
	_, err = clients[0].Post(ctx, "/api/v1/catalog/v1/balance-changes", []byte("{}"), time.Now())
	require.NoError(t, err)
	other := New(ops.URL, "another-credential", "test-only", time.Second)
	other.EnableReadBudget()
	_, err = other.Get(ctx, "/api/v1/catalog/v1/fees", time.Now())
	require.NoError(t, err)
	server.FastForward(61 * time.Second)
	_, err = clients[0].Get(ctx, "/api/v1/catalog/v1/fees", time.Now())
	require.NoError(t, err)
	// Redis failure must not silently turn into three independent full budgets.
	server.Close()
	_, err = clients[0].Get(ctx, "/api/v1/catalog/v1/fees", time.Now())
	require.ErrorContains(t, err, "budget unavailable")
	common.RDB = nil
	_, err = clients[0].Get(ctx, "/api/v1/catalog/v1/fees", time.Now())
	require.ErrorContains(t, err, "budget unavailable")
}

// Set OPS_TEST_REDIS_ADDR to a dedicated isolated Redis. Only this test's
// randomly named keys are removed; no shared database is flushed.
func TestReadBudgetRealRedisOptIn(t *testing.T) {
	addr := os.Getenv("OPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("OPS_TEST_REDIS_ADDR not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	before, enabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() { client.Close(); common.RDB, common.RedisEnabled = before, enabled })
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	id := "isolated-budget-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	first, second := New("https://local.invalid", id, "test-only", time.Second), New("https://local.invalid", id, "test-only", time.Second)
	t.Cleanup(func() { client.Del(ctx, first.budgetKey(), first.budgetKey()+":cooldown") })
	var workers sync.WaitGroup
	var admitted atomic.Int64
	for i := range 100 {
		workers.Go(func() {
			c := first
			if i%2 == 0 {
				c = second
			}
			err := c.reserveRead(ctx)
			if err == nil {
				admitted.Add(1)
				return
			}
			_, deferred := ReadDeferred(err)
			if !deferred {
				t.Errorf("unexpected budget failure: %v", err)
			}
		})
	}
	workers.Wait()
	require.Equal(t, int64(backgroundReadLimit), admitted.Load())
	for range readBudgetLimit - backgroundReadLimit {
		require.NoError(t, first.reserveRead(CallBalanceContext(ctx)))
	}
	_, deferred := ReadDeferred(second.reserveRead(CallBalanceContext(ctx)))
	require.True(t, deferred)
	first.coolReadBudget(ctx, 90*time.Second)
	second.coolReadBudget(ctx, time.Second)
	wait, deferred := ReadDeferred(second.reserveRead(CallBalanceContext(ctx)))
	require.True(t, deferred)
	require.Greater(t, wait, 89*time.Second)
}

func TestReadBudgetSharesServerCooldownAndKeepsLocalFallback(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	before, enabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() { client.Close(); common.RDB, common.RedisEnabled = before, enabled })
	calls := 0
	ops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(429)
	}))
	defer ops.Close()
	first, second := New(ops.URL, "cooldown-test", "test-only", time.Second), New(ops.URL, "cooldown-test", "test-only", time.Second)
	first.EnableReadBudget()
	second.EnableReadBudget()
	_, err := first.Get(context.Background(), "/api/v1/catalog/v1/fees", time.Now())
	var failure *ReadError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, 429, failure.Status)
	_, err = second.Get(CallBalanceContext(context.Background()), "/api/v1/catalog/v1/balances/15", time.Now())
	wait, deferred := ReadDeferred(err)
	require.True(t, deferred)
	require.Greater(t, wait, 89*time.Second)
	require.Equal(t, 1, calls)
	common.RedisEnabled = false
	for range backgroundReadLimit {
		require.NoError(t, first.reserveRead(context.Background()))
	}
	_, deferred = ReadDeferred(second.reserveRead(context.Background()))
	require.True(t, deferred)
}
