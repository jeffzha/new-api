// ops-hold-operator inspects and reconciles durable holds against explicitly
// reviewed provider evidence. It never migrates a database or invokes a model.
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
	flag.Parse()
	if err := run(*request, *decision, *evidence, *reviewer, *quota); err != nil {
		fmt.Fprintln(os.Stderr, "OPS hold operation failed; no automatic force release is permitted")
		os.Exit(1)
	}
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
