package opswallet

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsbilling"
)

// StartConfigured starts only delivery and recovery. It cannot transfer
// custody or write the historical local wallet.
func StartConfigured(ctx context.Context) (func(), error) {
	if !opsauthority.Enabled() {
		return func() {}, nil
	}
	if opsbilling.Default == nil || opsbilling.Default.Currency != "USD" || model.DB == nil {
		return nil, errors.New("OPS wallet recovery requires the USD balance reader")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	worker := &Reconciler{DB: model.DB, Reader: opsbilling.Default}
	commissions := &CommissionProjector{DB: model.DB, Client: opsbilling.Default.Client}
	if os.Getenv("OPS_DISTRIBUTOR_FINANCE_AUTHORITY") != "gateway" {
		cancel()
		return nil, errors.New("OPS custody requires explicit Gateway distributor finance authority")
	}
	if err := commissions.CheckSource(ctx); err != nil {
		cancel()
		return nil, errors.New("OPS source commission readiness failed: " + err.Error())
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if err := DeliverFunding(ctx, worker.DB.WithContext(ctx), worker.Reader.Client); err != nil && ctx.Err() == nil {
				common.SysError("OPS funding delivery retained pending events")
			}
			if ctx.Err() != nil {
				return
			}
			if err := worker.Step(ctx); err != nil && ctx.Err() == nil {
				common.SysError("OPS wallet recovery retained unresolved settlements")
			}
			if err := commissions.Step(ctx); err != nil && ctx.Err() == nil {
				common.SysError("OPS source commission retained unresolved receipts: " + err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}, nil
}
