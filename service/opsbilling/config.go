// Package opsbilling reads OPS debit receipts and balances without charging or
// changing the gateway's shared wallet database. Enabling reads is not a cutover.
package opsbilling

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/businessbilling"
	"github.com/QuantumNous/new-api/service/opsclient"
)

type Config struct {
	BaseURL, KeyID, Secret string
	Timeout                time.Duration
}

var Default *Reader

func init() {
	businessbilling.ConfigureCharges(&businessbilling.ChargeBinding{
		Authoritative: opsauthority.Enabled,
		Enabled:       func() bool { return os.Getenv("OPS_BILLING_RECEIPTS") == "charges" },
		Receipt: func(ctx context.Context, log *model.Log, wait time.Duration) businessapi.Billing {
			result := businessapi.Billing{Source: "ops", Currency: "CNY", Status: "unavailable"}
			if log == nil || log.UserId <= 0 || log.TokenId <= 0 || log.RequestId == "" || log.ModelName == "" {
				result.Reason = "invocation_unavailable"
				return result
			}
			if log.Type != model.LogTypeConsume {
				result.Reason = "call_failed"
				return result
			}
			if Default == nil {
				result.Reason = "ops_charge_reads_disabled"
				return result
			}
			return Default.WaitReceipt(ctx, log.RequestId, log.UserId, log.ModelName, wait)
		},
	})
}

func StartConfigured(ctx context.Context) error {
	if opsauthority.Enabled() && !common.LogConsumeEnabled {
		return errors.New("OPS wallet authority requires consumption logging")
	}
	if err := opsauthority.Validate(); err != nil {
		return err
	}
	if mode := os.Getenv("OPS_BILLING_RECEIPTS"); mode != "" && mode != "fees" && mode != "charges" {
		return errors.New("OPS_BILLING_RECEIPTS must be fees or charges")
	}
	if os.Getenv("OPS_BILLING_ENABLED") != "true" {
		if os.Getenv("OPS_BILLING_RECEIPTS") == "charges" {
			return errors.New("OPS charge receipts require OPS_BILLING_ENABLED=true")
		}
		return nil
	}
	c := Config{BaseURL: strings.TrimRight(os.Getenv("OPS_BILLING_URL"), "/"), KeyID: os.Getenv("OPS_BILLING_KEY_ID"), Secret: os.Getenv("OPS_BILLING_SECRET"), Timeout: 5 * time.Second}
	if err := opsclient.ValidateBase(c.BaseURL); err != nil {
		return err
	}
	if c.KeyID == "" || len(c.KeyID) > 160 || len(c.Secret) < 16 {
		return errors.New("OPS billing read credentials missing")
	}
	Default = New(c)
	if err := Default.Client.EnableReadBudget(); err != nil {
		return err
	}
	Default.restoreSnapshot(ctx, opsauthority.Scope())
	if opsauthority.Enabled() {
		Default.UserIDs = model.ListOPSBalanceUserIDs
	}
	go Default.Run(ctx)
	if opsauthority.Enabled() {
		common.SysLog("OPS project wallet authority enabled")
	} else {
		common.SysLog("OPS debit and balance reads enabled; local wallet charging unchanged")
	}
	return nil
}
