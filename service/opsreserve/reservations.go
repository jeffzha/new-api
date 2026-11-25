// Package opsreserve manages OPS holds; it never debits a local user wallet.
package opsreserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/QuantumNous/new-api/service/opsfees"
	"github.com/QuantumNous/new-api/service/opspush"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const Path = "/api/v1/catalog/v1/wallet-reservations"

var ErrInsufficient = errors.New("OPS available balance insufficient")
var ErrUnavailable = errors.New("OPS wallet reservation unavailable")

// ValidateAuthority is installed only by an accounting adapter that already
// owns the OPS wallet and its task lifecycle. Installing shared reservation
// code alone must never switch a product wallet away from local accounting.
var ValidateAuthority func() error

var RecoverMidjourney func(context.Context, *model.Midjourney) error
var RecoverUsage func(context.Context, *Intent) error

// Usage hints are bounded and expendable. The committed main-database journal
// remains the source of truth when the queue is full or the process restarts.
var usageHints = make(chan string, 256)

func NotifyUsage(request string) {
	select {
	case usageHints <- request:
	default:
	}
}

// A release intent survives network errors and process restarts. Accepted
// upstream work is never released on a timer: OPS closes its hold with debit.
type Intent = model.OPSHoldIntent

type Client struct {
	Transport *opsclient.Client
	DB        *gorm.DB
}

var configured struct {
	sync.Mutex
	client *Client
	key    string
}

func Enabled() bool {
	return os.Getenv("OPS_WALLET_RESERVATIONS") == "true" && os.Getenv("OPS_WALLET_AUTHORITY") == "ops"
}

func Configured() (*Client, error) {
	configured.Lock()
	defer configured.Unlock()
	base, key, secret := os.Getenv("OPS_BILLING_URL"), os.Getenv("OPS_BILLING_KEY_ID"), os.Getenv("OPS_BILLING_SECRET")
	identity := base + "\x00" + key + "\x00" + secret
	if configured.client != nil && configured.key == identity && configured.client.DB == model.DB {
		return configured.client, nil
	}
	if opsclient.ValidateBase(base) != nil || key == "" || len(secret) < 16 {
		return nil, ErrUnavailable
	}
	transport := opsclient.New(base, key, secret, 3*time.Second)
	transport.HTTP.Transport = &http.Transport{Proxy: http.ProxyFromEnvironment, MaxConnsPerHost: 32, MaxIdleConnsPerHost: 32, IdleConnTimeout: time.Minute, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 3 * time.Second}
	client := &Client{Transport: transport, DB: model.DB}
	configured.client, configured.key = client, identity
	return client, nil
}

func Start(ctx context.Context) error {
	if os.Getenv("OPS_WALLET_RESERVATIONS") == "true" && !Enabled() {
		return ErrUnavailable
	}
	if !Enabled() {
		if model.DB == nil || !model.DB.Migrator().HasTable(&Intent{}) {
			return nil
		}
		var outstanding []Intent
		if err := model.DB.Select("gateway").Where("gateway = ? AND reconciled = ?", os.Getenv("OPS_BILLING_KEY_ID"), false).Limit(1).Find(&outstanding).Error; err != nil {
			return err
		}
		if len(outstanding) == 0 {
			return nil
		}
	}
	if ValidateAuthority == nil {
		return ErrUnavailable
	}
	if err := ValidateAuthority(); err != nil {
		return err
	}
	if os.Getenv("OPS_BRIDGE_ENABLED") == "true" || os.Getenv("OPS_ENFORCE_CALLS") == "true" || os.Getenv("OPS_CN_DURABLE_TASK_ENABLED") == "true" || os.Getenv("OPS_CN_DURABLE_MIDJOURNEY_ENABLED") == "true" {
		return ErrUnavailable
	}
	if os.Getenv("OPS_BILLING_ENABLED") != "true" || os.Getenv("OPS_BILLING_RECEIPTS") != "charges" || os.Getenv("OPS_BILLING_URL") != os.Getenv("OPS_PUSH_URL") || os.Getenv("OPS_BILLING_KEY_ID") != os.Getenv("OPS_PUSH_KEY_ID") || os.Getenv("OPS_BILLING_SECRET") != os.Getenv("OPS_PUSH_SECRET") {
		return ErrUnavailable
	}
	client, err := Configured()
	if err != nil {
		return err
	}
	if Enabled() {
		if err = model.PrepareOPSHoldSchema(client.DB); err != nil {
			return err
		}
	}
	go client.runUsage(ctx)
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := client.Recover(ctx); err != nil {
					common.SysError("OPS hold release retry failed")
				}
			}
		}
	}()
	return nil
}

// runUsage has one worker per process, rather than a goroutine per request.
// HTTP delivery stays off the model response path; the slow recovery scanner
// also retries the same immutable event after a lost hint or acknowledgement.
func (c *Client) runUsage(ctx context.Context) {
	var next time.Time
	for {
		var request string
		select {
		case <-ctx.Done():
			return
		case request = <-usageHints:
		}
		requests := []string{request}
		timer := time.NewTimer(max(25*time.Millisecond, time.Until(next)))
	collect:
		for len(requests) < 250 {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case request := <-usageHints:
				requests = append(requests, request)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()
		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		interval := 1500 * time.Millisecond
		if common.RedisEnabled && common.RDB != nil {
			interval = 800 * time.Millisecond
		}
		next = time.Now().Add(interval)
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = c.SendJournalBatch(work, requests)
		cancel()
	}
}

func (c *Client) SendJournalUsage(ctx context.Context, request string) error {
	return c.SendJournalBatch(ctx, []string{request})
}

// SendJournalBatch sends bounded immutable events with one fleet send permit.
// A partial or lost acknowledgement leaves every journal eligible for replay.
func (c *Client) SendJournalBatch(ctx context.Context, requests []string) error {
	if len(requests) == 0 || len(requests) > 250 {
		return ErrUnavailable
	}
	var rows []Intent
	if err := c.DB.WithContext(ctx).Where("gateway = ? AND request IN ? AND finalized = ? AND cancel_pending = ? AND usage_delivered = ?", c.Transport.KeyID, requests, true, false, false).Order("request").Find(&rows).Error; err != nil {
		return err
	}
	events := []json.RawMessage{}
	delivered := []string{}
	size := 0
	for _, row := range rows {
		if row.UsageLog == "" {
			continue
		}
		if row.UsagePayload == "" {
			if RecoverUsage == nil {
				return ErrUnavailable
			}
			if err := RecoverUsage(ctx, &row); err != nil {
				return err
			}
		}
		var payload struct {
			Schema string            `json:"schemaVersion"`
			Events []json.RawMessage `json:"events"`
		}
		if common.UnmarshalJsonStr(row.UsagePayload, &payload) != nil || payload.Schema != "1.1" || len(payload.Events) != 1 {
			return ErrUnavailable
		}
		if size+len(payload.Events[0]) > 900*1024 {
			break // Remaining committed rows are recovered by the scanner.
		}
		events = append(events, payload.Events[0])
		delivered = append(delivered, row.Request)
		size += len(payload.Events[0])
	}
	if len(events) == 0 {
		return nil
	}
	rawEvents, err := common.Marshal(events)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(rawEvents)
	body, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": "reserved-" + hex.EncodeToString(sum[:16]), "events": events})
	if err != nil {
		return err
	}
	if err = opspush.WaitUsageBudget(ctx, c.Transport.BaseURL, c.Transport.KeyID); err != nil {
		return err
	}
	raw, err := c.Transport.Post(ctx, "/api/v1/ingest/v1/usage-events", body, time.Now())
	if err != nil {
		return err
	}
	var receipt struct {
		Data struct {
			Accepted   int   `json:"accepted"`
			Duplicates int   `json:"duplicates"`
			Rejected   []any `json:"rejected"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &receipt) != nil || receipt.Data.Accepted < 0 || receipt.Data.Duplicates < 0 || receipt.Data.Accepted+receipt.Data.Duplicates != len(events) || len(receipt.Data.Rejected) != 0 {
		return ErrUnavailable
	}
	opsfees.AcceptPushReceipts(c.Transport.BaseURL, c.Transport.KeyID, c.Transport.Secret, body, raw, time.Now())
	return c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request IN ?", c.Transport.KeyID, delivered).Update("usage_delivered", true).Error
}

func (c *Client) Reserve(ctx context.Context, request string, user int, modelName string, quota int, quote ...map[string]any) error {
	if request == "" || user <= 0 || modelName == "" || quota < 0 || quota > common.MaxQuota {
		return ErrUnavailable
	}
	intent := Intent{Gateway: c.Transport.KeyID, Request: request, User: strconv.Itoa(user), Model: modelName, Updated: time.Now().Unix(), LeaseUntil: time.Now().Add(5 * time.Minute).Unix()}
	created := c.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&intent)
	if created.Error != nil {
		return created.Error
	}
	var old Intent
	if err := c.DB.WithContext(ctx).Where("gateway = ? AND request = ?", intent.Gateway, request).First(&old).Error; err != nil {
		return err
	}
	if old.User != intent.User || old.Model != modelName || old.CancelPending || old.Finalized {
		return ErrUnavailable
	}
	refreshed := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ? AND cancel_pending = ? AND finalized = ?", intent.Gateway, request, false, false).Updates(map[string]any{"lease_until": intent.LeaseUntil, "updated": intent.Updated})
	if refreshed.Error != nil {
		return refreshed.Error
	}
	if refreshed.RowsAffected != 1 {
		var count int64
		if err := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ? AND cancel_pending = ? AND finalized = ? AND lease_until = ?", intent.Gateway, request, false, false, intent.LeaseUntil).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrUnavailable
		}
	}
	err := c.send(ctx, intent, "reserve", quota, quote...)
	if err != nil && created.RowsAffected > 0 {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		// Even a timeout may have committed remotely. Cancellation retains the same
		// ID and persists before network I/O; a late reserve cannot reopen it.
		_ = c.Release(cleanup, request)
	}
	return err
}

// TokenBudget is atomic with its replay marker, including refund/restart paths.
func (c *Client) TokenBudget(ctx context.Context, request string, user, token, target int, finalize bool) error {
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Intent
		if err := model.LockOPSHoldTx(tx).Where("gateway = ? AND request = ?", c.Transport.KeyID, request).First(&row).Error; err != nil {
			return err
		}
		if row.User != strconv.Itoa(user) || row.CancelPending || row.TokenID != 0 && row.TokenID != token || row.Finalized && (!finalize || row.TokenQuota != target) || !finalize && row.LeaseUntil <= time.Now().Unix() {
			return ErrUnavailable
		}
		if !finalize {
			target = max(target, row.TokenQuota)
		}
		if token > 0 {
			if err := model.AdjustOPSHoldTokenTx(tx, user, token, row.TokenQuota, target, !finalize); err != nil {
				return err
			}
		} else if target != 0 {
			return ErrUnavailable
		}
		return tx.Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Updates(map[string]any{"token_id": token, "token_quota": target, "finalized": finalize, "updated": time.Now().Unix()}).Error
	})
}

func (c *Client) AttachTask(ctx context.Context, request, task string) error {
	if task == "" {
		return ErrUnavailable
	}
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Intent
		if err := model.LockOPSHoldTx(tx).Where("gateway = ? AND request = ?", c.Transport.KeyID, request).First(&row).Error; err != nil {
			return err
		}
		if row.CancelPending || row.Finalized || (row.TaskID != "" && row.TaskID != task) {
			return ErrUnavailable
		}
		return tx.Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Updates(map[string]any{"task_id": task, "updated": time.Now().Unix()}).Error
	})
}

// MarkDispatch commits before provider I/O. Recovery never releases such a
// request based on time, since the provider may have accepted it.
func (c *Client) MarkDispatch(ctx context.Context, request string) error {
	result := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ? AND cancel_pending = ? AND finalized = ? AND lease_until > ?", c.Transport.KeyID, request, false, false, time.Now().Unix()).Updates(map[string]any{"dispatched": true, "updated": time.Now().Unix()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var count int64
		if err := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ? AND cancel_pending = ? AND finalized = ? AND dispatched = ? AND lease_until > ?", c.Transport.KeyID, request, false, false, true, time.Now().Unix()).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrUnavailable
		}
	}
	return nil
}

func (c *Client) send(ctx context.Context, intent Intent, operation string, quota int, quote ...map[string]any) error {
	payload := map[string]any{"requestId": intent.Request, "userId": intent.User, "model": intent.Model, "operation": operation, "quota": quota}
	if len(quote) > 0 && quote[0] != nil {
		payload["quote"] = quote[0]
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return err
	}
	raw, err := c.Transport.Post(ctx, Path, body, time.Now())
	if err != nil {
		var remote *opsclient.ReadError
		if errors.As(err, &remote) && remote.Code == "insufficient_balance" {
			return ErrInsufficient
		}
		return ErrUnavailable
	}
	var receipt struct {
		Data struct {
			Request string `json:"requestId"`
			State   string `json:"state"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &receipt) != nil || receipt.Data.Request != intent.Request || (operation == "reserve" && receipt.Data.State != "reserved") || (operation == "release" && receipt.Data.State != "released") {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) Release(ctx context.Context, request string) error {
	var intent Intent
	err := c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := model.LockOPSHoldTx(tx).Where("gateway = ? AND request = ?", c.Transport.KeyID, request).First(&intent).Error; err != nil {
			return err
		}
		if intent.Finalized {
			return ErrUnavailable
		}
		intent.CancelPending = true
		return tx.Model(&Intent{}).Where("gateway = ? AND request = ?", intent.Gateway, intent.Request).Updates(map[string]any{"cancel_pending": true, "updated": time.Now().Unix()}).Error
	})
	if err != nil {
		return err
	}
	return c.release(ctx, intent)
}
func (c *Client) release(ctx context.Context, intent Intent) error {
	if err := c.send(ctx, intent, "release", 0); err != nil {
		return err
	}
	// A transport success is not proof that OPS canceled accepted work. send
	// verifies a released receipt before the independent key budget is refunded.
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Intent
		if err := model.LockOPSHoldTx(tx).Where("gateway = ? AND request = ?", intent.Gateway, intent.Request).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if current.Reconciled && current.Finalized && current.Resolution == "confirmed_no_charge" && current.TokenQuota == 0 {
			return nil
		}
		if !current.CancelPending || current.Finalized {
			return ErrUnavailable
		}
		if current.TokenID > 0 && current.TokenQuota > 0 {
			user, err := strconv.Atoi(current.User)
			if err != nil {
				return err
			}
			if err := model.AdjustOPSHoldTokenTx(tx, user, current.TokenID, current.TokenQuota, 0, false); err != nil {
				return err
			}
		}
		if current.ReviewEvidence != "" {
			return tx.Model(&current).Updates(map[string]any{"token_quota": 0, "finalized": true, "cancel_pending": false, "reconciled": true, "resolved_at": time.Now().Unix()}).Error
		}
		return tx.Delete(&current).Error
	})
}
func (c *Client) Recover(ctx context.Context) error {
	// An expired pre-dispatch lease is safe to cancel only while holding the
	// same row lock checked by dispatch. An already dispatched request never
	// takes this path, however long the provider takes to finish.
	var abandoned []Intent
	if err := c.DB.WithContext(ctx).Where("gateway = ? AND cancel_pending = ? AND reconciled = ? AND dispatched = ? AND finalized = ? AND lease_until <= ?", c.Transport.KeyID, false, false, false, false, time.Now().Unix()).Order("updated").Limit(10).Find(&abandoned).Error; err != nil {
		return err
	}
	for _, row := range abandoned {
		if err := c.cancelAbandoned(ctx, row.Request); err != nil {
			return err
		}
	}
	var rows []Intent
	if err := c.DB.WithContext(ctx).Where("gateway = ? AND cancel_pending = ? AND reconciled = ? AND updated <= ?", c.Transport.KeyID, true, false, time.Now().Add(-3*time.Second).Unix()).Order("updated").Limit(50).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := c.release(ctx, row); err != nil {
			// A failed cancellation must not starve all later recovery work.
			if updateErr := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Update("updated", time.Now().Unix()).Error; updateErr != nil {
				return updateErr
			}
			common.SysError("OPS hold cancellation awaits reconciliation: " + row.Request)
		}
	}
	// Reconcile durable holds after lost acknowledgements or process restarts.
	// No timer is evidence of cancellation. Unknown dispatched work stays held
	// for reconciliation; only OPS's posted/released receipt removes the intent.
	rows = nil
	if err := c.DB.WithContext(ctx).Where("gateway = ? AND cancel_pending = ? AND reconciled = ? AND updated < ?", c.Transport.KeyID, false, false, time.Now().Add(-time.Minute).Unix()).Order("updated").Limit(10).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if row.UsageLog != "" && row.UsagePayload == "" && RecoverUsage != nil {
			if err := RecoverUsage(ctx, &row); err != nil {
				_ = c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Update("updated", time.Now().Unix()).Error
				continue
			}
		}
		midjourneyTerminal := row.MidjourneyID == 0
		if row.MidjourneyID > 0 && RecoverMidjourney != nil {
			var task model.Midjourney
			if err := c.DB.WithContext(ctx).Where("id = ? AND user_id = ?", row.MidjourneyID, row.User).First(&task).Error; err != nil {
				return err
			}
			if task.OPSReservationGateway != row.Gateway || task.OPSReservationRequest != row.Request {
				return ErrUnavailable
			}
			if err := RecoverMidjourney(ctx, &task); err != nil {
				_ = c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Update("updated", time.Now().Unix()).Error
				continue
			}
			if err := c.DB.WithContext(ctx).Where("gateway = ? AND request = ?", row.Gateway, row.Request).First(&row).Error; err != nil {
				return err
			}
			// A submitted image can fail after its initial usage was settled.
			// Keep checking its durable terminal state until a required refund
			// has been delivered, including a crash after failure persistence.
			midjourneyTerminal = task.Progress == "100%" && task.Status == "SUCCESS" || row.RefundPayload != "" && row.RefundDelivered
		}
		if row.UsagePayload != "" {
			if err := c.DeliverUsage(ctx, row.Request); err != nil {
				_ = c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Update("updated", time.Now().Unix()).Error
				continue
			}
			if err := c.DB.WithContext(ctx).Where("gateway = ? AND request = ?", row.Gateway, row.Request).First(&row).Error; err != nil {
				return err
			}
		}
		if row.TaskID != "" && !row.Finalized {
			var task model.Task
			err := c.DB.WithContext(ctx).Where("task_id = ? AND user_id = ?", row.TaskID, row.User).First(&task).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && task.Status == model.TaskStatusFailure {
				if _, err = model.FinalizeOPSHoldTask(ctx, c.DB, &task, 0); err != nil {
					return err
				}
				row.Finalized = true
			}
			// A successful task's persisted quota can still be its submission
			// estimate after a crash. Only its normal settlement or an evidenced
			// operator reconciliation may finalize the independent key budget.
		}
		body, err := common.Marshal(map[string]any{"requestId": row.Request, "userId": row.User, "model": row.Model, "operation": "status"})
		if err != nil {
			return err
		}
		raw, err := c.Transport.Post(ctx, Path, body, time.Now())
		if err != nil {
			return err
		}
		var receipt struct {
			Data struct {
				Request string `json:"requestId"`
				State   string `json:"state"`
			} `json:"data"`
		}
		if common.Unmarshal(raw, &receipt) != nil || receipt.Data.Request != row.Request {
			return ErrUnavailable
		}
		if (receipt.Data.State == "settled" && row.Finalized) || (receipt.Data.State == "released" && row.TokenQuota == 0) {
			if !midjourneyTerminal {
				if err := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Update("updated", time.Now().Unix()).Error; err != nil {
					return err
				}
				continue
			}
			if row.TaskID != "" || row.MidjourneyID > 0 {
				// Retain a compact task replay marker after the money hold closes.
				if err := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ? AND finalized = ? AND cancel_pending = ?", row.Gateway, row.Request, true, false).Update("reconciled", true).Error; err != nil {
					return err
				}
				continue
			}
			if err := c.DB.WithContext(ctx).Where("gateway = ? AND request = ? AND cancel_pending = ?", row.Gateway, row.Request, false).Delete(&Intent{}).Error; err != nil {
				return err
			}
		} else {
			if err := c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Update("updated", time.Now().Unix()).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// DeliverUsage replays exactly the committed event bytes. Lost acknowledgments
// cannot create another debit/refund because request and batch IDs are stable.
func (c *Client) DeliverUsage(ctx context.Context, request string) error {
	var row Intent
	if err := c.DB.WithContext(ctx).Where("gateway = ? AND request = ?", c.Transport.KeyID, request).First(&row).Error; err != nil {
		return err
	}
	for _, item := range []struct {
		body, column string
		delivered    bool
	}{{row.UsagePayload, "usage_delivered", row.UsageDelivered}, {row.RefundPayload, "refund_delivered", row.RefundDelivered}} {
		if item.body == "" || item.delivered {
			continue
		}
		raw, err := c.Transport.Post(ctx, "/api/v1/ingest/v1/usage-events", []byte(item.body), time.Now())
		if err != nil {
			return err
		}
		var receipt struct {
			Data struct {
				Accepted   int   `json:"accepted"`
				Duplicates int   `json:"duplicates"`
				Rejected   []any `json:"rejected"`
			} `json:"data"`
		}
		if common.Unmarshal(raw, &receipt) != nil || receipt.Data.Accepted+receipt.Data.Duplicates != 1 || len(receipt.Data.Rejected) != 0 {
			return ErrUnavailable
		}
		if err = c.DB.WithContext(ctx).Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Update(item.column, true).Error; err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) cancelAbandoned(ctx context.Context, request string) error {
	err := c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Intent
		if err := model.LockOPSHoldTx(tx).Where("gateway = ? AND request = ?", c.Transport.KeyID, request).First(&row).Error; err != nil {
			return err
		}
		if row.CancelPending || row.Dispatched || row.Finalized || row.LeaseUntil > time.Now().Unix() {
			return nil
		}
		return tx.Model(&Intent{}).Where("gateway = ? AND request = ?", row.Gateway, row.Request).Updates(map[string]any{"cancel_pending": true, "updated": time.Now().Add(-3 * time.Second).Unix()}).Error
	})
	return err
}
