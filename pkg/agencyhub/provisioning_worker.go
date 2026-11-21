package agencyhub

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// RunProvisioningWorker owns only the existing binding queue. It does not start
// legacy settlement, reconciliation, invoicing, exports or wallet workers.
// Existing per-job CAS/fencing and in-flight task checks remain authoritative.
func (a *App) RunProvisioningWorker(ctx context.Context, once bool) error {
	if a.db == nil {
		return fmt.Errorf("provisioning database unavailable")
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		tickCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		worker := App{db: a.db.WithContext(tickCtx), config: a.config}
		_, err := worker.ProcessProvisioningJobs(5)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if once {
			return err
		}
		delay := 2 * time.Second
		if err != nil {
			// Avoid logging SQL/connection or user data from driver errors.
			common.SysError("agency provisioning queue pass failed; retrying with backoff")
			delay = 10 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
