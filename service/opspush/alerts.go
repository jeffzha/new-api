package opspush

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/upstreamalerts"
)

// Keep the bounded alert when large diagnostics are trimmed from extra. The
// stored admin copy also lets OPS's log collector recover missed pushes.
func attachUpstreamAlert(other map[string]json.RawMessage, event map[string]any) {
	if event["status"] != "error" {
		return
	}
	var alert upstreamalerts.Alert
	if common.Unmarshal(object(other["admin_info"])["upstream_alert"], &alert) != nil ||
		alert.Type != upstreamalerts.BalanceInsufficient || alert.ChannelID <= 0 {
		return
	}
	raw, err := common.Marshal(event["extra"])
	if err != nil {
		return
	}
	extra := object(raw)
	if extra == nil {
		extra = map[string]json.RawMessage{}
	}
	encoded, err := common.Marshal(alert)
	if err != nil {
		return
	}
	if raw, err := common.Marshal(extra); err == nil && len(raw)+len(encoded)+32 > maxExtra-256 {
		extra = extraWithin(extra, v11BillingKeys, maxExtra-256-len(encoded)-32)
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
	}
	extra["upstream_alert"] = encoded
	event["extra"] = extra
}
