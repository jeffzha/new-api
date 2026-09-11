package agencyhub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAgencyTestApp(t *testing.T) *App {
	t.Helper()
	dsn := "file:agency-hub-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateAgency(db))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	return New(db, db, Config{BasePath: "/agency", PublicBaseURL: "https://gateway.example", SessionIdle: 30 * time.Minute, SessionAbsolute: time.Hour, MaxLoginAttempts: 5, SalesCapBPS: 30000, MinSpreadBPS: 500, WithdrawalsEnabled: true})
}

func TestCreateAgencyAndInvitePreview(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	agency, password, err := app.CreateAgency(1, "测试代理商", "agency_test", policy)
	require.NoError(t, err)
	require.NotEmpty(t, password)
	require.NotEmpty(t, agency.InviteCode)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/agency/api/v1/public/invitations/"+agency.InviteCode, nil)
	app.Router().ServeHTTP(recorder, request)
	require.Equal(t, 200, recorder.Code)
	require.Contains(t, recorder.Body.String(), "测试代理商")
	require.NotContains(t, recorder.Body.String(), "invite_url")
	require.NotContains(t, recorder.Body.String(), "invite_qr_url")
}

func TestRootCreateAgencyReturnsInviteLinkAndQR(t *testing.T) {
	app := newAgencyTestApp(t)
	t.Setenv("AGENCY_HUB_DELIVERY_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Task{}))
	root := model.User{Username: "root-create-agency", Password: "password", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&root).Error)
	source := model.UserSession{SID: "root-create-agency-session", UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "refresh-root-create-agency", LoginMethod: "password", LastActiveAt: 1, ExpiresAt: 4102444800}
	require.NoError(t, app.db.Create(&source).Error)
	sessionToken, csrf, err := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
	require.NoError(t, err)

	body := `{"display_name":"API Agency","operator_username":"api_operator","pricing":{"default_settlement_bps":7500,"default_sales_bps":9000,"min_spread_bps":500,"sales_cap_bps":30000}}`
	bodyDigest := sha256.Sum256(normalizeIdempotencyBody([]byte(body)))
	bodyHash := hex.EncodeToString(bodyDigest[:])
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	app.ssoPublicKey = publicKey
	now := time.Now().Unix()
	proof, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: int64(root.Id), SourceSID: source.SID, SessionVersion: source.Version, JTI: "root-create-agency-proof", KeyID: "test-key", Action: "agency.create", ObjectID: "agency:new", BodyHash: bodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: now + 300})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/agency/api/v1/root/agencies", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", "create-agency-invite-link")
	request.Header.Set("X-Agency-Verification-Proof", proof)
	request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
	app.Router().ServeHTTP(recorder, request)

	require.Equal(t, 201, recorder.Code, recorder.Body.String())
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			AgencyID            int64  `json:"agency_id"`
			InviteCode          string `json:"invite_code"`
			InviteURL           string `json:"invite_url"`
			InviteQRURL         string `json:"invite_qr_url"`
			DeliveryID          int64  `json:"delivery_id"`
			DeliveryOperationID string `json:"delivery_operation_id"`
			TemporaryPassword   string `json:"temporary_password"`
			Agency              struct {
				ID          int64  `json:"id"`
				InviteCode  string `json:"invite_code"`
				InviteURL   string `json:"invite_url"`
				InviteQRURL string `json:"invite_qr_url"`
			} `json:"agency"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotZero(t, response.Data.AgencyID)
	require.NotEmpty(t, response.Data.InviteCode)
	require.NotZero(t, response.Data.DeliveryID)
	require.NotEmpty(t, response.Data.DeliveryOperationID)
	require.NotEmpty(t, response.Data.TemporaryPassword)
	require.Equal(t, response.Data.AgencyID, response.Data.Agency.ID)
	require.Equal(t, response.Data.InviteCode, response.Data.Agency.InviteCode)
	require.Equal(t, "https://gateway.example/register?invite="+response.Data.InviteCode, response.Data.InviteURL)
	require.Equal(t, response.Data.InviteURL, response.Data.Agency.InviteURL)
	require.Equal(t, "https://gateway.example/agency/api/v1/public/invitations/"+response.Data.InviteCode+"/qr", response.Data.InviteQRURL)
	require.Equal(t, response.Data.InviteQRURL, response.Data.Agency.InviteQRURL)
	var delivery model.AgencyDeliverySecret
	require.NoError(t, app.db.First(&delivery, response.Data.DeliveryID).Error)
	require.NotEmpty(t, delivery.Ciphertext)
	require.Equal(t, int64(root.Id), delivery.CreatorRootID)
	var record model.AgencyIdempotencyRecord
	require.NoError(t, app.db.Where("resource_id = ?", "create-agency-invite-link").First(&record).Error)
	require.NotContains(t, record.ResponseJSON, response.Data.TemporaryPassword)

	qrRecorder := httptest.NewRecorder()
	qrRequest := httptest.NewRequest("GET", "/agency/api/v1/public/invitations/"+response.Data.InviteCode+"/qr", nil)
	app.Router().ServeHTTP(qrRecorder, qrRequest)
	require.Equal(t, 200, qrRecorder.Code)
	require.Equal(t, "image/png", qrRecorder.Header().Get("Content-Type"))
	require.True(t, bytes.HasPrefix(qrRecorder.Body.Bytes(), []byte{0x89, 0x50, 0x4e, 0x47}))

	ackBody := `{"operation_id":"` + response.Data.DeliveryOperationID + `"}`
	ackDigest := sha256.Sum256(normalizeIdempotencyBody([]byte(ackBody)))
	ackHash := hex.EncodeToString(ackDigest[:])
	ackProof, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: int64(root.Id), SourceSID: source.SID, SessionVersion: source.Version, JTI: "root-delivery-ack-proof", KeyID: "test-key", Action: "delivery.ack", ObjectID: fmt.Sprintf("delivery:%d", response.Data.DeliveryID), BodyHash: ackHash, IssuedAt: now, NotBefore: now, ExpiresAt: now + 300})
	require.NoError(t, err)
	ackRecorder := httptest.NewRecorder()
	ackRequest := httptest.NewRequest("POST", fmt.Sprintf("/agency/api/v1/root/deliveries/%d/ack", response.Data.DeliveryID), strings.NewReader(ackBody))
	ackRequest.Header.Set("Content-Type", "application/json")
	ackRequest.Header.Set("X-CSRF-Token", csrf)
	ackRequest.Header.Set("Idempotency-Key", "ack-agency-delivery")
	ackRequest.Header.Set("X-Agency-Verification-Proof", ackProof)
	ackRequest.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
	app.Router().ServeHTTP(ackRecorder, ackRequest)
	require.Equal(t, 200, ackRecorder.Code, ackRecorder.Body.String())
	require.Contains(t, ackRecorder.Header().Get("Cache-Control"), "no-store")
	require.Contains(t, ackRecorder.Body.String(), response.Data.TemporaryPassword)
	require.NoError(t, app.db.First(&delivery, response.Data.DeliveryID).Error)
	require.Empty(t, delivery.Ciphertext)
	require.NotNil(t, delivery.DeliveredAt)

	// Replaying the acknowledgement is idempotent, but never re-exposes the
	// one-time secret from the durable idempotency response.
	ackReplay := httptest.NewRecorder()
	ackReplayRequest := httptest.NewRequest("POST", fmt.Sprintf("/agency/api/v1/root/deliveries/%d/ack", response.Data.DeliveryID), strings.NewReader(ackBody))
	ackReplayRequest.Header.Set("Content-Type", "application/json")
	ackReplayRequest.Header.Set("X-CSRF-Token", csrf)
	ackReplayRequest.Header.Set("Idempotency-Key", "ack-agency-delivery")
	ackReplayRequest.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
	app.Router().ServeHTTP(ackReplay, ackReplayRequest)
	require.Equal(t, http.StatusOK, ackReplay.Code, ackReplay.Body.String())
	require.NotContains(t, ackReplay.Body.String(), response.Data.TemporaryPassword)
	require.Contains(t, ackReplay.Body.String(), "[redacted]")
}

func TestSalesPolicyPublishPreservesRootSettlementOverrides(t *testing.T) {
	settlement := 7000
	oldSales := 9300
	newSales := 9500
	base := agencycontract.Policy{
		DefaultSettlementBPS: 7500,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
		ModelOverrides: []agencycontract.ModelOverride{
			{OriginModelName: "settlement-only-model", SettlementBPS: &settlement},
			{OriginModelName: "sales-only-model", SalesBPS: &oldSales},
		},
	}
	candidate, err := mergeSalesPolicy(base, salesPricingRequest{
		DefaultSalesBPS: 9200,
		ModelSalesOverrides: []struct {
			OriginModelName string `json:"origin_model_name"`
			SalesBPS        *int   `json:"sales_bps"`
		}{
			{OriginModelName: "settlement-only-model", SalesBPS: &newSales},
		},
	})
	require.NoError(t, err)
	require.NoError(t, agencycontract.ValidatePolicy(candidate))
	require.Len(t, candidate.ModelOverrides, 1)
	require.Equal(t, "settlement-only-model", candidate.ModelOverrides[0].OriginModelName)
	require.NotNil(t, candidate.ModelOverrides[0].SettlementBPS)
	require.Equal(t, settlement, *candidate.ModelOverrides[0].SettlementBPS)
	require.NotNil(t, candidate.ModelOverrides[0].SalesBPS)
	require.Equal(t, newSales, *candidate.ModelOverrides[0].SalesBPS)
}

func TestPayoutAccountEncryptionRoundTrip(t *testing.T) {
	t.Setenv("AGENCY_PAYOUT_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	input := withdrawalAccountRequest{
		AccountType: "bank",
		AccountName: "测试账户",
		AccountNo:   "6222021234567890",
		BankName:    "测试银行",
		Last4:       "7890",
	}
	ciphertext, keyID, err := encryptPayoutAccount(input, 7, 1)
	require.NoError(t, err)
	require.NotEmpty(t, ciphertext)
	require.Equal(t, "agency-payout-v1", keyID)
	output, decryptKeyID, err := decryptPayoutAccount(ciphertext, 7, 1)
	require.NoError(t, err)
	require.Equal(t, keyID, decryptKeyID)
	require.Equal(t, input, output)
	_, _, err = decryptPayoutAccount(ciphertext, 8, 1)
	require.Error(t, err)
	_, _, err = decryptPayoutAccount(ciphertext, 7, 2)
	require.Error(t, err)
	tampered, err := base64.RawURLEncoding.DecodeString(ciphertext)
	require.NoError(t, err)
	tampered[len(tampered)-1] ^= 0x01
	_, _, err = decryptPayoutAccount(base64.RawURLEncoding.EncodeToString(tampered), 7, 1)
	require.Error(t, err)
}

func TestPayoutAccountEncryptionReadsHistoricalKeyAfterRotation(t *testing.T) {
	oldKey := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	newKey := "0101010101010101010101010101010101010101010101010101010101010101"
	t.Setenv("AGENCY_PAYOUT_KEY", oldKey)
	t.Setenv("AGENCY_PAYOUT_KEY_ID", "agency-payout-v1")
	input := withdrawalAccountRequest{AccountType: "bank", AccountName: "历史账户", AccountNo: "6222021234567890", Last4: "7890"}
	ciphertext, _, err := encryptPayoutAccount(input, 7, 1)
	require.NoError(t, err)

	oldKeysFile := t.TempDir() + "/old-keys"
	require.NoError(t, os.WriteFile(oldKeysFile, []byte("agency-payout-v1="+oldKey+"\n"), 0o600))
	t.Setenv("AGENCY_PAYOUT_KEY", newKey)
	t.Setenv("AGENCY_PAYOUT_KEY_ID", "agency-payout-v2")
	t.Setenv("AGENCY_PAYOUT_OLD_KEYS_FILE", oldKeysFile)

	output, keyID, err := decryptPayoutAccount(ciphertext, 7, 1)
	require.NoError(t, err)
	require.Equal(t, "agency-payout-v1", keyID)
	require.Equal(t, input, output)

	newCiphertext, keyID, err := encryptPayoutAccount(input, 7, 2)
	require.NoError(t, err)
	require.Equal(t, "agency-payout-v2", keyID)
	_, keyID, err = decryptPayoutAccount(newCiphertext, 7, 2)
	require.NoError(t, err)
	require.Equal(t, "agency-payout-v2", keyID)
}

func TestSSOTicketRejectsFutureValidityWindow(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().Unix()
	ticket, err := SignSSOTicket(privateKey, SSOTicketClaims{
		Issuer: "new-api", Audience: "agency-hub", Subject: 1,
		SourceSID: "sid", JTI: "future-ticket", KeyID: "k1",
		IssuedAt: now + 120, NotBefore: now + 120, ExpiresAt: now + 180,
	})
	require.NoError(t, err)
	_, err = VerifySSOTicket(publicKey, ticket, "new-api", "agency-hub")
	require.Error(t, err)

	ticket, err = SignSSOTicket(privateKey, SSOTicketClaims{
		Issuer: "new-api", Audience: "agency-hub", Subject: 1,
		SourceSID: "sid", JTI: "current-ticket", KeyID: "k1",
		IssuedAt: now, NotBefore: now, ExpiresAt: now + 60,
	})
	require.NoError(t, err)
	claims, err := VerifySSOTicket(publicKey, ticket, "new-api", "agency-hub")
	require.NoError(t, err)
	require.Equal(t, "current-ticket", claims.JTI)
}

func TestBillingEventIsIdempotent(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	agencyID := int64(7)
	bindingID := int64(1)
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-1", OperationID: "op-1", UserID: 100, AgencyID: &agencyID, BindingID: &bindingID, OriginModelName: "model", CurrencyCode: "CNY", CommissionEligible: true, CommissionAmountMicros: 100, ChargedTotalQuota: 900, SettlementCostQuota: 750, TheoreticalCommissionQuota: 150, PaidAllocatedQuota: 600, CommissionQuota: 100, OccurredAtMS: 1}
	require.NoError(t, app.ProcessBillingEvent(event))
	require.NoError(t, app.ProcessBillingEvent(event))
	// Simulate an approved payout that locked the full available balance.
	require.NoError(t, app.db.Model(&model.AgencyCommissionBalance{}).
		Where("agency_id = ? AND currency_code = ?", agencyID, "CNY").
		Updates(map[string]any{"available_micros": 0, "locked_micros": 100}).Error)
	require.NoError(t, app.db.Create(&model.AgencyWithdrawal{RequestNo: "wd-1", AgencyID: agencyID, CurrencyCode: "CNY", AmountMicros: 100, Status: "approved", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1}).Error)
	reversal := event
	reversal.EventID = "evt-2"
	reversal.EventType = "agency.billing_reversed"
	reversal.OriginalEventID = "evt-1"
	reversal.CommissionAmountMicros = 0
	reversal.ReversedCommissionAmountMicros = 40
	require.NoError(t, app.ProcessBillingEvent(reversal))
	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", agencyID, "CNY").First(&balance).Error)
	require.Equal(t, int64(-40), balance.AvailableMicros)
	require.Equal(t, int64(40), balance.ReversedMicros)
	var withdrawal model.AgencyWithdrawal
	require.NoError(t, app.db.Where("request_no = ?", "wd-1").First(&withdrawal).Error)
	require.Equal(t, "on_hold", withdrawal.Status)
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", "evt-1").Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestBillingEventRejectsPayloadHashConflict(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(8)
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-conflict",
		EventType: "agency.billing_finalized", OperationID: "op-conflict",
		UserID: 108, AgencyID: &agencyID, OriginModelName: "model", CurrencyCode: "CNY",
		CommissionEligible: true, CommissionAmountMicros: 100, OccurredAtMS: 1,
	}
	require.NoError(t, app.ProcessBillingEvent(event))
	conflict := event
	conflict.CommissionAmountMicros = 101
	require.ErrorContains(t, app.ProcessBillingEvent(conflict), "payload hash conflict")
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", event.EventID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestBillingEventRecordsUsageFactForNonCommissionableUsage(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(81)
	bindingID := int64(810)
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-usage-skipped",
		EventType: "agency.billing_finalized", OperationID: "op-usage-skipped",
		UserID: 8100, AgencyID: &agencyID, BindingID: &bindingID,
		OriginModelName: "hunyuan/hy3", Endpoint: "/v1/chat/completions",
		BusinessStatus: "success", BillingStatus: "finalized", CurrencyCode: "CNY",
		SalesBPS: 9000, CommissionEligible: false, CommissionSkipReason: "agency_disabled",
		StandardQuota: 1000, ChargedTotalQuota: 900, OccurredAtMS: time.Now().UnixMilli(),
	}
	require.NoError(t, app.ProcessBillingEvent(event))
	require.NoError(t, app.ProcessBillingEvent(event))

	var usageCount int64
	require.NoError(t, app.db.Model(&model.AgencyUsageFact{}).Where("event_id = ?", event.EventID).Count(&usageCount).Error)
	require.Equal(t, int64(1), usageCount)
	var usage model.AgencyUsageFact
	require.NoError(t, app.db.Where("event_id = ?", event.EventID).First(&usage).Error)
	require.Equal(t, agencyID, *usage.AgencyID)
	require.Equal(t, bindingID, *usage.BindingID)
	require.Equal(t, "agency_disabled", usage.SkipReason)
	require.Equal(t, int64(900), usage.ChargedQuota)

	var commissionCount int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Where("event_id = ?", event.EventID).Count(&commissionCount).Error)
	require.Equal(t, int64(0), commissionCount)
	var stat model.AgencyDailyStat
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", agencyID, "CNY").First(&stat).Error)
	require.Equal(t, int64(1), stat.Calls)
	require.Equal(t, int64(900), stat.ChargedQuota)
	require.Equal(t, int64(0), stat.CommissionMicros)
}

func TestCustomerUsageAndTopupsAreScopedToEventAgency(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}))
	agencyA := model.Agency{Code: "scope-a", DisplayName: "Scope A", Status: AgencyStatusActive, InviteCode: "SCOPEA", Version: 1}
	agencyB := model.Agency{Code: "scope-b", DisplayName: "Scope B", Status: AgencyStatusActive, InviteCode: "SCOPEB", Version: 1}
	require.NoError(t, app.db.Create(&agencyA).Error)
	require.NoError(t, app.db.Create(&agencyB).Error)
	user := model.User{Username: "scoped-customer"}
	require.NoError(t, app.db.Create(&user).Error)
	require.NoError(t, app.db.Create(&model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: agencyA.ID, Revision: 1, InviteSnapshot: agencyA.InviteCode, CreatedSource: "test", EffectiveAtMS: 1, CreatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: agencyB.ID, Revision: 2, InviteSnapshot: agencyB.InviteCode, CreatedSource: "test", EffectiveAtMS: 2, CreatedAt: 2}).Error)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{EventID: "evt-agency-a", ComponentID: "default", UserID: int64(user.Id), AgencyID: &agencyA.ID, OriginModelName: "model-a", BusinessStatus: "success", ChargedQuota: 10, CurrencyCode: "CNY", OccurredAtMS: 10}).Error)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{EventID: "evt-agency-b", ComponentID: "default", UserID: int64(user.Id), AgencyID: &agencyB.ID, OriginModelName: "model-b", BusinessStatus: "success", ChargedQuota: 20, CurrencyCode: "CNY", OccurredAtMS: 20}).Error)
	require.NoError(t, app.db.Create(&model.AgencyTopupFact{SourceOperationID: "topup-agency-a", UserID: int64(user.Id), AgencyID: &agencyA.ID, CreditedQuota: 100, CurrencyCode: "CNY", OccurredAtMS: 10}).Error)
	require.NoError(t, app.db.Create(&model.AgencyTopupFact{SourceOperationID: "topup-agency-b", UserID: int64(user.Id), AgencyID: &agencyB.ID, CreditedQuota: 200, CurrencyCode: "CNY", OccurredAtMS: 20}).Error)

	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agencyA.ID}
	usageRecorder := httptest.NewRecorder()
	usageContext, _ := gin.CreateTestContext(usageRecorder)
	usageContext.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/customers/1/usage", nil)
	usageContext.Params = gin.Params{{Key: "user_id", Value: fmt.Sprintf("%d", user.Id)}}
	usageContext.Set("agency_identity", identity)
	app.customerUsage(usageContext)
	require.Equal(t, http.StatusOK, usageRecorder.Code, usageRecorder.Body.String())
	require.Contains(t, usageRecorder.Body.String(), "evt-agency-a")
	require.NotContains(t, usageRecorder.Body.String(), "evt-agency-b")

	topupRecorder := httptest.NewRecorder()
	topupContext, _ := gin.CreateTestContext(topupRecorder)
	topupContext.Request = httptest.NewRequest(http.MethodGet, "/agency/api/v1/customers/1/topups", nil)
	topupContext.Params = gin.Params{{Key: "user_id", Value: fmt.Sprintf("%d", user.Id)}}
	topupContext.Set("agency_identity", identity)
	app.customerTopups(topupContext)
	require.Equal(t, http.StatusOK, topupRecorder.Code, topupRecorder.Body.String())
	require.Contains(t, topupRecorder.Body.String(), "topup-agency-a")
	require.NotContains(t, topupRecorder.Body.String(), "topup-agency-b")
}

func TestCustomerFactCursorPagingIsBoundToCustomerAndAgency(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}))
	agency := model.Agency{Code: "cursor-agency", DisplayName: "Cursor Agency", Status: AgencyStatusActive, InviteCode: "CURSOR", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	user := model.User{Username: "cursor-customer", AffCode: "cursor-customer-aff"}
	other := model.User{Username: "cursor-other", AffCode: "cursor-other-aff"}
	require.NoError(t, app.db.Create(&user).Error)
	require.NoError(t, app.db.Create(&other).Error)
	require.NoError(t, app.db.Create(&model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: agency.ID, Revision: 1, InviteSnapshot: agency.InviteCode, CreatedSource: "test", EffectiveAtMS: 1, CreatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyUserBinding{UserID: int64(other.Id), AgencyID: agency.ID, Revision: 1, InviteSnapshot: agency.InviteCode, CreatedSource: "test", EffectiveAtMS: 1, CreatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{EventID: "cursor-usage-1", ComponentID: "default", UserID: int64(user.Id), AgencyID: &agency.ID, OccurredAtMS: 20}).Error)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{EventID: "cursor-usage-2", ComponentID: "default", UserID: int64(user.Id), AgencyID: &agency.ID, OccurredAtMS: 10}).Error)

	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 1, AgencyID: &agency.ID}
	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	firstContext.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/agency/api/v1/customers/%d/usage?page_size=1", user.Id), nil)
	firstContext.Params = gin.Params{{Key: "user_id", Value: fmt.Sprintf("%d", user.Id)}}
	firstContext.Set("agency_identity", identity)
	app.customerUsage(firstContext)
	require.Equal(t, http.StatusOK, firstRecorder.Code, firstRecorder.Body.String())
	var first struct {
		Data struct {
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"meta"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(firstRecorder.Body.Bytes(), &first))
	require.NotEmpty(t, first.Data.Meta.NextCursor)

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/agency/api/v1/customers/%d/usage?cursor=%s&page_size=1", user.Id, url.QueryEscape(first.Data.Meta.NextCursor)), nil)
	secondContext.Params = gin.Params{{Key: "user_id", Value: fmt.Sprintf("%d", user.Id)}}
	secondContext.Set("agency_identity", identity)
	app.customerUsage(secondContext)
	require.Equal(t, http.StatusOK, secondRecorder.Code, secondRecorder.Body.String())
	require.Contains(t, secondRecorder.Body.String(), "cursor-usage-2")

	crossRecorder := httptest.NewRecorder()
	crossContext, _ := gin.CreateTestContext(crossRecorder)
	crossContext.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/agency/api/v1/customers/%d/usage?cursor=%s&page_size=1", other.Id, url.QueryEscape(first.Data.Meta.NextCursor)), nil)
	crossContext.Params = gin.Params{{Key: "user_id", Value: fmt.Sprintf("%d", other.Id)}}
	crossContext.Set("agency_identity", identity)
	app.customerUsage(crossContext)
	require.Equal(t, http.StatusBadRequest, crossRecorder.Code, crossRecorder.Body.String())
}

func TestRebuildDeliveryCreatesPendingRowForExistingOutbox(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(17)
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-rebuild", OperationID: "op-rebuild", UserID: 101, AgencyID: &agencyID, OriginModelName: "model", CurrencyCode: "CNY", OccurredAtMS: 1}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	payloadHash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{EventID: event.EventID, OperationID: event.OperationID, EventKind: "agency.billing_finalized", UserID: event.UserID, Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion, CreatedAtMS: 1}).Error)

	require.NoError(t, app.RebuildDelivery(event.EventID))
	var delivery model.AgencyEventDelivery
	require.NoError(t, app.db.Where("event_id = ?", event.EventID).First(&delivery).Error)
	require.Equal(t, "pending", delivery.Status)
	require.NotZero(t, delivery.CreatedAt)
}

func TestConsumerPoisonsMalformedAndUnknownSchemaEvents(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{EventID: "evt-poison-json", OperationID: "op-poison-json", EventKind: "agency.billing_finalized", UserID: 1, Payload: "{", PayloadHash: "bad", SchemaVersion: agencycontract.SchemaVersion, CreatedAtMS: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: "evt-poison-json", Status: "pending", NextRetryAt: 1, CreatedAt: 1}).Error)
	agencyID := int64(18)
	event := agencycontract.BillingEvent{SchemaVersion: "agency.billing.v999", EventID: "evt-poison-version", OperationID: "op-poison-version", UserID: 2, AgencyID: &agencyID, OriginModelName: "model", CurrencyCode: "CNY", CommissionEligible: true, CommissionAmountMicros: 10, OccurredAtMS: 1}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	payloadHash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{EventID: event.EventID, OperationID: event.OperationID, EventKind: "agency.billing_finalized", UserID: event.UserID, Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion, CreatedAtMS: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{EventID: event.EventID, Status: "pending", NextRetryAt: 1, CreatedAt: 1}).Error)

	require.NoError(t, app.RunConsumerOnce(t.Context(), 10))
	var deliveries []model.AgencyEventDelivery
	require.NoError(t, app.db.Where("event_id IN ?", []string{"evt-poison-json", "evt-poison-version"}).Order("event_id").Find(&deliveries).Error)
	require.Len(t, deliveries, 2)
	require.Equal(t, "poison", deliveries[0].Status)
	require.Equal(t, "poison", deliveries[1].Status)
	var ledgerCount int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Count(&ledgerCount).Error)
	require.Equal(t, int64(0), ledgerCount)
}

func TestConsumerLeaseFencePreventsStaleWorkerFinancialCommit(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(19)
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-stale-worker",
		EventType: "agency.billing_finalized", OperationID: "op-stale-worker",
		UserID: 19, AgencyID: &agencyID, OriginModelName: "model", CurrencyCode: "CNY",
		CommissionEligible: true, CommissionAmountMicros: 25, OccurredAtMS: time.Now().UnixMilli(),
	}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	payloadHash, err := agencycontract.CanonicalHash(event)
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{
		EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType,
		UserID: event.UserID, Payload: string(payload), PayloadHash: payloadHash,
		SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS,
	}).Error)
	delivery := model.AgencyEventDelivery{
		EventID: event.EventID, Status: "claimed", LeaseOwner: "worker-new",
		LeaseToken: 200, LeaseUntil: time.Now().Unix() + 60, NextRetryAt: 0, CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, app.db.Create(&delivery).Error)
	stale := delivery
	stale.LeaseOwner = "worker-old"
	stale.LeaseToken = 100
	err = app.processBillingEventWithLease(t.Context(), event, stale)
	require.ErrorContains(t, err, "lease lost")
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Count(&count).Error)
	require.Equal(t, int64(0), count)
}

func TestConsumerPoisonsOutboxPayloadHashMismatch(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(20)
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-outbox-hash-mismatch",
		EventType: "agency.billing_finalized", OperationID: "op-outbox-hash-mismatch",
		UserID: 20, AgencyID: &agencyID, OriginModelName: "model", CurrencyCode: "CNY",
		CommissionEligible: true, CommissionAmountMicros: 25, OccurredAtMS: 1,
	}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{
		EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType,
		UserID: event.UserID, Payload: string(payload), PayloadHash: "tampered",
		SchemaVersion: event.SchemaVersion, CreatedAtMS: 1,
	}).Error)
	require.NoError(t, app.db.Create(&model.AgencyEventDelivery{
		EventID: event.EventID, Status: "pending", NextRetryAt: 1, CreatedAt: 1,
	}).Error)
	require.NoError(t, app.RunConsumerOnce(t.Context(), 1))
	var delivery model.AgencyEventDelivery
	require.NoError(t, app.db.Where("event_id = ?", event.EventID).First(&delivery).Error)
	require.Equal(t, "poison", delivery.Status)
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommissionLedger{}).Count(&count).Error)
	require.Equal(t, int64(0), count)
}

func TestConsumerProcessesLateLowIDDeliveryAfterHigherIDDone(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(21)
	high := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-high-id",
		EventType: "agency.billing_finalized", OperationID: "op-high-id",
		UserID: 21, AgencyID: &agencyID, OriginModelName: "model", CurrencyCode: "CNY",
		CommissionEligible: true, CommissionAmountMicros: 30, OccurredAtMS: time.Now().UnixMilli(),
	}
	low := high
	low.EventID = "evt-low-id"
	low.OperationID = "op-low-id"
	low.CommissionAmountMicros = 40
	for _, item := range []struct {
		event      agencycontract.BillingEvent
		deliveryID int64
	}{{event: high, deliveryID: 100}, {event: low, deliveryID: 1}} {
		payload, err := common.Marshal(item.event)
		require.NoError(t, err)
		payloadHash, err := agencycontract.CanonicalHash(item.event)
		require.NoError(t, err)
		require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{
			EventID: item.event.EventID, OperationID: item.event.OperationID,
			EventKind: item.event.EventType, UserID: item.event.UserID, Payload: string(payload),
			PayloadHash: payloadHash, SchemaVersion: item.event.SchemaVersion,
			CreatedAtMS: item.event.OccurredAtMS,
		}).Error)
		require.NoError(t, app.db.Create(&model.AgencyEventDelivery{
			ID: item.deliveryID, EventID: item.event.EventID, Status: "pending",
			NextRetryAt: 1, CreatedAt: 1,
		}).Error)
		if item.deliveryID == 100 {
			require.NoError(t, app.RunConsumerOnce(t.Context(), 1))
		}
	}
	require.NoError(t, app.RunConsumerOnce(t.Context(), 10))
	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", agencyID, "CNY").First(&balance).Error)
	require.Equal(t, int64(70), balance.AvailableMicros)
	var pending int64
	require.NoError(t, app.db.Model(&model.AgencyEventDelivery{}).Where("status <> ?", "done").Count(&pending).Error)
	require.Equal(t, int64(0), pending)
}

func TestConsumerRetriesReversalBeforeOriginalAndLaterSettles(t *testing.T) {
	app := newAgencyTestApp(t)
	agencyID := int64(22)
	original := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "evt-reversal-original",
		EventType: "agency.billing_finalized", OperationID: "op-reversal-original",
		UserID: 22, AgencyID: &agencyID, OriginModelName: "model", CurrencyCode: "CNY",
		CommissionEligible: true, CommissionAmountMicros: 100, OccurredAtMS: time.Now().UnixMilli(),
	}
	reversal := original
	reversal.EventID = "evt-reversal-first"
	reversal.EventType = "agency.billing_reversed"
	reversal.OriginalEventID = original.EventID
	reversal.OperationID = "op-reversal-first"
	reversal.CommissionAmountMicros = 0
	reversal.ReversedCommissionAmountMicros = 35
	for _, event := range []agencycontract.BillingEvent{reversal, original} {
		payload, err := common.Marshal(event)
		require.NoError(t, err)
		payloadHash, err := agencycontract.CanonicalHash(event)
		require.NoError(t, err)
		require.NoError(t, app.db.Create(&model.AgencyBillingOutbox{
			EventID: event.EventID, OperationID: event.OperationID, EventKind: event.EventType,
			UserID: event.UserID, Payload: string(payload), PayloadHash: payloadHash,
			SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS,
		}).Error)
		require.NoError(t, app.db.Create(&model.AgencyEventDelivery{
			EventID: event.EventID, Status: "pending", NextRetryAt: 1, CreatedAt: 1,
		}).Error)
		if event.EventID == reversal.EventID {
			require.NoError(t, app.RunConsumerOnce(t.Context(), 1))
			var firstDelivery model.AgencyEventDelivery
			require.NoError(t, app.db.Where("event_id = ?", reversal.EventID).First(&firstDelivery).Error)
			require.Equal(t, "retry", firstDelivery.Status)
			require.NoError(t, app.db.Model(&firstDelivery).Update("next_retry_at", 1).Error)
		}
	}
	require.NoError(t, app.RunConsumerOnce(t.Context(), 10))
	require.NoError(t, app.db.Model(&model.AgencyEventDelivery{}).Where("event_id = ?", reversal.EventID).Update("next_retry_at", 1).Error)
	require.NoError(t, app.RunConsumerOnce(t.Context(), 10))
	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", agencyID, "CNY").First(&balance).Error)
	require.Equal(t, int64(65), balance.AvailableMicros)
	require.Equal(t, int64(35), balance.ReversedMicros)
}

func TestWithdrawalPayingGateRequiresImmutableAccountSnapshot(t *testing.T) {
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "wd-agency", DisplayName: "Withdrawal Agency", Status: AgencyStatusActive, InviteCode: "WDINVITE", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	account := model.AgencyWithdrawalAccount{AgencyID: agency.ID, Version: 1, Ciphertext: "encrypted-account-v1", KeyID: "key-1", Last4: "1234", Status: "active", CreatedAtMS: 1}
	require.NoError(t, app.db.Create(&account).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", AvailableMicros: 10, LockedMicros: 100, Version: 1}).Error)
	hash := sha256.Sum256([]byte(account.Ciphertext))
	withdrawal := model.AgencyWithdrawal{RequestNo: "wd-snapshot", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 100, Status: "approved", Version: 1, AccountID: account.ID, AccountVersion: account.Version, AccountSnapshotHash: hex.EncodeToString(hash[:]), AccountSnapshot: account.Ciphertext, AccountSnapshotKeyID: account.KeyID, CreatedAtMS: 1, UpdatedAtMS: 1}
	require.NoError(t, app.db.Create(&withdrawal).Error)

	require.NoError(t, app.db.Transaction(func(tx *gorm.DB) error {
		return app.validateWithdrawalPayingGate(tx, withdrawal)
	}))
	require.NoError(t, app.db.Model(&account).Update("ciphertext", "encrypted-account-v2").Error)
	err := app.db.Transaction(func(tx *gorm.DB) error {
		return app.validateWithdrawalPayingGate(tx, withdrawal)
	})
	require.ErrorContains(t, err, "snapshot")
}

func TestWithdrawalUnknownPaymentCannotRestartOrCancelButCanBeMarkedPaid(t *testing.T) {
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "wd-agency-2", DisplayName: "Withdrawal Agency 2", Status: AgencyStatusActive, InviteCode: "WDINVITE2", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", LockedMicros: 100, Version: 1}).Error)
	withdrawal := model.AgencyWithdrawal{RequestNo: "wd-unknown", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 100, Status: "on_hold", PreviousStatus: "payment_unknown", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1}
	require.NoError(t, app.db.Create(&withdrawal).Error)

	cancel := httptest.NewRecorder()
	cancelRequest := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/transition", withdrawal.ID), strings.NewReader(`{"target_status":"cancelled","expected_version":1,"reason":"operator retry"}`))
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelContext, _ := gin.CreateTestContext(cancel)
	cancelContext.Request = cancelRequest
	cancelContext.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", withdrawal.ID)}}
	cancelContext.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})
	cancelContext.Set("agency_transition_expected_version", int64(1))
	cancelContext.Set("agency_transition_reason", "operator retry")
	app.transitionWithdrawal(cancelContext, "cancelled")
	require.Equal(t, http.StatusConflict, cancel.Code, cancel.Body.String())

	markPaid := httptest.NewRecorder()
	paidRequest := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/mark-paid", withdrawal.ID), strings.NewReader(`{"expected_version":1,"payment_reference":"bank-ref-unknown-1"}`))
	paidRequest.Header.Set("Content-Type", "application/json")
	paidContext, _ := gin.CreateTestContext(markPaid)
	paidContext.Request = paidRequest
	paidContext.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", withdrawal.ID)}}
	paidContext.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})
	app.markWithdrawalPaid(paidContext)
	require.Equal(t, http.StatusOK, markPaid.Code, markPaid.Body.String())
	var stored model.AgencyWithdrawal
	require.NoError(t, app.db.First(&stored, withdrawal.ID).Error)
	require.Equal(t, "paid", stored.Status)
	require.Equal(t, "bank-ref-unknown-1", stored.PaymentReference)
}

func TestWithdrawalPaymentUnknownDoesNotDependOnExpiredLease(t *testing.T) {
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "wd-agency-unknown-lease", DisplayName: "Unknown Lease Agency", Status: AgencyStatusActive, InviteCode: "WDUNKNOWNLEASE", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", LockedMicros: 100, Version: 1}).Error)
	withdrawal := model.AgencyWithdrawal{
		RequestNo: "wd-unknown-expired-lease", AgencyID: agency.ID, CurrencyCode: "CNY",
		AmountMicros: 100, Status: "payment_unknown", Version: 1,
		PaymentLeaseOwner: "root:7", PaymentLeaseToken: 123, PaymentLeaseUntil: time.Now().Unix() - 1,
		CreatedAtMS: 1, UpdatedAtMS: 1,
	}
	require.NoError(t, app.db.Create(&withdrawal).Error)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/mark-paid", withdrawal.ID), strings.NewReader(`{"expected_version":1,"payment_reference":"bank-ref-unknown-expired-lease"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", withdrawal.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})
	app.markWithdrawalPaid(ctx)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var stored model.AgencyWithdrawal
	require.NoError(t, app.db.First(&stored, withdrawal.ID).Error)
	require.Equal(t, "paid", stored.Status)
}

func TestWithdrawalMarkPaidRequiresCurrentPaymentLease(t *testing.T) {
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "wd-agency-lease", DisplayName: "Withdrawal Agency Lease", Status: AgencyStatusActive, InviteCode: "WDLEASE", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	leaseToken := time.Now().UnixNano()
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", LockedMicros: 100, Version: 1}).Error)
	withdrawal := model.AgencyWithdrawal{
		RequestNo: "wd-lease", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 100,
		Status: "paying", Version: 1, PaymentLeaseOwner: "root:1",
		PaymentLeaseToken: leaseToken, PaymentLeaseUntil: time.Now().Unix() + 60,
		CreatedAtMS: 1, UpdatedAtMS: 1,
	}
	require.NoError(t, app.db.Create(&withdrawal).Error)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/mark-paid", withdrawal.ID), strings.NewReader(`{"expected_version":1,"payment_reference":"bank-ref-lease"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", withdrawal.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})
	app.markWithdrawalPaid(ctx)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/mark-paid", withdrawal.ID), strings.NewReader(fmt.Sprintf(`{"expected_version":1,"payment_lease_token":%d,"payment_reference":"bank-ref-lease"}`, leaseToken)))
	request.Header.Set("Content-Type", "application/json")
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", withdrawal.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})
	app.markWithdrawalPaid(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var stored model.AgencyWithdrawal
	require.NoError(t, app.db.First(&stored, withdrawal.ID).Error)
	require.Equal(t, "paid", stored.Status)
	require.Equal(t, int64(0), stored.PaymentLeaseToken)
}

func TestWithdrawalPaymentReferenceIsUniqueByChannel(t *testing.T) {
	app := newAgencyTestApp(t)
	agency := model.Agency{Code: "wd-agency-ref", DisplayName: "Withdrawal Agency Ref", Status: AgencyStatusActive, InviteCode: "WDREF", Version: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", LockedMicros: 200, Version: 1}).Error)
	first := model.AgencyWithdrawal{RequestNo: "wd-ref-1", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 100, Status: "payment_unknown", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1}
	second := model.AgencyWithdrawal{RequestNo: "wd-ref-2", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 100, Status: "payment_unknown", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1}
	require.NoError(t, app.db.Create(&first).Error)
	require.NoError(t, app.db.Create(&second).Error)
	for _, item := range []struct {
		id       int64
		expected int
	}{{id: first.ID, expected: http.StatusOK}, {id: second.ID, expected: http.StatusConflict}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agency/api/v1/root/withdrawals/%d/mark-paid", item.id), strings.NewReader(`{"expected_version":1,"payment_channel":"bank","payment_reference":"same-bank-ref"}`))
		request.Header.Set("Content-Type", "application/json")
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = request
		ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", item.id)}}
		ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})
		app.markWithdrawalPaid(ctx)
		require.Equal(t, item.expected, recorder.Code, recorder.Body.String())
	}
	var refs int64
	require.NoError(t, app.db.Model(&model.AgencyWithdrawalPaymentReference{}).Where("payment_channel = ? AND payment_reference = ?", "bank", "same-bank-ref").Count(&refs).Error)
	require.Equal(t, int64(1), refs)
}

func TestAgencyCanCancelSubmittedWithdrawalAndUnlockFunds(t *testing.T) {
	app := newAgencyTestApp(t)
	agency, _, err := app.CreateAgency(1, "Withdrawal Cancel Agency", "wd_cancel_operator", agencycontract.Policy{
		DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000,
	})
	require.NoError(t, err)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{
		AgencyID: agency.ID, CurrencyCode: "CNY", AvailableMicros: 25, LockedMicros: 100, Version: 1,
	}).Error)
	withdrawal := model.AgencyWithdrawal{
		RequestNo: "wd-cancel", AgencyID: agency.ID, CurrencyCode: "CNY", AmountMicros: 100,
		Status: "submitted", Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1,
	}
	require.NoError(t, app.db.Create(&withdrawal).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/agency/api/v1/withdrawals/1/cancel", strings.NewReader(`{"expected_version":1,"reason":"撤回申请"}`))
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", withdrawal.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 11, AgencyID: &agency.ID})
	app.cancelOwnWithdrawal(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var stored model.AgencyWithdrawal
	require.NoError(t, app.db.First(&stored, withdrawal.ID).Error)
	require.Equal(t, "cancelled", stored.Status)
	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ? AND currency_code = ?", agency.ID, "CNY").First(&balance).Error)
	require.Equal(t, int64(125), balance.AvailableMicros)
	require.Equal(t, int64(0), balance.LockedMicros)
}

func TestDisabledAgencyCannotCreateWithdrawalInManagedSession(t *testing.T) {
	app := newAgencyTestApp(t)
	agency, _, err := app.CreateAgency(1, "Disabled Withdrawal Agency", "wd_disabled_operator", agencycontract.Policy{
		DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000,
	})
	require.NoError(t, err)
	require.NoError(t, app.db.Model(&model.Agency{}).Where("id = ?", agency.ID).Update("status", AgencyStatusDisabled).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/agency/api/v1/withdrawals", strings.NewReader(`{"currency_code":"CNY","amount_micros":100,"account_id":1}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1, AgencyID: &agency.ID})
	app.createWithdrawal(ctx)

	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "agency_disabled")
}

func TestWithdrawalVerificationScopeIsActionBound(t *testing.T) {
	agencyID := int64(42)
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Request = httptest.NewRequest(http.MethodPost, "/agency/api/v1/withdrawals", nil)
	createContext.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 7, AgencyID: &agencyID})
	action, objectID, ok := expectedProofScope(createContext, currentIdentity(createContext))
	require.True(t, ok)
	require.Equal(t, "withdrawal.create", action)
	require.Equal(t, "agency:42", objectID)
	require.True(t, proofScopeMatchesRequest(createContext, currentIdentity(createContext), "withdrawal.create", "agency:42"))
	require.False(t, proofScopeMatchesRequest(createContext, currentIdentity(createContext), "withdrawal.cancel", "agency:42"))

	cancelRecorder := httptest.NewRecorder()
	cancelContext, _ := gin.CreateTestContext(cancelRecorder)
	cancelContext.Request = httptest.NewRequest(http.MethodPost, "/agency/api/v1/withdrawals/99/cancel", nil)
	cancelContext.Params = gin.Params{{Key: "id", Value: "99"}}
	cancelContext.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 7, AgencyID: &agencyID})
	action, objectID, ok = expectedProofScope(cancelContext, currentIdentity(cancelContext))
	require.True(t, ok)
	require.Equal(t, "withdrawal.cancel", action)
	require.Equal(t, "withdrawal:99", objectID)
	require.True(t, proofScopeMatchesRequest(cancelContext, currentIdentity(cancelContext), "withdrawal.cancel", "withdrawal:99"))
	require.False(t, proofScopeMatchesRequest(cancelContext, currentIdentity(cancelContext), "withdrawal.cancel", "withdrawal:98"))
	require.True(t, requiresAgencyVerification("/agency/api/v1/withdrawals"))
}

func TestWithdrawalAccountVerificationScopeIsActionBound(t *testing.T) {
	agencyID := int64(42)
	tests := []struct {
		name     string
		method   string
		path     string
		paramID  string
		action   string
		objectID string
	}{
		{name: "create", method: http.MethodPost, path: "/agency/api/v1/withdrawal-accounts", action: "withdrawal_account.create", objectID: "agency:42"},
		{name: "update", method: http.MethodPatch, path: "/agency/api/v1/withdrawal-accounts/12", paramID: "12", action: "withdrawal_account.update", objectID: "withdrawal_account:12"},
		{name: "disable", method: http.MethodPost, path: "/agency/api/v1/withdrawal-accounts/12/disable", paramID: "12", action: "withdrawal_account.disable", objectID: "withdrawal_account:12"},
		{name: "reveal", method: http.MethodPost, path: "/agency/api/v1/root/withdrawal-accounts/12/reveal", paramID: "12", action: "withdrawal_account.reveal", objectID: "withdrawal_account:12"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(testCase.method, testCase.path, nil)
			if testCase.paramID != "" {
				context.Params = gin.Params{{Key: "id", Value: testCase.paramID}}
			}
			context.Set("agency_identity", &Identity{ActorType: ActorTypeOperator, ActorID: 7, AgencyID: &agencyID})

			action, objectID, ok := expectedProofScope(context, currentIdentity(context))
			require.True(t, ok)
			require.Equal(t, testCase.action, action)
			require.Equal(t, testCase.objectID, objectID)
			require.True(t, proofScopeMatchesRequest(context, currentIdentity(context), testCase.action, testCase.objectID))
			require.False(t, proofScopeMatchesRequest(context, currentIdentity(context), testCase.action, testCase.objectID+"x"))
		})
	}
}

func TestRootManagementVerificationScopeIsActionBound(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		params   gin.Params
		action   string
		objectID string
	}{
		{name: "agency create", method: http.MethodPost, path: "/agency/api/v1/root/agencies", action: "agency.create", objectID: "agency:new"},
		{name: "agency update", method: http.MethodPatch, path: "/agency/api/v1/root/agencies/8", params: gin.Params{{Key: "id", Value: "8"}}, action: "agency.update", objectID: "agency:8"},
		{name: "agency disable", method: http.MethodPost, path: "/agency/api/v1/root/agencies/8/disable", params: gin.Params{{Key: "id", Value: "8"}}, action: "agency.disable", objectID: "agency:8"},
		{name: "root pricing", method: http.MethodPost, path: "/agency/api/v1/root/agencies/8/pricing/publish", params: gin.Params{{Key: "id", Value: "8"}}, action: "pricing.root.publish", objectID: "agency:8"},
		{name: "user bind", method: http.MethodPost, path: "/agency/api/v1/root/users/55/bind", params: gin.Params{{Key: "user_id", Value: "55"}}, action: "user.bind", objectID: "user:55"},
		{name: "user transfer", method: http.MethodPost, path: "/agency/api/v1/root/users/55/transfer", params: gin.Params{{Key: "user_id", Value: "55"}}, action: "user.transfer", objectID: "user:55"},
		{name: "reconciliation resolve", method: http.MethodPost, path: "/agency/api/v1/root/reconciliation/issues/9/resolve", params: gin.Params{{Key: "id", Value: "9"}}, action: "reconciliation.resolve", objectID: "reconciliation:9"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(testCase.method, testCase.path, nil)
			context.Params = testCase.params
			context.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})

			action, objectID, ok := expectedProofScope(context, currentIdentity(context))
			require.True(t, ok)
			require.Equal(t, testCase.action, action)
			require.Equal(t, testCase.objectID, objectID)
			require.True(t, proofScopeMatchesRequest(context, currentIdentity(context), testCase.action, testCase.objectID))
			require.False(t, proofScopeMatchesRequest(context, currentIdentity(context), testCase.action, testCase.objectID+"x"))
		})
	}
	require.True(t, requiresAgencyVerification("/agency/api/v1/root/provisioning/7/cancel"))
}

func TestSessionRouterDoesNotAcceptMissingCookie(t *testing.T) {
	app := newAgencyTestApp(t)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/agency/api/v1/auth/me", nil)
	app.Router().ServeHTTP(recorder, request)
	require.Equal(t, 401, recorder.Code)
}

func TestExportWorkerMaterializesUsageCSV(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.ExportDir = t.TempDir()
	agencyID := int64(9)
	job := model.AgencyExportJob{ActorType: ActorTypeOperator, ActorID: 4, AgencyID: &agencyID, Kind: "usage", FilterJSON: "{}", Status: "queued", ExpiresAt: 1, CreatedAtMS: 1}
	require.NoError(t, app.db.Create(&job).Error)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{EventID: "evt-export", ComponentID: "default", UserID: 12, AgencyID: &agencyID, OriginModelName: "demo", BusinessStatus: "success", StandardQuota: 10, ChargedQuota: 12, CurrencyCode: "TOKENS", OccurredAtMS: 100}).Error)
	processed, err := app.ProcessExportJobs(1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	var stored model.AgencyExportJob
	require.NoError(t, app.db.First(&stored, job.ID).Error)
	require.Equal(t, "ready", stored.Status)
	require.Equal(t, int64(1), stored.RowCount)
	data, err := os.ReadFile(app.config.ExportDir + "/" + stored.FileKey)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	require.Contains(t, string(data), "evt-export")
}

func TestExportCSVEscapesFormulaCells(t *testing.T) {
	var buffer bytes.Buffer
	require.NoError(t, ExportCSV(csv.NewWriter(&buffer), []string{"value"}, [][]string{{"=1+1"}, {"safe"}}))
	require.Contains(t, buffer.String(), "'=1+1")
}

func TestDurableFundingReservationCountsNonpaidSegments(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-funding-test-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, model.MigrateAgency(db))
	require.NoError(t, db.AutoMigrate(&model.User{}))
	user := &model.User{Username: "durable-funding-user", BillingMode: model.AgencyDurableBillingMode, Quota: 0}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.ApplyAgencyQuotaDeltaTx(tx, int64(user.Id), 100, "test_grant")
	}))
	paid, err := model.TryReserveUserQuotaAndAgency(user.Id, 60, "charge-1", 60)
	require.NoError(t, err)
	require.Equal(t, int64(0), paid)
	paid, err = model.TryReserveUserQuotaAndAgency(user.Id, 40, "charge-1", 100)
	require.NoError(t, err)
	require.Equal(t, int64(0), paid)
	var account model.AgencyFundingAccount
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(0), account.NonpaidAvailable)
	var allocationTotal int64
	require.NoError(t, db.Model(&model.AgencyFundingAllocation{}).Where("charge_id = ?", "charge-1").Select("COALESCE(SUM(nonpaid_consumed + consumed + debt_consumed), 0)").Scan(&allocationTotal).Error)
	require.Equal(t, int64(100), allocationTotal)
	_, err = model.TryReserveUserQuotaAndAgency(user.Id, 40, "charge-1", 100)
	require.NoError(t, err)
	_, err = model.TryReserveUserQuotaAndAgency(user.Id, 10, "charge-1", 200)
	require.Error(t, err)
	var storedUser model.User
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	require.Equal(t, 0, storedUser.Quota)
	require.NoError(t, db.Model(&model.AgencyFundingAllocation{}).Where("charge_id = ?", "charge-1").Select("COALESCE(SUM(nonpaid_consumed + consumed + debt_consumed), 0)").Scan(&allocationTotal).Error)
	require.Equal(t, int64(100), allocationTotal)
	// A paid top-up followed by an administrative debit must reduce both the
	// FIFO lot and the aggregate paid_available projection.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", 50)).Error; err != nil {
			return err
		}
		return model.RecordAgencyTopup(tx, int64(user.Id), "stripe", "topup-paid-1", "payment_callback", 50, 0)
	}))
	require.NoError(t, model.ApplyAgencyQuotaDelta(int64(user.Id), -20, "admin_debit"))
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, int64(30), account.PaidAvailable)
	var lot model.AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "topup-paid-1").First(&lot).Error)
	require.Equal(t, int64(30), lot.PaidAvailable)
	debtUser := &model.User{Username: "durable-debt-user", AffCode: "durable-debt-aff", BillingMode: model.AgencyDurableBillingMode, Quota: -25}
	require.NoError(t, db.Create(debtUser).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.EnsureAgencyFundingAccount(tx, int64(debtUser.Id))
	}))
	var debtAccount model.AgencyFundingAccount
	require.NoError(t, db.First(&debtAccount, debtUser.Id).Error)
	require.Equal(t, int64(25), debtAccount.DebtQuota)
}

func TestProvisioningWorkerDrainsBarrierAndBindsLegacyUser(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	previousDB := model.DB
	model.DB = app.db
	t.Cleanup(func() { model.DB = previousDB })
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	agency, _, err := app.CreateAgency(1, "provisioning-agency", "provisioning_operator", policy)
	require.NoError(t, err)
	user := &model.User{Username: "provisioning-legacy", BillingMode: "legacy", AuthVersion: 1, Quota: 42}
	require.NoError(t, app.db.Create(user).Error)
	job, created, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "migration")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, provisioningQueued, job.Status)
	var pending model.User
	require.NoError(t, app.db.First(&pending, user.Id).Error)
	require.Equal(t, model.AgencyProvisioningBillingMode, pending.BillingMode)
	err = model.IncreaseUserQuota(user.Id, 1, true)
	require.ErrorIs(t, err, model.ErrAgencyProvisioning)
	_, err = model.TryReserveUserQuotaAndAgency(user.Id, 1, "provisioning-charge", 1)
	require.ErrorIs(t, err, model.ErrAgencyProvisioning)
	processed, err := app.ProcessProvisioningJobs(1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	var completed model.AgencyProvisioningJob
	require.NoError(t, app.db.First(&completed, job.ID).Error)
	require.Equal(t, provisioningCompleted, completed.Status)
	require.NotZero(t, completed.CompletedAtMS)
	var bound model.AgencyActiveUserBinding
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&bound).Error)
	require.NotZero(t, bound.BindingID)
	require.NoError(t, app.db.First(&pending, user.Id).Error)
	require.Equal(t, model.AgencyDurableBillingMode, pending.BillingMode)
	var account model.AgencyFundingAccount
	require.NoError(t, app.db.First(&account, user.Id).Error)
	require.Equal(t, int64(42), account.NonpaidAvailable)
}

func TestProvisioningWorkerPersistsBlockersAndRetries(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	agency, _, err := app.CreateAgency(1, "blocking-agency", "blocking_operator", policy)
	require.NoError(t, err)
	user := &model.User{Username: "provisioning-blocked", BillingMode: "legacy", AuthVersion: 1}
	require.NoError(t, app.db.Create(user).Error)
	task := &model.Task{UserId: user.Id, TaskID: "upstream-blocker", Status: model.TaskStatusInProgress, Progress: "25%"}
	require.NoError(t, app.db.Create(task).Error)
	job, _, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "migration")
	require.NoError(t, err)
	_, err = app.ProcessProvisioningJobs(1)
	require.NoError(t, err)
	var blocked model.AgencyProvisioningJob
	require.NoError(t, app.db.First(&blocked, job.ID).Error)
	require.Equal(t, provisioningBlocked, blocked.Status)
	require.Equal(t, "in_flight_tasks", blocked.BlockReason)
	require.Contains(t, blocked.BlockingTasks, "upstream-blocker")
	require.NoError(t, app.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{"status": model.TaskStatusSuccess, "progress": "100%"}).Error)
	_, err = app.ProcessProvisioningJobs(1)
	require.NoError(t, err)
	require.NoError(t, app.db.First(&blocked, job.ID).Error)
	require.Equal(t, provisioningCompleted, blocked.Status)
}

func TestProvisioningCancellationReleasesAdmissionBarrier(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	user := &model.User{Username: "provisioning-cancelled", BillingMode: "legacy", AuthVersion: 1}
	require.NoError(t, app.db.Create(user).Error)
	agency := model.Agency{Code: "cancel-agency", DisplayName: "cancel", Status: AgencyStatusActive, InviteCode: "CANCELINVITE", PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: 1, UpdatedAt: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	job, _, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "cancel test")
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/agency/api/v1/root/provisioning/"+fmt.Sprint(job.ID)+"/cancel", strings.NewReader(`{"reason":"operator cancelled"}`))
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(job.ID)}}
	ctx.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: 1})
	app.cancelProvisioning(ctx)
	require.Equal(t, 200, recorder.Code)
	var stored model.AgencyProvisioningJob
	require.NoError(t, app.db.First(&stored, job.ID).Error)
	require.Equal(t, provisioningCancelled, stored.Status)
	var storedUser model.User
	require.NoError(t, app.db.First(&storedUser, user.Id).Error)
	require.Equal(t, "legacy", storedUser.BillingMode)
	processed, err := app.ProcessProvisioningJobs(1)
	require.NoError(t, err)
	require.Equal(t, 0, processed)
}

func TestProvisioningFencingTokenPreventsStaleCommit(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.Task{}))
	agency := model.Agency{Code: "fence-agency", DisplayName: "fence", Status: AgencyStatusActive, InviteCode: "FENCEINVITE", PriceRevision: 1, StateRevision: 1, Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: 1, UpdatedAt: 1}
	require.NoError(t, app.db.Create(&agency).Error)
	user := &model.User{Username: "provisioning-fenced", BillingMode: "legacy", AuthVersion: 1}
	require.NoError(t, app.db.Create(user).Error)
	job, _, err := app.enqueueProvisioningJob(int64(user.Id), agency.InviteCode, 1, "fence test")
	require.NoError(t, err)
	claimed, token, err := app.claimProvisioningJob(job.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	var owned model.AgencyProvisioningJob
	require.NoError(t, app.db.First(&owned, job.ID).Error)
	require.Equal(t, token, owned.FencingToken)
	// A retry owner supersedes the old token. The stale worker must fail before
	// it creates a binding, even though the user remains in provisioning mode.
	require.NoError(t, app.db.Model(&model.AgencyProvisioningJob{}).Where("id = ?", job.ID).Update("fencing_token", token+1).Error)
	err = app.commitProvisioningJob(owned)
	require.Error(t, err)
	var binding model.AgencyActiveUserBinding
	require.ErrorIs(t, app.db.Where("user_id = ?", user.Id).First(&binding).Error, gorm.ErrRecordNotFound)
}
