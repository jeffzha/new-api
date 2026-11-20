// ops-wallet-cutover is an offline native-USD wallet handover operator. It is
// deliberately separate from application startup and never takes custody back.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/QuantumNous/new-api/service/opswallet"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		// Never echo DSNs, credentials, account data or remote response bodies.
		fmt.Fprintln(os.Stderr, "wallet cutover stopped; keep writers stopped and retain the private journal for reconciliation")
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help") {
		_, err := fmt.Fprintln(out, "ops-wallet-cutover <prepare|apply> --state /private/opening.json --writers-stopped --evidence CHANGE-ID --scope OVERSEAS-ID\nStop and drain all Gateway/Agency/OPS writers, collectors and pending payment callbacks first. Prepare freezes legacy source quota in the database, snapshots every user and prints the review digest.\nApply additionally needs --review-sha256 DIGEST --approved-by OPERATOR --output /private/approved.json. It imports balances, checks each user and explicitly hands custody to OPS. Retain the state file for all retries; failure NEVER unfreezes money or takes custody back.\nreview-held --state /private/held.json --scope OVERSEAS-ID --evidence APPROVAL-ID --approved-by OPERATOR --bill-ids 5,6 creates a read-only pinned approval retaining those held terminal bills, without settling or changing balances. Configure OPS_PROJECT_SCOPE and pin OPS_WALLET_HISTORICAL_REVIEW_FILE/SHA256 before prepare; retain that manifest for apply and every custody startup.\nLinux operator. Environment: explicit SQL_DSN (SQLite: existing absolute SQLITE_PATH), OPS_PUSH_URL, OPS_PUSH_KEY_ID, OPS_PUSH_SECRET. No automatic application migrations. After verification, deploy OPS custody configuration with OPS_WALLET_OPENING_FILE and the printed OPS_WALLET_OPENING_SHA256.")
		return err
	}
	if len(args) < 1 {
		return errors.New("command required")
	}
	flags := flag.NewFlagSet("ops-wallet-cutover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var state, scope, evidence, digest, operator, output string
	var stopped bool
	flags.StringVar(&state, "state", "", "persistent private migration journal")
	flags.StringVar(&scope, "scope", "", "overseas station scope")
	flags.StringVar(&evidence, "evidence", "", "offline writer drain evidence")
	flags.BoolVar(&stopped, "writers-stopped", false, "attest that all writers and collectors are stopped and drained")
	flags.StringVar(&digest, "review-sha256", "", "reviewed frozen snapshot digest")
	flags.StringVar(&operator, "approved-by", "", "operator approval identity")
	flags.StringVar(&output, "output", "", "private verified opening approval")
	var billIDs string
	flags.StringVar(&billIDs, "bill-ids", "", "explicit comma-separated held provider bill IDs")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || state == "" || evidence == "" || scope == "" || args[0] != "prepare" && args[0] != "apply" && args[0] != "review-held" || args[0] != "review-held" && !stopped || args[0] == "review-held" && (billIDs == "" || operator == "") || args[0] == "apply" && (digest == "" || operator == "" || output == "" || output == state) {
		return errors.New("invalid operator arguments")
	}
	if opsclient.ValidateBase(os.Getenv("OPS_PUSH_URL")) != nil || os.Getenv("OPS_PUSH_KEY_ID") == "" || len(os.Getenv("OPS_PUSH_SECRET")) < 16 {
		return errors.New("invalid private station configuration")
	}
	// Lock file creation is exclusive; a crashed process leaves it in place.
	// Remove only after verifying that no earlier operator process is alive.
	lock, err := os.OpenFile(state+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(lock.Name())
	if _, err = fmt.Fprintf(lock, "%d\n", os.Getpid()); err != nil {
		_ = lock.Close()
		return err
	}
	if err = lock.Sync(); err != nil {
		_ = lock.Close()
		return err
	}
	if err = lock.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
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
	client := opsclient.New(os.Getenv("OPS_PUSH_URL"), os.Getenv("OPS_PUSH_KEY_ID"), os.Getenv("OPS_PUSH_SECRET"), 10*time.Second)
	if args[0] == "review-held" {
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			return errors.New("historical review file already exists")
		}
		var ids []int64
		for item := range strings.SplitSeq(billIDs, ",") {
			id, err := strconv.ParseInt(item, 10, 64)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		r, err := opswallet.ApproveHistoricalReview(ctx, db, scope, client.KeyID, ids, operator, evidence)
		if err != nil {
			return err
		}
		if err := opswallet.SaveMigrationFile(state, r); err != nil {
			return err
		}
		raw, err := common.Marshal(r)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "held bills retained=%d OPS_WALLET_HISTORICAL_REVIEW_SHA256=%x; no balances or bills changed\n", len(ids), sha256.Sum256(raw))
		return err
	}
	if args[0] == "prepare" {
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			return errors.New("journal already exists or cannot be inspected; do not overwrite")
		}
		m, err := opswallet.PrepareOpening(ctx, db, client, scope, evidence, stopped)
		if err != nil {
			return err
		}
		if err := opswallet.SaveMigrationFile(state, m); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "prepared: users=%d reviewSha256=%s; source money remains frozen\n", len(m.Users), m.SnapshotSHA256)
		return err
	}
	file, err := os.Open(state)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4*1024*1024+1))
	_ = file.Close()
	if err != nil || len(raw) > 4*1024*1024 {
		return errors.New("invalid migration journal size")
	}
	var m opswallet.OpeningMigration
	if common.Unmarshal(raw, &m) != nil || m.Scope != scope || m.WriterEvidence != evidence {
		return errors.New("migration journal identity differs")
	}
	proof, err := opswallet.ApplyOpening(ctx, db, client, &m, digest, operator, func(m *opswallet.OpeningMigration) error { return opswallet.SaveMigrationFile(state, m) })
	if err != nil {
		return err
	}
	if err := opswallet.SaveMigrationFile(output, proof); err != nil {
		return err
	}
	raw, err = common.Marshal(proof)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "verified: users=%d currency=USD OPS_WALLET_OPENING_SHA256=%x; keep the source wallet guard installed\n", len(proof.Users), sha256.Sum256(raw))
	return err
}
