package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

const (
	AgencyDurableBillingMode           = "agency-durable-v1"
	agencyFundingRuleLegacyUnversioned = "legacy_unversioned_bonus_first"
	// AgencyProvisioningBillingMode is a temporary fail-closed mode used while
	// Root provisions an existing legacy user.  It prevents quota mutations
	// from entering the legacy path while the barrier drains in-flight work.
	AgencyProvisioningBillingMode = "agency-provisioning-v1"
)

var ErrAgencyFundingUnavailable = errors.New("agency funding account unavailable")
var ErrAgencyProvisioning = errors.New("agency user provisioning in progress")
var ErrInsufficientAgencyWalletQuota = errors.New("agency wallet quota insufficient")
var ErrInsufficientAgencyTokenQuota = errors.New("agency token quota insufficient")
var ErrAgencyInsufficientTokenUsage = errors.New("agency token usage underflow")
var ErrAgencyFundingReversalConflict = errors.New("agency funding reversal conflicts with an existing refund")
var ErrAgencyTopupConflict = errors.New("agency topup conflicts with an existing source operation")
var ErrAgencyAdminGrantUnavailable = errors.New("agency administrator grant is unavailable for reversal")

type AgencyFundingReversalInput struct {
	RefundID          string
	SourceOperationID string
	UserID            int64
	Quota             int64
	CurrencyCode      string
	PaymentReference  string
	EvidenceRef       string
	Reason            string
}

type AgencyFundingReversalCharge struct {
	ChargeID string
	Quota    int64
}

// AdjustAgencyChargeTx applies a delta to a wallet-backed charge and keeps the
// durable funding projection in sync. A positive delta is an additional
// charge (and may drive the wallet into debt, matching legacy settlement
// semantics); a negative delta releases the corresponding reservation.
// targetQuota is the cumulative charge amount after applying a positive
// delta, and is ignored for releases.
func AdjustAgencyChargeTx(tx *gorm.DB, userID, delta int, chargeID string, targetQuota int64) error {
	return adjustAgencyChargeTxWithRule(tx, userID, delta, chargeID, targetQuota, agencyFundingRuleLegacyUnversioned)
}

func adjustAgencyChargeTxWithRule(tx *gorm.DB, userID, delta int, chargeID string, targetQuota int64, fundingRuleVersion string) error {
	if tx == nil || userID <= 0 || delta == 0 || strings.TrimSpace(chargeID) == "" {
		return ErrAgencyFundingUnavailable
	}
	var user User
	if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
		return err
	}
	if user.BillingMode == AgencyProvisioningBillingMode {
		return ErrAgencyProvisioning
	}
	if delta > 0 && targetQuota < 0 {
		return ErrAgencyFundingUnavailable
	}
	// Keep the int32 wallet column bounded even when a provider reports a
	// malformed settlement amount.
	delta64 := int64(delta)
	var targetWallet int64
	if delta64 > 0 {
		if delta64 > int64(user.Quota)+int64(common.MaxQuota)+1 {
			return errors.New("agency user quota arithmetic overflow")
		}
		targetWallet = int64(user.Quota) - delta64
	} else {
		if delta64 == -int64(^uint64(0)>>1)-1 {
			return errors.New("agency user quota arithmetic overflow")
		}
		increase := -delta64
		if increase > int64(common.MaxQuota)-int64(user.Quota) {
			return errors.New("agency user quota arithmetic overflow")
		}
		targetWallet = int64(user.Quota) + increase
	}
	if targetWallet > int64(common.MaxQuota) || targetWallet < -int64(common.MaxQuota)-1 {
		return errors.New("agency user quota arithmetic overflow")
	}
	if user.BillingMode != AgencyDurableBillingMode {
		return tx.Model(&User{}).Where("id = ?", userID).Update("quota", targetWallet).Error
	}
	if delta > 0 {
		allocated, err := agencyFundingAllocatedTx(tx, int64(userID), chargeID)
		if err != nil {
			return err
		}
		if targetQuota <= allocated {
			return nil
		}
		if err := tx.Model(&User{}).Where("id = ?", userID).Update("quota", targetWallet).Error; err != nil {
			return err
		}
		_, err = reserveAgencyFundingTxWithRule(tx, int64(userID), chargeID, targetQuota, fundingRuleVersion)
		return err
	}
	_, err := ReleaseUserQuotaAndAgencyTx(tx, userID, -delta, chargeID)
	return err
}

// AdjustAgencyCharge is the transaction wrapper used by asynchronous task
// billing/refund paths.
func AdjustAgencyCharge(userID, delta int, chargeID string, targetQuota int64) error {
	err := DB.Transaction(func(tx *gorm.DB) error {
		return AdjustAgencyChargeTx(tx, userID, delta, chargeID, targetQuota)
	})
	if err == nil && common.RedisEnabled {
		if cacheErr := InvalidateUserCache(userID); cacheErr != nil {
			common.SysLog("failed to invalidate agency wallet cache: " + cacheErr.Error())
		}
	}
	return err
}

// ApplyAgencyQuotaDelta applies a non-charge wallet adjustment and its typed
// funding projection in one transaction. Positive deltas are non-paid grants;
// negative administrative adjustments consume paid first, then non-paid lots,
// and finally create debt. Legacy users retain the original quota-only path.
func ApplyAgencyQuotaDelta(userID, delta int64, sourceKind string) error {
	return ApplyAgencyQuotaDeltaWithActor(userID, delta, sourceKind, 0)
}

// ApplyAgencyQuotaDeltaWithActor is the audited form used by Root quota
// adjustments. actorID is kept separate from the beneficiary userID so a
// report can answer both "who received" and "who operated".
func ApplyAgencyQuotaDeltaWithActor(userID, delta int64, sourceKind string, actorID int64) error {
	if userID <= 0 || strings.TrimSpace(sourceKind) == "" {
		return ErrAgencyFundingUnavailable
	}
	if delta == 0 {
		return nil
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		return ApplyAgencyQuotaDeltaWithActorTx(tx, userID, delta, sourceKind, actorID)
	})
	if err == nil && common.RedisEnabled {
		if isAgencyDurableUser(int(userID)) {
			if cacheErr := InvalidateUserCache(int(userID)); cacheErr != nil {
				common.SysLog("failed to invalidate agency wallet cache: " + cacheErr.Error())
			}
		} else if cacheErr := cacheIncrUserQuota(int(userID), delta); cacheErr != nil {
			common.SysLog("failed to sync agency wallet cache: " + cacheErr.Error())
		}
	}
	return err
}

// ApplyAgencyQuotaDeltaTx is the transaction-scoped form used by atomic
// business operations such as check-in and redemption.
func ApplyAgencyQuotaDeltaTx(tx *gorm.DB, userID, delta int64, sourceKind string) error {
	return ApplyAgencyQuotaDeltaWithActorTx(tx, userID, delta, sourceKind, 0)
}

func ApplyAgencyQuotaDeltaWithActorTx(tx *gorm.DB, userID, delta int64, sourceKind string, actorID int64) error {
	return applyAgencyQuotaDeltaTx(tx, userID, delta, sourceKind, "", actorID, 0)
}

// ApplyAgencyQuotaDeltaWithSourceTx preserves the business identifier of a
// redemption or administrative grant in the durable funding records.
func ApplyAgencyQuotaDeltaWithSourceTx(tx *gorm.DB, userID, delta int64, sourceKind, sourceID string) error {
	return applyAgencyQuotaDeltaTx(tx, userID, delta, sourceKind, sourceID, 0, 0)
}

func ApplyAgencyQuotaDeltaWithSourceAndActorTx(tx *gorm.DB, userID, delta int64, sourceKind, sourceID string, actorID int64) error {
	return applyAgencyQuotaDeltaTx(tx, userID, delta, sourceKind, sourceID, actorID, 0)
}

// ApplyAgencyRedemptionQuotaTx freezes the redeemed code's deadline onto its
// funding lot. The deadline continues to govern any unused credited balance.
func ApplyAgencyRedemptionQuotaTx(tx *gorm.DB, userID, delta int64, sourceID string, actorID, expiresAt int64) error {
	if expiresAt < 0 {
		return ErrAgencyFundingUnavailable
	}
	return applyAgencyQuotaDeltaTx(tx, userID, delta, "redemption", sourceID, actorID, expiresAt)
}

func applyAgencyQuotaDeltaTx(tx *gorm.DB, userID, delta int64, sourceKind, sourceID string, actorID, expiresAt int64) error {
	if tx == nil || userID <= 0 || strings.TrimSpace(sourceKind) == "" {
		return ErrAgencyFundingUnavailable
	}
	if !isRedemptionSource(sourceKind) {
		expiresAt = 0
	}
	if delta == 0 {
		return nil
	}
	var user User
	if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
		return err
	}
	if user.BillingMode == AgencyProvisioningBillingMode {
		return ErrAgencyProvisioning
	}
	if user.BillingMode != AgencyDurableBillingMode {
		result := tx.Model(&User{}).Where("id = ?", userID).Update("quota", gorm.Expr("quota + ?", delta))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}
	maxInt64 := int64(^uint64(0) >> 1)
	minInt64 := -maxInt64 - 1
	if delta == minInt64 {
		return errors.New("agency user quota arithmetic overflow")
	}
	// For negative deltas use minInt64 + quota; subtracting a positive quota
	// from MinInt64 would itself overflow before the comparison.
	if (delta > 0 && delta > maxInt64-int64(user.Quota)) || (delta < 0 && delta < minInt64+int64(user.Quota)) {
		return errors.New("agency user quota arithmetic overflow")
	}
	targetQuota := int64(user.Quota) + delta
	if targetQuota > int64(common.MaxQuota) {
		return errors.New("agency user quota overflow")
	}
	if targetQuota < -int64(common.MaxQuota)-1 {
		return errors.New("agency user quota underflow")
	}
	if err := EnsureAgencyFundingAccount(tx, userID); err != nil {
		return err
	}
	var account AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
		return err
	}
	paidDelta, nonpaidDelta, debtDelta := int64(0), int64(0), int64(0)
	if delta > 0 {
		next := account.NonpaidAvailable + delta
		if next < account.NonpaidAvailable || next < 0 {
			return errors.New("agency funding balance overflow")
		}
		nonpaidDelta = delta
		account.NonpaidAvailable = next
	} else {
		remaining := -delta
		if isAdministrativeQuotaAdjustment(sourceKind) {
			// An administrator decrease is a revocation of an unused
			// administrator grant. It must never confiscate verified paid
			// funding or manufacture debt when the grant is exhausted.
			var lots []AgencyFundingLot
			if err := AgencyLockForUpdate(tx).Where("user_id = ? AND bonus_available > 0", userID).Order("money_seq ASC, id ASC").Find(&lots).Error; err != nil {
				return err
			}
			for i := range lots {
				if remaining == 0 {
					break
				}
				if !isAdministrativeGrantSource(lots[i].SourceKind) {
					continue
				}
				take := lots[i].BonusAvailable
				if take > remaining {
					take = remaining
				}
				lots[i].BonusAvailable -= take
				lots[i].BonusRevoked += take
				lots[i].Version++
				if err := tx.Model(&lots[i]).Updates(map[string]any{"bonus_available": lots[i].BonusAvailable, "bonus_revoked": lots[i].BonusRevoked, "version": lots[i].Version}).Error; err != nil {
					return err
				}
				if account.NonpaidAvailable < take {
					return errors.New("agency funding nonpaid balance underflow")
				}
				account.NonpaidAvailable -= take
				remaining -= take
				nonpaidDelta -= take
			}
			if remaining > 0 {
				return ErrAgencyAdminGrantUnavailable
			}
		} else {
			var lots []AgencyFundingLot
			if err := AgencyLockForUpdate(tx).Where("user_id = ? AND paid_available > 0", userID).Order("money_seq ASC, id ASC").Find(&lots).Error; err != nil {
				return err
			}
			for i := range lots {
				if remaining == 0 {
					break
				}
				take := lots[i].PaidAvailable
				if take > remaining {
					take = remaining
				}
				lots[i].PaidAvailable -= take
				lots[i].Version++
				if err := tx.Model(&lots[i]).Updates(map[string]any{"paid_available": lots[i].PaidAvailable, "version": lots[i].Version}).Error; err != nil {
					return err
				}
				if account.PaidAvailable < take {
					return errors.New("agency funding paid balance underflow")
				}
				account.PaidAvailable -= take
				remaining -= take
				paidDelta -= take
			}
			nonpaidTake := remaining
			if nonpaidTake > account.NonpaidAvailable {
				nonpaidTake = account.NonpaidAvailable
			}
			account.NonpaidAvailable -= nonpaidTake
			remaining -= nonpaidTake
			nonpaidDelta = -nonpaidTake
			if remaining > 0 {
				if account.DebtQuota > int64(^uint64(0)>>1)-remaining {
					return errors.New("agency funding debt overflow")
				}
				account.DebtQuota += remaining
				debtDelta = remaining
			}
		}
	}
	if err := tx.Model(&User{}).Where("id = ?", userID).Update("quota", gorm.Expr("quota + ?", delta)).Error; err != nil {
		return err
	}
	seq := account.MoneySeq + 1
	if seq <= account.MoneySeq {
		return errors.New("agency funding sequence overflow")
	}
	if err := tx.Model(&account).Updates(map[string]any{"paid_available": account.PaidAvailable, "nonpaid_available": account.NonpaidAvailable, "debt_quota": account.DebtQuota, "money_seq": seq, "version": account.Version + 1, "updated_at": time.Now().Unix()}).Error; err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	operationID := fmt.Sprintf("%s-%d-%d", sourceKind, userID, seq)
	var lotID *int64
	if delta > 0 {
		if strings.TrimSpace(sourceID) == "" {
			sourceID = operationID
		}
		snapshot, err := common.Marshal(map[string]any{"source_kind": sourceKind, "source_id": sourceID, "actor_user_id": actorID, "expires_at": expiresAt})
		if err != nil {
			return err
		}
		lot := &AgencyFundingLot{UserID: userID, SourceKind: sourceKind, SourceID: sourceID, ActorUserID: actorID, SourceSnapshotJSON: string(snapshot), CompletionSource: "quota_adjustment", BonusInitial: delta, BonusAvailable: delta, ExpiresAt: expiresAt, MoneySeq: seq, Version: 1, CreatedAt: time.Now().Unix()}
		if err := tx.Create(lot).Error; err != nil {
			return err
		}
		lotID = &lot.ID
	}
	if err := tx.Create(&AgencyFundingLedger{OperationID: operationID, EntryNo: 0, UserID: userID, MoneySeq: seq, SourceKind: sourceKind, LotID: lotID, PaidDelta: paidDelta, NonpaidDelta: nonpaidDelta, DebtDelta: debtDelta, PaidAfter: account.PaidAvailable, NonpaidAfter: account.NonpaidAvailable, DebtAfter: account.DebtQuota, CreatedAtMS: now}).Error; err != nil {
		return err
	}
	var active AgencyActiveUserBinding
	var agencyID, bindingID *int64
	if err := tx.Where("user_id = ?", userID).First(&active).Error; err == nil {
		agencyID = &active.AgencyID
		bindingID = &active.BindingID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: "agency-adjust-" + operationID, EventType: "agency.funding_adjusted", FinancialChargeID: operationID, OperationID: operationID, JournalRevision: 1, MoneySeq: seq, EventIndex: 0, EventCount: 1, OccurredAtMS: now, UserID: userID, AgencyID: agencyID, BindingID: bindingID, BusinessStatus: sourceKind, BillingStatus: "funding_adjusted", CommissionEligible: false, CommissionSkipReason: "noncommissionable_funding_adjustment"}
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	if err := tx.Create(&AgencyBillingOutbox{EventID: event.EventID, OperationID: operationID, EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: userID, MoneySeq: seq, Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion, CreatedAtMS: now}).Error; err != nil {
		return err
	}
	if delta > 0 {
		if err := tx.Create(&AgencyTopupFact{SourceOperationID: operationID, SourceID: sourceID, UserID: userID, InitiatedByUserID: actorID, AgencyID: agencyID, BindingID: bindingID, CreditedQuota: delta, BonusQuota: delta, FundingSource: sourceKind, CompletionSource: sourceKind, PaymentStatus: "success", ExpiresAt: expiresAt, OccurredAtMS: now}).Error; err != nil {
			return err
		}
	}
	return tx.Create(&AgencyEventDelivery{EventID: event.EventID, Status: "pending", NextRetryAt: time.Now().Unix(), CreatedAt: time.Now().Unix()}).Error
}

func isAdministrativeQuotaAdjustment(sourceKind string) bool {
	switch strings.ToLower(strings.TrimSpace(sourceKind)) {
	case "admin", "admin_debit", "admin_adjustment", "admin_override", "quota_debit":
		return true
	default:
		return false
	}
}

func isAdministrativeGrantSource(sourceKind string) bool {
	switch strings.ToLower(strings.TrimSpace(sourceKind)) {
	case "admin", "admin_grant", "admin_adjustment", "admin_override", "quota_grant", "quota_debit":
		return true
	default:
		return false
	}
}

// SetAgencyQuotaAbsolute implements an administrative absolute-balance edit
// without bypassing durable funding accounting.
func SetAgencyQuotaAbsolute(userID int64, target int, sourceKind string) error {
	return SetAgencyQuotaAbsoluteWithActor(userID, target, sourceKind, 0)
}

func SetAgencyQuotaAbsoluteWithActor(userID int64, target int, sourceKind string, actorID int64) error {
	if userID <= 0 || strings.TrimSpace(sourceKind) == "" || target > common.MaxQuota {
		return ErrAgencyFundingUnavailable
	}
	var delta int64
	err := runAgencyFundingTransaction(func(tx *gorm.DB) error {
		var user User
		if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		delta = int64(target) - int64(user.Quota)
		if delta == 0 {
			return nil
		}
		return ApplyAgencyQuotaDeltaWithActorTx(tx, userID, delta, sourceKind, actorID)
	})
	if err == nil && delta != 0 && common.RedisEnabled {
		if isAgencyDurableUser(int(userID)) {
			if cacheErr := InvalidateUserCache(int(userID)); cacheErr != nil {
				common.SysLog("failed to invalidate agency wallet cache: " + cacheErr.Error())
			}
		} else if cacheErr := cacheIncrUserQuota(int(userID), delta); cacheErr != nil {
			common.SysLog("failed to sync agency wallet cache: " + cacheErr.Error())
		}
	}
	return err
}

// TryReserveUserQuotaAndAgency atomically deducts amount from a customer's
// wallet and mirrors fundingTarget as the cumulative reservation for chargeID.
// It intentionally bypasses the Redis reservation script so the core user row
// and funding projection share one database transaction.
func TryReserveUserQuotaAndAgency(userID int, amount int, chargeID string, fundingTarget int64) (int64, error) {
	if userID <= 0 || amount < 0 || fundingTarget < 0 || strings.TrimSpace(chargeID) == "" {
		return 0, ErrAgencyFundingUnavailable
	}
	var paid int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		paid, err = TryReserveUserQuotaAndAgencyTx(tx, userID, amount, chargeID, fundingTarget)
		return err
	})
	if err == nil && amount > 0 && common.RedisEnabled {
		if isAgencyDurableUser(userID) {
			// Durable reservations may be idempotent retries, so the requested
			// amount is not necessarily the amount persisted by this call.
			// Invalidate instead of blindly decrementing the cached snapshot.
			if cacheErr := InvalidateUserCache(userID); cacheErr != nil {
				common.SysLog("failed to invalidate agency wallet cache: " + cacheErr.Error())
			}
		} else if cacheErr := cacheIncrUserQuota(userID, -int64(amount)); cacheErr != nil {
			common.SysLog("failed to sync agency wallet cache: " + cacheErr.Error())
		}
	}
	return paid, err
}

// TryReserveUserQuotaAndAgencyTx is the transaction-scoped reservation used
// by asynchronous task reconciliation and other multi-row billing commits.
func TryReserveUserQuotaAndAgencyTx(tx *gorm.DB, userID, amount int, chargeID string, fundingTarget int64) (int64, error) {
	if tx == nil || userID <= 0 || amount < 0 || fundingTarget < 0 || strings.TrimSpace(chargeID) == "" {
		return 0, ErrAgencyFundingUnavailable
	}
	var user User
	if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
		return 0, err
	}
	if user.BillingMode == AgencyProvisioningBillingMode {
		return 0, ErrAgencyProvisioning
	}
	if user.BillingMode != AgencyDurableBillingMode {
		result := tx.Model(&User{}).Where("id = ? AND quota >= ?", userID, amount).Update("quota", gorm.Expr("quota - ?", amount))
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected != 1 {
			return 0, ErrInsufficientAgencyWalletQuota
		}
		return 0, nil
	}
	if err := EnsureAgencyFundingAccount(tx, int64(userID)); err != nil {
		return 0, err
	}
	expired, err := expireAgencyRedemptionLotsTx(tx, int64(userID), common.GetTimestamp())
	if err != nil {
		return 0, err
	}
	if expired > 0 {
		user.Quota -= int(expired)
	}
	// A retry may carry the same wallet delta as the original request while
	// repeating an already satisfied cumulative funding target. Check the
	// existing allocation before touching the user wallet so the retry is
	// fully idempotent.
	allocated, err := agencyFundingAllocatedTx(tx, int64(userID), chargeID)
	if err != nil {
		return 0, err
	}
	if fundingTarget <= allocated {
		return 0, nil
	}
	required := fundingTarget - allocated
	// The wallet delta and cumulative target must describe the same newly
	// reserved amount. Accepting a smaller delta would allocate more funding
	// than was deducted from users.quota; accepting a larger one would make a
	// retry over-deduct the wallet.
	if required != int64(amount) {
		return 0, errors.New("agency funding reservation increment mismatch")
	}
	if user.Quota < amount {
		return 0, ErrInsufficientAgencyWalletQuota
	}
	if result := tx.Model(&User{}).Where("id = ? AND quota >= ?", userID, amount).Update("quota", gorm.Expr("quota - ?", amount)); result.Error != nil {
		return 0, result.Error
	} else if result.RowsAffected != 1 {
		return 0, ErrInsufficientAgencyWalletQuota
	}
	return reserveAgencyFundingTx(tx, int64(userID), chargeID, fundingTarget)
}

// TryReserveUserQuotaAndAgencyWithToken atomically reserves a legacy
// agency-managed user's wallet and the API token used for the request. Legacy
// agency users do not have a durable funding projection, but they still must
// not be exposed to a partial wallet/token reservation when concurrent token
// usage exhausts the token between the two old helper calls.
//
// Durable agency users must use TryReserveAgencyWalletAndToken instead; this
// helper intentionally fails closed for that billing mode.
func TryReserveUserQuotaAndAgencyWithToken(userID, tokenID, amount int, tokenKey, chargeID string, fundingTarget int64, unlimited bool) error {
	if userID <= 0 || tokenID <= 0 || amount < 0 || fundingTarget < 0 || strings.TrimSpace(chargeID) == "" {
		return ErrAgencyFundingUnavailable
	}
	err := runAgencyFundingTransaction(func(tx *gorm.DB) error {
		var user User
		if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		if user.BillingMode == AgencyProvisioningBillingMode {
			return ErrAgencyProvisioning
		}
		if user.BillingMode == AgencyDurableBillingMode {
			return ErrAgencyFundingUnavailable
		}
		if amount == 0 {
			return nil
		}

		query := AgencyLockForUpdate(tx).Where("id = ?", tokenID)
		if strings.TrimSpace(tokenKey) != "" {
			query = query.Where(map[string]any{"key": tokenKey})
		}
		var token Token
		if err := query.First(&token).Error; err != nil {
			return err
		}
		if !unlimited && token.RemainQuota < amount {
			return ErrInsufficientAgencyTokenQuota
		}
		if result := tx.Model(&Token{}).Where("id = ?", tokenID).Updates(map[string]any{
			"remain_quota":  gorm.Expr("remain_quota - ?", amount),
			"used_quota":    gorm.Expr("used_quota + ?", amount),
			"accessed_time": common.GetTimestamp(),
		}); result.Error != nil {
			return result.Error
		} else if result.RowsAffected != 1 {
			return ErrInsufficientAgencyTokenQuota
		}
		if result := tx.Model(&User{}).Where("id = ? AND quota >= ?", userID, amount).Update("quota", gorm.Expr("quota - ?", amount)); result.Error != nil {
			return result.Error
		} else if result.RowsAffected != 1 {
			return ErrInsufficientAgencyWalletQuota
		}
		return nil
	})
	if err == nil && amount > 0 && common.RedisEnabled {
		if cacheErr := cacheIncrUserQuota(userID, -int64(amount)); cacheErr != nil {
			common.SysLog("failed to sync legacy agency wallet cache: " + cacheErr.Error())
		}
		if _, cacheErr := cacheApplyTokenQuotaDelta(tokenID, tokenKey, -int64(amount)); cacheErr != nil {
			common.SysLog("failed to sync legacy agency token cache: " + cacheErr.Error())
		}
	}
	return err
}

func agencyFundingAllocatedTx(tx *gorm.DB, userID int64, chargeID string) (int64, error) {
	var rows []AgencyFundingAllocation
	// Every caller holds a per-user lock (the user account row or the
	// funding account row) before this read, so a plain read is safe:
	// same-user replays are already serialized and a FOR UPDATE scan on
	// the global idx_agency_alloc_charge tail would deadlock concurrent
	// time-prefixed charge inserts on MySQL (1213).
	if err := tx.
		Where("user_id = ? AND charge_id = ? AND (reserved > 0 OR consumed > 0 OR nonpaid_consumed > 0 OR debt_consumed > 0)", userID, chargeID).
		Find(&rows).Error; err != nil {
		return 0, err
	}
	var allocated int64
	maxInt64 := int64(^uint64(0) >> 1)
	for _, row := range rows {
		componentTotal, err := agencyFundingAllocationActiveTotal(row)
		if err != nil {
			return 0, err
		}
		if allocated > maxInt64-componentTotal {
			return 0, errors.New("agency funding allocation overflow")
		}
		allocated += componentTotal
	}
	return allocated, nil
}

// agencyFundingAllocationActiveParts returns the still-effective parts of a
// charge allocation. Payment chargebacks retain the original consumed amount
// for audit, mark the revoked portion, and add the same portion to
// DebtConsumed so a later cancellation can repay debt. Counting all three
// fields naively would double count that moved portion on an idempotent retry.
func agencyFundingAllocationActiveParts(row AgencyFundingAllocation) (int64, int64, int64, error) {
	if row.Reserved < 0 || row.Consumed < 0 || row.Reversed < 0 || row.RevokedReservedDebt < 0 ||
		row.NonpaidConsumed < 0 || row.RevokedNonpaid < 0 || row.DebtConsumed < 0 {
		return 0, 0, 0, errors.New("agency funding allocation overflow")
	}
	paidBase := row.Consumed
	if row.Reserved > paidBase {
		paidBase = row.Reserved
	}
	if row.Reversed > paidBase || row.RevokedReservedDebt > paidBase-row.Reversed ||
		row.RevokedNonpaid > row.NonpaidConsumed {
		return 0, 0, 0, errors.New("agency funding allocation overflow")
	}
	effectivePaid := paidBase - row.Reversed - row.RevokedReservedDebt
	effectiveNonpaid := row.NonpaidConsumed - row.RevokedNonpaid
	return effectivePaid, effectiveNonpaid, row.DebtConsumed, nil
}

func agencyFundingAllocationActiveTotal(row AgencyFundingAllocation) (int64, error) {
	effectivePaid, effectiveNonpaid, debtConsumed, err := agencyFundingAllocationActiveParts(row)
	if err != nil {
		return 0, err
	}
	maxInt64 := int64(^uint64(0) >> 1)
	if effectivePaid > maxInt64-effectiveNonpaid {
		return 0, errors.New("agency funding allocation overflow")
	}
	total := effectivePaid + effectiveNonpaid
	if total > maxInt64-debtConsumed {
		return 0, errors.New("agency funding allocation overflow")
	}
	return total + debtConsumed, nil
}

// TryReserveAgencyWalletAndToken performs the initial durable reservation as
// one database transaction, including token remain/used accounting. Redis is
// updated only after commit and is never used to decide success.
func TryReserveAgencyWalletAndToken(userID, tokenID, amount int, tokenKey, chargeID string, fundingTarget int64, unlimited bool) (int64, error) {
	paid, _, err := TryReserveAgencyWalletAndTokenWithSequence(userID, tokenID, amount, tokenKey, chargeID, fundingTarget, unlimited, nil)
	return paid, err
}

// TryReserveAgencyWalletAndTokenWithSnapshot is the quote-acceptance variant.
// It rechecks the active binding, agency state revision and policy revision
// while the user/token/funding rows are locked, so a quote cannot be accepted
// after an agency was disabled, transferred, or republished.
func TryReserveAgencyWalletAndTokenWithSnapshot(userID, tokenID, amount int, tokenKey, chargeID string, fundingTarget int64, unlimited bool, snapshot *agencycontract.PricingSnapshot) (int64, error) {
	paid, _, err := TryReserveAgencyWalletAndTokenWithSequence(userID, tokenID, amount, tokenKey, chargeID, fundingTarget, unlimited, snapshot)
	return paid, err
}

// TryReserveAgencyWalletAndTokenWithSequence is the sequence-aware variant
// used by the gateway financial event path. The returned money sequence is
// read from the same locked funding-account transaction that applies the
// reservation, so the event cannot race with a later wallet mutation.
func TryReserveAgencyWalletAndTokenWithSequence(userID, tokenID, amount int, tokenKey, chargeID string, fundingTarget int64, unlimited bool, snapshot *agencycontract.PricingSnapshot) (int64, int64, error) {
	return tryReserveAgencyWalletAndToken(userID, tokenID, amount, tokenKey, chargeID, fundingTarget, unlimited, snapshot)
}

func tryReserveAgencyWalletAndToken(userID, tokenID, amount int, tokenKey, chargeID string, fundingTarget int64, unlimited bool, snapshot *agencycontract.PricingSnapshot) (int64, int64, error) {
	if userID <= 0 || amount < 0 || strings.TrimSpace(chargeID) == "" || fundingTarget < 0 {
		return 0, 0, ErrAgencyFundingUnavailable
	}
	var paid int64
	var moneySeq int64
	deducted := false
	err := runAgencyFundingTransaction(func(tx *gorm.DB) error {
		var user User
		if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		if user.BillingMode == AgencyProvisioningBillingMode {
			return ErrAgencyProvisioning
		}
		if user.BillingMode != AgencyDurableBillingMode {
			return ErrAgencyFundingUnavailable
		}
		if err := EnsureAgencyFundingAccount(tx, int64(userID)); err != nil {
			return err
		}
		expired, err := expireAgencyRedemptionLotsTx(tx, int64(userID), common.GetTimestamp())
		if err != nil {
			return err
		}
		if expired > 0 {
			user.Quota -= int(expired)
		}
		var before AgencyFundingAccount
		if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&before).Error; err != nil {
			return err
		}
		if before.ReconcileBlocked {
			return errors.New("agency funding account is blocked for reconciliation")
		}
		var journal *AgencyBillingJournal
		if snapshot != nil {
			var err error
			journal, err = acceptAgencyJournalTx(tx, userID, tokenID, chargeID, snapshot)
			if err != nil {
				return err
			}
		}
		allocated, err := agencyFundingAllocatedTx(tx, int64(userID), chargeID)
		if err != nil {
			return err
		}
		if fundingTarget <= allocated {
			if journal != nil {
				if err := recordAgencyReservationTx(tx, journal, snapshot, allocated, before); err != nil {
					return err
				}
			}
			var account AgencyFundingAccount
			if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
				return err
			}
			moneySeq = account.MoneySeq
			return nil
		}
		if user.Quota < amount {
			return ErrInsufficientAgencyWalletQuota
		}
		if tokenID > 0 {
			var token Token
			query := AgencyLockForUpdate(tx).Where("id = ?", tokenID)
			if strings.TrimSpace(tokenKey) != "" {
				query = query.Where(map[string]any{"key": tokenKey})
			}
			if err := query.First(&token).Error; err != nil {
				return err
			}
			if !unlimited && token.RemainQuota < amount {
				return ErrInsufficientAgencyTokenQuota
			}
			if int64(token.RemainQuota)-int64(amount) < -int64(common.MaxQuota)-1 || int64(token.UsedQuota)+int64(amount) > int64(common.MaxQuota) {
				return errors.New("agency token quota arithmetic overflow")
			}
			update := tx.Model(&Token{}).Where("id = ?", tokenID)
			if !unlimited {
				update = update.Where("remain_quota >= ?", amount)
			}
			result := update.Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota - ?", amount), "used_quota": gorm.Expr("used_quota + ?", amount), "accessed_time": common.GetTimestamp()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrInsufficientAgencyTokenQuota
			}
		}
		if result := tx.Model(&User{}).Where("id = ? AND quota >= ?", userID, amount).Update("quota", gorm.Expr("quota - ?", amount)); result.Error != nil {
			return result.Error
		} else if result.RowsAffected != 1 {
			return ErrInsufficientAgencyWalletQuota
		}
		paid, err = reserveAgencyFundingTxWithRule(tx, int64(userID), chargeID, fundingTarget, agencyFundingRuleVersion(snapshot))
		deducted = amount > 0
		if err != nil {
			return err
		}
		var account AgencyFundingAccount
		if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
			return err
		}
		moneySeq = account.MoneySeq
		if journal != nil {
			if err := recordAgencyReservationTx(tx, journal, snapshot, fundingTarget, before); err != nil {
				return err
			}
			if err := tx.Where("user_id = ?", userID).First(&account).Error; err != nil {
				return err
			}
			moneySeq = account.MoneySeq
		}
		return nil
	})
	if err == nil && deducted {
		if common.RedisEnabled {
			if cacheErr := InvalidateUserCache(userID); cacheErr != nil {
				common.SysLog("failed to sync agency wallet cache: " + cacheErr.Error())
			}
			if tokenID > 0 && strings.TrimSpace(tokenKey) != "" {
				if _, cacheErr := cacheApplyTokenQuotaDelta(tokenID, tokenKey, -int64(amount)); cacheErr != nil {
					common.SysLog("failed to sync agency token cache: " + cacheErr.Error())
				}
			}
		}
	}
	return paid, moneySeq, err
}

// ValidateAgencyPricingSnapshotTx verifies the immutable quote against the
// authoritative rows inside the caller's transaction. It intentionally does
// not read Redis and does not permit a disabled agency to accept a new charge.
func ValidateAgencyPricingSnapshotTx(tx *gorm.DB, userID int64, snapshot *agencycontract.PricingSnapshot) error {
	if tx == nil || userID <= 0 || snapshot == nil || snapshot.AgencyID <= 0 || snapshot.BindingID <= 0 || snapshot.PolicyVersionID <= 0 {
		return ErrAgencyFundingUnavailable
	}
	var active AgencyActiveUserBinding
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&active).Error; err != nil {
		return err
	}
	if active.AgencyID != snapshot.AgencyID || active.BindingID != snapshot.BindingID || active.Revision != snapshot.BindingRevision {
		return errors.New("agency pricing snapshot binding revision conflict")
	}
	var binding AgencyUserBinding
	if err := AgencyLockForUpdate(tx).Where("id = ? AND user_id = ? AND agency_id = ? AND ended_at_ms IS NULL", snapshot.BindingID, userID, snapshot.AgencyID).First(&binding).Error; err != nil {
		return err
	}
	if binding.Revision != snapshot.BindingRevision {
		return errors.New("agency pricing snapshot binding revision conflict")
	}
	var agency Agency
	if err := AgencyLockForUpdate(tx).Where("id = ?", snapshot.AgencyID).First(&agency).Error; err != nil {
		return err
	}
	if (agency.Status != "active" && agency.Status != "disabled") || agency.StateRevision != snapshot.AgencyStateRevision ||
		agency.CurrentPolicyVersionID != snapshot.PolicyVersionID {
		return errors.New("agency pricing snapshot is stale")
	}
	if snapshot.CommissionEligible != (agency.Status == "active") {
		return errors.New("agency pricing eligibility snapshot is stale")
	}
	var policy AgencyPricePolicyVersion
	if err := tx.Where("id = ? AND agency_id = ?", snapshot.PolicyVersionID, snapshot.AgencyID).First(&policy).Error; err != nil {
		return err
	}
	if policy.Revision != snapshot.PolicyRevision {
		return errors.New("agency pricing snapshot policy revision conflict")
	}
	return nil
}

// ReleaseAgencyWalletAndToken atomically returns a durable customer's wallet
// allocation and the corresponding API-token quota. It is used by realtime
// settlement when the provider's final cumulative usage is lower than the
// amount reserved from earlier usage frames. Keeping both adjustments in one
// transaction prevents a partial release from making the wallet and token
// projections disagree.
func ReleaseAgencyWalletAndToken(userID, tokenID, amount int, tokenKey, chargeID string) (int64, error) {
	if userID <= 0 || amount < 0 || strings.TrimSpace(chargeID) == "" {
		return 0, ErrAgencyFundingUnavailable
	}
	var released int64
	var releasedTotal int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := AgencyLockForUpdate(tx).Select("id, billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		if user.BillingMode == AgencyProvisioningBillingMode {
			return ErrAgencyProvisioning
		}
		allocated, err := agencyFundingAllocatedTx(tx, int64(userID), chargeID)
		if err != nil {
			return err
		}
		releaseAmount := int64(amount)
		if releaseAmount > allocated {
			releaseAmount = allocated
		}
		released, err = ReleaseUserQuotaAndAgencyTx(tx, userID, amount, chargeID)
		if err != nil {
			return err
		}
		releasedTotal = releaseAmount
		if releasedTotal == 0 || tokenID <= 0 {
			return nil
		}
		query := AgencyLockForUpdate(tx).Where("id = ?", tokenID)
		if strings.TrimSpace(tokenKey) != "" {
			query = query.Where(map[string]any{"key": tokenKey})
		}
		var token Token
		if err := query.First(&token).Error; err != nil {
			return err
		}
		result := tx.Model(&Token{}).
			Where("id = ? AND used_quota >= ?", tokenID, releasedTotal).
			Updates(map[string]any{
				"remain_quota":  gorm.Expr("remain_quota + ?", releasedTotal),
				"used_quota":    gorm.Expr("used_quota - ?", releasedTotal),
				"accessed_time": common.GetTimestamp(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAgencyInsufficientTokenUsage
		}
		return nil
	})
	if err == nil && releasedTotal > 0 && common.RedisEnabled {
		if cacheErr := InvalidateUserCache(userID); cacheErr != nil {
			common.SysLog("failed to invalidate agency wallet cache: " + cacheErr.Error())
		}
		if tokenID > 0 && strings.TrimSpace(tokenKey) != "" {
			if _, cacheErr := cacheApplyTokenQuotaDelta(tokenID, tokenKey, releasedTotal); cacheErr != nil {
				common.SysLog("failed to sync agency token cache: " + cacheErr.Error())
			}
		}
	}
	return released, err
}

// ReleaseUserQuotaAndAgency atomically returns a wallet delta to the user and
// releases the corresponding cumulative agency allocation. It is the inverse
// of TryReserveUserQuotaAndAgency for settlement/refund paths.
func ReleaseUserQuotaAndAgency(userID int, amount int, chargeID string) (int64, error) {
	if userID <= 0 || amount < 0 || strings.TrimSpace(chargeID) == "" {
		return 0, ErrAgencyFundingUnavailable
	}
	var released int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		released, err = ReleaseUserQuotaAndAgencyTx(tx, userID, amount, chargeID)
		return err
	})
	if err == nil && amount > 0 && common.RedisEnabled {
		if isAgencyDurableUser(userID) {
			if cacheErr := InvalidateUserCache(userID); cacheErr != nil {
				common.SysLog("failed to invalidate agency wallet cache: " + cacheErr.Error())
			}
		} else if cacheErr := cacheIncrUserQuota(userID, int64(amount)); cacheErr != nil {
			common.SysLog("failed to sync agency wallet cache: " + cacheErr.Error())
		}
	}
	return released, err
}

// ReleaseUserQuotaAndAgencyTx is the transaction-scoped inverse reservation.
func ReleaseUserQuotaAndAgencyTx(tx *gorm.DB, userID, amount int, chargeID string) (int64, error) {
	if tx == nil || userID <= 0 || amount < 0 || strings.TrimSpace(chargeID) == "" {
		return 0, ErrAgencyFundingUnavailable
	}
	var user User
	if err := AgencyLockForUpdate(tx).Select("id, billing_mode").First(&user, userID).Error; err != nil {
		return 0, err
	}
	if user.BillingMode == AgencyProvisioningBillingMode {
		return 0, ErrAgencyProvisioning
	}
	if user.BillingMode != AgencyDurableBillingMode || amount == 0 {
		if err := tx.Model(&User{}).Where("id = ?", userID).Update("quota", gorm.Expr("quota + ?", amount)).Error; err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := expireAgencyRedemptionLotsTx(tx, int64(userID), common.GetTimestamp()); err != nil {
		return 0, err
	}
	allocated, err := agencyFundingAllocatedTx(tx, int64(userID), chargeID)
	if err != nil {
		return 0, err
	}
	if allocated <= 0 {
		return 0, nil
	}
	releaseAmount := int64(amount)
	if releaseAmount > allocated {
		releaseAmount = allocated
	}
	if releaseAmount > int64(^uint(0)>>1) {
		return 0, errors.New("agency funding release amount overflow")
	}
	result, err := releaseAgencyFundingDetailedTx(tx, chargeID, releaseAmount)
	if err != nil {
		return 0, err
	}
	walletRelease := result.ReleasedTotal - result.ExpiredNonpaid
	if walletRelease < 0 {
		return 0, ErrAgencyFundingUnavailable
	}
	if walletRelease > 0 {
		if err := tx.Model(&User{}).Where("id = ?", userID).Update("quota", gorm.Expr("quota + ?", walletRelease)).Error; err != nil {
			return 0, err
		}
	}
	return result.PaidReleased, nil
}

// ReleaseUserQuotaAndAgencyWithToken is the atomic inverse of
// TryReserveUserQuotaAndAgencyWithToken for legacy agency-managed users.
// Durable users must use ReleaseAgencyWalletAndToken.
func ReleaseUserQuotaAndAgencyWithToken(userID, tokenID, amount int, tokenKey, chargeID string) error {
	if userID <= 0 || tokenID <= 0 || amount < 0 || strings.TrimSpace(chargeID) == "" {
		return ErrAgencyFundingUnavailable
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := AgencyLockForUpdate(tx).Select("id, billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		if user.BillingMode == AgencyProvisioningBillingMode {
			return ErrAgencyProvisioning
		}
		if user.BillingMode == AgencyDurableBillingMode {
			return ErrAgencyFundingUnavailable
		}
		if amount == 0 {
			return nil
		}

		query := AgencyLockForUpdate(tx).Where("id = ?", tokenID)
		if strings.TrimSpace(tokenKey) != "" {
			query = query.Where(map[string]any{"key": tokenKey})
		}
		var token Token
		if err := query.First(&token).Error; err != nil {
			return err
		}
		if result := tx.Model(&Token{}).
			Where("id = ? AND used_quota >= ?", tokenID, amount).
			Updates(map[string]any{
				"remain_quota":  gorm.Expr("remain_quota + ?", amount),
				"used_quota":    gorm.Expr("used_quota - ?", amount),
				"accessed_time": common.GetTimestamp(),
			}); result.Error != nil {
			return result.Error
		} else if result.RowsAffected != 1 {
			return ErrAgencyInsufficientTokenUsage
		}
		return tx.Model(&User{}).Where("id = ?", userID).Update("quota", gorm.Expr("quota + ?", amount)).Error
	})
	if err == nil && amount > 0 && common.RedisEnabled {
		if cacheErr := cacheIncrUserQuota(userID, int64(amount)); cacheErr != nil {
			common.SysLog("failed to sync legacy agency wallet cache: " + cacheErr.Error())
		}
		if _, cacheErr := cacheApplyTokenQuotaDelta(tokenID, tokenKey, int64(amount)); cacheErr != nil {
			common.SysLog("failed to sync legacy agency token cache: " + cacheErr.Error())
		}
	}
	return err
}

// EnsureAgencyFundingAccount creates the durable funding projection for an
// invited customer. Existing wallet quota is treated as paid only during the
// one-time migration/bootstrap; subsequent changes must use typed operations.
func EnsureAgencyFundingAccount(tx *gorm.DB, userID int64) error {
	if tx == nil || userID <= 0 {
		return ErrAgencyFundingUnavailable
	}
	var user User
	if err := tx.Select("id, quota, billing_mode").First(&user, userID).Error; err != nil {
		return err
	}
	if user.BillingMode != AgencyDurableBillingMode {
		return nil
	}
	var account AgencyFundingAccount
	err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	paid := int64(user.Quota)
	debt := int64(0)
	if paid < 0 {
		debt = -paid
		paid = 0
	}
	now := time.Now().Unix()
	account = AgencyFundingAccount{UserID: userID, PaidAvailable: paid, DebtQuota: debt, Version: 1, UpdatedAt: now}
	if err := tx.Create(&account).Error; err != nil {
		return err
	}
	if paid == 0 && debt == 0 {
		return nil
	}
	var lotID *int64
	if paid > 0 {
		lot := AgencyFundingLot{UserID: userID, SourceKind: "legacy_migration", SourceID: fmt.Sprintf("legacy-user-%d", userID), CompletionSource: "migration", PaidInitial: paid, PaidAvailable: paid, MoneySeq: 1, Version: 1, CreatedAt: now}
		if err := tx.Create(&lot).Error; err != nil {
			return err
		}
		lotID = &lot.ID
	}
	return tx.Create(&AgencyFundingLedger{OperationID: fmt.Sprintf("legacy-funding-%d", userID), EntryNo: 0, UserID: userID, MoneySeq: 1, SourceKind: "legacy_migration", LotID: lotID, PaidDelta: paid, DebtAfter: debt, PaidAfter: paid, CreatedAtMS: now * 1000}).Error
}

// RecordAgencyTopup adds a typed paid/bonus lot in the same transaction as
// the authoritative user quota update. Duplicate source operations are
// idempotent and never create a second lot.
func RecordAgencyTopup(tx *gorm.DB, userID int64, sourceKind, sourceID, completionSource string, paidQuota, bonusQuota int64, snapshots ...*TopupFundingSnapshot) error {
	return recordAgencyTopupWithActor(tx, userID, sourceKind, sourceID, completionSource, paidQuota, bonusQuota, 0, snapshots...)
}

// RecordAgencyTopupWithActor is the provenance-preserving form used by
// administrator-assisted payments. The actor is deliberately separate from
// the beneficiary userID so reports cannot mistake the payer/creator for the
// account that receives the quota.
func RecordAgencyTopupWithActor(tx *gorm.DB, userID int64, sourceKind, sourceID, completionSource string, paidQuota, bonusQuota, initiatedByUserID int64, snapshots ...*TopupFundingSnapshot) error {
	return recordAgencyTopupWithActor(tx, userID, sourceKind, sourceID, completionSource, paidQuota, bonusQuota, initiatedByUserID, snapshots...)
}

func recordAgencyTopupWithActor(tx *gorm.DB, userID int64, sourceKind, sourceID, completionSource string, paidQuota, bonusQuota, initiatedByUserID int64, snapshots ...*TopupFundingSnapshot) error {
	if tx == nil || userID <= 0 || strings.TrimSpace(sourceID) == "" || len(sourceID) > 120 || paidQuota < 0 || bonusQuota < 0 || (bonusQuota > 0 && paidQuota > int64(^uint64(0)>>1)-bonusQuota) {
		return ErrAgencyFundingUnavailable
	}
	var user User
	if err := tx.Select("id, billing_mode").First(&user, userID).Error; err != nil {
		return err
	}
	if user.BillingMode == AgencyProvisioningBillingMode {
		return ErrAgencyProvisioning
	}
	if user.BillingMode != AgencyDurableBillingMode {
		return nil
	}
	if len(snapshots) > 1 {
		return ErrTopupPaymentSnapshot
	}
	var payment TopupPaymentSnapshot
	conversionJSON := ""
	quotaPerUnit := ""
	if len(snapshots) == 1 && snapshots[0] != nil {
		snapshot := snapshots[0]
		if snapshot.Payment != nil {
			var err error
			payment, err = snapshot.Payment.normalized()
			if err != nil {
				return err
			}
		}
		if snapshot.Conversion != nil {
			if snapshot.Conversion.CreditedQuota != fmt.Sprint(paidQuota+bonusQuota) {
				return ErrTopupPaymentSnapshot
			}
			encoded, err := common.Marshal(snapshot.Conversion)
			if err != nil {
				return err
			}
			conversionJSON = string(encoded)
			quotaPerUnit = snapshot.Conversion.QuotaPerUnit
		}
		if initiatedByUserID == 0 {
			initiatedByUserID = snapshot.InitiatedByUserID
		}
	}
	if payment.ActualMoney == "0" {
		// A verified zero-price checkout can grant quota but provides no paid
		// funding. Keep that credit noncommissionable like any other bonus.
		bonusQuota += paidQuota
		paidQuota = 0
	}
	if sourceKind == "admin" || completionSource == "admin_adjustment" {
		// Manual credits must never become verified online payments because
		// an old order still carries its original payment-provider metadata.
		if payment.ActualMoney != "" {
			return ErrTopupPaymentSnapshot
		}
		bonusQuota += paidQuota
		paidQuota = 0
		completionSource = "admin_adjustment"
	}
	var existing AgencyFundingLedger
	if err := tx.Where("operation_id = ? AND entry_no = 0", sourceID).First(&existing).Error; err == nil {
		var fact AgencyTopupFact
		if err := tx.Where("source_operation_id = ?", sourceID).First(&fact).Error; err != nil {
			return ErrAgencyTopupConflict
		}
		if fact.UserID != userID || fact.PaidQuota != paidQuota || fact.BonusQuota != bonusQuota || fact.CompletionSource != completionSource || fact.PaymentStatus != "success" || existing.SourceKind != sourceKind || fact.ActualMoney != payment.ActualMoney || fact.CurrencyCode != payment.CurrencyCode || fact.PaymentReference != payment.PaymentReference || fact.QuotaConversionSnapshot != conversionJSON || fact.InitiatedByUserID != initiatedByUserID {
			return ErrAgencyTopupConflict
		}
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := EnsureAgencyFundingAccount(tx, userID); err != nil {
		return err
	}
	var account AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
		return err
	}
	seq := account.MoneySeq + 1
	if seq <= account.MoneySeq {
		return errors.New("agency funding sequence overflow")
	}
	now := time.Now().UnixMilli()
	sourceSnapshot, err := common.Marshal(map[string]any{"source_kind": sourceKind, "source_id": sourceID, "actor_user_id": initiatedByUserID, "actual_money": payment.ActualMoney, "currency_code": payment.CurrencyCode, "payment_reference": payment.PaymentReference, "quota_conversion": conversionJSON})
	if err != nil {
		return err
	}
	lot := AgencyFundingLot{UserID: userID, SourceKind: sourceKind, SourceID: sourceID, ActorUserID: initiatedByUserID, SourceSnapshotJSON: string(sourceSnapshot), CompletionSource: completionSource, PaidInitial: paidQuota, BonusInitial: bonusQuota, PaidAvailable: paidQuota, BonusAvailable: bonusQuota, MoneySeq: seq, Version: 1, CreatedAt: now / 1000}
	if err := tx.Create(&lot).Error; err != nil {
		return err
	}
	paidRepaid, nonpaidRepaid, err := repayAgencyFundingSourcesTx(tx, userID, sourceID, seq, account.DebtQuota,
		[]agencyFundingSource{{LotID: lot.ID, Paid: paidQuota, Nonpaid: bonusQuota}})
	if err != nil {
		return err
	}
	paidForWallet, nonpaidForWallet := paidQuota-paidRepaid, bonusQuota-nonpaidRepaid
	debtDelta := -paidRepaid - nonpaidRepaid
	paidAvailable := account.PaidAvailable + paidForWallet
	nonpaidAvailable := account.NonpaidAvailable + nonpaidForWallet
	debt := account.DebtQuota + debtDelta
	if paidAvailable < 0 || nonpaidAvailable < 0 || debt < 0 {
		return errors.New("agency funding balance overflow")
	}
	if err := tx.Model(&account).Updates(map[string]any{"paid_available": paidAvailable, "nonpaid_available": nonpaidAvailable, "debt_quota": debt, "money_seq": seq, "version": account.Version + 1, "updated_at": now / 1000}).Error; err != nil {
		return err
	}
	lotID := lot.ID
	if err := tx.Create(&AgencyFundingLedger{OperationID: sourceID, EntryNo: 0, UserID: userID, MoneySeq: seq, SourceKind: sourceKind, LotID: &lotID, PaidDelta: paidForWallet, NonpaidDelta: nonpaidForWallet, DebtDelta: debtDelta, PaidAfter: paidAvailable, NonpaidAfter: nonpaidAvailable, DebtAfter: debt, CurrencyCode: payment.CurrencyCode, CreatedAtMS: now}).Error; err != nil {
		return err
	}
	var active AgencyActiveUserBinding
	var agencyID, bindingID *int64
	if err := tx.Where("user_id = ?", userID).First(&active).Error; err == nil {
		agencyID = &active.AgencyID
		bindingID = &active.BindingID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := tx.Create(&AgencyTopupFact{SourceOperationID: sourceID, SourceID: sourceID, UserID: userID, InitiatedByUserID: initiatedByUserID, AgencyID: agencyID, BindingID: bindingID, CreditedQuota: paidQuota + bonusQuota, PaidQuota: paidQuota, BonusQuota: bonusQuota, FundingSource: sourceKind, ActualMoney: payment.ActualMoney, CurrencyCode: payment.CurrencyCode, PaymentReference: payment.PaymentReference, QuotaConversionSnapshot: conversionJSON, CompletionSource: completionSource, PaymentStatus: "success", OccurredAtMS: now}).Error; err != nil {
		return err
	}
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: "agency-topup-" + sourceID, EventType: "agency.topup_completed", FinancialChargeID: sourceID, OperationID: sourceID, JournalRevision: 1, MoneySeq: seq, EventIndex: 0, EventCount: 1, OccurredAtMS: now, UserID: userID, AgencyID: agencyID, BindingID: bindingID, BusinessStatus: "success", BillingStatus: "funded", CommissionEligible: false, CommissionSkipReason: "topup_noncommissionable"}
	event.CurrencyCode = payment.CurrencyCode
	event.QuotaPerUnit = quotaPerUnit
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	if err := tx.Create(&AgencyBillingOutbox{EventID: event.EventID, OperationID: sourceID, EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: userID, MoneySeq: seq, Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion, CreatedAtMS: now}).Error; err != nil {
		return err
	}
	return tx.Create(&AgencyEventDelivery{EventID: event.EventID, Status: "pending", NextRetryAt: time.Now().Unix(), CreatedAt: time.Now().Unix()}).Error
}

// ReverseAgencyTopupTx applies a payment refund/chargeback to the exact
// funding source that created the customer's paid lot. Available paid quota is
// revoked immediately; paid quota already allocated to a charge becomes debt
// and is marked on that allocation so a later model cancellation cannot make
// the revoked money available a second time.
func ReverseAgencyTopupTx(tx *gorm.DB, input AgencyFundingReversalInput) ([]AgencyFundingReversalCharge, bool, error) {
	if tx == nil || input.UserID <= 0 || strings.TrimSpace(input.RefundID) == "" ||
		strings.TrimSpace(input.SourceOperationID) == "" || input.Quota <= 0 ||
		input.Quota > int64(common.MaxQuota) {
		return nil, false, ErrAgencyFundingUnavailable
	}
	input.RefundID = strings.TrimSpace(input.RefundID)
	input.SourceOperationID = strings.TrimSpace(input.SourceOperationID)
	input.CurrencyCode = strings.ToUpper(strings.TrimSpace(input.CurrencyCode))
	input.PaymentReference = strings.TrimSpace(input.PaymentReference)
	input.EvidenceRef = strings.TrimSpace(input.EvidenceRef)
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.RefundID) > 128 || len(input.SourceOperationID) > 128 || len(input.CurrencyCode) > 16 ||
		len(input.PaymentReference) > 191 || len(input.EvidenceRef) > 191 || len(input.Reason) > 2000 {
		return nil, false, ErrAgencyFundingUnavailable
	}
	// Serialize against other wallet operations before touching a top-up or
	// allocation. A locking read also sees a concurrent committed refund under
	// MySQL's repeatable-read isolation, rather than a stale transaction snapshot.
	var user User
	if err := AgencyLockForUpdate(tx).Select("id, quota, billing_mode").First(&user, input.UserID).Error; err != nil {
		return nil, false, err
	}
	var existing AgencyFundingReversal
	if err := AgencyLockForUpdate(tx).Where("refund_id = ?", input.RefundID).First(&existing).Error; err == nil {
		// Empty currency means the original source's frozen currency. All other
		// evidence is part of the immutable request; changing it under the same
		// refund ID must not be reported as a successful retry.
		if input.CurrencyCode == "" {
			input.CurrencyCode = existing.CurrencyCode
		}
		if existing.RefundID != input.RefundID || existing.SourceOperationID != input.SourceOperationID || existing.UserID != input.UserID || existing.Quota != input.Quota ||
			existing.CurrencyCode != input.CurrencyCode || existing.PaymentReference != input.PaymentReference ||
			existing.EvidenceRef != input.EvidenceRef || existing.Reason != input.Reason {
			return nil, false, ErrAgencyFundingReversalConflict
		}
		charges, err := loadAgencyFundingReversalChargesTx(tx, input.RefundID)
		return charges, false, err
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if user.BillingMode != AgencyDurableBillingMode {
		return nil, false, errors.New("funding reversal requires a durable user")
	}
	if int64(user.Quota)-input.Quota < -int64(common.MaxQuota)-1 {
		return nil, false, errors.New("funding reversal wallet quota underflow")
	}
	if err := EnsureAgencyFundingAccount(tx, input.UserID); err != nil {
		return nil, false, err
	}
	var account AgencyFundingAccount
	if err := AgencyLockForUpdate(tx).Where("user_id = ?", input.UserID).First(&account).Error; err != nil {
		return nil, false, err
	}

	var topup AgencyTopupFact
	if err := AgencyLockForUpdate(tx).Where("source_operation_id = ?", input.SourceOperationID).First(&topup).Error; err != nil {
		return nil, false, err
	}
	if topup.UserID != input.UserID ||
		(topup.PaymentStatus != "success" && topup.PaymentStatus != "partially_refunded") {
		return nil, false, errors.New("funding reversal source is not refundable")
	}
	if topup.CreditedQuota < 0 || topup.RefundedQuota < 0 || topup.RefundedQuota > topup.CreditedQuota {
		return nil, false, errors.New("funding reversal source has invalid refund state")
	}
	if input.CurrencyCode != "" && topup.CurrencyCode != "" && input.CurrencyCode != topup.CurrencyCode {
		return nil, false, ErrAgencyFundingReversalConflict
	}
	if input.Quota > topup.CreditedQuota-topup.RefundedQuota {
		return nil, false, errors.New("funding reversal exceeds original credit")
	}
	if input.CurrencyCode == "" {
		input.CurrencyCode = topup.CurrencyCode
	}
	var lots []AgencyFundingLot
	if err := AgencyLockForUpdate(tx).
		Where("user_id = ? AND source_id = ?", input.UserID, input.SourceOperationID).
		Order("money_seq ASC, id ASC").Find(&lots).Error; err != nil {
		return nil, false, err
	}
	if len(lots) == 0 {
		return nil, false, errors.New("funding reversal source lot is missing")
	}

	remaining := input.Quota
	var availableRevoked, nonpaidRevoked, consumedRevoked int64
	charges := make([]AgencyFundingReversalCharge, 0)
	for i := range lots {
		if remaining == 0 {
			break
		}
		take := lots[i].PaidAvailable
		if take > remaining {
			take = remaining
		}
		if take <= 0 {
			continue
		}
		lots[i].PaidAvailable -= take
		lots[i].PaidRevoked += take
		lots[i].Version++
		if err := tx.Model(&lots[i]).Updates(map[string]any{
			"paid_available": lots[i].PaidAvailable,
			"paid_revoked":   lots[i].PaidRevoked,
			"version":        lots[i].Version,
		}).Error; err != nil {
			return nil, false, err
		}
		availableRevoked += take
		remaining -= take
	}
	// Bonuses are tracked on the same source lot. They are intentionally
	// revoked after paid availability, so a mixed paid+bonus refund never
	// consumes another payment's bonus balance.
	for i := range lots {
		if remaining == 0 {
			break
		}
		take := lots[i].BonusAvailable
		if take > remaining {
			take = remaining
		}
		if take <= 0 {
			continue
		}
		lots[i].BonusAvailable -= take
		lots[i].BonusRevoked += take
		lots[i].Version++
		if err := tx.Model(&lots[i]).Updates(map[string]any{
			"bonus_available": lots[i].BonusAvailable,
			"bonus_revoked":   lots[i].BonusRevoked,
			"version":         lots[i].Version,
		}).Error; err != nil {
			return nil, false, err
		}
		nonpaidRevoked += take
		remaining -= take
	}

	lotIDs := make([]int64, 0, len(lots))
	for _, lot := range lots {
		lotIDs = append(lotIDs, lot.ID)
	}
	// A top-up may have repaid an older chargeback debt instead of creating
	// paid_available. Reversing that top-up recreates the original debt and
	// marks the repayment as reversed, rather than silently treating it as a
	// fresh paid balance.
	if remaining > 0 {
		var repayments []AgencyDebtRepayment
		if err := AgencyLockForUpdate(tx).
			Where("funding_lot_id IN ? AND quota > reversed_quota", lotIDs).
			Order("id ASC").Find(&repayments).Error; err != nil {
			return nil, false, err
		}
		for i := range repayments {
			if remaining == 0 {
				break
			}
			if repayments[i].FundingLotID == nil {
				continue
			}
			available := repayments[i].Quota - repayments[i].ReversedQuota
			if available <= 0 {
				continue
			}
			take := available
			if take > remaining {
				take = remaining
			}
			var debt AgencyFundingDebt
			if err := AgencyLockForUpdate(tx).First(&debt, repayments[i].DebtID).Error; err != nil {
				return nil, false, err
			}
			if debt.UserID != input.UserID || debt.OriginalQuota < 0 || debt.ReversedQuota < 0 || debt.ReversedQuota > debt.OriginalQuota ||
				debt.OutstandingQuota < 0 || debt.OutstandingQuota > debt.OriginalQuota-debt.ReversedQuota ||
				take > debt.OriginalQuota-debt.ReversedQuota-debt.OutstandingQuota {
				return nil, false, ErrAgencyComponentRefundProvenance
			}
			debt.OutstandingQuota += take
			if err := tx.Model(&debt).Updates(map[string]any{"outstanding_quota": debt.OutstandingQuota}).Error; err != nil {
				return nil, false, err
			}
			repayments[i].ReversedQuota += take
			if err := tx.Model(&repayments[i]).Updates(map[string]any{"reversed_quota": repayments[i].ReversedQuota}).Error; err != nil {
				return nil, false, err
			}
			for j := range lots {
				if lots[j].ID != *repayments[i].FundingLotID {
					continue
				}
				updates := map[string]any{}
				switch repayments[i].SourceKind {
				case "", "paid":
					if lots[j].PaidDebtRepaid < take {
						return nil, false, ErrAgencyComponentRefundProvenance
					}
					lots[j].PaidDebtRepaid -= take
					lots[j].PaidRevoked += take
					updates["paid_debt_repaid"], updates["paid_revoked"] = lots[j].PaidDebtRepaid, lots[j].PaidRevoked
				case "nonpaid":
					if lots[j].BonusDebtRepaid < take {
						return nil, false, ErrAgencyComponentRefundProvenance
					}
					lots[j].BonusDebtRepaid -= take
					lots[j].BonusRevoked += take
					updates["bonus_debt_repaid"], updates["bonus_revoked"] = lots[j].BonusDebtRepaid, lots[j].BonusRevoked
				default:
					return nil, false, ErrAgencyComponentRefundProvenance
				}
				lots[j].Version++
				updates["version"] = lots[j].Version
				if err := tx.Model(&lots[j]).Updates(updates).Error; err != nil {
					return nil, false, err
				}
				break
			}
			consumedRevoked += take
			remaining -= take
		}
	}

	// If the refund exceeds currently available quota, walk the original
	// charge allocations for this source's lots. Allocation rows are retained
	// for audit; only the effective refundable component is reduced.
	if remaining > 0 {
		var allocations []AgencyFundingAllocation
		if err := AgencyLockForUpdate(tx).
			Where("user_id = ? AND lot_id IN ? AND (reserved > 0 OR consumed > 0 OR nonpaid_consumed > 0)", input.UserID, lotIDs).
			Order("id ASC").Find(&allocations).Error; err != nil {
			return nil, false, err
		}
		for i := range allocations {
			if remaining == 0 {
				break
			}
			paidBase := allocations[i].Consumed
			if allocations[i].Reserved > paidBase {
				paidBase = allocations[i].Reserved
			}
			effectivePaid := paidBase - allocations[i].Reversed - allocations[i].RevokedReservedDebt
			effectiveBonus := allocations[i].NonpaidConsumed - allocations[i].RevokedNonpaid
			if effectivePaid < 0 || effectiveBonus < 0 {
				return nil, false, errors.New("funding reversal allocation overflow")
			}
			if effectivePaid == 0 && effectiveBonus == 0 {
				continue
			}
			takePaid := effectivePaid
			if takePaid > remaining {
				takePaid = remaining
			}
			takeBonus := effectiveBonus
			if takeBonus > remaining-takePaid {
				takeBonus = remaining - takePaid
			}
			take := takePaid + takeBonus
			if take <= 0 {
				continue
			}
			// Component refunds need source-specific revocation watermarks and
			// commission attribution. The legacy charge-only result below cannot
			// supply them, even for bonus-funded components with zero commission.
			// Enforce this at the money boundary, not only in a service hook after
			// the caller has already committed the source reversal.
			var componentSource AgencyComponentFunding
			componentLookup := tx.Where("allocation_id = ?", allocations[i].ID).Limit(1).Find(&componentSource)
			if componentLookup.Error != nil {
				return nil, false, componentLookup.Error
			}
			if componentLookup.RowsAffected != 0 {
				return nil, false, ErrAgencyComponentRefundProvenance
			}
			if takePaid > 0 {
				allocations[i].RevokedReservedDebt += takePaid
			}
			if takeBonus > 0 {
				allocations[i].RevokedNonpaid += takeBonus
			}
			// Preserve the original charge allocation while moving the
			// reversed payment portion into the charge's debt bucket. This is
			// what lets a later cancellation release the frozen charge by
			// reducing debt without resurrecting quota from the revoked lot.
			// Without this marker, ReleaseUserQuotaAndAgencyTx would add the
			// wallet amount back to users.quota but leave the funding debt
			// outstanding.
			if allocations[i].DebtConsumed > int64(^uint64(0)>>1)-take {
				return nil, false, errors.New("funding reversal debt allocation overflow")
			}
			allocations[i].DebtConsumed += take
			allocations[i].Version++
			if err := tx.Model(&allocations[i]).Updates(map[string]any{
				"revoked_reserved_debt": allocations[i].RevokedReservedDebt,
				"revoked_nonpaid":       allocations[i].RevokedNonpaid,
				"debt_consumed":         allocations[i].DebtConsumed,
				"version":               allocations[i].Version,
			}).Error; err != nil {
				return nil, false, err
			}
			var lot AgencyFundingLot
			if err := AgencyLockForUpdate(tx).First(&lot, allocations[i].LotID).Error; err != nil {
				return nil, false, err
			}
			if takePaid > 0 {
				// Gateway allocations use consumed; the sidecar's legacy
				// reservation helper uses reserved. Treat the two fields as
				// cumulative watermarks (not additive) and consume the field
				// that actually carries this allocation.
				if lot.PaidConsumed >= takePaid {
					lot.PaidConsumed -= takePaid
				} else if lot.PaidReserved >= takePaid {
					lot.PaidReserved -= takePaid
				} else {
					return nil, false, errors.New("funding reversal allocation underflow")
				}
				lot.PaidRevoked += takePaid
			}
			if takeBonus > 0 {
				if lot.BonusConsumed < takeBonus {
					return nil, false, errors.New("funding reversal bonus allocation underflow")
				}
				lot.BonusConsumed -= takeBonus
				lot.BonusRevoked += takeBonus
			}
			lot.Version++
			if err := tx.Model(&lot).Updates(map[string]any{
				"paid_consumed":  lot.PaidConsumed,
				"paid_reserved":  lot.PaidReserved,
				"paid_revoked":   lot.PaidRevoked,
				"bonus_consumed": lot.BonusConsumed,
				"bonus_revoked":  lot.BonusRevoked,
				"version":        lot.Version,
			}).Error; err != nil {
				return nil, false, err
			}
			// Payment chargeback debt is attributable to this exact allocation.
			// Keep the source top-up as OriginOperationID for audit, while the
			// nullable AllocationID prevents one charge's cancellation from
			// consuming another charge's repayment. New rows are never pooled.
			allocationID := allocations[i].ID
			debt, debtErr := lockAgencyAllocationDebtTx(tx, allocations[i], input.SourceOperationID, "payment_chargeback")
			if errors.Is(debtErr, gorm.ErrRecordNotFound) {
				if allocations[i].RevokedReservedDebt+allocations[i].RevokedNonpaid != take {
					// Earlier revoked liability has no authoritative debt row. Do
					// not conceal the missing provenance by creating a fresh one.
					return nil, false, ErrAgencyComponentRefundProvenance
				}
				debt = AgencyFundingDebt{UserID: input.UserID, OriginOperationID: input.SourceOperationID, DebtKind: "payment_chargeback", AllocationID: &allocationID, CreatedAtMS: time.Now().UnixMilli()}
				if err := tx.Create(&debt).Error; err != nil {
					return nil, false, err
				}
			} else if debtErr != nil {
				return nil, false, debtErr
			}
			if debt.OriginalQuota < 0 || debt.ReversedQuota < 0 || debt.ReversedQuota > debt.OriginalQuota ||
				debt.OutstandingQuota < 0 || debt.OutstandingQuota > debt.OriginalQuota-debt.ReversedQuota ||
				debt.OriginalQuota > int64(common.MaxQuota)-take || debt.OriginalQuota-debt.ReversedQuota != allocations[i].DebtConsumed-take {
				return nil, false, ErrAgencyComponentRefundProvenance
			}
			debt.OriginalQuota += take
			debt.OutstandingQuota += take
			if err := tx.Model(&debt).Updates(map[string]any{"original_quota": debt.OriginalQuota, "outstanding_quota": debt.OutstandingQuota}).Error; err != nil {
				return nil, false, err
			}
			if takePaid > 0 {
				charges = append(charges, AgencyFundingReversalCharge{ChargeID: allocations[i].ChargeID, Quota: takePaid})
			}
			consumedRevoked += take
			remaining -= take
		}
	}
	if remaining != 0 {
		return nil, false, errors.New("funding reversal source is already fully reversed or unavailable")
	}
	charges = compactAgencyFundingReversalCharges(charges)
	if availableRevoked > account.PaidAvailable {
		return nil, false, errors.New("funding reversal paid balance underflow")
	}
	totalDebt := account.DebtQuota + consumedRevoked
	if totalDebt < account.DebtQuota {
		return nil, false, errors.New("funding reversal debt overflow")
	}
	// A payment reversal removes the credited wallet amount, even if the
	// customer has already spent it. This deliberately allows quota to become
	// negative and records the consumed portion in debt.
	if err := tx.Model(&User{}).Where("id = ?", input.UserID).
		Update("quota", gorm.Expr("quota - ?", input.Quota)).Error; err != nil {
		return nil, false, err
	}
	seq := account.MoneySeq + 1
	if seq <= account.MoneySeq {
		return nil, false, errors.New("funding reversal sequence overflow")
	}
	paidAfter := account.PaidAvailable - availableRevoked
	nonpaidAfter := account.NonpaidAvailable - nonpaidRevoked
	if nonpaidAfter < 0 {
		return nil, false, errors.New("funding reversal nonpaid balance underflow")
	}
	if err := tx.Model(&account).Updates(map[string]any{
		"paid_available":    paidAfter,
		"nonpaid_available": nonpaidAfter,
		"debt_quota":        totalDebt,
		"money_seq":         seq,
		"version":           account.Version + 1,
		"updated_at":        time.Now().Unix(),
	}).Error; err != nil {
		return nil, false, err
	}
	now := time.Now().UnixMilli()
	if err := tx.Model(&topup).Updates(map[string]any{
		"refunded_quota": topup.RefundedQuota + input.Quota,
		"payment_status": func() string {
			if topup.RefundedQuota+input.Quota == topup.CreditedQuota {
				return "refunded"
			}
			return "partially_refunded"
		}(),
	}).Error; err != nil {
		return nil, false, err
	}
	if err := tx.Create(&AgencyFundingReversal{
		RefundID: input.RefundID, SourceOperationID: input.SourceOperationID,
		UserID: input.UserID, Quota: input.Quota, CurrencyCode: input.CurrencyCode,
		PaymentReference: input.PaymentReference, EvidenceRef: input.EvidenceRef,
		Reason: input.Reason, CreatedAtMS: now,
	}).Error; err != nil {
		return nil, false, err
	}
	for _, charge := range charges {
		if strings.TrimSpace(charge.ChargeID) == "" || charge.Quota <= 0 {
			continue
		}
		if err := tx.Create(&AgencyFundingReversalChargeRecord{RefundID: input.RefundID, ChargeID: charge.ChargeID, Quota: charge.Quota}).Error; err != nil {
			return nil, false, err
		}
	}
	operationID := fundingReversalOperationID(input.RefundID)
	if err := tx.Create(&AgencyFundingLedger{
		OperationID: operationID, EntryNo: 0, UserID: input.UserID,
		MoneySeq: seq, SourceKind: "funding_reverse", PaidDelta: -availableRevoked,
		NonpaidDelta: -nonpaidRevoked, DebtDelta: consumedRevoked, PaidAfter: paidAfter, NonpaidAfter: nonpaidAfter,
		DebtAfter: totalDebt, CurrencyCode: input.CurrencyCode, CreatedAtMS: now,
	}).Error; err != nil {
		return nil, false, err
	}
	// Funding reversals are immutable financial facts too.  The event is
	// written in this same transaction as the wallet/debt changes, so a
	// committed reversal can never be missing its outbox delivery.
	eventID := fundingReversalEventID(input.RefundID)
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: eventID,
		EventType: "agency.funding_reversed", OriginalEventID: "agency-topup-" + input.SourceOperationID,
		FinancialChargeID: input.SourceOperationID, OperationID: operationID,
		JournalRevision: 1, MoneySeq: seq, EventCount: 1, OccurredAtMS: now,
		UserID: input.UserID, AgencyID: topup.AgencyID, BindingID: topup.BindingID,
		BusinessStatus: input.Reason, BillingStatus: "funding_reversed",
		CurrencyCode: input.CurrencyCode, CommissionEligible: false,
		CommissionSkipReason: "funding_reversal",
		ChargedTotalQuota:    input.Quota, StandardQuota: input.Quota,
	}
	payload, err := common.Marshal(event)
	if err != nil {
		return nil, false, err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Create(&AgencyBillingOutbox{
		EventID: eventID, OperationID: event.OperationID, EventIndex: 0, EventCount: 1,
		EventKind: event.EventType, UserID: input.UserID, MoneySeq: seq,
		Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion,
		CreatedAtMS: now,
	}).Error; err != nil {
		return nil, false, err
	}
	if err := tx.Create(&AgencyEventDelivery{EventID: eventID, Status: "pending", NextRetryAt: time.Now().Unix(), CreatedAt: time.Now().Unix()}).Error; err != nil {
		return nil, false, err
	}
	return charges, true, nil
}

func fundingReversalOperationID(refundID string) string {
	const prefix = "funding-reverse-"
	refundID = strings.TrimSpace(refundID)
	if len(prefix)+len(refundID) <= 128 {
		return prefix + refundID
	}
	sum := sha256.Sum256([]byte(refundID))
	return prefix + "sha256-" + hex.EncodeToString(sum[:])
}

func fundingReversalEventID(refundID string) string {
	const prefix = "agency-funding-reversal-"
	refundID = strings.TrimSpace(refundID)
	if len(prefix)+len(refundID) <= 128 {
		return prefix + refundID
	}
	sum := sha256.Sum256([]byte(refundID))
	return prefix + "sha256-" + hex.EncodeToString(sum[:])
}

func loadAgencyFundingReversalChargesTx(tx *gorm.DB, refundID string) ([]AgencyFundingReversalCharge, error) {
	var rows []AgencyFundingReversalChargeRecord
	if err := tx.Where("refund_id = ?", refundID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	charges := make([]AgencyFundingReversalCharge, 0, len(rows))
	for _, row := range rows {
		charges = append(charges, AgencyFundingReversalCharge{ChargeID: row.ChargeID, Quota: row.Quota})
	}
	return charges, nil
}

func compactAgencyFundingReversalCharges(charges []AgencyFundingReversalCharge) []AgencyFundingReversalCharge {
	if len(charges) <= 1 {
		return charges
	}
	positions := make(map[string]int, len(charges))
	compacted := make([]AgencyFundingReversalCharge, 0, len(charges))
	for _, charge := range charges {
		if strings.TrimSpace(charge.ChargeID) == "" || charge.Quota <= 0 {
			continue
		}
		if index, ok := positions[charge.ChargeID]; ok {
			compacted[index].Quota += charge.Quota
			continue
		}
		positions[charge.ChargeID] = len(compacted)
		compacted = append(compacted, charge)
	}
	return compacted
}

// agencyFundingSourcePriority is the single source of truth for the order in
// which a durable wallet is consumed. Redemption codes are the highest
// priority, administrator grants are next, and paid wallet lots (including a
// user checkout or an administrator-assisted checkout) are last.
func agencyFundingSourcePriority(sourceKind string) int {
	switch strings.ToLower(strings.TrimSpace(sourceKind)) {
	case "redemption", "redeem", "redemption_code":
		return 0
	case "admin", "admin_grant", "admin_adjustment", "admin_override", "quota_grant", "quota_debit", "batch_quota", "other_grant", "checkin", "affiliate_transfer", "legacy_migration", "legacy_unknown":
		return 1
	default:
		return 2
	}
}

func agencyFundingLotIsLegacyUnknown(lot AgencyFundingLot) bool {
	switch strings.ToLower(strings.TrimSpace(lot.SourceKind)) {
	case "legacy_migration", "legacy_unknown":
		return true
	default:
		return false
	}
}

func agencyFundingRuleVersion(snapshot *agencycontract.PricingSnapshot) string {
	if snapshot == nil || strings.TrimSpace(snapshot.FundingRuleVersion) == "" {
		return agencyFundingRuleLegacyUnversioned
	}
	if snapshot.FundingRuleVersion == agencycontract.FundingRuleVersionV2 {
		return agencycontract.FundingRuleVersionV2
	}
	if snapshot.FundingRuleVersion == agencycontract.FundingRuleVersionV1 {
		return agencycontract.FundingRuleVersionV1
	}
	return agencyFundingRuleLegacyUnversioned
}

// ReserveAgencyFunding mirrors an already completed wallet reservation into
// the typed funding ledger. amount is the target cumulative reservation for
// chargeID (not an increment), which makes retries and repeated settlement
// calls idempotent. It returns the newly allocated portion attributable to
// paid lots; the remainder is non-paid or debt and cannot earn commission.
func ReserveAgencyFunding(userID int64, chargeID string, amount int64) (int64, error) {
	paid, _, err := ReserveAgencyFundingWithSequence(userID, chargeID, amount)
	return paid, err
}

// ReserveAgencyFundingWithSequence mirrors a completed wallet reservation and
// returns the authoritative per-user money sequence from the same transaction.
// Non-durable users return sequence zero and preserve the legacy no-op behavior.
func ReserveAgencyFundingWithSequence(userID int64, chargeID string, amount int64) (int64, int64, error) {
	var paid, moneySeq int64
	err := runAgencyFundingTransaction(func(tx *gorm.DB) error {
		var err error
		paid, err = reserveAgencyFundingTx(tx, userID, chargeID, amount)
		if err != nil {
			return err
		}
		var user User
		if err := tx.Select("billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		if user.BillingMode != AgencyDurableBillingMode {
			return nil
		}
		var account AgencyFundingAccount
		if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
			return err
		}
		moneySeq = account.MoneySeq
		return nil
	})
	return paid, moneySeq, err
}

// ReserveAgencyFundingTx is the transaction-scoped variant used by task
// reconciliation, where the wallet/token/funding updates must commit together.
func ReserveAgencyFundingTx(tx *gorm.DB, userID int64, chargeID string, amount int64) (int64, error) {
	return reserveAgencyFundingTx(tx, userID, chargeID, amount)
}

func reserveAgencyFundingTx(tx *gorm.DB, userID int64, chargeID string, amount int64) (int64, error) {
	// Calls without an accepted pricing snapshot predate the versioned quote
	// path. Preserve their historical bonus-first behavior; new gateway calls
	// pass their frozen rule explicitly.
	return reserveAgencyFundingTxWithRule(tx, userID, chargeID, amount, agencyFundingRuleLegacyUnversioned)
}

func reserveAgencyFundingTxWithRule(tx *gorm.DB, userID int64, chargeID string, amount int64, fundingRuleVersion string) (int64, error) {
	if userID <= 0 || strings.TrimSpace(chargeID) == "" || amount < 0 {
		return 0, ErrAgencyFundingUnavailable
	}
	var paid int64
	err := func() error {
		var user User
		if err := AgencyLockForUpdate(tx).Select("id, billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		if user.BillingMode != AgencyDurableBillingMode {
			return nil
		}
		if err := EnsureAgencyFundingAccount(tx, userID); err != nil {
			return err
		}
		if _, err := expireAgencyRedemptionLotsTx(tx, userID, common.GetTimestamp()); err != nil {
			return err
		}
		// Lock the funding account row before the idempotency check: this
		// serializes every reserve for the same user (so the plain read below
		// is a safe replay check) and avoids InnoDB next-key/gap locks on the
		// global idx_agency_alloc_charge index tail. Charge ids are
		// time-prefixed, so a FOR UPDATE existence scan always lands on the
		// index supremum whose shared gap lock deadlocks concurrent inserts
		// (MySQL 1213) across different users.
		var account AgencyFundingAccount
		if err := AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&account).Error; err != nil {
			return err
		}
		var existing []AgencyFundingAllocation
		if err := tx.Where("charge_id = ? AND (reserved > 0 OR consumed > 0 OR nonpaid_consumed > 0 OR debt_consumed > 0)", chargeID).Find(&existing).Error; err != nil {
			return err
		}
		var allocated int64
		for _, row := range existing {
			// The reservation target covers every funding component. In
			// particular, non-paid and debt segments have Consumed == 0;
			// omitting them makes a retry allocate the same target again.
			componentTotal, err := agencyFundingAllocationActiveTotal(row)
			if err != nil {
				return err
			}
			if allocated > int64(^uint64(0)>>1)-componentTotal {
				return errors.New("agency funding allocation overflow")
			}
			allocated += componentTotal
		}
		if amount <= allocated {
			return nil
		}
		amount -= allocated
		remaining := amount
		var lots []AgencyFundingLot
		if err := AgencyLockForUpdate(tx).Where("user_id = ? AND (paid_available > 0 OR bonus_available > 0)", userID).Find(&lots).Error; err != nil {
			return err
		}
		// The business rule is explicit: redemption credit first, then
		// administrator grants, then paid wallet credit. Keep the ordering in
		// Go instead of a dialect-specific CASE expression so SQLite, MySQL,
		// and PostgreSQL share identical behavior.
		sort.SliceStable(lots, func(i, j int) bool {
			left, right := agencyFundingSourcePriority(lots[i].SourceKind), agencyFundingSourcePriority(lots[j].SourceKind)
			if left != right {
				return left < right
			}
			if left == 0 && lots[i].ExpiresAt != lots[j].ExpiresAt {
				if lots[i].ExpiresAt == 0 {
					return false
				}
				if lots[j].ExpiresAt == 0 {
					return true
				}
				return lots[i].ExpiresAt < lots[j].ExpiresAt
			}
			if lots[i].MoneySeq != lots[j].MoneySeq {
				return lots[i].MoneySeq < lots[j].MoneySeq
			}
			return lots[i].ID < lots[j].ID
		})
		segmentNo := 0
		for _, row := range existing {
			if row.SegmentNo >= segmentNo {
				segmentNo = row.SegmentNo + 1
			}
		}
		paid = 0
		nonpaid := account.NonpaidAvailable
		nonpaidTake := int64(0)
		paidTake := int64(0)
		legacyPaidTake := int64(0)
		consumeBonus := func(lot *AgencyFundingLot) error {
			if remaining == 0 || lot.BonusAvailable <= 0 {
				return nil
			}
			take := lot.BonusAvailable
			if take > remaining {
				take = remaining
			}
			if take > nonpaid {
				return errors.New("agency funding nonpaid balance underflow")
			}
			lot.BonusAvailable -= take
			lot.BonusConsumed += take
			lot.Version++
			if err := tx.Model(lot).Updates(map[string]any{"bonus_available": lot.BonusAvailable, "bonus_consumed": lot.BonusConsumed, "version": lot.Version}).Error; err != nil {
				return err
			}
			row := AgencyFundingAllocation{ChargeID: chargeID, SegmentNo: segmentNo, ComponentID: "default", UserID: userID, LotID: lot.ID, SourceSnapshotJSON: lot.SourceSnapshotJSON, NonpaidConsumed: take, Version: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			nonpaid -= take
			remaining -= take
			segmentNo++
			return nil
		}
		consumeLegacyUnknown := func(lot *AgencyFundingLot) error {
			if remaining == 0 || lot.PaidAvailable <= 0 {
				return nil
			}
			take := lot.PaidAvailable
			if take > remaining {
				take = remaining
			}
			if take > account.PaidAvailable-legacyPaidTake {
				return errors.New("agency funding legacy balance underflow")
			}
			lot.PaidAvailable -= take
			lot.PaidConsumed += take
			lot.Version++
			if err := tx.Model(lot).Updates(map[string]any{"paid_available": lot.PaidAvailable, "paid_consumed": lot.PaidConsumed, "version": lot.Version}).Error; err != nil {
				return err
			}
			// Legacy opening balances are historical paid quota without a
			// verifiable payment source. They remain spendable, but must stay
			// in the paid component so account aggregates, refunds, and reports
			// do not turn them into a fabricated non-paid grant.
			row := AgencyFundingAllocation{ChargeID: chargeID, SegmentNo: segmentNo, ComponentID: "default", UserID: userID, LotID: lot.ID, SourceSnapshotJSON: lot.SourceSnapshotJSON, Consumed: take, Version: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			legacyPaidTake += take
			remaining -= take
			segmentNo++
			return nil
		}
		consumePaid := func(lot *AgencyFundingLot) error {
			if remaining == 0 || lot.PaidAvailable <= 0 {
				return nil
			}
			take := lot.PaidAvailable
			if take > remaining {
				take = remaining
			}
			lot.PaidAvailable -= take
			lot.PaidConsumed += take
			lot.Version++
			if err := tx.Model(lot).Updates(map[string]any{"paid_available": lot.PaidAvailable, "paid_consumed": lot.PaidConsumed, "version": lot.Version}).Error; err != nil {
				return err
			}
			row := AgencyFundingAllocation{ChargeID: chargeID, SegmentNo: segmentNo, ComponentID: "default", UserID: userID, LotID: lot.ID, SourceSnapshotJSON: lot.SourceSnapshotJSON, Consumed: take, Version: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			paid += take
			paidTake += take
			remaining -= take
			segmentNo++
			return nil
		}
		if fundingRuleVersion == agencycontract.FundingRuleVersionV2 {
			// New charges consume redemption/admin/other non-paid sources first,
			// then verified paid lots. The lot sort above makes the FIFO order
			// deterministic within each source class.
			for i := range lots {
				if remaining == 0 {
					break
				}
				if err := consumeBonus(&lots[i]); err != nil {
					return err
				}
				if agencyFundingLotIsLegacyUnknown(lots[i]) {
					if err := consumeLegacyUnknown(&lots[i]); err != nil {
						return err
					}
				}
			}
			for i := range lots {
				if remaining == 0 {
					break
				}
				if agencyFundingLotIsLegacyUnknown(lots[i]) {
					continue
				}
				if err := consumePaid(&lots[i]); err != nil {
					return err
				}
			}
		} else if fundingRuleVersion == agencycontract.FundingRuleVersionV1 {
			// Old callers and accepted v1 snapshots retain the original
			// paid-first behavior. Legacy opening balances are part of the paid
			// wallet aggregate, but consumeLegacyUnknown deliberately excludes
			// them from the commissionable paid return value.
			sort.SliceStable(lots, func(i, j int) bool {
				if lots[i].MoneySeq != lots[j].MoneySeq {
					return lots[i].MoneySeq < lots[j].MoneySeq
				}
				return lots[i].ID < lots[j].ID
			})
			for i := range lots {
				if remaining == 0 {
					break
				}
				if agencyFundingLotIsLegacyUnknown(lots[i]) {
					if err := consumeLegacyUnknown(&lots[i]); err != nil {
						return err
					}
				} else if err := consumePaid(&lots[i]); err != nil {
					return err
				}
			}
			for i := range lots {
				if remaining == 0 {
					break
				}
				if err := consumeBonus(&lots[i]); err != nil {
					return err
				}
			}
		} else {
			// Journals created before funding-rule versioning retain the
			// historical bonus-first behavior. They cannot be reclassified as
			// either explicit v1 or v2 after acceptance.
			for i := range lots {
				if remaining == 0 {
					break
				}
				if err := consumeBonus(&lots[i]); err != nil {
					return err
				}
				if agencyFundingLotIsLegacyUnknown(lots[i]) {
					if err := consumeLegacyUnknown(&lots[i]); err != nil {
						return err
					}
				}
			}
			for i := range lots {
				if remaining == 0 {
					break
				}
				if agencyFundingLotIsLegacyUnknown(lots[i]) {
					continue
				}
				if err := consumePaid(&lots[i]); err != nil {
					return err
				}
			}
		}
		// The user wallet has already moved by amount. Remove the paid portion
		// from the durable available mirror; any remainder is bonus/debt.
		debtDelta := int64(0)
		uncovered := remaining
		if uncovered > 0 {
			// Older allocations did not retain a bonus lot ID. Keep the
			// aggregate fallback for migrated rows where no lot can be found.
			if uncovered > 0 && nonpaid > 0 {
				take := uncovered
				if take > nonpaid {
					take = nonpaid
				}
				nonpaid -= take
				uncovered -= take
				nonpaidTake += take
				if take > 0 {
					row := AgencyFundingAllocation{ChargeID: chargeID, SegmentNo: segmentNo, ComponentID: "default", UserID: userID, NonpaidConsumed: take, Version: 1}
					if err := tx.Create(&row).Error; err != nil {
						return err
					}
					segmentNo++
				}
			}
			debtDelta = uncovered
		}
		if debtDelta > 0 {
			row := AgencyFundingAllocation{ChargeID: chargeID, SegmentNo: segmentNo, ComponentID: "default", UserID: userID, DebtConsumed: debtDelta, Version: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			// Retain an exact origin for subsequent repayments and model refunds.
			// The allocation ID is stable even when a finalized charge is split
			// into several independently refundable components.
			allocationID := row.ID
			if err := tx.Create(&AgencyFundingDebt{UserID: userID,
				OriginOperationID: fmt.Sprintf("allocation-%d", row.ID), DebtKind: "model_charge", AllocationID: &allocationID,
				OriginalQuota: debtDelta, OutstandingQuota: debtDelta, CreatedAtMS: time.Now().UnixMilli()}).Error; err != nil {
				return err
			}
		}
		debt := account.DebtQuota + debtDelta
		seq := account.MoneySeq + 1
		return tx.Model(&account).Updates(map[string]any{"paid_available": account.PaidAvailable - paidTake - legacyPaidTake, "nonpaid_available": nonpaid, "debt_quota": debt, "money_seq": seq, "version": account.Version + 1, "updated_at": time.Now().Unix()}).Error
	}()
	return paid, err
}

// ReleaseAgencyFunding returns a previously consumed paid allocation to its
// original lots. It is used when a pre-consume is settled below its estimate
// or a request is refunded before finalization.
func ReleaseAgencyFunding(chargeID string, amount int64) (int64, error) {
	if strings.TrimSpace(chargeID) == "" || amount < 0 {
		return 0, ErrAgencyFundingUnavailable
	}
	var released int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		released, err = releaseAgencyFundingTx(tx, chargeID, amount)
		return err
	})
	return released, err
}

// ReleaseAgencyFundingTx is the transaction-scoped counterpart used by
// reconciliation and other atomic wallet settlement paths.
func ReleaseAgencyFundingTx(tx *gorm.DB, chargeID string, amount int64) (int64, error) {
	return releaseAgencyFundingTx(tx, chargeID, amount)
}

func releaseAgencyFundingTx(tx *gorm.DB, chargeID string, amount int64) (int64, error) {
	result, err := releaseAgencyFundingDetailedTx(tx, chargeID, amount)
	return result.PaidReleased, err
}

type agencyFundingReleaseResult struct {
	PaidReleased   int64
	ReleasedTotal  int64
	ExpiredNonpaid int64
}

func releaseAgencyFundingDetailedTx(tx *gorm.DB, chargeID string, amount int64) (agencyFundingReleaseResult, error) {
	if strings.TrimSpace(chargeID) == "" || amount < 0 {
		return agencyFundingReleaseResult{}, ErrAgencyFundingUnavailable
	}
	var componentCount int64
	if err := tx.Model(&AgencyChargeComponent{}).Where("charge_id = ?", chargeID).Count(&componentCount).Error; err != nil {
		return agencyFundingReleaseResult{}, err
	}
	if componentCount > 0 {
		// Component finalizations must restore their persisted proportional
		// matrix and commission in one command. A legacy post-task callback
		// must fail before its wallet transaction commits any source mutation.
		return agencyFundingReleaseResult{}, ErrAgencyComponentRefundProvenance
	}
	result := agencyFundingReleaseResult{}
	err := func() error {
		var rows []AgencyFundingAllocation
		if err := AgencyLockForUpdate(tx).Where("charge_id = ? AND (reserved > 0 OR consumed > 0 OR nonpaid_consumed > 0 OR debt_consumed > 0)", chargeID).Order("id DESC").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var account AgencyFundingAccount
		if err := AgencyLockForUpdate(tx).Where("user_id = ?", rows[0].UserID).First(&account).Error; err != nil {
			return err
		}
		remaining := amount
		restoredNonpaid := int64(0)
		restoredDebt := int64(0)
		restoredDebtPaid := int64(0)
		restoredLegacyPaid := int64(0)
		restoredExpiredNonpaid := int64(0)
		var restoredSources []agencyFundingSource
		for _, row := range rows {
			if remaining == 0 {
				break
			}
			effectiveConsumed, effectiveNonpaid, effectiveDebt, err := agencyFundingAllocationActiveParts(row)
			if err != nil {
				return err
			}
			rowTotal, err := agencyFundingAllocationActiveTotal(row)
			if err != nil {
				return err
			}
			take := rowTotal
			if take > remaining {
				take = remaining
			}
			left := take
			debtTake := effectiveDebt
			if debtTake > left {
				debtTake = left
			}
			left -= debtTake
			if debtTake > 0 {
				sources, unpaid, expiredNonpaid, err := restoreAgencyAllocationDebtTx(tx, row, debtTake, true)
				if err != nil {
					return err
				}
				for _, source := range sources {
					restoredDebtPaid += source.Paid
					restoredNonpaid += source.Nonpaid
				}
				restoredSources = append(restoredSources, sources...)
				restoredDebt += unpaid
				restoredExpiredNonpaid += expiredNonpaid
			}
			nonpaidTake := effectiveNonpaid
			if nonpaidTake > left {
				nonpaidTake = left
			}
			left -= nonpaidTake
			paidTake := effectiveConsumed
			if paidTake > left {
				paidTake = left
			}
			if paidTake > 0 {
				var lot AgencyFundingLot
				if err := AgencyLockForUpdate(tx).First(&lot, row.LotID).Error; err != nil {
					return err
				}
				switch {
				case lot.PaidConsumed >= paidTake && row.Consumed >= paidTake:
					lot.PaidConsumed -= paidTake
					row.Consumed -= paidTake
				case lot.PaidReserved >= paidTake && row.Reserved >= paidTake:
					lot.PaidReserved -= paidTake
					row.Reserved -= paidTake
				default:
					return errors.New("agency funding allocation underflow")
				}
				lot.PaidAvailable += paidTake
				lot.Version++
				if err := tx.Model(&lot).Updates(map[string]any{"paid_consumed": lot.PaidConsumed, "paid_reserved": lot.PaidReserved, "paid_available": lot.PaidAvailable, "version": lot.Version}).Error; err != nil {
					return err
				}
			}
			validNonpaidTake := nonpaidTake
			if nonpaidTake > 0 && row.LotID > 0 {
				var lot AgencyFundingLot
				if err := AgencyLockForUpdate(tx).First(&lot, row.LotID).Error; err != nil {
					return err
				}
				if agencyFundingLotIsLegacyUnknown(lot) {
					if lot.PaidConsumed < nonpaidTake {
						return errors.New("agency funding legacy allocation underflow")
					}
					lot.PaidConsumed -= nonpaidTake
					lot.PaidAvailable += nonpaidTake
					restoredLegacyPaid += nonpaidTake
				} else {
					expired, err := restoreAgencyBonusTx(tx, &lot, nonpaidTake, false, common.GetTimestamp())
					if err != nil {
						return err
					}
					if expired {
						restoredExpiredNonpaid += nonpaidTake
						validNonpaidTake = 0
					}
				}
				if agencyFundingLotIsLegacyUnknown(lot) {
					lot.Version++
					if err := tx.Model(&lot).Updates(map[string]any{"version": lot.Version, "paid_consumed": lot.PaidConsumed, "paid_available": lot.PaidAvailable}).Error; err != nil {
						return err
					}
				}
			}
			row.NonpaidConsumed -= nonpaidTake
			if paidTake+validNonpaidTake > 0 {
				restoredSources = append(restoredSources, agencyFundingSource{LotID: row.LotID, Paid: paidTake, Nonpaid: validNonpaidTake})
			}
			row.DebtConsumed -= debtTake
			row.Released += take
			row.Version++
			if err := tx.Model(&row).Updates(map[string]any{"consumed": row.Consumed, "nonpaid_consumed": row.NonpaidConsumed, "debt_consumed": row.DebtConsumed, "released": row.Released, "version": row.Version}).Error; err != nil {
				return err
			}
			result.PaidReleased += paidTake
			restoredNonpaid += validNonpaidTake
			remaining -= take
		}
		result.ReleasedTotal = amount - remaining
		result.ExpiredNonpaid = restoredExpiredNonpaid
		if result.PaidReleased == 0 && restoredNonpaid == 0 && restoredDebt == 0 && restoredDebtPaid == 0 && restoredExpiredNonpaid == 0 {
			return nil
		}
		seq := account.MoneySeq + 1
		newDebt := account.DebtQuota - restoredDebt
		if newDebt < 0 {
			return errors.New("agency funding debt underflow")
		}
		paidRepaid, nonpaidRepaid, err := repayAgencyFundingSourcesTx(tx, account.UserID, fmt.Sprintf("release:%s:%d", chargeID, seq), seq, newDebt, restoredSources)
		if err != nil {
			return err
		}
		return tx.Model(&account).Updates(map[string]any{"paid_available": account.PaidAvailable + result.PaidReleased + restoredDebtPaid + restoredLegacyPaid - paidRepaid, "nonpaid_available": account.NonpaidAvailable + restoredNonpaid - nonpaidRepaid, "debt_quota": newDebt - paidRepaid - nonpaidRepaid, "money_seq": seq, "version": account.Version + 1, "updated_at": time.Now().Unix()}).Error
	}()
	return result, err
}
