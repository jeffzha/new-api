package controller

import (
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opswallet"
)

// Response-only fields keep the OPS wallet separate from the shared user row.
// Reading a list only queues bounded background work; it never performs OPS HTTP.
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
	balance := opsbilling.BalanceView{Status: "unavailable", Source: "ops", Currency: "CNY", Reason: "ops_balance_reads_disabled"}
	if opsbilling.Default != nil {
		balance = opsbilling.Default.Balance(userID, time.Now())
	}
	return balance
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
