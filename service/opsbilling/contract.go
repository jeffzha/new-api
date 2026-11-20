package opsbilling

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/shopspring/decimal"
)

type Charge struct {
	LedgerEntryID json.Number `json:"ledgerEntryId,omitempty"`
	Currency      string      `json:"currency,omitempty"`
	RequestID     string      `json:"requestId"`
	UserID        string      `json:"userId"`
	Model         string      `json:"model"`
	Status        string      `json:"status"`
	Reason        string      `json:"reason"`
	Amount        *string     `json:"amount"`
	RefundAmount  *string     `json:"refundAmount"`
	NetAmount     *string     `json:"netAmount"`
	StationAmount *string     `json:"stationAmount"`
	BalanceAfter  *string     `json:"balanceAfter"`
	Balance       *string     `json:"balance"`
	ChargedAt     string      `json:"chargedAt"`
	LogFee        *LogFee     `json:"logFee,omitempty"`
}

// LogFee is the optional canonical fee-feed view, distinct from wallet money.
// Older OPS versions omit it; those versions keep using incremental fee reads.
type LogFee struct {
	RequestID     string  `json:"requestId"`
	UserID        string  `json:"userId"`
	Model         string  `json:"model"`
	Amount        *string `json:"opsAmount"`
	StationAmount *string `json:"stationAmount"`
	RefundAmount  *string `json:"refundAmount"`
	Difference    *string `json:"difference"`
	ChargedAt     string  `json:"chargedAt"`
}

// Balance is independent of User.Quota. CheckedAt is the last successful read,
// not the historical time of the user's last financial change.
type Balance struct {
	Currency    string      `json:"currency,omitempty"`
	UserID      string      `json:"userId"`
	Amount      string      `json:"balance"`
	Quota       *int64      `json:"balanceQuota"`
	Negative    bool        `json:"negative"`
	LastEntryID json.Number `json:"lastEntryId"`
	UpdatedAt   string      `json:"updatedAt"`
	Station     *struct {
		QuotaPerUnit int64  `json:"quotaPerUnit"`
		ExchangeRate string `json:"usdExchangeRate"`
	} `json:"station,omitempty"`
}

type balanceEntry struct {
	EntryID      json.Number `json:"entryId"`
	UserID       string      `json:"userId"`
	Kind         string      `json:"kind"`
	Amount       string      `json:"amount"`
	BalanceAfter string      `json:"balanceAfter"`
	Quota        *int64      `json:"balanceAfterQuota"`
	PostedAt     string      `json:"postedAt"`
}

func amount(value string, signed bool) (decimal.Decimal, error) {
	if value == "" || len(value) > 80 || strings.ContainsAny(value, "eE+ \t\r\n") {
		return decimal.Zero, errors.New("invalid OPS amount")
	}
	d, err := decimal.NewFromString(value)
	if err != nil || d.Exponent() < -18 || !signed && d.IsNegative() {
		return decimal.Zero, errors.New("invalid OPS amount")
	}
	return d, nil
}

func validUser(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}

func (b Balance) samePosition(other Balance) bool {
	a, err := amount(b.Amount, true)
	c, otherErr := amount(other.Amount, true)
	return err == nil && otherErr == nil && a.Equal(c) && b.Quota != nil && other.Quota != nil && *b.Quota == *other.Quota
}

func (c Charge) validate(requestID, userID, model string) error {
	if c.RequestID != "" && c.RequestID != requestID || c.UserID != userID && !(c.Status == "not_received" && c.UserID == "") || c.Model != "" && c.Model != model {
		return errors.New("OPS charge identity mismatch")
	}
	switch c.Status {
	case "charged", "pending", "failed", "not_received", "station_billed":
	default:
		return errors.New("invalid OPS charge status")
	}
	for i, value := range []*string{c.Amount, c.RefundAmount, c.NetAmount, c.StationAmount, c.BalanceAfter, c.Balance} {
		if value != nil {
			if _, err := amount(*value, i >= 4); err != nil {
				return err
			}
		}
	}
	if c.Status == "charged" {
		if c.LedgerEntryID != "" {
			if id, err := c.LedgerEntryID.Int64(); err != nil || id <= 0 {
				return errors.New("invalid OPS charge ledger position")
			}
		}
		if c.Amount == nil || c.RefundAmount == nil || c.NetAmount == nil {
			return errors.New("incomplete OPS charge")
		}
		a, _ := amount(*c.Amount, false)
		r, _ := amount(*c.RefundAmount, false)
		n, _ := amount(*c.NetAmount, false)
		if !a.Sub(r).Equal(n) {
			return errors.New("inconsistent OPS refund")
		}
		if _, err := time.Parse(time.RFC3339Nano, c.ChargedAt); err != nil {
			return errors.New("invalid OPS charge time")
		}
	}
	return nil
}

func (b Balance) validate() error {
	d, err := amount(b.Amount, true)
	id, idErr := b.LastEntryID.Int64()
	if err != nil || !validUser(b.UserID) || idErr != nil || id < 0 || id == 0 && !d.IsZero() || b.Quota == nil || *b.Quota > common.MaxWalletQuota || *b.Quota < -common.MaxWalletQuota || b.Negative != d.IsNegative() {
		return errors.New("invalid OPS balance")
	}
	if _, err = time.Parse(time.RFC3339Nano, b.UpdatedAt); err != nil {
		return errors.New("invalid OPS balance time")
	}
	if b.Station != nil {
		rate, err := amount(b.Station.ExchangeRate, false)
		if err != nil || !rate.IsPositive() || b.Station.QuotaPerUnit <= 0 || b.Station.QuotaPerUnit > common.MaxWalletQuota {
			return errors.New("invalid OPS balance conversion")
		}
		// OPS truncates quota toward zero; the CNY amount determines whether
		// the balance is negative even when its quota rounds to zero.
		q, _ := d.Mul(decimal.NewFromInt(b.Station.QuotaPerUnit)).QuoRem(rate, 0)
		if !q.Equal(decimal.NewFromInt(*b.Quota)) {
			return errors.New("inconsistent OPS balance quota")
		}
	}
	return nil
}

// Receipt deliberately excludes wallet values from an API-key-scoped receipt.
// The historical balanceAfter in a charge must never replace a current balance.
func (c Charge) Receipt(checked time.Time) businessapi.Billing {
	currency := c.Currency
	if currency == "" {
		currency = "CNY"
	}
	result := businessapi.Billing{Source: "ops", Currency: currency, Status: "pending", ChargeStatus: c.Status, CheckedAt: checked.UTC().Format(time.RFC3339)}
	retry := 1
	result.RetryAfterSeconds = &retry
	switch c.Status {
	case "charged":
		result.Status = "available"
		result.Amount = c.Amount
		result.RefundAmount = c.RefundAmount
		result.NetAmount = c.NetAmount
		result.ChargedAt = c.ChargedAt
		result.RetryAfterSeconds = nil
	case "pending":
		result.Reason = "awaiting_ops_charge"
	case "not_received":
		result.Reason = "ops_usage_not_received"
	case "failed":
		result.Status = "unavailable"
		result.Reason = "ops_charge_failed"
		retry = 300
	case "station_billed":
		result.Status = "unavailable"
		result.Reason = "station_billed"
		result.RetryAfterSeconds = nil
	}
	return result
}
