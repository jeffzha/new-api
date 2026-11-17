package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AgencyOpsPublisher serializes publication independently from source IDs. A
// transaction with an older outbox ID may commit after a newer transaction.
type AgencyOpsPublisher struct {
	ID         int64 `gorm:"primaryKey"`
	Generation int64 `gorm:"not null"`
}

func (AgencyOpsPublisher) TableName() string { return AgencyTablePrefix + "ops_publishers" }

// AgencyOpsPublication is the immutable, monotonic hand-off order consumed by
// the TokenOps bridge. SourceID uniqueness makes repeated scans idempotent.
type AgencyOpsPublication struct {
	ID          int64  `gorm:"primaryKey"`
	SourceID    int64  `gorm:"not null;uniqueIndex:uidx_agency_ops_publication_source"`
	EventID     string `gorm:"size:191;not null;uniqueIndex:uidx_agency_ops_publication_event"`
	PayloadHash string `gorm:"size:128;not null"`
	CreatedAtMS int64  `gorm:"not null"`
}

func (AgencyOpsPublication) TableName() string {
	return AgencyTablePrefix + "ops_publications"
}

var agencyOpsAuthorityKinds = []string{
	"agency.billing_reserved", "agency.billing_finalized",
	"agency.billing_reversed", "agency.billing_corrected",
	"agency.topup_completed", "agency.funding_adjusted",
	"agency.funding_reversed", "agency.wallet_checkpoint",
}

type agencyWalletCheckpointEvidence struct {
	OperationID  string `json:"operation_id"`
	EntryNo      int    `json:"entry_no"`
	SourceKind   string `json:"source_kind"`
	PaidDelta    int64  `json:"paid_delta"`
	NonpaidDelta int64  `json:"nonpaid_delta"`
	DebtDelta    int64  `json:"debt_delta"`
}

// publishAgencyWalletCheckpointsTx repairs only sequences that have immutable
// funding-ledger evidence. A missing ledger row remains poison and therefore
// continues to block single-writer cutover.
func publishAgencyWalletCheckpointsTx(tx *gorm.DB) error {
	var accounts []AgencyFundingAccount
	if err := tx.Select("user_id", "money_seq").Where("money_seq > 0").Order("user_id").Find(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		var covered []int64
		if err := tx.Model(&AgencyBillingOutbox{}).Where("user_id = ? AND money_seq > 0 AND money_seq <= ? AND event_kind IN ?", account.UserID, account.MoneySeq, agencyOpsAuthorityKinds).Distinct().Pluck("money_seq", &covered).Error; err != nil {
			return err
		}
		seen := make(map[int64]bool, len(covered))
		for _, seq := range covered {
			seen[seq] = true
		}
		for seq := int64(1); seq <= account.MoneySeq; seq++ {
			if seen[seq] {
				continue
			}
			var ledger []AgencyFundingLedger
			if err := tx.Where("user_id = ? AND money_seq = ?", account.UserID, seq).Order("id").Find(&ledger).Error; err != nil {
				return err
			}
			if len(ledger) == 0 {
				continue
			}
			last := ledger[len(ledger)-1]
			evidence := make([]agencyWalletCheckpointEvidence, 0, len(ledger))
			for _, row := range ledger {
				evidence = append(evidence, agencyWalletCheckpointEvidence{OperationID: row.OperationID, EntryNo: row.EntryNo, SourceKind: row.SourceKind, PaidDelta: row.PaidDelta, NonpaidDelta: row.NonpaidDelta, DebtDelta: row.DebtDelta})
			}
			basis, err := common.Marshal(map[string]any{"ledger_entries": evidence, "paid_after": last.PaidAfter, "nonpaid_after": last.NonpaidAfter, "debt_after": last.DebtAfter})
			if err != nil {
				return err
			}
			id := fmt.Sprintf("agency-wallet-checkpoint-%d-%d", account.UserID, seq)
			now := last.CreatedAtMS
			if now <= 0 {
				now = time.Now().UnixMilli()
			}
			event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: id, EventType: "agency.wallet_checkpoint", FinancialChargeID: id, OperationID: id, JournalRevision: 1, MoneySeq: seq, EventIndex: 0, EventCount: 1, OccurredAtMS: now, UserID: account.UserID, AgencyID: last.AgencyID, BindingID: last.BindingID, BusinessStatus: "checkpoint", BillingStatus: "ledger_verified", BillingBasis: string(basis), CommissionEligible: false, CommissionSkipReason: "wallet_checkpoint_noncommissionable"}
			payload, err := common.Marshal(event)
			if err != nil {
				return err
			}
			hash, err := agencycontract.CanonicalHash(event)
			if err != nil {
				return err
			}
			row := AgencyBillingOutbox{EventID: id, OperationID: id, EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: account.UserID, MoneySeq: seq, Payload: string(payload), PayloadHash: hash, SchemaVersion: event.SchemaVersion, CreatedAtMS: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func agencyOpsUnpublished(db *gorm.DB) *gorm.DB {
	published := db.Model(&AgencyOpsPublication{}).Select("source_id")
	return db.Model(&AgencyBillingOutbox{}).
		Where("event_kind IN ?", agencyOpsAuthorityKinds).
		Where("id NOT IN (?)", published)
}

// PublishAgencyOpsEvents assigns a commit-safe publication sequence. It is
// deliberately separate from the source primary key and can discover late
// commits after any previously published source row.
func PublishAgencyOpsEvents(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := publishAgencyWalletCheckpointsTx(tx); err != nil {
			return err
		}
		locked := tx.Model(&AgencyOpsPublisher{}).Where("id = ?", 1).
			Update("generation", gorm.Expr("generation + ?", 1))
		if locked.Error != nil {
			return locked.Error
		}
		if locked.RowsAffected != 1 {
			return errors.New("agency Ops publisher is unavailable")
		}
		var rows []AgencyBillingOutbox
		if err := agencyOpsUnpublished(tx).Order("id").Limit(256).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			publication := AgencyOpsPublication{SourceID: row.ID, EventID: row.EventID, PayloadHash: row.PayloadHash, CreatedAtMS: row.CreatedAtMS}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&publication).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
