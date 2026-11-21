package opswallet

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func commissionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "local-commission")
	t.Setenv("OPS_BILLING_CURRENCY", "USD")
	t.Setenv("OPS_PUSH_CURRENCY", "USD")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	db := migrationTestDB(t)
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	mainDB, logDB := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseType(db.Dialector.Name()), common.DatabaseType(db.Dialector.Name()))
	t.Cleanup(func() { common.SetDatabaseTypes(mainDB, logDB) })
	require.NoError(t, model.MigrateAgency(db))
	require.NoError(t, db.AutoMigrate(&model.TopUp{}, &model.Token{}, &model.OPSWalletEvent{}, &model.OPSWalletHold{}, &model.OPSWalletAdmission{}))
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.OPSAgencyOpening{}, &model.OPSAgencyAccount{}, &model.OPSAgencyCursor{}, &model.OPSAgencyReceipt{}, &model.OPSAgencyCharge{}))
	}
	return db
}

func commissionEntry(id int, kind, amount, after string) CommissionEntry {
	return CommissionEntry{EntryID: json.Number(strconv.Itoa(id)), UserID: "1", Kind: kind, Amount: amount, BalanceAfter: after, Currency: "USD", Version: "1", PostedAt: "2026-11-20T01:00:00Z"}
}

func applyCommission(t *testing.T, db *gorm.DB, e CommissionEntry) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return ApplyCommissionEntryTx(tx, e) }))
}

func seedCommissionHold(t *testing.T, db *gorm.DB, request string, user int, snapshot *agencycontract.PricingSnapshot) {
	t.Helper()
	raw, err := common.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.OPSWalletHold{ID: model.OPSAgencyID("test-hold", request), Scope: "local-commission", AccountID: commissionAdmissionID(user), UserID: user, RequestID: request, ChargeRequestID: request, Model: "local-model", State: "reconciled", AgencyPricing: string(raw)}).Error)
}

func TestOPSCommissionActualDebitFundingRefundAndWithdrawal(t *testing.T) {
	db := commissionTestDB(t)
	user := model.User{Id: 1, Username: "local-only", AffCode: "local-only", Quota: 123, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, model.FreezeOPSSourceWallet(db))
	// A completed USD checkout grants 2.5, but only 2 is verified real payment.
	proof := `{"actual_money":"2","currency_code":"USD","payment_reference":"synthetic-provider-receipt"}`
	conversion := `{"schema_version":1,"credited_quota":"1250000"}`
	order := model.TopUp{UserId: 1, TradeNo: "synthetic-checkout", Status: common.TopUpStatusSuccess, PaymentSnapshot: proof, QuotaConversionSnapshot: conversion}
	require.NoError(t, db.Create(&order).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.QueueOPSUSDFundingTx(tx, 1, 1250000, "topup", "stripe", order.TradeNo, &model.OPSUSDFundingFacts{TradeNo: order.TradeNo, PaidAmount: "2", PaidCurrency: "USD"})
	}))
	var funding model.OPSWalletEvent
	require.NoError(t, db.First(&funding).Error)
	credit := commissionEntry(1, "credit", "2.5", "2.5")
	credit.FundingRequestID = funding.EventID
	applyCommission(t, db, credit)
	applyCommission(t, db, credit)
	sales := 10000
	snapshot := &agencycontract.PricingSnapshot{UserID: 1, AgencyID: 7, BindingID: 8, OriginModelName: "local-model", CurrencyCode: "USD", ExchangeRate: "1", CommissionEligible: true, FundingRuleVersion: agencycontract.FundingRuleVersionV2, SalesBPS: sales, SettlementBPS: 6000, Hierarchy: []agencycontract.PricingTierNode{{AgencyID: 7, CostBPS: 6000, SalesBPS: &sales}}}
	seedCommissionHold(t, db, "call-one", 1, snapshot)
	standard := "1"
	charge := commissionEntry(2, "charge", "-1", "1.5")
	charge.RequestID = "call-one"
	charge.Model = "local-model"
	charge.StandardAmount = &standard
	applyCommission(t, db, charge)
	applyCommission(t, db, charge)
	var balance model.AgencyCommissionBalance
	require.NoError(t, db.Where("agency_id = ?", 7).First(&balance).Error)
	assert.Equal(t, int64(200000), balance.AvailableMicros, "gift portion must not earn commission")
	var stored model.OPSAgencyCharge
	require.NoError(t, db.First(&stored).Error)
	var event agencycontract.BillingEvent
	require.NoError(t, common.Unmarshal([]byte(stored.EventJSON), &event))
	assert.Equal(t, int64(500000), event.PaidAllocatedQuota)
	assert.Equal(t, int64(500000), event.NonpaidAllocatedQuota)
	require.NoError(t, agencycontract.ValidateCommissionSplits(event))
	assert.Equal(t, int64(600000), event.SettlementCostQuota)
	assert.Equal(t, int64(400000), event.TheoreticalCommissionQuota)
	assert.Equal(t, int64(200000), event.CommissionQuota)
	var usage model.AgencyUsageFact
	require.NoError(t, db.First(&usage).Error)
	assert.Equal(t, int64(500000), usage.ChargedQuota)
	assert.Equal(t, int64(250000), usage.PaidQuota)
	assert.Equal(t, int64(250000), usage.NonpaidQuota)
	var stat model.AgencyDailyStat
	require.NoError(t, db.First(&stat).Error)
	assert.Equal(t, int64(500000), stat.ChargedQuota)
	// A pending payout becomes held when its underlying commission is reversed.
	withdrawal := model.AgencyWithdrawal{AgencyID: 7, CurrencyCode: "USD", Status: "approved", Version: 1}
	require.NoError(t, db.Create(&withdrawal).Error)
	refund := commissionEntry(3, "refund", "0.333333", "1.833333")
	refund.RequestID = "call-one"
	refund.RefundID = "refund-one"
	applyCommission(t, db, refund)
	applyCommission(t, db, refund)
	require.NoError(t, db.First(&withdrawal, withdrawal.ID).Error)
	assert.Equal(t, "on_hold", withdrawal.Status)
	refund = commissionEntry(4, "refund", "0.666667", "2.5")
	refund.RequestID = "call-one"
	refund.RefundID = "refund-two"
	applyCommission(t, db, refund)
	applyCommission(t, db, refund)
	require.NoError(t, db.First(&balance, balance.ID).Error)
	assert.Zero(t, balance.AvailableMicros)
	assert.Equal(t, int64(200000), balance.ReversedMicros)
	var account model.OPSAgencyAccount
	require.NoError(t, db.First(&account).Error)
	assert.Equal(t, int64(2500000), account.BalanceMicros)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 123, user.Quota, "commission projection cannot touch historical wallet")
	var count int64
	require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&count).Error)
	assert.Equal(t, int64(3), count)
	duplicateRefund := refund
	duplicateRefund.EntryID = "5"
	duplicateRefund.Amount, duplicateRefund.BalanceAfter = "0", "2.5"
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error { return ApplyCommissionEntryTx(tx, duplicateRefund) }), "identity reused")
	// Altered replay, excess refund and a debit-price mismatch roll back entirely.
	altered := credit
	altered.Amount = "9"
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error { return ApplyCommissionEntryTx(tx, altered) }), "receipt changed")
	refund.EntryID = "5"
	refund.RefundID = "excess"
	refund.Amount = "0.000001"
	refund.BalanceAfter = "2.500001"
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error { return ApplyCommissionEntryTx(tx, refund) }), "exceeds charge")
	seedCommissionHold(t, db, "mismatch", 1, snapshot)
	bad := charge
	bad.EntryID = "5"
	bad.RequestID = "mismatch"
	bad.Amount = "-0.9"
	bad.BalanceAfter = "1.6"
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error { return ApplyCommissionEntryTx(tx, bad) }), "actual debit differs")
	require.NoError(t, db.First(&account).Error)
	assert.Equal(t, int64(2500000), account.BalanceMicros)
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.OPSAgencyAccount{}, &model.OPSAgencyCharge{}, &model.OPSAgencyReceipt{}))
	}
	require.NoError(t, db.First(&balance, balance.ID).Error)
	assert.Zero(t, balance.AvailableMicros)
}

func TestOPSCommissionOpeningUnknownDebtAndFrozenBinding(t *testing.T) {
	db := commissionTestDB(t)
	user := model.User{Id: 1, Username: "opening-user", AffCode: "opening-user", Quota: -500000, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, model.SaveOPSAgencyOpening(db, "local-commission", 1, -500000, 1000000, nil))
	opening := commissionEntry(1, "opening", "-1", "-1")
	applyCommission(t, db, opening)
	applyCommission(t, db, commissionEntry(2, "credit", "3", "2"))
	// Unknown remote adjustments are non-paid even if sufficient for a call.
	seedCommissionHold(t, db, "unbound", 1, nil)
	charge := commissionEntry(3, "charge", "-2.000001", "-0.000001")
	charge.RequestID = "unbound"
	charge.Model = "local-model"
	applyCommission(t, db, charge)
	refund := commissionEntry(4, "refund", "2.000001", "2")
	refund.RequestID = "unbound"
	refund.RefundID = "debt-refund"
	applyCommission(t, db, refund)
	var account model.OPSAgencyAccount
	require.NoError(t, db.First(&account).Error)
	assert.Zero(t, account.DebtMicros)
	assert.Equal(t, int64(2000000), account.BalanceMicros)
	var count int64
	require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, model.FreezeOPSSourceWallet(db))
	active := model.AgencyActiveUserBinding{UserID: 1, AgencyID: 7, BindingID: 8, Revision: 1}
	require.NoError(t, db.Create(&active).Error)
	snapshot := &agencycontract.PricingSnapshot{UserID: 1, TokenID: 1, AgencyID: 7, BindingID: 8, BindingRevision: 1, OriginModelName: "local-model"}
	identity := model.OPSWalletHoldIdentity{RequestID: "freeze", UserID: 1, TokenID: 1, Playground: true, Model: "local-model", AgencyPricing: snapshot}
	position := model.OPSWalletPosition{UserID: 1, EntryID: 4, Quota: 1000000}
	hold, err := model.ReserveOPSWallet(db, identity, 100, position)
	require.NoError(t, err)
	snapshot.SalesBPS = 12345
	replay, err := model.ReserveOPSWallet(db, identity, 100, position)
	require.NoError(t, err)
	assert.Equal(t, hold.AgencyPricing, replay.AgencyPricing)
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error { return model.CheckOPSAgencyBindingTx(tx, 1) }), "in-flight OPS calls")
	job := model.AgencyProvisioningJob{UserID: 1, Status: "queued"}
	require.NoError(t, db.Create(&job).Error)
	identity.RequestID = "blocked"
	_, err = model.ReserveOPSWallet(db, identity, 1, position)
	require.ErrorIs(t, err, model.ErrAgencyProvisioning)
	require.NoError(t, db.Model(&job).Update("status", "cancelled").Error)
	identity.RequestID = "missing-quote"
	identity.AgencyPricing = nil
	_, err = model.ReserveOPSWallet(db, identity, 1, position)
	require.ErrorIs(t, err, model.ErrOPSWalletHoldConflict)
	require.NoError(t, db.First(&user, 1).Error)
	assert.Equal(t, -500000, user.Quota)
	// The additive pricing column preserves released records; repeat migration.
	require.NoError(t, db.Migrator().DropColumn(&model.OPSWalletHold{}, "agency_pricing"))
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.OPSWalletHold{}))
	}
	require.NoError(t, db.First(&replay, "id = ?", hold.ID).Error)
	assert.Equal(t, hold.HeldQuota, replay.HeldQuota)
}

func TestOPSCommissionCursorRestartAndPageRollback(t *testing.T) {
	db := commissionTestDB(t)
	entry := commissionEntry(1, "credit", "1", "1")
	zero := commissionEntry(2, "charge", "0", "1")
	zero.RequestID, zero.Model = "rejected-attempt", "local-model"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/api/v1/catalog/v1/balances", r.URL.Path)
		since := r.URL.Query().Get("since")
		items := []CommissionEntry{}
		next := since
		if since == "0" {
			items = append(items, entry, zero)
			next = "2"
		}
		data := map[string]any{"data": map[string]any{"currency": "USD", "commissionFactsVersion": "1", "items": items, "hasMore": false, "nextCursor": next}}
		raw, err := common.Marshal(data)
		require.NoError(t, err)
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client := opsclient.New(server.URL, "synthetic-key", "synthetic-secret-only", time.Second)
	p := CommissionProjector{DB: db, Client: client}
	require.NoError(t, p.CheckSource(t.Context()))
	var cursorCount int64
	require.NoError(t, db.Model(&model.OPSAgencyCursor{}).Count(&cursorCount).Error)
	assert.Zero(t, cursorCount, "readiness cannot post commission or advance a cursor")
	require.NoError(t, p.Step(t.Context()))
	p = CommissionProjector{DB: db.Session(&gorm.Session{NewDB: true}), Client: client}
	require.NoError(t, p.Step(t.Context()))
	var cursor model.OPSAgencyCursor
	require.NoError(t, db.First(&cursor).Error)
	assert.Equal(t, int64(2), cursor.EntryID)
	assert.Equal(t, 3, calls)
	var zeroReceipt model.OPSAgencyReceipt
	require.NoError(t, db.First(&zeroReceipt, "id = ?", model.OPSAgencyID("receipt", "2")).Error)
	var projected int64
	require.NoError(t, db.Model(&model.OPSAgencyCharge{}).Count(&projected).Error)
	assert.Zero(t, projected, "an unexecuted zero attempt cannot create commission")
	changed := zero
	changed.Model = "different-model"
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error { return ApplyCommissionEntryTx(tx, changed) }), "receipt changed")
	changed.EntryID, changed.BalanceAfter = "3", "2"
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error { return ApplyCommissionEntryTx(tx, changed) }), "balance discontinuity")
	// A committed first entry followed by invalid evidence must roll back page.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		good := commissionEntry(3, "credit", "1", "2")
		bad := commissionEntry(4, "charge", "-1", "1")
		bad.RequestID = "missing-hold"
		raw, _ := common.Marshal(map[string]any{"data": map[string]any{"currency": "USD", "commissionFactsVersion": "1", "items": []CommissionEntry{good, bad}, "hasMore": false, "nextCursor": "4"}})
		_, _ = w.Write(raw)
	})
	require.NoError(t, p.CheckSource(t.Context()), "valid source with unresolved accounting must remain available")
	require.Error(t, p.Step(t.Context()))
	require.NoError(t, db.First(&cursor).Error)
	assert.Equal(t, int64(2), cursor.EntryID)
	var account model.OPSAgencyAccount
	require.NoError(t, db.First(&account).Error)
	assert.Equal(t, int64(1000000), account.BalanceMicros)
	client.KeyID = "changed-station"
	require.ErrorContains(t, p.Step(t.Context()), "source changed")
	t.Setenv("OPS_DISTRIBUTOR_NON_WALLET_ONLY", "true")
	t.Setenv("OPS_DISTRIBUTOR_FINANCE_AUTHORITY", "gateway")
	for _, kind := range []string{"commissions", "balances", "withdrawals", "source_payout"} {
		assert.True(t, model.AgencyOpsFactAllowed(model.AgencyOpsPayload(fmt.Sprintf(`{"kind":%q}`, kind))))
	}
	for _, kind := range []string{"wallets", "funding", "topups", "fundingLots", "future_kind"} {
		assert.False(t, model.AgencyOpsFactAllowed(model.AgencyOpsPayload(fmt.Sprintf(`{"kind":%q}`, kind))))
	}
}

func TestOPSCommissionTieredPricesAndUnknownFunds(t *testing.T) {
	sales := 15000
	snapshot := &agencycontract.PricingSnapshot{UserID: 1, AgencyID: 9, BindingID: 8, OriginModelName: "local-model", CurrencyCode: "USD", ExchangeRate: "1", CommissionEligible: true, SalesBPS: sales, MinSpreadBPS: 500, PlatformCostBPS: 4000, Hierarchy: []agencycontract.PricingTierNode{{AgencyID: 7, CostBPS: 6000, MinSpreadBPS: 500}, {AgencyID: 9, CostBPS: 8000, MinSpreadBPS: 500, SalesBPS: &sales}}}
	standard := "1"
	entry := commissionEntry(1, "charge", "-1.5", "0")
	entry.RequestID = "tiers"
	entry.Model = "local-model"
	entry.StandardAmount = &standard
	event, err := commissionCharge(entry, snapshot, []model.OPSAgencyLot{{Paid: true, Amount: 1500000}}, time.Now().UnixMilli())
	require.NoError(t, err)
	require.Len(t, event.CommissionSplits, 2)
	require.NoError(t, agencycontract.ValidateCommissionSplits(event))
	assert.Equal(t, 6000, event.CommissionSplits[0].CostBPS)
	assert.Equal(t, &sales, event.CommissionSplits[1].SalesBPS)
	assert.Equal(t, int64(200000), event.CommissionSplits[0].CommissionAmountMicros)
	assert.Equal(t, int64(700000), event.CommissionSplits[1].CommissionAmountMicros)
	event, err = commissionCharge(entry, snapshot, []model.OPSAgencyLot{{Source: "legacy_unknown", Amount: 1500000}}, time.Now().UnixMilli())
	require.NoError(t, err)
	assert.Zero(t, event.CommissionAmountMicros)
	override := 8000
	snapshot.CustomerSalesOverrideBPS = &override
	entry.Amount = "-0.8"
	event, err = commissionCharge(entry, snapshot, []model.OPSAgencyLot{{Paid: true, Amount: 800000}}, time.Now().UnixMilli())
	require.NoError(t, err)
	assert.Equal(t, int64(200000), event.CommissionAmountMicros)
	assert.Zero(t, event.CommissionSplits[1].CommissionAmountMicros)
	for _, raw := range []string{"1e99", "NaN", "0.0000001", "99999999999999999999"} {
		_, err := commissionMicros(raw)
		require.Error(t, err)
	}
}

func TestOPSCommissionReviewedOpeningKeepsPaidBonusAndUnknownSeparate(t *testing.T) {
	db := commissionTestDB(t)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: 1, PaidAvailable: 1000000, NonpaidAvailable: 250000, Version: 1}).Error)
	lot := model.AgencyFundingLot{UserID: 1, SourceKind: "stripe", SourceID: "paid-opening", PaidInitial: 1000000, PaidAvailable: 1000000, BonusInitial: 250000, BonusAvailable: 250000, MoneySeq: 1, SourceSnapshotJSON: `{"actual_money":"2","currency_code":"USD","payment_reference":"synthetic-opening"}`}
	require.NoError(t, db.Create(&lot).Error)
	fact := model.AgencyTopupFact{SourceOperationID: lot.SourceID, UserID: 1, ActualMoney: "2", CurrencyCode: "USD", PaymentReference: "synthetic-opening", PaymentStatus: "success", CreditedQuota: 1250000, PaidQuota: 1000000, BonusQuota: 250000, QuotaConversionSnapshot: `{"credited_quota":"1250000"}`}
	require.NoError(t, db.Create(&fact).Error)
	lots, debt, err := model.OPSAgencyOpeningLots(db, 1, 1250000)
	require.NoError(t, err)
	assert.Zero(t, debt)
	require.Len(t, lots, 2)
	assert.False(t, lots[0].Paid, "v2 spends the bonus first")
	assert.Equal(t, int64(500000), lots[0].Amount)
	assert.True(t, lots[1].Paid)
	model.SortOPSAgencyLots(lots, true)
	assert.True(t, lots[0].Paid, "v1 preserves paid-wallet-first ordering")
	// An unbounded numeric proof is never accepted as verified payment.
	require.NoError(t, db.Model(&lot).Update("source_snapshot_json", `{"actual_money":"1e999999999","currency_code":"USD","payment_reference":"synthetic-opening"}`).Error)
	lots, _, err = model.OPSAgencyOpeningLots(db, 1, 1250000)
	require.NoError(t, err)
	for _, part := range lots {
		assert.False(t, part.Paid)
	}
	model.SortOPSAgencyLots(lots, true)
	assert.True(t, lots[0].PaidBalance, "unverified paid-wallet funds retain order but cannot earn commission")
	// A no-commission charge may be fully refunded without requiring a missing earnings row.
	require.NoError(t, model.SaveOPSAgencyOpening(db, "local-commission", 1, 1250000, 0, lots))
	applyCommission(t, db, commissionEntry(1, "opening", "2.5", "2.5"))
	sales := 10000
	snapshot := &agencycontract.PricingSnapshot{UserID: 1, AgencyID: 7, BindingID: 8, OriginModelName: "local-model", CurrencyCode: "USD", ExchangeRate: "1", CommissionEligible: true, FundingRuleVersion: agencycontract.FundingRuleVersionV1, SalesBPS: sales, SettlementBPS: 6000}
	seedCommissionHold(t, db, "unknown-opening-call", 1, snapshot)
	standard := "1"
	charge := commissionEntry(2, "charge", "-1", "1.5")
	charge.RequestID, charge.Model, charge.StandardAmount = "unknown-opening-call", "local-model", &standard
	applyCommission(t, db, charge)
	refund := commissionEntry(3, "refund", "1", "2.5")
	refund.RequestID, refund.RefundID = charge.RequestID, "unknown-opening-refund"
	applyCommission(t, db, refund)
	var count int64
	require.NoError(t, db.Model(&model.AgencyCommissionLedger{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestOPSCommissionProvisioningAndTransferPreserveFrozenWallet(t *testing.T) {
	db := commissionTestDB(t)
	t.Setenv("AGENCY_ONBOARDING_ENABLED", "true")
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	app := agencyhub.New(db, db, agencyhub.Config{SalesCapBPS: 30000, MinSpreadBPS: 500})
	policy := agencycontract.Policy{DefaultSettlementBPS: 6000, DefaultSalesBPS: 10000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	source, _, err := app.CreateAgency(1, "Local Source", "local-source", policy)
	require.NoError(t, err)
	target, _, err := app.CreateAgency(1, "Local Target", "local-target", policy)
	require.NoError(t, err)
	user := model.User{Id: 1, Username: "frozen-provisioning", AffCode: "frozen-provisioning", Quota: 42, BillingMode: "legacy", AuthVersion: 1, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, model.FreezeOPSSourceWallet(db))
	position := model.OPSWalletPosition{UserID: 1, EntryID: 1, Quota: 1000000}
	identity := model.OPSWalletHoldIdentity{RequestID: "before-binding", UserID: 1, Model: "local-model", Playground: true}
	_, err = model.ReserveOPSWallet(db, identity, 1, position)
	require.NoError(t, err)
	job, created, err := app.EnqueueProvisioningJob(1, source.InviteCode, 1, "local binding verification")
	require.NoError(t, err)
	require.True(t, created)
	_, err = app.ProcessProvisioningJobs(1)
	require.NoError(t, err)
	require.NoError(t, db.First(&job, job.ID).Error)
	assert.Equal(t, "blocked", job.Status)
	assert.Equal(t, "in_flight_ops_calls", job.BlockReason)
	require.NoError(t, app.CancelProvisioningJob(job.ID, "cancel isolated test", 1))
	require.NoError(t, model.FinalizeOPSWallet(db, identity.RequestID, 1, 0, true))
	retry, created, err := app.EnqueueProvisioningJob(1, source.InviteCode, 1, "retry isolated binding")
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, job.ID, retry.ID)
	_, err = app.ProcessProvisioningJobs(1)
	require.NoError(t, err)
	require.NoError(t, db.First(&retry, retry.ID).Error)
	assert.Equal(t, "completed", retry.Status)
	var active model.AgencyActiveUserBinding
	require.NoError(t, db.First(&active, "user_id = ?", 1).Error)
	quote := &agencycontract.PricingSnapshot{UserID: 1, AgencyID: active.AgencyID, BindingID: active.BindingID, BindingRevision: active.Revision, OriginModelName: "local-model"}
	identity.RequestID, identity.AgencyPricing = "before-transfer", quote
	hold, err := model.ReserveOPSWallet(db, identity, 1, position)
	require.NoError(t, err)
	transferred, err := agencyhub.ApplyOpsCustomerTransfer(db, 1, target.ID, active.Revision, "local-transfer-one")
	require.NoError(t, err)
	assert.Equal(t, target.ID, transferred.AgencyID)
	_, err = agencyhub.ApplyOpsCustomerTransfer(db, 1, target.ID, active.Revision, "local-transfer-one")
	require.NoError(t, err, "same command replays its original result")
	_, err = agencyhub.ApplyOpsCustomerTransfer(db, 1, source.ID, active.Revision, "local-transfer-stale")
	require.ErrorContains(t, err, "revision conflict")
	var preserved model.OPSWalletHold
	require.NoError(t, db.First(&preserved, "id = ?", hold.ID).Error)
	assert.Equal(t, hold.AgencyPricing, preserved.AgencyPricing)
	newUser := model.User{Id: 2, Username: "new-bound-customer", AffCode: "new-bound-customer", Quota: 0, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&newUser).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := agencyhub.BindUserByInvite(tx, 2, source.InviteCode, "register", 0)
		return err
	}))
	require.NoError(t, db.First(&user, 1).Error)
	assert.Equal(t, 42, user.Quota)
	assert.Equal(t, "legacy", user.BillingMode)
	var accounts int64
	require.NoError(t, db.Model(&model.AgencyFundingAccount{}).Count(&accounts).Error)
	assert.Zero(t, accounts, "OPS custody must not create a competing local funding wallet")
}
