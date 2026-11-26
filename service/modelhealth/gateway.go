package modelhealth

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Gateway probes use the normal relay and its existing administrator token
// channel pin. Only the executor receives the key; HTTP slots keep using the
// existing hashed readiness contract, so enabling this needs no traffic cutover.
func gatewayProbeKey(db *gorm.DB, p Template, t Target) string {
	if !gatewayProbePath(p, t) {
		return ""
	}
	token := gatewayProbeToken(db, p, t)
	if token == nil || !token.GetModelLimitsMap()[t.Model] {
		return ""
	}
	return "sk-" + token.Key + "-" + strconv.Itoa(t.ChannelID)
}

func gatewayProbePath(p Template, t Target) bool {
	origin := os.Getenv("MODEL_HEALTH_GATEWAY_URL")
	u, err := checkedURL(origin)
	if err != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") || p.Auth != "bearer" {
		return false
	}
	info, supported := common.GetDefaultEndpointInfo(constant.EndpointType(p.Endpoint))
	if p.Endpoint == "openai-video" {
		info.Path, supported = "/v1/videos", true
	}
	if p.Endpoint == "audio_speech" {
		info.Path, supported = "/v1/audio/speech", true
	}
	if p.Endpoint == "audio_transcription" {
		info.Path, supported = "/v1/audio/transcriptions", true
	}
	if p.Endpoint == "gemini" {
		info.Path = strings.ReplaceAll(info.Path, "{model}", t.Model)
	}
	if !supported || p.Path != info.Path {
		return false
	}
	if p.PollPath != "" && (p.Endpoint != "openai-video" || p.PollPath != "/v1/videos/{task_id}") {
		return false
	}
	return true
}

// Read each dedicated credential once per executor heartbeat, regardless of
// how many models share it. The execution path always rechecks immediately.
func gatewayProbeToken(db *gorm.DB, p Template, t Target) *model.Token {
	key := strings.TrimPrefix(probeCredential(p, t), "sk-")
	if key == "" || strings.ContainsAny(key, "- \r\n\t") {
		return nil
	}
	var token model.Token
	// GORM error logging must never render a query containing the usable key.
	if db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Where(clause.Eq{Column: "key", Value: key}).Take(&token).Error != nil || token.Status != common.TokenStatusEnabled ||
		!strings.HasPrefix(token.Name, "model-health/") || token.UnlimitedQuota || token.RemainQuota <= 0 ||
		token.ExpiredTime <= time.Now().Unix() || !token.ModelLimitsEnabled ||
		token.CrossGroupRetry || token.Group == "auto" {
		return nil
	}
	var user model.User
	if db.Select("id", "role", "status").Where("id = ?", token.UserId).Take(&user).Error != nil ||
		user.Role != common.RoleAdminUser || user.Status != common.UserStatusEnabled {
		return nil
	}
	return &token
}

func gatewayProbeBindings(db *gorm.DB) ([]string, error) {
	rows, err := catalog(db, 0)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]Target, len(rows))
	for _, t := range rows {
		targets[t.ID] = t
	}
	var configs []model.ModelHealthConfig
	if err := db.Where("kind = ?", "template").Find(&configs).Error; err != nil {
		return nil, err
	}
	var bindings []string
	credentials := map[string]*model.Token{}
	for _, config := range configs {
		var p Template
		if common.UnmarshalJsonStr(config.Value, &p) != nil {
			continue
		}
		t, ok := targets[hash(p.Channel, p.Model)]
		if !ok || t.Paused || !slices.Contains(t.Endpoints, p.Endpoint) {
			continue
		}
		if !p.validFor(t, p.Endpoint) || !gatewayProbePath(p, t) {
			continue
		}
		token, loaded := credentials[p.Credential]
		if !loaded {
			token = gatewayProbeToken(db, p, t)
			credentials[p.Credential] = token
		}
		if token == nil || !token.GetModelLimitsMap()[t.Model] {
			continue
		}
		u, err := checkedURL(t.base)
		if err == nil {
			binding := probeTargetBinding(p, t, u.Host)
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}
