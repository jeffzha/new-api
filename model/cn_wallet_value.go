package model

import (
	"errors"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const cnWalletValuePolicy = "ordinary-unknown-first-paid-fifo-v1"
const cnWalletValueWriter = "cn_wallet_value_writer"

// A source switch permanently suspends ordinary attribution. Cash records and
// prior allocations remain intact; the new source cannot adopt their value.
type CNWalletValueAccount struct {
	UserID         int `gorm:"primaryKey;autoIncrement:false"`
	ExpectedQuota  int64
	UnknownQuota   int64
	OpeningQuota   int64
	OpeningBalance int64
	CreatedAt      string `gorm:"size:40;not null"`
	GapCount       int64
	Suspended      bool
	SuspendedAt    string `gorm:"size:40"`
	SuspendReason  string `gorm:"size:64"`
}

type CNWalletValueLot struct {
	ID        int64
	LotID     string `gorm:"size:64;not null;uniqueIndex"`
	UserID    int    `gorm:"not null;index"`
	SourceID  string `gorm:"size:191;not null"`
	PaymentID string `gorm:"size:300"`
	Currency  string `gorm:"size:3"`
	Quota     int64
	Amount    string `gorm:"size:128"`
	Evidence  string `gorm:"type:text"`
	CreatedAt string `gorm:"size:40"`
	Isolated  bool
}

type CNWalletValueCharge struct {
	ChargeKey    string `gorm:"size:64;primaryKey"`
	UserID       int    `gorm:"not null;index"`
	EconomicID   string `gorm:"size:300"`
	SegmentID    string `gorm:"size:64"`
	Actual       int64
	UnknownQuota int64
	DebtQuota    int64
}

type CNWalletValuePart struct {
	ID           int64
	ChargeKey    string `gorm:"size:64;not null;index"`
	LotID        string `gorm:"size:64;not null"`
	AllocationID string `gorm:"size:64;not null;uniqueIndex"`
}

// Each increase freezes its own residual share. Corrections unwind these
// slices in reverse order, even when a charge returns to an earlier lot.
type CNWalletValueSlice struct {
	ID           int64
	ChargeKey    string `gorm:"size:64;not null;index"`
	AllocationID string `gorm:"size:64"`
	Quota        int64
	Value        string `gorm:"size:128;not null"`
	Debt         bool
}

func cnWalletValueAvailable() bool {
	return os.Getenv("OPS_CN_WALLET_VALUE_ENABLED") == "true" && CNFinanceCaptureEnabled() && !common.RedisEnabled && !common.BatchUpdateEnabled
}

func cnWalletValueWrite(tx *gorm.DB) *gorm.DB { return tx.Set(cnWalletValueWriter, true) }

// Installed per GORM handle, including when admission is disabled: existing
// owners must remain protected while their persisted work is recovered.
func cnInstallWalletValueGuard(db *gorm.DB) error {
	if db.Callback().Update().Get("cn:wallet_value_guard") != nil {
		return nil
	}
	err := db.Callback().Update().Before("gorm:update").Register("cn:wallet_value_guard", func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Schema == nil || tx.Statement.Table != "users" {
			return
		}
		if allowed, _ := tx.Get(cnWalletValueWriter); allowed == true {
			return
		}
		if dest, ok := tx.Statement.Dest.(map[string]any); ok {
			if expr, ok := dest["quota"].(clause.Expr); ok && expr.SQL == "quota + 0" && len(expr.Vars) == 0 && len(dest) == 1 {
				return
			}
		}
		if !tx.Migrator().HasTable(&CNWalletValueAccount{}) {
			return
		}
		selected, restricted := tx.Statement.SelectAndOmitColumns(false, true)
		writesValue := false
		switch dest := tx.Statement.Dest.(type) {
		case map[string]any:
			for name := range dest {
				if field := tx.Statement.Schema.LookUpField(name); field != nil {
					name = field.DBName
				}
				include, exists := selected[name]
				if (name == "quota" || name == "billing_mode") && ((exists && include) || (!exists && !restricted)) {
					writesValue = true
				}
			}
		default:
			v := reflect.Indirect(reflect.ValueOf(dest))
			if v.IsValid() && v.Kind() == reflect.Struct {
				updating := &gorm.Statement{DB: tx}
				if err := updating.Parse(dest); err != nil {
					tx.AddError(err)
					return
				}
				for _, name := range []string{"quota", "billing_mode"} {
					field := updating.Schema.LookUpField(name)
					include, exists := selected[name]
					if field == nil || !field.Updatable || (exists && !include) || (!exists && restricted) {
						continue
					}
					_, zero := field.ValueOf(tx.Statement.Context, v)
					writesValue = writesValue || !zero || include
				}
			} else {
				// Unknown bulk forms cannot silently bypass a permanent owner.
				writesValue = true
			}
		}
		if !writesValue {
			return
		}
		query := tx.Session(&gorm.Session{NewDB: true}).Model(&User{})
		if where, ok := tx.Statement.Clauses["WHERE"]; ok {
			query = query.Clauses(where.Expression)
		}
		v := tx.Statement.ReflectValue
		if primary := tx.Statement.Schema.PrioritizedPrimaryField; primary != nil && v.IsValid() {
			switch v.Kind() {
			case reflect.Struct:
				if id, zero := primary.ValueOf(tx.Statement.Context, v); !zero {
					query = query.Where("id = ?", id)
				}
			case reflect.Slice, reflect.Array:
				var ids []any
				for i := range v.Len() {
					if id, zero := primary.ValueOf(tx.Statement.Context, reflect.Indirect(v.Index(i))); !zero {
						ids = append(ids, id)
					}
				}
				if len(ids) > 0 {
					query = query.Where("id IN ?", ids)
				}
			}
		}
		var users []User
		if err := cnWalletValueWrite(query.Session(&gorm.Session{})).Update("quota", gorm.Expr("quota + 0")).Error; err != nil {
			tx.AddError(err)
			return
		}
		if err := lockForUpdate(query).Select("id").Order("id").Find(&users).Error; err != nil {
			tx.AddError(err)
			return
		}
		var scopes []*cnIntegrityCaptureScope
		for _, user := range users {
			tx.Statement.Settings.Delete(cnIntegrityCaptureScopeKey)
			if err := cnInvalidateIntegrityUserTx(tx.Session(&gorm.Session{NewDB: true}), user.Id); err != nil {
				tx.AddError(err)
				return
			}
			if value, exists := tx.Statement.Settings.Load(cnIntegrityCaptureScopeKey); exists {
				scopes = append(scopes, value.(*cnIntegrityCaptureScope))
			}
			if err := cnSuspendWalletValueTx(tx.Session(&gorm.Session{NewDB: true}), user.Id); err != nil {
				tx.AddError(err)
				return
			}
		}
		tx.Statement.Settings.Delete(cnIntegrityCaptureScopeKey)
		tx.Statement.Settings.Store("cn:integrity-uncovered-scopes", scopes)
	})
	if err != nil {
		return err
	}
	return db.Callback().Update().After("gorm:update").Before("gorm:commit_or_rollback_transaction").Register("cn:integrity_uncovered_state", func(tx *gorm.DB) {
		value, exists := tx.Statement.Settings.LoadAndDelete("cn:integrity-uncovered-scopes")
		if !exists || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		for _, scope := range value.([]*cnIntegrityCaptureScope) {
			clean := tx.Session(&gorm.Session{NewDB: true})
			if err := cnRecordIntegrityValueStatesTx(clean, scope.Epoch.EpochID, scope.Admission, "control", strconv.FormatInt(scope.Epoch.Generation, 10), scope.Epoch.Generation, scope.Epoch.MutationSequence, nil, nil); err != nil {
				tx.AddError(err)
				return
			}
		}
	})
}

// The invalidation commits with the GORM mutation. With an explicitly disabled
// default transaction it commits first, conservatively keeping the gap even if
// the following write fails. It never restores attribution automatically.
func cnSuspendWalletValueTx(tx *gorm.DB, user int) error {
	var account CNWalletValueAccount
	err := lockForUpdate(tx).Where("user_id = ?", user).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || account.Suspended {
		return nil
	}
	if err != nil {
		return err
	}
	var lots []CNWalletValueLot
	if err := lockForUpdate(tx).Where("user_id = ? AND isolated = ?", user, false).Find(&lots).Error; err != nil {
		return err
	}
	for _, lot := range lots {
		var pool CNFundingValueLot
		if err := lockForUpdate(tx).Where("lot_id = ?", lot.LotID).First(&pool).Error; err != nil {
			return err
		}
		account.UnknownQuota += pool.RemainingQuota
	}
	if err := tx.Model(&CNWalletValueLot{}).Where("user_id = ?", user).Update("isolated", true).Error; err != nil {
		return err
	}
	return tx.Model(&account).Updates(map[string]any{"suspended": true, "gap_count": account.GapCount + 1, "unknown_quota": account.UnknownQuota, "suspended_at": time.Now().UTC().Format(CNTimeLayout), "suspend_reason": "uncovered_writer_or_source_switch"}).Error
}

// Caller holds the User writer lock. Unspent lots include in-flight native
// reservations; only an actual charge consumes their economic value.
func cnWalletValueAccountTx(tx *gorm.DB, user int, admit bool) (*CNWalletValueAccount, error) {
	if !tx.Migrator().HasTable(&CNWalletValueAccount{}) {
		return nil, nil
	}
	var native User
	if e := lockForUpdate(tx).Select("id", "quota", "billing_mode").First(&native, user).Error; e != nil {
		return nil, e
	}
	sourceSwitched := native.BillingMode == AgencyDurableBillingMode || native.BillingMode == AgencyProvisioningBillingMode || common.RedisEnabled || common.BatchUpdateEnabled
	if tx.Migrator().HasTable(&AgencyFundingAccount{}) {
		var owners []AgencyFundingAccount
		if e := lockForUpdate(tx).Select("user_id").Where("user_id = ?", user).Find(&owners).Error; e != nil {
			return nil, e
		}
		sourceSwitched = sourceSwitched || len(owners) != 0
	}
	if admit && cnWalletValueAvailable() && !sourceSwitched {
		// This uncommitted draft claims admission before current range reads.
		// It avoids both missing-account gap locks and hold-scan locks before
		// independent owners have inserted their admission rows.
		draft := CNWalletValueAccount{UserID: user}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&draft).Error; err != nil {
			return nil, err
		}
	}
	var account CNWalletValueAccount
	err := lockForUpdate(tx).Where("user_id = ?", user).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if account.CreatedAt == "" {
		unknown := int64(native.Quota)
		var wallets []CNWalletLifecycle
		if err := lockForUpdate(tx).Where("user_id = ? AND state = ?", user, "reserved").Find(&wallets).Error; err != nil {
			return nil, err
		}
		for _, row := range wallets {
			unknown += row.Reserved
		}
		var tasks []CNTaskBilling
		if err := lockForUpdate(tx).Where("user_id = ? AND source = ?", user, TaskBillingSourceWallet).Find(&tasks).Error; err != nil {
			return nil, err
		}
		for _, row := range tasks {
			unknown += int64(row.HeldQuota - row.Actual)
		}
		account = CNWalletValueAccount{UserID: user, ExpectedQuota: int64(native.Quota), UnknownQuota: unknown, OpeningQuota: unknown, OpeningBalance: int64(native.Quota), CreatedAt: time.Now().UTC().Format(CNTimeLayout)}
		if err := tx.Save(&account).Error; err != nil {
			return nil, err
		}
	}
	if sourceSwitched {
		if err := cnSuspendWalletValueTx(tx, user); err != nil {
			return nil, err
		}
		if err := lockForUpdate(tx).Where("user_id = ?", user).First(&account).Error; err != nil {
			return nil, err
		}
	}
	if account.ExpectedQuota != int64(native.Quota) {
		// External SQL cannot be fenced by GORM. Preserve cash records and
		// original receipts, but remove all remaining lots from attribution.
		var lots []CNWalletValueLot
		if err := lockForUpdate(tx).Where("user_id = ? AND isolated = ?", user, false).Order("created_at").Order("lot_id").Find(&lots).Error; err != nil {
			return nil, err
		}
		for _, lot := range lots {
			var pool CNFundingValueLot
			if err := lockForUpdate(tx).Where("lot_id = ?", lot.LotID).First(&pool).Error; err != nil {
				return nil, err
			}
			account.UnknownQuota += pool.RemainingQuota
		}
		if err := tx.Model(&CNWalletValueLot{}).Where("user_id = ?", user).Update("isolated", true).Error; err != nil {
			return nil, err
		}
		account.UnknownQuota += int64(native.Quota) - account.ExpectedQuota
		account.ExpectedQuota, account.GapCount = int64(native.Quota), account.GapCount+1
		account.Suspended = true
		if account.SuspendedAt == "" {
			account.SuspendedAt, account.SuspendReason = time.Now().UTC().Format(CNTimeLayout), "external_balance_mismatch"
		}
		if err := tx.Save(&account).Error; err != nil {
			return nil, err
		}
	}
	return &account, nil
}

func cnWalletValueDebitTx(tx *gorm.DB, user int, delta int64, admit bool) error {
	account, err := cnWalletValueAccountTx(tx, user, admit)
	if err != nil || account == nil {
		return err
	}
	return tx.Model(account).Update("expected_quota", account.ExpectedQuota-delta).Error
}

func cnWalletValueTopupTx(tx *gorm.DB, user int, quota int64, source string, snapshots []*TopupFundingSnapshot) error {
	account, err := cnWalletValueAccountTx(tx, user, true)
	if err != nil || account == nil {
		return err
	}
	var snapshot *TopupFundingSnapshot
	if len(snapshots) == 1 {
		snapshot = snapshots[0]
	}
	if !cnWalletValueAvailable() || !cnIntegrityAllocationAllowed(tx) || account.Suspended || snapshot == nil || snapshot.Payment == nil || snapshot.Conversion == nil || snapshot.Conversion.CreditedQuota != strconv.FormatInt(quota, 10) || source == "" {
		account.UnknownQuota += quota
	} else {
		payment, err := snapshot.Payment.normalized()
		if err != nil {
			return err
		}
		if payment.ActualMoney == "" {
			account.UnknownQuota += quota
		} else {
			amount, err := cnDecimal(payment.ActualMoney)
			if err != nil {
				account.UnknownQuota += quota
				account.ExpectedQuota += quota
				return tx.Save(account).Error
			}
			var topup TopUp
			err = lockForUpdate(tx).Where("trade_no = ? AND user_id = ?", source, user).First(&topup).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			encodedPayment, marshalErr := common.Marshal(payment)
			if marshalErr != nil {
				return marshalErr
			}
			encodedConversion, marshalErr := common.Marshal(snapshot.Conversion)
			if marshalErr != nil {
				return marshalErr
			}
			settled, known := topup.settledQuota()
			if err != nil || topup.Status != common.TopUpStatusSuccess || topup.CompleteTime <= 0 || !known || settled != quota || topup.PaymentSnapshot != string(encodedPayment) || topup.QuotaConversionSnapshot != string(encodedConversion) {
				account.UnknownQuota += quota
				account.ExpectedQuota += quota
				return tx.Save(account).Error
			}
			if _, err := cnLockPublisher(tx); err != nil {
				return err
			}
			var priorFacts []CNFinanceOutbox
			err = lockForUpdate(tx).Select("fact_id").Where("fact_id IN ?", []string{CNIdentity("payment", "topup", source), CNIdentity("funding_lot", "topup", source)}).Find(&priorFacts).Error
			if err != nil {
				return err
			}
			if len(priorFacts) != 0 {
				// Reader or older-writer facts are immutable and cannot be
				// enriched with native authority after their publication.
				account.UnknownQuota += quota
				account.ExpectedQuota += quota
				return tx.Save(account).Error
			}
			lot := CNWalletValueLot{LotID: CNIdentity("ordinary-wallet-lot", source), UserID: user, SourceID: source, PaymentID: "topup:" + source, Currency: payment.CurrencyCode, Quota: quota, Amount: amount.String(), Evidence: CNIdentity(topup.PaymentSnapshot, topup.QuotaConversionSnapshot, topup.Status), CreatedAt: time.Unix(topup.CompleteTime, 0).UTC().Format(CNTimeLayout)}
			if err := cnCaptureTopup(tx, topup, CNEvidence{System: "gateway-cn", RecordType: "cn_wallet_value_lot", RecordID: lot.LotID, EvidenceID: lot.Evidence}); err != nil {
				return err
			}
			var paymentFacts []CNFinanceOutbox
			if err := lockForUpdate(tx).Select("fact_id").Where("fact_id = ?", CNIdentity("payment", "topup", source)).Find(&paymentFacts).Error; err != nil {
				return err
			}
			if len(paymentFacts) == 0 {
				account.UnknownQuota += quota
				account.ExpectedQuota += quota
				return tx.Save(account).Error
			}
			if err := tx.Create(&lot).Error; err != nil {
				return err
			}
			pool, err := cnFundingValueLotTx(tx, lot.LotID, quota, amount)
			if err != nil {
				return err
			}
			// A payment that covers pre-existing debt cannot become new income
			// attributed retrospectively to an already completed charge.
			if account.UnknownQuota < 0 {
				retired := quota
				if account.UnknownQuota > -quota {
					retired = -account.UnknownQuota
				}
				value := cnDivideHalfEven(amount.Mul(decimal.NewFromInt(retired)), decimal.NewFromInt(quota))
				if retired == quota || value.GreaterThan(amount) {
					value = amount
				}
				pool.RemainingQuota, pool.RetiredQuota = quota-retired, retired
				pool.RemainingValue, pool.RetiredValue = amount.Sub(value).String(), value.String()
				if err := tx.Save(pool).Error; err != nil {
					return err
				}
				account.UnknownQuota += retired
			}
		}
	}
	account.ExpectedQuota += quota
	return tx.Save(account).Error
}

// Called only by covered native final-debit transactions, after the debit and
// before staging their charge. No reader/backfill path may allocate these lots.
func cnWalletValueChargeTx(tx *gorm.DB, f *CNFact, quota, prior int64) error {
	user, err := strconv.Atoi(*f.CustomerKey)
	if err != nil {
		return err
	}
	var account CNWalletValueAccount
	err = lockForUpdate(tx).Where("user_id = ?", user).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	key := CNIdentity(f.EconomicID, f.SegmentID)
	var charge CNWalletValueCharge
	err = lockForUpdate(tx).Where("charge_key = ?", key).First(&charge).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		charge = CNWalletValueCharge{ChargeKey: key, UserID: user, EconomicID: f.EconomicID, SegmentID: f.SegmentID, Actual: prior, UnknownQuota: prior}
		if err = tx.Create(&charge).Error; err != nil {
			return err
		}
		if prior > 0 {
			if err := tx.Create(&CNWalletValueSlice{ChargeKey: key, Quota: prior, Value: "0"}).Error; err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	if charge.UserID != user || charge.Actual != prior {
		return ErrCNConflict
	}
	var parts []CNWalletValuePart
	if err := lockForUpdate(tx).Where("charge_key = ?", key).Order("id").Find(&parts).Error; err != nil {
		return err
	}
	delta := quota - prior
	if delta > 0 {
		if account.Suspended || !cnIntegrityAllocationAllowed(tx) || cnIntegrityOpeningCharge(tx, f.EconomicID) {
			charge.UnknownQuota += delta
			account.UnknownQuota -= delta
			if err := tx.Create(&CNWalletValueSlice{ChargeKey: key, Quota: delta, Value: "0"}).Error; err != nil {
				return err
			}
			delta = 0
		}
		unknown := min(delta, max(account.UnknownQuota, 0))
		charge.UnknownQuota += unknown
		account.UnknownQuota -= unknown
		delta -= unknown
		if unknown > 0 {
			if err := tx.Create(&CNWalletValueSlice{ChargeKey: key, Quota: unknown, Value: "0"}).Error; err != nil {
				return err
			}
		}
		var lots []CNWalletValueLot
		if err := lockForUpdate(tx).Where("user_id = ? AND isolated = ?", user, false).Order("created_at").Order("lot_id").Find(&lots).Error; err != nil {
			return err
		}
		for _, lot := range lots {
			if delta == 0 {
				break
			}
			var pool CNFundingValueLot
			if err := lockForUpdate(tx).Where("lot_id = ?", lot.LotID).First(&pool).Error; err != nil {
				return err
			}
			take := min(delta, pool.RemainingQuota)
			if take == 0 {
				continue
			}
			part := CNWalletValuePart{ChargeKey: key, LotID: lot.LotID, AllocationID: CNIdentity("ordinary-wallet-allocation", key, lot.LotID)}
			var allocation CNFundingValueAllocation
			err := lockForUpdate(tx).Where("allocation_id = ?", part.AllocationID).First(&allocation).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(&part).Error; err != nil {
					return err
				}
				parts = append(parts, part)
			} else if err != nil {
				return err
			}
			amount, err := cnDecimal(lot.Amount)
			if err != nil {
				return err
			}
			priorValue := decimal.Zero
			if allocation.Value != "" {
				priorValue, err = cnDecimal(allocation.Value)
				if err != nil {
					return err
				}
			}
			value, err := cnFundingValueTx(tx, lot.LotID, part.AllocationID, lot.Quota, allocation.Quota+take, amount)
			if err != nil {
				return err
			}
			if err := tx.Create(&CNWalletValueSlice{ChargeKey: key, AllocationID: part.AllocationID, Quota: take, Value: value.Sub(priorValue).String()}).Error; err != nil {
				return err
			}
			delta -= take
		}
		charge.UnknownQuota += delta
		charge.DebtQuota += delta
		account.UnknownQuota -= delta
		if delta > 0 {
			if err := tx.Create(&CNWalletValueSlice{ChargeKey: key, Quota: delta, Value: "0", Debt: true}).Error; err != nil {
				return err
			}
		}
	} else if delta < 0 {
		remaining := -delta
		var slices []CNWalletValueSlice
		if err := lockForUpdate(tx).Where("charge_key = ? AND quota > 0", key).Order("id DESC").Find(&slices).Error; err != nil {
			return err
		}
		for _, slice := range slices {
			if remaining == 0 {
				break
			}
			take := min(remaining, slice.Quota)
			if slice.AllocationID == "" {
				charge.UnknownQuota -= take
				account.UnknownQuota += take
				if slice.Debt {
					charge.DebtQuota -= take
				}
				slice.Quota -= take
				if err := tx.Save(&slice).Error; err != nil {
					return err
				}
				remaining -= take
				continue
			}
			var allocation CNFundingValueAllocation
			if err := lockForUpdate(tx).Where("allocation_id = ?", slice.AllocationID).First(&allocation).Error; err != nil {
				return err
			}
			var lot CNWalletValueLot
			if err := lockForUpdate(tx).Where("lot_id = ?", allocation.LotID).First(&lot).Error; err != nil {
				return err
			}
			frozen, err := cnDecimal(slice.Value)
			if err != nil {
				return err
			}
			value := frozen
			if take != slice.Quota {
				value = cnDivideHalfEven(frozen.Mul(decimal.NewFromInt(take)), decimal.NewFromInt(slice.Quota))
			}
			var pool CNFundingValueLot
			if err := lockForUpdate(tx).Where("lot_id = ?", lot.LotID).First(&pool).Error; err != nil {
				return err
			}
			available, err := cnDecimal(pool.RemainingValue)
			if err != nil {
				return err
			}
			allocated, err := cnDecimal(allocation.Value)
			if err != nil || allocated.LessThan(value) || allocation.Quota < take {
				return ErrCNConflict
			}
			pool.RemainingQuota += take
			pool.RemainingValue = available.Add(value).String()
			allocation.Quota -= take
			allocation.Value = allocated.Sub(value).String()
			slice.Quota -= take
			slice.Value = frozen.Sub(value).String()
			if err := tx.Save(&pool).Error; err != nil {
				return err
			}
			if err := tx.Save(&allocation).Error; err != nil {
				return err
			}
			if err := tx.Save(&slice).Error; err != nil {
				return err
			}
			if err := cnValidateFundingValueTx(tx, &pool); err != nil {
				return err
			}
			if lot.Isolated {
				account.UnknownQuota += take
			}
			remaining -= take
		}
		if remaining != 0 {
			return ErrCNConflict
		}
	}
	charge.Actual = quota
	if err := tx.Save(&charge).Error; err != nil {
		return err
	}
	if err := tx.Save(&account).Error; err != nil {
		return err
	}
	for _, part := range parts {
		if err := cnWalletValueAllocationFactTx(tx, f, part); err != nil {
			return err
		}
	}
	f.SourceEvidence = append(f.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "cn_wallet_value_charge", RecordID: key, EvidenceID: CNIdentity(key, strconv.FormatInt(quota, 10), strconv.FormatInt(charge.UnknownQuota, 10))})
	f.SourceEvidence = append(f.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "cn_wallet_value_account", RecordID: strconv.Itoa(user), EvidenceID: CNIdentity(cnWalletValuePolicy, strconv.Itoa(user), account.CreatedAt, strconv.FormatInt(account.OpeningQuota, 10), strconv.FormatInt(account.OpeningBalance, 10))})
	return nil
}

func cnWalletValueAllocationFactTx(tx *gorm.DB, charge *CNFact, part CNWalletValuePart) error {
	var allocation CNFundingValueAllocation
	if err := lockForUpdate(tx).Where("allocation_id = ?", part.AllocationID).First(&allocation).Error; err != nil {
		return err
	}
	var lot CNWalletValueLot
	if err := lockForUpdate(tx).Where("lot_id = ?", part.LotID).First(&lot).Error; err != nil {
		return err
	}
	evidence := CNEvidence{System: "gateway-cn", RecordType: "cn_wallet_value_allocation", RecordID: part.AllocationID, EvidenceID: CNIdentity(part.AllocationID, charge.FactID, lot.Evidence, allocation.Value, strconv.FormatInt(allocation.Quota, 10))}
	f := cnBaseFact("funding_allocation", part.AllocationID, "0", charge.OperationID, "wallet", int64(lot.UserID), time.Now().UTC(), evidence)
	f.RequestID, f.TaskID = charge.RequestID, charge.TaskID
	var account CNWalletValueAccount
	if err := lockForUpdate(tx).Where("user_id = ?", lot.UserID).First(&account).Error; err != nil {
		return err
	}
	f.SourceEvidence = append(f.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "cn_wallet_value_account", RecordID: strconv.Itoa(lot.UserID), EvidenceID: CNIdentity(cnWalletValuePolicy, strconv.Itoa(lot.UserID), account.CreatedAt, strconv.FormatInt(account.OpeningQuota, 10), strconv.FormatInt(account.OpeningBalance, 10))})
	f.EffectiveAt, f.SourcePeriod = charge.EffectiveAt, charge.SourcePeriod
	f.Quality = "verified_payment"
	f.Conversion = map[string]any{"sourceUnit": "currency", "quotaPerUnit": nil, "originalCurrency": lot.Currency, "settlementCurrency": lot.Currency, "exchangeRate": "1", "fxVersion": "identity-1", "rounding": "HALF_EVEN_18"}
	var previous CNFinanceOutbox
	err := lockForUpdate(tx).Where("chain_key = ?", CNIdentity(f.Kind, f.EconomicID, f.SegmentID)).Order("revision DESC").First(&previous).Error
	priorQuota, priorValue := int64(0), decimal.Zero
	if err == nil {
		var p CNFact
		if err := common.UnmarshalJsonStr(previous.Payload, &p); err != nil {
			return err
		}
		priorQuota, err = strconv.ParseInt(*p.Cumulative.Quota, 10, 64)
		if err != nil {
			return err
		}
		priorValue, err = cnDecimal(p.Cumulative.Money.Gross)
		if err != nil {
			return err
		}
		f.Revision, f.PreviousFactID = previous.Revision+1, &previous.FactID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	charge.Links["paymentIds"] = append(charge.Links["paymentIds"], lot.PaymentID)
	charge.Links["fundingLotIds"] = append(charge.Links["fundingLotIds"], "topup-lot:"+lot.SourceID)
	charge.Links["allocationIds"] = append(charge.Links["allocationIds"], part.AllocationID)
	if priorQuota == allocation.Quota && err == nil {
		return nil
	}
	q, d := strconv.FormatInt(allocation.Quota, 10), strconv.FormatInt(allocation.Quota-priorQuota, 10)
	value, err := cnDecimal(allocation.Value)
	if err != nil {
		return err
	}
	f.Cumulative = CNMetric{Quota: &q, Money: cnUnknownTaxMoney(value.String(), lot.Currency)}
	f.Delta = CNMetric{Quota: &d, Money: cnUnknownTaxMoney(value.Sub(priorValue).String(), lot.Currency)}
	f.FactID = CNIdentity(part.AllocationID, strconv.FormatInt(f.Revision, 10))
	var restores any
	if allocation.Quota < priorQuota {
		restores = f.PreviousFactID
	}
	f.Funding = map[string]any{"allocationId": part.AllocationID, "lotId": "topup-lot:" + lot.SourceID, "chargeEconomicId": charge.EconomicID, "chargeSegmentId": charge.SegmentID, "allocatedQuota": q, "recognizedValue": f.Cumulative.Money, "policyVersion": cnWalletValuePolicy, "authority": "native", "previousAllocationFactId": f.PreviousFactID, "restoresAllocationFactId": restores, "evidence": f.SourceEvidence}
	f.Links["paymentIds"], f.Links["fundingLotIds"] = []string{lot.PaymentID}, []string{"topup-lot:" + lot.SourceID}
	f.Links["allocationIds"], f.Links["chargeFactIds"] = []string{part.AllocationID}, []string{charge.FactID}
	if err := StageCNFactTx(tx, f); err != nil {
		return err
	}
	// One revenue chain per lot supports mixed currencies and partial unknown
	// balances without guessing FX or changing the single-money charge contract.
	f.Kind, f.PostingRole = "recognized_revenue", "recognized_revenue"
	f.EconomicID = CNIdentity("ordinary-wallet-revenue", part.AllocationID)
	f.FactID = CNIdentity(f.EconomicID, strconv.FormatInt(f.Revision, 10))
	if f.PreviousFactID != nil {
		previousRevenue := CNIdentity(f.EconomicID, strconv.FormatInt(f.Revision-1, 10))
		f.PreviousFactID = &previousRevenue
	}
	f.Funding = nil
	return StageCNFactTx(tx, f)
}

func cnWalletValueProof(tx *gorm.DB, stream *CNFinanceStream, outbox []CNFinanceOutbox, published map[string]bool) (cnProofScan, error) {
	scan := cnProofScan{dependency: CNDependency{SourceID: "ordinary_wallet_native_funding", Version: "native-funding-v1", Required: true, SupportedPaths: []string{"ordinary-wallet/native-funding-v1"}, ExcludedPaths: []string{"pre-admission-balances", "unknown-bonus-and-debt", "midjourney", "realtime", "legacy-task", "uncovered-quota-writers", "external-sql-and-old-binaries", "agency-source-switch"}}}
	if !cnWalletValueAvailable() {
		scan.coverageGap = true
		scan.dependency.ExcludedPaths = append(scan.dependency.ExcludedPaths, "paid_lot_admission_disabled")
	}
	heads := map[string]CNFact{}
	for _, row := range outbox {
		if row.State == "quarantined" {
			continue
		}
		var fact CNFact
		if err := common.UnmarshalJsonStr(row.Payload, &fact); err != nil {
			return scan, err
		}
		key := CNIdentity(fact.Kind, fact.EconomicID, fact.SegmentID)
		if fact.Revision > heads[key].Revision {
			heads[key] = fact
		}
	}
	var accounts []CNWalletValueAccount
	if err := tx.Order("user_id").Find(&accounts).Error; err != nil {
		return scan, err
	}
	for _, account := range accounts {
		scan.records = append(scan.records, account)
		var native User
		if err := tx.Unscoped().Select("id", "quota", "billing_mode").First(&native, account.UserID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return scan, err
			}
			scan.records = append(scan.records, []any{account.UserID, "native_user_missing"})
			scan.dependency.UnknownCount++
			continue
		}
		scan.records = append(scan.records, []any{native.Id, native.Quota, native.BillingMode})
		if account.Suspended || account.ExpectedQuota != int64(native.Quota) || account.UnknownQuota != 0 || native.BillingMode == AgencyDurableBillingMode || native.BillingMode == AgencyProvisioningBillingMode {
			scan.dependency.UnknownCount++
		}
		var wallets []CNWalletLifecycle
		if err := tx.Where("user_id = ? AND state = ?", account.UserID, "reserved").Order("request_id").Find(&wallets).Error; err != nil {
			return scan, err
		}
		scan.records = append(scan.records, wallets)
		scan.dependency.PendingCount += int64(len(wallets))
		var tasks []CNTaskBilling
		if err := tx.Where("user_id = ? AND source = ? AND (held_quota <> actual OR pending = ?)", account.UserID, TaskBillingSourceWallet, true).Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Find(&tasks).Error; err != nil {
			return scan, err
		}
		scan.records = append(scan.records, tasks)
		scan.dependency.PendingCount += int64(len(tasks))
	}
	var lots []CNWalletValueLot
	if err := tx.Order("id").Find(&lots).Error; err != nil {
		return scan, err
	}
	for _, lot := range lots {
		var pool CNFundingValueLot
		if err := tx.Where("lot_id = ?", lot.LotID).First(&pool).Error; err != nil {
			return scan, err
		}
		scan.records = append(scan.records, []any{lot, pool})
		if lot.Isolated {
			scan.dependency.UnknownCount++
		}
		for _, key := range []string{CNIdentity("payment", lot.PaymentID, "0"), CNIdentity("funding_lot", "topup-lot:"+lot.SourceID, "0")} {
			if !published[heads[key].FactID] {
				scan.dependency.PendingCount++
			}
		}
	}
	var charges []CNWalletValueCharge
	if err := tx.Order("charge_key").Find(&charges).Error; err != nil {
		return scan, err
	}
	for _, charge := range charges {
		scan.records = append(scan.records, charge)
		if charge.UnknownQuota != 0 {
			scan.dependency.UnknownCount++
		}
		head := heads[CNIdentity("charge", charge.EconomicID, charge.SegmentID)]
		if !published[head.FactID] || head.Cumulative.Quota == nil || *head.Cumulative.Quota != strconv.FormatInt(charge.Actual, 10) {
			scan.dependency.PendingCount++
		}
	}
	var parts []CNWalletValuePart
	if err := tx.Order("id").Find(&parts).Error; err != nil {
		return scan, err
	}
	for _, part := range parts {
		var allocation CNFundingValueAllocation
		if err := tx.Where("allocation_id = ?", part.AllocationID).First(&allocation).Error; err != nil {
			return scan, err
		}
		scan.records = append(scan.records, []any{part, allocation})
		for kind, economic := range map[string]string{"funding_allocation": part.AllocationID, "recognized_revenue": CNIdentity("ordinary-wallet-revenue", part.AllocationID)} {
			head := heads[CNIdentity(kind, economic, "0")]
			if !published[head.FactID] || head.Cumulative.Quota == nil || *head.Cumulative.Quota != strconv.FormatInt(allocation.Quota, 10) || head.Cumulative.Money == nil || head.Cumulative.Money.Gross != allocation.Value {
				scan.dependency.PendingCount++
			}
		}
	}
	var slices []CNWalletValueSlice
	if err := tx.Order("id").Find(&slices).Error; err != nil {
		return scan, err
	}
	scan.records = append(scan.records, slices)
	if len(accounts) == 0 || len(lots) == 0 || len(parts) == 0 {
		scan.coverageGap = true
		scan.dependency.ExcludedPaths = append(scan.dependency.ExcludedPaths, "native_activity_not_observed")
	}
	return scan, nil
}
