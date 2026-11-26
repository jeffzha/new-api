package modelhealth

import (
	"context"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// Cycle limits share the admission/claim transaction across Gateway slots.
// Legacy daily/monthly budgets remain effective until a global cycle is saved.
type CycleBudgetInput struct {
	Selection
	Scope           string `json:"scope"`
	Override        bool   `json:"override"`
	Amount          int64  `json:"amount_micro"`
	Run             int64  `json:"run_micro"`
	IntervalSeconds int    `json:"interval_seconds"`
}

type cycleBudgetConfig struct {
	Override            bool  `json:"override"`
	Amount              int64 `json:"amount_micro"`
	Run                 int64 `json:"run_micro"`
	Anchor              int64 `json:"anchor"`
	IntervalSeconds     int   `json:"interval_seconds"`
	NextIntervalSeconds int   `json:"next_interval_seconds,omitempty"`
}

type CycleBudgetStatus struct {
	CycleBudgetInput
	Source              string   `json:"source"`
	Configured          bool     `json:"configured"`
	Start               int64    `json:"start"`
	End                 int64    `json:"end"`
	NextIntervalSeconds int      `json:"next_interval_seconds"`
	Used                int64    `json:"used_micro"`
	Reserved            int64    `json:"reserved_micro"`
	Remaining           int64    `json:"remaining_micro"`
	GlobalRemaining     int64    `json:"global_remaining_micro"`
	Recommended         int64    `json:"recommended_micro"`
	RecommendedRun      int64    `json:"recommended_run_micro"`
	EstimatedRound      int64    `json:"estimated_round_micro"`
	Targets             int      `json:"targets"`
	Missing             int      `json:"missing_templates"`
	OverRun             int      `json:"over_run"`
	Warnings            []string `json:"warnings"`
}

var budgetKinds = []string{"llm", "video", "image", "embedding", "audio", "tts", "speech2text", "rerank"}

func cycleBudgetKey(db *gorm.DB, scope string, s Selection) (string, string, error) {
	if scope == "global" {
		return "cycle-budget", "", nil
	}
	if scope == "model" {
		t, err := findTarget(db, s.Channel, s.Model, s.Endpoint)
		return "b-" + hash(t.ID, s.Endpoint)[:62], t.Type, err
	}
	if !slices.Contains(budgetKinds, scope) {
		return "", "", invalid("Unknown rule scope")
	}
	return "budget-" + scope, scope, nil
}

// A pending interval starts only at the existing boundary, never on save.
func cycleWindow(c cycleBudgetConfig, now int64) (int64, int64, int) {
	step := int64(c.IntervalSeconds)
	if step <= 0 {
		return 0, 0, 0
	}
	anchor := c.Anchor
	if c.NextIntervalSeconds > 0 && now >= anchor+step {
		anchor += step
		step = int64(c.NextIntervalSeconds)
	}
	start := anchor + max(int64(0), (now-anchor)/step)*step
	return start, start + step, int(step)
}

func SaveCycleBudget(ctx context.Context, p CycleBudgetInput) error {
	if p.Scope == "global" && !p.Override {
		return invalid("The overall cycle budget is required")
	}
	if p.Override && (p.Amount < 1 || p.Amount > 1000000000000 || p.Run < 1 || p.Run > 5000000 || p.Run > p.Amount || p.IntervalSeconds < daySeconds || p.IntervalSeconds > 7*daySeconds || p.IntervalSeconds%daySeconds != 0) {
		return invalid("Use a positive cycle budget and a per-detection limit up to CNY 5")
	}
	return model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		key, _, err := cycleBudgetKey(tx, p.Scope, p.Selection)
		if err != nil {
			return err
		}
		if p.Scope != "global" && p.Override {
			global, err := model.ReadModelHealthConfig(tx, "cycle-budget", cycleBudgetConfig{})
			if err != nil {
				return err
			}
			if !global.Override {
				return invalid("Save the overall cycle budget to activate these limits")
			}
		}
		c, err := model.ReadModelHealthConfig(tx, key, cycleBudgetConfig{})
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		if c.Anchor == 0 || c.IntervalSeconds <= 0 {
			c.Anchor, c.IntervalSeconds = dayStart(now), p.IntervalSeconds
		} else {
			c.Anchor, _, c.IntervalSeconds = cycleWindow(c, now)
		}
		c.NextIntervalSeconds = 0
		if p.Override && p.IntervalSeconds != c.IntervalSeconds {
			c.NextIntervalSeconds = p.IntervalSeconds
		}
		c.Override, c.Amount, c.Run = p.Override, p.Amount, p.Run
		return model.SaveModelHealthConfig(tx, key, "cycle-budget", c)
	})
}

func resolvedCycleBudget(db *gorm.DB, scope string, s Selection) (cycleBudgetConfig, cycleBudgetConfig, string, string, error) {
	key, kind, err := cycleBudgetKey(db, scope, s)
	if err != nil {
		return cycleBudgetConfig{}, cycleBudgetConfig{}, "", kind, err
	}
	own, err := model.ReadModelHealthConfig(db, key, cycleBudgetConfig{})
	if err != nil || own.Override {
		return own, own, scope, kind, err
	}
	if scope == "model" {
		inherited, err := model.ReadModelHealthConfig(db, "budget-"+kind, cycleBudgetConfig{})
		if err != nil || inherited.Override {
			return own, inherited, kind, kind, err
		}
	}
	global, err := model.ReadModelHealthConfig(db, "cycle-budget", cycleBudgetConfig{})
	return own, global, "global", kind, err
}

func budgetEndpointKind(endpoint string) string {
	switch endpoint {
	case string(constant.EndpointTypeOpenAIVideo):
		return "video"
	case string(constant.EndpointTypeImageGeneration):
		return "image"
	case string(constant.EndpointTypeEmbeddings):
		return "embedding"
	case string(constant.EndpointTypeJinaRerank):
		return "rerank"
	case "audio_speech":
		return "tts"
	case "audio_transcription":
		return "speech2text"
	case "audio_generation":
		return "audio"
	default:
		return "llm"
	}
}

func cycleSpend(db *gorm.DB, c cycleBudgetConfig, scope string, s Selection, now, exclude int64) (int64, int64, error) {
	start, _, _ := cycleWindow(c, now)
	q := db.Model(&model.ModelHealthRun{}).Where("created_at >= ? OR actual_micro IS NULL", start)
	if exclude > 0 {
		q = q.Where("id <> ?", exclude)
	}
	if scope == "model" {
		q = q.Where("target = ? AND endpoint = ?", hash(s.Channel, s.Model), s.Endpoint)
	}
	type total struct {
		Endpoint string
		Used     int64
		Reserved int64
	}
	var totals []total
	err := q.Select("endpoint, COALESCE(SUM(COALESCE(actual_micro, reservation_micro)), 0) AS used, COALESCE(SUM(CASE WHEN actual_micro IS NULL THEN reservation_micro ELSE 0 END), 0) AS reserved").Group("endpoint").Scan(&totals).Error
	var used, reserved int64
	for _, row := range totals {
		if scope != "global" && scope != "model" && budgetEndpointKind(row.Endpoint) != scope {
			continue
		}
		used += row.Used
		reserved += row.Reserved
	}
	return used, reserved, err
}

func cycleBudgetBlocks(db *gorm.DB, t Target, endpoint string, amount, now, exclude int64) ([]string, error) {
	selection := Selection{Channel: t.ChannelID, Model: t.Model, Endpoint: endpoint}
	_, c, source, _, err := resolvedCycleBudget(db, "model", selection)
	if err != nil {
		return nil, err
	}
	global, err := model.ReadModelHealthConfig(db, "cycle-budget", cycleBudgetConfig{})
	if err != nil || !global.Override {
		return nil, err
	}
	limits := []struct {
		config cycleBudgetConfig
		scope  string
	}{{global, "global"}}
	if source != "global" && c.Override {
		limits = append(limits, struct {
			config cycleBudgetConfig
			scope  string
		}{c, source})
	}
	for _, limit := range limits {
		used, _, err := cycleSpend(db, limit.config, limit.scope, selection, now, exclude)
		if err != nil {
			return nil, err
		}
		if amount > global.Run || amount > limit.config.Run {
			return []string{"Detection exceeds the per-detection budget"}, nil
		}
		if used > limit.config.Amount-amount {
			return []string{"Detection cycle budget is insufficient"}, nil
		}
	}
	return nil, nil
}

func GetCycleBudget(ctx context.Context, scope string, s Selection) (CycleBudgetStatus, error) {
	db := model.DB.WithContext(ctx)
	own, c, source, _, err := resolvedCycleBudget(db, scope, s)
	if err != nil {
		return CycleBudgetStatus{}, err
	}
	now := time.Now().Unix()
	status := CycleBudgetStatus{CycleBudgetInput: CycleBudgetInput{Selection: s, Scope: scope, Override: own.Override, Amount: c.Amount, Run: c.Run, IntervalSeconds: c.IntervalSeconds}, Source: source, Configured: c.Override, NextIntervalSeconds: c.NextIntervalSeconds, Warnings: []string{}}
	global, err := model.ReadModelHealthConfig(db, "cycle-budget", cycleBudgetConfig{})
	if err != nil {
		return status, err
	}
	if c.Override {
		status.Start, status.End, status.IntervalSeconds = cycleWindow(c, now)
		status.Used, status.Reserved, err = cycleSpend(db, c, source, s, now, 0)
		if err != nil {
			return status, err
		}
		status.Remaining = max(int64(0), c.Amount-status.Used)
	} else {
		status.IntervalSeconds, status.Amount, status.Run = 604800, 200000000, 5000000
		status.Warnings = append(status.Warnings, "Save the overall cycle budget to activate these limits")
	}
	if global.Override {
		used, _, err := cycleSpend(db, global, "global", Selection{}, now, 0)
		if err != nil {
			return status, err
		}
		status.GlobalRemaining = max(int64(0), global.Amount-used)
		if status.Configured && status.GlobalRemaining < status.Run {
			status.Warnings = append(status.Warnings, "Overall remaining budget is below the per-detection limit")
		}
	}
	rows, err := catalog(db, s.Channel)
	if err != nil {
		return status, err
	}
	for _, target := range rows {
		if target.Paused || scope == "model" && target.Model != s.Model || scope != "global" && scope != "model" && target.Type != scope {
			continue
		}
		for _, endpoint := range target.Endpoints {
			if scope == "model" && endpoint != s.Endpoint {
				continue
			}
			rule, _, err := effectiveRule(db, target, endpoint)
			if err != nil {
				return status, err
			}
			if !rule.Enabled && scope != "model" {
				continue
			}
			status.Targets++
			template, err := readTemplate(db, target, endpoint)
			if err != nil {
				return status, err
			}
			if !template.validFor(target, endpoint) {
				status.Missing++
				continue
			}
			count := max(1, (status.IntervalSeconds+rule.IntervalSeconds-1)/rule.IntervalSeconds) * rule.PerCycle
			status.EstimatedRound += template.MaxMicro * int64(count)
			status.RecommendedRun = max(status.RecommendedRun, template.MaxMicro)
			if template.MaxMicro > status.Run || global.Override && template.MaxMicro > global.Run {
				status.OverRun++
			}
		}
	}
	// Conservative reservation-based recommendation; it is never saved implicitly.
	status.Recommended = max(int64(10000), ((status.EstimatedRound*12/10+999999)/1000000)*1000000)
	status.RecommendedRun = min(int64(5000000), max(int64(10000), status.RecommendedRun))
	if scope == "global" {
		status.Recommended, status.RecommendedRun = 200000000, 5000000
	}
	if status.Configured && status.Remaining < status.EstimatedRound {
		status.Warnings = append(status.Warnings, "Remaining cycle budget may not cover all scheduled detections")
	}
	if status.Configured && status.Used >= status.Amount*8/10 {
		status.Warnings = append(status.Warnings, "At least 80 percent of the cycle budget is used or reserved")
	}
	if status.Missing > 0 {
		status.Warnings = append(status.Warnings, "Some models still require a validated probe template")
	}
	if status.OverRun > 0 {
		status.Warnings = append(status.Warnings, "Some detections exceed the per-detection limit")
	}
	return status, nil
}
