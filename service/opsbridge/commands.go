package opsbridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type CommandExecutor interface {
	ExecuteOpsAgencyCommand(context.Context, OpsAgencyCommand) (map[string]any, error)
}

type OpsAgencyCommand struct {
	ID          string
	Kind        string
	AgencyID    string
	Payload     map[string]any
	PayloadHash string
}

type CommandReceipt struct {
	CommandID        string         `json:"command_id"`
	ExecutionAttempt int            `json:"execution_attempt,omitempty"`
	PayloadHash      string         `json:"payload_hash"`
	LeaseToken       string         `json:"lease_token"`
	Outcome          string         `json:"outcome"`
	Result           map[string]any `json:"result"`
	Status           string         `json:"status"`
	Attempts         int            `json:"attempts"`
	LastError        string         `json:"last_error,omitempty"`
	UpdatedAt        int64          `json:"updated_at"`
	EvidenceSequence int            `json:"evidence_sequence"`
}

func (b *Bridge) commandReceiptBody(receipt *CommandReceipt) map[string]any {
	attempt := max(1, receipt.ExecutionAttempt)
	return map[string]any{"commandId": receipt.CommandID, "payloadHash": receipt.PayloadHash, "attempt": attempt, "evidenceSequence": receipt.EvidenceSequence,
		"status": receipt.Status, "outcome": receipt.Outcome, "result": receipt.Result}
}

func (b *Bridge) queueCommandReceiptLocked(receipt *CommandReceipt) error {
	attempt := max(1, receipt.ExecutionAttempt)
	key := receipt.CommandID + ":" + fmt.Sprint(attempt) + ":" + fmt.Sprint(receipt.EvidenceSequence)
	return b.enqueueLocked("agency-center/command-receipts", key, b.commandReceiptBody(receipt), "", "")
}

func (b *Bridge) recoverCommandReceiptDeliveries() error {
	for _, receipt := range b.state.CommandReceipts {
		if receipt.Result == nil {
			receipt.Result = map[string]any{}
		}
		if receipt.EvidenceSequence < 1 {
			receipt.EvidenceSequence = 1
		}
		if err := b.queueCommandReceiptLocked(receipt); err != nil {
			return err
		}
	}
	return nil
}

type commandEnvelope struct {
	ID          string            `json:"id"`
	Attempt     int               `json:"attempt,omitempty"`
	TenantID    string            `json:"tenantId"`
	GatewayID   string            `json:"gatewayId"`
	Payload     common.RawMessage `json:"payload"`
	PayloadHash string            `json:"payloadHash"`
	ExpiresAt   string            `json:"expiresAt"`
	LeaseToken  string            `json:"leaseToken"`
}

type commandClaimResponse struct {
	Command       *commandEnvelope `json:"command"`
	Signature     string           `json:"signature"`
	Algorithm     string           `json:"signatureAlgorithm"`
	SignedPayload string           `json:"signedPayload"`
}

func (b *Bridge) ProcessAgencyCommand(ctx context.Context, now time.Time) (bool, error) {
	if b.commandExecutor == nil {
		return false, nil
	}
	b.commandMu.Lock()
	defer b.commandMu.Unlock()

	if receipt := b.pendingCommandReceipt(); receipt != nil {
		return true, b.ackAgencyCommand(ctx, receipt, now)
	}
	var claim commandClaimResponse
	if err := b.request(ctx, http.MethodPost, "agency-center/commands/claim", "", map[string]any{}, &claim); err != nil {
		return false, err
	}
	if claim.Command == nil {
		return false, nil
	}
	envelope, command, err := b.verifyAgencyCommand(claim, now)
	if err != nil {
		return true, err
	}

	b.mu.Lock()
	receipt := b.state.CommandReceipts[command.ID]
	if receipt != nil && receipt.PayloadHash != command.PayloadHash {
		b.mu.Unlock()
		return true, errors.New("Ops agency command identity conflict")
	}
	if receipt != nil {
		if envelope.Attempt < receipt.ExecutionAttempt {
			b.mu.Unlock()
			return true, errors.New("stale Ops agency command attempt")
		}
		if receipt.Status == "executing" || receipt.Outcome == "" {
			receipt.Status = "awaiting_reconcile"
			receipt.EvidenceSequence++
			receipt.LastError = "execution interrupted; verify business state before reconciliation"
			err = b.queueCommandReceiptLocked(receipt)
			b.mu.Unlock()
			if err != nil {
				return true, err
			}
			return true, errors.New("Ops agency command requires business reconciliation")
		}
		// Only explicitly re-leased, idempotent business operations may retry a failure.
		retryable := command.Kind == "suspend_customer" || command.Kind == "resume_customer" || command.Kind == "sync_agency_policy" || command.Kind == "provision_customer" || command.Kind == "transition_writer_mode"
		if receipt.Outcome == "failed" && envelope.Attempt > receipt.ExecutionAttempt && retryable {
			receipt = nil
		}
	}
	if receipt != nil {
		receipt.LeaseToken = envelope.LeaseToken
		receipt.Status = "ack_pending"
		receipt.EvidenceSequence++
		receipt.LastError = ""
		receipt.UpdatedAt = now.Unix()
		err = b.queueCommandReceiptLocked(receipt)
		b.mu.Unlock()
		if err != nil {
			return true, err
		}
		return true, b.ackAgencyCommand(ctx, receipt, now)
	}
	// Persist intent before side effects. An interrupted execution must never be
	// retried blindly, even when the remote lease is manually reconciled.
	receipt = &CommandReceipt{CommandID: command.ID, PayloadHash: command.PayloadHash, ExecutionAttempt: envelope.Attempt,
		LeaseToken: envelope.LeaseToken, Status: "executing", Result: map[string]any{}, EvidenceSequence: 1, UpdatedAt: now.Unix()}
	b.state.CommandReceipts[command.ID] = receipt
	err = b.queueCommandReceiptLocked(receipt)
	b.mu.Unlock()
	if err != nil {
		return true, err
	}

	result, executeErr := b.commandExecutor.ExecuteOpsAgencyCommand(ctx, command)
	outcome := "succeeded"
	if executeErr != nil {
		outcome = "failed"
		result = map[string]any{"error": "gateway_command_failed"}
	}
	if result == nil {
		result = map[string]any{}
	}
	receipt = &CommandReceipt{CommandID: command.ID, PayloadHash: command.PayloadHash, ExecutionAttempt: envelope.Attempt,
		LeaseToken: envelope.LeaseToken, Outcome: outcome, Result: result, Status: "ack_pending", EvidenceSequence: 2, UpdatedAt: now.Unix()}
	b.mu.Lock()
	b.state.CommandReceipts[command.ID] = receipt
	err = b.queueCommandReceiptLocked(receipt)
	b.mu.Unlock()
	if err != nil {
		return true, err
	}
	return true, b.ackAgencyCommand(ctx, receipt, now)
}

func (b *Bridge) pendingCommandReceipt() *CommandReceipt {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, receipt := range b.state.CommandReceipts {
		if receipt.Status == "ack_pending" {
			copy := *receipt
			return &copy
		}
	}
	return nil
}

func (b *Bridge) ackAgencyCommand(ctx context.Context, receipt *CommandReceipt, now time.Time) error {
	body := map[string]any{"commandId": receipt.CommandID, "leaseToken": receipt.LeaseToken,
		"state": receipt.Outcome, "result": receipt.Result}
	err := b.request(ctx, http.MethodPost, "agency-center/commands/ack", "", body, nil)
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.state.CommandReceipts[receipt.CommandID]
	if current == nil || current.PayloadHash != receipt.PayloadHash {
		return errors.New("Ops agency command receipt disappeared")
	}
	current.Attempts++
	current.UpdatedAt = now.Unix()
	if err == nil {
		current.Status = "acknowledged"
		current.LeaseToken = ""
		current.LastError = ""
	} else {
		current.LastError = err.Error()
		var remote *HTTPError
		if errors.As(err, &remote) && remote.Status == http.StatusConflict {
			current.Status = "awaiting_reconcile"
		}
	}
	current.EvidenceSequence++
	if saveErr := b.queueCommandReceiptLocked(current); saveErr != nil {
		return saveErr
	}
	return err
}

func (b *Bridge) verifyAgencyCommand(claim commandClaimResponse, now time.Time) (commandEnvelope, OpsAgencyCommand, error) {
	if claim.Algorithm != "hmac-sha256" || claim.SignedPayload == "" || len(claim.SignedPayload) > 4<<20 || len(b.cfg.CommandSigningKey) < 32 {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("invalid Ops agency command signature contract")
	}
	expected := hmac.New(sha256.New, []byte(b.cfg.CommandSigningKey))
	expected.Write([]byte(claim.SignedPayload))
	if !hmac.Equal([]byte(fmt.Sprintf("%x", expected.Sum(nil))), []byte(claim.Signature)) {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("invalid Ops agency command signature")
	}
	var envelope commandEnvelope
	if common.Unmarshal([]byte(claim.SignedPayload), &envelope) != nil || envelope.ID == "" || envelope.LeaseToken == "" || envelope.PayloadHash == "" {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("invalid Ops agency command envelope")
	}
	if claim.Command == nil || claim.Command.ID != envelope.ID || claim.Command.TenantID != envelope.TenantID ||
		claim.Command.Attempt != envelope.Attempt || envelope.Attempt < 0 || claim.Command.GatewayID != envelope.GatewayID || claim.Command.PayloadHash != envelope.PayloadHash ||
		claim.Command.ExpiresAt != envelope.ExpiresAt || claim.Command.LeaseToken != envelope.LeaseToken {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("Ops agency command envelope mismatch")
	}
	var signedPayload, responsePayload any
	if common.Unmarshal(envelope.Payload, &signedPayload) != nil || common.Unmarshal(claim.Command.Payload, &responsePayload) != nil || !reflect.DeepEqual(signedPayload, responsePayload) {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("Ops agency command payload mismatch")
	}
	if envelope.TenantID == "" || envelope.GatewayID != b.cfg.GatewayID {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("Ops agency command gateway mismatch")
	}
	expires, err := time.Parse(time.RFC3339, envelope.ExpiresAt)
	if err != nil || !expires.After(now) || expires.After(now.Add(10*time.Minute)) {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("expired or invalid Ops agency command lease")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(envelope.Payload))
	if !hmac.Equal([]byte(digest), []byte(envelope.PayloadHash)) {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("Ops agency command payload hash mismatch")
	}
	var payload struct {
		Kind     string         `json:"kind"`
		AgencyID string         `json:"agencyId"`
		Payload  map[string]any `json:"payload"`
	}
	if common.Unmarshal(envelope.Payload, &payload) != nil || payload.Kind == "" || payload.AgencyID == "" || payload.Payload == nil {
		return commandEnvelope{}, OpsAgencyCommand{}, errors.New("invalid Ops agency command payload")
	}
	return envelope, OpsAgencyCommand{ID: envelope.ID, Kind: payload.Kind, AgencyID: payload.AgencyID,
		Payload: payload.Payload, PayloadHash: envelope.PayloadHash}, nil
}
