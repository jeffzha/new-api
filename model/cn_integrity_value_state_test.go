package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func cnAssertIntegrityFrozenGraph(t *testing.T, db *gorm.DB, epochID, dialect string) {
	t.Helper()
	_, err := ReadCNFacts(context.Background(), db, "", 1000)
	require.NoError(t, err)
	request := CNIntegrityFreezeRequest{EpochID: epochID, ExpectedGeneration: 2, CoverageFrom: time.Now().UTC().Add(-time.Hour).Format(CNTimeLayout), CoverageTo: time.Now().UTC().Add(time.Hour).Format(CNTimeLayout)}
	sealed, err := FreezeCNIntegrity(context.Background(), db, request)
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, common.Unmarshal([]byte(sealed.Manifest), &manifest))
	collections := map[string]any{}
	for _, name := range cnIntegrityCollections {
		page, err := ReadCNIntegrityPage(context.Background(), db, sealed.ProofID, name, 0, 1000)
		require.NoError(t, err)
		collections[name] = page["rows"]
		terminal, err := ReadCNIntegrityPage(context.Background(), db, sealed.ProofID, name, int64(len(page["rows"].([]common.RawMessage))), 1)
		require.NoError(t, err)
		require.Nil(t, terminal["nextOrdinal"])
	}
	var findings []map[string]any
	raw, err := common.Marshal(collections["findings"])
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &findings))
	require.Equal(t, "complete", manifest["status"], findings)
	index, err := ReadCNIntegrityIndex(context.Background(), db, "test-cn", 0, 1)
	require.NoError(t, err)
	require.False(t, index["hasMore"].(bool))
	_, err = ReadCNIntegrityIndex(context.Background(), db, "test-cn", sealed.FreezeSequence+1, 1)
	require.ErrorIs(t, err, ErrCNIntegrityCursorAhead)
	before, err := ReadCNIntegrityManifest(context.Background(), db, sealed.ProofID)
	require.NoError(t, err)
	request.ExpectedParentProofID = sealed.ProofID
	child, err := FreezeCNIntegrity(context.Background(), db, request)
	require.NoError(t, err)
	require.Equal(t, sealed.FreezeSequence+1, child.FreezeSequence)
	after, err := ReadCNIntegrityManifest(context.Background(), db, sealed.ProofID)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	_, err = FreezeCNIntegrity(context.Background(), db, request)
	require.ErrorIs(t, err, ErrCNConflict, "stale parent cannot seal a sibling")
	var childManifest map[string]any
	require.NoError(t, common.Unmarshal([]byte(child.Manifest), &childManifest))
	require.Equal(t, manifest["publicationThrough"], childManifest["publicationThrough"])
	t.Run("failed_freeze_rolls_back_every_published_boundary", func(t *testing.T) {
		request.ExpectedParentProofID = child.ProofID
		var before CNIntegrityEpoch
		require.NoError(t, db.Where("epoch_id = ?", epochID).First(&before).Error)
		failure := errors.New("injected frozen collection persistence failure")
		callback := "integrity-freeze-atomicity"
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "cn_integrity_snapshot_collections" {
				tx.AddError(failure)
			}
		}))
		_, err := FreezeCNIntegrity(context.Background(), db, request)
		require.NoError(t, db.Callback().Create().Remove(callback))
		require.ErrorIs(t, err, failure)
		var after CNIntegrityEpoch
		var head CNIntegrityFreezeHead
		var count int64
		require.NoError(t, db.Where("epoch_id = ?", epochID).First(&after).Error)
		require.NoError(t, db.First(&head, 1).Error)
		require.NoError(t, db.Model(&CNIntegritySnapshot{}).Count(&count).Error)
		require.Equal(t, before.ValueSequence, after.ValueSequence)
		require.Equal(t, child.FreezeSequence, head.Sequence)
		require.EqualValues(t, 2, count)
	})
	t.Run("independent_graph_detects_omitted_captured_fact_ids", func(t *testing.T) {
		rollback := errors.New("rollback omitted captured facts")
		err := db.Transaction(func(tx *gorm.DB) error {
			var disposition CNIntegrityDisposition
			require.NoError(t, tx.Where("epoch_id = ? AND known_quota > ? AND fact_ids <> ?", epochID, 0, "[]").First(&disposition).Error)
			require.NoError(t, tx.Model(&disposition).Update("fact_ids", "[]").Error)
			var epoch CNIntegrityEpoch
			require.NoError(t, tx.Where("epoch_id = ?", epochID).First(&epoch).Error)
			rows, _, err := cnBuildIntegrityCollectionsTx(tx, epoch, "3")
			require.NoError(t, err)
			found := false
			for _, finding := range rows["findings"] {
				found = found || finding["category"] == "orphan" && finding["reason"] == "native_fact_without_frozen_membership"
			}
			require.True(t, found)
			return rollback
		})
		require.ErrorIs(t, err, rollback)
	})
	t.Run("changed_parent_disposition_cannot_be_sealed", func(t *testing.T) {
		rollback := errors.New("rollback changed parent disposition")
		err := db.Transaction(func(tx *gorm.DB) error {
			var disposition CNIntegrityDisposition
			require.NoError(t, tx.Where("epoch_id = ?", epochID).First(&disposition).Error)
			require.NoError(t, tx.Model(&disposition).Update("reason", "unsupported_path").Error)
			request.ExpectedParentProofID = child.ProofID
			_, err := FreezeCNIntegrity(context.Background(), tx, request)
			require.ErrorIs(t, err, ErrCNConflict)
			return rollback
		})
		require.ErrorIs(t, err, rollback)
	})
	t.Run("zero_delta_source_switch_captures_account_isolation_and_exact_cutoff", func(t *testing.T) {
		rollback := errors.New("rollback zero-delta source switch")
		err := db.Transaction(func(tx *gorm.DB) error {
			var lot CNWalletValueLot
			admitted := tx.Model(&CNIntegrityAdmission{}).Select("user_id").Where("epoch_id = ?", epochID)
			require.NoError(t, tx.Where("isolated = ? AND user_id IN (?)", false, admitted).First(&lot).Error)
			var before User
			require.NoError(t, tx.First(&before, lot.UserID).Error)
			var epoch CNIntegrityEpoch
			require.NoError(t, tx.Where("epoch_id = ?", epochID).First(&epoch).Error)
			mutationBefore := epoch.MutationSequence
			valueBefore := epoch.ValueSequence
			require.NoError(t, tx.Model(&User{}).Where("id = ?", lot.UserID).Update("billing_mode", "integrity-test-source-switch").Error)
			var after User
			require.NoError(t, tx.First(&after, lot.UserID).Error)
			require.Equal(t, before.Quota, after.Quota, "source switch has zero balance delta")
			require.NoError(t, tx.Where("epoch_id = ?", epochID).First(&epoch).Error)
			require.Equal(t, "broken", epoch.State)
			require.Equal(t, mutationBefore, epoch.MutationSequence, "unsupported switch has control evidence, not an invented original")
			var changed []CNIntegrityValueState
			require.NoError(t, tx.Where("epoch_id = ? AND state_sequence > ?", epochID, valueBefore).Order("state_sequence").Find(&changed).Error)
			accountCaptured, lotCaptured := false, false
			for _, stored := range changed {
				var state CNIntegrityValueStateBody
				require.NoError(t, common.Unmarshal([]byte(stored.Payload), &state))
				require.Equal(t, "control", state.TriggerType)
				if state.EntityType == "account" && state.EntityID == strconv.Itoa(lot.UserID) {
					var account cnIntegrityAccountState
					require.NoError(t, common.Unmarshal(state.After, &account))
					require.Equal(t, "integrity-test-source-switch", account.BillingMode)
					require.NotNil(t, account.ValueAccount)
					require.True(t, account.ValueAccount.Suspended)
					accountCaptured = true
				}
				if state.EntityType == "lot" && state.EntityID == lot.LotID {
					var value cnIntegrityLotState
					require.NoError(t, common.Unmarshal(state.After, &value))
					require.True(t, value.Isolated)
					lotCaptured = true
				}
			}
			require.True(t, accountCaptured)
			require.True(t, lotCaptured)
			request.ExpectedParentProofID = child.ProofID
			request.ExpectedGeneration = epoch.Generation
			frozen, err := FreezeCNIntegrity(context.Background(), tx, request)
			require.NoError(t, err)
			var manifest map[string]any
			require.NoError(t, common.Unmarshal([]byte(frozen.Manifest), &manifest))
			require.Equal(t, "gap", manifest["status"])
			collections := map[string]any{}
			for _, name := range cnIntegrityCollections {
				var frozenRows CNIntegritySnapshotCollection
				require.NoError(t, tx.Where("proof_id = ? AND collection = ?", frozen.ProofID, name).First(&frozenRows).Error)
				collections[name] = common.RawMessage(frozenRows.Payload)
			}
			var states []CNIntegrityValueStateBody
			require.NoError(t, common.Unmarshal(collections["valueStates"].(common.RawMessage), &states))
			cutoffs := 0
			for _, state := range states {
				if state.TriggerType == "cutoff" && state.BatchSequence != nil && *state.BatchSequence == manifest["batchSequence"] {
					require.Equal(t, manifest["mutationThrough"], state.MutationSequence)
					require.Equal(t, manifest["controlGeneration"], state.ControlGeneration)
					cutoffs++
				}
			}
			require.Positive(t, cutoffs)
			t.Logf("zero-delta switch: account+isolation captured; all %d current cutoffs equal mutationThrough=%s; status=gap", cutoffs, manifest["mutationThrough"])
			if os.Getenv("CN_INTEGRITY_WRITE_EVIDENCE") == "1" {
				raw, err := CNIntegrityExportJSON(map[string]any{"manifest": manifest, "collections": collections, "syntheticIntegration": true, "productionEligibility": false, "financialGrants": 0})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join("testdata", "cn-task", "s4-integrity-source-switch-"+dialect+".json"), raw, 0600))
			}
			return rollback
		})
		require.ErrorIs(t, err, rollback)
	})
	if os.Getenv("CN_INTEGRITY_WRITE_EVIDENCE") == "1" {
		var approvals []CNIntegrityEligibilityApproval
		require.NoError(t, db.Where("epoch_id = ?", epochID).Find(&approvals).Error)
		exports := []common.RawMessage{}
		for _, approval := range approvals {
			exports = append(exports, common.RawMessage(approval.Payload))
		}
		raw, err := common.Marshal(map[string]any{"manifest": manifest, "collections": collections, "index": index, "eligibilitySnapshots": exports, "dialect": dialect, "synthetic": true, "trustBoundary": "fixture-only-explicit-local-operator-import"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join("testdata", "cn-task", "s4-integrity-fullgraph-"+dialect+".json"), raw, 0600))
	}
}

func TestCNIntegrityValueStateVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/cn-task/integrity-v2-value-state-vectors.json")
	require.NoError(t, err)
	var corpus struct {
		Cases []struct {
			ID              string                 `json:"id"`
			Balance         string                 `json:"balance"`
			UnknownQuota    string                 `json:"unknownQuota"`
			WalletHeldQuota string                 `json:"walletHeldQuota"`
			TaskHeldQuota   string                 `json:"taskHeldQuota"`
			Lot             *cnIntegrityLotState   `json:"lot"`
			AllocationQuota string                 `json:"allocationQuota"`
			AllocationValue string                 `json:"allocationValue"`
			Holds           []cnIntegrityHoldState `json:"holds"`
			Valid           bool                   `json:"valid"`
		} `json:"cases"`
	}
	require.NoError(t, common.Unmarshal(raw, &corpus))
	for _, c := range corpus.Cases {
		t.Run(c.ID, func(t *testing.T) {
			lots := map[string]cnIntegrityLotState{}
			if c.Lot != nil {
				lots[""] = *c.Lot
			}
			q, err := decimal.NewFromString(c.AllocationQuota)
			require.NoError(t, err)
			v, err := decimal.NewFromString(c.AllocationValue)
			require.NoError(t, err)
			account := cnIntegrityAccountState{Balance: c.Balance, WalletHeldQuota: c.WalletHeldQuota, TaskHeldQuota: c.TaskHeldQuota, ValueAccount: &cnIntegrityValueAccount{UnknownQuota: c.UnknownQuota}}
			require.Equal(t, c.Valid, cnIntegrityValueEquations(account, lots, map[string][2]decimal.Decimal{"": {q, v}}, c.Holds))
		})
	}
}
