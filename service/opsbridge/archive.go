package opsbridge

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// ArchiveDelivered moves evidence out of the active queue only after its private
// file is durable. Archived bodies still enforce immutable delivery identities.
func (b *Bridge) ArchiveDelivered() (count int, archiveErr error) {
	b.deliveryMu.Lock()
	defer b.deliveryMu.Unlock()
	b.importMu.Lock()
	defer b.importMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fault != "" {
		return 0, errors.New(b.fault)
	}
	// A partial archive retains durable evidence, but active state must stop
	// changing until restart reconciles it with the last committed state file.
	defer func() {
		if archiveErr != nil {
			b.fault = "Ops archive failed; restart after repairing storage"
		}
	}()
	dir := filepath.Join(b.cfg.StateDir, "archive")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, errors.New("cannot create Ops archive")
	}
	changed := false
	for id, value := range b.state.Notifications {
		item, ok := value.(map[string]any)
		if !ok {
			return count, errors.New("invalid notification cache")
		}
		ackID := hash([]byte("notifications/" + url.PathEscape(id) + "/ack\nops-ack-" + id))
		job := b.state.Deliveries[ackID]
		if job == nil || job.Status != "delivered" {
			if _, err := os.Stat(filepath.Join(dir, "delivery-"+ackID+".json")); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return count, errors.New("cannot read notification ACK evidence")
			}
		}
		receipt := map[string]any{"id": id, "type": item["type"], "snapshot": item["snapshot"], "received_at": item["received_at"]}
		data, err := common.Marshal(receipt)
		if err != nil {
			return count, err
		}
		if err := writeEvidence(filepath.Join(dir, "notification-"+hash([]byte(id))+".json"), data); err != nil {
			return count, err
		}
		kind, _ := item["type"].(string)
		previous, _ := b.state.LatestNotifications[kind].(map[string]any)
		if previous == nil || fmt.Sprint(previous["received_at"]) <= fmt.Sprint(item["received_at"]) {
			b.state.LatestNotifications[kind] = receipt
		}
		delete(b.state.Notifications, id)
		changed = true
	}
	for id, job := range b.state.Deliveries {
		if job.Status != "delivered" {
			continue
		}
		data, err := common.Marshal(job)
		if err != nil {
			return count, err
		}
		if err := writeEvidence(filepath.Join(dir, "delivery-"+id+".json"), data); err != nil {
			return count, err
		}
		requestID := job.RequestID
		if requestID == "" && job.Path == "native-usage/bulk" {
			requestID = strings.TrimPrefix(job.Key, "usage-")
		}
		if requestID == "" && job.Path == "call-reservations/cancel" {
			requestID = job.Key
		}
		if requestID != "" {
			status := b.state.Requests[requestID]
			if status == "usage_queued" || status == "cancel_queued" || status == "blocked" {
				data, _ := common.Marshal(map[string]string{"id": requestID, "status": status})
				if err := writeEvidence(filepath.Join(dir, "request-"+hash([]byte(requestID))+".json"), data); err != nil {
					return count, err
				}
				delete(b.state.Requests, requestID)
				for taskID, task := range b.state.Tasks {
					if task.RequestID == requestID {
						delete(b.state.Tasks, taskID)
					}
				}
			}
		}
		delete(b.state.Deliveries, id)
		count++
		changed = true
	}
	for id, status := range b.state.Requests {
		if status != "blocked" {
			continue
		}
		pending := false
		for _, job := range b.state.Deliveries {
			if job.Key == id || job.RequestID == id {
				pending = true
				break
			}
		}
		if pending {
			continue
		}
		data, err := common.Marshal(map[string]string{"id": id, "status": status})
		if err != nil {
			return count, err
		}
		if err := writeEvidence(filepath.Join(dir, "request-"+hash([]byte(id))+".json"), data); err != nil {
			return count, err
		}
		delete(b.state.Requests, id)
		changed = true
	}
	// An expired unused preview has no external side effect. Prepared imports
	// remain active until their local commit and remote confirmation are resolved.
	for id, plan := range b.state.Imports {
		if plan.Status != "applied" && !(plan.Status == "preview" && time.Now().Unix()-plan.CreatedAt > 3600) {
			continue
		}
		data, _ := common.Marshal(plan)
		if err := writeEvidence(filepath.Join(dir, "import-"+hash([]byte(id))+".json"), data); err != nil {
			return count, err
		}
		delete(b.state.Imports, id)
		changed = true
	}
	if !changed {
		return count, nil
	}
	return count, b.saveLocked()
}
