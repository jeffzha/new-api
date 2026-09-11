package agencyhub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	CommandActionFundingReverse     = "funding.reverse"
	CommandActionProvisioningStart  = "provisioning.start"
	CommandActionProvisioningCancel = "provisioning.cancel"
	CommandStatusQueued             = "queued"
	CommandStatusProcessing         = "processing"
	CommandStatusSucceeded          = "succeeded"
	CommandStatusFailed             = "failed"
	CommandStatusCancelled          = "cancelled"
	CommandServiceSignatureHeader   = "X-Agency-Hub-Signature"
	CommandMaxLifetime              = 5 * time.Minute
	commandClockSkew                = 30 * time.Second
)

var allowedCommandActions = map[string]struct{}{
	CommandActionFundingReverse: {}, CommandActionProvisioningStart: {}, CommandActionProvisioningCancel: {},
}

// AgencyCommandRequest is the signed command envelope accepted by the
// internal endpoint. Payload is retained as RawMessage so its canonical hash
// can be checked before any action-specific decoder is invoked.
type AgencyCommandRequest struct {
	CommandID       string          `json:"command_id"`
	Action          string          `json:"action"`
	Actor           string          `json:"actor"`
	SourceSID       string          `json:"source_sid"`
	ObjectID        string          `json:"object_id"`
	ExpectedVersion int64           `json:"expected_version"`
	Payload         json.RawMessage `json:"payload"`
	IssuedAt        int64           `json:"issued_at"`
	ExpiresAt       int64           `json:"expiry"`
	BodyHash        string          `json:"body_hash"`
	HubSignature    string          `json:"hub_signature"`
	RootProof       string          `json:"root_proof"`
}

// commandEnvelope is the exact set of fields protected by the hub service
// signature. The signature itself is intentionally excluded. Canonical bytes
// are produced by canonicalJSON, which sorts object keys recursively.
type commandEnvelope struct {
	CommandID       string          `json:"command_id"`
	Action          string          `json:"action"`
	Actor           string          `json:"actor"`
	SourceSID       string          `json:"source_sid"`
	ObjectID        string          `json:"object_id"`
	ExpectedVersion int64           `json:"expected_version"`
	Payload         json.RawMessage `json:"payload"`
	IssuedAt        int64           `json:"issued_at"`
	ExpiresAt       int64           `json:"expiry"`
	BodyHash        string          `json:"body_hash"`
	RootProof       string          `json:"root_proof"`
}

func (r AgencyCommandRequest) envelope() commandEnvelope {
	return commandEnvelope{CommandID: r.CommandID, Action: r.Action, Actor: r.Actor, SourceSID: r.SourceSID, ObjectID: r.ObjectID, ExpectedVersion: r.ExpectedVersion, Payload: r.Payload, IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt, BodyHash: r.BodyHash, RootProof: r.RootProof}
}

// CanonicalPayload returns the canonical UTF-8 JSON bytes used by body_hash.
// It rejects non-object payloads, exponents/fractions for numeric values, and
// malformed values. Monetary quantities therefore must be represented as
// strings, as required by the command contract.
func CanonicalPayload(payload []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("payload must be a JSON object")
	}
	if err := rejectDuplicateObjectKeys(trimmed); err != nil {
		return nil, err
	}
	return canonicalJSON(trimmed, 0)
}

func rejectDuplicateObjectKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err == nil {
		return errors.New("multiple JSON values are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("invalid JSON object")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key: %s", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
		return nil
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func canonicalJSON(raw []byte, depth int) ([]byte, error) {
	if depth > 32 {
		return nil, errors.New("payload nesting exceeds limit")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("empty JSON")
	}
	switch raw[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := common.Unmarshal(raw, &object); err != nil {
			return nil, errors.New("invalid JSON object")
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var out bytes.Buffer
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			encodedKey, _ := common.Marshal(key)
			out.Write(encodedKey)
			out.WriteByte(':')
			value, err := canonicalJSON(object[key], depth+1)
			if err != nil {
				return nil, err
			}
			out.Write(value)
		}
		out.WriteByte('}')
		return out.Bytes(), nil
	case '[':
		var array []json.RawMessage
		if err := common.Unmarshal(raw, &array); err != nil {
			return nil, errors.New("invalid JSON array")
		}
		var out bytes.Buffer
		out.WriteByte('[')
		for index, item := range array {
			if index > 0 {
				out.WriteByte(',')
			}
			value, err := canonicalJSON(item, depth+1)
			if err != nil {
				return nil, err
			}
			out.Write(value)
		}
		out.WriteByte(']')
		return out.Bytes(), nil
	case '"':
		var value string
		if err := common.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("invalid JSON string")
		}
		return common.Marshal(value)
	case 't':
		if bytes.Equal(raw, []byte("true")) {
			return []byte("true"), nil
		}
	case 'f':
		if bytes.Equal(raw, []byte("false")) {
			return []byte("false"), nil
		}
	case 'n':
		if bytes.Equal(raw, []byte("null")) {
			return []byte("null"), nil
		}
	default:
		// Strict decimal integers avoid float rounding and exponent aliases.
		if validCanonicalInteger(raw) {
			return normalizeInteger(raw), nil
		}
	}
	return nil, errors.New("unsupported or non-canonical JSON value")
}

func validCanonicalInteger(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	start := 0
	if raw[0] == '-' {
		start = 1
	}
	if start == len(raw) {
		return false
	}
	if raw[start] == '0' {
		return start+1 == len(raw)
	}
	for _, char := range raw[start:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func normalizeInteger(raw []byte) []byte {
	negative := raw[0] == '-'
	if negative {
		raw = raw[1:]
	}
	index := 0
	for index+1 < len(raw) && raw[index] == '0' {
		index++
	}
	raw = raw[index:]
	if negative && !bytes.Equal(raw, []byte("0")) {
		return append([]byte{'-'}, raw...)
	}
	return raw
}

func CanonicalCommandEnvelope(req AgencyCommandRequest) ([]byte, error) {
	payload, err := CanonicalPayload(req.Payload)
	if err != nil {
		return nil, err
	}
	envelope := req.envelope()
	envelope.Payload = json.RawMessage(payload)
	encoded, err := common.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(encoded, 0)
}

func CommandBodyHash(payload []byte) (string, error) {
	canonical, err := CanonicalPayload(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// SignCommandEnvelope is shared by the hub client and tests. Ed25519 is
// intentionally fixed; callers may encode the result as a body field.
func SignCommandEnvelope(privateKey ed25519.PrivateKey, req AgencyCommandRequest) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("invalid command service key")
	}
	canonical, err := CanonicalCommandEnvelope(req)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(privateKey, canonical)
	return base64.RawURLEncoding.EncodeToString(signature), nil
}

func (a *App) signCommandEnvelope(req AgencyCommandRequest) (string, error) {
	if len(a.commandServicePrivateKey) != ed25519.PrivateKeySize {
		return "", errors.New("command service private key is not configured")
	}
	return SignCommandEnvelope(ed25519.PrivateKey(a.commandServicePrivateKey), req)
}

func decodeCommandSignature(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("missing command service signature")
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid command service signature")
	}
	return decoded, nil
}

func (a *App) verifyCommandServiceSignature(req AgencyCommandRequest, supplied string) error {
	return VerifyCommandServiceSignature(a.commandServicePublicKey, req, supplied)
}

// VerifyCommandServiceSignature verifies the hub service signature over the
// complete command envelope. The gateway uses the same helper when it reads a
// command row written by the hub, so direct shared-database delivery cannot
// bypass the service-authentication boundary.
func VerifyCommandServiceSignature(publicKey ed25519.PublicKey, req AgencyCommandRequest, supplied string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("command service key is not configured")
	}
	canonical, err := CanonicalCommandEnvelope(req)
	if err != nil {
		return err
	}
	signature, err := decodeCommandSignature(supplied)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("invalid command service signature")
	}
	return nil
}

func commandPayloadAllowed(action string, payload []byte) error {
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(payload, &fields); err != nil || fields == nil {
		return errors.New("payload must be a JSON object")
	}
	allowed := map[string]struct{}{}
	switch action {
	case CommandActionFundingReverse:
		for _, key := range []string{"original_event_id", "original_operation_id", "refund_id", "user_id", "quota", "refund_quota", "currency_code", "payment_reference", "evidence_ref", "reason"} {
			allowed[key] = struct{}{}
		}
	case CommandActionProvisioningStart:
		for _, key := range []string{"user_id", "invite_code", "expected_user_version", "reason"} {
			allowed[key] = struct{}{}
		}
	case CommandActionProvisioningCancel:
		for _, key := range []string{"user_id", "reason"} {
			allowed[key] = struct{}{}
		}
	default:
		return errors.New("unsupported command action")
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown payload field: %s", key)
		}
	}
	return nil
}

func validateCommandRequest(req AgencyCommandRequest, now int64) (string, error) {
	if _, ok := allowedCommandActions[req.Action]; !ok {
		return "", errors.New("unsupported command action")
	}
	for field, value := range map[string]string{"command_id": req.CommandID, "actor": req.Actor, "source_sid": req.SourceSID, "object_id": req.ObjectID, "body_hash": req.BodyHash, "root_proof": req.RootProof} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s is required", field)
		}
		if len(value) > 4096 {
			return "", fmt.Errorf("%s is too long", field)
		}
	}
	if req.ExpectedVersion < 0 {
		return "", errors.New("expected_version must not be negative")
	}
	if req.IssuedAt <= 0 || req.ExpiresAt <= 0 || req.ExpiresAt <= req.IssuedAt {
		return "", errors.New("invalid command validity interval")
	}
	if req.ExpiresAt-req.IssuedAt > int64(CommandMaxLifetime/time.Second) {
		return "", errors.New("command validity exceeds five minutes")
	}
	if req.IssuedAt > now+int64(commandClockSkew/time.Second) || req.ExpiresAt <= now {
		return "", errors.New("command is expired or issued in the future")
	}
	if len(req.Payload) == 0 || len(req.Payload) > 1<<20 {
		return "", errors.New("invalid command payload size")
	}
	if err := commandPayloadAllowed(req.Action, req.Payload); err != nil {
		return "", err
	}
	hash, err := CommandBodyHash(req.Payload)
	if err != nil {
		return "", err
	}
	if len(req.BodyHash) != sha256.Size*2 || !strings.EqualFold(hash, req.BodyHash) {
		return "", errors.New("body_hash mismatch")
	}
	return hash, nil
}

func commandActorID(actor string) (int64, error) {
	actor = strings.TrimSpace(actor)
	if strings.HasPrefix(actor, "root:") {
		actor = strings.TrimPrefix(actor, "root:")
	}
	value, err := strconv.ParseInt(actor, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid root actor")
	}
	return value, nil
}

func (a *App) verifyRootCommandProof(req AgencyCommandRequest, bodyHash string, now int64) (SSOTicketClaims, error) {
	if len(a.ssoPublicKey) != ed25519.PublicKeySize {
		return SSOTicketClaims{}, errors.New("root proof key is not configured")
	}
	claims, err := VerifySSOTicket(ed25519.PublicKey(a.ssoPublicKey), req.RootProof, "new-api", "agency-gateway-command")
	if err != nil {
		return SSOTicketClaims{}, err
	}
	actorID, err := commandActorID(req.Actor)
	if err != nil || claims.Subject != actorID || claims.SourceSID != req.SourceSID || claims.Action != req.Action || claims.CommandID != req.CommandID || claims.ObjectID != req.ObjectID || claims.ExpectedVersion != req.ExpectedVersion || claims.BodyHash != bodyHash {
		return SSOTicketClaims{}, errors.New("root proof command binding mismatch")
	}
	if claims.ExpiresAt != req.ExpiresAt || claims.IssuedAt != req.IssuedAt || claims.ExpiresAt <= now || claims.IssuedAt > now+int64(commandClockSkew/time.Second) {
		return SSOTicketClaims{}, errors.New("root proof is expired")
	}
	return claims, nil
}

func (a *App) createInternalCommand(c *gin.Context) {
	if a.config.CommandRequireTLS && c.Request.TLS == nil {
		respondError(c, http.StatusForbidden, "internal_transport_required", "命令接口仅允许mTLS内部连接", nil)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		respondError(c, http.StatusRequestEntityTooLarge, "request_too_large", "请求正文过大", nil)
		return
	}
	if err = rejectDuplicateObjectKeys(raw); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "命令JSON包含重复字段", nil)
		return
	}
	var req AgencyCommandRequest
	if err = common.Unmarshal(raw, &req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "命令格式错误", nil)
		return
	}
	// Reject unknown top-level fields. This keeps the signed DTO stable and
	// prevents an intermediary from smuggling unsigned business attributes.
	var fields map[string]json.RawMessage
	if err = common.Unmarshal(raw, &fields); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "命令格式错误", nil)
		return
	}
	known := map[string]struct{}{"command_id": {}, "action": {}, "actor": {}, "source_sid": {}, "object_id": {}, "expected_version": {}, "payload": {}, "issued_at": {}, "expiry": {}, "body_hash": {}, "hub_signature": {}, "root_proof": {}}
	for key := range fields {
		if _, ok := known[key]; !ok {
			respondError(c, http.StatusUnprocessableEntity, "unknown_field", "命令包含未知字段", key)
			return
		}
	}
	now := time.Now().Unix()
	signature := strings.TrimSpace(req.HubSignature)
	headerSignature := strings.TrimSpace(c.GetHeader(CommandServiceSignatureHeader))
	if signature != "" && headerSignature != "" && signature != headerSignature {
		respondError(c, http.StatusUnauthorized, "invalid_service_signature", "服务签名无效", nil)
		return
	}
	if signature == "" {
		signature = headerSignature
	}
	if err = a.verifyCommandServiceSignature(req, signature); err != nil {
		respondError(c, http.StatusUnauthorized, "invalid_service_signature", "服务签名无效", nil)
		return
	}
	bodyHash, err := validateCommandRequest(req, now)
	if err != nil {
		// An already accepted command remains queryable/replayable after its
		// five-minute submission window. The service signature still protects
		// this lookup, while a new command continues to require a live proof.
		if strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "future") {
			var existing model.AgencyCommand
			if lookupErr := a.db.Where("command_id = ?", req.CommandID).First(&existing).Error; lookupErr == nil && commandEnvelopeMatchesStored(req, existing, req.BodyHash) {
				respondAccepted(c, commandResponse(existing))
				return
			}
		}
		respondError(c, http.StatusUnprocessableEntity, "invalid_command", err.Error(), nil)
		return
	}
	claims, err := a.verifyRootCommandProof(req, bodyHash, now)
	if err != nil {
		respondError(c, http.StatusForbidden, "invalid_root_proof", "Root授权证明无效", nil)
		return
	}
	// A command_id retry is answered from the durable row and deliberately does
	// not consume the proof a second time. A different body/action is a conflict.
	var existing model.AgencyCommand
	lookupErr := a.db.Where("command_id = ?", req.CommandID).First(&existing).Error
	if lookupErr == nil {
		if !commandEnvelopeMatchesStored(req, existing, bodyHash) || existing.RootProofJTI != claims.JTI {
			respondError(c, http.StatusConflict, "command_conflict", "command_id已用于其他命令", nil)
			return
		}
		respondAccepted(c, commandResponse(existing))
		return
	}
	if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusInternalServerError, "database_error", "命令读取失败", nil)
		return
	}
	var jtiUse model.AgencyCommand
	if err = a.db.Where("root_proof_jti = ?", claims.JTI).First(&jtiUse).Error; err == nil {
		respondError(c, http.StatusConflict, "proof_replayed", "Root授权证明已用于其他命令", nil)
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusInternalServerError, "database_error", "命令读取失败", nil)
		return
	}
	canonicalPayload, _ := CanonicalPayload(req.Payload)
	created := &model.AgencyCommand{CommandID: req.CommandID, Action: req.Action, Actor: req.Actor, SourceSID: req.SourceSID, ObjectID: req.ObjectID, ExpectedVersion: req.ExpectedVersion, Payload: string(canonicalPayload), BodyHash: bodyHash, IssuedAt: req.IssuedAt, ExpiresAt: req.ExpiresAt, HubSignature: signature, RootProof: req.RootProof, RootProofJTI: claims.JTI, Status: CommandStatusQueued, CreatedAt: now, UpdatedAt: now}
	if err = a.db.Create(created).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			var raced model.AgencyCommand
			if lookupErr := a.db.Where("command_id = ?", req.CommandID).First(&raced).Error; lookupErr == nil {
				if commandEnvelopeMatchesStored(req, raced, bodyHash) && raced.RootProofJTI == claims.JTI {
					respondAccepted(c, commandResponse(raced))
					return
				}
				respondError(c, http.StatusConflict, "command_conflict", "command_id已用于其他命令", nil)
				return
			}
			if lookupErr := a.db.Where("root_proof_jti = ?", claims.JTI).First(&raced).Error; lookupErr == nil {
				respondError(c, http.StatusConflict, "proof_replayed", "Root授权证明已用于其他命令", nil)
				return
			}
			respondError(c, http.StatusConflict, "command_conflict", "命令已存在但无法读取", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, "database_error", "命令写入失败", nil)
		return
	}
	respondAccepted(c, commandResponse(*created))
}

func commandEnvelopeMatchesStored(req AgencyCommandRequest, stored model.AgencyCommand, bodyHash string) bool {
	return stored.CommandID == req.CommandID &&
		stored.Action == req.Action &&
		strings.EqualFold(stored.BodyHash, bodyHash) &&
		stored.Actor == req.Actor &&
		stored.SourceSID == req.SourceSID &&
		stored.ObjectID == req.ObjectID &&
		stored.ExpectedVersion == req.ExpectedVersion &&
		stored.RootProof == req.RootProof
}

func commandResponse(command model.AgencyCommand) gin.H {
	result := gin.H{"command_id": command.CommandID, "action": command.Action, "status": command.Status, "status_url": "/internal/agency/v1/commands/" + command.CommandID, "issued_at": command.IssuedAt, "expiry": command.ExpiresAt}
	if command.ResultCode != 0 {
		result["result_code"] = command.ResultCode
	}
	if command.ResultJSON != "" {
		var value any
		if common.Unmarshal([]byte(command.ResultJSON), &value) == nil {
			result["result"] = value
		}
	}
	if command.LastError != "" {
		result["error"] = command.LastError
	}
	if command.CompletedAt != nil {
		result["completed_at"] = *command.CompletedAt
	}
	return result
}

// SetInternalCommandResult is used by the gateway command worker after the
// authoritative Model transaction finishes. It is deliberately explicit
// about the terminal status and only writes the durable result; no business
// balance is changed by the hub.
func (a *App) SetInternalCommandResult(commandID, status string, resultCode int, result any, lastError string) error {
	if strings.TrimSpace(commandID) == "" {
		return errors.New("command_id is required")
	}
	switch status {
	case CommandStatusProcessing, CommandStatusSucceeded, CommandStatusFailed, CommandStatusCancelled:
	default:
		return errors.New("invalid command result status")
	}
	if resultCode < 0 {
		return errors.New("invalid command result code")
	}
	resultJSON := ""
	if result != nil {
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		if len(encoded) > 1<<20 {
			return errors.New("command result is too large")
		}
		resultJSON = string(encoded)
	}
	return a.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		var current model.AgencyCommand
		if err := model.AgencyLockForUpdate(tx).Where("command_id = ?", commandID).First(&current).Error; err != nil {
			return err
		}
		if current.Status == CommandStatusSucceeded || current.Status == CommandStatusFailed || current.Status == CommandStatusCancelled {
			if current.Status == status {
				return nil
			}
			return errors.New("command is already terminal")
		}
		updates := map[string]any{"status": status, "result_code": resultCode, "result_json": resultJSON, "last_error": strings.TrimSpace(lastError), "updated_at": now}
		if status == CommandStatusSucceeded || status == CommandStatusFailed || status == CommandStatusCancelled {
			updates["completed_at"] = now
		}
		resultDB := tx.Model(&model.AgencyCommand{}).Where("id = ?", current.ID).Updates(updates)
		if resultDB.Error != nil {
			return resultDB.Error
		}
		if resultDB.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (a *App) getInternalCommand(c *gin.Context) {
	if a.config.CommandRequireTLS && c.Request.TLS == nil {
		respondError(c, http.StatusForbidden, "internal_transport_required", "命令接口仅允许mTLS内部连接", nil)
		return
	}
	commandID := strings.TrimSpace(c.Param("id"))
	if commandID == "" || len(commandID) > 128 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的command_id", nil)
		return
	}
	var command model.AgencyCommand
	if err := a.db.Where("command_id = ?", commandID).First(&command).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "command_not_found", "命令不存在", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, "database_error", "命令读取失败", nil)
		return
	}
	// GET uses the stored envelope, with a fresh service signature required to
	// prevent an untrusted caller from enumerating command state.
	req := AgencyCommandRequest{CommandID: command.CommandID, Action: command.Action, Actor: command.Actor, SourceSID: command.SourceSID, ObjectID: command.ObjectID, ExpectedVersion: command.ExpectedVersion, Payload: json.RawMessage(command.Payload), IssuedAt: command.IssuedAt, ExpiresAt: command.ExpiresAt, BodyHash: command.BodyHash, RootProof: command.RootProof}
	signature := c.GetHeader(CommandServiceSignatureHeader)
	if err := a.verifyCommandServiceSignature(req, signature); err != nil {
		respondError(c, http.StatusUnauthorized, "invalid_service_signature", "服务签名无效", nil)
		return
	}
	respondOK(c, commandResponse(command))
}
