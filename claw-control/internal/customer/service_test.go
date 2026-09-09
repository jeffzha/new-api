package customer_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/identity"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/resourcebinding"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrimaryMembershipAndIdentityConfirmation(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customers := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test"))
	identities := identity.New(db)
	first, err := customers.Create(customer.CreateCommand{CustomerCode: "customer-one", DisplayName: "Customer One", Actor: "test"})
	require.NoError(t, err)
	second, err := customers.Create(customer.CreateCommand{CustomerCode: "customer-two", DisplayName: "Customer Two", Actor: "test"})
	require.NoError(t, err)

	member, err := customers.AddMember(context.Background(), customer.AddMemberCommand{CustomerID: first.ID, NewAPIUserID: 123, Role: "owner", Actor: "test"})
	require.NoError(t, err)
	secondMember, err := customers.AddMember(context.Background(), customer.AddMemberCommand{CustomerID: second.ID, NewAPIUserID: 123, Role: "owner", Actor: "test"})
	require.NoError(t, err)
	assert.NotEqual(t, member.Identity.PublicID, secondMember.Identity.PublicID)
	assert.Equal(t, model.MembershipSlot(first.ID), member.Member.MembershipSlot)
	assert.Equal(t, model.MembershipSlot(second.ID), secondMember.Member.MembershipSlot)

	command := identity.ConfirmCommand{
		BindingID: member.Identity.PublicID, CanonicalSubject: member.Identity.CanonicalSubject,
		ADPAccountID: "adp-account-123", ADPAccountVersion: 1, Actor: "adp-backend",
	}
	confirmed, err := identities.Confirm(command)
	require.NoError(t, err)
	assert.Equal(t, "active", confirmed.Status)
	confirmedAgain, err := identities.Confirm(command)
	require.NoError(t, err)
	assert.Equal(t, confirmed.ID, confirmedAgain.ID)
	command.ADPAccountID = "different-account"
	_, err = identities.Confirm(command)
	assert.Error(t, err)
}

func TestCustomerAndMemberUpdatesUseOptimisticAuthorizationVersions(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customers := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test"))
	created, err := customers.Create(customer.CreateCommand{CustomerCode: "customer-one", DisplayName: "Customer One", Actor: "test"})
	require.NoError(t, err)
	member, err := customers.AddMember(context.Background(), customer.AddMemberCommand{CustomerID: created.ID, NewAPIUserID: 123, Role: "member", Actor: "test"})
	require.NoError(t, err)

	billingUserID := int64(456)
	updated, err := customers.Update(customer.UpdateCommand{
		CustomerID: created.ID, ExpectedVersion: created.RowVersion, DisplayName: "Renamed Customer",
		BillingUserID: &billingUserID, Actor: "root:1",
	})
	require.NoError(t, err)
	assert.Equal(t, "Renamed Customer", updated.DisplayName)
	assert.Equal(t, created.RowVersion+1, updated.RowVersion)
	_, err = customers.Update(customer.UpdateCommand{
		CustomerID: created.ID, ExpectedVersion: created.RowVersion, DisplayName: "Stale Update", Actor: "root:1",
	})
	assert.Error(t, err)

	updatedMember, err := customers.UpdateMemberRole(customer.UpdateMemberCommand{
		CustomerID: created.ID, NewAPIUserID: 123, ExpectedAuthEpoch: member.Member.AuthEpoch,
		Role: "admin", Actor: "root:1",
	})
	require.NoError(t, err)
	assert.Equal(t, "admin", updatedMember.Role)
	assert.Equal(t, member.Member.AuthEpoch+1, updatedMember.AuthEpoch)
	_, err = customers.UpdateMemberRole(customer.UpdateMemberCommand{
		CustomerID: created.ID, NewAPIUserID: 123, ExpectedAuthEpoch: member.Member.AuthEpoch,
		Role: "viewer", Actor: "root:1",
	})
	assert.Error(t, err)
}

func TestArchiveRequiresSafeTerminalStateAndRevokesCustomerAccess(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customers := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.archive"))
	created, err := customers.Create(customer.CreateCommand{
		CustomerCode: "archive-customer", DisplayName: "Archive Customer", Actor: "root:1",
	})
	require.NoError(t, err)
	membership, err := customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: created.ID, NewAPIUserID: 991, Role: "owner", Actor: "root:1",
	})
	require.NoError(t, err)
	application := model.CustomerApp{
		CustomerID: created.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "archive-app", DisplayName: "Archive App", Status: model.AppStatusActive,
		AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&application).Error)
	period := model.PlanPeriod{
		CustomerID: created.ID, Status: model.PeriodStatusActive,
		StartAt: time.Now().UTC().Add(-time.Hour), EndAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, db.Create(&period).Error)

	_, err = customers.Archive(customer.ArchiveCommand{
		CustomerID: created.ID, ExpectedVersion: created.RowVersion,
		Reason: "contract ended", Actor: "root:1", RequestID: "archive-blocked",
	})
	require.Error(t, err)
	var domainErr *domain.Error
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, domain.KindConflict, domainErr.Kind)

	require.NoError(t, db.Model(&application).Updates(map[string]any{"status": model.AppStatusDisabled}).Error)
	require.NoError(t, db.Model(&period).Updates(map[string]any{"status": model.PeriodStatusExpired}).Error)
	archived, err := customers.Archive(customer.ArchiveCommand{
		CustomerID: created.ID, ExpectedVersion: created.RowVersion,
		Reason: "contract ended", Actor: "root:1", RequestID: "archive-success",
	})
	require.NoError(t, err)
	assert.Equal(t, model.CustomerStatusArchived, archived.Status)
	assert.NotNil(t, archived.ArchivedAt)
	assert.Equal(t, created.RowVersion+1, archived.RowVersion)

	var persistedMember model.CustomerMember
	require.NoError(t, db.First(&persistedMember, membership.Member.ID).Error)
	assert.Equal(t, model.MemberStatusDisabled, persistedMember.Status)
	assert.Equal(t, fmt.Sprintf("historical:%d", persistedMember.ID), persistedMember.MembershipSlot)
	assert.Equal(t, membership.Member.AuthEpoch+1, persistedMember.AuthEpoch)
	var persistedIdentity model.IdentityBinding
	require.NoError(t, db.First(&persistedIdentity, membership.Identity.ID).Error)
	assert.Equal(t, model.IdentityStatusDisabled, persistedIdentity.Status)
	assert.Equal(t, membership.Identity.AuthEpoch+1, persistedIdentity.AuthEpoch)

	// Idempotent retries return the archived record even with the original version.
	retried, err := customers.Archive(customer.ArchiveCommand{
		CustomerID: created.ID, ExpectedVersion: created.RowVersion,
		Reason: "contract ended", Actor: "root:1", RequestID: "archive-retry",
	})
	require.NoError(t, err)
	assert.Equal(t, archived.RowVersion, retried.RowVersion)
	var revokeCount int64
	require.NoError(t, db.Model(&model.ControlOutbox{}).Where("customer_id = ? AND event_type = ?", created.ID, "SESSION_REVOKE").Count(&revokeCount).Error)
	assert.EqualValues(t, 1, revokeCount)
	var auditCount int64
	require.NoError(t, db.Model(&model.AdminAudit{}).Where("customer_id = ? AND action = ?", created.ID, "customer.archive").Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

func TestAddMemberRequiresFreshEnabledNewAPIIdentity(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	verifier := testutil.NewIdentityVerifier("v1.initial")
	customers := customer.New(db, "prod", verifier)
	created, err := customers.Create(customer.CreateCommand{
		CustomerCode: "identity-check", DisplayName: "Identity Check", Actor: "test",
	})
	require.NoError(t, err)

	verifier.Set("", errors.New("identity-status unavailable"))
	_, err = customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: created.ID, NewAPIUserID: 901, Role: "member", Actor: "test",
	})
	require.Error(t, err)
	var memberCount int64
	require.NoError(t, db.Model(&model.CustomerMember{}).Where("new_api_user_id = ?", 901).Count(&memberCount).Error)
	assert.Zero(t, memberCount, "a failed authoritative identity lookup must not create membership")
	var bindingCount int64
	require.NoError(t, db.Model(&model.IdentityBinding{}).Where("new_api_user_id = ?", 901).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount, "a failed authoritative identity lookup must not create a binding")

	verifier.Set("v1.current", nil)
	result, err := customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: created.ID, NewAPIUserID: 901, Role: "member", Actor: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, "v1.current", result.Identity.IdentityVersion)
}

func TestDisabledMemberCanRejoinTheSameCustomerWithoutLosingIdentityHistory(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	verifier := testutil.NewIdentityVerifier("v1.original")
	customers := customer.New(db, "prod", verifier)
	identities := identity.New(db)
	firstCustomer, err := customers.Create(customer.CreateCommand{
		CustomerCode: "rejoin-first", DisplayName: "Rejoin First", Actor: "test",
	})
	require.NoError(t, err)
	secondCustomer, err := customers.Create(customer.CreateCommand{
		CustomerCode: "rejoin-second", DisplayName: "Rejoin Second", Actor: "test",
	})
	require.NoError(t, err)
	first, err := customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: firstCustomer.ID, NewAPIUserID: 811, Role: "member", Actor: "test",
	})
	require.NoError(t, err)
	confirmed, err := identities.Confirm(identity.ConfirmCommand{
		BindingID: first.Identity.PublicID, CanonicalSubject: first.Identity.CanonicalSubject,
		ADPAccountID: "adp-rejoin-811", ADPAccountVersion: 1, Actor: "adp-backend",
	})
	require.NoError(t, err)
	second, err := customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: secondCustomer.ID, NewAPIUserID: 811, Role: "viewer", Actor: "test",
	})
	require.NoError(t, err)

	require.NoError(t, customers.DisableMember(firstCustomer.ID, 811, "root:1", "temporary removal", "disable-rejoin"))
	var disabledMember model.CustomerMember
	require.NoError(t, db.First(&disabledMember, first.Member.ID).Error)
	assert.Equal(t, model.MemberStatusDisabled, disabledMember.Status)
	assert.Equal(t, fmt.Sprintf("historical:%d", disabledMember.ID), disabledMember.MembershipSlot)
	var disabledIdentity model.IdentityBinding
	require.NoError(t, db.First(&disabledIdentity, confirmed.ID).Error)
	assert.Equal(t, model.IdentityStatusDisabled, disabledIdentity.Status)

	verifier.Set("v1.rejoined", nil)
	rejoined, err := customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: firstCustomer.ID, NewAPIUserID: 811, Role: "admin", Actor: "root:1", RequestID: "rejoin",
	})
	require.NoError(t, err)
	assert.Equal(t, first.Member.ID, rejoined.Member.ID, "same-customer rejoin must reuse the membership row")
	assert.Equal(t, confirmed.ID, rejoined.Identity.ID, "same-customer rejoin must preserve the canonical identity and history")
	assert.Equal(t, confirmed.PublicID, rejoined.Identity.PublicID)
	assert.Equal(t, confirmed.CanonicalSubject, rejoined.Identity.CanonicalSubject)
	assert.Equal(t, confirmed.ADPAccountID, rejoined.Identity.ADPAccountID)
	assert.Equal(t, "v1.rejoined", rejoined.Identity.IdentityVersion)
	assert.Equal(t, model.MemberStatusActive, rejoined.Member.Status)
	assert.Equal(t, model.MembershipSlot(firstCustomer.ID), rejoined.Member.MembershipSlot)
	assert.Equal(t, "admin", rejoined.Member.Role)
	assert.Nil(t, rejoined.Member.DisabledAt)
	assert.Equal(t, model.IdentityStatusActive, rejoined.Identity.Status)
	assert.Nil(t, rejoined.Identity.DisabledAt)
	assert.Equal(t, rejoined.Member.AuthEpoch, rejoined.Identity.AuthEpoch)
	assert.Greater(t, rejoined.Member.AuthEpoch, disabledMember.AuthEpoch)

	var secondPersisted model.CustomerMember
	require.NoError(t, db.First(&secondPersisted, second.Member.ID).Error)
	assert.Equal(t, model.MemberStatusActive, secondPersisted.Status, "another customer membership must remain independent")
	assert.Equal(t, model.MembershipSlot(secondCustomer.ID), secondPersisted.MembershipSlot)
	var membershipCount int64
	require.NoError(t, db.Model(&model.CustomerMember{}).
		Where("customer_id = ? AND new_api_user_id = ?", firstCustomer.ID, 811).
		Count(&membershipCount).Error)
	assert.EqualValues(t, 1, membershipCount)
}

func TestCustomerMigrationPreservesHistoricalOwnershipAndCanonicalIdentity(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	verifier := testutil.NewIdentityVerifier("v1.profile-original")
	customers := customer.New(db, "prod", verifier)
	identities := identity.New(db)
	resources := resourcebinding.New(db)
	accessService := access.New(
		db, secrets.EnvironmentResolver{}, verifier,
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)
	firstCustomer, err := customers.Create(customer.CreateCommand{
		CustomerCode: "migration-source", DisplayName: "Migration Source", Actor: "test",
	})
	require.NoError(t, err)
	secondCustomer, err := customers.Create(customer.CreateCommand{
		CustomerCode: "migration-target", DisplayName: "Migration Target", Actor: "test",
	})
	require.NoError(t, err)

	firstMembership, err := customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: firstCustomer.ID, NewAPIUserID: 777, Role: "owner", Actor: "test",
	})
	require.NoError(t, err)
	firstIdentity, err := identities.Confirm(identity.ConfirmCommand{
		BindingID: firstMembership.Identity.PublicID, CanonicalSubject: firstMembership.Identity.CanonicalSubject,
		ADPAccountID: "adp-source-777", ADPAccountVersion: 1, Actor: "adp-backend",
	})
	require.NoError(t, err)
	firstApp := model.CustomerApp{
		CustomerID: firstCustomer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "provider-app-source", DisplayName: "Source App", Status: model.AppStatusActive,
		AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&firstApp).Error)
	firstConfig := model.AppConfigVersion{
		CustomerAppID: firstApp.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space-source", TemplateAgentID: "template-source",
		AppKeySecretRef: "env://SOURCE_APP_KEY", AppKeyFingerprint: "sha256:source",
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&firstConfig).Error)
	require.NoError(t, db.Model(&firstApp).Update("current_config_version_id", firstConfig.ID).Error)

	firstScope := resourcebinding.BindCommand{
		BindingID: firstIdentity.PublicID, CanonicalSubject: firstIdentity.CanonicalSubject,
		CustomerID: firstCustomer.ID, ApplicationID: firstApp.AppID, AppProfileID: firstApp.ID,
		ConfigVersion: firstConfig.ConfigVersion, SourceService: "adp-backend", SourceVersion: 1,
	}
	accountCommand := firstScope
	accountCommand.ResourceType = resourcebinding.ResourceAccount
	accountCommand.ResourceID = "historical-account-777"
	accountCommand.SourceEventID = "historical-account-created"
	_, err = resources.Bind(context.Background(), accountCommand)
	require.NoError(t, err)
	agentCommand := firstScope
	agentCommand.ResourceType = resourcebinding.ResourceAgent
	agentCommand.ResourceID = "historical-agent-777"
	agentCommand.ParentResourceType = resourcebinding.ResourceAccount
	agentCommand.ParentResourceID = accountCommand.ResourceID
	agentCommand.SourceEventID = "historical-agent-created"
	_, err = resources.Bind(context.Background(), agentCommand)
	require.NoError(t, err)

	// A signed identity-profile version change must not remap the stable subject,
	// ADP account, or already-owned resources.
	verifier.Set("v1.profile-renamed", nil)
	_, err = accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 777, IdentityVersion: "v1.profile-renamed",
	})
	require.NoError(t, err)
	var renamedIdentity model.IdentityBinding
	require.NoError(t, db.First(&renamedIdentity, firstIdentity.ID).Error)
	assert.Equal(t, firstIdentity.CanonicalSubject, renamedIdentity.CanonicalSubject)
	assert.Equal(t, firstIdentity.ADPAccountID, renamedIdentity.ADPAccountID)
	assert.Equal(t, "v1.profile-renamed", renamedIdentity.IdentityVersion)
	var historicalAgent model.ResourceBinding
	require.NoError(t, db.Where("resource_id = ?", agentCommand.ResourceID).First(&historicalAgent).Error)
	assert.Equal(t, firstIdentity.ID, historicalAgent.IdentityBindingID)

	require.NoError(t, customers.DisableMember(firstCustomer.ID, 777, "root:1", "customer migration", "request-migrate"))
	secondMembership, err := customers.AddMember(context.Background(), customer.AddMemberCommand{
		CustomerID: secondCustomer.ID, NewAPIUserID: 777, Role: "owner", Actor: "test",
	})
	require.NoError(t, err)
	assert.NotEqual(t, firstIdentity.PublicID, secondMembership.Identity.PublicID)
	assert.NotEqual(t, firstIdentity.CanonicalSubject, secondMembership.Identity.CanonicalSubject)
	assert.Equal(t, fmt.Sprintf("napi:prod:customer:%d:user:777", secondCustomer.ID), secondMembership.Identity.CanonicalSubject)
	secondIdentity, err := identities.Confirm(identity.ConfirmCommand{
		BindingID: secondMembership.Identity.PublicID, CanonicalSubject: secondMembership.Identity.CanonicalSubject,
		ADPAccountID: "adp-target-777", ADPAccountVersion: 1, Actor: "adp-backend",
	})
	require.NoError(t, err)

	secondApp := model.CustomerApp{
		CustomerID: secondCustomer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "provider-app-target", DisplayName: "Target App", Status: model.AppStatusActive,
		AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&secondApp).Error)
	secondConfig := model.AppConfigVersion{
		CustomerAppID: secondApp.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space-target", TemplateAgentID: "template-target",
		AppKeySecretRef: "env://TARGET_APP_KEY", AppKeyFingerprint: "sha256:target",
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&secondConfig).Error)
	require.NoError(t, db.Model(&secondApp).Update("current_config_version_id", secondConfig.ID).Error)

	secondContext := resourcebinding.BindCommand{
		BindingID: secondIdentity.PublicID, CanonicalSubject: secondIdentity.CanonicalSubject,
		CustomerID: secondCustomer.ID, ApplicationID: secondApp.AppID, AppProfileID: secondApp.ID,
		ConfigVersion: secondConfig.ConfigVersion, ResourceType: resourcebinding.ResourceConversation,
		ResourceID: "target-conversation", ParentResourceType: resourcebinding.ResourceAgent,
		ParentResourceID: agentCommand.ResourceID, SourceService: "adp-backend",
		SourceEventID: "cross-customer-history-attempt", SourceVersion: 1,
	}
	_, err = resources.Bind(context.Background(), secondContext)
	var domainErr *domain.Error
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, domain.KindForbidden, domainErr.Kind, "the target customer's active context cannot attach to source history")

	var disabledIdentity model.IdentityBinding
	require.NoError(t, db.First(&disabledIdentity, firstIdentity.ID).Error)
	assert.Equal(t, model.IdentityStatusDisabled, disabledIdentity.Status)
	require.NoError(t, db.First(&historicalAgent, historicalAgent.ID).Error)
	assert.Equal(t, firstIdentity.ID, historicalAgent.IdentityBindingID)
}
