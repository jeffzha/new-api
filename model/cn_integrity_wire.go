package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// Sidecar hashes use RFC 8785, separately from existing v2 fact digests and
// internal persistence checks. The upstream implementation owns normalization.
func CNIntegrityCanonicalJSON(input []byte) ([]byte, error) {
	return jsoncanonicalizer.Transform(input)
}

func CNIntegrityBodyHash(input []byte) (string, error) {
	canonical, err := CNIntegrityCanonicalJSON(input)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(canonical)), nil
}

// Neither stream epoch nor integrity epoch is identity material. Multiple
// economic facts from one original mutation share the same native chain owner.
func CNIntegrityNativeIdentity(gatewayID string, user int, nativeKind, nativeKey string) (chainKey, lineageID string, err error) {
	if gatewayID == "" || len(gatewayID) > 191 || user <= 0 || nativeKey == "" || len(nativeKey) > 255 {
		return "", "", errors.New("invalid native integrity identity")
	}
	var entity string
	switch nativeKind {
	case "topup_credit":
		entity = "topup"
	case "wallet_reserve", "wallet_release", "wallet_final":
		entity = "wallet"
	case "task_reserve", "task_revision":
		entity = "task"
	case "quota_mutation", "source_switch":
		entity = "user"
		if nativeKey != strconv.Itoa(user) {
			return "", "", errors.New("native user chain key mismatch")
		}
	default:
		return "", "", errors.New("unsupported native integrity kind")
	}
	body, err := common.Marshal([]string{"cn-native-chain-v2", "gateway-cn", gatewayID, strconv.Itoa(user), entity, nativeKey})
	if err != nil {
		return "", "", err
	}
	chainHash, err := CNIntegrityBodyHash(body)
	if err != nil {
		return "", "", err
	}
	chainKey = strings.TrimPrefix(chainHash, "sha256:")
	body, err = common.Marshal([]string{"cn-native-lineage-v2", chainKey})
	if err != nil {
		return "", "", err
	}
	lineageHash, err := CNIntegrityBodyHash(body)
	return chainKey, strings.TrimPrefix(lineageHash, "sha256:"), err
}
