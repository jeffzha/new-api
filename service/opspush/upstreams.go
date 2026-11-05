package opspush

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/upstreammap"
	"gorm.io/gorm"
)

var ErrUpstreamPending = errors.New("upstream registration pending")

// Only captured execution addresses are used. Looking up today's channel would
// incorrectly reattribute a completed request after its URL was edited.
func (p *Pusher) attachUpstream(log *model.Log, event map[string]any) error {
	if log.ChannelId <= 0 || log.Type != model.LogTypeConsume && log.Type != model.LogTypeError && log.Type != model.LogTypeRefund {
		return nil
	}
	other := object(json.RawMessage(log.Other))
	captured := object(object(other["admin_info"])["upstream"])
	base, ok := textValue(captured["base_url"])
	if !ok || base == "" {
		return nil
	}
	// Databases upgraded independently can continue the pre-existing push until
	// this optional integration is enabled.
	if os.Getenv("OPS_UPSTREAM_SYNC_ENABLED") != "true" {
		return nil
	}
	targetURL := os.Getenv("OPS_UPSTREAM_URL")
	if targetURL == "" {
		targetURL = p.Config.URL
	}
	row, opsID, err := upstreammap.Resolve(p.DB, base, upstreammap.Target(targetURL, p.Config.KeyID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row, err = upstreammap.Ensure(p.DB, base)
		if err == nil {
			err = upstreammap.EnsureBinding(p.DB, row.ID, upstreammap.Target(targetURL, p.Config.KeyID))
		}
		model.NotifyUpstreamMappingChange()
	}
	if err != nil {
		return err
	}
	if opsID == nil {
		if err := upstreammap.EnsureBinding(p.DB, row.ID, upstreammap.Target(targetURL, p.Config.KeyID)); err != nil {
			return err
		}
		model.NotifyUpstreamMappingChange()
		return ErrUpstreamPending
	}
	encodedExtra, err := common.Marshal(event["extra"])
	if err != nil {
		return err
	}
	extra := object(encodedExtra)
	if extra == nil {
		extra = map[string]json.RawMessage{}
	}
	// Rebuild bounded extras when diagnostics compete with the address. Never
	// silently remove image/video quantities to make room for upstream identity.
	required, err := common.Marshal(map[string]any{"base_url": base, "gateway_upstream_id": row.ID, "ops_upstream_id": opsID})
	if err != nil {
		return err
	}
	if raw, err := common.Marshal(extra); err == nil && len(raw)+len(required) > maxExtra {
		keys := billingKeys
		if p.Config.Spec == SpecV11 || extra["recordKind"] != nil || extra["project_task_terminal"] != nil {
			keys = append(append([]string{}, v11BillingKeys...), "recordKind", "direction", "quotaPerUnit", "usdExchangeRate", "topupPrice", "project_task_terminal", "project_original_request", "project_task_row", "billing_scope")
		}
		extra = extraWithin(extra, keys, maxExtra-len(required)-64)
		if extra == nil {
			return errors.New("upstream metadata exceeds billing fact budget")
		}
	}
	for key, value := range map[string]any{"base_url": base, "gateway_upstream_id": row.ID, "ops_upstream_id": opsID} {
		encoded, err := common.Marshal(value)
		if err != nil {
			return err
		}
		extra[key] = encoded
	}
	// Reserve the extra metadata budget without dropping billing facts.
	if raw, err := common.Marshal(extra); err != nil || len(raw) > maxExtra {
		return errors.New("upstream metadata exceeds usage extra limit")
	}
	event["extra"] = extra
	return nil
}
