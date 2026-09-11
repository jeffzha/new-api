// Package agencypricing resolves immutable agency policy snapshots. It never
// reads channels or reproduces provider pricing formulas.
package agencypricing

import (
	"sync"
	"time"

	"github.com/QuantumNous/new-api/pkg/agencycontract"
)

type Resolver struct {
	mu       sync.RWMutex
	policies map[int64]agencycontract.Policy
	updated  map[int64]time.Time
}

func NewResolver() *Resolver {
	return &Resolver{policies: make(map[int64]agencycontract.Policy), updated: make(map[int64]time.Time)}
}

func (r *Resolver) Put(agencyID int64, policy agencycontract.Policy) error {
	if err := agencycontract.ValidatePolicy(policy); err != nil {
		return err
	}
	r.mu.Lock()
	r.policies[agencyID] = policy
	r.updated[agencyID] = time.Now()
	r.mu.Unlock()
	return nil
}

func (r *Resolver) Delete(agencyID int64) {
	r.mu.Lock()
	delete(r.policies, agencyID)
	delete(r.updated, agencyID)
	r.mu.Unlock()
}

func (r *Resolver) Resolve(agencyID int64, originModelName string) (agencycontract.Policy, agencycontract.ResolvedPolicy, bool, error) {
	r.mu.RLock()
	policy, ok := r.policies[agencyID]
	r.mu.RUnlock()
	if !ok {
		return agencycontract.Policy{}, agencycontract.ResolvedPolicy{}, false, nil
	}
	resolved, err := agencycontract.Resolve(policy, originModelName)
	return policy, resolved, true, err
}

func (r *Resolver) UpdatedAt(agencyID int64) (time.Time, bool) {
	r.mu.RLock()
	value, ok := r.updated[agencyID]
	r.mu.RUnlock()
	return value, ok
}

func Calculate(standardQuota, paidAllocatedQuota int64, policy agencycontract.Policy, originModelName string, round bool) (agencycontract.ChargeResult, agencycontract.ResolvedPolicy, error) {
	resolved, err := agencycontract.Resolve(policy, originModelName)
	if err != nil {
		return agencycontract.ChargeResult{}, agencycontract.ResolvedPolicy{}, err
	}
	result, err := agencycontract.Calculate(standardQuota, resolved, paidAllocatedQuota, round)
	return result, resolved, err
}
