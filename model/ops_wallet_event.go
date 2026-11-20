package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOPSLocalWalletWrite = errors.New("OPS owns this wallet; local balance writes are disabled")

// OPSWalletEvent is the transactional outbox for funding owned by OPS. The
// local order or redemption and this immutable event commit together; User.Quota
// is not credited. A delivery retry always sends the same stored body.
type OPSWalletEvent struct {
	ID          int64  `gorm:"primaryKey;index:idx_ops_wallet_pending,priority:2"`
	EventID     string `gorm:"size:80;not null;uniqueIndex:uidx_ops_wallet_event"`
	Payload     string `gorm:"type:text;not null"`
	PayloadHash string `gorm:"size:64;not null"`
	CreatedAt   int64  `gorm:"not null"`
	DeliveredAt int64  `gorm:"not null;index:idx_ops_wallet_pending,priority:1"`
}

func (OPSWalletEvent) TableName() string { return "ops_wallet_events" }

type OPSUSDFundingFacts struct {
	TradeNo       string `json:"tradeNo,omitempty"`
	PaymentMethod string `json:"paymentMethod,omitempty"`
	PaidAmount    string `json:"paidAmount,omitempty"`
	PaidCurrency  string `json:"paidCurrency,omitempty"`
}

// QueueOPSUSDFundingTx must run inside the business transaction after its
// provider/code validation. The source ID is an order/code ID, never a secret.
func QueueOPSUSDFundingTx(tx *gorm.DB, userID, quota int64, kind, source, sourceID string, facts *OPSUSDFundingFacts) error {
	if !opsauthority.Enabled() || !opsauthority.USDFundingEnabled() {
		return errors.New("OPS native funding is not enabled")
	}
	if tx == nil || userID <= 0 || quota <= 0 || quota > common.MaxWalletQuota || kind != "topup" && kind != "gift" || source == "" || len(source) > 32 || sourceID == "" || len(sourceID) > 255 || strings.ContainsRune(sourceID, 0) || opsauthority.Scope() == "" {
		return errors.New("invalid OPS funding event")
	}
	unit := decimal.NewFromFloat(common.QuotaPerUnit)
	if !unit.IsPositive() {
		return errors.New("invalid OPS funding quota unit")
	}
	amount := decimal.NewFromInt(quota).Div(unit)
	if !amount.Equal(amount.Truncate(6)) || !amount.Mul(unit).Equal(decimal.NewFromInt(quota)) || amount.GreaterThan(decimal.NewFromInt(1_000_000_000)) {
		return errors.New("OPS funding exceeds receiver precision or range")
	}
	var user User
	if err := lockForUpdate(tx).Select("id").First(&user, userID).Error; err != nil {
		return err
	}
	id := fmt.Sprintf("wf-%x", sha256.Sum256([]byte(opsauthority.Scope()+"\x00"+source+"\x00"+sourceID)))
	extra := map[string]any{"recordKind": kind, "direction": "credit", "fundingSource": source, "fundingSourceId": sourceID}
	if facts != nil {
		extra["topup"] = facts
	}
	// occurredAt is frozen before any remote I/O. It must survive retries and
	// process restarts so a lost ACK cannot turn one credit into a new event.
	var existing OPSWalletEvent
	err := tx.Where("event_id = ?", id).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	at := common.GetTimestamp()
	if err == nil {
		at = existing.CreatedAt
	}
	event := map[string]any{"requestId": id, "userId": fmt.Sprint(userID), "occurredAt": time.Unix(at, 0).UTC().Format(time.RFC3339), "status": "success", "currency": "USD", "amount": amount.String(), "extra": extra}
	body, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": id, "events": []any{event}})
	if err != nil || len(body) > 16*1024 {
		return errors.New("invalid OPS funding payload")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	if existing.ID != 0 {
		if existing.PayloadHash != hash || existing.Payload != string(body) {
			return errors.New("OPS funding identity conflict")
		}
		return nil
	}
	row := OPSWalletEvent{EventID: id, Payload: string(body), PayloadHash: hash, CreatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := tx.Where("event_id = ?", id).First(&existing).Error; err != nil {
		return err
	}
	if existing.PayloadHash != hash || existing.Payload != string(body) {
		return errors.New("OPS funding identity conflict")
	}
	return nil
}

// ReadOPSUSDFunding returns only committed immutable events. Multiple senders
// are safe: the receiver deduplicates the stable event and batch identities.
func ReadOPSUSDFunding(db *gorm.DB, limit int) ([]OPSWalletEvent, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid OPS funding read limit")
	}
	var rows []OPSWalletEvent
	err := db.Where("delivered_at = ?", 0).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

func AcknowledgeOPSUSDFunding(db *gorm.DB, row OPSWalletEvent, at int64) error {
	if row.ID <= 0 || row.EventID == "" || len(row.PayloadHash) != 64 || at <= 0 {
		return errors.New("invalid OPS funding acknowledgement")
	}
	result := db.Model(&OPSWalletEvent{}).Where("id = ? AND event_id = ? AND payload_hash = ? AND delivered_at = ?", row.ID, row.EventID, row.PayloadHash, 0).Update("delivered_at", at)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := db.Model(&OPSWalletEvent{}).Where("id = ? AND event_id = ? AND payload_hash = ? AND delivered_at > ?", row.ID, row.EventID, row.PayloadHash, 0).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("OPS funding acknowledgement conflict")
	}
	return nil
}

func ValidateOPSUSDFunding(row OPSWalletEvent) error {
	if row.ID <= 0 || !strings.HasPrefix(row.EventID, "wf-") || row.PayloadHash != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Payload))) {
		return errors.New("OPS funding payload changed")
	}
	return nil
}
