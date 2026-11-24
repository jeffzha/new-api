// ops-wallet-funding prepares only the transactional funding outbox. This
// command cannot transfer custody, change balances, or enable a runtime flag.
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "prepare" && os.Args[1] != "verify") {
		fmt.Fprintln(os.Stderr, "usage: ops-wallet-funding prepare|verify (explicit SQL_DSN required)")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := model.OpenCNIntegrityOperatorDB(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OPS funding database open failed; existing services unchanged")
		os.Exit(1)
	}
	pool, err := db.DB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "OPS funding database unavailable")
		os.Exit(1)
	}
	defer pool.Close()
	names := []string{"uidx_ops_wallet_event", "idx_ops_wallet_pending"}
	receipt := &model.OPSFundingSchemaReceipt{Created: []string{}, Skipped: []string{}}
	if os.Args[1] == "prepare" {
		receipt, err = model.PrepareOPSWalletFundingSchemaReceipt(ctx, db)
	} else {
		err = model.VerifyOPSWalletFundingSchema(db.WithContext(ctx))
		if err == nil {
			receipt.Skipped = names
		}
	}
	if err != nil {
		// Driver errors may contain connection details. Keep the operator
		// output non-secret and never start a release on an incomplete gate.
		fmt.Fprintln(os.Stderr, "OPS funding schema/index gate failed; release must stop")
		os.Exit(1)
	}
	raw, err := common.Marshal(map[string]any{
		"ready": true, "scope": "gateway-funding-outbox-schema-only",
		"mode": os.Args[1], "engine": db.Dialector.Name(),
		"checkedAt": time.Now().UTC().Format(time.RFC3339),
		"indexes":   names, "created": receipt.Created, "skipped": receipt.Skipped,
		"release":              common.Version,
		"definitionHash":       fmt.Sprintf("%x", sha256.Sum256([]byte("ops_wallet_events:v1;unique:uidx_ops_wallet_event(event_id);btree:idx_ops_wallet_pending(scope,delivered_at,id)"))),
		"walletCustodyChanged": false,
	})
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(string(raw))
}
