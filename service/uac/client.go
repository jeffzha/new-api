package uac

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var ErrAuthentication = errors.New("unified authentication failed")

type discovery struct {
	Issuer        string   `json:"issuer"`
	Authorization string   `json:"authorization_endpoint"`
	Token         string   `json:"token_endpoint"`
	UserInfo      string   `json:"userinfo_endpoint"`
	JWKS          string   `json:"jwks_uri"`
	Logout        string   `json:"end_session_endpoint"`
	AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
	PKCE          []string `json:"code_challenge_methods_supported"`
}

type Flow struct {
	Binding  string
	Verifier string
	Nonce    string
	Expires  time.Time
}

type Identity struct {
	Subject       string `json:"sub"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

type Tokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ID        string `json:"id_token"`
	Type      string `json:"token_type"`
	ExpiresIn int64  `json:"expires_in"`
}

// Sessions are deliberately process-local: no upstream credential is persisted
// in the product database. Restarting this single project instance requires login.
type Session struct {
	mu            sync.Mutex
	Identity      Identity
	Tokens        Tokens
	Nonce         string
	Expires       time.Time
	AccessExpires time.Time
	Checked       time.Time
}

type Client struct {
	Config   Config
	HTTP     *http.Client
	mu       sync.Mutex
	metadata *discovery
	keys     oidc.KeySet
	flows    map[string]Flow
	sessions map[string]sessionEntry
	handoffs map[string]Handoff
}

type sessionEntry struct {
	session *Session
	expires time.Time
}

func Random() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (c *Client) Discover(ctx context.Context) (*discovery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.metadata != nil {
		return c.metadata, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Config.Issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, ErrAuthentication
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("cannot read UAC discovery document; check HTTPS certificate and connectivity")
	}
	defer response.Body.Close()
	var data discovery
	if response.StatusCode != http.StatusOK || common.DecodeJson(io.LimitReader(response.Body, 1<<20), &data) != nil || data.Issuer != c.Config.Issuer {
		return nil, ErrAuthentication
	}
	issuer, _ := url.Parse(c.Config.Issuer)
	for _, endpoint := range []string{data.Authorization, data.Token, data.UserInfo, data.JWKS, data.Logout} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host != issuer.Host || parsed.User != nil || parsed.Fragment != "" {
			return nil, ErrAuthentication
		}
	}
	c.metadata = &data
	c.keys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), c.HTTP), data.JWKS)
	return c.metadata, nil
}

func (c *Client) Start(ctx context.Context) (authorization, binding string, err error) {
	metadata, err := c.Discover(ctx)
	if err != nil {
		return "", "", err
	}
	state, err := Random()
	if err != nil {
		return "", "", err
	}
	binding, err = Random()
	if err != nil {
		return "", "", err
	}
	nonce, err := Random()
	if err != nil {
		return "", "", err
	}
	flow := Flow{Binding: binding, Verifier: oauth2.GenerateVerifier(), Nonce: nonce, Expires: time.Now().Add(5 * time.Minute)}
	c.mu.Lock()
	for key, previous := range c.flows {
		if time.Now().After(previous.Expires) {
			delete(c.flows, key)
		}
	}
	if len(c.flows) >= 5000 {
		c.mu.Unlock()
		return "", "", ErrAuthentication
	}
	c.flows[state] = flow
	c.mu.Unlock()
	params := url.Values{"response_type": {"code"}, "client_id": {c.Config.ClientID}, "redirect_uri": {c.Config.PublicURL + "/api/oauth/uac/callback"}, "scope": {"openid profile email"}, "state": {state}, "nonce": {nonce}, "code_challenge": {oauth2.S256ChallengeFromVerifier(flow.Verifier)}, "code_challenge_method": {"S256"}}
	return metadata.Authorization + "?" + params.Encode(), binding, nil
}

func (c *Client) Complete(ctx context.Context, state, binding, code string) (*Session, error) {
	c.mu.Lock()
	flow, ok := c.flows[state]
	if ok && binding != "" && subtle.ConstantTimeCompare([]byte(flow.Binding), []byte(binding)) == 1 {
		delete(c.flows, state)
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok || !time.Now().Before(flow.Expires) || code == "" {
		return nil, ErrAuthentication
	}
	metadata, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	tokens, err := c.exchange(ctx, metadata.Token, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.Config.PublicURL + "/api/oauth/uac/callback"}, "code_verifier": {flow.Verifier}})
	if err != nil {
		return nil, err
	}
	verified, err := c.verifyID(ctx, tokens, flow.Nonce)
	if err != nil {
		return nil, ErrAuthentication
	}
	identity, err := c.userInfo(ctx, metadata.UserInfo, tokens.Access)
	if err != nil || identity.Subject != verified.Subject {
		return nil, ErrAuthentication
	}
	return &Session{Identity: identity, Tokens: tokens, Nonce: flow.Nonce, Expires: time.Now().Add(12 * time.Hour), AccessExpires: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second), Checked: time.Now()}, nil
}

func (c *Client) verifyID(ctx context.Context, tokens Tokens, nonce string) (*oidc.IDToken, error) {
	verified, err := oidc.NewVerifier(c.Config.Issuer, c.keys, &oidc.Config{ClientID: c.Config.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}).Verify(ctx, tokens.ID)
	if err != nil || verified.Nonce != nonce || verified.Subject == "" || verified.IssuedAt.IsZero() || verified.IssuedAt.After(time.Now().Add(30*time.Second)) || verified.AccessTokenHash == "" || verified.VerifyAccessToken(tokens.Access) != nil {
		return nil, ErrAuthentication
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if verified.Claims(&claims) != nil || (len(verified.Audience) > 1 && claims.AuthorizedParty != c.Config.ClientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != c.Config.ClientID) {
		return nil, ErrAuthentication
	}
	return verified, nil
}

func (c *Client) exchange(ctx context.Context, endpoint string, values url.Values) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return Tokens{}, ErrAuthentication
	}
	req.SetBasicAuth(url.QueryEscape(c.Config.ClientID), url.QueryEscape(c.Config.ClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return Tokens{}, ErrAuthentication
	}
	defer response.Body.Close()
	var tokens Tokens
	if response.StatusCode != http.StatusOK || common.DecodeJson(io.LimitReader(response.Body, 1<<20), &tokens) != nil || tokens.Access == "" || !strings.EqualFold(tokens.Type, "Bearer") || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > 300 {
		return Tokens{}, ErrAuthentication
	}
	return tokens, nil
}

func (c *Client) userInfo(ctx context.Context, endpoint, access string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Identity{}, ErrAuthentication
	}
	req.Header.Set("Authorization", "Bearer "+access)
	response, err := c.HTTP.Do(req)
	if err != nil {
		return Identity{}, ErrAuthentication
	}
	defer response.Body.Close()
	var identity Identity
	if response.StatusCode != http.StatusOK || common.DecodeJson(io.LimitReader(response.Body, 1<<20), &identity) != nil || identity.Subject == "" {
		return Identity{}, ErrAuthentication
	}
	return identity, nil
}

func (c *Client) Attach(sid string, session *Session) error {
	session.mu.Lock()
	expires := session.Expires
	session.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.sessions {
		if !time.Now().Before(entry.expires) {
			delete(c.sessions, key)
		}
	}
	if len(c.sessions) >= 5000 {
		return ErrAuthentication
	}
	c.sessions[sid] = sessionEntry{session: session, expires: expires}
	return nil
}

func (c *Client) Validate(ctx context.Context, sid string) error {
	c.mu.Lock()
	session := c.sessions[sid].session
	c.mu.Unlock()
	if session == nil {
		return ErrAuthentication
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !time.Now().Before(session.Expires) {
		return ErrAuthentication
	}
	if time.Since(session.Checked) < 30*time.Second && time.Now().Before(session.AccessExpires) {
		return nil
	}
	metadata, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	if !time.Now().Add(15 * time.Second).Before(session.AccessExpires) {
		if session.Tokens.Refresh == "" {
			return ErrAuthentication
		}
		next, err := c.exchange(ctx, metadata.Token, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {session.Tokens.Refresh}})
		if err != nil || next.Refresh == "" || next.Refresh == session.Tokens.Refresh {
			session.Expires = time.Time{}
			return ErrAuthentication
		}
		verified, err := c.verifyID(ctx, next, session.Nonce)
		if err != nil || verified.Subject != session.Identity.Subject {
			session.Expires = time.Time{}
			return ErrAuthentication
		}
		session.Tokens = next
		session.AccessExpires = time.Now().Add(time.Duration(next.ExpiresIn) * time.Second)
	}
	identity, err := c.userInfo(ctx, metadata.UserInfo, session.Tokens.Access)
	if err != nil || identity.Subject != session.Identity.Subject {
		session.Expires = time.Time{}
		return ErrAuthentication
	}
	session.Checked = time.Now()
	return nil
}

func (c *Client) Logout(ctx context.Context, sid, state string) (string, error) {
	c.mu.Lock()
	session := c.sessions[sid].session
	delete(c.sessions, sid)
	c.mu.Unlock()
	metadata, err := c.Discover(ctx)
	if err != nil {
		return "", err
	}
	if session == nil {
		return "", ErrAuthentication
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	params := url.Values{"id_token_hint": {session.Tokens.ID}, "client_id": {c.Config.ClientID}, "post_logout_redirect_uri": {c.Config.PublicURL + "/api/oauth/uac/logout/callback"}, "state": {state}}
	return metadata.Logout + "?" + params.Encode(), nil
}
