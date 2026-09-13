package agencyhub

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// InitializeCommandTransport validates production mTLS material before Hub
// readiness. An unavailable transport never falls back to direct SQL writes.
func (a *App) InitializeCommandTransport() error {
	cfg := a.config
	if cfg.CommandGatewayURL == "" {
		if cfg.CommandClientCAFile != "" || cfg.CommandClientCertFile != "" || cfg.CommandClientKeyFile != "" || cfg.CommandServerName != "" {
			return errors.New("agency command gateway URL is required with TLS configuration")
		}
		if cfg.CommandAllowLocalSQLite && (a.db == nil || a.db.Dialector.Name() != "sqlite") {
			return errors.New("local agency commands are supported only with explicit SQLite development configuration")
		}
		return nil
	}
	endpoint, err := url.Parse(cfg.CommandGatewayURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("agency command gateway URL must be an HTTPS origin without credentials, path, query or fragment")
	}
	if cfg.CommandClientCAFile == "" || cfg.CommandClientCertFile == "" || cfg.CommandClientKeyFile == "" {
		return errors.New("agency command client CA, certificate and private key are required")
	}
	certificate, err := tls.LoadX509KeyPair(cfg.CommandClientCertFile, cfg.CommandClientKeyFile)
	if err != nil {
		return fmt.Errorf("load agency command client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CommandClientCAFile)
	if err != nil {
		return fmt.Errorf("load agency command server CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("agency command server CA contains no certificate")
	}
	a.commandClient = &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: cfg.CommandServerName}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("agency command redirects are forbidden")
		},
	}
	a.commandEndpoint = strings.TrimRight(cfg.CommandGatewayURL, "/")
	return nil
}

func (a *App) CloseCommandTransport() {
	if a.commandClient != nil {
		a.commandClient.CloseIdleConnections()
	}
}

// SubmitRootCommand always sends the unchanged signed envelope to the gateway.
// Retrying after an ambiguous network failure reuses command_id and root proof.
func (a *App) SubmitRootCommand(ctx context.Context, req AgencyCommandRequest) (gin.H, error) {
	if a.commandClient != nil {
		return a.sendRootCommand(ctx, http.MethodPost, req)
	}
	if a.config.CommandGatewayURL != "" || !a.config.CommandAllowLocalSQLite || a.db == nil || a.db.Dialector.Name() != "sqlite" {
		return nil, errors.New("agency command mTLS transport is not configured")
	}
	if err := a.verifyCommandServiceSignature(req, req.HubSignature); err != nil {
		return nil, err
	}
	var existing model.AgencyCommand
	if err := a.db.WithContext(ctx).Where("command_id = ?", req.CommandID).First(&existing).Error; err == nil {
		if !commandEnvelopeMatchesStored(req, existing, req.BodyHash) {
			return nil, errCommandConflict
		}
		return commandResponse(existing), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().Unix()
	bodyHash, err := validateCommandRequest(req, now)
	if err != nil {
		return nil, err
	}
	claims, err := a.verifyRootCommandProof(req, bodyHash, now)
	if err != nil {
		return nil, err
	}
	payload, err := CanonicalPayload(req.Payload)
	if err != nil {
		return nil, err
	}
	command := model.AgencyCommand{CommandID: req.CommandID, Action: req.Action, Actor: req.Actor, SourceSID: req.SourceSID, ObjectID: req.ObjectID,
		ExpectedVersion: req.ExpectedVersion, Payload: string(payload), BodyHash: bodyHash, IssuedAt: req.IssuedAt, ExpiresAt: req.ExpiresAt,
		HubSignature: req.HubSignature, RootProof: req.RootProof, RootProofJTI: claims.JTI, Status: CommandStatusQueued, CreatedAt: now, UpdatedAt: now}
	if err := a.persistAcceptedCommand(ctx, req, &command); err != nil {
		return nil, err
	}
	return commandResponse(command), nil
}

// QueryRootCommand uses the same authenticated envelope and identity scope;
// the command's original expiry does not remove its durable result.
func (a *App) QueryRootCommand(ctx context.Context, req AgencyCommandRequest) (gin.H, error) {
	if a.commandClient == nil {
		return nil, errors.New("agency command mTLS transport is not configured")
	}
	return a.sendRootCommand(ctx, http.MethodGet, req)
}

type CommandTransportError struct {
	Status int
	Code   string
}

func (e *CommandTransportError) Error() string {
	return fmt.Sprintf("agency command gateway rejected request: HTTP %d (%s)", e.Status, e.Code)
}

func (a *App) sendRootCommand(ctx context.Context, method string, req AgencyCommandRequest) (gin.H, error) {
	endpoint := a.commandEndpoint + "/internal/agency/v1/commands"
	var body io.Reader
	if method == http.MethodPost {
		encoded, err := common.Marshal(req)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	} else {
		endpoint += "/" + url.PathEscape(req.CommandID)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(CommandServiceSignatureHeader, req.HubSignature)
	response, err := a.commandClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("agency command gateway unavailable: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("agency command gateway response exceeds limit")
	}
	var envelope struct {
		Success bool      `json:"success"`
		Data    gin.H     `json:"data"`
		Error   *apiError `json:"error"`
	}
	if err := common.Unmarshal(raw, &envelope); err != nil {
		return nil, errors.New("invalid agency command gateway response")
	}
	expectedStatus := http.StatusAccepted
	if method == http.MethodGet {
		expectedStatus = http.StatusOK
	}
	if response.StatusCode != expectedStatus || !envelope.Success {
		code := "command_failed"
		if envelope.Error != nil {
			code = envelope.Error.Code
		}
		return nil, &CommandTransportError{Status: response.StatusCode, Code: code}
	}
	if envelope.Data["command_id"] != req.CommandID {
		return nil, errors.New("agency command gateway response identity mismatch")
	}
	return envelope.Data, nil
}
