package model

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

// The persisted snapshot is a business fact, not client input. Empty snapshots
// belong to existing accounts or agency registrations, never to current policy.
type registrationFunding struct {
	Version      int                   `json:"version"`
	Owner        string                `json:"owner"`
	Binding      *opsauthority.Binding `json:"binding,omitempty"`
	NewUserQuota int                   `json:"new_user_quota"`
	InviteeQuota int                   `json:"invitee_quota"`
	InviterQuota int                   `json:"inviter_quota"`
	InviterID    int                   `json:"inviter_id"`
	Applied      bool                  `json:"applied"`
	Username     string                `json:"username"`
	QuotaPerUnit float64               `json:"quota_per_unit"`
	ExchangeRate float64               `json:"exchange_rate"`
	TopupPrice   float64               `json:"topup_price"`
	NewUserText  string                `json:"new_user_text"`
	InviteeText  string                `json:"invitee_text"`
}

func (user *User) prepareRegistrationFunding(inviterID int) error {
	facts := registrationFunding{Version: 1, Owner: "gateway", NewUserQuota: common.QuotaForNewUser, Username: user.Username, QuotaPerUnit: common.QuotaPerUnit, ExchangeRate: operation_setting.USDExchangeRate, TopupPrice: operation_setting.Price}
	if inviterID > 0 && operation_setting.IsPaymentComplianceConfirmed() {
		facts.InviterID, facts.InviteeQuota, facts.InviterQuota = inviterID, common.QuotaForInvitee, common.QuotaForInviter
	}
	for _, quota := range []int{facts.NewUserQuota, facts.InviteeQuota, facts.InviterQuota} {
		if quota < 0 || int64(quota) > common.MaxWalletQuota {
			return errors.New("invalid registration gift quota")
		}
	}
	if int64(facts.NewUserQuota) > common.MaxWalletQuota-int64(facts.InviteeQuota) {
		return errors.New("registration gifts exceed wallet capacity")
	}
	for _, conversion := range []float64{facts.QuotaPerUnit, facts.ExchangeRate, facts.TopupPrice} {
		if conversion <= 0 || math.IsNaN(conversion) || math.IsInf(conversion, 0) {
			return errors.New("invalid registration money conversion")
		}
	}
	facts.NewUserText = fmt.Sprintf("新用户注册赠送 %s", logger.LogQuota(facts.NewUserQuota))
	facts.InviteeText = fmt.Sprintf("使用邀请码赠送 %s", logger.LogQuota(facts.InviteeQuota))
	user.Quota = facts.NewUserQuota
	if opsauthority.FundingEnabled() {
		var err error
		facts.Binding, err = opsauthority.NewBinding(os.Getenv("OPS_PUSH_URL"), os.Getenv("OPS_PUSH_KEY_ID"), opsauthority.Scope())
		if err != nil {
			return err
		}
		facts.Owner, user.Quota = "ops", 0
	}
	encoded, err := common.Marshal(facts)
	user.RegistrationFunding = string(encoded)
	return err
}

// Registration, its funding and affiliate rewards commit with the OAuth binding.
// Re-running post-commit hooks cannot write money, even after a flag rollback.
func (user *User) applyRegistrationFundingTx(tx *gorm.DB) error {
	var stored User
	if err := lockForUpdate(tx).First(&stored, user.Id).Error; err != nil {
		return err
	}
	var facts registrationFunding
	if common.UnmarshalJsonStr(stored.RegistrationFunding, &facts) != nil || facts.Version != 1 {
		return errors.New("missing registration funding decision")
	}
	if facts.Applied {
		return nil
	}
	switch facts.Owner {
	case "ops":
		if !opsauthority.FundingEnabled() || !facts.Binding.Matches(os.Getenv("OPS_PUSH_URL"), os.Getenv("OPS_PUSH_KEY_ID"), opsauthority.Scope()) {
			return errors.New("registration funding destination changed")
		}
		for _, gift := range []struct {
			quota  int
			source string
		}{{facts.NewUserQuota, "registration"}, {facts.InviteeQuota, "invitation"}} {
			if gift.quota > 0 {
				if err := QueueOPSWalletFundingTx(tx, int64(user.Id), int64(gift.quota), "gift", gift.source, strconv.Itoa(user.Id), nil); err != nil {
					return err
				}
			}
		}
	case "gateway":
		if facts.InviteeQuota > 0 {
			// Keep the existing durable funding path for managed accounts; a
			// direct quota update would lose its non-paid lot and ledger facts.
			if err := ApplyAgencyQuotaDeltaWithSourceTx(tx, int64(user.Id), int64(facts.InviteeQuota), "quota_grant", "invite-gift-"+strconv.Itoa(user.Id)); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid registration funding owner")
	}
	// Affiliate rewards remain a separate legacy balance until OPS supports an
	// atomic reward transfer. Freezing the amount here avoids post-hook replay.
	if facts.InviterID > 0 && facts.InviterQuota > 0 {
		if facts.InviterID == user.Id {
			return errors.New("a new account cannot invite itself")
		}
		var inviter User
		if err := lockForUpdate(tx).First(&inviter, facts.InviterID).Error; err != nil {
			return err
		}
		if inviter.AffCount >= math.MaxInt32 || int64(inviter.AffQuota) > common.MaxWalletQuota-int64(facts.InviterQuota) || int64(inviter.AffHistoryQuota) > common.MaxWalletQuota-int64(facts.InviterQuota) {
			return errors.New("inviter reward exceeds wallet capacity")
		}
		if err := tx.Model(&inviter).Updates(map[string]any{
			"aff_count": gorm.Expr("aff_count + ?", 1), "aff_quota": gorm.Expr("aff_quota + ?", facts.InviterQuota), "aff_history": gorm.Expr("aff_history + ?", facts.InviterQuota),
		}).Error; err != nil {
			return err
		}
	}
	facts.Applied = true
	encoded, err := common.Marshal(facts)
	if err != nil {
		return err
	}
	result := tx.Model(&User{}).Where("id = ? AND registration_funding = ?", user.Id, stored.RegistrationFunding).Update("registration_funding", string(encoded))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("registration funding decision changed")
	}
	user.RegistrationFunding = string(encoded)
	if facts.Owner == "gateway" {
		user.Quota += facts.InviteeQuota
	}
	return nil
}

func (user *User) recordRegistrationFunding() {
	// Read the committed account, including after process restart. Never trust
	// an in-memory object from a rolled-back registration transaction.
	var stored User
	if err := DB.Select("id", "created_at", "registration_funding").First(&stored, user.Id).Error; err != nil {
		return
	}
	if stored.RegistrationFunding == "" {
		return
	}
	var facts registrationFunding
	if common.UnmarshalJsonStr(stored.RegistrationFunding, &facts) != nil || facts.Version != 1 || !facts.Applied || facts.Owner != "ops" && facts.Owner != "gateway" {
		common.SysError(fmt.Sprintf("invalid committed registration funding: user=%d", user.Id))
		return
	}
	fields := map[string]any{"wallet_event_owner": facts.Owner}
	if facts.Owner == "ops" {
		fields["wallet_event_owner"] = "ops_funding"
		if facts.NewUserQuota > 0 || facts.InviteeQuota > 0 {
			opsnotify.FundingCommitted()
		}
	}
	// A replay may add another audit row in a separate log database. Its OPS
	// mirror uses the same business ID and frozen payload, never log-row IDs.
	for _, gift := range []struct {
		quota                int
		source, text, prefix string
	}{{facts.NewUserQuota, LogQuotaSourceRegister, facts.NewUserText, "reg-gift-"}, {facts.InviteeQuota, LogQuotaSourceInvitee, facts.InviteeText, "invite-gift-"}} {
		if gift.quota <= 0 {
			continue
		}
		values := walletLogFields(gift.source, false, fields)
		values["registration_event_id"] = gift.prefix + strconv.Itoa(user.Id)
		values["quota_per_unit"], values["usd_exchange_rate"], values["topup_price"] = facts.QuotaPerUnit, facts.ExchangeRate, facts.TopupPrice
		other, err := common.Marshal(values)
		if err != nil {
			common.SysError("failed to encode registration audit")
			continue
		}
		log := Log{UserId: user.Id, Username: facts.Username, CreatedAt: stored.CreatedAt, Type: LogTypeSystem, Content: gift.text, Quota: gift.quota, Other: string(other)}
		if err := createLog(&log); err != nil {
			common.SysError("failed to record registration audit: " + err.Error())
		}
	}
	if facts.InviterID > 0 && facts.InviterQuota > 0 {
		RecordQuotaLog(facts.InviterID, LogTypeSystem, fmt.Sprintf("邀请用户赠送 %s", logger.LogQuota(facts.InviterQuota)), 0, LogQuotaSourceInviter, false, map[string]any{"aff_quota": facts.InviterQuota})
	}
}
