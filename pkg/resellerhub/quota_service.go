package resellerhub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	ledgerStatusPrepared          = "prepared"
	ledgerStatusQuotaApplied      = "quota_applied"
	ledgerStatusReconcileRequired = "reconcile_required"
	ledgerStatusApplied           = "applied"
	ledgerStatusFailed            = "failed"
)

type quotaAdjustmentRequest struct {
	Mode            string `json:"mode"`
	InputUnit       string `json:"input_unit"`
	Amount          string `json:"amount"`
	Reason          string `json:"reason"`
	IdempotencyKey  string `json:"idempotency_key"`
	ConfigVersion   string `json:"config_version"`
	ReversesEventID string `json:"reverses_event_id"`
}

func (a *App) decodeQuotaRequest(c *gin.Context, fundingOnly bool) (quotaAdjustmentRequest, CurrencyConfig, error) {
	var input quotaAdjustmentRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		return input, CurrencyConfig{}, errors.New("invalid request body")
	}
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	input.InputUnit = strings.ToLower(strings.TrimSpace(input.InputUnit))
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ReversesEventID = strings.TrimSpace(input.ReversesEventID)
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	}
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 {
		return input, CurrencyConfig{}, errors.New("valid idempotency_key is required")
	}
	if input.Reason == "" || len(input.Reason) > 512 {
		return input, CurrencyConfig{}, errors.New("reason is required")
	}
	if fundingOnly {
		if input.Mode != "add" {
			return input, CurrencyConfig{}, errors.New("funding adjustment only supports add")
		}
	} else if input.Mode != "add" && input.Mode != "subtract" {
		return input, CurrencyConfig{}, errors.New("mode must be add or subtract")
	}
	config, err := a.fetchCurrencyConfig(c.Request.Context())
	if err != nil {
		return input, CurrencyConfig{}, err
	}
	if input.ConfigVersion != "" && input.ConfigVersion != config.Version {
		return input, config, errors.New("currency configuration changed; refresh and confirm again")
	}
	return input, config, nil
}

func (a *App) adjustFunding(c *gin.Context) {
	resellerID, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	var reseller Reseller
	if err := a.db.First(&reseller, resellerID).Error; err != nil {
		respondError(c, http.StatusNotFound, "reseller not found")
		return
	}
	if err := a.ensureQuotaWritesConverged(c.Request.Context(), reseller.ID); err != nil {
		respondError(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	input, currency, err := a.decodeQuotaRequest(c, true)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	quota, displayAmount, err := quotaFromInput(input.InputUnit, input.Amount, currency, 10000)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	ledger, duplicate, err := a.applyUserQuotaAdjustment(c, reseller, input, currency, quota, displayAmount)
	if err != nil {
		respondError(c, http.StatusConflict, err.Error())
		return
	}
	respondOK(c, gin.H{"ledger": ledger, "duplicate": duplicate, "currency_config": currencyView(currency)})
}

func (a *App) adjustCustomerQuota(c *gin.Context) {
	customerID, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	customer, ok := a.customerForRequest(c, customerID)
	if !ok {
		return
	}
	if customer.ActiveTokenMappingID == nil {
		respondError(c, http.StatusConflict, "customer has no active token")
		return
	}
	var mapping CustomerToken
	if err := a.db.Where("id = ? AND reseller_id = ? AND customer_id = ?", *customer.ActiveTokenMappingID, customer.ResellerID, customer.ID).First(&mapping).Error; err != nil {
		respondError(c, http.StatusConflict, "active token mapping is invalid")
		return
	}
	if mapping.Status != "active" {
		respondError(c, http.StatusConflict, "token is not active")
		return
	}
	if err := a.ensureQuotaWritesConverged(c.Request.Context(), customer.ResellerID); err != nil {
		respondError(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	input, currency, err := a.decodeQuotaRequest(c, false)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	discount, _, err := a.effectiveDiscount(customer)
	if err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	quota, displayAmount, err := quotaFromInput(input.InputUnit, input.Amount, currency, discount)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	ledger, duplicate, err := a.applyTokenQuotaAdjustment(c, customer, mapping, input, currency, discount, quota, displayAmount)
	if err != nil {
		respondError(c, http.StatusConflict, err.Error())
		return
	}
	respondOK(c, gin.H{"ledger": ledger, "duplicate": duplicate, "currency_config": currencyView(currency)})
}

func newEventID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func (a *App) ensureQuotaWritesConverged(ctx context.Context, resellerID int) error {
	cutoff := time.Now().Add(-a.config.ConsistencyGrace).Unix()
	var count int64
	err := a.db.WithContext(ctx).Model(&QuotaLedger{}).
		Where("reseller_id = ? AND status IN ? AND created_at <= ?", resellerID, []string{ledgerStatusPrepared, ledgerStatusReconcileRequired}, cutoff).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return errors.New("quota writes are paused while earlier adjustments require reconciliation")
	}
	return nil
}

func (a *App) existingLedger(resellerID int, key string) (*QuotaLedger, error) {
	var ledger QuotaLedger
	err := a.db.Where("reseller_id = ? AND idempotency_key = ?", resellerID, key).First(&ledger).Error
	return &ledger, err
}

func validateDuplicateLedger(existing *QuotaLedger, customerID *int, targetType string, targetID int, input quotaAdjustmentRequest, quota int) error {
	if existing.Operation != input.Mode || existing.TargetType != targetType || existing.RequestedQuota != quota {
		return errors.New("idempotency_key was already used for a different quota adjustment")
	}
	if (existing.CustomerID == nil) != (customerID == nil) || (customerID != nil && *existing.CustomerID != *customerID) {
		return errors.New("idempotency_key was already used for a different customer")
	}
	if targetType == "user_quota" && existing.NewAPIUserID != targetID {
		return errors.New("idempotency_key was already used for a different carrier account")
	}
	if targetType == "token_quota" && (existing.NewAPITokenID == nil || *existing.NewAPITokenID != targetID) {
		return errors.New("idempotency_key was already used for a different token")
	}
	return nil
}

func baseLedger(c *gin.Context, resellerID int, customerID *int, input quotaAdjustmentRequest, currency CurrencyConfig, discount, quota int, displayAmount decimal.Decimal) QuotaLedger {
	identity := currentIdentity(c)
	ledger := QuotaLedger{
		EventID:                   newEventID(),
		IdempotencyKey:            input.IdempotencyKey,
		ResellerID:                resellerID,
		CustomerID:                customerID,
		Operation:                 input.Mode,
		RequestedQuota:            quota,
		InputUnit:                 input.InputUnit,
		InputAmountDecimal:        input.Amount,
		CurrencyTypeSnapshot:      currency.DisplayType,
		CurrencySymbolSnapshot:    currency.Symbol,
		QuotaPerUnitSnapshot:      currency.QuotaPerUnit.String(),
		USDToCurrencyRateSnapshot: currency.USDToDisplayRate.String(),
		DiscountBPSSnapshot:       discount,
		Status:                    ledgerStatusPrepared,
		Reason:                    input.Reason,
		ActorUserID:               identity.NewAPIUserID,
		RequestID:                 requestID(c),
		CreatedAt:                 time.Now().Unix(),
	}
	if input.ReversesEventID != "" {
		ledger.ReversesEventID = &input.ReversesEventID
	}
	return ledger
}

func markReversedLedger(tx *gorm.DB, ledger *QuotaLedger) error {
	if ledger.ReversesEventID == nil {
		return nil
	}
	var original QuotaLedger
	if err := tx.Where("reseller_id = ? AND event_id = ?", ledger.ResellerID, *ledger.ReversesEventID).First(&original).Error; err != nil {
		return errors.New("reversed quota event was not found")
	}
	if original.Status != ledgerStatusApplied || original.TargetType != ledger.TargetType || original.NewAPIUserID != ledger.NewAPIUserID || original.QuotaDelta != -ledger.QuotaDelta {
		return errors.New("quota reversal must target an applied event with the same target and opposite amount")
	}
	if (original.CustomerID == nil) != (ledger.CustomerID == nil) || (ledger.CustomerID != nil && *original.CustomerID != *ledger.CustomerID) {
		return errors.New("quota reversal customer does not match")
	}
	if (original.NewAPITokenID == nil) != (ledger.NewAPITokenID == nil) || (ledger.NewAPITokenID != nil && *original.NewAPITokenID != *ledger.NewAPITokenID) {
		return errors.New("quota reversal token does not match")
	}
	result := tx.Model(&QuotaLedger{}).Where("id = ? AND status = ?", original.ID, ledgerStatusApplied).Update("status", QuotaLedgerStatusReversed)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("quota event was already reversed")
	}
	return nil
}

func (a *App) applyUserQuotaAdjustment(c *gin.Context, reseller Reseller, input quotaAdjustmentRequest, currency CurrencyConfig, quota int, displayAmount decimal.Decimal) (*QuotaLedger, bool, error) {
	if existing, err := a.existingLedger(reseller.ID, input.IdempotencyKey); err == nil {
		if err = validateDuplicateLedger(existing, nil, "user_quota", reseller.QuotaCarrierUserID, input, quota); err != nil {
			return nil, false, err
		}
		return existing, true, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if err := redisHealthy(c.Request.Context()); err != nil {
		return nil, false, fmt.Errorf("Redis unavailable; quota writes are paused: %w", err)
	}
	ledger := baseLedger(c, reseller.ID, nil, input, currency, 10000, quota, displayAmount)
	ledger.TargetType = "user_quota"
	ledger.NewAPIUserID = reseller.QuotaCarrierUserID
	ledger.QuotaDelta = quota
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ledger).Error; err != nil {
			return err
		}
		var user model.User
		if err := model.AgencyLockForUpdate(tx).Where("id = ? AND status = ?", reseller.QuotaCarrierUserID, common.UserStatusEnabled).First(&user).Error; err != nil {
			return errors.New("quota carrier account not found or disabled")
		}
		if user.BillingMode == model.AgencyDurableBillingMode || user.BillingMode == model.AgencyProvisioningBillingMode {
			return model.ErrAgencyFundingUnavailable
		}
		ledger.QuotaBefore = user.Quota
		ledger.UsedQuotaBefore = user.UsedQuota
		if user.Quota > common.MaxQuota-quota {
			return errors.New("quota carrier balance would overflow")
		}
		result := tx.Model(&model.User{}).Where("id = ? AND quota <= ?", user.Id, common.MaxQuota-quota).Update("quota", gorm.Expr("quota + ?", quota))
		if result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return errors.New("quota carrier update was not applied")
		}
		ledger.QuotaAfter = user.Quota + quota
		ledger.UsedQuotaAfter = user.UsedQuota
		if err := markReversedLedger(tx, &ledger); err != nil {
			return err
		}
		ledger.Status = ledgerStatusQuotaApplied
		return tx.Model(&QuotaLedger{}).Where("id = ?", ledger.ID).Updates(map[string]any{
			"quota_before": ledger.QuotaBefore, "quota_after": ledger.QuotaAfter,
			"used_quota_before": ledger.UsedQuotaBefore, "used_quota_after": ledger.UsedQuotaAfter,
			"status": ledgerStatusQuotaApplied,
		}).Error
	})
	if err != nil {
		if existing, findErr := a.existingLedger(reseller.ID, input.IdempotencyKey); findErr == nil {
			if validateErr := validateDuplicateLedger(existing, nil, "user_quota", reseller.QuotaCarrierUserID, input, quota); validateErr != nil {
				return nil, false, validateErr
			}
			return existing, true, nil
		}
		return nil, false, err
	}
	cacheKey := "user:" + strconv.Itoa(reseller.QuotaCarrierUserID)
	return a.finishQuotaEvent(c.Request.Context(), ledger, cacheKey, "Quota", quota, nil)
}

func (a *App) applyTokenQuotaAdjustment(c *gin.Context, customer *Customer, mapping CustomerToken, input quotaAdjustmentRequest, currency CurrencyConfig, discount, quota int, displayAmount decimal.Decimal) (*QuotaLedger, bool, error) {
	if existing, err := a.existingLedger(customer.ResellerID, input.IdempotencyKey); err == nil {
		if err = validateDuplicateLedger(existing, &customer.ID, "token_quota", mapping.NewAPITokenID, input, quota); err != nil {
			return nil, false, err
		}
		return existing, true, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if err := redisHealthy(c.Request.Context()); err != nil {
		return nil, false, fmt.Errorf("Redis unavailable; quota writes are paused: %w", err)
	}
	tokenSnapshot, err := a.managedToken(&mapping)
	if err != nil {
		return nil, false, errors.New("managed token not found")
	}
	cacheKey := "token:" + common.GenerateHMAC(tokenSnapshot.Key)
	if input.Mode == "subtract" {
		cachedQuota, exists, err := readCachedQuota(c.Request.Context(), cacheKey, "RemainQuota")
		if err != nil {
			return nil, false, fmt.Errorf("read cached token quota: %w", err)
		}
		if exists && cachedQuota < int64(quota) {
			return nil, false, errors.New("insufficient cached token quota or pending consumption")
		}
	}
	ledger := baseLedger(c, customer.ResellerID, &customer.ID, input, currency, discount, quota, displayAmount)
	ledger.TargetType = "token_quota"
	ledger.NewAPIUserID = mapping.QuotaCarrierUserID
	ledger.NewAPITokenID = &mapping.NewAPITokenID
	delta := quota
	if input.Mode == "subtract" {
		delta = -quota
	}
	ledger.QuotaDelta = delta
	var tokenKey string
	var finalStatus *int
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ledger).Error; err != nil {
			return err
		}
		var token model.Token
		if err := tx.Where("id = ? AND user_id = ?", mapping.NewAPITokenID, mapping.QuotaCarrierUserID).First(&token).Error; err != nil {
			return errors.New("managed token not found")
		}
		if token.UnlimitedQuota {
			return errors.New("unlimited token cannot be adjusted")
		}
		ledger.QuotaBefore = token.RemainQuota
		ledger.UsedQuotaBefore = token.UsedQuota
		query := tx.Model(&model.Token{}).Where("id = ? AND user_id = ?", token.Id, token.UserId)
		if delta < 0 {
			query = query.Where("remain_quota >= ?", -delta)
		} else if token.RemainQuota > common.MaxQuota-delta {
			return errors.New("token balance would overflow")
		} else {
			query = query.Where("remain_quota <= ?", common.MaxQuota-delta)
		}
		result := query.Update("remain_quota", gorm.Expr("remain_quota + ?", delta))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("insufficient token quota or concurrent adjustment")
		}
		ledger.QuotaAfter = token.RemainQuota + delta
		ledger.UsedQuotaAfter = token.UsedQuota
		if err := markReversedLedger(tx, &ledger); err != nil {
			return err
		}
		newStatus := token.Status
		if ledger.QuotaAfter <= 0 && token.Status == common.TokenStatusEnabled {
			newStatus = common.TokenStatusExhausted
		}
		if ledger.QuotaAfter > 0 && token.Status == common.TokenStatusExhausted && customer.Status == "active" && mapping.Status == "active" {
			var reseller Reseller
			if err := tx.Where("id = ? AND status = ?", customer.ResellerID, "active").First(&reseller).Error; err == nil {
				newStatus = common.TokenStatusEnabled
			}
		}
		if newStatus != token.Status {
			if err := tx.Model(&model.Token{}).Where("id = ?", token.Id).Update("status", newStatus).Error; err != nil {
				return err
			}
			finalStatus = &newStatus
		}
		tokenKey = token.Key
		ledger.Status = ledgerStatusQuotaApplied
		return tx.Model(&QuotaLedger{}).Where("id = ?", ledger.ID).Updates(map[string]any{
			"quota_before": ledger.QuotaBefore, "quota_after": ledger.QuotaAfter,
			"used_quota_before": ledger.UsedQuotaBefore, "used_quota_after": ledger.UsedQuotaAfter,
			"status": ledgerStatusQuotaApplied,
		}).Error
	})
	if err != nil {
		if existing, findErr := a.existingLedger(customer.ResellerID, input.IdempotencyKey); findErr == nil {
			if validateErr := validateDuplicateLedger(existing, &customer.ID, "token_quota", mapping.NewAPITokenID, input, quota); validateErr != nil {
				return nil, false, validateErr
			}
			return existing, true, nil
		}
		return nil, false, err
	}
	cacheKey = "token:" + common.GenerateHMAC(tokenKey)
	return a.finishQuotaEvent(c.Request.Context(), ledger, cacheKey, "RemainQuota", delta, finalStatus)
}

func (a *App) finishQuotaEvent(ctx context.Context, ledger QuotaLedger, cacheKey, field string, delta int, status *int) (*QuotaLedger, bool, error) {
	redisResult, redisErr := a.applyRedisQuotaEvent(ctx, cacheKey, field, ledger.EventID, delta, status)
	now := time.Now().Unix()
	updates := map[string]any{"applied_at": now}
	if redisErr != nil {
		updates["status"] = ledgerStatusReconcileRequired
		updates["error_message"] = redisErr.Error()
		ledger.Status = ledgerStatusReconcileRequired
		ledger.ErrorMessage = redisErr.Error()
	} else {
		updates["status"] = ledgerStatusApplied
		updates["error_message"] = ""
		ledger.Status = ledgerStatusApplied
		ledger.ErrorMessage = ""
	}
	ledger.AppliedAt = &now
	if err := a.db.Model(&QuotaLedger{}).Where("id = ?", ledger.ID).Updates(updates).Error; err != nil {
		return &ledger, false, err
	}
	if redisErr != nil {
		return &ledger, false, fmt.Errorf("database quota applied; Redis reconciliation required: %w", redisErr)
	}
	_ = redisResult
	return &ledger, false, nil
}

func (a *App) customerQuotaLedger(c *gin.Context) {
	customerID, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	customer, ok := a.customerForRequest(c, customerID)
	if !ok {
		return
	}
	page := queryPositiveInt(c, "page", 1, 1000000)
	pageSize := queryPositiveInt(c, "page_size", 50, 200)
	query := a.db.Model(&QuotaLedger{}).Where("reseller_id = ? AND customer_id = ?", customer.ResellerID, customer.ID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	var rows []QuotaLedger
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

func (a *App) getQuotaAdjustment(c *gin.Context) {
	eventID := strings.TrimSpace(c.Param("event_id"))
	identity := currentIdentity(c)
	query := a.db.Where("event_id = ?", eventID)
	if identity.HubRole != HubRoleSuperAdmin {
		query = query.Where("reseller_id = ?", identity.ResellerID)
	}
	var ledger QuotaLedger
	if err := query.First(&ledger).Error; err != nil {
		respondError(c, http.StatusNotFound, "quota adjustment not found")
		return
	}
	respondOK(c, ledger)
}
