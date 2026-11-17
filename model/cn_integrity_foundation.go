package model

import (
	"database/sql"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// These internal capture records are not the frozen sidecar wire. Preparation
// alone cannot activate capture or an Ops authority.
type CNIntegrityEpoch struct {
	EpochID            string `gorm:"size:64;primaryKey"`
	GatewayID          string `gorm:"size:191;not null"`
	StreamEpoch        string `gorm:"size:64;not null"`
	PolicyVersion      string `gorm:"size:64;not null"`
	State              string `gorm:"size:32;not null"`
	Generation         int64
	MembershipSequence int64
	MutationSequence   int64
	ValueSequence      int64
	OperatorEvidenceID string `gorm:"size:191;not null"`
	CreatedAt          string `gorm:"size:40;not null"`
}

type CNIntegrityAdmission struct {
	AdmissionID        string `gorm:"size:64;primaryKey"`
	EpochID            string `gorm:"size:64;not null;uniqueIndex:uidx_cn_integrity_member,priority:1;uniqueIndex:uidx_cn_integrity_member_seq,priority:1"`
	UserID             int    `gorm:"not null;uniqueIndex:uidx_cn_integrity_member,priority:2"`
	MembershipSequence int64  `gorm:"not null;uniqueIndex:uidx_cn_integrity_member_seq,priority:2"`
	OpeningBalance     int64
	OpeningHeld        int64
	BillingMode        string `gorm:"size:64"`
	OpeningRows        string `gorm:"type:text;not null"`
	OpeningHash        string `gorm:"size:64;not null"`
	OpsInventoryID     string `gorm:"size:64"`
	OpsInventoryHash   string `gorm:"size:71"`
	AdmittedAt         string `gorm:"size:40;not null"`
}

// Original receipts journal source state before a cumulative native row loses
// its revision. Lineage excludes both streamEpoch and integrityEpoch.
type CNIntegrityOriginal struct {
	ReceiptID         string `gorm:"size:64;primaryKey"`
	EpochID           string `gorm:"size:64;not null;uniqueIndex:uidx_cn_integrity_mutation,priority:1"`
	MutationSequence  int64  `gorm:"not null;uniqueIndex:uidx_cn_integrity_mutation,priority:2"`
	AdmissionID       string `gorm:"size:64;not null;index"`
	UserID            int    `gorm:"not null;index"`
	Generation        int64
	LineageID         string `gorm:"size:64;not null;index"`
	NativeType        string `gorm:"size:32;not null"`
	NativeKey         string `gorm:"size:255;not null"`
	NativeRevision    int64
	SourceRevision    int64
	PreviousReceiptID string `gorm:"size:64"`
	BeforeQuota       int64
	AfterQuota        int64
	TargetQuota       int64
	NativePayload     string `gorm:"type:text;not null"`
	NativeHash        string `gorm:"size:64;not null"`
	OriginalBody      string `gorm:"type:text;not null"`
	OriginalBodyHash  string `gorm:"size:71;not null"`
	WriterID          string `gorm:"size:191"`
	WriterBuild       string `gorm:"size:191"`
	WriterSetHash     string `gorm:"size:71"`
	FenceHash         string `gorm:"size:71"`
}

type CNIntegrityDisposition struct {
	ReceiptID    string `gorm:"size:64;primaryKey"`
	EpochID      string `gorm:"size:64;not null;index"`
	OriginalHash string `gorm:"size:71;not null"`
	KnownQuota   int64
	UnknownQuota int64
	Reason       string `gorm:"size:64;not null"`
	Outcome      string `gorm:"size:32;not null"`
	FactIDs      string `gorm:"type:text;not null"`
}

type CNIntegrityControlReceipt struct {
	EpochID            string `gorm:"size:64;primaryKey"`
	Generation         int64  `gorm:"primaryKey;autoIncrement:false"`
	PreviousGeneration int64
	MutationThrough    int64
	State              string `gorm:"size:32;not null"`
	OperatorEvidenceID string `gorm:"size:191;not null"`
	WriterSetHash      string `gorm:"size:71;not null"`
	FenceHash          string `gorm:"size:71"`
	ChangedAt          string `gorm:"size:40;not null"`
}

type cnIntegrityNativeRow struct {
	Type               string `json:"type"`
	Key                string `json:"key"`
	Revision           int64  `json:"revision"`
	Held               int64  `json:"held"`
	Actual             int64  `json:"actual"`
	State              string `json:"state"`
	Source             string `json:"source"`
	CompleteTime       int64  `json:"completeTime"`
	PaymentSnapshot    string `json:"paymentSnapshot"`
	ConversionSnapshot string `json:"conversionSnapshot"`
	EconomicID         string `json:"economicId"`
	RowID              string `json:"rowId"`
	CreatedAt          string `json:"createdAt"`
}

type CNIntegrityOriginalBody struct {
	Schema                  string            `json:"schema"`
	NativeKind              string            `json:"nativeKind"`
	NativeKey               string            `json:"nativeKey"`
	NativeRevision          string            `json:"nativeRevision"`
	NativeRowID             string            `json:"nativeRowId"`
	CustomerKey             string            `json:"customerKey"`
	BalanceBefore           string            `json:"balanceBefore"`
	BalanceAfter            string            `json:"balanceAfter"`
	HeldBefore              string            `json:"heldBefore"`
	HeldAfter               string            `json:"heldAfter"`
	ActualBefore            string            `json:"actualBefore"`
	ActualAfter             string            `json:"actualAfter"`
	BillingModeBefore       string            `json:"billingModeBefore"`
	BillingModeAfter        string            `json:"billingModeAfter"`
	NativeStatus            string            `json:"nativeStatus"`
	PaymentSnapshot         common.RawMessage `json:"paymentSnapshot"`
	QuotaConversionSnapshot common.RawMessage `json:"quotaConversionSnapshot"`
	EffectiveAt             string            `json:"effectiveAt"`
	OccurredAt              string            `json:"occurredAt"`
}

func cnIntegrityRequireTx(tx *gorm.DB) error {
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return errors.New("integrity foundation requires an explicit native transaction")
	}
	return nil
}

func cnPrepareIntegrityEpochTx(tx *gorm.DB, operatorEvidence string) (*CNIntegrityEpoch, error) {
	if err := cnIntegrityRequireTx(tx); err != nil {
		return nil, err
	}
	if operatorEvidence == "" || len(operatorEvidence) > 191 {
		return nil, errors.New("integrity preparation requires operator evidence")
	}
	var stream CNFinanceStream
	if err := tx.First(&stream, 1).Error; err != nil {
		return nil, err
	}
	epoch := CNIntegrityEpoch{EpochID: uuid.NewString(), GatewayID: stream.GatewayID, StreamEpoch: stream.Epoch, PolicyVersion: cnWalletValuePolicy, State: "prepared", Generation: 1, OperatorEvidenceID: operatorEvidence, CreatedAt: time.Now().UTC().Format(CNTimeLayout)}
	if err := tx.Create(&epoch).Error; err != nil {
		return nil, err
	}
	emptyWriterSetHash, err := CNIntegrityBodyHash([]byte("[]"))
	if err != nil {
		return nil, err
	}
	control := CNIntegrityControlReceipt{EpochID: epoch.EpochID, Generation: 1, State: "prepared", OperatorEvidenceID: operatorEvidence, WriterSetHash: emptyWriterSetHash, ChangedAt: epoch.CreatedAt}
	return &epoch, tx.Create(&control).Error
}

// Stopping preserves receipts and never resumes a broken epoch.
func cnStopIntegrityEpochTx(tx *gorm.DB, epochID string, expectedGeneration int64, state, operatorEvidence string) error {
	if err := cnIntegrityRequireTx(tx); err != nil {
		return err
	}
	if (state != "paused" && state != "broken" && state != "closed") || operatorEvidence == "" || len(operatorEvidence) > 191 {
		return ErrCNConflict
	}
	var epoch CNIntegrityEpoch
	if err := lockForUpdate(tx).Where("epoch_id = ?", epochID).First(&epoch).Error; err != nil {
		return err
	}
	if epoch.Generation != expectedGeneration || epoch.Generation == math.MaxInt64 || epoch.State == "broken" || epoch.State == "closed" {
		return ErrCNConflict
	}
	control := CNIntegrityControlReceipt{EpochID: epochID, Generation: epoch.Generation + 1, PreviousGeneration: epoch.Generation, MutationThrough: epoch.MutationSequence, State: state, OperatorEvidenceID: operatorEvidence, ChangedAt: time.Now().UTC().Format(CNTimeLayout)}
	fence, err := cnCopyIntegrityFenceTx(tx, epoch, control.Generation)
	if err != nil {
		return err
	}
	if fence == nil {
		control.WriterSetHash, err = CNIntegrityBodyHash([]byte("[]"))
		if err != nil {
			return err
		}
	} else {
		control.WriterSetHash, control.FenceHash = fence.WriterHash, fence.FenceHash
	}
	if err := tx.Create(&control).Error; err != nil {
		return err
	}
	return tx.Model(&epoch).Updates(map[string]any{"generation": control.Generation, "state": state, "operator_evidence_id": operatorEvidence}).Error
}

// Project explicit economic fields: TopUp's public JSON omits its snapshots,
// while task rows also contain private provider credentials that must not leak.
func cnIntegrityNativeRows(tx *gorm.DB, user int, current bool) ([]cnIntegrityNativeRow, error) {
	query := tx.Session(&gorm.Session{NewDB: true}).Where("user_id = ?", user)
	if current {
		query = lockForUpdate(query)
	}
	rows := []cnIntegrityNativeRow{}
	var topups []TopUp
	if err := query.Session(&gorm.Session{}).Where("status = ?", common.TopUpStatusSuccess).Order("id").Find(&topups).Error; err != nil {
		return nil, err
	}
	for _, row := range topups {
		quota, _ := row.settledQuota()
		rows = append(rows, cnIntegrityNativeRow{Type: "topup", Key: row.TradeNo, Revision: 1, Actual: quota, State: row.Status, CompleteTime: row.CompleteTime, PaymentSnapshot: row.PaymentSnapshot, ConversionSnapshot: row.QuotaConversionSnapshot, RowID: strconv.Itoa(row.Id)})
	}
	var wallets []CNWalletLifecycle
	if err := query.Session(&gorm.Session{}).Order("request_id").Find(&wallets).Error; err != nil {
		return nil, err
	}
	for _, row := range wallets {
		held := int64(0)
		if row.State == "reserved" {
			held = row.Reserved
		}
		rows = append(rows, cnIntegrityNativeRow{Type: "wallet", Key: row.RequestID, Revision: row.Version, Held: held, Actual: row.Actual, State: row.State, Source: TaskBillingSourceWallet, EconomicID: "request:" + row.RequestID, RowID: row.RequestID, CreatedAt: row.CreatedAt})
	}
	var tasks []CNTaskBilling
	if err := query.Session(&gorm.Session{}).Where("source = ?", TaskBillingSourceWallet).Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Find(&tasks).Error; err != nil {
		return nil, err
	}
	for _, row := range tasks {
		rows = append(rows, cnIntegrityNativeRow{Type: "task", Key: row.Key, Revision: row.Version, Held: int64(row.HeldQuota - row.Actual), Actual: int64(row.Actual), Source: row.Source, EconomicID: "request:" + row.RequestID, RowID: row.Key, CreatedAt: row.CreatedAt})
	}
	return rows, nil
}

func cnAdmitIntegrityUserTx(tx *gorm.DB, epochID string, user int, generation int64, inventoryIDs ...string) (*CNIntegrityAdmission, error) {
	if err := cnIntegrityRequireTx(tx); err != nil {
		return nil, err
	}
	var native User
	if err := lockForUpdate(tx).Select("id", "quota", "billing_mode").First(&native, user).Error; err != nil {
		return nil, err
	}
	var epoch CNIntegrityEpoch
	if err := lockForUpdate(tx).Where("epoch_id = ?", epochID).First(&epoch).Error; err != nil {
		return nil, err
	}
	if (epoch.State != "prepared" && epoch.State != "active") || epoch.Generation != generation || epoch.MembershipSequence == math.MaxInt64 || native.BillingMode == AgencyDurableBillingMode || native.BillingMode == AgencyProvisioningBillingMode || common.RedisEnabled || common.BatchUpdateEnabled {
		return nil, ErrCNConflict
	}
	var approval *CNIntegrityEligibilityApproval
	if len(inventoryIDs) > 1 {
		return nil, ErrCNConflict
	}
	if len(inventoryIDs) == 1 {
		var err error
		approval, err = cnValidateIntegrityEligibilityTx(tx, epoch, user, inventoryIDs[0])
		if err != nil {
			return nil, err
		}
	}
	if epoch.State == "active" && approval == nil {
		return nil, errors.New("active admission requires an approved Ops eligibility snapshot")
	}
	var existing CNIntegrityAdmission
	err := lockForUpdate(tx).Where("epoch_id = ? AND user_id = ?", epochID, user).First(&existing).Error
	if err == nil {
		if approval != nil && (existing.OpsInventoryID != approval.SnapshotID || existing.OpsInventoryHash != approval.InventoryHash) {
			return nil, ErrCNConflict
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var legacyLots []CNWalletValueLot
	if err := lockForUpdate(tx).Where("user_id = ?", user).Find(&legacyLots).Error; err != nil {
		return nil, err
	}
	if len(legacyLots) != 0 {
		return nil, errors.New("integrity admission requires legacy paid-lot ownership transition")
	}
	if tx.Migrator().HasTable(&AgencyFundingAccount{}) {
		var owners []AgencyFundingAccount
		if err := lockForUpdate(tx).Where("user_id = ?", user).Find(&owners).Error; err != nil {
			return nil, err
		}
		if len(owners) != 0 {
			return nil, errors.New("integrity admission excludes existing agency funding owners")
		}
	}
	rows, err := cnIntegrityNativeRows(tx, user, true)
	if err != nil {
		return nil, err
	}
	payload, err := common.Marshal(rows)
	if err != nil {
		return nil, err
	}
	admission := CNIntegrityAdmission{AdmissionID: CNIdentity("integrity-admission", epochID, strconv.Itoa(user)), EpochID: epochID, UserID: user, MembershipSequence: epoch.MembershipSequence + 1, OpeningBalance: int64(native.Quota), BillingMode: native.BillingMode, OpeningRows: string(payload), OpeningHash: CNIdentity("integrity-opening-internal-v1", string(payload))}
	if approval != nil {
		admission.OpsInventoryID, admission.OpsInventoryHash = approval.SnapshotID, approval.InventoryHash
	}
	admission.AdmittedAt = time.Now().UTC().Format(CNTimeLayout)
	for _, row := range rows {
		if row.Held > 0 && admission.OpeningHeld > math.MaxInt64-row.Held {
			return nil, errors.New("integrity opening hold overflow")
		}
		admission.OpeningHeld += row.Held
	}
	if err := tx.Create(&admission).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&epoch).Update("membership_sequence", admission.MembershipSequence).Error; err != nil {
		return nil, err
	}
	return &admission, cnRecordIntegrityValueStatesTx(tx, epochID, admission, "admission", admission.AdmissionID, epoch.Generation, epoch.MutationSequence, nil, nil)
}

// Called after the original native row/balance mutation, inside the same
// transaction. Prepared shadow capture grants no economic posting authority.
func cnJournalIntegrityOriginalTx(tx *gorm.DB, epochID string, user int, generation int64, nativeType, key string, beforeQuota int64) (*CNIntegrityOriginal, error) {
	if err := cnIntegrityRequireTx(tx); err != nil {
		return nil, err
	}
	var native User
	if err := lockForUpdate(tx).Select("id", "quota", "billing_mode").First(&native, user).Error; err != nil {
		return nil, err
	}
	var epoch CNIntegrityEpoch
	if err := lockForUpdate(tx).Where("epoch_id = ?", epochID).First(&epoch).Error; err != nil {
		return nil, err
	}
	if (epoch.State != "prepared" && epoch.State != "active" && epoch.State != "paused" && epoch.State != "broken" && epoch.State != "closed") || epoch.Generation != generation || epoch.MutationSequence == math.MaxInt64 {
		return nil, ErrCNConflict
	}
	var admission CNIntegrityAdmission
	if err := lockForUpdate(tx).Where("epoch_id = ? AND user_id = ?", epochID, user).First(&admission).Error; err != nil {
		return nil, err
	}
	rows, err := cnIntegrityNativeRows(tx, user, true)
	if err != nil {
		return nil, err
	}
	var source *cnIntegrityNativeRow
	for i := range rows {
		if rows[i].Type == nativeType && rows[i].Key == key {
			source = &rows[i]
		}
	}
	if source == nil || source.Revision < 1 {
		return nil, errors.New("integrity original native row missing")
	}
	payload, err := common.Marshal(source)
	if err != nil {
		return nil, err
	}
	kind := map[string]string{"topup": "topup_credit", "wallet": "wallet_final", "task": "task_revision"}[nativeType]
	_, lineage, err := CNIntegrityNativeIdentity(epoch.GatewayID, user, kind, key)
	if err != nil {
		return nil, err
	}
	receipt := CNIntegrityOriginal{EpochID: epochID, MutationSequence: epoch.MutationSequence + 1, AdmissionID: admission.AdmissionID, UserID: user, Generation: generation, LineageID: lineage, NativeType: nativeType, NativeKey: key, SourceRevision: source.Revision, BeforeQuota: beforeQuota, AfterQuota: int64(native.Quota), TargetQuota: source.Actual, NativePayload: string(payload), NativeHash: CNIdentity("integrity-native-internal-v1", string(payload))}
	var previous CNIntegrityOriginal
	err = lockForUpdate(tx).Where("lineage_id = ?", lineage).Order("native_revision DESC").First(&previous).Error
	if err == nil && previous.NativePayload == receipt.NativePayload {
		receipt.ReceiptID, receipt.NativeRevision, receipt.MutationSequence, receipt.PreviousReceiptID = previous.ReceiptID, previous.NativeRevision, previous.MutationSequence, previous.PreviousReceiptID
		receipt.OriginalBody, receipt.OriginalBodyHash = previous.OriginalBody, previous.OriginalBodyHash
		receipt.WriterID, receipt.WriterBuild, receipt.WriterSetHash, receipt.FenceHash = previous.WriterID, previous.WriterBuild, previous.WriterSetHash, previous.FenceHash
		if receipt != previous {
			return nil, ErrCNConflict
		}
		return &previous, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var opening []cnIntegrityNativeRow
	if err := common.Unmarshal([]byte(admission.OpeningRows), &opening); err != nil {
		return nil, err
	}
	priorRevision := int64(0)
	var priorSource cnIntegrityNativeRow
	for _, row := range opening {
		if row.Type == nativeType && row.Key == key {
			priorRevision = row.Revision
			priorSource = row
		}
	}
	receipt.NativeRevision = priorRevision + 1
	if err == nil {
		if previous.EpochID != epochID || previous.AdmissionID != admission.AdmissionID {
			return nil, ErrCNConflict
		}
		priorRevision = previous.SourceRevision
		if err := common.Unmarshal([]byte(previous.NativePayload), &priorSource); err != nil {
			return nil, err
		}
		receipt.PreviousReceiptID = previous.ReceiptID
		receipt.NativeRevision = previous.NativeRevision + 1
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	maxAdvance := int64(1)
	if priorRevision == 0 && nativeType == "wallet" {
		maxAdvance = 2
	}
	stopped := epoch.State == "broken" || epoch.State == "closed"
	if receipt.NativeRevision < 1 || (!stopped && (source.Revision < priorRevision || source.Revision-priorRevision > maxAdvance)) {
		return nil, ErrCNConflict
	}
	receipt.ReceiptID = CNIdentity("integrity-original-v2", lineage, strconv.FormatInt(receipt.NativeRevision, 10))
	var userHead CNIntegrityOriginal
	err = lockForUpdate(tx).Where("admission_id = ?", admission.AdmissionID).Order("mutation_sequence DESC").First(&userHead).Error
	priorBalance := admission.OpeningBalance
	if err == nil {
		priorBalance = userHead.AfterQuota
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if beforeQuota != priorBalance && !stopped {
		return nil, ErrCNConflict
	}
	if stopped {
		if value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey); exists {
			priorSource = value.(*cnIntegrityCaptureScope).BeforeRows[CNIdentity(nativeType, key)]
		}
	}
	if !decimal.NewFromInt(receipt.AfterQuota).Sub(decimal.NewFromInt(beforeQuota)).Equal(cnIntegrityNativeDelta(*source, priorSource)) {
		return nil, ErrCNConflict
	}
	if nativeType == "wallet" {
		kind = "wallet_reserve"
		if source.State != "reserved" {
			kind = "wallet_final"
		} else if source.Held < priorSource.Held {
			kind = "wallet_release"
		}
	} else if nativeType == "task" && source.Actual == priorSource.Actual && source.Held != priorSource.Held {
		kind = "task_reserve"
	}
	occurred := time.Now().UTC().Format(CNTimeLayout)
	effective := source.CreatedAt
	if nativeType == "topup" {
		effective = time.Unix(source.CompleteTime, 0).UTC().Format(CNTimeLayout)
	}
	if effective == "" {
		effective = occurred
	}
	body := CNIntegrityOriginalBody{Schema: "cn-original-mutation-v2", NativeKind: kind, NativeKey: key, NativeRevision: strconv.FormatInt(receipt.NativeRevision, 10), NativeRowID: source.RowID, CustomerKey: strconv.Itoa(user), BalanceBefore: strconv.FormatInt(beforeQuota, 10), BalanceAfter: strconv.FormatInt(receipt.AfterQuota, 10), HeldBefore: strconv.FormatInt(priorSource.Held, 10), HeldAfter: strconv.FormatInt(source.Held, 10), ActualBefore: strconv.FormatInt(priorSource.Actual, 10), ActualAfter: strconv.FormatInt(source.Actual, 10), BillingModeBefore: admission.BillingMode, BillingModeAfter: native.BillingMode, NativeStatus: source.State, EffectiveAt: effective, OccurredAt: occurred}
	if nativeType == "topup" {
		if source.PaymentSnapshot != "" {
			body.PaymentSnapshot = common.RawMessage(source.PaymentSnapshot)
		}
		if source.ConversionSnapshot != "" {
			body.QuotaConversionSnapshot = common.RawMessage(source.ConversionSnapshot)
		}
	}
	if nativeType == "task" {
		var task CNTaskBilling
		if err := lockForUpdate(tx).Select("state").Where(map[string]any{"key": key}).First(&task).Error; err != nil {
			return nil, err
		}
		body.NativeStatus = task.State
	}
	if value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey); exists {
		body.BillingModeBefore = value.(*cnIntegrityCaptureScope).BeforeMode
	}
	encoded, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	receipt.OriginalBody = string(encoded)
	receipt.OriginalBodyHash, err = CNIntegrityBodyHash(encoded)
	if err != nil {
		return nil, err
	}
	if value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey); exists {
		scope := value.(*cnIntegrityCaptureScope)
		receipt.WriterID, receipt.WriterBuild = scope.Writer, scope.Build
		receipt.WriterSetHash, receipt.FenceHash = scope.Fence.WriterHash, scope.Fence.FenceHash
	}
	if err := tx.Create(&receipt).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&epoch).Update("mutation_sequence", receipt.MutationSequence).Error; err != nil {
		return nil, err
	}
	var before []cnIntegrityValueEntity
	if value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey); exists {
		before = value.(*cnIntegrityCaptureScope).BeforeValues
	}
	if err := cnRecordIntegrityValueStatesTx(tx, epochID, admission, "original", receipt.ReceiptID, generation, receipt.MutationSequence, nil, before); err != nil {
		return nil, err
	}
	if value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey); exists {
		scope := value.(*cnIntegrityCaptureScope)
		scope.BeforeValues, err = cnIntegrityNativeValueStates(tx, user, true)
		if err != nil {
			return nil, err
		}
	}
	return &receipt, nil
}

func cnAppendIntegrityDispositionTx(tx *gorm.DB, receipt *CNIntegrityOriginal, unknown int64, reason string) error {
	if err := cnIntegrityRequireTx(tx); err != nil {
		return err
	}
	// The foundation only records explicit unknown/no-effect shadow outcomes.
	// Known slices/facts require the separately frozen graph implementation.
	if receipt == nil || unknown < 0 || unknown != receipt.TargetQuota {
		return ErrCNConflict
	}
	if !cnIntegrityUnknownReason(reason) {
		return errors.New("invalid integrity disposition reason")
	}
	var original CNIntegrityOriginal
	if err := lockForUpdate(tx).Where("receipt_id = ?", receipt.ReceiptID).First(&original).Error; err != nil {
		return err
	}
	if original != *receipt {
		return ErrCNConflict
	}
	disposition := CNIntegrityDisposition{ReceiptID: receipt.ReceiptID, EpochID: receipt.EpochID, OriginalHash: receipt.OriginalBodyHash, UnknownQuota: unknown, Reason: reason, Outcome: "unknown", FactIDs: "[]"}
	var previous CNIntegrityDisposition
	err := lockForUpdate(tx).Where("receipt_id = ?", receipt.ReceiptID).First(&previous).Error
	if err == nil {
		if previous != disposition {
			return ErrCNConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&disposition).Error
}

func cnIntegrityUnknownReason(reason string) bool {
	switch reason {
	case "opening_history", "opening_hold", "missing_payment_value", "allocation_disabled", "admission_disabled", "preexisting_fact", "suspended", "source_switched", "unsupported_path":
		return true
	default:
		return false
	}
}

func cnIntegrityNativeDelta(current, previous cnIntegrityNativeRow) decimal.Decimal {
	if current.Type == "topup" {
		return decimal.NewFromInt(current.Actual)
	}
	return decimal.NewFromInt(current.Actual).Add(decimal.NewFromInt(current.Held)).Sub(decimal.NewFromInt(previous.Actual)).Sub(decimal.NewFromInt(previous.Held)).Neg()
}

type cnIntegrityFoundationScan struct {
	MembershipThrough       int64
	MutationThrough         int64
	MissingOriginalCount    int64
	MissingDispositionCount int64
	MismatchCount           int64
	OrphanCount             int64
	IntentionalUnknownCount int64
	ActivationBlockers      []string
}

// Enumerate current original business rows independently of shadow receipts,
// then check both directions. This is diagnostic foundation evidence, never a
// complete proof: frozen published fact graph verification is separate.
func cnScanIntegrityFoundation(db *gorm.DB, epochID string) (cnIntegrityFoundationScan, error) {
	scan := cnIntegrityFoundationScan{ActivationBlockers: []string{}}
	err := db.Transaction(func(tx *gorm.DB) error {
		var epoch CNIntegrityEpoch
		if err := tx.Where("epoch_id = ?", epochID).First(&epoch).Error; err != nil {
			return err
		}
		scan.MembershipThrough, scan.MutationThrough = epoch.MembershipSequence, epoch.MutationSequence
		if epoch.State != "active" {
			scan.ActivationBlockers = append(scan.ActivationBlockers, "epoch_"+epoch.State)
		}
		var fence CNIntegrityFence
		fenceErr := tx.Where("epoch_id = ? AND generation = ?", epochID, epoch.Generation).First(&fence).Error
		if errors.Is(fenceErr, gorm.ErrRecordNotFound) {
			scan.ActivationBlockers = append(scan.ActivationBlockers, "writer_inventory_fence_missing")
		} else if fenceErr != nil {
			return fenceErr
		} else {
			writerHash, writerErr := cnIntegrityWriterSetHash([]byte(fence.WriterSet))
			fenceHash, hashErr := CNIntegrityBodyHash([]byte(fence.FencePayload))
			if writerErr != nil || hashErr != nil || writerHash != fence.WriterHash || fenceHash != fence.FenceHash || !cnIntegrityFenceValid(fence) {
				scan.MismatchCount++
			}
		}
		writers := map[string]CNIntegrityWriter{}
		fences := map[int64]CNIntegrityFence{}
		if fenceErr == nil {
			var history []CNIntegrityFence
			if err := tx.Where("epoch_id = ?", epochID).Find(&history).Error; err != nil {
				return err
			}
			for _, row := range history {
				fences[row.Generation] = row
			}
			var registered []CNIntegrityWriter
			if err := tx.Where("epoch_id = ?", epochID).Find(&registered).Error; err != nil {
				return err
			}
			for _, writer := range registered {
				writers[strconv.FormatInt(writer.LeaseGeneration, 10)+":"+writer.WriterID] = writer
				if !cnIntegrityWriterMatchesFence(writer, fences[writer.LeaseGeneration]) {
					scan.MismatchCount++
				}
			}
		}
		var admissions []CNIntegrityAdmission
		if err := tx.Where("epoch_id = ?", epochID).Order("membership_sequence").Find(&admissions).Error; err != nil {
			return err
		}
		var originals []CNIntegrityOriginal
		if err := tx.Where("epoch_id = ?", epochID).Order("mutation_sequence").Find(&originals).Error; err != nil {
			return err
		}
		var dispositions []CNIntegrityDisposition
		if err := tx.Where("epoch_id = ?", epochID).Find(&dispositions).Error; err != nil {
			return err
		}
		var controls []CNIntegrityControlReceipt
		if err := tx.Where("epoch_id = ?", epochID).Order("generation").Find(&controls).Error; err != nil {
			return err
		}
		if int64(len(controls)) != epoch.Generation {
			scan.MismatchCount++
		}
		for i, control := range controls {
			if control.Generation != int64(i)+1 || control.PreviousGeneration != int64(i) || control.MutationThrough > epoch.MutationSequence {
				scan.MismatchCount++
			}
			if control.FenceHash != "" {
				frozen, exists := fences[control.Generation]
				writerHash, writerErr := cnIntegrityWriterSetHash([]byte(frozen.WriterSet))
				fenceHash, hashErr := CNIntegrityBodyHash([]byte(frozen.FencePayload))
				if !exists || writerErr != nil || hashErr != nil || writerHash != frozen.WriterHash || fenceHash != frozen.FenceHash || control.WriterSetHash != frozen.WriterHash || control.FenceHash != frozen.FenceHash || !cnIntegrityFenceValid(frozen) {
					scan.MismatchCount++
				}
			}
		}
		if len(controls) > 0 {
			head := controls[len(controls)-1]
			if head.State != epoch.State || head.OperatorEvidenceID != epoch.OperatorEvidenceID {
				scan.MismatchCount++
			}
		}
		byReceipt := map[string]CNIntegrityOriginal{}
		dispositionByReceipt := map[string]CNIntegrityDisposition{}
		for _, row := range dispositions {
			dispositionByReceipt[row.ReceiptID] = row
		}
		for i, row := range originals {
			byReceipt[row.ReceiptID] = row
			if row.MutationSequence != int64(i)+1 || row.NativeHash != CNIdentity("integrity-native-internal-v1", row.NativePayload) {
				scan.MismatchCount++
			}
			originalHash, hashErr := CNIntegrityBodyHash([]byte(row.OriginalBody))
			var originalBody CNIntegrityOriginalBody
			if hashErr != nil || common.Unmarshal([]byte(row.OriginalBody), &originalBody) != nil || originalHash != row.OriginalBodyHash || originalBody.NativeRevision != strconv.FormatInt(row.NativeRevision, 10) || originalBody.BalanceBefore != strconv.FormatInt(row.BeforeQuota, 10) || originalBody.BalanceAfter != strconv.FormatInt(row.AfterQuota, 10) || originalBody.ActualAfter != strconv.FormatInt(row.TargetQuota, 10) {
				scan.MismatchCount++
			}
			if fenceErr == nil {
				writer, exists := writers[strconv.FormatInt(row.Generation, 10)+":"+row.WriterID]
				frozen := fences[row.Generation]
				expires, leaseErr := time.Parse(CNTimeLayout, writer.LeaseUntil)
				occurred, timeErr := time.Parse(CNTimeLayout, originalBody.OccurredAt)
				if !exists || writer.BuildID != row.WriterBuild || row.WriterSetHash != frozen.WriterHash || row.FenceHash != frozen.FenceHash || leaseErr != nil || timeErr != nil || !expires.After(occurred) {
					scan.MismatchCount++
				}
			}
			var source cnIntegrityNativeRow
			if err := common.Unmarshal([]byte(row.NativePayload), &source); err != nil {
				return err
			}
			kind := map[string]string{"topup": "topup_credit", "wallet": "wallet_final", "task": "task_revision"}[row.NativeType]
			_, lineage, err := CNIntegrityNativeIdentity(epoch.GatewayID, row.UserID, kind, row.NativeKey)
			if err != nil {
				return err
			}
			if source.Type != row.NativeType || source.Key != row.NativeKey || source.Revision != row.SourceRevision || source.Actual != row.TargetQuota || row.LineageID != lineage || row.ReceiptID != CNIdentity("integrity-original-v2", lineage, strconv.FormatInt(row.NativeRevision, 10)) {
				scan.MismatchCount++
			}
			if disposition, exists := dispositionByReceipt[row.ReceiptID]; !exists {
				scan.MissingDispositionCount++
			} else if disposition.OriginalHash != row.OriginalBodyHash || disposition.KnownQuota < 0 || disposition.UnknownQuota < 0 || !decimal.NewFromInt(disposition.KnownQuota).Add(decimal.NewFromInt(disposition.UnknownQuota)).Equal(decimal.NewFromInt(row.TargetQuota)) || (disposition.UnknownQuota > 0 && !cnIntegrityUnknownReason(disposition.Reason)) {
				scan.MismatchCount++
			} else if disposition.UnknownQuota > 0 {
				scan.IntentionalUnknownCount++
			}
		}
		if int64(len(originals)) != epoch.MutationSequence || int64(len(admissions)) != epoch.MembershipSequence {
			scan.MismatchCount++
		}
		for _, row := range dispositions {
			if _, exists := byReceipt[row.ReceiptID]; !exists {
				scan.OrphanCount++
			}
		}
		seenReceipts := map[string]bool{}
		for i, admission := range admissions {
			if admission.MembershipSequence != int64(i)+1 || admission.OpeningHash != CNIdentity("integrity-opening-internal-v1", admission.OpeningRows) {
				scan.MismatchCount++
			}
			var opening []cnIntegrityNativeRow
			if err := common.Unmarshal([]byte(admission.OpeningRows), &opening); err != nil {
				return err
			}
			expected := map[string]string{}
			for _, row := range opening {
				body, err := common.Marshal(row)
				if err != nil {
					return err
				}
				expected[CNIdentity(row.Type, row.Key)] = string(body)
			}
			balance := admission.OpeningBalance
			priorReceipts := map[string]string{}
			priorRevisions := map[string]int64{}
			for _, row := range opening {
				priorRevisions[CNIdentity(row.Type, row.Key)] = row.Revision
			}
			for _, row := range originals {
				if row.AdmissionID != admission.AdmissionID {
					continue
				}
				seenReceipts[row.ReceiptID] = true
				if row.BeforeQuota != balance || row.UserID != admission.UserID {
					scan.MismatchCount++
				}
				key := CNIdentity(row.NativeType, row.NativeKey)
				var prior, current cnIntegrityNativeRow
				if body, exists := expected[key]; exists {
					if err := common.Unmarshal([]byte(body), &prior); err != nil {
						return err
					}
				}
				if err := common.Unmarshal([]byte(row.NativePayload), &current); err != nil {
					return err
				}
				if row.NativeRevision != priorRevisions[key]+1 || row.PreviousReceiptID != priorReceipts[key] || !decimal.NewFromInt(row.AfterQuota).Sub(decimal.NewFromInt(row.BeforeQuota)).Equal(cnIntegrityNativeDelta(current, prior)) {
					scan.MismatchCount++
				}
				if row.Generation < 1 || row.Generation > int64(len(controls)) {
					scan.MismatchCount++
				}
				priorReceipts[key] = row.ReceiptID
				priorRevisions[key] = row.NativeRevision
				balance = row.AfterQuota
				expected[key] = row.NativePayload
			}
			var user User
			if err := tx.Select("id", "quota", "billing_mode").First(&user, admission.UserID).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				scan.MismatchCount++
			} else if balance != int64(user.Quota) || admission.BillingMode != user.BillingMode {
				scan.MismatchCount++
			}
			rows, err := cnIntegrityNativeRows(tx, admission.UserID, false)
			if err != nil {
				return err
			}
			for _, row := range rows {
				key := CNIdentity(row.Type, row.Key)
				body, err := common.Marshal(row)
				if err != nil {
					return err
				}
				if prior, exists := expected[key]; !exists || prior != string(body) {
					scan.MissingOriginalCount++
				}
				delete(expected, key)
			}
			scan.OrphanCount += int64(len(expected))
		}
		for _, row := range originals {
			if !seenReceipts[row.ReceiptID] {
				scan.OrphanCount++
			}
		}
		if err := cnScanIntegritySlicesTx(tx, epochID, byReceipt, dispositionByReceipt, admissions, &scan); err != nil {
			return err
		}
		return cnScanIntegrityValueStatesTx(tx, epoch, admissions, byReceipt, &scan)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return scan, err
}

func cnScanIntegritySlicesTx(tx *gorm.DB, epochID string, originals map[string]CNIntegrityOriginal, dispositions map[string]CNIntegrityDisposition, admissions []CNIntegrityAdmission, scan *cnIntegrityFoundationScan) error {
	var deltas []CNIntegritySliceDelta
	if err := tx.Where("epoch_id = ?", epochID).Order("mutation_sequence").Order("source_slice_id").Find(&deltas).Error; err != nil {
		return err
	}
	totals := map[int64]decimal.Decimal{}
	values := map[int64]decimal.Decimal{}
	heads := map[int64]CNIntegritySliceDelta{}
	knownDeltas := map[string]decimal.Decimal{}
	for _, delta := range deltas {
		original, exists := originals[delta.ReceiptID]
		if !exists {
			scan.OrphanCount++
		}
		value, err := decimal.NewFromString(delta.ValueDelta)
		if err != nil {
			scan.MismatchCount++
			continue
		}
		prior := heads[delta.SourceSliceID]
		if original.LineageID != delta.LineageID || original.MutationSequence != delta.MutationSequence || delta.PreviousSliceID != prior.SliceID || (prior.SliceID != "" && (prior.AllocationID != delta.AllocationID || prior.LotID != delta.LotID || prior.LineageID != delta.LineageID || prior.Currency != delta.Currency)) || value.String() != delta.ValueDelta || (delta.QuotaDelta >= 0 && value.IsNegative()) || (delta.QuotaDelta <= 0 && value.IsPositive()) {
			scan.MismatchCount++
		}
		totals[delta.SourceSliceID] = totals[delta.SourceSliceID].Add(decimal.NewFromInt(delta.QuotaDelta))
		values[delta.SourceSliceID] = values[delta.SourceSliceID].Add(value)
		knownDeltas[delta.ReceiptID] = knownDeltas[delta.ReceiptID].Add(decimal.NewFromInt(delta.QuotaDelta))
		heads[delta.SourceSliceID] = delta
	}
	var ordered []CNIntegrityOriginal
	if err := tx.Where("epoch_id = ?", epochID).Order("mutation_sequence").Find(&ordered).Error; err != nil {
		return err
	}
	known := map[string]decimal.Decimal{}
	for _, original := range ordered {
		if original.NativeType == "topup" {
			continue
		}
		known[original.LineageID] = known[original.LineageID].Add(knownDeltas[original.ReceiptID])
		if !known[original.LineageID].Equal(decimal.NewFromInt(dispositions[original.ReceiptID].KnownQuota)) {
			scan.MismatchCount++
		}
	}
	seen := map[int64]bool{}
	for _, admission := range admissions {
		var charges []CNWalletValueCharge
		if err := tx.Where("user_id = ?", admission.UserID).Find(&charges).Error; err != nil {
			return err
		}
		for _, charge := range charges {
			var native []CNWalletValueSlice
			if err := tx.Where("charge_key = ? AND allocation_id <> ?", charge.ChargeKey, "").Find(&native).Error; err != nil {
				return err
			}
			allocationQuota := map[string]decimal.Decimal{}
			allocationValue := map[string]decimal.Decimal{}
			for _, slice := range native {
				seen[slice.ID] = true
				head, exists := heads[slice.ID]
				if !exists {
					scan.MissingOriginalCount++
				}
				value, err := decimal.NewFromString(slice.Value)
				if err != nil {
					scan.MismatchCount++
					continue
				}
				if head.AllocationID != slice.AllocationID || !totals[slice.ID].Equal(decimal.NewFromInt(slice.Quota)) || !values[slice.ID].Equal(value) || originals[head.ReceiptID].UserID != charge.UserID {
					scan.MismatchCount++
				}
				allocationQuota[slice.AllocationID] = allocationQuota[slice.AllocationID].Add(decimal.NewFromInt(slice.Quota))
				allocationValue[slice.AllocationID] = allocationValue[slice.AllocationID].Add(value)
			}
			var parts []CNWalletValuePart
			if err := tx.Where("charge_key = ?", charge.ChargeKey).Find(&parts).Error; err != nil {
				return err
			}
			for _, part := range parts {
				var allocation CNFundingValueAllocation
				if err := tx.Where("allocation_id = ?", part.AllocationID).First(&allocation).Error; err != nil {
					if !errors.Is(err, gorm.ErrRecordNotFound) {
						return err
					}
					scan.MismatchCount++
					continue
				}
				if allocation.LotID != part.LotID || !allocationQuota[part.AllocationID].Equal(decimal.NewFromInt(allocation.Quota)) || allocationValue[part.AllocationID].String() != allocation.Value {
					scan.MismatchCount++
				}
			}
		}
	}
	for source := range heads {
		if !seen[source] {
			scan.OrphanCount++
		}
	}
	return nil
}
