package opswallet

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"gorm.io/gorm"
)

// Reconciler retains unresolved holds across restarts. Its cursor only bounds
// a scan; reaching the end starts another pass without expiring any request.
type Reconciler struct {
	DB         *gorm.DB
	Reader     *opsbilling.Reader
	after      string
	relayAfter string
}

func (r *Reconciler) Step(ctx context.Context) error {
	if r.DB == nil || r.Reader == nil {
		return opsbilling.ErrBalanceUnavailable
	}
	intents, err := model.PendingOPSRelaySettlements(r.DB.WithContext(ctx), r.relayAfter, 20)
	if err != nil {
		return err
	}
	var failures []error
	if len(intents) == 0 {
		r.relayAfter = ""
	}
	for _, intent := range intents {
		if err := model.FinalizeOPSRelay(r.DB.WithContext(ctx), intent.RequestID, intent.UserID, intent.SettlementQuota, []byte(intent.SettlementEvent)); err != nil {
			failures = append(failures, err)
		} else {
			r.Reader.InvalidateCall(intent.UserID)
			opsnotify.Committed(intent.Scope)
			if err := model.InvalidateUserTokensCache(intent.UserID); err != nil {
				common.SysError("OPS recovered settlement token cache invalidation failed")
			}
		}
		r.relayAfter = intent.ID
	}
	rows, err := model.PendingOPSWalletHolds(r.DB.WithContext(ctx), r.after, 20)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		r.after = ""
		return errors.Join(failures...)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		request := row.ChargeRequestID
		if request == "" {
			request = row.RequestID
		}
		entry, posted := r.Reader.ChargedLedgerEntry(request, row.UserID, row.Model, time.Now())
		if !posted {
			r.after = row.ID
			continue
		}
		position, err := r.Reader.AdmissionPosition(ctx, row.UserID, time.Now())
		if err == nil {
			err = model.ReconcileOPSWallet(r.DB.WithContext(ctx), row.RequestID, row.UserID, row.Model, entry, position)
		}
		if err != nil {
			r.Reader.InvalidateCall(row.UserID)
			failures = append(failures, err)
		}
		r.after = row.ID
	}
	return errors.Join(failures...)
}
