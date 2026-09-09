package workbenchbridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ContractVersionHeader   = "X-Workbench-Contract-Version"
	ContractVersion         = "1"
	InternalTimestampHeader = "X-Workbench-Timestamp"
	InternalNonceHeader     = "X-Workbench-Nonce"
	InternalSignatureHeader = "X-Workbench-Signature"
	ResponseTimestampHeader = "X-Workbench-Response-Timestamp"
	ResponseNonceHeader     = "X-Workbench-Response-Nonce"
	ResponseSignatureHeader = "X-Workbench-Response-Signature"
)

var ErrInvalidInternalSignature = errors.New("invalid workbench internal request signature")

func SignInternalRequest(secret []byte, method string, path string, timestamp int64, nonce string, body []byte) string {
	bodyDigest := sha256.Sum256(body)
	canonical := fmt.Sprintf(
		"%s\n%d\n%s\n%s\n%s\n%s",
		ContractVersion,
		timestamp,
		nonce,
		strings.ToUpper(method),
		path,
		hex.EncodeToString(bodyDigest[:]),
	)
	signer := hmac.New(sha256.New, secret)
	_, _ = signer.Write([]byte(canonical))
	return "sha256=" + hex.EncodeToString(signer.Sum(nil))
}

func VerifyInternalRequest(
	secret []byte,
	method string,
	path string,
	contractVersionHeader string,
	timestampHeader string,
	nonceHeader string,
	signatureHeader string,
	body []byte,
	now time.Time,
	maxSkew time.Duration,
) error {
	if strings.TrimSpace(contractVersionHeader) != ContractVersion {
		return ErrInvalidInternalSignature
	}
	timestamp, err := strconv.ParseInt(strings.TrimSpace(timestampHeader), 10, 64)
	if err != nil || maxSkew <= 0 {
		return ErrInvalidInternalSignature
	}
	signedAt := time.Unix(timestamp, 0)
	delta := now.Sub(signedAt)
	if delta < -maxSkew || delta > maxSkew {
		return ErrInvalidInternalSignature
	}
	nonce := strings.TrimSpace(nonceHeader)
	decodedNonce, err := hex.DecodeString(nonce)
	if err != nil || len(nonce) != 64 || len(decodedNonce) != 32 {
		return ErrInvalidInternalSignature
	}

	expected := SignInternalRequest(secret, method, path, timestamp, nonce, body)
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signatureHeader))) {
		return ErrInvalidInternalSignature
	}
	return nil
}

func SignInternalResponse(secret []byte, status int, path string, timestamp int64, requestNonce string, body []byte) string {
	bodyDigest := sha256.Sum256(body)
	canonical := fmt.Sprintf(
		"%s\n%d\n%s\n%d\n%s\n%s",
		ContractVersion,
		status,
		path,
		timestamp,
		requestNonce,
		hex.EncodeToString(bodyDigest[:]),
	)
	signer := hmac.New(sha256.New, secret)
	_, _ = signer.Write([]byte(canonical))
	return hex.EncodeToString(signer.Sum(nil))
}
