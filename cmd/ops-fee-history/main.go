// ops-fee-history fills missing display fees without application startup,
// migrations, usage replay, wallet changes, or changes to the live fee cursor.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsfees"
)

func main() {
	since := flag.String("since", "", "reviewed RFC3339 history lower bound")
	connection := flag.String("connection", "", "reviewed current fee connection SHA256")
	pages := flag.Int("max-pages", 200, "maximum steps per run, 1..500")
	gap := flag.Duration("gap", 10*time.Second, "minimum history read gap, 10s..1m")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, *since, *connection, *pages, *gap); err != nil {
		fmt.Fprintln(os.Stderr, "fee history stopped; progress retained; no money operations performed")
		os.Exit(1)
	}
}

func run(ctx context.Context, since, connection string, pages int, gap time.Duration) error {
	start, err := time.Parse(time.RFC3339, since)
	if err != nil || start.IsZero() || start.After(time.Now()) || pages < 1 || pages > 500 || gap < 10*time.Second || gap > time.Minute {
		return errors.New("invalid history bounds")
	}
	cfg, err := opsfees.LoadConfig()
	if err != nil || connection == "" || connection != cfg.Connection {
		return errors.New("fee connection differs from reviewed target")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	db, err := model.OpenOPSWalletOperatorDB(ctx)
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	if !db.Migrator().HasTable(&model.OpsFee{}) || !db.Migrator().HasTable(&model.OpsFeeSync{}) {
		return errors.New("existing fee tables required; no automatic migrations")
	}
	w := opsfees.New(db, db, cfg)
	for step := range pages {
		r, err := w.HistoryStep(ctx, start, gap)
		if err != nil {
			return err
		}
		fmt.Printf("step=%d results=%d complete=%t\n", step+1, r.Rows, r.Complete)
		if r.Complete {
			return nil
		}
		timer := time.NewTimer(r.Wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("bounded run complete; resume with identical arguments")
}
