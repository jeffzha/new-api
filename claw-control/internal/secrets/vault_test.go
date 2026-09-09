package secrets_test

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProviderVaultStoresWriteOnlyAppKeyAndDetectsTampering(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	vault, err := secrets.NewVaultResolver(db, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	var reference, fingerprint string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var storeErr error
		reference, fingerprint, storeErr = vault.StoreAppKey(tx, 42, "customer-app-key-value", "root:1")
		return storeErr
	}))
	assert.True(t, secrets.ValidVaultReference(reference))
	assert.Equal(t, secrets.AppKeyFingerprint("customer-app-key-value"), fingerprint)
	resolved, err := vault.Resolve(reference)
	require.NoError(t, err)
	assert.Equal(t, "customer-app-key-value", resolved)

	var record model.ProviderSecret
	require.NoError(t, db.First(&record).Error)
	assert.NotContains(t, record.Ciphertext, "customer-app-key-value")
	assert.True(t, strings.HasPrefix(record.PublicID, "pvs_"))
	require.NoError(t, db.Model(&record).Update("ciphertext", "invalid").Error)
	_, err = vault.Resolve(reference)
	assert.ErrorIs(t, err, secrets.ErrProviderSecretUnavailable)
}
