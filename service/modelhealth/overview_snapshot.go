package modelhealth

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// An overview uses one request-scoped snapshot. Page size never changes chart
// totals, and adding models does not add a database round trip per endpoint.
type overviewContextKey struct{}
type overviewSnapshot struct {
	configs  map[string]string
	events   map[string][]model.OpsRequestEvent
	recent   map[string][]model.ModelHealthRun
	active   map[string]*model.ModelHealthRun
	last     map[string]model.ModelHealthRun
	outcomes map[string]Outcomes
	tasks    map[string][]model.Task
}

func overviewSnapshotFor(db *gorm.DB) *overviewSnapshot {
	s, _ := db.Statement.Context.Value(overviewContextKey{}).(*overviewSnapshot)
	return s
}
func overviewKey(t Target, endpoint string) string { return t.ID + "/" + t.Upstream + "/" + endpoint }
func overviewEventKey(channel int, name, upstream string) string {
	return hash(channel, name, upstream)
}

func healthConfig[T any](db *gorm.DB, key string, fallback T) (T, error) {
	if s := overviewSnapshotFor(db); s != nil {
		value, ok := s.configs[key]
		if !ok {
			return fallback, nil
		}
		err := common.UnmarshalJsonStr(value, &fallback)
		return fallback, err
	}
	return model.ReadModelHealthConfig(db, key, fallback)
}

func matchesEndpoint(e model.OpsRequestEvent, endpoint string) bool {
	if endpoint == string(constant.EndpointTypeOpenAIVideo) {
		return e.EndpointType == "video_submit"
	}
	if endpoint == string(constant.EndpointTypeGemini) {
		return strings.HasPrefix(e.Path, "/v1beta/models/") && strings.HasSuffix(e.Path, ":generateContent") && e.EndpointType != "embedding"
	}
	path := ""
	switch endpoint {
	case "audio_speech":
		path = "/v1/audio/speech"
	case "audio_transcription":
		path = "/v1/audio/transcriptions"
	default:
		if info, ok := common.GetDefaultEndpointInfo(constant.EndpointType(endpoint)); ok {
			path = info.Path
		}
	}
	return path != "" && e.Path == path && e.Method == "POST"
}

func (s *overviewSnapshot) eventsFor(t Target, endpoint string) []model.OpsRequestEvent {
	rows := make([]model.OpsRequestEvent, 0, 60)
	for _, event := range s.events[overviewEventKey(t.ChannelID, t.Model, t.Upstream)] {
		if matchesEndpoint(event, endpoint) {
			rows = append(rows, event)
			if len(rows) == 60 {
				break
			}
		}
	}
	return rows
}

func overviewTask(db *gorm.DB, id string, user, channel int, task *model.Task) error {
	if s := overviewSnapshotFor(db); s != nil {
		rows := s.tasks[id]
		if len(rows) == 1 && rows[0].UserId == user && rows[0].ChannelId == channel {
			*task = rows[0]
			return nil
		}
		return gorm.ErrRecordNotFound
	}
	return db.Select("status", "finish_time", "submit_time", "channel_id", "properties").Where("task_id = ? AND user_id = ? AND channel_id = ?", id, user, channel).Take(task).Error
}

func loadOverviewSnapshot(db *gorm.DB, targets []Target, f Filter) (*overviewSnapshot, error) {
	s := &overviewSnapshot{configs: map[string]string{}, events: map[string][]model.OpsRequestEvent{}, recent: map[string][]model.ModelHealthRun{}, active: map[string]*model.ModelHealthRun{}, last: map[string]model.ModelHealthRun{}, outcomes: map[string]Outcomes{}, tasks: map[string][]model.Task{}}
	var configs []model.ModelHealthConfig
	if err := db.Where("kind IN ?", []string{"rule", "template", "policy", "media-schedule"}).Find(&configs).Error; err != nil {
		return nil, err
	}
	for _, row := range configs {
		s.configs[row.ID] = row.Value
	}
	selected := map[string]Target{}
	var channels []int
	var ids []string
	for _, target := range targets {
		if matches(target, f) {
			selected[overviewEventKey(target.ChannelID, target.Model, target.Upstream)] = target
			channels = append(channels, target.ChannelID)
			ids = append(ids, target.ID)
		}
	}
	if len(ids) == 0 {
		return s, nil
	}
	// Window limits apply to each source/endpoint, never to the whole catalogue.
	eventColumns := "id, request_id, channel_id, model_name, upstream_model_name, path, method, endpoint_type, occurred_at_ms, completed_at_ms, duration_ms, status_code, upstream_status_code, success, business_limited, error_code, task_id, user_id, channel_key_index"
	ranked := db.Model(&model.OpsRequestEvent{}).Select(eventColumns+", ROW_NUMBER() OVER (PARTITION BY channel_id, model_name, upstream_model_name, path, method, endpoint_type ORDER BY occurred_at_ms DESC, id DESC) AS evidence_rank").Where("channel_id IN ? AND occurred_at_ms >= ? AND occurred_at_ms < ?", channels, f.From*1000, f.To*1000)
	var events []model.OpsRequestEvent
	if err := db.Table("(?) AS ranked_events", ranked).Where("evidence_rank <= 60").Order("occurred_at_ms DESC, id DESC").Find(&events).Error; err != nil {
		return nil, err
	}
	taskIDs := map[string]bool{}
	for _, e := range events {
		key := overviewEventKey(e.ChannelID, e.ModelName, e.UpstreamModelName)
		if _, ok := selected[key]; ok {
			s.events[key] = append(s.events[key], e)
			if e.TaskID != "" {
				taskIDs[e.TaskID] = true
			}
		}
	}
	var groups []struct {
		model.OpsRequestEvent
		N int
	}
	columns := "channel_id, model_name, upstream_model_name, path, method, endpoint_type, success, business_limited, status_code, upstream_status_code, task_id, user_id"
	if err := db.Model(&model.OpsRequestEvent{}).Select(columns+", COUNT(*) AS n").Where("channel_id IN ? AND occurred_at_ms >= ? AND occurred_at_ms < ?", channels, f.From*1000, f.To*1000).Group(columns).Scan(&groups).Error; err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.TaskID != "" {
			taskIDs[g.TaskID] = true
		}
	}
	var taskList []string
	for id := range taskIDs {
		taskList = append(taskList, id)
	}
	for start := 0; start < len(taskList); start += 100 {
		var tasks []model.Task
		if err := db.Select("task_id", "user_id", "channel_id", "status", "finish_time", "submit_time", "properties").Where("task_id IN ?", taskList[start:min(start+100, len(taskList))]).Find(&tasks).Error; err != nil {
			return nil, err
		}
		for _, task := range tasks {
			s.tasks[task.TaskID] = append(s.tasks[task.TaskID], task)
		}
	}
	for _, g := range groups {
		target, ok := selected[overviewEventKey(g.ChannelID, g.ModelName, g.UpstreamModelName)]
		if !ok {
			continue
		}
		for _, endpoint := range target.Endpoints {
			if !matchesEndpoint(g.OpsRequestEvent, endpoint) {
				continue
			}
			key := overviewKey(target, endpoint)
			total := s.outcomes[key]
			if g.Success && (g.TaskID != "" || endpoint == string(constant.EndpointTypeOpenAIVideo)) {
				tasks := s.tasks[g.TaskID]
				if len(tasks) == 1 {
					task := tasks[0]
					if task.UserId == g.UserID && task.ChannelId == target.ChannelID && task.Properties.OriginModelName == target.Model && task.Properties.UpstreamModelName == target.Upstream && task.FinishTime > 0 && task.FinishTime < f.To {
						if task.Status == model.TaskStatusSuccess {
							total.UserCount += g.N
							total.UserOK += g.N
						}
						if task.Status == model.TaskStatusFailure {
							total.UserCount += g.N
						}
					}
				}
			} else if g.Success {
				total.UserCount += g.N
				total.UserOK += g.N
			} else {
				code := g.StatusCode
				if g.UpstreamStatusCode != 0 {
					code = g.UpstreamStatusCode
				}
				upstreamFailure := code >= 500 && g.UpstreamStatusCode > 0 || code == 429 && g.UpstreamStatusCode == 429
				excluded := g.BusinessLimited || g.UpstreamStatusCode == 0 && code >= 400 && code < 500 || code == 400 || code == 422
				if upstreamFailure || !excluded {
					total.UserCount += g.N
				}
			}
			s.outcomes[key] = total
		}
	}
	var runs []model.ModelHealthRun
	rankedRuns := db.Model(&model.ModelHealthRun{}).Select("*, ROW_NUMBER() OVER (PARTITION BY target, endpoint ORDER BY id DESC) AS evidence_rank").Where("target IN ? AND state <> ?", ids, "skipped")
	if err := db.Table("(?) AS ranked_runs", rankedRuns).Where("evidence_rank = 1").Order("id DESC").Find(&runs).Error; err != nil {
		return nil, err
	}
	for _, run := range runs {
		s.last[run.Target+"/"+run.Endpoint] = run
	}
	// Historical queries need the last thirty within their selected interval.
	rankedRuns = db.Model(&model.ModelHealthRun{}).Select("*, ROW_NUMBER() OVER (PARTITION BY target, endpoint, upstream_model ORDER BY id DESC) AS evidence_rank").Where("target IN ? AND created_at >= ? AND created_at < ?", ids, f.From, f.To)
	runs = nil
	if err := db.Table("(?) AS ranked_runs", rankedRuns).Where("evidence_rank <= 30").Order("id DESC").Find(&runs).Error; err != nil {
		return nil, err
	}
	s.recent = map[string][]model.ModelHealthRun{}
	for _, run := range runs {
		key := run.Target + "/" + run.UpstreamModel + "/" + run.Endpoint
		s.recent[key] = append(s.recent[key], run)
	}
	runs = nil
	if err := db.Where("target IN ? AND state IN ?", ids, activeStates).Order("id DESC").Find(&runs).Error; err != nil {
		return nil, err
	}
	for _, run := range runs {
		key := run.Target + "/" + run.UpstreamModel + "/" + run.Endpoint
		if s.active[key] == nil {
			s.active[key] = &run
		}
	}
	var probes []struct {
		Target, Endpoint, UpstreamModel, State string
		N                                      int
	}
	if err := db.Model(&model.ModelHealthRun{}).Select("target, endpoint, upstream_model, state, COUNT(*) AS n").Where("target IN ? AND created_at >= ? AND created_at < ? AND state IN ?", ids, f.From, f.To, []string{"succeeded", "failed", "uncertain"}).Group("target, endpoint, upstream_model, state").Scan(&probes).Error; err != nil {
		return nil, err
	}
	for _, p := range probes {
		key := p.Target + "/" + p.UpstreamModel + "/" + p.Endpoint
		total := s.outcomes[key]
		total.ProbeCount += p.N
		if p.State == "succeeded" {
			total.ProbeOK += p.N
		}
		s.outcomes[key] = total
	}
	return s, nil
}
