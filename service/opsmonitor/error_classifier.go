package opsmonitor

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/types"
)

type relayErrorClassification struct {
	owner              string
	businessLimited    bool
	upstreamStatusCode int
}

func classifyRelayError(err *types.NewAPIError) relayErrorClassification {
	if err == nil {
		return relayErrorClassification{}
	}
	code := err.GetErrorCode()
	typeName := err.GetErrorType()
	status := err.StatusCode
	classification := relayErrorClassification{owner: "gateway"}

	providerCode := code == types.ErrorCodeDoRequestFailed ||
		code == types.ErrorCodeReadResponseBodyFailed ||
		code == types.ErrorCodeBadResponseStatusCode ||
		code == types.ErrorCodeBadResponse ||
		code == types.ErrorCodeBadResponseBody ||
		code == types.ErrorCodeEmptyResponse ||
		code == types.ErrorCodeAwsInvokeError
	if typeName != types.ErrorTypeNewAPIError || providerCode {
		classification.owner = "provider"
		classification.upstreamStatusCode = status
	}

	localBusinessCode := code == types.ErrorCodeInsufficientUserQuota ||
		code == types.ErrorCodePreConsumeTokenQuotaFailed ||
		code == types.ErrorCodeModelPriceError ||
		code == types.ErrorCodeInvalidRequest ||
		code == types.ErrorCodeBadRequestBody ||
		code == types.ErrorCodeAccessDenied ||
		code == types.ErrorCodeGetChannelFailed ||
		strings.Contains(string(code), "quota") ||
		strings.Contains(string(code), "concurrency")
	classification.businessLimited = classification.owner == "gateway" && (localBusinessCode ||
		status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests)
	return classification
}
