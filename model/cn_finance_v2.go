package model

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const CNFinanceSource = "gateway-cn-finance-v2"
const CNTimeLayout = "2006-01-02T15:04:05.000000Z"

// CN_EXPORT_0001 is additive. It never changes the v1 exporter or source receipts.
type CNFinanceStream struct {
	ID         int64  `gorm:"primaryKey"`
	Epoch      string `gorm:"size:64;not null"`
	GatewayID  string `gorm:"size:191;not null"`
	Sequence   int64  `gorm:"not null"`
	Generation int64  `gorm:"not null"`
	StartedAt  string `gorm:"size:40;not null"`
	Migration  string `gorm:"size:32;not null"`
	ScanAfter  int64
}

type CNFinanceOutbox struct {
	ID             int64  `gorm:"primaryKey"`
	FactID         string `gorm:"size:300;not null;uniqueIndex"`
	ChainKey       string `gorm:"size:64;not null;uniqueIndex:uidx_cn_chain_revision,priority:1"`
	Revision       int64  `gorm:"not null;uniqueIndex:uidx_cn_chain_revision,priority:2"`
	ForkKey        string `gorm:"size:64;not null;uniqueIndex"`
	PreviousFactID string `gorm:"size:300;index"`
	Payload        string `gorm:"type:text;not null"`
	PayloadHash    string `gorm:"size:64;not null"`
	State          string `gorm:"size:32;index"`
}

type CNFinancePublication struct {
	Sequence int64   `gorm:"primaryKey;autoIncrement:false"`
	FactID   *string `gorm:"size:300;uniqueIndex"`
	Day      string  `gorm:"size:10;index"`
	Payload  string  `gorm:"type:text;not null"`
}

// Legacy intents are deliberately not facts. A crash between a non-atomic wallet
// mutation and acknowledgment cannot safely be inferred from the current balance.
type CNFinanceIntent struct {
	Key       string `gorm:"size:64;primaryKey"`
	Payload   string `gorm:"type:text;not null"`
	State     string `gorm:"size:32;not null;index"`
	CreatedAt int64  `gorm:"not null"`
}

type CNFinanceAssociation struct {
	ChargeID    string `gorm:"size:191;primaryKey"`
	RequestID   string `gorm:"size:191;not null"`
	CustomerKey string `gorm:"size:64;not null"`
}

type CNFinanceGap struct {
	ID       string `gorm:"size:64;primaryKey"`
	SourceID string `gorm:"size:64;not null;index"`
	Reason   string `gorm:"size:128;not null"`
	Resolved bool
}

type CNMoney struct {
	Currency         string  `json:"currency"`
	OriginalCurrency string  `json:"originalCurrency"`
	OriginalAmount   string  `json:"originalAmount"`
	Gross            string  `json:"gross"`
	Net              *string `json:"net"`
	Tax              *string `json:"tax"`
	TaxTreatment     string  `json:"taxTreatment"`
	TaxEvidenceID    *string `json:"taxEvidenceId"`
}

type CNMetric struct {
	Quota *string  `json:"quota"`
	Money *CNMoney `json:"money"`
}

type CNEvidence struct {
	System     string `json:"system"`
	RecordType string `json:"recordType"`
	RecordID   string `json:"recordId"`
	EvidenceID string `json:"evidenceId"`
}

type CNFact struct {
	FactID              string              `json:"factId"`
	PublicationSequence string              `json:"publicationSequence"`
	EconomicID          string              `json:"economicId"`
	SegmentID           string              `json:"segmentId"`
	Kind                string              `json:"kind"`
	OperationID         string              `json:"operationId"`
	PostingRole         string              `json:"postingRole"`
	Revision            int64               `json:"revision"`
	PreviousFactID      *string             `json:"previousFactId"`
	Basis               string              `json:"basis"`
	BillingSource       string              `json:"billingSource"`
	RequestID           *string             `json:"requestId"`
	TaskID              *string             `json:"taskId"`
	CustomerKey         *string             `json:"customerKey"`
	OccurredAt          string              `json:"occurredAt"`
	EffectiveAt         string              `json:"effectiveAt"`
	SourcePeriod        string              `json:"sourcePeriod"`
	Status              string              `json:"status"`
	Quality             string              `json:"quality"`
	Cumulative          CNMetric            `json:"cumulative"`
	Delta               CNMetric            `json:"delta"`
	Recognition         map[string]any      `json:"recognition"`
	Funding             map[string]any      `json:"funding"`
	Conversion          map[string]any      `json:"conversion"`
	SourceEvidence      []CNEvidence        `json:"sourceEvidence"`
	Links               map[string][]string `json:"links"`
}

func CNIdentity(parts ...string) string {
	encoded, _ := common.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func CNLinks() map[string][]string {
	result := map[string][]string{}
	for _, key := range []string{"paymentIds", "fundingLotIds", "allocationIds", "chargeFactIds", "refundIds", "supplierBillIds", "approvalIds"} {
		result[key] = []string{}
	}
	return result
}

func MigrateCNFinance(db *gorm.DB, gatewayID string) error {
	if gatewayID == "" || len(gatewayID) > 191 {
		return errors.New("invalid finance gateway identity")
	}
	if err := db.AutoMigrate(&CNFinanceStream{}, &CNFinanceOutbox{}, &CNFinancePublication{}, &CNFinanceIntent{}, &CNFinanceAssociation{}, &CNFinanceGap{}, &CNWalletLifecycle{}, &CNWalletCancellation{}, &CNFinanceReceipt{}, &CNFundingValueLot{}, &CNFundingValueAllocation{}); err != nil {
		return err
	}
	row := CNFinanceStream{ID: 1, GatewayID: gatewayID, Epoch: uuid.NewString(), StartedAt: time.Now().UTC().Format(CNTimeLayout), Migration: "CN_EXPORT_0001"}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := db.First(&row, 1).Error; err != nil {
		return err
	}
	if row.GatewayID != gatewayID || row.Migration != "CN_EXPORT_0001" {
		return errors.New("finance stream identity mismatch")
	}
	return nil
}

var cnCanonicalMoney = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]{1,18})?$`)
var cnEpoch = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var ErrCNConflict = errors.New("fact_identity_conflict")
var ErrCNWaiting = errors.New("waiting_predecessor")

func cnDecimal(value string) (decimal.Decimal, error) {
	if len(value) > 128 || !cnCanonicalMoney.MatchString(value) {
		return decimal.Zero, errors.New("invalid decimal")
	}
	n, err := decimal.NewFromString(value)
	if err != nil || n.String() != value {
		return decimal.Zero, errors.New("noncanonical decimal")
	}
	return n, nil
}

func cnValidateMoney(m *CNMoney) error {
	if m == nil {
		return nil
	}
	if !opsMoneyCurrency.MatchString(m.Currency) || !opsMoneyCurrency.MatchString(m.OriginalCurrency) {
		return errors.New("invalid currency")
	}
	gross, err := cnDecimal(m.Gross)
	if err != nil {
		return err
	}
	if _, err = cnDecimal(m.OriginalAmount); err != nil {
		return err
	}
	if m.TaxTreatment == "unknown" && m.Net == nil && m.Tax == nil {
		return nil
	}
	if m.Net == nil || m.Tax == nil || m.TaxEvidenceID == nil || *m.TaxEvidenceID == "" {
		return errors.New("tax evidence missing")
	}
	net, err := cnDecimal(*m.Net)
	if err != nil {
		return err
	}
	tax, err := cnDecimal(*m.Tax)
	if err != nil {
		return err
	}
	if !gross.Equal(net.Add(tax)) {
		return errors.New("tax identity mismatch")
	}
	switch m.TaxTreatment {
	case "inclusive", "exclusive":
	case "not_applicable":
		if !tax.IsZero() {
			return errors.New("invalid tax")
		}
	default:
		return errors.New("invalid tax treatment")
	}
	return nil
}

func cnValidateMetric(current, delta CNMetric, previous *CNMetric) error {
	if (current.Quota == nil) != (delta.Quota == nil) || (current.Money == nil) != (delta.Money == nil) {
		return errors.New("metric availability mismatch")
	}
	if previous != nil && ((current.Quota == nil) != (previous.Quota == nil) || (current.Money == nil) != (previous.Money == nil)) {
		return ErrCNConflict
	}
	if current.Quota != nil {
		a, e := strconv.ParseInt(*current.Quota, 10, 64)
		if e != nil || strconv.FormatInt(a, 10) != *current.Quota || a < 0 {
			return errors.New("invalid quota")
		}
		d, e := strconv.ParseInt(*delta.Quota, 10, 64)
		if e != nil || strconv.FormatInt(d, 10) != *delta.Quota {
			return errors.New("invalid quota delta")
		}
		p := int64(0)
		if previous != nil {
			p, e = strconv.ParseInt(*previous.Quota, 10, 64)
			if e != nil {
				return e
			}
		}
		if decimal.NewFromInt(a).Sub(decimal.NewFromInt(p)).String() != strconv.FormatInt(d, 10) {
			return errors.New("invalid_revision_delta")
		}
	}
	if err := cnValidateMoney(current.Money); err != nil {
		return err
	}
	if err := cnValidateMoney(delta.Money); err != nil {
		return err
	}
	if current.Money != nil {
		a, d := current.Money, delta.Money
		if a.Currency != d.Currency || a.OriginalCurrency != d.OriginalCurrency || a.TaxTreatment != d.TaxTreatment {
			return ErrCNConflict
		}
		var p *CNMoney
		if previous != nil {
			p = previous.Money
			if p.Currency != a.Currency || p.OriginalCurrency != a.OriginalCurrency || p.TaxTreatment != a.TaxTreatment {
				return ErrCNConflict
			}
		}
		fields := [][3]*string{{&a.Gross, &d.Gross, nil}, {&a.OriginalAmount, &d.OriginalAmount, nil}, {a.Net, d.Net, nil}, {a.Tax, d.Tax, nil}}
		if p != nil {
			fields[0][2] = &p.Gross
			fields[1][2] = &p.OriginalAmount
			fields[2][2] = p.Net
			fields[3][2] = p.Tax
		}
		for _, f := range fields {
			if f[0] == nil {
				continue
			}
			av, _ := cnDecimal(*f[0])
			dv, _ := cnDecimal(*f[1])
			pv := decimal.Zero
			if f[2] != nil {
				pv, _ = cnDecimal(*f[2])
			}
			if !av.Sub(pv).Equal(dv) {
				return errors.New("invalid_revision_delta")
			}
		}
	}
	return nil
}

func cnDivideHalfEven(numerator, denominator decimal.Decimal) decimal.Decimal {
	q, r := numerator.QuoRem(denominator, 18)
	step := decimal.New(1, -18)
	comparison := r.Abs().Mul(decimal.NewFromInt(2)).Cmp(denominator.Abs().Mul(step))
	if comparison > 0 || (comparison == 0 && q.Shift(18).BigInt().Bit(0) == 1) {
		if numerator.Sign()*denominator.Sign() < 0 {
			return q.Sub(step)
		}
		return q.Add(step)
	}
	return q
}

func cnValidateRecognition(fact CNFact, previous *CNFact) error {
	if previous != nil && (fact.Recognition == nil) != (previous.Recognition == nil) {
		return ErrCNConflict
	}
	if fact.Recognition == nil {
		return nil
	}
	var current, delta, prior CNMoney
	for key, target := range map[string]*CNMoney{"cumulative": &current, "delta": &delta} {
		encoded, err := common.Marshal(fact.Recognition[key])
		if err != nil {
			return err
		}
		if err = common.Unmarshal(encoded, target); err != nil {
			return err
		}
	}
	var p *CNMetric
	if previous != nil {
		encoded, err := common.Marshal(previous.Recognition["cumulative"])
		if err != nil {
			return err
		}
		if err = common.Unmarshal(encoded, &prior); err != nil {
			return err
		}
		p = &CNMetric{Money: &prior}
	}
	return cnValidateMetric(CNMetric{Money: &current}, CNMetric{Money: &delta}, p)
}

// StageCNFactTx writes immutable evidence with the caller's money mutation.
// Out-of-order dependencies remain staged; publication validates the full chain.
func StageCNFactTx(tx *gorm.DB, fact CNFact) error {
	if fact.FactID == "" || len(fact.FactID) > 300 || fact.EconomicID == "" || len(fact.EconomicID) > 300 || fact.SegmentID == "" || fact.OperationID == "" || len(fact.OperationID) > 300 || fact.PostingRole != fact.Kind || fact.Basis != "gateway_actual" || fact.Revision < 1 || fact.Revision > 9007199254740991 || len(fact.SourceEvidence) == 0 {
		return errors.New("invalid finance fact")
	}
	validKind := false
	for _, kind := range []string{"charge", "payment", "payment_refund", "supplier_cost", "recognized_revenue", "agency_commission", "payment_fee", "business_tax", "other_expense", "funding_lot", "funding_allocation"} {
		validKind = validKind || fact.Kind == kind
	}
	if !validKind {
		return errors.New("unsupported financial kind")
	}
	if fact.Kind == "charge" && (fact.CustomerKey == nil || (fact.RequestID == nil && fact.TaskID == nil)) {
		return errors.New("charge association missing")
	}
	if fact.PreviousFactID != nil && (*fact.PreviousFactID == "" || *fact.PreviousFactID == fact.FactID) {
		return ErrCNConflict
	}
	at, err := time.Parse(time.RFC3339Nano, fact.OccurredAt)
	if err != nil || !strings.HasSuffix(fact.OccurredAt, "Z") || at.Year() < 1 {
		return errors.New("invalid occurredAt")
	}
	effective, err := time.Parse(time.RFC3339Nano, fact.EffectiveAt)
	if err != nil || !strings.HasSuffix(fact.EffectiveAt, "Z") || fact.SourcePeriod != effective.Format("2006-01") {
		return errors.New("invalid effectiveAt")
	}
	fact.OccurredAt = at.UTC().Format(CNTimeLayout)
	fact.EffectiveAt = effective.UTC().Format(CNTimeLayout)
	if fact.Kind == "funding_lot" || fact.Kind == "funding_allocation" {
		if fact.Funding == nil {
			return errors.New("funding evidence missing")
		}
		if fact.Kind == "funding_lot" {
			purchased, ok := fact.Funding["purchasedAt"].(string)
			timestamp, err := time.Parse(time.RFC3339Nano, purchased)
			if !ok || err != nil || !strings.HasSuffix(purchased, "Z") {
				return errors.New("invalid purchasedAt")
			}
			fact.Funding["purchasedAt"] = timestamp.UTC().Format(CNTimeLayout)
		}
	}
	fact.PublicationSequence = ""
	encoded, err := common.Marshal(fact)
	if err != nil {
		return err
	}
	row := CNFinanceOutbox{FactID: fact.FactID, ChainKey: CNIdentity(fact.Kind, fact.EconomicID, fact.SegmentID), Revision: fact.Revision, Payload: string(encoded), PayloadHash: fmt.Sprintf("%x", sha256.Sum256(encoded)), State: "pending"}
	if fact.PreviousFactID != nil {
		row.PreviousFactID = *fact.PreviousFactID
	}
	row.ForkKey = CNIdentity(row.ChainKey, row.PreviousFactID)
	ancestor := row.PreviousFactID
	for range 10000 {
		if ancestor == "" {
			break
		}
		var prior CNFinanceOutbox
		err := tx.Where("fact_id = ?", ancestor).First(&prior).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return err
		}
		if prior.ChainKey != row.ChainKey || prior.Revision >= row.Revision {
			return ErrCNConflict
		}
		ancestor = prior.PreviousFactID
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	var stored CNFinanceOutbox
	if err := tx.Where("fact_id = ?", fact.FactID).First(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCNConflict
		}
		return err
	}
	if stored.PayloadHash != row.PayloadHash {
		return ErrCNConflict
	}
	return nil
}

func CNCursor(epoch string, sequence int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("cnf2:" + epoch + ":" + strconv.FormatInt(sequence, 10)))
}

func ParseCNCursor(cursor string, stream CNFinanceStream) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 256 {
		return 0, ErrOpsExportCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return 0, ErrOpsExportCursor
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "cnf2" || !cnEpoch.MatchString(parts[1]) || parts[1] != stream.Epoch {
		return 0, ErrOpsExportCursor
	}
	n, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || n < 0 || n > stream.Sequence || strconv.FormatInt(n, 10) != parts[2] {
		return 0, ErrOpsExportCursor
	}
	return n, nil
}

func cnLockPublisher(tx *gorm.DB) (CNFinanceStream, error) {
	var stream CNFinanceStream
	result := tx.Model(&CNFinanceStream{}).Where("id = ? AND generation < ?", 1, math.MaxInt64).Update("generation", gorm.Expr("generation + 1"))
	if result.Error != nil {
		return stream, result.Error
	}
	if result.RowsAffected != 1 {
		return stream, errors.New("finance publisher unavailable")
	}
	err := tx.First(&stream, 1).Error
	return stream, err
}

func cnPublishRecord(tx *gorm.DB, stream *CNFinanceStream, fact *CNFact, checkpoint string) error {
	if stream.Sequence == math.MaxInt64 {
		return errors.New("publication sequence exhausted")
	}
	stream.Sequence++
	row := CNFinancePublication{Sequence: stream.Sequence, Payload: checkpoint}
	if fact != nil {
		fact.PublicationSequence = strconv.FormatInt(stream.Sequence, 10)
		row.FactID = &fact.FactID
		row.Day = fact.OccurredAt[:10]
		encoded, err := common.Marshal(fact)
		if err != nil {
			return err
		}
		row.Payload = string(encoded)
	}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	return tx.Model(&CNFinanceStream{}).Where("id = ?", 1).Update("sequence", stream.Sequence).Error
}

func cnPublishPending(tx *gorm.DB, stream *CNFinanceStream) error {
	// Persist the scan position so a prefix of waiting records cannot starve a
	// later predecessor or unrelated source. The bounded pass resumes on retry.
	after := stream.ScanAfter
	for range 16 {
		var rows []CNFinanceOutbox
		published := tx.Model(&CNFinancePublication{}).Select("fact_id").Where("fact_id IS NOT NULL")
		if err := tx.Where("fact_id NOT IN (?) AND id > ? AND state <> ?", published, after, "quarantined").Order("id").Limit(1000).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			if after == 0 {
				break
			}
			after = 0
			continue
		}
		progress := false
		for _, row := range rows {
			after = row.ID
			var fact CNFact
			if err := common.UnmarshalJsonStr(row.Payload, &fact); err != nil {
				if err := cnQuarantine(tx, row, "invalid_payload"); err != nil {
					return err
				}
				continue
			}
			var previous *CNFact
			var predecessor CNFinancePublication
			if fact.PreviousFactID != nil {
				err := tx.Where("fact_id = ?", *fact.PreviousFactID).First(&predecessor).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				previous = &CNFact{}
				if err = common.UnmarshalJsonStr(predecessor.Payload, previous); err != nil {
					return err
				}
				if previous.Kind != fact.Kind || previous.EconomicID != fact.EconomicID || previous.SegmentID != fact.SegmentID || previous.Revision >= fact.Revision {
					if err := cnQuarantine(tx, row, "invalid_predecessor"); err != nil {
						return err
					}
					continue
				}
				a, _ := common.Marshal(previous.Conversion)
				b, _ := common.Marshal(fact.Conversion)
				if string(a) != string(b) {
					if err := cnQuarantine(tx, row, "conversion_changed"); err != nil {
						return err
					}
					continue
				}
			}
			// A second genesis or a fork is never a new economic event.
			var siblings int64
			if err := tx.Model(&CNFinanceOutbox{}).Where("chain_key = ? AND fact_id <> ? AND previous_fact_id = ?", row.ChainKey, row.FactID, row.PreviousFactID).Count(&siblings).Error; err != nil {
				return err
			}
			if siblings > 0 {
				if err := cnQuarantine(tx, row, "forked_chain"); err != nil {
					return err
				}
				continue
			}
			var prior *CNMetric
			if previous != nil {
				prior = &previous.Cumulative
			}
			if err := cnValidateMetric(fact.Cumulative, fact.Delta, prior); err != nil {
				if err := cnQuarantine(tx, row, "invalid_revision_delta"); err != nil {
					return err
				}
				continue
			}
			if err := cnValidateRecognition(fact, previous); err != nil {
				if err := cnQuarantine(tx, row, "invalid_recognition_delta"); err != nil {
					return err
				}
				continue
			}
			if err := cnPublishRecord(tx, stream, &fact, ""); err != nil {
				return err
			}
			progress = true
		}
		if len(rows) < 1000 {
			after = 0
			if !progress {
				break
			}
		}
	}
	stream.ScanAfter = after
	return tx.Model(&CNFinanceStream{}).Where("id = ?", 1).Update("scan_after", after).Error
}

func cnQuarantine(tx *gorm.DB, row CNFinanceOutbox, reason string) error {
	if err := tx.Model(&CNFinanceOutbox{}).Where("id = ?", row.ID).Update("state", "quarantined").Error; err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&CNFinanceGap{ID: CNIdentity("publication-gap", row.FactID), SourceID: "publication", Reason: reason}).Error
}

func ReadCNFacts(ctx context.Context, db *gorm.DB, after string, limit int) (map[string]any, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("invalid limit")
	}
	var result map[string]any
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stream, err := cnLockPublisher(tx)
		if err != nil {
			return err
		}
		// Validate against the pre-request watermark, before any publication writes.
		n, err := ParseCNCursor(after, stream)
		if err != nil {
			return err
		}
		if err = cnCaptureSources(tx); err != nil {
			return err
		}
		if err = cnPublishPending(tx, &stream); err != nil {
			return err
		}
		if err = cnPublishCheckpoint(tx, &stream); err != nil {
			return err
		}
		var rows []CNFinancePublication
		if err = tx.Where("sequence > ? AND sequence <= ?", n, stream.Sequence).Order("sequence").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		items := []CNFact{}
		for _, row := range rows {
			if row.FactID != nil {
				var f CNFact
				if err = common.UnmarshalJsonStr(row.Payload, &f); err != nil {
					return err
				}
				items = append(items, f)
			}
			n = row.Sequence
		}
		var pending int64
		if err = cnPending(tx).Count(&pending).Error; err != nil {
			return err
		}
		result = map[string]any{"schemaVersion": "2", "gatewayId": stream.GatewayID, "sourceSystem": CNFinanceSource, "streamEpoch": stream.Epoch, "items": items, "nextCursor": CNCursor(stream.Epoch, n), "hasMore": n < stream.Sequence, "publishedThrough": CNCursor(stream.Epoch, stream.Sequence), "publicationPendingCount": pending}
		return nil
	})
	return result, err
}

func cnPending(tx *gorm.DB) *gorm.DB {
	published := tx.Model(&CNFinancePublication{}).Select("fact_id").Where("fact_id IS NOT NULL")
	return tx.Model(&CNFinanceOutbox{}).Where("fact_id NOT IN (?)", published)
}
