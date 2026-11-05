// Package opsupstreams registers missing upstreams without touching relay or pricing.
package opsupstreams

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
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
}

func (c Config) Target() string { return upstreammap.Target(c.URL, c.KeyID) }
func Configured() Config {
	base := strings.TrimRight(os.Getenv("OPS_UPSTREAM_URL"), "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("OPS_PUSH_URL"), "/")
	}
	return Config{URL: base, KeyID: os.Getenv("OPS_PUSH_KEY_ID"), Secret: os.Getenv("OPS_PUSH_SECRET"), Enabled: os.Getenv("OPS_UPSTREAM_SYNC_ENABLED") == "true"}
}

type Syncer struct {
	DB      *gorm.DB
	Config  Config
	Client  *http.Client
	afterID int64
}

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
	if err := s.DB.WithContext(ctx).Select("id, type, base_url").Find(&channels).Error; err != nil {
		return err
	}
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
	}
	return nil
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
	return errors.Join(failures...)
}

func (s *Syncer) register(ctx context.Context, binding upstreammap.Binding) error {
	var row upstreammap.Mapping
	if err := s.DB.First(&row, binding.MappingID).Error; err != nil {
		return err
	}
	body, err := common.Marshal(map[string]any{"gateway_upstream_id": row.ID, "base_url": row.BaseURL})
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
	return nil
}
