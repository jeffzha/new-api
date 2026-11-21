// ops-commission-review replays one explicitly reviewed existing inbox range.
// It starts no HTTP server, migrations, timers, source fetches or wallet writers.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opswallet"
)

func main() {
	user := flag.Int("user", 0, "explicit reviewed user ID")
	first := flag.Int64("first", 0, "inclusive first source entry")
	last := flag.Int64("last", 0, "inclusive last source entry, at most 100")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := model.OpenOPSWalletOperatorDB(ctx)
	if err == nil {
		pool, e := db.DB()
		err = e
		if e == nil {
			defer pool.Close()
			var count int
			count, err = opswallet.ReplayReviewedCommissionInbox(ctx, db, *user, *first, *last)
			if err == nil {
				fmt.Printf("reviewed_receipts=%d\n", count)
				return
			}
		}
	}
	fmt.Fprintln(os.Stderr, "reviewed replay failed; transaction rolled back; original evidence retained")
	os.Exit(1)
}
