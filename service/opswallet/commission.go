package opswallet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CommissionEntry contains only signed station evidence. Money is converted
// exactly to USD micros; Gateway estimates are never a commission basis.
type CommissionEntry struct {
	EntryID          json.Number `json:"entryId"`
	UserID           string      `json:"userId"`
	Kind             string      `json:"kind"`
	RequestID        string      `json:"requestId"`
	RefundID         string      `json:"refundId"`
	FundingRequestID string      `json:"fundingRequestId"`
	Amount           string      `json:"amount"`
	BalanceAfter     string      `json:"balanceAfter"`
	StandardAmount   *string     `json:"standardAmount"`
	Model            string      `json:"model"`
	Currency         string      `json:"currency"`
	Version          string      `json:"commissionFactsVersion"`
	PostedAt         string      `json:"postedAt"`
}

type CommissionProjector struct {
	DB     *gorm.DB
	Client *opsclient.Client
}

// CheckSource validates identity, transport and the source page without posting
// accounting entries. An unresolved historical commission cannot stop HTTP.
func (p *CommissionProjector) CheckSource(ctx context.Context) error {
	return p.step(ctx, false)
}

// Step consumes at most one bounded page on the existing sequential worker.
// The durable cursor is bound to the service identity and source scope.
func (p *CommissionProjector) Step(ctx context.Context) error {
	return p.step(ctx, true)
}

func (p *CommissionProjector) step(ctx context.Context, apply bool) error {
	if !opsauthority.Enabled() || p.DB == nil || p.Client == nil {
		return errors.New("OPS commission source unavailable")
	}
	source := fmt.Sprintf("%x", sha256.Sum256([]byte(p.Client.BaseURL+"\x00"+p.Client.KeyID+"\x00"+opsauthority.Scope())))
	id := model.OPSAgencyID("cursor", "commission")
	var cursor model.OPSAgencyCursor
	err := p.DB.WithContext(ctx).First(&cursor, "id = ?", id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil && cursor.Source != source {
		return errors.New("OPS commission source changed; review required")
	}
	raw, err := p.Client.Get(ctx, "/api/v1/catalog/v1/balances?since="+strconv.FormatInt(cursor.EntryID, 10), time.Now())
	if err != nil {
		return err
	}
	var envelope struct {
		Data *struct {
			Currency   string            `json:"currency"`
			Version    string            `json:"commissionFactsVersion"`
			Items      []CommissionEntry `json:"items"`
			NextCursor *string           `json:"nextCursor"`
			HasMore    *bool             `json:"hasMore"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &envelope) != nil || envelope.Data == nil || envelope.Data.Currency != "USD" || envelope.Data.Version != "1" || envelope.Data.Items == nil || envelope.Data.HasMore == nil || len(envelope.Data.Items) > 500 {
		return errors.New("invalid OPS commission page")
	}
	page := envelope.Data
	last := cursor.EntryID
	for _, entry := range page.Items {
		n, e := entry.EntryID.Int64()
		if e != nil || n <= last {
			return errors.New("OPS commission page order invalid")
		}
		last = n
	}
	next := strconv.FormatInt(cursor.EntryID, 10)
	if page.NextCursor != nil {
		next = *page.NextCursor
	}
	if next != strconv.FormatInt(last, 10) || len(page.Items) == 0 && *page.HasMore {
		return errors.New("OPS commission cursor invalid")
	}
	if !apply {
		return nil
	}
	err = p.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := model.OPSAgencyCursor{ID: id, Source: source}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		var current model.OPSAgencyCursor
		if err := model.AgencyLockForUpdate(tx).First(&current, "id = ?", id).Error; err != nil {
			return err
		}
		if current.Source != source {
			return errors.New("OPS commission source conflict")
		}
		// Another instance may have committed this entire page while HTTP was in flight.
		if current.EntryID != cursor.EntryID {
			return nil
		}
		for _, entry := range page.Items {
			if err := retainCommissionEntry(tx, entry); err != nil {
				return err
			}
		}
		return tx.Model(&current).Update("entry_id", last).Error
	})
	if err != nil {
		return err
	}
	return p.drainCommissionInbox(ctx, id)
}

func commissionMicros(raw string) (int64, error) {
	if raw == "" || len(raw) > 32 || strings.ContainsAny(raw, "eE+") {
		return 0, errors.New("invalid commission amount")
	}
	d, err := decimal.NewFromString(raw)
	if err != nil || !d.Equal(d.Truncate(6)) || d.Abs().GreaterThan(decimal.NewFromInt(common.MaxWalletQuota*2).Div(decimal.NewFromInt(1_000_000))) {
		return 0, errors.New("invalid commission amount")
	}
	return d.Mul(decimal.NewFromInt(1_000_000)).IntPart(), nil
}

// ApplyCommissionEntryTx must run under the source cursor transaction lock.
// Account continuity, immutable receipt, commission and cursor commit together.
func ApplyCommissionEntryTx(tx *gorm.DB, entry CommissionEntry) error {
	if !opsauthority.Enabled() || tx == nil || len(entry.RequestID) > 80 || len(entry.RefundID) > 255 || len(entry.FundingRequestID) > 80 || len(entry.Model) > 200 {
		return errors.New("invalid OPS commission entry")
	}
	n, err := entry.EntryID.Int64()
	if err != nil || n <= 0 || entry.Currency != "USD" || entry.Version != "1" {
		return errors.New("OPS commission evidence unsupported")
	}
	user, err := strconv.Atoi(entry.UserID)
	if err != nil || user <= 0 {
		return errors.New("OPS commission user invalid")
	}
	amount, err := commissionMicros(entry.Amount)
	if err != nil {
		return err
	}
	after, err := commissionMicros(entry.BalanceAfter)
	if err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339Nano, entry.PostedAt)
	if err != nil {
		return errors.New("OPS commission timestamp invalid")
	}
	raw, err := common.Marshal(entry)
	if err != nil {
		return err
	}
	receipt := model.OPSAgencyReceipt{ID: model.OPSAgencyID("receipt", entry.EntryID.String()), PayloadHash: fmt.Sprintf("%x", sha256.Sum256(raw)), Payload: model.AgencyOpsPayload(raw)}
	var prior model.OPSAgencyReceipt
	err = tx.First(&prior, "id = ?", receipt.ID).Error
	if err == nil {
		if prior.PayloadHash != receipt.PayloadHash || prior.Payload != receipt.Payload {
			return errors.New("OPS commission receipt changed")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	id := model.OPSAgencyID("account", entry.UserID)
	var account model.OPSAgencyAccount
	err = model.AgencyLockForUpdate(tx).First(&account, "id = ?", id).Error
	fresh := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !fresh {
		return err
	}
	lots := []model.OPSAgencyLot{}
	if fresh {
		account = model.OPSAgencyAccount{ID: id, UserID: user}
	} else if common.Unmarshal([]byte(account.LotsJSON), &lots) != nil {
		return errors.New("OPS commission lots invalid")
	}
	if account.UserID != user {
		return errors.New("OPS commission account mismatch")
	}
	if entry.Kind != "opening" && account.BalanceMicros+amount != after {
		return errors.New("OPS commission balance discontinuity")
	}
	switch entry.Kind {
	case "opening":
		if !fresh || amount != after {
			return errors.New("OPS commission opening repeated or inconsistent")
		}
		var opening model.OPSAgencyOpening
		if err := tx.First(&opening, "id = ?", id).Error; err != nil {
			return fmt.Errorf("OPS commission reviewed opening unavailable: %w", err)
		}
		if opening.UserID != user || opening.BalanceMicros != after || common.Unmarshal([]byte(opening.LotsJSON), &lots) != nil {
			return errors.New("OPS commission opening mismatch")
		}
		account.DebtMicros = opening.DebtMicros
	case "credit":
		if amount > 0 {
			additions, err := commissionFundingLots(tx, entry, user, amount)
			if err != nil {
				return err
			}
			for _, lot := range additions {
				repay := min(account.DebtMicros, lot.Amount)
				account.DebtMicros -= repay
				lot.Amount -= repay
				if lot.Amount > 0 {
					lots = append(lots, lot)
				}
			}
		} else if amount < 0 {
			_ = consumeCommissionLots(&lots, &account.DebtMicros, -amount, false)
		}
	case "charge":
		if amount > 0 || entry.RequestID == "" {
			return errors.New("OPS commission debit invalid")
		}
		var hold model.OPSWalletHold
		if err := tx.Where("account_id = ? AND charge_request_id = ? AND scope = ?", commissionAdmissionID(user), entry.RequestID, opsauthority.Scope()).First(&hold).Error; err != nil {
			// Rejected relay attempts can have an authoritative zero ledger
			// entry without ever reserving funds. Retain its receipt and verify
			// balance continuity, but there is no debit or commission to project.
			if amount == 0 && errors.Is(err, gorm.ErrRecordNotFound) {
				break
			}
			return fmt.Errorf("OPS commission frozen request missing: %w", err)
		}
		if hold.UserID != user || hold.Model != entry.Model || hold.AgencyPricing == "" || hold.State != "settled" && hold.State != "reconciled" {
			return errors.New("OPS commission frozen identity invalid")
		}
		var snapshot *agencycontract.PricingSnapshot
		if common.Unmarshal([]byte(hold.AgencyPricing), &snapshot) != nil {
			return errors.New("OPS commission snapshot invalid")
		}
		if snapshot != nil && (snapshot.UserID != int64(user) || snapshot.OriginModelName != entry.Model) {
			return errors.New("OPS commission snapshot identity mismatch")
		}
		paidFirst := snapshot != nil && snapshot.FundingRuleVersion == agencycontract.FundingRuleVersionV1
		allocated := consumeCommissionLots(&lots, &account.DebtMicros, -amount, paidFirst)
		event, err := commissionCharge(entry, snapshot, allocated, at.UnixMilli())
		if err != nil {
			return err
		}
		allocationJSON, err := common.Marshal(allocated)
		if err != nil {
			return err
		}
		eventJSON, err := common.Marshal(event)
		if err != nil {
			return err
		}
		charge := model.OPSAgencyCharge{ID: model.OPSAgencyID("charge", entry.RequestID), UserID: user, AmountMicros: -amount, AllocationJSON: model.AgencyOpsPayload(allocationJSON), EventJSON: model.AgencyOpsPayload(eventJSON)}
		if err := tx.Create(&charge).Error; err != nil {
			return err
		}
		if err := agencyhub.ProjectOPSCommissionTx(tx, event); err != nil {
			return err
		}
	case "refund":
		if amount < 0 || entry.RequestID == "" || entry.RefundID == "" {
			return errors.New("OPS commission refund invalid")
		}
		var duplicateRefund int64
		if err := tx.Model(&model.OPSAgencyReceipt{}).Where("id = ?", model.OPSAgencyID("refund-receipt", entry.RefundID)).Count(&duplicateRefund).Error; err != nil {
			return err
		}
		if duplicateRefund != 0 {
			return errors.New("OPS refund identity reused with another ledger entry")
		}
		refundReceipt := receipt
		refundReceipt.ID = model.OPSAgencyID("refund-receipt", entry.RefundID)
		if err := tx.Create(&refundReceipt).Error; err != nil {
			return err
		}
		var charge model.OPSAgencyCharge
		if err := model.AgencyLockForUpdate(tx).First(&charge, "id = ?", model.OPSAgencyID("charge", entry.RequestID)).Error; err != nil {
			return err
		}
		if charge.UserID != user || charge.AmountMicros <= 0 || amount > charge.AmountMicros-charge.RefundedMicros {
			return errors.New("OPS commission refund exceeds charge")
		}
		var allocated []model.OPSAgencyLot
		var event agencycontract.BillingEvent
		if common.Unmarshal([]byte(charge.AllocationJSON), &allocated) != nil || common.Unmarshal([]byte(charge.EventJSON), &event) != nil {
			return errors.New("OPS commission original evidence invalid")
		}
		// Cumulative allocation makes partial-refund rounding conserve every micro.
		cumulative := charge.RefundedMicros + amount
		beforeSum, afterSum := int64(0), int64(0)
		for _, lot := range allocated {
			beforeSum += lot.Amount
			beforeTarget := decimal.NewFromInt(beforeSum).Mul(decimal.NewFromInt(charge.RefundedMicros)).Div(decimal.NewFromInt(charge.AmountMicros)).Truncate(0).IntPart()
			afterTarget := decimal.NewFromInt(beforeSum).Mul(decimal.NewFromInt(cumulative)).Div(decimal.NewFromInt(charge.AmountMicros)).Truncate(0).IntPart()
			restored := afterTarget - beforeTarget - afterSum
			afterSum = afterTarget - beforeTarget
			repay := min(account.DebtMicros, restored)
			account.DebtMicros -= repay
			restored -= repay
			if restored > 0 {
				lot.Amount = restored
				lots = append(lots, lot)
			}
		}
		originalID := event.EventID
		event.EventID = model.OPSAgencyID("refund", entry.RefundID)
		event.OriginalEventID = originalID
		event.EventType = "agency.billing_reversed"
		event.OccurredAtMS = at.UnixMilli()
		event.CommissionAmountMicros, event.CommissionQuota, event.ReversedCommissionAmountMicros = 0, 0, 0
		for i := range event.CommissionSplits {
			split := &event.CommissionSplits[i]
			total := decimal.NewFromInt(split.CommissionAmountMicros)
			previous := total.Mul(decimal.NewFromInt(charge.RefundedMicros)).Div(decimal.NewFromInt(charge.AmountMicros)).Truncate(0).IntPart()
			target := total.Mul(decimal.NewFromInt(cumulative)).Div(decimal.NewFromInt(charge.AmountMicros)).Truncate(0).IntPart()
			split.ReversedCommissionAmountMicros = target - previous
			split.CommissionQuota = target - previous
			split.CommissionAmountMicros = 0
			event.ReversedCommissionAmountMicros += split.ReversedCommissionAmountMicros
			event.CommissionQuota += split.CommissionQuota
		}
		if err := agencyhub.ProjectOPSCommissionTx(tx, event); err != nil {
			return err
		}
		if err := tx.Model(&charge).Update("refunded_micros", cumulative).Error; err != nil {
			return err
		}
	default:
		return errors.New("OPS commission entry kind invalid")
	}
	// Merge returned lots; do not grow state on repeated partial refunds.
	merged := make([]model.OPSAgencyLot, 0, len(lots))
	positions := map[string]int{}
	total := int64(0)
	for _, lot := range lots {
		if lot.Amount == 0 {
			continue
		}
		if lot.Amount < 0 || lot.Amount > common.MaxWalletQuota*4-total {
			return errors.New("OPS commission lots overflow")
		}
		total += lot.Amount
		if pos, ok := positions[lot.ID]; ok {
			if merged[pos].Source != lot.Source || merged[pos].Paid != lot.Paid || merged[pos].PaidBalance != lot.PaidBalance {
				return errors.New("OPS commission lot identity conflict")
			}
			merged[pos].Amount += lot.Amount
		} else {
			positions[lot.ID] = len(merged)
			merged = append(merged, lot)
		}
	}
	if len(merged) > 4000 || account.DebtMicros < 0 || total-account.DebtMicros != after {
		return errors.New("OPS commission funds do not reconcile")
	}
	encoded, err := common.Marshal(merged)
	if err != nil || len(encoded) > 1024*1024 {
		return errors.New("OPS commission state exceeds bound")
	}
	account.BalanceMicros = after
	account.LotsJSON = model.AgencyOpsPayload(encoded)
	if err := tx.Save(&account).Error; err != nil {
		return err
	}
	return tx.Create(&receipt).Error
}

func commissionAdmissionID(user int) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", opsauthority.Scope(), user))))
}

func consumeCommissionLots(lots *[]model.OPSAgencyLot, debt *int64, amount int64, paidFirst bool) []model.OPSAgencyLot {
	model.SortOPSAgencyLots(*lots, paidFirst)
	result := []model.OPSAgencyLot{}
	// Source ordering and the frozen funding rule decide which funds are spent.
	for i := range *lots {
		lot := &(*lots)[i]
		take := min(amount, lot.Amount)
		if take > 0 {
			part := *lot
			part.Amount = take
			result = append(result, part)
			lot.Amount -= take
			amount -= take
		}
	}
	if amount > 0 {
		*debt += amount
		result = append(result, model.OPSAgencyLot{ID: "debt", Source: "debt", Amount: amount})
	}
	return result
}

func commissionCharge(entry CommissionEntry, snapshot *agencycontract.PricingSnapshot, lots []model.OPSAgencyLot, at int64) (agencycontract.BillingEvent, error) {
	actual, err := commissionMicros(entry.Amount)
	if err != nil {
		return agencycontract.BillingEvent{}, err
	}
	actual = -actual
	user, _ := strconv.ParseInt(entry.UserID, 10, 64)
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: model.OPSAgencyID("earned", entry.RequestID), EventType: "agency.billing_finalized", FinancialChargeID: entry.RequestID, UserID: user, OriginModelName: entry.Model, CurrencyCode: "USD", QuotaPerUnit: "1000000", ExchangeRate: "1", ChargedTotalQuota: actual, OccurredAtMS: at, FinancialFinal: true}
	if snapshot == nil {
		return event, nil
	}
	if snapshot.CurrencyCode != "USD" || snapshot.ExchangeRate != "1" || entry.StandardAmount == nil {
		return event, errors.New("OPS commission standard price evidence unavailable")
	}
	standard, err := commissionMicros(*entry.StandardAmount)
	if err != nil || standard < 0 {
		return event, errors.New("OPS commission standard invalid")
	}
	paid := int64(0)
	for _, lot := range lots {
		if lot.Paid {
			paid += lot.Amount
		} else if lot.Source == "debt" {
			event.DebtAllocatedQuota += lot.Amount
		} else {
			event.NonpaidAllocatedQuota += lot.Amount
		}
	}
	event.AgencyID = &snapshot.AgencyID
	event.BindingID = &snapshot.BindingID
	event.StandardQuota = standard
	event.PaidAllocatedQuota = paid
	event.CommissionEligible = snapshot.CommissionEligible
	event.BusinessStatus, event.BillingStatus = "success", "finalized"
	event.SalesBPS, event.SettlementBPS = snapshot.SalesBPS, snapshot.SettlementBPS
	event.CommissionSkipReason = snapshot.EligibilityReason
	nodes := make([]agencycontract.TierNode, len(snapshot.Hierarchy))
	for i, node := range snapshot.Hierarchy {
		nodes[i] = agencycontract.TierNode{AgencyID: node.AgencyID, CostBPS: node.CostBPS, MinSpreadBPS: node.MinSpreadBPS, SalesBPS: node.SalesBPS}
	}
	if len(nodes) == 0 {
		sales := snapshot.SalesBPS
		nodes = []agencycontract.TierNode{{AgencyID: snapshot.AgencyID, CostBPS: snapshot.SettlementBPS, SalesBPS: &sales}}
	}
	if snapshot.CustomerSalesOverrideBPS != nil {
		sales := *snapshot.CustomerSalesOverrideBPS
		nodes[len(nodes)-1].SalesBPS = &sales
		if sales == nodes[len(nodes)-1].CostBPS {
			// The quote validator explicitly permits a customer at-cost override.
			// Do not reapply the usual minimum margin to this accepted exception.
			for i := range nodes {
				nodes[i].MinSpreadBPS = 0
			}
		}
	}
	spread := snapshot.MinSpreadBPS
	if snapshot.CustomerSalesOverrideBPS != nil && *snapshot.CustomerSalesOverrideBPS == nodes[len(nodes)-1].CostBPS {
		spread = 0
	}
	result, err := agencycontract.CalculateTieredCommission(standard, nodes, snapshot.PlatformCostBPS, spread, min(paid, standard), true)
	if err != nil {
		return event, err
	}
	if result.CustomerChargedQuota != actual {
		// Non-paid funds never earn commission. Preserve both the OPS debit
		// and the original quote for review while allowing its balance sequence
		// to advance. Real payment or debt still requires an explicit correction.
		if paid == 0 && event.DebtAllocatedQuota == 0 {
			basis, err := common.Marshal(map[string]any{
				"source": "ops_debit_and_frozen_quote", "review": "nonpaid_price_difference",
				"actual_micros": actual, "quoted_micros": result.CustomerChargedQuota,
				"standard_micros": standard, "frozen_quote": snapshot,
			})
			if err != nil {
				return event, err
			}
			event.BillingBasis = string(basis)
			event.CommissionEligible = false
			event.NoncommissionableQuota = actual
			event.CommissionSkipReason = "nonpaid_price_difference_review"
			return event, nil
		}
		return event, errors.New("OPS actual debit differs from frozen agency sales; review required")
	}
	event.SettlementCostQuota = result.PlatformCostQuota + result.PlatformMarginQuota
	event.CommissionableQuota = actual
	event.TheoreticalCommissionQuota = result.TotalCommissionQuota
	// CalculateTieredCommission bounds paid against standard; coefficients can
	// exceed 1, so recompute eligibility against actual customer payment.
	for i, segment := range result.Segments {
		earned, err := agencycontract.CommissionForPaid(segment.TheoreticalQuota, paid, actual, true)
		if err != nil {
			return event, err
		}
		if !snapshot.CommissionEligible {
			earned = 0
		}
		split := agencycontract.CommissionSplit{AgencyID: segment.AgencyID, Depth: i + 1, CostBPS: nodes[i].CostBPS, SalesBPS: nodes[i].SalesBPS, TheoreticalQuota: segment.TheoreticalQuota, PaidAllocatedQuota: earned, CommissionQuota: earned, CommissionAmountMicros: earned}
		if i < len(snapshot.Hierarchy) {
			split.ParentAgencyID = snapshot.Hierarchy[i].ParentAgencyID
			if snapshot.Hierarchy[i].Depth > 0 {
				split.Depth = snapshot.Hierarchy[i].Depth
			}
		}
		event.CommissionSplits = append(event.CommissionSplits, split)
		event.CommissionQuota += earned
		event.CommissionAmountMicros += earned
	}
	if !snapshot.CommissionEligible {
		event.NoncommissionableQuota, event.CommissionableQuota = actual, 0
	}
	return event, nil
}

// Unknown credits remain spendable non-paid funds. A label alone is never
// sufficient: verified money must match the immutable local completed order.
func commissionFundingLots(tx *gorm.DB, entry CommissionEntry, user int, amount int64) ([]model.OPSAgencyLot, error) {
	sequence, _ := entry.EntryID.Int64()
	lot := model.OPSAgencyLot{ID: entry.EntryID.String(), Source: "admin_adjustment", Amount: amount, Sequence: sequence}
	if !strings.HasPrefix(entry.FundingRequestID, "wf-") {
		return []model.OPSAgencyLot{lot}, nil
	}
	var row model.OPSWalletEvent
	if err := tx.Where("event_id = ?", entry.FundingRequestID).First(&row).Error; err != nil {
		return nil, err
	}
	if err := model.ValidateOPSUSDFunding(row); err != nil {
		return nil, err
	}
	var body struct {
		Events []struct {
			RequestID string `json:"requestId"`
			UserID    string `json:"userId"`
			Amount    string `json:"amount"`
			Currency  string `json:"currency"`
			Extra     struct {
				Kind     string                    `json:"recordKind"`
				Source   string                    `json:"fundingSource"`
				SourceID string                    `json:"fundingSourceId"`
				Topup    *model.OPSUSDFundingFacts `json:"topup"`
			} `json:"extra"`
		} `json:"events"`
	}
	if common.Unmarshal([]byte(row.Payload), &body) != nil || len(body.Events) != 1 {
		return nil, errors.New("OPS commission funding invalid")
	}
	e := body.Events[0]
	credited, err := commissionMicros(e.Amount)
	if err != nil || credited != amount || e.Currency != "USD" || e.UserID != entry.UserID || e.RequestID != entry.FundingRequestID {
		return nil, errors.New("OPS commission funding identity mismatch")
	}
	lot.Source = e.Extra.Source
	if e.Extra.Kind != "topup" || e.Extra.Topup == nil || e.Extra.Source == "admin" {
		return []model.OPSAgencyLot{lot}, nil
	}
	var order model.TopUp
	if err := tx.Where("trade_no = ? AND user_id = ?", e.Extra.SourceID, user).First(&order).Error; err != nil {
		return nil, err
	}
	var proof model.TopupPaymentSnapshot
	var conversion model.TopupQuotaConversion
	if order.Status != common.TopUpStatusSuccess || common.Unmarshal([]byte(order.PaymentSnapshot), &proof) != nil || common.Unmarshal([]byte(order.QuotaConversionSnapshot), &conversion) != nil || conversion.CreditedQuota == "" || proof.PaymentReference == "" || proof.CurrencyCode != "USD" || proof.ActualMoney != e.Extra.Topup.PaidAmount || e.Extra.Topup.PaidCurrency != "USD" || e.Extra.Topup.TradeNo != order.TradeNo {
		return []model.OPSAgencyLot{lot}, nil
	}
	quota, err := strconv.ParseInt(conversion.CreditedQuota, 10, 64)
	if err != nil || quota < 0 || quota > common.MaxWalletQuota || quota*2 != amount {
		return nil, errors.New("OPS commission order conversion mismatch")
	}
	paid, err := commissionMicros(proof.ActualMoney)
	if err != nil || paid <= 0 {
		return []model.OPSAgencyLot{lot}, nil
	}
	paid = min(paid, amount)
	paidLot := lot
	paidLot.ID += "-paid"
	paidLot.Paid = true
	paidLot.PaidBalance = true
	paidLot.Amount = paid
	lot.Amount -= paid
	lot.ID += "-bonus"
	return []model.OPSAgencyLot{lot, paidLot}, nil
}
