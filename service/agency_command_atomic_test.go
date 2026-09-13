package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func agencyCommandAtomicFixture(t *testing.T, dialect string) (*gorm.DB, model.User, model.User) {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "mysql", "postgres":
		if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" {
			t.Skip("isolated external test database is not configured")
		}
		dsn := strings.TrimSpace(os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN"))
		if dsn == "" {
			t.Skip("isolated external test database is not configured")
		}
		if dialect == "mysql" {
			driver = mysql.Open(dsn)
		} else {
			driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}
	default:
		driver = sqlite.Open("file:agency-command-atomic-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	}
	connection, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	pool, err := connection.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	require.NoError(t, connection.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Task{}))
	require.NoError(t, model.MigrateAgency(connection))
	// Keep isolated external fixtures after their schema migration inside an
	// outer transaction. All worker operations still use their real savepoints.
	db := connection.Begin()
	require.NoError(t, db.Error)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		require.NoError(t, db.Rollback().Error)
		require.NoError(t, pool.Close())
	})
	root := model.User{Username: "atomic-root", AffCode: "atomic-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&root).Error)
	user := model.User{Username: "atomic-customer", AffCode: "atomic-customer", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{SID: "atomic-root-session", UserID: root.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "atomic-session-hash", ExpiresAt: time.Now().Add(time.Hour).Unix()}).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.RecordAgencyTopup(tx, int64(user.Id), "payment", "atomic-topup", "payment_callback", 100, 0)
	}))
	return db, root, user
}

func createAtomicAgencyCommand(t *testing.T, db *gorm.DB, root model.User, action, object string, version int64, payload any) model.AgencyCommand {
	t.Helper()
	keys := make([]ed25519.PrivateKey, 2)
	for index, variable := range []string{"AGENCY_SSO_PUBLIC_KEY_FILE", "AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE"} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		keys[index] = private
		der, err := x509.MarshalPKIXPublicKey(public)
		require.NoError(t, err)
		path := t.TempDir() + "/public.pem"
		require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600))
		t.Setenv(variable, path)
	}
	raw, err := common.Marshal(payload)
	require.NoError(t, err)
	hash, err := agencyhub.CommandBodyHash(raw)
	require.NoError(t, err)
	now := time.Now().Unix()
	command := model.AgencyCommand{CommandID: "atomic-command", Action: action, Actor: fmt.Sprintf("root:%d", root.Id), SourceSID: "atomic-root-session",
		ObjectID: object, ExpectedVersion: version, Payload: string(raw), BodyHash: hash, IssuedAt: now, ExpiresAt: now + 120,
		RootProofJTI: "atomic-proof", Status: agencyCommandQueued, CreatedAt: now, UpdatedAt: now}
	command.RootProof, err = agencyhub.SignSSOTicket(keys[0], agencyhub.SSOTicketClaims{Issuer: "new-api", Audience: "agency-gateway-command", Subject: int64(root.Id),
		SourceSID: command.SourceSID, UserAuthVersion: 1, SessionVersion: 1, JTI: command.RootProofJTI, KeyID: "test", Action: action,
		CommandID: command.CommandID, ObjectID: object, ExpectedVersion: version, BodyHash: hash, IssuedAt: now, NotBefore: now, ExpiresAt: command.ExpiresAt})
	require.NoError(t, err)
	command.HubSignature, err = agencyhub.SignCommandEnvelope(keys[1], agencyhub.AgencyCommandRequest{CommandID: command.CommandID, Action: action, Actor: command.Actor,
		SourceSID: command.SourceSID, ObjectID: object, ExpectedVersion: version, Payload: raw, IssuedAt: now, ExpiresAt: command.ExpiresAt, BodyHash: hash, RootProof: command.RootProof})
	require.NoError(t, err)
	require.NoError(t, db.Create(&command).Error)
	return command
}

func TestAgencyCommandAtomicExecutionAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Run("funding_receipt", func(t *testing.T) { testAgencyCommandFundingAndReceiptCommitAtomically(t, dialect) })
			t.Run("current_authorization", func(t *testing.T) { testAgencyCommandExecutionRechecksCurrentRootAndJobVersion(t, dialect) })
			for _, conflict := range []string{"quota_aliases", "original_reference"} {
				t.Run(conflict, func(t *testing.T) {
					db, root, user := agencyCommandAtomicFixture(t, dialect)
					payload := agencyFundingReverseCommand{OriginalOperationID: "atomic-topup", RefundID: "atomic-refund", UserID: int64(user.Id), RefundQuota: 25}
					if conflict == "quota_aliases" {
						payload.Quota = 40
					} else {
						payload.OriginalEventID = "agency-topup-other-payment"
					}
					createAtomicAgencyCommand(t, db, root, agencyhub.CommandActionFundingReverse, "atomic-topup", 0, payload)
					summary, err := RunAgencyCommandWorkerOnce(t.Context(), 1)
					require.NoError(t, err)
					assert.Equal(t, 1, summary.Failed)
					require.NoError(t, db.First(&user, user.Id).Error)
					assert.Equal(t, 100, user.Quota)
					var count int64
					require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Count(&count).Error)
					assert.Zero(t, count)
				})
			}
		})
	}
}

func testAgencyCommandFundingAndReceiptCommitAtomically(t *testing.T, dialect string) {
	db, root, user := agencyCommandAtomicFixture(t, dialect)
	command := createAtomicAgencyCommand(t, db, root, agencyhub.CommandActionFundingReverse, "atomic-topup", 0,
		agencyFundingReverseCommand{OriginalOperationID: "atomic-topup", RefundID: "atomic-refund", UserID: int64(user.Id), RefundQuota: 25, Reason: "confirmed payment refund"})
	claimed, err := claimAgencyCommand(command.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	failure := errors.New("injected final command receipt failure")
	const callback = "agency_atomic_test:receipt"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AgencyCommand{}).TableName() {
			tx.AddError(failure)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	require.ErrorIs(t, executeAgencyCommand(t.Context(), command), failure)
	var wallet model.User
	var account model.AgencyFundingAccount
	var topup model.AgencyTopupFact
	var stored model.AgencyCommand
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&account).Error)
	require.NoError(t, db.Where("source_operation_id = ?", "atomic-topup").First(&topup).Error)
	require.NoError(t, db.First(&stored, command.ID).Error)
	assert.Equal(t, 100, wallet.Quota)
	assert.Equal(t, int64(100), account.PaidAvailable)
	assert.Zero(t, topup.RefundedQuota)
	assert.Equal(t, agencyCommandProcessing, stored.Status)
	var reversals, events int64
	require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Count(&reversals).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("event_kind = ?", "agency.funding_reversed").Count(&events).Error)
	assert.Zero(t, reversals)
	assert.Zero(t, events)

	require.NoError(t, db.Callback().Update().Remove(callback))
	require.NoError(t, executeAgencyCommand(t.Context(), command))
	require.NoError(t, db.First(&stored, command.ID).Error)
	assert.Equal(t, agencyCommandSucceeded, stored.Status)
	assert.Equal(t, 200, stored.ResultCode)
	assert.JSONEq(t, `{"user_id":`+fmt.Sprint(user.Id)+`,"requested_quota":25,"refunded_quota":25}`, stored.ResultJSON)
	originalReceipt := stored
	// Re-delivery after the original Root session is revoked still returns
	// the committed result; it does not initiate a second business execution.
	require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", command.SourceSID).Update("revoked_at", time.Now().Unix()).Error)
	require.NoError(t, executeAgencyCommand(t.Context(), command))
	require.NoError(t, db.First(&stored, command.ID).Error)
	assert.Equal(t, originalReceipt, stored)
	require.NoError(t, db.First(&wallet, user.Id).Error)
	assert.Equal(t, 75, wallet.Quota)
	require.NoError(t, db.Model(&model.AgencyFundingReversal{}).Count(&reversals).Error)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("event_kind = ?", "agency.funding_reversed").Count(&events).Error)
	assert.Equal(t, int64(1), reversals)
	assert.Equal(t, int64(1), events)
}

func testAgencyCommandExecutionRechecksCurrentRootAndJobVersion(t *testing.T, dialect string) {
	for _, scenario := range []string{"root_disabled", "root_demoted", "session_revoked", "session_version_changed", "job_version_changed", "receipt_failure"} {
		t.Run(scenario, func(t *testing.T) {
			db, root, user := agencyCommandAtomicFixture(t, dialect)
			require.NoError(t, db.Model(&user).Update("billing_mode", model.AgencyProvisioningBillingMode).Error)
			job := model.AgencyProvisioningJob{UserID: int64(user.Id), RootActorID: int64(root.Id), InviteCode: "test-invite", Status: "queued", FencingToken: 4}
			require.NoError(t, db.Create(&job).Error)
			command := createAtomicAgencyCommand(t, db, root, agencyhub.CommandActionProvisioningCancel, fmt.Sprint(job.ID), 4,
				agencyProvisioningCancelCommand{UserID: int64(user.Id), Reason: "operator cancelled"})
			switch scenario {
			case "root_disabled":
				require.NoError(t, db.Model(&root).Update("status", common.UserStatusDisabled).Error)
			case "root_demoted":
				require.NoError(t, db.Model(&root).Update("role", common.RoleCommonUser).Error)
			case "session_revoked":
				require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", command.SourceSID).Update("revoked_at", time.Now().Unix()).Error)
			case "session_version_changed":
				require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", command.SourceSID).Update("version", 2).Error)
			case "job_version_changed":
				require.NoError(t, db.Model(&job).Update("fencing_token", 5).Error)
			case "receipt_failure":
				const callback = "agency_atomic_test:cancel_receipt"
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
					updates, ok := tx.Statement.Dest.(map[string]any)
					if ok && tx.Statement.Table == (model.AgencyCommand{}).TableName() && updates["result_code"] == 200 {
						tx.AddError(errors.New("injected cancel receipt failure"))
					}
				}))
				t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(callback)) })
			}
			summary, err := RunAgencyCommandWorkerOnce(t.Context(), 1)
			require.NoError(t, err)
			assert.Equal(t, 1, summary.Failed)
			var storedJob model.AgencyProvisioningJob
			var wallet model.User
			var stored model.AgencyCommand
			require.NoError(t, db.First(&storedJob, job.ID).Error)
			require.NoError(t, db.First(&wallet, user.Id).Error)
			require.NoError(t, db.First(&stored, command.ID).Error)
			assert.Equal(t, "queued", storedJob.Status)
			assert.Equal(t, model.AgencyProvisioningBillingMode, wallet.BillingMode)
			assert.Equal(t, 100, wallet.Quota)
			var auditCount int64
			require.NoError(t, db.Model(&model.AgencyAuditLog{}).Where("action = ?", "provisioning.cancel").Count(&auditCount).Error)
			assert.Zero(t, auditCount)
			if scenario == "job_version_changed" || scenario == "receipt_failure" {
				assert.Equal(t, agencyCommandFailed, stored.Status)
			} else {
				assert.Equal(t, agencyCommandCancelled, stored.Status)
				assert.Equal(t, 403, stored.ResultCode)
			}
		})
	}
}
