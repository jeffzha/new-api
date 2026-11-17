// Package businessbilling selects receipts without calculating or charging money.
package businessbilling

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/businessapi"
)

// Project-only debit selection is installed at startup; the shared product fee
// reader never starts a balance reader or switches local wallet ownership.
type ChargeBinding struct {
	Authoritative func() bool
	Enabled       func() bool
	Receipt       func(context.Context, *model.Log, time.Duration) businessapi.Billing
}

var chargeBinding atomic.Pointer[ChargeBinding]

func ConfigureCharges(binding *ChargeBinding) { chargeBinding.Store(binding) }

func WaitDuration(query url.Values) (time.Duration, error) {
	values, exists := query["billing_wait_ms"]
	if !exists {
		binding := chargeBinding.Load()
		if model.OpsFeesEnabled() || binding != nil && binding.Enabled != nil && binding.Enabled() {
			return 800 * time.Millisecond, nil
		}
		return 0, nil
	}
	if len(values) != 1 {
		return 0, errors.New("billing_wait_ms must be an integer from 0 to 1000")
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n < 0 || n > 1000 {
		return 0, errors.New("billing_wait_ms must be an integer from 0 to 1000")
	}
	return time.Duration(n) * time.Millisecond, nil
}

func Source(query url.Values) (string, error) {
	values, exists := query["billing_source"]
	if !exists {
		binding := chargeBinding.Load()
		if model.OpsFeesEnabled() || binding != nil && binding.Authoritative != nil && binding.Authoritative() {
			return "ops", nil
		}
		return "gateway", nil
	}
	if len(values) != 1 || values[0] != "gateway" && values[0] != "ops" {
		return "", errors.New("billing_source must be gateway or ops")
	}
	return values[0], nil
}

func Receipt(ctx context.Context, source string, original businessapi.Billing, log *model.Log) businessapi.Billing {
	return ReceiptWithWait(ctx, source, original, log, 0)
}

func ReceiptWithWait(ctx context.Context, source string, original businessapi.Billing, log *model.Log, wait time.Duration) businessapi.Billing {
	binding := chargeBinding.Load()
	if binding != nil && binding.Authoritative != nil && binding.Authoritative() && original.Source == "ops" {
		source = "ops"
	}
	if source != "ops" {
		return original
	}
	if binding != nil && binding.Receipt != nil && binding.Enabled != nil && binding.Enabled() {
		return binding.Receipt(ctx, log, wait)
	}
	result := businessapi.Billing{Source: "ops", Currency: "CNY", Status: "unavailable"}
	fee, _, reason, err := model.BusinessOpsFee(ctx, log)
	if err != nil {
		result.Reason = "fee_read_failed"
		return result
	}
	if reason != "" {
		result.Reason = reason
		return result
	}
	if fee != nil && fee.Status == "waiting" && wait > 0 {
		if value := model.WaitOpsFee(ctx, log, wait); value != nil {
			fee = value
		}
	}
	if fee != nil && fee.Status == "available" && fee.Amount != nil {
		result.Status, result.Amount = "available", fee.Amount
		result.ChargedAt = fee.ChargedAt
		return result
	}
	if fee != nil && fee.Status == "unconfirmed" {
		result.Reason = "ops_amount_unconfirmed"
		return result
	}
	retry := 1
	result.Status, result.Reason, result.RetryAfterSeconds = "pending", "awaiting_ops", &retry
	return result
}
