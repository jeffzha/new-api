package service

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// SyncOpsObjects exports only identity fields, never entire database models.
// Organization/project inventories remain owned by Enterprise Policy Hub.
func SyncOpsObjects(ctx context.Context, bridge *opsbridge.Bridge) (map[string]int, error) {
	encoded, err := model.ReadOpsInventory(ctx, model.DB, "")
	if err != nil {
		return nil, err
	}
	var snapshot struct {
		Objects []map[string]any `json:"objects"`
	}
	if err = common.Unmarshal(encoded, &snapshot); err != nil {
		return nil, err
	}
	objects := snapshot.Objects
	counts := map[string]int{"user": 0, "api_key": 0, "channel": 0}
	for _, object := range objects {
		counts[object["object_type"].(string)]++
	}
	if err := bridge.QueueInventory(rand.Text(), objects); err != nil {
		return nil, err
	}
	return counts, nil
}

// SyncOpsPricing submits local configuration evidence. Ops administrators still
// control official prices and any subsequent catalog publication.
func SyncOpsPricing(ctx context.Context, bridge *opsbridge.Bridge) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	snapshot, err := model.GetModelPricingSnapshot(nil)
	if err != nil {
		return 0, errors.New("cannot read local pricing snapshot")
	}
	entries := make([]map[string]any, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries = append(entries, map[string]any{"model_name": entry.ModelName, "version": entry.Version,
			"configured": entry.Configured, "effective": entry.Effective})
	}
	payload := map[string]any{"entries": entries, "complete": true, "captured_at": time.Now().UTC().Format(time.RFC3339Nano),
		"quota_per_unit": common.QuotaPerUnit, "group_ratio": ratio_setting.GetGroupRatioCopy(),
		"group_group_ratio": ratio_setting.GetGroupRatioSetting().GroupGroupRatio.ReadAll(),
		"usd_exchange_rate": operation_setting.USDExchangeRate}
	if err := bridge.QueuePricingSnapshot(rand.Text(), payload); err != nil {
		return 0, err
	}
	return len(entries), nil
}
