package modelhealth

import (
	"os"
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
	origin := os.Getenv("MODEL_HEALTH_GATEWAY_URL")
	u, err := checkedURL(origin)
	if err != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") || p.Auth != "bearer" {
		return ""
	}
	info, supported := common.GetDefaultEndpointInfo(constant.EndpointType(p.Endpoint))
	if !supported || p.Path != info.Path {
		return ""
	}
	if p.PollPath != "" && (p.Endpoint != "openai-video" || p.PollPath != "/v1/videos/{task_id}") {
		return ""
	}
	key := strings.TrimPrefix(probeCredential(p, t), "sk-")
	if key == "" || strings.ContainsAny(key, "- \r\n\t") {
		return ""
	}
	var token model.Token
	// GORM error logging must never render a query containing the usable key.
	if db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Where(clause.Eq{Column: "key", Value: key}).Take(&token).Error != nil || token.Status != common.TokenStatusEnabled ||
		!strings.HasPrefix(token.Name, "model-health/") || token.UnlimitedQuota || token.RemainQuota <= 0 ||
		token.ExpiredTime <= time.Now().Unix() || !token.ModelLimitsEnabled || !token.GetModelLimitsMap()[t.Model] ||
		token.CrossGroupRetry || token.Group == "auto" {
		return ""
	}
	var user model.User
	if db.Select("id", "role", "status").Where("id = ?", token.UserId).Take(&user).Error != nil ||
		user.Role != common.RoleAdminUser || user.Status != common.UserStatusEnabled {
		return ""
	}
	return "sk-" + key + "-" + strconv.Itoa(t.ChannelID)
}

func gatewayProbeBindings(db *gorm.DB) ([]string, error) {
	var configs []model.ModelHealthConfig
	if err := db.Where("kind = ?", "template").Find(&configs).Error; err != nil {
		return nil, err
	}
	var bindings []string
	invalidBindings := map[string]bool{}
	for _, config := range configs {
		var p Template
		if common.UnmarshalJsonStr(config.Value, &p) != nil {
			continue
		}
		t, err := findTarget(db, p.Channel, p.Model, p.Endpoint)
		if err != nil {
			continue
		}
		u, err := checkedURL(t.base)
		if err == nil {
			binding := hash(p.Credential, "https://"+u.Host)
			if !p.validFor(t, p.Endpoint) || gatewayProbeKey(db, p, t) == "" {
				invalidBindings[binding] = true
				continue
			}
			bindings = append(bindings, binding)
		}
	}
	valid := bindings[:0]
	for _, binding := range bindings {
		if !invalidBindings[binding] {
			valid = append(valid, binding)
		}
	}
	return valid, nil
}
