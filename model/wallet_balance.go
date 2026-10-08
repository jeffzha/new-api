package model

import "strings"

const (
	WalletBalanceTypeRecharge   = "recharge"
	WalletBalanceTypeGift       = "gift"
	WalletBalanceTypeRedemption = "redemption"
	WalletBalanceTypeOther      = "other"
)

// WalletBalance reports the currently available quota for one wallet source
// category. Type is intentionally stable so clients can add presentation for
// new categories without changing this response shape.
type WalletBalance struct {
	Type  string `json:"type"`
	Quota int64  `json:"quota"`
}

// GetWalletBalanceBreakdown derives a read-only wallet view from durable
// funding lots. Unattributed legacy quota is kept in the explicit other bucket
// so the breakdown always reconciles to the user's authoritative quota.
func GetWalletBalanceBreakdown(userID int64, totalQuota int64, billingMode string) ([]WalletBalance, error) {
	if billingMode != AgencyDurableBillingMode {
		return walletBalancesFromAmounts(0, 0, 0, totalQuota), nil
	}

	var lots []AgencyFundingLot
	if err := DB.Select("source_kind, paid_available, bonus_available").
		Where("user_id = ? AND (paid_available <> 0 OR bonus_available <> 0)", userID).
		Find(&lots).Error; err != nil {
		return nil, err
	}

	amounts := map[string]int64{
		WalletBalanceTypeRecharge:   0,
		WalletBalanceTypeGift:       0,
		WalletBalanceTypeRedemption: 0,
		WalletBalanceTypeOther:      0,
	}
	for _, lot := range lots {
		available := lot.PaidAvailable + lot.BonusAvailable
		switch walletBalanceTypeForFundingSource(lot.SourceKind) {
		case WalletBalanceTypeRecharge:
			amounts[WalletBalanceTypeRecharge] += lot.PaidAvailable
			amounts[WalletBalanceTypeGift] += lot.BonusAvailable
		case WalletBalanceTypeGift:
			amounts[WalletBalanceTypeGift] += available
		case WalletBalanceTypeRedemption:
			amounts[WalletBalanceTypeRedemption] += available
		default:
			amounts[WalletBalanceTypeOther] += available
		}
	}

	classified := amounts[WalletBalanceTypeRecharge] + amounts[WalletBalanceTypeGift] +
		amounts[WalletBalanceTypeRedemption] + amounts[WalletBalanceTypeOther]
	difference := totalQuota - classified
	if difference >= 0 || totalQuota < 0 {
		amounts[WalletBalanceTypeOther] += difference
	} else {
		// The user quota is authoritative. If a concurrent debit or an old
		// reconciliation issue leaves lots temporarily ahead of it, reduce
		// classified balances in wallet consumption order instead of showing a
		// negative source balance for an otherwise positive wallet.
		overage := -difference
		for _, balanceType := range []string{
			WalletBalanceTypeRedemption,
			WalletBalanceTypeGift,
			WalletBalanceTypeRecharge,
			WalletBalanceTypeOther,
		} {
			take := min(amounts[balanceType], overage)
			amounts[balanceType] -= take
			overage -= take
			if overage == 0 {
				break
			}
		}
	}

	return walletBalancesFromAmounts(
		amounts[WalletBalanceTypeRecharge],
		amounts[WalletBalanceTypeGift],
		amounts[WalletBalanceTypeRedemption],
		amounts[WalletBalanceTypeOther],
	), nil
}

func walletBalancesFromAmounts(recharge, gift, redemption, other int64) []WalletBalance {
	balances := []WalletBalance{
		{Type: WalletBalanceTypeRecharge, Quota: recharge},
		{Type: WalletBalanceTypeGift, Quota: gift},
		{Type: WalletBalanceTypeRedemption, Quota: redemption},
	}
	if other != 0 {
		balances = append(balances, WalletBalance{Type: WalletBalanceTypeOther, Quota: other})
	}
	return balances
}

func walletBalanceTypeForFundingSource(sourceKind string) string {
	source := strings.ToLower(strings.TrimSpace(sourceKind))
	switch source {
	case "redemption", "redeem", "redemption_code":
		return WalletBalanceTypeRedemption
	case "admin", "admin_grant", "admin_adjustment", "admin_override", "quota_grant",
		"batch_quota", "other_grant", "checkin", "affiliate_transfer", "provisioning_opening":
		return WalletBalanceTypeGift
	case "payment", "payment_self", "payment_user", "payment_assisted", "epay", "stripe",
		"creem", "waffo", "waffo_pancake":
		return WalletBalanceTypeRecharge
	default:
		return WalletBalanceTypeOther
	}
}
