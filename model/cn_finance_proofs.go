package model

import (
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type cnProofScan struct {
	dependency CNDependency
	records    []any
}

// Proofs certify a named forward path and fixed source snapshot, not unseen
// historical writes or externally owned finance documents. Every gap stays in
// the manifest so Ops can combine its own authoritative dependency proofs.
func cnBuildSourceProofs(tx *gorm.DB, stream *CNFinanceStream) ([]byte, error) {
	started, err := time.Parse(time.RFC3339Nano, stream.StartedAt)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().Truncate(5 * time.Minute)
	if cutoff.Before(started) {
		cutoff = started
	}
	cutoffText := cutoff.Format(CNTimeLayout)
	coverageFrom := started.UTC().Format(CNTimeLayout)
	var outbox []CNFinanceOutbox
	if err = tx.Order("id").Find(&outbox).Error; err != nil {
		return nil, err
	}
	receipts := map[string]bool{}
	heads := map[string]CNFact{}
	stagedHash := []string{}
	var pending, quarantined int64
	published := map[string]bool{}
	var publications []CNFinancePublication
	var lastFactSequence int64
	if err = tx.Where("fact_id IS NOT NULL").Select("sequence", "fact_id").Find(&publications).Error; err != nil {
		return nil, err
	}
	for _, p := range publications {
		published[*p.FactID] = true
		lastFactSequence = max(lastFactSequence, p.Sequence)
	}
	for _, row := range outbox {
		stagedHash = append(stagedHash, row.FactID+":"+row.PayloadHash+":"+row.State)
		if !published[row.FactID] {
			pending++
		}
		if row.State == "quarantined" {
			quarantined++
			continue
		}
		var f CNFact
		if err = common.UnmarshalJsonStr(row.Payload, &f); err != nil {
			return nil, err
		}
		for _, e := range f.SourceEvidence {
			if published[f.FactID] {
				receipts[e.RecordType+":"+e.RecordID] = true
			}
		}
		if f.Kind == "charge" && published[f.FactID] {
			key := CNIdentity(f.Kind, f.EconomicID, f.SegmentID)
			if previous, ok := heads[key]; !ok || previous.Revision < f.Revision {
				heads[key] = f
			}
		}
	}
	deps := []CNDependency{}
	scans := []cnProofScan{}
	wallet := cnProofScan{dependency: CNDependency{SourceID: "ordinary_wallet_durable_session", Version: "durable-session-v1", Required: true, CoverageFrom: stream.StartedAt, SupportedPaths: []string{"billing-session/reserve/finalize/cancel", "image-reserve-rollback", "trusted-zero-preconsume"}, ExcludedPaths: []string{"pre-epoch", "redis", "batch", "legacy-direct-wallet-mutations"}}}
	if !CNDurableWalletAvailable() {
		wallet.dependency.UnknownCount++
	}
	var wallets []CNWalletLifecycle
	if err = tx.Where("created_at >= ?", stream.StartedAt).Order("request_id").Find(&wallets).Error; err != nil {
		return nil, err
	}
	for _, row := range wallets {
		wallet.records = append(wallet.records, []any{row.RequestID, row.State, strconv.FormatInt(row.Version, 10), strconv.FormatInt(row.Reserved, 10), strconv.FormatInt(row.Actual, 10), row.Currency, row.QuotaPerUnit, row.ExchangeRate, row.ConversionVersion})
		if cnQuotaMoney(row.Actual, map[string]any{"quotaPerUnit": row.QuotaPerUnit, "exchangeRate": row.ExchangeRate, "settlementCurrency": row.Currency, "originalCurrency": "USD"}) == nil {
			wallet.dependency.UnknownCount++
		}
		if row.State == "reserved" {
			wallet.dependency.PendingCount++
			continue
		}
		if !receipts["cn_wallet_lifecycle_final:"+row.RequestID] {
			wallet.dependency.PendingCount++
		}
	}
	scans = append(scans, wallet)
	var cancellations []CNWalletCancellation
	if err = tx.Order("request_id").Find(&cancellations).Error; err != nil {
		return nil, err
	}
	for _, intent := range cancellations {
		scans[len(scans)-1].records = append(scans[len(scans)-1].records, []any{intent.RequestID, intent.State, intent.Attempts, intent.LastErrorCode})
		if intent.State != "complete" {
			scans[len(scans)-1].dependency.PendingCount++
		}
		if intent.LastErrorCode != "" {
			scans[len(scans)-1].dependency.UnknownCount++
		}
	}
	agency := cnProofScan{dependency: CNDependency{SourceID: "agency_wallet", Version: "native-final-journal-v1", Required: true, CoverageFrom: stream.StartedAt, SupportedPaths: []string{"agency-durable-final-journal"}, ExcludedPaths: []string{"pre-epoch", "non-journal-agency"}}}
	if tx.Migrator().HasTable(&AgencyBillingOutbox{}) {
		var rows []AgencyBillingOutbox
		if err = tx.Where("created_at_ms >= ? AND event_kind IN ?", started.UnixMilli(), opsBillingKinds).Order("event_id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			agency.records = append(agency.records, []any{r.EventID, r.PayloadHash})
			if !receipts["agency_billing_outbox:"+r.EventID] {
				agency.dependency.UnknownCount++
			}
		}
		if tx.Migrator().HasTable(&AgencyBillingJournal{}) {
			if err = tx.Model(&AgencyBillingJournal{}).Where("created_at_ms >= ? AND status IN ?", started.UnixMilli(), []string{"reserved", "submitted", "reconcile_required"}).Count(&agency.dependency.PendingCount).Error; err != nil {
				return nil, err
			}
		}
	} else {
		agency.dependency.UnknownCount++
	}
	scans = append(scans, agency)
	tasks := cnProofScan{dependency: CNDependency{SourceID: "image_video_tasks", Version: "task-funding-cas-v1", Required: true, CoverageFrom: stream.StartedAt, SupportedPaths: []string{"ApplyTaskQuotaAdjustment", "SettleTaskBillingReconciliation", "durable-session-task-request-link"}, ExcludedPaths: []string{"pre-epoch", "legacy-direct-task-writes", "midjourney-legacy"}}}
	if tx.Migrator().HasTable(&Task{}) {
		var rows []Task
		if err = tx.Select("id", "task_id", "status", "quota", "private_data", "created_at").Where("created_at >= ?", started.Unix()).Order("id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			request := ""
			if r.PrivateData.Execution != nil {
				request = r.PrivateData.Execution.RequestID
			}
			tasks.records = append(tasks.records, []any{strconv.FormatInt(r.ID, 10), r.TaskID, r.Status, strconv.Itoa(r.Quota), request})
			if r.Status != TaskStatusSuccess && r.Status != TaskStatusFailure {
				tasks.dependency.PendingCount++
				continue
			}
			economic := "task:" + r.TaskID
			if request != "" {
				economic = "request:" + request
			}
			f, ok := heads[CNIdentity("charge", economic, "0")]
			if !ok || f.Cumulative.Quota == nil || *f.Cumulative.Quota != strconv.Itoa(r.Quota) {
				tasks.dependency.UnknownCount++
			}
		}
	} else {
		tasks.dependency.UnknownCount++
	}
	scans = append(scans, tasks)
	payments := cnProofScan{dependency: CNDependency{SourceID: "payments_topups", Version: "verified-snapshot-v1", Required: true, CoverageFrom: stream.StartedAt, SupportedPaths: []string{"topup.PaymentSnapshot", "AgencyTopupFact.ActualMoney"}, ExcludedPaths: []string{"pre-epoch", "manual-order-without-provider-receipt"}}}
	if tx.Migrator().HasTable(&TopUp{}) {
		var rows []TopUp
		if err = tx.Select("trade_no", "status", "payment_snapshot", "complete_time").Where("complete_time >= ?", started.Unix()).Order("trade_no").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			payments.records = append(payments.records, []any{r.TradeNo, r.Status, CNIdentity(r.PaymentSnapshot)})
			if r.Status != common.TopUpStatusSuccess {
				payments.dependency.PendingCount++
				continue
			}
			var snapshot TopupPaymentSnapshot
			if r.PaymentSnapshot == "" || common.UnmarshalJsonStr(r.PaymentSnapshot, &snapshot) != nil || snapshot.ActualMoney == "" {
				payments.dependency.UnknownCount++
			} else if !receipts["topup:"+r.TradeNo] {
				payments.dependency.PendingCount++
			}
		}
	} else {
		payments.dependency.UnknownCount++
	}
	if tx.Migrator().HasTable(&AgencyTopupFact{}) {
		var rows []AgencyTopupFact
		if err = tx.Where("occurred_at_ms >= ?", started.UnixMilli()).Order("source_operation_id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			payments.records = append(payments.records, []any{r.SourceOperationID, r.ActualMoney, r.CurrencyCode, r.PaymentStatus})
			if r.PaymentStatus != "success" || r.ActualMoney == "" || r.PaymentReference == "" {
				payments.dependency.UnknownCount++
			} else if !receipts["agency_topup_fact:"+r.SourceOperationID] {
				payments.dependency.PendingCount++
			}
		}
	}
	scans = append(scans, payments)
	commission := cnProofScan{dependency: CNDependency{SourceID: "agency_commissions", Version: "committed-ledger-micros-v1", Required: true, CoverageFrom: stream.StartedAt, SupportedPaths: []string{"AgencyCommissionLedger.AmountMicros"}, ExcludedPaths: []string{"commissionTaxAndWithdrawable-estimate", "supplier-cost-coefficients"}}}
	if tx.Migrator().HasTable(&AgencyCommissionLedger{}) {
		var rows []AgencyCommissionLedger
		if err = tx.Where("occurred_at_ms >= ?", started.UnixMilli()).Order("id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			identity := CNIdentity("agency-commission", r.EventID, r.ComponentID, strconv.FormatInt(r.AgencyID, 10), r.EntryType, strconv.Itoa(r.HierarchyDepth))
			commission.records = append(commission.records, []any{identity, strconv.FormatInt(r.AmountMicros, 10), r.CurrencyCode})
			if !receipts["agency_commission_ledger:"+identity] {
				commission.dependency.PendingCount++
			}
		}
	} else {
		commission.dependency.UnknownCount++
	}
	scans = append(scans, commission)
	localComplete := true
	for _, scan := range scans {
		d := scan.dependency
		d.CoverageFrom = coverageFrom
		d.CoverageTo = &cutoffText
		encoded, err := common.Marshal(scan.records)
		if err != nil {
			return nil, err
		}
		proof := CNIdentity(d.SourceID, d.Version, stream.Epoch, stream.StartedAt, cutoffText, string(encoded))
		d.SourceCutoff = &proof
		d.ProofID = &proof
		d.Status = "complete"
		if d.UnknownCount > 0 {
			d.Status = "gap"
		}
		if d.PendingCount > 0 {
			d.Status = "pending"
		}
		if !cutoff.After(started) {
			d.Status = "gap"
			d.ExcludedPaths = append(d.ExcludedPaths, "coverage_window_not_advanced")
		}
		if d.Status == "complete" {
			d.CapturedThrough = &proof
			sequence := strconv.FormatInt(lastFactSequence, 10)
			d.PublishedThrough = &sequence
		} else {
			localComplete = false
		}
		deps = append(deps, d)
	}
	var legacy, sourceGaps int64
	if err = tx.Model(&CNFinanceIntent{}).Where("state <> ?", "captured").Count(&legacy).Error; err != nil {
		return nil, err
	}
	if err = tx.Model(&CNFinanceGap{}).Where("resolved = ?", false).Count(&sourceGaps).Error; err != nil {
		return nil, err
	}
	gaps := []struct {
		id               string
		pending, unknown int64
		paths            []string
	}{
		{"ordinary_wallet_legacy_batch", legacy, 1, []string{"legacy-direct-wallet", "redis", "batch-and-inflight"}},
		{"subscriptions_and_credit_schedules", 0, 1, []string{"ordinary-subscription", "agency-subscription", "approved-recognition-schedules", "credit-sale"}},
		{"payments_refunds_compensations", 0, 1, []string{"cash-refund-provider-receipts", "consumed-compensations", "safe-reservations"}},
		{"funding_lots_allocations", 0, 1, []string{"pre-epoch-native-allocation-history", "unattributed-bonus-and-debt", "ordinary-native-allocation-not-present"}},
		{"confirmed_supplier_bills_allocations", 0, 1, []string{"ops-owned-confirmed-supplier-bills"}},
		{"payment_fees", 0, 1, []string{"ops-owned-approved-provider-fees"}},
		{"business_taxes_other_expenses", 0, 1, []string{"ops-owned-paid-tax-and-expense-documents"}},
		{"historical_and_uninstrumented_paths", pending, sourceGaps + quarantined, []string{"before-stream-coverage", "unsupported-direct-mutations"}},
	}
	for _, gap := range gaps {
		proof := CNIdentity(gap.id, strconv.FormatInt(gap.pending, 10), strconv.FormatInt(gap.unknown, 10))
		d := CNDependency{SourceID: gap.id, Version: "scope-gap-v1", Required: true, CoverageFrom: coverageFrom, CoverageTo: &cutoffText, SourceCutoff: &proof, ProofID: &proof, PendingCount: gap.pending, UnknownCount: gap.unknown, SupportedPaths: []string{}, ExcludedPaths: gap.paths, Status: "gap"}
		if gap.pending > 0 {
			d.Status = "pending"
		}
		deps = append(deps, d)
	}
	allComplete := true
	for _, d := range deps {
		if d.Required && d.Status != "complete" && d.Status != "not_applicable" {
			allComplete = false
		}
	}
	return common.Marshal(map[string]any{"dependencies": deps, "backfillComplete": allComplete && pending == 0 && sourceGaps == 0 && quarantined == 0, "sourceCoverageComplete": allComplete, "forwardGatewayScopeComplete": localComplete && sourceGaps == 0 && quarantined == 0, "unresolvedPredecessorCount": pending - quarantined, "quarantinedCount": quarantined, "captureEnabled": CNFinanceCaptureEnabled(), "stagedDigest": CNIdentity(stagedHash...)})
}
