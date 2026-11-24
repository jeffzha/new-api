package modelhealth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Destination struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	URLRef         string `json:"url_ref"`
	SignRef        string `json:"sign_ref"`
	Enabled        bool   `json:"enabled"`
	Channel        int    `json:"channel_id"`
	Type           string `json:"type"`
	Source         string `json:"source"`
	Interval       int    `json:"interval_seconds"`
	MaxPerIncident int    `json:"max_per_incident"`
	MaxPerDay      int    `json:"max_per_day"`
	StartHour      int    `json:"start_hour"`
	EndHour        int    `json:"end_hour"`
	Recovery       bool   `json:"recovery"`
}

var destinationRef = regexp.MustCompile(`^MODEL_HEALTH_(HOOK|SIGN)_[A-Z0-9_]{1,64}$`)
var destinationID = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

func SaveDestination(ctx context.Context, d Destination) error {
	if !destinationID.MatchString(d.ID) || strings.TrimSpace(d.Name) == "" || len(d.Name) > 120 || !slices.Contains([]string{"feishu", "dingtalk", "webhook"}, d.Kind) || !destinationRef.MatchString(d.URLRef) || !strings.HasPrefix(d.URLRef, "MODEL_HEALTH_HOOK_") || d.Interval < 60 || d.Interval > 86400 || d.MaxPerIncident < 1 || d.MaxPerIncident > 12 || d.MaxPerDay < 1 || d.MaxPerDay > 100 || d.StartHour < 0 || d.EndHour > 24 || d.StartHour >= d.EndHour || d.Channel < 0 || !slices.Contains([]string{"", "user", "manual", "scheduled"}, d.Source) {
		return invalid("Invalid notification destination or limits")
	}
	if d.SignRef != "" && (!destinationRef.MatchString(d.SignRef) || !strings.HasPrefix(d.SignRef, "MODEL_HEALTH_SIGN_") || d.Kind == "webhook") {
		return invalid("Invalid robot signing reference")
	}
	if raw := os.Getenv(d.URLRef); raw != "" {
		if err := validateDestination(raw, d.Kind); err != nil {
			return err
		}
	}
	return model.SaveModelHealthConfig(model.DB.WithContext(ctx), "notify-"+d.ID, "notification", d)
}

func validateDestination(address, kind string) error {
	u, err := checkedURL(address)
	if err != nil {
		return err
	}
	switch kind {
	case "feishu":
		if !slices.Contains([]string{"open.feishu.cn", "open.larksuite.com"}, u.Hostname()) || !strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") {
			return invalid("Invalid Feishu robot destination")
		}
	case "dingtalk":
		if u.Hostname() != "oapi.dingtalk.com" || u.Path != "/robot/send" {
			return invalid("Invalid DingTalk robot destination")
		}
	}
	return nil
}

func destinations(db *gorm.DB) ([]Destination, error) {
	var configs []model.ModelHealthConfig
	if err := db.Where("kind = ?", "notification").Order("id").Find(&configs).Error; err != nil {
		return nil, err
	}
	rows := make([]Destination, 0, len(configs))
	for _, c := range configs {
		var d Destination
		if err := common.UnmarshalJsonStr(c.Value, &d); err != nil {
			return nil, err
		}
		rows = append(rows, d)
	}
	return rows, nil
}

func queueDelivery(ctx context.Context, d Destination, incident, event, payload string, now int64) error {
	return model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		q := tx.Model(&model.ModelHealthDelivery{}).Where("target = ?", d.ID)
		var count int64
		if err := q.Session(&gorm.Session{}).Where("created_at >= ?", dayStart(now)).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(d.MaxPerDay) {
			return invalid("Notification daily limit reached")
		}
		q = q.Where("incident = ? AND event = ?", incident, event)
		if err := q.Session(&gorm.Session{}).Count(&count).Error; err != nil {
			return err
		}
		limit := d.MaxPerIncident
		if event != "failure" {
			limit = 1
		}
		if count >= int64(limit) {
			return nil
		}
		var last model.ModelHealthDelivery
		if err := q.Order("created_at desc").Limit(1).Find(&last).Error; err != nil {
			return err
		}
		if last.ID != "" && (last.State == "queued" || last.State == "sending" || now-last.CreatedAt < int64(d.Interval)) {
			return nil
		}
		row := model.ModelHealthDelivery{ID: hash(d.ID, incident, event, count), Target: d.ID, Incident: incident, Event: event, State: "queued", CreatedAt: now, NextAt: now, Payload: payload}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
}

func TestDestination(ctx context.Context, id string) error {
	if !WorkerEnabled() {
		return invalid("Monitoring worker is disabled")
	}
	d, err := model.ReadModelHealthConfig(model.DB.WithContext(ctx), "notify-"+id, Destination{})
	if err != nil {
		return err
	}
	if d.ID == "" {
		return invalid("Notification destination not found")
	}
	if err := validateDestination(os.Getenv(d.URLRef), d.Kind); err != nil {
		return err
	}
	now := time.Now().Unix()
	return queueDelivery(ctx, d, hash("test", now/int64(d.Interval)), "test", "[Model Health] Test notification", now)
}

func notifyIncidents(ctx context.Context, now int64) {
	db := model.DB.WithContext(ctx)
	dests, err := destinations(db)
	if err != nil {
		return
	}
	hour := time.Unix(now, 0).In(cst).Hour()
	dests = slices.DeleteFunc(dests, func(d Destination) bool { return !d.Enabled || hour < d.StartHour || hour >= d.EndHour })
	if len(dests) == 0 {
		return
	}
	targets, err := catalog(db, 0)
	if err != nil {
		return
	}
	for _, t := range targets {
		if t.Paused || ctx.Err() != nil {
			continue
		}
		for _, ep := range t.Endpoints {
			r, _, err := effectiveRule(db, t, ep)
			if err != nil {
				continue
			}
			rows, err := samples(db, t, ep, now-86400, now+1)
			if err != nil {
				continue
			}
			for _, d := range dests {
				if (d.Channel != 0 && d.Channel != t.ChannelID) || (d.Type != "" && d.Type != t.Type) {
					continue
				}
				notifyTarget(ctx, db, t, ep, r, d, rows, now)
			}
		}
	}
}

func notifyTarget(ctx context.Context, db *gorm.DB, t Target, ep string, r Rule, d Destination, rows []Sample, now int64) {
	var first, last Sample
	failures, leadingSuccess, recoveryStreak := 0, 0, 0
	for _, s := range rows {
		if (d.Source != "" && s.Source != d.Source) || !slices.Contains([]string{"succeeded", "failed", "uncertain"}, s.State) {
			continue
		}
		if s.State == "succeeded" {
			if failures == 0 {
				leadingSuccess++
				continue
			}
			recoveryStreak++
			if recoveryStreak >= r.RecoverAfter {
				break
			}
			continue
		}
		recoveryStreak = 0
		if failures == 0 {
			last = s
		}
		first = s
		failures++
	}
	if failures < r.FailAfter {
		return
	}
	incident := hash(t.ID, t.Upstream, ep, d.Source, first.Source, first.RequestID, first.RunID, first.At)
	event := "failure"
	if leadingSuccess >= r.RecoverAfter {
		var count int64
		if !d.Recovery || db.Model(&model.ModelHealthDelivery{}).Where("target = ? AND incident = ? AND event = ? AND state = ?", d.ID, incident, "failure", "sent").Count(&count).Error != nil || count == 0 {
			return
		}
		event = "recovery"
	}
	payload := fmt.Sprintf("[Model Health] %s\nChannel: %s (#%d)\nModel: %s → %s\nEndpoint: %s\nSource: %s; stage: %s; HTTP: %d; reason: %s\nFirst: %s; latest: %s; samples: %d\nRequest: %s; probe: %d\nReview: /model-health", event, t.Channel, t.ChannelID, t.Model, t.Upstream, ep, last.Source, last.Stage, last.HTTP, last.Reason, time.Unix(first.At, 0).In(cst).Format(time.RFC3339), time.Unix(last.At, 0).In(cst).Format(time.RFC3339), failures, last.RequestID, last.RunID)
	_ = queueDelivery(ctx, d, incident, event, payload, now)
}

func deliver(ctx context.Context, client *http.Client, now int64) error {
	var dj model.ModelHealthDelivery
	err := model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Model(&model.ModelHealthDelivery{}).Where("state = ? AND lease_until <= ?", "sending", now).Update("state", "uncertain").Error; err != nil {
			return err
		}
		if err := tx.Where("state = ? AND next_at <= ?", "queued", now).Order("created_at, id").Take(&dj).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return tx.Model(&dj).Updates(map[string]any{"state": "sending", "lease_until": now + 60, "attempts": dj.Attempts + 1}).Error
	})
	if err != nil || dj.ID == "" {
		return err
	}
	db := model.DB.WithContext(ctx)
	d, err := model.ReadModelHealthConfig(db, "notify-"+dj.Target, Destination{})
	if err != nil {
		return err
	}
	status, state := 0, "failed"
	if d.Enabled || dj.Event == "test" {
		status, state = sendNotification(ctx, client, d, dj.Payload, now)
	}
	fields := map[string]any{"state": state, "status": status, "next_at": 0, "lease_until": 0}
	if state == "retry" && dj.Attempts < 2 {
		fields["state"] = "queued"
		fields["next_at"] = now + int64(60*(dj.Attempts+1))
	} else if state == "retry" {
		fields["state"] = "failed"
	}
	return db.Model(&dj).Where("state = ? AND lease_until = ?", "sending", now+60).Updates(fields).Error
}

func sendNotification(ctx context.Context, client *http.Client, d Destination, text string, now int64) (int, string) {
	address, secret := os.Getenv(d.URLRef), os.Getenv(d.SignRef)
	if validateDestination(address, d.Kind) != nil || (d.SignRef != "" && secret == "") {
		return 0, "failed"
	}
	u, _ := checkedURL(address)
	body := map[string]any{"text": text}
	switch d.Kind {
	case "feishu":
		body = map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
		if secret != "" {
			ts := strconv.FormatInt(now, 10)
			body["timestamp"] = ts
			body["sign"] = base64.StdEncoding.EncodeToString(hmac.New(sha256.New, []byte(ts+"\n"+secret)).Sum(nil))
		}
	case "dingtalk":
		body = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
		if secret != "" {
			ts := strconv.FormatInt(now*1000, 10)
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(ts + "\n" + secret))
			q := u.Query()
			q.Set("timestamp", ts)
			q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			u.RawQuery = q.Encode()
		}
	}
	b, _ := common.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return 0, "failed"
	}
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = nil
	resp, err := client.Do(req)
	if err != nil {
		return 0, "uncertain"
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return resp.StatusCode, "retry"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, "failed"
	}
	if d.Kind != "webhook" {
		var result map[string]any
		data, e := readBounded(resp.Body, 32<<10)
		if e != nil || common.Unmarshal(data, &result) != nil {
			return resp.StatusCode, "failed"
		}
		key := "errcode"
		if d.Kind == "feishu" {
			key = "code"
		}
		code, ok := result[key].(float64)
		if !ok && d.Kind == "feishu" {
			code, ok = result["StatusCode"].(float64)
		}
		if !ok || code != 0 {
			return resp.StatusCode, "failed"
		}
	}
	return resp.StatusCode, "sent"
}
