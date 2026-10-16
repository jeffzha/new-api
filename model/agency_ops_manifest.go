package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	AgencyWriterSourcePrimary = "source_primary"
	AgencyWriterShadow        = "shadow"
	AgencyWriterDraining      = "draining"
	AgencyWriterOpsSingle     = "ops_single_writer"
	AgencyWriterRolledBack    = "rolled_back"
)

type AgencyOpsDeliveryHealth struct {
	Backlog     int `json:"deliveryBacklog"`
	NeedsRepair int `json:"needsRepair"`
}

type AgencyOpsManifest struct {
	ManifestVersion int64            `json:"manifestVersion"`
	WriterMode      string           `json:"writerMode"`
	FenceVersion    int64            `json:"fenceVersion"`
	EventCount      int64            `json:"eventCount"`
	EventSetHash    string           `json:"eventSetHash"`
	MoneySeqByUser  map[string]int64 `json:"moneySeqByUser"`
	DeliveryBacklog int              `json:"deliveryBacklog"`
	PoisonCount     int              `json:"poisonCount"`
}

type agencyOpsEventIdentity struct {
	EventID     string
	PayloadHash string
}

func agencyOpsCoverage(tx *gorm.DB) ([]agencyOpsEventIdentity, map[string]int64, int, error) {
	var events []agencyOpsEventIdentity
	if err := tx.Model(&AgencyBillingOutbox{}).Where("event_kind IN ?", agencyOpsAuthorityKinds).
		Order("event_id").Find(&events).Error; err != nil {
		return nil, nil, 0, err
	}
	var accounts []AgencyFundingAccount
	if err := tx.Select("user_id", "money_seq").Order("user_id").Find(&accounts).Error; err != nil {
		return nil, nil, 0, err
	}
	sequences := make(map[string]int64, len(accounts))
	poison := 0
	for _, account := range accounts {
		sequences[fmt.Sprint(account.UserID)] = account.MoneySeq
		var covered int64
		if err := tx.Model(&AgencyBillingOutbox{}).Where("user_id = ? AND money_seq > 0 AND money_seq <= ? AND event_kind IN ?",
			account.UserID, account.MoneySeq, agencyOpsAuthorityKinds).Distinct("money_seq").Count(&covered).Error; err != nil {
			return nil, nil, 0, err
		}
		if covered != account.MoneySeq {
			poison++
		}
	}
	return events, sequences, poison, nil
}

func agencyOpsEventSetHash(events []agencyOpsEventIdentity) (string, error) {
	pairs := make([][2]string, 0, len(events))
	for _, event := range events {
		pairs = append(pairs, [2]string{event.EventID, event.PayloadHash})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] == pairs[j][0] {
			return pairs[i][1] < pairs[j][1]
		}
		return pairs[i][0] < pairs[j][0]
	})
	raw, err := common.Marshal(pairs)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

// CreateAgencyOpsSourceManifest creates one immutable, monotonically numbered
// source snapshot. The bridge cursor makes retries return the same row.
func CreateAgencyOpsSourceManifest(ctx context.Context, db *gorm.DB, after int64, health AgencyOpsDeliveryHealth) (*AgencyOpsSourceManifest, error) {
	var result *AgencyOpsSourceManifest
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing AgencyOpsSourceManifest
		if err := tx.Where("version > ?", after).Order("version").First(&existing).Error; err == nil {
			result = &existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var state AgencyOpsWriterState
		if err := AgencyLockForUpdate(tx).First(&state, 1).Error; err != nil {
			return err
		}
		events, sequences, poison, err := agencyOpsCoverage(tx)
		if err != nil {
			return err
		}
		eventHash, err := agencyOpsEventSetHash(events)
		if err != nil {
			return err
		}
		version := state.ManifestVersion + 1
		manifest := AgencyOpsManifest{ManifestVersion: version, WriterMode: state.Mode, FenceVersion: state.FenceVersion,
			EventCount: int64(len(events)), EventSetHash: eventHash, MoneySeqByUser: sequences,
			DeliveryBacklog: health.Backlog, PoisonCount: poison + health.NeedsRepair}
		if state.ManifestVersion > 0 {
			var previous AgencyOpsSourceManifest
			if err := tx.Where("version = ?", state.ManifestVersion).First(&previous).Error; err != nil {
				return err
			}
			var prior AgencyOpsManifest
			if err := common.Unmarshal([]byte(previous.Payload), &prior); err != nil {
				return err
			}
			if prior.WriterMode == manifest.WriterMode && prior.FenceVersion == manifest.FenceVersion &&
				prior.EventCount == manifest.EventCount && prior.EventSetHash == manifest.EventSetHash &&
				prior.DeliveryBacklog == manifest.DeliveryBacklog && prior.PoisonCount == manifest.PoisonCount &&
				reflect.DeepEqual(prior.MoneySeqByUser, manifest.MoneySeqByUser) {
				return nil
			}
		}
		// TokenOps hashes canonical JSON with sorted object keys. Marshaling a map
		// preserves that contract while still using the repository JSON helper.
		payload, err := common.Marshal(map[string]any{
			"deliveryBacklog": manifest.DeliveryBacklog,
			"eventCount":      manifest.EventCount,
			"eventSetHash":    manifest.EventSetHash,
			"fenceVersion":    manifest.FenceVersion,
			"manifestVersion": manifest.ManifestVersion,
			"moneySeqByUser":  manifest.MoneySeqByUser,
			"poisonCount":     manifest.PoisonCount,
			"writerMode":      manifest.WriterMode,
		})
		if err != nil {
			return err
		}
		row := AgencyOpsSourceManifest{Version: version, FenceVersion: state.FenceVersion, Payload: string(payload),
			PayloadHash: fmt.Sprintf("%x", sha256.Sum256(payload)), CreatedAtMS: time.Now().UnixMilli()}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		updated := tx.Model(&AgencyOpsWriterState{}).Where("id = ? AND manifest_version = ?", 1, state.ManifestVersion).
			Update("manifest_version", version)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("agency writer manifest version conflict")
		}
		result = &row
		return nil
	})
	return result, err
}

func validAgencyWriterTransition(from, to string) bool {
	allowed := map[string]map[string]bool{
		AgencyWriterSourcePrimary: {AgencyWriterShadow: true, AgencyWriterRolledBack: true},
		AgencyWriterShadow:        {AgencyWriterDraining: true, AgencyWriterRolledBack: true},
		AgencyWriterDraining:      {AgencyWriterOpsSingle: true, AgencyWriterRolledBack: true},
		AgencyWriterOpsSingle:     {AgencyWriterRolledBack: true},
		AgencyWriterRolledBack:    {AgencyWriterSourcePrimary: true},
	}
	return allowed[from][to]
}

func TransitionAgencyOpsWriter(ctx context.Context, db *gorm.DB, commandID, target string, expectedFence int64, reason string, evidence map[string]any, health AgencyOpsDeliveryHealth) (AgencyOpsWriterState, error) {
	var state AgencyOpsWriterState
	if strings.TrimSpace(commandID) == "" || strings.TrimSpace(reason) == "" || len(evidence) == 0 {
		return state, errors.New("writer transition reason and evidence are required")
	}
	evidenceJSON, err := common.Marshal(evidence)
	if err != nil {
		return state, err
	}
	payload, err := common.Marshal(map[string]any{
		"evidence": evidence, "expectedFenceVersion": expectedFence, "reason": reason, "targetMode": target,
	})
	if err != nil {
		return state, err
	}
	payloadHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := AgencyLockForUpdate(tx).First(&state, 1).Error; err != nil {
			return err
		}
		var receipt AgencyOpsWriterTransition
		if err := tx.Where("command_id = ?", commandID).First(&receipt).Error; err == nil {
			if receipt.PayloadHash != payloadHash {
				return errors.New("writer transition command identity conflict")
			}
			state = AgencyOpsWriterState{ID: 1, Mode: receipt.TargetMode, FenceVersion: receipt.ResultingFenceVersion,
				ManifestVersion: receipt.ManifestVersion, LastCommandID: receipt.CommandID, Reason: receipt.Reason,
				EvidenceJSON: receipt.EvidenceJSON, UpdatedAtMS: receipt.CreatedAtMS}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if state.FenceVersion != expectedFence {
			return errors.New("writer fence version conflict")
		}
		if !validAgencyWriterTransition(state.Mode, target) {
			return errors.New("invalid writer mode transition")
		}
		if target == AgencyWriterOpsSingle {
			if health.Backlog != 0 || health.NeedsRepair != 0 {
				return errors.New("Ops delivery queue is not drained")
			}
			if count := agencyOpsUnpublished(tx); count != nil {
				var pending int64
				if err := count.Count(&pending).Error; err != nil {
					return err
				}
				if pending != 0 {
					return errors.New("Ops authority publication queue is not drained")
				}
			}
			_, _, poison, err := agencyOpsCoverage(tx)
			if err != nil {
				return err
			}
			if poison != 0 {
				return errors.New("wallet authority coverage is incomplete")
			}
			var active int64
			if err := tx.Model(&AgencyProvisioningJob{}).Where("status IN ?", []string{"queued", "processing", "blocked"}).Count(&active).Error; err != nil {
				return err
			}
			if active != 0 {
				return errors.New("provisioning jobs are not drained")
			}
		}
		next := state.FenceVersion + 1
		now := time.Now().UnixMilli()
		updated := tx.Model(&AgencyOpsWriterState{}).Where("id = ? AND fence_version = ?", 1, expectedFence).Updates(map[string]any{
			"mode": target, "fence_version": next, "last_command_id": commandID, "reason": reason,
			"evidence_json": string(evidenceJSON), "updated_at_ms": now,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("writer fence version conflict")
		}
		if err := tx.First(&state, 1).Error; err != nil {
			return err
		}
		resultJSON, err := common.Marshal(map[string]any{"writerMode": state.Mode, "fenceVersion": state.FenceVersion, "manifestVersion": state.ManifestVersion})
		if err != nil {
			return err
		}
		return tx.Create(&AgencyOpsWriterTransition{CommandID: commandID, PayloadHash: payloadHash, TargetMode: target,
			ExpectedFenceVersion: expectedFence, ResultingFenceVersion: state.FenceVersion, ManifestVersion: state.ManifestVersion,
			Reason: reason, EvidenceJSON: string(evidenceJSON), ResultJSON: string(resultJSON), CreatedAtMS: now}).Error
	})
	return state, err
}
