package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func (s *CNRefundService) Execute(ctx context.Context, c CNRefundCommand, reconcile bool) (CNRefundReceipt, error) {
	var result CNRefundReceipt
	if err := s.scope(c.TenantID, c.GatewayID, c.OperationID); err != nil {
		return result, err
	}
	if c.ExpectedVersion <= 0 || !cnRefundID.MatchString(c.AttemptID) || !cnRefundID.MatchString(c.ApprovalID) || !cnRefundHash.MatchString(c.ApprovalHash) {
		return result, ErrCNRefundInvalid
	}
	key, action := s.key(c.OperationID), "execute"
	if reconcile {
		action = "reconcile"
	}
	commandKey := cnRefundCommandKey(key, action, c.AttemptID)
	raw, err := common.Marshal(c)
	if err != nil {
		return result, err
	}
	hash := CNIdentity(string(raw))
	var intent CNRefundIntent
	var completed bool
	err = retryAgencyTransaction(func() error {
		return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			row, _, _, _, err := cnRefundLockReservation(tx, key)
			if err != nil {
				return err
			}
			var command CNRefundMutation
			err = lockForUpdate(tx).Where(map[string]any{"key": commandKey}).First(&command).Error
			if err == nil {
				if command.RequestHash != hash {
					return ErrCNRefundConflict
				}
				if command.ResponseJSON != "" {
					completed = true
					return common.UnmarshalJsonStr(command.ResponseJSON, &result)
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var r CNRefundReserveRequest
			if err := common.UnmarshalJsonStr(row.RequestJSON, &r); err != nil {
				return err
			}
			if r.ApprovalID != c.ApprovalID || r.ApprovalHash != c.ApprovalHash {
				return ErrCNRefundConflict
			}
			missingCommand := errors.Is(err, gorm.ErrRecordNotFound)
			if missingCommand && row.Version != c.ExpectedVersion {
				return ErrCNRefundStale
			}
			if row.Status == "succeeded" || row.Status == "failed_released" {
				result, err = cnRefundReceipt(row)
				if err != nil {
					return err
				}
				response, err := common.Marshal(result)
				if err != nil {
					return err
				}
				completed = true
				if !missingCommand {
					return tx.Model(&command).Update("response_json", string(response)).Error
				}
				return tx.Create(&CNRefundMutation{Key: commandKey, ReservationKey: key, RequestHash: hash, ResponseJSON: string(response)}).Error
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if reconcile && row.ProviderIdentity == "" {
					return ErrCNRefundConflict
				}
				if s.Provider == nil || s.Provider.Name() == "" {
					return ErrCNRefundUnavailable
				}
				if row.ProviderName != "" && row.ProviderName != s.Provider.Name() {
					return ErrCNRefundUnavailable
				}
				if row.ProviderIdentity == "" {
					row.ProviderIdentity = CNIdentity("cn-refund-provider", key)
					row.ProviderName = s.Provider.Name()
				}
				row.Status = "executing"
				row.Version++
				row.UpdatedAt = time.Now().UnixMilli()
				if err := tx.Save(&row).Error; err != nil {
					return err
				}
				if err := tx.Create(&CNRefundMutation{Key: commandKey, ReservationKey: key, RequestHash: hash, ResponseJSON: ""}).Error; err != nil {
					return err
				}
			}
			if s.Provider == nil || s.Provider.Name() != row.ProviderName {
				return ErrCNRefundUnavailable
			}
			intent = CNRefundIntent{Identity: row.ProviderIdentity, OriginalPaymentID: r.OriginalPaymentID, PaymentReference: row.PaymentReference, Amount: r.Amount, Currency: r.Currency}
			return nil
		})
	})
	if err != nil || completed {
		return result, err
	}
	// No DB transaction spans external I/O. A crash leaves the committed intent
	// and pending command; a retry can only submit the exact same provider identity.
	var outcome CNRefundProviderResult
	if reconcile {
		outcome, err = s.Provider.Query(ctx, intent)
	} else {
		outcome, err = s.Provider.Submit(ctx, intent)
	}
	if err != nil || outcome.Intent != intent || len(outcome.ReceiptID) > 191 || len(outcome.Evidence) > 65536 ||
		(outcome.Status != "succeeded" && outcome.Status != "definitive_failed") || outcome.ReceiptID == "" || outcome.Evidence == "" || outcome.ConfirmedAt.IsZero() {
		outcome = CNRefundProviderResult{Intent: intent, Status: "unknown_reconcile"}
	}
	// Finalization uses an independent bounded context: caller disconnect cannot
	// discard an already received provider result. Failure leaves intent recoverable.
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	err = retryAgencyTransaction(func() error {
		return s.DB.WithContext(finalCtx).Transaction(func(tx *gorm.DB) error {
			row, u, a, l, err := cnRefundLockReservation(tx, key)
			if err != nil {
				return err
			}
			var command CNRefundMutation
			if err = lockForUpdate(tx).Where(map[string]any{"key": commandKey}).First(&command).Error; err != nil {
				return err
			}
			if command.RequestHash != hash {
				return ErrCNRefundConflict
			}
			if command.ResponseJSON != "" {
				return common.UnmarshalJsonStr(command.ResponseJSON, &result)
			}
			if row.ProviderIdentity != intent.Identity || row.ProviderName != s.Provider.Name() {
				return ErrCNRefundConflict
			}
			if row.Status != "succeeded" && row.Status != "failed_released" {
				switch outcome.Status {
				case "succeeded":
					if err = RetireCNFundingValueTx(tx, row.Key); err != nil {
						return err
					}
					if err = cnRefundMoveHoldTx(tx, &u, &a, &l, row.HeldQuota, "settle", c.OperationID); err != nil {
						return err
					}
					row.Status = "succeeded"
					row.CanonicalFactID, err = cnRefundStageCashTx(tx, row, outcome)
					if err != nil {
						return err
					}
					row.HeldQuota = 0
				case "definitive_failed":
					if err = ReleaseCNFundingValueTx(tx, row.Key); err != nil {
						return err
					}
					if err = cnRefundMoveHoldTx(tx, &u, &a, &l, row.HeldQuota, "release", c.OperationID); err != nil {
						return err
					}
					row.HeldQuota = 0
					row.Status = "failed_released"
				default:
					row.Status = "unknown_reconcile"
				}
				row.Version++
				row.UpdatedAt = time.Now().UnixMilli()
				if outcome.Status != "unknown_reconcile" {
					row.ProviderReceiptID = outcome.ReceiptID
					row.ProviderEvidence = outcome.Evidence
				}
				if err = tx.Save(&row).Error; err != nil {
					return err
				}
			}
			result, err = cnRefundReceipt(row)
			if err != nil {
				return err
			}
			response, err := common.Marshal(result)
			if err != nil {
				return err
			}
			return tx.Model(&command).Update("response_json", string(response)).Error
		})
	})
	return result, err
}

func cnRefundLockReservation(tx *gorm.DB, key string) (CNRefundReservation, User, AgencyFundingAccount, AgencyFundingLot, error) {
	var row CNRefundReservation
	// This lookup supplies immutable IDs only. The current locking read below
	// avoids MySQL repeatable-read snapshots for the mutable state.
	if err := tx.Where(map[string]any{"key": key}).First(&row).Error; err != nil {
		return row, User{}, AgencyFundingAccount{}, AgencyFundingLot{}, err
	}
	u, a, l, err := cnRefundLockFunding(tx, row.UserID, row.LotID)
	if err != nil {
		return row, u, a, l, err
	}
	err = lockForUpdate(tx).Where(map[string]any{"key": key}).First(&row).Error
	return row, u, a, l, err
}

func cnRefundStageCashTx(tx *gorm.DB, row CNRefundReservation, outcome CNRefundProviderResult) (string, error) {
	var r CNRefundReserveRequest
	if err := common.UnmarshalJsonStr(row.RequestJSON, &r); err != nil {
		return "", err
	}
	evidence := CNEvidence{System: row.ProviderName, RecordType: "cn_refund_provider_receipt", RecordID: outcome.ReceiptID, EvidenceID: CNIdentity(outcome.Evidence)}
	f := cnBaseFact("payment_refund", row.Key, "0", r.OperationID, "agency_wallet", row.UserID, outcome.ConfirmedAt, evidence)
	f.FactID = CNIdentity("cn-refund-cash", row.Key)
	f.Quality = "verified_payment"
	f.Cumulative.Money = cnUnknownTaxMoney(r.Amount, r.Currency)
	f.Delta = f.Cumulative
	f.Links["paymentIds"] = []string{r.OriginalPaymentID}
	f.Links["refundIds"] = []string{row.Key}
	f.Links["approvalIds"] = []string{r.ApprovalID}
	var lot AgencyFundingLot
	if err := tx.First(&lot, row.LotID).Error; err != nil {
		return "", err
	}
	f.Links["fundingLotIds"] = []string{CNIdentity("agency-lot", lot.SourceKind, lot.SourceID, "paid")}
	f.Conversion = map[string]any{"sourceUnit": "currency", "quotaPerUnit": nil, "originalCurrency": r.Currency, "settlementCurrency": r.Currency, "exchangeRate": "1", "fxVersion": "identity-1", "rounding": "EXACT_REFUND_QUOTA_V1"}
	if err := StageCNFactTx(tx, f); err != nil {
		return "", err
	}
	return f.FactID, nil
}
