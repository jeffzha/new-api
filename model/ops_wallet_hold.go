package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOPSWalletHoldConflict = errors.New("OPS wallet reservation conflict")
var ErrOPSBalanceStale = errors.New("OPS balance snapshot predates reconciled charges")
var ErrOPSInsufficientAvailable = errors.New("OPS wallet available balance insufficient")

// OPSWalletAdmission serializes all gateway instances for one source wallet.
// PendingQuota is admission capacity, never customer money or User.Quota.
type OPSWalletAdmission struct {
	ID           string `gorm:"size:64;primaryKey"`
	Scope        string `gorm:"size:80;not null"`
	UserID       int    `gorm:"not null"`
	LastEntryID  int64  `gorm:"not null"`
	BalanceQuota int64  `gorm:"not null"`
	PendingQuota int64  `gorm:"not null"`
}

type OPSWalletHold struct {
	Scope           string `gorm:"size:80;not null;index:idx_ops_hold_reconcile,priority:1"`
	ID              string `gorm:"size:64;primaryKey;index:idx_ops_hold_reconcile,priority:3"`
	AccountID       string `gorm:"size:64;not null;index:idx_ops_hold_account"`
	RequestID       string `gorm:"size:64;not null"`
	ChargeRequestID string `gorm:"size:80;not null"`
	UserID          int    `gorm:"not null"`
	TokenID         int    `gorm:"not null"`
	TokenUnlimited  bool   `gorm:"not null"`
	Playground      bool   `gorm:"not null"`
	Model           string `gorm:"size:200;not null"`
	State           string `gorm:"size:16;not null;index:idx_ops_hold_reconcile,priority:2"`
	HeldQuota       int64  `gorm:"not null"`
	TokenQuota      int64  `gorm:"not null"`
	DisposedQuota   int64  `gorm:"not null"`
	ActualQuota     int64  `gorm:"not null"`
	SettlementQuota int    `gorm:"not null;default:0"`
	SettlementEvent string `gorm:"type:text"`
	ChargeEntryID   int64  `gorm:"not null"`
	TaskID          int64  `gorm:"not null"`
	DispatchState   string `gorm:"size:16;not null"`
	DispatchDigest  string `gorm:"size:64;not null"`
	PublicTaskID    string `gorm:"size:191;not null"`
	UpstreamTaskID  string `gorm:"size:191;not null"`
	ChannelID       int    `gorm:"not null"`
	Platform        string `gorm:"size:30;not null"`
	CreatedAt       int64  `gorm:"not null"`
	AgencyPricing   string `gorm:"type:text"`
}

type OPSWalletHoldIdentity struct {
	RequestID                  string
	ChargeRequestID            string
	UserID, TokenID            int
	TokenUnlimited, Playground bool
	Model                      string
	AgencyPricing              *agencycontract.PricingSnapshot
}

// OPSWalletUsage is the financial outbox, independent of the optional log DB.
// Its identity and payload commit with the API-key settlement in the main DB.
type OPSWalletUsage struct {
	ID          string `gorm:"size:64;primaryKey;index:idx_ops_usage_pending,priority:3"`
	Scope       string `gorm:"size:80;not null;index:idx_ops_usage_pending,priority:1"`
	RequestID   string `gorm:"size:64;not null"`
	UserID      int    `gorm:"not null"`
	Quota       int    `gorm:"not null"`
	Payload     string `gorm:"type:text;not null"`
	DeliveredAt int64  `gorm:"not null;index:idx_ops_usage_pending,priority:2"`
}

type opsRelayEventIdentity struct {
	RequestID string `json:"requestId"`
	UserID    string `json:"userId"`
	Model     string `json:"model"`
	Currency  string `json:"currency"`
	Amount    string `json:"amount"`
	Status    string `json:"status"`
	Extra     struct {
		Scope     string `json:"billing_scope"`
		Terminal  bool   `json:"project_relay_terminal"`
		Kind      string `json:"recordKind"`
		Direction string `json:"direction"`
	} `json:"extra"`
}

func validateOPSRelaySettlement(h OPSWalletHold, request string, user, actual int, event []byte) error {
	if request == "" || user <= 0 || actual < 0 || actual > common.MaxQuota || len(event) == 0 || len(event) > 48*1024 {
		return ErrOPSWalletHoldConflict
	}
	var identity opsRelayEventIdentity
	if common.Unmarshal(event, &identity) != nil || h.ChargeRequestID != request || h.DispatchState != "" || identity.RequestID != request || identity.UserID != fmt.Sprint(user) || identity.Model != h.Model || identity.Currency != "USD" || identity.Status != "success" || identity.Extra.Scope != opsauthority.Scope() || !identity.Extra.Terminal || identity.Extra.Kind != "usage" || identity.Extra.Direction != "debit" {
		return ErrOPSWalletHoldConflict
	}
	amount, err := decimal.NewFromString(identity.Amount)
	if err != nil || amount.IsNegative() || common.QuotaPerUnit != 500_000 || !amount.Mul(decimal.NewFromInt(500_000)).Equal(decimal.NewFromInt(int64(actual))) {
		return ErrOPSWalletHoldConflict
	}
	return nil
}

// StageOPSRelaySettlement durably retains terminal evidence before token
// settlement. A replacement worker can finish it without another model call.
func StageOPSRelaySettlement(db *gorm.DB, request string, user, actual int, event []byte) error {
	return db.Transaction(func(tx *gorm.DB) error {
		a, err := opsAdmissionTx(tx, user)
		if err != nil {
			return err
		}
		var h OPSWalletHold
		if err := tx.Where("id = ? AND account_id = ?", opsHoldID(request), a.ID).First(&h).Error; err != nil {
			return err
		}
		if err := validateOPSRelaySettlement(h, request, user, actual, event); err != nil {
			return err
		}
		if h.SettlementEvent != "" {
			if h.SettlementQuota != actual || h.SettlementEvent != string(event) {
				return ErrOPSWalletHoldConflict
			}
			return nil
		}
		if h.State != "reserved" {
			return ErrOPSWalletHoldConflict
		}
		h.SettlementQuota, h.SettlementEvent = actual, string(event)
		return tx.Save(&h).Error
	})
}

func PendingOPSRelaySettlements(db *gorm.DB, after string, limit int) ([]OPSWalletHold, error) {
	if !opsauthority.Enabled() || opsauthority.Scope() == "" || limit < 1 || limit > 100 {
		return nil, ErrOPSWalletHoldConflict
	}
	var rows []OPSWalletHold
	err := db.Where("scope = ? AND state = ? AND settlement_event <> ? AND id > ?", opsauthority.Scope(), "reserved", "", after).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// FinalizeOPSRelay atomically freezes the debit before any best-effort log.
func FinalizeOPSRelay(db *gorm.DB, request string, user, actual int, event []byte) error {
	if !opsauthority.Enabled() || request == "" || user <= 0 || actual < 0 || actual > common.MaxQuota || len(event) == 0 || len(event) > 48*1024 {
		return ErrOPSWalletHoldConflict
	}
	return db.Transaction(func(tx *gorm.DB) error {
		a, err := opsAdmissionTx(tx, user)
		if err != nil {
			return err
		}
		var h OPSWalletHold
		if err := tx.Where("id = ? AND account_id = ?", opsHoldID(request), a.ID).First(&h).Error; err != nil {
			return err
		}
		if err := validateOPSRelaySettlement(h, request, user, actual, event); err != nil {
			return err
		}
		if h.SettlementEvent != "" && (h.SettlementEvent != string(event) || h.SettlementQuota != actual) {
			return ErrOPSWalletHoldConflict
		}
		var existing OPSWalletUsage
		err = tx.First(&existing, "id = ?", h.ID).Error
		if err == nil {
			if existing.Scope != opsauthority.Scope() || existing.RequestID != request || existing.UserID != user || existing.Quota != actual || existing.Payload != string(event) || h.ActualQuota != int64(actual) || h.State != "settled" && h.State != "reconciled" {
				return ErrOPSWalletHoldConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := finalizeOPSHoldTx(tx, a, &h, actual, false); err != nil {
			return err
		}
		return tx.Create(&OPSWalletUsage{ID: h.ID, Scope: opsauthority.Scope(), RequestID: request, UserID: user, Quota: actual, Payload: string(event)}).Error
	})
}

type OPSWalletPosition struct {
	UserID         int
	EntryID, Quota int64
}

func opsAdmissionID(user int) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", opsauthority.Scope(), user))))
}

func opsHoldID(request string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(opsauthority.Scope()+"\x00"+request)))
}

func GetOPSWalletHold(db *gorm.DB, request string, user int) (OPSWalletHold, error) {
	var hold OPSWalletHold
	if !opsauthority.Enabled() || request == "" || user <= 0 {
		return hold, ErrOPSWalletHoldConflict
	}
	err := db.Where("id = ? AND user_id = ? AND scope = ?", opsHoldID(request), user, opsauthority.Scope()).First(&hold).Error
	return hold, err
}

// BeginOPSTaskDispatch is a one-way provider submission fence. A restart or
// an ambiguous network failure cannot turn the same hold into a second video.
func BeginOPSTaskDispatch(db *gorm.DB, request string, user int, public, digest string, channel int, platform string) error {
	decoded, err := hex.DecodeString(digest)
	if public == "" || len(public) > 191 || err != nil || len(decoded) != sha256.Size || channel <= 0 || platform == "" || len(platform) > 30 {
		return ErrOPSWalletHoldConflict
	}
	return db.Transaction(func(tx *gorm.DB) error {
		a, err := opsAdmissionTx(tx, user)
		if err != nil {
			return err
		}
		var h OPSWalletHold
		if err := tx.Where("id = ? AND account_id = ?", opsHoldID(request), a.ID).First(&h).Error; err != nil {
			return err
		}
		if h.State != "reserved" || h.DispatchState != "" || h.ChargeRequestID != opsauthority.TaskRequestID(request) {
			return ErrOPSWalletHoldConflict
		}
		h.DispatchState, h.DispatchDigest, h.PublicTaskID = "dispatched", digest, public
		h.ChannelID, h.Platform = channel, platform
		return tx.Save(&h).Error
	})
}

// ResolveOPSTaskDispatch records acceptance or definitive rejection. Unknown
// outcomes remain dispatched and require provider/operator reconciliation.
func ResolveOPSTaskDispatch(db *gorm.DB, request string, user int, upstream string, rejected bool) error {
	if len(upstream) > 191 || rejected && upstream != "" {
		return ErrOPSWalletHoldConflict
	}
	state := "accepted"
	if rejected {
		state = "rejected"
	}
	return db.Transaction(func(tx *gorm.DB) error {
		a, err := opsAdmissionTx(tx, user)
		if err != nil {
			return err
		}
		var h OPSWalletHold
		if err := tx.Where("id = ? AND account_id = ?", opsHoldID(request), a.ID).First(&h).Error; err != nil {
			return err
		}
		if h.State != "reserved" || h.ChargeRequestID != opsauthority.TaskRequestID(request) {
			return ErrOPSWalletHoldConflict
		}
		if h.DispatchState == state && h.UpstreamTaskID == upstream {
			return nil
		}
		if h.DispatchState != "dispatched" {
			return ErrOPSWalletHoldConflict
		}
		h.DispatchState, h.UpstreamTaskID = state, upstream
		return tx.Save(&h).Error
	})
}

func opsAdmissionTx(tx *gorm.DB, user int) (*OPSWalletAdmission, error) {
	if !opsauthority.Enabled() || opsauthority.Scope() == "" || user <= 0 {
		return nil, ErrOPSWalletHoldConflict
	}
	row := OPSWalletAdmission{ID: opsAdmissionID(user), Scope: opsauthority.Scope(), UserID: user}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	if err := lockForUpdate(tx).Where("id = ?", row.ID).First(&row).Error; err != nil {
		return nil, err
	}
	if row.UserID != user || row.Scope != opsauthority.Scope() || row.PendingQuota < 0 || row.PendingQuota > common.MaxWalletQuota {
		return nil, ErrOPSWalletHoldConflict
	}
	return &row, nil
}

func (a *OPSWalletAdmission) observe(p OPSWalletPosition) error {
	if p.UserID != a.UserID || p.EntryID < 0 || p.Quota < -common.MaxWalletQuota || p.Quota > common.MaxWalletQuota || p.EntryID == 0 && p.Quota != 0 {
		return ErrOPSWalletHoldConflict
	}
	if p.EntryID < a.LastEntryID {
		return ErrOPSBalanceStale
	}
	if p.EntryID == a.LastEntryID && a.LastEntryID > 0 && p.Quota != a.BalanceQuota {
		return ErrOPSWalletHoldConflict
	}
	a.LastEntryID, a.BalanceQuota = p.EntryID, p.Quota
	return nil
}

// ReserveOPSWallet holds capacity and the API-key budget in one transaction.
// No timeout releases unresolved requests: a crash requires reconciliation.
func ReserveOPSWallet(db *gorm.DB, identity OPSWalletHoldIdentity, target int, position OPSWalletPosition) (OPSWalletHold, error) {
	var hold OPSWalletHold
	if identity.ChargeRequestID == "" {
		identity.ChargeRequestID = identity.RequestID
	}
	if identity.RequestID == "" || len(identity.RequestID) > 64 || len(identity.ChargeRequestID) > 80 || identity.Model == "" || len(identity.Model) > 200 || target < 0 || target > common.MaxQuota || !identity.Playground && identity.TokenID <= 0 {
		return hold, ErrOPSWalletHoldConflict
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id,status").First(&user, identity.UserID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrOPSWalletHoldConflict
		}
		// The job is the custody-mode barrier: billing_mode remains frozen.
		if tx.Migrator().HasTable(&AgencyProvisioningJob{}) {
			var jobs int64
			if err := tx.Model(&AgencyProvisioningJob{}).Where("user_id = ? AND status IN ?", identity.UserID, []string{"queued", "processing", "blocked"}).Count(&jobs).Error; err != nil {
				return err
			}
			if jobs > 0 {
				return ErrAgencyProvisioning
			}
		}
		a, err := opsAdmissionTx(tx, identity.UserID)
		if err != nil {
			return err
		}
		if err = a.observe(position); err != nil {
			return err
		}
		err = tx.Where("id = ?", opsHoldID(identity.RequestID)).First(&hold).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			pricing, err := freezeOPSAgencyPricingTx(tx, identity)
			if err != nil {
				return err
			}
			hold = OPSWalletHold{Scope: opsauthority.Scope(), ID: opsHoldID(identity.RequestID), AccountID: a.ID, RequestID: identity.RequestID, ChargeRequestID: identity.ChargeRequestID, UserID: identity.UserID, TokenID: identity.TokenID, TokenUnlimited: identity.TokenUnlimited, Playground: identity.Playground, Model: identity.Model, State: "reserved", CreatedAt: time.Now().Unix(), AgencyPricing: pricing}
			if err = tx.Create(&hold).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if hold.AccountID != a.ID || hold.UserID != identity.UserID || hold.TokenID != identity.TokenID || hold.TokenUnlimited != identity.TokenUnlimited || hold.Playground != identity.Playground || hold.Model != identity.Model || hold.ChargeRequestID != identity.ChargeRequestID || hold.State != "reserved" {
			return ErrOPSWalletHoldConflict
		}
		delta := max(int64(target)-hold.HeldQuota, 0)
		if a.BalanceQuota <= 0 || a.PendingQuota > a.BalanceQuota || delta > a.BalanceQuota-a.PendingQuota {
			return ErrOPSInsufficientAvailable
		}
		if err = adjustOPSHoldTokenTx(tx, &hold, max(hold.HeldQuota, int64(target)), true); err != nil {
			return err
		}
		hold.HeldQuota += delta
		a.PendingQuota += delta
		if err = tx.Save(&hold).Error; err != nil {
			return err
		}
		return tx.Save(a).Error
	})
	return hold, err
}

// PendingOPSWalletHolds reads a bounded page without expiring unresolved holds.
func PendingOPSWalletHolds(db *gorm.DB, after string, limit int) ([]OPSWalletHold, error) {
	if !opsauthority.Enabled() || opsauthority.Scope() == "" || limit < 1 || limit > 100 {
		return nil, ErrOPSWalletHoldConflict
	}
	var rows []OPSWalletHold
	err := db.Where("scope = ? AND state = ? AND id > ?", opsauthority.Scope(), "settled", after).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

func adjustOPSHoldTokenTx(tx *gorm.DB, h *OPSWalletHold, target int64, requireAvailable bool) error {
	if h.Playground {
		return nil
	}
	delta := target - h.TokenQuota
	var token Token
	err := lockForUpdate(tx.Unscoped()).Where("id = ?", h.TokenID).First(&token).Error
	if (errors.Is(err, gorm.ErrRecordNotFound) || err == nil && token.DeletedAt.Valid) && delta < 0 {
		if err == nil && token.UserId != h.UserID {
			return ErrOPSWalletHoldConflict
		}
		h.DisposedQuota -= delta
		h.TokenQuota = target
		return nil
	}
	if err != nil {
		return err
	}
	if token.UserId != h.UserID || token.DeletedAt.Valid || token.UnlimitedQuota != h.TokenUnlimited {
		return ErrOPSWalletHoldConflict
	}
	if (requireAvailable || delta > 0) && (token.Status != common.TokenStatusEnabled || token.ExpiredTime > 0 && token.ExpiredTime < common.GetTimestamp()) {
		return ErrOPSWalletHoldConflict
	}
	remain, used := int64(token.RemainQuota)-delta, int64(token.UsedQuota)+delta
	if requireAvailable && !h.TokenUnlimited && remain < 0 {
		return ErrInsufficientAgencyTokenQuota
	}
	if remain < -common.MaxWalletQuota || remain > common.MaxWalletQuota || used < 0 || used > common.MaxWalletQuota {
		return ErrWalletQuotaLimitExceeded
	}
	if delta != 0 {
		result := tx.Model(&Token{}).Where("id = ? AND user_id = ?", h.TokenID, h.UserID).Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": common.GetTimestamp()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOPSWalletHoldConflict
		}
	}
	h.TokenQuota = target
	return nil
}

// FinalizeOPSWallet leaves successful usage held until its OPS debit is proven.
// Cancellation is only for a failed request whose usage will not be delivered.
func FinalizeOPSWallet(db *gorm.DB, request string, user, actual int, cancel bool) error {
	if request == "" || actual < 0 || actual > common.MaxQuota || cancel && actual != 0 {
		return ErrOPSWalletHoldConflict
	}
	return db.Transaction(func(tx *gorm.DB) error {
		a, err := opsAdmissionTx(tx, user)
		if err != nil {
			return err
		}
		var h OPSWalletHold
		if err = tx.Where("id = ? AND account_id = ?", opsHoldID(request), a.ID).First(&h).Error; err != nil {
			return err
		}
		return finalizeOPSHoldTx(tx, a, &h, actual, cancel)
	})
}

func finalizeOPSHoldTx(tx *gorm.DB, a *OPSWalletAdmission, h *OPSWalletHold, actual int, cancel bool) error {
	if cancel && h.SettlementEvent != "" {
		return ErrOPSWalletHoldConflict
	}
	if cancel && h.DispatchState != "" && h.DispatchState != "rejected" {
		return ErrOPSWalletHoldConflict
	}
	state := "settled"
	if cancel {
		state = "cancelled"
	}
	if h.State != "reserved" {
		if h.ActualQuota == int64(actual) && (h.State == state || !cancel && h.State == "reconciled") {
			return nil
		}
		return ErrOPSWalletHoldConflict
	}
	if err := adjustOPSHoldTokenTx(tx, h, int64(actual), false); err != nil {
		return err
	}
	target := max(h.HeldQuota, int64(actual))
	if cancel {
		target = 0
	}
	if a.PendingQuota < h.HeldQuota || target > common.MaxWalletQuota-(a.PendingQuota-h.HeldQuota) {
		return ErrOPSWalletHoldConflict
	}
	a.PendingQuota += target - h.HeldQuota
	h.HeldQuota, h.ActualQuota, h.State = target, int64(actual), state
	if err := tx.Save(h).Error; err != nil {
		return err
	}
	return tx.Save(a).Error
}

// FinalizeOPSTask commits the token adjustment and immutable terminal event
// together. Provider polling may already have committed the terminal status;
// an interrupted financial step is retried without another provider request.
// The input is immutable: this function can run inside a caller-owned
// transaction, so a successful savepoint does not prove the outer commit.
func FinalizeOPSTask(db *gorm.DB, task *Task, original string, actual int, event []byte) error {
	if task == nil || task.ID <= 0 || task.PrivateData.BillingSource != "ops" || original == "" || actual < 0 || actual > common.MaxQuota || len(event) == 0 || len(event) > 1024*1024 {
		return ErrOPSWalletHoldConflict
	}
	return db.Transaction(func(tx *gorm.DB) error {
		a, err := opsAdmissionTx(tx, task.UserId)
		if err != nil {
			return err
		}
		var current Task
		if err := lockForUpdate(tx).First(&current, task.ID).Error; err != nil {
			return err
		}
		intent := current.PrivateData.OPSTerminal
		// Native JSON columns may reorder object keys and whitespace. Compare
		// their canonical values while retaining every financial field.
		if intent == nil || intent.Quota != actual || !cnIntegrityJSONEqual(intent.Event, event) || current.UserId != task.UserId || current.Status != task.Status || current.PrivateData.BillingSource != "ops" || current.PrivateData.BillingScope != opsauthority.Scope() || current.PrivateData.Execution == nil || current.PrivateData.Execution.RequestID != original || current.Status != TaskStatusSuccess && current.Status != TaskStatusFailure {
			return ErrOPSWalletHoldConflict
		}
		var h OPSWalletHold
		if err := tx.Where("id = ? AND account_id = ?", opsHoldID(original), a.ID).First(&h).Error; err != nil {
			return err
		}
		var identity struct {
			RequestID string `json:"requestId"`
			UserID    string `json:"userId"`
			Model     string `json:"model"`
			Currency  string `json:"currency"`
			Amount    string `json:"amount"`
			Status    string `json:"status"`
			Extra     struct {
				Scope     string `json:"billing_scope"`
				Original  string `json:"project_original_request"`
				Terminal  bool   `json:"project_task_terminal"`
				Kind      string `json:"recordKind"`
				Direction string `json:"direction"`
			} `json:"extra"`
		}
		if common.Unmarshal(event, &identity) != nil || h.ChargeRequestID != identity.RequestID || identity.RequestID != opsauthority.TaskRequestID(original) || identity.UserID != fmt.Sprint(current.UserId) || identity.Model != h.Model || h.TokenID != current.PrivateData.TokenId || h.TaskID != 0 && h.TaskID != current.ID {
			return ErrOPSWalletHoldConflict
		}
		if h.DispatchState != "accepted" || h.PublicTaskID != current.TaskID || h.UpstreamTaskID != current.PrivateData.UpstreamTaskID || h.ChannelID != current.ChannelId || h.Platform != string(current.Platform) {
			return ErrOPSWalletHoldConflict
		}
		kind, status := "usage", "success"
		if current.Status == TaskStatusFailure {
			kind, status = "error", "error"
			if actual != 0 {
				return ErrOPSWalletHoldConflict
			}
		}
		amount, amountErr := decimal.NewFromString(identity.Amount)
		if amountErr != nil || amount.IsNegative() || common.QuotaPerUnit != 500_000 || !amount.Mul(decimal.NewFromInt(500_000)).Equal(decimal.NewFromInt(int64(actual))) || identity.Currency != "USD" || identity.Status != status || identity.Extra.Kind != kind || identity.Extra.Direction != "debit" || identity.Extra.Scope != opsauthority.Scope() || identity.Extra.Original != original || !identity.Extra.Terminal {
			return ErrOPSWalletHoldConflict
		}
		if current.BillingReconciliationPending {
			return ErrOPSWalletHoldConflict
		}
		var pending int64
		if err := tx.Model(&TaskBillingReconciliation{}).Where("task_id = ? AND status IN ?", current.ID, []string{TaskBillingReconciliationPending, TaskBillingReconciliationProcessing}).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return ErrOPSWalletHoldConflict
		}
		if len(current.PrivateData.OpsUsage) > 0 {
			if cnIntegrityJSONEqual(current.PrivateData.OpsUsage, event) && current.Quota == actual && (h.State == "settled" || h.State == "reconciled") {
				return nil
			}
			return ErrOPSWalletHoldConflict
		}
		h.TaskID = current.ID
		if err := finalizeOPSHoldTx(tx, a, &h, actual, false); err != nil {
			return err
		}
		if current.Status == TaskStatusFailure {
			// A persisted terminal failure has no debit. OPS does not create a
			// charge receipt for error events, so none can release this capacity.
			a.PendingQuota -= h.HeldQuota
			h.HeldQuota, h.State = 0, "reconciled"
			if err := tx.Save(&h).Error; err != nil {
				return err
			}
			if err := tx.Save(a).Error; err != nil {
				return err
			}
		}
		current.Quota = actual
		current.PrivateData.OpsUsage = append([]byte(nil), event...)
		current.OpsUsagePending = true
		if err := tx.Model(&Task{}).Where("id = ?", current.ID).Updates(map[string]any{"quota": actual, "private_data": current.PrivateData, "ops_usage_pending": true}).Error; err != nil {
			return err
		}
		return nil
	})
}

// SettleOPSTaskProviderBill persists the provider bill, terminal debit and
// API-key adjustment atomically. It never writes either local money ledger.
func SettleOPSTaskProviderBill(db *gorm.DB, task *Task, record *TaskBillingReconciliation, bill TaskBillingReconciliationSettlement) error {
	if task == nil || record == nil || record.ID <= 0 || record.TaskID != task.ID || task.PrivateData.OPSTerminal == nil || task.PrivateData.Execution == nil || bill.ActualQuota != task.PrivateData.OPSTerminal.Quota || bill.ActualQuota < 0 || bill.ActualQuota > common.MaxQuota || bill.TotalTokens < 0 || bill.TotalTokens > int64(common.MaxQuota) {
		return ErrOPSWalletHoldConflict
	}
	intent := task.PrivateData.OPSTerminal
	return db.Transaction(func(tx *gorm.DB) error {
		if _, err := opsAdmissionTx(tx, task.UserId); err != nil {
			return err
		}
		var current Task
		if err := lockForUpdate(tx).First(&current, task.ID).Error; err != nil {
			return err
		}
		var claimed TaskBillingReconciliation
		if err := lockForUpdate(tx).First(&claimed, record.ID).Error; err != nil {
			return err
		}
		if current.UserId != task.UserId || current.PrivateData.BillingSource != "ops" || current.PrivateData.BillingScope != opsauthority.Scope() || current.PrivateData.Execution == nil || current.PrivateData.Execution.RequestID != task.PrivateData.Execution.RequestID || current.Status != TaskStatusSuccess || claimed.TaskID != current.ID || claimed.Provider != record.Provider || claimed.Attempts != record.Attempts {
			return ErrOPSWalletHoldConflict
		}
		if claimed.ChannelID != current.ChannelId || claimed.UpstreamTaskID != current.GetUpstreamTaskID() || record.ChannelID != claimed.ChannelID || record.UpstreamTaskID != claimed.UpstreamTaskID {
			return ErrOPSWalletHoldConflict
		}
		if claimed.Status == TaskBillingReconciliationSettled {
			if claimed.ActualQuota != bill.ActualQuota || claimed.TotalTokens != bill.TotalTokens || claimed.SupplierPrice != bill.SupplierPrice || claimed.SupplierDiscount != bill.SupplierDiscount || claimed.SupplierAmountPaid != bill.SupplierAmountPaid || claimed.ExpenseTime != bill.ExpenseTime {
				return ErrOPSWalletHoldConflict
			}
			return FinalizeOPSTask(tx, task, task.PrivateData.Execution.RequestID, intent.Quota, intent.Event)
		}
		if claimed.Status != TaskBillingReconciliationProcessing || current.PrivateData.OPSTerminal != nil || len(current.PrivateData.OpsUsage) > 0 || current.PrivateData.BillingContext == nil || current.PrivateData.BillingContext.ProviderBilling == nil || current.PrivateData.BillingContext.ProviderBilling.Provider != claimed.Provider {
			return ErrOPSWalletHoldConflict
		}
		claimed.Status, claimed.NextRetryAt, claimed.LastError = TaskBillingReconciliationSettled, 0, ""
		claimed.PreConsumedQuota, claimed.ActualQuota = current.Quota, bill.ActualQuota
		claimed.QuotaDelta, claimed.TotalTokens = bill.ActualQuota-current.Quota, bill.TotalTokens
		claimed.SupplierPrice, claimed.SupplierDiscount = bill.SupplierPrice, bill.SupplierDiscount
		claimed.SupplierAmountPaid, claimed.ExpenseTime = bill.SupplierAmountPaid, bill.ExpenseTime
		claimed.UpdatedAt = time.Now().Unix()
		if err := tx.Save(&claimed).Error; err != nil {
			return err
		}
		current.PrivateData.OPSTerminal, current.PrivateData.OPSBillingError = intent, ""
		current.PrivateData.SettledTokens = bill.TotalTokens
		current.OpsUsagePending, current.BillingReconciliationPending = true, false
		if err := tx.Model(&Task{}).Where("id = ?", current.ID).Updates(map[string]any{"private_data": current.PrivateData, "ops_usage_pending": true, "billing_reconciliation_pending": false}).Error; err != nil {
			return err
		}
		return FinalizeOPSTask(tx, task, task.PrivateData.Execution.RequestID, intent.Quota, intent.Event)
	})
}

// ReconcileOPSWallet requires an authenticated charged receipt and a balance
// snapshot at or after that debit. A pending/failed receipt never releases it.
func ReconcileOPSWallet(db *gorm.DB, request string, user int, model string, chargeEntry int64, position OPSWalletPosition) error {
	if chargeEntry <= 0 || position.EntryID < chargeEntry {
		return ErrOPSBalanceStale
	}
	return db.Transaction(func(tx *gorm.DB) error {
		a, err := opsAdmissionTx(tx, user)
		if err != nil {
			return err
		}
		if err = a.observe(position); err != nil {
			return err
		}
		var h OPSWalletHold
		if err = tx.Where("id = ? AND account_id = ?", opsHoldID(request), a.ID).First(&h).Error; err != nil {
			return err
		}
		if h.Model != model {
			return ErrOPSWalletHoldConflict
		}
		if h.State == "reconciled" && h.ChargeEntryID == chargeEntry {
			return nil
		}
		if h.State != "settled" || a.PendingQuota < h.HeldQuota {
			return ErrOPSWalletHoldConflict
		}
		a.PendingQuota -= h.HeldQuota
		h.HeldQuota, h.ChargeEntryID, h.State = 0, chargeEntry, "reconciled"
		if err = tx.Save(&h).Error; err != nil {
			return err
		}
		return tx.Save(a).Error
	})
}
