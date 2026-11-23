package opsbilling

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type balanceTestTransport func(*http.Request) (*http.Response, error)

func (f balanceTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestConcurrentQuotaChecksShareOneReadAndKeepIndividualEstimates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New(Config{BaseURL: "https://ops.example", Timeout: time.Second})
		var reads atomic.Int32
		release := make(chan struct{})
		r.Client.HTTP.Transport = balanceTestTransport(func(req *http.Request) (*http.Response, error) {
			reads.Add(1)
			<-release
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"userId":"15","balance":"1","balanceQuota":50000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`))}, nil
		})
		results := make(chan error, 64)
		at := time.Now()
		for i := range 64 {
			go func() { results <- r.CheckQuota(context.Background(), 15, 50000+i%2, at) }()
		}
		synctest.Wait()
		assert.Equal(t, int32(1), reads.Load(), "one in-flight wallet read serves all callers")
		close(release)
		synctest.Wait()
		denied := 0
		for range 64 {
			err := <-results
			if err != nil {
				require.ErrorIs(t, err, ErrInsufficientBalance)
				denied++
			}
		}
		assert.Equal(t, 32, denied, "each caller still checks its own estimate")
		assert.Equal(t, int32(1), reads.Load())
		assert.Empty(t, r.balanceFlights)
	})
}

func TestCancelledQuotaCallerDoesNotCancelSharedWalletRead(t *testing.T) {
	for _, cancelOwner := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancelOwner=%t", cancelOwner), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := New(Config{BaseURL: "https://ops.example", Timeout: time.Second})
				var reads atomic.Int32
				release := make(chan struct{})
				r.Client.HTTP.Transport = balanceTestTransport(func(req *http.Request) (*http.Response, error) {
					reads.Add(1)
					select {
					case <-req.Context().Done():
						return nil, req.Context().Err()
					case <-release:
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"userId":"15","balance":"1","balanceQuota":50000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`))}, nil
					}
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ownerCtx, waiterCtx := context.Background(), ctx
				if cancelOwner {
					ownerCtx, waiterCtx = ctx, context.Background()
				}
				ownerResult, waiterResult := make(chan error, 1), make(chan error, 1)
				go func() { ownerResult <- r.CheckCall(ownerCtx, 15, time.Now()) }()
				synctest.Wait()
				go func() { waiterResult <- r.CheckCall(waiterCtx, 15, time.Now()) }()
				synctest.Wait()
				cancel()
				synctest.Wait()
				close(release)
				synctest.Wait()
				if cancelOwner {
					require.ErrorIs(t, <-ownerResult, ErrBalanceUnavailable)
					require.NoError(t, <-waiterResult)
				} else {
					require.NoError(t, <-ownerResult)
					require.ErrorIs(t, <-waiterResult, ErrBalanceUnavailable)
				}
				assert.Equal(t, int32(1), reads.Load())
				assert.False(t, r.balances["15"].failed)
				assert.Empty(t, r.balanceFlights)
			})
		})
	}
}

func TestConcurrentQuotaChecksKeepNegativeBalanceAndFailureProtection(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := New(Config{BaseURL: "https://ops.example", Timeout: time.Second})
				var reads atomic.Int32
				release := make(chan struct{})
				r.Client.HTTP.Transport = balanceTestTransport(func(req *http.Request) (*http.Response, error) {
					reads.Add(1)
					<-release
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":{"userId":"15","balance":"-0.000001","balanceQuota":0,"negative":true,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`))}, nil
				})
				results := make(chan error, 32)
				at := time.Now()
				for range 32 {
					go func() { results <- r.CheckCall(context.Background(), 15, at) }()
				}
				synctest.Wait()
				close(release)
				synctest.Wait()
				want := ErrBalanceUnavailable
				if status == 200 {
					want = ErrInsufficientBalance
				}
				for range 32 {
					require.ErrorIs(t, <-results, want)
				}
				assert.Equal(t, int32(1), reads.Load())
				wait := 2 * time.Second
				if status == 401 || status == 403 {
					wait = 5 * time.Minute
				} else if status == 429 || status >= 500 {
					wait = time.Minute
				}
				assert.Equal(t, at.Add(wait), r.nextRead)
				assert.Empty(t, r.balanceFlights)
			})
		})
	}
}

func TestConcurrentQuotaChecksShareBudgetWaitAndRecoverAfterInvalidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New(Config{BaseURL: "https://ops.example", Timeout: time.Second})
		var reads atomic.Int32
		r.Client.HTTP.Transport = balanceTestTransport(func(req *http.Request) (*http.Response, error) {
			reads.Add(1)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"userId":"15","balance":"1","balanceQuota":50000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`))}, nil
		})
		for round := range 2 {
			at := time.Now()
			r.nextRead = at.Add(2 * time.Second)
			results := make(chan error, 64)
			for range 64 {
				go func() { results <- r.CheckCall(context.Background(), 15, at) }()
			}
			synctest.Wait()
			assert.Equal(t, int32(round), reads.Load())
			time.Sleep(2 * time.Second)
			synctest.Wait()
			for range 64 {
				require.NoError(t, <-results)
			}
			assert.Equal(t, int32(round+1), reads.Load())
			r.InvalidateCall(15)
		}
	})
}

func TestConcurrentQuotaChecksDoNotFailWhenAnotherCallCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New(Config{BaseURL: "https://ops.example", Timeout: time.Second})
		release := make(chan struct{})
		r.Client.HTTP.Transport = balanceTestTransport(func(req *http.Request) (*http.Response, error) {
			<-release
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"userId":"15","balance":"1","balanceQuota":50000,"negative":false,"lastEntryId":1,"updatedAt":"2026-11-04T01:00:00Z"}}`))}, nil
		})
		results := make(chan error, 64)
		for range 64 {
			go func() {
				position, err := r.AdmissionPosition(context.Background(), 15, time.Now())
				if err == nil && (position.EntryID != 1 || position.Quota != 50000 || position.UserID != 15) {
					err = fmt.Errorf("unexpected admission position: %+v", position)
				}
				r.InvalidateCall(15)
				results <- err
			}()
		}
		synctest.Wait()
		close(release)
		synctest.Wait()
		for range 64 {
			require.NoError(t, <-results, "completed calls must not invalidate other waiters' authoritative result")
		}
	})
}
