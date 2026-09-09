package credential_test

import (
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/adminquery"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomerScopedProfilesAreUniquePerOwnerAndRotationPreservesScope(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	first := model.Customer{CustomerCode: "credential-first", DisplayName: "First", Status: model.CustomerStatusActive, RowVersion: 1}
	second := model.Customer{CustomerCode: "credential-second", DisplayName: "Second", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Create(&second).Error)
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_SCOPED_ID": "scoped-id", "env://WORKBENCH_PROVIDER_SCOPED_KEY": "scoped-key",
		"env://WORKBENCH_PROVIDER_ROTATED_ID": "rotated-id", "env://WORKBENCH_PROVIDER_ROTATED_KEY": "rotated-key",
	}
	service := credential.New(db, resolver)

	platform, err := service.Create(createCommand("shared", nil))
	require.NoError(t, err)
	firstProfile, err := service.Create(createCommand("shared", &first.ID))
	require.NoError(t, err)
	secondProfile, err := service.Create(createCommand("shared", &second.ID))
	require.NoError(t, err)
	assert.Equal(t, credential.PlatformOwnerScope, platform.OwnerScope)
	assert.Equal(t, credential.CustomerOwnerScope(first.ID), firstProfile.OwnerScope)
	assert.Equal(t, credential.CustomerOwnerScope(second.ID), secondProfile.OwnerScope)
	assert.Equal(t, int64(1), platform.Version)
	assert.Equal(t, int64(1), firstProfile.Version)
	assert.Equal(t, int64(1), secondProfile.Version)

	_, err = service.Create(createCommand("shared", &first.ID))
	assert.Error(t, err, "the same owner/provider/name must rotate instead of creating another v1")
	mismatched := createCommand("mismatched", &first.ID)
	mismatched.OwnerScope = credential.CustomerOwnerScope(second.ID)
	_, err = service.Create(mismatched)
	assert.Error(t, err)

	rotated, err := service.StageRotation(credential.StageRotationCommand{
		CurrentProfileID: firstProfile.ID, ExpectedCurrentVersion: firstProfile.RowVersion,
		SecretIDRef: "env://WORKBENCH_PROVIDER_ROTATED_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_ROTATED_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("rotated-id", "rotated-key"), Actor: "admin", RequestID: "rotate-first",
	})
	require.NoError(t, err)
	assert.Equal(t, firstProfile.OwnerScope, rotated.OwnerScope)
	require.NotNil(t, rotated.CustomerID)
	assert.Equal(t, first.ID, *rotated.CustomerID)
	assert.Equal(t, int64(2), rotated.Version)

	var audit model.AdminAudit
	require.NoError(t, db.Where("action = ? AND resource_id = ?", "credential.rotation.stage", rotated.ID).First(&audit).Error)
	require.NotNil(t, audit.CustomerID)
	assert.Equal(t, first.ID, *audit.CustomerID)

	eligible, err := adminquery.New(db).CredentialProfiles(20, &first.ID)
	require.NoError(t, err)
	ids := make([]uint64, 0, len(eligible))
	for _, profile := range eligible {
		ids = append(ids, profile.ID)
		assert.True(t, profile.OwnerScope == credential.PlatformOwnerScope || profile.CustomerID != nil && *profile.CustomerID == first.ID)
	}
	assert.Contains(t, ids, platform.ID)
	assert.Contains(t, ids, firstProfile.ID)
	assert.Contains(t, ids, rotated.ID)
	assert.NotContains(t, ids, secondProfile.ID)
}

func TestCredentialCreateRejectsAdministratorFingerprintMismatchWithoutLeakingSecrets(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_SCOPED_ID":  "sensitive-id",
		"env://WORKBENCH_PROVIDER_SCOPED_KEY": "sensitive-key",
	}
	_, err = credential.New(db, resolver).Create(createCommand("mismatch", nil))
	require.Error(t, err)
	assert.ErrorContains(t, err, "fingerprint does not match")
	assert.NotContains(t, err.Error(), "WORKBENCH_PROVIDER")
	assert.NotContains(t, err.Error(), "sensitive")
	var count int64
	require.NoError(t, db.Model(&model.CredentialProfile{}).Count(&count).Error)
	assert.Zero(t, count)
}

func createCommand(name string, customerID *uint64) credential.CreateCommand {
	command := credential.CreateCommand{
		ProviderEnvironment: model.ProviderChinaTencentCloud, Name: name,
		SecretIDRef: "env://WORKBENCH_PROVIDER_SCOPED_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_SCOPED_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("scoped-id", "scoped-key"), Actor: "admin",
	}
	if customerID != nil {
		command.CustomerID = customerID
		command.OwnerScope = credential.CustomerOwnerScope(*customerID)
	}
	return command
}
