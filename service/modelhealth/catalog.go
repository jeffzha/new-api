package modelhealth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/businessapi"
	"gorm.io/gorm"
)

type Target struct {
	ID          string   `json:"id"`
	ChannelID   int      `json:"channel_id"`
	Channel     string   `json:"channel"`
	Model       string   `json:"model"`
	Upstream    string   `json:"upstream"`
	Type        string   `json:"type"`
	Groups      []string `json:"groups"`
	Paused      bool     `json:"paused"`
	Endpoints   []string `json:"endpoints"`
	base        string
	channelType int
}

func hash(parts ...any) string {
	data, _ := common.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func targetFor(ch *model.Channel, name string) Target {
	mapping := map[string]string{}
	mappingValid := common.UnmarshalJsonStr(ch.GetModelMapping(), &mapping) == nil || strings.TrimSpace(ch.GetModelMapping()) == ""
	up := name
	seen := map[string]bool{}
	for mapping[up] != "" && mapping[up] != up {
		if seen[up] {
			mappingValid = false
			break
		}
		seen[up] = true
		up = mapping[up]
	}
	endpoints := common.GetEndpointTypesByChannelType(ch.Type, name)
	if constant.IsAdvancedCustomChannel(ch.Type) {
		var settings dto.ChannelOtherSettings
		if ch.OtherSettings != "" && common.UnmarshalJsonStr(ch.OtherSettings, &settings) != nil {
			mappingValid = false
		}
		config := settings.AdvancedCustom
		if ch.Type == constant.ChannelTypeVLLM || ch.Type == constant.ChannelTypeSGLang {
			config = common.GetAdvancedCustomPreset(ch.Type)
		}
		if config != nil {
			endpoints = config.SupportedEndpointTypesForModel(name)
		}
	}
	kind := businessapi.ModelType(up, endpoints)
	switch kind {
	case "video":
		endpoints = []constant.EndpointType{constant.EndpointTypeOpenAIVideo}
	case "image":
		endpoints = []constant.EndpointType{constant.EndpointTypeImageGeneration}
	case "embedding":
		endpoints = []constant.EndpointType{constant.EndpointTypeEmbeddings}
	case "rerank":
		endpoints = []constant.EndpointType{constant.EndpointTypeJinaRerank}
	case "tts":
		endpoints = []constant.EndpointType{"audio_speech"}
	case "speech2text":
		endpoints = []constant.EndpointType{"audio_transcription"}
	case "audio":
		endpoints = []constant.EndpointType{"audio_generation"}
	}
	t := Target{ID: hash(ch.Id, name), ChannelID: ch.Id, Channel: ch.Name, Model: name, Upstream: up, Type: kind, Groups: ch.GetGroups(), Paused: ch.Status != common.ChannelStatusEnabled || !mappingValid, base: ch.GetBaseURL()}
	t.channelType = ch.Type
	for _, endpoint := range endpoints {
		t.Endpoints = append(t.Endpoints, string(endpoint))
	}
	return t
}

func catalog(db *gorm.DB, channelID int) ([]Target, error) {
	q := db.Model(&model.Channel{}).Select("id", "type", "status", "name", "base_url", "models", "group", "model_mapping", "settings").Order("id")
	if channelID > 0 {
		q = q.Where("id = ?", channelID)
	}
	var channels []model.Channel
	if err := q.Find(&channels).Error; err != nil {
		return nil, err
	}
	rows := make([]Target, 0)
	for i := range channels {
		names := channels[i].GetModels()
		slices.Sort(names)
		for _, name := range slices.Compact(names) {
			if name = strings.TrimSpace(name); name != "" {
				rows = append(rows, targetFor(&channels[i], name))
			}
		}
	}
	return rows, nil
}

func findTarget(db *gorm.DB, channel int, name, endpoint string) (Target, error) {
	if channel < 1 {
		return Target{}, invalid("Invalid channel")
	}
	rows, err := catalog(db, channel)
	if err != nil {
		return Target{}, err
	}
	for _, t := range rows {
		if t.Model == name && slices.Contains(t.Endpoints, endpoint) {
			return t, nil
		}
	}
	return Target{}, invalid("Model or endpoint does not belong to this channel")
}

type Sample struct {
	Source     string `json:"source"`
	State      string `json:"state"`
	Stage      string `json:"stage"`
	Reason     string `json:"reason"`
	At         int64  `json:"at"`
	Duration   int64  `json:"duration_ms"`
	HTTP       int    `json:"status_code"`
	RequestID  string `json:"request_id"`
	KeyIndex   int    `json:"key_index"`
	RunID      int64  `json:"run_id"`
	EvidenceID int64  `json:"evidence_id,omitempty"`
}

type Endpoint struct {
	Name            string                `json:"name"`
	State           string                `json:"state"`
	Stale           bool                  `json:"stale"`
	Configured      bool                  `json:"configured"`
	TemplateState   string                `json:"template_state"`
	NextAt          int64                 `json:"next_at"`
	Run             *model.ModelHealthRun `json:"run,omitempty"`
	LastUser        int64                 `json:"last_user"`
	LastProbe       int64                 `json:"last_probe"`
	LastProbeSource string                `json:"last_probe_source"`
	UserCount       int                   `json:"user_count"`
	UserOK          int                   `json:"user_ok"`
	ProbeCount      int                   `json:"probe_count"`
	ProbeOK         int                   `json:"probe_ok"`
	Excluded        int                   `json:"excluded"`
	Recent          []Sample              `json:"recent"`
}

type Row struct {
	Target
	Details []Endpoint `json:"details"`
	State   string     `json:"state"`
}
type Filter struct {
	Search, Channel, Kind, Group, Endpoint, State string
	From, To                                      int64
	Page, Limit                                   int
}
type Overview struct {
	Rows   []Row          `json:"rows"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

func matches(t Target, f Filter) bool {
	return (f.Channel == "" || f.Channel == fmt.Sprint(t.ChannelID)) && (f.Group == "" || slices.Contains(t.Groups, f.Group)) && (f.Kind == "" || f.Kind == t.Type) && (f.Endpoint == "" || slices.Contains(t.Endpoints, f.Endpoint)) && strings.Contains(strings.ToLower(t.Channel+" "+t.Model+" "+t.Upstream), strings.ToLower(f.Search))
}

func eventFilter(db *gorm.DB, t Target, endpoint string) *gorm.DB {
	q := db.Model(&model.OpsRequestEvent{}).Where("channel_id = ? AND model_name = ? AND upstream_model_name = ?", t.ChannelID, t.Model, t.Upstream)
	var path string
	switch endpoint {
	case string(constant.EndpointTypeOpenAIVideo):
		return q.Where("endpoint_type = ?", "video_submit")
	case "audio_speech":
		path = "/v1/audio/speech"
	case "audio_transcription":
		path = "/v1/audio/transcriptions"
	default:
		if info, ok := common.GetDefaultEndpointInfo(constant.EndpointType(endpoint)); ok {
			path = info.Path
		}
	}
	if path == "" {
		return q.Where("1 = 0")
	}
	if endpoint == string(constant.EndpointTypeGemini) {
		return q.Where("path LIKE ? AND endpoint_type <> ?", "/v1beta/models/%:generateContent", "embedding")
	}
	return q.Where("path = ? AND method = ?", path, "POST")
}

func samples(db *gorm.DB, t Target, endpoint string, from, to int64) ([]Sample, error) {
	var events []model.OpsRequestEvent
	err := eventFilter(db, t, endpoint).Select("id", "request_id", "completed_at_ms", "duration_ms", "status_code", "upstream_status_code", "success", "error_owner", "business_limited", "error_code", "task_id", "user_id", "channel_key_index").Where("occurred_at_ms >= ? AND occurred_at_ms < ?", from*1000, to*1000).Order("occurred_at_ms desc, id desc").Limit(60).Find(&events).Error
	if err != nil {
		return nil, err
	}
	rows := make([]Sample, 0, len(events)+30)
	for _, e := range events {
		s := Sample{Source: "user", State: "failed", Stage: "request", At: e.CompletedAtMs / 1000, Duration: e.DurationMs, HTTP: e.StatusCode, KeyIndex: e.ChannelKeyIndex, EvidenceID: e.ID}
		if identifier.MatchString(e.RequestID) {
			s.RequestID = e.RequestID
		}
		if identifier.MatchString(e.ErrorCode) {
			s.Reason = e.ErrorCode
		}
		if e.UpstreamStatusCode != 0 {
			s.HTTP = e.UpstreamStatusCode
		}
		switch {
		case e.Success && (e.TaskID != "" || endpoint == string(constant.EndpointTypeOpenAIVideo)):
			s.State, s.Stage = "pending", "submission"
			if e.TaskID != "" {
				var task model.Task
				err := db.Select("status", "finish_time", "submit_time", "channel_id", "properties").Where("task_id = ? AND user_id = ? AND channel_id = ?", e.TaskID, e.UserID, t.ChannelID).Take(&task).Error
				if err != nil && err != gorm.ErrRecordNotFound {
					return nil, err
				}
				if err == nil && task.Properties.UpstreamModelName == t.Upstream && task.Properties.OriginModelName == t.Model && task.FinishTime > 0 && task.FinishTime < to {
					s.Stage, s.At, s.Duration = "generation", task.FinishTime, max(0, task.FinishTime-task.SubmitTime)*1000
					if task.Status == model.TaskStatusSuccess {
						s.State = "succeeded"
					}
					if task.Status == model.TaskStatusFailure {
						s.State, s.Reason = "failed", "task_terminal_failure"
					}
				}
			}
		case e.Success:
			s.State = "succeeded"
		case s.HTTP >= 500 && e.UpstreamStatusCode > 0, s.HTTP == 429 && e.UpstreamStatusCode == 429:
			s.Stage = "upstream"
		case e.BusinessLimited || e.UpstreamStatusCode == 0 && s.HTTP >= 400 && s.HTTP < 500 || s.HTTP == 400 || s.HTTP == 422:
			s.State = "excluded"
		}
		rows = append(rows, s)
	}
	var runs []model.ModelHealthRun
	if err := db.Select("id", "source", "state", "stage", "reason", "status_code", "created_at", "started_at", "finished_at", "request_id").Where("target = ? AND endpoint = ? AND upstream_model = ? AND created_at >= ? AND created_at < ?", t.ID, endpoint, t.Upstream, from, to).Order("id desc").Limit(30).Find(&runs).Error; err != nil {
		return nil, err
	}
	for _, r := range runs {
		rows = append(rows, Sample{Source: r.Source, State: r.State, Stage: r.Stage, Reason: r.Reason, At: max(r.FinishedAt, r.CreatedAt), Duration: max(0, r.FinishedAt-r.StartedAt) * 1000, HTTP: r.StatusCode, RunID: r.ID, RequestID: r.RequestID, KeyIndex: -1})
	}
	slices.SortStableFunc(rows, func(a, b Sample) int {
		if a.At > b.At {
			return -1
		}
		if a.At < b.At {
			return 1
		}
		return 0
	})
	return rows, nil
}

func summarize(name string, rows []Sample, r Rule, now int64) Endpoint {
	e := Endpoint{Name: name, State: "waiting", Recent: rows[:min(12, len(rows))]}
	latest := int64(0)
	streak := 0
	for _, s := range rows {
		switch s.State {
		case "excluded":
			e.Excluded++
			continue
		case "failed", "succeeded", "uncertain":
		default:
			continue
		}
		if s.Source == "user" {
			e.UserCount++
			if s.State == "succeeded" {
				e.UserOK++
				if e.LastUser == 0 {
					e.LastUser = s.At
				}
			}
		} else {
			e.ProbeCount++
			if s.State == "succeeded" {
				e.ProbeOK++
				if e.LastProbe == 0 {
					e.LastProbe, e.LastProbeSource = s.At, s.Source
				}
			}
		}
		if latest == 0 {
			latest = s.At
		}
		if s.State != "succeeded" && streak < r.RecoverAfter {
			e.State = "abnormal"
		}
		if e.State == "waiting" {
			e.State = "healthy"
			if s.Duration > r.SlowMs {
				e.State = "slow"
			}
		}
		streak++
	}
	e.Stale = latest != 0 && now-latest > int64(r.IntervalSeconds)*2
	return e
}

func List(ctx context.Context, f Filter) (Overview, error) {
	db := model.DB.WithContext(ctx)
	rows, err := catalog(db, 0)
	result := Overview{Rows: []Row{}, Counts: map[string]int{}}
	if err != nil {
		return result, err
	}
	now := time.Now().Unix()
	if f.To == 0 {
		f.To = now + 1
	}
	if f.From == 0 {
		f.From = f.To - 86400
	}
	if f.From < 1 || f.To > now+60 || f.From >= f.To || f.To-f.From > 30*86400 {
		return result, invalid("Select a time range of at most 30 days")
	}
	worker, err := readWorkerState(db)
	if err != nil {
		return result, err
	}
	limit := min(50, max(1, f.Limit))
	offset := (min(10000, max(1, f.Page)) - 1) * limit
	for _, t := range rows {
		if !matches(t, f) {
			continue
		}
		// Overview counters describe the displayed page; the catalog total is separate.
		if f.State == "" && (result.Total < offset || len(result.Rows) >= limit) {
			result.Total++
			continue
		}
		row, err := describe(db, t, f.Endpoint, f.From, f.To, now, worker)
		if err != nil {
			return result, err
		}
		if f.State != "" && row.State != f.State {
			continue
		}
		if result.Total >= offset && len(result.Rows) < limit {
			result.Rows = append(result.Rows, row)
			result.Counts[row.State]++
		}
		result.Total++
	}
	return result, nil
}

func describe(db *gorm.DB, t Target, endpoint string, from, to, now int64, worker workerState) (Row, error) {
	row := Row{Target: t, State: "waiting"}
	for _, name := range t.Endpoints {
		if endpoint != "" && name != endpoint {
			continue
		}
		r, _, err := effectiveRule(db, t, name)
		if err != nil {
			return row, err
		}
		history, err := samples(db, t, name, from, to)
		if err != nil {
			return row, err
		}
		e := summarize(name, history, r, now)
		template, err := readTemplate(db, t, name)
		if err != nil {
			return row, err
		}
		e.Configured = template.validFor(t, name)
		e.TemplateState = "missing"
		if e.Configured {
			e.TemplateState = "ready"
		} else if template.GeneratedProfile != "" {
			e.TemplateState = "draft"
		}
		if e.Configured && r.Enabled && !t.Paused && worker.active(now) && worker.hasProbe(template, t) {
			e.NextAt = nextScheduled(t, name, r, now)
			if paidMedia(t.Type) {
				e.NextAt, err = mediaScheduledAt(db, t, name, r, now)
				if err != nil {
					return row, err
				}
			}
		}
		var run model.ModelHealthRun
		q := db.Where("target = ? AND endpoint = ? AND upstream_model = ? AND state IN ?", t.ID, name, t.Upstream, activeStates).Order("id desc").Limit(1).Find(&run)
		if q.Error != nil {
			return row, q.Error
		}
		if q.RowsAffected > 0 {
			e.Run = &run
		}
		row.Details = append(row.Details, e)
		switch {
		case e.State == "abnormal":
			row.State = e.State
		case e.State == "slow" && row.State != "abnormal":
			row.State = e.State
		case e.State == "healthy" && row.State == "waiting":
			row.State = e.State
		}
	}
	return row, nil
}
