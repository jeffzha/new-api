package access_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMultiCustomerSelectionIsBoundOneTimeAndRevalidated(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const identityVersion = "v1.multi-context"
	verifier := testutil.NewIdentityVerifier(identityVersion)
	service := access.New(db, secrets.EnvironmentResolver{}, verifier, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	first := createSelectableContext(t, db, 7001, identityVersion, "tenant-one", "app-one", "primary", true)
	second := createSelectableContext(t, db, 7001, identityVersion, "tenant-two", "app-two", "primary", true)

	entered := enterUser(t, service, 7001, identityVersion)
	assert.True(t, entered.SelectionRequired)
	require.Len(t, entered.Selections, 2)
	assert.Empty(t, entered.ADPSSOTicket)
	projection, err := jsonx.Marshal(entered.Selections)
	require.NoError(t, err)
	for _, forbidden := range []string{`"customer_id"`, `"application_id"`, `"app_profile_id"`, `"space_id"`, `"config_version"`, `"secret`} {
		assert.NotContains(t, string(projection), forbidden)
	}
	var pending model.ControlSession
	sessionHash := tokenHash(entered.ControlSessionToken)
	require.NoError(t, db.Where("token_hash = ?", sessionHash).First(&pending).Error)
	assert.Equal(t, model.ControlSessionStateSelectionPending, pending.SelectionState)
	assert.Zero(t, pending.CustomerID)

	firstOption := selectionForCustomer(t, entered.Selections, first.customer.CustomerCode)
	otherSession := enterUser(t, service, 7001, identityVersion)
	_, err = service.SelectContext(context.Background(), otherSession.ControlSessionToken, firstOption.SelectionToken)
	assert.ErrorContains(t, err, "different session")

	selected, err := service.SelectContext(context.Background(), entered.ControlSessionToken, firstOption.SelectionToken)
	require.NoError(t, err)
	claims, err := service.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: selected.ADPSSOTicket, BrowserBinding: selected.SSOBrowserBinding, ConsumerService: "adp-backend",
	})
	require.NoError(t, err)
	assert.Equal(t, first.customer.ID, claims.CustomerID)
	assert.Equal(t, first.application.ID, claims.AppProfileID)
	assert.Equal(t, first.configuration.ConfigVersion, claims.ConfigVersion)
	_, err = service.SelectContext(context.Background(), entered.ControlSessionToken, firstOption.SelectionToken)
	assert.ErrorContains(t, err, "already been consumed")

	// A token issued for the same new-api identity is still bound to its exact
	// session and cannot be presented from another user's session.
	third := createSelectableContext(t, db, 7002, identityVersion, "tenant-three", "app-three", "primary", true)
	_ = third
	otherUser := enterUser(t, service, 7002, identityVersion)
	freshOptions, err := service.ListSelectionOptions(context.Background(), otherSession.ControlSessionToken)
	require.NoError(t, err)
	secondUserBound := selectionForCustomer(t, freshOptions, second.customer.CustomerCode)
	_, err = service.SelectContext(context.Background(), otherUser.ControlSessionToken, secondUserBound.SelectionToken)
	assert.ErrorContains(t, err, "different session or identity")

	// App lifecycle/version changes after issuance invalidate the nonce.
	staleSession := enterUser(t, service, 7001, identityVersion)
	staleOption := selectionForCustomer(t, staleSession.Selections, second.customer.CustomerCode)
	require.NoError(t, db.Model(&second.application).Updates(map[string]any{
		"status": model.AppStatusDisabled, "auth_epoch": gorm.Expr("auth_epoch + 1"),
	}).Error)
	_, err = service.SelectContext(context.Background(), staleSession.ControlSessionToken, staleOption.SelectionToken)
	assert.Error(t, err)

	require.NoError(t, db.Model(&second.application).Updates(map[string]any{
		"status": model.AppStatusActive, "auth_epoch": gorm.Expr("auth_epoch + 1"),
	}).Error)
	versionSession := enterUser(t, service, 7001, identityVersion)
	versionOption := selectionForCustomer(t, versionSession.Selections, second.customer.CustomerCode)
	nextConfig := second.configuration
	nextConfig.ID = 0
	nextConfig.ConfigVersion = 2
	require.NoError(t, db.Create(&nextConfig).Error)
	require.NoError(t, db.Model(&second.application).Updates(map[string]any{
		"current_config_version_id": nextConfig.ID, "auth_epoch": gorm.Expr("auth_epoch + 1"),
	}).Error)
	_, err = service.SelectContext(context.Background(), versionSession.ControlSessionToken, versionOption.SelectionToken)
	assert.Error(t, err, "a config-version change must invalidate an issued selection")

	migrationSession := enterUser(t, service, 7001, identityVersion)
	migrationOption := selectionForCustomer(t, migrationSession.Selections, second.customer.CustomerCode)
	require.NoError(t, db.Model(&second.application).Updates(map[string]any{
		"slot": fmt.Sprintf("archived:%d", second.application.ID), "status": model.AppStatusArchived,
		"auth_epoch": gorm.Expr("auth_epoch + 1"),
	}).Error)
	_, err = service.SelectContext(context.Background(), migrationSession.ControlSessionToken, migrationOption.SelectionToken)
	assert.Error(t, err, "an App migration/archive must invalidate the old selector nonce")
	require.NoError(t, db.Model(&second.application).Updates(map[string]any{
		"slot": "primary", "status": model.AppStatusActive, "auth_epoch": gorm.Expr("auth_epoch + 1"),
	}).Error)

	// Expiry is checked at atomic consumption time.
	expiredSession := enterUser(t, service, 7001, identityVersion)
	expiredOption := selectionForCustomer(t, expiredSession.Selections, first.customer.CustomerCode)
	require.NoError(t, db.Model(&model.ContextSelectionNonce{}).Where("token_hash = ?", tokenHash(expiredOption.SelectionToken)).Update("expires_at", time.Now().UTC().Add(-time.Second)).Error)
	_, err = service.SelectContext(context.Background(), expiredSession.ControlSessionToken, expiredOption.SelectionToken)
	assert.ErrorContains(t, err, "expired")
}

func TestAgentStoreEntryDefersSSOAndBindsDefaultCustomerContext(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const identityVersion = "v1.agent-store"
	verifier := testutil.NewIdentityVerifier(identityVersion)
	service := access.New(db, secrets.EnvironmentResolver{}, verifier, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	primary := createSelectableContext(t, db, 7101, identityVersion, "tenant-store", "app-primary", "primary", true)
	_ = createAdditionalSelectableApp(t, db, primary.customer.ID, "app-secondary", "secondary")

	ticket, err := service.IssueEntryTicket(access.IssueEntryTicketCommand{NewAPIUserID: 7101, IdentityVersion: identityVersion})
	require.NoError(t, err)
	entered, err := service.Enter(context.Background(), access.EnterCommand{Ticket: ticket.Ticket, DeferSSO: true})
	require.NoError(t, err)
	assert.False(t, entered.SelectionRequired)
	assert.Empty(t, entered.ADPSSOTicket)
	assert.NotEmpty(t, entered.ControlSessionToken)
	assert.NotEmpty(t, entered.ControlCSRFToken)

	principal, err := service.AuthorizeControlSession(context.Background(), entered.ControlSessionToken, entered.ControlCSRFToken, true)
	require.NoError(t, err)
	assert.Equal(t, primary.customer.ID, principal.CustomerID)
	_, err = service.AuthorizeControlSession(context.Background(), entered.ControlSessionToken, "forged", true)
	assert.ErrorContains(t, err, "CSRF")
}

func TestSelectionNonceConcurrentConsumptionHasSingleWinner(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const identityVersion = "v1.concurrent"
	verifier := testutil.NewIdentityVerifier(identityVersion)
	service := access.New(db, secrets.EnvironmentResolver{}, verifier, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	createSelectableContext(t, db, 7101, identityVersion, "concurrent-one", "concurrent-app-one", "primary", true)
	createSelectableContext(t, db, 7101, identityVersion, "concurrent-two", "concurrent-app-two", "primary", true)
	entered := enterUser(t, service, 7101, identityVersion)
	require.True(t, entered.SelectionRequired)
	token := entered.Selections[0].SelectionToken

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.SelectContext(context.Background(), entered.ControlSessionToken, token)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes)
}

func TestSingleMembershipWithMultipleAppsRequiresExplicitSelection(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const identityVersion = "v1.default-app"
	verifier := testutil.NewIdentityVerifier(identityVersion)
	service := access.New(db, secrets.EnvironmentResolver{}, verifier, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	primary := createSelectableContext(t, db, 7201, identityVersion, "default-customer", "default-app", "primary", true)
	secondary := createAdditionalSelectableApp(t, db, primary.customer.ID, "secondary-app", "secondary")

	entered := enterUser(t, service, 7201, identityVersion)
	assert.True(t, entered.SelectionRequired)
	require.Len(t, entered.Selections, 2)
	assert.Empty(t, entered.ADPSSOTicket)
	selected, err := service.SelectContext(
		context.Background(), entered.ControlSessionToken,
		selectionForCustomerAndApp(t, entered.Selections, primary.customer.CustomerCode, secondary.AppID).SelectionToken,
	)
	require.NoError(t, err)
	claims, err := service.ConsumeTicket(access.ConsumeTicketCommand{
		Ticket: selected.ADPSSOTicket, BrowserBinding: selected.SSOBrowserBinding, ConsumerService: "adp-backend",
	})
	require.NoError(t, err)
	assert.Equal(t, secondary.ID, claims.AppProfileID)
	assert.Equal(t, secondary.AppID, claims.ApplicationID)
	authorized, err := service.Authorize(context.Background(), access.AuthzCommand{
		BindingID: claims.BindingID, CanonicalSubject: claims.CanonicalSubject,
		AuthEpoch: claims.AuthEpoch, CustomerID: claims.CustomerID,
		AppProfileID: claims.AppProfileID, ConfigVersion: claims.ConfigVersion,
		Method: "GET", ResourcePath: "/workbench/conversations",
	})
	require.NoError(t, err)
	assert.True(t, authorized.Allowed)
	_, err = service.Authorize(context.Background(), access.AuthzCommand{
		BindingID: claims.BindingID, CanonicalSubject: claims.CanonicalSubject,
		AuthEpoch: claims.AuthEpoch, Method: "GET", ResourcePath: "/workbench/conversations",
	})
	assert.ErrorContains(t, err, "ambiguous")
}

type selectableFixture struct {
	customer      model.Customer
	membership    model.CustomerMember
	identity      model.IdentityBinding
	application   model.CustomerApp
	configuration model.AppConfigVersion
}

func createSelectableContext(t *testing.T, db *gorm.DB, userID int64, identityVersion, customerCode, appID, slot string, activePlan bool) selectableFixture {
	t.Helper()
	now := time.Now().UTC()
	fixture := selectableFixture{}
	fixture.customer = model.Customer{CustomerCode: customerCode, DisplayName: customerCode, Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&fixture.customer).Error)
	fixture.membership = model.CustomerMember{
		CustomerID: fixture.customer.ID, NewAPIUserID: userID, Role: "member",
		Status: model.MemberStatusActive, MembershipSlot: model.MembershipSlot(fixture.customer.ID), AuthEpoch: 1,
	}
	require.NoError(t, db.Create(&fixture.membership).Error)
	fixture.identity = model.IdentityBinding{
		PublicID: support.PublicID("wid"), CustomerID: fixture.customer.ID, NewAPIUserID: userID,
		CanonicalSubject: fmt.Sprintf("napi:test:customer:%d:user:%d", fixture.customer.ID, userID),
		IdentityVersion:  identityVersion, Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&fixture.identity).Error)
	fixture.application = model.CustomerApp{
		CustomerID: fixture.customer.ID, Slot: slot, ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: appID, DisplayName: appID, Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&fixture.application).Error)
	limits := productpolicy.Limits{
		CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 600,
		MaxReasoningRounds: 20, MaxOutputTokens: 4096, WebSearchPerTurn: 1, MaxFileBytes: 1024,
	}
	limitsJSON, err := jsonx.Marshal(limits)
	require.NoError(t, err)
	capabilitiesJSON, err := jsonx.Marshal([]string{"chat"})
	require.NoError(t, err)
	fixture.configuration = model.AppConfigVersion{
		CustomerAppID: fixture.application.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space-" + appID, TemplateAgentID: "agent-" + appID,
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_SELECTION_APP_KEY", AppKeyFingerprint: "sha256:test",
		LimitsJSON: string(limitsJSON), CapabilitiesJSON: string(capabilitiesJSON), CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&fixture.configuration).Error)
	require.NoError(t, db.Model(&fixture.application).Update("current_config_version_id", fixture.configuration.ID).Error)
	fixture.application.CurrentConfigVersionID = &fixture.configuration.ID
	if activePlan {
		snapshotJSON, err := jsonx.Marshal(map[string]any{"capabilities": []string{"chat"}, "limits": limits})
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.PlanPeriod{
			CustomerID: fixture.customer.ID, PlanVersionID: 1,
			StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour), AmountCNY: "1.00",
			PaymentMode: model.PaymentModeOfflineManual, PaymentStatus: model.PaymentStatusPaid,
			Status: model.PeriodStatusActive, SnapshotJSON: string(snapshotJSON), RowVersion: 1,
		}).Error)
	}
	return fixture
}

func createAdditionalSelectableApp(t *testing.T, db *gorm.DB, customerID uint64, appID, alias string) model.CustomerApp {
	t.Helper()
	now := time.Now().UTC()
	application := model.CustomerApp{
		CustomerID: customerID, Slot: "app:" + support.PublicID("aps"), Alias: alias,
		ProviderEnvironment: model.ProviderChinaTencentADP, AppID: appID, DisplayName: appID,
		Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&application).Error)
	limits := app.Limits{CustomerConcurrency: 1, UserConcurrency: 1, MaxRuntimeSeconds: 60, MaxReasoningRounds: 1, MaxOutputTokens: 100, MaxFileBytes: 1}
	limitsJSON, err := jsonx.Marshal(limits)
	require.NoError(t, err)
	capabilitiesJSON, err := jsonx.Marshal([]string{"chat"})
	require.NoError(t, err)
	configuration := model.AppConfigVersion{
		CustomerAppID: application.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space", TemplateAgentID: "agent",
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_SELECTION_APP_KEY", AppKeyFingerprint: "sha256:test",
		LimitsJSON: string(limitsJSON), CapabilitiesJSON: string(capabilitiesJSON), CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&configuration).Error)
	require.NoError(t, db.Model(&application).Update("current_config_version_id", configuration.ID).Error)
	application.CurrentConfigVersionID = &configuration.ID
	return application
}

func enterUser(t *testing.T, service *access.Service, userID int64, identityVersion string) *access.EnterResult {
	t.Helper()
	ticket, err := service.IssueEntryTicket(access.IssueEntryTicketCommand{NewAPIUserID: userID, IdentityVersion: identityVersion})
	require.NoError(t, err)
	result, err := service.Enter(context.Background(), access.EnterCommand{Ticket: ticket.Ticket})
	require.NoError(t, err)
	return result
}

func selectionForCustomer(t *testing.T, options []access.SelectionOption, customerCode string) access.SelectionOption {
	t.Helper()
	for index := range options {
		if options[index].CustomerCode == customerCode {
			return options[index]
		}
	}
	require.FailNow(t, "selection option not found", customerCode)
	return access.SelectionOption{}
}

func selectionForCustomerAndApp(t *testing.T, options []access.SelectionOption, customerCode, appID string) access.SelectionOption {
	t.Helper()
	for index := range options {
		if options[index].CustomerCode == customerCode && options[index].AppDisplayName == appID {
			return options[index]
		}
	}
	require.FailNow(t, "selection option not found", customerCode+":"+appID)
	return access.SelectionOption{}
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
