package model

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func CNTaskRequested() bool { return os.Getenv("OPS_CN_DURABLE_TASK_ENABLED") == "true" }

func CNDurableTaskAvailable() bool {
	return CNTaskRequested() && CNFinanceCaptureEnabled() && !common.RedisEnabled && !common.BatchUpdateEnabled
}

// RequestID owns the only charge chain. Provider identity is bound once after
// acceptance; neither a lost response nor a failed local insert permits resubmit.
type CNTaskBilling struct {
	Key                string `gorm:"size:64;primaryKey"`
	RequestID          string `gorm:"size:64;not null;uniqueIndex"`
	PublicTaskID       string `gorm:"size:191;not null;uniqueIndex"`
	TaskID             int64  `gorm:"index"`
	UserID             int    `gorm:"not null;index"`
	TokenID            int
	TokenUnlimited     bool
	Playground         bool
	ChannelID          int
	Platform           string `gorm:"size:30"`
	Source             string `gorm:"size:32"`
	SubscriptionID     int
	UsageGeneration    int64
	PeriodBound        bool
	InitialReserve     int
	SubmitQuota        int
	HeldQuota          int
	Actual             int
	State              string `gorm:"size:32;not null;index"`
	Version            int64
	UpstreamID         string `gorm:"size:191"`
	BodyDigest         string `gorm:"size:64"`
	CredentialDigest   string `gorm:"size:64"`
	Seed               string `gorm:"type:text"`
	SeedPrivate        string `gorm:"type:text"`
	AcceptedTask       string `gorm:"type:text"`
	ProviderBill       string `gorm:"type:text"`
	Pending            bool   `gorm:"index"`
	Target             int
	Terminal           bool
	FinalError         string `gorm:"size:64"`
	Currency           string `gorm:"size:3"`
	QuotaPerUnit       string `gorm:"size:128"`
	ExchangeRate       string `gorm:"size:128"`
	ConversionVersion  string `gorm:"size:191"`
	CreatedAt          string `gorm:"size:40"`
	FinalizedAt        string `gorm:"size:40"`
	FactID             string `gorm:"size:300"`
	TokenRefundStatus  string `gorm:"size:40"`
	TokenDisposedQuota int
	Attempts           int
	NextAttemptAt      int64  `gorm:"index"`
	LastErrorCode      string `gorm:"size:64"`
}

// Task's public JSON intentionally omits private data and durable side-effect
// intents. Recovery must serialize those fields explicitly.
type cnTaskAcceptance struct {
	Task                         *Task           `json:"task"`
	Private                      TaskPrivateData `json:"private"`
	BillingReconciliationPending bool            `json:"billing_reconciliation_pending"`
	OpsUsagePending              bool            `json:"ops_usage_pending"`
}

// A single unique request claim arbitrates first preparation across receipt
// tables, including requests presented concurrently by different users.
type CNTaskRequestOwner struct {
	RequestID string `gorm:"size:191;primaryKey"`
	UserID    int    `gorm:"not null"`
	Lane      string `gorm:"size:32;not null"`
}

func cnClaimTaskRequestTx(tx *gorm.DB, request string, user int, lane string) error {
	if !tx.Migrator().HasTable(&CNTaskRequestOwner{}) {
		return nil
	}
	claim := CNTaskRequestOwner{RequestID: request, UserID: user, Lane: lane}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim).Error; err != nil {
		return err
	}
	var owner CNTaskRequestOwner
	if err := lockForUpdate(tx).Where("request_id = ?", request).First(&owner).Error; err != nil {
		return err
	}
	if owner.UserID != user || owner.Lane != lane {
		return ErrCNConflict
	}
	// Requests reserved before the claim table existed still own their lane.
	// Check under the unique claim lock before adopting a historical request.
	// Their immutable identity needs no gap lock on unrelated absent requests.
	for _, historical := range []struct {
		model any
		lane  string
	}{{&CNWalletLifecycle{}, "wallet"}, {&CNSubscriptionLifecycle{}, "subscription"}, {&CNTaskBilling{}, "task"}} {
		var receipt struct{ UserID int }
		err := tx.Model(historical.model).Select("user_id").Where("request_id = ?", request).Take(&receipt).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if historical.lane != lane || receipt.UserID != user {
			return ErrCNConflict
		}
	}
	return nil
}

func MigrateCNTask(db *gorm.DB) error {
	if err := db.AutoMigrate(&CNTaskBilling{}, &CNTaskEffect{}, &CNTaskRequestOwner{}); err != nil {
		return err
	}
	if db.Migrator().HasTable(&Task{}) {
		return db.AutoMigrate(&Task{})
	}
	return nil
}

// Only immutable routing metadata is read outside the transaction. The first
// transactional statement locks the user, before reading task/receipt state.
func cnTaskTransaction(ctx context.Context, db *gorm.DB, key string, apply func(*gorm.DB, *Task, *CNTaskBilling) error) error {
	var identity CNTaskBilling
	if err := db.WithContext(ctx).Select("user_id").Where(map[string]any{"key": key}).First(&identity).Error; err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, row, err := cnTaskRowsTx(tx, key, identity.UserID)
		if err != nil {
			return err
		}
		return apply(tx, task, row)
	})
}

func cnTaskRowsTx(tx *gorm.DB, key string, user int) (*Task, *CNTaskBilling, error) {
	if err := cnLockSubscriptionUserTx(tx, key, user); err != nil {
		return nil, nil, err
	}
	var identity CNTaskBilling
	if err := tx.Where(map[string]any{"key": key}).First(&identity).Error; err != nil {
		return nil, nil, err
	}
	if identity.UserID != user {
		return nil, nil, ErrCNConflict
	}
	var task *Task
	if identity.TaskID != 0 {
		task = &Task{}
		if err := lockForUpdate(tx).First(task, identity.TaskID).Error; err != nil {
			return nil, nil, err
		}
	}
	var row CNTaskBilling
	if err := lockForUpdate(tx).Where(map[string]any{"key": key}).First(&row).Error; err != nil {
		return nil, nil, err
	}
	if row.UserID != identity.UserID || row.TaskID != identity.TaskID || row.Key != CNIdentity("ordinary-task", strconv.Itoa(row.UserID), row.RequestID) || row.InitialReserve < 0 || row.HeldQuota < 0 || row.HeldQuota > common.MaxQuota || row.Actual < 0 || row.Actual > common.MaxQuota || row.Version < 1 {
		return nil, nil, ErrCNConflict
	}
	if _, err := time.Parse(CNTimeLayout, row.CreatedAt); err != nil {
		return nil, nil, ErrCNConflict
	}
	switch row.State {
	case "reserved", "dispatching", "unknown", "accepted", "active", "final", "cancelled":
	default:
		return nil, nil, ErrCNConflict
	}
	if task != nil && (task.CNBillingKey != key || task.UserId != row.UserID || task.TaskID != row.PublicTaskID || task.ChannelId != row.ChannelID || string(task.Platform) != row.Platform || task.PrivateData.UpstreamTaskID != row.UpstreamID || task.Quota != row.Actual || task.PrivateData.TokenId != row.TokenID || task.PrivateData.BillingSource != row.Source || task.PrivateData.SubscriptionId != row.SubscriptionID || task.PrivateData.Execution == nil || task.PrivateData.Execution.RequestID != row.RequestID) {
		return nil, nil, ErrCNConflict
	}
	return task, &row, nil
}

func PrepareCNTask(seed *Task, request, source string, quota, token int, unlimited, playground bool) (*CNTaskBilling, error) {
	if !CNDurableTaskAvailable() || seed == nil || seed.UserId <= 0 || seed.ChannelId <= 0 || seed.TaskID == "" || len(seed.TaskID) > 191 || request == "" || len(request) > 64 || quota < 0 || quota > common.MaxQuota || (!playground && token <= 0) || (source != TaskBillingSourceWallet && source != TaskBillingSourceSubscription) || (seed.PrivateData.BillingContext != nil && seed.PrivateData.BillingContext.AgencyPricing != nil) {
		return nil, ErrCNConflict
	}
	key := CNIdentity("ordinary-task", strconv.Itoa(seed.UserId), request)
	now := GetDBTimestamp()
	var prepared CNTaskBilling
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := cnLockSubscriptionUserTx(tx, key, seed.UserId); err != nil {
			return err
		}
		if err := cnClaimTaskRequestTx(tx, request, seed.UserId, "task"); err != nil {
			return err
		}
		var existing CNTaskBilling
		err := lockForUpdate(tx).Where("request_id = ?", request).First(&existing).Error
		if err == nil {
			if existing.Key != key || existing.PublicTaskID != seed.TaskID || existing.Source != source || existing.ChannelID != seed.ChannelId || existing.Platform != string(seed.Platform) || existing.InitialReserve != quota || existing.TokenID != token || existing.TokenUnlimited != unlimited || existing.Playground != playground || existing.State != "reserved" {
				return ErrCNConflict
			}
			prepared = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Refuse dual ownership, including an old request receipt whose fact is
		// not published yet. Historical task rows are never adopted.
		for _, target := range []struct {
			model any
			query string
			value any
		}{{&CNWalletLifecycle{}, "request_id = ?", request}, {&CNSubscriptionLifecycle{}, "request_id = ?", request}, {&CNFinanceOutbox{}, "chain_key = ?", CNIdentity("charge", "request:"+request, "0")}, {&Task{}, "task_id = ?", seed.TaskID}} {
			var count int64
			if err := tx.Model(target.model).Where(target.query, target.value).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return ErrCNConflict
			}
		}
		unit := strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
		currency := operation_setting.GetQuotaDisplayType()
		if currency != "USD" && currency != "CNY" {
			currency = ""
		}
		rate := strconv.FormatFloat(operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate), 'f', -1, 64)
		prepared = CNTaskBilling{Key: key, RequestID: request, PublicTaskID: seed.TaskID, UserID: seed.UserId, TokenID: token, TokenUnlimited: unlimited, Playground: playground, ChannelID: seed.ChannelId, Platform: string(seed.Platform), Source: source, InitialReserve: quota, State: "reserved", Version: 1, Currency: currency, QuotaPerUnit: unit, ExchangeRate: rate, ConversionVersion: CNIdentity("gateway-operation-settings-v1", unit, currency, rate), CreatedAt: time.Now().UTC().Format(CNTimeLayout), NextAttemptAt: time.Now().Unix() + 600}
		prepared.CredentialDigest = CNIdentity("ordinary-task-credential", seed.PrivateData.Key)
		if err := tx.Create(&prepared).Error; err != nil {
			return err
		}
		if source == TaskBillingSourceSubscription {
			if quota <= 0 {
				return ErrCNConflict
			}
			result, err := preConsumeUserSubscriptionTx(tx, request, seed.UserId, int64(quota), now)
			if err != nil {
				return err
			}
			var sub UserSubscription
			if err := lockForUpdate(tx).First(&sub, result.UserSubscriptionId).Error; err != nil {
				return err
			}
			prepared.SubscriptionID, prepared.UsageGeneration, prepared.PeriodBound = sub.Id, sub.UsageGeneration, true
		} else {
			var account User
			if err := tx.First(&account, seed.UserId).Error; err != nil {
				return err
			}
			if account.Quota < quota {
				return ErrInsufficientAgencyWalletQuota
			}
			if err := cnWalletValueDebitTx(tx, seed.UserId, int64(quota), true); err != nil {
				return err
			}
			if err := cnWalletValueWrite(tx).Model(&account).Update("quota", gorm.Expr("quota - ?", quota)).Error; err != nil {
				return err
			}
		}
		if err := cnTaskTokenTx(tx, &prepared, quota); err != nil {
			return err
		}
		prepared.HeldQuota = quota
		copySeed := *seed
		copySeed.CNBillingKey = key
		copySeed.PrivateData.Key = ""
		copySeed.PrivateData.BillingSource, copySeed.PrivateData.SubscriptionId, copySeed.PrivateData.TokenId = source, prepared.SubscriptionID, token
		copySeed.PrivateData.Execution = &TaskExecutionSnapshot{RequestID: request}
		payload, err := common.Marshal(copySeed)
		if err != nil {
			return err
		}
		// Task's public JSON hides private_data; serialize the safe private
		// snapshot explicitly so recovery retains frozen pricing and identity.
		private, err := common.Marshal(copySeed.PrivateData)
		if err != nil {
			return err
		}
		prepared.Seed = string(payload)
		prepared.SeedPrivate = string(private)
		return tx.Save(&prepared).Error
	})
	return &prepared, err
}

func cnTaskTokenTx(tx *gorm.DB, row *CNTaskBilling, delta int) error {
	if delta == 0 {
		return nil
	}
	receipt := CNMidjourneyBilling{UserID: row.UserID, TokenID: row.TokenID, TokenUnlimited: row.TokenUnlimited, Playground: row.Playground}
	status, err := cnMidjourneyTokenTx(tx, &receipt, delta, delta < 0)
	if err != nil {
		return err
	}
	if delta < 0 {
		row.TokenRefundStatus = status
		if status == "missing_token_not_recredited" || status == "deleted_token_not_recredited" {
			row.TokenDisposedQuota -= delta
		}
	}
	return nil
}

func cnTaskFundingTx(tx *gorm.DB, row *CNTaskBilling, delta int, available bool) error {
	if row.Source == TaskBillingSourceSubscription {
		period := CNSubscriptionLifecycle{UserID: row.UserID, SubscriptionID: row.SubscriptionID, UsageGeneration: row.UsageGeneration, PeriodBound: row.PeriodBound}
		if err := cnSubscriptionDeltaTx(tx, &period, int64(delta)); err != nil {
			return err
		}
	} else if row.Source == TaskBillingSourceWallet {
		if err := cnWalletValueDebitTx(tx, row.UserID, int64(delta), false); err != nil {
			return err
		}
		var account User
		if err := tx.First(&account, row.UserID).Error; err != nil {
			return err
		}
		balance := int64(account.Quota) - int64(delta)
		if balance < 0 || balance > int64(common.MaxWalletQuota) {
			if available && balance < 0 {
				return ErrInsufficientAgencyWalletQuota
			}
			return ErrWalletQuotaLimitExceeded
		}
		if err := cnWalletValueWrite(tx).Model(&account).Update("quota", balance).Error; err != nil {
			return err
		}
	} else {
		return ErrCNConflict
	}
	return cnTaskTokenTx(tx, row, delta)
}

func BeginCNTaskDispatch(key, digest string, channel int, platform string) error {
	return cnTaskTransaction(context.Background(), DB, key, func(tx *gorm.DB, _ *Task, row *CNTaskBilling) error {
		if row.State != "reserved" || row.Pending || len(digest) != 64 || row.ChannelID != channel || row.Platform != platform {
			return ErrCNConflict
		}
		return tx.Model(row).Updates(map[string]any{"state": "dispatching", "body_digest": digest, "next_attempt_at": time.Now().Unix() + 600}).Error
	})
}

func RecordCNTaskUpstream(key, upstream string) error {
	if len(upstream) > 191 {
		return ErrCNConflict
	}
	return cnTaskTransaction(context.Background(), DB, key, func(tx *gorm.DB, _ *Task, row *CNTaskBilling) error {
		if row.UpstreamID != "" && row.UpstreamID != upstream {
			return ErrCNConflict
		}
		if row.State != "dispatching" && row.State != "unknown" && row.State != "accepted" {
			return ErrCNConflict
		}
		return tx.Model(row).Updates(map[string]any{"upstream_id": upstream, "state": "accepted", "next_attempt_at": time.Now().Unix() + 600}).Error
	})
}

func ResolveCNTaskSubmission(key string, rejected bool) error {
	return cnTaskTransaction(context.Background(), DB, key, func(tx *gorm.DB, _ *Task, row *CNTaskBilling) error {
		if row.State != "dispatching" {
			return ErrCNConflict
		}
		if rejected {
			row.State, row.Pending, row.Target, row.Terminal = "reserved", true, 0, true
			row.FinalError = "provider_rejected"
		} else {
			row.State, row.LastErrorCode = "unknown", "provider_acceptance_unknown"
		}
		row.NextAttemptAt = 0
		return tx.Save(row).Error
	})
}

func ReserveCNTask(key string, target int) error {
	if target < 0 || target > common.MaxQuota {
		return ErrCNConflict
	}
	return cnTaskTransaction(context.Background(), DB, key, func(tx *gorm.DB, _ *Task, row *CNTaskBilling) error {
		if (row.State != "reserved" && row.State != "accepted") || row.Pending {
			return ErrCNConflict
		}
		if target <= row.HeldQuota {
			return nil
		}
		if err := cnTaskFundingTx(tx, row, target-row.HeldQuota, true); err != nil {
			return err
		}
		row.HeldQuota = target
		return tx.Save(row).Error
	})
}

func cnTaskStageTx(tx *gorm.DB, task *Task, row *CNTaskBilling, target int, terminal bool, at time.Time) error {
	first := row.FactID == ""
	if target < 0 || target > common.MaxQuota || (row.State == "final" && row.Actual != target) {
		return ErrCNConflict
	}
	if err := cnTaskFundingTx(tx, row, target-row.HeldQuota, true); err != nil {
		return err
	}
	delta := target - row.Actual
	var account User
	if err := tx.First(&account, row.UserID).Error; err != nil {
		return err
	}
	used := int64(account.UsedQuota) + int64(delta)
	if used < 0 || used > int64(common.MaxWalletQuota) {
		return ErrWalletQuotaLimitExceeded
	}
	updates := map[string]any{"used_quota": used}
	if first && task != nil {
		updates["request_count"] = gorm.Expr("request_count + 1")
	}
	if err := tx.Model(&account).Updates(updates).Error; err != nil {
		return err
	}
	var channel Channel
	if err := lockForUpdate(tx).First(&channel, row.ChannelID).Error; err != nil {
		return err
	}
	channelUsed := int64(channel.UsedQuota) + int64(delta)
	if channelUsed < 0 || channelUsed > int64(common.MaxWalletQuota) {
		return ErrWalletQuotaLimitExceeded
	}
	if err := tx.Model(&channel).Update("used_quota", channelUsed).Error; err != nil {
		return err
	}
	if task != nil {
		if err := tx.Model(task).Update("quota", target).Error; err != nil {
			return err
		}
		task.Quota = target
	}
	evidence := CNEvidence{System: "gateway-cn", RecordType: "cn_task_financial_receipt", RecordID: row.Key, EvidenceID: CNIdentity("ordinary-task-transition", row.Key, strconv.FormatInt(row.Version, 10), strconv.Itoa(target), strconv.FormatBool(terminal))}
	fact := cnBaseFact("charge", "request:"+row.RequestID, "0", evidence.EvidenceID, row.Source, int64(row.UserID), at, evidence)
	fact.RequestID, fact.TaskID = &row.RequestID, &row.PublicTaskID
	fact.Status, fact.Quality = "settled", "settled_quota"
	if terminal && task != nil && task.Status == TaskStatusFailure {
		fact.Status = "reversed"
	}
	fact.EffectiveAt, fact.SourcePeriod = row.CreatedAt, row.CreatedAt[:7]
	if opsMoneyCurrency.MatchString(row.Currency) {
		fact.Conversion = map[string]any{"sourceUnit": "quota", "quotaPerUnit": row.QuotaPerUnit, "originalCurrency": "USD", "settlementCurrency": row.Currency, "exchangeRate": row.ExchangeRate, "fxVersion": row.ConversionVersion, "rounding": "HALF_EVEN_18"}
		fact.Cumulative.Money = cnQuotaMoney(int64(target), fact.Conversion)
		fact.SourceEvidence = append(fact.SourceEvidence, CNEvidence{System: "gateway-cn", RecordType: "gateway_operation_currency_snapshot", RecordID: row.ConversionVersion, EvidenceID: row.ConversionVersion})
	}
	if _, err := cnLockPublisher(tx); err != nil {
		return err
	}
	if err := cnStageChargeSnapshot(tx, &fact, int64(target)); err != nil {
		return err
	}
	var head CNFinanceOutbox
	if err := tx.Where("chain_key = ?", CNIdentity("charge", fact.EconomicID, "0")).Order("revision DESC").First(&head).Error; err != nil {
		return err
	}
	if first || delta != 0 {
		if err := cnStageTaskEffectTx(tx, task, row, delta, at); err != nil {
			return err
		}
	}
	row.HeldQuota, row.Actual, row.FactID = target, target, head.FactID
	row.Pending, row.FinalError, row.LastErrorCode = false, "", ""
	row.State, row.FinalizedAt = "active", at.Format(CNTimeLayout)
	if terminal {
		row.State = "final"
		if task == nil {
			row.State = "cancelled"
		}
	}
	row.Version++
	return tx.Save(row).Error
}

func cnInsertTask(ctx context.Context, task *Task) error {
	// Persist acceptance payload before the financial transaction so a failed
	// task insert can recover without repeating the provider submission.
	err := cnTaskTransaction(context.WithoutCancel(ctx), DB, task.CNBillingKey, func(tx *gorm.DB, _ *Task, row *CNTaskBilling) error {
		if task.TaskID != row.PublicTaskID || task.UserId != row.UserID || task.ChannelId != row.ChannelID || string(task.Platform) != row.Platform || task.PrivateData.UpstreamTaskID != row.UpstreamID || task.PrivateData.TokenId != row.TokenID || task.PrivateData.BillingSource != row.Source || task.PrivateData.SubscriptionId != row.SubscriptionID || task.PrivateData.Execution == nil || task.PrivateData.Execution.RequestID != row.RequestID {
			return ErrCNConflict
		}
		if row.State == "active" || row.State == "final" {
			return nil
		}
		if row.State != "accepted" || task.TaskID != row.PublicTaskID || task.UserId != row.UserID || task.ChannelId != row.ChannelID || task.PrivateData.UpstreamTaskID != row.UpstreamID || task.Quota < 0 || task.Quota > common.MaxQuota {
			return ErrCNConflict
		}
		copyTask := *task
		copyTask.PrivateData.Key = ""
		if copyTask.PrivateData.Endpoint == nil {
			var frozen TaskPrivateData
			if err := common.UnmarshalJsonStr(row.SeedPrivate, &frozen); err != nil {
				return err
			}
			copyTask.PrivateData.Endpoint = frozen.Endpoint
		}
		payload, err := common.Marshal(cnTaskAcceptance{Task: &copyTask, Private: copyTask.PrivateData, BillingReconciliationPending: task.BillingReconciliationPending, OpsUsagePending: task.OpsUsagePending})
		if err != nil {
			return err
		}
		if row.Pending && row.AcceptedTask != string(payload) {
			return ErrCNConflict
		}
		target := task.Quota
		if task.Status == TaskStatusFailure {
			target = 0
		}
		providerPending := task.PrivateData.BillingContext != nil && task.PrivateData.BillingContext.ProviderBilling != nil && task.PrivateData.BillingContext.ProviderBilling.AsyncReconciliationRequired
		return tx.Model(row).Updates(map[string]any{"accepted_task": string(payload), "submit_quota": task.Quota, "pending": true, "target": target, "terminal": task.Status == TaskStatusFailure || (task.Status == TaskStatusSuccess && !providerPending), "final_error": task.cnFinalError, "next_attempt_at": int64(0)}).Error
	})
	if err != nil {
		return err
	}
	if err = CompleteCNTaskIntent(context.WithoutCancel(ctx), DB, task.CNBillingKey); err != nil {
		return err
	}
	return DB.Where("cn_billing_key = ?", task.CNBillingKey).First(task).Error
}

func CompleteCNTaskIntent(ctx context.Context, db *gorm.DB, key string) error {
	err := cnTaskTransaction(ctx, db, key, func(tx *gorm.DB, task *Task, row *CNTaskBilling) error {
		if !row.Pending {
			return nil
		}
		if row.FinalError != "" && row.FinalError != "provider_rejected" {
			return ErrCNConflict
		}
		if row.State == "accepted" && task == nil {
			var payload cnTaskAcceptance
			if err := common.UnmarshalJsonStr(row.AcceptedTask, &payload); err != nil || payload.Task == nil {
				return ErrCNConflict
			}
			task = payload.Task
			task.PrivateData, task.CNBillingKey, task.Quota = payload.Private, key, 0
			task.BillingReconciliationPending, task.OpsUsagePending = payload.BillingReconciliationPending, payload.OpsUsagePending
			if bc := task.PrivateData.BillingContext; bc != nil && bc.ProviderBilling != nil && bc.ProviderBilling.AsyncReconciliationRequired && task.Status != TaskStatusFailure {
				task.BillingReconciliationPending = true
			}
			if task.ID != 0 || task.TaskID != row.PublicTaskID || task.UserId != row.UserID || task.ChannelId != row.ChannelID || string(task.Platform) != row.Platform || task.PrivateData.UpstreamTaskID != row.UpstreamID || task.PrivateData.Execution == nil || task.PrivateData.Execution.RequestID != row.RequestID || task.PrivateData.TokenId != row.TokenID || task.PrivateData.BillingSource != row.Source || task.PrivateData.SubscriptionId != row.SubscriptionID {
				return ErrCNConflict
			}
			if err := tx.Create(task).Error; err != nil {
				return err
			}
			row.TaskID = task.ID
		} else if task == nil && row.State != "reserved" {
			return ErrCNConflict
		}
		if err := cnCompleteTaskProviderBillTx(tx, task, row); err != nil {
			return err
		}
		return cnTaskStageTx(tx, task, row, row.Target, row.Terminal, time.Now().UTC())
	})
	if err != nil {
		failure := db.WithContext(context.WithoutCancel(ctx)).Model(&CNTaskBilling{}).Where(map[string]any{"key": key}).Where("pending = ?", true).Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_attempt_at": time.Now().Unix() + 5, "last_error_code": "task_financial_transaction_failed"}).Error
		return errors.Join(err, failure)
	}
	return nil
}

func RequestCNTaskCancellation(key string) error {
	return requestCNTaskCancellation(DB, key)
}

func requestCNTaskCancellation(db *gorm.DB, key string) error {
	return cnTaskTransaction(context.Background(), db, key, func(tx *gorm.DB, _ *Task, row *CNTaskBilling) error {
		if row.State == "cancelled" {
			return nil
		}
		if row.State != "reserved" {
			return ErrCNConflict
		}
		return tx.Model(row).Updates(map[string]any{"pending": true, "target": 0, "terminal": true, "next_attempt_at": int64(0)}).Error
	})
}

func RequestCNTaskAdjustment(task *Task, target int, terminal bool) error {
	if task == nil || task.CNBillingKey == "" || target < 0 || target > common.MaxQuota {
		return ErrCNConflict
	}
	return cnTaskTransaction(context.Background(), DB, task.CNBillingKey, func(tx *gorm.DB, current *Task, row *CNTaskBilling) error {
		if current == nil || current.ID != task.ID || current.Quota != task.Quota || (row.State != "active" && row.State != "final") || (row.State == "final" && target != row.Actual) || (current.Status == TaskStatusFailure && target != 0) {
			return ErrCNConflict
		}
		if row.State == "final" && target == row.Actual {
			return nil
		}
		if bc := current.PrivateData.BillingContext; current.Status != TaskStatusFailure && bc != nil && bc.ProviderBilling != nil && bc.ProviderBilling.AsyncReconciliationRequired {
			return ErrCNConflict
		}
		if row.Pending && (row.Target != target || row.Terminal != terminal) {
			return ErrCNConflict
		}
		return tx.Model(row).Updates(map[string]any{"pending": true, "target": target, "terminal": terminal, "final_error": "", "next_attempt_at": int64(0)}).Error
	})
}

func cnUpdateTask(task *Task, fromStatus *TaskStatus) (bool, error) {
	won := false
	err := cnTaskTransaction(context.Background(), DB, task.CNBillingKey, func(tx *gorm.DB, current *Task, row *CNTaskBilling) error {
		if current == nil || task.ID != current.ID || task.UserId != row.UserID || task.TaskID != row.PublicTaskID || task.ChannelId != row.ChannelID {
			return ErrCNConflict
		}
		if fromStatus != nil && current.Status != *fromStatus {
			return nil
		}
		if task.cnFinalTarget != nil && task.Quota != current.Quota {
			return ErrCNConflict
		}
		if (current.Status == TaskStatusSuccess || current.Status == TaskStatusFailure) && current.Status != task.Status {
			return ErrCNConflict
		}
		private := current.PrivateData
		private.PluginState, private.ResultURL, private.PollFailures, private.OpsUsage = task.PrivateData.PluginState, task.PrivateData.ResultURL, task.PrivateData.PollFailures, task.PrivateData.OpsUsage
		updates := map[string]any{"status": task.Status, "progress": task.Progress, "start_time": task.StartTime, "finish_time": task.FinishTime, "fail_reason": task.FailReason, "data": task.Data, "private_data": private, "ops_usage_pending": task.OpsUsagePending}
		if err := tx.Model(current).Updates(updates).Error; err != nil {
			return err
		}
		won = true
		if row.State == "active" && (task.Status == TaskStatusFailure || task.Status == TaskStatusSuccess) {
			if bc := current.PrivateData.BillingContext; task.Status == TaskStatusSuccess && bc != nil && bc.ProviderBilling != nil && bc.ProviderBilling.AsyncReconciliationRequired {
				// The separate durable reconciliation record/enqueue intent owns
				// this target. Provider success alone supplies no final bill.
				return nil
			}
			target, failure := row.Actual, task.cnFinalError
			if task.Status == TaskStatusFailure {
				target, failure = 0, ""
			} else if task.cnFinalTarget != nil {
				target = *task.cnFinalTarget
			} else {
				failure = "task_completion_target_missing"
			}
			if target < 0 || target > common.MaxQuota {
				return ErrCNConflict
			}
			return tx.Model(row).Updates(map[string]any{"pending": true, "target": target, "terminal": true, "final_error": failure, "next_attempt_at": int64(0)}).Error
		}
		return nil
	})
	return won, err
}

func RecoverCNTaskBilling(ctx context.Context, db *gorm.DB, limit int) error {
	var rows []CNTaskBilling
	if err := db.WithContext(ctx).Where("(pending = ? OR state IN ?) AND next_attempt_at <= ?", true, []string{"reserved", "dispatching"}, time.Now().Unix()).Order("next_attempt_at, created_at").Limit(min(max(limit, 1), 1000)).Find(&rows).Error; err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if row.State == "dispatching" {
			failures = append(failures, db.WithContext(ctx).Model(&CNTaskBilling{}).Where(map[string]any{"key": row.Key}).Where("state = ?", "dispatching").Updates(map[string]any{"state": "unknown", "last_error_code": "provider_acceptance_unknown"}).Error)
			continue
		}
		if row.State == "reserved" && !row.Pending {
			if err := requestCNTaskCancellation(db.WithContext(ctx), row.Key); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		failures = append(failures, CompleteCNTaskIntent(ctx, db, row.Key))
	}
	return errors.Join(failures...)
}

func (task *Task) PrepareCNFinancialTerminal(target *int, code string) {
	task.cnFinalTarget, task.cnFinalError = target, code
}

func cnGuardLegacyTask(task *Task) error {
	if !DB.Migrator().HasTable(&CNTaskBilling{}) {
		return nil
	}
	var count int64
	query := DB.Model(&CNTaskBilling{})
	if task.ID > 0 {
		query = query.Where("task_id = ? OR public_task_id = ?", task.ID, task.TaskID)
	} else {
		query = query.Where("public_task_id = ?", task.TaskID)
	}
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrCNConflict
	}
	return nil
}

func HasDueCNTaskSubmissions() bool {
	if !DB.Migrator().HasTable(&CNTaskBilling{}) {
		return false
	}
	var count int64
	return DB.Model(&CNTaskBilling{}).Where("task_id = 0 AND pending = ? AND state IN ? AND upstream_id <> ? AND next_attempt_at <= ?", false, []string{"accepted", "unknown"}, "", time.Now().Unix()).Limit(1).Count(&count).Error == nil && count > 0
}

func ClaimCNTaskSubmissionProbes(ctx context.Context, limit int) ([]string, error) {
	var candidates []CNTaskBilling
	if err := DB.WithContext(ctx).Where("task_id = 0 AND pending = ? AND state IN ? AND upstream_id <> ? AND next_attempt_at <= ?", false, []string{"accepted", "unknown"}, "", time.Now().Unix()).Order("next_attempt_at, created_at").Limit(min(max(limit, 1), 100)).Find(&candidates).Error; err != nil {
		return nil, err
	}
	var keys []string
	for _, candidate := range candidates {
		if err := cnTaskTransaction(ctx, DB, candidate.Key, func(tx *gorm.DB, _ *Task, row *CNTaskBilling) error {
			if row.TaskID != 0 || row.Pending || (row.State != "accepted" && row.State != "unknown") || row.UpstreamID == "" || row.NextAttemptAt > time.Now().Unix() {
				return nil
			}
			if err := tx.Model(row).Updates(map[string]any{"next_attempt_at": time.Now().Unix() + 300, "attempts": gorm.Expr("attempts + 1")}).Error; err != nil {
				return err
			}
			keys = append(keys, row.Key)
			return nil
		}); err != nil {
			return keys, err
		}
	}
	return keys, nil
}

func MarkCNTaskRepair(ctx context.Context, key, code string) error {
	return DB.WithContext(ctx).Model(&CNTaskBilling{}).Where(map[string]any{"key": key}).Where("state NOT IN ?", []string{"final", "cancelled"}).Updates(map[string]any{"last_error_code": code, "next_attempt_at": time.Now().Unix() + 300}).Error
}

func cnTaskProofs(tx *gorm.DB, stream *CNFinanceStream, published map[string]bool) ([]cnProofScan, error) {
	var rows []CNTaskBilling
	if err := tx.Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Find(&rows).Error; err != nil {
		return nil, err
	}
	byProvider := map[string]*cnProofScan{}
	for _, provider := range CNTaskSupportedPlatforms() {
		scan := &cnProofScan{dependency: CNDependency{SourceID: "ordinary_task_" + provider, Version: "task-single-settler-v1", Required: true, CoverageFrom: stream.StartedAt, SupportedPaths: []string{"ordinary-task/single-settler-v1"}, ExcludedPaths: []string{"pre-epoch", "agency", "legacy-tasks", "audio", "redis", "batch"}}}
		if !CNDurableTaskAvailable() {
			scan.coverageGap = true
			scan.dependency.ExcludedPaths = append(scan.dependency.ExcludedPaths, "durable_task_lane_unavailable")
		}
		byProvider[provider] = scan
	}
	for _, row := range rows {
		scan := byProvider[CNTaskProvider(row.Platform)]
		if scan == nil {
			return nil, ErrCNConflict
		}
		scan.records = append(scan.records, row)
		if row.Pending || (row.State != "final" && row.State != "cancelled") || !published[row.FactID] {
			scan.dependency.PendingCount++
		}
		if row.State == "unknown" || row.LastErrorCode != "" || row.FinalError != "" || cnQuotaMoney(0, map[string]any{"quotaPerUnit": row.QuotaPerUnit, "exchangeRate": row.ExchangeRate, "settlementCurrency": row.Currency, "originalCurrency": "USD"}) == nil {
			scan.dependency.UnknownCount++
		}
		var effects int64
		if err := tx.Model(&CNTaskEffect{}).Where("billing_key = ? AND state = ?", row.Key, "pending").Count(&effects).Error; err != nil {
			return nil, err
		}
		scan.dependency.PendingCount += effects
	}
	var scans []cnProofScan
	for _, provider := range CNTaskSupportedPlatforms() {
		scans = append(scans, *byProvider[provider])
	}
	return scans, nil
}

func CNTaskSupportedPlatforms() []string {
	return []string{"seedance_domestic", "openai_seedance", "mobilecloud_seedance", "happyhorse", "alibaba", "doubao", "hailuo", "google", "vertex-ai", "kling", "sora", "vidu", "jimeng", "newapi-video", "wangpaiai"}
}

func CNTaskProvider(platform string) string {
	for _, name := range CNTaskSupportedPlatforms() {
		if platform == name {
			return name
		}
	}
	providers := map[int]string{constant.ChannelTypeSeedanceDomestic: "seedance_domestic", constant.ChannelTypeOpenAISeedance: "openai_seedance", constant.ChannelTypeMobileCloudSeedance: "mobilecloud_seedance", constant.ChannelTypeHappyHorse: "happyhorse", constant.ChannelTypeAli: "alibaba", constant.ChannelTypeDoubaoVideo: "doubao", constant.ChannelTypeVolcEngine: "doubao", constant.ChannelTypeMiniMax: "hailuo", constant.ChannelTypeGemini: "google", constant.ChannelTypeVertexAi: "vertex-ai", constant.ChannelTypeKling: "kling", constant.ChannelTypeSora: "sora", constant.ChannelTypeOpenAI: "sora", constant.ChannelTypeVidu: "vidu", constant.ChannelTypeJimeng: "jimeng"}
	id, err := strconv.Atoi(platform)
	if err != nil {
		return ""
	}
	return providers[id]
}

func ValidateCNTaskCredential(ctx context.Context, key string, channel int, credential string) error {
	var row CNTaskBilling
	if err := DB.WithContext(ctx).Select("channel_id", "credential_digest").Where(map[string]any{"key": key}).First(&row).Error; err != nil {
		return err
	}
	if row.ChannelID != channel || row.CredentialDigest != CNIdentity("ordinary-task-credential", credential) {
		return errors.New("task_provider_credential_changed_requires_repair")
	}
	return nil
}

func cnRejectTaskRequestTx(tx *gorm.DB, request string) error {
	if !tx.Migrator().HasTable(&CNTaskBilling{}) {
		return nil
	}
	var count int64
	if err := tx.Model(&CNTaskBilling{}).Where("request_id = ?", request).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrCNConflict
	}
	return nil
}
