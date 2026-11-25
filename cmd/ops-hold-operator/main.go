// ops-hold-operator inspects and reconciles durable holds against explicitly
// reviewed provider evidence. Schema and drained owner preparation require
// explicit flags; the default operation is read-only and never calls a model.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsreserve"
)

func main() {
	request := flag.String("request", "", "request ID; empty lists up to 100 unresolved holds")
	decision := flag.String("decision", "", "confirmed_no_charge or posted_charge; empty is read-only")
	evidence := flag.String("evidence", "", "provider terminal evidence reference, never a token")
	reviewer := flag.String("reviewer", "", "operator identity")
	quota := flag.Int("quota", 0, "reviewed final independent API-key quota")
	prepare := flag.Bool("prepare", false, "prepare only reservation recovery schema and required index")
	from := flag.String("handoff-from", "", "exact current uploader owner; requires independently verified drain")
	to := flag.String("handoff-to", "", "explicit compatible successor for uploader and async settlement")
	state := flag.Int("state", 0, "existing uploader state ID for owner handoff")
	flag.Parse()
	if *prepare || *to != "" {
		if *decision != "" || *request != "" {
			fmt.Fprintln(os.Stderr, "schema/owner operations cannot be combined with reconciliation")
			os.Exit(1)
		}
		if err := prepareRelease(*prepare, *state, *from, *to); err != nil {
			fmt.Fprintln(os.Stderr, "reservation preparation/handoff failed; current service must remain available")
			os.Exit(1)
		}
		fmt.Println("reservation preparation/handoff ready")
		return
	}
	if err := run(*request, *decision, *evidence, *reviewer, *quota); err != nil {
		fmt.Fprintln(os.Stderr, "OPS hold operation failed; no automatic force release is permitted")
		os.Exit(1)
	}
}

func prepareRelease(schema bool, state int, previous, successor string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := model.OpenCNIntegrityOperatorDB(ctx)
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	if schema {
		if err := model.PrepareOPSHoldSchema(db.WithContext(ctx)); err != nil {
			return err
		}
		if db.Migrator().HasTable(&model.Midjourney{}) {
			for _, field := range []string{"OPSReservationGateway", "OPSReservationRequest", "OPSReservationBaseURL"} {
				if db.Migrator().HasColumn(&model.Midjourney{}, field) {
					continue
				}
				if err := db.WithContext(ctx).Migrator().AddColumn(&model.Midjourney{}, field); err != nil {
					return err
				}
			}
		}
	}
	if successor != "" {
		return model.TransferOPSSettlementOwner(ctx, db, state, previous, successor)
	}
	return nil
}

func run(request, decision, evidence, reviewer string, quota int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := model.OpenCNIntegrityOperatorDB(ctx)
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	model.DB = db
	client, err := opsreserve.Configured()
	if err != nil {
		return err
	}
	if decision != "" {
		if request == "" {
			return opsreserve.ErrUnavailable
		}
		if err := client.ResolveReviewed(ctx, request, decision, evidence, reviewer, quota); err != nil {
			return err
		}
	}
	query := db.WithContext(ctx).Where("gateway = ?", client.Transport.KeyID)
	if request != "" {
		query = query.Where("request = ?", request)
	} else {
		query = query.Where("reconciled = ?", false)
	}
	var rows []opsreserve.Intent
	if err := query.Order("updated").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	raw, err := common.Marshal(rows)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(raw, 10))
	return err
}
