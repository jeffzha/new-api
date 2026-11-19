package opsfees

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/service/opsclient"
)

// feeRecoveryDelay handles brief outages without turning one failed read into
// minutes of stale fees. The existing shared lease and request gap still limit
// all blue/green instances to one reader per connection. It never retries a
// model invocation, moves a cursor, or overrides an OPS Retry-After response.
func feeRecoveryDelay(err error, failures int, requestGap time.Duration) time.Duration {
	wait := min(3*time.Second*time.Duration(1<<min(max(failures-1, 0), 7)), 5*time.Minute)
	var response *opsclient.ReadError
	if errors.As(err, &response) {
		switch response.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			wait = max(wait, 5*time.Minute)
		case http.StatusTooManyRequests:
			wait = max(wait, time.Minute)
		default:
			if response.Status < 500 && response.Status != http.StatusRequestTimeout {
				wait = max(wait, 5*time.Minute)
			}
		}
		wait = max(wait, response.RetryAfter)
	}
	return max(wait, requestGap)
}
