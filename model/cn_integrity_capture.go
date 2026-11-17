package model

import (
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CNIntegrityGate struct {
	ID      int    `gorm:"primaryKey;autoIncrement:false"`
	EpochID string `gorm:"size:64;not null"`
}

type CNIntegrityWriter struct {
	EpochID                  string `gorm:"size:64;primaryKey"`
	WriterID                 string `gorm:"size:191;primaryKey"`
	BuildID                  string `gorm:"size:191;not null"`
	LeaseUntil               string `gorm:"size:40;not null"`
	SetHash                  string `gorm:"size:71;not null"`
	ProtocolVersion          string `gorm:"size:64;not null"`
	CapabilityPaths          string `gorm:"type:text;not null"`
	LeaseGeneration          int64  `gorm:"primaryKey;autoIncrement:false"`
	DrainAcknowledgedThrough int64
	MembershipBodyHash       string `gorm:"size:71;not null"`
}

type CNIntegrityFence struct {
	EpochID      string `gorm:"size:64;primaryKey"`
	WriterSet    string `gorm:"type:text;not null"`
	WriterHash   string `gorm:"size:71;not null"`
	FencePayload string `gorm:"type:text;not null"`
	FenceHash    string `gorm:"size:71;not null"`
	Generation   int64  `gorm:"primaryKey;autoIncrement:false"`
	Attestation  string `gorm:"type:text;not null"`
	AttestedAt   string `gorm:"size:40;not null"`
}

type CNIntegritySliceDelta struct {
	SliceID          string `gorm:"size:64;primaryKey"`
	EpochID          string `gorm:"size:64;not null;index"`
	ReceiptID        string `gorm:"size:64;not null;index"`
	SourceSliceID    int64  `gorm:"not null;index"`
	MutationSequence int64
	PreviousSliceID  string `gorm:"size:64"`
	AllocationID     string `gorm:"size:64;not null"`
	LotID            string `gorm:"size:64;not null"`
	Currency         string `gorm:"size:3;not null"`
	QuotaDelta       int64
	ValueDelta       string `gorm:"size:128;not null"`
	LineageID        string `gorm:"size:64;not null"`
}

// This is an explicit operator declaration, never inferred from environment
// flags or a heartbeat. The importing Ops side independently checks inventory.
type CNIntegrityDrainAttestation struct {
	InventoryID              string `json:"inventoryId"`
	InventoryHash            string `json:"inventoryHash"`
	OperatorEvidenceID       string `json:"operatorEvidenceId"`
	OldWritersDrained        bool   `json:"oldWritersDrained"`
	RedisDisabled            bool   `json:"redisDisabled"`
	BatchDisabled            bool   `json:"batchDisabled"`
	ExternalSQLExcluded      bool   `json:"externalSqlExcluded"`
	UnguardedHandlesExcluded bool   `json:"unguardedHandlesExcluded"`
}

type CNIntegrityWriterDeclaration struct {
	WriterID        string   `json:"writerId"`
	BuildID         string   `json:"buildId"`
	LeaseUntil      string   `json:"leaseUntil"`
	ProtocolVersion string   `json:"protocolVersion"`
	CapabilityPaths []string `json:"capabilityPaths"`
}

const cnIntegrityWriterProtocol = "native-mutation-receipt-v2"

func PrepareCNIntegrityCapture(db *gorm.DB, operatorEvidence string) (*CNIntegrityEpoch, error) {
	var epoch *CNIntegrityEpoch
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		epoch, err = cnPrepareIntegrityEpochTx(tx, operatorEvidence)
		return err
	})
	return epoch, err
}

func AdmitCNIntegrityUser(db *gorm.DB, epochID string, user int, expectedGeneration int64, inventoryID string) (*CNIntegrityAdmission, error) {
	var admission *CNIntegrityAdmission
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		admission, err = cnAdmitIntegrityUserTx(tx, epochID, user, expectedGeneration, inventoryID)
		return err
	})
	return admission, err
}

func StopCNIntegrityCapture(db *gorm.DB, epochID string, expectedGeneration int64, state, operatorEvidence string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		return cnStopIntegrityEpochTx(tx, epochID, expectedGeneration, state, operatorEvidence)
	})
}

func cnIntegrityHashValid(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

func cnIntegrityWriterCapabilitiesValid(protocol string, paths []string) bool {
	return protocol == cnIntegrityWriterProtocol && slices.Equal(paths, []string{"quota_mutation", "source_switch", "task_reserve", "task_revision", "topup_credit", "wallet_final", "wallet_release", "wallet_reserve"})
}

func cnIntegrityWriterMatchesFence(writer CNIntegrityWriter, fence CNIntegrityFence) bool {
	var declarations []cnIntegrityWriterMembership
	var capabilities []string
	if writer.EpochID != fence.EpochID || writer.LeaseGeneration != fence.Generation || writer.SetHash != fence.WriterHash || common.Unmarshal([]byte(fence.WriterSet), &declarations) != nil || common.Unmarshal([]byte(writer.CapabilityPaths), &capabilities) != nil || !cnIntegrityWriterCapabilitiesValid(writer.ProtocolVersion, capabilities) {
		return false
	}
	for _, declaration := range declarations {
		if declaration.WriterID == writer.WriterID {
			member, err := cnIntegrityMembership(writer)
			return err == nil && declaration.BuildID == writer.BuildID && declaration.ProtocolVersion == writer.ProtocolVersion && declaration.LeaseExpiresAt == writer.LeaseUntil && declaration.LeaseGeneration == strconv.FormatInt(writer.LeaseGeneration, 10) && declaration.DrainAcknowledgedThrough == strconv.FormatInt(writer.DrainAcknowledgedThrough, 10) && declaration.MembershipBodyHash == member.MembershipBodyHash && member.MembershipBodyHash == writer.MembershipBodyHash && slices.Equal(declaration.CapabilityPaths, capabilities)
		}
	}
	return false
}

// Start only activates native capture. It does not create an Ops authority or
// claim source completeness. Cohort admissions must already exist explicitly.
func StartCNIntegrityCapture(db *gorm.DB, epochID string, expectedGeneration int64, writers []CNIntegrityWriterDeclaration, attestation CNIntegrityDrainAttestation) error {
	if attestation.InventoryID == "" || !cnIntegrityHashValid(attestation.InventoryHash) || attestation.OperatorEvidenceID == "" || !attestation.OldWritersDrained || !attestation.RedisDisabled || !attestation.BatchDisabled || !attestation.ExternalSQLExcluded || !attestation.UnguardedHandlesExcluded || common.RedisEnabled || common.BatchUpdateEnabled || len(writers) == 0 {
		return errors.New("integrity capture requires an explicit complete writer drain attestation")
	}
	writers = slices.Clone(writers)
	slices.SortFunc(writers, func(a, b CNIntegrityWriterDeclaration) int {
		if a.WriterID < b.WriterID {
			return -1
		}
		if a.WriterID > b.WriterID {
			return 1
		}
		return 0
	})
	for i, writer := range writers {
		expires, err := time.Parse(CNTimeLayout, writer.LeaseUntil)
		if err != nil || !expires.After(time.Now().UTC()) || writer.WriterID == "" || writer.BuildID == "" || len(writer.WriterID) > 191 || len(writer.BuildID) > 191 || !cnIntegrityWriterCapabilitiesValid(writer.ProtocolVersion, writer.CapabilityPaths) || (i > 0 && writers[i-1].WriterID == writer.WriterID) {
			return errors.New("invalid integrity writer inventory")
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var gate CNIntegrityGate
		if err := lockForUpdate(tx).First(&gate, 1).Error; err != nil {
			return err
		}
		if gate.EpochID != "" {
			return ErrCNConflict
		}
		var epoch CNIntegrityEpoch
		if err := lockForUpdate(tx).Where("epoch_id = ?", epochID).First(&epoch).Error; err != nil {
			return err
		}
		if epoch.State != "prepared" || epoch.Generation != expectedGeneration || epoch.MembershipSequence < 1 {
			return ErrCNConflict
		}
		var stream CNFinanceStream
		if err := lockForUpdate(tx).First(&stream, 1).Error; err != nil {
			return err
		}
		if stream.Epoch != epoch.StreamEpoch || stream.GatewayID != epoch.GatewayID {
			return ErrCNConflict
		}
		var admissions []CNIntegrityAdmission
		if err := lockForUpdate(tx).Where("epoch_id = ?", epoch.EpochID).Find(&admissions).Error; err != nil {
			return err
		}
		for _, admission := range admissions {
			approval, err := cnValidateIntegrityEligibilityTx(tx, epoch, admission.UserID, admission.OpsInventoryID)
			if err != nil {
				return err
			}
			if approval.InventoryHash != admission.OpsInventoryHash {
				return ErrCNConflict
			}
		}
		var memberships []CNIntegrityWriter
		for _, writer := range writers {
			capabilities, err := common.Marshal(writer.CapabilityPaths)
			if err != nil {
				return err
			}
			memberships = append(memberships, CNIntegrityWriter{EpochID: epochID, WriterID: writer.WriterID, BuildID: writer.BuildID, LeaseUntil: writer.LeaseUntil, ProtocolVersion: writer.ProtocolVersion, CapabilityPaths: string(capabilities), DrainAcknowledgedThrough: epoch.MutationSequence})
		}
		fence, err := cnPersistIntegrityFenceTx(tx, epochID, epoch.Generation+1, memberships, attestation, time.Now().UTC().Format(CNTimeLayout))
		if err != nil {
			return err
		}
		control := CNIntegrityControlReceipt{EpochID: epochID, Generation: epoch.Generation + 1, PreviousGeneration: epoch.Generation, MutationThrough: epoch.MutationSequence, State: "active", OperatorEvidenceID: attestation.OperatorEvidenceID, WriterSetHash: fence.WriterHash, FenceHash: fence.FenceHash, ChangedAt: time.Now().UTC().Format(CNTimeLayout)}
		if err := tx.Create(&control).Error; err != nil {
			return err
		}
		if err := tx.Model(&epoch).Updates(map[string]any{"state": "active", "generation": control.Generation, "operator_evidence_id": attestation.OperatorEvidenceID}).Error; err != nil {
			return err
		}
		return tx.Model(&gate).Update("epoch_id", epochID).Error
	})
}

type cnIntegrityCaptureScope struct {
	Epoch        CNIntegrityEpoch
	Admission    CNIntegrityAdmission
	BeforeQuota  int64
	BeforeMode   string
	BeforeRows   map[string]cnIntegrityNativeRow
	BeforeSlices map[int64]CNWalletValueSlice
	BeforeValues []cnIntegrityValueEntity
	OutboxAfter  int64
	Writer       string
	Build        string
	Fence        CNIntegrityFence
	Broken       bool
}

const cnIntegrityCaptureScopeKey = "cn:integrity-capture-scope"

func cnBeginIntegrityCaptureTx(tx *gorm.DB, user int) error {
	if _, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey); exists {
		return nil
	}
	if !tx.Migrator().HasTable(&CNIntegrityGate{}) {
		return nil
	}
	var native User
	if err := lockForUpdate(tx).Select("id", "quota", "billing_mode").First(&native, user).Error; err != nil {
		return err
	}
	var gate CNIntegrityGate
	gateRead := tx
	if tx.Dialector.Name() != "sqlite" {
		gateRead = tx.Clauses(clause.Locking{Strength: "SHARE"})
	}
	if err := gateRead.First(&gate, 1).Error; err != nil {
		return err
	}
	if gate.EpochID == "" {
		return nil
	}
	var epoch CNIntegrityEpoch
	if err := lockForUpdate(tx).Where("epoch_id = ?", gate.EpochID).First(&epoch).Error; err != nil {
		return err
	}
	var admission CNIntegrityAdmission
	err := lockForUpdate(tx).Where("epoch_id = ? AND user_id = ?", epoch.EpochID, user).First(&admission).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	scope := &cnIntegrityCaptureScope{Epoch: epoch, Admission: admission, BeforeQuota: int64(native.Quota), BeforeMode: native.BillingMode, BeforeRows: map[string]cnIntegrityNativeRow{}, BeforeSlices: map[int64]CNWalletValueSlice{}, Writer: os.Getenv("OPS_CN_INTEGRITY_WRITER_ID"), Build: os.Getenv("OPS_CN_INTEGRITY_WRITER_BUILD"), Broken: epoch.State == "broken" || epoch.State == "closed"}
	var writer CNIntegrityWriter
	err = lockForUpdate(tx).Where("epoch_id = ? AND writer_id = ? AND lease_generation = ?", epoch.EpochID, scope.Writer, epoch.Generation).First(&writer).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if e := lockForUpdate(tx).Where("epoch_id = ? AND generation = ?", epoch.EpochID, epoch.Generation).First(&scope.Fence).Error; e != nil {
		return e
	}
	expires, leaseErr := time.Parse(CNTimeLayout, writer.LeaseUntil)
	var control CNIntegrityControlReceipt
	if err := lockForUpdate(tx).Where("epoch_id = ? AND generation = ?", epoch.EpochID, epoch.Generation).First(&control).Error; err != nil {
		return err
	}
	valid := err == nil && leaseErr == nil && cnIntegrityWriterMatchesFence(writer, scope.Fence) && cnIntegrityFenceValid(scope.Fence) && control.State == epoch.State && control.WriterSetHash == scope.Fence.WriterHash && control.FenceHash == scope.Fence.FenceHash && expires.After(time.Now().UTC()) && writer.BuildID == scope.Build && !common.RedisEnabled && !common.BatchUpdateEnabled && os.Getenv("OPS_CN_INTEGRITY_CAPTURE_ENABLED") == "true"
	if !valid && !scope.Broken {
		if err := cnStopIntegrityEpochTx(tx, epoch.EpochID, epoch.Generation, "broken", "writer_capability_or_capture_unavailable"); err != nil {
			return err
		}
		scope.Epoch.Generation++
		scope.Epoch.State, scope.Broken = "broken", true
		scope.Fence = CNIntegrityFence{}
		if err := lockForUpdate(tx).Where("epoch_id = ? AND generation = ?", epoch.EpochID, scope.Epoch.Generation).First(&scope.Fence).Error; err != nil {
			return err
		}
	}
	rows, err := cnIntegrityNativeRows(tx, user, true)
	if err != nil {
		return err
	}
	for _, row := range rows {
		scope.BeforeRows[CNIdentity(row.Type, row.Key)] = row
	}
	scope.BeforeValues, err = cnIntegrityNativeValueStates(tx, user, true)
	if err != nil {
		return err
	}
	var charges []CNWalletValueCharge
	if err := lockForUpdate(tx).Where("user_id = ?", user).Find(&charges).Error; err != nil {
		return err
	}
	for _, charge := range charges {
		var slices []CNWalletValueSlice
		if err := lockForUpdate(tx).Where("charge_key = ? AND allocation_id <> ?", charge.ChargeKey, "").Find(&slices).Error; err != nil {
			return err
		}
		for _, slice := range slices {
			scope.BeforeSlices[slice.ID] = slice
		}
	}
	stream, err := cnLockPublisher(tx)
	if err != nil {
		return err
	}
	if (stream.Epoch != epoch.StreamEpoch || stream.GatewayID != epoch.GatewayID) && !scope.Broken {
		if err := cnStopIntegrityEpochTx(tx, epoch.EpochID, scope.Epoch.Generation, "broken", "original_stream_identity_changed"); err != nil {
			return err
		}
		scope.Epoch.Generation++
		scope.Epoch.State, scope.Broken = "broken", true
		scope.Fence = CNIntegrityFence{}
		if err := lockForUpdate(tx).Where("epoch_id = ? AND generation = ?", epoch.EpochID, scope.Epoch.Generation).First(&scope.Fence).Error; err != nil {
			return err
		}
	}
	var last CNFinanceOutbox
	err = lockForUpdate(tx).Order("id DESC").First(&last).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	scope.OutboxAfter = last.ID
	tx.Statement.Settings.Store(cnIntegrityCaptureScopeKey, scope)
	return nil
}

func cnIntegrityOpeningCharge(tx *gorm.DB, economic string) bool {
	if economic == "" {
		return false
	}
	value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey)
	if !exists {
		return false
	}
	scope := value.(*cnIntegrityCaptureScope)
	var rows []cnIntegrityNativeRow
	if common.Unmarshal([]byte(scope.Admission.OpeningRows), &rows) != nil {
		return true
	}
	for _, row := range rows {
		if row.EconomicID == economic {
			return true
		}
	}
	return false
}

func cnIntegrityAllocationAllowed(tx *gorm.DB) bool {
	value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey)
	if !exists {
		return true
	}
	scope := value.(*cnIntegrityCaptureScope)
	return scope.Epoch.State == "active" && !scope.Broken
}

func cnInvalidateIntegrityUserTx(tx *gorm.DB, user int) error {
	if err := cnBeginIntegrityCaptureTx(tx, user); err != nil {
		return err
	}
	value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey)
	if !exists {
		return nil
	}
	scope := value.(*cnIntegrityCaptureScope)
	if scope.Broken {
		return nil
	}
	if err := cnStopIntegrityEpochTx(tx, scope.Epoch.EpochID, scope.Epoch.Generation, "broken", "uncovered_native_writer"); err != nil {
		return err
	}
	scope.Epoch.Generation++
	scope.Epoch.State, scope.Broken = "broken", true
	scope.Fence = CNIntegrityFence{}
	if err := lockForUpdate(tx).Where("epoch_id = ? AND generation = ?", scope.Epoch.EpochID, scope.Epoch.Generation).First(&scope.Fence).Error; err != nil {
		return err
	}
	return nil
}

func cnFinishIntegrityCaptureTx(tx *gorm.DB, user int, nativeType, key string) error {
	value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey)
	if !exists {
		return nil
	}
	scope := value.(*cnIntegrityCaptureScope)
	if scope.Admission.UserID != user {
		return ErrCNConflict
	}
	rows, err := cnIntegrityNativeRows(tx, user, true)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Type != nativeType || row.Key != key {
			continue
		}
		identity := CNIdentity(row.Type, row.Key)
		if previous, exists := scope.BeforeRows[identity]; exists && previous == row {
			return nil
		}
		receipt, err := cnJournalIntegrityOriginalTx(tx, scope.Epoch.EpochID, user, scope.Epoch.Generation, nativeType, key, scope.BeforeQuota)
		if err != nil {
			return err
		}
		if nativeType != "topup" {
			if err := cnCaptureIntegritySlicesTx(tx, scope, receipt, CNIdentity(row.EconomicID, "0")); err != nil {
				return err
			}
		}
		known := int64(0)
		reason := "missing_payment_value"
		if nativeType == "topup" {
			var lot CNWalletValueLot
			err := lockForUpdate(tx).Where("user_id = ? AND source_id = ?", user, key).First(&lot).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && !lot.Isolated {
				known = row.Actual
			}
		} else {
			var charge CNWalletValueCharge
			err := lockForUpdate(tx).Where("charge_key = ?", CNIdentity(row.EconomicID, "0")).First(&charge).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				known = row.Actual - charge.UnknownQuota
			}
		}
		if cnIntegrityOpeningCharge(tx, row.EconomicID) {
			reason = "opening_hold"
		}
		if !cnWalletValueAvailable() || scope.Epoch.State == "paused" {
			reason = "allocation_disabled"
		}
		if scope.Broken {
			reason = "suspended"
		}
		if known < 0 || known > row.Actual {
			return ErrCNConflict
		}
		outcome := "unknown"
		if known == row.Actual {
			outcome, reason = "known", ""
		} else if known > 0 {
			outcome = "mixed"
		}
		if row.Actual == 0 {
			outcome, reason = "no_economic_effect", ""
		}
		facts := []string{}
		var staged []CNFinanceOutbox
		if err := lockForUpdate(tx).Where("id > ?", scope.OutboxAfter).Order("id").Find(&staged).Error; err != nil {
			return err
		}
		for _, stagedFact := range staged {
			var fact CNFact
			if err := common.Unmarshal([]byte(stagedFact.Payload), &fact); err != nil {
				return err
			}
			if fact.CustomerKey != nil && *fact.CustomerKey == strconv.Itoa(user) {
				facts = append(facts, fact.FactID)
			}
			scope.OutboxAfter = stagedFact.ID
		}
		factIDs, err := common.Marshal(facts)
		if err != nil {
			return err
		}
		disposition := CNIntegrityDisposition{ReceiptID: receipt.ReceiptID, EpochID: receipt.EpochID, OriginalHash: receipt.OriginalBodyHash, KnownQuota: known, UnknownQuota: row.Actual - known, Reason: reason, Outcome: outcome, FactIDs: string(factIDs)}
		if err := tx.Create(&disposition).Error; err != nil {
			return err
		}
		scope.BeforeQuota = receipt.AfterQuota
		scope.BeforeRows[identity] = row
		return nil
	}
	return errors.New("captured native mutation has no committed source row")
}

func cnCaptureIntegritySlicesTx(tx *gorm.DB, scope *cnIntegrityCaptureScope, receipt *CNIntegrityOriginal, chargeKey string) error {
	var slices []CNWalletValueSlice
	if err := lockForUpdate(tx).Where("charge_key = ? AND allocation_id <> ?", chargeKey, "").Order("id").Find(&slices).Error; err != nil {
		return err
	}
	for _, slice := range slices {
		previous := scope.BeforeSlices[slice.ID]
		if previous == slice {
			continue
		}
		priorValue := decimal.Zero
		if previous.ID != 0 {
			var err error
			priorValue, err = decimal.NewFromString(previous.Value)
			if err != nil || previous.AllocationID != slice.AllocationID || previous.ChargeKey != slice.ChargeKey {
				return ErrCNConflict
			}
		}
		value, err := decimal.NewFromString(slice.Value)
		if err != nil {
			return err
		}
		var part CNWalletValuePart
		if err := lockForUpdate(tx).Where("charge_key = ? AND allocation_id = ?", chargeKey, slice.AllocationID).First(&part).Error; err != nil {
			return err
		}
		var lot CNWalletValueLot
		if err := lockForUpdate(tx).Where("lot_id = ? AND user_id = ?", part.LotID, receipt.UserID).First(&lot).Error; err != nil {
			return err
		}
		var priorDelta CNIntegritySliceDelta
		err = lockForUpdate(tx).Where("source_slice_id = ?", slice.ID).Order("mutation_sequence DESC").First(&priorDelta).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && (priorDelta.EpochID != receipt.EpochID || priorDelta.LineageID != receipt.LineageID) {
			return ErrCNConflict
		}
		material, err := common.Marshal([]string{"cn-native-slice-v2", receipt.ReceiptID, strconv.FormatInt(slice.ID, 10)})
		if err != nil {
			return err
		}
		hash, err := CNIntegrityBodyHash(material)
		if err != nil {
			return err
		}
		delta := CNIntegritySliceDelta{SliceID: strings.TrimPrefix(hash, "sha256:"), EpochID: receipt.EpochID, ReceiptID: receipt.ReceiptID, SourceSliceID: slice.ID, MutationSequence: receipt.MutationSequence, PreviousSliceID: priorDelta.SliceID, AllocationID: slice.AllocationID, LotID: part.LotID, Currency: lot.Currency, QuotaDelta: slice.Quota - previous.Quota, ValueDelta: value.Sub(priorValue).String(), LineageID: receipt.LineageID}
		if err := tx.Create(&delta).Error; err != nil {
			return err
		}
		scope.BeforeSlices[slice.ID] = slice
	}
	return nil
}
