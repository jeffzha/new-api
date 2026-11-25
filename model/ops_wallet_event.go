package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOPSLocalWalletWrite = errors.New("OPS owns this wallet; local balance writes are disabled")

// Capture this at the business transaction, then carry it to post-commit audit
// and cache work. A later process flag cannot change who received the credit.
func walletFundingOwner() string {
	if opsauthority.FundingEnabled() {
		return "ops_funding"
	}
	return "gateway"
}

// OPSWalletEvent is the transactional outbox for funding owned by OPS. The
// local order or redemption and this immutable event commit together; User.Quota
// is not credited. A delivery retry always sends the same stored body.
type OPSWalletEvent struct {
	ID          int64  `gorm:"primaryKey;index:idx_ops_wallet_pending,priority:3"`
	Scope       string `gorm:"size:80;not null;index:idx_ops_wallet_pending,priority:1"`
	EventID     string `gorm:"size:80;not null;uniqueIndex:uidx_ops_wallet_event"`
	Payload     string `gorm:"type:text;not null"`
	PayloadHash string `gorm:"size:64;not null"`
	IntentHash  string `gorm:"size:64;not null"`
	CreatedAt   int64  `gorm:"not null"`
	DeliveredAt int64  `gorm:"not null;index:idx_ops_wallet_pending,priority:2"`
}

func (OPSWalletEvent) TableName() string { return "ops_wallet_events" }

func opsWalletFundingEventID(scope, source, sourceID string) string {
	return fmt.Sprintf("wf-%x", sha256.Sum256([]byte(scope+"\x00"+source+"\x00"+sourceID)))
}

type OPSFundingSchemaReceipt struct {
	Created []string `json:"created"`
	Skipped []string `json:"skipped"`
}

// PrepareOPSWalletFundingSchema is an explicit release operation. It only
// creates the additive outbox and missing indexes, never modifies user money or
// rewrites an existing index. A per-database maintenance lock serializes slots.
func PrepareOPSWalletFundingSchema(ctx context.Context, db *gorm.DB) error {
	_, err := PrepareOPSWalletFundingSchemaReceipt(ctx, db)
	return err
}

// The created/skipped inventory is read under the same maintenance lock as
// preparation, so two release operators cannot both claim a new index.
func PrepareOPSWalletFundingSchemaReceipt(ctx context.Context, db *gorm.DB) (*OPSFundingSchemaReceipt, error) {
	if db == nil {
		return nil, errors.New("OPS funding preparation: nil database")
	}
	receipt := &OPSFundingSchemaReceipt{Created: []string{}, Skipped: []string{}}
	err := db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		switch conn.Dialector.Name() {
		case "postgres":
			var acquired bool
			if err := conn.Raw("SELECT pg_try_advisory_lock(?)", int64(75820291)).Scan(&acquired).Error; err != nil {
				return err
			}
			if !acquired {
				return errors.New("OPS funding preparation already running")
			}
			defer conn.WithContext(context.Background()).Exec("SELECT pg_advisory_unlock(?)", int64(75820291))
		case "mysql":
			var acquired int
			if err := conn.Raw("SELECT GET_LOCK(?, 0)", "new_api_ops_wallet_events").Scan(&acquired).Error; err != nil {
				return err
			}
			if acquired != 1 {
				return errors.New("OPS funding preparation already running")
			}
			defer conn.WithContext(context.Background()).Exec("SELECT RELEASE_LOCK(?)", "new_api_ops_wallet_events")
		case "sqlite":
			// SQLite serializes DDL with its database write transaction.
			return conn.Transaction(func(tx *gorm.DB) error { return prepareOPSWalletFundingIndexes(tx, receipt) })
		default:
			return errors.New("OPS funding preparation: unsupported database")
		}
		return prepareOPSWalletFundingIndexes(conn, receipt)
	})
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

func prepareOPSWalletFundingIndexes(db *gorm.DB, receipt *OPSFundingSchemaReceipt) error {
	db = db.Session(&gorm.Session{NewDB: true})
	if !db.Migrator().HasTable(&OPSWalletEvent{}) {
		if err := db.Migrator().CreateTable(&OPSWalletEvent{}); err != nil {
			return err
		}
		receipt.Created = append(receipt.Created, "uidx_ops_wallet_event", "idx_ops_wallet_pending")
		return VerifyOPSWalletFundingSchema(db)
	}
	if err := verifyOPSWalletFundingColumns(db); err != nil {
		return err
	}
	// Validate every existing definition before creating anything. A bad
	// second index must not leave a partially prepared first index behind.
	if err := verifyOPSWalletFundingIndexes(db, false); err != nil {
		return err
	}
	for _, name := range []string{"uidx_ops_wallet_event", "idx_ops_wallet_pending"} {
		if db.Migrator().HasIndex(&OPSWalletEvent{}, name) {
			receipt.Skipped = append(receipt.Skipped, name)
			continue
		}
		if db.Dialector.Name() == "postgres" {
			// An existing outbox may already have traffic; do not block its
			// writers with ordinary CREATE INDEX or wrap this in a transaction.
			sql := "CREATE INDEX CONCURRENTLY idx_ops_wallet_pending ON ops_wallet_events (scope, delivered_at, id)"
			if name == "uidx_ops_wallet_event" {
				sql = "CREATE UNIQUE INDEX CONCURRENTLY uidx_ops_wallet_event ON ops_wallet_events (event_id)"
			}
			if err := db.Exec(sql).Error; err != nil {
				return err
			}
		} else if db.Dialector.Name() == "mysql" {
			query := "ALTER TABLE ops_wallet_events ADD INDEX idx_ops_wallet_pending (scope, delivered_at, id), ALGORITHM=INPLACE, LOCK=NONE"
			if name == "uidx_ops_wallet_event" {
				query = "ALTER TABLE ops_wallet_events ADD UNIQUE INDEX uidx_ops_wallet_event (event_id), ALGORITHM=INPLACE, LOCK=NONE"
			}
			if err := db.Exec(query).Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&OPSWalletEvent{}, name); err != nil {
				return err
			}
		}
		receipt.Created = append(receipt.Created, name)
	}
	return VerifyOPSWalletFundingSchema(db)
}

// VerifyOPSWalletFundingSchema is a read-only candidate gate. Schema preparation
// belongs to the release migration, never to every sender's HTTP startup.
func VerifyOPSWalletFundingSchema(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&OPSWalletEvent{}) {
		return errors.New("OPS funding outbox requires release preparation")
	}
	if err := verifyOPSWalletFundingColumns(db); err != nil {
		return err
	}
	return verifyOPSWalletFundingIndexes(db, true)
}

func verifyOPSWalletFundingIndexes(db *gorm.DB, required bool) error {
	db = db.Session(&gorm.Session{NewDB: true})
	if db.Dialector.Name() == "sqlite" {
		// The driver calls Debug() inside GetIndexes, contaminating the JSON
		// release receipt with SQL output even when logging is disabled. Read
		// the catalogue directly and also verify ordered, complete index keys.
		var indexes []struct {
			Name              string
			IsUnique, Partial bool
		}
		if err := db.Raw(`SELECT name, "unique" AS is_unique, partial FROM pragma_index_list('ops_wallet_events')
			WHERE name IN ('uidx_ops_wallet_event','idx_ops_wallet_pending')`).Scan(&indexes).Error; err != nil {
			return err
		}
		for _, index := range indexes {
			expected, unique := []string{"scope", "delivered_at", "id"}, false
			if index.Name == "uidx_ops_wallet_event" {
				expected, unique = []string{"event_id"}, true
			}
			var keys []struct {
				Name       string
				Cid        int
				Descending bool
			}
			if err := db.Raw(`SELECT name, cid, "desc" AS descending FROM pragma_index_xinfo(?) WHERE "key"=1 ORDER BY seqno`, index.Name).Scan(&keys).Error; err != nil {
				return err
			}
			if index.Partial || index.IsUnique != unique || len(keys) != len(expected) {
				return fmt.Errorf("OPS funding index definition conflict: %s", index.Name)
			}
			for i, key := range keys {
				if key.Cid < 0 || key.Descending || key.Name != expected[i] {
					return fmt.Errorf("OPS funding index definition conflict: %s", index.Name)
				}
			}
		}
		if required && len(indexes) != 2 {
			return errors.New("OPS funding required indexes missing")
		}
		return nil
	}
	if db.Dialector.Name() == "postgres" {
		// This driver's GetIndexes returns columns in table order instead of
		// index-key order. Read ordinality from PostgreSQL's catalogue directly.
		var indexes []struct {
			Name, Columns, Options string
			IsUnique, Ready        bool
		}
		err := db.Raw(`SELECT c.relname AS name, i.indisunique AS is_unique,
			i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indexprs IS NULL AS ready,
			i.indoption::text AS options,
			(SELECT string_agg(a.attname, ',' ORDER BY k.ordinality)
			FROM unnest(i.indkey) WITH ORDINALITY k(attnum, ordinality)
			JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum) AS columns
			FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid
			WHERE i.indrelid=to_regclass('ops_wallet_events')
			AND c.relname IN ('uidx_ops_wallet_event','idx_ops_wallet_pending')`).Scan(&indexes).Error
		if err != nil {
			return err
		}
		for _, index := range indexes {
			columns, options, unique := "scope,delivered_at,id", "0 0 0", false
			if index.Name == "uidx_ops_wallet_event" {
				columns, options, unique = "event_id", "0", true
			}
			if !index.Ready || index.IsUnique != unique || index.Columns != columns || index.Options != options {
				return fmt.Errorf("OPS funding index definition conflict: %s", index.Name)
			}
		}
		if required && len(indexes) != 2 {
			return errors.New("OPS funding required indexes missing")
		}
		return nil
	}
	indexes, err := db.Migrator().GetIndexes(&OPSWalletEvent{})
	if err != nil {
		return err
	}
	missing := ""
	for _, expected := range []struct {
		name    string
		columns []string
		unique  bool
	}{
		{"uidx_ops_wallet_event", []string{"event_id"}, true},
		{"idx_ops_wallet_pending", []string{"scope", "delivered_at", "id"}, false},
	} {
		found := false
		for _, actual := range indexes {
			if actual.Name() != expected.name {
				continue
			}
			unique, known := actual.Unique()
			if !known || unique != expected.unique || !slices.Equal(actual.Columns(), expected.columns) {
				return fmt.Errorf("OPS funding index definition conflict: %s", expected.name)
			}
			found = true
		}
		if required && !found {
			missing = expected.name
		}
	}
	if db.Dialector.Name() == "sqlite" || db.Dialector.Name() == "mysql" {
		var invalid int64
		if db.Dialector.Name() == "sqlite" {
			err = db.Raw(`SELECT count(*) FROM pragma_index_list('ops_wallet_events')
				WHERE name IN ('uidx_ops_wallet_event','idx_ops_wallet_pending') AND partial <> 0`).Scan(&invalid).Error
		} else {
			err = db.Raw(`SELECT count(*) FROM information_schema.statistics
				WHERE table_schema=DATABASE() AND table_name='ops_wallet_events'
				AND index_name IN ('uidx_ops_wallet_event','idx_ops_wallet_pending')
				AND (sub_part IS NOT NULL OR index_type <> 'BTREE')`).Scan(&invalid).Error
		}
		if err != nil {
			return err
		}
		if invalid != 0 {
			return errors.New("OPS funding index is invalid, not ready or partial")
		}
	}
	if missing != "" {
		return fmt.Errorf("OPS funding required index missing: %s", missing)
	}
	return nil
}

func verifyOPSWalletFundingColumns(db *gorm.DB) error {
	db = db.Session(&gorm.Session{NewDB: true})
	columns, err := db.Migrator().ColumnTypes(&OPSWalletEvent{})
	if err != nil {
		return err
	}
	for _, expected := range []struct {
		name   string
		number bool
		size   int64
	}{
		{"id", true, 0}, {"scope", false, 80}, {"event_id", false, 80},
		{"payload", false, 0}, {"payload_hash", false, 64}, {"intent_hash", false, 64},
		{"created_at", true, 0}, {"delivered_at", true, 0},
	} {
		i := slices.IndexFunc(columns, func(column gorm.ColumnType) bool { return column.Name() == expected.name })
		if i < 0 {
			return fmt.Errorf("OPS funding required column missing: %s", expected.name)
		}
		column := columns[i]
		primary, known := column.PrimaryKey()
		if !known || primary != (expected.name == "id") {
			return fmt.Errorf("OPS funding primary key conflict: %s", expected.name)
		}
		// SQLite reports INTEGER PRIMARY KEY as nullable despite rowid
		// semantics. All non-key fields must explicitly reject NULL.
		if nullable, known := column.Nullable(); expected.name != "id" && (!known || nullable) {
			return fmt.Errorf("OPS funding nullability conflict: %s", expected.name)
		}
		typeName := strings.ToLower(column.DatabaseTypeName())
		if expected.number {
			valid := typeName == "bigint" || typeName == "int8" || db.Dialector.Name() == "sqlite" && typeName == "integer"
			full, _ := column.ColumnType()
			if !valid || strings.Contains(strings.ToLower(full), "unsigned") {
				return fmt.Errorf("OPS funding integer type conflict: %s", expected.name)
			}
			continue
		}
		if typeName == "text" {
			continue
		}
		length, known := column.Length()
		if expected.size == 0 || typeName != "varchar" && typeName != "character varying" || !known || length < expected.size {
			return fmt.Errorf("OPS funding string type conflict: %s", expected.name)
		}
	}
	return nil
}

type OPSFundingFacts struct {
	TradeNo          string `json:"tradeNo,omitempty"`
	PaymentMethod    string `json:"paymentMethod,omitempty"`
	PaymentReference string `json:"paymentReference,omitempty"`
	PaidMoney        string `json:"paidMoney,omitempty"`
	PaidCurrency     string `json:"paidCurrency,omitempty"`
	MoneyCNY         string `json:"moneyCny,omitempty"`
}

// Validate payment evidence both before commit and before delivery. The paid
// amount is independent of the credited quota: bonuses must not be represented
// as customer cash. Older events may omit a provider reference or paid amount.
func (facts *OPSFundingFacts) validate(sourceID string) error {
	if facts.TradeNo != sourceID || facts.TradeNo == "" || facts.TradeNo != strings.TrimSpace(facts.TradeNo) || len(facts.TradeNo) > 200 || len(facts.PaymentMethod) > 60 || len(facts.PaymentReference) > 191 {
		return errors.New("invalid OPS payment provenance")
	}
	reference := facts.PaymentReference
	if reference == "" {
		// This is only a validation input, never an invented provider receipt.
		reference = "validation-only"
	}
	payment, err := (TopupPaymentSnapshot{ActualMoney: facts.PaidMoney, CurrencyCode: facts.PaidCurrency, PaymentReference: reference}).normalized()
	if err != nil || payment.ActualMoney != facts.PaidMoney || payment.CurrencyCode != facts.PaidCurrency || payment.PaymentReference != reference {
		return errors.New("invalid OPS payment provenance")
	}
	if facts.PaidMoney != "" {
		paid, err := decimal.NewFromString(facts.PaidMoney)
		if err != nil || paid.GreaterThan(decimal.NewFromInt(1_000_000_000)) || !paid.Equal(paid.Truncate(6)) {
			return errors.New("invalid OPS payment amount")
		}
	}
	if facts.MoneyCNY != "" && (facts.PaidCurrency != "CNY" || facts.MoneyCNY != facts.PaidMoney) {
		return errors.New("invalid OPS CNY payment provenance")
	}
	return nil
}

// OPSWalletFundingTarget binds durable credits to one credential-owned ledger.
// Secret rotation does not change identity; changing the host, proxy prefix or
// key requires an explicit reconciliation of pending events.
func OPSWalletFundingTarget(base, key string) (string, error) {
	return opsauthority.Target(base, key)
}

// QueueOPSWalletFundingTx must run inside the business transaction after its
// provider/code validation. The source ID is an order/code ID, never a secret.
func QueueOPSWalletFundingTx(tx *gorm.DB, userID, quota int64, kind, source, sourceID string, facts *OPSFundingFacts) error {
	if !opsauthority.Enabled() || !opsauthority.FundingEnabled() {
		return errors.New("OPS native funding is not enabled")
	}
	if tx == nil || userID <= 0 || quota <= 0 || quota > common.MaxWalletQuota || kind != "topup" && kind != "gift" || source == "" || len(source) > 32 || sourceID == "" || len(sourceID) > 255 || strings.ContainsRune(sourceID, 0) || !opsauthority.Product() && opsauthority.Scope() == "" {
		return errors.New("invalid OPS funding event")
	}
	target, err := OPSWalletFundingTarget(os.Getenv("OPS_PUSH_URL"), os.Getenv("OPS_PUSH_KEY_ID"))
	if err != nil {
		return err
	}
	if facts != nil {
		if kind != "topup" {
			return errors.New("invalid OPS payment provenance")
		}
		if err := facts.validate(sourceID); err != nil {
			return err
		}
	}
	// Compare the business intent before consulting mutable conversion settings.
	// Replaying a committed payment keeps its original amount and conversion.
	intent, err := common.Marshal(map[string]any{"scope": opsauthority.Scope(), "target": target, "userId": userID, "quota": quota, "kind": kind, "source": source, "sourceId": sourceID, "facts": facts})
	if err != nil {
		return err
	}
	intentHash := fmt.Sprintf("%x", sha256.Sum256(intent))
	var user User
	if err := lockForUpdate(tx).Select("id").First(&user, userID).Error; err != nil {
		return err
	}
	id := opsWalletFundingEventID(opsauthority.Scope(), source, sourceID)
	var existing OPSWalletEvent
	err = tx.Where("event_id = ?", id).First(&existing).Error
	if err == nil {
		if existing.Scope != opsauthority.Scope() || existing.IntentHash != intentHash || ValidateOPSWalletFunding(existing) != nil {
			return errors.New("OPS funding identity conflict")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	unit := decimal.NewFromFloat(common.QuotaPerUnit)
	if !unit.IsPositive() {
		return errors.New("invalid OPS funding quota unit")
	}
	rate := decimal.NewFromFloat(operation_setting.USDExchangeRate)
	if !rate.IsPositive() {
		return errors.New("invalid OPS funding exchange rate")
	}
	amount := decimal.NewFromInt(quota).Mul(rate).Div(unit)
	if amount.GreaterThan(decimal.NewFromInt(1_000_000_000)) || !unit.Equal(unit.Truncate(0)) {
		return errors.New("OPS funding exceeds receiver precision or range")
	}
	extra := map[string]any{"recordKind": kind, "direction": "credit", "fundingSource": source, "fundingSourceId": sourceID, "quotaPerUnit": unit.IntPart(), "usdExchangeRate": rate.String()}
	extra["walletFunding"] = map[string]any{"version": 1, "scope": opsauthority.Scope(), "target": target}
	if facts != nil {
		extra["topup"] = facts
	}
	// occurredAt is frozen before any remote I/O. It must survive retries and
	// process restarts so a lost ACK cannot turn one credit into a new event.
	at := common.GetTimestamp()
	// CNY uses the existing v1.1 quota/conversion contract. The USD native
	// currency/amount fields are rejected by the CNY receiver.
	event := map[string]any{"requestId": id, "userId": fmt.Sprint(userID), "occurredAt": time.Unix(at, 0).UTC().Format(time.RFC3339), "status": "success", "gatewayQuota": quota, "extra": extra}
	body, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": id, "events": []any{event}})
	if err != nil || len(body) > 16*1024 {
		return errors.New("invalid OPS funding payload")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	row := OPSWalletEvent{Scope: opsauthority.Scope(), EventID: id, Payload: string(body), PayloadHash: hash, IntentHash: intentHash, CreatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := tx.Where("event_id = ?", id).First(&existing).Error; err != nil {
		return err
	}
	if existing.Scope != opsauthority.Scope() || existing.IntentHash != intentHash || ValidateOPSWalletFunding(existing) != nil {
		return errors.New("OPS funding identity conflict")
	}
	return nil
}

// QueueOPSWalletRefundTx durably requests an OPS refund for an accepted task.
// It remains usable after the new-call authority flag changes. The original
// ledger binding, rather than the active credentials, selects its destination.
func QueueOPSWalletRefundTx(tx *gorm.DB, userID, quota int64, binding *opsauthority.Binding, sourceID, originalRequest, taskID, modelName string) error {
	if tx == nil || userID <= 0 || quota <= 0 || quota > common.MaxQuota || binding == nil || binding.Version != 1 || len(binding.Target) != 64 || len(binding.Scope) > 80 || sourceID == "" || len(sourceID) > 80 || originalRequest == "" || len(originalRequest) > 64 || modelName == "" || len(modelName) > 200 || taskID == "" || len(taskID) > 200 {
		return errors.New("invalid OPS task refund")
	}
	const source = "midjourney_refund"
	id := opsWalletFundingEventID(binding.Scope, source, sourceID)
	intent, err := common.Marshal(map[string]any{"binding": binding, "userId": userID, "quota": quota, "sourceId": sourceID, "requestId": originalRequest, "taskId": taskID, "model": modelName})
	if err != nil {
		return err
	}
	intentHash := fmt.Sprintf("%x", sha256.Sum256(intent))
	var user User
	if err := lockForUpdate(tx).Select("id").First(&user, userID).Error; err != nil {
		return err
	}
	var existing OPSWalletEvent
	err = tx.Where("event_id = ?", id).First(&existing).Error
	if err == nil {
		if existing.Scope != binding.Scope || existing.IntentHash != intentHash || ValidateOPSWalletFunding(existing) != nil {
			return errors.New("OPS task refund identity conflict")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	at := common.GetTimestamp()
	event := map[string]any{"requestId": id, "userId": strconv.FormatInt(userID, 10), "model": modelName, "occurredAt": time.Unix(at, 0).UTC().Format(time.RFC3339), "status": "success", "gatewayQuota": quota, "extra": map[string]any{"recordKind": "refund", "direction": "credit", "fundingSource": source, "fundingSourceId": sourceID, "request_id": originalRequest, "task_id": taskID, "settlement_stage": "refund", "billing_source": "ops", "billing_scope": binding.Scope, "walletFunding": binding}}
	body, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": id, "events": []any{event}})
	if err != nil || len(body) > 16*1024 {
		return errors.New("invalid OPS task refund payload")
	}
	row := OPSWalletEvent{Scope: binding.Scope, EventID: id, Payload: string(body), PayloadHash: fmt.Sprintf("%x", sha256.Sum256(body)), IntentHash: intentHash, CreatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := tx.Where("event_id = ?", id).First(&existing).Error; err != nil {
		return err
	}
	if existing.Scope != binding.Scope || existing.IntentHash != intentHash || ValidateOPSWalletFunding(existing) != nil {
		return errors.New("OPS task refund identity conflict")
	}
	return nil
}

// ReadOPSWalletFunding returns only committed immutable events. Multiple senders
// are safe: the receiver deduplicates the stable event and batch identities.
func ReadOPSWalletFunding(db *gorm.DB, limit int) ([]OPSWalletEvent, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid OPS funding read limit")
	}
	var rows []OPSWalletEvent
	err := db.Where("scope = ? AND delivered_at = ?", opsauthority.FinancialOutboxScope(), 0).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

func AcknowledgeOPSWalletFunding(db *gorm.DB, row OPSWalletEvent, at int64) error {
	if row.ID <= 0 || row.EventID == "" || len(row.PayloadHash) != 64 || at <= 0 {
		return errors.New("invalid OPS funding acknowledgement")
	}
	result := db.Model(&OPSWalletEvent{}).Where("id = ? AND scope = ? AND event_id = ? AND payload_hash = ? AND delivered_at = ?", row.ID, row.Scope, row.EventID, row.PayloadHash, 0).Update("delivered_at", at)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := db.Model(&OPSWalletEvent{}).Where("id = ? AND scope = ? AND event_id = ? AND payload_hash = ? AND delivered_at > ?", row.ID, row.Scope, row.EventID, row.PayloadHash, 0).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("OPS funding acknowledgement conflict")
	}
	return nil
}

func ValidateOPSWalletFunding(row OPSWalletEvent) error {
	if row.ID <= 0 || row.CreatedAt <= 0 || len(row.Payload) > 16*1024 || len(row.IntentHash) != 64 || row.PayloadHash != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Payload))) {
		return errors.New("OPS funding payload changed")
	}
	var body struct {
		Version string `json:"schemaVersion"`
		BatchID string `json:"batchId"`
		Events  []struct {
			RequestID  string `json:"requestId"`
			UserID     string `json:"userId"`
			OccurredAt string `json:"occurredAt"`
			Status     string `json:"status"`
			Quota      int64  `json:"gatewayQuota"`
			Model      string `json:"model"`
			Extra      struct {
				Kind      string           `json:"recordKind"`
				Direction string           `json:"direction"`
				Source    string           `json:"fundingSource"`
				SourceID  string           `json:"fundingSourceId"`
				Facts     *OPSFundingFacts `json:"topup"`
				RequestID string           `json:"request_id"`
				TaskID    string           `json:"task_id"`
				Stage     string           `json:"settlement_stage"`
				Owner     string           `json:"billing_source"`
				Scope     string           `json:"billing_scope"`
				Binding   struct {
					Version int    `json:"version"`
					Scope   string `json:"scope"`
					Target  string `json:"target"`
				} `json:"walletFunding"`
			} `json:"extra"`
		} `json:"events"`
	}
	if common.UnmarshalJsonStr(row.Payload, &body) != nil || body.Version != "1.1" || body.BatchID != row.EventID || len(body.Events) != 1 {
		return errors.New("invalid OPS funding envelope")
	}
	e := body.Events[0]
	userID, err := strconv.ParseInt(e.UserID, 10, 64)
	if err != nil || userID <= 0 || e.Quota <= 0 || e.Quota > common.MaxWalletQuota || e.Status != "success" || e.Extra.Direction != "credit" || e.Extra.Kind != "topup" && e.Extra.Kind != "gift" && e.Extra.Kind != "refund" || e.Extra.Binding.Version != 1 || e.Extra.Binding.Scope != row.Scope || len(e.Extra.Binding.Target) != 64 || e.OccurredAt != time.Unix(row.CreatedAt, 0).UTC().Format(time.RFC3339) {
		return errors.New("invalid OPS funding facts")
	}
	if e.Extra.Facts != nil {
		if e.Extra.Kind != "topup" {
			return errors.New("invalid OPS payment provenance")
		}
		if err := e.Extra.Facts.validate(e.Extra.SourceID); err != nil {
			return err
		}
	}
	id := fmt.Sprintf("wf-%x", sha256.Sum256([]byte(row.Scope+"\x00"+e.Extra.Source+"\x00"+e.Extra.SourceID)))
	intent, err := common.Marshal(map[string]any{"scope": row.Scope, "target": e.Extra.Binding.Target, "userId": userID, "quota": e.Quota, "kind": e.Extra.Kind, "source": e.Extra.Source, "sourceId": e.Extra.SourceID, "facts": e.Extra.Facts})
	if e.Extra.Kind == "refund" {
		if e.Quota > common.MaxQuota || e.Extra.Source != "midjourney_refund" || e.Extra.SourceID == "" || len(e.Extra.SourceID) > 80 || e.Model == "" || len(e.Model) > 200 || e.Extra.RequestID == "" || len(e.Extra.RequestID) > 64 || e.Extra.TaskID == "" || len(e.Extra.TaskID) > 200 || e.Extra.Stage != "refund" || e.Extra.Owner != "ops" || e.Extra.Scope != row.Scope || e.Extra.Facts != nil {
			return errors.New("invalid OPS task refund facts")
		}
		intent, err = common.Marshal(map[string]any{"binding": e.Extra.Binding, "userId": userID, "quota": e.Quota, "sourceId": e.Extra.SourceID, "requestId": e.Extra.RequestID, "taskId": e.Extra.TaskID, "model": e.Model})
	}
	if err != nil || id != row.EventID || e.RequestID != id || row.IntentHash != fmt.Sprintf("%x", sha256.Sum256(intent)) {
		return errors.New("OPS funding intent changed")
	}
	return nil
}
