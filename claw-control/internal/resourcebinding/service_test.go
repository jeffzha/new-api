package resourcebinding_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/resourcebinding"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type bindingScope struct {
	command resourcebinding.BindCommand
	app     model.CustomerApp
}

func TestBindResourceCreatesStrictParentChain(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	scope := createBindingScope(t, db, 101)
	service := resourcebinding.New(db)

	chain := []struct {
		resourceType, resourceID, parentType, parentID string
	}{
		{resourcebinding.ResourceAccount, "account-101", "", ""},
		{resourcebinding.ResourceAgent, "agent-101", resourcebinding.ResourceAccount, "account-101"},
		{resourcebinding.ResourceConversation, "conversation-101", resourcebinding.ResourceAgent, "agent-101"},
		{resourcebinding.ResourceWorkspace, "workspace-101", resourcebinding.ResourceConversation, "conversation-101"},
		{resourcebinding.ResourceFile, "file-101", resourcebinding.ResourceWorkspace, "workspace-101"},
	}
	for index, item := range chain {
		command := scope.command
		command.ResourceType, command.ResourceID = item.resourceType, item.resourceID
		command.ParentResourceType, command.ParentResourceID = item.parentType, item.parentID
		command.SourceEventID = fmt.Sprintf("event-%d", index)
		result, bindErr := service.Bind(context.Background(), command)
		require.NoError(t, bindErr)
		assert.NotEmpty(t, result.ResourceBindingID)
		assert.False(t, result.Idempotent)
		assert.Equal(t, item.resourceType, result.ResourceType)
		assert.Equal(t, item.parentType, result.ParentResourceType)
	}

	var bindings []model.ResourceBinding
	require.NoError(t, db.Order("id asc").Find(&bindings).Error)
	require.Len(t, bindings, len(chain))
	for index := 1; index < len(bindings); index++ {
		require.NotNil(t, bindings[index].ParentResourceBindingID)
		assert.Equal(t, bindings[index-1].ID, *bindings[index].ParentResourceBindingID)
	}
}

func TestBindResourceIsConcurrentAndIdempotent(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	scope := createBindingScope(t, db, 201)
	service := resourcebinding.New(db)
	command := scope.command
	command.ResourceType = resourcebinding.ResourceAccount
	command.ResourceID = "account-idempotent"
	command.SourceEventID = "stable-create-event"

	const workerCount = 12
	results := make(chan *resourcebinding.BindResult, workerCount)
	errorsSeen := make(chan error, workerCount)
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, bindErr := service.Bind(context.Background(), command)
			if bindErr != nil {
				errorsSeen <- bindErr
				return
			}
			results <- result
		}()
	}
	workers.Wait()
	close(results)
	close(errorsSeen)
	for bindErr := range errorsSeen {
		require.NoError(t, bindErr)
	}

	var publicID string
	created := 0
	for result := range results {
		if publicID == "" {
			publicID = result.ResourceBindingID
		}
		assert.Equal(t, publicID, result.ResourceBindingID)
		if !result.Idempotent {
			created++
		}
	}
	assert.Equal(t, 1, created)
	var count int64
	require.NoError(t, db.Model(&model.ResourceBinding{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestBindResourceRejectsIDORAcrossIdentityAppAndConfig(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	first := createBindingScope(t, db, 301)
	second := createBindingScope(t, db, 302)
	service := resourcebinding.New(db)

	firstAccount := first.command
	firstAccount.ResourceType = resourcebinding.ResourceAccount
	firstAccount.ResourceID = "globally-unique-account"
	firstAccount.SourceEventID = "first-account-event"
	_, err = service.Bind(context.Background(), firstAccount)
	require.NoError(t, err)

	crossIdentity := second.command
	crossIdentity.ResourceType = resourcebinding.ResourceAccount
	crossIdentity.ResourceID = firstAccount.ResourceID
	crossIdentity.SourceEventID = "second-account-event"
	_, err = service.Bind(context.Background(), crossIdentity)
	assertDomainKind(t, err, domain.KindConflict)

	badCanonicalSubject := first.command
	badCanonicalSubject.CanonicalSubject = second.command.CanonicalSubject
	badCanonicalSubject.ResourceType = resourcebinding.ResourceAccount
	badCanonicalSubject.ResourceID = "other-account"
	badCanonicalSubject.SourceEventID = "bad-subject-event"
	_, err = service.Bind(context.Background(), badCanonicalSubject)
	assertDomainKind(t, err, domain.KindForbidden)

	wrongApp := first.command
	wrongApp.AppProfileID = second.command.AppProfileID
	wrongApp.ApplicationID = second.command.ApplicationID
	wrongApp.ConfigVersion = second.command.ConfigVersion
	wrongApp.ResourceType = resourcebinding.ResourceAccount
	wrongApp.ResourceID = "wrong-app-account"
	wrongApp.SourceEventID = "wrong-app-event"
	_, err = service.Bind(context.Background(), wrongApp)
	assertDomainKind(t, err, domain.KindForbidden)

	wrongConfig := first.command
	wrongConfig.ConfigVersion++
	wrongConfig.ResourceType = resourcebinding.ResourceAccount
	wrongConfig.ResourceID = "wrong-config-account"
	wrongConfig.SourceEventID = "wrong-config-event"
	_, err = service.Bind(context.Background(), wrongConfig)
	assertDomainKind(t, err, domain.KindForbidden)

	crossParent := second.command
	crossParent.ResourceType = resourcebinding.ResourceAgent
	crossParent.ResourceID = "second-agent"
	crossParent.ParentResourceType = resourcebinding.ResourceAccount
	crossParent.ParentResourceID = firstAccount.ResourceID
	crossParent.SourceEventID = "cross-parent-event"
	_, err = service.Bind(context.Background(), crossParent)
	assertDomainKind(t, err, domain.KindForbidden)
}

func TestBindResourceRejectsBrokenParentShapeAndEventReuse(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	scope := createBindingScope(t, db, 401)
	service := resourcebinding.New(db)

	missingParent := scope.command
	missingParent.ResourceType = resourcebinding.ResourceConversation
	missingParent.ResourceID = "conversation-without-agent"
	missingParent.ParentResourceType = resourcebinding.ResourceAgent
	missingParent.ParentResourceID = "missing-agent"
	missingParent.SourceEventID = "missing-parent-event"
	_, err = service.Bind(context.Background(), missingParent)
	assertDomainKind(t, err, domain.KindNotFound)

	wrongParentType := missingParent
	wrongParentType.ParentResourceType = resourcebinding.ResourceAccount
	_, err = service.Bind(context.Background(), wrongParentType)
	assertDomainKind(t, err, domain.KindInvalid)

	account := scope.command
	account.ResourceType = resourcebinding.ResourceAccount
	account.ResourceID = "event-account"
	account.SourceEventID = "single-event"
	_, err = service.Bind(context.Background(), account)
	require.NoError(t, err)
	changed := account
	changed.ResourceID = "different-account"
	_, err = service.Bind(context.Background(), changed)
	assertDomainKind(t, err, domain.KindConflict)
}

func createBindingScope(t *testing.T, db *gorm.DB, suffix int64) bindingScope {
	t.Helper()
	customer := model.Customer{
		CustomerCode: fmt.Sprintf("customer-%d", suffix), DisplayName: "Customer",
		Status: model.CustomerStatusActive, RowVersion: 1,
	}
	require.NoError(t, db.Create(&customer).Error)
	member := model.CustomerMember{
		CustomerID: customer.ID, NewAPIUserID: suffix, Role: "member",
		Status: model.MemberStatusActive, MembershipSlot: model.MembershipSlot(customer.ID), AuthEpoch: 1,
	}
	require.NoError(t, db.Create(&member).Error)
	identity := model.IdentityBinding{
		PublicID: fmt.Sprintf("binding-%d", suffix), CustomerID: customer.ID,
		NewAPIUserID: suffix, CanonicalSubject: fmt.Sprintf("new-api:user:%d", suffix),
		ADPAccountID: fmt.Sprintf("account-%d", suffix), ADPAccountVersion: 1,
		IdentityVersion: "v1", Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&identity).Error)
	app := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: fmt.Sprintf("provider-app-%d", suffix), DisplayName: "App",
		Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&app).Error)
	config := model.AppConfigVersion{
		CustomerAppID: app.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space", TemplateAgentID: "template",
		AppKeySecretRef: "env://APP_KEY", AppKeyFingerprint: "sha256:test",
		LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test",
	}
	require.NoError(t, db.Create(&config).Error)
	require.NoError(t, db.Model(&app).Update("current_config_version_id", config.ID).Error)
	app.CurrentConfigVersionID = &config.ID
	return bindingScope{
		app: app,
		command: resourcebinding.BindCommand{
			BindingID: identity.PublicID, CanonicalSubject: identity.CanonicalSubject,
			CustomerID: customer.ID, ApplicationID: app.AppID, AppProfileID: app.ID,
			ConfigVersion: config.ConfigVersion, SourceService: "adp-backend", SourceVersion: 1,
		},
	}
}

func assertDomainKind(t *testing.T, err error, expected domain.ErrorKind) {
	t.Helper()
	require.Error(t, err)
	var domainError *domain.Error
	require.True(t, errors.As(err, &domainError))
	assert.Equal(t, expected, domainError.Kind)
}
