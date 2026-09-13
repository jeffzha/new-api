package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
)

type AgencyCommandServerConfig struct {
	ListenAddress  string
	CertFile       string
	KeyFile        string
	ClientCAFile   string
	ClientIdentity string
}

type AgencyCommandServer struct {
	server   *http.Server
	listener net.Listener
	errors   chan error
}

func (s *AgencyCommandServer) Addr() net.Addr       { return s.listener.Addr() }
func (s *AgencyCommandServer) Errors() <-chan error { return s.errors }
func (s *AgencyCommandServer) Shutdown(ctx context.Context) error {
	if err := s.server.Shutdown(ctx); err != nil {
		return errors.Join(err, s.server.Close())
	}
	return nil
}

// StartConfiguredAgencyCommandServer fails closed on partial configuration.
// Leaving every transport setting empty disables this additional listener.
func StartConfiguredAgencyCommandServer() (*AgencyCommandServer, error) {
	cfg := AgencyCommandServerConfig{
		ListenAddress:  strings.TrimSpace(os.Getenv("AGENCY_COMMAND_LISTEN_ADDRESS")),
		CertFile:       strings.TrimSpace(os.Getenv("AGENCY_COMMAND_SERVER_CERT_FILE")),
		KeyFile:        strings.TrimSpace(os.Getenv("AGENCY_COMMAND_SERVER_KEY_FILE")),
		ClientCAFile:   strings.TrimSpace(os.Getenv("AGENCY_COMMAND_CLIENT_CA_FILE")),
		ClientIdentity: strings.TrimSpace(os.Getenv("AGENCY_COMMAND_CLIENT_IDENTITY")),
	}
	if cfg == (AgencyCommandServerConfig{}) {
		return nil, nil
	}
	if model.DB == nil {
		return nil, errors.New("agency command database is unavailable")
	}
	rootKey, err := loadAgencyCommandPublicKey()
	if err != nil {
		return nil, err
	}
	serviceKey, err := loadAgencyHubCommandServicePublicKey()
	if err != nil {
		return nil, err
	}
	app := agencyhub.New(model.DB, model.LOG_DB, agencyhub.Config{CommandRequireTLS: true})
	app.SetSSOPublicKey(rootKey)
	app.SetCommandServicePublicKey(serviceKey)
	return StartAgencyCommandServer(cfg, app.CommandRouter())
}

// StartAgencyCommandServer binds synchronously so certificate and address
// failures are returned before the public gateway reports successful startup.
// A CA-valid certificate still needs the explicitly pinned URI or DNS SAN.
func StartAgencyCommandServer(cfg AgencyCommandServerConfig, handler http.Handler) (*AgencyCommandServer, error) {
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid agency command listen address: %w", err)
	}
	ip := net.ParseIP(host)
	portNumber, portErr := strconv.Atoi(port)
	if ip == nil || ip.IsUnspecified() || (!ip.IsLoopback() && !ip.IsPrivate()) || portErr != nil || portNumber < 0 || portNumber > 65535 {
		return nil, errors.New("agency command listener requires a loopback or private literal IP and numeric port")
	}
	if handler == nil || cfg.CertFile == "" || cfg.KeyFile == "" || cfg.ClientCAFile == "" || strings.TrimSpace(cfg.ClientIdentity) == "" || strings.Contains(cfg.ClientIdentity, "*") {
		return nil, errors.New("agency command mTLS certificate, key, client CA and exact client identity are required")
	}
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load agency command server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("load agency command client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("agency command client CA contains no certificate")
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return errors.New("verified agency command client certificate required")
			}
			leaf := state.PeerCertificates[0]
			if strings.Contains(cfg.ClientIdentity, "://") {
				for _, uri := range leaf.URIs {
					if uri.String() == cfg.ClientIdentity {
						return nil
					}
				}
			} else {
				for _, name := range leaf.DNSNames {
					if name == cfg.ClientIdentity {
						return nil
					}
				}
			}
			return errors.New("agency command client identity is not authorized")
		},
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("bind agency command listener: %w", err)
	}
	s := &AgencyCommandServer{
		listener: listener,
		server:   &http.Server{Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10},
		errors:   make(chan error, 1),
	}
	go func() {
		defer close(s.errors)
		if err := s.server.Serve(tls.NewListener(listener, tlsConfig)); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errors <- err
		}
	}()
	return s, nil
}
