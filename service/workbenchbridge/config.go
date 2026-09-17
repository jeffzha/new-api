package workbenchbridge

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	defaultControlTimeoutMS    = 3000
	minimumControlTimeoutMS    = 500
	maximumControlTimeoutMS    = 10000
	defaultInternalSkewSeconds = 60
	minimumInternalSkewSeconds = 5
	maximumInternalSkewSeconds = 300
	defaultControlServiceName  = "new-api-core"
)

var ErrInvalidConfiguration = errors.New("invalid workbench bridge configuration")

type Config struct {
	Enabled                  bool
	ControlURL               string
	AllowInsecureControlHTTP bool
	ControlHMACSecret        []byte
	ControlServiceName       string
	ControlTimeout           time.Duration
	ServiceHMACSecret        []byte
	InternalRequestSkew      time.Duration
}

func ConfigFromEnvironment() Config {
	controlTimeoutMS := common.GetEnvOrDefault("WORKBENCH_CONTROL_TIMEOUT_MS", defaultControlTimeoutMS)
	if controlTimeoutMS < minimumControlTimeoutMS {
		controlTimeoutMS = minimumControlTimeoutMS
	}
	if controlTimeoutMS > maximumControlTimeoutMS {
		controlTimeoutMS = maximumControlTimeoutMS
	}

	skewSeconds := common.GetEnvOrDefault("WORKBENCH_INTERNAL_MAX_SKEW_SECONDS", defaultInternalSkewSeconds)
	if skewSeconds < minimumInternalSkewSeconds {
		skewSeconds = minimumInternalSkewSeconds
	}
	if skewSeconds > maximumInternalSkewSeconds {
		skewSeconds = maximumInternalSkewSeconds
	}

	controlHMACSecret := readSecretEnv("WORKBENCH_CONTROL_HMAC_SECRET", "/run/secrets/new_api_control_hmac")
	serviceHMACSecret := readSecretEnv("WORKBENCH_SERVICE_HMAC_SECRET", "/run/secrets/new_api_identity_hmac")

	return Config{
		Enabled:                  common.GetEnvOrDefaultBool("WORKBENCH_ENABLED", false),
		ControlURL:               strings.TrimRight(strings.TrimSpace(os.Getenv("WORKBENCH_CONTROL_URL")), "/"),
		AllowInsecureControlHTTP: common.GetEnvOrDefaultBool("WORKBENCH_ALLOW_INSECURE_CONTROL_HTTP", false),
		ControlHMACSecret:        []byte(controlHMACSecret),
		ControlServiceName:       strings.TrimSpace(common.GetEnvOrDefaultString("WORKBENCH_CONTROL_SERVICE_NAME", defaultControlServiceName)),
		ControlTimeout:           time.Duration(controlTimeoutMS) * time.Millisecond,
		ServiceHMACSecret:        []byte(serviceHMACSecret),
		InternalRequestSkew:      time.Duration(skewSeconds) * time.Second,
	}
}

// readSecretEnv supports deployments that mount credentials as Docker secrets
// instead of placing usable HMAC material in the process environment. The
// environment variable remains authoritative when explicitly configured.
func readSecretEnv(envName string, secretPath string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	data, err := os.ReadFile(secretPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (config Config) Validate() error {
	if !config.Enabled {
		return nil
	}
	if err := config.ValidateTicketIssuer(); err != nil {
		return err
	}
	if err := config.ValidateInternalAuth(); err != nil {
		return err
	}
	if bytes.Equal(config.ControlHMACSecret, config.ServiceHMACSecret) {
		return ErrInvalidConfiguration
	}
	return nil
}

func (config Config) ValidateTicketIssuer() error {
	if !config.Enabled {
		return nil
	}
	controlURL, err := url.Parse(config.ControlURL)
	if err != nil || controlURL.Host == "" || controlURL.User != nil || controlURL.Path != "" || controlURL.RawPath != "" ||
		(controlURL.Scheme != "https" && !(config.AllowInsecureControlHTTP && controlURL.Scheme == "http")) ||
		controlURL.RawQuery != "" || controlURL.Fragment != "" {
		return ErrInvalidConfiguration
	}
	if len(config.ControlHMACSecret) < 32 || config.ControlServiceName == "" || config.ControlTimeout <= 0 {
		return ErrInvalidConfiguration
	}
	return nil
}

func (config Config) ValidateInternalAuth() error {
	if !config.Enabled {
		return nil
	}
	if len(config.ServiceHMACSecret) < 32 || config.InternalRequestSkew <= 0 {
		return ErrInvalidConfiguration
	}
	return nil
}
