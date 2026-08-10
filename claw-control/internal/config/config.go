package config

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                               string
	MetricsAddr                        string
	DBDriver                           string
	DBDSN                              string
	DBMaxOpenConns                     int
	DBMaxIdleConns                     int
	DBConnMaxLifetime                  time.Duration
	AdminToken                         string
	InternalHMACKeys                   map[string]string
	InternalHMACTimeSkew               time.Duration
	NewAPIServiceName                  string
	ADPServiceName                     string
	SSOTicketTTL                       time.Duration
	EntryTicketTTL                     time.Duration
	ControlSessionTTL                  time.Duration
	AdminSessionTTL                    time.Duration
	AppContextTTL                      time.Duration
	NewAPIIdentityStatusURL            string
	NewAPIAdminStatusURL               string
	NewAPIIdentitySecret               string
	IdentityStatusTimeout              time.Duration
	ADPSSORedirectPath                 string
	AdminRedirectPath                  string
	AdminAssetDir                      string
	Environment                        string
	ProviderVerificationTimeout        time.Duration
	PeriodReconcileInterval            time.Duration
	OutboxInterval                     time.Duration
	MaintenanceInterval                time.Duration
	EphemeralRetention                 time.Duration
	DeliveredOutboxRetention           time.Duration
	ADPRetentionURL                    string
	RetentionCoordinatorInterval       time.Duration
	RetentionCoordinatorTimeout        time.Duration
	RedisAddr                          string
	RedisPassword                      string
	RedisDB                            int
	RedisEventChannel                  string
	AllowUntrustedProviderVerification bool
	EvidenceRoot                       string
	EvidenceMasterKey                  []byte
	EvidenceMaxBytes                   int64
	EvidenceClamAVAddress              string
	EvidenceClamAVTimeout              time.Duration
	BillingImportEnabled               bool
	TencentBillingSecretID             string
	TencentBillingSecretKey            string
	TencentBillingPayerUIN             string
	BillingImportInterval              time.Duration
	BillingImportTimeout               time.Duration
	BillingImportPageSize              int
	BillingImportMaxPages              int
	BillingImportMaxRecords            int
	BillingImportMaxAttempts           int
	BillingImportLeaseDuration         time.Duration
	BillingImportMaxResponseBytes      int64
}

func Load() (Config, error) {
	evidenceKey, evidenceKeyErr := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(os.Getenv("CLAW_EVIDENCE_MASTER_KEY")))
	billingEnabledValue := strings.ToLower(strings.TrimSpace(os.Getenv("CLAW_TENCENT_BILLING_IMPORT_ENABLED")))
	if billingEnabledValue != "" && billingEnabledValue != "true" && billingEnabledValue != "false" {
		return Config{}, errors.New("CLAW_TENCENT_BILLING_IMPORT_ENABLED must be true or false")
	}
	cfg := Config{
		Addr:                               env("CLAW_CONTROL_ADDR", ":8090"),
		MetricsAddr:                        strings.TrimSpace(os.Getenv("CLAW_METRICS_ADDR")),
		DBDriver:                           strings.ToLower(env("CLAW_DB_DRIVER", "sqlite")),
		DBDSN:                              env("CLAW_DB_DSN", "file:claw-control.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"),
		DBMaxOpenConns:                     envInt("CLAW_DB_MAX_OPEN_CONNS", 20),
		DBMaxIdleConns:                     envInt("CLAW_DB_MAX_IDLE_CONNS", 5),
		DBConnMaxLifetime:                  envDuration("CLAW_DB_CONN_MAX_LIFETIME", 30*time.Minute),
		AdminToken:                         strings.TrimSpace(os.Getenv("CLAW_ADMIN_TOKEN")),
		InternalHMACKeys:                   parseServiceKeys(os.Getenv("CLAW_INTERNAL_HMAC_KEYS")),
		InternalHMACTimeSkew:               envDuration("CLAW_INTERNAL_HMAC_TIME_SKEW", 5*time.Minute),
		NewAPIServiceName:                  env("CLAW_NEW_API_SERVICE_NAME", "new-api-core"),
		ADPServiceName:                     env("CLAW_ADP_SERVICE_NAME", "adp-backend"),
		SSOTicketTTL:                       envDuration("CLAW_SSO_TICKET_TTL", time.Minute),
		EntryTicketTTL:                     envDuration("CLAW_ENTRY_TICKET_TTL", time.Minute),
		ControlSessionTTL:                  envDuration("CLAW_CONTROL_SESSION_TTL", 8*time.Hour),
		AdminSessionTTL:                    envDuration("CLAW_ADMIN_SESSION_TTL", 30*time.Minute),
		AppContextTTL:                      envDuration("CLAW_APP_CONTEXT_TTL", time.Minute),
		NewAPIIdentityStatusURL:            env("CLAW_NEW_API_IDENTITY_STATUS_URL", ""),
		NewAPIAdminStatusURL:               env("CLAW_NEW_API_ADMIN_IDENTITY_STATUS_URL", ""),
		NewAPIIdentitySecret:               strings.TrimSpace(os.Getenv("CLAW_NEW_API_IDENTITY_STATUS_HMAC_SECRET")),
		IdentityStatusTimeout:              envDuration("CLAW_NEW_API_IDENTITY_STATUS_TIMEOUT", 3*time.Second),
		ADPSSORedirectPath:                 env("CLAW_ADP_SSO_REDIRECT_PATH", "/workbench/auth/sso"),
		AdminRedirectPath:                  env("CLAW_ADMIN_REDIRECT_PATH", "/workbench/admin"),
		AdminAssetDir:                      env("CLAW_ADMIN_ASSET_DIR", "/app/admin-ui"),
		Environment:                        strings.TrimSpace(env("CLAW_ENVIRONMENT", "prod")),
		ProviderVerificationTimeout:        envDuration("CLAW_PROVIDER_VERIFICATION_TIMEOUT", 15*time.Second),
		PeriodReconcileInterval:            envDuration("CLAW_PERIOD_RECONCILE_INTERVAL", time.Minute),
		OutboxInterval:                     envDuration("CLAW_OUTBOX_INTERVAL", time.Second),
		MaintenanceInterval:                envDuration("CLAW_MAINTENANCE_INTERVAL", time.Hour),
		EphemeralRetention:                 envDuration("CLAW_EPHEMERAL_RETENTION", 24*time.Hour),
		DeliveredOutboxRetention:           envDuration("CLAW_DELIVERED_OUTBOX_RETENTION", 30*24*time.Hour),
		ADPRetentionURL:                    strings.TrimSpace(os.Getenv("CLAW_ADP_RETENTION_URL")),
		RetentionCoordinatorInterval:       envDuration("CLAW_RETENTION_COORDINATOR_INTERVAL", 0),
		RetentionCoordinatorTimeout:        envDuration("CLAW_RETENTION_COORDINATOR_TIMEOUT", 15*time.Second),
		RedisAddr:                          env("CLAW_REDIS_ADDR", ""),
		RedisPassword:                      strings.TrimSpace(os.Getenv("CLAW_REDIS_PASSWORD")),
		RedisDB:                            envInt("CLAW_REDIS_DB", 0),
		RedisEventChannel:                  env("CLAW_REDIS_EVENT_CHANNEL", "claw:control:events"),
		AllowUntrustedProviderVerification: strings.EqualFold(strings.TrimSpace(os.Getenv("CLAW_ALLOW_UNTRUSTED_PROVIDER_VERIFICATION")), "true"),
		EvidenceRoot:                       env("CLAW_EVIDENCE_ROOT", "/var/lib/claw-control/evidence"),
		EvidenceMasterKey:                  evidenceKey,
		EvidenceMaxBytes:                   int64(envInt("CLAW_EVIDENCE_MAX_BYTES", 10<<20)),
		EvidenceClamAVAddress:              env("CLAW_EVIDENCE_CLAMAV_ADDR", "127.0.0.1:3310"),
		EvidenceClamAVTimeout:              envDuration("CLAW_EVIDENCE_CLAMAV_TIMEOUT", 15*time.Second),
		BillingImportEnabled:               billingEnabledValue == "true",
		TencentBillingSecretID:             strings.TrimSpace(os.Getenv("CLAW_TENCENT_BILLING_SECRET_ID")),
		TencentBillingSecretKey:            strings.TrimSpace(os.Getenv("CLAW_TENCENT_BILLING_SECRET_KEY")),
		TencentBillingPayerUIN:             strings.TrimSpace(os.Getenv("CLAW_TENCENT_BILLING_PAYER_UIN")),
		BillingImportInterval:              envDuration("CLAW_TENCENT_BILLING_IMPORT_INTERVAL", 5*time.Second),
		BillingImportTimeout:               envDuration("CLAW_TENCENT_BILLING_TIMEOUT", 15*time.Second),
		BillingImportPageSize:              envInt("CLAW_TENCENT_BILLING_PAGE_SIZE", 300),
		BillingImportMaxPages:              envInt("CLAW_TENCENT_BILLING_MAX_PAGES", 100),
		BillingImportMaxRecords:            envInt("CLAW_TENCENT_BILLING_MAX_RECORDS", 30000),
		BillingImportMaxAttempts:           envInt("CLAW_TENCENT_BILLING_MAX_ATTEMPTS", 3),
		BillingImportLeaseDuration:         envDuration("CLAW_TENCENT_BILLING_LEASE_DURATION", 30*time.Minute),
		BillingImportMaxResponseBytes:      int64(envInt("CLAW_TENCENT_BILLING_MAX_RESPONSE_BYTES", 4<<20)),
	}
	if cfg.Environment == "prod" && cfg.AllowUntrustedProviderVerification {
		return Config{}, errors.New("CLAW_ALLOW_UNTRUSTED_PROVIDER_VERIFICATION is forbidden in production")
	}
	if len(cfg.AdminToken) < 32 || len(cfg.InternalHMACKeys) == 0 {
		return Config{}, errors.New("CLAW_ADMIN_TOKEN must be at least 32 characters and CLAW_INTERNAL_HMAC_KEYS is required")
	}
	if evidenceKeyErr != nil || len(cfg.EvidenceMasterKey) != 32 {
		return Config{}, errors.New("CLAW_EVIDENCE_MASTER_KEY must be standard base64 encoding of exactly 32 random bytes")
	}
	decodedEvidenceKey := string(cfg.EvidenceMasterKey)
	if cfg.EvidenceRoot == "" || cfg.EvidenceMaxBytes <= 0 || cfg.EvidenceMaxBytes > 100<<20 {
		return Config{}, errors.New("CLAW_EVIDENCE_ROOT is required and CLAW_EVIDENCE_MAX_BYTES must be between 1 and 104857600")
	}
	if cfg.BillingImportPageSize < 1 || cfg.BillingImportPageSize > 300 || cfg.BillingImportMaxPages < 1 || cfg.BillingImportMaxPages > 1000 ||
		cfg.BillingImportMaxRecords < cfg.BillingImportPageSize || cfg.BillingImportMaxRecords > 200000 ||
		cfg.BillingImportMaxAttempts < 1 || cfg.BillingImportMaxAttempts > 10 ||
		cfg.BillingImportTimeout <= 0 || cfg.BillingImportTimeout > 30*time.Second ||
		cfg.BillingImportLeaseDuration < time.Minute || cfg.BillingImportLeaseDuration > 2*time.Hour ||
		cfg.BillingImportLeaseDuration < cfg.EvidenceClamAVTimeout+30*time.Second ||
		cfg.BillingImportMaxResponseBytes < 1024 || cfg.BillingImportMaxResponseBytes > 16<<20 || cfg.BillingImportInterval < 0 {
		return Config{}, errors.New("Tencent Billing import safety limits are invalid")
	}
	if cfg.BillingImportEnabled {
		if len(cfg.TencentBillingSecretID) < 8 || len(cfg.TencentBillingSecretKey) < 16 || !decimalDigits(cfg.TencentBillingPayerUIN, 1, 32) {
			return Config{}, errors.New("enabled Tencent Billing import requires server-side SecretId, SecretKey, and a numeric PayerUin")
		}
		if cfg.BillingImportInterval <= 0 {
			return Config{}, errors.New("CLAW_TENCENT_BILLING_IMPORT_INTERVAL must be positive when importing is enabled")
		}
		for _, existing := range []string{cfg.AdminToken, cfg.NewAPIIdentitySecret, decodedEvidenceKey} {
			if cfg.TencentBillingSecretID == existing || cfg.TencentBillingSecretKey == existing {
				return Config{}, errors.New("Tencent Billing credentials must not reuse administrative, evidence, identity, or control secrets")
			}
		}
		for _, existing := range cfg.InternalHMACKeys {
			if cfg.TencentBillingSecretID == existing || cfg.TencentBillingSecretKey == existing {
				return Config{}, errors.New("Tencent Billing credentials must not reuse administrative, evidence, identity, or control secrets")
			}
		}
		if cfg.TencentBillingSecretID == cfg.TencentBillingSecretKey {
			return Config{}, errors.New("Tencent Billing SecretId and SecretKey must be distinct")
		}
	}
	if _, _, err := net.SplitHostPort(cfg.EvidenceClamAVAddress); err != nil || cfg.EvidenceClamAVTimeout <= 0 || cfg.EvidenceClamAVTimeout > time.Minute {
		return Config{}, errors.New("CLAW_EVIDENCE_CLAMAV_ADDR must include host:port and CLAW_EVIDENCE_CLAMAV_TIMEOUT must be positive and at most 1m")
	}
	if decodedEvidenceKey == cfg.AdminToken || decodedEvidenceKey == cfg.NewAPIIdentitySecret {
		return Config{}, errors.New("evidence master key must be independent from administrative and identity secrets")
	}
	seenServiceSecrets := make(map[string]string, len(cfg.InternalHMACKeys))
	for service, secret := range cfg.InternalHMACKeys {
		if len(service) > 48 || len(secret) < 32 || secret == cfg.AdminToken || secret == decodedEvidenceKey {
			return Config{}, errors.New("internal HMAC service names must be at most 48 characters and secrets must be distinct values of at least 32 characters")
		}
		if otherService, duplicate := seenServiceSecrets[secret]; duplicate {
			return Config{}, errors.New("internal HMAC service secrets must be pairwise distinct: " + otherService + " and " + service)
		}
		seenServiceSecrets[secret] = service
	}
	if _, ok := cfg.InternalHMACKeys[cfg.NewAPIServiceName]; !ok {
		return Config{}, errors.New("CLAW_NEW_API_SERVICE_NAME must have a key in CLAW_INTERNAL_HMAC_KEYS")
	}
	if _, ok := cfg.InternalHMACKeys[cfg.ADPServiceName]; !ok {
		return Config{}, errors.New("CLAW_ADP_SERVICE_NAME must have a key in CLAW_INTERNAL_HMAC_KEYS")
	}
	if cfg.InternalHMACTimeSkew <= 0 || cfg.SSOTicketTTL <= 0 || cfg.EntryTicketTTL <= 0 || cfg.ControlSessionTTL <= 0 || cfg.AdminSessionTTL <= 0 || cfg.AppContextTTL <= 0 || cfg.IdentityStatusTimeout <= 0 || cfg.ProviderVerificationTimeout <= 0 {
		return Config{}, errors.New("internal HMAC skew and all ticket/session/context timeouts must be positive")
	}
	if cfg.PeriodReconcileInterval < 0 || cfg.OutboxInterval < 0 || cfg.MaintenanceInterval < 0 || cfg.RetentionCoordinatorInterval < 0 || cfg.RetentionCoordinatorTimeout <= 0 || cfg.RetentionCoordinatorTimeout > time.Minute || cfg.EphemeralRetention <= 0 || cfg.DeliveredOutboxRetention <= 0 {
		return Config{}, errors.New("worker intervals must be non-negative and retention durations must be positive")
	}
	if cfg.RetentionCoordinatorInterval > 0 {
		retentionURL, err := url.Parse(cfg.ADPRetentionURL)
		if err != nil || retentionURL.Host == "" || retentionURL.User != nil || retentionURL.RawQuery != "" || retentionURL.Fragment != "" || retentionURL.Path != "/api/internal/workbench/retention/intents" ||
			(retentionURL.Scheme != "https" && !(cfg.Environment != "prod" && retentionURL.Scheme == "http")) {
			return Config{}, errors.New("CLAW_ADP_RETENTION_URL must be the exact HTTPS internal retention endpoint")
		}
	}
	if cfg.OutboxInterval > 0 && (cfg.RedisAddr == "" || cfg.RedisPassword == "" || cfg.RedisDB < 0) {
		return Config{}, errors.New("CLAW_REDIS_ADDR, CLAW_REDIS_PASSWORD, and a non-negative CLAW_REDIS_DB are required when the outbox worker is enabled")
	}
	if strings.ContainsAny(cfg.RedisEventChannel, " \t\r\n") || cfg.RedisEventChannel == "" || len(cfg.RedisEventChannel) > 128 {
		return Config{}, errors.New("CLAW_REDIS_EVENT_CHANNEL must be a non-empty channel name without whitespace")
	}
	identityURL, err := url.Parse(cfg.NewAPIIdentityStatusURL)
	if err != nil || identityURL.Host == "" || identityURL.User != nil || identityURL.RawQuery != "" || identityURL.Fragment != "" ||
		identityURL.Path != "/api/internal/workbench/identity-status" ||
		(identityURL.Scheme != "https" && !(cfg.Environment != "prod" && identityURL.Scheme == "http")) {
		return Config{}, errors.New("CLAW_NEW_API_IDENTITY_STATUS_URL must be HTTPS in production")
	}
	adminIdentityURL, err := url.Parse(cfg.NewAPIAdminStatusURL)
	if err != nil || adminIdentityURL.Host == "" || adminIdentityURL.User != nil || adminIdentityURL.RawQuery != "" || adminIdentityURL.Fragment != "" ||
		adminIdentityURL.Path != "/api/internal/workbench/admin-identity-status" ||
		(adminIdentityURL.Scheme != "https" && !(cfg.Environment != "prod" && adminIdentityURL.Scheme == "http")) {
		return Config{}, errors.New("CLAW_NEW_API_ADMIN_IDENTITY_STATUS_URL must be HTTPS in production")
	}
	if len(cfg.NewAPIIdentitySecret) < 32 {
		return Config{}, errors.New("CLAW_NEW_API_IDENTITY_STATUS_HMAC_SECRET must be at least 32 characters and distinct from control v2 service secrets")
	}
	for _, secret := range cfg.InternalHMACKeys {
		if secret == cfg.NewAPIIdentitySecret {
			return Config{}, errors.New("identity-status v1 secret must not reuse a control v2 service secret")
		}
	}
	if !validLocalRedirectPath(cfg.ADPSSORedirectPath) {
		return Config{}, errors.New("CLAW_ADP_SSO_REDIRECT_PATH must be an absolute local path")
	}
	if cfg.ADPSSORedirectPath != "/workbench/auth/sso" {
		return Config{}, errors.New("CLAW_ADP_SSO_REDIRECT_PATH must remain /workbench/auth/sso for the route-scoped browser binding")
	}
	if !validLocalRedirectPath(cfg.AdminRedirectPath) {
		return Config{}, errors.New("CLAW_ADMIN_REDIRECT_PATH must be an absolute local path")
	}
	if cfg.AdminAssetDir == "" {
		return Config{}, errors.New("CLAW_ADMIN_ASSET_DIR is required")
	}
	switch cfg.DBDriver {
	case "sqlite", "mysql", "postgres":
	default:
		return Config{}, errors.New("CLAW_DB_DRIVER must be sqlite, mysql, or postgres")
	}
	if cfg.DBDSN == "" || cfg.Environment == "" {
		return Config{}, errors.New("database DSN and environment are required")
	}
	if cfg.MetricsAddr != "" {
		_, port, err := net.SplitHostPort(cfg.MetricsAddr)
		parsedPort, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return Config{}, errors.New("CLAW_METRICS_ADDR must be empty (disabled) or a valid host:port")
		}
	}
	return cfg, nil
}

func validLocalRedirectPath(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "" && parsed.Host == "" && parsed.User == nil && parsed.Opaque == "" &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.RawPath == "" && parsed.Path == value &&
		strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.Contains(value, `\`) &&
		path.Clean(value) == value
}

func parseServiceKeys(value string) map[string]string {
	result := make(map[string]string)
	for _, pair := range strings.Split(value, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) != 2 {
			continue
		}
		service := strings.TrimSpace(parts[0])
		secret := strings.TrimSpace(parts[1])
		if service != "" && secret != "" {
			result[service] = secret
		}
	}
	return result
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return fallback
	}
	return duration
}

func decimalDigits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
