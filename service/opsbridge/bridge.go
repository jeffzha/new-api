// Package opsbridge owns the optional TokenOps data-plane connection.
package opsbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type Config struct {
	URL, GatewayID, Key, StateDir, CommandSigningKey string
	EnforceCalls                                     bool
	CommandExecutor                                  CommandExecutor
	BillingSource                                    BillingEventSource
	ManifestSource                                   SourceManifestSource
}

type Delivery struct {
	ID        string         `json:"id"`
	Path      string         `json:"path"`
	Key       string         `json:"key"`
	Body      map[string]any `json:"body"`
	Status    string         `json:"status"`
	Attempts  int            `json:"attempts"`
	NextAt    int64          `json:"next_at"`
	LastError string         `json:"last_error,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Sequence  int64          `json:"sequence,omitempty"`
}

type State struct {
	Schema               int                        `json:"schema"`
	Scope                string                     `json:"scope"`
	Deliveries           map[string]*Delivery       `json:"deliveries"`
	Notifications        map[string]any             `json:"notifications"`
	LatestNotifications  map[string]any             `json:"latest_notifications,omitempty"`
	Requests             map[string]string          `json:"requests"`
	Imports              map[string]*ImportPlan     `json:"imports"`
	Baselines            map[string]Baseline        `json:"baselines"`
	Tasks                map[string]PendingTask     `json:"tasks,omitempty"`
	CommandReceipts      map[string]*CommandReceipt `json:"command_receipts,omitempty"`
	BillingCursor        int64                      `json:"billing_cursor,omitempty"`
	ManifestCursor       int64                      `json:"manifest_cursor,omitempty"`
	NextDeliverySequence int64                      `json:"next_delivery_sequence,omitempty"`
}

type Bridge struct {
	cfg             Config
	client          *http.Client
	mu              sync.Mutex
	state           State
	lock            *os.File
	importMu        sync.Mutex
	deliveryMu      sync.Mutex
	notificationMu  sync.Mutex
	commandMu       sync.Mutex
	commandExecutor CommandExecutor
	billingSource   BillingEventSource
	manifestSource  SourceManifestSource
	fault           string
	stop            context.CancelFunc
	done            chan struct{}
}

var Default *Bridge

// Scope binds persisted task evidence to this non-secret gateway identity.
func (b *Bridge) Scope() string { return b.state.Scope }

// StartConfigured fails startup on an invalid enabled configuration. Credentials
// never enter persistent state or application responses.
func StartConfigured(ctx context.Context, commandExecutor CommandExecutor, billingSource BillingEventSource, manifestSources ...SourceManifestSource) error {
	if os.Getenv("OPS_BRIDGE_ENABLED") != "true" {
		return nil
	}
	if os.Getenv("OPS_AGENCY_COMMANDS_ENABLED") != "true" {
		commandExecutor = nil
	} else if commandExecutor == nil {
		return errors.New("Ops agency command executor is required")
	}
	var manifestSource SourceManifestSource
	if len(manifestSources) > 0 {
		manifestSource = manifestSources[0]
	}
	b, err := Open(Config{URL: os.Getenv("OPS_BRIDGE_URL"), GatewayID: os.Getenv("OPS_GATEWAY_ID"),
		Key: os.Getenv("OPS_GATEWAY_KEY"), StateDir: os.Getenv("OPS_BRIDGE_STATE_DIR"),
		CommandSigningKey: os.Getenv("OPS_AGENCY_COMMAND_SIGNING_KEY"),
		CommandExecutor:   commandExecutor,
		BillingSource:     billingSource,
		ManifestSource:    manifestSource,
		EnforceCalls:      os.Getenv("OPS_ENFORCE_CALLS") == "true"})
	if err != nil {
		return err
	}
	Default = b
	workerCtx, cancel := context.WithCancel(ctx)
	b.stop = cancel
	b.done = make(chan struct{})
	if err := b.RecoverInterruptedTasks(); err != nil {
		cancel()
		_ = b.lock.Close()
		Default = nil
		return err
	}
	go func() { defer close(b.done); b.Run(workerCtx) }()
	return nil
}

func Open(cfg Config) (*Bridge, error) {
	if cfg.CommandExecutor != nil && len(cfg.CommandSigningKey) < 32 {
		return nil, errors.New("Ops agency command signing key must contain at least 32 characters")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid Ops URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return nil, errors.New("Ops requires HTTPS (loopback HTTP is allowed for tests)")
	}
	if cfg.Key == "" || cfg.GatewayID == "" || cfg.StateDir == "" || strings.ContainsAny(cfg.Key+cfg.GatewayID, "\r\n") {
		return nil, errors.New("Ops identity and private state directory are required")
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, errors.New("cannot create Ops state directory")
	}
	lock, err := acquireLock(filepath.Join(cfg.StateDir, "bridge.lock"))
	if err != nil {
		return nil, errors.New("Ops state directory is already in use or unavailable")
	}
	b := &Bridge{cfg: cfg, lock: lock, commandExecutor: cfg.CommandExecutor, billingSource: cfg.BillingSource, manifestSource: cfg.ManifestSource, client: &http.Client{Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	b.state = State{Schema: 1, Scope: hash([]byte(cfg.URL + "\n" + cfg.GatewayID)), Deliveries: map[string]*Delivery{},
		Notifications: map[string]any{}, Requests: map[string]string{}, Imports: map[string]*ImportPlan{}, Baselines: map[string]Baseline{}}
	data, err := os.ReadFile(filepath.Join(cfg.StateDir, "state.json"))
	if err == nil {
		var saved State
		if len(data) > 64<<20 || common.Unmarshal(data, &saved) != nil || saved.Schema != 1 || saved.Scope != b.state.Scope ||
			saved.Deliveries == nil || saved.Notifications == nil || saved.Requests == nil || saved.Imports == nil || saved.Baselines == nil {
			b.Close()
			return nil, errors.New("invalid Ops state or gateway scope mismatch")
		}
		b.state = saved
	} else if !os.IsNotExist(err) {
		b.Close()
		return nil, errors.New("cannot read Ops state")
	}
	if b.state.LatestNotifications == nil {
		b.state.LatestNotifications = map[string]any{}
	}
	if b.state.Tasks == nil {
		b.state.Tasks = map[string]PendingTask{}
	}
	if b.state.CommandReceipts == nil {
		b.state.CommandReceipts = map[string]*CommandReceipt{}
	}
	// Older state files did not persist enqueue order. Assign a deterministic
	// one once, then preserve it for all future restarts.
	legacy := make([]string, 0)
	for id, delivery := range b.state.Deliveries {
		if delivery.Sequence == 0 {
			legacy = append(legacy, id)
		}
		if delivery.Sequence > b.state.NextDeliverySequence {
			b.state.NextDeliverySequence = delivery.Sequence
		}
	}
	sort.Strings(legacy)
	for _, id := range legacy {
		b.state.NextDeliverySequence++
		b.state.Deliveries[id].Sequence = b.state.NextDeliverySequence
	}
	if len(legacy) > 0 {
		if err := b.saveLocked(); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.recoverCommandReceiptDeliveries(); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *Bridge) Close() error {
	if b.stop != nil {
		b.stop()
		<-b.done
	}
	return b.lock.Close()
}

func hash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (b *Bridge) saveLocked() (saveErr error) {
	if b.fault != "" {
		return errors.New(b.fault)
	}
	defer func() {
		if saveErr != nil {
			b.fault = "Ops persistence failed; restart after repairing storage"
		}
	}()
	data, err := common.Marshal(b.state)
	if err != nil || len(data) > 64<<20 {
		return errors.New("Ops state capacity exceeded; archive delivered evidence before continuing")
	}
	return writeEvidence(filepath.Join(b.cfg.StateDir, "state.json"), data)
}

func writeEvidence(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return errors.New("cannot persist Ops state")
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return errors.New("cannot persist Ops state")
	}
	return syncDirectory(dir)
}

// request deliberately returns no upstream body or URL on error: either can
// contain credentials or customer data from a misconfigured remote service.
func (b *Bridge) request(ctx context.Context, method, path, key string, body any, result any) error {
	b.mu.Lock()
	fault := b.fault
	b.mu.Unlock()
	if fault != "" {
		return errors.New(fault)
	}
	var reader io.Reader
	if body != nil {
		encoded, err := common.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.cfg.URL+"/api/v1/gateway/"+path, reader)
	if err != nil {
		return errors.New("invalid Ops request")
	}
	req.Header.Set("X-Gateway-Id", b.cfg.GatewayID)
	req.Header.Set("X-Gateway-Key", b.cfg.Key)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return errors.New("Ops transport unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return errors.New("invalid Ops response size")
	}
	if result != nil {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if common.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" || common.Unmarshal(envelope.Data, result) != nil {
			return errors.New("invalid Ops response")
		}
	}
	return nil
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Ops HTTP %d", e.Status) }

func (b *Bridge) Enqueue(path, key string, body map[string]any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enqueueLocked(path, key, body, "", "")
}

// The delivery and its request state are one durable transition. A relay must
// never cancel a reservation after its terminal usage was already queued.
func (b *Bridge) enqueueLocked(path, key string, body map[string]any, requestID, status string) error {
	encoded, err := common.Marshal(body)
	if err != nil {
		return err
	}
	// Freeze caller-owned maps before returning, including nested usage details.
	var frozen map[string]any
	if err := common.Unmarshal(encoded, &frozen); err != nil {
		return err
	}
	id := hash([]byte(path + "\n" + key))
	if prior := b.state.Deliveries[id]; prior != nil {
		priorBody, _ := common.Marshal(prior.Body)
		if !bytes.Equal(priorBody, encoded) {
			return errors.New("Ops delivery identity conflict")
		}
		if requestID != "" && b.state.Requests[requestID] != status {
			b.state.Requests[requestID] = status
			return b.saveLocked()
		}
		return nil
	}
	archived, err := os.ReadFile(filepath.Join(b.cfg.StateDir, "archive", "delivery-"+id+".json"))
	if err == nil {
		var prior Delivery
		if common.Unmarshal(archived, &prior) != nil || prior.ID != id || prior.Path != path || prior.Key != key {
			return errors.New("invalid archived delivery evidence")
		}
		priorBody, _ := common.Marshal(prior.Body)
		if !bytes.Equal(priorBody, encoded) {
			return errors.New("Ops delivery identity conflict")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return errors.New("cannot read archived delivery evidence")
	}
	if requestID != "" && b.state.Requests[requestID] == "" && len(b.state.Requests) >= 10000 {
		return errors.New("Ops evidence capacity exceeded")
	}
	if len(b.state.Deliveries) >= 10000 {
		return errors.New("Ops outbox capacity exceeded")
	}
	b.state.NextDeliverySequence++
	b.state.Deliveries[id] = &Delivery{ID: id, Path: path, Key: key, Body: frozen, Status: "pending", RequestID: requestID, Sequence: b.state.NextDeliverySequence}
	previous := b.state.Requests[requestID]
	if requestID != "" {
		b.state.Requests[requestID] = status
	}
	if err := b.saveLocked(); err != nil {
		delete(b.state.Deliveries, id)
		if requestID != "" {
			if previous == "" {
				delete(b.state.Requests, requestID)
			} else {
				b.state.Requests[requestID] = previous
			}
		}
		return err
	}
	return nil
}

func (b *Bridge) ProcessNext(ctx context.Context, now time.Time) (bool, error) {
	b.deliveryMu.Lock()
	defer b.deliveryMu.Unlock()
	b.mu.Lock()
	jobs := make([]*Delivery, 0, len(b.state.Deliveries))
	for _, job := range b.state.Deliveries {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Sequence == jobs[j].Sequence {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].Sequence < jobs[j].Sequence
	})
	var selected *Delivery
	billingBlocked := false
	for _, candidate := range jobs {
		isBilling := candidate.Path == "agency-center/billing-events"
		if isBilling && candidate.Status != "delivered" {
			if billingBlocked {
				continue
			}
			billingBlocked = true
		}
		if candidate.Status == "pending" && candidate.NextAt <= now.Unix() && (!isBilling || billingBlocked) {
			selected = candidate
			break
		}
	}
	if selected == nil {
		b.mu.Unlock()
		return false, nil
	}
	job := *selected
	b.mu.Unlock()
	err := b.request(ctx, http.MethodPost, job.Path, job.Key, job.Body, nil)
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.state.Deliveries[job.ID]
	current.Attempts++
	if err == nil {
		current.Status = "delivered"
		current.LastError = ""
	} else {
		current.LastError = err.Error()
		current.NextAt = now.Add(time.Duration(min(3600, 1<<min(current.Attempts, 11))) * time.Second).Unix()
		var remote *HTTPError
		isHTTPError := errors.As(err, &remote)
		sequenceGapMayRecover := job.Path == "agency-center/billing-events" && isHTTPError && remote.Status == http.StatusConflict
		if isHTTPError && remote.Status >= 400 && remote.Status < 500 && remote.Status != 408 && remote.Status != 429 && !sequenceGapMayRecover {
			current.Status = "needs_repair"
		}
	}
	if saveErr := b.saveLocked(); saveErr != nil {
		return true, saveErr
	}
	return true, err
}

func (b *Bridge) Retry(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	job := b.state.Deliveries[id]
	if job == nil || job.Status == "delivered" {
		return errors.New("delivery is missing or already delivered")
	}
	job.Status = "pending"
	job.NextAt = 0
	return b.saveLocked()
}

func (b *Bridge) Status() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	jobs := make([]map[string]any, 0, len(b.state.Deliveries))
	for _, job := range b.state.Deliveries {
		jobs = append(jobs, map[string]any{"id": job.ID, "path": job.Path, "status": job.Status, "attempts": job.Attempts, "last_error": job.LastError})
	}
	requests := map[string]int{}
	requestItems := []map[string]string{}
	for id, status := range b.state.Requests {
		requests[status]++
		if status != "usage_queued" && status != "cancel_queued" && status != "blocked" {
			requestItems = append(requestItems, map[string]string{"id": id, "status": status})
		}
	}
	sort.Slice(requestItems, func(i, j int) bool { return requestItems[i]["id"] < requestItems[j]["id"] })
	commands := map[string]int{}
	for _, receipt := range b.state.CommandReceipts {
		commands[receipt.Status]++
	}
	return map[string]any{"enabled": true, "persistence_error": b.fault, "enforce_calls": b.cfg.EnforceCalls, "deliveries": jobs, "request_states": requests, "agency_commands": commands,
		"requests": requestItems, "finance_coverage": "not_exported", "supported_relay_types": []string{"text_generation", "embedding"}}
}

// DeliveryHealth is a bounded snapshot used by the database-backed cutover
// fence. Delivered evidence does not block a transition; queued and repair
// deliveries do.
func (b *Bridge) DeliveryHealth() DeliveryHealth {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := DeliveryHealth{}
	for _, job := range b.state.Deliveries {
		if job.Status != "delivered" {
			result.Backlog++
		}
		if job.Status == "needs_repair" {
			result.NeedsRepair++
		}
	}
	return result
}

func (b *Bridge) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if b.billingSource != nil {
				if _, err := b.PublishBillingEvent(ctx); err != nil {
					common.SysError(err.Error())
				}
			}
			if b.manifestSource != nil && now.Unix()%5 == 0 {
				if _, err := b.PublishSourceManifest(ctx); err != nil {
					common.SysError(err.Error())
				}
			}
			if b.commandExecutor != nil && now.Unix()%2 == 0 {
				if _, err := b.ProcessAgencyCommand(ctx, now); err != nil {
					common.SysError(err.Error())
				}
			}
			if now.Unix()%60 == 0 {
				if err := b.RenewTasks(ctx); err != nil {
					common.SysError(err.Error())
				}
			}
			_, err := b.ProcessNext(ctx, now)
			if err != nil {
				common.SysError(err.Error())
			}
			if now.Unix()%30 == 0 {
				if _, err := b.ArchiveDelivered(); err != nil {
					common.SysError(err.Error())
				}
				if err := b.RefreshNotifications(ctx); err != nil {
					common.SysError(err.Error())
				}
			}
		}
	}
}

func (b *Bridge) RefreshNotifications(ctx context.Context) error {
	b.notificationMu.Lock()
	defer b.notificationMu.Unlock()
	var response struct {
		Items []struct {
			ID   any    `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := b.request(ctx, "GET", "notifications", "", nil, &response); err != nil {
		return err
	}
	for _, notice := range response.Items {
		id := fmt.Sprint(notice.ID)
		if id == "" || strings.ContainsAny(id, "/\\?#") {
			return errors.New("invalid notification ID")
		}
		b.mu.Lock()
		_, done := b.state.Notifications[id]
		b.mu.Unlock()
		if done {
			continue
		}
		archived, err := os.ReadFile(filepath.Join(b.cfg.StateDir, "archive", "notification-"+hash([]byte(id))+".json"))
		if err == nil {
			var receipt map[string]any
			if common.Unmarshal(archived, &receipt) != nil || receipt["id"] != id || receipt["type"] != notice.Type {
				return errors.New("invalid archived notification receipt")
			}
			continue
		}
		if !os.IsNotExist(err) {
			return errors.New("cannot read archived notification receipt")
		}
		path := ""
		switch notice.Type {
		case "catalog_refresh":
			path = "package-catalog"
		case "entitlement_changed", "quota_warning":
			path = "quota-snapshots"
		default:
			continue
		}
		var snapshot map[string]any
		if err := b.request(ctx, "GET", path, "", nil, &snapshot); err != nil {
			return err
		}
		// Persist the refreshed cache and receipt before ever acknowledging it.
		b.mu.Lock()
		b.state.Notifications[id] = map[string]any{"type": notice.Type, "snapshot": snapshot, "received_at": time.Now().UTC().Format(time.RFC3339)}
		err = b.saveLocked()
		b.mu.Unlock()
		if err != nil {
			return err
		}
	}
	// Recreate a missing ACK after a crash between cache persistence and enqueue.
	b.mu.Lock()
	acks := map[string]map[string]any{}
	for id, value := range b.state.Notifications {
		item, ok := value.(map[string]any)
		if !ok {
			b.mu.Unlock()
			return errors.New("invalid notification cache")
		}
		encoded, _ := common.Marshal(item["snapshot"])
		evidence, err := common.Marshal(map[string]any{"snapshot_hash": hash(encoded), "type": item["type"]})
		if err != nil {
			b.mu.Unlock()
			return err
		}
		acks[id] = map[string]any{"idempotency_key": "ops-ack-" + id, "gateway_received_at": item["received_at"],
			"appliedAction": "refreshed_local_cache", "appliedEvidence": string(evidence)}
	}
	b.mu.Unlock()
	for id, ack := range acks {
		if err := b.Enqueue("notifications/"+url.PathEscape(id)+"/ack", "ops-ack-"+id, ack); err != nil {
			return err
		}
	}
	return nil
}
