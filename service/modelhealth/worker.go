package modelhealth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/abema/go-mp4"
	"github.com/go-audio/wav"
	"gorm.io/gorm"
)

var rejectNetworks = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("240.0.0.0/4"),
}

func checkedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, invalid("An HTTPS address on port 443 is required")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicIP(ip) {
		return nil, invalid("Non-public addresses are not allowed")
	}
	return u, nil
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.Zone() == "" && ip.IsGlobalUnicast() && !ip.IsPrivate() && !slices.ContainsFunc(rejectNetworks, func(p netip.Prefix) bool { return p.Contains(ip) })
}

var probeClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 8 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil || port != "443" {
				return nil, errors.New("invalid destination")
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil || len(ips) == 0 || slices.ContainsFunc(ips, func(ip netip.Addr) bool { return !publicIP(ip) }) {
				return nil, errors.New("non-public destination")
			}
			dialer := net.Dialer{Timeout: 5 * time.Second}
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
			}
			return nil, errors.New("destination unreachable")
		},
	},
}

type Trace struct {
	At              int64             `json:"at"`
	Stage           string            `json:"stage"`
	Method          string            `json:"method"`
	Host            string            `json:"host,omitempty"`
	Path            string            `json:"path"`
	HTTP            int               `json:"http"`
	Protocol        string            `json:"protocol,omitempty"`
	WaitMs          *int64            `json:"wait_ms,omitempty"`
	DurationMs      *int64            `json:"duration_ms,omitempty"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	Request         any               `json:"request,omitempty"`
	Response        any               `json:"response,omitempty"`
}

func Start(ctx context.Context) {
	if !WorkerEnabled() {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		var lastSchedule, lastNotify, lastRetention int64 = -1, -1, -1
		for {
			if ctx.Err() != nil {
				return
			}
			now := time.Now().Unix()
			if now/15 != lastSchedule {
				lastSchedule = now / 15
				schedule(ctx, now)
			}
			if now/60 != lastNotify {
				lastNotify = now / 60
				notifyIncidents(ctx, now)
			}
			if common.IsMasterNode && dayStart(now) != lastRetention {
				if err := expireEvidence(model.DB.WithContext(ctx), now); err == nil {
					lastRetention = dayStart(now)
				}
			}
			if err := work(ctx, probeClient, now); err != nil && ctx.Err() == nil {
				common.SysError("model health worker could not persist a detection step")
			}
			if err := deliver(ctx, probeClient, time.Now().Unix()); err != nil && ctx.Err() == nil {
				common.SysError("model health notification state could not be persisted")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func expireEvidence(db *gorm.DB, now int64) error {
	var runs []int64
	if err := db.Model(&model.ModelHealthRun{}).Where("created_at < ? AND evidence <> ?", now-7*86400, "").Order("id").Limit(200).Pluck("id", &runs).Error; err != nil {
		return err
	}
	if len(runs) > 0 {
		if err := db.Model(&model.ModelHealthRun{}).Where("id IN ?", runs).Update("evidence", "").Error; err != nil {
			return err
		}
	}
	// Keep unreconciled spend and every charge in the current budget month.
	var old []int64
	if err := db.Model(&model.ModelHealthRun{}).Where("created_at < ? AND actual_micro IS NOT NULL AND state NOT IN ?", min(now-30*86400, monthStart(now)), activeStates).Order("id").Limit(200).Pluck("id", &old).Error; err != nil {
		return err
	}
	if len(old) > 0 {
		return db.Delete(&model.ModelHealthRun{}, old).Error
	}
	return nil
}

func schedule(ctx context.Context, now int64) {
	// The legacy all-channel job has its own billing and no per-model exclusion.
	// Do not run another automatic generator while it covers enabled channels.
	legacy := operation_setting.GetMonitorSetting()
	if legacy.AutoTestChannelEnabled && legacy.ChannelTestMode == operation_setting.ChannelTestModeScheduledAll {
		return
	}
	db := model.DB.WithContext(ctx)
	b, err := model.ReadModelHealthConfig(db, "budget", Budget{})
	if err != nil || b.Run == 0 {
		return
	}
	var rules int64
	if db.Model(&model.ModelHealthConfig{}).Where("kind = ?", "rule").Count(&rules).Error != nil || rules == 0 {
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
			if err != nil || !r.Enabled {
				continue
			}
			p, err := readTemplate(db, t, ep)
			if err != nil || !p.validFor(t, ep) {
				continue
			}
			var slot int64
			if paidMedia(t.Type) {
				due, err := mediaScheduledAt(db, t, ep, r, now)
				if err != nil || due > now {
					continue
				}
			} else {
				slot = nextScheduled(t, ep, r, now) - int64(r.IntervalSeconds/r.PerCycle)
				if now-slot >= 60 {
					continue
				}
			}
			_, _ = admit(ctx, Selection{Channel: t.ChannelID, Model: t.Model, Endpoint: ep}, "scheduled", "", 0, slot)
		}
	}
}

func claim(ctx context.Context, now int64) (model.ModelHealthRun, error) {
	var run model.ModelHealthRun
	err := model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Model(&model.ModelHealthRun{}).Where("state = ? AND lease_until <= ?", "submitting", now).Updates(map[string]any{"state": "uncertain", "reason": "submission_outcome_unknown", "finished_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Where("state = ? OR (state = ? AND next_poll <= ? AND lease_until <= ?)", "queued", "polling", now, now).Order("id").Take(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		fields := map[string]any{"lease_until": now + 90}
		if run.State == "queued" {
			b, err := model.ReadModelHealthConfig(tx, "budget", Budget{})
			if err != nil {
				return err
			}
			day, err := spend(tx, dayStart(now))
			if err != nil {
				return err
			}
			month, err := spend(tx, monthStart(now))
			if err != nil {
				return err
			}
			if run.ReservationMicro > b.Run || day > b.Day || month > b.Month || run.TokenBudget > b.ChannelTPM {
				if err := tx.Model(&run).Updates(map[string]any{"state": "skipped", "stage": "configuration", "reason": "budget_changed", "actual_micro": 0, "finished_at": now}).Error; err != nil {
					return err
				}
				run = model.ModelHealthRun{}
				return nil
			}
			var calls, tokens int64
			window := tx.Model(&model.ModelHealthRun{}).Where("channel_id = ? AND started_at > ?", run.ChannelID, now-60)
			if err := window.Count(&calls).Error; err != nil {
				return err
			}
			if err := window.Select("COALESCE(SUM(token_budget),0)").Scan(&tokens).Error; err != nil {
				return err
			}
			if calls >= int64(b.ChannelRPM) || tokens+int64(run.TokenBudget) > int64(b.ChannelTPM) {
				run = model.ModelHealthRun{}
				return nil
			}
			run.State, run.Stage, run.StartedAt = "submitting", "submission", now
			fields["state"], fields["stage"], fields["started_at"] = run.State, run.Stage, now
		}
		run.LeaseUntil = now + 90
		return tx.Model(&run).Updates(fields).Error
	})
	return run, err
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func work(ctx context.Context, client *http.Client, now int64) error {
	run, err := claim(ctx, now)
	if err != nil || run.ID == 0 {
		return err
	}
	return execute(ctx, client, run, now)
}

func execute(ctx context.Context, client *http.Client, run model.ModelHealthRun, now int64) error {
	db := model.DB.WithContext(ctx)
	var p Template
	if err := common.UnmarshalJsonStr(run.Template, &p); err != nil {
		return err
	}
	var trace []Trace
	_ = common.UnmarshalJsonStr(run.Evidence, &trace)
	update := func(state, stage, reason string, actual *int64) error {
		encoded, err := common.Marshal(trace)
		if err != nil {
			return err
		}
		fields := map[string]any{"state": state, "stage": stage, "reason": reason, "status_code": run.StatusCode, "lease_until": 0, "request_id": run.RequestID, "upstream_task_id": run.UpstreamTaskID, "evidence": string(encoded), "actual_micro": actual}
		if state == "polling" {
			fields["next_poll"] = time.Now().Unix() + 15
		} else {
			fields["finished_at"] = time.Now().Unix()
		}
		q := db.Model(&run).Where("lease_until = ? AND state = ?", run.LeaseUntil, run.State).Updates(fields)
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return errors.New("probe lease lost")
		}
		return nil
	}
	zero := int64(0)
	t, err := findTarget(db, run.ChannelID, run.ModelName, run.Endpoint)
	if err != nil && !IsValidation(err) {
		return err
	}
	key := probeCredential(p, t)
	if err != nil || !p.validFor(t, run.Endpoint) || key == "" || (t.Paused && run.State == "submitting") {
		if run.State == "submitting" {
			return update("skipped", "configuration", "target_or_credential_changed", &zero)
		}
		return update("uncertain", "polling", "target_or_credential_changed", nil)
	}
	if run.State == "polling" && now-run.StartedAt >= int64(p.TimeoutSeconds) {
		return update("uncertain", "polling", "generation_timeout", nil)
	}
	base, err := checkedURL(t.base)
	if err != nil || base.RawQuery != "" {
		return update("uncertain", run.Stage, "invalid_upstream_address", nil)
	}
	method, path := http.MethodPost, p.Path
	var payload []byte
	var reqBody any
	if run.State == "polling" {
		method, path = http.MethodGet, strings.ReplaceAll(p.PollPath, "{task_id}", run.UpstreamTaskID)
	} else {
		p.Body["model"] = run.UpstreamModel
		payload, err = common.Marshal(p.Body)
		if err != nil {
			return err
		}
		reqBody = p.Body
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	step := Trace{At: now, Stage: run.Stage, Method: method, Host: base.Hostname(), Path: p.Path, Request: redacted(reqBody)}
	if method == http.MethodGet {
		step.Path = path
	}
	stepCtx, cancel := context.WithTimeout(ctx, min(30*time.Second, time.Duration(p.TimeoutSeconds)*time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(stepCtx, method, base.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	switch p.Auth {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		req.Header.Set("x-goog-api-key", key)
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
	// Disable implicit transport replay for a paid submission.
	if method == http.MethodPost {
		req.GetBody = nil
	}
	step.RequestHeaders = evidenceHeaders(req.Header, req.ContentLength)
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		step.DurationMs = common.GetPointer(time.Since(started).Milliseconds())
		trace = appendTrace(trace, step)
		if method == http.MethodGet {
			return update("polling", "polling", "poll_transport_failure", nil)
		}
		return update("uncertain", "submission", "submission_transport_failure", nil)
	}
	defer resp.Body.Close()
	step.HTTP, run.StatusCode = resp.StatusCode, resp.StatusCode
	step.Protocol = resp.Proto
	step.WaitMs = common.GetPointer(time.Since(started).Milliseconds())
	step.ResponseHeaders = evidenceHeaders(resp.Header, resp.ContentLength)
	if method == http.MethodPost {
		for _, h := range []string{"x-request-id", "request-id", "x-tt-logid"} {
			v := resp.Header.Get(h)
			if identifier.MatchString(v) && !strings.Contains(v, key) {
				run.RequestID = v
				break
			}
		}
	}
	data, err := readBounded(resp.Body, 8<<20)
	step.DurationMs = common.GetPointer(time.Since(started).Milliseconds())
	var result any
	jsonOK := err == nil && common.Unmarshal(data, &result) == nil
	if jsonOK {
		step.Response = redacted(result)
	} else {
		step.Response = evidenceBody(data, err)
	}
	trace = appendTrace(trace, step)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := "upstream_http_error"
		if resp.StatusCode == 429 {
			reason = "upstream_rate_limit"
		}
		if method == http.MethodGet && (resp.StatusCode >= 500 || resp.StatusCode == 429) {
			return update("polling", "polling", reason, nil)
		}
		if method == http.MethodPost && (resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode < 400) {
			return update("uncertain", "submission", reason, nil)
		}
		return update("failed", run.Stage, reason, nil)
	}
	if method == http.MethodPost && p.PollPath != "" {
		id, _ := field(result, p.TaskField).(string)
		if !identifier.MatchString(id) || strings.Contains(id, key) || id == "." || id == ".." {
			return update("uncertain", "submission", "missing_task_id: "+p.TaskField, nil)
		}
		run.UpstreamTaskID = id
		return update("polling", "polling", "", nil)
	}
	if method == http.MethodGet {
		status, _ := field(result, p.StatusField).(string)
		switch {
		case slices.Contains(p.PendingValues, status):
			return update("polling", "polling", "", nil)
		case slices.Contains(p.FailureValues, status):
			return update("failed", "generation", "upstream_terminal_failure", actualCost(result, p))
		case !slices.Contains(p.SuccessValues, status):
			return update("uncertain", "polling", "unexpected_task_status: "+p.StatusField, nil)
		}
	}
	if t.Type == "video" && p.ResultField == "" && hasMP4Header(data) && step.ResponseHeaders["Content-Type"] != "video/mp4" {
		return update("failed", "validation", "video_download_content_type_mismatch", actualCost(result, p))
	}
	if err := verifyResult(stepCtx, client, t.Type, p, result, data, &trace); err != nil {
		return update("failed", "validation", err.Error(), actualCost(result, p))
	}
	return update("succeeded", "complete", "", actualCost(result, p))
}

func appendTrace(rows []Trace, t Trace) []Trace {
	if len(rows) >= 12 {
		rows = append(rows[:1], rows[2:]...)
	}
	return append(rows, t)
}

func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if int64(len(b)) > maxBytes {
		return nil, errors.New("response exceeds verification limit")
	}
	return b, e
}

func field(v any, path string) any {
	if path == "" {
		return nil
	}
	for part := range strings.SplitSeq(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[part]
		case []any:
			i, e := strconv.Atoi(part)
			if e != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func actualCost(result any, p Template) *int64 {
	if n, ok := field(result, p.CostField).(float64); ok && n >= 0 && n <= 1e12 && n == float64(int64(n)) {
		v := int64(n)
		return &v
	}
	return nil
}

func redacted(v any) any {
	value := scrub(v, 0)
	if b, err := common.Marshal(value); err != nil || len(b) > 2000 {
		return "[structured evidence exceeds storage limit]"
	}
	return value
}

var evidenceKeys = strings.Fields(`model id task_id request_id code message error type object created status state progress data metadata result content result_url video_url image_url audio_url url b64_json input messages role prompt negative_prompt stream duration seconds width height resolution size aspect_ratio ratio generate_audio n max_tokens max_completion_tokens temperature top_p usage prompt_tokens completion_tokens total_tokens input_tokens output_tokens choices finish_reason index embedding results relevance_score documents query`)

func scrub(v any, depth int) any {
	if depth > 5 {
		return "[truncated]"
	}
	switch x := v.(type) {
	case string:
		return "[string redacted]"
	case map[string]any:
		if len(x) > 20 {
			return "[large object omitted]"
		}
		m := map[string]any{}
		for k, value := range x {
			if slices.Contains(evidenceKeys, k) {
				if text, ok := value.(string); ok && safeEvidenceEnum(k, text) {
					m[k] = text
					continue
				}
				m[k] = scrub(value, depth+1)
			}
		}
		return m
	case []any:
		a := make([]any, min(4, len(x)))
		for i := range a {
			a[i] = scrub(x[i], depth+1)
		}
		return a
	default:
		return v
	}
}

func safeEvidenceEnum(key, value string) bool {
	switch key {
	case "status", "state":
		return slices.Contains([]string{"SUCCESS", "succeeded", "success", "completed", "done", "queued", "pending", "processing", "running", "FAILED", "failed", "error", "cancelled"}, value)
	case "resolution", "size":
		return slices.Contains([]string{"480p", "720p", "1080p", "1440p", "2k", "2K", "4k", "4K"}, value)
	case "aspect_ratio", "ratio":
		return slices.Contains([]string{"16:9", "9:16", "1:1", "4:3", "3:4", "adaptive"}, value)
	}
	return false
}

func verifyResult(ctx context.Context, client *http.Client, kind string, p Template, result any, data []byte, trace *[]Trace) error {
	value := field(result, p.ResultField)
	switch kind {
	case "llm", "speech2text":
		if p.ResultField == "" {
			value = field(result, "choices.0.message.content")
		}
		if text, ok := value.(string); !ok || strings.TrimSpace(text) == "" {
			return errors.New("missing generated text: " + p.ResultField)
		}
	case "embedding":
		if p.ResultField == "" {
			value = field(result, "data.0.embedding")
		}
		vec, ok := value.([]any)
		if !ok || len(vec) != p.Dimension {
			return errors.New("unexpected embedding dimension")
		}
		for _, x := range vec {
			if _, ok := x.(float64); !ok {
				return errors.New("invalid embedding value")
			}
		}
	case "rerank":
		if p.ResultField == "" {
			value = field(result, "results")
		}
		items, ok := value.([]any)
		if !ok || len(items) == 0 {
			return errors.New("missing ranking results")
		}
		docs, _ := p.Body["documents"].([]any)
		seen := map[int]bool{}
		for _, x := range items {
			i, ok := field(x, "index").(float64)
			score, valid := field(x, "relevance_score").(float64)
			if !ok || !valid || score != score || i < 0 || i >= float64(len(docs)) || float64(int(i)) != i || seen[int(i)] {
				return errors.New("invalid ranking result")
			}
			seen[int(i)] = true
		}
	case "video", "image", "audio", "tts":
		if p.ResultField != "" {
			text, ok := value.(string)
			if !ok || text == "" {
				return errors.New("missing result: " + p.ResultField)
			}
			if strings.HasPrefix(text, "https://") {
				u, err := checkedURL(text)
				if err != nil {
					return errors.New("result destination blocked")
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
				if err != nil {
					return errors.New("invalid result URL")
				}
				step := Trace{At: time.Now().Unix(), Stage: "download", Method: req.Method, Host: u.Hostname(), Path: "/[redacted-media-path]", RequestHeaders: evidenceHeaders(req.Header, req.ContentLength)}
				started := time.Now()
				res, err := client.Do(req)
				if err != nil {
					step.DurationMs = common.GetPointer(time.Since(started).Milliseconds())
					*trace = appendTrace(*trace, step)
					return errors.New("result download failed")
				}
				defer res.Body.Close()
				step.HTTP, step.Protocol = res.StatusCode, res.Proto
				step.WaitMs = common.GetPointer(time.Since(started).Milliseconds())
				step.ResponseHeaders = evidenceHeaders(res.Header, res.ContentLength)
				data, err = readBounded(res.Body, 24<<20)
				step.DurationMs = common.GetPointer(time.Since(started).Milliseconds())
				step.Response = evidenceBody(data, err)
				*trace = appendTrace(*trace, step)
				if (res.StatusCode == 200 || res.StatusCode == 206) && kind == "video" && step.ResponseHeaders["Content-Type"] != "video/mp4" && hasMP4Header(data) {
					return errors.New("video_download_content_type_mismatch")
				}
				if res.StatusCode != 200 {
					return errors.New("result download returned non-200")
				}
				if err != nil {
					return errors.New("unable to read complete result")
				}
			} else if kind == "image" {
				var err error
				data, err = base64.StdEncoding.DecodeString(text)
				if err != nil {
					return errors.New("invalid image encoding")
				}
			} else {
				return errors.New("invalid media URL")
			}
		}
		switch kind {
		case "image":
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
				return errors.New("invalid image dimensions")
			}
			if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
				return errors.New("invalid image bytes")
			}
		case "video":
			if !validProbeMP4(data) {
				return errors.New("invalid or unsupported video container")
			}
		default:
			r := bytes.NewReader(data)
			decoder := wav.NewDecoder(r)
			if !decoder.IsValidFile() || decoder.WavAudioFormat != 1 || decoder.FwdToPCM() != nil || decoder.Err() != nil || decoder.PCMSize <= 0 || decoder.PCMSize > r.Len() {
				return errors.New("invalid or unsupported audio result")
			}
		}
	default:
		return errors.New("unsupported probe modality")
	}
	return nil
}

func validProbeMP4(data []byte) bool {
	// Inspect bounded boxes without expanding upstream-controlled sample counts.
	moov, trak := mp4.BoxTypeMoov(), mp4.BoxTypeTrak()
	stbl := mp4.BoxPath{moov, trak, mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl()}
	media, movie, codec, samples := mp4.BoxTypeMdat(), mp4.BoxTypeMvhd(), mp4.BoxTypeAvc1(), mp4.BoxTypeStsz()
	paths := []mp4.BoxPath{{media}, {moov, movie}, append(slices.Clone(stbl), mp4.BoxTypeStsd(), codec), append(slices.Clone(stbl), samples)}
	boxes, err := mp4.ExtractBoxes(bytes.NewReader(data), nil, paths)
	if err != nil {
		return false
	}
	seen := [4]bool{}
	for _, box := range boxes {
		if box.Offset > uint64(len(data)) || box.Size > uint64(len(data))-box.Offset || box.HeaderSize > box.Size {
			return false
		}
		payload := data[box.Offset+box.HeaderSize : box.Offset+box.Size]
		switch box.Type {
		case media:
			seen[0] = seen[0] || len(payload) > 0
		case movie:
			if len(payload) <= 128 {
				var header mp4.Mvhd
				if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &header, box.Context); err == nil {
					seen[1] = seen[1] || header.Timescale > 0 && header.GetDuration() > 0
				}
			}
		case codec:
			seen[2] = seen[2] || len(payload) >= 78 && binary.BigEndian.Uint16(payload[24:]) > 0 && binary.BigEndian.Uint16(payload[26:]) > 0
		case samples:
			seen[3] = seen[3] || len(payload) >= 12 && binary.BigEndian.Uint32(payload[8:]) > 0
		}
	}
	return seen[0] && seen[1] && seen[2] && seen[3]
}
