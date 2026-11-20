package opswallet

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// CutoverApproval is an operator-approved opening, not a request to transfer
// custody. Its exact file digest is pinned in the private release environment.
// The source quotas remain an audit snapshot after OPS begins writing money.
type CutoverApproval struct {
	SchemaVersion       int              `json:"schemaVersion"`
	Scope               string           `json:"scope"`
	URL                 string           `json:"url"`
	KeyID               string           `json:"keyId"`
	Currency            string           `json:"currency"`
	GatewayQuotaPerUnit int64            `json:"gatewayQuotaPerUnit"`
	ApprovedBy          string           `json:"approvedBy"`
	ApprovedAt          string           `json:"approvedAt"`
	FrozenAt            string           `json:"frozenAt"`
	CustodySince        string           `json:"custodySince"`
	Differences         *int             `json:"differences"`
	MissingBalances     *int             `json:"missingBalances"`
	Total               string           `json:"total"`
	Users               []OpeningBalance `json:"users"`
}

type OpeningBalance struct {
	UserID int   `json:"userId"`
	Quota  int64 `json:"quota"`
}

// ValidateCutover only reads OPS. In particular, POST wallet-custody must never
// be used as a probe: it changes who owns money immediately.
func ValidateCutover(ctx context.Context, db *gorm.DB, client *opsclient.Client) error {
	if !opsauthority.Enabled() {
		return nil
	}
	if err := ValidateSourceReadiness(ctx, db); err != nil {
		return err
	}
	f, err := os.Open(os.Getenv("OPS_WALLET_OPENING_FILE"))
	if err != nil {
		return errors.New("approved OPS wallet opening is unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 || fmt.Sprintf("%x", sha256.Sum256(raw)) != os.Getenv("OPS_WALLET_OPENING_SHA256") {
		return errors.New("OPS wallet opening digest mismatch")
	}
	var proof CutoverApproval
	if common.Unmarshal(raw, &proof) != nil {
		return errors.New("invalid OPS wallet opening")
	}
	frozen, frozenErr := time.Parse(time.RFC3339Nano, proof.FrozenAt)
	since, sinceErr := time.Parse(time.RFC3339Nano, proof.CustodySince)
	approved, approvedErr := time.Parse(time.RFC3339Nano, proof.ApprovedAt)
	if proof.SchemaVersion != 1 || proof.Scope != opsauthority.Scope() || strings.TrimRight(proof.URL, "/") != client.BaseURL || proof.KeyID != client.KeyID || proof.Currency != "USD" || proof.GatewayQuotaPerUnit != 500_000 || common.QuotaPerUnit != 500_000 || strings.TrimSpace(proof.ApprovedBy) == "" || len(proof.ApprovedBy) > 100 || frozenErr != nil || sinceErr != nil || approvedErr != nil || frozen.After(since) || approved.Before(since) || approved.After(time.Now().Add(time.Minute)) || proof.Differences == nil || *proof.Differences != 0 || proof.MissingBalances == nil || *proof.MissingBalances != 0 || len(proof.Users) == 0 || len(proof.Users) > 10000 {
		return errors.New("OPS wallet opening is not approved for this station")
	}
	slices.SortFunc(proof.Users, func(a, b OpeningBalance) int { return cmp.Compare(a.UserID, b.UserID) })
	total := decimal.Zero
	for i, row := range proof.Users {
		if row.UserID <= 0 || row.Quota < -common.MaxWalletQuota || row.Quota > common.MaxWalletQuota || i > 0 && proof.Users[i-1].UserID == row.UserID {
			return errors.New("invalid OPS opening user balance")
		}
		total = total.Add(decimal.NewFromInt(row.Quota).Div(decimal.NewFromInt(proof.GatewayQuotaPerUnit)))
	}
	declared, err := decimal.NewFromString(proof.Total)
	if err != nil || len(proof.Total) > 40 || !declared.Equal(total) {
		return errors.New("OPS opening total does not match its users")
	}
	var users []struct {
		ID    int
		Quota int64
	}
	if err := db.WithContext(ctx).Unscoped().Model(&model.User{}).Select("id, quota").Order("id").Find(&users).Error; err != nil {
		return err
	}
	positions := make(map[int]int64, len(users))
	for _, user := range users {
		positions[user.ID] = user.Quota
	}
	for _, row := range proof.Users {
		if quota, exists := positions[row.UserID]; !exists || quota != row.Quota {
			return errors.New("source wallet changed after approved OPS opening")
		}
		delete(positions, row.UserID)
	}
	for _, quota := range positions {
		if quota != 0 {
			return errors.New("unapproved source wallet added after OPS opening")
		}
	}
	if err := model.VerifyOPSSourceWalletFrozen(db.WithContext(ctx), proof.Users[0].UserID); err != nil {
		return err
	}
	raw, err = client.Get(ctx, "/api/v1/catalog/v1/wallets", time.Now())
	if err != nil {
		return errors.New("cannot confirm OPS wallet custody")
	}
	var response struct {
		Data *struct {
			Since    string `json:"opsKeepsBalanceSince"`
			Currency string `json:"currency"`
			Station  *struct {
				PerUnit int64  `json:"quotaPerUnit"`
				Rate    string `json:"usdExchangeRate"`
			} `json:"station"`
			Items []struct {
				UserID  string      `json:"userId"`
				Source  string      `json:"balanceSource"`
				Opening *string     `json:"openingBalance"`
				EntryID json.Number `json:"openingEntryId"`
			} `json:"items"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &response) != nil || response.Data == nil {
		return errors.New("invalid OPS custody response")
	}
	p := response.Data
	remoteSince, err := time.Parse(time.RFC3339Nano, p.Since)
	if err != nil || !remoteSince.Equal(since) || p.Currency != "USD" || p.Station == nil || p.Station.PerUnit != 1_000_000 || p.Station.Rate != "1" || len(p.Items) < len(proof.Users) {
		return errors.New("OPS custody or currency does not match approved opening")
	}
	remote := make(map[string]decimal.Decimal, len(p.Items))
	identities := make(map[string]bool, len(p.Items))
	for _, row := range p.Items {
		id, idErr := strconv.Atoi(row.UserID)
		if row.Source != "ops" || identities[row.UserID] || idErr != nil || id <= 0 || strconv.Itoa(id) != row.UserID {
			return errors.New("invalid OPS custody wallet identity")
		}
		identities[row.UserID] = true
		if row.Opening == nil {
			if quota, exists := positions[id]; !exists || quota != 0 {
				return errors.New("unapproved new OPS custody wallet")
			}
			continue // Users created after custody have no opening in this period.
		}
		value, err := decimal.NewFromString(*row.Opening)
		entry, entryErr := row.EntryID.Int64()
		if err != nil || len(*row.Opening) > 40 || value.Exponent() < -6 || entryErr != nil || entry <= 0 {
			return errors.New("invalid OPS custody opening amount")
		}
		remote[row.UserID] = value
	}
	for _, row := range proof.Users {
		value, exists := remote[strconv.Itoa(row.UserID)]
		expected := decimal.NewFromInt(row.Quota).Div(decimal.NewFromInt(proof.GatewayQuotaPerUnit))
		if !exists || !value.Equal(expected) {
			return errors.New("approved opening user balance does not match OPS custody")
		}
		delete(remote, strconv.Itoa(row.UserID))
	}
	if len(remote) != 0 {
		return errors.New("unapproved user in OPS custody opening")
	}
	return nil
}

// ValidateSourceReadiness prevents changing owners while unsupported local
// entitlements or settlement writers still need to change the old wallet.
// It is read-only; an operator must drain or explicitly migrate these items.
func ValidateSourceReadiness(ctx context.Context, db *gorm.DB) error {
	checks := []struct {
		name     string
		optional any
		query    *gorm.DB
	}{
		{"subscriptions", nil, db.Model(&model.UserSubscription{}).Where("status = ?", "active")},
		{"subscription checkouts", nil, db.Model(&model.SubscriptionOrder{}).Where("status = ?", common.TopUpStatusPending)},
		{"expiring funding or reservations", &model.AgencyFundingLot{}, db.Model(&model.AgencyFundingLot{}).Where("paid_reserved <> 0 OR bonus_reserved <> 0 OR (expires_at > 0 AND (paid_available > 0 OR bonus_available > 0))")},
		{"refunds", &model.CNRefundReservation{}, db.Model(&model.CNRefundReservation{}).Where("status IS NULL OR status NOT IN ?", []string{"succeeded", "failed_released"})},
		{"Midjourney tasks", nil, db.Model(&model.Midjourney{}).Where("progress IS NULL OR progress <> ?", "100%")},
	}
	for _, check := range checks {
		// Agency and payment-refund schemas are installed separately; absence
		// on a site that has never enabled those modules is not pending money.
		if check.optional != nil && !db.WithContext(ctx).Migrator().HasTable(check.optional) {
			continue
		}
		var found []int
		if err := check.query.WithContext(ctx).Select("1").Limit(1).Scan(&found).Error; err != nil {
			return fmt.Errorf("cannot verify source wallet %s: %w", check.name, err)
		}
		if len(found) != 0 {
			return fmt.Errorf("source wallet is not ready: unresolved %s", check.name)
		}
	}
	// JSON is decoded through the model serializer rather than dialect-specific
	// JSON SQL. OPS tasks must remain resumable after a normal process restart.
	var tasks []model.Task
	query := db.WithContext(ctx).Select("id, private_data").Where("status IS NULL OR status NOT IN ? OR billing_reconciliation_pending = ?", []model.TaskStatus{model.TaskStatusSuccess, model.TaskStatusFailure}, true)
	if err := query.FindInBatches(&tasks, 100, func(_ *gorm.DB, _ int) error {
		for _, task := range tasks {
			if task.PrivateData.BillingSource != "ops" || task.PrivateData.BillingScope != opsauthority.Scope() {
				return errors.New("source wallet is not ready: unresolved local tasks")
			}
		}
		return nil
	}).Error; err != nil {
		return err
	}
	var bills []model.TaskBillingReconciliation
	query = db.WithContext(ctx).Select("id, task_id").Where("status IS NULL OR status NOT IN ?", []string{model.TaskBillingReconciliationSettled, model.TaskBillingReconciliationNotNeeded})
	return query.FindInBatches(&bills, 100, func(tx *gorm.DB, _ int) error {
		for _, bill := range bills {
			var task model.Task
			if err := tx.Session(&gorm.Session{NewDB: true}).Select("id, private_data").First(&task, bill.TaskID).Error; err != nil {
				return errors.New("source wallet is not ready: unresolved orphan provider bill")
			}
			if task.PrivateData.BillingSource != "ops" || task.PrivateData.BillingScope != opsauthority.Scope() {
				return errors.New("source wallet is not ready: unresolved local provider bill")
			}
		}
		return nil
	}).Error
}
