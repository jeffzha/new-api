package agencyhub

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qrcode "github.com/skip2/go-qrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func invitationOperatorFixture(t *testing.T) (*App, model.Agency, string) {
	t.Helper()
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "own", DisplayName: "Own agency", InviteCode: "OWNINVITE", Status: AgencyStatusActive, Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	other := model.Agency{Code: "other", DisplayName: "Other private agency", InviteCode: "OTHERINVITE", Status: AgencyStatusActive, Version: 1}
	require.NoError(t, app.db.Create(&other).Error)
	account := model.AgencyOperatorAccount{AgencyID: agency.ID, Username: "invite-operator", NormalizedUsername: "invite-operator", PasswordHash: "unused-session-fixture", Status: OperatorStatusActive, AuthVersion: 1}
	require.NoError(t, app.db.Create(&account).Error)
	token := "invitation-session-token"
	now := time.Now().Unix()
	session := model.AgencySession{TokenHash: tokenHash(token), ActorType: ActorTypeOperator, ActorID: account.ID, AgencyID: &agency.ID, AuthVersion: 1, LastSeenAt: now, ExpiresAt: now + 3600}
	require.NoError(t, app.db.Create(&session).Error)
	return app, agency, token
}

func invitationGET(app *App, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: token})
	}
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, request)
	return recorder
}

func TestOwnInvitationUsesOnlyCurrentAgencyAndAbsoluteConfiguredLinks(t *testing.T) {
	app, agency, token := invitationOperatorFixture(t)
	app.config.BasePath = "/partners"
	response := invitationGET(app, "/partners/api/v1/invitation?agency_id=2", token)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	var result struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	assert.Equal(t, map[string]string{
		"display_name": agency.DisplayName, "invite_code": agency.InviteCode,
		"invite_url":    "https://gateway.example/register?invite=OWNINVITE",
		"invite_qr_url": "https://gateway.example/partners/api/v1/public/invitations/OWNINVITE/qr",
	}, result.Data)
	assert.NotContains(t, response.Body.String(), "Other private agency")
	assert.NotContains(t, response.Body.String(), "OTHERINVITE")

	qrURL, err := url.Parse(result.Data["invite_qr_url"])
	require.NoError(t, err)
	qrResponse := invitationGET(app, qrURL.RequestURI(), "")
	require.Equal(t, http.StatusOK, qrResponse.Code)
	require.Equal(t, "image/png", qrResponse.Header().Get("Content-Type"))
	decoded, err := png.Decode(bytes.NewReader(qrResponse.Body.Bytes()))
	require.NoError(t, err)
	assert.Equal(t, 512, decoded.Bounds().Dx())
	assert.Equal(t, 512, decoded.Bounds().Dy())
	// Existing dependencies provide a QR encoder and PNG decoder. Compare the
	// actual rendered QR pixels with the absolute registration target, which
	// catches encoding a relative URL or a different agency's invitation.
	expected, err := qrcode.New(result.Data["invite_url"], qrcode.Medium)
	require.NoError(t, err)
	expectedImage := expected.Image(512)
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			r, g, b, a := decoded.At(x, y).RGBA()
			er, eg, eb, ea := expectedImage.At(x, y).RGBA()
			if r != er || g != eg || b != eb || a != ea {
				require.FailNow(t, "QR registration target differs", "pixel %d,%d", x, y)
			}
		}
	}
}

func TestOwnInvitationRejectsUnavailableSessionsAndAgency(t *testing.T) {
	for _, scenario := range []string{"anonymous", "revoked_session", "expired_session", "disabled_operator", "disabled_agency"} {
		t.Run(scenario, func(t *testing.T) {
			app, agency, token := invitationOperatorFixture(t)
			switch scenario {
			case "anonymous":
				token = ""
			case "revoked_session":
				require.NoError(t, app.db.Model(&model.AgencySession{}).Where("token_hash = ?", tokenHash(token)).Update("revoked_at", time.Now().Unix()).Error)
			case "expired_session":
				require.NoError(t, app.db.Model(&model.AgencySession{}).Where("token_hash = ?", tokenHash(token)).Update("expires_at", 1).Error)
			case "disabled_operator":
				require.NoError(t, app.db.Model(&model.AgencyOperatorAccount{}).Where("agency_id = ?", agency.ID).Update("status", OperatorStatusDisabled).Error)
			case "disabled_agency":
				require.NoError(t, app.db.Model(&agency).Update("status", AgencyStatusDisabled).Error)
			}
			response := invitationGET(app, "/agency/api/v1/invitation", token)
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, response.Code)
			assert.NotContains(t, response.Body.String(), agency.InviteCode)
		})
	}
}

func TestOwnInvitationRootRequiresManagedActiveAgency(t *testing.T) {
	client := newFinanceRootClient(t)
	response := invitationGET(client.app, "/agency/api/v1/invitation?agency_id=1", client.sessionToken)
	assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	agency := model.Agency{Code: "managed-invite", DisplayName: "Managed agency", InviteCode: "MANAGEDINVITE", Status: AgencyStatusActive}
	require.NoError(t, client.app.db.Create(&agency).Error)
	require.NoError(t, client.app.db.Model(&model.AgencySession{}).Where("token_hash = ?", tokenHash(client.sessionToken)).Update("agency_id", agency.ID).Error)
	response = invitationGET(client.app, "/agency/api/v1/invitation", client.sessionToken)
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), agency.InviteCode)
	require.NoError(t, client.app.db.Model(&agency).Update("status", AgencyStatusDisabled).Error)
	response = invitationGET(client.app, "/agency/api/v1/invitation", client.sessionToken)
	assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), agency.InviteCode)
}

func TestOwnInvitationRejectsMissingAndInvalidPublicOrigins(t *testing.T) {
	for index, base := range []string{"", "/relative", "javascript:alert(1)", "https://", "https://name:secret@gateway.example", "https://gateway.example?redirect=elsewhere", "https://gateway.example#fragment"} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			app, agency, token := invitationOperatorFixture(t)
			app.config.PublicBaseURL = base
			response := invitationGET(app, "/agency/api/v1/invitation", token)
			assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "invitation_url_unavailable")
			assert.NotContains(t, response.Body.String(), agency.InviteCode)
			qr := invitationGET(app, "/agency/api/v1/public/invitations/"+agency.InviteCode+"/qr", "")
			assert.Equal(t, http.StatusServiceUnavailable, qr.Code)
			assert.NotEqual(t, "image/png", qr.Header().Get("Content-Type"), "invalid configuration must not generate an unscannable relative URL")
		})
	}
}

func TestInvitationLinksPreservePublicAndHubPrefixesAndEscapeInviteCode(t *testing.T) {
	app := New(nil, nil, Config{PublicBaseURL: "https://gateway.example/portal/", BasePath: "/partners"})
	link, qr, err := app.invitationLinks("INV+ITE")
	require.NoError(t, err)
	assert.Equal(t, "https://gateway.example/portal/register?invite=INV%2BITE", link)
	assert.Equal(t, "https://gateway.example/portal/partners/api/v1/public/invitations/INV+ITE/qr", qr)
}
