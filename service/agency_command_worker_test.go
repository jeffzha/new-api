package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOpsAgencyPythonHTTPAcceptance(t *testing.T) {
	raw := os.Getenv("OPS_AGENCY_HTTP_FIXTURE")
	if raw == "" {
		t.Skip("run from TokenOps HTTP acceptance script")
	}
	var fixture struct {
		URL          string `json:"url"`
		GatewayID    string `json:"gatewayId"`
		GatewayKey   string `json:"gatewayKey"`
		SigningKey   string `json:"signingKey"`
		CommandCount int    `json:"commandCount"`
	}
	require.NoError(t, common.Unmarshal([]byte(raw), &fixture))
	require.Equal(t, 3, fixture.CommandCount)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/gateway.sqlite"), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		pool, poolErr := db.DB()
		if poolErr == nil {
			_ = pool.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	require.NoError(t, model.MigrateAgency(db))
	policy := agencycontract.Policy{Revision: 1, DefaultSettlementBPS: 7500, DefaultChildCostBPS: 9000, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	encoded, err := common.Marshal(policy)
	require.NoError(t, err)
	policyHash, err := agencycontract.CanonicalHash(policy)
	require.NoError(t, err)
	version := model.AgencyPricePolicyVersion{AgencyID: 7, Revision: 1, PolicyJSON: string(encoded), PolicyHash: policyHash}
	require.NoError(t, db.Create(&version).Error)
	require.NoError(t, db.Create(&model.Agency{ID: 7, Code: "ops-http", InviteCode: "OPSHTTP", Status: agencyhub.AgencyStatusActive, PriceRevision: 1, CurrentPolicyVersionID: version.ID}).Error)
	require.NoError(t, db.Create(&model.User{Id: 42, Username: "ops-http-customer", AffCode: "opshttp", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}).Error)
	binding := model.AgencyUserBinding{UserID: 42, AgencyID: 7, Revision: 1, InviteSnapshot: "OPSHTTP", CreatedSource: "test"}
	require.NoError(t, db.Create(&binding).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: 42, AgencyID: 7, BindingID: binding.ID, Revision: 1}).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSession{SID: "ops-http-session", UserID: 42, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", ExpiresAt: now + 3600, LastActiveAt: now}).Error)
	bridge, err := opsbridge.Open(opsbridge.Config{URL: fixture.URL, GatewayID: fixture.GatewayID, Key: fixture.GatewayKey, CommandSigningKey: fixture.SigningKey, StateDir: t.TempDir(), CommandExecutor: OpsAgencyCommandExecutor{}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bridge.Close()) })
	for index := range fixture.CommandCount {
		processed, err := bridge.ProcessAgencyCommand(t.Context(), time.Now())
		require.NoError(t, err)
		require.True(t, processed)
		var user model.User
		require.NoError(t, db.First(&user, 42).Error)
		if index == 0 {
			assert.Equal(t, common.UserStatusDisabled, user.Status)
			assert.EqualValues(t, 2, user.AuthVersion)
			var session model.UserSession
			require.NoError(t, db.Where("sid = ?", "ops-http-session").First(&session).Error)
			assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		} else {
			assert.Equal(t, common.UserStatusEnabled, user.Status)
			assert.EqualValues(t, 3, user.AuthVersion)
		}
	}
	var agency model.Agency
	require.NoError(t, db.First(&agency, 7).Error)
	assert.EqualValues(t, 2, agency.PriceRevision)
	var applied model.AgencyPricePolicyVersion
	require.NoError(t, db.First(&applied, agency.CurrentPolicyVersionID).Error)
	var stored agencycontract.Policy
	require.NoError(t, common.Unmarshal([]byte(applied.PolicyJSON), &stored))
	assert.Equal(t, 7500, stored.DefaultSettlementBPS)
	assert.Equal(t, 9000, stored.DefaultSalesBPS)
	processed, err := bridge.ProcessAgencyCommand(t.Context(), time.Now())
	require.NoError(t, err)
	assert.False(t, processed)
}

func TestOpsAgencyPolicyMapping(t *testing.T) {
	base := map[string]any{"modelName": "*", "costMultiplier": "0.75", "childCostMultiplier": "0.85", "saleMultiplier": "0.9", "minSpreadMultiplier": "0.05", "maxSaleMultiplier": "3"}
	modelRule := map[string]any{"modelName": "model", "costMultiplier": "0.8", "childCostMultiplier": "0.95", "saleMultiplier": "1"}
	customer := map[string]any{"modelName": "model", "customerId": "ops-customer", "externalUserId": "42", "costMultiplier": "0.8", "saleMultiplier": "1.1"}
	for _, rules := range [][]any{{customer, modelRule, base}, {base, modelRule, customer}} {
		policy, customers, children, err := mapOpsAgencyPolicy(rules)
		require.NoError(t, err)
		require.NoError(t, agencycontract.ValidatePolicy(policy))
		assert.Equal(t, 7500, policy.DefaultSettlementBPS)
		assert.Equal(t, 8500, policy.DefaultChildCostBPS)
		assert.Equal(t, 9000, policy.DefaultSalesBPS)
		assert.Empty(t, children)
		resolved, err := agencycontract.Resolve(policy, "model")
		require.NoError(t, err)
		assert.Equal(t, 8000, resolved.SettlementBPS)
		assert.Equal(t, 10000, resolved.SalesBPS)
		require.Len(t, policy.ModelOverrides, 1)
		require.NotNil(t, policy.ModelOverrides[0].ChildCostBPS)
		assert.Equal(t, 9500, *policy.ModelOverrides[0].ChildCostBPS)
		require.Len(t, customers, 1)
		assert.Equal(t, 11000, customers[0].SalesBPS)
	}
	for _, tc := range []struct {
		name string
		rule map[string]any
	}{
		{"child offer", map[string]any{"modelName": "*", "childAgencyId": "child", "saleMultiplier": "1"}},
		{"model spread", map[string]any{"modelName": "model", "minSpreadMultiplier": "0.1"}},
		{"customer cost", map[string]any{"modelName": "*", "customerId": "customer", "externalUserId": "42", "costMultiplier": "0.8", "saleMultiplier": "1"}},
		{"precision", map[string]any{"modelName": "model", "saleMultiplier": "0.90001"}},
		{"missing model", map[string]any{"saleMultiplier": "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := mapOpsAgencyPolicy([]any{base, tc.rule})
			require.Error(t, err)
		})
	}
	_, customers, _, err := mapOpsAgencyPolicy([]any{map[string]any{"modelName": "*", "customerId": "customer", "externalUserId": "42", "saleMultiplier": "1"}, base})
	require.NoError(t, err)
	assert.Equal(t, "*", customers[0].OriginModelName)
	_, _, children, err := mapOpsAgencyPolicy([]any{base, map[string]any{"modelName": "*", "childAgencyId": "child", "externalChildAgencyId": "8", "childCostMultiplier": "0.88", "saleMultiplier": "1.1"}})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.EqualValues(t, 8, children[0].AgencyID)
	assert.Equal(t, 8800, children[0].SettlementBPS)
	require.NotNil(t, children[0].SalesBPS)
	assert.Equal(t, 11000, *children[0].SalesBPS)
}

func TestOpsAgencyCustomerCommandsEnforceBindingAndRevokeSessions(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:ops-customer-commands?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		pool, err := db.DB()
		if err == nil {
			_ = pool.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	require.NoError(t, model.MigrateAgency(db))
	agency := model.Agency{ID: 7, Code: "ops-test", InviteCode: "OPS", Status: agencyhub.AgencyStatusActive}
	require.NoError(t, db.Create(&agency).Error)
	user := model.User{Id: 42, Username: "ops-customer", AffCode: "opscustomer", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	command := opsbridge.OpsAgencyCommand{ID: "ops-customer-1", Kind: "provision_customer", Payload: map[string]any{"externalAgencyId": "7", "externalUserId": "42"}}
	executor := OpsAgencyCommandExecutor{}
	_, err = executor.ExecuteOpsAgencyCommand(t.Context(), command)
	require.ErrorContains(t, err, "gateway_root_provisioning_required")
	var count int64
	require.NoError(t, db.Model(&model.AgencyActiveUserBinding{}).Count(&count).Error)
	assert.Zero(t, count)
	binding := model.AgencyUserBinding{UserID: 42, AgencyID: 7, Revision: 1, InviteSnapshot: "OPS", CreatedSource: "test"}
	require.NoError(t, db.Create(&binding).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: 42, AgencyID: 7, BindingID: binding.ID, Revision: 1}).Error)
	_, err = executor.ExecuteOpsAgencyCommand(t.Context(), command)
	require.NoError(t, err)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSession{SID: "ops-customer-session", UserID: 42, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", ExpiresAt: now + 3600, LastActiveAt: now}).Error)
	command.Kind = "suspend_customer"
	_, err = executor.ExecuteOpsAgencyCommand(t.Context(), command)
	require.NoError(t, err)
	stored := model.User{}
	require.NoError(t, db.First(&stored, 42).Error)
	assert.Equal(t, common.UserStatusDisabled, stored.Status)
	assert.EqualValues(t, 2, stored.AuthVersion)
	var session model.UserSession
	require.NoError(t, db.First(&session, "sid = ?", "ops-customer-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
	command.Payload["externalAgencyId"] = "8"
	_, err = executor.ExecuteOpsAgencyCommand(t.Context(), command)
	require.Error(t, err)
	command.Payload["externalAgencyId"] = "7"
	command.Kind = "resume_customer"
	_, err = executor.ExecuteOpsAgencyCommand(t.Context(), command)
	require.NoError(t, err)
	require.NoError(t, db.First(&stored, 42).Error)
	assert.Equal(t, common.UserStatusEnabled, stored.Status)
	assert.EqualValues(t, 3, stored.AuthVersion)
}

func TestAgencyCommandWorkerExecutesProvisioningCancelWithFreshRootSession(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	db, err := gorm.Open(sqlite.Open("file:agency-command-worker-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	require.NoError(t, model.MigrateAgency(db))

	root := model.User{Id: 1, Username: "command-root", AffCode: "rootcmd", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&root).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSession{
		SID: "command-root-session", UserID: root.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "test-refresh-hash",
		LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)
	user := model.User{Id: 2, Username: "legacy-customer", AffCode: "customercmd", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, BillingMode: model.AgencyProvisioningBillingMode}
	require.NoError(t, db.Create(&user).Error)
	job := model.AgencyProvisioningJob{
		UserID: int64(user.Id), InviteCode: "INVITE123", RootActorID: int64(root.Id),
		ExpectedUserVersion: user.AuthVersion, Status: "queued", FencingToken: 0,
		CreatedAtMS: time.Now().UnixMilli(), UpdatedAtMS: time.Now().UnixMilli(),
	}
	require.NoError(t, db.Create(&job).Error)

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	require.NoError(t, err)
	keyFile := t.TempDir() + "/agency-public.pem"
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0600))
	t.Setenv("AGENCY_SSO_PUBLIC_KEY_FILE", keyFile)
	servicePublic, servicePrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	serviceDER, err := x509.MarshalPKIXPublicKey(servicePublic)
	require.NoError(t, err)
	serviceFile := t.TempDir() + "/agency-command-service-public.pem"
	require.NoError(t, os.WriteFile(serviceFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: serviceDER}), 0600))
	t.Setenv("AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE", serviceFile)

	payload := []byte(`{"user_id":2,"reason":"operator cancelled"}`)
	bodyHash, err := agencyhub.CommandBodyHash(payload)
	require.NoError(t, err)
	commandID := "command-cancel-1"
	expires := time.Now().Add(2 * time.Minute).Unix()
	proof, err := agencyhub.SignSSOTicket(privateKey, agencyhub.SSOTicketClaims{
		Issuer: "new-api", Audience: "agency-gateway-command", Subject: int64(root.Id),
		SourceSID: "command-root-session", UserAuthVersion: 1, SessionVersion: 1,
		JTI: "command-cancel-proof-1", KeyID: "root-k1", Action: agencyhub.CommandActionProvisioningCancel,
		CommandID: commandID, ObjectID: strconv.FormatInt(job.ID, 10), ExpectedVersion: job.FencingToken,
		BodyHash: bodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: expires,
	})
	require.NoError(t, err)
	request := agencyhub.AgencyCommandRequest{
		CommandID: commandID, Action: agencyhub.CommandActionProvisioningCancel,
		Actor: "root:1", SourceSID: "command-root-session",
		ObjectID: strconv.FormatInt(job.ID, 10), ExpectedVersion: job.FencingToken,
		Payload: payload, IssuedAt: now, ExpiresAt: expires, BodyHash: bodyHash, RootProof: proof,
	}
	serviceSignature, err := agencyhub.SignCommandEnvelope(servicePrivate, request)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyCommand{
		CommandID: commandID, Action: agencyhub.CommandActionProvisioningCancel,
		Actor: "root:1", SourceSID: "command-root-session",
		ObjectID: strconv.FormatInt(job.ID, 10), ExpectedVersion: job.FencingToken,
		Payload: string(payload), BodyHash: bodyHash, IssuedAt: now, ExpiresAt: expires,
		HubSignature: serviceSignature, RootProof: proof, RootProofJTI: "command-cancel-proof-1",
		Status: agencyCommandQueued, CreatedAt: now, UpdatedAt: now,
	}).Error)

	summary, err := RunAgencyCommandWorkerOnce(t.Context(), 1)
	require.NoError(t, err)
	var command model.AgencyCommand
	require.NoError(t, db.Where("command_id = ?", commandID).First(&command).Error)
	require.Equal(t, 1, summary.Succeeded)
	require.Equal(t, agencyCommandCancelled, command.Status)
	var storedJob model.AgencyProvisioningJob
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.Equal(t, "cancelled", storedJob.Status)
	var storedUser model.User
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	require.Equal(t, "legacy", storedUser.BillingMode)
}

func TestFundingReversalRollsBackWhenCommissionProjectionCannotBeReversed(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	db, err := gorm.Open(sqlite.Open("file:agency-funding-cross-transaction-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, model.MigrateAgency(db))
	agencyID := int64(7)
	bindingID := int64(70)
	user := model.User{
		Username: "funding-cross-transaction-user", Password: "password",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 100,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Create(&model.AgencyUserBinding{UserID: int64(user.Id), AgencyID: agencyID, Revision: 1, InviteSnapshot: "ATOMIC", CreatedSource: "test", EffectiveAtMS: 1, CreatedAt: 1}).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: int64(user.Id), BindingID: bindingID, Revision: 1, AgencyID: agencyID, UpdatedAt: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := model.RecordAgencyTopup(tx, int64(user.Id), "payment", "topup-cross-transaction", "payment_callback", 100, 0); err != nil {
			return err
		}
		_, err := model.TryReserveUserQuotaAndAgencyTx(tx, user.Id, 100, "charge-cross-transaction", 100)
		return err
	}))

	// No original billing journal exists yet. The command must fail and roll
	// back the already-applied funding reversal instead of leaving a half
	// reversed wallet.
	err = reverseAgencyTopup(agencyFundingReverseCommand{
		OriginalOperationID: "topup-cross-transaction", RefundID: "refund-cross-transaction",
		UserID: int64(user.Id), RefundQuota: 100, CurrencyCode: "CNY",
	}, 100)
	require.Error(t, err)
	var stored model.User
	require.NoError(t, db.First(&stored, user.Id).Error)
	require.Equal(t, 0, stored.Quota)
	var reversalCount int64
	require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Where("refund_id = ?", "refund-cross-transaction").Count(&reversalCount).Error)
	require.Equal(t, int64(0), reversalCount)
	var lot model.AgencyFundingLot
	require.NoError(t, db.Where("source_id = ?", "topup-cross-transaction").First(&lot).Error)
	require.Equal(t, int64(100), lot.PaidConsumed)

	original := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: "billing-cross-original",
		EventType: "agency.billing_finalized", FinancialChargeID: "charge-cross-transaction",
		OperationID: "billing-cross-operation", SegmentNo: 0, JournalRevision: 1,
		OccurredAtMS: 1, UserID: int64(user.Id), AgencyID: &agencyID, BindingID: &bindingID,
		OriginModelName: "model", CurrencyCode: "CNY", CommissionEligible: true,
		ChargedTotalQuota: 100, CommissionAmountMicros: 100,
	}
	payload, err := common.Marshal(original)
	require.NoError(t, err)
	hash, err := agencycontract.CanonicalHash(original)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyBillingOutbox{
		EventID: original.EventID, OperationID: original.OperationID, EventIndex: 0,
		EventCount: 1, EventKind: original.EventType, UserID: original.UserID,
		Payload: string(payload), PayloadHash: hash, SchemaVersion: original.SchemaVersion,
		CreatedAtMS: original.OccurredAtMS,
	}).Error)
	require.NoError(t, db.Create(&model.AgencyBillingOperation{
		ChargeID: "charge-cross-transaction", SegmentNo: 0, Revision: 1,
		Operation: "finalize", InputHash: hash, CommittedResult: string(payload),
		EventCount: 1, CreatedAtMS: 1,
	}).Error)
	require.NoError(t, db.Create(&model.AgencyBillingJournal{
		ChargeID: "charge-cross-transaction", SegmentNo: 0, UserID: int64(user.Id),
		Status: "finalized", BusinessStatus: "success", DeliveryStatus: "done",
		PricingSnapshot: "{}", BillingBasis: "test", ChargedTotalQuota: 100,
		CommissionableQuota: 100, CommissionAmountMicros: 100, CurrencyCode: "CNY",
		Revision: 1, Version: 1, CreatedAtMS: 1, UpdatedAtMS: 1,
	}).Error)

	require.NoError(t, reverseAgencyTopup(agencyFundingReverseCommand{
		OriginalOperationID: "topup-cross-transaction", RefundID: "refund-cross-transaction",
		UserID: int64(user.Id), RefundQuota: 100, CurrencyCode: "CNY",
	}, 100))
	require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Where("refund_id = ?", "refund-cross-transaction").Count(&reversalCount).Error)
	require.Equal(t, int64(1), reversalCount)
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", "charge-cross-transaction").First(&journal).Error)
	require.Equal(t, int64(100), journal.ReversedQuota)
	require.Equal(t, int64(100), journal.ReversedCommissionQuota)
	var fundingOutboxCount, commissionOutboxCount int64
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("event_kind = ?", "agency.funding_reversed").Count(&fundingOutboxCount).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("event_kind = ?", "agency.billing_reversed").Count(&commissionOutboxCount).Error)
	require.Equal(t, int64(1), fundingOutboxCount)
	require.Equal(t, int64(1), commissionOutboxCount)
}
