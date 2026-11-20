// ops-wallet-schema prepares only the additive OPS wallet tables in the
// Gateway primary database. It never starts workers or switches wallet custody.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/model"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := prepare(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "OPS wallet schema preparation failed; active services and wallet authority are unchanged")
		os.Exit(1)
	}
}

func prepare(ctx context.Context) error {
	db, err := model.OpenOPSWalletOperatorDB(ctx)
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	// Index definitions live with their models and use portable GORM tags.
	// Main database only; the separate log database is not involved.
	// idx_ops_wallet_pending(delivered_at,id): funding outbox recovery.
	// uidx_ops_wallet_event(event_id), unique: funding delivery idempotency.
	// idx_ops_hold_reconcile(scope,state,id): bounded hold reconciliation.
	// idx_ops_hold_account(account_id): per-account outstanding holds.
	// idx_ops_usage_pending(scope,delivered_at,id): durable usage outbox.
	tables := []any{
		&model.OPSWalletEvent{}, &model.OPSWalletAdmission{},
		&model.OPSWalletHold{}, &model.OPSWalletUsage{},
		&model.OPSAgencyCursor{}, &model.OPSAgencyAccount{},
		&model.OPSAgencyOpening{}, &model.OPSAgencyReceipt{}, &model.OPSAgencyCharge{},
	}
	if err := db.WithContext(ctx).AutoMigrate(tables...); err != nil {
		return err
	}
	for _, table := range tables {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("required wallet table missing")
		}
	}
	fmt.Println("Additive Gateway primary wallet schema prepared; verify target index catalog before traffic")
	return nil
}
