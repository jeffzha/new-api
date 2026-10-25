// Package agencyopspush delivers independent distributor facts after commit.
package agencyopspush

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shirou/gopsutil/cpu"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const path = "/api/v1/gateway/distributor-events"

type Sender struct {
	DB                *gorm.DB
	URL, Gateway, Key string
	Client            *http.Client
	Now               func() time.Time
	Owner             string
}

func New(db *gorm.DB, base, gateway, key string) (*Sender, error) {
	u, err := url.Parse(base)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) ||
		gateway == "" || key == "" || strings.ContainsAny(gateway+key, "\r\n") {
		return nil, errors.New("invalid distributor delivery configuration")
	}
	return &Sender{DB: db, URL: strings.TrimRight(base, "/"), Gateway: gateway, Key: key, Now: time.Now, Owner: common.NewRequestId(),
		Client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: time.Second,
		}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func StartConfigured(ctx context.Context, db *gorm.DB) error {
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" {
		return nil
	}
	s, err := New(db, os.Getenv("OPS_BRIDGE_URL"), os.Getenv("OPS_GATEWAY_ID"), os.Getenv("OPS_GATEWAY_KEY"))
	if err != nil {
		return err
	}
	if !db.Migrator().HasTable(&model.AgencyOpsEvent{}) || !db.Migrator().HasTable(&model.AgencyOpsObjectRevision{}) || !db.Migrator().HasTable(&model.AgencyOpsSenderLease{}) {
		return errors.New("distributor event schema requires migration")
	}
	if err := listenForCommits(ctx, os.Getenv("OPS_DISTRIBUTOR_WAKE_SOCKET")); err != nil {
		return err
	}
	go s.Run(ctx)
	return nil
}

func (s *Sender) Run(ctx context.Context) {
	resume, sampled := time.Time{}, time.Time{}
	load := 100.0
	// Every process that creates facts runs a sender. Only one may send at
	// once; a waking non-owner can hand the lease over within one second.
	// This works across containers without a shared in-memory channel.
	burstUntil := time.Time{}
	for ctx.Err() == nil {
		if time.Since(sampled) >= 5*time.Second {
			percent, err := cpu.Percent(500*time.Millisecond, false)
			sampled = time.Now()
			load = 100
			if err == nil && len(percent) > 0 {
				load = percent[0]
			}
			if load >= 70 || !resume.IsZero() && load >= 50 {
				resume = sampled.Add(30 * time.Second)
			}
			if !resume.IsZero() && sampled.After(resume) && load < 50 {
				resume = time.Time{}
			}
		}
		paused := load >= 70 || !resume.IsZero()
		wait := 30 * time.Second
		if !paused {
			stepCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
			next, err := s.Step(stepCtx)
			wait = next
			cancel()
			if err != nil {
				common.SysError("distributor event delivery unavailable")
				wait = 30 * time.Second
			}
			if time.Now().Before(burstUntil) {
				wait = min(wait, time.Second)
			}
		} else {
			wait = 5 * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-model.AgencyOpsEventWake:
			timer.Stop()
			burstUntil = time.Now().Add(5 * time.Second)
			// Pre-commit hints get bounded rechecks; explicit post-commit hints
			// below do not depend on how long that transaction took to commit.
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		case <-model.AgencyOpsCommitted:
			timer.Stop()
			burstUntil = time.Now().Add(5 * time.Second)
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		case <-timer.C:
		}
	}
}

// Step uses short claim/ack transactions; all HTTP is between them. Each event
// retains its ID on retry, including after a successful receive with a lost ACK.
func (s *Sender) Step(ctx context.Context) (time.Duration, error) {
	now := s.Now().UnixMilli()
	var leased []model.AgencyOpsEvent
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lease := model.AgencyOpsSenderLease{ID: 1}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&lease).Error; err != nil {
			return err
		}
		// Release after the HTTP request. Another producer with a new commit
		// need not wait for an idle process's 30-second recovery check.
		claim := tx.Model(&model.AgencyOpsSenderLease{}).Where("id = ? AND (until_ms <= ? OR owner = ?)", 1, now, s.Owner).
			Updates(map[string]any{"owner": s.Owner, "until_ms": now + 10000})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return nil
		}
		var expired model.AgencyOpsEvent
		err := tx.Where("status = ? AND lease_until <= ?", "leased", now).Order("lease_until").First(&expired).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			if err := tx.Model(&model.AgencyOpsEvent{}).Where("id = ? AND status = ? AND lease_until <= ?", expired.ID, "leased", now).
				Updates(map[string]any{"status": "retry", "next_at_ms": now, "lease_token": "", "lease_until": 0}).Error; err != nil {
				return err
			}
		}
		var candidates []model.AgencyOpsEvent
		// Equality on status/priority lets every database use the ordered due
		// index. An IN plus mixed-direction sort can scan a whole backlog.
		for _, priority := range []int{1, 0} {
			var lane []model.AgencyOpsEvent
			for _, status := range []string{"queued", "retry"} {
				var rows []model.AgencyOpsEvent
				if err := tx.Where("status = ? AND priority = ? AND next_at_ms <= ?", status, priority, now).Order("next_at_ms,id").Limit(10).Find(&rows).Error; err != nil {
					return err
				}
				lane = append(lane, rows...)
			}
			slices.SortFunc(lane, func(a, b model.AgencyOpsEvent) int {
				if a.NextAtMS != b.NextAtMS {
					if a.NextAtMS < b.NextAtMS {
						return -1
					}
					return 1
				}
				if a.ID < b.ID {
					return -1
				}
				if a.ID > b.ID {
					return 1
				}
				return 0
			})
			candidates = append(candidates, lane[:min(len(lane), 10-len(candidates))]...)
			if len(candidates) == 10 {
				break
			}
		}
		size := 0
		for _, row := range candidates {
			if row.ErrorCode == "delivery_isolate" && len(leased) > 0 {
				break
			}
			if size+len(row.Payload) > 500*1024 && len(leased) > 0 {
				break
			}
			token := common.NewRequestId()
			result := tx.Model(&model.AgencyOpsEvent{}).Where("id = ? AND status IN ? AND next_at_ms <= ?", row.ID, []string{"queued", "retry"}, now).
				Updates(map[string]any{"status": "leased", "lease_token": token, "lease_until": now + 15000, "attempts": gorm.Expr("attempts + ?", 1)})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				row.LeaseToken = token
				row.Attempts++
				leased = append(leased, row)
				size += len(row.Payload)
			}
			if row.ErrorCode == "delivery_isolate" {
				break
			}
		}
		return nil
	})
	if err != nil {
		return 30 * time.Second, err
	}
	if len(leased) == 0 {
		_ = s.DB.WithContext(ctx).Model(&model.AgencyOpsSenderLease{}).Where("id = ? AND owner = ?", 1, s.Owner).Update("until_ms", 0).Error
		wait := 30 * time.Second
		for _, status := range []string{"queued", "retry"} {
			var next model.AgencyOpsEvent
			err := s.DB.WithContext(ctx).Select("id,next_at_ms").Where("status = ?", status).Order("next_at_ms").First(&next).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return 30 * time.Second, err
			}
			if err == nil {
				wait = min(wait, max(time.Second, time.Duration(next.NextAtMS-now)*time.Millisecond))
			}
		}
		return wait, nil
	}
	var body bytes.Buffer
	body.WriteString(`{"schemaVersion":"1","events":[`)
	for i, row := range leased {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(string(row.Payload))
	}
	body.WriteString(`]}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+path, &body)
	if err != nil {
		return 30 * time.Second, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gateway-Id", s.Gateway)
	request.Header.Set("X-Gateway-Key", s.Key)
	response, sendErr := s.Client.Do(request)
	ack := map[string]bool{}
	invalid := false
	if sendErr == nil {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 65537))
		response.Body.Close()
		var receipt struct {
			Data struct {
				Accepted   []string `json:"accepted"`
				Duplicates []string `json:"duplicates"`
				Durable    bool     `json:"durable"`
			} `json:"data"`
		}
		if response.StatusCode == 200 && readErr == nil && len(raw) <= 65536 && common.Unmarshal(raw, &receipt) == nil && receipt.Data.Durable {
			for _, id := range append(receipt.Data.Accepted, receipt.Data.Duplicates...) {
				ack[id] = true
			}
		}
		invalid = response.StatusCode == 409 || response.StatusCode == 400 || response.StatusCode == 413
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range leased {
			state, code, delivered := "retry", "delivery_unavailable", int64(0)
			if ack[row.EventID] {
				state, code, delivered = "delivered", "", s.Now().UnixMilli()
			} else if invalid && len(leased) == 1 || !invalid && row.Attempts >= 8 {
				state, code = "dead", "delivery_needs_review"
			}
			delays := []int64{1000, 5000, 15000, 60000, 300000}
			next := s.Now().UnixMilli() + delays[min(row.Attempts-1, 4)] + row.ID%997
			if invalid && len(leased) > 1 {
				// Isolate a rejected batch on retry; a poison message cannot move
				// all its valid neighbours directly to dead-letter.
				code = "delivery_isolate"
			}
			if err := tx.Model(&model.AgencyOpsEvent{}).Where("id = ? AND status = ? AND lease_token = ?", row.ID, "leased", row.LeaseToken).
				Updates(map[string]any{"status": state, "error_code": code, "next_at_ms": next, "delivered_ms": delivered, "lease_token": "", "lease_until": 0}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.AgencyOpsSenderLease{}).Where("id = ? AND owner = ?", 1, s.Owner).Update("until_ms", 0).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return time.Second, err
	}
	if len(ack) == 0 {
		return time.Second, nil
	}
	return 200 * time.Millisecond, nil
}
