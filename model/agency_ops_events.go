package model

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// AgencyOpsEvent has an independent receipt from the local commission consumer.
// Rows are created in the business transaction, not discovered by a max-ID scan.
type AgencyOpsEvent struct {
	ID           int64            `gorm:"primaryKey;index:idx_agency_ops_priority_due,priority:4"`
	EventID      string           `gorm:"size:160;not null;uniqueIndex:uidx_agency_ops_event"`
	Payload      AgencyOpsPayload `gorm:"not null"`
	Priority     int              `gorm:"not null;index:idx_agency_ops_priority_due,priority:2"`
	PayloadHash  string           `gorm:"size:64;not null"`
	Status       string           `gorm:"size:16;not null;index:idx_agency_ops_event_due,priority:1;index:idx_agency_ops_priority_due,priority:1"`
	NextAtMS     int64            `gorm:"not null;index:idx_agency_ops_event_due,priority:2;index:idx_agency_ops_priority_due,priority:3"`
	LeaseToken   string           `gorm:"size:64"`
	LeaseUntil   int64            `gorm:"not null;index:idx_agency_ops_event_lease"`
	Attempts     int              `gorm:"not null"`
	ErrorCode    string           `gorm:"size:64"`
	CreatedAtMS  int64            `gorm:"not null;index:idx_agency_ops_reviewed,priority:2"`
	DeliveredMS  int64            `gorm:"not null"`
	ReviewedAtMS int64            `gorm:"not null;default:0;index:idx_agency_ops_reviewed,priority:1"`
}

func (AgencyOpsEvent) TableName() string { return AgencyTablePrefix + "ops_events" }

// MySQL TEXT is only 64 KiB; PostgreSQL and SQLite use their native TEXT.
type AgencyOpsPayload string

func (AgencyOpsPayload) GormDataType() string { return "string" }
func (AgencyOpsPayload) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db.Dialector.Name() == "mysql" {
		return "longtext"
	}
	return "text"
}

// Revisions are allocated under a per-object transaction lock, not wall-clock
// time or the outbox primary key. Concurrent writers cannot invert revisions.
type AgencyOpsObjectRevision struct {
	ID       int64  `gorm:"primaryKey"`
	Kind     string `gorm:"size:32;not null;uniqueIndex:uidx_agency_ops_object,priority:1"`
	ObjectID int64  `gorm:"not null;uniqueIndex:uidx_agency_ops_object,priority:2"`
	Version  int64  `gorm:"not null"`
}

func (AgencyOpsObjectRevision) TableName() string { return AgencyTablePrefix + "ops_object_revisions" }

type AgencyOpsSenderLease struct {
	ID      int    `gorm:"primaryKey;autoIncrement:false"`
	Owner   string `gorm:"size:64;not null"`
	UntilMS int64  `gorm:"not null"`
}

func (AgencyOpsSenderLease) TableName() string { return AgencyTablePrefix + "ops_sender_lease" }

type AgencyOpsReview struct {
	ID   int64  `json:"id"`
	Hash string `json:"hash"`
}

// Each immutable event is reviewed once, not every business row every night.
// A per-row acknowledgement (not max(id)) also covers smaller IDs committed late.
func ReadAgencyOpsReview(db *gorm.DB, beforeMS int64) ([]AgencyOpsEvent, error) {
	if beforeMS <= 0 || beforeMS > time.Now().UnixMilli() {
		return nil, fmt.Errorf("invalid Ops review window")
	}
	var rows []AgencyOpsEvent
	if err := db.Where("reviewed_at_ms = ? AND created_at_ms < ?", 0, beforeMS).
		Order("created_at_ms,id").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	size, count := 0, 0
	for _, row := range rows {
		if count > 0 && size+len(row.Payload) > 500*1024 {
			break
		}
		size += len(row.Payload)
		count++
	}
	return rows[:count], nil
}

func AcknowledgeAgencyOpsReview(db *gorm.DB, rows []AgencyOpsReview, atMS int64) error {
	if len(rows) == 0 || len(rows) > 100 || atMS <= 0 || atMS > time.Now().Add(time.Minute).UnixMilli() {
		return fmt.Errorf("invalid Ops review acknowledgement")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			if row.ID <= 0 || len(row.Hash) != 64 {
				return fmt.Errorf("invalid Ops review identity")
			}
			result := tx.Model(&AgencyOpsEvent{}).Where("id = ? AND payload_hash = ? AND reviewed_at_ms = ?", row.ID, row.Hash, 0).
				Update("reviewed_at_ms", atMS)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				var count int64
				if err := tx.Model(&AgencyOpsEvent{}).Where("id = ? AND payload_hash = ? AND reviewed_at_ms > ?", row.ID, row.Hash, 0).Count(&count).Error; err != nil {
					return err
				}
				if count != 1 {
					return fmt.Errorf("Ops review content changed")
				}
			}
		}
		return nil
	})
}

var AgencyOpsEventWake = make(chan struct{}, 1)
var AgencyOpsCommitted = make(chan struct{}, 1)
var agencyOpsEventSchemas sync.Map
var agencyOpsAuditKey = regexp.MustCompile(`[^a-z0-9]`)

// A successful commit only wakes the independent sender. No remote I/O runs
// on the business goroutine, and a rollback never advertises a commit.
func AgencyOpsTransaction(db *gorm.DB, fn func(*gorm.DB) error) error {
	err := db.Transaction(fn)
	if err == nil && os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") == "true" {
		select {
		case AgencyOpsCommitted <- struct{}{}:
		default:
		}
	}
	return err
}

// This is an explicit wire contract, not a serialization of every DB field.
// Adding credentials or payment fields to a model cannot expand the export.
var agencyOpsFields = map[string]string{
	"usage":           "id event_id financial_charge_id component_id component_key usage_hash cumulative_usage user_id agency_id binding_id origin_model_name model_key endpoint business_status input_tokens output_tokens cache_read_tokens cache_write_tokens standard_quota sales_bps charged_quota paid_quota nonpaid_quota debt_quota currency_code occurred_at_ms skip_reason log_id",
	"topups":          "id source_operation_id source_id user_id initiated_by_user_id agency_id binding_id payment_reference actual_money currency_code quota_conversion_snapshot credited_quota paid_quota bonus_quota funding_source completion_source payment_status refunded_quota expired_quota expires_at occurred_at_ms",
	"commissions":     "id event_id component_id component_key entry_type original_entry_id agency_id binding_id user_id origin_model_name standard_quota settlement_cost_quota theoretical_commission_quota paid_allocated_quota commission_quota amount_micros currency_code quota_per_unit exchange_rate occurred_at_ms hierarchy_depth parent_agency_id",
	"balances":        "id agency_id currency_code earned_micros reversed_micros available_micros locked_micros paid_micros version updated_at_ms",
	"withdrawals":     "id request_no agency_id currency_code amount_micros status version account_id account_version account_snapshot_hash payment_channel created_at_ms updated_at_ms",
	"audit":           "id event_id actor_type actor_id acting_agency_id action object_type object_id request_id before_json after_json created_at_ms",
	"wallets":         "user_id paid_available nonpaid_available debt_quota money_seq version opening_snapshot reconcile_blocked reconcile_reason updated_at",
	"funding":         "id operation_id entry_no user_id money_seq source_kind lot_id paid_delta nonpaid_delta debt_delta paid_after nonpaid_after debt_after agency_id binding_id currency_code created_at_ms",
	"journals":        "id charge_id segment_no user_id token_id task_id status business_status delivery_status pricing_snapshot billing_basis reserve_quota charged_total_quota commissionable_quota settlement_cost_quota theoretical_commission_quota paid_allocated_quota commission_quota commission_amount_micros usage_hash last_cumulative_usage reversed_quota reversed_commission_quota currency_code revision version last_error created_at_ms updated_at_ms",
	"fundingLots":     "id user_id source_kind source_id actor_user_id source_snapshot_json completion_source paid_initial bonus_initial paid_available paid_reserved paid_consumed paid_revoked bonus_available bonus_reserved bonus_consumed bonus_revoked expires_at bonus_expired paid_debt_repaid bonus_debt_repaid money_seq version created_at",
	"allocations":     "id charge_id segment_no component_id user_id lot_id source_snapshot_json reserved consumed nonpaid_consumed debt_consumed released reversed revoked_reserved_debt revoked_nonpaid version",
	"debts":           "id user_id origin_operation_id debt_kind allocation_id original_quota outstanding_quota reversed_quota created_at_ms",
	"repayments":      "id debt_id repayment_id funding_lot_id source_kind quota reversed_quota restored_total money_seq created_at_ms",
	"corrections":     "id charge_id corrected_charged_total_quota user_id task_id original_event_id previous_charged_total_quota previous_commission_amount_micros corrected_commission_amount_micros journal_revision event_id operation_id reason operator_id created_at_ms",
	"reversals":       "id refund_id source_operation_id user_id quota currency_code payment_reference evidence_ref reason created_at_ms",
	"reversalCharges": "id refund_id charge_id quota",
	"creditTerms":     "user_id credit_limit payment_term_days repayment_day status email_enabled updated_by created_at_ms updated_at_ms",
	"creditInvoices":  "id user_id period period_start_ms period_end_ms amount_quota paid_quota bad_debt_quota currency_code status repayment_day payment_term_days due_at_ms overdue_at_ms bad_debt_at_ms bad_debt_reason email_status email_sent_at_ms attachment_name email_last_error paid_at_ms payment_reference received_by created_at_ms updated_at_ms",
	"bindings":        "id user_id agency_id revision invite_snapshot created_source effective_at_ms ended_at_ms root_actor_id reason created_at",
	"activeBindings":  "user_id binding_id revision agency_id updated_at",
	"salesOverrides":  "id agency_id user_id model_key origin_model_name sales_bps revision created_by_type created_by_id created_at_ms updated_at_ms",
	"dailyStats":      "id stat_date agency_id binding_id user_id model_key currency_code billing_source calls usage_quota charged_quota commission_micros reversal_micros revision",
	"prices":          "id agency_id revision policy_json policy_hash created_by_type created_by_id reason created_at_ms",
	"platformPrices":  "id revision policy_json policy_hash created_by_id reason created_at_ms",
	"priceItems":      "id policy_version_id scope model_key origin_model_name settlement_bps child_cost_bps sales_bps resolved_settlement_bps resolved_sales_bps",
}

// Match OPS historical redaction so live delivery shares the original identity.
func agencyOpsSafeAudit(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			normalized := agencyOpsAuditKey.ReplaceAllString(strings.ToLower(key), "")
			sensitive := false
			for _, part := range []string{"password", "secret", "credential", "ciphertext", "apikey", "privatekey", "accountnumber", "accountholder", "bankname"} {
				sensitive = sensitive || strings.Contains(normalized, part)
			}
			switch normalized {
			case "account", "iban", "authorization", "accesskey", "refreshkey":
				sensitive = true
			}
			usage := false
			switch normalized {
			case "inputtokens", "outputtokens", "totaltokens", "usagetokens", "cachedtokens", "cachecreationinputtokens", "cachereadinputtokens", "reasoningoutputtokens":
				_, usage = item.(float64)
			}
			sensitive = sensitive || strings.Contains(normalized, "token") && !usage
			if _, boolean := item.(bool); sensitive && !boolean {
				v[key] = "[REDACTED]"
			} else {
				v[key] = agencyOpsSafeAudit(item)
			}
		}
	case []any:
		for i := range v {
			v[i] = agencyOpsSafeAudit(v[i])
		}
	}
	return value
}

// CaptureAgencyOpsRecordTx accepts only the explicit fact types below. It does
// not commit, contact OPS, or change any existing financial writer.
func CaptureAgencyOpsRecordTx(tx *gorm.DB, value any) error {
	return captureAgencyOpsRecordTx(tx, value, nil, false)
}

// Delete customer overrides in bounded pages and retain a versioned tombstone.
// The caller supplies its existing scoped query and owns the business transaction.
func DeleteAgencyCustomerSalesOverridesTx(query *gorm.DB) error {
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" {
		return query.Delete(&AgencyCustomerSalesOverride{}).Error
	}
	var after int64
	for {
		var rows []AgencyCustomerSalesOverride
		if err := query.Session(&gorm.Session{}).Where("id > ?", after).Order("id").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]int64, len(rows))
		for i := range rows {
			ids[i] = rows[i].ID
		}
		if err := query.Session(&gorm.Session{NewDB: true}).Where("id IN ?", ids).Delete(&AgencyCustomerSalesOverride{}).Error; err != nil {
			return err
		}
		for i := range rows {
			if err := captureAgencyOpsRecordTx(query, &rows[i], nil, true); err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
	}
}

// Price publication writes many items with one parent. Load that association
// once, not once per model, and preserve the historical export's metadata.
func CaptureAgencyOpsPriceItemsTx(tx *gorm.DB, items []AgencyPricePolicyItem) error {
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" || len(items) == 0 {
		return nil
	}
	var version AgencyPricePolicyVersion
	if err := tx.Session(&gorm.Session{NewDB: true}).Select("id,agency_id,created_at_ms").First(&version, items[0].PolicyVersionID).Error; err != nil {
		return err
	}
	for i := range items {
		if items[i].PolicyVersionID != version.ID {
			return fmt.Errorf("Ops price batch requires one version")
		}
		if err := captureAgencyOpsRecordTx(tx, &items[i], &version, false); err != nil {
			return err
		}
	}
	return nil
}

func captureAgencyOpsRecordTx(tx *gorm.DB, value any, priceVersion *AgencyPricePolicyVersion, deleted bool) error {
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" {
		return nil
	}
	kind := ""
	var ident, revision int64
	switch row := value.(type) {
	case *AgencyUsageFact:
		kind, ident = "usage", row.ID
	case *AgencyTopupFact:
		kind, ident = "topups", row.ID
	case *AgencyCommissionLedger:
		kind, ident, revision = "commissions", row.ID, 1
	case *AgencyCommissionBalance:
		kind, ident, revision = "balances", row.ID, row.Version
	case *AgencyWithdrawal:
		kind, ident, revision = "withdrawals", row.ID, row.Version
	case *AgencyAuditLog:
		kind, ident, revision = "audit", row.ID, 1
	case *AgencyFundingAccount:
		kind, ident = "wallets", row.UserID
	case *AgencyFundingLedger:
		kind, ident, revision = "funding", row.ID, 1
	case *AgencyBillingJournal:
		kind, ident = "journals", row.ID
	case *AgencyFundingLot:
		kind, ident = "fundingLots", row.ID
	case *AgencyFundingAllocation:
		kind, ident = "allocations", row.ID
	case *AgencyFundingDebt:
		kind, ident = "debts", row.ID
	case *AgencyDebtRepayment:
		kind, ident = "repayments", row.ID
	case *AgencyBillingCorrection:
		kind, ident, revision = "corrections", row.ID, 1
	case *AgencyFundingReversal:
		kind, ident, revision = "reversals", row.ID, 1
	case *AgencyFundingReversalChargeRecord:
		kind, ident, revision = "reversalCharges", row.ID, 1
	case *AgencyCreditTerms:
		kind, ident = "creditTerms", row.UserID
	case *AgencyCreditInvoice:
		kind, ident = "creditInvoices", row.ID
	case *AgencyUserBinding:
		kind, ident = "bindings", row.ID
	case *AgencyActiveUserBinding:
		kind, ident = "activeBindings", row.UserID
	case *AgencyCustomerSalesOverride:
		kind, ident = "salesOverrides", row.ID
	case *AgencyDailyStat:
		kind, ident = "dailyStats", row.ID
	case *AgencyPricePolicyVersion:
		kind, ident, revision = "prices", row.ID, 1
	case *AgencyPlatformPriceVersion:
		kind, ident, revision = "platformPrices", row.ID, 1
	case *AgencyPricePolicyItem:
		kind, ident, revision = "priceItems", row.ID, 1
	case *Agency:
		return CaptureAgencyOpsChangeTx(tx, "source_directory", row.ID)
	case *AgencyOperatorAccount:
		return CaptureAgencyOpsChangeTx(tx, "source_credential", row.ID)
	default:
		return fmt.Errorf("unsupported Ops fact type")
	}
	if ident <= 0 {
		return fmt.Errorf("Ops fact identity is required")
	}
	tx = tx.Session(&gorm.Session{NewDB: true})
	if revision == 0 {
		object := AgencyOpsObjectRevision{Kind: kind, ObjectID: ident}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&object).Error; err != nil {
			return err
		}
		if err := tx.Model(&AgencyOpsObjectRevision{}).Where("kind = ? AND object_id = ?", kind, ident).
			Update("version", gorm.Expr("version + ?", 1)).Error; err != nil {
			return err
		}
		if err := tx.Where("kind = ? AND object_id = ?", kind, ident).First(&object).Error; err != nil {
			return err
		}
		revision = object.Version
	}
	s, err := schema.Parse(value, &agencyOpsEventSchemas, tx.NamingStrategy)
	if err != nil {
		return err
	}
	record := make(map[string]any)
	rv := reflect.Indirect(reflect.ValueOf(value))
	for name := range strings.FieldsSeq(agencyOpsFields[kind]) {
		field := s.LookUpField(name)
		if field == nil {
			return fmt.Errorf("Ops contract field is unavailable")
		}
		record[name], _ = field.ValueOf(context.Background(), rv)
	}
	if deleted {
		if kind != "salesOverrides" {
			return fmt.Errorf("unsupported Ops deletion type")
		}
		record["is_deleted"] = true
	}
	// The envelope identity is always the object's primary key. User-keyed
	// tables retain their original record without a fabricated database ID.
	if kind == "repayments" {
		var debt AgencyFundingDebt
		if err := tx.Select("user_id").First(&debt, value.(*AgencyDebtRepayment).DebtID).Error; err != nil {
			return err
		}
		record["_source_user_id"] = debt.UserID
	}
	if kind == "reversalCharges" {
		var reversal AgencyFundingReversal
		if err := tx.Select("user_id").Where("refund_id = ?", value.(*AgencyFundingReversalChargeRecord).RefundID).First(&reversal).Error; err != nil {
			return err
		}
		record["_source_user_id"] = reversal.UserID
	}
	if kind == "audit" {
		// The same immutable audit record is used by historical and live imports.
		row := value.(*AgencyAuditLog)
		for _, field := range []string{"before_json", "after_json"} {
			raw, _ := record[field].(string)
			if raw == "" {
				continue
			}
			var parsed any
			if common.Unmarshal([]byte(raw), &parsed) != nil {
				record[field] = `"[unavailable]"`
				continue
			}
			safe, err := common.Marshal(agencyOpsSafeAudit(parsed))
			if err != nil {
				return err
			}
			record[field] = string(safe)
		}
		record["scope_agency_id"] = row.ActingAgencyID
		if row.ActingAgencyID == nil && row.ActorType == "agency_operator" {
			var operator AgencyOperatorAccount
			if err := tx.Select("agency_id").First(&operator, row.ActorID).Error; err != nil {
				return err
			}
			record["scope_agency_id"] = operator.AgencyID
		}
	}
	if kind == "priceItems" {
		if priceVersion == nil {
			priceVersion = &AgencyPricePolicyVersion{}
			if err := tx.Select("agency_id,created_at_ms").First(priceVersion, value.(*AgencyPricePolicyItem).PolicyVersionID).Error; err != nil {
				return err
			}
		}
		record["agency_id"] = priceVersion.AgencyID
		record["created_at_ms"] = priceVersion.CreatedAtMS
	}
	eventID := "agency-ops-" + common.NewRequestId()
	payload, err := common.Marshal(map[string]any{"eventId": eventID, "kind": kind, "objectId": ident, "revision": revision, "record": record})
	if err != nil {
		return err
	}
	if len(payload) > 480*1024 {
		return fmt.Errorf("Ops fact exceeds delivery size")
	}
	now := time.Now().UnixMilli()
	row := AgencyOpsEvent{EventID: eventID, Payload: AgencyOpsPayload(payload), PayloadHash: fmt.Sprintf("%x", sha256.Sum256(payload)), Status: "queued", NextAtMS: now, CreatedAtMS: now}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	if kind == "audit" {
		if err := captureAgencyOpsAuditChangeTx(tx, value.(*AgencyAuditLog)); err != nil {
			return err
		}
	}
	// This is only a hint: it may arrive before commit or be dropped. Recovery
	// always queries the indexed durable queue, never advances a max-ID cursor.
	select {
	case AgencyOpsEventWake <- struct{}{}:
	default:
	}
	return nil
}

// Management notifications carry no credentials, account details or policies.
// OPS performs a targeted version-checked read, not a directory-wide scan.
func CaptureAgencyOpsChangeTx(tx *gorm.DB, kind string, id int64) error {
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" {
		return nil
	}
	switch kind {
	case "source_directory", "source_price", "source_customer", "source_payout", "source_credential":
	default:
		return fmt.Errorf("unsupported Ops management type")
	}
	if id <= 0 {
		return fmt.Errorf("Ops management identity is required")
	}
	tx = tx.Session(&gorm.Session{NewDB: true})
	object := AgencyOpsObjectRevision{Kind: kind, ObjectID: id}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&object).Error; err != nil {
		return err
	}
	if err := tx.Model(&AgencyOpsObjectRevision{}).Where("kind = ? AND object_id = ?", kind, id).Update("version", gorm.Expr("version + ?", 1)).Error; err != nil {
		return err
	}
	if err := tx.Where("kind = ? AND object_id = ?", kind, id).First(&object).Error; err != nil {
		return err
	}
	eventID := "agency-change-" + common.NewRequestId()
	payload, err := common.Marshal(map[string]any{"eventId": eventID, "kind": kind, "objectId": id, "revision": object.Version, "record": map[string]any{"id": id}})
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	event := AgencyOpsEvent{EventID: eventID, Payload: AgencyOpsPayload(payload), PayloadHash: fmt.Sprintf("%x", sha256.Sum256(payload)), Priority: 1, Status: "queued", NextAtMS: now, CreatedAtMS: now}
	if err := tx.Create(&event).Error; err != nil {
		return err
	}
	select {
	case AgencyOpsEventWake <- struct{}{}:
	default:
	}
	return nil
}

func captureAgencyOpsAuditChangeTx(tx *gorm.DB, row *AgencyAuditLog) error {
	id, err := strconv.ParseInt(row.ObjectID, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	kind := ""
	switch {
	case row.ObjectType == "agency" && strings.HasPrefix(row.Action, "pricing."):
		kind = "source_price"
	case row.ObjectType == "user_binding" && row.Action == "user.transfer":
		kind = "source_customer"
	case row.ObjectType == "customer_sales" || row.ObjectType == "customer" && strings.HasPrefix(row.Action, "pricing."):
		var binding AgencyUserBinding
		err := tx.Where("user_id = ? AND ended_at_ms IS NULL", id).First(&binding).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		id, kind = binding.AgencyID, "source_price"
	case row.ObjectType == "operator_account" && row.Action == "agency.password_reset":
		kind = "source_credential"
	}
	if kind != "" {
		return CaptureAgencyOpsChangeTx(tx, kind, id)
	}
	return nil
}

func (row *Agency) AfterCreate(tx *gorm.DB) error  { return CaptureAgencyOpsRecordTx(tx, row) }
func (row *Agency) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, row) }
func (row *Agency) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, row) }
func (row *AgencyWithdrawalAccount) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsChangeTx(tx, "source_payout", row.AgencyID)
}
func (row *AgencyWithdrawalAccount) AfterUpdate(tx *gorm.DB) error {
	// Superseding a set is followed by creating its replacement in the same
	// transaction. The replacement hook carries the agency identity.
	if row.AgencyID == 0 {
		return nil
	}
	return CaptureAgencyOpsChangeTx(tx, "source_payout", row.AgencyID)
}
func (row *AgencyUserBinding) AfterCreate(tx *gorm.DB) error {
	if err := CaptureAgencyOpsRecordTx(tx, row); err != nil {
		return err
	}
	return CaptureAgencyOpsChangeTx(tx, "source_customer", int64(row.UserID))
}

func (row *AgencyOperatorAccount) BeforeUpdate(tx *gorm.DB) error {
	// Login counters and transparent rehashes are deliberately excluded.
	if tx.Statement.Changed("AuthVersion", "Status", "MustChangePassword") {
		tx.Statement.Settings.Store("agency_ops_operator_changed", true)
		return agencyOpsBeforeUpdate(tx, row)
	}
	return nil
}

func (row *AgencyOperatorAccount) AfterUpdate(tx *gorm.DB) error {
	if changed, _ := tx.Statement.Settings.Load("agency_ops_operator_changed"); changed == true {
		return agencyOpsAfterUpdate(tx, row)
	}
	return nil
}

func (row *AgencyUsageFact) AfterCreate(tx *gorm.DB) error { return CaptureAgencyOpsRecordTx(tx, row) }
func (row *AgencyCommissionLedger) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, row)
}
func (row *AgencyTopupFact) AfterCreate(tx *gorm.DB) error  { return CaptureAgencyOpsRecordTx(tx, row) }
func (row *AgencyTopupFact) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, row) }
func (row *AgencyTopupFact) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, row) }
func (row *AgencyUsageFact) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, row) }
func (row *AgencyUsageFact) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, row) }
func (row *AgencyAuditLog) AfterCreate(tx *gorm.DB) error   { return CaptureAgencyOpsRecordTx(tx, row) }

func (row *AgencyCommissionBalance) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, row)
}
func (row *AgencyCommissionBalance) BeforeUpdate(tx *gorm.DB) error {
	return agencyOpsBeforeUpdate(tx, row)
}
func (row *AgencyCommissionBalance) AfterUpdate(tx *gorm.DB) error {
	return agencyOpsAfterUpdate(tx, row)
}

func (row *AgencyWithdrawal) AfterCreate(tx *gorm.DB) error  { return CaptureAgencyOpsRecordTx(tx, row) }
func (row *AgencyWithdrawal) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, row) }
func (row *AgencyWithdrawal) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, row) }

// Some existing writers use Model(&T{}).Where(primaryKey, ...). BeforeUpdate
// freezes exactly the matched identities, before a version predicate changes.
// It never parses SQL, scans history, or expands an unbounded update.
func agencyOpsBeforeUpdate(tx *gorm.DB, value any) error {
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" {
		return nil
	}
	s, err := schema.Parse(value, &agencyOpsEventSchemas, tx.NamingStrategy)
	if err != nil {
		return err
	}
	if len(s.PrimaryFields) != 1 {
		return fmt.Errorf("Ops object requires one primary key")
	}
	key := s.PrimaryFields[0]
	_, zero := key.ValueOf(context.Background(), reflect.Indirect(reflect.ValueOf(value)))
	if !zero {
		return nil
	}
	where, ok := tx.Statement.Clauses["WHERE"]
	if !ok {
		return fmt.Errorf("Ops update requires a bounded predicate")
	}
	var ids []int64
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(value).Where(where.Expression).Limit(101).Pluck(key.DBName, &ids).Error; err != nil {
		return err
	}
	if len(ids) > 100 {
		return fmt.Errorf("Ops update exceeds capture batch")
	}
	tx.Statement.Settings.Store("agency_ops_ids_"+s.Table, ids)
	return nil
}

func agencyOpsAfterUpdate(tx *gorm.DB, value any) error {
	// GORM invokes hooks with a NewDB session whose RowsAffected is zero.
	// The statement still owns the original update result.
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" || tx.Statement.DB.RowsAffected == 0 {
		return nil
	}
	s, err := schema.Parse(value, &agencyOpsEventSchemas, tx.NamingStrategy)
	if err != nil {
		return err
	}
	key := s.PrimaryFields[0]
	ident, zero := key.ValueOf(context.Background(), reflect.Indirect(reflect.ValueOf(value)))
	var ids []int64
	if !zero {
		ids = []int64{reflect.ValueOf(ident).Int()}
	} else if found, ok := tx.Statement.Settings.Load("agency_ops_ids_" + s.Table); ok {
		ids = found.([]int64)
	} else {
		return fmt.Errorf("Ops update identity was not captured")
	}
	if len(ids) == 0 {
		return nil
	}
	rows := reflect.New(reflect.SliceOf(reflect.TypeOf(value)))
	if err := tx.Session(&gorm.Session{NewDB: true}).Where(key.DBName+" IN ?", ids).Find(rows.Interface()).Error; err != nil {
		return err
	}
	for i := range rows.Elem().Len() {
		if err := CaptureAgencyOpsRecordTx(tx, rows.Elem().Index(i).Interface()); err != nil {
			return err
		}
	}
	return nil
}

func (r *AgencyFundingAccount) AfterCreate(tx *gorm.DB) error  { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyFundingAccount) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyFundingAccount) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyFundingLedger) AfterCreate(tx *gorm.DB) error   { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyBillingJournal) AfterCreate(tx *gorm.DB) error  { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyBillingJournal) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyBillingJournal) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyFundingLot) AfterCreate(tx *gorm.DB) error      { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyFundingLot) BeforeUpdate(tx *gorm.DB) error     { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyFundingLot) AfterUpdate(tx *gorm.DB) error      { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyFundingAllocation) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
func (r *AgencyFundingAllocation) BeforeUpdate(tx *gorm.DB) error {
	return agencyOpsBeforeUpdate(tx, r)
}
func (r *AgencyFundingAllocation) AfterUpdate(tx *gorm.DB) error { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyFundingDebt) AfterCreate(tx *gorm.DB) error       { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyFundingDebt) BeforeUpdate(tx *gorm.DB) error      { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyFundingDebt) AfterUpdate(tx *gorm.DB) error       { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyDebtRepayment) AfterCreate(tx *gorm.DB) error     { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyDebtRepayment) BeforeUpdate(tx *gorm.DB) error    { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyDebtRepayment) AfterUpdate(tx *gorm.DB) error     { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyBillingCorrection) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
func (r *AgencyFundingReversal) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
func (r *AgencyFundingReversalChargeRecord) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
func (r *AgencyCreditTerms) AfterCreate(tx *gorm.DB) error    { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyCreditTerms) BeforeUpdate(tx *gorm.DB) error   { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyCreditTerms) AfterUpdate(tx *gorm.DB) error    { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyCreditInvoice) AfterCreate(tx *gorm.DB) error  { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyCreditInvoice) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyCreditInvoice) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyUserBinding) BeforeUpdate(tx *gorm.DB) error   { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyUserBinding) AfterUpdate(tx *gorm.DB) error    { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyActiveUserBinding) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
func (r *AgencyActiveUserBinding) BeforeUpdate(tx *gorm.DB) error {
	return agencyOpsBeforeUpdate(tx, r)
}
func (r *AgencyActiveUserBinding) AfterUpdate(tx *gorm.DB) error { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyCustomerSalesOverride) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
func (r *AgencyCustomerSalesOverride) BeforeUpdate(tx *gorm.DB) error {
	return agencyOpsBeforeUpdate(tx, r)
}
func (r *AgencyCustomerSalesOverride) AfterUpdate(tx *gorm.DB) error {
	return agencyOpsAfterUpdate(tx, r)
}
func (r *AgencyDailyStat) AfterCreate(tx *gorm.DB) error  { return CaptureAgencyOpsRecordTx(tx, r) }
func (r *AgencyDailyStat) BeforeUpdate(tx *gorm.DB) error { return agencyOpsBeforeUpdate(tx, r) }
func (r *AgencyDailyStat) AfterUpdate(tx *gorm.DB) error  { return agencyOpsAfterUpdate(tx, r) }
func (r *AgencyPricePolicyVersion) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
func (r *AgencyPlatformPriceVersion) AfterCreate(tx *gorm.DB) error {
	return CaptureAgencyOpsRecordTx(tx, r)
}
