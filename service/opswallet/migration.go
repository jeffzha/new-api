package opswallet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// OpeningMigration persists the exact frozen input before any remote write.
// Retrying uses identical batch IDs, bodies and timestamps. No recovery path
// takes custody back or removes source guards automatically.
type OpeningMigration struct {
	HistoricalReviewSHA256 string          `json:"historicalReviewSha256,omitempty"`
	Version                int             `json:"version"`
	Scope                  string          `json:"scope"`
	URL                    string          `json:"url"`
	KeyID                  string          `json:"keyId"`
	FrozenAt               string          `json:"frozenAt"`
	WriterEvidence         string          `json:"writerEvidence"`
	Users                  []MigrationUser `json:"users"`
	SnapshotSHA256         string          `json:"snapshotSha256"`
	Phase                  string          `json:"phase"`
	CustodySince           string          `json:"custodySince,omitempty"`
}

// Only public profile identity and exact native quota are selected, never
// credentials, contact information or payment data. Removed users keep funds.
type MigrationUser struct {
	ID                int                  `json:"id"`
	Quota             int64                `json:"quota"`
	Username          string               `json:"username"`
	Status            int                  `json:"status"`
	DeletedAt         gorm.DeletedAt       `json:"deletedAt"`
	FundingLots       []model.OPSAgencyLot `json:"fundingLots,omitempty" gorm:"-"`
	FundingDebtMicros int64                `json:"fundingDebtMicros,omitempty" gorm:"-"`
}

func sourceMigrationUsers(ctx context.Context, db *gorm.DB) ([]MigrationUser, error) {
	var users []MigrationUser
	err := db.WithContext(ctx).Unscoped().Model(&model.User{}).Select("id, quota, username, status, deleted_at").Order("id").Limit(10001).Find(&users).Error
	if err != nil || len(users) == 0 || len(users) > 10000 {
		return nil, errors.New("cannot snapshot source wallets (supported range: 1..10000 users)")
	}
	for i, u := range users {
		if u.ID <= 0 || u.Quota < -common.MaxWalletQuota || u.Quota > common.MaxWalletQuota {
			return nil, errors.New("invalid source wallet identity or quota")
		}
		lots, debt, err := openingFundingLots(db.WithContext(ctx), u.ID, u.Quota)
		if err != nil {
			return nil, err
		}
		users[i].FundingLots, users[i].FundingDebtMicros = lots, debt
	}
	return users, nil
}

func (m OpeningMigration) snapshotDigest() (string, error) {
	m.Phase, m.CustodySince, m.SnapshotSHA256 = "", "", ""
	raw, err := common.Marshal(m)
	return fmt.Sprintf("%x", sha256.Sum256(raw)), err
}

// SaveMigrationFile atomically replaces a private journal or approval on the
// same filesystem. A failed write leaves the last durable state intact.
func SaveMigrationFile(path string, value any) error {
	raw, err := common.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".ops-opening-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// PrepareOpening requires an offline drain attestation, then installs durable
// guards before taking the snapshot. It does not send anything to OPS.
func PrepareOpening(ctx context.Context, db *gorm.DB, client *opsclient.Client, scope, evidence string, writersStopped bool) (*OpeningMigration, error) {
	if !writersStopped || strings.TrimSpace(evidence) == "" || len(evidence) > 200 || strings.TrimSpace(scope) == "" || len(scope) > 80 || common.QuotaPerUnit != 500_000 || opsclient.ValidateBase(client.BaseURL) != nil || client.KeyID == "" {
		return nil, errors.New("offline writer drain, scope and USD source conversion required")
	}
	if err := ValidateSourceReadiness(ctx, db); err != nil {
		return nil, err
	}
	if os.Getenv("OPS_WALLET_HISTORICAL_REVIEW_FILE") != "" && (scope != os.Getenv("OPS_PROJECT_SCOPE") || client.KeyID != os.Getenv("OPS_PUSH_KEY_ID")) {
		return nil, errors.New("historical review station differs from opening station")
	}
	// Persisted unsent batches or an active sync lease must be drained before
	// freezing. An old profile batch could overwrite the opening timestamp.
	for _, table := range []string{"ops_push_states", "ops_user_sync_states"} {
		if db.Migrator().HasTable(table) {
			var count int64
			if err := db.WithContext(ctx).Table(table).Where("batch_body <> ? OR lease_until >= ?", "", time.Now().Unix()).Count(&count).Error; err != nil || count != 0 {
				return nil, errors.New("source sync batches or leases must drain before opening")
			}
		}
	}
	if err := model.FreezeOPSSourceWallet(db.WithContext(ctx)); err != nil {
		return nil, errors.New("source wallet guard installation failed; keep writers stopped")
	}
	if err := ValidateSourceReadiness(ctx, db); err != nil {
		return nil, err
	}
	users, err := sourceMigrationUsers(ctx, db)
	if err != nil {
		return nil, err
	}
	// OPS ledger timestamps have whole-second precision. Every writer and
	// outstanding upload was drained before this snapshot.
	m := &OpeningMigration{Version: 1, Scope: scope, URL: client.BaseURL, KeyID: client.KeyID, FrozenAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), WriterEvidence: evidence, Users: users, Phase: "prepared"}
	m.HistoricalReviewSHA256, _, err = historicalReview(ctx, db)
	if err != nil {
		return nil, err
	}
	m.SnapshotSHA256, err = m.snapshotDigest()
	return m, err
}

type migrationWallets struct {
	Data *struct {
		Since    string `json:"opsKeepsBalanceSince"`
		Currency string `json:"currency"`
		Station  *struct {
			PerUnit int64  `json:"quotaPerUnit"`
			Rate    string `json:"usdExchangeRate"`
		} `json:"station"`
		Items []struct {
			ID      string      `json:"userId"`
			Balance *string     `json:"balance"`
			Opening *string     `json:"openingBalance"`
			Entry   json.Number `json:"openingEntryId"`
			Source  string      `json:"balanceSource"`
			Unknown *int        `json:"unknownEntries"`
		} `json:"items"`
	} `json:"data"`
}

func readMigrationWallets(ctx context.Context, client *opsclient.Client) (*migrationWallets, error) {
	raw, err := client.Get(ctx, "/api/v1/catalog/v1/wallets", time.Now())
	if err != nil {
		return nil, err
	}
	var wallets migrationWallets
	if common.Unmarshal(raw, &wallets) != nil || wallets.Data == nil || wallets.Data.Currency != "USD" || wallets.Data.Station == nil || wallets.Data.Station.PerUnit != 1_000_000 || wallets.Data.Station.Rate != "1" {
		return nil, errors.New("opening requires native USD OPS wallet contract")
	}
	return &wallets, nil
}

func (m *OpeningMigration) reconcile(wallets *migrationWallets, custody bool) error {
	if len(wallets.Data.Items) != len(m.Users) {
		return errors.New("opening wallet coverage differs; inspect stale or missing OPS users")
	}
	expected := make(map[string]int64, len(m.Users))
	for _, u := range m.Users {
		expected[strconv.Itoa(u.ID)] = u.Quota
	}
	for _, row := range wallets.Data.Items {
		quota, exists := expected[row.ID]
		amount := row.Balance
		if custody {
			entry, err := row.Entry.Int64()
			if row.Source != "ops" || err != nil || entry <= 0 {
				return errors.New("OPS opening ledger evidence missing")
			}
			amount = row.Opening
		} else if row.Source != "station" || row.Unknown == nil || *row.Unknown != 0 {
			return errors.New("source wallet snapshot incomplete at OPS")
		}
		if !exists || amount == nil || len(*amount) > 40 {
			return errors.New("unapproved opening wallet")
		}
		value, err := decimal.NewFromString(*amount)
		if err != nil || !value.Equal(decimal.NewFromInt(quota).Div(decimal.NewFromInt(500_000))) {
			return errors.New("per-user opening balance differs")
		}
		delete(expected, row.ID)
	}
	return nil
}

// ApplyOpening is resumable. Save must durably persist before it returns. The
// explicit reviewed snapshot digest pins the source account and station set.
// Stop ALL writers throughout, including OPS admin adjustments/collectors.
func ApplyOpening(ctx context.Context, db *gorm.DB, client *opsclient.Client, m *OpeningMigration, reviewedSHA, approvedBy string, save func(*OpeningMigration) error) (*CutoverApproval, error) {
	heldSHA, _, heldErr := historicalReview(ctx, db)
	if heldErr != nil || heldSHA != m.HistoricalReviewSHA256 {
		return nil, errors.New("historical review differs from frozen opening")
	}
	digest, err := m.snapshotDigest()
	if err != nil || m.Version != 1 || reviewedSHA != digest || m.SnapshotSHA256 != digest || m.URL != client.BaseURL || m.KeyID != client.KeyID || strings.TrimSpace(approvedBy) == "" || len(approvedBy) > 100 || common.QuotaPerUnit != 500_000 || save == nil {
		return nil, errors.New("reviewed opening or station identity mismatch")
	}
	switch m.Phase {
	case "prepared", "uploaded", "custody_requested", "verified":
	default:
		return nil, errors.New("invalid opening phase")
	}
	frozen, err := time.Parse(time.RFC3339Nano, m.FrozenAt)
	if err != nil || frozen.After(time.Now()) {
		return nil, errors.New("invalid source freeze timestamp")
	}
	users, err := sourceMigrationUsers(ctx, db)
	if err != nil {
		return nil, err
	}
	raw, _ := common.Marshal(users)
	approved, _ := common.Marshal(m.Users)
	if string(raw) != string(approved) {
		return nil, errors.New("source users changed after opening snapshot")
	}
	if err := model.VerifyOPSSourceWalletFrozen(db.WithContext(ctx), users[0].ID); err != nil {
		return nil, err
	}
	if err := ValidateSourceReadiness(ctx, db); err != nil {
		return nil, err
	}
	// Persist only reviewed commission evidence before any remote custody write.
	if err := db.WithContext(ctx).AutoMigrate(&model.OPSAgencyOpening{}); err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, u := range m.Users {
			if err := model.SaveOPSAgencyOpening(tx, m.Scope, u.ID, u.Quota, u.FundingDebtMicros, u.FundingLots); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	wallets, err := readMigrationWallets(ctx, client)
	if err != nil {
		return nil, err
	}
	if wallets.Data.Since == "" && (m.Phase == "custody_requested" || m.Phase == "verified") {
		// A custody timeout might have committed, then another actor took it
		// back. Do not start a second period or overwrite an unknown outcome.
		return nil, errors.New("custody outcome unresolved; keep source frozen and inspect OPS")
	}
	if wallets.Data.Since != "" && (m.Phase == "prepared" || m.Phase == "uploaded") {
		return nil, errors.New("OPS custody already active outside this migration")
	}
	if wallets.Data.Since == "" {
		// Replay all bounded chunks after a crash. Profiles carry the SAME
		// balance timestamp and batch identity; acknowledgements are checked.
		for start := 0; start < len(m.Users); start += 100 {
			end := min(start+100, len(m.Users))
			profiles := make([]map[string]any, 0, end-start)
			for _, u := range m.Users[start:end] {
				status := "disabled"
				if u.Status == common.UserStatusEnabled {
					status = "active"
				}
				if u.DeletedAt.Valid {
					status = "removed"
				}
				profiles = append(profiles, map[string]any{"userId": strconv.Itoa(u.ID), "username": u.Username, "status": status, "updatedAt": m.FrozenAt, "balance": map[string]any{"amount": decimal.NewFromInt(u.Quota).Div(decimal.NewFromInt(500_000)).String(), "currency": "USD", "at": m.FrozenAt}})
			}
			body, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": fmt.Sprintf("opening-%s-%d", digest, start), "users": profiles})
			if err != nil || len(body) > 1024*1024 {
				return nil, errors.New("opening profile batch too large")
			}
			reply, err := client.Post(ctx, "/api/v1/ingest/v1/users", body, time.Now())
			if err != nil {
				return nil, err
			}
			var ack struct {
				Data *struct {
					Accepted  int
					Unchanged int
					Rejected  []json.RawMessage
					Stale     []json.RawMessage
				}
			}
			if common.Unmarshal(reply, &ack) != nil || ack.Data == nil || len(ack.Data.Rejected) != 0 || len(ack.Data.Stale) != 0 || ack.Data.Accepted < 0 || ack.Data.Unchanged < 0 || ack.Data.Accepted+ack.Data.Unchanged != len(profiles) {
				return nil, errors.New("opening profiles not fully acknowledged")
			}
		}
		m.Phase = "uploaded"
		if err := save(m); err != nil {
			return nil, err
		}
		wallets, err = readMigrationWallets(ctx, client)
		if err != nil {
			return nil, err
		}
		if wallets.Data.Since != "" {
			return nil, errors.New("custody changed during opening upload")
		}
		if err := m.reconcile(wallets, false); err != nil {
			return nil, err
		}
		m.Phase = "custody_requested"
		if err := save(m); err != nil {
			return nil, err
		}
		// Only this explicit apply command is allowed to change custody. If
		// it times out, the next run reads immutable opening evidence first.
		if _, err := client.Post(ctx, "/api/v1/catalog/v1/wallet-custody", []byte(`{"holder":"ops"}`), time.Now()); err != nil {
			return nil, err
		}
		wallets, err = readMigrationWallets(ctx, client)
		if err != nil {
			return nil, err
		}
	}
	since, err := time.Parse(time.RFC3339Nano, wallets.Data.Since)
	if err != nil || since.Before(frozen) || m.CustodySince != "" && m.CustodySince != wallets.Data.Since {
		return nil, errors.New("OPS custody period differs from frozen opening")
	}
	if err := m.reconcile(wallets, true); err != nil {
		return nil, err
	}
	m.Phase, m.CustodySince = "verified", wallets.Data.Since
	if err := save(m); err != nil {
		return nil, err
	}
	zero, total := 0, decimal.Zero
	proof := &CutoverApproval{SchemaVersion: 1, Scope: m.Scope, URL: m.URL, KeyID: m.KeyID, Currency: "USD", GatewayQuotaPerUnit: 500_000, ApprovedBy: approvedBy, ApprovedAt: time.Now().UTC().Format(time.RFC3339Nano), FrozenAt: m.FrozenAt, CustodySince: m.CustodySince, Differences: &zero, MissingBalances: &zero}
	proof.HistoricalReviewSHA256 = m.HistoricalReviewSHA256
	for _, u := range m.Users {
		proof.Users = append(proof.Users, OpeningBalance{UserID: u.ID, Quota: u.Quota})
		total = total.Add(decimal.NewFromInt(u.Quota).Div(decimal.NewFromInt(500_000)))
	}
	proof.Total = total.String()
	return proof, nil
}
