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

func TestReadBudgetConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, total, background   string
		wantTotal, wantBackground int
		invalid                   bool
	}{
		{"default", "", "", 54, 42, false},
		{"lower-server-limit", "18", "", 18, 14, false},
		{"explicit-reserve", "18", "10", 18, 10, false},
		{"invalid-total", "x", "", 0, 0, true},
		{"no-reserve", "18", "18", 0, 0, true},
		{"no-background", "18", "0", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPS_READ_BUDGET_PER_MINUTE", tc.total)
			t.Setenv("OPS_BACKGROUND_READ_BUDGET_PER_MINUTE", tc.background)
			c := New("https://local.invalid", tc.name, "test-only", time.Second)
			err := c.EnableReadBudget()
			if tc.invalid {
				require.Error(t, err)
				require.False(t, c.readBudget)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantTotal, c.readBudgetLimit)
			require.Equal(t, tc.wantBackground, c.backgroundReadLimit)
		})
	}
}

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

func TestIngestBudgetIsolatesFundingAndSharesFleetAllowance(t *testing.T) {
	t.Setenv("OPS_INGEST_BUDGET_ENABLED", "true")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	before, enabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() { _ = client.Close(); common.RDB, common.RedisEnabled = before, enabled })
	ctx := context.Background()
	const base, key = "https://ops.example.test/test", "funding-fleet"
	for _, lane := range []struct {
		kind  IngestLane
		limit int
	}{{IngestFunding, 18}, {IngestMetadata, 12}, {IngestUsage, 78}} {
		for i := range lane.limit {
			// Separate callers and equivalent base URLs share the same lane.
			url := base
			if i%2 == 0 {
				url += "/"
			}
			require.NoError(t, ReserveIngest(ctx, url, key, lane.kind))
		}
		wait, deferred := ReadDeferred(ReserveIngest(ctx, base, key, lane.kind))
		require.True(t, deferred)
		require.Positive(t, wait)
	}
	// Other stations remain independent, including a distinct /test scope.
	require.NoError(t, ReserveIngest(ctx, base, "other-key", IngestUsage))
	require.NoError(t, ReserveIngest(ctx, "https://ops.example.test", key, IngestUsage))
	server.FastForward(61 * time.Second)
	require.NoError(t, ReserveIngest(ctx, base, key, IngestUsage))
	CoolIngest(ctx, base, key, 90*time.Second)
	CoolIngest(ctx, base, key, time.Second)
	for _, lane := range []IngestLane{IngestUsage, IngestFunding, IngestMetadata} {
		wait, deferred := ReadDeferred(ReserveIngest(ctx, base, key, lane))
		require.True(t, deferred)
		require.Greater(t, wait, 89*time.Second)
	}
	server.Close()
	require.Error(t, ReserveIngest(ctx, base, key, IngestUsage))
	t.Setenv("OPS_INGEST_BUDGET_ENABLED", "false")
	require.NoError(t, ReserveIngest(ctx, base, key, IngestUsage), "unconfigured deployments retain their existing contract")
}

func TestIngestBudgetRealRedisOptIn(t *testing.T) {
	addr := os.Getenv("OPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("OPS_TEST_REDIS_ADDR not set")
	}
	t.Setenv("OPS_INGEST_BUDGET_ENABLED", "true")
	client := redis.NewClient(&redis.Options{Addr: addr})
	before, enabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() { _ = client.Close(); common.RDB, common.RedisEnabled = before, enabled })
	ctx := t.Context()
	require.NoError(t, client.Ping(ctx).Err())
	base, key := "https://ingest.example.invalid", common.GetUUID()
	for _, lane := range []struct {
		kind  IngestLane
		limit int64
	}{{IngestUsage, 78}, {IngestFunding, 18}, {IngestMetadata, 12}} {
		t.Cleanup(func() {
			k := ingestKey(base, key, lane.kind)
			_ = client.Del(context.Background(), k, k+":cooldown").Err()
		})
		var accepted, unexpected atomic.Int64
		var workers sync.WaitGroup
		// More callers than the contract's allowance, representing two slots.
		for i := range lane.limit + 2 {
			workers.Go(func() {
				url := base
				if i%2 == 0 {
					url += "/"
				}
				err := ReserveIngest(ctx, url, key, lane.kind)
				if err == nil {
					accepted.Add(1)
					return
				}
				if _, deferred := ReadDeferred(err); !deferred {
					unexpected.Add(1)
				}
			})
		}
		workers.Wait()
		require.Zero(t, unexpected.Load())
		require.Equal(t, lane.limit, accepted.Load())
	}
	CoolIngest(ctx, base, key, 90*time.Second)
	for _, lane := range []IngestLane{IngestUsage, IngestFunding, IngestMetadata} {
		wait, deferred := ReadDeferred(ReserveIngest(ctx, base, key, lane))
		require.True(t, deferred)
		require.Greater(t, wait, 89*time.Second)
	}
}

func TestRetryAfterHonorsBothServerFormats(t *testing.T) {
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"90", 90 * time.Second},
		{at.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"7200", 2 * time.Hour},
		{at.Add(2 * time.Hour).Format(http.TimeFormat), 2 * time.Hour},
		{"9223372036854775807", time.Duration((1<<63-1)/time.Second) * time.Second},
		{"-1", 0}, {"", 0}, {"invalid", 0},
		{at.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		t.Run(tc.value, func(t *testing.T) { require.Equal(t, tc.want, RetryAfter(tc.value, at)) })
	}
}
