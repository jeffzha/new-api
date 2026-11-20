package opsbilling

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const emptyBalances = `{"data":{"items":[],"nextCursor":null,"hasMore":false}}`

func TestWaitReceiptWakesOnValidatedReceiptAndHonorsBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New(Config{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan string, 1)
		go func() { result <- r.WaitReceipt(ctx, "wake", 15, "m", time.Second).Status }()
		// Synchronize registration; no sleep-based timing assertion is needed.
		synctest.Wait()
		r.AcceptPushReceipts([]byte(`{"events":[{"requestId":"wake","userId":"15","model":"m"}]}`), []byte(`{"data":{"charges":[{"requestId":"wake","userId":"15","model":"m","status":"charged","amount":"0.2","refundAmount":"0.1","netAmount":"0.1","chargedAt":"2026-11-17T01:00:00Z"}]}}`), time.Now())
		select {
		case status := <-result:
			assert.Equal(t, "available", status)
		case <-time.After(time.Second):
			t.Fatal("validated receipt did not wake caller")
		}
		assert.Equal(t, "pending", r.WaitReceipt(ctx, "wake", 16, "m", 0).Status)
		cancel()
		assert.Equal(t, "pending", r.WaitReceipt(ctx, "cancelled", 15, "m", time.Hour).Status)
		r.mu.Lock()
		assert.Zero(t, r.waiters)
		r.waiters = 1024
		r.mu.Unlock()
		assert.Equal(t, "pending", r.WaitReceipt(context.Background(), "full", 15, "m", time.Hour).Status)
	})
	synctest.Test(t, func(t *testing.T) {
		r := New(Config{})
		start := time.Now()
		got := r.WaitReceipt(context.Background(), "never-arrives", 15, "m", time.Hour)
		assert.Equal(t, time.Second, time.Since(start), "wait must be capped without polling")
		assert.Equal(t, "pending", got.Status)
		assert.Nil(t, got.Amount)
		require.NotNil(t, got.RetryAfterSeconds)
		assert.Equal(t, 1, *got.RetryAfterSeconds)
		assert.Zero(t, r.waiters)
	})
}

func BenchmarkCachedChargeReceipt(b *testing.B) {
	r := New(Config{})
	r.AcceptPushReceipts([]byte(`{"events":[{"requestId":"hot","userId":"15","model":"m"}]}`), []byte(`{"data":{"charges":[{"requestId":"hot","userId":"15","model":"m","status":"charged","amount":"0.2","refundAmount":"0","netAmount":"0.2","chargedAt":"2026-11-17T01:00:00Z"}]}}`), time.Now())
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if r.WaitReceipt(context.Background(), "hot", 15, "m", time.Second).Status != "available" {
				b.Fatal("confirmed receipt unavailable")
			}
		}
	})
}

func TestReceiptCapacityEvictsOldEntriesWithoutStoppingNewCalls(t *testing.T) {
	r := New(Config{})
	at := time.Now()
	for i := range cacheLimit {
		require.True(t, r.Track(strconv.Itoa(i), 15, "m", at))
	}
	// Keep an actively queried receipt while the oldest invocation is evicted.
	require.Equal(t, "pending", r.Receipt("0", 15, "m", at).Status)
	require.True(t, r.Track("new", 15, "m", at))
	assert.Len(t, r.charges, cacheLimit)
	assert.NotNil(t, r.charges[chargeKey{"0", "15", "m"}])
	assert.Nil(t, r.charges[chargeKey{"1", "15", "m"}])
	assert.NotNil(t, r.charges[chargeKey{"new", "15", "m"}])
}

func TestCachedLogFeeRejectsAmbiguousOrInvalidReceipts(t *testing.T) {
	at := time.Now()
	for _, tc := range []struct {
		name, status, fee string
		want              bool
	}{
		{"legacy receipt", "charged", `null`, false},
		{"pending", "pending", `{"requestId":"r","userId":"15","model":"m","opsAmount":"1","chargedAt":"2026-11-17T01:00:00Z"}`, false},
		{"wrong user", "charged", `{"requestId":"r","userId":"16","model":"m","opsAmount":"1","chargedAt":"2026-11-17T01:00:00Z"}`, false},
		{"wrong model", "charged", `{"requestId":"r","userId":"15","model":"other","opsAmount":"1","chargedAt":"2026-11-17T01:00:00Z"}`, false},
		{"wrong request", "charged", `{"requestId":"other","userId":"15","model":"m","opsAmount":"1","chargedAt":"2026-11-17T01:00:00Z"}`, false},
		{"unknown amount", "charged", `{"requestId":"r","userId":"15","model":"m","chargedAt":"2026-11-17T01:00:00Z"}`, false},
		{"negative fee", "charged", `{"requestId":"r","userId":"15","model":"m","opsAmount":"-1","chargedAt":"2026-11-17T01:00:00Z"}`, false},
		{"invalid refund", "charged", `{"requestId":"r","userId":"15","model":"m","opsAmount":"1","refundAmount":"1e100","chargedAt":"2026-11-17T01:00:00Z"}`, false},
		{"invalid time", "charged", `{"requestId":"r","userId":"15","model":"m","opsAmount":"1","chargedAt":"bad"}`, false},
		{"confirmed zero", "charged", `{"requestId":"r","userId":"15","model":"m","opsAmount":"0","difference":"-0.1","chargedAt":"2026-11-17T01:00:00Z"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Config{})
			raw := []byte(`{"data":{"charges":[{"requestId":"r","userId":"15","status":"` + tc.status + `","amount":"1","refundAmount":"0","netAmount":"1","chargedAt":"2026-11-17T01:00:00Z","logFee":` + tc.fee + `}]}}`)
			r.AcceptPushReceipts([]byte(`{"events":[{"requestId":"r","userId":"15","model":"m"}]}`), raw, at)
			fee := r.CachedLogFee("r", 15, "m", at)
			assert.Equal(t, tc.want, fee != nil)
			if tc.want {
				require.Equal(t, "0", *fee.Amount)
			}
		})
	}
}

func TestOldBackgroundReadDoesNotReplaceImmediatePushCharge(t *testing.T) {
	r := New(Config{})
	at := time.Now()
	r.Track("fast", 15, "m", at)
	key := chargeKey{"fast", "15", "m"}
	require.NoError(t, r.applyCharge([]byte(`{"data":{"requestId":"fast","userId":"15","status":"charged","amount":"0.1","refundAmount":"0","netAmount":"0.1","chargedAt":"2026-11-16T01:00:00Z"}}`), key, at.Add(time.Second)))
	require.NoError(t, r.applyCharge([]byte(`{"data":{"requestId":"fast","userId":"15","status":"not_received"}}`), key, at))
	require.Equal(t, "available", r.Receipt("fast", 15, "m", at.Add(2*time.Second)).Status)
}

func TestPushReceiptAndAdjustmentNeverOverwriteNewerBalance(t *testing.T) {
	r := New(Config{})
	at := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	r.AcceptAdjustment("15", "1", 68493, 9, at.Format(time.RFC3339))
	r.AcceptAdjustment("15", "2", 136986, 7, at.Format(time.RFC3339))
	assert.Equal(t, "1", *r.Balance(15, time.Now()).Amount)
	raw := []byte(`{"data":{"charges":[{"requestId":"req-1","userId":"15","status":"charged","amount":"0.1","refundAmount":"0","netAmount":"0.1","balanceAfter":"999","chargedAt":"2026-11-12T01:00:00Z"},{"requestId":"foreign","userId":"15","status":"pending"}]}}`)
	r.AcceptPushReceipts([]byte(`{"events":[{"requestId":"req-1","userId":"15","model":"m"}]}`), raw, at)
	assert.Equal(t, "available", r.Receipt("req-1", 15, "m", at).Status)
	assert.Equal(t, "1", *r.Balance(15, time.Now()).Amount)
	assert.Len(t, r.charges, 1)
}

func readerServer(t *testing.T, handler http.HandlerFunc) *Reader {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.True(t, strings.HasPrefix(req.URL.Path, "/test/api/v1/catalog/v1/"))
		target := strings.TrimPrefix(req.URL.RequestURI(), "/test")
		expected := opsclient.SignWithNonce("station", "test-secret-billing-only", "GET", target, nil, req.Header.Get("X-Timestamp"), req.Header.Get("X-Nonce"))
		assert.Equal(t, expected.Get("X-Signature"), req.Header.Get("X-Signature"))
		assert.Empty(t, req.URL.Query().Get("tenantId"))
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	return New(Config{BaseURL: server.URL + "/test", KeyID: "station", Secret: "test-secret-billing-only", Timeout: time.Second})
}

func TestChargeStatesAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body, status, chargeStatus, reason string
		amount                                   *string
		invalid                                  bool
	}{
		{"charged zero", `{"status":"charged","userId":"15","amount":"0","refundAmount":"0","netAmount":"0","chargedAt":"2026-11-04T01:00:00Z"}`, "available", "charged", "", stringPointer("0"), false},
		{"pending", `{"status":"pending","userId":"15","amount":"99"}`, "pending", "pending", "awaiting_ops_charge", nil, false},
		{"failed", `{"status":"failed","userId":"15","reason":"private internal detail"}`, "unavailable", "failed", "ops_charge_failed", nil, false},
		{"not received", `{"status":"not_received"}`, "pending", "not_received", "ops_usage_not_received", nil, false},
		{"old locally billed", `{"status":"station_billed","userId":"15","amount":"0.1"}`, "unavailable", "station_billed", "station_billed", nil, false},
		{"foreign user", `{"status":"pending","userId":"16"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
		{"foreign request", `{"status":"pending","userId":"15","requestId":"another"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
		{"foreign model", `{"status":"pending","userId":"15","model":"another"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
		{"unknown status", `{"status":"ok","userId":"15"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
		{"missing charged money", `{"status":"charged","userId":"15"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
		{"inconsistent refund", `{"status":"charged","userId":"15","amount":"0.1","refundAmount":"0.2","netAmount":"0","chargedAt":"2026-11-04T01:00:00Z"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
		{"negative debit", `{"status":"pending","userId":"15","amount":"-1"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
		{"exponent overflow", `{"status":"pending","userId":"15","amount":"1e99999999"}`, "unavailable", "", "ops_charge_read_unavailable", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
				reads++
				if strings.Contains(req.URL.Path, "/charges/") {
					assert.Empty(t, req.URL.RawQuery)
					_, _ = w.Write([]byte(`{"data":` + tc.body + `}`))
				} else {
					_, _ = w.Write([]byte(emptyBalances))
				}
			})
			at := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
			initial := r.Receipt("request-1", 15, "model", at)
			assert.Equal(t, "pending", initial.Status)
			assert.Nil(t, initial.Amount)
			require.NotNil(t, initial.RetryAfterSeconds)
			assert.Equal(t, 1, *initial.RetryAfterSeconds)
			assert.Zero(t, reads, "receipt must not wait on HTTP")
			require.NoError(t, r.Step(context.Background(), at))
			err := r.Step(context.Background(), at.Add(3*time.Second))
			assert.Equal(t, tc.invalid, err != nil)
			got := r.Receipt("request-1", 15, "model", at.Add(4*time.Second))
			assert.Equal(t, tc.status, got.Status)
			assert.Equal(t, tc.chargeStatus, got.ChargeStatus)
			assert.Equal(t, tc.reason, got.Reason)
			assert.Equal(t, tc.amount, got.Amount)
			switch {
			case got.Status == "pending":
				require.NotNil(t, got.RetryAfterSeconds)
				assert.Equal(t, 1, *got.RetryAfterSeconds)
				assert.Nil(t, got.Amount)
			case tc.chargeStatus == "failed":
				require.NotNil(t, got.RetryAfterSeconds)
				assert.Equal(t, 300, *got.RetryAfterSeconds)
			case tc.invalid:
				require.NotNil(t, got.RetryAfterSeconds)
				assert.Equal(t, 30, *got.RetryAfterSeconds)
			default:
				assert.Nil(t, got.RetryAfterSeconds)
			}
			assert.Nil(t, got.Quota)
			assert.Equal(t, 2, reads)
			assert.Nil(t, r.Receipt("request-1", 16, "model", at.Add(5*time.Second)).Amount)
		})
	}
}

func stringPointer(s string) *string { return &s }

func TestWarmBalancesBeforeFirstPageVisit(t *testing.T) {
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/balances/16") {
			_, _ = w.Write([]byte(`{"data":{"userId":"16","balance":"0","balanceQuota":0,"negative":false,"lastEntryId":0,"updatedAt":"2026-11-04T01:00:00Z"}}`))
			return
		}
		_, _ = w.Write([]byte(emptyBalances))
	})
	at := time.Now()
	r.UserIDs = func(ctx context.Context, after, limit int) ([]int, error) {
		assert.Equal(t, 0, after)
		assert.Equal(t, 500, limit)
		return []int{16}, nil
	}
	require.NoError(t, r.Step(context.Background(), at))
	require.NoError(t, r.Step(context.Background(), at.Add(3*time.Second)))
	view := r.Balance(16, at.Add(4*time.Second))
	require.NotNil(t, view.Amount, "zero-balance users must be cached without opening a page")
	assert.Equal(t, "0", *view.Amount)
}

func TestCallGuardDoesNotTrustDisplaySnapshotOrIgnoreFailures(t *testing.T) {
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(503) })
	at := time.Now()
	quota := int64(50000)
	r.balances["15"] = &cachedBalance{value: &Balance{UserID: "15", Amount: "1", Quota: &quota}, checked: at, complete: true}
	assert.ErrorIs(t, r.CheckCall(context.Background(), 15, at), ErrBalanceUnavailable)
	assert.Equal(t, "1", *r.Balance(15, at).Amount, "failed authorization must not erase last known display money")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, r.CheckCall(ctx, 15, at.Add(time.Second)), ErrBalanceUnavailable)
	assert.ErrorIs(t, r.CheckCall(context.Background(), 0, at), ErrBalanceUnavailable)
}

func TestCallDecisionInvalidatesAfterCallAndNewLedgerEntry(t *testing.T) {
	r := New(Config{BaseURL: "https://ops.example/test", Timeout: time.Second})
	at := time.Now()
	r.balances["15"] = &cachedBalance{}
	require.NoError(t, r.applyBalance([]byte(`{"data":{"userId":"15","balance":"1","balanceQuota":50000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`), "15", at))
	ok, err := r.callDecision("15", 0, at)
	require.True(t, ok)
	require.NoError(t, err)
	r.InvalidateCall(15)
	ok, _ = r.callDecision("15", 0, at)
	assert.False(t, ok)
	require.NoError(t, r.applyBalance([]byte(`{"data":{"userId":"15","balance":"1","balanceQuota":50000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`), "15", at))
	require.NoError(t, r.applyPage([]byte(`{"data":{"items":[{"entryId":2,"userId":"15","kind":"charge","amount":"-2","balanceAfter":"-1","balanceAfterQuota":-50000,"postedAt":"2026-11-04T01:00:00Z"}],"nextCursor":"2","hasMore":false}}`), at))
	ok, _ = r.callDecision("15", 0, at)
	assert.False(t, ok, "a ledger change must invalidate the earlier authorization")
	assert.True(t, *r.Balance(15, at).Negative)
}

func TestCallBalanceGuardRejectsNegativeAndRefreshesStale(t *testing.T) {
	negative := true
	reads := 0
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		reads++
		value := `"balance":"-0.000001","balanceQuota":0,"negative":true,"lastEntryId":1`
		if !negative {
			value = `"balance":"1","balanceQuota":50000,"negative":false,"lastEntryId":2`
		}
		_, _ = w.Write([]byte(`{"data":{"userId":"15",` + value + `,"updatedAt":"2026-11-04T01:00:00Z"}}`))
	})
	at := time.Now()
	assert.ErrorIs(t, r.CheckCall(context.Background(), 15, at), ErrInsufficientBalance)
	assert.ErrorIs(t, r.CheckCall(context.Background(), 15, at.Add(time.Second)), ErrInsufficientBalance)
	assert.Equal(t, 1, reads, "concurrent/repeated calls reuse only the short-lived authorization read")
	negative = false
	require.NoError(t, r.CheckCall(context.Background(), 15, at.Add(6*time.Second)))
	assert.Equal(t, 2, reads)
	r.mu.Lock()
	r.balances["15"].failed = true
	r.mu.Unlock()
	r.nextRead = at.Add(time.Minute)
	assert.ErrorIs(t, r.CheckCall(context.Background(), 15, at.Add(7*time.Second)), ErrBalanceUnavailable)
	assert.Equal(t, 2, reads)
}

func TestCallQuotaUsesOPSBalanceForZeroAndEstimate(t *testing.T) {
	for _, tc := range []struct {
		name, money string
		quota       int64
		estimate    int
		denied      bool
	}{
		{"zero cannot submit even zero estimate", "0", 0, 0, true},
		{"positive below one quota cannot submit", "0.000001", 0, 0, true},
		{"insufficient estimate", "1", 50000, 50001, true},
		{"exact estimate", "1", 50000, 50000, false},
		{"zero estimate with positive balance", "1", 50000, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
				reads++
				raw, err := common.Marshal(map[string]any{"data": map[string]any{"userId": "15", "balance": tc.money, "balanceQuota": tc.quota, "negative": false, "lastEntryId": 1, "updatedAt": "2026-11-04T01:00:00Z"}})
				require.NoError(t, err)
				_, _ = w.Write(raw)
			})
			err := r.CheckQuota(context.Background(), 15, tc.estimate, time.Now())
			if tc.denied {
				assert.ErrorIs(t, err, ErrInsufficientBalance)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, 1, reads)
		})
	}
}

func TestPendingChargeRefreshesBeforeThirtySeconds(t *testing.T) {
	charged := false
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		if !strings.Contains(req.URL.Path, "/charges/") {
			_, _ = w.Write([]byte(emptyBalances))
			return
		}
		if !charged {
			_, _ = w.Write([]byte(`{"data":{"status":"pending","userId":"15"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"status":"charged","userId":"15","amount":"0.1","refundAmount":"0","netAmount":"0.1","chargedAt":"2026-11-04T01:00:00Z"}}`))
	})
	at := time.Now()
	r.Receipt("request", 15, "model", at)
	require.NoError(t, r.Step(context.Background(), at))
	require.NoError(t, r.Step(context.Background(), at.Add(3*time.Second)))
	charged = true
	require.NoError(t, r.Step(context.Background(), at.Add(8*time.Second)))
	receipt := r.Receipt("request", 15, "model", at.Add(9*time.Second))
	assert.Equal(t, "available", receipt.Status)
	require.NotNil(t, receipt.Amount)
	assert.Equal(t, "0.1", *receipt.Amount)
}

func TestRefundRefreshDoesNotLeakOrOverwriteBalance(t *testing.T) {
	refunded := false
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "/charges/") {
			refund, net := "0", "0.12"
			if refunded {
				refund, net = "0.02", "0.1"
			}
			_, _ = w.Write([]byte(`{"data":{"status":"charged","userId":"15","amount":"0.12","refundAmount":"` + refund + `","netAmount":"` + net + `","balanceAfter":"5","balance":"4","chargedAt":"2026-11-04T01:00:00Z"}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"items":[{"entryId":8,"userId":"15","kind":"charge","amount":"-0.2","balanceAfter":"3","balanceAfterQuota":150000,"postedAt":"2026-11-04T01:00:00Z"}],"nextCursor":"8","hasMore":false}}`))
		}
	})
	at := time.Now().UTC()
	require.True(t, r.Track("request", 15, "model", at))
	require.NoError(t, r.Step(context.Background(), at))
	require.NoError(t, r.Step(context.Background(), at.Add(3*time.Second)))
	assert.Equal(t, "3", *r.Balance(15, at).Amount)
	refunded = true
	require.NoError(t, r.Step(context.Background(), at.Add(33*time.Second)))
	got := r.Receipt("request", 15, "model", at.Add(34*time.Second))
	assert.Equal(t, "0.12", *got.Amount)
	assert.Equal(t, "0.02", *got.RefundAmount)
	assert.Equal(t, "0.1", *got.NetAmount)
	raw, err := common.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "balance")
	assert.Equal(t, "3", *r.Balance(15, at.Add(34*time.Second)).Amount)
}

func TestBalancePagesReplayAndAtomicValidation(t *testing.T) {
	page := 0
	invalid := false
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		page++
		switch page {
		case 1:
			assert.Empty(t, req.URL.RawQuery)
			_, _ = w.Write([]byte(`{"data":{"items":[{"entryId":1,"userId":"15","kind":"opening","amount":"1","balanceAfter":"1","balanceAfterQuota":50000,"postedAt":"2026-11-04T01:00:00Z"}],"nextCursor":"1","hasMore":true}}`))
		default:
			assert.Equal(t, "1", req.URL.Query().Get("since"))
			more := `{"entryId":2,"userId":"15","kind":"charge","amount":"-1.1","balanceAfter":"-0.1","balanceAfterQuota":-5000,"postedAt":"2026-11-04T01:01:00Z"}`
			if invalid {
				more += `,{"entryId":3,"userId":"15","kind":"charge","amount":"1e999999"}`
			}
			_, _ = w.Write([]byte(`{"data":{"items":[` + more + `],"nextCursor":"2","hasMore":false}}`))
		}
	})
	at := time.Now().UTC()
	require.NoError(t, r.Step(context.Background(), at))
	assert.Equal(t, "pending", r.Balance(15, at).Status, "do not expose partial replay as current balance")
	invalid = true
	require.Error(t, r.Step(context.Background(), at.Add(3*time.Second)))
	assert.Nil(t, r.Balance(15, at.Add(4*time.Second)).Amount)
	invalid = false
	require.NoError(t, r.Step(context.Background(), at.Add(63*time.Second)))
	got := r.Balance(15, at.Add(64*time.Second))
	require.NotNil(t, got.Amount)
	assert.Equal(t, "-0.1", *got.Amount)
	assert.Equal(t, int64(-5000), *got.Quota)
	assert.True(t, *got.Negative)
	assert.Equal(t, "available", got.Status)
	assert.Equal(t, "stale", r.Balance(15, at.Add(5*time.Minute)).Status)
}

func TestSingleBalanceNegativeTruncationAndUnknownUser(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		ok         bool
	}{
		{"negative truncation", `{"data":{"userId":"15","balance":"-0.000001","balanceQuota":0,"negative":true,"lastEntryId":"3","updatedAt":"2026-11-04T01:00:00Z","station":{"quotaPerUnit":500000,"usdExchangeRate":"7.3"}}}`, 200, true},
		{"wrong rounding", `{"data":{"userId":"15","balance":"-0.000001","balanceQuota":-1,"negative":true,"lastEntryId":3,"updatedAt":"2026-11-04T01:00:00Z","station":{"quotaPerUnit":500000,"usdExchangeRate":"7.3"}}}`, 200, false},
		{"unknown user", `{"error":{"code":"unknown_user"}}`, 404, false},
		{"not started", `{"error":{"code":"ops_balance_not_started"}}`, 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
				count++
				if strings.HasSuffix(req.URL.Path, "/balances/15") {
					assert.Empty(t, req.URL.RawQuery)
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
				} else {
					_, _ = w.Write([]byte(emptyBalances))
				}
			})
			at := time.Now()
			assert.Nil(t, r.Balance(15, at).Amount)
			assert.Zero(t, count)
			require.NoError(t, r.Step(context.Background(), at))
			err := r.Step(context.Background(), at.Add(3*time.Second))
			assert.Equal(t, tc.ok, err == nil)
			got := r.Balance(15, at.Add(4*time.Second))
			if tc.ok {
				assert.Equal(t, "-0.000001", *got.Amount)
				assert.Equal(t, int64(0), *got.Quota)
				assert.True(t, *got.Negative)
			} else {
				assert.Nil(t, got.Amount)
				assert.Equal(t, "unavailable", got.Status)
			}
		})
	}
}

func TestBackoffAndNoBusyPolling(t *testing.T) {
	reads := 0
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		reads++
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	})
	at := time.Now()
	require.Error(t, r.Step(context.Background(), at))
	require.NoError(t, r.Step(context.Background(), at.Add(119*time.Second)))
	assert.Equal(t, 1, reads)
	require.Error(t, r.Step(context.Background(), at.Add(120*time.Second)))
	assert.Equal(t, 2, reads)
}

func TestRepeatedBalancePageNeverCreditsTwiceAndStaleSnapshotIsRejected(t *testing.T) {
	const page = `{"data":{"items":[{"entryId":8,"userId":"15","kind":"credit","amount":"1","balanceAfter":"5","balanceAfterQuota":250000,"postedAt":"2026-11-04T01:00:00Z"}],"nextCursor":"8","hasMore":false}}`
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte(page)) })
	at := time.Now()
	require.NoError(t, r.Step(context.Background(), at))
	require.NoError(t, r.Step(context.Background(), at.Add(time.Minute)))
	assert.Equal(t, "5", *r.Balance(15, at.Add(time.Minute)).Amount)
	require.Error(t, r.applyBalance([]byte(`{"data":{"userId":"15","balance":"6","balanceQuota":300000,"negative":false,"lastEntryId":7,"updatedAt":"2026-11-04T01:00:00Z"}}`), "15", at))
	require.Error(t, r.applyPage([]byte(strings.ReplaceAll(page, `"balanceAfter":"5"`, `"balanceAfter":"6"`)), at))
	assert.Equal(t, "5", *r.Balance(15, at.Add(time.Minute)).Amount)
}

func TestCanceledReadKeepsAmountsUnknown(t *testing.T) {
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte(emptyBalances)) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	at := time.Now()
	assert.Nil(t, r.Balance(15, at).Amount)
	require.Error(t, r.Step(ctx, at))
	assert.Nil(t, r.Balance(15, at).Amount)
	assert.Equal(t, "unavailable", r.Balance(15, at).Status)
}

func TestReaderRestartStartsFromBeginningAndDefaultsDisabled(t *testing.T) {
	t.Setenv("OPS_BILLING_ENABLED", "")
	t.Setenv("OPS_BILLING_RECEIPTS", "")
	old := Default
	Default = nil
	t.Cleanup(func() { Default = old })
	require.NoError(t, StartConfigured(context.Background()))
	assert.Nil(t, Default)
	t.Setenv("OPS_BILLING_RECEIPTS", "charges")
	require.Error(t, StartConfigured(context.Background()))
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		assert.Empty(t, req.URL.RawQuery)
		_, _ = w.Write([]byte(emptyBalances))
	})
	require.NoError(t, r.Step(context.Background(), time.Now()))
	assert.Nil(t, r.Balance(15, time.Now()).Amount)
}

func TestMissingBalanceDoesNotBlockOtherUsers(t *testing.T) {
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/balances/15") {
			w.WriteHeader(404)
			return
		}
		if strings.HasSuffix(req.URL.Path, "/balances/16") {
			_, _ = w.Write([]byte(`{"data":{"userId":"16","balance":"2","balanceQuota":100000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`))
			return
		}
		_, _ = w.Write([]byte(emptyBalances))
	})
	at := time.Now()
	r.Balance(15, at)
	require.NoError(t, r.Step(context.Background(), at))
	require.Error(t, r.Step(context.Background(), at.Add(3*time.Second)))
	r.Balance(16, at.Add(4*time.Second))
	require.NoError(t, r.Step(context.Background(), at.Add(6*time.Second)))
	balance := r.Balance(16, at.Add(7*time.Second))
	require.NotNil(t, balance.Amount)
	assert.Equal(t, "2", *balance.Amount)
}

func TestZeroBalanceRefreshesWithoutLedgerChanges(t *testing.T) {
	reads := 0
	r := readerServer(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/balances/15") {
			reads++
			_, _ = w.Write([]byte(`{"data":{"userId":"15","balance":"0","balanceQuota":0,"negative":false,"lastEntryId":0,"updatedAt":"2026-11-04T01:00:00Z"}}`))
			return
		}
		_, _ = w.Write([]byte(emptyBalances))
	})
	at := time.Now()
	r.Balance(15, at)
	require.NoError(t, r.Step(context.Background(), at))
	require.NoError(t, r.Step(context.Background(), at.Add(3*time.Second)))
	r.Balance(15, at.Add(65*time.Second))
	require.NoError(t, r.Step(context.Background(), at.Add(65*time.Second)))
	require.NoError(t, r.Step(context.Background(), at.Add(68*time.Second)))
	assert.False(t, r.Balance(15, at.Add(69*time.Second)).Stale)
	assert.Equal(t, 2, reads, "users without ledger entries still need refreshes")
}

func TestBalanceSnapshotSurvivesRestartWithoutMixingProjects(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	enabled, old := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, client
	t.Cleanup(func() { common.RedisEnabled, common.RDB = enabled, old; _ = client.Close() })
	ctx := context.Background()
	c := Config{BaseURL: "https://ops.example/test", KeyID: "project", Secret: "test-secret-for-cache"}
	first := New(c)
	first.restoreSnapshot(ctx, "project-a")
	at := time.Now().Add(-4 * time.Minute)
	first.balances["15"] = &cachedBalance{}
	first.balances["16"] = &cachedBalance{}
	require.NoError(t, first.applyBalance([]byte(`{"data":{"userId":"15","balance":"-0.1","balanceQuota":-5000,"negative":true,"lastEntryId":3,"updatedAt":"2026-11-04T01:00:00Z"}}`), "15", at))
	require.NoError(t, first.applyBalance([]byte(`{"data":{"userId":"16","balance":"0","balanceQuota":0,"negative":false,"lastEntryId":0,"updatedAt":"2026-11-04T01:00:00Z"}}`), "16", at))
	first.saveSnapshot(ctx, time.Now())
	restarted := New(c)
	restarted.restoreSnapshot(ctx, "project-a")
	view := restarted.Balance(15, time.Now())
	require.NotNil(t, view.Amount)
	assert.Equal(t, "-0.1", *view.Amount)
	assert.True(t, view.Stale)
	zero := restarted.Balance(16, time.Now())
	require.NotNil(t, zero.Amount)
	assert.Equal(t, "0", *zero.Amount)
	assert.Nil(t, restarted.Balance(17, time.Now()).Amount)
	other := New(c)
	other.restoreSnapshot(ctx, "project-b")
	assert.Nil(t, other.Balance(15, time.Now()).Amount)
	// A recent saved value is useful immediately, but must be marked stale
	// until this process has completed its own ledger catch-up.
	recent := *first.balances["15"]
	recent.checked = time.Now()
	first.balances["15"] = &recent
	first.snapshotDue = time.Time{}
	first.saveSnapshot(ctx, time.Now())
	warming := New(c)
	warming.restoreSnapshot(ctx, "project-a")
	assert.Equal(t, "stale", warming.Balance(15, time.Now()).Status)
	server.FastForward(25 * time.Hour)
	expired := New(c)
	expired.restoreSnapshot(ctx, "project-a")
	assert.Nil(t, expired.Balance(15, time.Now()).Amount)
}

func TestUSDWalletUsesGatewayUnitsWithoutChangingWireReceipt(t *testing.T) {
	r := New(Config{Currency: "USD"})
	at := time.Now()
	r.Balance(15, at)
	const balance = `{"data":{"userId":"15","balance":"1.000001","balanceQuota":1000001,"negative":false,"lastEntryId":7,"updatedAt":"2026-11-19T01:00:00Z","station":{"quotaPerUnit":1000000,"usdExchangeRate":"1"}}}`
	require.NoError(t, r.applyBalance([]byte(balance), "15", at))
	view := r.Balance(15, at)
	assert.Equal(t, "USD", view.Currency)
	require.NotNil(t, view.Quota)
	assert.Equal(t, int64(500000), *view.Quota)
	assert.Equal(t, int64(1000001), *r.balances["15"].value.Quota)
	ok, err := r.callDecision("15", 500001, at)
	require.True(t, ok)
	require.ErrorIs(t, err, ErrInsufficientBalance)
	ok, err = r.callDecision("15", 500000, at)
	require.True(t, ok)
	require.NoError(t, err)
	for _, invalid := range []string{
		strings.ReplaceAll(balance, "1000001", "500000"),
		strings.ReplaceAll(balance, `"usdExchangeRate":"1"`, `"usdExchangeRate":"7.3"`),
		strings.ReplaceAll(balance, `"userId":"15"`, `"userId":"15","currency":"CNY"`),
	} {
		require.Error(t, r.applyBalance([]byte(invalid), "15", at.Add(time.Second)))
	}
	assert.Equal(t, "1.000001", *r.Balance(15, at).Amount)
	r.AcceptPushReceipts([]byte(`{"events":[{"requestId":"usd-receipt","userId":"15","model":"m"}]}`), []byte(`{"data":{"charges":[{"requestId":"usd-receipt","userId":"15","model":"m","status":"charged","amount":"0.2","refundAmount":"0.1","netAmount":"0.1","balanceAfter":"999","chargedAt":"2026-11-19T01:00:00Z"}]}}`), at)
	charge := r.Receipt("usd-receipt", 15, "m", at)
	assert.Equal(t, "USD", charge.Currency)
	assert.Equal(t, "available", charge.Status)
	assert.Equal(t, "0.1", *charge.NetAmount)
	assert.Equal(t, "1.000001", *r.Balance(15, at).Amount, "historical charge must not replace current wallet")
	for _, tc := range []struct {
		name, model, ledger string
		wantPosted          bool
	}{
		{"legacy receipt cannot release capacity", "m", "", false},
		{"missing model cannot release capacity", "", `,"ledgerEntryId":8`, false},
		{"foreign model cannot release capacity", "other", `,"ledgerEntryId":8`, false},
		{"confirmed zero charge releases capacity", "m", `,"ledgerEntryId":8`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := New(Config{Currency: "USD"})
			body := fmt.Sprintf(`{"data":{"charges":[{"requestId":"r","userId":"15","model":%q,"status":"charged","amount":"0","refundAmount":"0","netAmount":"0","chargedAt":"2026-11-19T01:00:00Z"%s}]}}`, tc.model, tc.ledger)
			reader.AcceptPushReceipts([]byte(`{"events":[{"requestId":"r","userId":"15","model":"m"}]}`), []byte(body), at)
			id, posted := reader.ChargedLedgerEntry("r", 15, "m", at)
			assert.Equal(t, tc.wantPosted, posted)
			if tc.wantPosted {
				assert.Equal(t, int64(8), id)
			}
		})
	}
	r.AcceptAdjustment("15", "0.5", 500000, 8, at.Format(time.RFC3339))
	assert.True(t, r.Track("pt-"+strings.Repeat("a", 64), 15, "m", at), "terminal task IDs use the shared project contract")
	assert.False(t, r.Track(strings.Repeat("a", 81), 15, "m", at))
	assert.Equal(t, int64(250000), *r.Balance(15, at).Quota)
	negative := strings.ReplaceAll(balance, `"1.000001"`, `"-0.000001"`)
	negative = strings.ReplaceAll(negative, "1000001", "-1")
	negative = strings.ReplaceAll(negative, `"negative":false`, `"negative":true`)
	negative = strings.ReplaceAll(negative, `"lastEntryId":7`, `"lastEntryId":9`)
	require.NoError(t, r.applyBalance([]byte(negative), "15", at.Add(time.Second)))
	assert.Zero(t, *r.Balance(15, at.Add(time.Second)).Quota)
	ok, err = r.callDecision("15", 0, at.Add(time.Second))
	require.True(t, ok)
	require.ErrorIs(t, err, ErrInsufficientBalance)
}
