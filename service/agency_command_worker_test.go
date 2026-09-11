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
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
