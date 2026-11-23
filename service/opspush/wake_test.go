package opspush

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func TestCommittedWakeReachesLeaseOwnerWithoutFinancialPayload(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousClient, previousEnabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); client.Close(); common.RDB, common.RedisEnabled = previousClient, previousEnabled })
	for len(opsnotify.Wake()) > 0 {
		<-opsnotify.Wake()
	}
	p := &Pusher{Config: Config{URL: "https://ops.example.test", KeyID: "overseas", Scope: "usd-scope"}}
	other := &Pusher{Config: p.Config}
	first, owner := p.committedWake(ctx), other.committedWake(ctx)
	channel := fmt.Sprintf("ops-usage-wake:%x", sha256.Sum256([]byte(p.Config.URL+"\n"+p.Config.KeyID+"\n"+p.Config.Scope)))
	require.Eventually(t, func() bool { n, _ := client.PubSubNumSub(ctx, channel).Result(); return n[channel] == 2 }, time.Second, time.Millisecond)
	observe := client.Subscribe(ctx, channel)
	defer observe.Close()
	_, err := observe.Receive(ctx)
	require.NoError(t, err)
	opsnotify.Committed(p.Config.Scope)
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("local wake missing")
	}
	select {
	case <-owner:
	case <-time.After(time.Second):
		t.Fatal("lease owner wake missing")
	}
	message, err := observe.ReceiveMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, "1", message.Payload, "a wake contains no identity, usage, money or credential")
}
