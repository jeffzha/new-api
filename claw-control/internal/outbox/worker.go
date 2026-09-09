package outbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const defaultEventChannel = "claw:control:events"

type Event struct {
	EventKey         string          `json:"event_key"`
	CustomerID       *uint64         `json:"customer_id,omitempty"`
	EventType        string          `json:"event_type"`
	Payload          json.RawMessage `json:"payload"`
	CreatedAt        time.Time       `json:"created_at"`
	SignatureVersion string          `json:"signature_version,omitempty"`
	Signature        string          `json:"signature,omitempty"`
}

type Publisher interface {
	Publish(ctx context.Context, event Event) error
}

type RedisPublisher struct {
	client        *redis.Client
	channel       string
	signingSecret string
}

func NewRedisPublisher(redisURL, channel string) (*RedisPublisher, error) {
	options, err := redis.ParseURL(strings.TrimSpace(redisURL))
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	if options.Addr == "" {
		return nil, fmt.Errorf("Redis URL must include an address")
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = defaultEventChannel
	}
	return NewRedisPublisherWithOptions(options, channel)
}

func NewRedisPublisherWithOptions(options *redis.Options, channel string, signingSecret ...string) (*RedisPublisher, error) {
	if options == nil || strings.TrimSpace(options.Addr) == "" {
		return nil, fmt.Errorf("Redis options must include an address")
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = defaultEventChannel
	}
	secret := ""
	if len(signingSecret) > 0 {
		secret = signingSecret[0]
	}
	return &RedisPublisher{client: redis.NewClient(options), channel: channel, signingSecret: secret}, nil
}

func (p *RedisPublisher) Ping(ctx context.Context) error {
	return p.client.Ping(ctx).Err()
}

func (p *RedisPublisher) Close() error { return p.client.Close() }

func (p *RedisPublisher) Publish(ctx context.Context, event Event) error {
	if event.EventType == "APP_MIGRATION_CUTOVER" {
		if len(p.signingSecret) < 32 || event.CustomerID == nil {
			return errors.New("App migration cutover event signing context is unavailable")
		}
		var decoded any
		if err := jsonx.Unmarshal(event.Payload, &decoded); err != nil {
			return fmt.Errorf("decode App migration event payload: %w", err)
		}
		canonicalPayload, err := jsonx.Marshal(decoded)
		if err != nil {
			return fmt.Errorf("canonicalize App migration event payload: %w", err)
		}
		payloadHash := sha256.Sum256(canonicalPayload)
		canonical := strings.Join([]string{
			"1", event.EventKey, fmt.Sprint(*event.CustomerID), event.EventType,
			event.CreatedAt.UTC().Format(time.RFC3339Nano), fmt.Sprintf("%x", payloadHash),
		}, "\n")
		mac := hmac.New(sha256.New, []byte(p.signingSecret))
		_, _ = mac.Write([]byte(canonical))
		event.SignatureVersion = "1"
		event.Signature = fmt.Sprintf("%x", mac.Sum(nil))
	}
	payload, err := jsonx.Marshal(event)
	if err != nil {
		return err
	}
	pipeline := p.client.TxPipeline()
	pipeline.XAdd(ctx, &redis.XAddArgs{
		Stream: p.channel + ":stream",
		MaxLen: 100_000,
		Approx: true,
		Values: map[string]any{"event_key": event.EventKey, "event": string(payload)},
	})
	pipeline.Publish(ctx, p.channel, payload)
	_, err = pipeline.Exec(ctx)
	return err
}

type Worker struct {
	db        *gorm.DB
	publisher Publisher
	now       func() time.Time
}

func New(db *gorm.DB, publisher Publisher) *Worker {
	return &Worker{db: db, publisher: publisher, now: time.Now}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w.db == nil || w.publisher == nil {
		return false, errors.New("outbox worker is not configured")
	}
	processed := false
	var deliveryErr error
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.ControlOutbox
		now := w.now().UTC()
		query := database.ForUpdate(tx).
			Where("status = ? AND next_retry_at <= ?", model.OutboxStatusPending, now).
			Order("id asc").First(&row)
		if query.Error == gorm.ErrRecordNotFound {
			return nil
		}
		if query.Error != nil {
			return query.Error
		}
		processed = true
		var payload json.RawMessage
		if err := jsonx.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			deliveryErr = fmt.Errorf("decode outbox payload: %w", err)
		} else {
			deliveryErr = w.publisher.Publish(ctx, Event{
				EventKey: row.EventKey, CustomerID: row.CustomerID, EventType: row.EventType,
				Payload: payload, CreatedAt: row.CreatedAt,
			})
		}
		row.Attempts++
		if deliveryErr == nil {
			row.Status = model.OutboxStatusDelivered
			row.DeliveredAt = &now
			row.LastError = ""
		} else {
			row.NextRetryAt = now.Add(retryDelay(row.Attempts))
			row.LastError = sanitizedError(deliveryErr)
		}
		return tx.Save(&row).Error
	})
	if err != nil {
		return processed, err
	}
	return processed, deliveryErr
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	delay := 5 * time.Second * time.Duration(1<<(attempt-1))
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

func sanitizedError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
