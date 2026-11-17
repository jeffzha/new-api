package model

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The publication order is independent of source IDs: an older source ID may
// commit after a newer one. All publishers serialize on this singleton row.
type OpsExportPublisher struct {
	ID         int64 `gorm:"primaryKey"`
	Generation int64
}

func (OpsExportPublisher) TableName() string { return "ops_export_publisher" }

type OpsExportFact struct {
	ID         int64  `gorm:"primaryKey"`
	SourceID   int64  `gorm:"not null;uniqueIndex:uidx_ops_export_source"`
	Day        string `gorm:"size:10;not null;index:idx_ops_export_day"`
	QuotaDelta int64
	Amount     *string `gorm:"size:128"`
	Currency   string  `gorm:"size:3"`
	Payload    string  `gorm:"type:text;not null"`
}

func (OpsExportFact) TableName() string { return "ops_export_facts" }

func MigrateOpsExport(db *gorm.DB) error {
	if err := db.AutoMigrate(&OpsExportPublisher{}, &OpsExportFact{}); err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&OpsExportPublisher{ID: 1}).Error
}

var opsBillingKinds = []string{"agency.billing_finalized", "agency.billing_corrected", "agency.billing_reversed"}
var opsMoneyCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
var ErrOpsExportCursor = errors.New("invalid Ops export cursor")

func opsUnpublished(db *gorm.DB) *gorm.DB {
	published := db.Model(&OpsExportFact{}).Select("source_id")
	return db.Model(&AgencyBillingOutbox{}).Where("event_kind IN ?", opsBillingKinds).Where("id NOT IN (?)", published)
}

// PublishOpsFacts only reads immutable agency receipts. It never creates agency
// tables or changes journals, funding, charges, payments, or supplier balances.
func PublishOpsFacts(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// An UPDATE acquires SQLite's writer lock before taking any read snapshot,
		// and holds the same exclusive row lock as FOR UPDATE on the other engines.
		locked := tx.Model(&OpsExportPublisher{}).Where("id = ?", 1).Update("generation", gorm.Expr("generation + ?", 1))
		if locked.Error != nil {
			return locked.Error
		}
		if locked.RowsAffected != 1 {
			return errors.New("Ops export publisher is unavailable")
		}
		var sources []AgencyBillingOutbox
		if err := opsUnpublished(tx).Order("id").Limit(256).Find(&sources).Error; err != nil {
			return err
		}
		for _, source := range sources {
			fact, err := opsChargeFact(tx, source)
			if err != nil {
				return err
			}
			if err := tx.Create(&fact).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func opsChargeFact(db *gorm.DB, source AgencyBillingOutbox) (OpsExportFact, error) {
	var event agencycontract.BillingEvent
	invalid := errors.New("Ops export billing evidence is incomplete or inconsistent")
	if len(source.Payload) > 1<<20 || common.UnmarshalJsonStr(source.Payload, &event) != nil ||
		event.EventID != source.EventID || event.EventType != source.EventKind || event.OperationID != source.OperationID ||
		event.EventID == "" || len(event.EventID) > 280 || event.OperationID == "" || len(event.OperationID) > 300 ||
		event.FinancialChargeID == "" || len(event.FinancialChargeID) > 270 ||
		event.JournalRevision < 1 || event.JournalRevision > int64(common.MaxWalletQuota) || event.OccurredAtMS <= 0 || event.SegmentNo < 0 {
		return OpsExportFact{}, invalid
	}
	digest, err := agencycontract.CanonicalHash(&event)
	if err != nil || digest != source.PayloadHash {
		return OpsExportFact{}, invalid
	}
	var operations []AgencyBillingOperation
	if err := db.Where("charge_id = ? AND segment_no = ? AND revision <= ?", event.FinancialChargeID, event.SegmentNo, event.JournalRevision).
		Order("revision").Limit(10001).Find(&operations).Error; err != nil {
		return OpsExportFact{}, err
	}
	if len(operations) > 10000 {
		return OpsExportFact{}, invalid
	}
	var total, previous, lastRevision int64
	finalized, matched := false, false
	for _, operation := range operations {
		var receipt agencycontract.BillingEvent
		if len(operation.CommittedResult) > 1<<20 || common.UnmarshalJsonStr(operation.CommittedResult, &receipt) != nil ||
			receipt.FinancialChargeID != event.FinancialChargeID || receipt.SegmentNo != event.SegmentNo ||
			receipt.JournalRevision != operation.Revision || operation.Revision <= lastRevision {
			return OpsExportFact{}, invalid
		}
		lastRevision = operation.Revision
		previous = total
		switch receipt.EventType {
		case "agency.billing_finalized", "agency.billing_corrected":
			if receipt.ChargedTotalQuota < 0 || receipt.ChargedTotalQuota > int64(common.MaxWalletQuota) {
				return OpsExportFact{}, invalid
			}
			total, finalized = receipt.ChargedTotalQuota, true
		case "agency.billing_reversed":
			if !finalized || receipt.ChargedTotalQuota < 0 || receipt.ChargedTotalQuota > total {
				return OpsExportFact{}, invalid
			}
			total -= receipt.ChargedTotalQuota
		}
		if receipt.JournalRevision == event.JournalRevision {
			receiptHash, hashErr := agencycontract.CanonicalHash(&receipt)
			matched = hashErr == nil && receiptHash == digest && operation.OperationID == event.OperationID
		}
	}
	if !finalized || !matched {
		return OpsExportFact{}, invalid
	}
	at := time.UnixMilli(event.OccurredAtMS).UTC()
	if at.Year() < 1 || at.Year() > 9999 {
		return OpsExportFact{}, invalid
	}
	fact := OpsExportFact{SourceID: source.ID, Day: at.Format("2006-01-02"), QuotaDelta: total - previous}
	payload := map[string]any{
		"eventId": "ops-charge-" + event.EventID, "kind": "charge", "occurredAt": at.Format(time.RFC3339Nano),
		"referenceId": event.FinancialChargeID + ":" + strconv.Itoa(event.SegmentNo), "revision": event.JournalRevision,
		"quotaDelta": fact.QuotaDelta, "evidenceId": event.EventID, "status": "settled", "quality": "settled_quota",
	}
	if opsMoneyCurrency.MatchString(event.CurrencyCode) && len(event.QuotaPerUnit) <= 64 && len(event.ExchangeRate) <= 64 {
		unit, unitErr := decimal.NewFromString(event.QuotaPerUnit)
		rate, rateErr := decimal.NewFromString(event.ExchangeRate)
		if unitErr == nil && rateErr == nil && unit.IsPositive() && rate.IsPositive() &&
			unit.Exponent() >= -18 && unit.Exponent() <= 18 && rate.Exponent() >= -18 && rate.Exponent() <= 18 {
			amount := decimal.NewFromInt(total).DivRound(unit, 18).Mul(rate).Round(18).String()
			if len(amount) > 128 {
				return OpsExportFact{}, invalid
			}
			fact.Amount, fact.Currency = &amount, event.CurrencyCode
			payload["amount"], payload["currency"] = amount, event.CurrencyCode
		}
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return OpsExportFact{}, err
	}
	fact.Payload = string(encoded)
	return fact, nil
}

func ReadOpsFacts(ctx context.Context, db *gorm.DB, after int64, limit int) ([]map[string]any, int64, bool, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, after, false, errors.New("invalid Ops export page")
	}
	if err := PublishOpsFacts(ctx, db); err != nil {
		return nil, after, false, err
	}
	var watermark int64
	if err := db.WithContext(ctx).Model(&OpsExportFact{}).Select("COALESCE(MAX(id), 0)").Scan(&watermark).Error; err != nil {
		return nil, after, false, err
	}
	if after > watermark {
		return nil, after, false, ErrOpsExportCursor
	}
	var rows []OpsExportFact
	if err := db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, after, false, err
	}
	more := len(rows) > limit
	items := make([]map[string]any, 0, min(len(rows), limit))
	for _, row := range rows[:min(len(rows), limit)] {
		var item map[string]any
		if err := common.UnmarshalJsonStr(row.Payload, &item); err != nil {
			return nil, after, false, err
		}
		items = append(items, item)
		after = row.ID
	}
	var pending int64
	if err := opsUnpublished(db.WithContext(ctx)).Count(&pending).Error; err != nil {
		return nil, after, false, err
	}
	return items, after, more || pending > 0, nil
}

func ReadOpsDaily(ctx context.Context, db *gorm.DB, day string) (map[string]any, error) {
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil || parsed.Year() < 1 || parsed.Format("2006-01-02") != day {
		return nil, errors.New("invalid Ops export date")
	}
	if err := PublishOpsFacts(ctx, db); err != nil {
		return nil, err
	}
	// Bound all rows to one immutable watermark even if another publisher runs.
	var watermark int64
	if err := db.WithContext(ctx).Model(&OpsExportFact{}).Select("COALESCE(MAX(id), 0)").Scan(&watermark).Error; err != nil {
		return nil, err
	}
	amounts := map[string]decimal.Decimal{}
	quota := decimal.Zero
	var count, unknown, after int64
	for {
		var rows []OpsExportFact
		if err := db.WithContext(ctx).Where("day = ? AND id > ? AND id <= ?", day, after, watermark).
			Order("id").Limit(500).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			count++
			quota = quota.Add(decimal.NewFromInt(row.QuotaDelta))
			if row.Amount == nil {
				unknown++
			} else {
				value, err := decimal.NewFromString(*row.Amount)
				if err != nil {
					return nil, err
				}
				amounts[row.Currency] = amounts[row.Currency].Add(value)
			}
			after = row.ID
		}
		if len(rows) < 500 {
			break
		}
	}
	if quota.Abs().GreaterThan(decimal.NewFromInt(1<<63 - 1)) {
		return nil, errors.New("Ops export daily quota exceeds supported range")
	}
	money := map[string]string{}
	for currency, amount := range amounts {
		money[currency] = amount.String()
	}
	var pending int64
	if err := opsUnpublished(db.WithContext(ctx)).Count(&pending).Error; err != nil {
		return nil, err
	}
	var newer int64
	if err := db.WithContext(ctx).Model(&OpsExportFact{}).Where("id > ?", watermark).Count(&newer).Error; err != nil {
		return nil, err
	}
	return map[string]any{"date": day, "timezone": "UTC", "maxFactId": watermark,
		"backfillComplete": pending == 0 && newer == 0, "pendingPublicationCount": pending,
		"sourceCoverageComplete": false, "coverageGaps": []string{"legacy_non_agency_charges", "cash_payments_and_refunds", "recognized_revenue", "supplier_bills_and_payments", "commissions_and_fees"},
		"totals": map[string]any{"charge": map[string]any{"count": count, "quotaDelta": quota.IntPart(),
			"unknownAmountCount": unknown, "amountByCurrency": money, "recognizedByCurrency": map[string]string{}}}}, nil
}
