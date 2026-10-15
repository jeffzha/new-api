package model

import (
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func CNFinanceCaptureEnabled() bool { return os.Getenv("OPS_CN_FINANCE_V2_ENABLED") == "true" }

func BindCNChargeRequest(charge, request string, user int) error {
	if !CNFinanceCaptureEnabled() {
		return nil
	}
	if charge == "" || request == "" || len(charge) > 191 || len(request) > 191 {
		return errors.New("finance association missing")
	}
	row := CNFinanceAssociation{ChargeID: charge, RequestID: request, CustomerKey: strconv.Itoa(user)}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	var existing CNFinanceAssociation
	if err := DB.Where("charge_id = ?", charge).First(&existing).Error; err != nil {
		return err
	}
	if row != existing {
		return ErrCNConflict
	}
	return nil
}

func cnBaseFact(kind, economic, segment, operation, source string, user int64, at time.Time, evidence CNEvidence) CNFact {
	customer := strconv.FormatInt(user, 10)
	timestamp := at.UTC().Format(CNTimeLayout)
	return CNFact{FactID: CNIdentity(kind, evidence.RecordType, evidence.RecordID), EconomicID: economic, SegmentID: segment, Kind: kind, OperationID: operation, PostingRole: kind, Revision: 1, Basis: "gateway_actual", BillingSource: source, CustomerKey: &customer, OccurredAt: timestamp, EffectiveAt: timestamp, SourcePeriod: timestamp[:7], Status: "confirmed", Quality: "legacy_unknown", SourceEvidence: []CNEvidence{evidence}, Links: CNLinks()}
}

func cnUnknownTaxMoney(amount, currency string) *CNMoney {
	return &CNMoney{Currency: currency, OriginalCurrency: currency, OriginalAmount: amount, Gross: amount, TaxTreatment: "unknown"}
}

// BeginCNLegacyIntent precedes the existing non-atomic finalization. It does not
// authorize replay of a wallet mutation. Interrupted intents require reconciliation.
func BeginCNLegacyIntent(request string, user int, source string, pre, actual int) (string, error) {
	if !CNFinanceCaptureEnabled() {
		return "", nil
	}
	if request == "" {
		return "", errors.New("finance request identity missing")
	}
	payload, err := common.Marshal(map[string]any{"requestId": request, "customerKey": strconv.Itoa(user), "billingSource": source, "preConsumedQuota": strconv.Itoa(pre), "actualQuota": strconv.Itoa(actual), "batchUpdateEnabled": common.BatchUpdateEnabled})
	if err != nil {
		return "", err
	}
	key := CNIdentity("legacy-final", request, source, strconv.Itoa(actual))
	row := CNFinanceIntent{Key: key, Payload: string(payload), State: "pending_mutation", CreatedAt: time.Now().Unix()}
	if err = DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return "", err
	}
	var existing CNFinanceIntent
	if err = DB.Where(map[string]any{"key": key}).First(&existing).Error; err != nil {
		return "", err
	}
	if existing.Payload != row.Payload {
		return "", ErrCNConflict
	}
	return key, nil
}

func FinishCNLegacyIntent(key string, committed bool) error {
	if key == "" {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var row CNFinanceIntent
		if err := lockForUpdate(tx).Where(map[string]any{"key": key}).First(&row).Error; err != nil {
			return err
		}
		if row.State == "captured" {
			return nil
		}
		state := "mutation_failed"
		if committed {
			state = "acknowledged_unverified"
		}
		var body struct {
			RequestID     string `json:"requestId"`
			CustomerKey   string `json:"customerKey"`
			BillingSource string `json:"billingSource"`
			ActualQuota   string `json:"actualQuota"`
			Batch         bool   `json:"batchUpdateEnabled"`
		}
		if err := common.UnmarshalJsonStr(row.Payload, &body); err != nil {
			return err
		}
		// Acknowledgment outside the money transaction is an intent receipt only.
		// It cannot establish crash-safe final debit evidence, with or without batch.
		return tx.Model(&CNFinanceIntent{}).Where(map[string]any{"key": key}).Update("state", state).Error
	})
}

func cnStageChargeSnapshot(tx *gorm.DB, f *CNFact, quota int64) error {
	chain := CNIdentity(f.Kind, f.EconomicID, f.SegmentID)
	var previous CNFinanceOutbox
	err := lockForUpdate(tx).Where("chain_key = ?", chain).Order("revision DESC").First(&previous).Error
	prior := int64(0)
	var priorMoney *CNMoney
	if err == nil {
		var p CNFact
		if err = common.UnmarshalJsonStr(previous.Payload, &p); err != nil {
			return err
		}
		if p.Cumulative.Quota == nil {
			return ErrCNConflict
		}
		prior, err = strconv.ParseInt(*p.Cumulative.Quota, 10, 64)
		if err != nil {
			return err
		}
		if quota == prior {
			return nil
		}
		f.Revision = previous.Revision + 1
		f.PreviousFactID = &previous.FactID
		f.EffectiveAt = p.EffectiveAt
		f.SourcePeriod = p.SourcePeriod
		priorMoney = p.Cumulative.Money
		if priorMoney != nil && f.Cumulative.Money == nil {
			f.Cumulative.Money = cnQuotaMoney(quota, p.Conversion)
			if f.Cumulative.Money == nil {
				return ErrCNConflict
			}
			f.Conversion = p.Conversion
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	cumulative, delta := strconv.FormatInt(quota, 10), strconv.FormatInt(quota-prior, 10)
	f.Cumulative.Quota = &cumulative
	f.Delta.Quota = &delta
	if f.Cumulative.Money != nil {
		current, err := cnDecimal(f.Cumulative.Money.Gross)
		if err != nil {
			return err
		}
		previous := decimal.Zero
		if priorMoney != nil {
			previous, err = cnDecimal(priorMoney.Gross)
			if err != nil {
				return err
			}
		}
		f.Delta.Money = cnUnknownTaxMoney(current.Sub(previous).String(), f.Cumulative.Money.Currency)
		f.Delta.Money.OriginalCurrency = f.Cumulative.Money.OriginalCurrency
		original, err := cnDecimal(f.Cumulative.Money.OriginalAmount)
		if err != nil {
			return err
		}
		priorOriginal := decimal.Zero
		if priorMoney != nil {
			priorOriginal, err = cnDecimal(priorMoney.OriginalAmount)
			if err != nil {
				return err
			}
		}
		f.Delta.Money.OriginalAmount = original.Sub(priorOriginal).String()
	}
	f.FactID = CNIdentity(f.FactID, strconv.FormatInt(f.Revision, 10))
	if f.BillingSource == "wallet" {
		for _, evidence := range f.SourceEvidence {
			if evidence.RecordType == "cn_wallet_lifecycle_final" || evidence.RecordType == "cn_task_financial_receipt" {
				if err := cnWalletValueChargeTx(tx, f, quota, prior); err != nil {
					return err
				}
				break
			}
		}
	}
	return StageCNFactTx(tx, *f)
}

// CaptureCNTaskChargeTx shares the task/funding transaction, including zero-delta
// settlement. Agency charges are exported from their native immutable journals.
func CaptureCNTaskChargeTx(tx *gorm.DB, task *Task, target int) error {
	if task != nil && task.CNBillingKey != "" {
		return ErrCNConflict
	}
	if !CNFinanceCaptureEnabled() || task == nil || task.ID <= 0 {
		return nil
	}
	var user User
	if err := tx.Select("id", "billing_mode").First(&user, task.UserId).Error; err != nil {
		return err
	}
	if user.BillingMode == AgencyDurableBillingMode {
		return nil
	}
	source := task.PrivateData.BillingSource
	if source != TaskBillingSourceWallet && source != TaskBillingSourceSubscription {
		source = "legacy_unknown"
	}
	if task.TaskID == "" {
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&CNFinanceGap{ID: CNIdentity("task-identity", strconv.FormatInt(task.ID, 10)), SourceID: "image_video_tasks", Reason: "stable_task_identity_missing"}).Error
	}
	identity := "task:" + task.TaskID
	var request *string
	if task.PrivateData.Execution != nil && task.PrivateData.Execution.RequestID != "" {
		request = &task.PrivateData.Execution.RequestID
		identity = "request:" + *request
	}
	taskID := task.TaskID
	if taskID == "" {
		taskID = strconv.FormatInt(task.ID, 10)
	}
	evidenceID := CNIdentity("task-settlement", identity, strconv.Itoa(target), strconv.FormatInt(task.UpdatedAt, 10))
	f := cnBaseFact("charge", identity, "0", evidenceID, source, int64(task.UserId), time.Now().UTC(), CNEvidence{System: "gateway-cn", RecordType: "task_funding_transaction", RecordID: task.TaskID, EvidenceID: evidenceID})
	f.RequestID = request
	f.TaskID = &taskID
	f.Status = "settled"
	f.Quality = "settled_quota"
	if _, err := cnLockPublisher(tx); err != nil {
		return err
	}
	return cnStageChargeSnapshot(tx, &f, int64(target))
}

// Source scanners use anti-joins, never an increasing source-row cursor: an old
// row that commits late must receive a new publication sequence.
func cnCaptureSources(tx *gorm.DB) error {
	if tx.Migrator().HasTable(&AgencyBillingOutbox{}) {
		var sources []AgencyBillingOutbox
		if err := tx.Where("event_kind IN ?", opsBillingKinds).Order("id").Find(&sources).Error; err != nil {
			return err
		}
		for _, source := range sources {
			id := CNIdentity("charge", "agency_billing_outbox", source.EventID)
			var count int64
			if err := tx.Model(&CNFinanceOutbox{}).Where("fact_id = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			if err := cnCaptureSource(tx, "agency_wallet", source.EventID, func(child *gorm.DB) error { return cnCaptureAgencyCharge(child, source) }); err != nil {
				return err
			}
		}
	}
	if tx.Migrator().HasTable(&TopUp{}) {
		var topups []TopUp
		if err := tx.Where("status = ?", common.TopUpStatusSuccess).Order("id").Find(&topups).Error; err != nil {
			return err
		}
		for _, topup := range topups {
			if err := cnCaptureSource(tx, "payments_topups", topup.TradeNo, func(child *gorm.DB) error { return cnCaptureTopup(child, topup) }); err != nil {
				return err
			}
		}
	}
	return cnCaptureNativeSources(tx)
}

func cnCaptureAgencyCharge(tx *gorm.DB, source AgencyBillingOutbox) error {
	legacy, err := opsChargeFact(tx, source)
	if err != nil {
		return err
	}
	var event agencycontract.BillingEvent
	if err = common.UnmarshalJsonStr(source.Payload, &event); err != nil {
		return err
	}
	occurred := time.UnixMilli(event.OccurredAtMS)
	var receipt CNFinanceReceipt
	err = tx.Where("event_id = ?", event.EventID).First(&receipt).Error
	if err == nil {
		occurred, err = time.Parse(CNTimeLayout, receipt.OccurredAt)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	} else if event.EventType == "agency.billing_corrected" || event.EventType == "agency.billing_reversed" {
		// Legacy reversals reuse usage time. Without a committed CN receipt,
		// their event day is unknown and must remain a visible capture gap.
		var correction AgencyBillingCorrection
		if !tx.Migrator().HasTable(&AgencyBillingCorrection{}) {
			return errors.New("financial_commit_receipt_missing")
		}
		if err = tx.Where("event_id = ?", event.EventID).First(&correction).Error; err != nil {
			return errors.New("financial_commit_receipt_missing")
		}
		if correction.CreatedAtMS <= 0 {
			return errors.New("financial_commit_receipt_missing")
		}
		occurred = time.UnixMilli(correction.CreatedAtMS)
	}
	evidence := CNEvidence{System: "gateway-cn", RecordType: "agency_billing_outbox", RecordID: source.EventID, EvidenceID: source.PayloadHash}
	f := cnBaseFact("charge", event.FinancialChargeID, strconv.Itoa(event.SegmentNo), event.OperationID, "agency_wallet", event.UserID, occurred, evidence)
	f.EffectiveAt = time.UnixMilli(event.OccurredAtMS).UTC().Format(CNTimeLayout)
	f.SourcePeriod = f.EffectiveAt[:7]
	f.FactID = CNIdentity("charge", "agency_billing_outbox", source.EventID)
	f.Revision = event.JournalRevision
	f.Status = "settled"
	f.Quality = "settled_quota"
	var association CNFinanceAssociation
	err = tx.Where("charge_id = ?", event.FinancialChargeID).First(&association).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&CNFinanceGap{ID: f.FactID, SourceID: "agency_wallet", Reason: "historical_request_or_task_association_missing"}).Error
	}
	if err != nil {
		return err
	}
	if association.CustomerKey != strconv.FormatInt(event.UserID, 10) {
		return ErrCNConflict
	}
	f.RequestID = &association.RequestID
	var operations []AgencyBillingOperation
	if err = tx.Where("charge_id = ? AND segment_no = ? AND revision < ?", event.FinancialChargeID, event.SegmentNo, event.JournalRevision).Order("revision").Find(&operations).Error; err != nil {
		return err
	}
	for _, op := range operations {
		var receipt agencycontract.BillingEvent
		if err = common.UnmarshalJsonStr(op.CommittedResult, &receipt); err != nil {
			return err
		}
		if receipt.EventType != "agency.billing_finalized" && receipt.EventType != "agency.billing_corrected" && receipt.EventType != "agency.billing_reversed" {
			continue
		}
		prev := CNIdentity("charge", "agency_billing_outbox", receipt.EventID)
		f.PreviousFactID = &prev
		if op.Revision == operations[0].Revision {
			f.EffectiveAt = time.UnixMilli(receipt.OccurredAtMS).UTC().Format(CNTimeLayout)
			f.SourcePeriod = f.EffectiveAt[:7]
		}
	}
	var total int64
	// v1 amount is not used as v2 arithmetic authority. Recompute the cumulative
	// quota from immutable operations, including cumulative reversals.
	for _, op := range append(operations, AgencyBillingOperation{CommittedResult: source.Payload}) {
		var receipt agencycontract.BillingEvent
		if err = common.UnmarshalJsonStr(op.CommittedResult, &receipt); err != nil {
			return err
		}
		switch receipt.EventType {
		case "agency.billing_finalized", "agency.billing_corrected":
			total = receipt.ChargedTotalQuota
		case "agency.billing_reversed":
			total -= receipt.ChargedTotalQuota
		}
	}
	q, dq := strconv.FormatInt(total, 10), strconv.FormatInt(legacy.QuotaDelta, 10)
	f.Cumulative = CNMetric{Quota: &q}
	f.Delta = CNMetric{Quota: &dq}
	if legacy.Amount != nil {
		f.Conversion = map[string]any{"sourceUnit": "quota", "quotaPerUnit": event.QuotaPerUnit, "originalCurrency": "USD", "settlementCurrency": event.CurrencyCode, "exchangeRate": event.ExchangeRate, "fxVersion": CNIdentity("agency-frozen", event.QuotaPerUnit, event.ExchangeRate, event.CurrencyCode), "rounding": "HALF_EVEN_18"}
		f.Cumulative.Money = cnQuotaMoney(total, f.Conversion)
		previous := cnQuotaMoney(total-legacy.QuotaDelta, f.Conversion)
		if f.Cumulative.Money == nil || previous == nil {
			return errors.New("invalid frozen conversion")
		}
		currentGross, _ := cnDecimal(f.Cumulative.Money.Gross)
		priorGross, _ := cnDecimal(previous.Gross)
		currentOriginal, _ := cnDecimal(f.Cumulative.Money.OriginalAmount)
		priorOriginal, _ := cnDecimal(previous.OriginalAmount)
		f.Delta.Money = cnUnknownTaxMoney(currentGross.Sub(priorGross).String(), event.CurrencyCode)
		f.Delta.Money.OriginalCurrency = "USD"
		f.Delta.Money.OriginalAmount = currentOriginal.Sub(priorOriginal).String()
	}
	if err := StageCNFactTx(tx, f); err != nil {
		return err
	}
	return tx.Model(&CNFinanceGap{}).Where("id = ?", f.FactID).Update("resolved", true).Error
}

func cnCaptureTopup(tx *gorm.DB, topup TopUp, nativeLotEvidence ...CNEvidence) error {
	if topup.TradeNo == "" {
		return errors.New("topup business identity missing")
	}
	var payment TopupPaymentSnapshot
	if topup.PaymentSnapshot != "" {
		if err := common.UnmarshalJsonStr(topup.PaymentSnapshot, &payment); err != nil {
			return err
		}
	}
	if payment.PaymentReference != "" && tx.Migrator().HasTable(&AgencyTopupFact{}) {
		var native int64
		if err := tx.Model(&AgencyTopupFact{}).Where("payment_reference = ?", payment.PaymentReference).Count(&native).Error; err != nil {
			return err
		}
		if native > 0 {
			return nil
		}
	}
	id := CNIdentity("payment", "topup", topup.TradeNo)
	var count int64
	if err := tx.Model(&CNFinanceOutbox{}).Where("fact_id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	evidence := CNEvidence{System: "gateway-cn", RecordType: "topup", RecordID: topup.TradeNo, EvidenceID: CNIdentity(topup.PaymentSnapshot, topup.QuotaConversionSnapshot, topup.Status)}
	f := cnBaseFact("payment", "topup:"+topup.TradeNo, "0", "topup:"+topup.TradeNo, "not_applicable", int64(topup.UserId), time.Unix(topup.CompleteTime, 0), evidence)
	f.FactID = id
	if topup.PaymentSnapshot != "" && common.UnmarshalJsonStr(topup.PaymentSnapshot, &payment) == nil {
		normalized, err := payment.normalized()
		if err != nil {
			return err
		}
		if normalized.ActualMoney != "" && normalized.CurrencyCode != "" {
			f.Quality = "verified_payment"
			f.Cumulative.Money = cnUnknownTaxMoney(normalized.ActualMoney, normalized.CurrencyCode)
			f.Delta = f.Cumulative
			f.Conversion = map[string]any{"sourceUnit": "currency", "quotaPerUnit": nil, "originalCurrency": normalized.CurrencyCode, "settlementCurrency": normalized.CurrencyCode, "exchangeRate": "1", "fxVersion": "identity-1", "rounding": "HALF_EVEN_18"}
		}
	}
	f.Links["paymentIds"] = []string{f.EconomicID}
	if err := StageCNFactTx(tx, f); err != nil {
		return err
	}
	quota, known := topup.settledQuota()
	if !known || quota <= 0 {
		return nil
	}
	q := strconv.FormatInt(quota, 10)
	lot := cnBaseFact("funding_lot", "topup-lot:"+topup.TradeNo, "0", f.OperationID, "wallet", int64(topup.UserId), time.Unix(topup.CompleteTime, 0), evidence)
	lot.Cumulative = CNMetric{Quota: &q, Money: f.Cumulative.Money}
	lot.Delta = lot.Cumulative
	lot.Quality = f.Quality
	lot.Conversion = f.Conversion
	kind := "legacy_unknown"
	var paymentID any
	if f.Cumulative.Money != nil {
		kind = "paid"
		paymentID = f.EconomicID
		if f.Cumulative.Money.Gross == "0" {
			kind = "bonus"
		}
	}
	lot.Funding = map[string]any{"lotId": lot.EconomicID, "paymentId": paymentID, "lotKind": kind, "purchasedQuota": q, "paidValue": f.Cumulative.Money, "purchasedAt": lot.EffectiveAt, "policyVersion": "gateway-topup-evidence-v1", "evidence": lot.SourceEvidence}
	lot.Links["paymentIds"] = []string{f.EconomicID}
	lot.Links["fundingLotIds"] = []string{lot.EconomicID}
	if len(nativeLotEvidence) > 0 {
		lot.SourceEvidence = append(lot.SourceEvidence, nativeLotEvidence...)
		lot.Funding["evidence"] = lot.SourceEvidence
		lot.Funding["policyVersion"] = cnWalletValuePolicy
	}
	// Purchase alone never recognizes income. Native future lots carry an
	// additional receipt; reader/backfill purchases cannot acquire it.
	return StageCNFactTx(tx, lot)
}

func CNRefundCapabilities(gateway string) map[string]any {
	return map[string]any{"gatewayId": gateway, "sourceSystem": CNFinanceSource, "status": "blocked", "reservationAvailable": false, "executionAvailable": false, "blockers": []string{"ordinary_wallet_batch_and_inflight_not_reserved", "agency_cash_value_hold_not_implemented", "provider_executor_not_configured"}, "manualVoucherEvidenceAvailable": false}
}

// A savepoint isolates malformed source evidence, including PostgreSQL's aborted
// statement state. Retry the source on every scan; retain a visible gap meanwhile.
func cnCaptureSource(tx *gorm.DB, source, identity string, capture func(*gorm.DB) error) error {
	id := CNIdentity("source-gap", source, identity)
	if err := tx.Transaction(capture); err != nil {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.Assignments(map[string]any{"resolved": false})}).Create(&CNFinanceGap{ID: id, SourceID: source, Reason: "source_capture_failed"}).Error
	}
	return tx.Model(&CNFinanceGap{}).Where("id = ?", id).Update("resolved", true).Error
}
