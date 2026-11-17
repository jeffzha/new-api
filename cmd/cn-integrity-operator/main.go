package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type operatorOptions struct {
	command, gateway, epoch, evidence, inventory, input, parent, from, to, state, stopState string
	generation                                                                              int64
	user                                                                                    int
	interval, timeout                                                                       time.Duration
}

type freezeJournal struct {
	Schema       string                          `json:"schema"`
	GatewayID    string                          `json:"gatewayId"`
	EpochID      string                          `json:"epochId"`
	Generation   int64                           `json:"expectedGeneration"`
	Parent       string                          `json:"expectedParentProofId"`
	CoverageFrom string                          `json:"coverageFrom"`
	Pending      *model.CNIntegrityFreezeRequest `json:"pending"`
	EvidenceID   string                          `json:"operatorEvidenceId"`
	LastRequest  *model.CNIntegrityFreezeRequest `json:"lastRequest"`
	LastProofID  string                          `json:"lastProofId"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		code := "operation_failed"
		if errors.Is(err, model.ErrCNConflict) {
			code = "control_or_parent_conflict"
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "{\"error\":%q}\n", code)
		os.Exit(1)
	}
}

func parseOptions(args []string) (operatorOptions, error) {
	var o operatorOptions
	if len(args) == 0 {
		return o, errors.New("command required")
	}
	o.command = args[0]
	flags := flag.NewFlagSet("cn-integrity-operator", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&o.gateway, "gateway", "", "expected existing gateway identity")
	flags.StringVar(&o.epoch, "epoch", "", "integrity epoch")
	flags.Int64Var(&o.generation, "expected-generation", 0, "exact control CAS generation")
	flags.StringVar(&o.parent, "expected-parent", "", "exact frozen parent proof; empty only for batch 1")
	flags.StringVar(&o.evidence, "evidence", "", "local operator evidence ID")
	flags.StringVar(&o.inventory, "inventory", "", "imported approved inventory ID")
	flags.StringVar(&o.input, "input", "", "private JSON input file")
	flags.IntVar(&o.user, "user", 0, "native customer ID")
	flags.StringVar(&o.from, "from", "", "coverage start in Gateway timestamp format")
	flags.StringVar(&o.to, "to", "", "fixed single freeze coverage end")
	flags.StringVar(&o.state, "state-file", "", "private persistent freeze request journal")
	flags.StringVar(&o.stopState, "state", "", "paused, broken or closed")
	flags.DurationVar(&o.interval, "interval", time.Minute, "explicit freeze-loop interval")
	flags.DurationVar(&o.timeout, "timeout", 2*time.Minute, "per-operation timeout")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || o.gateway == "" || o.timeout <= 0 {
		return o, errors.New("invalid operator arguments")
	}
	switch o.command {
	case "status", "prepare", "import", "admit", "start", "stop", "freeze", "freeze-loop":
	default:
		return o, errors.New("unknown command")
	}
	if o.command != "prepare" && o.epoch == "" {
		return o, errors.New("epoch required")
	}
	if o.command != "status" && o.evidence == "" {
		return o, errors.New("operator evidence required")
	}
	if o.command != "prepare" && o.command != "status" && o.generation <= 0 {
		return o, errors.New("generation required")
	}
	if (o.command == "import" || o.command == "start") && o.input == "" {
		return o, errors.New("private input required")
	}
	if o.command == "admit" && (o.user <= 0 || o.inventory == "") {
		return o, errors.New("customer and inventory required")
	}
	if (o.command == "freeze" || o.command == "freeze-loop") && (o.state == "" || o.from == "") {
		return o, errors.New("freeze journal and coverage start required")
	}
	if o.command == "freeze" && o.to == "" {
		return o, errors.New("fixed coverage end required")
	}
	if o.command == "freeze-loop" && (o.interval < time.Second || o.to != "") {
		return o, errors.New("invalid loop interval or fixed end")
	}
	return o, nil
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, "cn-integrity-operator <status|prepare|import|admit|start|stop|freeze|freeze-loop> --gateway ID [--epoch ID] [--expected-generation N] [--evidence ID]\nDatabase: explicit SQL_DSN; SQLite also requires absolute existing SQLITE_PATH. No automatic migration.\nimport/start: --input private.json; admit: --user N --inventory ID; stop: --state paused|broken|closed.\nfreeze: --from TIME --to TIME --expected-parent PROOF --state-file private.json.\nfreeze-loop: --from TIME --interval 1m --expected-parent PROOF --state-file private.json.\nTimes: 2006-01-02T15:04:05.000000Z. Persist the private journal for crash recovery. See S4_WALLET_VALUE_HANDOFF.md.")
		return err
	}
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	common.RedisEnabled = os.Getenv("REDIS_CONN_STRING") != ""
	common.BatchUpdateEnabled = os.Getenv("BATCH_UPDATE_ENABLED") == "true"
	openCtx, cancel := context.WithTimeout(ctx, o.timeout)
	db, err := model.OpenCNIntegrityOperatorDB(openCtx)
	cancel()
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return errors.New("database unavailable")
	}
	defer pool.Close()
	return execute(ctx, db, o, out)
}

func execute(ctx context.Context, db *gorm.DB, o operatorOptions, out io.Writer) error {
	opCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	db = db.WithContext(opCtx)
	var stream model.CNFinanceStream
	if err := db.First(&stream, 1).Error; err != nil || stream.GatewayID != o.gateway {
		return model.ErrCNConflict
	}
	if o.command != "prepare" {
		var epoch model.CNIntegrityEpoch
		if err := db.Where("epoch_id = ? AND gateway_id = ? AND stream_epoch = ?", o.epoch, o.gateway, stream.Epoch).First(&epoch).Error; err != nil {
			return model.ErrCNConflict
		}
	}
	if o.command == "freeze" || o.command == "freeze-loop" {
		return runFreezes(ctx, db.Session(&gorm.Session{Context: ctx}), o, out)
	}
	var result any
	switch o.command {
	case "prepare":
		epoch, err := model.PrepareCNIntegrityCapture(db, o.evidence)
		if err != nil {
			return err
		}
		result = map[string]any{"epochId": epoch.EpochID, "generation": epoch.Generation, "state": epoch.State}
	case "status":
		var epoch model.CNIntegrityEpoch
		if err := db.Where("epoch_id = ? AND gateway_id = ? AND stream_epoch = ?", o.epoch, o.gateway, stream.Epoch).First(&epoch).Error; err != nil {
			return err
		}
		var frozen model.CNIntegritySnapshot
		err := db.Where("epoch_id = ?", o.epoch).Order("batch_sequence DESC").First(&frozen).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		result = map[string]any{"epochId": epoch.EpochID, "generation": epoch.Generation, "state": epoch.State, "membershipThrough": epoch.MembershipSequence, "mutationThrough": epoch.MutationSequence, "latestProofId": frozen.ProofID, "batchSequence": frozen.BatchSequence}
	case "import":
		raw, err := readPrivateJSON(o.input)
		if err != nil {
			return err
		}
		approved, err := model.DecodeCNIntegrityEligibilitySnapshot(raw)
		if err != nil || approved.Body.GatewayID != o.gateway || approved.Body.IntegrityEpoch != o.epoch || approved.Body.StreamEpoch != stream.Epoch {
			return model.ErrCNConflict
		}
		if err := model.ImportCNIntegrityEligibilitySnapshot(db, raw, o.evidence, o.generation); err != nil {
			return err
		}
		result = map[string]any{"epochId": o.epoch, "inventoryId": approved.OpsInventoryID, "imported": true}
	case "admit":
		admission, err := model.AdmitCNIntegrityUser(db, o.epoch, o.user, o.generation, o.inventory)
		if err != nil {
			return err
		}
		result = map[string]any{"epochId": o.epoch, "admissionId": admission.AdmissionID, "membershipSequence": admission.MembershipSequence}
	case "start":
		if !model.CNDurableWalletAvailable() || !model.CNDurableTaskAvailable() || os.Getenv("OPS_CN_WALLET_VALUE_ENABLED") != "true" || os.Getenv("OPS_CN_INTEGRITY_CAPTURE_ENABLED") != "true" {
			return errors.New("capture writer flags must be explicitly enabled before start")
		}
		raw, err := readPrivateJSON(o.input)
		if err != nil {
			return err
		}
		var request struct {
			Writers     []model.CNIntegrityWriterDeclaration `json:"writers"`
			Attestation model.CNIntegrityDrainAttestation    `json:"attestation"`
		}
		if err := common.Unmarshal(raw, &request); err != nil || request.Attestation.OperatorEvidenceID != o.evidence {
			return errors.New("invalid start declaration")
		}
		if err := model.StartCNIntegrityCapture(db, o.epoch, o.generation, request.Writers, request.Attestation); err != nil {
			return err
		}
		result = map[string]any{"epochId": o.epoch, "generation": o.generation + 1, "state": "active"}
	case "stop":
		if err := model.StopCNIntegrityCapture(db, o.epoch, o.generation, o.stopState, o.evidence); err != nil {
			return err
		}
		result = map[string]any{"epochId": o.epoch, "generation": o.generation + 1, "state": o.stopState}
	}
	result.(map[string]any)["operatorEvidenceId"] = o.evidence
	return writeResult(out, result)
}

func readPrivateJSON(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private input unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("invalid private input")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return nil, errors.New("private input read failed")
	}
	return raw, nil
}

func writeResult(out io.Writer, result any) error {
	raw, err := model.CNIntegrityExportJSON(result)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(raw))
	return err
}

func saveJournal(path string, journal freezeJournal, create bool) error {
	raw, err := common.Marshal(journal)
	if err != nil {
		return err
	}
	if create {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("freeze journal create failed")
		}
		_, writeErr := file.Write(raw)
		syncErr := file.Sync()
		closeErr := file.Close()
		return errors.Join(writeErr, syncErr, closeErr)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".cn-integrity-journal-*")
	if err != nil {
		return errors.New("freeze journal write failed")
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func runFreezes(ctx context.Context, db *gorm.DB, o operatorOptions, out io.Writer) error {
	journal := freezeJournal{Schema: "cn-integrity-operator-freeze-v1", GatewayID: o.gateway, EpochID: o.epoch, Generation: o.generation, Parent: o.parent, CoverageFrom: o.from, EvidenceID: o.evidence}
	raw, err := readPrivateJSON(o.state)
	if err == nil {
		if common.Unmarshal(raw, &journal) != nil || journal.Schema != "cn-integrity-operator-freeze-v1" || journal.GatewayID != o.gateway || journal.EpochID != o.epoch || journal.Generation != o.generation || journal.CoverageFrom != o.from || journal.EvidenceID != o.evidence {
			return model.ErrCNConflict
		}
	} else if _, statErr := os.Stat(o.state); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("freeze journal unavailable")
	} else if err := saveJournal(o.state, journal, true); err != nil {
		return err
	}
	for {
		if o.command == "freeze" && journal.Pending == nil && journal.LastRequest != nil {
			last := journal.LastRequest
			if last.ExpectedParentProofID != o.parent || last.CoverageTo != o.to {
				return model.ErrCNConflict
			}
			opCtx, cancel := context.WithTimeout(ctx, o.timeout)
			sealed, err := freezeOrRecover(opCtx, db, *last)
			cancel()
			if err != nil || sealed.ProofID != journal.LastProofID {
				return model.ErrCNConflict
			}
			return writeResult(out, map[string]any{"proofId": sealed.ProofID, "batchSequence": strconv.FormatInt(sealed.BatchSequence, 10), "freezeSequence": strconv.FormatInt(sealed.FreezeSequence, 10), "operatorEvidenceId": o.evidence})
		}
		if journal.Pending == nil {
			to := o.to
			if o.command == "freeze-loop" {
				to = time.Now().UTC().Format(model.CNTimeLayout)
			}
			journal.Pending = &model.CNIntegrityFreezeRequest{EpochID: o.epoch, ExpectedGeneration: o.generation, ExpectedParentProofID: journal.Parent, CoverageFrom: o.from, CoverageTo: to}
			if err := saveJournal(o.state, journal, false); err != nil {
				return err
			}
		}
		request := *journal.Pending
		if request.EpochID != journal.EpochID || request.ExpectedGeneration != journal.Generation || request.ExpectedParentProofID != journal.Parent || request.CoverageFrom != journal.CoverageFrom {
			return model.ErrCNConflict
		}
		if o.command == "freeze" && (request.CoverageTo != o.to || request.ExpectedParentProofID != o.parent) {
			return model.ErrCNConflict
		}
		opCtx, cancel := context.WithTimeout(ctx, o.timeout)
		sealed, err := freezeOrRecover(opCtx, db, request)
		cancel()
		if err != nil {
			return err
		}
		if err := writeResult(out, map[string]any{"proofId": sealed.ProofID, "batchSequence": strconv.FormatInt(sealed.BatchSequence, 10), "freezeSequence": strconv.FormatInt(sealed.FreezeSequence, 10), "operatorEvidenceId": o.evidence}); err != nil {
			return err
		}
		journal.LastRequest, journal.LastProofID = journal.Pending, sealed.ProofID
		journal.Parent, journal.Pending = sealed.ProofID, nil
		if err := saveJournal(o.state, journal, false); err != nil {
			return err
		}
		if o.command == "freeze" {
			return nil
		}
		timer := time.NewTimer(o.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func freezeOrRecover(ctx context.Context, db *gorm.DB, request model.CNIntegrityFreezeRequest) (*model.CNIntegritySnapshot, error) {
	batch := int64(1)
	if request.ExpectedParentProofID != "" {
		var parent model.CNIntegritySnapshot
		if err := db.WithContext(ctx).Where("proof_id = ? AND epoch_id = ?", request.ExpectedParentProofID, request.EpochID).First(&parent).Error; err != nil {
			return nil, err
		}
		if parent.BatchSequence == math.MaxInt64 {
			return nil, model.ErrCNConflict
		}
		batch = parent.BatchSequence + 1
	}
	var existing model.CNIntegritySnapshot
	err := db.WithContext(ctx).Where("epoch_id = ? AND batch_sequence = ?", request.EpochID, batch).First(&existing).Error
	if err == nil {
		hash, hashErr := model.CNIntegrityBodyHash([]byte(existing.Manifest))
		if hashErr != nil || hash != existing.ProofID {
			return nil, model.ErrCNConflict
		}
		var manifest map[string]any
		if common.Unmarshal([]byte(existing.Manifest), &manifest) != nil {
			return nil, model.ErrCNConflict
		}
		parent := manifest["parentProofId"]
		if (request.ExpectedParentProofID == "" && parent != nil) || (request.ExpectedParentProofID != "" && parent != request.ExpectedParentProofID) || manifest["controlGeneration"] != strconv.FormatInt(request.ExpectedGeneration, 10) || manifest["coverageFrom"] != request.CoverageFrom || manifest["coverageTo"] != request.CoverageTo {
			return nil, model.ErrCNConflict
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return model.FreezeCNIntegrity(ctx, db, request)
}
