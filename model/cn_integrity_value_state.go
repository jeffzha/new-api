package model

import (
	"bytes"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type CNIntegrityValueState struct {
	StateID       string `gorm:"size:64;primaryKey"`
	EpochID       string `gorm:"size:64;not null;uniqueIndex:uidx_cn_integrity_value_seq,priority:1;index:idx_cn_integrity_value_entity,priority:1"`
	StateSequence int64  `gorm:"not null;uniqueIndex:uidx_cn_integrity_value_seq,priority:2"`
	EntityType    string `gorm:"size:32;not null;index:idx_cn_integrity_value_entity,priority:2"`
	EntityID      string `gorm:"size:191;not null;index:idx_cn_integrity_value_entity,priority:3"`
	Payload       string `gorm:"type:text;not null"`
}

type CNIntegrityValueStateBody struct {
	StateID            string            `json:"stateId"`
	StateSequence      string            `json:"stateSequence"`
	EntityType         string            `json:"entityType"`
	EntityID           string            `json:"entityId"`
	CustomerKey        string            `json:"customerKey"`
	AdmissionID        string            `json:"admissionId"`
	TriggerType        string            `json:"triggerType"`
	TriggerID          string            `json:"triggerId"`
	ControlGeneration  string            `json:"controlGeneration"`
	MutationSequence   string            `json:"mutationSequence"`
	BatchSequence      *string           `json:"batchSequence"`
	PredecessorStateID *string           `json:"predecessorStateId"`
	Before             common.RawMessage `json:"before"`
	After              common.RawMessage `json:"after"`
	BodyHash           string            `json:"bodyHash,omitempty"`
}

type cnIntegrityValueAccount struct {
	ExpectedQuota  string  `json:"expectedQuota"`
	UnknownQuota   string  `json:"unknownQuota"`
	OpeningQuota   string  `json:"openingQuota"`
	OpeningBalance string  `json:"openingBalance"`
	Suspended      bool    `json:"suspended"`
	GapCount       string  `json:"gapCount"`
	SuspendedAt    *string `json:"suspendedAt"`
	SuspendReason  *string `json:"suspendReason"`
}

type cnIntegrityAccountState struct {
	Balance         string                   `json:"balance"`
	BillingMode     string                   `json:"billingMode"`
	WalletHeldQuota string                   `json:"walletHeldQuota"`
	TaskHeldQuota   string                   `json:"taskHeldQuota"`
	ValueAccount    *cnIntegrityValueAccount `json:"valueAccount"`
}

type cnIntegrityLotState struct {
	NativeLotKind  string `json:"nativeLotKind"`
	NativeLotID    string `json:"nativeLotId"`
	SourceID       string `json:"sourceId"`
	PaymentID      string `json:"paymentId"`
	Currency       string `json:"currency"`
	PurchasedQuota string `json:"purchasedQuota"`
	PurchasedValue string `json:"purchasedValue"`
	Isolated       bool   `json:"isolated"`
	RemainingQuota string `json:"remainingQuota"`
	RemainingValue string `json:"remainingValue"`
	HeldQuota      string `json:"heldQuota"`
	HeldValue      string `json:"heldValue"`
	RetiredQuota   string `json:"retiredQuota"`
	RetiredValue   string `json:"retiredValue"`
}

type cnIntegrityHoldState struct {
	OperationID string `json:"operationId"`
	NativeLotID string `json:"nativeLotId"`
	LotID       string `json:"lotId"`
	Quota       string `json:"quota"`
	Value       string `json:"value"`
	State       string `json:"state"`
}

type cnIntegrityValueEntity struct {
	Type string
	ID   string
	Body common.RawMessage
}

// Read native balances and saved pools, including zero and isolated pools.
// Agency hold IDs cannot be reinterpreted as ordinary wallet lot IDs.
func cnIntegrityNativeValueStates(tx *gorm.DB, user int, current bool) ([]cnIntegrityValueEntity, error) {
	read := func() *gorm.DB {
		query := tx.Session(&gorm.Session{NewDB: true})
		if current {
			return lockForUpdate(query)
		}
		return query
	}
	var native User
	if err := read().Select("id", "quota", "billing_mode").First(&native, user).Error; err != nil {
		return nil, err
	}
	rows, err := cnIntegrityNativeRows(tx, user, current)
	if err != nil {
		return nil, err
	}
	walletHeld, taskHeld := decimal.Zero, decimal.Zero
	for _, row := range rows {
		if row.Type == "wallet" {
			walletHeld = walletHeld.Add(decimal.NewFromInt(row.Held))
		}
		if row.Type == "task" {
			taskHeld = taskHeld.Add(decimal.NewFromInt(row.Held))
		}
	}
	account := cnIntegrityAccountState{Balance: strconv.Itoa(native.Quota), BillingMode: native.BillingMode, WalletHeldQuota: walletHeld.String(), TaskHeldQuota: taskHeld.String()}
	var value CNWalletValueAccount
	err = read().Where("user_id = ?", user).First(&value).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil {
		account.ValueAccount = &cnIntegrityValueAccount{ExpectedQuota: strconv.FormatInt(value.ExpectedQuota, 10), UnknownQuota: strconv.FormatInt(value.UnknownQuota, 10), OpeningQuota: strconv.FormatInt(value.OpeningQuota, 10), OpeningBalance: strconv.FormatInt(value.OpeningBalance, 10), Suspended: value.Suspended, GapCount: strconv.FormatInt(value.GapCount, 10), SuspendedAt: cnIntegrityNullable(value.SuspendedAt), SuspendReason: cnIntegrityNullable(value.SuspendReason)}
	}
	body, err := common.Marshal(account)
	if err != nil {
		return nil, err
	}
	entities := []cnIntegrityValueEntity{{Type: "account", ID: strconv.Itoa(user), Body: body}}
	var lots []CNWalletValueLot
	if err := read().Where("user_id = ?", user).Order("lot_id").Find(&lots).Error; err != nil {
		return nil, err
	}
	holds := []CNFundingValueHold{}
	slices.SortFunc(lots, func(a, b CNWalletValueLot) int { return strings.Compare(a.LotID, b.LotID) })
	for _, lot := range lots {
		var pool CNFundingValueLot
		if err := read().Where("lot_id = ?", lot.LotID).First(&pool).Error; err != nil {
			return nil, err
		}
		state := cnIntegrityLotState{SourceID: lot.SourceID, PaymentID: lot.PaymentID, Currency: lot.Currency, PurchasedQuota: strconv.FormatInt(lot.Quota, 10), PurchasedValue: lot.Amount, Isolated: lot.Isolated, RemainingQuota: strconv.FormatInt(pool.RemainingQuota, 10), RemainingValue: pool.RemainingValue, HeldQuota: strconv.FormatInt(pool.HeldQuota, 10), HeldValue: pool.HeldValue, RetiredQuota: strconv.FormatInt(pool.RetiredQuota, 10), RetiredValue: pool.RetiredValue}
		state.NativeLotKind, state.NativeLotID = "ordinary_wallet", strconv.FormatInt(lot.ID, 10)
		body, err := common.Marshal(state)
		if err != nil {
			return nil, err
		}
		entities = append(entities, cnIntegrityValueEntity{Type: "lot", ID: lot.LotID, Body: body})
		var lotHolds []CNFundingValueHold
		if err := read().Where("lot_id = ?", lot.LotID).Find(&lotHolds).Error; err != nil {
			return nil, err
		}
		holds = append(holds, lotHolds...)
	}
	// The ordinary cohort excludes agency owners. Any such hold is evidence of
	// an ownership violation, retained for independent verification below.
	slices.SortFunc(holds, func(a, b CNFundingValueHold) int { return strings.Compare(a.OperationID, b.OperationID) })
	for _, hold := range holds {
		body, err := common.Marshal(cnIntegrityHoldState{OperationID: hold.OperationID, NativeLotID: strconv.FormatInt(hold.NativeLotID, 10), LotID: hold.LotID, Quota: strconv.FormatInt(hold.Quota, 10), Value: hold.Value, State: hold.State})
		if err != nil {
			return nil, err
		}
		entities = append(entities, cnIntegrityValueEntity{Type: "refund_hold", ID: hold.OperationID, Body: body})
	}
	return entities, nil
}

func cnIntegrityNullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func cnIntegrityJSONEqual(a, b []byte) bool {
	if len(a) == 0 {
		a = []byte("null")
	}
	if len(b) == 0 {
		b = []byte("null")
	}
	wrappedA, err := common.Marshal([]common.RawMessage{a})
	if err != nil {
		return false
	}
	wrappedB, err := common.Marshal([]common.RawMessage{b})
	if err != nil {
		return false
	}
	x, err := CNIntegrityCanonicalJSON(wrappedA)
	if err != nil {
		return false
	}
	y, err := CNIntegrityCanonicalJSON(wrappedB)
	return err == nil && bytes.Equal(x, y)
}

func cnRecordIntegrityValueStatesTx(tx *gorm.DB, epochID string, admission CNIntegrityAdmission, trigger, triggerID string, generation, mutation int64, batch *string, before []cnIntegrityValueEntity) error {
	var epoch CNIntegrityEpoch
	if err := lockForUpdate(tx).Where("epoch_id = ?", epochID).First(&epoch).Error; err != nil {
		return err
	}
	entities, err := cnIntegrityNativeValueStates(tx, admission.UserID, trigger != "cutoff")
	if err != nil {
		return err
	}
	beforeByID := map[string]common.RawMessage{}
	for _, entity := range before {
		beforeByID[entity.Type+":"+entity.ID] = entity.Body
	}
	for _, entity := range entities {
		var prior CNIntegrityValueState
		err := lockForUpdate(tx).Where("epoch_id = ? AND entity_type = ? AND entity_id = ?", epochID, entity.Type, entity.ID).Order("state_sequence DESC").First(&prior).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var previous CNIntegrityValueStateBody
		if err == nil && common.Unmarshal([]byte(prior.Payload), &previous) != nil {
			return ErrCNConflict
		}
		actualBefore := previous.After
		if trigger == "cutoff" {
			actualBefore = entity.Body
		}
		if before != nil {
			actualBefore = beforeByID[entity.Type+":"+entity.ID]
		}
		if trigger != "cutoff" && cnIntegrityJSONEqual(previous.After, entity.Body) && cnIntegrityJSONEqual(actualBefore, previous.After) {
			continue
		}
		if epoch.ValueSequence == math.MaxInt64 {
			return ErrCNConflict
		}
		epoch.ValueSequence++
		sequence := strconv.FormatInt(epoch.ValueSequence, 10)
		material, err := common.Marshal([]string{"cn-native-value-state-v2", epochID, sequence})
		if err != nil {
			return err
		}
		id, err := CNIntegrityBodyHash(material)
		if err != nil {
			return err
		}
		state := CNIntegrityValueStateBody{StateID: strings.TrimPrefix(id, "sha256:"), StateSequence: sequence, EntityType: entity.Type, EntityID: entity.ID, CustomerKey: strconv.Itoa(admission.UserID), AdmissionID: admission.AdmissionID, TriggerType: trigger, TriggerID: triggerID, ControlGeneration: strconv.FormatInt(generation, 10), MutationSequence: strconv.FormatInt(mutation, 10), BatchSequence: batch, PredecessorStateID: cnIntegrityNullable(prior.StateID), Before: actualBefore, After: entity.Body}
		body, err := common.Marshal(state)
		if err != nil {
			return err
		}
		state.BodyHash, err = CNIntegrityBodyHash(body)
		if err != nil {
			return err
		}
		body, err = common.Marshal(state)
		if err != nil {
			return err
		}
		if err := tx.Create(&CNIntegrityValueState{StateID: state.StateID, EpochID: epochID, StateSequence: epoch.ValueSequence, EntityType: entity.Type, EntityID: entity.ID, Payload: string(body)}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&epoch).Update("value_sequence", epoch.ValueSequence).Error
}

// Validate using exact decimals, never machine integer sums or inferred money.
func cnIntegrityValueEquations(account cnIntegrityAccountState, lots map[string]cnIntegrityLotState, allocations map[string][2]decimal.Decimal, holds []cnIntegrityHoldState) bool {
	parse := func(s string) (decimal.Decimal, bool) {
		d, e := decimal.NewFromString(s)
		return d, e == nil && d.String() == s
	}
	balance, ok := parse(account.Balance)
	if !ok {
		return false
	}
	wallet, ok := parse(account.WalletHeldQuota)
	if !ok || wallet.IsNegative() {
		return false
	}
	task, ok := parse(account.TaskHeldQuota)
	if !ok || task.IsNegative() {
		return false
	}
	unknown := balance.Add(wallet).Add(task)
	if account.ValueAccount != nil {
		var ok bool
		unknown, ok = parse(account.ValueAccount.UnknownQuota)
		if !ok {
			return false
		}
	}
	for id, lot := range lots {
		qs, vs := decimal.Zero, decimal.Zero
		for _, bucket := range [][2]string{{lot.RemainingQuota, lot.RemainingValue}, {lot.HeldQuota, lot.HeldValue}, {lot.RetiredQuota, lot.RetiredValue}} {
			q, ok := parse(bucket[0])
			if !ok || q.IsNegative() {
				return false
			}
			v, ok := parse(bucket[1])
			if !ok || v.IsNegative() {
				return false
			}
			qs = qs.Add(q)
			vs = vs.Add(v)
		}
		q, ok := parse(lot.PurchasedQuota)
		if !ok || q.IsNegative() {
			return false
		}
		v, ok := parse(lot.PurchasedValue)
		if !ok || v.IsNegative() {
			return false
		}
		if !q.Equal(qs.Add(allocations[id][0])) || !v.Equal(vs.Add(allocations[id][1])) {
			return false
		}
		hq, hv := decimal.Zero, decimal.Zero
		for _, hold := range holds {
			if hold.LotID == id && hold.State == "held" {
				q, ok := parse(hold.Quota)
				if !ok {
					return false
				}
				v, ok := parse(hold.Value)
				if !ok {
					return false
				}
				hq = hq.Add(q)
				hv = hv.Add(v)
			}
		}
		if hq.String() != lot.HeldQuota || hv.String() != lot.HeldValue {
			return false
		}
		if !lot.Isolated {
			q, _ := parse(lot.RemainingQuota)
			unknown = unknown.Add(q)
		}
	}
	return unknown.Equal(balance.Add(wallet).Add(task))
}

func cnScanIntegrityValueStatesTx(tx *gorm.DB, epoch CNIntegrityEpoch, admissions []CNIntegrityAdmission, originals map[string]CNIntegrityOriginal, scan *cnIntegrityFoundationScan) error {
	var states []CNIntegrityValueState
	if err := tx.Where("epoch_id = ?", epoch.EpochID).Order("state_sequence").Find(&states).Error; err != nil {
		return err
	}
	if int64(len(states)) != epoch.ValueSequence {
		scan.MismatchCount++
	}
	var deltas []CNIntegritySliceDelta
	if err := tx.Where("epoch_id = ?", epoch.EpochID).Find(&deltas).Error; err != nil {
		return err
	}
	if err := cnScanIntegrityHistoricalValues(states, deltas, scan); err != nil {
		return err
	}
	members := map[string]CNIntegrityAdmission{}
	for _, a := range admissions {
		members[a.AdmissionID] = a
	}
	heads := map[string]CNIntegrityValueStateBody{}
	for i, row := range states {
		var body CNIntegrityValueStateBody
		if common.Unmarshal([]byte(row.Payload), &body) != nil {
			scan.MismatchCount++
			continue
		}
		expected := body.BodyHash
		body.BodyHash = ""
		raw, err := common.Marshal(body)
		if err != nil {
			return err
		}
		hash, err := CNIntegrityBodyHash(raw)
		if err != nil {
			return err
		}
		key := body.EntityType + ":" + body.EntityID
		prior := heads[key]
		a, exists := members[body.AdmissionID]
		material, _ := common.Marshal([]string{"cn-native-value-state-v2", epoch.EpochID, strconv.FormatInt(row.StateSequence, 10)})
		id, err := CNIntegrityBodyHash(material)
		if err != nil {
			return err
		}
		if row.StateSequence != int64(i)+1 || row.StateID != strings.TrimPrefix(id, "sha256:") || body.StateID != row.StateID || body.StateSequence != strconv.FormatInt(row.StateSequence, 10) || body.EntityType != row.EntityType || body.EntityID != row.EntityID || !exists || body.CustomerKey != strconv.Itoa(a.UserID) || hash != expected || !cnIntegrityJSONEqual(body.Before, prior.After) || (body.PredecessorStateID == nil) != (prior.StateID == "") || (body.PredecessorStateID != nil && *body.PredecessorStateID != prior.StateID) {
			scan.MismatchCount++
			scan.ActivationBlockers = append(scan.ActivationBlockers, "value_state_chain:"+row.StateID)
			if !cnIntegrityJSONEqual(body.Before, prior.After) {
				scan.ActivationBlockers = append(scan.ActivationBlockers, "value_before_mismatch:"+row.StateID)
			}
			if hash != expected {
				scan.ActivationBlockers = append(scan.ActivationBlockers, "value_hash_mismatch:"+row.StateID)
			}
		}
		switch body.TriggerType {
		case "admission":
			if body.TriggerID != a.AdmissionID || prior.StateID != "" {
				scan.MismatchCount++
			}
		case "original":
			o, ok := originals[body.TriggerID]
			if !ok || o.AdmissionID != body.AdmissionID || body.MutationSequence != strconv.FormatInt(o.MutationSequence, 10) || body.ControlGeneration != strconv.FormatInt(o.Generation, 10) {
				scan.MismatchCount++
			}
		case "cutoff":
			if body.BatchSequence == nil || *body.BatchSequence != body.TriggerID || !cnIntegrityJSONEqual(body.Before, body.After) {
				scan.MismatchCount++
			}
		case "control":
			var count int64
			if err := tx.Model(&CNIntegrityControlReceipt{}).Where("epoch_id = ? AND generation = ?", epoch.EpochID, body.TriggerID).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 || body.ControlGeneration != body.TriggerID {
				scan.MismatchCount++
			}
		default:
			scan.MismatchCount++
		}
		heads[key] = body
	}
	seen := map[string]bool{}
	for _, a := range admissions {
		entities, err := cnIntegrityNativeValueStates(tx, a.UserID, false)
		if err != nil {
			return err
		}
		var account cnIntegrityAccountState
		lots := map[string]cnIntegrityLotState{}
		holds := []cnIntegrityHoldState{}
		allocations := map[string][2]decimal.Decimal{}
		for _, entity := range entities {
			key := entity.Type + ":" + entity.ID
			seen[key] = true
			head, ok := heads[key]
			if !ok {
				scan.MissingOriginalCount++
			} else if !cnIntegrityJSONEqual(head.After, entity.Body) || head.AdmissionID != a.AdmissionID {
				scan.MismatchCount++
				scan.ActivationBlockers = append(scan.ActivationBlockers, "value_state_head:"+key)
			}
			switch entity.Type {
			case "account":
				if err := common.Unmarshal(entity.Body, &account); err != nil {
					return err
				}
			case "lot":
				var lot cnIntegrityLotState
				if err := common.Unmarshal(entity.Body, &lot); err != nil {
					return err
				}
				lots[entity.ID] = lot
				var pool CNFundingValueLot
				if err := tx.Where("lot_id = ?", entity.ID).First(&pool).Error; err != nil {
					return err
				}
				if strconv.FormatInt(pool.InitialQuota, 10) != lot.PurchasedQuota || pool.PaidValue != lot.PurchasedValue {
					scan.MismatchCount++
				}
				var native []CNFundingValueAllocation
				if err := tx.Where("lot_id = ?", entity.ID).Find(&native).Error; err != nil {
					return err
				}
				sum := [2]decimal.Decimal{decimal.Zero, decimal.Zero}
				for _, n := range native {
					v, e := decimal.NewFromString(n.Value)
					if e != nil {
						scan.MismatchCount++
						continue
					}
					sum[0] = sum[0].Add(decimal.NewFromInt(n.Quota))
					sum[1] = sum[1].Add(v)
				}
				allocations[entity.ID] = sum
			case "refund_hold":
				var hold cnIntegrityHoldState
				if err := common.Unmarshal(entity.Body, &hold); err != nil {
					return err
				}
				holds = append(holds, hold)
				scan.MismatchCount++ // Native refund API owns AgencyFundingLot, excluded from this cohort.
			}
		}
		if !cnIntegrityValueEquations(account, lots, allocations, holds) {
			scan.MismatchCount++
			scan.ActivationBlockers = append(scan.ActivationBlockers, "value_state_equation:"+strconv.Itoa(a.UserID))
		}
	}
	for key := range heads {
		if !seen[key] {
			scan.OrphanCount++
		}
	}
	return nil
}
