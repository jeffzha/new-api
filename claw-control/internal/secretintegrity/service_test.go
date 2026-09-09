package secretintegrity_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secretintegrity"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLegacyFingerprintsRequireExplicitReenrollmentAndSecretChangesFailClosed(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_READY_ID":      "ready-id",
		"env://WORKBENCH_PROVIDER_READY_KEY":     "ready-key",
		"env://WORKBENCH_PROVIDER_READY_APP_KEY": "ready-app-key",
	}
	customer := model.Customer{CustomerCode: "ready-customer", DisplayName: "Ready", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	profile := model.CredentialProfile{
		OwnerScope: "platform", ProviderEnvironment: model.ProviderChinaTencentADP, Name: "legacy",
		SecretIDRef: "env://WORKBENCH_PROVIDER_READY_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_READY_KEY",
		Fingerprint: "legacy-untrusted", FingerprintVersion: 0, Status: model.CredentialStatusActive,
		Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&profile).Error)
	application := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: "ready-app", DisplayName: "Ready", Status: model.AppStatusVerified, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&application).Error)
	config := model.AppConfigVersion{
		CustomerAppID: application.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space", TemplateAgentID: "agent", CredentialProfileID: &profile.ID,
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_READY_APP_KEY", AppKeyFingerprint: "legacy-untrusted",
		AppKeyFingerprintVersion: 0, RowVersion: 1, LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "legacy",
	}
	require.NoError(t, db.Create(&config).Error)
	application.CurrentConfigVersionID = &config.ID
	require.NoError(t, db.Save(&application).Error)

	service := secretintegrity.New(db, resolver)
	assert.Error(t, service.Check(), "legacy fingerprints must never be silently trusted")
	_, err = service.ReenrollLegacy(secretintegrity.ReenrollCommand{Confirmation: "wrong", Actor: "admin-a"})
	assert.Error(t, err)
	result, err := service.ReenrollLegacy(secretintegrity.ReenrollCommand{
		Confirmation: secretintegrity.ReenrollConfirmation, Actor: "admin-a", RequestID: "reenroll-1",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.CredentialProfiles)
	assert.Equal(t, 1, result.AppConfigs)
	require.NoError(t, service.Check())
	require.NoError(t, db.First(&profile, profile.ID).Error)
	assert.Equal(t, secrets.CanonicalFingerprintVersion, profile.FingerprintVersion)
	assert.Equal(t, secrets.CredentialPairFingerprint("ready-id", "ready-key"), profile.Fingerprint)
	require.NoError(t, db.First(&config, config.ID).Error)
	assert.Equal(t, secrets.CanonicalFingerprintVersion, config.AppKeyFingerprintVersion)
	assert.Equal(t, secrets.AppKeyFingerprint("ready-app-key"), config.AppKeyFingerprint)

	resolver["env://WORKBENCH_PROVIDER_READY_KEY"] = "changed-out-of-band"
	assert.Error(t, service.Check(), "out-of-band secret changes must make the instance unready")
	_, err = service.ReenrollLegacy(secretintegrity.ReenrollCommand{
		Confirmation: secretintegrity.ReenrollConfirmation, Actor: "admin-a",
	})
	require.NoError(t, err)
	assert.Error(t, service.Check(), "canonical mismatches cannot be overwritten by legacy re-enrollment")
}

func TestReadinessLoadsAllRuntimeAppConfigsInOneBatchAndHonorsContext(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_BATCH_ID":      "batch-id",
		"env://WORKBENCH_PROVIDER_BATCH_KEY":     "batch-key",
		"env://WORKBENCH_PROVIDER_BATCH_APP_KEY": "batch-app-key",
	}
	profile := model.CredentialProfile{
		OwnerScope: "platform", ProviderEnvironment: model.ProviderChinaTencentADP, Name: "batch",
		SecretIDRef: "env://WORKBENCH_PROVIDER_BATCH_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_BATCH_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("batch-id", "batch-key"), FingerprintVersion: secrets.CanonicalFingerprintVersion,
		Status: model.CredentialStatusActive, Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&profile).Error)
	for index := 0; index < 4; index++ {
		customer := model.Customer{CustomerCode: fmt.Sprintf("batch-%d", index), DisplayName: "Batch", Status: model.CustomerStatusActive, RowVersion: 1}
		require.NoError(t, db.Create(&customer).Error)
		application := model.CustomerApp{
			CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
			AppID: fmt.Sprintf("batch-app-%d", index), DisplayName: "Batch", Status: model.AppStatusVerified, AuthEpoch: 1, RowVersion: 1,
		}
		require.NoError(t, db.Create(&application).Error)
		config := model.AppConfigVersion{
			CustomerAppID: application.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
			Region: "ap-guangzhou", SpaceID: "space", TemplateAgentID: "agent", CredentialProfileID: &profile.ID,
			AppKeySecretRef: "env://WORKBENCH_PROVIDER_BATCH_APP_KEY", AppKeyFingerprint: secrets.AppKeyFingerprint("batch-app-key"),
			AppKeyFingerprintVersion: secrets.CanonicalFingerprintVersion, RowVersion: 1,
			LimitsJSON: `{}`, CapabilitiesJSON: `[]`, CreatedBy: "test",
		}
		require.NoError(t, db.Create(&config).Error)
		application.CurrentConfigVersionID = &config.ID
		require.NoError(t, db.Save(&application).Error)
	}
	configQueries := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:count_app_config_readiness_queries", func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AppConfigVersion{}).TableName() {
			configQueries++
		}
	}))
	defer db.Callback().Query().Remove("test:count_app_config_readiness_queries")
	service := secretintegrity.New(db, resolver)
	require.NoError(t, service.CheckContext(context.Background()))
	assert.Equal(t, 1, configQueries, "readiness must batch App-config reads instead of issuing one query per App")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, service.CheckContext(canceled))
}
