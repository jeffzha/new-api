// Package opsupstreams registers missing upstreams without touching relay or pricing.
package opsupstreams

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/upstreammap"
	"github.com/QuantumNous/new-api/service/opspush"
	"gorm.io/gorm"
)

const Path = "/api/v1/ingest/v1/upstreams"

type Config struct {
	URL, KeyID, Secret string
	Enabled            bool
	// Models also reports each address's model list (spec 10.2); it needs Enabled.
	Models bool
}

func (c Config) Target() string { return upstreammap.Target(c.URL, c.KeyID) }
func Configured() Config {
	base := strings.TrimRight(os.Getenv("OPS_UPSTREAM_URL"), "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("OPS_PUSH_URL"), "/")
	}
	return Config{URL: base, KeyID: os.Getenv("OPS_PUSH_KEY_ID"), Secret: os.Getenv("OPS_PUSH_SECRET"), Enabled: os.Getenv("OPS_UPSTREAM_SYNC_ENABLED") == "true",
		Models: os.Getenv("OPS_UPSTREAM_MODELS_ENABLED") == "true"}
}

type Syncer struct {
	DB      *gorm.DB
	Config  Config
	Client  *http.Client
	afterID int64
	// models is the current model list of every discovered address, keyed by mapping id; nil until a discovery succeeds.
	models map[int64]modelList
}

type modelList struct {
	Items []modelItem
	Hash  string
}

type modelItem struct {
	Model    string `json:"model"`
	Priority int64  `json:"priority"`
}

const maxModels = 5000

func New(db *gorm.DB, c Config) *Syncer {
	return &Syncer{DB: db, Config: c, Client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func Start(ctx context.Context) error {
	c := Configured()
	if c.Enabled {
		if pushURL := strings.TrimRight(os.Getenv("OPS_PUSH_URL"), "/"); pushURL != "" && pushURL != c.URL {
			return errors.New("upstream registration and usage push must use the same OPS service")
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Path, "..") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) || len(c.Secret) < 16 || c.KeyID == "" {
			return errors.New("invalid OPS upstream registration configuration")
		}
	}
	s := New(model.DB, c)
	go s.Run(ctx)
	return nil
}
func (s *Syncer) Run(ctx context.Context) {
	scan := true
	var retryAfter time.Time
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		if scan {
			if err := s.Discover(ctx); err != nil {
				common.SysError("upstream discovery failed")
			}
		}
		if s.Config.Enabled && !time.Now().Before(retryAfter) {
			if err := s.SyncPending(ctx); err != nil {
				common.SysError("OPS upstream registration pending; retry scheduled")
				retryAfter = time.Now().Add(30 * time.Second)
			} else {
				retryAfter = time.Time{}
			}
		}
		if err := upstreammap.Refresh(s.DB, s.Config.Target()); err != nil {
			common.SysError("upstream identity cache refresh failed")
		}
		scan = false
		select {
		case <-ctx.Done():
			return
		case <-model.UpstreamMappingChanges:
			scan = true
		case <-timer.C:
			scan = true
		}
	}
}
func (s *Syncer) Discover(ctx context.Context) error {
	var channels []model.Channel
	if err := s.DB.WithContext(ctx).Select("id, type, base_url, models, status, priority").Find(&channels).Error; err != nil {
		return err
	}
	lists := map[int64][]modelItem{}
	for _, channel := range channels {
		raw := channel.GetBaseURL()
		if raw == "" {
			raw = constant.GetChannelBaseURL(channel.Type)
		}
		if raw == "" {
			continue
		}
		// Bad addresses do not prevent discovery of the remaining channels.
		if _, err := upstreammap.Normalize(raw); err != nil {
			continue
		}
		row, err := upstreammap.Ensure(s.DB.WithContext(ctx), raw)
		if err != nil {
			return err
		}
		if s.Config.Enabled {
			if err := upstreammap.EnsureBinding(s.DB.WithContext(ctx), row.ID, s.Config.Target()); err != nil {
				return err
			}
		}
		items := lists[row.ID]
		if channel.Status == common.ChannelStatusEnabled {
			// Each channel lists its models once; two channels selling one model both appear (spec 10.2).
			seen := map[string]bool{}
			for _, name := range channel.GetModels() {
				name = strings.TrimSpace(name)
				if name == "" || len([]rune(name)) > 200 || seen[name] {
					continue
				}
				seen[name] = true
				items = append(items, modelItem{Model: name, Priority: channel.GetPriority()})
			}
		}
		lists[row.ID] = items
	}
	if s.Config.Models {
		models := make(map[int64]modelList, len(lists))
		for id, items := range lists {
			slices.SortFunc(items, func(a, b modelItem) int {
				return cmp.Or(strings.Compare(a.Model, b.Model), cmp.Compare(a.Priority, b.Priority))
			})
			if len(items) > maxModels {
				common.SysError(fmt.Sprintf("OPS upstream %d lists more than %d models; the rest are not reported", id, maxModels))
				items = items[:maxModels]
			}
			models[id] = newModelList(items)
		}
		s.models = models
	}
	return nil
}

func newModelList(items []modelItem) modelList {
	if items == nil {
		items = []modelItem{}
	}
	raw, _ := common.Marshal(items)
	return modelList{Items: items, Hash: upstreammap.Hash(string(raw))}
}

// desiredModels is the list to report for an address; an address without any enabled channel reports [].
func (s *Syncer) desiredModels(mappingID int64) (modelList, bool) {
	if !s.Config.Models || s.models == nil {
		return modelList{}, false
	}
	list, ok := s.models[mappingID]
	if !ok {
		list = newModelList(nil)
	}
	return list, true
}
func (s *Syncer) SyncPending(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var pending []upstreammap.Binding
	if err := s.DB.WithContext(ctx).Where("target = ? AND ops_upstream_id IS NULL AND id > ?", s.Config.Target(), s.afterID).Order("id").Limit(100).Find(&pending).Error; err != nil {
		return err
	}
	if len(pending) == 0 && s.afterID > 0 {
		s.afterID = 0
		return s.SyncPending(ctx)
	}
	var failures []error
	for _, binding := range pending {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if err := s.register(ctx, binding); err != nil {
			failures = append(failures, err)
		}
		s.afterID = binding.ID
	}
	if s.Config.Models && s.models != nil {
		// Registered addresses whose model list changed since OPS last accepted one (spec 10.2 resend rule).
		var registered []upstreammap.Binding
		if err := s.DB.WithContext(ctx).Where("target = ? AND ops_upstream_id IS NOT NULL", s.Config.Target()).Order("id").Find(&registered).Error; err != nil {
			return errors.Join(append(failures, err)...)
		}
		sent := 0
		for _, binding := range registered {
			if list, _ := s.desiredModels(binding.MappingID); list.Hash == binding.ModelsHash || sent >= 100 {
				continue
			}
			if ctx.Err() != nil {
				return errors.Join(append(failures, ctx.Err())...)
			}
			sent++
			if err := s.register(ctx, binding); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (s *Syncer) register(ctx context.Context, binding upstreammap.Binding) error {
	var row upstreammap.Mapping
	if err := s.DB.First(&row, binding.MappingID).Error; err != nil {
		return err
	}
	payload := map[string]any{"gateway_upstream_id": row.ID, "base_url": row.BaseURL}
	list, withModels := s.desiredModels(row.ID)
	if withModels {
		payload["models"] = list.Items
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Config.URL+Path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range opspush.Sign(s.Config.KeyID, s.Config.Secret, http.MethodPost, Path, body, time.Now()) {
		request.Header[k] = v
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.Client.Do(request)
	if err != nil {
		return errors.New("OPS registration unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
	response.Body.Close()
	var result struct {
		Data struct {
			ID      int64  `json:"ops_upstream_id"`
			BaseURL string `json:"base_url"`
		} `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if withModels && response.StatusCode == http.StatusBadRequest && common.Unmarshal(raw, &result) == nil && result.Error.Code == "invalid_upstream_models" {
		// Not retried until the list changes; a new address is still registered, without a list, as before.
		common.SysError(fmt.Sprintf("OPS rejected the model list of upstream %d (invalid_upstream_models)", row.ID))
		if err := s.DB.Model(&upstreammap.Binding{}).Where("id = ?", binding.ID).Update("models_hash", list.Hash).Error; err != nil {
			return err
		}
		if binding.OpsUpstreamID != nil {
			return nil
		}
		saved := s.Config.Models
		s.Config.Models = false
		defer func() { s.Config.Models = saved }()
		return s.register(ctx, binding)
	}
	if readErr != nil || len(raw) > 16384 || response.StatusCode != 200 || common.Unmarshal(raw, &result) != nil || result.Data.ID <= 0 || result.Data.BaseURL != row.BaseURL {
		return errors.New("invalid OPS upstream registration response")
	}
	// First successful reply wins; another service cannot overwrite the binding.
	update := s.DB.Model(&upstreammap.Binding{}).Where("id = ? AND ops_upstream_id IS NULL", binding.ID).Update("ops_upstream_id", result.Data.ID)
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 0 {
		var current upstreammap.Binding
		if err := s.DB.First(&current, binding.ID).Error; err != nil {
			return err
		}
		if current.OpsUpstreamID == nil || *current.OpsUpstreamID != result.Data.ID {
			return errors.New("OPS upstream identity conflict")
		}
	}
	if withModels {
		return s.DB.Model(&upstreammap.Binding{}).Where("id = ?", binding.ID).Update("models_hash", list.Hash).Error
	}
	return nil
}
