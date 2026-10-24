package model

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

func cnIntegrityValueBoundaryValid(heads map[string]CNIntegrityValueStateBody, customer string, through int64, deltas []CNIntegritySliceDelta) bool {
	var account cnIntegrityAccountState
	lots := map[string]cnIntegrityLotState{}
	holds := []cnIntegrityHoldState{}
	allocations := map[string][2]decimal.Decimal{}
	for _, state := range heads {
		if state.CustomerKey != customer {
			continue
		}
		switch state.EntityType {
		case "account":
			if common.Unmarshal(state.After, &account) != nil {
				return false
			}
		case "lot":
			var lot cnIntegrityLotState
			if common.Unmarshal(state.After, &lot) != nil {
				return false
			}
			lots[state.EntityID] = lot
		case "refund_hold":
			var hold cnIntegrityHoldState
			if common.Unmarshal(state.After, &hold) != nil {
				return false
			}
			holds = append(holds, hold)
		default:
			return false
		}
	}
	if account.ValueAccount == nil && len(lots) > 0 {
		return false
	}
	for _, delta := range deltas {
		if delta.MutationSequence > through {
			continue
		}
		if _, ok := lots[delta.LotID]; !ok {
			continue
		}
		v, err := decimal.NewFromString(delta.ValueDelta)
		if err != nil {
			return false
		}
		total := allocations[delta.LotID]
		total[0] = total[0].Add(decimal.NewFromInt(delta.QuotaDelta))
		total[1] = total[1].Add(v)
		allocations[delta.LotID] = total
	}
	return cnIntegrityValueEquations(account, lots, allocations, holds)
}

func cnIntegrityValueTransitionValid(prior, current CNIntegrityValueStateBody) bool {
	if prior.StateID == "" {
		return true
	}
	if prior.CustomerKey != current.CustomerKey || prior.AdmissionID != current.AdmissionID {
		return false
	}
	var before, after map[string]common.RawMessage
	if common.Unmarshal(prior.After, &before) != nil || common.Unmarshal(current.After, &after) != nil {
		return false
	}
	var stable []string
	switch current.EntityType {
	case "lot":
		stable = []string{"nativeLotKind", "nativeLotId", "sourceId", "paymentId", "currency", "purchasedQuota", "purchasedValue"}
	case "refund_hold":
		stable = []string{"operationId", "nativeLotId", "lotId", "quota", "value"}
	case "account":
		var old, next cnIntegrityAccountState
		if common.Unmarshal(prior.After, &old) != nil || common.Unmarshal(current.After, &next) != nil {
			return false
		}
		if old.ValueAccount != nil {
			if next.ValueAccount == nil || old.ValueAccount.OpeningBalance != next.ValueAccount.OpeningBalance || old.ValueAccount.OpeningQuota != next.ValueAccount.OpeningQuota || old.ValueAccount.Suspended && !next.ValueAccount.Suspended {
				return false
			}
		}
	}
	for _, key := range stable {
		if !cnIntegrityJSONEqual(before[key], after[key]) {
			return false
		}
	}
	if current.EntityType == "lot" {
		var old, next cnIntegrityLotState
		if common.Unmarshal(prior.After, &old) != nil || common.Unmarshal(current.After, &next) != nil {
			return false
		}
		if old.Isolated && !next.Isolated {
			return false
		}
	}
	if current.EntityType == "refund_hold" {
		var old, next cnIntegrityHoldState
		if common.Unmarshal(prior.After, &old) != nil || common.Unmarshal(current.After, &next) != nil {
			return false
		}
		if old.State != next.State && (old.State != "held" || next.State != "released" && next.State != "retired") {
			return false
		}
	}
	return true
}

func cnIntegrityValueGroup(state CNIntegrityValueStateBody) string {
	batch := ""
	if state.BatchSequence != nil {
		batch = *state.BatchSequence
	}
	return CNIdentity(state.CustomerKey, state.TriggerType, state.TriggerID, state.ControlGeneration, state.MutationSequence, batch)
}

func cnScanIntegrityHistoricalValues(states []CNIntegrityValueState, deltas []CNIntegritySliceDelta, scan *cnIntegrityFoundationScan) error {
	heads := map[string]CNIntegrityValueStateBody{}
	var group CNIntegrityValueStateBody
	for _, stored := range states {
		var state CNIntegrityValueStateBody
		if err := common.Unmarshal([]byte(stored.Payload), &state); err != nil {
			return err
		}
		if group.StateID != "" && cnIntegrityValueGroup(group) != cnIntegrityValueGroup(state) {
			prefix, err := strconv.ParseInt(group.MutationSequence, 10, 64)
			if err != nil {
				return err
			}
			if !cnIntegrityValueBoundaryValid(heads, group.CustomerKey, prefix, deltas) {
				scan.MismatchCount++
				scan.ActivationBlockers = append(scan.ActivationBlockers, "value_history_equation:"+group.StateID)
			}
		}
		key := state.EntityType + ":" + state.EntityID
		if !cnIntegrityValueTransitionValid(heads[key], state) {
			scan.MismatchCount++
		}
		heads[key] = state
		group = state
	}
	if group.StateID != "" {
		prefix, err := strconv.ParseInt(group.MutationSequence, 10, 64)
		if err != nil {
			return err
		}
		if !cnIntegrityValueBoundaryValid(heads, group.CustomerKey, prefix, deltas) {
			scan.MismatchCount++
			scan.ActivationBlockers = append(scan.ActivationBlockers, "value_history_equation:"+group.StateID)
		}
	}
	return nil
}
