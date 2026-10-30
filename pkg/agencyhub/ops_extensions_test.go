package agencyhub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOpsExtensionsAcrossDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "ops.sqlite"))
			if dialect != "sqlite" {
				dsn := os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("disposable external database is not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, model.MigrateAgency(db))
			require.NoError(t, model.MigrateAgency(db))
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Task{}, &model.Channel{}, &model.Ability{}))
			db = db.Begin()
			require.NoError(t, db.Error)
			t.Cleanup(func() { require.NoError(t, db.Rollback().Error) })
			t.Setenv("AGENCY_ONBOARDING_ENABLED", "true")
			app := New(db, db, Config{SalesCapBPS: 30000, WithdrawalsEnabled: true})
			root := model.User{Username: "ops-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(&root).Error)
			channel := model.Channel{Name: "ops-route", Type: 1, Key: "unused", Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(&channel).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "ops-model", ChannelId: channel.Id, Enabled: true}).Error)
			policy := agencycontract.Policy{DefaultSettlementBPS: 7000, DefaultChildCostBPS: 8500,
				DefaultSalesBPS: 10000, MinSpreadBPS: 500, SalesCapBPS: 30000}
			agency, _, err := app.CreateAgency(int64(root.Id), "OPS Partner", "ops-partner", policy)
			require.NoError(t, err)
			before, err := ReadOpsPlatformPriceSnapshot(db, int64(root.Id))
			require.NoError(t, err)
			tenantPolicy := policy
			tenantPolicy.DefaultChildCostBPS = 8000
			proposal := OpsPlatformPriceCommand{RootID: int64(root.Id), CommandID: "tenant-price-1", ExpectedSnapshot: before.Fingerprint,
				Policy: agencycontract.PlatformPolicy{Revision: before.Revision, TenantPolicy: &tenantPolicy,
					ModelPrices: []agencycontract.PlatformModelPrice{{OriginModelName: "ops-model",
						ChannelCosts:  []agencycontract.PlatformChannelCost{{ChannelID: channel.Id, PlatformCostBPS: 6000}},
						AgencyCostBPS: 8500, DefaultSalesBPS: 10000}},
					RootAgencyCosts: []agencycontract.RootAgencyCost{{AgencyID: agency.ID, OriginModelName: "ops-model", SettlementBPS: 8000}}}}
			receipt, err := ApplyOpsPlatformPrice(db, proposal)
			require.NoError(t, err)
			fresh, err := ReadOpsPriceSnapshot(db, agency.ID)
			require.NoError(t, err)
			quote, err := agencycontract.Resolve(fresh.Policy, "ops-model")
			require.NoError(t, err)
			assert.Equal(t, 8000, quote.SettlementBPS)
			replay, err := ApplyOpsPlatformPrice(db, proposal)
			require.NoError(t, err)
			assert.EqualValues(t, receipt["revision"], replay["revision"])
			stale := proposal
			stale.CommandID = "tenant-price-stale"
			_, err = ApplyOpsPlatformPrice(db, stale)
			require.ErrorIs(t, err, agencycontract.ErrPolicyRevision)
			proposal.Policy.RootAgencyCosts[0].SettlementBPS = 8100
			_, err = ApplyOpsPlatformPrice(db, proposal)
			require.ErrorContains(t, err, "identity conflict")
			nativePolicy := fresh.Policy
			nativePolicy.DefaultSettlementBPS = 8000
			nativePolicy.DefaultChildCostBPS = 8600
			for i := range nativePolicy.ModelOverrides {
				cost := 8100
				child := 8600
				nativePolicy.ModelOverrides[i].SettlementBPS = &cost
				nativePolicy.ModelOverrides[i].ChildCostBPS = &child
			}
			body, err := common.Marshal(rootPricingRequest{ExpectedRevision: fresh.Revision,
				DefaultSettlementBPS: nativePolicy.DefaultSettlementBPS,
				DefaultChildCostBPS:  nativePolicy.DefaultChildCostBPS, DefaultSalesBPS: nativePolicy.DefaultSalesBPS,
				MinSpreadBPS: nativePolicy.MinSpreadBPS, SalesCapBPS: nativePolicy.SalesCapBPS, ModelOverrides: nativePolicy.ModelOverrides})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			context.Params = gin.Params{{Key: "id", Value: stringID(agency.ID)}}
			context.Set("agency_identity", &Identity{ActorType: ActorTypeRoot, ActorID: int64(root.Id)})
			app.publishRootPricing(context)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			reverse, err := ReadOpsPlatformPriceSnapshot(db, int64(root.Id))
			require.NoError(t, err)
			assert.Greater(t, reverse.Revision, before.Revision)
			assert.Contains(t, reverse.Policy.RootAgencyCosts, agencycontract.RootAgencyCost{
				AgencyID: agency.ID, OriginModelName: "ops-model", SettlementBPS: 8100})
			fresh, err = ReadOpsPriceSnapshot(db, agency.ID)
			require.NoError(t, err)
			quote, err = agencycontract.Resolve(fresh.Policy, "ops-model")
			require.NoError(t, err)
			assert.Equal(t, 8100, quote.SettlementBPS)
			runOpsWithdrawalLifecycle(t, app, int64(root.Id), agency.ID)
			runOpsIdentityLifecycle(t, app, int64(root.Id), agency.ID)
		})
	}
}

func runOpsIdentityLifecycle(t *testing.T, app *App, root, agency int64) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	user := model.User{Username: "ops-source-user", AffCode: "ops-source-invite", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&user).Error)
	source := model.UserSession{SID: "ops-source-session", UserID: user.Id, Version: 1,
		UserAuthVersion: 1, Status: model.UserSessionStatusActive, ExpiresAt: time.Now().Unix() + 3600}
	require.NoError(t, app.db.Create(&source).Error)
	var operator model.AgencyOperatorAccount
	require.NoError(t, app.db.Where("agency_id = ?", agency).First(&operator).Error)
	claims := SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-account-link", KeyID: "fixture",
		Subject: int64(user.Id), ActorType: ActorTypePlatformUser, SourceSID: source.SID,
		UserAuthVersion: 1, SessionVersion: 1, JTI: "ops-link-ticket", ObjectID: "ops-browser-request"}
	ticket, err := SignSSOTicket(private, claims)
	require.NoError(t, err)
	command := OpsIdentityCommand{RootID: root, OperatorID: operator.ID, Action: "link", Ticket: ticket, RequestID: claims.ObjectID}
	result, err := ApplyOpsIdentity(app.db, public, command)
	require.NoError(t, err)
	assert.Equal(t, true, result["linked"])
	_, err = ApplyOpsIdentity(app.db, public, command)
	require.Error(t, err, "single-use proof cannot be replayed")
	claims.Audience, claims.ActorType, claims.ActorID, claims.AgencyID = "tokenops-distributor", ActorTypeOperator, operator.ID, agency
	claims.JTI, claims.StateHash = "ops-login-ticket", strings.Repeat("a", 64)
	ticket, err = SignSSOTicket(private, claims)
	require.NoError(t, err)
	command.Action, command.Ticket, command.StateHash = "login", ticket, strings.Repeat("b", 64)
	_, err = ApplyOpsIdentity(app.db, public, command)
	require.Error(t, err, "another browser cannot consume the ticket")
	command.StateHash = claims.StateHash
	result, err = ApplyOpsIdentity(app.db, public, command)
	require.NoError(t, err)
	assert.Equal(t, operator.ID, result["operatorId"])
	claims.JTI = "ops-revoked-session"
	ticket, err = SignSSOTicket(private, claims)
	require.NoError(t, err)
	require.NoError(t, app.db.Model(&source).Update("revoked_at", time.Now().Unix()).Error)
	command.Ticket = ticket
	_, err = ApplyOpsIdentity(app.db, public, command)
	require.Error(t, err)
	command.Action = "unlink"
	result, err = ApplyOpsIdentity(app.db, public, command)
	require.NoError(t, err)
	assert.Equal(t, false, result["linked"])
}

func runOpsWithdrawalLifecycle(t *testing.T, app *App, root, agency int64) {
	t.Helper()
	require.NoError(t, app.db.Model(&model.AgencyOpsWriterState{}).Where("id = ?", 1).
		Update("mode", model.AgencyWriterSourcePrimary).Error)
	account := model.AgencyWithdrawalAccount{AgencyID: agency, Version: 1, Status: "active", Ciphertext: "fixture-cipher", KeyID: "fixture"}
	require.NoError(t, app.db.Create(&account).Error)
	require.NoError(t, app.db.Create(&model.AgencyCommissionBalance{AgencyID: agency, CurrencyCode: "CNY",
		EarnedMicros: 100000000, AvailableMicros: 100000000, Version: 1}).Error)
	created, err := ApplyOpsWithdrawal(app.db, OpsWithdrawalCommand{RootID: root, AgencyID: agency,
		AccountID: account.ID, AccountVersion: 1, AmountMicros: 10000000, Currency: "CNY", CommandID: "withdrawal-create-1"}, app.config)
	require.NoError(t, err)
	var withdrawal model.AgencyWithdrawal
	require.NoError(t, app.db.Where("agency_id = ?", agency).First(&withdrawal).Error)
	assert.Equal(t, "submitted", created["status"])
	command := OpsWithdrawalAction{RootID: root, AgencyID: agency, WithdrawalID: withdrawal.ID,
		ActorKey: "tenant:reviewer-one", ExpectedVersion: 1, Reason: "Verified application"}
	var lease string
	for _, target := range []string{"reviewing", "approved", "paying", "paid"} {
		command.CommandID, command.Target = "withdrawal-"+target, target
		if target == "paid" {
			command.PaymentChannel, command.PaymentReference = "bank", "fixture-bank-reference"
			require.NoError(t, command.PaymentLease.UnmarshalJSON([]byte(`"`+lease+`"`)))
		}
		result, err := ApplyOpsWithdrawalAction(app.db, command, app.config)
		require.NoError(t, err)
		replay, err := ApplyOpsWithdrawalAction(app.db, command, app.config)
		require.NoError(t, err)
		expectedJSON, err := common.Marshal(result["record"])
		require.NoError(t, err)
		actualJSON, err := common.Marshal(replay["record"])
		require.NoError(t, err)
		assert.JSONEq(t, string(expectedJSON), string(actualJSON))
		if target == "paying" {
			lease = result["paymentLease"].(string)
			other := command
			other.CommandID, other.Target, other.ActorKey = "withdrawal-other-actor", "payment_unknown", "tenant:reviewer-two"
			other.ExpectedVersion++
			_, err := ApplyOpsWithdrawalAction(app.db, other, app.config)
			require.ErrorContains(t, err, "another root")
		}
		command.ExpectedVersion++
	}
	var balance model.AgencyCommissionBalance
	require.NoError(t, app.db.Where("agency_id = ?", agency).First(&balance).Error)
	assert.Zero(t, balance.LockedMicros)
	assert.EqualValues(t, 10000000, balance.PaidMicros)
	assert.EqualValues(t, 90000000, balance.AvailableMicros)
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyWithdrawalPaymentReference{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
