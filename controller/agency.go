package controller

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type agencySSORequest struct {
	StateHash string `json:"state_hash"`
}
type agencyVerifyRequest struct {
	Password string `json:"password"`
	Action   string `json:"action"`
	ObjectID string `json:"object_id"`
	BodyHash string `json:"body_hash"`
}

type agencyCommandProofRequest struct {
	Password        string `json:"password"`
	CommandID       string `json:"command_id"`
	Action          string `json:"action"`
	ObjectID        string `json:"object_id"`
	ExpectedVersion int64  `json:"expected_version"`
	BodyHash        string `json:"body_hash"`
}

func loadAgencySigningKey() (ed25519.PrivateKey, string, error) {
	path := strings.TrimSpace(os.Getenv("AGENCY_SSO_PRIVATE_KEY_FILE"))
	if path == "" {
		return nil, "", errors.New("AGENCY_SSO_PRIVATE_KEY_FILE is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, "", errors.New("invalid agency SSO private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, "", errors.New("agency SSO key is not Ed25519")
	}
	kid := strings.TrimSpace(os.Getenv("AGENCY_SSO_KEY_ID"))
	if kid == "" {
		kid = "agency-sso-v1"
	}
	return key, kid, nil
}

func IssueAgencySSOTicket(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "a browser session is required"})
		return
	}
	if !sameOriginWorkbenchRequest(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "a same-origin request is required"})
		return
	}
	var request agencySSORequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid request"})
		return
	}
	if decoded, err := hex.DecodeString(request.StateHash); err != nil || len(decoded) != 32 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid state hash"})
		return
	}
	key, kid, err := loadAgencySigningKey()
	if err != nil {
		common.SysError(err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "agency SSO is unavailable"})
		return
	}
	now := time.Now().Unix()
	jti := common.NewRequestId()
	ticket, err := agencyhub.SignSSOTicket(key, agencyhub.SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: int64(identity.UserID), SourceSID: identity.SessionID, UserAuthVersion: identity.UserAuthVersion, SessionVersion: identity.SessionVersion, StateHash: request.StateHash, JTI: jti, KeyID: kid, IssuedAt: now, NotBefore: now, ExpiresAt: now + 60})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to sign agency ticket"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"ticket": ticket, "expires_at": now + 60}})
}

func IssueAgencyVerification(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "a browser session is required"})
		return
	}
	if !sameOriginWorkbenchRequest(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "a same-origin request is required"})
		return
	}
	var request agencyVerifyRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Action == "" || request.ObjectID == "" || request.BodyHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid verification request"})
		return
	}
	if len(request.Action) > 128 || len(request.ObjectID) > 191 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid verification request"})
		return
	}
	if decoded, err := hex.DecodeString(request.BodyHash); err != nil || len(decoded) != 32 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid body hash"})
		return
	}
	user, err := model.GetUserById(identity.UserID, true)
	if err != nil || user.Role != common.RoleRootUser || user.Status != common.UserStatusEnabled || !common.ValidatePasswordAndHash(request.Password, user.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "verification failed"})
		return
	}
	key, kid, err := loadAgencySigningKey()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "agency verification is unavailable"})
		return
	}
	now := time.Now().Unix()
	proof, err := agencyhub.SignSSOTicket(key, agencyhub.SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: int64(identity.UserID), SourceSID: identity.SessionID, UserAuthVersion: identity.UserAuthVersion, SessionVersion: identity.SessionVersion, JTI: common.NewRequestId(), KeyID: kid, Action: request.Action, ObjectID: request.ObjectID, BodyHash: request.BodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: now + 300})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to sign verification proof"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"proof": proof, "expires_at": now + 300}})
}

// IssueAgencyCommandProof signs the Root authorization consumed by the
// gateway command endpoint. It is intentionally a separate audience from
// ordinary agency verification proofs so a hub-side proof cannot authorize a
// privileged gateway mutation.
func IssueAgencyCommandProof(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || c.GetBool("use_access_token") || !sameOriginWorkbenchRequest(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "a browser session is required"})
		return
	}
	var request agencyCommandProofRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || strings.TrimSpace(request.Password) == "" || strings.TrimSpace(request.CommandID) == "" || strings.TrimSpace(request.Action) == "" || strings.TrimSpace(request.ObjectID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid command proof request"})
		return
	}
	if len(request.CommandID) > 128 || len(request.Action) > 64 || len(request.ObjectID) > 191 || request.ExpectedVersion < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid command proof request"})
		return
	}
	decodedHash, err := hex.DecodeString(strings.TrimSpace(request.BodyHash))
	if err != nil || len(decodedHash) != 32 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid body hash"})
		return
	}
	user, err := model.GetUserById(identity.UserID, true)
	if err != nil || user.Role != common.RoleRootUser || user.Status != common.UserStatusEnabled || !common.ValidatePasswordAndHash(request.Password, user.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "verification failed"})
		return
	}
	key, kid, err := loadAgencySigningKey()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "agency command proof is unavailable"})
		return
	}
	now := time.Now().Unix()
	proof, err := agencyhub.SignSSOTicket(key, agencyhub.SSOTicketClaims{Issuer: "new-api", Audience: "agency-gateway-command", Subject: int64(identity.UserID), SourceSID: identity.SessionID, UserAuthVersion: identity.UserAuthVersion, SessionVersion: identity.SessionVersion, JTI: common.NewRequestId(), KeyID: kid, Action: request.Action, CommandID: request.CommandID, ObjectID: request.ObjectID, ExpectedVersion: request.ExpectedVersion, BodyHash: strings.ToLower(request.BodyHash), IssuedAt: now, NotBefore: now, ExpiresAt: now + 300})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to sign command proof"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"proof": proof, "expires_at": now + 300}})
}

func GetAgencyEffectivePricing(c *gin.Context) {
	modelName := c.Query("model")
	if strings.TrimSpace(modelName) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "model is required"})
		return
	}
	snapshot, err := service.AgencyQuoteForUser(c.GetInt("id"), 0, modelName, time.Now().UnixMilli())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"managed": false}})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "pricing is unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"managed": true, "agency_id": snapshot.AgencyID, "origin_model_name": snapshot.OriginModelName, "sales_bps": snapshot.SalesBPS, "policy_revision": snapshot.PolicyRevision}})
}
