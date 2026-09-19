//go:build agency_browser

package agencyhub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestAgencyBrowserFixture is an explicitly enabled local test server, never
// compiled into production. Hub auth, CSRF, proofs, idempotency, business handlers,
// migrations and persistence are real. Only the upstream gateway login/signing
// boundary is replaced by a fixture. No real users, payouts or upstream API calls
// are involved. The frontend is the supplied production Vite build.
func TestAgencyBrowserFixture(t *testing.T) {
	if os.Getenv("AGENCY_BROWSER_FIXTURE") != "1" {
		t.Skip("set AGENCY_BROWSER_FIXTURE=1 only for the isolated browser runner")
	}
	dist := os.Getenv("AGENCY_BROWSER_DIST")
	t.Setenv("AGENCY_ONBOARDING_ENABLED", "true")
	require.NotEmpty(t, dist)
	index, err := os.ReadFile(filepath.Join(dist, "index.html"))
	require.NoError(t, err)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "browser.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, model.MigrateAgency(db))
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Task{}, &model.Log{}, &model.Channel{}, &model.Ability{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	t.Setenv("AGENCY_HUB_DELIVERY_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	t.Setenv("AGENCY_PAYOUT_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	address := os.Getenv("AGENCY_BROWSER_ADDR")
	if address == "" {
		address = "127.0.0.1:4328"
	}
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err)
	origin := "http://" + listener.Addr().String()
	app := New(db, db, Config{BasePath: "/agency", PublicBaseURL: origin, PlatformBaseURL: origin, SessionIdle: time.Hour, SessionAbsolute: time.Hour, LoginLockout: time.Minute, MaxLoginAttempts: 5, SalesCapBPS: 30000, MinSpreadBPS: 500, WithdrawalsEnabled: true, ExportDir: t.TempDir()})
	app.SetReady(true)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	app.ssoPublicKey = pub
	root := model.User{Username: "browser-root", Password: "unused", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&root).Error)
	source := model.UserSession{SID: "browser-root-session", UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "browser-refresh", LoginMethod: "password", LastActiveAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, db.Create(&source).Error)
	policy := agencycontract.Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	channel := model.Channel{Name: "Browser model channel", Type: 1, Key: "browser-only", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "browser-chat-model", ChannelId: channel.Id, Enabled: true}).Error)
	agency, _, err := app.CreateAgency(int64(root.Id), "Browser Agency", "browser-operator", policy)
	require.NoError(t, err)
	passwordHash, err := common.Password2Hash("Browser-operator-2026!")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.AgencyOperatorAccount{}).Where("agency_id = ?", agency.ID).Updates(map[string]any{"password_hash": passwordHash, "must_change_password": false}).Error)
	require.NoError(t, db.Create(&model.AgencyCommissionBalance{AgencyID: agency.ID, CurrencyCode: "CNY", EarnedMicros: 1000000000, AvailableMicros: 1000000000, Version: 1, UpdatedAtMS: time.Now().UnixMilli()}).Error)
	targetAgency, _, err := app.CreateAgency(int64(root.Id), "Transfer Target", "browser-target", policy)
	require.NoError(t, err)
	// Separate reporting fixture: refunds and payouts must not change the
	// lifetime-total formula or mix currencies, including values above 2^53.
	summaryAgency, _, err := app.CreateAgency(int64(root.Id), "Summary Agency", "browser-summary", policy)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.AgencyOperatorAccount{}).Where("agency_id = ?", summaryAgency.ID).Updates(map[string]any{"password_hash": passwordHash, "must_change_password": false}).Error)
	require.NoError(t, db.Create(&[]model.AgencyCommissionBalance{
		{AgencyID: summaryAgency.ID, CurrencyCode: "CNY", EarnedMicros: 1000000000, ReversedMicros: 200000000, AvailableMicros: 400000000, LockedMicros: 100000000, PaidMicros: 300000000, Version: 1},
		{AgencyID: summaryAgency.ID, CurrencyCode: "USD", EarnedMicros: 9007199254740993, ReversedMicros: 1, AvailableMicros: 9007199254740992, Version: 1},
	}).Error)
	legacy := model.User{Username: "browser-legacy", AffCode: "browser-legacy-aff", BillingMode: "legacy", Quota: 42, AuthVersion: 1}
	require.NoError(t, db.Create(&legacy).Error)
	blocker := model.Task{UserId: legacy.Id, TaskID: "BROWSER-BINDING-BLOCKER", Status: model.TaskStatusInProgress, Progress: "25%"}
	require.NoError(t, db.Create(&blocker).Error)
	managed := model.User{Username: "browser-managed", AffCode: "browser-managed-aff", BillingMode: model.AgencyDurableBillingMode, AuthVersion: 1}
	require.NoError(t, db.Create(&managed).Error)
	err = db.Transaction(func(tx *gorm.DB) error {
		_, bindErr := BindUserByInvite(tx, int64(managed.Id), agency.InviteCode, "invite", 0)
		return bindErr
	})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyUsageFact{EventID: "browser-export-visible", ComponentID: "text", UserID: int64(managed.Id), AgencyID: &agency.ID, OriginModelName: "browser-export-model", ModelKey: "browser-export-model", ChargedQuota: 9, OccurredAtMS: time.Now().UnixMilli()}).Error)
	require.NoError(t, db.Create(&model.AgencyUsageFact{EventID: "browser-export-private", ComponentID: "text", UserID: 9999, AgencyID: &targetAgency.ID, OriginModelName: "OTHER-AGENCY-PRIVATE-MODEL", ModelKey: "other", ChargedQuota: 12, OccurredAtMS: time.Now().UnixMilli()}).Error)
	workerContext, stopWorker := context.WithCancel(context.Background())
	t.Cleanup(stopWorker)
	app.StartBackground(workerContext)
	var proofSequence atomic.Int64
	router := app.Router()
	router.POST("/__fixture/reconciliation", func(c *gin.Context) {
		user := model.User{Username: "browser-reconciliation-mismatch", AffCode: "browser-reconcile-aff", BillingMode: model.AgencyDurableBillingMode, Quota: 100, AuthVersion: 1}
		issues := make([]model.AgencyReconciliationIssue, 0, 3)
		eventID := "browser-reconciliation-missing-delivery"
		var payloadHash string
		seedErr := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			if err := tx.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 90, MoneySeq: 1, Version: 1}).Error; err != nil {
				return err
			}
			lot := model.AgencyFundingLot{UserID: int64(user.Id), SourceKind: "fixture", SourceID: "browser-reconcile-opening", PaidInitial: 90, PaidAvailable: 90, MoneySeq: 1, Version: 1}
			if err := tx.Create(&lot).Error; err != nil {
				return err
			}
			if err := tx.Create(&model.AgencyFundingLedger{OperationID: "browser-reconcile-opening", UserID: int64(user.Id), MoneySeq: 1, SourceKind: "fixture", LotID: &lot.ID, PaidDelta: 90, PaidAfter: 90}).Error; err != nil {
				return err
			}
			event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: eventID, EventType: "agency.topup_completed", FinancialChargeID: "browser-reconcile-operation", OperationID: "browser-reconcile-operation", UserID: int64(user.Id), MoneySeq: 1, EventCount: 1, JournalRevision: 1, BusinessStatus: "success", BillingStatus: "funded", CommissionSkipReason: "topup_noncommissionable", OccurredAtMS: time.Now().UnixMilli()}
			payload, err := common.Marshal(event)
			if err != nil {
				return err
			}
			payloadHash, err = agencycontract.CanonicalHash(event)
			if err != nil {
				return err
			}
			if err := tx.Create(&model.AgencyBillingOutbox{EventID: eventID, OperationID: event.OperationID, EventKind: event.EventType, EventCount: 1, UserID: int64(user.Id), MoneySeq: 1, SchemaVersion: event.SchemaVersion, Payload: string(payload), PayloadHash: payloadHash, CreatedAtMS: event.OccurredAtMS}).Error; err != nil {
				return err
			}
			for _, scenario := range []struct{ kind, id, difference string }{
				{"funding_account", fmt.Sprint(user.Id), "Browser evidence: wallet quota 100, available funding 90"},
				{"billing_outbox", eventID, "Browser evidence: immutable event has no delivery"},
				{"external_bank_result", "browser-unknown-payment", "Browser evidence: provider result is not available"},
			} {
				key := sha256.Sum256([]byte(scenario.kind + "\x00" + scenario.id))
				activeKey := hex.EncodeToString(key[:])
				issue := model.AgencyReconciliationIssue{ObjectType: scenario.kind, ObjectID: scenario.id, Difference: scenario.difference, EvidenceHash: "fixture-original-evidence", Status: "open", ActiveKey: &activeKey, CreatedAtMS: time.Now().UnixMilli()}
				if err := tx.Create(&issue).Error; err != nil {
					return err
				}
				issues = append(issues, issue)
			}
			return nil
		})
		if seedErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": seedErr.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"funding_issue_id": fmt.Sprint(issues[0].ID), "delivery_issue_id": fmt.Sprint(issues[1].ID), "unsupported_issue_id": fmt.Sprint(issues[2].ID), "event_id": eventID, "user_id": fmt.Sprint(user.Id), "payload_hash": payloadHash})
	})
	router.GET("/__fixture/reconciliation-state", func(c *gin.Context) {
		var user model.User
		if err := db.Where("username = ?", "browser-reconciliation-mismatch").First(&user).Error; err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		var funding model.AgencyFundingAccount
		var outbox model.AgencyBillingOutbox
		var deliveries []model.AgencyEventDelivery
		var issues []model.AgencyReconciliationIssue
		var audits []model.AgencyAuditLog
		if db.Where("user_id = ?", user.Id).First(&funding).Error != nil || db.Where("event_id = ?", "browser-reconciliation-missing-delivery").First(&outbox).Error != nil || db.Where("event_id = ?", outbox.EventID).Find(&deliveries).Error != nil || db.Where("object_id IN ?", []string{fmt.Sprint(user.Id), outbox.EventID, "browser-unknown-payment"}).Find(&issues).Error != nil || db.Where("object_type = ?", "reconciliation_issue").Find(&audits).Error != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		items := make(map[string]gin.H)
		for _, issue := range issues {
			items[fmt.Sprint(issue.ID)] = gin.H{"status": issue.Status, "resolution_evidence": issue.ResolutionEvidence, "repair_event_id": issue.RepairEventID, "resolution": issue.Resolution}
		}
		c.JSON(http.StatusOK, gin.H{"wallet_quota": user.Quota, "paid_available": funding.PaidAvailable, "money_seq": funding.MoneySeq, "payload_hash": outbox.PayloadHash, "payload": outbox.Payload, "deliveries": deliveries, "issues": items, "audits": audits})
	})
	router.POST("/__fixture/root", func(c *gin.Context) {
		token, csrf, createErr := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
		if createErr != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		app.setSessionCookies(c, token, csrf, time.Now().Add(time.Hour).Unix())
		respondOK(c, gin.H{"root_id": root.Id, "agency_id": agency.ID})
	})
	router.POST("/__fixture/proof", func(c *gin.Context) {
		var input struct {
			Password string `json:"password"`
			Action   string `json:"action"`
			ObjectID string `json:"object_id"`
			BodyHash string `json:"body_hash"`
		}
		if err := c.ShouldBindJSON(&input); err != nil || input.Password != "Browser-root-2026!" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid fixture password"})
			return
		}
		proof, signErr := SignSSOTicket(private, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: int64(root.Id), SourceSID: source.SID, UserAuthVersion: 1, SessionVersion: source.Version, JTI: fmt.Sprintf("browser-proof-%d", proofSequence.Add(1)), KeyID: "browser-only", Action: input.Action, ObjectID: input.ObjectID, BodyHash: input.BodyHash})
		if signErr != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"proof": proof})
	})
	router.GET("/api/agency/sso", func(c *gin.Context) {
		if c.Query("origin") != origin || c.Query("mode") != "verify" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(browserVerificationBridge))
	})
	router.GET("/__fixture/state", func(c *gin.Context) {
		var agencies []model.Agency
		var withdrawals []model.AgencyWithdrawal
		var balances []model.AgencyCommissionBalance
		var deliveries []model.AgencyDeliverySecret
		var accounts []model.AgencyWithdrawalAccount
		if db.Find(&agencies).Error != nil || db.Find(&withdrawals).Error != nil || db.Find(&balances).Error != nil || db.Find(&deliveries).Error != nil || db.Find(&accounts).Error != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		payments := make([]gin.H, 0, len(withdrawals))
		for _, row := range withdrawals {
			payments = append(payments, gin.H{"id": row.ID, "status": row.Status, "amount_micros": fmt.Sprint(row.AmountMicros), "payment_lease_token": fmt.Sprint(row.PaymentLeaseToken), "payment_reference": row.PaymentReference})
		}
		receipts := make([]gin.H, 0, len(deliveries))
		for _, row := range deliveries {
			receipts = append(receipts, gin.H{"id": row.ID, "ciphertext_present": row.Ciphertext != "", "delivered": row.DeliveredAt != nil})
		}
		var legacyUser model.User
		var activeBindings []model.AgencyActiveUserBinding
		if db.Select("id, billing_mode, quota").First(&legacyUser, legacy.Id).Error != nil || db.Find(&activeBindings).Error != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"run_id": os.Getenv("AGENCY_BROWSER_RUN_ID"), "agencies": agencies, "withdrawals": payments, "balances": balances, "deliveries": receipts, "account_count": len(accounts), "agency_id": agency.ID, "target_agency_id": targetAgency.ID, "legacy_user_id": legacy.Id, "managed_user_id": managed.Id, "legacy_billing_mode": legacyUser.BillingMode, "legacy_quota": legacyUser.Quota, "bindings": activeBindings})
	})
	configJSON, err := common.Marshal(map[string]string{"base_path": "/agency", "platform_base_url": origin})
	require.NoError(t, err)
	index = bytes.Replace(index, []byte("</head>"), []byte("<script>window.__AGENCY_CONFIG__="+string(configJSON)+"</script></head>"), 1)
	assets := http.StripPrefix("/agency/", http.FileServer(http.Dir(dist)))
	finished := make(chan struct{})
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture/shutdown" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			select {
			case <-finished:
			default:
				close(finished)
			}
			return
		}
		if r.URL.Path == "/agency" || r.URL.Path == "/agency/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(index)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/agency/assets/") {
			assets.ServeHTTP(w, r)
			return
		}
		router.ServeHTTP(w, r)
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	t.Logf("Agency browser fixture listening at %s (SQLite, fixture gateway, no external calls)", origin)
	<-finished
}

const browserVerificationBridge = `<!doctype html><meta charset="utf-8"><script>
addEventListener('message', async event => {
  if (event.source !== parent || event.origin !== location.origin || event.data?.source !== 'agency-hub-verification') return;
  const result = await fetch('/__fixture/proof', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(event.data.payload)});
  const data = await result.json();
  parent.postMessage({source:'new-api-agency-verification',request_id:event.data.request_id,ok:result.ok,proof:data.proof,error:data.error},location.origin);
});
parent.postMessage({source:'new-api-agency-verification',ready:true},location.origin);
</script>`
