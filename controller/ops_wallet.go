package controller

import (
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opswallet"
)

// Response-only fields preserve the shared project contract without copying
// an OPS balance into the local user row or issuing HTTP for each list row.
type opsWalletUser struct {
	*model.User
	WalletAuthority          string                 `json:"wallet_authority"`
	OPSBalance               opsbilling.BalanceView `json:"ops_balance"`
	WalletAdjustmentsEnabled bool                   `json:"wallet_adjustments_enabled"`
}

func walletUserResponse(user *model.User) any {
	if !opsauthority.Enabled() {
		return user
	}
	return opsWalletUser{User: user, WalletAuthority: "ops", OPSBalance: walletBalance(user.Id), WalletAdjustmentsEnabled: opswallet.Enabled()}
}

func walletBalance(userID int) opsbilling.BalanceView {
	if opsbilling.Default != nil {
		return opsbilling.Default.Balance(userID, time.Now())
	}
	return opsbilling.BalanceView{Status: "unavailable", Source: "ops", Currency: opsbilling.Currency(), Reason: "ops_balance_reads_disabled"}
}

func walletUsersResponse(users []*model.User) any {
	if !opsauthority.Enabled() {
		return users
	}
	result := make([]any, len(users))
	for i, user := range users {
		result[i] = walletUserResponse(user)
	}
	return result
}
