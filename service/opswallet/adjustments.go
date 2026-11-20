// Package opswallet adapts administrative money changes to the credential-owned
// OPS ledger. It never writes User.Quota or creates a second adjustment event.
package opswallet

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
)

const Path = "/api/v1/catalog/v1/balance-changes"

var operationID = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,160}$`)
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,12})(\.[0-9]{1,6})?$`)

func Enabled() bool {
	return opsauthority.Enabled() && os.Getenv("OPS_BALANCE_CHANGES_ENABLED") == "true"
}

type Adjustment struct {
	RequestID string `json:"requestId"`
	UserID    string `json:"userId"`
	Username  string `json:"username,omitempty"`
	Operation string `json:"operation"`
	Amount    string `json:"amount"`
	Operator  string `json:"operator"`
	Reason    string `json:"reason"`
}

type Result struct {
	Currency      string `json:"currency,omitempty"`
	RequestID     string `json:"requestId"`
	UserID        string `json:"userId"`
	Operation     string `json:"operation"`
	Amount        string `json:"amount"`
	Change        string `json:"change"`
	BalanceBefore string `json:"balanceBefore"`
	Balance       string `json:"balance"`
	Quota         *int64 `json:"balanceQuota"`
	LedgerEntryID int64  `json:"ledgerEntryId"`
	RecordedAt    string `json:"recordedAt"`
	Replayed      bool   `json:"replayed"`
	Station       struct {
		QuotaPerUnit int64  `json:"quotaPerUnit"`
		ExchangeRate string `json:"usdExchangeRate"`
	} `json:"station"`
}

func Validate(a Adjustment) error {
	id, err := strconv.Atoi(a.UserID)
	if !operationID.MatchString(a.RequestID) || err != nil || id <= 0 || strconv.Itoa(id) != a.UserID || !amountPattern.MatchString(a.Amount) {
		return errors.New("余额调整信息不完整，请重新填写")
	}
	n, _ := decimal.NewFromString(a.Amount)
	if n.GreaterThan(decimal.NewFromInt(1_000_000_000)) {
		return errors.New("单次调整金额不能超过十亿")
	}
	if a.Operation != "set" && a.Operation != "add" && a.Operation != "subtract" || a.Operation != "set" && !n.IsPositive() {
		return errors.New("请输入大于零的调整金额")
	}
	if strings.TrimSpace(a.Operator) == "" || utf8.RuneCountInString(a.Operator) > 100 || strings.TrimSpace(a.Reason) == "" || utf8.RuneCountInString(a.Reason) > 500 {
		return errors.New("请填写余额调整原因")
	}
	return nil
}

func Adjust(ctx context.Context, client *opsclient.Client, a Adjustment) (*Result, error) {
	currency := opsbilling.Currency()
	if currency != "CNY" && currency != "USD" {
		return nil, errors.New("invalid OPS wallet currency")
	}
	if err := Validate(a); err != nil {
		return nil, err
	}
	body, err := common.Marshal(a)
	if err != nil {
		return nil, err
	}
	raw, err := client.Post(ctx, Path, body, time.Now())
	if err != nil {
		return nil, err
	}
	var response struct {
		Data *Result `json:"data"`
	}
	if common.Unmarshal(raw, &response) != nil || response.Data == nil {
		return nil, errors.New("运营平台返回结果无法确认，请重试原操作")
	}
	r := response.Data
	if currency == "USD" && r.Currency != currency || r.Currency != "" && r.Currency != currency {
		return nil, errors.New("OPS adjustment currency mismatch")
	}
	for _, value := range []string{r.Amount, strings.TrimPrefix(r.BalanceBefore, "-"), strings.TrimPrefix(r.Balance, "-"), strings.TrimPrefix(r.Change, "-")} {
		if !amountPattern.MatchString(value) {
			return nil, errors.New("运营平台返回金额无效，请重试原操作")
		}
	}
	requested, _ := decimal.NewFromString(a.Amount)
	actual, amountErr := decimal.NewFromString(r.Amount)
	before, beforeErr := decimal.NewFromString(r.BalanceBefore)
	after, afterErr := decimal.NewFromString(r.Balance)
	change, changeErr := decimal.NewFromString(r.Change)
	_, dateErr := time.Parse(time.RFC3339Nano, r.RecordedAt)
	if r.RequestID != a.RequestID || r.UserID != a.UserID || r.Operation != a.Operation || amountErr != nil || !actual.Equal(requested) || beforeErr != nil || afterErr != nil || changeErr != nil || !before.Add(change).Equal(after) || r.LedgerEntryID <= 0 || r.Quota == nil || dateErr != nil || *r.Quota > common.MaxWalletQuota || *r.Quota < -common.MaxWalletQuota {
		return nil, errors.New("运营平台返回结果无法确认，请重试原操作")
	}
	expected := requested
	if a.Operation == "subtract" {
		expected = requested.Neg()
	}
	if a.Operation == "set" {
		expected = requested.Sub(before)
	}
	if !expected.Equal(change) {
		return nil, errors.New("运营平台余额变化无法确认，请重试原操作")
	}
	rate, rateErr := decimal.NewFromString(r.Station.ExchangeRate)
	if rateErr != nil || !rate.IsPositive() || r.Station.QuotaPerUnit <= 0 {
		return nil, errors.New("运营平台额度换算无法确认，请重试原操作")
	}
	converted := after.Mul(decimal.NewFromInt(r.Station.QuotaPerUnit)).Div(rate).Truncate(0)
	if !converted.Equal(decimal.NewFromInt(*r.Quota)) {
		return nil, errors.New("运营平台额度换算无法确认，请重试原操作")
	}
	if currency == "USD" && (r.Station.QuotaPerUnit != 1_000_000 || !rate.Equal(decimal.NewFromInt(1))) {
		return nil, errors.New("invalid native OPS adjustment conversion")
	}
	r.Currency = currency
	// Only the OPS receipt's monotonically increasing ledger position can update
	// the display cache. Replaying an old operation never restores an old balance.
	if opsbilling.Default != nil && !r.Replayed {
		opsbilling.Default.AcceptAdjustment(r.UserID, r.Balance, *r.Quota, r.LedgerEntryID, r.RecordedAt)
	}
	return r, nil
}

// A definite rejection has not changed money and may be edited. Transport
// failures and unverified responses must keep the original request unchanged.
func CanEditAfterError(err error) bool {
	var remote *opsclient.ReadError
	if !errors.As(err, &remote) {
		return false
	}
	if remote.Code == "request_id_conflict" {
		return false
	}
	return remote.Status >= 400 && remote.Status < 500 && remote.Status != 408 && remote.Status != 429
}

func ErrorMessage(err error) string {
	var remote *opsclient.ReadError
	if !errors.As(err, &remote) {
		return "暂时无法确认调整结果，请重试原操作，勿重复创建调整"
	}
	switch remote.Code {
	case "insufficient_balance":
		return "运营平台余额不足，无法扣减该金额"
	case "request_id_conflict":
		return "这次调整的信息与原操作不一致，请核对原操作"
	case "user_removed":
		return "该用户已删除，无法调整余额"
	case "ops_balance_not_started", "project_wallet_disabled":
		return "该项目尚未开通余额调整，请联系管理员"
	case "scope_forbidden", "scope_required", "permission_denied":
		return "尚未开通余额调整权限，请联系管理员"
	}
	return "暂时无法确认调整结果，请重试原操作，勿重复创建调整"
}
