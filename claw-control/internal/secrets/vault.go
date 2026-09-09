package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	providerVaultKeyVersion  = 1
	providerSecretKindAppKey = "app_key"
)

var vaultReferencePattern = regexp.MustCompile(`^vault://(pvs_[a-f0-9]{32})$`)

// AppKeyWriter is implemented by resolvers that can atomically persist a
// write-only AppKey in the same database transaction as its App config.
type AppKeyWriter interface {
	StoreAppKey(tx *gorm.DB, customerID uint64, value, actor string) (reference, fingerprint string, err error)
}

type VaultResolver struct {
	db   *gorm.DB
	aead cipher.AEAD
}

func NewVaultResolver(db *gorm.DB, key []byte) (*VaultResolver, error) {
	if db == nil || len(key) != 32 {
		return nil, errors.New("provider vault requires a database and exactly 32 key bytes")
	}
	keyCopy := append([]byte(nil), key...)
	block, err := aes.NewCipher(keyCopy)
	clear(keyCopy)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &VaultResolver{db: db, aead: aead}, nil
}

func (resolver *VaultResolver) StoreAppKey(tx *gorm.DB, customerID uint64, value, actor string) (string, string, error) {
	actor = strings.TrimSpace(actor)
	if resolver == nil || resolver.aead == nil || tx == nil || customerID == 0 || actor == "" {
		return "", "", errors.New("provider vault is unavailable")
	}
	if len(value) < 16 || len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
		return "", "", errors.New("AppKey must contain 16-4096 non-NUL characters without surrounding whitespace")
	}
	publicID := "pvs_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	nonce := make([]byte, resolver.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", errors.New("generate provider secret nonce")
	}
	aad := providerSecretAAD(publicID, customerID, providerSecretKindAppKey)
	plaintext := []byte(value)
	sealed := resolver.aead.Seal(nil, nonce, plaintext, aad)
	clear(plaintext)
	payload := append(nonce, sealed...)
	fingerprint := AppKeyFingerprint(value)
	record := &model.ProviderSecret{
		PublicID: publicID, CustomerID: customerID, Kind: providerSecretKindAppKey,
		Ciphertext: base64.RawStdEncoding.EncodeToString(payload), Fingerprint: fingerprint,
		KeyVersion: providerVaultKeyVersion, CreatedBy: actor,
	}
	clear(payload)
	if err := tx.Create(record).Error; err != nil {
		return "", "", err
	}
	return "vault://" + publicID, fingerprint, nil
}

func (resolver *VaultResolver) Resolve(reference string) (string, error) {
	if resolver == nil || resolver.db == nil || resolver.aead == nil {
		return "", ErrProviderSecretUnavailable
	}
	match := vaultReferencePattern.FindStringSubmatch(strings.TrimSpace(reference))
	if len(match) != 2 {
		return "", ErrProviderSecretUnavailable
	}
	var record model.ProviderSecret
	if err := resolver.db.Where("public_id = ? AND revoked_at IS NULL", match[1]).First(&record).Error; err != nil {
		return "", ErrProviderSecretUnavailable
	}
	if record.Kind != providerSecretKindAppKey || record.KeyVersion != providerVaultKeyVersion {
		return "", ErrProviderSecretUnavailable
	}
	payload, err := base64.RawStdEncoding.Strict().DecodeString(record.Ciphertext)
	if err != nil || len(payload) <= resolver.aead.NonceSize() {
		return "", ErrProviderSecretUnavailable
	}
	nonce, ciphertext := payload[:resolver.aead.NonceSize()], payload[resolver.aead.NonceSize():]
	plaintext, err := resolver.aead.Open(nil, nonce, ciphertext, providerSecretAAD(record.PublicID, record.CustomerID, record.Kind))
	clear(payload)
	if err != nil || len(plaintext) == 0 {
		clear(plaintext)
		return "", ErrProviderSecretUnavailable
	}
	value := string(plaintext)
	clear(plaintext)
	actual := AppKeyFingerprint(value)
	if subtle.ConstantTimeCompare([]byte(actual), []byte(record.Fingerprint)) != 1 {
		return "", ErrProviderFingerprintInvalid
	}
	return value, nil
}

func providerSecretAAD(publicID string, customerID uint64, kind string) []byte {
	return []byte(fmt.Sprintf("claw-provider-vault/v1|%s|%d|%s", publicID, customerID, kind))
}

type CompositeResolver struct {
	environment Resolver
	vault       *VaultResolver
}

func NewCompositeResolver(environment Resolver, vault *VaultResolver) *CompositeResolver {
	return &CompositeResolver{environment: environment, vault: vault}
}

func (resolver *CompositeResolver) Resolve(reference string) (string, error) {
	if resolver == nil {
		return "", ErrProviderSecretUnavailable
	}
	if ValidVaultReference(reference) {
		if resolver.vault == nil {
			return "", ErrProviderSecretUnavailable
		}
		return resolver.vault.Resolve(reference)
	}
	if resolver.environment == nil {
		return "", ErrProviderSecretUnavailable
	}
	return resolver.environment.Resolve(reference)
}

func (resolver *CompositeResolver) StoreAppKey(tx *gorm.DB, customerID uint64, value, actor string) (string, string, error) {
	if resolver == nil || resolver.vault == nil {
		return "", "", ErrProviderSecretUnavailable
	}
	return resolver.vault.StoreAppKey(tx, customerID, value, actor)
}

func ValidVaultReference(reference string) bool {
	return vaultReferencePattern.MatchString(strings.TrimSpace(reference))
}
