package modelhealth

import (
	"context"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AutoTemplatePolicy contains only references. The dedicated key remains on the
// executor; its finite quota, expiry and service identity are never changed.
type AutoTemplatePolicy struct {
	Enabled    bool   `json:"enabled"`
	Credential string `json:"credential"`
	TokenID    int    `json:"token_id"`
	MaxMicro   int64  `json:"max_micro"`
	Profile    string `json:"profile,omitempty"`
}

const defaultTemplateProfile = "builtin-v4"

func SaveAutoTemplatePolicy(ctx context.Context, policy AutoTemplatePolicy) error {
	if policy.Profile != "" && policy.Profile != defaultTemplateProfile {
		return invalid("Unsupported automatic template profile")
	}
	if !credentialRef.MatchString(policy.Credential) || policy.TokenID < 1 || policy.MaxMicro < 1 || policy.MaxMicro > 100000000 {
		return invalid("Specify a dedicated platform token and a bounded detection budget")
	}
	db := model.DB.WithContext(ctx)
	if policy.Profile == "" {
		previous, err := model.ReadModelHealthConfig(db, "auto-templates", AutoTemplatePolicy{})
		if err != nil {
			return err
		}
		policy.Profile = previous.Profile
	}
	if policy.Enabled {
		if _, err := autoTemplateToken(db, policy.TokenID); err != nil {
			return err
		}
	}
	if err := model.SaveModelHealthConfig(db, "auto-templates", "policy", policy); err != nil {
		return err
	}
	return SyncAutoTemplates(ctx)
}

func GetAutoTemplatePolicy(ctx context.Context) (AutoTemplatePolicy, error) {
	return model.ReadModelHealthConfig(model.DB.WithContext(ctx), "auto-templates", AutoTemplatePolicy{})
}

func autoTemplateToken(db *gorm.DB, id int) (model.Token, error) {
	var token model.Token
	// No key is selected or logged while provisioning templates.
	if err := db.Select("id", "user_id", "name", "status", "expired_time", "remain_quota", "unlimited_quota", "model_limits_enabled", "model_limits", "group", "cross_group_retry").Take(&token, "id = ?", id).Error; err != nil {
		return token, err
	}
	var user model.User
	if err := db.Select("id", "role", "status").Take(&user, "id = ?", token.UserId).Error; err != nil {
		return token, err
	}
	if !strings.HasPrefix(token.Name, "model-health/") || token.Status != common.TokenStatusEnabled || token.ExpiredTime <= time.Now().Unix() || token.RemainQuota <= 0 || token.UnlimitedQuota || !token.ModelLimitsEnabled || token.CrossGroupRetry || token.Group == "auto" || user.Role != common.RoleAdminUser || user.Status != common.UserStatusEnabled {
		return token, invalid("An enabled dedicated monitoring token with finite expiry and quota is required")
	}
	return token, nil
}

// builtinTemplate is pure: reading a plan cannot write configuration or send a
// request. Unsupported model contracts receive an editable, unconfirmed draft.
func builtinTemplate(t Target, endpoint string, policy AutoTemplatePolicy) Template {
	p := Template{Selection: Selection{Channel: t.ChannelID, Model: t.Model, Endpoint: endpoint}, GeneratedProfile: defaultTemplateProfile, Credential: policy.Credential, Auth: "bearer", MaxMicro: policy.MaxMicro, MaxTokens: 128, TimeoutSeconds: 30}
	p.QuotaPerUnit = decimal.NewFromFloat(common.QuotaPerUnit).String()
	if info, ok := common.GetDefaultEndpointInfo(constant.EndpointType(endpoint)); ok {
		p.Path = info.Path
	}
	p.Body = map[string]any{}
	text := "What is 1+1? Reply with the single digit only."
	switch endpoint {
	case "openai":
		p.Body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}, "stream": false, "max_tokens": float64(128)}
		name := strings.ToLower(t.Upstream)
		if strings.HasPrefix(name, "glm-") || strings.HasPrefix(name, "deepseek-v4") {
			p.Body["reasoning_effort"] = "low"
		}
		if strings.HasPrefix(name, "gpt-5") || strings.HasPrefix(name, "o1") || strings.HasPrefix(name, "o3") || strings.HasPrefix(name, "o4") {
			delete(p.Body, "max_tokens")
			p.Body["max_completion_tokens"] = float64(128)
		}
		p.MinimumConfirmed = t.Type == "llm"
	case "anthropic":
		p.Body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}, "stream": false, "max_tokens": float64(128)}
		p.ResultField, p.MinimumConfirmed = "content.0.text", t.Type == "llm"
	case "openai-response":
		p.Body = map[string]any{"input": text, "stream": false, "max_output_tokens": float64(128)}
		p.ResultField, p.MinimumConfirmed = "output.0.content.0.text", t.Type == "llm"
	case "gemini":
		p.Path = strings.ReplaceAll(p.Path, "{model}", t.Model)
		p.Body = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}}, "generationConfig": map[string]any{"maxOutputTokens": float64(128)}}
		p.ResultField, p.MinimumConfirmed = "candidates.0.content.parts.0.text", t.Type == "llm"
	case "embeddings":
		p.Body = map[string]any{"input": []any{"hello"}}
		// Validate the native dimension without forcing optional provider parameters.
		switch t.Upstream {
		case "qwen3.7-text-embedding-flash", "text-embedding-v3", "text-embedding-v4":
			p.Dimension = 1024
		case "text-embedding-3-small", "text-embedding-ada-002":
			p.Dimension = 1536
		case "text-embedding-3-large":
			p.Dimension = 3072
		}
		p.MinimumConfirmed = p.Dimension > 0
		p.DraftReason = "Confirm the expected embedding dimension"
	case "jina-rerank":
		p.Body = map[string]any{"query": "hello", "documents": []any{"hello world", "goodbye"}, "top_n": float64(1)}
		p.MinimumConfirmed = t.Type == "rerank"
	case "image-generation":
		p.Body = map[string]any{"prompt": "A small red circle on a white background.", "n": float64(1), "size": "1024x1024"}
		p.ResultField, p.TimeoutSeconds = "data.0.url", 180
		p.DraftReason = "Confirm the minimum image size, response format and cost ceiling"
		confirmImageProfile(t, &p)
	case "openai-video":
		p.Path, p.PollPath = "/v1/videos", "/v1/videos/{task_id}"
		p.Body = map[string]any{"prompt": "A red ball resting on a white table.", "seconds": "4", "resolution": "720p"}
		p.TaskField, p.StatusField, p.ResultField = "id", "status", "metadata.url"
		p.SuccessValues, p.FailureValues, p.PendingValues = []string{"completed"}, []string{"failed", "cancelled"}, []string{"queued", "in_progress"}
		p.TimeoutSeconds = 600
		p.DraftReason = "Confirm the minimum video duration, resolution and cost ceiling"
		// Both native Seedance adaptors validate this exact 4s contract.
		// Other video providers retain drafts until their own contract is known.
		if t.channelType == constant.ChannelTypeOpenAISeedance || t.channelType == constant.ChannelTypeDoubaoVideo {
			resolution, tokens := "720p", int64(86400)
			if t.Model == seedancepricing.Seedance25Model || t.Model == seedancepricing.AimodelSeedance25Model {
				resolution, tokens = "480p", 38430
			}
			p.Body["resolution"] = resolution
			if price, ok := seedancepricing.GetUnitPriceCNY(t.Model, resolution, false); ok {
				p.Body["ratio"], p.Body["generate_audio"] = "16:9", false
				if t.channelType == constant.ChannelTypeDoubaoVideo {
					// The native adaptor reads these optional controls from metadata.
					p.Body["metadata"] = map[string]any{"ratio": "16:9", "generate_audio": false}
				}
				// Four seconds at 720p use 86400 estimated video tokens;
				// the 2.5 minimum at 480p uses 38430.
				// Reserve 125% and round upward to whole CNY; budget admission
				// still rejects this when the administrator's ceiling is lower.
				ceiling := price.Mul(decimal.NewFromInt(tokens)).Mul(decimal.RequireFromString("1.25")).Div(decimal.NewFromInt(1000000)).Ceil().Mul(decimal.NewFromInt(1000000))
				if ceiling.IsPositive() && ceiling.LessThanOrEqual(decimal.NewFromInt(100000000)) {
					p.MaxMicro = max(p.MaxMicro, ceiling.IntPart())
					p.ResultField, p.MinimumConfirmed = "", true
				}
			}
		}
		if !p.MinimumConfirmed {
			confirmVideoProfile(t, &p)
		}
	case "audio_speech":
		p.Path, p.Body = "/v1/audio/speech", map[string]any{"input": "Hello.", "voice": "alloy", "response_format": "wav"}
		p.DraftReason = "Confirm the supported voice, audio format and cost ceiling"
	case "audio_transcription":
		p.Path, p.ResultField = "/v1/audio/transcriptions", "text"
		p.Body = map[string]any{"response_format": "json"}
		p.DraftReason = "A supported public audio fixture and upload adapter are required"
		if t.channelType == constant.ChannelTypeAli && t.Upstream == "fun-asr-flash-2026-06-15" {
			p.InputFixture = "speech-hello-v1"
			p.TimeoutSeconds = 180
			confirmMediaPrice(t, &p, map[string]any{"seconds": float64(5)}, false, false)
		}
	case "audio_generation":
		p.Path = "/v1/chat/completions"
		p.Body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Say hello."}}, "modalities": []any{"text", "audio"}, "audio": map[string]any{"voice": "alloy", "format": "wav"}, "max_tokens": float64(128), "stream": false}
		p.DraftReason = "Confirm the model-specific audio request and response contract"
	}
	if p.MinimumConfirmed {
		p.DraftReason = ""
	} else if p.DraftReason == "" {
		p.DraftReason = "Confirm the model-specific minimum request and cost ceiling"
	}
	p.Binding = templateBinding(t, endpoint)
	return p
}

// SyncAutoTemplates is idempotent and never enables schedules or replaces an
// existing manual template. Generated profiles follow channel mapping changes.
// It runs in Gateway (with its cache invalidation
// configuration), not in the minimal executor.
func SyncAutoTemplates(ctx context.Context) error {
	db := model.DB.WithContext(ctx)
	policy, err := model.ReadModelHealthConfig(db, "auto-templates", AutoTemplatePolicy{})
	if err != nil || !policy.Enabled {
		return err
	}
	// Zero-traffic candidates must not publish new paid request contracts while
	// old HTTP writers/executors are still serving. The release activates this
	// profile through the existing policy API only after their safe drain.
	if os.Getenv("MODEL_HEALTH_TEMPLATE_PROFILE_GATE") == "true" && policy.Profile != defaultTemplateProfile {
		return nil
	}
	targets, err := catalog(db, 0)
	if err != nil {
		return err
	}
	var models []string
	var configs []model.ModelHealthConfig
	if err := db.Where("kind = ?", "template").Find(&configs).Error; err != nil {
		return err
	}
	existing := make(map[string]string, len(configs))
	for _, config := range configs {
		existing[config.ID] = config.Value
	}
	for _, t := range targets {
		if !t.Paused {
			models = append(models, t.Model)
		}
		for _, ep := range t.Endpoints {
			id := "p-" + hash(t.ID, ep)[:62]
			p := builtinTemplate(t, ep, policy)
			encoded, err := common.Marshal(p)
			if err != nil {
				return err
			}
			row := model.ModelHealthConfig{ID: id, Kind: "template", Value: string(encoded)}
			if previous, ok := existing[id]; ok {
				var saved Template
				if common.UnmarshalJsonStr(previous, &saved) != nil || saved.GeneratedProfile == "" || previous == row.Value {
					continue
				}
				// Preserve a manual edit racing with the background reconciliation.
				if err := db.Model(&model.ModelHealthConfig{}).Where("id = ? AND value = ?", id, previous).Update("value", row.Value).Error; err != nil {
					return err
				}
				continue
			}
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
	}
	slices.Sort(models)
	models = slices.Compact(models)
	return model.ExtendModelHealthTokenModels(ctx, policy.TokenID, models)
}

func StartAutoTemplates(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			cycle, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := syncProbeBudgetReceipts(cycle); err != nil && ctx.Err() == nil {
				common.SysError("model health budget receipt reconciliation failed")
			}
			if err := SyncAutoTemplates(cycle); err != nil && ctx.Err() == nil {
				common.SysError("model health automatic template synchronization failed")
			}
			if err := recoverProbeOpsReceipts(cycle); err != nil && ctx.Err() == nil {
				common.SysError("model health OPS receipt recovery deferred")
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
