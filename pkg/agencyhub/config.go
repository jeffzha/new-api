package agencyhub

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                         string
	BasePath                     string
	PublicBaseURL                string
	CookieName                   string
	SessionIdle                  time.Duration
	SessionAbsolute              time.Duration
	LoginLockout                 time.Duration
	MaxLoginAttempts             int
	MinSpreadBPS                 int
	SalesCapBPS                  int
	CommissionEnabled            bool
	WithdrawalsEnabled           bool
	AutoMigrate                  bool
	InstanceID                   string
	SSOPublicKeyFile             string
	CommandServicePublicKeyFile  string
	CommandServicePrivateKeyFile string
	DeliveryKeyFile              string
	CommandRequireTLS            bool
	ExportDir                    string
	CursorSecret                 string
}

func LoadConfig() Config {
	instance := strings.TrimSpace(os.Getenv("AGENCY_HUB_INSTANCE_ID"))
	if instance == "" {
		host, _ := os.Hostname()
		instance = host + "-" + strconv.Itoa(os.Getpid())
	}
	return Config{
		Port:                         envString("AGENCY_HUB_PORT", "3201"),
		BasePath:                     normalizeBasePath(envString("AGENCY_HUB_BASE_PATH", "/agency")),
		PublicBaseURL:                strings.TrimRight(strings.TrimSpace(os.Getenv("AGENCY_HUB_PUBLIC_BASE_URL")), "/"),
		CookieName:                   envString("AGENCY_HUB_COOKIE_NAME", "agency_session"),
		SessionIdle:                  envDuration("AGENCY_HUB_SESSION_IDLE_SECONDS", 1800),
		SessionAbsolute:              envDuration("AGENCY_HUB_SESSION_ABSOLUTE_SECONDS", 28800),
		LoginLockout:                 envDuration("AGENCY_HUB_LOGIN_LOCKOUT_SECONDS", 900),
		MaxLoginAttempts:             envInt("AGENCY_HUB_MAX_LOGIN_ATTEMPTS", 5),
		MinSpreadBPS:                 envInt("AGENCY_HUB_MIN_SPREAD_BPS", 500),
		SalesCapBPS:                  envInt("AGENCY_HUB_SALES_CAP_BPS", 30000),
		CommissionEnabled:            envBool("AGENCY_COMMISSION_PROCESSING_ENABLED", false),
		WithdrawalsEnabled:           envBool("AGENCY_WITHDRAWALS_ENABLED", false),
		AutoMigrate:                  envBool("AGENCY_HUB_AUTO_MIGRATE", false),
		InstanceID:                   instance,
		SSOPublicKeyFile:             strings.TrimSpace(os.Getenv("AGENCY_HUB_SSO_PUBLIC_KEY_FILE")),
		CommandServicePublicKeyFile:  strings.TrimSpace(os.Getenv("AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE")),
		CommandServicePrivateKeyFile: strings.TrimSpace(os.Getenv("AGENCY_HUB_COMMAND_SERVICE_PRIVATE_KEY_FILE")),
		DeliveryKeyFile:              strings.TrimSpace(os.Getenv("AGENCY_HUB_DELIVERY_KEY_FILE")),
		CommandRequireTLS:            envBool("AGENCY_HUB_COMMAND_REQUIRE_TLS", true),
		ExportDir:                    strings.TrimSpace(os.Getenv("AGENCY_HUB_EXPORT_DIR")),
		CursorSecret:                 strings.TrimSpace(os.Getenv("AGENCY_HUB_CURSOR_SECRET")),
	}
}

func normalizeBasePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/agency"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	value = strings.TrimRight(value, "/")
	if value == "" {
		return "/agency"
	}
	return value
}
func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value == 0 {
		return fallback
	}
	return value
}
func envBool(name string, fallback bool) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return value
}
func envDuration(name string, fallback int) time.Duration {
	return time.Duration(envInt(name, fallback)) * time.Second
}
