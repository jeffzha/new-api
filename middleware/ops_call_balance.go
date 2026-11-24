package middleware

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/gin-gonic/gin"
)

// Use only on billable submissions, never on fee/history reads, task polling,
// refunds or settlement of a request that has already been accepted.
func OPSCallBalanceGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !opsauthority.Enabled() {
			c.Next()
			return
		}
		err := opsbilling.ErrBalanceUnavailable
		if opsbilling.Default != nil {
			err = opsbilling.Default.CheckCall(c.Request.Context(), c.GetInt("id"), time.Now())
		}
		if err == nil {
			defer opsbilling.Default.InvalidateCall(c.GetInt("id"))
			c.Next()
			return
		}
		status, code := http.StatusServiceUnavailable, "ops_balance_unavailable"
		c.Header("Retry-After", "2")
		if errors.Is(err, opsbilling.ErrInsufficientBalance) {
			status, code = http.StatusForbidden, "insufficient_user_quota"
		}
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "billing_error", "code": code, "message": err.Error()}})
	}
}
