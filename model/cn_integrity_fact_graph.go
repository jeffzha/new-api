package model

import (
	"errors"
	"slices"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func cnBuildIntegrityFactsTx(tx *gorm.DB, epoch CNIntegrityEpoch, originals []CNIntegrityOriginal, dispositions map[string]CNIntegrityDisposition, deltas []CNIntegritySliceDelta, rows map[string][]map[string]any) error {
	facts := map[string]CNFact{}
	hashes := map[string]string{}
	owners := map[string]CNIntegrityOriginal{}
	stagedFacts := map[string]CNFact{}
	knownLineages := map[string]bool{}
	ownedReceipts := map[string]bool{}
	for _, o := range originals {
		d, exists := dispositions[o.ReceiptID]
		if !exists {
			continue
		}
		var ids []string
		if err := common.Unmarshal([]byte(d.FactIDs), &ids); err != nil {
			return err
		}
		// A later known revision cannot retroactively adopt an earlier unknown
		// receipt. Once owned, zero-valued corrections retain that ownership.
		knownLineages[o.LineageID] = knownLineages[o.LineageID] || d.KnownQuota > 0
		if !knownLineages[o.LineageID] {
			continue
		}
		ownedReceipts[o.ReceiptID] = true
		for _, id := range ids {
			if prior, exists := owners[id]; exists && prior.ReceiptID != o.ReceiptID {
				rows["findings"] = append(rows["findings"], cnIntegrityFinding("duplicate", "fact", id, "multiple_original_receipts", false))
				continue
			}
			var outbox CNFinanceOutbox
			if err := tx.Where("fact_id = ?", id).First(&outbox).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				rows["findings"] = append(rows["findings"], cnIntegrityFinding("missing_capture", "fact", id, "outbox_missing", false))
				continue
			}
			var staged CNFact
			if err := common.Unmarshal([]byte(outbox.Payload), &staged); err != nil {
				return err
			}
			owners[id] = o
			stagedFacts[id] = staged
			var publication CNFinancePublication
			if err := tx.Where("fact_id = ?", id).First(&publication).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				rows["findings"] = append(rows["findings"], cnIntegrityFinding("pending_publication", "fact", id, "publication_missing", true))
				continue
			}
			var fact CNFact
			if err := common.Unmarshal([]byte(publication.Payload), &fact); err != nil {
				return err
			}
			staged.PublicationSequence = fact.PublicationSequence
			if !cnIntegrityJSONEqual([]byte(publication.Payload), mustCNIntegrityJSON(staged)) || fact.FactID != id || fact.CustomerKey == nil || *fact.CustomerKey != strconv.Itoa(o.UserID) || !slices.Contains([]string{"payment", "funding_lot", "charge", "funding_allocation", "recognized_revenue"}, fact.Kind) {
				rows["findings"] = append(rows["findings"], cnIntegrityFinding("mismatch", "fact", id, "native_publication_body_mismatch", false))
				continue
			}
			hash, err := cnIntegrityMarshalHash(fact)
			if err != nil {
				return err
			}
			facts[id] = fact
			hashes[id] = hash
		}
	}
	if err := cnScanIntegrityOutboxOwnersTx(tx, originals, owners, rows); err != nil {
		return err
	}
	for id, f := range facts {
		o := owners[id]
		rows["facts"] = append(rows["facts"], map[string]any{"factId": id, "bodyHash": hashes[id], "body": f})
		rows["memberships"] = append(rows["memberships"], map[string]any{"factId": id, "bodyHash": hashes[id], "kind": f.Kind, "economicId": f.EconomicID, "segmentId": f.SegmentID, "revision": strconv.FormatInt(f.Revision, 10), "predecessorFactId": f.PreviousFactID, "publicationSequence": f.PublicationSequence, "customerKey": f.CustomerKey, "receiptId": o.ReceiptID, "admissionId": o.AdmissionID, "nativeLineageId": o.LineageID, "claimMode": "grant", "borrowedAuthorityId": nil})
		rows["edges"] = append(rows["edges"], map[string]any{"fromId": o.ReceiptID, "relation": "receipt_fact", "toId": id, "targetBodyHash": hashes[id]})
		if f.PreviousFactID != nil {
			if _, ok := facts[*f.PreviousFactID]; !ok {
				rows["findings"] = append(rows["findings"], cnIntegrityFinding("missing_capture", "fact", id, "revision_predecessor_missing", false))
			} else {
				rows["edges"] = append(rows["edges"], map[string]any{"fromId": id, "relation": "revision_predecessor", "toId": *f.PreviousFactID, "targetBodyHash": hashes[*f.PreviousFactID]})
			}
		}
	}
	for id, f := range facts {
		for otherID, other := range facts {
			if f.CustomerKey == nil || other.CustomerKey == nil || *f.CustomerKey != *other.CustomerKey {
				continue
			}
			relation := ""
			switch {
			case f.Kind == "payment" && other.Kind == "funding_lot" && slices.Contains(other.Links["paymentIds"], f.EconomicID):
				relation = "payment_lot"
			case f.Kind == "charge" && other.Kind == "funding_allocation" && slices.Contains(other.Links["chargeFactIds"], f.FactID):
				relation = "charge_allocation"
			case f.Kind == "funding_lot" && other.Kind == "funding_allocation" && slices.Contains(other.Links["fundingLotIds"], f.EconomicID):
				relation = "lot_allocation"
			case f.Kind == "funding_allocation" && other.Kind == "recognized_revenue" && other.Revision == f.Revision && slices.Contains(other.Links["allocationIds"], f.EconomicID):
				relation = "allocation_revenue"
			}
			if relation != "" {
				rows["edges"] = append(rows["edges"], map[string]any{"fromId": id, "relation": relation, "toId": otherID, "targetBodyHash": hashes[otherID]})
			}
		}
	}
	for _, o := range originals {
		d, exists := dispositions[o.ReceiptID]
		if !exists {
			continue
		}
		factIDs, sliceIDs := []string{}, []string{}
		currencyValues := map[string]decimal.Decimal{}
		if ownedReceipts[o.ReceiptID] {
			if err := common.Unmarshal([]byte(d.FactIDs), &factIDs); err != nil {
				return err
			}
			for _, id := range factIDs {
				f := stagedFacts[id]
				if f.Kind == "funding_allocation" && f.Cumulative.Money != nil {
					v, err := decimal.NewFromString(f.Cumulative.Money.Gross)
					if err != nil {
						return err
					}
					currencyValues[f.Cumulative.Money.Currency] = currencyValues[f.Cumulative.Money.Currency].Add(v)
				}
				if f.Kind == "payment" && f.Cumulative.Money != nil {
					v, err := decimal.NewFromString(f.Cumulative.Money.Gross)
					if err != nil {
						return err
					}
					currencyValues[f.Cumulative.Money.Currency] = currencyValues[f.Cumulative.Money.Currency].Add(v)
				}
			}
		}
		for _, delta := range deltas {
			if delta.ReceiptID == o.ReceiptID {
				sliceIDs = append(sliceIDs, delta.SliceID)
			}
		}
		slices.Sort(factIDs)
		slices.Sort(sliceIDs)
		values := map[string]string{}
		for currency, v := range currencyValues {
			values[currency] = v.String()
		}
		rows["dispositions"] = append(rows["dispositions"], map[string]any{"dispositionId": CNIdentity("integrity-disposition-v2", o.ReceiptID), "receiptId": o.ReceiptID, "admissionId": o.AdmissionID, "customerKey": strconv.Itoa(o.UserID), "originalBodyHash": d.OriginalHash, "outcome": d.Outcome, "reason": cnIntegrityNullable(d.Reason), "knownQuota": strconv.FormatInt(d.KnownQuota, 10), "unknownQuota": strconv.FormatInt(d.UnknownQuota, 10), "valueByCurrency": values, "allocationSliceIds": sliceIDs, "factIds": factIDs, "nativeLineageId": o.LineageID})
	}
	return cnVerifyIntegrityFinancialGraph(rows)
}

func cnScanIntegrityOutboxOwnersTx(tx *gorm.DB, originals []CNIntegrityOriginal, owners map[string]CNIntegrityOriginal, rows map[string][]map[string]any) error {
	knownCharges, knownPurchases := map[string]bool{}, map[string]bool{}
	for _, original := range originals {
		if original.NativeType == "topup" {
			var count int64
			if err := tx.Model(&CNWalletValueLot{}).Where("user_id = ? AND source_id = ?", original.UserID, original.NativeKey).Count(&count).Error; err != nil {
				return err
			}
			knownPurchases[original.NativeKey] = count > 0
			continue
		}
		var native cnIntegrityNativeRow
		if err := common.Unmarshal([]byte(original.NativePayload), &native); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&CNWalletValuePart{}).Where("charge_key = ?", CNIdentity(native.EconomicID, "0")).Count(&count).Error; err != nil {
			return err
		}
		knownCharges[native.EconomicID] = count > 0
	}
	var outbox []CNFinanceOutbox
	if err := tx.Order("id").Find(&outbox).Error; err != nil {
		return err
	}
	for _, stored := range outbox {
		var fact CNFact
		if err := common.Unmarshal([]byte(stored.Payload), &fact); err != nil {
			return err
		}
		required := fact.Kind == "charge" && knownCharges[fact.EconomicID]
		for _, evidence := range fact.SourceEvidence {
			if evidence.System != "gateway-cn" {
				continue
			}
			switch evidence.RecordType {
			case "cn_wallet_value_lot":
				var lot CNWalletValueLot
				if err := tx.Where("lot_id = ?", evidence.RecordID).First(&lot).Error; err != nil {
					return err
				}
				required = required || knownPurchases[lot.SourceID]
			case "cn_wallet_value_allocation":
				var part CNWalletValuePart
				if err := tx.Where("allocation_id = ?", evidence.RecordID).First(&part).Error; err != nil {
					return err
				}
				var charge CNWalletValueCharge
				if err := tx.Where("charge_key = ?", part.ChargeKey).First(&charge).Error; err != nil {
					return err
				}
				required = required || knownCharges[charge.EconomicID]
			}
		}
		if required {
			if _, exists := owners[fact.FactID]; !exists {
				category, pending := "orphan", false
				var published int64
				if err := tx.Model(&CNFinancePublication{}).Where("fact_id = ?", fact.FactID).Count(&published).Error; err != nil {
					return err
				}
				if published == 0 {
					category, pending = "pending_publication", true
				}
				rows["findings"] = append(rows["findings"], cnIntegrityFinding(category, "fact", fact.FactID, "native_fact_without_frozen_membership", pending))
			}
		}
	}
	return nil
}

func mustCNIntegrityJSON(value any) []byte { raw, _ := common.Marshal(value); return raw }

// Verify the required five-kind dependency graph independently of disposition
// success flags. Each allocation must have original purchase and charge heads.
func cnVerifyIntegrityFinancialGraph(rows map[string][]map[string]any) error {
	facts := map[string]CNFact{}
	edges := map[string]map[string]bool{}
	for _, row := range rows["facts"] {
		f := row["body"].(CNFact)
		facts[f.FactID] = f
	}
	for _, edge := range rows["edges"] {
		target := edge["toId"].(string)
		relation := edge["relation"].(string)
		if edges[target] == nil {
			edges[target] = map[string]bool{}
		}
		edges[target][relation] = true
	}
	for id, f := range facts {
		required := []string{}
		switch f.Kind {
		case "funding_lot":
			required = []string{"payment_lot"}
		case "funding_allocation":
			required = []string{"charge_allocation", "lot_allocation"}
		case "recognized_revenue":
			required = []string{"allocation_revenue"}
		}
		for _, relation := range required {
			if !edges[id][relation] {
				rows["findings"] = append(rows["findings"], cnIntegrityFinding("mismatch", "fact", id, "missing_"+relation, false))
			}
		}
		var previous *CNFact
		var previousMetric *CNMetric
		if f.PreviousFactID != nil {
			if p, ok := facts[*f.PreviousFactID]; ok {
				previous = &p
				previousMetric = &p.Cumulative
			}
		}
		if cnValidateMetric(f.Cumulative, f.Delta, previousMetric) != nil || cnValidateRecognition(f, previous) != nil {
			rows["findings"] = append(rows["findings"], cnIntegrityFinding("mismatch", "fact", id, "invalid_fact_body", false))
		}
	}
	return nil
}
