package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type agencyCommandTLSFixture struct {
	config       AgencyCommandServerConfig
	authority    *x509.Certificate
	authorityKey ed25519.PrivateKey
	roots        *x509.CertPool
}

func newAgencyCommandTLSFixture(t *testing.T) agencyCommandTLSFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "command-test-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	require.NoError(t, err)
	authority, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	authorityPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(authorityPEM))
	fixture := agencyCommandTLSFixture{authority: authority, authorityKey: privateKey, roots: roots}
	serverCertificate := fixture.certificate(t, &x509.Certificate{
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	serverKey, err := x509.MarshalPKCS8PrivateKey(serverCertificate.PrivateKey)
	require.NoError(t, err)
	// The invoking test command selects an E: TMP/TEMP directory; no fixed C: paths.
	directory := t.TempDir()
	fixture.config = AgencyCommandServerConfig{
		ListenAddress: "127.0.0.1:0",
		CertFile:      filepath.Join(directory, "server.pem"), KeyFile: filepath.Join(directory, "server-key.pem"),
		ClientCAFile:   filepath.Join(directory, "client-ca.pem"),
		ClientIdentity: "spiffe://nexight/agency-hub",
	}
	require.NoError(t, os.WriteFile(fixture.config.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertificate.Certificate[0]}), 0600))
	require.NoError(t, os.WriteFile(fixture.config.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKey}), 0600))
	require.NoError(t, os.WriteFile(fixture.config.ClientCAFile, authorityPEM, 0600))
	return fixture
}

func (fixture agencyCommandTLSFixture) certificate(t *testing.T, template *x509.Certificate) tls.Certificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	template.SerialNumber = serial.Add(serial, big.NewInt(1))
	template.NotBefore = fixture.authority.NotBefore
	template.NotAfter = fixture.authority.NotAfter
	template.KeyUsage = x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, template, fixture.authority, publicKey, fixture.authorityKey)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey}
}

func (fixture agencyCommandTLSFixture) client(t *testing.T, certificate *tls.Certificate, maxVersion uint16) *http.Client {
	t.Helper()
	tlsConfig := &tls.Config{RootCAs: fixture.roots, MinVersion: tls.VersionTLS12, MaxVersion: maxVersion}
	if certificate != nil {
		tlsConfig.Certificates = []tls.Certificate{*certificate}
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func startAgencyCommandTLSTestServer(t *testing.T, config AgencyCommandServerConfig, handler http.Handler) *AgencyCommandServer {
	t.Helper()
	server, err := StartAgencyCommandServer(config, handler)
	require.NoError(t, err)
	require.NotNil(t, server)
	require.NotNil(t, server.Addr())
	require.NotNil(t, server.Errors())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		assert.NoError(t, server.Shutdown(ctx))
	})
	return server
}

func TestAgencyCommandServerRejectsUnsafeListenerConfiguration(t *testing.T) {
	fixture := newAgencyCommandTLSFixture(t)
	for _, address := range []string{
		"", ":0", "0.0.0.0:0", "[::]:0", "8.8.8.8:0", "[2606:4700:4700::1111]:0",
		"localhost:0", "agency-hub.internal:0", "127.0.0.1", "127.0.0.1:invalid",
	} {
		t.Run(address, func(t *testing.T) {
			config := fixture.config
			config.ListenAddress = address
			server, err := StartAgencyCommandServer(config, http.NotFoundHandler())
			if server != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = server.Shutdown(ctx)
			}
			require.Error(t, err)
			assert.Nil(t, server)
			var networkError *net.OpError
			assert.False(t, errors.As(err, &networkError), "unsafe addresses must be rejected before attempting to listen")
		})
	}
}

func TestAgencyCommandServerRequiresCompleteTLSConfiguration(t *testing.T) {
	fixture := newAgencyCommandTLSFixture(t)
	cases := []struct {
		name   string
		change func(*AgencyCommandServerConfig)
	}{
		{"missing_certificate", func(c *AgencyCommandServerConfig) { c.CertFile = "" }},
		{"missing_key", func(c *AgencyCommandServerConfig) { c.KeyFile = "" }},
		{"missing_client_ca", func(c *AgencyCommandServerConfig) { c.ClientCAFile = "" }},
		{"missing_identity", func(c *AgencyCommandServerConfig) { c.ClientIdentity = "" }},
		{"wildcard_dns_identity", func(c *AgencyCommandServerConfig) { c.ClientIdentity = "*.internal" }},
		{"wildcard_uri_identity", func(c *AgencyCommandServerConfig) { c.ClientIdentity = "spiffe://nexight/*" }},
		{"unreadable_certificate", func(c *AgencyCommandServerConfig) { c.CertFile += ".missing" }},
		{"unreadable_key", func(c *AgencyCommandServerConfig) { c.KeyFile += ".missing" }},
		{"unreadable_client_ca", func(c *AgencyCommandServerConfig) { c.ClientCAFile += ".missing" }},
		{"invalid_client_ca", func(c *AgencyCommandServerConfig) { c.ClientCAFile = c.KeyFile }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := fixture.config
			tc.change(&config)
			server, err := StartAgencyCommandServer(config, http.NotFoundHandler())
			if server != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = server.Shutdown(ctx)
			}
			require.Error(t, err)
			assert.Nil(t, server)
		})
	}
}

func TestAgencyCommandServerAcceptsVerifiedExactClientIdentity(t *testing.T) {
	for _, identity := range []string{"spiffe://nexight/agency-hub", "agency-hub.internal"} {
		t.Run(identity, func(t *testing.T) {
			fixture := newAgencyCommandTLSFixture(t)
			fixture.config.ClientIdentity = identity
			template := &x509.Certificate{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
			if identity == "agency-hub.internal" {
				template.DNSNames = []string{identity}
			} else {
				uri, err := url.Parse(identity)
				require.NoError(t, err)
				template.URIs = []*url.URL{uri}
			}
			certificate := fixture.certificate(t, template)
			var calls atomic.Int64
			var verifiedClient atomic.Bool
			server := startAgencyCommandTLSTestServer(t, fixture.config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				verifiedClient.Store(r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && r.TLS.Version == tls.VersionTLS13)
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, "command accepted")
			}))
			client := fixture.client(t, &certificate, tls.VersionTLS13)
			response, err := client.Post("https://"+server.Addr().String()+"/commands", "application/json", nil)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, http.StatusAccepted, response.StatusCode)
			assert.Equal(t, "command accepted", string(body))
			assert.Equal(t, int64(1), calls.Load())
			assert.True(t, verifiedClient.Load(), "handler must receive a TLS 1.3 connection with a CA-verified client certificate")
			require.NotNil(t, response.TLS)
			assert.Equal(t, uint16(tls.VersionTLS13), response.TLS.Version)
		})
	}
}

func TestAgencyCommandServerRejectsUnauthenticatedTLSBeforeHandler(t *testing.T) {
	fixture := newAgencyCommandTLSFixture(t)
	untrusted := newAgencyCommandTLSFixture(t)
	expectedURI, err := url.Parse(fixture.config.ClientIdentity)
	require.NoError(t, err)
	wrongURI, err := url.Parse("spiffe://nexight/other-service")
	require.NoError(t, err)
	matching := fixture.certificate(t, &x509.Certificate{
		URIs: []*url.URL{expectedURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	wrongCA := untrusted.certificate(t, &x509.Certificate{
		URIs: []*url.URL{expectedURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	wrongIdentity := fixture.certificate(t, &x509.Certificate{
		URIs: []*url.URL{wrongURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	commonNameOnly := fixture.certificate(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: "agency-hub.internal"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	wildcardDNS := fixture.certificate(t, &x509.Certificate{
		DNSNames: []string{"*.internal"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	wrongDNS := fixture.certificate(t, &x509.Certificate{
		DNSNames: []string{"other-service.internal"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	uriInDNS := fixture.certificate(t, &x509.Certificate{
		DNSNames: []string{fixture.config.ClientIdentity}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	dnsInURI := fixture.certificate(t, &x509.Certificate{
		URIs: []*url.URL{{Path: "agency-hub.internal"}}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	serverOnly := fixture.certificate(t, &x509.Certificate{
		URIs: []*url.URL{expectedURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	cases := []struct {
		name        string
		identity    string
		certificate *tls.Certificate
		maxVersion  uint16
	}{
		{"missing_certificate", fixture.config.ClientIdentity, nil, tls.VersionTLS13},
		{"untrusted_ca", fixture.config.ClientIdentity, &wrongCA, tls.VersionTLS13},
		{"wrong_uri_san", fixture.config.ClientIdentity, &wrongIdentity, tls.VersionTLS13},
		{"common_name_is_not_dns_san", "agency-hub.internal", &commonNameOnly, tls.VersionTLS13},
		{"wildcard_san_is_not_exact_identity", "agency-hub.internal", &wildcardDNS, tls.VersionTLS13},
		{"wrong_dns_san", "agency-hub.internal", &wrongDNS, tls.VersionTLS13},
		{"uri_identity_requires_uri_san", fixture.config.ClientIdentity, &uriInDNS, tls.VersionTLS13},
		{"dns_identity_requires_dns_san", "agency-hub.internal", &dnsInURI, tls.VersionTLS13},
		{"server_certificate_is_not_client_certificate", fixture.config.ClientIdentity, &serverOnly, tls.VersionTLS13},
		{"tls_12_is_rejected", fixture.config.ClientIdentity, &matching, tls.VersionTLS12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := fixture.config
			config.ClientIdentity = tc.identity
			var calls atomic.Int64
			server := startAgencyCommandTLSTestServer(t, config, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusAccepted)
			}))
			client := fixture.client(t, tc.certificate, tc.maxVersion)
			response, err := client.Get("https://" + server.Addr().String() + "/commands")
			if response != nil {
				_ = response.Body.Close()
			}
			assert.Error(t, err, "unauthorized clients must fail TLS, not reach the HTTP command handler")
			assert.Zero(t, calls.Load())
		})
	}
}

func TestAgencyCommandServerRejectsPlaintextAndReleasesListenerOnShutdown(t *testing.T) {
	fixture := newAgencyCommandTLSFixture(t)
	var calls atomic.Int64
	server := startAgencyCommandTLSTestServer(t, fixture.config, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	address := server.Addr().String()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("http://" + address + "/commands")
	if response != nil {
		defer response.Body.Close()
		assert.GreaterOrEqual(t, response.StatusCode, http.StatusBadRequest)
	} else {
		require.Error(t, err)
	}
	assert.Zero(t, calls.Load(), "plaintext connections must never invoke the command handler")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err, "graceful shutdown must release the command socket")
	require.NoError(t, listener.Close())
	select {
	case serverError := <-server.Errors():
		assert.NoError(t, serverError, "normal shutdown must not be reported as a serve failure")
	default:
	}
}
