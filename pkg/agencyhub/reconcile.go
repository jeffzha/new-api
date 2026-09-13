package agencyhub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReconcileSummary counts inspected current records and persisted findings.
// Reconciliation never changes money or guesses an external outcome.
type ReconcileSummary struct {
	CheckedFundingAccounts    int   `json:"checked_funding_accounts"`
	CheckedCommissionBalances int   `json:"checked_commission_balances"`
	CheckedBindings           int   `json:"checked_bindings"`
	CheckedOutboxEvents       int   `json:"checked_outbox_events"`
	MissingDeliveries         int   `json:"missing_deliveries"`
	IssuesCreated             int   `json:"issues_created"`
	OpenIssuesBefore          int64 `json:"open_issues_before"`
	OpenIssuesAfter           int64 `json:"open_issues_after"`
	CheckedWithdrawals        int   `json:"checked_withdrawals"`
	CheckedOperations         int   `json:"checked_operations"`
	CheckedFundingLots        int   `json:"checked_funding_lots"`
	CheckedChargeComponents   int   `json:"checked_charge_components"`
}

type reconciliationSource struct {
	objectType string
	table      string
	primaryKey string
	fields     string
	checked    *int
}

// Reconcile uses the same authoritative checks as Root's evidence review.
// Each bounded page is inspected in one consistent read snapshot. Findings
// are written afterwards, so SQLite readers never upgrade into writers and
// gateway commits cannot be combined into a false per-object discrepancy.
// Pages are current-state snapshots, not a historical daily close.
func (a *App) Reconcile(ctx context.Context) (ReconcileSummary, error) {
	var summary ReconcileSummary
	if a == nil || a.db == nil {
		return summary, errors.New("agency database unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.db.WithContext(ctx).Model(&model.AgencyReconciliationIssue{}).
		Where("status = ?", "open").Count(&summary.OpenIssuesBefore).Error; err != nil {
		return summary, err
	}
	for _, source := range []reconciliationSource{
		{"funding_account", (model.AgencyFundingAccount{}).TableName(), "user_id", "user_id AS id", &summary.CheckedFundingAccounts},
		{"commission_balance", (model.AgencyCommissionBalance{}).TableName(), "id", "id, agency_id, currency_code", &summary.CheckedCommissionBalances},
		{"active_binding", (model.AgencyActiveUserBinding{}).TableName(), "user_id", "user_id AS id", &summary.CheckedBindings},
		{"billing_outbox", (model.AgencyBillingOutbox{}).TableName(), "id", "id, event_id", &summary.CheckedOutboxEvents},
		{"withdrawal_lock", (model.AgencyCommissionBalance{}).TableName(), "id", "id, agency_id, currency_code", nil},
		{"billing_operation", (model.AgencyBillingOperation{}).TableName(), "id", "id, operation_id, charge_id", &summary.CheckedOperations},
		{"funding_lot", (model.AgencyFundingLot{}).TableName(), "id", "id", &summary.CheckedFundingLots},
		{"charge_component", (model.AgencyChargeComponent{}).TableName(), "id", "id", &summary.CheckedChargeComponents},
	} {
		if err := a.reconcileEvidenceSource(ctx, source, &summary); err != nil {
			return summary, err
		}
	}
	var withdrawals int64
	if err := a.db.WithContext(ctx).Model(&model.AgencyWithdrawal{}).
		Where("status IN ?", []string{"submitted", "reviewing", "approved", "on_hold", "paying", "payment_unknown"}).
		Count(&withdrawals).Error; err != nil {
		return summary, err
	}
	summary.CheckedWithdrawals = int(withdrawals)
	if err := a.db.WithContext(ctx).Model(&model.AgencyReconciliationIssue{}).
		Where("status = ?", "open").Count(&summary.OpenIssuesAfter).Error; err != nil {
		return summary, err
	}
	return summary, nil
}

func (a *App) reconcileEvidenceSource(ctx context.Context, source reconciliationSource, summary *ReconcileSummary) error {
	// Fix the upper primary-key boundary for this source. New and late-commit
	// rows are included on the next complete scan, without a permanent cursor.
	var boundary struct{ ID int64 }
	if err := a.db.WithContext(ctx).Table(source.table).Select(source.primaryKey + " AS id").
		Order(source.primaryKey + " DESC").Limit(1).Find(&boundary).Error; err != nil {
		return err
	}
	var lastID int64
	for lastID < boundary.ID {
		var rows []struct {
			ID           int64
			AgencyID     int64
			CurrencyCode string
			EventID      string
			OperationID  string
			ChargeID     string
		}
		var findings []model.AgencyReconciliationIssue
		missingDeliveries := 0
		err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Table(source.table).Select(source.fields).
				Where(source.primaryKey+" > ? AND "+source.primaryKey+" <= ?", lastID, boundary.ID).
				Order(source.primaryKey + " ASC").Limit(200).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				objectID := stringID(row.ID)
				switch source.objectType {
				case "commission_balance", "withdrawal_lock":
					objectID = stringID(row.AgencyID) + ":" + row.CurrencyCode
				case "billing_outbox":
					objectID = row.EventID
				case "billing_operation":
					objectID = row.OperationID
					if objectID == "" {
						objectID = row.ChargeID
					}
				}
				issue := model.AgencyReconciliationIssue{ObjectType: source.objectType, ObjectID: objectID, Status: "open"}
				evidence, err := reconciliationEvidence(tx, issue, false)
				if err != nil {
					return err
				}
				var differences []string
				deliveryChecked := false
				for _, check := range evidence.Checks {
					if check.Name == "delivery_exists" {
						deliveryChecked = true
						if !check.Matched {
							missingDeliveries++
						}
					}
					if !check.Matched {
						differences = append(differences, fmt.Sprintf("%s: expected=%s, actual=%s", check.Name, check.Expected, check.Actual))
					}
				}
				// Unreadable payloads can stop evidence inspection before the
				// delivery check. Still count missing rows accurately.
				if source.objectType == "billing_outbox" && !deliveryChecked {
					var count int64
					if err := tx.Model(&model.AgencyEventDelivery{}).Where("event_id = ?", objectID).Count(&count).Error; err != nil {
						return err
					}
					if count == 0 {
						missingDeliveries++
					}
				}
				if evidence.State != "consistent" {
					issue.Difference = strings.Join(differences, "; ")
					findings = append(findings, issue)
				}
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		if source.checked != nil {
			*source.checked += len(rows)
		}
		summary.MissingDeliveries += missingDeliveries
		for _, issue := range findings {
			created, err := a.createReconciliationIssue(ctx, issue.ObjectType, issue.ObjectID, issue.Difference)
			if err != nil {
				return err
			}
			summary.IssuesCreated += created
		}
		lastID = rows[len(rows)-1].ID
	}
	return nil
}

func (a *App) createReconciliationIssue(ctx context.Context, objectType, objectID, difference string) (int, error) {
	if objectType == "" || objectID == "" || difference == "" {
		return 0, errors.New("invalid reconciliation issue")
	}
	digest := sha256.Sum256([]byte(objectType + "\x00" + objectID + "\x00" + difference))
	evidenceHash := hex.EncodeToString(digest[:])
	activeKey := model.AgencyReconciliationActiveKey(objectType, objectID)
	created := 0
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "active_key"}}, DoNothing: true}).Create(&model.AgencyReconciliationIssue{
			ObjectType: objectType, ObjectID: objectID, Difference: difference,
			ActiveKey: &activeKey, EvidenceHash: evidenceHash, Status: "open", CreatedAtMS: time.Now().UnixMilli(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			created = 1
			return nil
		}
		// Keep the unique-key conflict lock until the refresh commits. Closing
		// a finding cannot interleave between the conflict and this update.
		return tx.Model(&model.AgencyReconciliationIssue{}).Where("active_key = ? AND status = ?", activeKey, "open").Updates(map[string]any{"difference": difference, "evidence_hash": evidenceHash}).Error
	})
	return created, err
}
