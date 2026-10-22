// Package opspush delivers usage logs to TokenOps under the station push spec v1
// (TokenOps docs/contracts/STATION_INTEGRATION_SPEC_v1.md).
//
// The logs table is the outbox. The cursor is the highest delivered log id; a
// batch body is persisted before it is sent and resent byte for byte until OPS
// answers 200, so a retry always carries the same batchId and events. Event
// fields mirror the OPS pull collector (tokenops/core/collector.py
// normalise_item) so pushed and pulled copies of one log dedupe by requestId.
package opspush

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	pushPath      = "/api/v1/ingest/v1/usage-events"
	maxEvents     = 500
	maxBody       = 1024 * 1024
	maxText       = 200
	maxExtra      = 4096
	maxCount      = 1_000_000_000_000
	maxQuota      = 1_000_000_000_000_000
	maxSeconds    = 10_000_000
	settleSeconds = 10 // logs younger than this wait one more round, so ids committed out of order are not skipped
	leaseSeconds  = 60
	idleWait      = 3 * time.Second
	stalledWait   = 10 * time.Minute
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,160}$`)

// State is the single pusher row (id 1). Owner/LeaseUntil keep one pushing
// instance when blue and green both run.
type State struct {
	Id            int    `gorm:"primaryKey;autoIncrement:false"`
	Cursor        int64  `gorm:"not null;default:0"`
	BatchId       string `gorm:"type:varchar(160);not null;default:''"`
	BatchLast     int64  `gorm:"not null;default:0"`
	BatchBody     string `gorm:"type:text"`
	Attempts      int    `gorm:"not null;default:0"`
	Owner         string `gorm:"type:varchar(64);not null;default:''"`
	LeaseUntil    int64  `gorm:"not null;default:0"`
	LastError     string `gorm:"type:varchar(255);not null;default:''"`
	LastSuccessAt int64  `gorm:"not null;default:0"`
	Delivered     int64  `gorm:"not null;default:0"`
	Rejected      int64  `gorm:"not null;default:0"`
}

func (State) TableName() string { return "ops_push_states" }

type Config struct {
	URL, KeyID, Secret string
	Since              int64 // first run only: start after the last log older than this; 0 = start at the newest log
}

type Pusher struct {
	DB, LogDB *gorm.DB
	Config    Config
	Client    *http.Client
	Owner     string
	Now       func() time.Time
}

// StartConfigured starts the pusher when OPS_PUSH_URL is set.
func StartConfigured(ctx context.Context) error {
	base := strings.TrimRight(os.Getenv("OPS_PUSH_URL"), "/")
	if base == "" {
		return nil
	}
	config := Config{URL: base, KeyID: os.Getenv("OPS_PUSH_KEY_ID"), Secret: os.Getenv("OPS_PUSH_SECRET")}
	if err := config.validate(); err != nil {
		return err
	}
	if raw := os.Getenv("OPS_PUSH_SINCE"); raw != "" {
		since, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || since < 0 {
			return errors.New("OPS_PUSH_SINCE must be a Unix timestamp in seconds")
		}
		config.Since = since
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return errors.New("Ops push needs SQL log ids; ClickHouse logs are not supported")
	}
	pusher := New(model.DB, model.LOG_DB, config)
	if err := pusher.Migrate(); err != nil {
		return err
	}
	go pusher.Run(ctx)
	common.SysLog("Ops push enabled: " + base + pushPath)
	return nil
}

func (c Config) validate() error {
	parsed, err := url.Parse(c.URL)
	loopback := parsed != nil && parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost")
	if err != nil || (parsed.Scheme != "https" && !loopback) || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("OPS_PUSH_URL must be an https origin (http only for loopback)")
	}
	if !idPattern.MatchString(c.KeyID) {
		return errors.New("OPS_PUSH_KEY_ID is missing or invalid")
	}
	if len(c.Secret) < 16 {
		return errors.New("OPS_PUSH_SECRET is missing")
	}
	return nil
}

func New(db, logDB *gorm.DB, config Config) *Pusher {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	host, _ := os.Hostname()
	if len(host) > 40 {
		host = host[:40]
	}
	return &Pusher{
		DB: db, LogDB: logDB, Config: config, Owner: host + "-" + hex.EncodeToString(raw), Now: time.Now,
		Client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

func (p *Pusher) Migrate() error {
	if err := p.DB.AutoMigrate(&State{}); err != nil {
		return err
	}
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		// A batch body is up to 1 MB; MySQL TEXT stops at 64 KB.
		if err := p.DB.Exec("ALTER TABLE ops_push_states MODIFY batch_body MEDIUMTEXT").Error; err != nil {
			return err
		}
	}
	var cursor int64
	if p.Config.Since > 0 {
		p.LogDB.Model(&model.Log{}).Where("created_at < ?", p.Config.Since).Select("COALESCE(MAX(id), 0)").Scan(&cursor)
	} else {
		p.LogDB.Model(&model.Log{}).Select("COALESCE(MAX(id), 0)").Scan(&cursor)
	}
	// Only the first instance to start sets the starting point; later starts keep the stored cursor.
	return p.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&State{Id: 1, Cursor: cursor}).Error
}

func (p *Pusher) Run(ctx context.Context) {
	for {
		wait := p.Step()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Step does one unit of work and returns how long to wait before the next.
func (p *Pusher) Step() time.Duration {
	state, ok := p.lease()
	if !ok {
		return leaseSeconds * time.Second / 2
	}
	if state.BatchId == "" {
		built, err := p.build(state)
		if err != nil {
			p.fail(state, "build: "+err.Error())
			return stalledWait
		}
		if !built {
			return idleWait
		}
	}
	return p.send(state)
}

func (p *Pusher) lease() (*State, bool) {
	now := p.Now().Unix()
	err := p.DB.Model(&State{}).Where("id = 1 AND (owner = ? OR lease_until < ?)", p.Owner, now).
		Updates(map[string]any{"owner": p.Owner, "lease_until": now + leaseSeconds}).Error
	// Read the owner back: MySQL counts changed rows, so RowsAffected is 0 when the lease is renewed within a second.
	var state State
	if err != nil || p.DB.First(&state, 1).Error != nil || state.Owner != p.Owner {
		return nil, false
	}
	return &state, true
}

// save writes the given columns only while this instance still holds the lease.
func (p *Pusher) save(values map[string]any) error {
	if err := p.DB.Model(&State{}).Where("id = 1 AND owner = ?", p.Owner).Updates(values).Error; err != nil {
		return err
	}
	var owner string
	if err := p.DB.Model(&State{}).Where("id = 1").Select("owner").Scan(&owner).Error; err != nil || owner != p.Owner {
		return errors.New("lease lost")
	}
	return nil
}

func (p *Pusher) fail(state *State, message string) {
	if len(message) > 255 {
		message = message[:255]
	}
	state.Attempts++
	_ = p.save(map[string]any{"attempts": state.Attempts, "last_error": message})
	common.SysError("Ops push: " + message)
}

type batch struct {
	SchemaVersion string           `json:"schemaVersion"`
	BatchId       string           `json:"batchId"`
	Events        []map[string]any `json:"events"`
}

// build persists the next batch after the cursor. It returns false when there is nothing to send.
func (p *Pusher) build(state *State) (bool, error) {
	var logs []*model.Log
	err := p.LogDB.Where("id > ? AND type IN ? AND created_at <= ?", state.Cursor,
		[]int{model.LogTypeConsume, model.LogTypeError}, p.Now().Unix()-settleSeconds).
		Order("id").Limit(maxEvents).Find(&logs).Error
	if err != nil || len(logs) == 0 {
		return false, err
	}
	names := p.channelNames(logs)
	for len(logs) > 0 {
		events, skipped := make([]map[string]any, 0, len(logs)), 0
		for _, log := range logs {
			if event := Event(log, names[log.ChannelId]); event != nil {
				events = append(events, event)
			} else {
				skipped++
			}
		}
		last := int64(logs[len(logs)-1].Id)
		if skipped > 0 {
			common.SysError(fmt.Sprintf("Ops push: skipped %d log(s) up to id %d without a valid request id, user or model", skipped, last))
		}
		if len(events) == 0 {
			state.Cursor = last
			return false, p.save(map[string]any{"cursor": last})
		}
		id := fmt.Sprintf("gw-%d-%d", state.Cursor+1, last)
		body, err := common.Marshal(batch{SchemaVersion: "1", BatchId: id, Events: events})
		if err != nil {
			return false, err
		}
		if len(body) > maxBody && len(logs) > 1 {
			logs = logs[:len(logs)/2]
			continue
		}
		state.BatchId, state.BatchLast, state.BatchBody, state.Attempts = id, last, string(body), 0
		return true, p.save(map[string]any{"batch_id": id, "batch_last": last, "batch_body": state.BatchBody, "attempts": 0})
	}
	return false, nil
}

func (p *Pusher) channelNames(logs []*model.Log) map[int]string {
	ids := make([]int, 0)
	for _, log := range logs {
		if log.ChannelId > 0 {
			ids = append(ids, log.ChannelId)
		}
	}
	names := map[int]string{}
	if len(ids) == 0 {
		return names
	}
	var rows []struct {
		Id   int
		Name string
	}
	p.DB.Table("channels").Select("id, name").Where("id IN ?", ids).Find(&rows)
	for _, row := range rows {
		names[row.Id] = row.Name
	}
	return names
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Rejected []struct {
				Index     int    `json:"index"`
				RequestId string `json:"requestId"`
				Reason    string `json:"reason"`
			} `json:"rejected"`
		} `json:"details"`
	} `json:"error"`
}

type okBody struct {
	Data struct {
		Accepted   int `json:"accepted"`
		Duplicates int `json:"duplicates"`
		Rejected   []struct {
			RequestId string `json:"requestId"`
			Reason    string `json:"reason"`
		} `json:"rejected"`
	} `json:"data"`
}

func (p *Pusher) send(state *State) time.Duration {
	body := []byte(state.BatchBody)
	request, err := http.NewRequest(http.MethodPost, p.Config.URL+pushPath, bytes.NewReader(body))
	if err != nil {
		p.fail(state, err.Error())
		return stalledWait
	}
	for name, values := range Sign(p.Config.KeyID, p.Config.Secret, http.MethodPost, pushPath, body, p.Now()) {
		request.Header[name] = values
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.Client.Do(request)
	if err != nil {
		p.fail(state, "send "+state.BatchId+": "+err.Error())
		return backoff(state.Attempts, 0)
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		var ok okBody
		_ = common.Unmarshal(raw, &ok)
		for _, item := range ok.Data.Rejected {
			common.SysError(fmt.Sprintf("Ops push: %s rejected %s (%s)", state.BatchId, item.RequestId, item.Reason))
		}
		count := ok.Data.Accepted + ok.Data.Duplicates + len(ok.Data.Rejected)
		err := p.save(map[string]any{
			"cursor": state.BatchLast, "batch_id": "", "batch_last": 0, "batch_body": "", "attempts": 0, "last_error": "",
			"last_success_at": p.Now().Unix(), "delivered": gorm.Expr("delivered + ?", count),
			"rejected": gorm.Expr("rejected + ?", len(ok.Data.Rejected)),
		})
		if err != nil {
			return idleWait
		}
		return 0 // there may be more waiting
	}
	var failure errorBody
	_ = common.Unmarshal(raw, &failure)
	code := failure.Error.Code
	if code == "" {
		code = http.StatusText(response.StatusCode)
	}
	message := fmt.Sprintf("send %s: HTTP %d %s", state.BatchId, response.StatusCode, code)
	switch {
	case response.StatusCode == http.StatusBadRequest && code == "invalid_events" && len(failure.Error.Details.Rejected) > 0:
		// The spec lets the same batchId be resent once corrected: drop the rejected events and resend the rest.
		p.dropRejected(state, failure)
		return 0
	case response.StatusCode == http.StatusTooManyRequests:
		retry, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		p.fail(state, message)
		return backoff(state.Attempts, time.Duration(retry)*time.Second)
	case response.StatusCode >= 500 || code == "timestamp_out_of_window" || code == "nonce_reused":
		p.fail(state, message)
		return backoff(state.Attempts, 0)
	default:
		// Signature, credential, conflict or format errors need a person; keep the batch and look again later.
		p.fail(state, message)
		return stalledWait
	}
}

func (p *Pusher) dropRejected(state *State, failure errorBody) {
	var current batch
	if err := common.Unmarshal([]byte(state.BatchBody), &current); err != nil {
		p.fail(state, "stored batch unreadable: "+err.Error())
		return
	}
	drop := map[int]bool{}
	for _, item := range failure.Error.Details.Rejected {
		drop[item.Index] = true
		common.SysError(fmt.Sprintf("Ops push: %s dropped %s (%s)", state.BatchId, item.RequestId, item.Reason))
	}
	kept := current.Events[:0]
	for index, event := range current.Events {
		if !drop[index] {
			kept = append(kept, event)
		}
	}
	dropped := len(current.Events) - len(kept)
	if len(kept) == 0 {
		_ = p.save(map[string]any{"cursor": state.BatchLast, "batch_id": "", "batch_last": 0, "batch_body": "",
			"attempts": 0, "rejected": gorm.Expr("rejected + ?", dropped)})
		return
	}
	current.Events = kept
	body, _ := common.Marshal(current)
	_ = p.save(map[string]any{"batch_body": string(body), "rejected": gorm.Expr("rejected + ?", dropped)})
}

// backoff is min(300, 2^(n-1)) seconds times a 0.5–1.0 jitter, never below the server's Retry-After.
func backoff(attempts int, floor time.Duration) time.Duration {
	exponent := math.Min(float64(attempts-1), 9)
	wait := time.Duration(math.Min(300, math.Pow(2, math.Max(exponent, 0))) * (0.5 + mrand.Float64()/2) * float64(time.Second))
	if wait < floor {
		wait = floor
	}
	return wait
}

// Sign returns the four spec 3.1 headers for one request; each call draws a new nonce.
func Sign(keyID, secret, method, target string, body []byte, now time.Time) http.Header {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return signWith(keyID, secret, method, target, body, strconv.FormatInt(now.Unix(), 10), hex.EncodeToString(raw))
}

func signWith(keyID, secret, method, target string, body []byte, timestamp, nonce string) http.Header {
	key := sha256.Sum256([]byte(secret))
	bodySum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(hex.EncodeToString(key[:])))
	mac.Write([]byte(method + "\n" + target + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(bodySum[:])))
	header := http.Header{}
	header.Set("X-Gateway-Id", keyID)
	header.Set("X-Timestamp", timestamp)
	header.Set("X-Nonce", nonce)
	header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	return header
}

// Event maps one consume or error log to a spec 4.3 event, or nil when OPS would reject it.
func Event(log *model.Log, channelName string) map[string]any {
	status := map[int]string{model.LogTypeConsume: "success", model.LogTypeError: "error"}[log.Type]
	requestId := log.RequestId
	switch {
	case requestId == "":
		requestId = "log-" + strconv.Itoa(log.Id)
	case log.Type == model.LogTypeError:
		// A retried request logs each failed attempt and then its charge under one request id; the error
		// rows get their own ids so they never take the id of the charge (same rule as the OPS collector).
		requestId += "-e" + strconv.Itoa(log.Id)
	}
	if status == "" || !idPattern.MatchString(requestId) || log.UserId <= 0 || !text(log.ModelName) || log.ModelName == "" {
		return nil
	}
	other := object(json.RawMessage(log.Other))
	facts := object(other["usage_facts"])
	event := map[string]any{
		"requestId":        requestId,
		"occurredAt":       time.Unix(log.CreatedAt, 0).UTC().Format(time.RFC3339),
		"userId":           strconv.Itoa(log.UserId),
		"model":            log.ModelName,
		"status":           status,
		"promptTokens":     clamp(int64(log.PromptTokens)),
		"completionTokens": clamp(int64(log.CompletionTokens)),
		"cacheReadTokens":  count(other["cache_tokens"]),
		"cacheWriteTokens": count(other["cache_creation_tokens"]),
		"imageCount":       count(other["image_count"]),
		"isStream":         log.IsStream,
	}
	optionalText(event, "upstreamRequestId", log.UpstreamRequestId)
	optionalText(event, "username", log.Username)
	optionalText(event, "tokenName", log.TokenName)
	optionalText(event, "group", log.Group)
	optionalText(event, "channelName", channelName)
	if log.TokenId > 0 {
		event["tokenId"] = strconv.Itoa(log.TokenId)
	}
	if log.ChannelId > 0 {
		event["channelId"] = strconv.Itoa(log.ChannelId)
	}
	if log.Quota >= 0 && int64(log.Quota) <= maxQuota {
		event["gatewayQuota"] = log.Quota
	}
	if log.UseTime >= 0 && int64(log.UseTime)*1000 <= maxCount {
		event["latencyMs"] = log.UseTime * 1000
	}
	for _, key := range []string{"seconds", "duration"} {
		if value, ok := number(facts[key]); ok && value >= 0 && value <= maxSeconds {
			event["durationSeconds"] = value
			break
		}
	}
	if attributes := logAttributes(other, facts); len(attributes) > 0 {
		event["attributes"] = attributes
	}
	if extra := extraOf(other); extra != nil {
		event["extra"] = extra
	}
	return event
}

// object reads a Gateway log `other` (or a nested object in it) with its values left raw, so integers
// and fractions stay distinguishable the way the OPS collector's Python parser sees them.
func object(raw json.RawMessage) map[string]json.RawMessage {
	values := map[string]json.RawMessage{}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || common.Unmarshal(raw, &values) != nil {
		return map[string]json.RawMessage{}
	}
	return values
}

func text(value string) bool {
	if len([]rune(value)) > maxText {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func optionalText(event map[string]any, name, value string) {
	if value != "" && text(value) {
		event[name] = value
	}
}

func clamp(value int64) int64 {
	return max(0, min(value, maxCount))
}

var integerLiteral = regexp.MustCompile(`^[0-9]+$`)

// integer reads a non-negative JSON integer literal; fractions, exponents, strings and negatives are not integers.
func integer(raw json.RawMessage) (int64, bool) {
	if !integerLiteral.Match(raw) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(string(raw), 10, 64)
	return parsed, err == nil
}

// count reads a token or image count the way the OPS collector does: anything but a non-negative integer is 0.
func count(raw json.RawMessage) int64 {
	parsed, _ := integer(raw)
	return min(parsed, maxCount)
}

func number(raw json.RawMessage) (float64, bool) {
	var parsed float64
	if len(raw) == 0 || !strings.ContainsAny(string(raw[:1]), "-0123456789") || common.Unmarshal(raw, &parsed) != nil {
		return 0, false
	}
	return parsed, !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
}

// logAttributes mirrors collector.LOG_ATTRIBUTES: the first valid value of each name wins.
func logAttributes(other, facts map[string]json.RawMessage) map[string]any {
	keys := []struct {
		name, kind string
		keys       []string
	}{
		{"size", "text", []string{"size", "image_size"}},
		{"quality", "text", []string{"quality"}},
		{"resolution", "text", []string{"resolution"}},
		{"withAudio", "flag", []string{"withAudio", "with_audio", "generate_audio"}},
		{"referenceVideo", "flag", []string{"referenceVideo", "reference_video"}},
		{"characters", "count", []string{"characters", "character_count"}},
	}
	found := map[string]any{}
	for _, attribute := range keys {
		for _, source := range []map[string]json.RawMessage{other, facts} {
			for _, key := range attribute.keys {
				value, present := source[key]
				if _, done := found[attribute.name]; !present || done {
					continue
				}
				switch attribute.kind {
				case "text":
					var s string
					if common.Unmarshal(value, &s) == nil && s != "" && len([]rune(s)) <= 40 && text(s) {
						found[attribute.name] = s
					}
				case "flag":
					if string(value) == "true" || string(value) == "false" {
						found[attribute.name] = string(value) == "true"
					}
				case "count":
					if parsed, ok := integer(value); ok && parsed <= maxCount {
						found[attribute.name] = parsed
					}
				}
			}
		}
	}
	return found
}

// billingKeys are the `other` keys OPS shows in its usage detail; used when the whole object is over 4 KB.
var billingKeys = []string{"model_ratio", "group_ratio", "completion_ratio", "cache_ratio", "model_price", "user_group_ratio",
	"matched_tier", "billing_source", "request_path", "cache_tokens", "cache_creation_tokens", "image_count", "frt"}

// extraOf is the log `other` as event `extra` (OPS shows it as the Gateway log detail), cut to the billing keys
// when the whole object is over the 4 KB limit. The margin covers OPS re-serialising it with different spacing.
func extraOf(other map[string]json.RawMessage) map[string]json.RawMessage {
	if len(other) == 0 {
		return nil
	}
	if raw, err := common.Marshal(other); err == nil && len(raw) <= maxExtra-256 {
		return other
	}
	subset := map[string]json.RawMessage{}
	for _, key := range billingKeys {
		if value, ok := other[key]; ok {
			subset[key] = value
		}
	}
	if raw, err := common.Marshal(subset); err != nil || len(subset) == 0 || len(raw) > maxExtra-256 {
		return nil
	}
	return subset
}
