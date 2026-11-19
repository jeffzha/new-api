// Package opspricing supplies display-only list prices. It never updates model
// options, billing ratios, wallet balances, or the database.
package opspricing

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/opsclient"
)

type Config struct {
	BaseURL, KeyID, Secret string
	Interval, Timeout      time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{BaseURL: strings.TrimRight(os.Getenv("OPS_PRICING_URL"), "/"), KeyID: os.Getenv("OPS_PRICING_KEY_ID"), Secret: os.Getenv("OPS_PRICING_SECRET"), Interval: time.Minute, Timeout: 5 * time.Second}
	if err := opsclient.ValidateBase(c.BaseURL); err != nil {
		return c, err
	}
	if c.KeyID == "" || len(c.KeyID) > 160 || len(c.Secret) < 16 {
		return c, errors.New("OPS list price credentials missing")
	}
	for _, option := range []struct {
		name   string
		dest   *time.Duration
		lo, hi int
	}{{"OPS_PRICING_INTERVAL_SECONDS", &c.Interval, 60, 3600}, {"OPS_PRICING_TIMEOUT_SECONDS", &c.Timeout, 1, 10}} {
		if raw := os.Getenv(option.name); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < option.lo || v > option.hi {
				return c, errors.New("invalid " + option.name)
			}
			*option.dest = time.Duration(v) * time.Second
		}
	}
	return c, nil
}

type priceList struct {
	Version string `json:"version"`
	Station struct {
		QuotaPerUnit    int64  `json:"quotaPerUnit"`
		USDExchangeRate string `json:"usdExchangeRate"`
	} `json:"station"`
	Items []priceEntry `json:"items"`
}

type priceEntry struct {
	Model    string           `json:"model"`
	CallType string           `json:"callType"`
	Official map[string]any   `json:"officialCny"`
	Upcoming []scheduledPrice `json:"upcoming"`
}

type snapshot struct {
	list      priceList
	byModel   map[string][]priceEntry
	checkedAt time.Time
}

type Status struct {
	Version   string `json:"version,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	Stale     bool   `json:"stale"`
	Error     string `json:"error,omitempty"`
}

type Reader struct {
	Config    Config
	Client    *opsclient.Client
	current   atomic.Pointer[snapshot]
	refreshMu sync.Mutex
	mu        sync.Mutex
	lastError string
}

var Default *Reader

func StartConfigured(ctx context.Context) error {
	if os.Getenv("OPS_PRICING_ENABLED") != "true" {
		return nil
	}
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	Default = New(c)
	go Default.Run(ctx)
	common.SysLog("OPS list price display enabled (memory only; billing unchanged)")
	return nil
}

func New(c Config) *Reader {
	return &Reader{Config: c, Client: opsclient.New(c.BaseURL, c.KeyID, c.Secret, c.Timeout)}
}

func (r *Reader) Run(ctx context.Context) {
	for ctx.Err() == nil {
		wait := r.Config.Interval
		if err := r.Refresh(ctx); err != nil {
			common.SysError("OPS list price reader: " + err.Error())
			var readErr *opsclient.ReadError
			if errors.As(err, &readErr) {
				wait = max(wait, readErr.RetryAfter)
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *Reader) Refresh(ctx context.Context) (err error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.lastError = ""
		if err != nil {
			r.lastError = err.Error()
		}
	}()
	target := "/api/v1/catalog/v1/prices"
	previous := r.current.Load()
	if previous != nil {
		target += "?since=" + url.QueryEscape(previous.list.Version)
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.Config.Timeout)
	defer cancel()
	raw, unchanged, err := r.Client.GetVersioned(requestCtx, target, time.Now())
	if err != nil {
		return err
	}
	if unchanged {
		if previous == nil {
			return errors.New("OPS list price response has no initial snapshot")
		}
		next := *previous
		next.checkedAt = time.Now()
		r.current.Store(&next)
		return nil
	}
	var envelope struct {
		Data *priceList `json:"data"`
	}
	if common.Unmarshal(raw, &envelope) != nil || envelope.Data == nil {
		return errors.New("OPS list price response invalid")
	}
	list := *envelope.Data
	if list.Version == "" || len(list.Version) > 128 || list.Items == nil || len(list.Items) > 20000 {
		return errors.New("OPS list price response invalid")
	}
	if _, err := positivePrice(list.Station.USDExchangeRate); err != nil || list.Station.QuotaPerUnit <= 0 {
		return errors.New("OPS list price conversion invalid")
	}
	next := &snapshot{list: list, byModel: make(map[string][]priceEntry), checkedAt: time.Now()}
	seen := make(map[string]bool)
	for _, entry := range list.Items {
		key := entry.Model + "\n" + entry.CallType
		if entry.Model == "" || len(entry.Model) > 200 || entry.CallType == "" || len(entry.CallType) > 40 || seen[key] {
			return errors.New("OPS list price model invalid")
		}
		seen[key] = true
		if err := validateSchedule(entry.Upcoming); err != nil {
			return err
		}
		next.byModel[entry.Model] = append(next.byModel[entry.Model], entry)
	}
	r.current.Store(next)
	return nil
}

func (r *Reader) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{Stale: true, Error: r.lastError}
	if current := r.current.Load(); current != nil {
		s.Version = current.list.Version
		s.CheckedAt = current.checkedAt.UTC().Format(time.RFC3339)
		s.Stale = s.Error != "" || time.Since(current.checkedAt) > 2*r.Config.Interval
	}
	return s
}
