package model

import (
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CNIntegrityEligibilityCustomer struct {
	CustomerKey              string   `json:"customerKey"`
	Decision                 string   `json:"decision"`
	OpsFIFOHistoryCount      string   `json:"opsFifoHistoryCount"`
	IntersectingAuthorityIDs []string `json:"intersectingAuthorityIds"`
}

type CNIntegrityEligibilityBody struct {
	Schema         string                           `json:"schema"`
	SnapshotID     string                           `json:"snapshotId"`
	SourceSystem   string                           `json:"sourceSystem"`
	GatewayID      string                           `json:"gatewayId"`
	StreamEpoch    string                           `json:"streamEpoch"`
	IntegrityEpoch string                           `json:"integrityEpoch"`
	ApprovedAt     string                           `json:"approvedAt"`
	ReviewSchema   string                           `json:"reviewSchema"`
	ReviewHash     string                           `json:"reviewHash"`
	Customers      []CNIntegrityEligibilityCustomer `json:"customers"`
}

type CNIntegrityEligibilityExport struct {
	OpsInventoryID   string                     `json:"opsInventoryId"`
	OpsInventoryHash string                     `json:"opsInventoryHash"`
	Body             CNIntegrityEligibilityBody `json:"body"`
}

// This is an immutable copy of an Ops-approved export, never a local review of
// Ops state. Ops must resolve its ID/hash in its own approved store at import.
type CNIntegrityEligibilityApproval struct {
	SnapshotID       string `gorm:"size:64;primaryKey"`
	InventoryHash    string `gorm:"size:71;not null"`
	EpochID          string `gorm:"size:64;not null;index"`
	Payload          string `gorm:"type:text;not null"`
	ImportEvidenceID string `gorm:"size:191;not null"`
}

func cnIntegrityExactObject(raw []byte, keys ...string) bool {
	var object map[string]common.RawMessage
	if common.Unmarshal(raw, &object) != nil || len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func DecodeCNIntegrityEligibilitySnapshot(raw []byte) (*CNIntegrityEligibilityExport, error) {
	if _, err := CNIntegrityCanonicalJSON(raw); err != nil {
		return nil, err
	}
	var envelope map[string]common.RawMessage
	if common.Unmarshal(raw, &envelope) != nil || !cnIntegrityExactObject(raw, "opsInventoryId", "opsInventoryHash", "body") || !cnIntegrityExactObject(envelope["body"], "schema", "snapshotId", "sourceSystem", "gatewayId", "streamEpoch", "integrityEpoch", "approvedAt", "reviewSchema", "reviewHash", "customers") {
		return nil, errors.New("invalid Ops eligibility snapshot schema")
	}
	var snapshot CNIntegrityEligibilityExport
	if err := common.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	body := snapshot.Body
	approvedAt, err := time.Parse(CNTimeLayout, body.ApprovedAt)
	if err != nil || approvedAt.Format(CNTimeLayout) != body.ApprovedAt || body.Schema != "ops-native-integrity-eligibility-v2" || body.SourceSystem != CNFinanceSource || body.GatewayID == "" || len(body.GatewayID) > 191 || uuid.Validate(body.SnapshotID) != nil || uuid.Validate(body.StreamEpoch) != nil || uuid.Validate(body.IntegrityEpoch) != nil || snapshot.OpsInventoryID != body.SnapshotID || body.ReviewSchema != "ops-native-integrity-review-v2" || !cnIntegrityHashValid(body.ReviewHash) || body.Customers == nil {
		return nil, errors.New("invalid approved Ops eligibility snapshot identity")
	}
	hash, err := CNIntegrityBodyHash(envelope["body"])
	if err != nil || hash != snapshot.OpsInventoryHash {
		return nil, errors.New("Ops eligibility snapshot body hash mismatch")
	}
	var encodedBody map[string]common.RawMessage
	if err := common.Unmarshal(envelope["body"], &encodedBody); err != nil {
		return nil, err
	}
	var customers []common.RawMessage
	if err := common.Unmarshal(encodedBody["customers"], &customers); err != nil {
		return nil, err
	}
	previous := int64(0)
	for i, customer := range body.Customers {
		if !cnIntegrityExactObject(customers[i], "customerKey", "decision", "opsFifoHistoryCount", "intersectingAuthorityIds") {
			return nil, errors.New("invalid Ops eligibility customer schema")
		}
		id, idErr := strconv.ParseInt(customer.CustomerKey, 10, 64)
		count, countErr := strconv.ParseInt(customer.OpsFIFOHistoryCount, 10, 64)
		if idErr != nil || id <= previous || strconv.FormatInt(id, 10) != customer.CustomerKey || countErr != nil || count < 0 || strconv.FormatInt(count, 10) != customer.OpsFIFOHistoryCount || customer.IntersectingAuthorityIDs == nil || !slices.IsSorted(customer.IntersectingAuthorityIDs) || (customer.Decision != "eligible" && customer.Decision != "excluded") || (customer.Decision == "eligible" && (count != 0 || len(customer.IntersectingAuthorityIDs) != 0)) {
			return nil, errors.New("invalid Ops eligibility customer decision")
		}
		for j, authority := range customer.IntersectingAuthorityIDs {
			if authority == "" || (j > 0 && customer.IntersectingAuthorityIDs[j-1] == authority) {
				return nil, errors.New("invalid Ops eligibility authority inventory")
			}
		}
		previous = id
	}
	return &snapshot, nil
}

// This local operator API records who imported the exact Ops export bytes.
// A matching hash is not issuer authentication; Ops checks its own approved
// snapshot store before granting authority. Do not expose this as a public API.
func ImportCNIntegrityEligibilitySnapshot(db *gorm.DB, raw []byte, operatorEvidenceID string) error {
	if operatorEvidenceID == "" || len(operatorEvidenceID) > 191 {
		return errors.New("exact Ops snapshot import requires local operator evidence")
	}
	snapshot, err := DecodeCNIntegrityEligibilitySnapshot(raw)
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var epoch CNIntegrityEpoch
		if err := lockForUpdate(tx).Where("epoch_id = ?", snapshot.Body.IntegrityEpoch).First(&epoch).Error; err != nil {
			return err
		}
		if epoch.GatewayID != snapshot.Body.GatewayID || epoch.StreamEpoch != snapshot.Body.StreamEpoch || (epoch.State != "prepared" && epoch.State != "active") {
			return ErrCNConflict
		}
		var existing CNIntegrityEligibilityApproval
		err := lockForUpdate(tx).Where("snapshot_id = ?", snapshot.OpsInventoryID).First(&existing).Error
		if err == nil {
			if existing.InventoryHash != snapshot.OpsInventoryHash || existing.EpochID != epoch.EpochID {
				return ErrCNConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&CNIntegrityEligibilityApproval{SnapshotID: snapshot.OpsInventoryID, InventoryHash: snapshot.OpsInventoryHash, EpochID: epoch.EpochID, Payload: string(raw), ImportEvidenceID: operatorEvidenceID}).Error
	})
}

func cnValidateIntegrityEligibilityTx(tx *gorm.DB, epoch CNIntegrityEpoch, user int, inventoryID string) (*CNIntegrityEligibilityApproval, error) {
	if inventoryID == "" {
		return nil, errors.New("approved Ops eligibility snapshot required: import the Ops snapshot and select its ID")
	}
	var approval CNIntegrityEligibilityApproval
	if err := lockForUpdate(tx).Where("snapshot_id = ?", inventoryID).First(&approval).Error; err != nil {
		return nil, err
	}
	snapshot, err := DecodeCNIntegrityEligibilitySnapshot([]byte(approval.Payload))
	if err != nil {
		return nil, err
	}
	if approval.ImportEvidenceID == "" || approval.SnapshotID != snapshot.OpsInventoryID || approval.InventoryHash != snapshot.OpsInventoryHash || approval.EpochID != epoch.EpochID || snapshot.Body.IntegrityEpoch != epoch.EpochID || snapshot.Body.StreamEpoch != epoch.StreamEpoch || snapshot.Body.GatewayID != epoch.GatewayID {
		return nil, ErrCNConflict
	}
	for _, customer := range snapshot.Body.Customers {
		if customer.CustomerKey == strconv.Itoa(user) && customer.Decision == "eligible" {
			return &approval, nil
		}
	}
	return nil, errors.New("customer excluded from approved Ops eligibility snapshot")
}
