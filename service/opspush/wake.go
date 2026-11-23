package opspush

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
)

// Wake the existing leased financial outbox worker, including when the lease
// belongs to another blue/green process. No log is reconstructed into a debit.
func (p *Pusher) committedWake(ctx context.Context) <-chan struct{} {
	wake := make(chan struct{}, 1)
	if p.Config.Scope == "" {
		return wake
	}
	channel := fmt.Sprintf("ops-usage-wake:%x", sha256.Sum256([]byte(p.Config.URL+"\n"+p.Config.KeyID+"\n"+p.Config.Scope)))
	client := common.RDB
	shared := common.RedisEnabled && client != nil
	if shared {
		go func() {
			sub := client.Subscribe(ctx, channel)
			defer sub.Close()
			messages := sub.Channel()
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-messages:
					if !ok {
						return
					}
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
		}()
	}
	go func() {
		var next time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case scope := <-opsnotify.Wake():
				if scope != p.Config.Scope {
					continue
				}
			}
			// Coalesce a burst into one hint without delaying API completion.
			if delay := time.Until(next); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			next = time.Now().Add(800 * time.Millisecond)
			select {
			case wake <- struct{}{}:
			default:
			}
			if shared {
				pub, stop := context.WithTimeout(ctx, 100*time.Millisecond)
				_ = client.Publish(pub, channel, "1").Err()
				stop()
			}
		}
	}()
	return wake
}
