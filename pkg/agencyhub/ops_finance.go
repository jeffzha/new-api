package agencyhub

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// Host-local delegation uses the existing regional root authority. It creates
// an application, never approves or pays it, and never credits the OPS ledger.
type OpsWithdrawalCommand struct {
	RootID         int64  `json:"rootUserId"`
	AgencyID       int64  `json:"agencyId"`
	AccountID      int64  `json:"accountId"`
	AccountVersion int64  `json:"accountVersion"`
	AmountMicros   int64  `json:"amountMicros"`
	Currency       string `json:"currency"`
	CommandID      string `json:"commandId"`
}

func validateOpsFinanceRoot(db *gorm.DB, id int64) error {
	var root model.User
	if id <= 0 {
		return errors.New("invalid finance authority")
	}
	if err := db.Select("id,role,status").First(&root, id).Error; err != nil {
		return err
	}
	if root.Role != common.RoleRootUser || root.Status != common.UserStatusEnabled {
		return errors.New("finance authority is inactive")
	}
	return nil
}

func ApplyOpsWithdrawal(db *gorm.DB, input OpsWithdrawalCommand, config Config) (map[string]any, error) {
	if !config.WithdrawalsEnabled || input.AgencyID <= 0 || input.AccountID <= 0 || input.AccountVersion <= 0 ||
		input.Currency != "CNY" || input.AmountMicros < 10000 || input.AmountMicros%10000 != 0 ||
		len(input.CommandID) < 8 || len(input.CommandID) > 160 {
		return nil, errors.New("invalid withdrawal command")
	}
	body, err := common.Marshal(input)
	if err != nil {
		return nil, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.withdrawal\n"+input.CommandID)))
	var result map[string]any
	err = model.AgencyOpsTransaction(db, func(tx *gorm.DB) error {
		if err := validateOpsFinanceRoot(tx, input.RootID); err != nil {
			return err
		}
		var writer model.AgencyOpsWriterState
		if err := model.AgencyLockForUpdate(tx).First(&writer, 1).Error; err != nil {
			return err
		}
		if writer.Mode != model.AgencyWriterSourcePrimary && writer.Mode != model.AgencyWriterShadow && writer.Mode != model.AgencyWriterRolledBack {
			return errors.New("source financial writer inactive")
		}
		var agency model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&agency, input.AgencyID).Error; err != nil {
			return err
		}
		var prior model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scope).First(&prior).Error; err == nil {
			if prior.BodyHash != hash {
				return errors.New("withdrawal command identity conflict")
			}
			return common.UnmarshalJsonStr(prior.ResponseJSON, &result)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if agency.Status != AgencyStatusActive {
			return errors.New("agency is disabled")
		}
		var account model.AgencyWithdrawalAccount
		if err := model.AgencyLockForUpdate(tx).Where("id = ? AND agency_id = ? AND version = ? AND status = ?",
			input.AccountID, input.AgencyID, input.AccountVersion, "active").First(&account).Error; err != nil {
			return err
		}
		withdrawal, err := submitWithdrawal(tx, input.AgencyID, input.Currency, input.AmountMicros, account,
			&Identity{ActorType: "tokenops", ActorID: input.RootID}, input.CommandID)
		if err != nil {
			return err
		}
		result = withdrawalView(withdrawal)
		result["agency_id"] = input.AgencyID
		var balance model.AgencyCommissionBalance
		if err := tx.Where("agency_id = ? AND currency_code = ?", input.AgencyID, input.Currency).First(&balance).Error; err != nil {
			return err
		}
		result["balance_version"] = balance.Version
		if err := recordAuditTx(tx, nil, &Identity{ActorType: "tokenops", ActorID: input.RootID, AgencyID: &input.AgencyID},
			"withdrawal.create", "withdrawal", strconv.FormatInt(withdrawal.ID, 10), input.CommandID, nil,
			map[string]any{"status": withdrawal.Status, "amount_micros": input.AmountMicros, "currency_code": input.Currency}); err != nil {
			return err
		}
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scope, ActorType: "tokenops", ActorID: input.RootID,
			Action: "withdrawal.create", BodyHash: hash, ResourceID: strconv.FormatInt(withdrawal.ID, 10),
			ResultCode: 201, ResponseJSON: string(encoded), CreatedAt: time.Now().Unix(), ExpiresAt: 253402300799}).Error
	})
	return result, err
}

// Both the original Hub form and the delegated OPS form use this transaction
// body, including the same tax, balance lock and immutable recipient snapshot.
func submitWithdrawal(tx *gorm.DB, agency int64, currency string, amount int64, account model.AgencyWithdrawalAccount, identity *Identity, operation string) (model.AgencyWithdrawal, error) {
	var balance model.AgencyCommissionBalance
	if err := model.AgencyLockForUpdate(tx).Where("agency_id = ? AND currency_code = ?", agency, currency).First(&balance).Error; err != nil {
		return model.AgencyWithdrawal{}, err
	}
	_, withdrawable := commissionTaxAndWithdrawable(balance)
	if withdrawable < amount {
		return model.AgencyWithdrawal{}, errors.New("提现金额超过可提现佣金余额")
	}
	locked, err := checkedAdd(balance.LockedMicros, amount)
	if err != nil {
		return model.AgencyWithdrawal{}, err
	}
	now := time.Now().UnixMilli()
	if err = tx.Model(&balance).Updates(map[string]any{"available_micros": balance.AvailableMicros - amount, "locked_micros": locked, "version": balance.Version + 1, "updated_at_ms": now}).Error; err != nil {
		return model.AgencyWithdrawal{}, err
	}
	no, err := randomToken(16)
	if err != nil {
		return model.AgencyWithdrawal{}, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(account.Ciphertext)))
	withdrawal := model.AgencyWithdrawal{RequestNo: no, AgencyID: agency, CurrencyCode: currency, AmountMicros: amount,
		Status: "submitted", Version: 1, AccountID: account.ID, AccountVersion: account.Version, AccountSnapshotHash: fingerprint,
		AccountSnapshot: account.Ciphertext, AccountSnapshotKeyID: account.KeyID, CreatedAtMS: now, UpdatedAtMS: now}
	if err = tx.Create(&withdrawal).Error; err != nil {
		return withdrawal, err
	}
	err = tx.Create(&model.AgencyWithdrawalTransition{WithdrawalID: withdrawal.ID, OperationID: operation, BeforeStatus: "",
		AfterStatus: "submitted", AmountDeltaMicros: -amount, ActorType: identity.ActorType, ActorID: identity.ActorID, CreatedAtMS: now}).Error
	return withdrawal, err
}

// Current-state evidence only. Closing exceptions still requires the original
// source session and single-use Root proof; this read transport cannot close.
func ReadOpsReconciliation(db *gorm.DB, root, id, before int64) (map[string]any, error) {
	if err := validateOpsFinanceRoot(db, root); err != nil {
		return nil, err
	}
	app := (*App)(nil)
	if id > 0 {
		var issue model.AgencyReconciliationIssue
		if err := db.First(&issue, id).Error; err != nil {
			return nil, err
		}
		evidence, err := reconciliationEvidence(db, issue, false)
		return map[string]any{"issue": (&App{db: db}).reconciliationIssueView(issue), "verification": evidence}, err
	}
	var runs []model.AgencyReconciliationRun
	var issues []model.AgencyReconciliationIssue
	if err := db.Order("id DESC").Limit(20).Find(&runs).Error; err != nil {
		return nil, err
	}
	query := db.Order("id DESC").Limit(51)
	if before > 0 {
		query = query.Where("id < ?", before)
	}
	if err := query.Find(&issues).Error; err != nil {
		return nil, err
	}
	next := int64(0)
	if len(issues) > 50 {
		issues = issues[:50]
		next = issues[49].ID
	}
	// Resolve display names in batches. Evidence lists must not issue one query
	// per exception, particularly when many issues concern the same customer.
	agencyIDs, userIDs, withdrawalIDs, accountIDs := []string{}, []string{}, []string{}, []string{}
	for _, issue := range issues {
		kind, raw := issue.ObjectType, issue.ObjectID
		if kind == "commission_balance" || kind == "withdrawal_lock" {
			raw, _, _ = strings.Cut(raw, ":")
			kind = "agency"
		} else if prefix, id, ok := strings.Cut(raw, ":"); ok {
			if _, err := strconv.ParseInt(id, 10, 64); err == nil {
				kind, raw = prefix, id
			}
		}
		switch kind {
		case "agency":
			agencyIDs = append(agencyIDs, raw)
		case "user", "customer", "funding_account", "active_binding":
			userIDs = append(userIDs, raw)
		case "withdrawal":
			withdrawalIDs = append(withdrawalIDs, raw)
		case "withdrawal_account":
			accountIDs = append(accountIDs, raw)
		}
	}
	names := map[string]string{}
	if len(agencyIDs) > 0 {
		var rows []model.Agency
		if err := db.Select("id,display_name").Where("id IN ?", agencyIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			names["agency:"+stringID(r.ID)] = r.DisplayName
		}
	}
	if len(userIDs) > 0 {
		var rows []model.User
		if err := db.Select("id,username,display_name").Where("id IN ?", userIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			names["user:"+strconv.Itoa(r.Id)] = userAccountName(r)
		}
	}
	if len(withdrawalIDs) > 0 {
		var rows []model.AgencyWithdrawal
		if err := db.Select("id,request_no").Where("id IN ?", withdrawalIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			names["withdrawal:"+stringID(r.ID)] = r.RequestNo
		}
	}
	if len(accountIDs) > 0 {
		var rows []model.AgencyWithdrawalAccount
		if err := db.Select("id,last4").Where("id IN ?", accountIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			names["withdrawal_account:"+stringID(r.ID)] = "收款账户 · 尾号 " + r.Last4
		}
	}
	values := make([]any, 0, len(issues))
	for _, issue := range issues {
		value := app.reconciliationIssueView(issue)
		kind, raw, suffix := issue.ObjectType, issue.ObjectID, ""
		if kind == "commission_balance" || kind == "withdrawal_lock" {
			raw, suffix, _ = strings.Cut(raw, ":")
			kind = "agency"
			suffix = " · " + suffix
		} else if prefix, id, ok := strings.Cut(raw, ":"); ok {
			if _, err := strconv.ParseInt(id, 10, 64); err == nil {
				kind, raw = prefix, id
			}
		}
		if kind == "customer" || kind == "funding_account" || kind == "active_binding" {
			kind = "user"
		}
		if name := names[kind+":"+raw]; name != "" {
			value["object_name"] = name + suffix
		}
		values = append(values, value)
	}
	return map[string]any{"runs": runs, "items": values, "nextBefore": next, "observedAt": time.Now().UnixMilli()}, nil
}
