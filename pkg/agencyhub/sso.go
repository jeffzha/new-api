package agencyhub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

type SSOTicketClaims struct {
	Issuer          string `json:"iss"`
	Audience        string `json:"aud"`
	Subject         int64  `json:"sub"`
	SourceSID       string `json:"sid"`
	UserAuthVersion int64  `json:"user_auth_version"`
	SessionVersion  int64  `json:"session_version"`
	StateHash       string `json:"state_hash"`
	JTI             string `json:"jti"`
	KeyID           string `json:"kid"`
	Action          string `json:"action,omitempty"`
	CommandID       string `json:"command_id,omitempty"`
	ObjectID        string `json:"object_id,omitempty"`
	ExpectedVersion int64  `json:"expected_version,omitempty"`
	BodyHash        string `json:"body_hash,omitempty"`
	IssuedAt        int64  `json:"iat"`
	NotBefore       int64  `json:"nbf"`
	ExpiresAt       int64  `json:"exp"`
}

func SignSSOTicket(privateKey ed25519.PrivateKey, claims SSOTicketClaims) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize || claims.Subject <= 0 || claims.JTI == "" {
		return "", errors.New("invalid SSO signing input")
	}
	now := time.Now().Unix()
	if claims.IssuedAt == 0 {
		claims.IssuedAt = now
	}
	if claims.NotBefore == 0 {
		claims.NotBefore = now
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = now + 60
	}
	tokenClaims := jwt.MapClaims{"iss": claims.Issuer, "aud": claims.Audience, "sub": claims.Subject, "sid": claims.SourceSID, "user_auth_version": claims.UserAuthVersion, "session_version": claims.SessionVersion, "state_hash": claims.StateHash, "jti": claims.JTI, "kid": claims.KeyID, "action": claims.Action, "command_id": claims.CommandID, "object_id": claims.ObjectID, "expected_version": claims.ExpectedVersion, "body_hash": claims.BodyHash, "iat": claims.IssuedAt, "nbf": claims.NotBefore, "exp": claims.ExpiresAt}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, tokenClaims)
	token.Header["kid"] = claims.KeyID
	return token.SignedString(privateKey)
}

func VerifySSOTicket(publicKey ed25519.PublicKey, raw string, issuer, audience string) (SSOTicketClaims, error) {
	if len(publicKey) != ed25519.PublicKeySize || strings.TrimSpace(raw) == "" {
		return SSOTicketClaims{}, errors.New("invalid SSO ticket")
	}
	token, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, errors.New("unexpected SSO algorithm")
		}
		return publicKey, nil
	}, jwt.WithIssuer(issuer), jwt.WithAudience(audience), jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}))
	if err != nil || !token.Valid {
		return SSOTicketClaims{}, errors.New("invalid SSO ticket")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return SSOTicketClaims{}, errors.New("invalid SSO claims")
	}
	headerKid, _ := token.Header["kid"].(string)
	claimKid, _ := claims["kid"].(string)
	if strings.TrimSpace(headerKid) == "" || headerKid != claimKid {
		return SSOTicketClaims{}, errors.New("SSO key id mismatch")
	}
	toInt := func(value any) int64 {
		switch v := value.(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case jsonNumber:
			result, _ := v.Int64()
			return result
		}
		return 0
	}
	result := SSOTicketClaims{Issuer: asString(claims["iss"]), Audience: asString(claims["aud"]), Subject: toInt(claims["sub"]), SourceSID: asString(claims["sid"]), UserAuthVersion: toInt(claims["user_auth_version"]), SessionVersion: toInt(claims["session_version"]), StateHash: asString(claims["state_hash"]), JTI: asString(claims["jti"]), KeyID: claimKid, Action: asString(claims["action"]), CommandID: asString(claims["command_id"]), ObjectID: asString(claims["object_id"]), ExpectedVersion: toInt(claims["expected_version"]), BodyHash: asString(claims["body_hash"]), IssuedAt: toInt(claims["iat"]), NotBefore: toInt(claims["nbf"]), ExpiresAt: toInt(claims["exp"])}
	now := time.Now().Unix()
	if result.Subject <= 0 || result.JTI == "" ||
		result.IssuedAt <= 0 || result.NotBefore <= 0 || result.ExpiresAt <= now ||
		result.IssuedAt > now+int64(commandClockSkew/time.Second) ||
		result.NotBefore > now+int64(commandClockSkew/time.Second) {
		return SSOTicketClaims{}, errors.New("expired SSO ticket")
	}
	return result, nil
}

type jsonNumber interface{ Int64() (int64, error) }

func asString(value any) string {
	value, _ = value.(string)
	result, _ := value.(string)
	return result
}
func hashState(value string) string {
	hash := sha256.Sum256([]byte(value))
	return tokenHash(string(hash[:]))
}

func (a *App) SetSSOPublicKey(key ed25519.PublicKey) {
	a.ssoPublicKey = append(ed25519.PublicKey(nil), key...)
}

// SetCommandServicePublicKey configures the Ed25519 public key used to
// authenticate command envelopes sent by the hub/gateway peer. It is kept
// separate from the Root SSO key so possession of one key cannot authorize the
// other audience.
func (a *App) SetCommandServicePublicKey(key ed25519.PublicKey) {
	a.commandServicePublicKey = append(ed25519.PublicKey(nil), key...)
}

// SetCommandServicePrivateKey configures the hub service key used to sign
// command envelopes written into the shared command queue.
func (a *App) SetCommandServicePrivateKey(key ed25519.PrivateKey) {
	a.commandServicePrivateKey = append(ed25519.PrivateKey(nil), key...)
}

func LoadSSOPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid SSO public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("SSO public key is not Ed25519")
	}
	return key, nil
}

func LoadSSOPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid Ed25519 private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("private key is not Ed25519")
	}
	return key, nil
}
func (a *App) ssoStart(c *gin.Context) {
	state, err := randomToken(24)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "sso_failed", "SSO初始化失败", nil)
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "sso_failed", "SSO初始化失败", nil)
		return
	}
	c.SetCookie("agency_sso_nonce", nonce, 300, a.config.BasePath, "", a.config.CookieSecure, true)
	c.SetCookie("agency_sso_state", state, 300, a.config.BasePath, "", a.config.CookieSecure, true)
	respondOK(c, gin.H{"state": state, "state_hash": hashState(state), "return_path": a.config.BasePath + "/"})
}
func (a *App) ssoCallback(c *gin.Context) {
	var request struct {
		Ticket string `json:"ticket"`
		State  string `json:"state"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	state, err := c.Cookie("agency_sso_state")
	if err != nil || state == "" || state != request.State {
		respondError(c, http.StatusUnauthorized, "invalid_state", "SSO状态无效", nil)
		return
	}
	if len(a.ssoPublicKey) != ed25519.PublicKeySize {
		respondError(c, http.StatusServiceUnavailable, "sso_unconfigured", "SSO尚未配置", nil)
		return
	}
	claims, err := VerifySSOTicket(a.ssoPublicKey, request.Ticket, "new-api", "agency-hub")
	if err != nil || claims.StateHash != hashState(state) {
		respondError(c, http.StatusUnauthorized, "invalid_ticket", "SSO票据无效", nil)
		return
	}
	now := time.Now().Unix()
	var used model.AgencySSOTicketUse
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("jti = ?", claims.JTI).First(&used).Error; err == nil {
			return errors.New("SSO票据已使用")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&model.AgencySSOTicketUse{JTI: claims.JTI, ActorID: claims.Subject, SourceSID: claims.SourceSID, ExpiresAt: claims.ExpiresAt, ConsumedAt: now, CreatedAt: now}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		respondError(c, http.StatusUnauthorized, "ticket_replayed", err.Error(), nil)
		return
	}
	token, csrf, err := a.CreateRootSession(claims.Subject, claims.SourceSID, claims.SessionVersion)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "session_failed", "SSO登录失败", nil)
		return
	}
	a.setSessionCookies(c, token, csrf, now+int64(a.config.SessionAbsolute/time.Second))
	respondOK(c, gin.H{"redirect": a.config.BasePath + "/", "actor_type": ActorTypeRoot})
}
