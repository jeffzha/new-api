// Package upstreamalerts describes provider failures for the OPS notification
// receiver. It does not send mail, change routing, or make network requests.
package upstreamalerts

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

const BalanceInsufficient = "upstream_balance_insufficient"

// Alert is stored under logs.other.admin_info.upstream_alert and copied to
// usage-events[].extra.upstream_alert. Unknown balances are explicitly null;
// neither channel balance caches nor local customer quotas are authoritative.
type Alert struct {
	Type          string  `json:"type"`
	RequestID     string  `json:"request_id"`
	ChannelID     int     `json:"channel_id"`
	ErrorCode     string  `json:"error_code"`
	StatusCode    int     `json:"status_code"`
	ErrorSummary  string  `json:"error_summary"`
	Balance       *string `json:"balance"`
	Currency      *string `json:"currency"`
	BalanceSource string  `json:"balance_source"`
}

var safeErrorCode = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)

// FromRelayError requires a provider response and explicit financial evidence.
// HTTP 402/403/429 and generic quota/rate-limit errors alone are insufficient.
func FromRelayError(err *types.NewAPIError) *Alert {
	if err == nil || err.StatusCode < 400 || err.StatusCode > 599 {
		return nil
	}
	code := string(err.GetErrorCode())
	if err.GetErrorType() == types.ErrorTypeNewAPIError {
		switch err.GetErrorCode() {
		case types.ErrorCodeBadResponseStatusCode, types.ErrorCodeBadResponse, types.ErrorCodeBadResponseBody:
		default:
			return nil
		}
	}
	matched := false
	switch strings.ToLower(code) {
	case "insufficient_balance", "balance_not_enough", "insufficient_funds",
		"insufficient_credit", "insufficient_credits", "credit_balance_too_low",
		"arrearage", "account.arrearage", "insufficientbalance":
		matched = true
	}
	message := strings.ToLower(err.Error())
	if len(message) > 8192 {
		message = message[:8192]
	}
	// OpenAI's financial quota error is distinct from rate_limit_exceeded.
	if strings.EqualFold(code, "insufficient_quota") && strings.Contains(message, "billing") &&
		strings.Contains(message, "current quota") {
		matched = true
	}
	for _, phrase := range []string{
		"insufficient balance", "insufficient account balance", "insufficient funds",
		"insufficient credit", "credit balance is too low", "credit balance too low",
		"balance is insufficient", "balance is not enough", "account is in arrears",
		"余额不足", "余额已耗尽", "余额已用完", "账户欠费", "账号欠费",
	} {
		if strings.Contains(message, phrase) {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	if !safeErrorCode.MatchString(code) {
		code = "upstream_balance_insufficient"
	}
	return &Alert{Type: BalanceInsufficient, ErrorCode: code, StatusCode: err.StatusCode,
		ErrorSummary: "上游返回账户余额或付费额度不足", BalanceSource: "unavailable"}
}

// AppendRelayError returns true when a durable error log is required even if
// general error logging was disabled. Only trusted relay errors enter here;
// no client-supplied log fields are used to create financial alerts.
func AppendRelayError(other *model.LogOther, err *types.NewAPIError, requestID string, channelID int) bool {
	alert := FromRelayError(err)
	if alert == nil || other == nil || channelID <= 0 {
		return false
	}
	alert.RequestID, alert.ChannelID = requestID, channelID
	other.SetAdmin("upstream_alert", alert)
	return true
}
