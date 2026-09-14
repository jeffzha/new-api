package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	agencyRedemptionExpiryInterval = time.Minute
	agencyRedemptionExpiryBatch    = 100
)

// StartAgencyRedemptionExpiry keeps idle durable wallets aligned with frozen
// redemption-code deadlines. Spend-boundary expiry remains authoritative.
func StartAgencyRedemptionExpiry() {
	if !common.IsMasterNode {
		return
	}
	go func() {
		expireAgencyRedemptionBatches(time.Now().Unix())
		ticker := time.NewTicker(agencyRedemptionExpiryInterval)
		defer ticker.Stop()
		for now := range ticker.C {
			expireAgencyRedemptionBatches(now.Unix())
		}
	}()
}

func expireAgencyRedemptionBatches(now int64) {
	for {
		users, quota, err := model.ExpireAgencyRedemptionLots(now, agencyRedemptionExpiryBatch)
		if err != nil {
			common.SysError("agency redemption expiry failed: " + err.Error())
			return
		}
		if quota > 0 {
			common.SysLog(fmt.Sprintf("agency redemption expiry: users=%d quota=%d", users, quota))
		}
		if users < agencyRedemptionExpiryBatch {
			return
		}
	}
}
