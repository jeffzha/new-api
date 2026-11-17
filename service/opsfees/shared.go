package opsfees

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type sharedFees struct {
	Sender     string `json:"sender"`
	ReceivedAt int64  `json:"receivedAt,omitempty"`
	Items      []Fee  `json:"items"`
	Feed       bool   `json:"feed"`
	Version    int64  `json:"version"`
	Sent       []byte `json:"sent,omitempty"`
	Response   []byte `json:"response,omitempty"`
}

// Redis distributes validated results between blue/green processes. Bounded
// asynchronous publication adds no network I/O to an HTTP fee cache hit.
func shareResults(ctx context.Context, c Config) func([]model.OpsFee, bool) {
	if !common.RedisEnabled || common.RDB == nil {
		return nil
	}
	client := common.RDB
	sender := common.GetUUID()
	queue := make(chan sharedFees, 64)
	channel := "ops-fee-results:" + c.Connection
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case message := <-queue:
				message.Sender = sender
				raw, err := common.Marshal(message)
				if err == nil {
					publish, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
					_ = client.Publish(publish, channel, raw).Err()
					cancel()
				}
			}
		}
	}()
	go func() {
		sub := client.Subscribe(ctx, channel)
		defer sub.Close()
		messages := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-messages:
				if !ok {
					return
				}
				if len(message.Payload) > 4*1024*1024 {
					continue
				}
				var value sharedFees
				if common.UnmarshalJsonStr(message.Payload, &value) != nil || len(value.Items) > 500 || value.Sender == sender {
					continue
				}
				if len(value.Sent) > 0 {
					if observer := receiptObserver.Load(); observer != nil {
						if value.ReceivedAt > 0 {
							(*observer)(value.Sent, value.Response, time.Unix(0, value.ReceivedAt))
						}
					}
					continue
				}
				raw, err := common.Marshal(map[string]any{"data": map[string]any{"items": value.Items, "hasMore": false, "nextCursor": "shared"}})
				if err != nil {
					continue
				}
				rows, _, _, err := decodePage(raw, c.Connection, time.Now().Unix())
				if err == nil {
					for i := range rows {
						rows[i].Version = value.Version
					}
					model.AcceptSharedOpsFeeResults(c.Connection, rows, value.Feed)
				}
			}
		}
	}()
	sharePush.Store(&pushPublication{Send: func(sent, response []byte, at time.Time) {
		// Only allowlisted invocation identities and charge receipt fields
		// belong on this ephemeral channel; no original usage metadata.
		select {
		case queue <- sharedFees{Sent: sent, Response: response, ReceivedAt: at.UnixNano()}:
		default:
		}
	}})
	return func(rows []model.OpsFee, feed bool) {
		if len(rows) == 0 {
			return
		}
		items := make([]Fee, 0, len(rows))
		for _, r := range rows {
			items = append(items, Fee{RequestID: r.RequestID, UserID: r.UserID, Model: r.Model, Amount: r.Amount, StationAmount: r.StationAmount, RefundAmount: r.RefundAmount, Difference: r.Difference, ChargedAt: r.ChargedAt})
		}
		select {
		case queue <- sharedFees{Items: items, Feed: feed, Version: rows[0].Version}:
		default:
		}
	}
}
