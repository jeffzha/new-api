package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type agencyTicketResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Ticket string `json:"ticket"`
		Proof  string `json:"proof"`
	} `json:"data"`
}

func TestAgencyTicketIssuanceEnforcesRootOrLinkedOperatorIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Agency{}, &model.AgencyOperatorAccount{}, &model.AgencyOperatorIdentityBinding{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	keyPath := t.TempDir() + "/agency-sso-private.pem"
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600))
	t.Setenv("AGENCY_SSO_PRIVATE_KEY_FILE", keyPath)
	t.Setenv("AGENCY_SSO_KEY_ID", "agency-test-key")

	now := time.Now().Unix()
	root := model.User{Username: "agency-root", Password: "unused", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "agency-root-code"}
	linkedUser := model.User{Username: "agency-linked", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "agency-linked-code"}
	unlinkedUser := model.User{Username: "agency-unlinked", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "agency-unlinked-code"}
	for _, user := range []*model.User{&root, &linkedUser, &unlinkedUser} {
		require.NoError(t, db.Create(user).Error)
	}
	agency := model.Agency{Depth: 1, Code: "agency-sso-test", DisplayName: "Agency SSO Test", Status: agencyhub.AgencyStatusActive, InviteCode: "AGENCYSSO", Version: 1, CreatedByType: agencyhub.ActorTypeRoot, CreatedByID: int64(root.Id), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&agency).Error)
	operator := model.AgencyOperatorAccount{AgencyID: agency.ID, NormalizedUsername: "agency-operator", Username: "agency-operator", PasswordHash: "unused", Status: agencyhub.OperatorStatusActive, AuthVersion: 1, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&operator).Error)
	require.NoError(t, db.Create(&model.AgencyOperatorIdentityBinding{OperatorAccountID: operator.ID, AgencyID: agency.ID, PlatformUserID: int64(linkedUser.Id), CreatedAt: now, UpdatedAt: now}).Error)

	call := func(handler gin.HandlerFunc, user model.User, useAccessToken bool, body string) *httptest.ResponseRecorder {
		router := gin.New()
		router.POST("/test", func(c *gin.Context) {
			c.Set("id", user.Id)
			c.Set("session_id", "session-"+user.Username)
			c.Set("auth_version", user.AuthVersion)
			c.Set("session_version", int64(1))
			c.Set("use_access_token", useAccessToken)
			handler(c)
		})
		request := httptest.NewRequest(http.MethodPost, "http://example.com/test", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://example.com")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	stateHash := strings.Repeat("a", 64)
	for _, test := range []struct {
		name       string
		user       model.User
		actorType  string
		actorID    int64
		agencyID   int64
		statusCode int
	}{
		{name: "root", user: root, actorType: agencyhub.ActorTypeRoot, statusCode: http.StatusOK},
		{name: "linked operator", user: linkedUser, actorType: agencyhub.ActorTypeOperator, actorID: operator.ID, agencyID: agency.ID, statusCode: http.StatusOK},
		{name: "unlinked user", user: unlinkedUser, statusCode: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := call(IssueAgencySSOTicket, test.user, false, `{"state_hash":"`+stateHash+`"}`)
			require.Equal(t, test.statusCode, response.Code, response.Body.String())
			if test.statusCode != http.StatusOK {
				return
			}
			var payload agencyTicketResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			require.True(t, payload.Success)
			claims, err := agencyhub.VerifySSOTicket(publicKey, payload.Data.Ticket, "new-api", "agency-hub")
			require.NoError(t, err)
			require.Equal(t, test.actorType, claims.ActorType)
			require.Equal(t, test.actorID, claims.ActorID)
			require.Equal(t, test.agencyID, claims.AgencyID)
			require.Equal(t, int64(test.user.Id), claims.Subject)
		})
	}

	pat := call(IssueAgencySSOTicket, linkedUser, true, `{"state_hash":"`+stateHash+`"}`)
	require.Equal(t, http.StatusForbidden, pat.Code, pat.Body.String())

	rootLink := call(IssueAgencyLinkProof, root, false, `{"request_id":"root-link"}`)
	require.Equal(t, http.StatusForbidden, rootLink.Code, rootLink.Body.String())
	patLink := call(IssueAgencyLinkProof, linkedUser, true, `{"request_id":"pat-link"}`)
	require.Equal(t, http.StatusForbidden, patLink.Code, patLink.Body.String())
	linkedProof := call(IssueAgencyLinkProof, linkedUser, false, `{"request_id":"linked-link"}`)
	require.Equal(t, http.StatusOK, linkedProof.Code, linkedProof.Body.String())
	var proofPayload agencyTicketResponse
	require.NoError(t, common.Unmarshal(linkedProof.Body.Bytes(), &proofPayload))
	proofClaims, err := agencyhub.VerifySSOTicket(publicKey, proofPayload.Data.Proof, "new-api", "agency-hub-account-link")
	require.NoError(t, err)
	require.Equal(t, agencyhub.ActorTypePlatformUser, proofClaims.ActorType)
	require.Equal(t, "linked-link", proofClaims.ObjectID)
}
