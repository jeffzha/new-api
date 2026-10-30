// Package uac implements the project instance's confidential OIDC client.
// Product login settings remain owned by the existing authentication system.
package uac

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type Config struct {
	Enabled      bool
	Required     bool
	Issuer       string
	ClientID     string
	ClientSecret string
	PublicURL    string
	InitialRole  int
	CAFile       string
}

var Default *Client

func ConfigureFromEnv() error {
	if os.Getenv("UAC_OIDC_ENABLED") != "true" {
		if os.Getenv("UAC_OIDC_REQUIRED") == "true" {
			return errors.New("UAC_OIDC_REQUIRED requires UAC_OIDC_ENABLED=true")
		}
		Default = nil
		return nil
	}
	if !common.SessionCookieSecure {
		return errors.New("UAC requires SESSION_COOKIE_SECURE=true and its HTTPS trusted origin")
	}
	cfg := Config{Enabled: true, Required: os.Getenv("UAC_OIDC_REQUIRED") == "true",
		Issuer: strings.TrimSpace(os.Getenv("UAC_OIDC_ISSUER")), ClientID: strings.TrimSpace(os.Getenv("UAC_OIDC_CLIENT_ID")),
		ClientSecret: os.Getenv("UAC_OIDC_CLIENT_SECRET"), PublicURL: strings.TrimRight(os.Getenv("UAC_PUBLIC_URL"), "/"),
		InitialRole: 10, CAFile: os.Getenv("UAC_OIDC_CA_FILE")}
	if value := os.Getenv("UAC_INITIAL_ROLE"); value != "" {
		role, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("invalid UAC_INITIAL_ROLE")
		}
		cfg.InitialRole = role
	}
	client, err := New(cfg)
	if err != nil {
		return err
	}
	public, _ := url.Parse(cfg.PublicURL)
	origin, err := common.NormalizeOrigin(public.Scheme + "://" + public.Host)
	if err != nil || !slices.Contains(common.SessionCookieTrustedURLs, origin) {
		return errors.New("UAC_PUBLIC_URL must match SESSION_COOKIE_TRUSTED_URL")
	}
	Default = client
	return nil
}

func New(cfg Config) (*Client, error) {
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("UAC issuer must be an exact HTTPS URL")
	}
	public, err := url.Parse(cfg.PublicURL)
	if err != nil || public.Scheme != "https" || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" {
		return nil, errors.New("UAC_PUBLIC_URL must be an HTTPS URL")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || (cfg.InitialRole != 10 && cfg.InitialRole != 100) {
		return nil, errors.New("UAC client credentials and an administrator initial role are required")
	}
	if (!strings.HasPrefix(public.Path, "/") && public.Path != "") || strings.Contains(public.Path, "..") || strings.Contains(public.Path, "//") || public.RawPath != "" {
		return nil, errors.New("UAC_PUBLIC_URL contains an invalid instance path")
	}
	// Never inherit the relay's optional TLS_INSECURE_SKIP_VERIFY setting.
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("cannot load system CA certificates")
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("cannot load UAC CA certificate")
		}
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, ResponseHeaderTimeout: 10 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{Config: cfg, HTTP: client, flows: make(map[string]Flow), sessions: make(map[string]sessionEntry), handoffs: make(map[string]Handoff)}, nil
}

func Required() bool { return Default != nil && Default.Config.Required }
func Enabled() bool  { return Default != nil && Default.Config.Enabled }
func Prefix() string {
	if Default == nil {
		return ""
	}
	parsed, _ := url.Parse(Default.Config.PublicURL)
	return strings.TrimRight(parsed.Path, "/")
}
func RefreshCookie() string {
	if Enabled() {
		return "new_api_uac_refresh"
	}
	return "new_api_refresh"
}
func HintCookie() string {
	if Enabled() {
		return "new_api_uac_has_session"
	}
	return "new_api_has_session"
}
