package model

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const CNIntegritySourceID = "ordinary_wallet_native_funding_integrity"
const CNIntegrityVersion = "native-funding-integrity-v2"
const CNIntegrityPath = "ordinary-wallet/native-funding-integrity-v2"

var cnIntegrityCollections = []string{"admissions", "eligibility", "openings", "originals", "dispositions", "slices", "memberships", "facts", "edges", "controls", "writers", "fence", "findings", "valueStates"}
var ErrCNIntegrityCursorAhead = errors.New("integrity_discovery_cursor_ahead")
var ErrCNIntegrityPage = errors.New("invalid_integrity_page")

type CNIntegrityFreezeHead struct {
	ID       int `gorm:"primaryKey;autoIncrement:false"`
	Sequence int64
}

type CNIntegritySnapshot struct {
	ProofID        string `gorm:"size:71;primaryKey"`
	FreezeSequence int64  `gorm:"not null;uniqueIndex"`
	EpochID        string `gorm:"size:64;not null;uniqueIndex:uidx_cn_integrity_batch,priority:1"`
	BatchSequence  int64  `gorm:"not null;uniqueIndex:uidx_cn_integrity_batch,priority:2"`
	Manifest       string `gorm:"type:text;not null"`
	IndexEntry     string `gorm:"type:text;not null"`
}

type CNIntegritySnapshotCollection struct {
	ProofID    string               `gorm:"size:71;primaryKey"`
	Collection string               `gorm:"size:32;primaryKey"`
	Payload    cnIntegrityLargeText `gorm:"not null"`
	Hashes     cnIntegrityLargeText `gorm:"not null"`
}

type cnIntegrityLargeText string

func (cnIntegrityLargeText) GormDataType() string { return "text" }

func (cnIntegrityLargeText) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db.Dialector.Name() == "mysql" {
		return "longtext"
	}
	return "text"
}

type CNIntegrityFreezeRequest struct {
	EpochID               string
	ExpectedGeneration    int64
	ExpectedParentProofID string
	CoverageFrom          string
	CoverageTo            string
}

func cnIntegrityMarshalHash(value any) (string, error) {
	raw, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	return CNIntegrityBodyHash(raw)
}

func CNIntegrityExportJSON(value any) ([]byte, error) {
	raw, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	return CNIntegrityCanonicalJSON(raw)
}

func cnIntegrityCollectionHashes(name string, rows []map[string]any) ([]string, error) {
	root, err := cnIntegrityMarshalHash([]string{"cn-integrity-rows-v2", name})
	if err != nil {
		return nil, err
	}
	hashes := []string{root}
	for i, row := range rows {
		root, err = cnIntegrityMarshalHash([]any{"cn-integrity-row-v2", root, strconv.Itoa(i), row})
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, root)
	}
	return hashes, nil
}

// Freeze is a local operator operation. It never runs from GET or startup.
func FreezeCNIntegrity(ctx context.Context, db *gorm.DB, request CNIntegrityFreezeRequest) (*CNIntegritySnapshot, error) {
	from, e1 := time.Parse(CNTimeLayout, request.CoverageFrom)
	to, e2 := time.Parse(CNTimeLayout, request.CoverageTo)
	if e1 != nil || e2 != nil || !from.Before(to) || from.Format(CNTimeLayout) != request.CoverageFrom || to.Format(CNTimeLayout) != request.CoverageTo {
		return nil, ErrCNIntegrityPage
	}
	var sealed CNIntegritySnapshot
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var epoch CNIntegrityEpoch
		if err := lockForUpdate(tx).Where("epoch_id = ?", request.EpochID).First(&epoch).Error; err != nil {
			return err
		}
		if epoch.Generation != request.ExpectedGeneration {
			return ErrCNConflict
		}
		var global CNIntegrityFreezeHead
		if err := lockForUpdate(tx).First(&global, 1).Error; err != nil {
			return err
		}
		if global.Sequence == math.MaxInt64 {
			return ErrCNConflict
		}
		var parent CNIntegritySnapshot
		err := tx.Where("epoch_id = ?", epoch.EpochID).Order("batch_sequence DESC").First(&parent).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if parent.ProofID != request.ExpectedParentProofID || parent.BatchSequence == math.MaxInt64 {
			return ErrCNConflict
		}
		batch := parent.BatchSequence + 1
		batchText := strconv.FormatInt(batch, 10)
		var stream CNFinanceStream
		if err := lockForUpdate(tx).First(&stream, 1).Error; err != nil {
			return err
		}
		if stream.Epoch != epoch.StreamEpoch || stream.GatewayID != epoch.GatewayID {
			return ErrCNConflict
		}
		collections, scan, err := cnBuildIntegrityCollectionsTx(tx, epoch, batchText)
		if err != nil {
			return err
		}
		if err := cnRetainIntegrityFindingsTx(tx, parent.ProofID, batchText, collections); err != nil {
			return err
		}
		if err := cnVerifyIntegrityParentTx(tx, parent.ProofID, collections); err != nil {
			return err
		}
		if err := tx.Where("epoch_id = ?", epoch.EpochID).First(&epoch).Error; err != nil {
			return err
		}
		var control CNIntegrityControlReceipt
		if err := tx.Where("epoch_id = ? AND generation = ?", epoch.EpochID, epoch.Generation).First(&control).Error; err != nil {
			return err
		}
		descriptors := map[string]any{}
		collectionHashes := map[string][]string{}
		for _, name := range cnIntegrityCollections {
			hashes, err := cnIntegrityCollectionHashes(name, collections[name])
			if err != nil {
				return err
			}
			collectionHashes[name] = hashes
			descriptors[name] = map[string]any{"count": strconv.Itoa(len(collections[name])), "root": hashes[len(hashes)-1]}
		}
		unknown := map[string]string{}
		for _, reason := range []string{"opening_history", "opening_hold", "missing_payment_value", "allocation_disabled", "admission_disabled", "preexisting_fact", "suspended", "source_switched", "unsupported_path"} {
			unknown[reason] = "0"
		}
		known := int64(0)
		for _, row := range collections["dispositions"] {
			if reasonPtr, ok := row["reason"].(*string); ok && reasonPtr != nil {
				reason := *reasonPtr
				n, _ := strconv.ParseInt(unknown[reason], 10, 64)
				unknown[reason] = strconv.FormatInt(n+1, 10)
			}
			if row["knownQuota"] != "0" {
				known++
			}
		}
		pending, gaps := int64(0), int64(0)
		resolved := map[string]bool{}
		for _, finding := range collections["findings"] {
			if id, ok := finding["resolvesFindingId"].(string); ok {
				resolved[id] = true
			}
		}
		for _, finding := range collections["findings"] {
			if finding["resolvesFindingId"] != nil || resolved[finding["findingId"].(string)] {
				continue
			}
			if finding["pending"] == true {
				pending++
			} else {
				gaps++
			}
		}
		if scan.MismatchCount+scan.MissingOriginalCount+scan.MissingDispositionCount+scan.OrphanCount > 0 && gaps == 0 {
			return ErrCNConflict
		}
		status := "complete"
		if pending > 0 {
			status = "pending"
		}
		if gaps > 0 {
			status = "gap"
		}
		now := time.Now().UTC().Format(CNTimeLayout)
		manifest := map[string]any{"schema": "cn-funding-integrity-manifest-v2", "sourceId": CNIntegritySourceID, "version": CNIntegrityVersion, "path": CNIntegrityPath, "sourceSystem": CNFinanceSource, "gatewayId": epoch.GatewayID, "streamEpoch": epoch.StreamEpoch, "integrityEpoch": epoch.EpochID, "policyVersion": cnWalletValuePolicy, "writerProtocolVersion": cnIntegrityWriterProtocol, "batchSequence": batchText, "parentProofId": cnIntegrityNullable(parent.ProofID), "controlGeneration": strconv.FormatInt(epoch.Generation, 10), "writerSetHash": control.WriterSetHash, "fenceHash": cnIntegrityNullable(control.FenceHash), "membershipThrough": strconv.FormatInt(epoch.MembershipSequence, 10), "mutationThrough": strconv.FormatInt(epoch.MutationSequence, 10), "valueThrough": strconv.FormatInt(epoch.ValueSequence, 10), "publicationThrough": strconv.FormatInt(stream.Sequence, 10), "publishedThrough": strconv.FormatInt(stream.Sequence, 10), "coverageFrom": request.CoverageFrom, "coverageTo": request.CoverageTo, "capturedAt": now, "sourceCutoff": now, "capturedThrough": now, "authorityKinds": []string{"charge", "funding_allocation", "funding_lot", "payment", "recognized_revenue"}, "supportedPaths": []string{CNIntegrityPath}, "excludedPaths": []string{"historical-and-uninstrumented", "ops-fifo-customer-transition"}, "companionSourceIds": []string{"payments_topups"}, "status": status, "pendingCount": strconv.FormatInt(pending, 10), "unresolvedIntegrityCount": strconv.FormatInt(gaps, 10), "intentionalUnknownByReason": unknown, "knownMutationCount": strconv.FormatInt(known, 10), "admittedCustomerCount": strconv.Itoa(len(collections["admissions"])), "collections": descriptors}
		manifest["companionSourceIds"] = []string{"funding_lots_allocations", "payments_topups"}
		raw, err := common.Marshal(manifest)
		if err != nil {
			return err
		}
		raw, err = CNIntegrityCanonicalJSON(raw)
		if err != nil {
			return err
		}
		proofID, err := CNIntegrityBodyHash(raw)
		if err != nil {
			return err
		}
		entry := map[string]any{"freezeSequence": strconv.FormatInt(global.Sequence+1, 10), "proofId": proofID, "sourceId": CNIntegritySourceID, "version": CNIntegrityVersion, "path": CNIntegrityPath, "sourceSystem": CNFinanceSource, "gatewayId": epoch.GatewayID, "streamEpoch": epoch.StreamEpoch, "integrityEpoch": epoch.EpochID, "batchSequence": batchText, "parentProofId": cnIntegrityNullable(parent.ProofID), "publicationThrough": strconv.FormatInt(stream.Sequence, 10), "status": status}
		index, err := CNIntegrityExportJSON(entry)
		if err != nil {
			return err
		}
		sealed = CNIntegritySnapshot{ProofID: proofID, FreezeSequence: global.Sequence + 1, EpochID: epoch.EpochID, BatchSequence: batch, Manifest: string(raw), IndexEntry: string(index)}
		if err := tx.Create(&sealed).Error; err != nil {
			return err
		}
		for _, name := range cnIntegrityCollections {
			payload, err := CNIntegrityExportJSON(collections[name])
			if err != nil {
				return err
			}
			hashes, err := common.Marshal(collectionHashes[name])
			if err != nil {
				return err
			}
			if err := tx.Create(&CNIntegritySnapshotCollection{ProofID: proofID, Collection: name, Payload: cnIntegrityLargeText(payload), Hashes: cnIntegrityLargeText(hashes)}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&global).Update("sequence", global.Sequence+1).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return &sealed, err
}

func cnVerifyIntegrityParentTx(tx *gorm.DB, parentID string, collections map[string][]map[string]any) error {
	if parentID == "" {
		return nil
	}
	identities := map[string][]string{
		"admissions": {"admissionId"}, "eligibility": {"customerKey"}, "openings": {"openingId"},
		"originals": {"receiptId"}, "dispositions": {"dispositionId"}, "slices": {"sliceId"},
		"memberships": {"factId"}, "facts": {"factId"}, "edges": {"fromId", "relation", "toId"},
		"controls": {"generation"}, "writers": {"leaseGeneration", "writerId"}, "fence": {"fenceId"},
		"findings": {"findingId"}, "valueStates": {"stateId"},
	}
	for _, name := range cnIntegrityCollections {
		current := map[string]string{}
		for _, row := range collections[name] {
			key := []any{}
			for _, field := range identities[name] {
				key = append(key, row[field])
			}
			id, err := cnIntegrityMarshalHash(key)
			if err != nil {
				return err
			}
			hash, err := cnIntegrityMarshalHash(row)
			if err != nil {
				return err
			}
			if _, exists := current[id]; exists {
				return ErrCNConflict
			}
			current[id] = hash
		}
		var previous CNIntegritySnapshotCollection
		if err := tx.Where("proof_id = ? AND collection = ?", parentID, name).First(&previous).Error; err != nil {
			return err
		}
		var prior []map[string]any
		if err := common.Unmarshal([]byte(previous.Payload), &prior); err != nil {
			return err
		}
		for _, row := range prior {
			key := []any{}
			for _, field := range identities[name] {
				key = append(key, row[field])
			}
			id, err := cnIntegrityMarshalHash(key)
			if err != nil {
				return err
			}
			hash, err := cnIntegrityMarshalHash(row)
			if err != nil {
				return err
			}
			if current[id] != hash {
				return ErrCNConflict
			}
		}
	}
	return nil
}

func ReadCNIntegrityManifest(ctx context.Context, db *gorm.DB, proofID string) ([]byte, error) {
	if !cnIntegrityHashValid(proofID) {
		return nil, ErrCNIntegrityPage
	}
	var row CNIntegritySnapshot
	if err := db.WithContext(ctx).Where("proof_id = ?", proofID).First(&row).Error; err != nil {
		return nil, err
	}
	return []byte(row.Manifest), nil
}

func cnFrozenIntegrityDependencyTx(tx *gorm.DB, stream *CNFinanceStream) (*CNDependency, error) {
	var frozen CNIntegritySnapshot
	epochs := tx.Model(&CNIntegrityEpoch{}).Select("epoch_id").Where("gateway_id = ? AND stream_epoch = ?", stream.GatewayID, stream.Epoch)
	err := tx.Where("epoch_id IN (?)", epochs).Order("freeze_sequence DESC").First(&frozen).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		CoverageFrom     string   `json:"coverageFrom"`
		CoverageTo       string   `json:"coverageTo"`
		SourceCutoff     string   `json:"sourceCutoff"`
		CapturedThrough  string   `json:"capturedThrough"`
		PublishedThrough string   `json:"publishedThrough"`
		PendingCount     string   `json:"pendingCount"`
		UnresolvedCount  string   `json:"unresolvedIntegrityCount"`
		SupportedPaths   []string `json:"supportedPaths"`
		ExcludedPaths    []string `json:"excludedPaths"`
		Status           string   `json:"status"`
	}
	if err := common.Unmarshal([]byte(frozen.Manifest), &manifest); err != nil {
		return nil, err
	}
	pending, err := strconv.ParseInt(manifest.PendingCount, 10, 64)
	if err != nil {
		return nil, err
	}
	unresolved, err := strconv.ParseInt(manifest.UnresolvedCount, 10, 64)
	if err != nil {
		return nil, err
	}
	return &CNDependency{SourceID: CNIntegritySourceID, Version: CNIntegrityVersion, Required: true,
		CoverageFrom: manifest.CoverageFrom, CoverageTo: &manifest.CoverageTo,
		SourceCutoff: &manifest.SourceCutoff, CapturedThrough: &manifest.CapturedThrough,
		PublishedThrough: &manifest.PublishedThrough, PendingCount: pending, UnknownCount: unresolved,
		SupportedPaths: manifest.SupportedPaths, ExcludedPaths: manifest.ExcludedPaths,
		ProofID: &frozen.ProofID, Status: manifest.Status}, nil
}

func ReadCNIntegrityPage(ctx context.Context, db *gorm.DB, proofID, name string, ordinal int64, limit int) (map[string]any, error) {
	if !cnIntegrityHashValid(proofID) || !slices.Contains(cnIntegrityCollections, name) || ordinal < 0 || limit < 1 || limit > 1000 {
		return nil, ErrCNIntegrityPage
	}
	var row CNIntegritySnapshotCollection
	if err := db.WithContext(ctx).Where("proof_id = ? AND collection = ?", proofID, name).First(&row).Error; err != nil {
		return nil, err
	}
	var rows []common.RawMessage
	var hashes []string
	if err := common.Unmarshal([]byte(row.Payload), &rows); err != nil {
		return nil, err
	}
	if err := common.Unmarshal([]byte(row.Hashes), &hashes); err != nil {
		return nil, err
	}
	if ordinal > int64(len(rows)) {
		return nil, ErrCNIntegrityPage
	}
	end := min(ordinal+int64(limit), int64(len(rows)))
	var next *string
	if end < int64(len(rows)) {
		n := strconv.FormatInt(end, 10)
		next = &n
	}
	return map[string]any{"schema": "cn-funding-integrity-page-v2", "snapshotId": proofID, "proofId": proofID, "collection": name, "ordinal": strconv.FormatInt(ordinal, 10), "nextOrdinal": next, "totalCount": strconv.Itoa(len(rows)), "root": hashes[len(hashes)-1], "prefixHash": hashes[ordinal], "suffixHash": hashes[end], "rows": rows[ordinal:end]}, nil
}

func ReadCNIntegrityIndex(ctx context.Context, db *gorm.DB, gateway string, after int64, limit int) (map[string]any, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, ErrCNIntegrityPage
	}
	var result map[string]any
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var head CNIntegrityFreezeHead
		if err := tx.First(&head, 1).Error; err != nil {
			return err
		}
		if after > head.Sequence {
			return ErrCNIntegrityCursorAhead
		}
		var rows []CNIntegritySnapshot
		if err := tx.Where("freeze_sequence > ? AND freeze_sequence <= ?", after, head.Sequence).Order("freeze_sequence").Limit(limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		items := make([]common.RawMessage, 0, len(rows))
		next := after
		for _, row := range rows {
			items = append(items, common.RawMessage(row.IndexEntry))
			next = row.FreezeSequence
		}
		result = map[string]any{"schema": "cn-funding-integrity-index-v2", "sourceSystem": "gateway-cn", "gatewayId": gateway, "after": strconv.FormatInt(after, 10), "observedThrough": strconv.FormatInt(head.Sequence, 10), "nextCursor": strconv.FormatInt(next, 10), "hasMore": more, "items": items}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

func cnIntegritySortRows(name string, rows []map[string]any) {
	keys := map[string][]string{"admissions": {"membershipSequence"}, "eligibility": {"customerKey"}, "openings": {"openingId"}, "originals": {"mutationSequence"}, "dispositions": {"receiptId"}, "slices": {"sliceId"}, "memberships": {"factId"}, "facts": {"factId"}, "edges": {"fromId", "relation", "toId"}, "controls": {"generation"}, "writers": {"leaseGeneration", "writerId"}, "fence": {"fenceId"}, "findings": {"findingId"}, "valueStates": {"stateSequence"}}[name]
	numeric := map[string]bool{"membershipSequence": true, "mutationSequence": true, "generation": true, "leaseGeneration": true, "stateSequence": true}
	slices.SortFunc(rows, func(a, b map[string]any) int {
		for _, key := range keys {
			av, bv := a[key].(string), b[key].(string)
			if numeric[key] {
				an, _ := strconv.ParseInt(av, 10, 64)
				bn, _ := strconv.ParseInt(bv, 10, 64)
				if an < bn {
					return -1
				}
				if an > bn {
					return 1
				}
			} else if c := strings.Compare(av, bv); c != 0 {
				return c
			}
		}
		return 0
	})
}

func cnIntegrityFinding(category, entityType, entityID, reason string, pending bool) map[string]any {
	return map[string]any{"findingId": CNIdentity("integrity-finding-v2", category, entityType, entityID, reason), "category": category, "entityType": entityType, "entityId": entityID, "reason": reason, "pending": pending, "relatedIds": []string{}, "resolvesFindingId": nil}
}

func cnBuildIntegrityCollectionsTx(tx *gorm.DB, epoch CNIntegrityEpoch, batch string) (map[string][]map[string]any, cnIntegrityFoundationScan, error) {
	rows := map[string][]map[string]any{}
	for _, name := range cnIntegrityCollections {
		rows[name] = []map[string]any{}
	}
	var admissions []CNIntegrityAdmission
	var originals []CNIntegrityOriginal
	var dispositions []CNIntegrityDisposition
	var deltas []CNIntegritySliceDelta
	for _, query := range []struct {
		dest  any
		order string
	}{{&admissions, "membership_sequence"}, {&originals, "mutation_sequence"}, {&dispositions, "receipt_id"}, {&deltas, "slice_id"}} {
		if err := tx.Where("epoch_id = ?", epoch.EpochID).Order(query.order).Find(query.dest).Error; err != nil {
			return nil, cnIntegrityFoundationScan{}, err
		}
	}
	byAdmission := map[string]CNIntegrityAdmission{}
	byOriginal := map[string]CNIntegrityOriginal{}
	byDisposition := map[string]CNIntegrityDisposition{}
	for _, a := range admissions {
		byAdmission[a.AdmissionID] = a
		var opening []cnIntegrityNativeRow
		if err := common.Unmarshal([]byte(a.OpeningRows), &opening); err != nil {
			return nil, cnIntegrityFoundationScan{}, err
		}
		openingRows := []map[string]any{}
		openingIDs := []string{}
		for _, o := range opening {
			kind := map[string]string{"topup": "topup_credit", "wallet": "wallet_reserve", "task": "task_reserve"}[o.Type]
			_, lineage, err := CNIntegrityNativeIdentity(epoch.GatewayID, a.UserID, kind, o.Key)
			if err != nil {
				return nil, cnIntegrityFoundationScan{}, err
			}
			hash, err := cnIntegrityMarshalHash(o)
			if err != nil {
				return nil, cnIntegrityFoundationScan{}, err
			}
			id := CNIdentity("integrity-opening-v2", a.AdmissionID, o.Type, o.Key)
			openingIDs = append(openingIDs, id)
			openingRows = append(openingRows, map[string]any{"openingId": id, "admissionId": a.AdmissionID, "nativeKind": kind, "nativeKey": o.Key, "nativeRevision": strconv.FormatInt(o.Revision, 10), "heldQuota": strconv.FormatInt(o.Held, 10), "actualQuota": strconv.FormatInt(o.Actual, 10), "ownerMode": "unknown", "nativeLineageId": lineage, "originalBodyHash": hash})
		}
		cnIntegritySortRows("openings", openingRows)
		slices.Sort(openingIDs)
		openingHash, err := cnIntegrityMarshalHash(openingRows)
		if err != nil {
			return nil, cnIntegrityFoundationScan{}, err
		}
		rows["openings"] = append(rows["openings"], openingRows...)
		_, lineage, err := CNIntegrityNativeIdentity(epoch.GatewayID, a.UserID, "quota_mutation", strconv.Itoa(a.UserID))
		if err != nil {
			return nil, cnIntegrityFoundationScan{}, err
		}
		eligibilityID := CNIdentity("integrity-eligibility-v2", a.AdmissionID, a.OpsInventoryID)
		rows["admissions"] = append(rows["admissions"], map[string]any{"admissionId": a.AdmissionID, "membershipSequence": strconv.FormatInt(a.MembershipSequence, 10), "customerKey": strconv.Itoa(a.UserID), "nativeUserId": strconv.Itoa(a.UserID), "billingMode": a.BillingMode, "admittedAt": a.AdmittedAt, "openingBalance": strconv.FormatInt(a.OpeningBalance, 10), "openingUnknownQuota": decimal.NewFromInt(a.OpeningBalance).Add(decimal.NewFromInt(a.OpeningHeld)).String(), "openingHoldIds": openingIDs, "openingReceiptHash": openingHash, "nativeLineageId": lineage, "eligibilityId": eligibilityID})
		rows["eligibility"] = append(rows["eligibility"], map[string]any{"eligibilityId": eligibilityID, "customerKey": strconv.Itoa(a.UserID), "opsInventoryId": a.OpsInventoryID, "opsInventoryHash": a.OpsInventoryHash, "opsFifoHistoryCount": "0", "intersectingAuthorityIds": []string{}, "decision": "eligible"})
		if _, err := cnValidateIntegrityEligibilityTx(tx, epoch, a.UserID, a.OpsInventoryID); err != nil {
			rows["findings"] = append(rows["findings"], cnIntegrityFinding("mismatch", "admission", a.AdmissionID, "eligibility_snapshot_invalid", false))
		}
	}
	for _, o := range originals {
		byOriginal[o.ReceiptID] = o
		var body CNIntegrityOriginalBody
		if err := common.Unmarshal([]byte(o.OriginalBody), &body); err != nil {
			return nil, cnIntegrityFoundationScan{}, err
		}
		var paymentHash *string
		if len(body.PaymentSnapshot) > 0 && string(body.PaymentSnapshot) != "null" {
			hash, err := CNIntegrityBodyHash(body.PaymentSnapshot)
			if err != nil {
				return nil, cnIntegrityFoundationScan{}, err
			}
			paymentHash = &hash
		}
		before, _ := decimal.NewFromString(body.HeldBefore)
		after, _ := decimal.NewFromString(body.HeldAfter)
		rows["originals"] = append(rows["originals"], map[string]any{"receiptId": o.ReceiptID, "mutationSequence": strconv.FormatInt(o.MutationSequence, 10), "membershipSequence": strconv.FormatInt(byAdmission[o.AdmissionID].MembershipSequence, 10), "admissionId": o.AdmissionID, "customerKey": strconv.Itoa(o.UserID), "nativeKind": body.NativeKind, "nativeKey": o.NativeKey, "nativeRevision": strconv.FormatInt(o.NativeRevision, 10), "predecessorReceiptId": cnIntegrityNullable(o.PreviousReceiptID), "writerId": o.WriterID, "writerBuild": o.WriterBuild, "controlGeneration": strconv.FormatInt(o.Generation, 10), "writerSetHash": o.WriterSetHash, "fenceHash": o.FenceHash, "effectiveAt": body.EffectiveAt, "occurredAt": body.OccurredAt, "quotaDelta": decimal.NewFromInt(o.AfterQuota).Sub(decimal.NewFromInt(o.BeforeQuota)).String(), "cumulativeActual": strconv.FormatInt(o.TargetQuota, 10), "heldDelta": after.Sub(before).String(), "originalBody": body, "originalBodyHash": o.OriginalBodyHash, "paymentSnapshotHash": paymentHash, "nativeLineageId": o.LineageID})
	}
	for _, d := range dispositions {
		byDisposition[d.ReceiptID] = d
	}
	for _, delta := range deltas {
		rows["slices"] = append(rows["slices"], map[string]any{"sliceId": delta.SliceID, "receiptId": delta.ReceiptID, "lotId": delta.LotID, "allocationId": delta.AllocationID, "predecessorSliceId": cnIntegrityNullable(delta.PreviousSliceID), "quotaDelta": strconv.FormatInt(delta.QuotaDelta, 10), "valueDelta": delta.ValueDelta, "currency": delta.Currency, "nativeLineageId": delta.LineageID})
	}
	if err := cnBuildIntegrityFactsTx(tx, epoch, originals, byDisposition, deltas, rows); err != nil {
		return nil, cnIntegrityFoundationScan{}, err
	}
	var controls []CNIntegrityControlReceipt
	if err := tx.Where("epoch_id = ?", epoch.EpochID).Order("generation").Find(&controls).Error; err != nil {
		return nil, cnIntegrityFoundationScan{}, err
	}
	for _, c := range controls {
		active := c.State == "active"
		rows["controls"] = append(rows["controls"], map[string]any{"generation": strconv.FormatInt(c.Generation, 10), "previousGeneration": strconv.FormatInt(c.PreviousGeneration, 10), "state": c.State, "admissionEnabled": active, "allocationEnabled": active, "captureEnabled": c.State != "broken" && c.State != "closed" && c.State != "prepared", "writerSetHash": c.WriterSetHash, "fenceHash": cnIntegrityNullable(c.FenceHash), "mutationThrough": strconv.FormatInt(c.MutationThrough, 10), "operatorEvidenceId": c.OperatorEvidenceID, "changedAt": c.ChangedAt})
	}
	var fences []CNIntegrityFence
	if err := tx.Where("epoch_id = ?", epoch.EpochID).Find(&fences).Error; err != nil {
		return nil, cnIntegrityFoundationScan{}, err
	}
	for _, f := range fences {
		var body map[string]any
		if err := common.Unmarshal([]byte(f.FencePayload), &body); err != nil {
			return nil, cnIntegrityFoundationScan{}, err
		}
		body["fenceBodyHash"] = f.FenceHash
		rows["fence"] = append(rows["fence"], body)
		var members []map[string]any
		if err := common.Unmarshal([]byte(f.WriterSet), &members); err != nil {
			return nil, cnIntegrityFoundationScan{}, err
		}
		rows["writers"] = append(rows["writers"], members...)
	}
	// Scan before cutoff; an unexplained current-state change must remain a gap.
	scan, err := cnScanIntegrityFoundation(tx, epoch.EpochID)
	if err != nil {
		return nil, scan, err
	}
	for _, entry := range []struct {
		category string
		count    int64
	}{{"missing_capture", scan.MissingOriginalCount}, {"mismatch", scan.MismatchCount}, {"missing_capture", scan.MissingDispositionCount}, {"orphan", scan.OrphanCount}} {
		if entry.count > 0 {
			rows["findings"] = append(rows["findings"], cnIntegrityFinding(entry.category, "epoch", epoch.EpochID, entry.category+":"+strconv.FormatInt(entry.count, 10), false))
		}
	}
	if epoch.State != "active" && epoch.State != "paused" {
		rows["findings"] = append(rows["findings"], cnIntegrityFinding("fence_broken", "epoch", epoch.EpochID, "epoch_"+epoch.State, false))
	}
	known := false
	for _, d := range dispositions {
		known = known || d.KnownQuota > 0
	}
	if !known {
		rows["findings"] = append(rows["findings"], cnIntegrityFinding("pending_native", "epoch", epoch.EpochID, "known_activity_required", true))
	}
	for _, a := range admissions {
		if err := cnRecordIntegrityValueStatesTx(tx, epoch.EpochID, a, "cutoff", batch, epoch.Generation, epoch.MutationSequence, &batch, nil); err != nil {
			return nil, scan, err
		}
	}
	var states []CNIntegrityValueState
	if err := tx.Where("epoch_id = ?", epoch.EpochID).Order("state_sequence").Find(&states).Error; err != nil {
		return nil, scan, err
	}
	for _, state := range states {
		var body map[string]any
		if err := common.Unmarshal([]byte(state.Payload), &body); err != nil {
			return nil, scan, err
		}
		rows["valueStates"] = append(rows["valueStates"], body)
	}
	for _, name := range cnIntegrityCollections {
		cnIntegritySortRows(name, rows[name])
	}
	return rows, scan, nil
}
