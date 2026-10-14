package opsbridge

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
)

func (b *Bridge) QueueInventory(id string, objects []map[string]any) error {
	if id == "" || len(objects) > 10000 {
		return errors.New("invalid inventory snapshot")
	}
	seen := map[string]bool{}
	for _, row := range objects {
		kind, _ := row["object_type"].(string)
		ident, _ := row["external_id"].(string)
		if (kind != "user" && kind != "api_key" && kind != "channel") || ident == "" || seen[kind+"\n"+ident] {
			return errors.New("invalid inventory identity")
		}
		seen[kind+"\n"+ident] = true
	}
	body := map[string]any{"objects": objects, "snapshot_types": []string{"user", "api_key", "channel"}}
	encoded, err := common.Marshal(body)
	if err != nil || len(encoded) > 8<<20 {
		return errors.New("inventory snapshot is too large")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, job := range b.state.Deliveries {
		if job.Path == "objects/sync" && job.Status != "delivered" {
			return errors.New("previous inventory must be delivered or repaired first")
		}
	}
	return b.enqueueLocked("objects/sync", id, body, "", "")
}

func (b *Bridge) QueuePricingSnapshot(id string, payload map[string]any) error {
	if id == "" {
		return errors.New("invalid pricing snapshot identity")
	}
	encoded, err := common.Marshal(payload)
	if err != nil || len(encoded) > 8<<20 {
		return errors.New("pricing snapshot is too large")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, job := range b.state.Deliveries {
		if job.Path == "pricing-snapshots" && job.Status != "delivered" {
			return errors.New("previous pricing snapshot must be delivered or repaired first")
		}
	}
	return b.enqueueLocked("pricing-snapshots", id, payload, "", "")
}
