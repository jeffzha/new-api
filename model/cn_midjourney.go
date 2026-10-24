package model

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func CNMidjourneyRequested() bool { return os.Getenv("OPS_CN_DURABLE_MIDJOURNEY_ENABLED") == "true" }

func CNDurableMidjourneyAvailable() bool {
	return CNMidjourneyRequested() && CNFinanceCaptureEnabled() && !common.RedisEnabled && !common.BatchUpdateEnabled
}

func cnMigrateMidjourney(db *gorm.DB) error {
	if err := db.AutoMigrate(&CNMidjourneyBilling{}, &CNMidjourneyEffect{}); err != nil {
		return err
	}
	if db.Migrator().HasTable(&Midjourney{}) {
		return db.AutoMigrate(&Midjourney{})
	}
	return nil
}

// This receipt is retained independently of task retention. Prepared means no
// debit has committed. Refund intent and retry metadata survive process loss.
type CNMidjourneyBilling struct {
	Key                string `gorm:"size:64;primaryKey"`
	TaskID             int    `gorm:"not null;uniqueIndex"`
	UserID             int    `gorm:"not null;index"`
	MjID               string `gorm:"size:191;not null"`
	TokenID            int
	TokenUnlimited     bool
	Playground         bool
	ChannelID          int
	Quota              int
	Action             string `gorm:"size:40"`
	State              string `gorm:"size:32;not null;index"`
	Version            int64
	FactID             string `gorm:"size:300"`
	Currency           string `gorm:"size:3"`
	QuotaPerUnit       string `gorm:"size:128"`
	ExchangeRate       string `gorm:"size:128"`
	ConversionVersion  string `gorm:"size:191"`
	CreatedAt          string `gorm:"size:40;not null"`
	FinalizedAt        string `gorm:"size:40"`
	RefundRequestedAt  string `gorm:"size:40"`
	RefundReason       string `gorm:"size:512"`
	TokenRefundStatus  string `gorm:"size:40"`
	TokenDisposedQuota int    `gorm:"not null;default:0"`
	Attempts           int
	NextAttemptAt      int64  `gorm:"index"`
	LastErrorCode      string `gorm:"size:64"`
}

func PrepareCNMidjourney(task *Midjourney, token int, unlimited, playground bool) error {
	if !CNDurableMidjourneyAvailable() || task.UserId <= 0 || task.MjId == "" || len(task.MjId) > 191 || task.Quota < 0 || task.Quota > common.MaxQuota || (!playground && token <= 0) || task.GetBillingChannelId() <= 0 || task.AgencyChargeID != "" {
		return ErrCNConflict
	}
	unit := strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
	currency := operation_setting.GetQuotaDisplayType()
	if currency != "USD" && currency != "CNY" {
		currency = ""
	}
	rate := strconv.FormatFloat(operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate), 'f', -1, 64)
	key := CNIdentity("midjourney", strconv.Itoa(task.UserId), task.MjId)
	task.CNBillingKey = key
	task.cnBilling = &CNMidjourneyBilling{Key: key, UserID: task.UserId, MjID: task.MjId, TokenID: token, TokenUnlimited: unlimited, Playground: playground, ChannelID: task.GetBillingChannelId(), Quota: task.Quota, Action: task.Action, State: "prepared", Currency: currency, QuotaPerUnit: unit, ExchangeRate: rate, ConversionVersion: CNIdentity("gateway-operation-settings-v1", unit, currency, rate), CreatedAt: time.Now().UTC().Format(CNTimeLayout)}
	return nil
}

func cnInsertMidjourney(task *Midjourney) error {
	if task.cnBilling == nil || task.CNBillingKey != task.cnBilling.Key || task.cnBilling.UserID != task.UserId || task.cnBilling.MjID != task.MjId || task.cnBilling.ChannelID != task.GetBillingChannelId() || task.cnBilling.Quota != task.Quota {
		return ErrCNConflict
	}
	original := *task
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := cnLockSubscriptionUserTx(tx, task.CNBillingKey, task.UserId); err != nil {
			return err
		}
		var duplicates int64
		if err := tx.Model(&Midjourney{}).Where("user_id = ? AND mj_id = ?", task.UserId, task.MjId).Count(&duplicates).Error; err != nil {
			return err
		}
		if duplicates > 0 {
			return ErrCNConflict
		}
		task.Quota, task.TokenId = 0, 0
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		receipt := *task.cnBilling
		receipt.TaskID = task.Id
		if task.Status == "FAILURE" || task.FailReason != "" {
			receipt.RefundRequestedAt, receipt.RefundReason = receipt.CreatedAt, "persisted_task_failure"
		}
		return tx.Create(&receipt).Error
	})
	if err != nil {
		*task = original
	}
	return err
}

// User -> task -> receipt -> token -> channel -> publisher is the writer order.
func cnMidjourneyRowsTx(tx *gorm.DB, taskID, user int, mj string) (*Midjourney, *CNMidjourneyBilling, error) {
	key := CNIdentity("midjourney", strconv.Itoa(user), mj)
	if err := cnLockSubscriptionUserTx(tx, key, user); err != nil {
		return nil, nil, err
	}
	var task Midjourney
	if err := lockForUpdate(tx).First(&task, taskID).Error; err != nil {
		return nil, nil, err
	}
	if task.UserId != user || task.MjId != mj || task.CNBillingKey != key || task.AgencyChargeID != "" || task.AgencyBillingEventID != "" {
		return nil, nil, ErrCNConflict
	}
	var receipt CNMidjourneyBilling
	if err := lockForUpdate(tx).Where(map[string]any{"key": key}).First(&receipt).Error; err != nil {
		return nil, nil, err
	}
	if receipt.UserID != user || receipt.MjID != mj || receipt.TaskID != taskID || receipt.ChannelID != task.GetBillingChannelId() || receipt.Quota < 0 || receipt.Quota > common.MaxQuota {
		return nil, nil, ErrCNConflict
	}
	if _, err := time.Parse(CNTimeLayout, receipt.CreatedAt); err != nil {
		return nil, nil, ErrCNConflict
	}
	switch receipt.State {
	case "prepared":
		if receipt.Version != 0 || receipt.FactID != "" {
			return nil, nil, ErrCNConflict
		}
	case "charged", "cancelled":
		if receipt.Version != 1 || receipt.FactID == "" {
			return nil, nil, ErrCNConflict
		}
	case "refunded":
		if receipt.Version != 2 || receipt.FactID == "" {
			return nil, nil, ErrCNConflict
		}
	default:
		return nil, nil, ErrCNConflict
	}
	if receipt.State == "charged" && (task.Quota != receipt.Quota || (!receipt.Playground && task.TokenId != receipt.TokenID)) {
		return nil, nil, ErrCNConflict
	}
	return &task, &receipt, nil
}

func cnMidjourneyTokenTx(tx *gorm.DB, receipt *CNMidjourneyBilling, delta int, refund bool) (string, error) {
	if receipt.Playground {
		return "playground", nil
	}
	var token Token
	err := lockForUpdate(tx.Unscoped()).Where("id = ?", receipt.TokenID).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && refund {
		return "missing_token_not_recredited", nil
	}
	if err != nil {
		return "", err
	}
	if token.UserId != receipt.UserID {
		return "", ErrCNConflict
	}
	if token.DeletedAt.Valid {
		if refund {
			return "deleted_token_not_recredited", nil
		}
		return "", ErrCNConflict
	}
	remain, used := int64(token.RemainQuota)-int64(delta), int64(token.UsedQuota)+int64(delta)
	if !refund && !receipt.TokenUnlimited && remain < 0 {
		return "", ErrInsufficientAgencyTokenQuota
	}
	if remain < -int64(common.MaxWalletQuota) || remain > int64(common.MaxWalletQuota) || used < 0 || used > int64(common.MaxWalletQuota) {
		return "", ErrWalletQuotaLimitExceeded
	}
	err = tx.Model(&Token{}).Where("id = ? AND user_id = ?", token.Id, receipt.UserID).Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": common.GetTimestamp()}).Error
	return "recredited", err
}

func cnMidjourneyStageTx(tx *gorm.DB, receipt *CNMidjourneyBilling, state string, at time.Time) error {
	prior, actual := 0, 0
	if receipt.State == "charged" {
		prior = receipt.Quota
	}
	if state == "charged" {
		actual = receipt.Quota
	}
	operation := CNIdentity("midjourney-terminal", receipt.Key, state)
	evidence := CNEvidence{System: "gateway-cn", RecordType: "cn_midjourney_" + state, RecordID: receipt.Key, EvidenceID: operation}
	fact := cnBaseFact("charge", "midjourney:"+strconv.Itoa(receipt.UserID)+":"+receipt.MjID, "0", operation, "wallet", int64(receipt.UserID), at, evidence)
	fact.TaskID = &receipt.MjID
	fact.Status, fact.Quality = "settled", "settled_quota"
	if state != "charged" {
		fact.Status = "reversed"
	}
	fact.Revision = receipt.Version + 1
	if receipt.FactID != "" {
		fact.PreviousFactID = &receipt.FactID
	}
	fact.EffectiveAt, fact.SourcePeriod = receipt.CreatedAt, receipt.CreatedAt[:7]
	cumulative, delta := strconv.Itoa(actual), strconv.Itoa(actual-prior)
	fact.Cumulative.Quota, fact.Delta.Quota = &cumulative, &delta
	if opsMoneyCurrency.MatchString(receipt.Currency) {
		fact.Conversion = map[string]any{"sourceUnit": "quota", "quotaPerUnit": receipt.QuotaPerUnit, "originalCurrency": "USD", "settlementCurrency": receipt.Currency, "exchangeRate": receipt.ExchangeRate, "fxVersion": receipt.ConversionVersion, "rounding": "HALF_EVEN_18"}
		fact.Cumulative.Money = cnQuotaMoney(int64(actual), fact.Conversion)
		fact.Delta.Money = cnQuotaMoney(int64(actual-prior), fact.Conversion)
		fact.SourceEvidence = append(fact.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "gateway_operation_currency_snapshot", RecordID: receipt.ConversionVersion, EvidenceID: receipt.ConversionVersion})
	}
	if _, err := cnLockPublisher(tx); err != nil {
		return err
	}
	if err := StageCNFactTx(tx, fact); err != nil {
		return err
	}
	if err := cnStageMidjourneyEffectTx(tx, receipt, state, actual-prior, at); err != nil {
		return err
	}
	receipt.State, receipt.Version, receipt.FactID = state, fact.Revision, fact.FactID
	receipt.FinalizedAt, receipt.LastErrorCode = at.Format(CNTimeLayout), ""
	return tx.Save(receipt).Error
}

func cnApplyMidjourneyTx(tx *gorm.DB, task *Midjourney, receipt *CNMidjourneyBilling, refund bool, at time.Time) error {
	state, delta := "charged", receipt.Quota
	if refund {
		state, delta = "refunded", -receipt.Quota
		if receipt.State == "prepared" {
			state, delta = "cancelled", 0
		}
	}
	if !refund && receipt.State != "prepared" {
		if receipt.State == "charged" {
			return nil
		}
		return ErrCNConflict
	}
	if refund && (receipt.State == "refunded" || receipt.State == "cancelled") {
		return nil
	}
	if delta != 0 || state == "charged" || state == "refunded" {
		tokenState, err := cnMidjourneyTokenTx(tx, receipt, delta, refund)
		if err != nil {
			return err
		}
		if refund {
			receipt.TokenRefundStatus = tokenState
			if tokenState == "missing_token_not_recredited" || tokenState == "deleted_token_not_recredited" {
				receipt.TokenDisposedQuota = receipt.Quota
			}
		}
		var account User
		if err := tx.First(&account, receipt.UserID).Error; err != nil {
			return err
		}
		quota, used := int64(account.Quota)-int64(delta), int64(account.UsedQuota)+int64(delta)
		if quota < 0 || quota > int64(common.MaxWalletQuota) || used < 0 || used > int64(common.MaxWalletQuota) {
			return ErrWalletQuotaLimitExceeded
		}
		updates := map[string]any{"quota": quota, "used_quota": used}
		if state == "charged" {
			updates["request_count"] = gorm.Expr("request_count + 1")
		}
		if err := tx.Model(&User{}).Where("id = ?", receipt.UserID).Updates(updates).Error; err != nil {
			return err
		}
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, receipt.ChannelID).Error; err != nil {
			return err
		}
		if int64(channel.UsedQuota)+int64(delta) < 0 || int64(channel.UsedQuota)+int64(delta) > int64(common.MaxWalletQuota) {
			return ErrWalletQuotaLimitExceeded
		}
		if err := tx.Model(&Channel{}).Where("id = ?", receipt.ChannelID).Update("used_quota", gorm.Expr("used_quota + ?", delta)).Error; err != nil {
			return err
		}
	}
	quota, token := 0, receipt.TokenID
	if state == "charged" {
		quota = receipt.Quota
	}
	if receipt.Playground {
		token = 0
	}
	result := tx.Model(&Midjourney{}).Where("id = ? AND cn_billing_key = ? AND quota = ?", task.Id, receipt.Key, task.Quota).Updates(map[string]any{"quota": quota, "token_id": token})
	if result.Error != nil {
		return result.Error
	}
	// MySQL reports zero changed rows for a zero-quota success. The locked task
	// identity is already verified; the receipt is the permanent terminal CAS.
	return cnMidjourneyStageTx(tx, receipt, state, at)
}

func SettleCNMidjourney(ctx context.Context, db *gorm.DB, taskID, user int, mj string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, receipt, err := cnMidjourneyRowsTx(tx, taskID, user, mj)
		if err != nil {
			return err
		}
		if receipt.RefundRequestedAt != "" || task.Status == "FAILURE" {
			return ErrCNConflict
		}
		return cnApplyMidjourneyTx(tx, task, receipt, false, time.Now().UTC())
	})
}

func RequestCNMidjourneyRefund(ctx context.Context, db *gorm.DB, taskID, user int, mj, reason string) error {
	if len(reason) > 512 {
		return ErrCNConflict
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, receipt, err := cnMidjourneyRowsTx(tx, taskID, user, mj)
		if err != nil {
			return err
		}
		if receipt.RefundRequestedAt != "" {
			return nil
		}
		return tx.Model(receipt).Updates(map[string]any{"refund_requested_at": time.Now().UTC().Format(CNTimeLayout), "refund_reason": reason, "next_attempt_at": int64(0)}).Error
	})
}

func CompleteCNMidjourneyRefund(ctx context.Context, db *gorm.DB, key string) error {
	var identity CNMidjourneyBilling
	if err := db.WithContext(ctx).Where(map[string]any{"key": key}).First(&identity).Error; err != nil {
		return err
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, receipt, err := cnMidjourneyRowsTx(tx, identity.TaskID, identity.UserID, identity.MjID)
		if err != nil {
			return err
		}
		if _, err = time.Parse(CNTimeLayout, receipt.RefundRequestedAt); err != nil {
			return err
		}
		return cnApplyMidjourneyTx(tx, task, receipt, true, time.Now().UTC())
	})
	if err != nil {
		failure := db.WithContext(context.WithoutCancel(ctx)).Model(&CNMidjourneyBilling{}).Where(map[string]any{"key": key}).Where("state IN ?", []string{"prepared", "charged"}).Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_attempt_at": time.Now().Unix() + 5, "last_error_code": "midjourney_refund_transaction_failed"}).Error
		return errors.Join(err, failure)
	}
	return nil
}

func RecoverCNMidjourney(ctx context.Context, db *gorm.DB, limit int) error {
	var rows []CNMidjourneyBilling
	if err := db.WithContext(ctx).Where("(state = ? OR (state = ? AND refund_requested_at <> ?)) AND next_attempt_at <= ?", "prepared", "charged", "", time.Now().Unix()).Order("next_attempt_at").Order("created_at").Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Limit(min(max(limit, 1), 1000)).Find(&rows).Error; err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		var err error
		if row.RefundRequestedAt != "" {
			err = CompleteCNMidjourneyRefund(ctx, db, row.Key)
		} else if row.State == "prepared" {
			err = SettleCNMidjourney(ctx, db, row.TaskID, row.UserID, row.MjID)
		}
		if err != nil {
			failures = append(failures, err)
			failure := db.WithContext(context.WithoutCancel(ctx)).Model(&CNMidjourneyBilling{}).Where(map[string]any{"key": row.Key}).Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_attempt_at": time.Now().Unix() + 5, "last_error_code": "midjourney_recovery_failed"}).Error
			failures = append(failures, failure)
		}
	}
	return errors.Join(failures...)
}

func cnUpdateMidjourney(task *Midjourney, fromStatus *string) (bool, error) {
	won := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		_, receipt, err := cnMidjourneyRowsTx(tx, task.Id, task.UserId, task.MjId)
		if err != nil {
			return err
		}
		query := tx.Model(&Midjourney{}).Where("id = ? AND cn_billing_key = ?", task.Id, receipt.Key)
		if fromStatus != nil {
			query = query.Where("status = ?", *fromStatus)
		}
		result := query.Select("*").Omit("id", "user_id", "mj_id", "quota", "token_id", "billing_channel_id", "channel_id", "agency_charge_id", "agency_billing_event_id", "cn_billing_key").Updates(task)
		if result.Error != nil {
			return result.Error
		}
		won = result.RowsAffected > 0
		if won && (task.Status == "FAILURE" || task.FailReason != "") && receipt.RefundRequestedAt == "" {
			return tx.Model(receipt).Updates(map[string]any{"refund_requested_at": time.Now().UTC().Format(CNTimeLayout), "refund_reason": "persisted_task_failure", "next_attempt_at": int64(0)}).Error
		}
		return nil
	})
	return won, err
}

func cnBulkMidjourney(query *gorm.DB, params map[string]any) error {
	var tasks []Midjourney
	if err := query.Find(&tasks).Error; err != nil {
		return err
	}
	var legacyIDs []int
	for _, task := range tasks {
		if task.CNBillingKey == "" {
			legacyIDs = append(legacyIDs, task.Id)
			continue
		}
		for key, value := range params {
			text, ok := value.(string)
			if !ok {
				return ErrCNConflict
			}
			switch key {
			case "status":
				task.Status = text
			case "progress":
				task.Progress = text
			case "fail_reason":
				task.FailReason = text
			default:
				return ErrCNConflict
			}
		}
		if _, err := cnUpdateMidjourney(&task, nil); err != nil {
			return err
		}
	}
	if len(legacyIDs) == 0 {
		return nil
	}
	return query.Session(&gorm.Session{NewDB: true}).Model(&Midjourney{}).Where("id IN ? AND (cn_billing_key = ? OR cn_billing_key IS NULL)", legacyIDs, "").Updates(params).Error
}

func cnMidjourneyProofs(tx *gorm.DB, stream *CNFinanceStream, published map[string]bool) ([]cnProofScan, error) {
	durable := cnProofScan{dependency: CNDependency{SourceID: "ordinary_midjourney_durable", Version: "midjourney-terminal-v1", Required: true, SupportedPaths: []string{"midjourney/prepare/charge/refund/recovery", "midjourney-deleted-token-disposition"}, ExcludedPaths: []string{"pre-epoch", "agency-midjourney", "legacy-midjourney", "redis", "batch", "email-and-telemetry"}}}
	if !CNDurableMidjourneyAvailable() {
		durable.coverageGap = true
		durable.dependency.ExcludedPaths = append(durable.dependency.ExcludedPaths, "durable_midjourney_lane_unavailable")
	}
	var rows []CNMidjourneyBilling
	if err := tx.Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		durable.records = append(durable.records, row)
		if row.State == "prepared" || (row.RefundRequestedAt != "" && row.State == "charged") || !published[row.FactID] {
			durable.dependency.PendingCount++
		}
		if row.LastErrorCode != "" || (row.State != "prepared" && cnQuotaMoney(0, map[string]any{"quotaPerUnit": row.QuotaPerUnit, "exchangeRate": row.ExchangeRate, "settlementCurrency": row.Currency, "originalCurrency": "USD"}) == nil) {
			durable.dependency.UnknownCount++
		}
	}
	var effects int64
	if err := tx.Model(&CNMidjourneyEffect{}).Where("state = ?", "pending").Count(&effects).Error; err != nil {
		return nil, err
	}
	durable.dependency.PendingCount += effects
	legacy := cnProofScan{coverageGap: true, unsupportedScope: true, dependency: CNDependency{SourceID: "midjourney_legacy", Version: "measured-midjourney-gap-v1", Required: true, SupportedPaths: []string{}, ExcludedPaths: []string{"writer_coverage_unproven", "pre-epoch", "legacy-and-agency-midjourney"}}}
	if tx.Migrator().HasTable(&Midjourney{}) {
		var tasks []Midjourney
		if err := tx.Select("id", "user_id", "mj_id", "quota", "status").Where("cn_billing_key = ? OR cn_billing_key IS NULL", "").Order("id").Find(&tasks).Error; err != nil {
			return nil, err
		}
		for _, task := range tasks {
			legacy.records = append(legacy.records, []any{task.Id, task.UserId, task.MjId, task.Quota, task.Status})
			if task.Quota != 0 {
				legacy.dependency.UnknownCount++
			}
		}
	}
	return []cnProofScan{durable, legacy}, nil
}

func cnMidjourneyModelName(action string) string {
	if action == "SWAP_FACE" {
		return "swap_face"
	}
	return "mj_" + strings.ToLower(action)
}
