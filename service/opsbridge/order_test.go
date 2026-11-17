package opsbridge

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeliveryUsesDurableEnqueueOrderAndRetriesBillingConflict(t *testing.T) {
	paths := []string{}
	statuses := []int{http.StatusConflict, http.StatusOK, http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(statuses[len(paths)-1])
	}))
	defer server.Close()
	b, err := Open(Config{URL: server.URL, GatewayID: "gateway", Key: "key", StateDir: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	require.NoError(t, b.Enqueue("agency-center/billing-events", "z-key", map[string]any{"event": 1}))
	require.NoError(t, b.Enqueue("agency-center/billing-events", "a-key", map[string]any{"event": 2}))
	now := time.Now()
	worked, err := b.ProcessNext(t.Context(), now)
	require.True(t, worked)
	require.Error(t, err)
	first := b.state.Deliveries[hash([]byte("agency-center/billing-events\nz-key"))]
	require.Equal(t, "pending", first.Status)
	require.Greater(t, first.NextAt, now.Unix())
	worked, err = b.ProcessNext(t.Context(), time.Unix(first.NextAt, 0))
	require.True(t, worked)
	require.NoError(t, err)
	worked, err = b.ProcessNext(t.Context(), time.Unix(first.NextAt, 0))
	require.True(t, worked)
	require.NoError(t, err)
	require.Len(t, paths, 3)
	require.Equal(t, []string{"/api/v1/gateway/agency-center/billing-events", "/api/v1/gateway/agency-center/billing-events", "/api/v1/gateway/agency-center/billing-events"}, paths)
}
