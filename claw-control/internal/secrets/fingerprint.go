package secrets

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strconv"
)

const CanonicalFingerprintVersion = 1

const (
	credentialPairDomain = "claw-control/tencent-secret-pair/v1"
	appKeyDomain         = "claw-control/tencent-app-key/v1"
)

var (
	ErrProviderSecretUnavailable  = errors.New("provider secret material is unavailable")
	ErrProviderFingerprintInvalid = errors.New("provider secret fingerprint is invalid")
)

type CredentialPair struct {
	SecretID  string
	SecretKey string
}

func CredentialPairFingerprint(secretID, secretKey string) string {
	return canonicalFingerprint(credentialPairDomain, secretID, secretKey)
}

func AppKeyFingerprint(appKey string) string {
	return canonicalFingerprint(appKeyDomain, appKey)
}

func ResolveCredentialPair(resolver Resolver, secretIDRef, secretKeyRef, expectedFingerprint string, fingerprintVersion int) (CredentialPair, error) {
	pair, fingerprint, err := ResolveCredentialPairFingerprint(resolver, secretIDRef, secretKeyRef)
	if err != nil {
		return CredentialPair{}, err
	}
	if fingerprintVersion != CanonicalFingerprintVersion || !fingerprintsEqual(fingerprint, expectedFingerprint) {
		return CredentialPair{}, ErrProviderFingerprintInvalid
	}
	return pair, nil
}

func ResolveCredentialPairFingerprint(resolver Resolver, secretIDRef, secretKeyRef string) (CredentialPair, string, error) {
	if resolver == nil {
		return CredentialPair{}, "", ErrProviderSecretUnavailable
	}
	secretID, err := resolver.Resolve(secretIDRef)
	if err != nil || secretID == "" {
		return CredentialPair{}, "", ErrProviderSecretUnavailable
	}
	secretKey, err := resolver.Resolve(secretKeyRef)
	if err != nil || secretKey == "" {
		return CredentialPair{}, "", ErrProviderSecretUnavailable
	}
	pair := CredentialPair{SecretID: secretID, SecretKey: secretKey}
	return pair, CredentialPairFingerprint(secretID, secretKey), nil
}

func ResolveAppKey(resolver Resolver, appKeyRef, expectedFingerprint string, fingerprintVersion int) (string, error) {
	appKey, fingerprint, err := ResolveAppKeyFingerprint(resolver, appKeyRef)
	if err != nil {
		return "", err
	}
	if fingerprintVersion != CanonicalFingerprintVersion || !fingerprintsEqual(fingerprint, expectedFingerprint) {
		return "", ErrProviderFingerprintInvalid
	}
	return appKey, nil
}

func ResolveAppKeyFingerprint(resolver Resolver, appKeyRef string) (string, string, error) {
	if resolver == nil {
		return "", "", ErrProviderSecretUnavailable
	}
	appKey, err := resolver.Resolve(appKeyRef)
	if err != nil || appKey == "" {
		return "", "", ErrProviderSecretUnavailable
	}
	return appKey, AppKeyFingerprint(appKey), nil
}

func canonicalFingerprint(domain string, values ...string) string {
	hash := sha256.New()
	writeCanonicalPart(hash, domain)
	for _, value := range values {
		writeCanonicalPart(hash, value)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

type canonicalWriter interface {
	Write([]byte) (int, error)
}

func writeCanonicalPart(writer canonicalWriter, value string) {
	length := strconv.Itoa(len(value))
	_, _ = writer.Write([]byte(length))
	_, _ = writer.Write([]byte{':'})
	_, _ = writer.Write([]byte(value))
	_, _ = writer.Write([]byte{'\n'})
}

func fingerprintsEqual(actual, expected string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}
