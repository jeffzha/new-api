package opsmonitor

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// ObserveGatewayRejection records a gateway-side rejection that happens before
// relay metadata exists (for example the system overload gate), so the
// operational request store keeps an explicit error code, error owner and
// summary instead of an anonymous 503.
func ObserveGatewayRejection(c *gin.Context, statusCode int, errorType string, errorCode string, summary string) {
	observation := getObservation(c)
	if observation == nil {
		return
	}
	observation.mu.Lock()
	defer observation.mu.Unlock()
	observation.errorType = errorType
	observation.errorCode = errorCode
	observation.errorSummary = common.LocalLogPreview(summary)
	observation.errorOwner = "gateway"
	observation.businessLimited = statusCode == http.StatusBadRequest ||
		statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusTooManyRequests
}
